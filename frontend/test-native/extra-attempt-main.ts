// Explicit isolated test. No command can select Approve in the native dialog.
import { app, BrowserWindow, desktopCapturer } from "electron";
import { randomBytes } from "node:crypto";
import { readFileSync, writeFileSync, writeSync } from "node:fs";
import path from "node:path";
import readline from "node:readline";
import { startHumanAuthorityHost } from "../src/main/human-authority-host";
import { createHumanDecisionPresentation } from "../src/main/human-decision-presentation";
import type { AppLocale } from "../src/shared/ui-locale";
const root = process.env.AO_EXTRA_ATTEMPT_NATIVE_ROOT;
if (process.env.AO_EXTRA_ATTEMPT_NATIVE !== "1" || !root || !path.isAbsolute(root)) throw new Error("Explicit isolated extra-attempt check required");
app.setPath("userData", path.join(root, "electron"));
app.disableHardwareAcceleration();
const record = (event: string, extra: object = {}) => console.log(JSON.stringify({ event, ...extra }));
void app.whenReady().then(async () => {
  const window = new BrowserWindow({ width: 1100, height: 900, show: true, title: "ClearDev isolated extra-attempt check", webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false } });
  await window.loadURL("data:text/html,<h1>Isolated extra-attempt check</h1>");
  const status = readFileSync(`/proc/${window.webContents.getOSProcessId()}/status`, "utf8");
  if (!/^Seccomp:\s+2$/m.test(status) || !/^NoNewPrivs:\s+1$/m.test(status)) throw new Error("Native renderer sandbox not observed");
  let locale: AppLocale = "en", readers = 0, nativeDialogs = 0, realApprovals = 0;
  const presentation = createHumanDecisionPresentation();
  const waitForReader = async (reader: BrowserWindow, signal?: AbortSignal): Promise<void> => new Promise((resolve, reject) => {
    const contents = reader.webContents;
    const cleanup = (): void => {
      clearTimeout(timer);
      clearInterval(observe);
      reader.removeListener("show", check);
      reader.removeListener("closed", unavailable);
      contents.removeListener("did-finish-load", check);
      contents.removeListener("did-stop-loading", check);
      signal?.removeEventListener("abort", unavailable);
    };
    const unavailable = (): void => { cleanup(); record("reader-unavailable", { destroyed: reader.isDestroyed(), visible: !reader.isDestroyed() && reader.isVisible(), loading: !contents.isDestroyed() && contents.isLoadingMainFrame() }); reject(new Error("Actual trusted reader did not become visible")); };
    const check = (): void => {
      if (!reader.isDestroyed() && reader.isVisible() && !contents.isLoadingMainFrame()) { cleanup(); resolve(); }
    };
    const timer = setTimeout(unavailable, 15_000);
    const observe = setInterval(check, 25);
    reader.on("show", check);
    reader.once("closed", unavailable);
    contents.on("did-finish-load", check);
    contents.on("did-stop-loading", check);
    signal?.addEventListener("abort", unavailable, { once: true });
    if (signal?.aborted) unavailable(); else check();
  });
  const host = await startHumanAuthorityHost({
    browserRuntimeToken: randomBytes(32).toString("base64url"),
    presentation: { getLocale: async () => locale, reviewOffer: async (offer, language, signal) => {
      const existing = new Set(BrowserWindow.getAllWindows());
      const decision = presentation.reviewOffer(offer, language, signal);
      const reader = BrowserWindow.getAllWindows().find((item) => !existing.has(item) && !item.isDestroyed());
      if (!reader) throw new Error("Actual trusted reader unavailable");
      record("reader-created", { visible: reader.isVisible(), loading: reader.webContents.isLoadingMainFrame() });
      await waitForReader(reader, signal);
      readers++; record("reader", { readers, locale: language, decisionKind: offer.decisionKind, requestId: offer.requestId, title: offer.display.title, visible: reader.isVisible(), loading: reader.webContents.isLoadingMainFrame() });
      return decision;
    } },
    showDialog: async (options, signal) => {
      nativeDialogs++; record("native", { nativeDialogs, title: options.title });
      const result = await presentation.showDialog(options, signal);
      if (result.response === 0 && !signal?.aborted) realApprovals++;
      record("native-closed", { cancelled: signal?.aborted, realApprovals });
      return result;
    },
    bootstrapLine: (secrets) => JSON.stringify({ schemaVersion: 1, ...secrets }),
  });
  writeSync(3, host.bootstrapLine + "\n");
  record("ready", { sandbox: true });
  const commands = readline.createInterface({ input: process.stdin });
  let pending = Promise.resolve();
  commands.on("line", (line) => {
    pending = pending.then(async () => {
      const command = JSON.parse(line) as { action: string; url?: string; locale?: AppLocale; name?: string; label?: string };
      if (command.action === "open") { locale = command.locale ?? "en"; await window.loadURL(command.url!); record("opened"); }
      if (command.action === "click") {
        const deadline = Date.now() + 15_000;
        let clicked = false;
        while (!clicked && Date.now() < deadline) {
          clicked = await window.webContents.executeJavaScript(`(()=>{const b=[...document.querySelectorAll('button')].find(b=>b.textContent.trim()===${JSON.stringify(command.label)});if(!b||b.disabled)return false;b.click();b.click();return true})()`);
          if (!clicked) await new Promise((resolve) => setTimeout(resolve, 100));
        }
        record("clicked", { clicked, label: command.label });
      }
      if (command.action === "reader-later" || command.action === "reader-continue") {
        const reader = BrowserWindow.getAllWindows().find((item) => item !== window && !item.isDestroyed());
        if (!reader) throw new Error("Actual trusted reader unavailable");
        await reader.webContents.executeJavaScript(command.action === "reader-later" ? "document.querySelector('footer a.secondary').click()" : "document.querySelector('footer a:not(.secondary)').click()");
      }
      if (command.action === "snapshot") {
        if (!command.name || !/^[a-z0-9-]+$/.test(command.name)) throw new Error("Bounded snapshot name required");
        const windows = BrowserWindow.getAllWindows().filter((item) => !item.isDestroyed() && item.isVisible());
        for (let i = 0; i < windows.length; i++) writeFileSync(path.join(root, `${command.name}-${i}.png`), (await windows[i].webContents.capturePage()).toPNG());
        const sources = await desktopCapturer.getSources({ types: ["screen"], thumbnailSize: { width: 1280, height: 960 } });
        if (sources[0]) writeFileSync(path.join(root, `${command.name}-screen.png`), sources[0].thumbnail.toPNG());
        record("snapshot", { name: command.name, readers, nativeDialogs, realApprovals, visibleWindows: windows.length, text: await window.webContents.executeJavaScript("document.body.innerText") });
      }
      if (command.action === "quit") { host.dispose(); record("finished", { readers, nativeDialogs, realApprovals }); app.quit(); }
    }).catch(() => { record("failure", { message: "Isolated native command failed; private data withheld" }); app.exit(1); });
  });
  app.once("before-quit", () => host.dispose());
}).catch(() => { console.error("Native extra-attempt startup failed; private bootstrap withheld"); app.exit(1); });
