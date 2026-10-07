// @vitest-environment node
import net from "node:net";
import { describe, expect, it, vi } from "vitest";
import {
	HUMAN_AUTHORITY_HMAC_DAEMON,
	HUMAN_AUTHORITY_HMAC_DESKTOP,
	HUMAN_AUTHORITY_MAX_FRAME_BYTES,
	encodeFrame,
	handshakeProof,
	newUnpaddedToken,
	parseExactObject,
	splitFrames,
} from "./human-authority";
import { startHumanAuthorityHost } from "./human-authority-host";

describe("human authority host", () => {
	it("destroys illegal frames and duplicate-key offers without showing a dialog", async () => {
		let dialogs = 0;
		const host = await startHumanAuthorityHost({
			browserRuntimeToken: "A".repeat(43),
			showDialog: async () => {
				dialogs += 1;
				return { response: 0 };
			},
			bootstrapLine: () => "",
		});
		try {
			await writeIllegalFrame(host.secrets.humanAuthorityEndpoint, HUMAN_AUTHORITY_MAX_FRAME_BYTES + 1);
			expect(dialogs).toBe(0);
			await writeIllegalFrame(host.secrets.humanAuthorityEndpoint, 0);
			expect(dialogs).toBe(0);

			const first = await connectHost(host.secrets.humanAuthorityEndpoint);
			try {
				await performDaemonHandshake(first, host.secrets.humanAuthorityToken, host.secrets.desktopRunId);
			} finally {
				first.socket.destroy();
			}
			expect(dialogs).toBe(0);

			const second = await connectHost(host.secrets.humanAuthorityEndpoint);
			try {
				await performDaemonHandshake(second, host.secrets.humanAuthorityToken, host.secrets.desktopRunId);
				const nonce = newUnpaddedToken();
				second.socket.write(
					encodeRawJSON(`{
						"protocolVersion": 1,
						"kind": "HUMAN_DECISION_OFFER",
						"kind": "HUMAN_DECISION_OFFER",
						"desktopRunId": ${JSON.stringify(host.secrets.desktopRunId)},
						"requestId": "req-1",
						"decisionKind": "CONFIRM_REQUIREMENT_VERSION",
						"bindingSchemaVersion": 1,
						"binding": {"id": "object-1"},
						"contentSha256": "${"a".repeat(64)}",
						"nonce": ${JSON.stringify(nonce)},
						"expiresAt": "2026-08-25T04:00:00Z",
						"display": {"title": "t", "summary": "s", "fullContent": "f", "changeSummary": "c"}
					}`),
				);
				await closed(second.socket);
			} finally {
				second.socket.destroy();
			}
			expect(dialogs).toBe(0);

			const third = await connectHost(host.secrets.humanAuthorityEndpoint);
			try {
				await performDaemonHandshake(third, host.secrets.humanAuthorityToken, host.secrets.desktopRunId);
			} finally {
				third.socket.destroy();
			}
			expect(dialogs).toBe(0);
		} finally {
			host.dispose();
		}
	}, 15_000);

	it("destroys an offer with a __proto__ extra field without showing a dialog", async () => {
		let dialogs = 0;
		const host = await startHumanAuthorityHost({
			browserRuntimeToken: "A".repeat(43),
			showDialog: async () => {
				dialogs += 1;
				return { response: 0 };
			},
			bootstrapLine: () => "",
		});
		try {
			const poisoned = await connectHost(host.secrets.humanAuthorityEndpoint);
			try {
				await performDaemonHandshake(poisoned, host.secrets.humanAuthorityToken, host.secrets.desktopRunId);
				const nonce = newUnpaddedToken();
				poisoned.socket.write(
					encodeRawJSON(`{
						"protocolVersion": 1,
						"kind": "HUMAN_DECISION_OFFER",
						"desktopRunId": ${JSON.stringify(host.secrets.desktopRunId)},
						"requestId": "req-1",
						"decisionKind": "CONFIRM_REQUIREMENT_VERSION",
						"bindingSchemaVersion": 1,
						"binding": {"id": "object-1"},
						"contentSha256": "${"a".repeat(64)}",
						"nonce": ${JSON.stringify(nonce)},
						"expiresAt": "2026-08-25T04:00:00Z",
						"display": {"title": "t", "summary": "s", "fullContent": "f", "changeSummary": "c"},
						"__proto__": {"injected": true}
					}`),
				);
				await closed(poisoned.socket);
			} finally {
				poisoned.socket.destroy();
			}
			expect(dialogs).toBe(0);

			const next = await connectHost(host.secrets.humanAuthorityEndpoint);
			try {
				await performDaemonHandshake(next, host.secrets.humanAuthorityToken, host.secrets.desktopRunId);
			} finally {
				next.socket.destroy();
			}
			expect(dialogs).toBe(0);
		} finally {
			host.dispose();
		}
	}, 15_000);
});

