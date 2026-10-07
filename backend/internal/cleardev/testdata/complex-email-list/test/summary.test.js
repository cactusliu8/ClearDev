import test from "node:test";
import assert from "node:assert/strict";
import { summarize } from "../src/summary.js";

test("summarize returns accepted and rejected counts", () => {
  assert.deepEqual(summarize(2, 1), { accepted: 2, rejected: 1 });
});
