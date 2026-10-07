import test from "node:test";
import assert from "node:assert/strict";
import { normalizeEmail } from "../src/email.js";

test("normalizeEmail trims and lowercases", () => {
  assert.equal(normalizeEmail("  Ada@Example.COM "), "ada@example.com");
});
