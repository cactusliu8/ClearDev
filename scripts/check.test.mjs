import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import { lintArgs, listSteps, loadPin, localModuleProxy, repoRoot, stepNames } from "./check.mjs";

const pin = {
	go: "1.25.7",
	node: "22.23.1",
	npm: "10.9.8",
	golangciLint: "2.12.2",
};
const startSha = "4c384891a31e5551b6661521f9d75295a65b65b8";

test("backend runs race before incremental lint", () => {
	const names = stepNames("backend", { startSha, pin });
	assert.equal(names[0], "toolchain-go");
	assert.ok(names.indexOf("race") >= 0);
	assert.ok(names.indexOf("lint") >= 0);
	assert.ok(names.indexOf("race") < names.indexOf("lint"));
});

test("incremental lint binds the plan start commit", () => {
	const args = lintArgs(pin, startSha);
	assert.ok(args.includes(`--new-from-rev=${startSha}`));
	assert.ok(args.some((arg) => arg.includes("golangci-lint@v2.12.2")));
	assert.throws(() => lintArgs(pin, null), /new-from-rev/);
});

test("scoped backend checks require explicit packages and cap concurrency", () => {
	const packages = ["./internal/service/cleardev", "./internal/storage/sqlite/..."];
	for (const kind of ["backend-test", "backend-race"]) {
		const steps = listSteps(kind, { startSha, pin, packages });
		assert.deepEqual(steps.map((step) => step.name), ["toolchain-go", kind === "backend-race" ? "race" : "test"]);
		const args = steps[1].args;
		assert.ok(args.includes("-p"));
		assert.ok(args.includes("2"));
		assert.deepEqual(args.slice(-2), packages);
	}
	const lint = listSteps("backend-lint", { startSha, pin, packages }).at(-1);
	assert.equal(lint.name, "lint");
	assert.ok(lint.args.includes("--concurrency=2"));
	assert.ok(lint.args.includes(`--new-from-rev=${startSha}`));
	assert.deepEqual(lint.args.slice(-2), packages);
	assert.throws(() => listSteps("backend-test", { startSha, pin }), /显式提供/);
	assert.throws(() => listSteps("backend-race", { startSha, pin, packages: ["internal/service"] }), /以 \.\//);
});

test("all runs quick, backend, frontend, then api", () => {
	const names = stepNames("all", { startSha, pin });
	assert.equal(names[0], "toolchain");
	assert.ok(names.includes("toolchain-go"));
	assert.ok(names.includes("toolchain-node"));
	assert.equal(names.at(-1), "api-drift");
	assert.ok(names.indexOf("race") < names.indexOf("lint"));
});

test("quick checks the exact local Go, Node.js, and npm versions", () => {
	const toolchain = listSteps("quick", { pin })[0];
	assert.equal(toolchain.name, "toolchain");
	assert.ok(toolchain.args.includes("-require-go"));
	assert.ok(toolchain.args.includes("-require-node"));
});

test("frontend lists steps without a lint baseline", () => {
	const steps = listSteps("frontend", { pin });
	const names = steps.map((step) => step.name);
	assert.equal(names[0], "toolchain-node");
	assert.ok(!names.includes("lint"));
	assert.ok(steps.find((step) => step.name === "browser-runtime").args.includes("--offline"));
});

test("api checks Go, Node.js, and npm before generation", () => {
	const toolchain = listSteps("api", { pin })[0];
	assert.equal(toolchain.name, "toolchain-api");
	assert.ok(toolchain.args.includes("-require-go"));
	assert.ok(toolchain.args.includes("-require-node"));
});

test("Go modules use only the local download cache", () => {
	assert.equal(localModuleProxy("/tmp/go modules"), "file:///tmp/go%20modules/cache/download");
	assert.throws(() => localModuleProxy("relative-cache"), /绝对路径/);
});

test("offline browser preparation fails without downloading", (t) => {
	const workdir = mkdtempSync(path.join(tmpdir(), "cleardev-browser-offline-"));
	t.after(() => rmSync(workdir, { recursive: true, force: true }));
	const result = spawnSync(
		process.execPath,
		[path.join(repoRoot, "frontend/scripts/prepare-agent-browser.mjs"), "--quiet"],
		{ cwd: workdir, encoding: "utf8", env: { ...process.env, CLEARDEV_OFFLINE_CHECK: "1" } },
	);
	assert.notEqual(result.status, 0);
	assert.match(result.stderr, /本机缺少 agent-browser/);
});

test("unknown check entry fails", () => {
	assert.throws(() => listSteps("nope", { pin, startSha }), /未知检查入口/);
});

test("toolchain pin file is complete", () => {
	const loaded = loadPin();
	assert.equal(loaded.go, "1.25.7");
	assert.equal(loaded.node, "22.23.1");
	assert.equal(loaded.npm, "10.9.8");
	assert.equal(loaded.golangciLint, "2.12.2");
});
