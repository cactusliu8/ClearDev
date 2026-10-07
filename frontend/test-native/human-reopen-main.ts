// Explicit isolated test: real production Host, reader, native dialog and renderer component.
import { app, BrowserWindow, desktopCapturer } from "electron";
import { randomBytes } from "node:crypto";
import { writeSync, writeFileSync, readFileSync } from "node:fs";
import path from "node:path";
import readline from "node:readline";
import { startHumanAuthorityHost } from "../src/main/human-authority-host";
import { createHumanDecisionPresentation } from "../src/main/human-decision-presentation";
import type { AppLocale } from "../src/shared/ui-locale";
const root = process.env.CLEARDEV_NATIVE_REOPEN_ROOT;
if (
  process.env.CLEARDEV_NATIVE_REOPEN_TEST !== "1" ||
  !root ||
  !path.isAbsolute(root)
)
  throw new Error("Explicit isolated reopen test required");
app.setPath("userData", path.join(root, "electron"));
app.disableHardwareAcceleration();
void app
  .whenReady()
  .then(async () => {
    const window = new BrowserWindow({
      width: 1100,
      height: 820,
      show: true,
      title: "ClearDev isolated reopen test",
      webPreferences: {
        sandbox: true,
        contextIsolation: true,
        nodeIntegration: false,
      },
    });
    await window.loadURL(
      "data:text/html,<h1>Isolated ClearDev confirmation test</h1>",
    );
    const status = readFileSync(
      `/proc/${window.webContents.getOSProcessId()}/status`,
      "utf8",
    );
    if (!/^Seccomp:\s+2$/m.test(status) || !/^NoNewPrivs:\s+1$/m.test(status))
      throw new Error("Native renderer sandbox not observed");
    const presentation = createHumanDecisionPresentation();
    let locale: AppLocale = "zh-CN";
    let readers = 0,
      nativeDialogs = 0,
      approvals = 0;
    const record = (event: string, extra: object = {}) =>
      console.log(JSON.stringify({ event, ...extra }));
    window.webContents.on("console-message", (_event, level, message) => {
      if (level >= 2) record("renderer-message", { message });
    });
    const host = await startHumanAuthorityHost({
      browserRuntimeToken: randomBytes(32).toString("base64url"),
      presentation: {
        getLocale: async () => locale,
        reviewOffer: async (offer, language, signal) => {
          readers++;
          record("reader", {
            number: readers,
            locale: language,
            requestId: offer.requestId,
            contentSha256: offer.contentSha256,
          });
          return presentation.reviewOffer(offer, language, signal);
        },
      },
      showDialog: async (options, signal) => {
        nativeDialogs++;
        record("native", { number: nativeDialogs, title: options.title });
        const result = await presentation.showDialog(options, signal);
        if (result.response === 0 && !signal?.aborted) approvals++;
        record("native-closed", { cancelled: signal?.aborted, approvals });
        return result;
      },
      bootstrapLine: (secrets) =>
        JSON.stringify({ schemaVersion: 1, ...secrets }),
    });
    writeSync(3, host.bootstrapLine + "\n");
    record("ready", { sandbox: true });
    const commands = readline.createInterface({ input: process.stdin });
    commands.on("line", (line) => {
      void (async () => {
        const command = JSON.parse(line) as {
          action: string;
          url?: string;
          locale?: AppLocale;
          name?: string;
        };
        if (command.action === "open") {
          locale = command.locale ?? "en";
          await window.loadURL(command.url!);
        }
        if (command.action === "snapshot") {
          const windows = BrowserWindow.getAllWindows().filter(
            (w) => !w.isDestroyed() && w.isVisible(),
          );
          for (let i = 0; i < windows.length; i++)
            writeFileSync(
              path.join(root, `${command.name}-${i}.png`),
              (await windows[i].webContents.capturePage()).toPNG(),
            );
          const sources = await desktopCapturer.getSources({
            types: ["screen"],
            thumbnailSize: { width: 1280, height: 960 },
          });
          if (sources[0])
            writeFileSync(
              path.join(root, `${command.name}-screen.png`),
              sources[0].thumbnail.toPNG(),
            );
          record("snapshot", {
            name: command.name,
            readers: readers,
            nativeDialogs,
            approvals,
            visibleWindows: windows.length,
            text: await window.webContents.executeJavaScript(
              "document.body.innerText",
            ),
          });
        }
        if (command.action === "reopen") {
          const clicked = await window.webContents.executeJavaScript(
            `(()=>{const b=[...document.querySelectorAll('button')].find(b=>b.textContent.includes('Reopen pending confirmation')||b.textContent.includes('重新打开原待确认事项'));if(!b||b.disabled)return false;b.click();b.click();return true})()`,
          );
          record("clicked", { clicked });
        }
        if (command.action === "continue") {
          const reader = BrowserWindow.getAllWindows().find(
            (w) => w !== window && !w.isDestroyed(),
          );
          if (!reader) throw new Error("No actual trusted reader");
          await reader.webContents.executeJavaScript(
            "document.querySelector('footer a:not(.secondary)').click()",
          );
        }
        if (command.action === "quit") {
          host.dispose();
          record("finished", { readers, nativeDialogs, approvals });
          app.quit();
        }
      })().catch((error: unknown) => {
        record("failure", {
          message: error instanceof Error ? error.message : "unknown",
        });
        app.exit(1);
      });
    });
    app.once("before-quit", () => host.dispose());
  })
  .catch(() => {
    console.error("native reopen startup failed; no private bootstrap logged");
    app.exit(1);
  });
