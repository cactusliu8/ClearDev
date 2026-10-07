import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";

const insideClearDevCheck = process.env.CLEARDEV_CHECK_SANDBOX === "1";

test("ClearDev integration checks run in the frozen sandbox", { skip: !insideClearDevCheck }, () => {
  assert.equal(process.getuid?.(), 65532, "check process must not run as root");
  assert.deepEqual(fs.readdirSync("/sys/class/net").sort(), ["lo"], "check must have no external network interface");
  assert.equal(fs.readFileSync("/sys/fs/cgroup/memory.max", "utf8").trim(), "536870912");
  assert.equal(fs.readFileSync("/sys/fs/cgroup/pids.max", "utf8").trim(), "64");

  assert.throws(() => fs.writeFileSync("/cleardev-root-write-probe", "blocked"), /EROFS|EACCES/);
  assert.throws(() => fs.writeFileSync("/workspace/package.json", "blocked"), /EROFS|EACCES/);
  fs.writeFileSync("/workspace/.cleardev-output-probe", "temporary");
  fs.rmSync("/workspace/.cleardev-output-probe");
});
