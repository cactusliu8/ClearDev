import test from "node:test";
import assert from "node:assert/strict";
import { deduplicateEmails } from "../src/deduplicate.js";

test("deduplicateEmails keeps the first normalized address", () => {
  assert.deepEqual(deduplicateEmails(["Ada@Example.COM", " ada@example.com "]), ["ada@example.com"]);
});
