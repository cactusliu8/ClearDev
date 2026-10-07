import { mkdir, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";

export const DESKTOP_STARTUP_SCHEMA_VERSION = 1;

export type DesktopStartupStage =
	| "APP_READY"
	| "WINDOW_STARTING"
	| "WINDOW_READY"
	| "DAEMON_STARTING"
	| "HUMAN_AUTHORITY_STARTING"
	| "HUMAN_AUTHORITY_READY"
	| "DAEMON_STARTED"
	| "DAEMON_READY"
	| "FAILED";

export type DesktopStartupFailureInjection =
	| "window"
	| "human_authority"
	| "daemon_process"
	| "health_check";

export type DesktopStartupEvent = {
	timestamp: string;
	stage: DesktopStartupStage;
	code?: string;
	reason?: string;
	details?: Record<string, string | number | boolean>;
};

export type DesktopStartupFailure = {
	stage: DesktopStartupStage;
	code: string;
	message: string;
	reason: string;
};

export type DesktopStartupRecord = {
	schemaVersion: typeof DESKTOP_STARTUP_SCHEMA_VERSION;
	appRunId: string;
	pid: number;
	startedAt: string;
	updatedAt: string;
	stage: DesktopStartupStage;
	lastSuccessfulStage?: DesktopStartupStage;
	events: DesktopStartupEvent[];
	failure?: DesktopStartupFailure;
};

type DesktopStartupRecorderOptions = {
	filePath: string;
	appRunId: string;
	pid?: number;
	now?: () => Date;
};

export class DesktopStartupError extends Error {
	readonly stage: DesktopStartupStage;
	readonly code: string;
	readonly reason: string;

	constructor(stage: DesktopStartupStage, code: string, message: string, cause?: unknown) {
		super(message, cause === undefined ? undefined : { cause });
		this.name = "DesktopStartupError";
		this.stage = stage;
		this.code = code;
		this.reason = fullErrorReason(cause ?? this);
	}
}

export class DesktopStartupRecorder {
	private readonly now: () => Date;
	private readonly filePath: string;
	private readonly temporaryPath: string;
	private readonly record: DesktopStartupRecord;
	private writeQueue: Promise<void> = Promise.resolve();

	constructor(options: DesktopStartupRecorderOptions) {
		this.now = options.now ?? (() => new Date());
		this.filePath = options.filePath;
		const pid = options.pid ?? process.pid;
		this.temporaryPath = `${options.filePath}.${pid}.tmp`;
		const startedAt = this.now().toISOString();
		this.record = {
			schemaVersion: DESKTOP_STARTUP_SCHEMA_VERSION,
			appRunId: options.appRunId,
			pid,
			startedAt,
			updatedAt: startedAt,
			stage: "APP_READY",
			events: [],
		};
	}

	stage(stage: Exclude<DesktopStartupStage, "FAILED">, details?: DesktopStartupEvent["details"]): Promise<void> {
		return this.enqueue(() => {
			const timestamp = this.now().toISOString();
			this.record.stage = stage;
			this.record.lastSuccessfulStage = stage;
			this.record.updatedAt = timestamp;
			this.record.events.push({ timestamp, stage, ...(details ? { details } : {}) });
		});
	}

	fail(error: DesktopStartupError): Promise<void> {
		return this.enqueue(() => {
			const timestamp = this.now().toISOString();
			const failure: DesktopStartupFailure = {
				stage: error.stage,
				code: error.code,
				message: error.message,
				reason: error.reason,
			};
			this.record.stage = "FAILED";
			this.record.updatedAt = timestamp;
			this.record.failure = failure;
			this.record.events.push({
				timestamp,
				stage: "FAILED",
				code: error.code,
				reason: error.reason,
				details: { failedStage: error.stage },
			});
		});
	}

	flush(): Promise<void> {
		return this.writeQueue;
	}

	snapshot(): DesktopStartupRecord {
		return structuredClone(this.record);
	}

	private enqueue(update: () => void): Promise<void> {
		this.writeQueue = this.writeQueue.then(async () => {
			update();
			await mkdir(path.dirname(this.filePath), { recursive: true, mode: 0o750 });
			const contents = `${JSON.stringify(this.record, null, 2)}\n`;
			try {
				await writeFile(this.temporaryPath, contents, { encoding: "utf8", mode: 0o600 });
				await rename(this.temporaryPath, this.filePath);
			} catch (error) {
				await rm(this.temporaryPath, { force: true }).catch(() => undefined);
				throw error;
			}
		});
		return this.writeQueue;
	}
}

export function asDesktopStartupError(
	error: unknown,
	stage: DesktopStartupStage,
	code: string,
	message: string,
): DesktopStartupError {
	if (error instanceof DesktopStartupError) return error;
	const suffix = error instanceof Error ? error.message : String(error);
	return new DesktopStartupError(stage, code, `${message}: ${suffix}`, error);
}

export function throwInjectedStartupFailure(
	environment: NodeJS.ProcessEnv,
	injection: DesktopStartupFailureInjection,
	stage: DesktopStartupStage,
): void {
	if (environment.AO_STARTUP_FAILURE_INJECTION?.trim() !== injection) return;
	throw new DesktopStartupError(
		stage,
		`injected_${injection}_failure`,
		`Injected desktop startup failure at ${injection}`,
	);
}

function fullErrorReason(error: unknown): string {
	if (error instanceof Error) {
		const own = error.stack?.trim() || `${error.name}: ${error.message}`;
		const cause = error.cause;
		if (cause === undefined || cause === error) return own;
		return `${own}\nCaused by: ${fullErrorReason(cause)}`;
	}
	return String(error);
}
