import { unlinkSync } from "node:fs";
import net from "node:net";
import { DEFAULT_LOCALE, type AppLocale } from "../shared/ui-locale";
import {
	buildHumanDecisionResult,
	createHumanAuthoritySecrets,
	decisionFromDialogIndex,
	encodeFrame,
	offerDialogOptions,
	parseHumanDecisionOffer,
	performDesktopHandshake,
	splitFrames,
	type HumanAuthoritySecrets,
	type HumanDecisionChoice,
	type HumanDecisionOffer,
} from "./human-authority";

export type HumanDecisionDialog = (options: ReturnType<typeof offerDialogOptions>, signal?: AbortSignal) => Promise<{ response: number }>;
type DecisionPresentation = {
	getLocale: () => Promise<AppLocale>;
	reviewOffer: (offer: HumanDecisionOffer, locale: AppLocale, signal?: AbortSignal) => Promise<boolean>;
};

export type HumanAuthorityHost = {
	secrets: HumanAuthoritySecrets;
	bootstrapLine: string;
	dispose: () => void;
};

export async function startHumanAuthorityHost(input: {
	browserRuntimeToken: string;
	showDialog: HumanDecisionDialog;
	decide?: (offer: HumanDecisionOffer) => Promise<HumanDecisionChoice>;
	presentation?: DecisionPresentation;
	bootstrapLine: (secrets: HumanAuthoritySecrets) => string;
	log?: (message: string) => void;
}): Promise<HumanAuthorityHost> {
	const secrets = createHumanAuthoritySecrets(input.browserRuntimeToken);
	const sockets = new Set<net.Socket>();
 let active: net.Socket | undefined;
 const server = net.createServer((socket) => {
  sockets.add(socket);
  socket.once("close", () => sockets.delete(socket));
  void handleHumanAuthoritySocket(socket, secrets, input.showDialog, input.decide, input.log, input.presentation, () => {
   if (active && active !== socket) active.destroy();
   active = socket;
  });
 });
	await new Promise<void>((resolve, reject) => {
		server.once("error", reject);
		server.listen(secrets.humanAuthorityEndpoint, () => resolve());
	});
	return {
		secrets,
		bootstrapLine: input.bootstrapLine(secrets),
		dispose: () => {
			for (const socket of sockets) socket.destroy();
			server.close();
			if (process.platform !== "win32") {
				try {
					unlinkSync(secrets.humanAuthorityEndpoint);
				} catch {
					// the socket file is gone or was never created
				}
			}
		},
	};
}

async function handleHumanAuthoritySocket(
	socket: net.Socket,
	secrets: HumanAuthoritySecrets,
	showDialog: HumanDecisionDialog,
	decide: ((offer: HumanDecisionOffer) => Promise<HumanDecisionChoice>) | undefined,
	log?: (message: string) => void,
	presentation?: DecisionPresentation,
 authenticated?: () => void,
): Promise<void> {
	let buffer: Buffer<ArrayBufferLike> = Buffer.alloc(0);
	const frames: Buffer[] = [];
	let notify: (() => void) | undefined;
	let protocolError: Error | undefined;
 let pending: AbortController | undefined;
 socket.on("close", () => { pending?.abort(); protocolError ??= new Error("human-authority socket closed"); notify?.(); });
 socket.on("end", () => socket.destroy());
	const failSocket = (err: unknown) => {
		protocolError = err instanceof Error ? err : new Error(String(err));
		socket.destroy();
		notify?.();
	};
	socket.on("error", (err) => failSocket(err));
	socket.on("data", (chunk) => {
		try {
			buffer = Buffer.concat([buffer, chunk]);
			const split = splitFrames(buffer);
			buffer = split.rest;
			frames.push(...split.frames);
			notify?.();
		} catch (err) {
			failSocket(err);
		}
	});
	const readFrame = () =>
		new Promise<Buffer>((resolve, reject) => {
			const tryRead = () => {
				if (protocolError) {
					reject(protocolError);
					return;
				}
				const next = frames.shift();
				if (next) {
					resolve(next);
					return;
				}
				if (socket.destroyed) {
					reject(new Error("human-authority socket closed"));
					return;
				}
				notify = tryRead;
			};
			tryRead();
		});
	const send = async (value: unknown) => {
		await new Promise<void>((resolve, reject) => {
			socket.write(encodeFrame(value), (err) => (err ? reject(err) : resolve()));
		});
	};
	try {
		await performDesktopHandshake(send, readFrame, secrets.humanAuthorityToken, secrets.desktopRunId);
 authenticated?.();
		for (;;) {
			const offer = parseHumanDecisionOffer(await readFrame());
			if (offer.desktopRunId !== secrets.desktopRunId) {
				throw new Error("human decision offer belongs to another desktop run");
			}
			const controller = new AbortController();
   pending = controller;
   const expiresAt = Date.parse(offer.expiresAt);
   if (!Number.isFinite(expiresAt) || expiresAt <= Date.now()) throw new Error("human decision offer expired");
   const timer = setTimeout(() => { controller.abort(); socket.destroy(); }, Math.min(expiresAt - Date.now(), 2147483647));
   try {
    const decision = decide ? await decide(offer) : await decideOffer(offer, showDialog, presentation, controller.signal);
    if (controller.signal.aborted || socket.destroyed || Date.now() >= expiresAt) throw new Error("human decision display is no longer current");
    await send(buildHumanDecisionResult(offer, decision));
   } finally { clearTimeout(timer); controller.abort(); pending = undefined; }
		}
	} catch {
		log?.("human authority channel closed");
		socket.destroy();
	}
}

async function decideOffer(
	offer: HumanDecisionOffer,
	showDialog: HumanDecisionDialog,
	presentation?: DecisionPresentation,
 signal?: AbortSignal,
): Promise<HumanDecisionChoice> {
	try {
		const locale = presentation ? await presentation.getLocale() : DEFAULT_LOCALE;
		if (signal?.aborted) return "LATER";
  if (presentation && !(await presentation.reviewOffer(offer, locale, signal))) return "LATER";
  if (signal?.aborted) return "LATER";
		const result = await showDialog(offerDialogOptions(offer, locale, !!presentation), signal);
		return decisionFromDialogIndex(result.response);
	} catch (error) {
		// A failed dialog must never look like a human decision; record why it
		// could not be shown instead of deciding silently.
		console.error(`[human-authority] decision dialog failed: ${error instanceof Error ? error.message : String(error)}`);
		return "LATER";
	}
}
