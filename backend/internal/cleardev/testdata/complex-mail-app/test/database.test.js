import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import { pathToFileURL } from "node:url";

const dbMod = await import(pathToFileURL("backend/src/db.ts").href);

test("applied migrations create the contacts table", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "mail-app-db-"));
  const file = path.join(dir, "app.sqlite");
  const db = new DatabaseSync(file);
  dbMod.applyMigrations(db, "migrations");
  const rows = db.prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'contacts'").all();
  assert.equal(rows.length, 1);
  const columns = db.prepare("PRAGMA table_info(contacts)").all().map((row) => row.name);
  assert.equal(columns.includes("normalized_email"), true);
  db.close();
});
