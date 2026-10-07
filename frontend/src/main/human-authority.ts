import { createHmac, randomBytes, randomUUID } from "node:crypto";
import { DEFAULT_LOCALE, type AppLocale } from "../shared/ui-locale";
import { decisionLabels, decisionTitle, decisionSummary } from "./human-decision-reader";

export const HUMAN_AUTHORITY_PROTOCOL_VERSION = 1;
export const HUMAN_DECISION_OFFER_KIND = "HUMAN_DECISION_OFFER";
export const HUMAN_DECISION_RESULT_KIND = "HUMAN_DECISION_RESULT";
export const HUMAN_AUTHORITY_MAX_FRAME_BYTES = 65536;
export const HUMAN_AUTHORITY_HMAC_DAEMON = "cleardev-human/daemon/v1";
export const HUMAN_AUTHORITY_HMAC_DESKTOP = "cleardev-human/desktop/v1";

export type HumanDecisionChoice = "APPROVE" | "REJECT" | "LATER";

export type HumanDecisionDisplay = {
	title: string;
	summary: string;
	fullContent: string;
	changeSummary: string;
};

export type HumanDecisionOffer = {
	protocolVersion: number;
	kind: string;
	desktopRunId: string;
	requestId: string;
	decisionKind: string;
	bindingSchemaVersion: number;
	binding: unknown;
	contentSha256: string;
	nonce: string;
	expiresAt: string;
	display: HumanDecisionDisplay;
};

export type HumanDecisionResult = {
	protocolVersion: number;
	kind: string;
	desktopRunId: string;
	requestId: string;
	decisionKind: string;
	bindingSchemaVersion: number;
	binding: unknown;
	contentSha256: string;
	nonce: string;
	decision: HumanDecisionChoice;
};

export type HumanAuthoritySecrets = {
	browserRuntimeToken: string;
	humanAuthorityToken: string;
	humanAuthorityEndpoint: string;
	desktopRunId: string;
};

export function newUnpaddedToken(): string {
	return randomBytes(32).toString("base64url");
}

export function newDesktopRunId(): string {
	return `deskrun-${randomUUID()}`;
}

export function newHumanAuthorityEndpoint(desktopRunId: string, pid = process.pid, platform = process.platform): string {
	if (platform === "win32") {
		return `\\\\.\\pipe\\ao-human-${desktopRunId}`;
	}
	return `/tmp/ao-hum-${pid}-${randomBytes(4).toString("hex")}.sock`;
}

export function createHumanAuthoritySecrets(browserRuntimeToken: string, platform = process.platform, pid = process.pid): HumanAuthoritySecrets {
	let humanAuthorityToken = newUnpaddedToken();
	while (humanAuthorityToken === browserRuntimeToken) {
		humanAuthorityToken = newUnpaddedToken();
	}
	const desktopRunId = newDesktopRunId();
	return {
		browserRuntimeToken,
		humanAuthorityToken,
		humanAuthorityEndpoint: newHumanAuthorityEndpoint(desktopRunId, pid, platform),
		desktopRunId,
	};
}

export function buildDesktopBootstrapLine(secrets: HumanAuthoritySecrets): string {
	return JSON.stringify({
		schemaVersion: 1,
		browserRuntimeToken: secrets.browserRuntimeToken,
		humanAuthorityToken: secrets.humanAuthorityToken,
		humanAuthorityEndpoint: secrets.humanAuthorityEndpoint,
		desktopRunId: secrets.desktopRunId,
	});
}

export function handshakeProof(
	token: string,
	role: string,
	desktopRunId: string,
	desktopChallenge: string,
	daemonChallenge: string,
): string {
	const mac = createHmac("sha256", Buffer.from(token, "base64url"));
	mac.update(role);
	mac.update(Buffer.from([0]));
	mac.update(desktopRunId);
	mac.update(Buffer.from([0]));
	mac.update(desktopChallenge);
	mac.update(Buffer.from([0]));
	mac.update(daemonChallenge);
	return mac.digest("base64url");
}

export function encodeFrame(value: unknown): Buffer {
	const body = Buffer.from(JSON.stringify(value), "utf8");
	if (body.length === 0 || body.length > HUMAN_AUTHORITY_MAX_FRAME_BYTES) {
		throw new Error("human-authority frame exceeds 65536 bytes");
	}
	const header = Buffer.alloc(4);
	header.writeUInt32BE(body.length, 0);
	return Buffer.concat([header, body]);
}

export function splitFrames(buffer: Buffer): { frames: Buffer[]; rest: Buffer } {
	const frames: Buffer[] = [];
	let offset = 0;
	while (offset + 4 <= buffer.length) {
		const size = buffer.readUInt32BE(offset);
		if (size === 0 || size > HUMAN_AUTHORITY_MAX_FRAME_BYTES) {
			throw new Error(`human-authority frame length ${size} is not allowed`);
		}
		if (offset + 4 + size > buffer.length) {
			break;
		}
		frames.push(buffer.subarray(offset + 4, offset + 4 + size));
		offset += 4 + size;
	}
	return { frames, rest: buffer.subarray(offset) };
}

