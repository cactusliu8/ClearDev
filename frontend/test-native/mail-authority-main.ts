// Test-only native shell. It imports the production host and opens the real
// Electron dialog; no decide callback, mock dialog, HTTP grant or database write.
import { app, BrowserWindow, dialog, desktopCapturer } from "electron";
import { randomBytes } from "node:crypto";
import { writeSync, writeFileSync, readFileSync } from "node:fs";
import path from "node:path";
import { startHumanAuthorityHost } from "../src/main/human-authority-host";

const root = process.env.CLEARDEV_NATIVE_AUTHORITY_ROOT;
if (process.env.CLEARDEV_NATIVE_AUTHORITY_TEST !== "1" || !root || !path.isAbsolute(root)) {
  throw new Error("Native authority test requires an explicit isolated data root");
}
app.setPath("userData", path.join(root, "electron"));
app.disableHardwareAcceleration();
void app.whenReady().then(async () => {
  const window = new BrowserWindow({ width: 1000, height: 720, show: true, webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false } });
  await window.loadURL("data:text/html;charset=utf-8," + encodeURIComponent("<h1>ClearDev native authority test</h1><p>Explicit test model and synthetic button input. Not a human approval or a live demonstration.</p>"));
  const rendererStatus = readFileSync(`/proc/${window.webContents.getOSProcessId()}/status`, "utf8");
  const seccomp = /^Seccomp:\s+2$/m.test(rendererStatus);
  const noNewPrivileges = /^NoNewPrivs:\s+1$/m.test(rendererStatus);
  const nestedPID = (rendererStatus.match(/^NSpid:\s+(.+)$/m)?.[1]?.trim().split(/\s+/).length ?? 0) > 1;
  if (!seccomp || !noNewPrivileges || !nestedPID) throw new Error("Renderer kernel sandbox was not independently observed");
  console.log(JSON.stringify({ event: "sandbox-verified", seccomp, noNewPrivileges, nestedPID }));
  let count = 0;
  const host = await startHumanAuthorityHost({
    browserRuntimeToken: randomBytes(32).toString("base64url"),
    showDialog: async (options) => {
      count++;
      const number = count;
      console.log(JSON.stringify({ event: "dialog", number, title: options.title }));
      const capture = setTimeout(() => {
        void desktopCapturer.getSources({ types: ["screen"], thumbnailSize: { width: 1000, height: 750 } }).then((sources) => {
          if (sources[0]) writeFileSync(path.join(root, `dialog-${number}.png`), sources[0].thumbnail.toPNG(), { mode: 0o600 });
        }).catch(() => console.log("native capture unavailable"));
      }, 250);
      const result = await dialog.showMessageBox(window, options);
      clearTimeout(capture);
      console.log(JSON.stringify({ event: "selected", response: result.response }));
      return result;
    },
    bootstrapLine: (secrets) => JSON.stringify({ schemaVersion: 1, ...secrets }),
  });
  // Private inherited pipe. Never put credentials on stdout, argv or disk.
  writeSync(3, host.bootstrapLine + "\n");
  console.log(JSON.stringify({ event: "ready", pid: process.pid }));
  process.stdin.once("data", () => { host.dispose(); app.quit(); });
  process.stdin.resume();
  app.once("before-quit", () => host.dispose());
}).catch((error: unknown) => { console.error("native authority test startup failed:", error instanceof Error ? error.message : "unknown startup failure"); app.exit(1); });