async function writeIllegalFrame(endpoint: string, size: number): Promise<void> {
	const { socket } = await connectHost(endpoint);
	const header = Buffer.alloc(4);
	header.writeUInt32BE(size, 0);
	socket.write(header);
	await closed(socket);
}

async function performDaemonHandshake(
	conn: ConnectedHost,
	token: string,
	desktopRunId: string,
): Promise<void> {
	const hello = parseExactObject(await conn.read(), ["protocolVersion", "kind", "desktopRunId", "desktopChallenge"]);
	if (hello.kind !== "DESKTOP_HELLO") {
		throw new Error(`expected DESKTOP_HELLO, got ${String(hello.kind)}`);
	}
	const daemonChallenge = newUnpaddedToken();
	await writeValue(conn.socket, {
		protocolVersion: 1,
		kind: "DAEMON_HELLO",
		desktopRunId,
		daemonChallenge,
		proof: handshakeProof(token, HUMAN_AUTHORITY_HMAC_DAEMON, desktopRunId, String(hello.desktopChallenge), daemonChallenge),
	});
	const proof = parseExactObject(await conn.read(), ["protocolVersion", "kind", "desktopRunId", "proof"]);
	const want = handshakeProof(
		token,
		HUMAN_AUTHORITY_HMAC_DESKTOP,
		desktopRunId,
		String(hello.desktopChallenge),
		daemonChallenge,
	);
	if (proof.proof !== want) {
		throw new Error("desktop proof is invalid");
	}
}

type ConnectedHost = {
	socket: net.Socket;
	read: () => Promise<Buffer>;
};

function connectHost(endpoint: string): Promise<ConnectedHost> {
	return new Promise((resolve, reject) => {
		const socket = net.connect(endpoint);
		const read = readFrames(socket);
		socket.once("error", reject);
		socket.once("connect", () => resolve({ socket, read }));
	});
}

function closed(socket: net.Socket): Promise<void> {
	return new Promise((resolve) => {
		if (socket.destroyed) {
			resolve();
			return;
		}
		socket.once("close", () => resolve());
	});
}

function writeValue(socket: net.Socket, value: unknown): Promise<void> {
	return new Promise((resolve, reject) => {
		socket.write(encodeFrame(value), (err) => (err ? reject(err) : resolve()));
	});
}

function encodeRawJSON(body: string): Buffer {
	const payload = Buffer.from(body, "utf8");
	const header = Buffer.alloc(4);
	header.writeUInt32BE(payload.length, 0);
	return Buffer.concat([header, payload]);
}

function readFrames(socket: net.Socket): () => Promise<Buffer> {
	let buffer: Buffer<ArrayBufferLike> = Buffer.alloc(0);
	const frames: Buffer[] = [];
	let notify: (() => void) | undefined;
	socket.on("data", (chunk) => {
		buffer = Buffer.concat([buffer, chunk]);
		const split = splitFrames(buffer);
		buffer = split.rest;
		frames.push(...split.frames);
		notify?.();
	});
	socket.on("close", () => notify?.());
	return () =>
		new Promise<Buffer>((resolve, reject) => {
			const tryRead = () => {
				const next = frames.shift();
				if (next) {
					resolve(next);
					return;
				}
				if (socket.destroyed) {
					reject(new Error("socket closed"));
					return;
				}
				notify = tryRead;
			};
			tryRead();
		});
}