export function parseHumanDecisionOffer(raw: Buffer | string): HumanDecisionOffer {
	const parsed = parseExactObject(raw, [
		"protocolVersion",
		"kind",
		"desktopRunId",
		"requestId",
		"decisionKind",
		"bindingSchemaVersion",
		"binding",
		"contentSha256",
		"nonce",
		"expiresAt",
		"display",
	]) as HumanDecisionOffer;
	if (parsed.protocolVersion !== HUMAN_AUTHORITY_PROTOCOL_VERSION || parsed.kind !== HUMAN_DECISION_OFFER_KIND) {
		throw new Error("human decision offer is not protocol v1");
	}
	requireExactFields(parsed.display, ["title", "summary", "fullContent", "changeSummary"]);
	return parsed;
}

export function buildHumanDecisionResult(offer: HumanDecisionOffer, decision: HumanDecisionChoice): HumanDecisionResult {
	return {
		protocolVersion: offer.protocolVersion,
		kind: HUMAN_DECISION_RESULT_KIND,
		desktopRunId: offer.desktopRunId,
		requestId: offer.requestId,
		decisionKind: offer.decisionKind,
		bindingSchemaVersion: offer.bindingSchemaVersion,
		binding: offer.binding,
		contentSha256: offer.contentSha256,
		nonce: offer.nonce,
		decision,
	};
}

export function decisionFromDialogIndex(index: number, cancelId = 2): HumanDecisionChoice {
	if (index === 0) return "APPROVE";
	if (index === 1) return "REJECT";
	if (index === cancelId || index === 2) return "LATER";
	return "LATER";
}

export function offerDialogOptions(offer: HumanDecisionOffer, locale: AppLocale = DEFAULT_LOCALE, reviewed = false): {
	type: "question";
	buttons: string[];
	defaultId: number;
	cancelId: number;
	noLink: true;
	title: string;
	message: string;
	detail: string;
} {
	const t = decisionLabels(locale);
	const title = locale === "en" && !reviewed ? offer.display.title : decisionTitle(offer, locale);
	return {
		type: "question",
		buttons: [t("approve"), t("reject"), t("later")],
		defaultId: 2,
		cancelId: 2,
		noLink: true,
		title,
		message: title,
		detail: reviewed ? [t("reviewed"), decisionSummary(offer, locale), `SHA-256 ${offer.contentSha256}`, `${t("expires")} ${offer.expiresAt}`].join("\n\n") : [
			offer.display.summary,
			"",
			offer.display.fullContent,
			"",
			offer.display.changeSummary,
			"",
			`SHA-256 ${offer.contentSha256}`,
			`Expires at ${offer.expiresAt}; a later answer needs a fresh confirmation request.`,
		].join("\n"),
	};
}

export function parseExactObject(raw: Buffer | string, fields: string[]): Record<string, unknown> {
	return requireExactFields(parseStrictJSON(typeof raw === "string" ? raw : raw.toString("utf8")), fields);
}

function requireExactFields(value: unknown, fields: string[]): Record<string, unknown> {
	if (value === null || typeof value !== "object" || Array.isArray(value)) {
		throw new Error("payload must be exactly one JSON object");
	}
	const record = value as Record<string, unknown>;
	const keys = Object.keys(record);
	if (keys.length !== fields.length) {
		throw new Error("payload has missing or extra fields");
	}
	for (const field of fields) {
		if (!Object.hasOwn(record, field) || record[field] === null) {
			throw new Error(`payload is missing field ${field}`);
		}
	}
	return record;
}

type jsonCursor = { text: string; i: number };

export function parseStrictJSON(text: string): unknown {
	const cursor: jsonCursor = { text, i: 0 };
	const value = parseJSONValue(cursor);
	skipJSONSpace(cursor);
	if (cursor.i !== cursor.text.length) {
		throw new Error("payload must contain exactly one JSON value");
	}
	return value;
}

function parseJSONValue(cursor: jsonCursor): unknown {
	skipJSONSpace(cursor);
	const ch = cursor.text[cursor.i];
	if (ch === "{") {
		return parseJSONObject(cursor);
	}
	if (ch === "[") {
		return parseJSONArray(cursor);
	}
	if (ch === '"') {
		return readJSONString(cursor);
	}
	if (ch === "t" || ch === "f") {
		return readJSONLiteral(cursor);
	}
	if (ch === "n") {
		throw new Error("JSON cannot contain null");
	}
	if (ch === "-" || (ch >= "0" && ch <= "9")) {
		return readJSONNumber(cursor);
	}
	throw new Error("payload is not valid JSON");
}

