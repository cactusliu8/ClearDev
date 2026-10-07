import {
	closeSync,
	existsSync,
	fsyncSync,
	lstatSync,
	mkdirSync,
	openSync,
	readFileSync,
	realpathSync,
	renameSync,
	unlinkSync,
	writeSync,
} from "node:fs";
import { dirname, isAbsolute, join } from "node:path";
import type { HumanDecisionChoice, HumanDecisionOffer } from "./human-authority";

// Private per-attempt simulated-user connection. The runner writes this file
// inside the attempt control root; a malformed value refuses to start rather
// than silently falling back to human dialogs.

export const SIMULATED_USER_ENV = "CLEARDEV_BENCHMARK_SIMULATED_USER";
export const SIMULATED_USER_ALLOWED_KINDS = ["CONFIRM_REQUIREMENT_VERSION", "APPROVE_DIRECTION_CHANGE"] as const;
export const SIMULATED_USER_POLL_MS = 200;

export type SimulatedUserConfig = {
	schemaVersion: 1;
	mode: "SIMULATED_USER";
	planCommit: string;
	candidateCommit: string;
	attemptID: string;
	group: string;
	directory: string;
	policySha256: string;
};

export type SimulatedUserSuggestion = {
	schemaVersion: 1;
	requestId: string;
	contentSha256: string;
	decision: "APPROVE" | "NEEDS_USER";
	source: "RULE" | "AI";
	policySha256: string;
	reason: string;
};

export type SimulatedUserRecorder = (value: Record<string, unknown>) => void;

function privateRegularFile(path: string): boolean {
	try {
		if (realpathSync(path) !== path) return false;
		const stat = lstatSync(path);
		return stat.isFile() && !stat.isSymbolicLink() && stat.uid === process.getuid?.() && (stat.mode & 0o077) === 0;
	} catch {
		return false;
	}
}

export function loadSimulatedUserConfig(env: NodeJS.ProcessEnv = process.env): SimulatedUserConfig | null {
	const raw = env[SIMULATED_USER_ENV];
	if (raw === undefined || raw === "") return null;
	if (!isAbsolute(raw) || realpathSync(raw) !== raw || !privateRegularFile(raw)) {
		throw new Error("simulated-user configuration path is not a private absolute file");
	}
	const parsed = JSON.parse(readFileSync(raw, "utf8")) as Partial<SimulatedUserConfig>;
	if (
		parsed.schemaVersion !== 1 ||
		parsed.mode !== "SIMULATED_USER" ||
		typeof parsed.planCommit !== "string" ||
		!/^[0-9a-f]{40}$/.test(parsed.planCommit) ||
		typeof parsed.candidateCommit !== "string" ||
		!/^[0-9a-f]{40}$/.test(parsed.candidateCommit) ||
		typeof parsed.attemptID !== "string" ||
		parsed.attemptID.length === 0 ||
		typeof parsed.group !== "string" ||
		!/^G[34]$/.test(parsed.group) ||
		typeof parsed.policySha256 !== "string" ||
		!/^[0-9a-f]{64}$/.test(parsed.policySha256) ||
		typeof parsed.directory !== "string" ||
		!isAbsolute(parsed.directory)
	) {
		throw new Error("simulated-user configuration is malformed");
	}
	mkdirSync(parsed.directory, { recursive: true, mode: 0o700 });
	const resolved = realpathSync(parsed.directory);
	if (resolved !== parsed.directory || lstatSync(resolved).uid !== process.getuid?.()) {
		throw new Error("simulated-user directory is not a private real path");
	}
	return parsed as SimulatedUserConfig;
}

function writePrivateJSON(path: string, value: unknown): void {
	const temporary = `${path}.tmp-${process.pid}`;
	const fd = openSync(temporary, "wx", 0o600);
	try {
		const bytes = Buffer.from(JSON.stringify(value) + "\n", "utf8");
		let offset = 0;
		while (offset < bytes.length) {
			const written = writeSync(fd, bytes, offset);
			if (written <= 0) throw new Error("simulated-user evidence write failed");
			offset += written;
		}
		fsyncSync(fd);
	} finally {
		closeSync(fd);
	}
	renameSync(temporary, path);
	const directory = openSync(dirname(path), "r");
	try {
		fsyncSync(directory);
	} finally {
		closeSync(directory);
	}
}

