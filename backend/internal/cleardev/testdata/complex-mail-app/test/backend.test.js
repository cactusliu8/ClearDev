import test from "node:test";
import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";

const emails = await import(pathToFileURL("backend/src/emails.ts").href);

test("normalizeEmail trims and lowercases", () => {
  assert.equal(emails.normalizeEmail("  User@Example.COM "), "user@example.com");
});

test("invalid addresses are rejected", () => {
  assert.equal(emails.isValidEmail("not-an-email"), false);
  assert.equal(emails.isValidEmail("user@example.com"), true);
});

test("duplicates keep the first normalized address", () => {
  const result = emails.classifyEmails([
    "user@example.com",
    " user@example.com ",
    "USER@example.com",
    "not-an-email",
    "foo@example.com",
  ]);
  assert.deepEqual(result.accepted, ["user@example.com", "foo@example.com"]);
  assert.equal(result.rejected, 1);
  assert.equal(result.duplicates, 2);
});
