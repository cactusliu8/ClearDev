import { spawn } from "node:child_process";
import path from "node:path";
// A browser may inherit the opener's output pipes and keep them open for its
// entire lifetime. Wait for xdg-open itself, with no captured browser pipes.
export function launchDesktopApplication(file: string, args: string[], options: { env: NodeJS.ProcessEnv }): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn(file, args, { ...options, stdio: "ignore" });
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0) resolve();
      else reject(new Error(`Desktop application opener failed (${signal || code})`));
    });
  });
}
type DesktopOpenOptions = {
  platform: NodeJS.Platform;
  env: NodeJS.ProcessEnv;
  launch: (file: string, args: string[], options: { env: NodeJS.ProcessEnv }) => Promise<unknown>;
};

const APP_EXTERNAL_PROTOCOLS = new Set(["http:", "https:", "mailto:"]);

export type ExternalOpener = {
	openExternal: (url: string) => Promise<unknown>;
};

export function isAllowedAppExternalURL(rawUrl: string): boolean {
	try {
		const url = new URL(rawUrl);
		return APP_EXTERNAL_PROTOCOLS.has(url.protocol);
	} catch {
		return false;
	}
}

export async function openAllowedAppExternalURL(rawUrl: string, opener: ExternalOpener, desktop: DesktopOpenOptions = {
  platform: process.platform, env: process.env, launch: launchDesktopApplication,
}): Promise<void> {
	if (!isAllowedAppExternalURL(rawUrl)) {
		throw new Error("Unsupported external URL");
	}
  if (desktop.platform === "linux" && desktop.env.AO_DESKTOP_HOME) {
    const home = desktop.env.AO_DESKTOP_HOME;
    const config = desktop.env.AO_DESKTOP_CONFIG_HOME || path.join(home, ".config");
    const data = desktop.env.AO_DESKTOP_DATA_HOME || path.join(home, ".local/share");
    if (![home, config, data].every((value) => path.isAbsolute(value))) {
      throw new Error("Invalid desktop application directories");
    }
    // Child-only environment: do not change the daemon/worker HOME or browser choice.
    const env: NodeJS.ProcessEnv = { ...desktop.env, HOME: home, XDG_CONFIG_HOME: config, XDG_DATA_HOME: data };
    delete env.BROWSER;
    await desktop.launch("/usr/bin/xdg-open", [rawUrl], { env });
    return;
  }
	await opener.openExternal(rawUrl);
}