describe("human authority readable presentation", () => {
	it("requires owned reading before native confirmation and preserves the original result envelope", async () => {
		for (const outcome of ["continue", "close", "error", "locale-error"] as const) {
			let dialogs = 0, reads = 0;
			const host = await startHumanAuthorityHost({
				browserRuntimeToken: "A".repeat(43), bootstrapLine: () => "",
				presentation: { getLocale: async () => { if (outcome === "locale-error") throw new Error("fixture locale failed"); return "zh-CN"; }, reviewOffer: async (_offer, locale) => {
					reads++; expect(locale).toBe("zh-CN");
					if (outcome === "error") throw new Error("fixture reader failed");
					return outcome === "continue";
				} },
				showDialog: async (options) => {
					dialogs++; expect(options.buttons).toEqual(["批准", "拒绝", "稍后决定"]);
					expect(options.detail).not.toContain("raw fixture document");
					return { response: 0 }; // Explicit fake dialog; never an actual human decision.
				},
			});
			let conn: ConnectedHost | undefined;
			try {
				conn = await connectHost(host.secrets.humanAuthorityEndpoint);
				await performDaemonHandshake(conn, host.secrets.humanAuthorityToken, host.secrets.desktopRunId);
				const fixture = { protocolVersion: 1, kind: "HUMAN_DECISION_OFFER", desktopRunId: host.secrets.desktopRunId, requestId: "readable-fixture", decisionKind: "CONFIRM_REQUIREMENT_VERSION", bindingSchemaVersion: 1, binding: { id: "exact-fixture", version: 3 }, contentSha256: "c".repeat(64), nonce: newUnpaddedToken(), expiresAt: "2099-01-01T00:00:00Z", display: { title: "Confirm requirement version v3", summary: "fixture only", fullContent: "raw fixture document", changeSummary: "original hash" } };
				await writeValue(conn.socket, fixture);
				const result = JSON.parse((await conn.read()).toString());
				expect(reads).toBe(outcome === "locale-error" ? 0 : 1); expect(dialogs).toBe(outcome === "continue" ? 1 : 0);
				expect(result).toEqual({ protocolVersion: fixture.protocolVersion, kind: "HUMAN_DECISION_RESULT", desktopRunId: fixture.desktopRunId, requestId: fixture.requestId, decisionKind: fixture.decisionKind, bindingSchemaVersion: fixture.bindingSchemaVersion, binding: fixture.binding, contentSha256: fixture.contentSha256, nonce: fixture.nonce, decision: outcome === "continue" ? "APPROVE" : "LATER" });
			} finally { conn?.socket.destroy(); host.dispose(); }
		}
	});
});

describe("cancelled private confirmation offers", () => {
 it.each(["disconnect", "expire", "dispose", "replace"] as const)("cancels an open native dialog on %s and discards a late approval", async (action) => {
  let observed: AbortSignal | undefined; let dialogs = 0;
  const host = await startHumanAuthorityHost({
   browserRuntimeToken: "A".repeat(43), bootstrapLine: () => "",
   showDialog: async (_options, signal) => {
    observed = signal; dialogs++;
    return await new Promise<{response:number}>((resolve) => signal!.addEventListener("abort", () => resolve({response:0}), {once:true}));
   },
  });
  const first = await connectHost(host.secrets.humanAuthorityEndpoint);
  let second: Awaited<ReturnType<typeof connectHost>> | undefined;
  try {
   await performDaemonHandshake(first, host.secrets.humanAuthorityToken, host.secrets.desktopRunId);
   const expiresAt = action === "expire" ? new Date(Date.now() + 300).toISOString() : "2099-01-01T00:00:00Z";
   first.socket.write(encodeFrame({protocolVersion:1,kind:"HUMAN_DECISION_OFFER",desktopRunId:host.secrets.desktopRunId,requestId:"original",decisionKind:"CONFIRM_REQUIREMENT_VERSION",bindingSchemaVersion:1,binding:{id:"original"},contentSha256:"a".repeat(64),nonce:newUnpaddedToken(),expiresAt,display:{title:"original",summary:"same",fullContent:"same",changeSummary:"same"}}));
   await vi.waitFor(() => expect(dialogs).toBe(1));
   let replies = 0; first.socket.on("data", () => replies++);
   if (action === "disconnect") first.socket.destroy();
   if (action === "dispose") host.dispose();
   if (action === "replace") { second = await connectHost(host.secrets.humanAuthorityEndpoint); await performDaemonHandshake(second, host.secrets.humanAuthorityToken, host.secrets.desktopRunId); }
   await vi.waitFor(() => expect(observed?.aborted).toBe(true));
   await vi.waitFor(() => expect(first.socket.destroyed).toBe(true));
   expect(replies).toBe(0); expect(dialogs).toBe(1);
  } finally { first.socket.destroy(); second?.socket.destroy(); host.dispose(); }
 });
});
