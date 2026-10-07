import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";

test("locked package scripts stay present", () => {
  const pkg = JSON.parse(fs.readFileSync("package.json", "utf8"));
  assert.equal(pkg.name, "complex-mail-app");
  assert.deepEqual(Object.keys(pkg.dependencies || {}), []);
  for (const name of [
    "build",
    "test:template",
    "test:backend",
    "test:database",
    "test:api",
    "test:frontend",
    "test",
    "check:migrations",
    "start",
  ]) {
    assert.equal(typeof pkg.scripts[name], "string", name);
  }
});

test("template layout is present", () => {
  for (const file of [
    "PRD.md",
    "backend/src/emails.ts",
    "backend/src/db.ts",
    "backend/src/server.ts",
    "frontend/index.html",
    "frontend/app.js",
    "migrations/000_placeholder.sql",
    "scripts/check-migrations.mjs",
    ".github/workflows/ci.yml",
  ]) {
    assert.equal(fs.existsSync(file), true, file);
  }
});
