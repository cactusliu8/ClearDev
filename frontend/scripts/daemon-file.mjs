import { chmodSync } from "node:fs";

export function ensureDaemonExecutableMode(file, platform = process.platform) {
	if (platform !== "win32") chmodSync(file, 0o755);
}
