import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { openDatabase, applyMigrations } from "./db.ts";
import { classifyEmails } from "./emails.ts";

const host = "127.0.0.1";
const port = Number(process.env.PORT || 3456);
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const db = openDatabase(process.env.MAIL_APP_DB || path.join(root, "data", "app.sqlite"));
applyMigrations(db, path.join(root, "migrations"));
const insert = db.prepare("INSERT OR IGNORE INTO contacts (normalized_email) VALUES (?)");
const list = db.prepare("SELECT id, normalized_email AS email FROM contacts ORDER BY id");
const assets: Record<string, [string, string]> = {
  "/": ["frontend/index.html", "text/html; charset=utf-8"],
  "/index.html": ["frontend/index.html", "text/html; charset=utf-8"],
  "/frontend/app.js": ["frontend/app.js", "text/javascript; charset=utf-8"],
  "/frontend/app.css": ["frontend/app.css", "text/css; charset=utf-8"],
};

function send(res: http.ServerResponse, status: number, body: string, type = "application/json; charset=utf-8"): void {
  res.writeHead(status, {
    "content-type": type,
    "content-length": Buffer.byteLength(body),
    "x-content-type-options": "nosniff",
    "cache-control": "no-store",
  });
  res.end(body);
}

const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url || "/", `http://${host}:${port}`);
    if (req.method === "GET" && Object.hasOwn(assets, url.pathname)) {
      const [file, type] = assets[url.pathname];
      send(res, 200, fs.readFileSync(path.join(root, file), "utf8"), type);
      return;
    }
    if (req.method === "GET" && url.pathname === "/api/health") {
      db.prepare("SELECT COUNT(*) FROM contacts").get();
      send(res, 200, JSON.stringify({ status: "ok", application: "complex-mail-app" }));
      return;
    }
    if (req.method === "GET" && url.pathname === "/api/contacts") {
      send(res, 200, JSON.stringify({ contacts: list.all() }));
      return;
    }
    if (req.method === "POST" && url.pathname === "/api/import") {
      if (req.headers["content-type"]?.split(";")[0].trim().toLowerCase() !== "application/json") {
        send(res, 415, JSON.stringify({ error: "expected application/json" }));
        return;
      }
      const chunks: Buffer[] = [];
      let bytes = 0;
      for await (const chunk of req) {
        bytes += chunk.length;
        if (bytes > 64 * 1024) {
          send(res, 413, JSON.stringify({ error: "import body too large" }));
          return;
        }
        chunks.push(chunk);
      }
      let body;
      try {
        body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      } catch {
        send(res, 400, JSON.stringify({ error: "invalid JSON" }));
        return;
      }
      if (!body || !Array.isArray(body.emails) || body.emails.length > 2000 || !body.emails.every((value: unknown) => typeof value === "string")) {
        send(res, 400, JSON.stringify({ error: "emails must be an array of at most 2000 strings" }));
        return;
      }
      const classified = classifyEmails(body.emails);
      let accepted = 0;
      db.exec("BEGIN IMMEDIATE");
      try {
        for (const email of classified.accepted) accepted += Number(insert.run(email).changes);
        db.exec("COMMIT");
      } catch (error) {
        db.exec("ROLLBACK");
        throw error;
      }
      send(res, 200, JSON.stringify({
        accepted,
        rejected: classified.rejected,
        duplicates: classified.duplicates + classified.accepted.length - accepted,
      }));
      return;
    }
    send(res, 404, JSON.stringify({ error: "not found" }));
  } catch {
    if (!res.headersSent) send(res, 500, JSON.stringify({ error: "request failed" }));
    else res.end();
  }
});

server.requestTimeout = 10_000;
server.listen(port, host, () => {
  const address = server.address();
  if (address && typeof address !== "string") process.stdout.write(`listening on http://${host}:${address.port}\n`);
});
for (const signal of ["SIGTERM", "SIGINT"] as const) {
  process.once(signal, () => server.close(() => {
    db.close();
    process.exit(0);
  }));
}