function parseJSONObject(cursor: jsonCursor): Record<string, unknown> {
	cursor.i++;
	const out = Object.create(null) as Record<string, unknown>;
	const seen = new Set<string>();
	skipJSONSpace(cursor);
	if (cursor.text[cursor.i] === "}") {
		cursor.i++;
		return out;
	}
	for (;;) {
		skipJSONSpace(cursor);
		if (cursor.text[cursor.i] !== '"') {
			throw new Error("JSON object key is not a string");
		}
		const key = readJSONString(cursor);
		if (key === "__proto__") {
			throw new Error('JSON field "__proto__" is not allowed');
		}
		if (seen.has(key)) {
			throw new Error(`JSON field ${JSON.stringify(key)} is repeated`);
		}
		seen.add(key);
		skipJSONSpace(cursor);
		if (cursor.text[cursor.i] !== ":") {
			throw new Error("JSON object is missing a colon");
		}
		cursor.i++;
		Object.defineProperty(out, key, {
			value: parseJSONValue(cursor),
			enumerable: true,
			writable: true,
			configurable: true,
		});
		skipJSONSpace(cursor);
		const next = cursor.text[cursor.i];
		if (next === ",") {
			cursor.i++;
			continue;
		}
		if (next === "}") {
			cursor.i++;
			return out;
		}
		throw new Error("unterminated JSON object");
	}
}

function parseJSONArray(cursor: jsonCursor): unknown[] {
	cursor.i++;
	const out: unknown[] = [];
	skipJSONSpace(cursor);
	if (cursor.text[cursor.i] === "]") {
		cursor.i++;
		return out;
	}
	for (;;) {
		out.push(parseJSONValue(cursor));
		skipJSONSpace(cursor);
		const next = cursor.text[cursor.i];
		if (next === ",") {
			cursor.i++;
			continue;
		}
		if (next === "]") {
			cursor.i++;
			return out;
		}
		throw new Error("unterminated JSON array");
	}
}

function readJSONString(cursor: jsonCursor): string {
	const start = cursor.i;
	if (cursor.text[cursor.i] !== '"') {
		throw new Error("JSON string is not quoted");
	}
	cursor.i++;
	while (cursor.i < cursor.text.length) {
		const ch = cursor.text[cursor.i];
		if (ch === "\\") {
			cursor.i += 2;
			continue;
		}
		if (ch === '"') {
			cursor.i++;
			return JSON.parse(cursor.text.slice(start, cursor.i)) as string;
		}
		cursor.i++;
	}
	throw new Error("unterminated JSON string");
}

function readJSONLiteral(cursor: jsonCursor): boolean {
	if (cursor.text.startsWith("true", cursor.i)) {
		cursor.i += 4;
		return true;
	}
	if (cursor.text.startsWith("false", cursor.i)) {
		cursor.i += 5;
		return false;
	}
	throw new Error("payload is not valid JSON");
}

function readJSONNumber(cursor: jsonCursor): number {
	const start = cursor.i;
	if (cursor.text[cursor.i] === "-") {
		cursor.i++;
	}
	while (cursor.i < cursor.text.length && /[0-9.eE+-]/.test(cursor.text[cursor.i] ?? "")) {
		cursor.i++;
	}
	const parsed = JSON.parse(cursor.text.slice(start, cursor.i)) as unknown;
	if (typeof parsed !== "number" || !Number.isFinite(parsed)) {
		throw new Error("JSON number is not allowed");
	}
	return parsed;
}

function skipJSONSpace(cursor: jsonCursor): void {
	while (cursor.i < cursor.text.length && " \t\n\r".includes(cursor.text[cursor.i] ?? "")) {
		cursor.i++;
	}
}

export async function performDesktopHandshake(
	send: (value: unknown) => Promise<void>,
	read: () => Promise<Buffer>,
	token: string,
	desktopRunId: string,
): Promise<void> {
	const desktopChallenge = newUnpaddedToken();
	await send({
		protocolVersion: HUMAN_AUTHORITY_PROTOCOL_VERSION,
		kind: "DESKTOP_HELLO",
		desktopRunId,
		desktopChallenge,
	});
	const hello = parseExactObject(await read(), [
		"protocolVersion",
		"kind",
		"desktopRunId",
		"daemonChallenge",
		"proof",
	]);
	if (hello.protocolVersion !== HUMAN_AUTHORITY_PROTOCOL_VERSION || hello.kind !== "DAEMON_HELLO") {
		throw new Error("daemon hello is not protocol v1");
	}
	if (hello.desktopRunId !== desktopRunId) {
		throw new Error("daemon hello run id does not match");
	}
	const want = handshakeProof(
		token,
		HUMAN_AUTHORITY_HMAC_DAEMON,
		desktopRunId,
		desktopChallenge,
		String(hello.daemonChallenge),
	);
	if (want !== hello.proof) {
		throw new Error("daemon proof is invalid");
	}
	await send({
		protocolVersion: HUMAN_AUTHORITY_PROTOCOL_VERSION,
		kind: "DESKTOP_PROOF",
		desktopRunId,
		proof: handshakeProof(
			token,
			HUMAN_AUTHORITY_HMAC_DESKTOP,
			desktopRunId,
			desktopChallenge,
			String(hello.daemonChallenge),
		),
	});
}
