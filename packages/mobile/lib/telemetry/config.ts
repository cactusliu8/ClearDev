// Telemetry is disabled by default in the public ClearDev snapshot.
// A build may opt in by providing its own PostHog project key.
export const MOBILE_POSTHOG_KEY =
	process.env.EXPO_PUBLIC_POSTHOG_KEY?.trim() || "";

export const MOBILE_POSTHOG_HOST =
	process.env.EXPO_PUBLIC_POSTHOG_HOST?.trim() || "https://us.i.posthog.com";

/**
 * Kill switches, mirroring the desktop's AO_TELEMETRY_DISABLED_EVENTS. Build-time
 * (EXPO_PUBLIC_* is inlined), so it controls the next build. A shipped binary
 * cannot be hotfixed from here; the runtime kill switch for an event already in
 * the field is the PostHog-side ingestion drop rule, the same lever used for the
 * legacy desktop events (see docs/posthog-cost-controls.md).
 */
export const MOBILE_TELEMETRY_DISABLED =
	(process.env.EXPO_PUBLIC_AO_TELEMETRY_DISABLED ?? "").trim() === "1";

export const MOBILE_DISABLED_EVENTS = (process.env.EXPO_PUBLIC_AO_TELEMETRY_DISABLED_EVENTS ?? "")
	.split(",")
	.map((name: string) => name.trim())
	.filter((name: string) => name.length > 0);
