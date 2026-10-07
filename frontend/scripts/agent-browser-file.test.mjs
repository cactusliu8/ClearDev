import { createHash } from "node:crypto";
import { mkdtemp, rm, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { ensureVerifiedExecutable } from "./agent-browser-file.mjs";

const temporaryDirectories = [];

afterEach(async () => {
	await Promise.all(temporaryDirectories.splice(0).map((dir) => rm(dir, { recursive: true, force: true })));
});

describe("ensureVerifiedExecutable", () => {
	it.skipIf(process.platform === "win32")("repairs a checksum-valid cached binary to mode 0755 without downloading", async () => {
		const dir = await mkdtemp(path.join(os.tmpdir(), "ao-browser-cache-"));
		temporaryDirectories.push(dir);
		const binary = path.join(dir, "agent-browser");
		const contents = Buffer.from("already cached and checksum-valid");
		await writeFile(binary, contents, { mode: 0o644 });
		const expected = createHash("sha256").update(contents).digest("hex");

		await expect(ensureVerifiedExecutable(binary, expected, process.platform)).resolves.toBe(expected);
		expect((await stat(binary)).mode & 0o777).toBe(0o755);
	});

	it("rejects a cached file before changing permissions when its checksum is wrong", async () => {
		const dir = await mkdtemp(path.join(os.tmpdir(), "ao-browser-cache-bad-"));
		temporaryDirectories.push(dir);
		const binary = path.join(dir, "agent-browser");
		await writeFile(binary, "wrong bytes", { mode: 0o600 });

		await expect(ensureVerifiedExecutable(binary, "a".repeat(64), "linux")).rejects.toThrow("checksum mismatch");
		expect((await stat(binary)).mode & 0o777).toBe(0o600);
	});
});
