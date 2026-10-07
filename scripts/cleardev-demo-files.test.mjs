import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { requirePackagedRegularFiles } from "./cleardev-demo-files.mjs";

test("accepts ordinary runtime files inside the package", async (t) => {
	const root = await fixture(t);
	const runtime = path.join(root, "resources", "daemon", "ao");
	await mkdir(path.dirname(runtime), { recursive: true });
	await writeFile(runtime, "daemon", { mode: 0o755 });
	await requirePackagedRegularFiles(root, { daemon: runtime });
});

test("rejects a runtime target symbolic link", async (t) => {
	const root = await fixture(t);
	const outside = await fixture(t);
	const target = path.join(outside, "outside-daemon");
	const runtime = path.join(root, "resources", "daemon", "ao");
	await mkdir(path.dirname(runtime), { recursive: true });
	await writeFile(target, "outside", { mode: 0o755 });
	await symlink(target, runtime);
	await assert.rejects(requirePackagedRegularFiles(root, { daemon: runtime }), /symbolic link/);
});

test("rejects a runtime reached through a parent link outside the package", async (t) => {
	const root = await fixture(t);
	const outside = await fixture(t);
	const outsideBrowser = path.join(outside, "agent-browser");
	const linkedDirectory = path.join(root, "resources", "agent-browser");
	await mkdir(path.dirname(linkedDirectory), { recursive: true });
	await writeFile(outsideBrowser, "outside", { mode: 0o755 });
	await symlink(outside, linkedDirectory);
	await assert.rejects(
		requirePackagedRegularFiles(root, { agentBrowser: path.join(linkedDirectory, "agent-browser") }),
		/resolves outside package root/,
	);
});

async function fixture(t) {
	const root = await mkdtemp(path.join(os.tmpdir(), "cleardev-package-files-"));
	t.after(() => rm(root, { recursive: true, force: true }));
	return root;
}
