// @vitest-environment node
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { HumanDecisionOffer } from "./human-authority";
import {
	createSimulatedUserDecider,
	loadSimulatedUserConfig,
	simulatedUserOfferRecord,
	type SimulatedUserConfig,
} from "./benchmark-simulated-user";

function privateJSON(directory: string, name: string, value: unknown): string {
	const path = join(directory, name);
	writeFileSync(path, JSON.stringify(value) + "\n", { mode: 0o600 });
	chmodSync(path, 0o600);
	return path;
}

function configFixture(): { directory: string; config: SimulatedUserConfig; path: string } {
	const root = mkdtempSync(join(tmpdir(), "cleardev-sim-user-"));
	const directory = join(root, "decisions");
	mkdirSync(directory, { recursive: true, mode: 0o700 });
	const path = privateJSON(root, "simulated-user.json", {
		schemaVersion: 1,
		mode: "SIMULATED_USER",
		planCommit: "a".repeat(40),
		candidateCommit: "b".repeat(40),
		attemptID: "attempt-1",
		group: "G3",
		directory,
		policySha256: "c".repeat(64),
	});
	const config = { ...(JSON.parse(readFileSync(path, "utf8")) as SimulatedUserConfig), directory };
	return { directory, config, path };
}

const offer = (extra: Partial<HumanDecisionOffer> = {}): HumanDecisionOffer => ({
	protocolVersion: 1,
	kind: "HUMAN_DECISION_OFFER",
	desktopRunId: "run-1",
	requestId: "req-1",
	decisionKind: "APPROVE_DIRECTION_CHANGE",
	bindingSchemaVersion: 1,
	binding: { requirementVersionId: "v2" },
	contentSha256: "d".repeat(64),
	nonce: "top-secret-nonce",
	expiresAt: new Date(Date.now() + 60_000).toISOString(),
	display: { title: "t", summary: "s", fullContent: "# v2", changeSummary: "c" },
	...extra,
});

describe("simulated user configuration", () => {
	it("is absent without the environment variable and rejects malformed or non-private files", () => {
		expect(loadSimulatedUserConfig({})).toBeNull();
		const { path } = configFixture();
		expect(loadSimulatedUserConfig({ CLEARDEV_BENCHMARK_SIMULATED_USER: path })?.attemptID).toBe("attempt-1");
		chmodSync(path, 0o644);
		expect(() => loadSimulatedUserConfig({ CLEARDEV_BENCHMARK_SIMULATED_USER: path })).toThrow();
		expect(() => loadSimulatedUserConfig({ CLEARDEV_BENCHMARK_SIMULATED_USER: "relative.json" })).toThrow();
	});
});

describe("simulated user decider", () => {
	it("keeps the nonce out of the private offer copy", () => {
		const copy = simulatedUserOfferRecord(offer());
		expect(copy.requestId).toBe("req-1");
		expect(JSON.stringify(copy)).not.toContain("nonce");
		expect(JSON.stringify(copy)).not.toContain("top-secret-nonce");
	});

	it("refuses out-of-scope kinds without writing an offer", async () => {
		const { directory, config } = configFixture();
		const records: Record<string, unknown>[] = [];
		const decide = createSimulatedUserDecider(config, (value) => records.push(value), { pollMs: 1 });
		expect(await decide(offer({ decisionKind: "DELETE_EVERYTHING" }))).toBe("LATER");
		expect(records.at(-1)?.reason).toBe("DECISION_KIND_OUT_OF_SCOPE");
		expect(() => readFileSync(join(directory, "offer-req-1.json"), "utf8")).toThrow();
	});

	it("approves only a matching suggestion and records intent and sent evidence", async () => {
		const { directory, config } = configFixture();
		const records: Record<string, unknown>[] = [];
		const decide = createSimulatedUserDecider(config, (value) => records.push(value), { pollMs: 1 });
		const pending = decide(offer());
		await new Promise((resolve) => setTimeout(resolve, 10));
		privateJSON(directory, "suggestion-req-1.json", {
			schemaVersion: 1,
			requestId: "req-1",
			contentSha256: "d".repeat(64),
			decision: "APPROVE",
			source: "RULE",
			policySha256: "c".repeat(64),
			reason: "exact published text",
		});
		expect(await pending).toBe("APPROVE");
		expect(JSON.parse(readFileSync(join(directory, "intent-req-1.json"), "utf8")).source).toBe("RULE");
		expect(JSON.parse(readFileSync(join(directory, "sent-req-1.json"), "utf8")).channelDecision).toBe("APPROVE");
		expect(records.some((value) => value.phase === "sent" || value.phase === "intent")).toBe(true);
	});

	it("refuses a suggestion bound to another content or policy", async () => {
		const { directory, config } = configFixture();
		const records: Record<string, unknown>[] = [];
		const decide = createSimulatedUserDecider(config, (value) => records.push(value), { pollMs: 1 });
		const pending = decide(offer());
		await new Promise((resolve) => setTimeout(resolve, 10));
		privateJSON(directory, "suggestion-req-1.json", {
			schemaVersion: 1,
			requestId: "req-1",
			contentSha256: "e".repeat(64),
			decision: "APPROVE",
			source: "AI",
			policySha256: "c".repeat(64),
			reason: "tampered",
		});
		expect(await pending).toBe("LATER");
		expect(records.at(-1)?.reason).toBe("SUGGESTION_MISMATCH");
	});
});
