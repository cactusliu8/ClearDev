import test from "node:test";
import assert from "node:assert/strict";
import { startApp, stopApp } from "./start-app.js";

test("import API returns accepted, rejected, and duplicate counts", async (t) => {
  const { port, child } = await startApp("mail-app-api-");
  t.after(() => stopApp(child));
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
});
