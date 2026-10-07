// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
	HUMAN_AUTHORITY_HMAC_DAEMON,
	HUMAN_AUTHORITY_HMAC_DESKTOP,
	buildDesktopBootstrapLine,
	buildHumanDecisionResult,
	createHumanAuthoritySecrets,
	decisionFromDialogIndex,
	encodeFrame,
	handshakeProof,
	newUnpaddedToken,
	offerDialogOptions,
	parseExactObject,
	parseHumanDecisionOffer,
	splitFrames,
} from "./human-authority";

describe("human authority protocol", () => {
	it("builds a strict v1 bootstrap payload without padding", () => {
		const secrets = createHumanAuthoritySecrets("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA");
		const line = buildDesktopBootstrapLine(secrets);
		const parsed = JSON.parse(line) as Record<string, unknown>;
		expect(Object.keys(parsed)).toEqual([
			"schemaVersion",
			"browserRuntimeToken",
			"humanAuthorityToken",
			"humanAuthorityEndpoint",
			"desktopRunId",
		]);
		expect(parsed.schemaVersion).toBe(1);
		expect(secrets.humanAuthorityToken).not.toContain("+");
		expect(secrets.humanAuthorityToken).not.toContain("/");
		expect(secrets.humanAuthorityToken).not.toContain("=");
		expect(secrets.humanAuthorityToken).not.toBe(secrets.browserRuntimeToken);
		expect(secrets.desktopRunId.startsWith("deskrun-")).toBe(true);
	});

	it("uses a named pipe on Windows and a short unix socket elsewhere", () => {
		const windows = createHumanAuthoritySecrets("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "win32", 12);
		expect(windows.humanAuthorityEndpoint.startsWith("\\\\.\\pipe\\ao-human-")).toBe(true);
		const unix = createHumanAuthoritySecrets("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "darwin", 12);
		expect(unix.humanAuthorityEndpoint.startsWith("/tmp/ao-hum-12-")).toBe(true);
		expect(unix.humanAuthorityEndpoint.endsWith(".sock")).toBe(true);
		expect(unix.humanAuthorityEndpoint.length).toBeLessThan(80);
	});

	it("round-trips frames and maps dialog buttons", () => {
		const encoded = encodeFrame({ kind: "HUMAN_DECISION_OFFER", n: 1 });
		const split = splitFrames(encoded);
		expect(split.frames).toHaveLength(1);
		expect(split.rest).toHaveLength(0);
		expect(decisionFromDialogIndex(0)).toBe("APPROVE");
		expect(decisionFromDialogIndex(1)).toBe("REJECT");
		expect(decisionFromDialogIndex(2)).toBe("LATER");
		expect(decisionFromDialogIndex(-1)).toBe("LATER");
	});

	it("parses a generic offer and echoes it in the result without inspecting the kind", () => {
		const offerRaw = {
			protocolVersion: 1,
			kind: "HUMAN_DECISION_OFFER",
			desktopRunId: "deskrun-1",
			requestId: "req-1",
			decisionKind: "TEST_SECOND_KIND",
			bindingSchemaVersion: 1,
			binding: { id: "object-1" },
			contentSha256: "a".repeat(64),
			nonce: newUnpaddedToken(),
			expiresAt: "2026-08-25T04:00:00Z",
			display: {
				title: "Second kind",
				summary: "A test-only kind.",
				fullContent: "body",
				changeSummary: "none",
			},
		};
		const offer = parseHumanDecisionOffer(JSON.stringify(offerRaw));
		const dialog = offerDialogOptions(offer);
		expect(dialog.buttons).toEqual(["Approve", "Reject", "Later"]);
		expect(dialog.cancelId).toBe(2);
		expect(dialog.detail).toContain(offer.contentSha256);
		const result = buildHumanDecisionResult(offer, "APPROVE");
		expect(result.decisionKind).toBe("TEST_SECOND_KIND");
		expect(result.binding).toEqual({ id: "object-1" });
		expect(result.nonce).toBe(offer.nonce);
	});

	it("rejects extra offer fields", () => {
		expect(() =>
			parseHumanDecisionOffer(
				JSON.stringify({
					protocolVersion: 1,
					kind: "HUMAN_DECISION_OFFER",
					desktopRunId: "deskrun-1",
					requestId: "req-1",
					decisionKind: "CONFIRM_REQUIREMENT_VERSION",
					bindingSchemaVersion: 1,
					binding: {},
					contentSha256: "a".repeat(64),
					nonce: newUnpaddedToken(),
					expiresAt: "2026-08-25T04:00:00Z",
					display: { title: "t", summary: "s", fullContent: "f", changeSummary: "c" },
					extra: true,
				}),
			),
		).toThrow(/extra fields/);
	});

	it("rejects duplicate JSON keys in offers, display, binding, and handshake messages", () => {
		const nonce = newUnpaddedToken();
		const digest = "a".repeat(64);
		expect(() =>
			parseHumanDecisionOffer(`{
				"protocolVersion": 1,
				"kind": "HUMAN_DECISION_OFFER",
				"kind": "HUMAN_DECISION_OFFER",
				"desktopRunId": "deskrun-1",
				"requestId": "req-1",
				"decisionKind": "CONFIRM_REQUIREMENT_VERSION",
				"bindingSchemaVersion": 1,
				"binding": {"id": "object-1"},
				"contentSha256": "${digest}",
				"nonce": ${JSON.stringify(nonce)},
				"expiresAt": "2026-08-25T04:00:00Z",
				"display": {"title": "t", "summary": "s", "fullContent": "f", "changeSummary": "c"}
			}`),
		).toThrow(/repeated/);
		expect(() =>
			parseHumanDecisionOffer(`{
				"protocolVersion": 1,
				"kind": "HUMAN_DECISION_OFFER",
				"desktopRunId": "deskrun-1",
				"requestId": "req-1",
				"decisionKind": "CONFIRM_REQUIREMENT_VERSION",
				"bindingSchemaVersion": 1,
				"binding": {"id": "object-1"},
				"contentSha256": "${digest}",
				"nonce": ${JSON.stringify(nonce)},
				"expiresAt": "2026-08-25T04:00:00Z",
				"display": {"title": "t", "title": "other", "summary": "s", "fullContent": "f", "changeSummary": "c"}
			}`),
		).toThrow(/repeated/);
		expect(() =>
			parseHumanDecisionOffer(`{
				"protocolVersion": 1,
				"kind": "HUMAN_DECISION_OFFER",
				"desktopRunId": "deskrun-1",
				"requestId": "req-1",
				"decisionKind": "CONFIRM_REQUIREMENT_VERSION",
				"bindingSchemaVersion": 1,
				"binding": {"id": "object-1", "id": "object-2"},
				"contentSha256": "${digest}",
				"nonce": ${JSON.stringify(nonce)},
				"expiresAt": "2026-08-25T04:00:00Z",
				"display": {"title": "t", "summary": "s", "fullContent": "f", "changeSummary": "c"}
			}`),
		).toThrow(/repeated/);
		expect(() =>
			parseExactObject(
				`{"protocolVersion":1,"kind":"DAEMON_HELLO","kind":"DAEMON_HELLO","desktopRunId":"deskrun-1","daemonChallenge":"x","proof":"y"}`,
				["protocolVersion", "kind", "desktopRunId", "daemonChallenge", "proof"],
			),
		).toThrow(/repeated/);
	});

	it("rejects __proto__ extra fields in offers, display, binding, and handshake messages", () => {
		const nonce = newUnpaddedToken();
		const digest = "a".repeat(64);
		expect(() =>
			parseHumanDecisionOffer(`{
				"protocolVersion": 1,
				"kind": "HUMAN_DECISION_OFFER",
				"desktopRunId": "deskrun-1",
				"requestId": "req-1",
				"decisionKind": "CONFIRM_REQUIREMENT_VERSION",
				"bindingSchemaVersion": 1,
				"binding": {"id": "object-1"},
				"contentSha256": "${digest}",
				"nonce": ${JSON.stringify(nonce)},
				"expiresAt": "2026-08-25T04:00:00Z",
				"display": {"title": "t", "summary": "s", "fullContent": "f", "changeSummary": "c"},
				"__proto__": {"injected": true}
			}`),
		).toThrow(/__proto__/);
		expect(() =>
			parseHumanDecisionOffer(`{
				"protocolVersion": 1,
				"kind": "HUMAN_DECISION_OFFER",
				"desktopRunId": "deskrun-1",
				"requestId": "req-1",
				"decisionKind": "CONFIRM_REQUIREMENT_VERSION",
				"bindingSchemaVersion": 1,
				"binding": {"id": "object-1"},
				"contentSha256": "${digest}",
				"nonce": ${JSON.stringify(nonce)},
				"expiresAt": "2026-08-25T04:00:00Z",
				"display": {"title": "t", "summary": "s", "fullContent": "f", "changeSummary": "c", "__proto__": {"injected": true}}
			}`),
		).toThrow(/__proto__/);
		expect(() =>
			parseHumanDecisionOffer(`{
				"protocolVersion": 1,
				"kind": "HUMAN_DECISION_OFFER",
				"desktopRunId": "deskrun-1",
				"requestId": "req-1",
				"decisionKind": "CONFIRM_REQUIREMENT_VERSION",
				"bindingSchemaVersion": 1,
				"binding": {"id": "object-1", "__proto__": {"injected": true}},
				"contentSha256": "${digest}",
				"nonce": ${JSON.stringify(nonce)},
				"expiresAt": "2026-08-25T04:00:00Z",
				"display": {"title": "t", "summary": "s", "fullContent": "f", "changeSummary": "c"}
			}`),
		).toThrow(/__proto__/);
		expect(() =>
			parseExactObject(
				`{"protocolVersion":1,"kind":"DAEMON_HELLO","desktopRunId":"deskrun-1","daemonChallenge":"x","proof":"y","__proto__":{"injected":true}}`,
				["protocolVersion", "kind", "desktopRunId", "daemonChallenge", "proof"],
			),
		).toThrow(/__proto__/);
	});

	it("computes the frozen HMAC vector", () => {
		const token = Buffer.from(Array.from({ length: 32 }, (_, i) => i + 1)).toString("base64url");
		const desktop = Buffer.from(Array.from({ length: 32 }, (_, i) => i + 33)).toString("base64url");
		const daemon = Buffer.from(Array.from({ length: 32 }, (_, i) => i + 65)).toString("base64url");
		const proof = handshakeProof(token, HUMAN_AUTHORITY_HMAC_DAEMON, "deskrun-1", desktop, daemon);
		const desktopProof = handshakeProof(token, HUMAN_AUTHORITY_HMAC_DESKTOP, "deskrun-1", desktop, daemon);
		expect(proof).toBe("sPxNNJbu5wgQtYCv54T_T3z2yt8fESdd2tZhR05B8J4");
		expect(desktopProof).toBe("DlCYSK4gKczc1u1La3z5lWvvZvrV8SJPMJMp3khcxC4");
	});
});

describe("readable native confirmation", () => {
	it("uses Chinese labels and a short final confirmation without altering the offer", () => {
		const offer = parseHumanDecisionOffer(JSON.stringify({ protocolVersion: 1, kind: "HUMAN_DECISION_OFFER", desktopRunId: "fixture-desktop", requestId: "fixture-request", decisionKind: "CONFIRM_REQUIREMENT_VERSION", bindingSchemaVersion: 1, binding: { version: 4 }, contentSha256: "d".repeat(64), nonce: "fixture-nonce", expiresAt: "2099-01-01T00:00:00Z", display: { title: "Confirm requirement version v4", summary: "测试需求 version 4 is waiting for confirmation.", fullContent: '{"summary":"完整测试内容"}', changeSummary: "原始差异" } }));
		const before = structuredClone(offer);
		const options = offerDialogOptions(offer, "zh-CN", true);
		expect(options.buttons).toEqual(["批准", "拒绝", "稍后决定"]);
		expect(options.defaultId).toBe(2); expect(options.cancelId).toBe(2);
		expect(options.title).toBe("确认需求版本");
		expect(options.detail).toContain("测试需求 · 需求版本 4");
		expect(options.detail).toContain(offer.contentSha256);
		expect(options.detail).not.toContain(offer.display.fullContent);
		expect(offer).toEqual(before);
	});
});
