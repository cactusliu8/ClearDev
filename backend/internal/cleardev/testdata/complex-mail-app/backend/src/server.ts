import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const host = "127.0.0.1";
const port = Number(process.env.PORT || 3456);
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const dbFile = process.env.MAIL_APP_DB || path.join(root, "data", "app.sqlite");
void dbFile;

function send(res: http.ServerResponse, status: number, body: string, type = "text/plain; charset=utf-8"): void {
  res.writeHead(status, { "content-type": type, "content-length": Buffer.byteLength(body) });
  res.end(body);
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url || "/", `http://${host}:${port}`);
  if (req.method === "GET" && (url.pathname === "/" || url.pathname === "/index.html")) {
    const html = fs.readFileSync(path.join(root, "frontend", "index.html"), "utf8");
    send(res, 200, html, "text/html; charset=utf-8");
    return;
  }
  if (req.method === "GET" && url.pathname.startsWith("/frontend/")) {
    const relative = url.pathname.replace(/^\/frontend\//, "");
    const file = path.join(root, "frontend", relative);
    if (!file.startsWith(path.join(root, "frontend"))) {
      send(res, 404, "not found");
      return;
    }
    if (!fs.existsSync(file)) {
      send(res, 404, "not found");
      return;
    }
    const type = file.endsWith(".css") ? "text/css; charset=utf-8" : "text/javascript; charset=utf-8";
    send(res, 200, fs.readFileSync(file, "utf8"), type);
    return;
  }
  if (url.pathname.startsWith("/api/")) {
    send(res, 501, JSON.stringify({ error: "not implemented" }), "application/json; charset=utf-8");
    return;
  }
  send(res, 404, "not found");
});

server.listen(port, host, () => {
  process.stdout.write(`listening on http://${host}:${port}\n`);
});
