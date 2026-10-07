// Trusted probe embedded in ClearDev, not copied from the candidate. It runs
// inside the existing non-networked, non-root, resource-bounded check container.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import fs from "node:fs";

const listener = net.createServer();
listener.listen(0, "127.0.0.1");
await once(listener, "listening");
const port = listener.address().port;
await new Promise((resolve, reject) => listener.close(error => error ? reject(error) : resolve()));
const dir = fs.mkdtempSync(path.join(os.tmpdir(), "cleardev-health-"));
const child = spawn("npm", ["run", "start", "--silent"], {
  cwd: process.cwd(), detached: true, stdio: ["ignore", "ignore", "inherit"],
  env: { ...process.env, PORT: String(port), MAIL_APP_DB: path.join(dir, "app.sqlite"), npm_config_offline: "true" },
});
let startError;
child.on("error", error => { startError = error; });
const base = `http://127.0.0.1:${port}`;
try {
  const deadline = Date.now() + 10_000;
  let response;
  while (Date.now() < deadline) {
    if (startError || child.exitCode !== null || child.signalCode !== null) throw startError || new Error("application exited before health check");
    try {
      response = await fetch(`${base}/api/health`, { redirect: "error", signal: AbortSignal.timeout(1500) });
      break;
    } catch {
      await new Promise(resolve => setTimeout(resolve, 100));
    }
  }
  assert.ok(response, "application did not respond before the startup deadline");
  assert.equal(response.status, 200, "health response must be 200, not merely reachable");
  assert.match(response.headers.get("content-type") || "", /^application\/json\b/);
  assert.deepEqual(await response.json(), { status: "ok", application: "complex-mail-app" });
  const page = await fetch(`${base}/`, { redirect: "error", signal: AbortSignal.timeout(1500) });
  assert.equal(page.status, 200);
  assert.match(page.headers.get("content-type") || "", /^text\/html\b/);
  assert.match(await page.text(), /id=["']import-form["']/);
  const missing = await fetch(`${base}/__cleardev_missing_health_probe__`, { redirect: "error", signal: AbortSignal.timeout(1500) });
  assert.equal(missing.status, 404, "a catch-all success response is not the application contract");
  if (startError || child.exitCode !== null || child.signalCode !== null) throw new Error("application exited during health check");
  console.log(JSON.stringify({ health: "PASS", status: 200, application: "complex-mail-app" }));
} finally {
  if (child.pid) {
    try { process.kill(-child.pid, "SIGKILL"); } catch (error) { if (error.code !== "ESRCH") throw error; }
    if (child.exitCode === null && child.signalCode === null) await once(child, "exit");
  }
  fs.rmSync(dir, { recursive: true, force: true });
}
