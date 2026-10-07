import assert from "node:assert/strict";
import test from "node:test";

import { projectName } from "../src/project.js";

test("reports the frozen project name", () => {
  assert.equal(projectName(), "ClearDev S02 fixture");
});
