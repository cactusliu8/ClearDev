import { spawn } from "node:child_process";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import fs from "node:fs";

export async function freePort() {
  return await new Promise((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      server.close(() => resolve(address.port));
    });
    server.on("error", reject);
  });
}

export function stopApp(child) {
  if (!child || child.exitCode != null) {
    return;
  }
  child.kill("SIGKILL");
}

export async function startApp(prefix) {
  const port = await freePort();
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), prefix));
  const dbFile = path.join(dataDir, "app.sqlite");
  const child = spawn(process.execPath, ["--experimental-sqlite", "--experimental-strip-types", "backend/src/server.ts"], {
    env: { ...process.env, PORT: String(port), MAIL_APP_DB: dbFile },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "";
  const collect = (chunk) => {
    output += String(chunk);
  };
  child.stdout.on("data", collect);
  child.stderr.on("data", collect);
  const deadline = Date.now() + 12000;
  try {
    while (Date.now() < deadline) {
      if (child.exitCode != null) {
        throw new Error(`server exited ${child.exitCode}: ${output}`);
      }
      if (output.includes("listening") || (await portOpen(port))) {
        return { port, child, dbFile };
      }
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    throw new Error(`server start timeout: ${output}`);
  } catch (err) {
    stopApp(child);
    throw err;
  }
}

async function portOpen(port) {
  return await new Promise((resolve) => {
    const sock = net.connect({ host: "127.0.0.1", port });
    const done = (ok) => {
      sock.removeAllListeners();
      sock.on("error", () => {});
      sock.destroy();
      resolve(ok);
    };
    sock.setTimeout(100);
    sock.on("connect", () => done(true));
    sock.on("timeout", () => done(false));
    sock.on("error", () => done(false));
  });
}