function removeQuietly(path: string): void {
	try {
		unlinkSync(path);
	} catch {
		// best effort
	}
}

// The private offer copy keeps every non-secret field the controller needs to
// judge. The channel token and nonce never enter this directory.
export function simulatedUserOfferRecord(offer: HumanDecisionOffer): Record<string, unknown> {
	return {
		schemaVersion: 1,
		requestId: offer.requestId,
		desktopRunId: offer.desktopRunId,
		decisionKind: offer.decisionKind,
		bindingSchemaVersion: offer.bindingSchemaVersion,
		binding: offer.binding,
		contentSha256: offer.contentSha256,
		expiresAt: offer.expiresAt,
		display: offer.display,
	};
}

export function createSimulatedUserDecider(
	config: SimulatedUserConfig,
	record: SimulatedUserRecorder,
	options: { pollMs?: number; wait?: (milliseconds: number) => Promise<void> } = {},
): (offer: HumanDecisionOffer) => Promise<HumanDecisionChoice> {
	const wait = options.wait ?? ((milliseconds: number) => new Promise<void>((resolve) => setTimeout(resolve, milliseconds)));
	const pollMs = options.pollMs ?? SIMULATED_USER_POLL_MS;
	return async (offer) => {
		if (!(SIMULATED_USER_ALLOWED_KINDS as readonly string[]).includes(offer.decisionKind)) {
			record({ phase: "refused", reason: "DECISION_KIND_OUT_OF_SCOPE", requestId: offer.requestId, decisionKind: offer.decisionKind });
			return "LATER";
		}
		const offerPath = join(config.directory, `offer-${offer.requestId}.json`);
		const suggestionPath = join(config.directory, `suggestion-${offer.requestId}.json`);
		const intentPath = join(config.directory, `intent-${offer.requestId}.json`);
		const sentPath = join(config.directory, `sent-${offer.requestId}.json`);
		removeQuietly(suggestionPath);
		writePrivateJSON(offerPath, simulatedUserOfferRecord(offer));
		record({ phase: "offer-written", requestId: offer.requestId, decisionKind: offer.decisionKind, contentSha256: offer.contentSha256 });
		const deadline = Date.parse(offer.expiresAt);
		if (!Number.isFinite(deadline)) return "LATER";
		for (;;) {
			if (Date.now() >= deadline) {
				record({ phase: "expired", requestId: offer.requestId });
				return "LATER";
			}
			if (existsSync(suggestionPath) && privateRegularFile(suggestionPath)) {
				let suggestion: SimulatedUserSuggestion;
				try {
					suggestion = JSON.parse(readFileSync(suggestionPath, "utf8")) as SimulatedUserSuggestion;
				} catch (error) {
					record({ phase: "refused", reason: "SUGGESTION_INVALID", requestId: offer.requestId, error: String(error) });
					return "LATER";
				}
				if (
					suggestion.schemaVersion !== 1 ||
					suggestion.requestId !== offer.requestId ||
					suggestion.contentSha256 !== offer.contentSha256 ||
					suggestion.policySha256 !== config.policySha256 ||
					!["RULE", "AI"].includes(suggestion.source) ||
					!["APPROVE", "NEEDS_USER"].includes(suggestion.decision)
				) {
					record({ phase: "refused", reason: "SUGGESTION_MISMATCH", requestId: offer.requestId, suggestion });
					return "LATER";
				}
				const decision: HumanDecisionChoice = suggestion.decision === "APPROVE" ? "APPROVE" : "LATER";
				writePrivateJSON(intentPath, {
					schemaVersion: 1,
					requestId: offer.requestId,
					desktopRunId: offer.desktopRunId,
					decisionKind: offer.decisionKind,
					contentSha256: offer.contentSha256,
					source: suggestion.source,
					decision: suggestion.decision,
					reason: suggestion.reason,
					at: new Date().toISOString(),
				});
				record({ phase: "intent", requestId: offer.requestId, source: suggestion.source, decision: suggestion.decision });
				writePrivateJSON(sentPath, {
					schemaVersion: 1,
					requestId: offer.requestId,
					contentSha256: offer.contentSha256,
					channelDecision: decision,
					at: new Date().toISOString(),
				});
				return decision;
			}
			await wait(pollMs);
		}
	};
}
