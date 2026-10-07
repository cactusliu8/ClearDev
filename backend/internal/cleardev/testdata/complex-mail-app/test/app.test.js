import test from "node:test";
import assert from "node:assert/strict";
import { DatabaseSync } from "node:sqlite";
import { startApp, stopApp } from "./start-app.js";

test("page, API, and SQLite agree on the frozen sample", async (t) => {
  const { port, child, dbFile } = await startApp("mail-app-int-");
  t.after(() => stopApp(child));
  const page = await fetch(`http://127.0.0.1:${port}/`);
  assert.equal(page.ok, true);
  const html = await page.text();
  assert.match(html, /data-testid="accepted-count"/);
  const response = await fetch(`http://127.0.0.1:${port}/api/import`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      emails: [
        "user@example.com",
        " user@example.com ",
        "USER@example.com",
        "not-an-email",
        "foo@example.com",
      ],
    }),
  });
  assert.equal(response.ok, true);
  const body = await response.json();
  assert.equal(body.accepted, 2);
  assert.equal(body.rejected, 1);
  assert.equal(body.duplicates, 2);
  const db = new DatabaseSync(dbFile);
  const rows = db.prepare("SELECT normalized_email FROM contacts ORDER BY id").all();
  assert.deepEqual(
    rows.map((row) => row.normalized_email),
    ["user@example.com", "foo@example.com"],
  );
  db.close();
});
