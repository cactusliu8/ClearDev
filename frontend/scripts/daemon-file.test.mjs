import { mkdtempSync, rmSync, statSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { ensureDaemonExecutableMode } from "./daemon-file.mjs";

const temporaryDirectories = [];

afterEach(() => {
	for (const dir of temporaryDirectories.splice(0)) rmSync(dir, { recursive: true, force: true });
});

describe("ensureDaemonExecutableMode", () => {
	it.skipIf(process.platform === "win32")("normalizes a file created under umask 0002 to mode 0755", () => {
		const dir = mkdtempSync(join(tmpdir(), "ao-daemon-mode-"));
		temporaryDirectories.push(dir);
		const binary = join(dir, "ao");
		const created = spawnSync(
			process.execPath,
			[
				"--input-type=module",
				"--eval",
				'import { writeFileSync } from "node:fs"; process.umask(0o002); writeFileSync(process.env.AO_TEST_BINARY, "daemon", { mode: 0o777 });',
			],
			{ env: { ...process.env, AO_TEST_BINARY: binary }, encoding: "utf8" },
		);
		expect(created.status, created.stderr).toBe(0);
		expect(statSync(binary).mode & 0o777).toBe(0o775);

		ensureDaemonExecutableMode(binary, "linux");
		expect(statSync(binary).mode & 0o777).toBe(0o755);
	});

	it.skipIf(process.platform === "win32")("does not chmod Windows builds", () => {
		const dir = mkdtempSync(join(tmpdir(), "ao-daemon-mode-win-"));
		temporaryDirectories.push(dir);
		const binary = join(dir, "ao.exe");
		writeFileSync(binary, "daemon", { mode: 0o775 });
		const createdMode = statSync(binary).mode & 0o777;

		ensureDaemonExecutableMode(binary, "win32");
		expect(statSync(binary).mode & 0o777).toBe(createdMode);
	});
});
