// Opt-in native PRODUCT bootstrap test, not a Demo orchestrator.
// Launches unmodified Forge/main/preload/daemon, uses public project endpoints,
// and checks fresh baselines. It never approves a requirement, advances an
// execution, sends an Agent message, or writes SQLite/state to fake progress.
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readdirSync, linkSync, symlinkSync, statSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import net from "node:net";
import { once } from "node:events";
import { createHash } from "node:crypto";
import { chromium } from "playwright";

assert.equal(process.env.CLEARDEV_REQUIRE_PRODUCT_SMOKE, "1", "explicit opt-in required");
assert.ok(process.env.DISPLAY, "run under an isolated xvfb-run display");
const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const root = path.dirname(frontend);
const sandbox = process.env.CLEARDEV_NATIVE_SYSTEM_SANDBOX;
const daemon = process.env.CLEARDEV_NATIVE_BACKEND_BINARY;
assert.ok(sandbox && path.isAbsolute(sandbox));
assert.ok(daemon && path.isAbsolute(daemon));
const helper = statSync(sandbox);
assert.equal(helper.uid, 0); assert.equal(helper.mode & 0o4000, 0o4000); assert.equal(helper.mode & 0o022, 0);
const base = path.join(os.homedir(), ".ao", "demo-sprint-native");
mkdirSync(base, { recursive: true, mode: 0o700 });
const evidence = mkdtempSync(path.join(base, "product-"));
const runtime = path.join(evidence, "runtime");
mkdirSync(runtime, { mode: 0o700 });
const dist = path.join(frontend, "node_modules/electron/dist");
for (const entry of readdirSync(dist)) {
  if (entry === "chrome-sandbox") continue;
  if (entry === "electron") linkSync(path.join(dist, entry), path.join(runtime, entry));
  else symlinkSync(path.join(dist, entry), path.join(runtime, entry));
}
symlinkSync(sandbox, path.join(runtime, "chrome-sandbox"));
const home = path.join(evidence, "home"); mkdirSync(home, { mode: 0o700 });
const projects = mkdtempSync(path.join(os.tmpdir(), "cleardev-native-projects-"));
const sha = spawnSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).stdout.trim();
const daemonSHA256 = createHash("sha256").update(readFileSync(daemon)).digest("hex");
const freePort = async () => {
  const server = net.createServer(); server.listen(0, "127.0.0.1"); await once(server, "listening");
  const port = server.address().port; await new Promise((resolve, reject) => server.close(e => e ? reject(e) : resolve())); return port;
};
const port = await freePort(), cdpPort = await freePort();
const runFile = path.join(evidence, "running.json");
const env = { ...process.env, HOME: home, AO_DATA_DIR: path.join(evidence, "data"), AO_RUN_FILE: runFile, AO_PORT: String(port), AO_DAEMON_COMMAND: `'${daemon.replaceAll("'", "'\\''")}' daemon`, AO_DEV_API_TARGET: `http://127.0.0.1:${port}`, ELECTRON_OVERRIDE_DIST_PATH: runtime, AO_DISABLE_GPU: "1", AO_TELEMETRY_REMOTE: "off", AO_TELEMETRY_EVENTS: "off", npm_config_cache: path.join(os.homedir(), ".npm"), npm_config_offline: "true" };
for (const key of ["ELECTRON_RUN_AS_NODE", "NODE_TEST_CONTEXT", "NODE_CHANNEL_FD", "AO_KEEP_DAEMON", "CLEARDEV_BENCHMARK_MANIFEST", "CLEARDEV_BENCHMARK_RUNTIME"]) delete env[key];
const child = spawn(process.execPath, [path.join(frontend, "node_modules/@electron-forge/cli/dist/electron-forge-start.js"), frontend, "--", `--remote-debugging-port=${cdpPort}`], { cwd: frontend, env, stdio: ["pipe", "pipe", "pipe"] });
let tail = "", browser;
const record = chunk => { tail = (tail + chunk.toString()).slice(-96000); };
child.stdout.on("data", record); child.stderr.on("data", record);
const baseURL = `http://127.0.0.1:${port}`;
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const processBirth = pid => { try { const raw = readFileSync(`/proc/${pid}/stat`, "utf8"); return raw.slice(raw.lastIndexOf(")")+2).split(" ")[19]; } catch { return undefined; } };
// Foreground, supervised processes only. Snapshot exact descendants and their
// kernel start times before shutdown; never kill by name, port, or stale PID.
const ownedTree = pid => {
  const table = spawnSync("ps", ["-eo", "pid=,ppid="], { encoding: "utf8" });
  assert.equal(table.status, 0, "cannot inspect test-owned child processes");
  const rows = table.stdout.trim().split("\n").map(line => line.trim().split(/\s+/).map(Number));
  const owned = [pid];
  for (let i = 0; i < owned.length; i++) for (const [childPID, parent] of rows) if (parent === owned[i] && !owned.includes(childPID)) owned.push(childPID);
  return owned.map(pid => ({ pid, birth: processBirth(pid) })).filter(p => p.birth && p.pid !== process.pid);
};
const stopOwnedTree = async owned => {
  for (const item of owned.toReversed()) if (processBirth(item.pid) === item.birth) { try { process.kill(item.pid, "SIGTERM"); } catch (error) { if (error.code !== "ESRCH") throw error; } }
  await pause(250);
};
const wait = async (fn, timeout = 90000) => { const until = Date.now()+timeout; while (Date.now()<until) { if (child.exitCode !== null) throw new Error("Forge exited during bootstrap"); try { const v = await fn(); if (v) return v; } catch {} await pause(250); } throw new Error("product readiness deadline reached"); };
const request = async (route, payload, status = 200) => {
  const r = await fetch(baseURL+"/api/v1"+route, { method: payload === undefined ? "GET" : "POST", headers: { "content-type": "application/json" }, body: payload === undefined ? undefined : JSON.stringify(payload), signal: AbortSignal.timeout(20000) });
  assert.equal(r.status, status, `normal product ${route} returned ${r.status}`); return r.json();
};
const results = { kind: "NATIVE_PRODUCT_BOOTSTRAP_TEST", sourceSHA: sha, daemonSHA256, baselineProjects: [], realAgentsRun: false, humanApprovalPerformed: false, completed: false };
try {
  await wait(async () => (await fetch(baseURL+"/api/v1/projects", { signal: AbortSignal.timeout(1000) })).status === 200);
  // Forge's non-TTY reporter omits its interactive success banner. Require
  // fresh main/preload builds, then verify the real native renderer below.
  assert.ok(tail.includes("target built src/main.ts") && tail.includes("target built src/preload.ts"), "Forge did not build the current main/preload");
  assert.ok(tail.includes('msg="daemon listening"'), "the managed daemon did not start");
  browser = await chromium.connectOverCDP(`http://127.0.0.1:${cdpPort}`);
  const page = await wait(async () => browser.contexts().flatMap(c => c.pages()).find(p => /^http:\/\/(localhost|127\.0\.0\.1):/.test(p.url())));
  await page.waitForLoadState("domcontentloaded");
  assert.equal(await page.evaluate(() => typeof window.ao), "object", "real preload must be present, not a dev:web page");
  const rendererText = await page.locator("body").innerText();
  assert.ok(rendererText.length > 30, "native shell did not render");
  await page.screenshot({ path: path.join(evidence, "native-product-before.png") });
  results.rendererURL = page.url(); results.daemonURL = baseURL;
  for (const name of ["mail-search-baseline", "mail-sort-baseline"]) {
    const repo = path.join(projects, name); mkdirSync(repo);
    await request("/projects/initialize", { path: repo, template: "complex-mail-app" });
    await request("/projects", { path: repo }, 201);
    const git = (...args) => { const r = spawnSync("git", ["-C", repo, ...args], { encoding: "utf8" }); assert.equal(r.status, 0); return r.stdout.trim(); };
    assert.equal(git("status", "--porcelain", "--untracked-files=all"), "");
    const baseline = git("rev-parse", "HEAD");
    const test = spawnSync("npm", ["test"], { cwd: repo, env, encoding: "utf8", timeout: 60000 });
    writeFileSync(path.join(evidence, `${name}-npm-test.log`), (test.stdout || "")+(test.stderr || ""), { mode: 0o600 });
    assert.equal(test.status, 0); assert.ok(/# fail 0/.test(test.stdout), "full npm test emitted no zero-failure summary"); assert.ok(/# skipped 0/.test(test.stdout), "full npm test emitted no zero-skip summary");
    const appPort = await freePort();
    const app = spawn("npm", ["run", "start", "--silent"], { cwd: repo, env: { ...env, PORT: String(appPort), MAIL_APP_DB: path.join(evidence, name+".sqlite") }, stdio: ["pipe", "ignore", "pipe"] });
    let appError = ""; app.stderr.on("data", b => { appError = (appError+b.toString()).slice(-4000); });
    try {
      const health = await wait(async () => { if (app.exitCode !== null) throw new Error("baseline app exited"); const r = await fetch(`http://127.0.0.1:${appPort}/api/health`, { signal: AbortSignal.timeout(1000) }); return r.status === 200 ? r : false; }, 15000);
      assert.deepEqual(await health.json(), { status: "ok", application: "complex-mail-app" });
      assert.equal((await fetch(`http://127.0.0.1:${appPort}/__missing__`)).status, 404);
      assert.equal(git("status", "--porcelain", "--untracked-files=all"), "");
      results.baselineProjects.push({ name, path: repo, SHA: baseline, fullTests: "PASS", exactHealth: "PASS", gitClean: true });
    } finally {
      await stopOwnedTree(ownedTree(app.pid));
      await Promise.race([once(app, "exit"), pause(1000)]);
      app.stdin.destroy(); app.stderr.destroy();
    }
  }
  assert.equal(results.baselineProjects[0].SHA, results.baselineProjects[1].SHA, "fresh product baselines differ");
  await page.reload(); await page.waitForLoadState("domcontentloaded");
  await page.getByText("mail-search-baseline", { exact: false }).first().waitFor({ timeout: 15000 });
  await page.getByText("mail-sort-baseline", { exact: false }).first().waitFor({ timeout: 15000 });
  await page.screenshot({ path: path.join(evidence, "native-product-projects.png") });
  results.completed = true;
  console.log(JSON.stringify({ ...results, evidence }, null, 2));
} catch (e) { results.failure = e instanceof Error ? e.message : String(e); console.error(results.failure); process.exitCode = 1;
} finally {
  writeFileSync(path.join(evidence, "result.json"), JSON.stringify(results, null, 2), { mode: 0o600 });
  // Bootstrap stdout may contain provider paths; keep locally, do not echo.
  writeFileSync(path.join(evidence, "forge.log"), tail, { mode: 0o600 });
  const owned = ownedTree(child.pid);
  if (browser) { try { const session = await browser.newBrowserCDPSession(); await session.send("Browser.close"); } catch {} try { await browser.close(); } catch {} }
  child.kill("SIGINT");
  await Promise.race([once(child, "exit"), pause(4000)]);
  await stopOwnedTree(owned);
  await Promise.race([once(child, "exit"), pause(1000)]);
  child.stdin.destroy(); child.stdout.destroy(); child.stderr.destroy();
  rmSync(runtime, { recursive: true, force: true });
  console.log("Native product test evidence:", evidence);
}
