import { mkdtemp, readFile, stat } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";
import {
	DesktopStartupError,
	DesktopStartupRecorder,
	asDesktopStartupError,
	throwInjectedStartupFailure,
} from "./desktop-startup";

describe("DesktopStartupRecorder", () => {
	it("records ordered startup stages and the complete failure reason atomically", async () => {
		const root = await mkdtemp(path.join(os.tmpdir(), "ao-desktop-startup-"));
		const filePath = path.join(root, "desktop-startup.json");
		const timestamps = [
			"2026-08-29T01:00:00.000Z",
			"2026-08-29T01:00:01.000Z",
			"2026-08-29T01:00:02.000Z",
			"2026-08-29T01:00:03.000Z",
			"2026-08-29T01:00:04.000Z",
		];
		let index = 0;
		const recorder = new DesktopStartupRecorder({
			filePath,
			appRunId: "app-run-test",
			pid: 42,
			now: () => new Date(timestamps[index++] ?? timestamps.at(-1)!),
		});

		await Promise.all([
			recorder.stage("APP_READY"),
			recorder.stage("WINDOW_STARTING"),
			recorder.stage("WINDOW_READY"),
		]);
		const cause = new Error("window bridge exploded");
		await recorder.fail(new DesktopStartupError("DAEMON_STARTING", "daemon_start_failed", "Could not start daemon", cause));

		const record = JSON.parse(await readFile(filePath, "utf8"));
		expect(record).toMatchObject({
			schemaVersion: 1,
			appRunId: "app-run-test",
			pid: 42,
			stage: "FAILED",
			lastSuccessfulStage: "WINDOW_READY",
			failure: {
				stage: "DAEMON_STARTING",
				code: "daemon_start_failed",
				message: "Could not start daemon",
			},
		});
		expect(record.events.map((event: { stage: string }) => event.stage)).toEqual([
			"APP_READY",
			"WINDOW_STARTING",
			"WINDOW_READY",
			"FAILED",
		]);
		expect(record.failure.reason).toContain("window bridge exploded");
		expect((await stat(filePath)).mode & 0o777).toBe(0o600);
	});

	it("maps every supported injected boundary to a stable code", () => {
		for (const [injection, stage] of [
			["window", "WINDOW_STARTING"],
			["human_authority", "HUMAN_AUTHORITY_STARTING"],
			["daemon_process", "DAEMON_STARTING"],
			["health_check", "DAEMON_READY"],
		] as const) {
			expect(() => throwInjectedStartupFailure({ AO_STARTUP_FAILURE_INJECTION: injection }, injection, stage)).toThrowError(
				expect.objectContaining({ stage, code: `injected_${injection}_failure` }),
			);
		}
	});

	it("keeps an existing startup error and wraps ordinary failures with context", () => {
		const existing = new DesktopStartupError("WINDOW_STARTING", "window_failed", "window failed");
		expect(asDesktopStartupError(existing, "APP_READY", "other", "other")).toBe(existing);
		const wrapped = asDesktopStartupError(new Error("boom"), "DAEMON_READY", "ready_failed", "Readiness failed");
		expect(wrapped).toMatchObject({ stage: "DAEMON_READY", code: "ready_failed", message: "Readiness failed: boom" });
		expect(wrapped.reason).toContain("boom");
	});
});
