import { describe, expect, it, vi } from "vitest";
import { isAllowedAppExternalURL, openAllowedAppExternalURL, launchDesktopApplication } from "./external-open";

describe("isAllowedAppExternalURL", () => {
	it("allows web and mail handoff URLs from the app renderer", () => {
		expect(isAllowedAppExternalURL("https://github.com/Untrivial-ai/agent-orchestrator/issues/new")).toBe(true);
		expect(isAllowedAppExternalURL("http://localhost:5173/help")).toBe(true);
		expect(isAllowedAppExternalURL("mailto:support@example.invalid?subject=AO%20feedback")).toBe(true);
	});

	it("blocks local, privileged, and script schemes", () => {
		expect(isAllowedAppExternalURL("file:///Users/alice/private.txt")).toBe(false);
		expect(isAllowedAppExternalURL("app://renderer/index.html")).toBe(false);
		expect(isAllowedAppExternalURL("javascript:alert(1)")).toBe(false);
	});

	it("opens allowed URLs through the native shell opener", async () => {
		const openExternal = vi.fn().mockResolvedValue(undefined);

		await openAllowedAppExternalURL("mailto:support@example.invalid?subject=AO%20feedback", { openExternal });

		expect(openExternal).toHaveBeenCalledWith("mailto:support@example.invalid?subject=AO%20feedback");
	});

	it("rejects unsupported URLs before reaching the shell opener", async () => {
		const openExternal = vi.fn().mockResolvedValue(undefined);

		await expect(openAllowedAppExternalURL("file:///Users/alice/private.txt", { openExternal })).rejects.toThrow(
			"Unsupported external URL",
		);
		expect(openExternal).not.toHaveBeenCalled();
	});
});

describe("desktop default opener with isolated app HOME", () => {
  it("passes the exact URL as one argument and uses desktop associations only in the child", async () => {
    const env = { HOME: "/isolated", AO_DESKTOP_HOME: "/desktop", AO_DESKTOP_CONFIG_HOME: "/settings", AO_DESKTOP_DATA_HOME: "/applications", BROWSER: "other-browser" };
    const launch = vi.fn().mockResolvedValue(undefined);
    const openExternal = vi.fn();
    const url = "http://127.0.0.1:38309/?q=$(touch%20bad)&x=1";
    await openAllowedAppExternalURL(url, { openExternal }, { platform: "linux", env, launch });
    expect(launch).toHaveBeenCalledWith("/usr/bin/xdg-open", [url], { env: { ...env, HOME: "/desktop", XDG_CONFIG_HOME: "/settings", XDG_DATA_HOME: "/applications", BROWSER: undefined } });
    expect(openExternal).not.toHaveBeenCalled();
    expect(env.HOME).toBe("/isolated");
    expect(env.BROWSER).toBe("other-browser");
  });
  it("reports launcher failures and does not fall back to the wrong environment", async () => {
    const openExternal = vi.fn();
    const launch = vi.fn().mockRejectedValue(new Error("desktop unavailable"));
    await expect(openAllowedAppExternalURL("http://localhost/", { openExternal }, { platform: "linux", env: { AO_DESKTOP_HOME: "/desktop" }, launch })).rejects.toThrow("desktop unavailable");
    expect(openExternal).not.toHaveBeenCalled();
  });
  it("keeps ordinary Linux and other platforms on the native shell", async () => {
    for (const [platform, env] of [["linux", {}], ["darwin", { AO_DESKTOP_HOME: "/desktop" }]] as const) {
      const openExternal = vi.fn(); const launch = vi.fn();
      await openAllowedAppExternalURL("http://localhost/", { openExternal }, { platform, env, launch });
      expect(openExternal).toHaveBeenCalledOnce(); expect(launch).not.toHaveBeenCalled();
    }
  });
  it("rejects unsupported URLs and malformed desktop paths before launching", async () => {
    const launch = vi.fn();const openExternal = vi.fn();
    for (const url of ["file:///tmp/a", "javascript:alert(1)"]) {
      await expect(openAllowedAppExternalURL(url, { openExternal }, { platform: "linux", env: { AO_DESKTOP_HOME: "/desktop" }, launch })).rejects.toThrow("Unsupported");
    }
    await expect(openAllowedAppExternalURL("http://localhost/", { openExternal }, { platform: "linux", env: { AO_DESKTOP_HOME: "relative" }, launch })).rejects.toThrow("Invalid desktop");
    expect(launch).not.toHaveBeenCalled();
  });
});

// Exercise an actual child whose descendant keeps inherited stdout/stderr open.
it("does not wait for the browser descendant's output handles", async () => {
  const code = `const {spawn}=require('node:child_process'); const p=spawn(process.execPath,['-e','setTimeout(()=>{},1500)'],{stdio:'inherit'}); p.unref();`;
  const started = Date.now();
  await launchDesktopApplication(process.execPath, ["-e", code], { env: process.env });
  expect(Date.now() - started).toBeLessThan(1200);
});
it("reports actual missing or failing desktop launchers", async () => {
  await expect(launchDesktopApplication("/missing-cleardev-opener", [], { env: process.env })).rejects.toThrow();
  await expect(launchDesktopApplication(process.execPath, ["-e", "process.exit(3)"], { env: process.env })).rejects.toThrow("failed (3)");
});
