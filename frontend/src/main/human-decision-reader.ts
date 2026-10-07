import { randomUUID } from "node:crypto";
import type { BrowserWindow, BrowserWindowConstructorOptions } from "electron";
import { catalogFor } from "../renderer/i18n/messages";
import type { AppLocale } from "../shared/ui-locale";
import type { HumanDecisionOffer } from "./human-authority";

function escape(value: string): string {
	return value.replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]!);
}

export function decisionLabels(locale: AppLocale): (key: string) => string {
	const catalog = catalogFor(locale);
	return (key) => catalog[`humanDecision.${key}`] ?? key;
}

export function decisionTitle(offer: HumanDecisionOffer, locale: AppLocale): string {
	const key: Record<string, string> = {
		CONFIRM_REQUIREMENT_VERSION: "confirmRequirement",
        CONFIRM_PRODUCT_PLAN: "confirmProductPlan",
		APPROVE_DIRECTION_CHANGE: "confirmDirection",
		AUTHORIZE_EXTRA_BUILDER_TURN: "confirmRecovery",
		AUTHORIZE_PLANNING_REVIEW_RECOVERY: "confirmRecovery",
		AUTHORIZE_FINAL_REVIEW_EVIDENCE_RECHECK: "confirmRecovery",
		AUTHORIZE_EXTRA_REVIEW_BUDGET: "confirmRecovery",
		AUTHORIZE_EXTRA_PLANNING_ATTEMPT: "confirmExtraPlanningAttempt",
		AUTHORIZE_BUILDER_REPLACEMENT: "confirmBuilderReplacement",
	};
	return key[offer.decisionKind] ? decisionLabels(locale)(key[offer.decisionKind]) : offer.display.title;
}

export function decisionSummary(offer: HumanDecisionOffer, locale: AppLocale): string {
 if (offer.decisionKind === "CONFIRM_PRODUCT_PLAN" && offer.display.summary === "只在下面已列明的阶段、功能验收和非目标内自动推进。每阶段仍须真实独立验收；新增功能或改变目标和验收时再次询问。" && (locale === "en" || locale === "zh-CN")) return decisionLabels(locale)("productPlanSummary");
	if (offer.decisionKind === "AUTHORIZE_BUILDER_REPLACEMENT" && offer.display.summary === "Replace the lost Builder for the original task without changing its code, history or budgets." && (locale === "en" || locale === "zh-CN")) {
		return decisionLabels(locale)("builderReplacementSummary");
	}
	const extraPlanningSummary = "Two original attempts failed. Authorize one additional original message, then continue the same request separately.\n\n原来的两次尝试均已明确失败。可批准追加一次原消息，再单独继续原事项。";
	if (offer.decisionKind === "AUTHORIZE_EXTRA_PLANNING_ATTEMPT" && offer.display.summary === extraPlanningSummary && (locale === "en" || locale === "zh-CN")) {
		return decisionLabels(locale)("extraPlanningAttemptSummary");
	}
	if (locale === "zh-CN" && offer.decisionKind === "CONFIRM_REQUIREMENT_VERSION") {
		const match = offer.display.summary.match(/^(.+) version (\d+) is waiting for confirmation\.$/);
		if (match) return `${match[1]} · 需求版本 ${match[2]}`;
	}
	return offer.display.summary;
}

// Every field is retained, including fields from future document versions. The
// original signed display remains available verbatim; this is only a reader.
function contentHtml(content: string, locale: AppLocale): string {
	const t = decisionLabels(locale);
	const technical = new Set(["schemaVersion", "sourcePrdSha256", "sourcePRDSHA256", "acceptanceIds"]);
	const valueHtml = (value: unknown, depth: number): string => {
		if (depth > 20) return `<pre>${escape(JSON.stringify(value, null, 2))}</pre>`;
		if (Array.isArray(value)) return value.length ? `<ol>${value.map((item) => `<li>${valueHtml(item, depth + 1)}</li>`).join("")}</ol>` : `<p class="muted">${escape(t("empty"))}</p>`;
		if (value && typeof value === "object") {
			const record = value as Record<string, unknown>;
			const compact = typeof record.id === "string" && typeof record.text === "string";
			const heading = compact ? `<p><span class="muted">${escape(record.id as string)}</span>${typeof record.priority === "string" ? ` · <strong>${escape(record.priority === "MUST" ? t("must") : record.priority)}</strong>` : ""}</p><p>${escape(record.text as string)}</p>` : "";
			return heading + Object.entries(record).filter(([key]) => !compact || (key !== "id" && key !== "text" && (key !== "priority" || typeof record.priority !== "string"))).map(([key, item]) => {
			const body = `<h3>${escape(t(`field.${key}`) === `field.${key}` ? key : t(`field.${key}`))}</h3>${valueHtml(item, depth + 1)}`;
			return technical.has(key) ? `<details><summary>${escape(t("technical"))}: ${escape(key)}</summary>${body}</details>` : `<section>${body}</section>`;
			}).join("");
		}
		if (typeof value === "string") {
			// Execution proposals are sometimes stored as a labelled JSON suffix.
			// Preserve its label and all fields, instead of presenting an escaped blob.
			const start = value.indexOf("{");
			if (start > 0) {
				try {
					const nested = JSON.parse(value.slice(start));
					if (nested && typeof nested === "object") {
						const label = escape(value.slice(0, start));
						const body = valueHtml(nested, depth + 1);
						return value.startsWith("项目执行依据") ? `<details><summary>${label}</summary>${body}</details>` : `<p>${label}</p>${body}`;
					}
				} catch { /* ordinary prose remains verbatim */ }
			}
			return `<p>${escape(value)}</p>`;
		}
		return `<p>${escape(JSON.stringify(value))}</p>`;
	};
	try {
		const parsed: unknown = JSON.parse(content);
		if (parsed && typeof parsed === "object") return valueHtml(parsed, 0);
	} catch { /* legacy plain requirements remain readable */ }
	return `<p>${escape(content)}</p>`;
}

export function decisionReaderHtml(offer: HumanDecisionOffer, locale: AppLocale, continueUrl: string): string {
	const t = decisionLabels(locale);
	return `<!doctype html><html lang="${escape(locale)}"><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>${escape(decisionTitle(offer, locale))}</title><style>
	:root{color-scheme:light dark}*{box-sizing:border-box}body{margin:0;background:#f5f7fb;color:#182235;font:15px/1.65 system-ui,sans-serif}header,main,footer{max-width:880px;margin:auto;padding:24px 32px}header{padding-bottom:12px}h1{font-size:24px;margin:0 0 12px}h2{font-size:18px}h3{font-size:15px;margin:8px 0;color:#344b79}p{white-space:pre-wrap;overflow-wrap:anywhere;margin:6px 0}main>section,.card{background:white;border:1px solid #dce3ef;border-radius:12px;padding:18px 22px;margin-bottom:14px}section section{border-left:2px solid #e1e8f6;padding-left:14px;margin:12px 0}ol{padding-left:24px}li{margin:10px 0}details{margin:14px 0;border-top:1px solid #e2e7f0;padding-top:10px}summary{cursor:pointer;color:#466399}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px}.muted{color:#637087}footer{position:sticky;bottom:0;background:#f5f7fb;border-top:1px solid #dce3ef;display:flex;justify-content:flex-end;gap:14px}a{display:inline-block;text-decoration:none;padding:9px 18px;border-radius:8px;color:#fff;background:#4169e1}a.secondary{background:white;color:#344b79;border:1px solid #dce3ef}@media(prefers-color-scheme:dark){body,footer{background:#171b25;color:#e6edf9}main>section,.card{background:#222938;border-color:#364258}h3{color:#b9ccff}.muted{color:#a5b1c7}a.secondary{background:#222938;color:#e6edf9}details,footer{border-color:#364258}}
	</style></head><body><header><h1>${escape(decisionTitle(offer, locale))}</h1><p class="muted">${escape(t("readFirst"))}</p></header><main><div class="card"><p>${escape(decisionSummary(offer, locale))}</p></div><section>${contentHtml(offer.display.fullContent, locale)}</section><details><summary>${escape(t("original"))}</summary><h2>${escape(offer.display.title)}</h2><p>${escape(offer.display.summary)}</p><pre>${escape(offer.display.fullContent)}</pre><p>${escape(offer.display.changeSummary)}</p><p>SHA-256: ${escape(offer.contentSha256)}</p></details><p class="muted">${escape(t("expires"))} ${escape(offer.expiresAt)}</p></main><footer><a class="secondary" href="${escape(continueUrl.replace(/\/continue$/, "/later"))}">${escape(t("later"))}</a><a href="${escape(continueUrl)}">${escape(t("continue"))}</a></footer></body></html>`;
}

export async function reviewHumanDecision(input: {
	offer: HumanDecisionOffer;
	locale: AppLocale;
	createWindow: (options: BrowserWindowConstructorOptions) => BrowserWindow;
 signal?: AbortSignal;
 keepOpenOnContinue?: boolean;
}): Promise<boolean> {
	const expiresAt = Date.parse(input.offer.expiresAt);
	if (input.signal?.aborted || !Number.isFinite(expiresAt) || expiresAt <= Date.now()) return false;
	const continueUrl = `https://cleardev-human.invalid/${randomUUID()}/continue`;
	const window = input.createWindow({
		width: 900, height: 760, minWidth: 540, minHeight: 440, show: false,
		title: decisionTitle(input.offer, input.locale), autoHideMenuBar: true,
		webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false, webviewTag: false, partition: `human-reader-${randomUUID()}` },
	});
	return new Promise<boolean>((resolve) => {
		let continued = false;
		let settled = false;
		const finish = (): void => {
			if (settled) return;
			settled = true;
			if (!continued || !input.keepOpenOnContinue) { clearTimeout(timer); input.signal?.removeEventListener("abort", abort); }
			resolve(continued);
		};
		const close = (): void => { if (!window.isDestroyed()) window.close(); finish(); };
		const abort = (): void => { continued = false; close(); };
  input.signal?.addEventListener("abort", abort, { once: true });
  const timer = setTimeout(close, Math.min(expiresAt - Date.now(), 2147483647));
		window.once("closed", () => { clearTimeout(timer); input.signal?.removeEventListener("abort", abort); finish(); });
		window.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
		window.webContents.session.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
		window.webContents.session.setPermissionCheckHandler(() => false);
		window.webContents.on("will-navigate", (event, url) => {
			event.preventDefault();
			if (url === continueUrl && !settled) { continued = !input.signal?.aborted && Date.now() < expiresAt; if (continued && input.keepOpenOnContinue) finish(); else close(); }
			else if (url === continueUrl.replace(/\/continue$/, "/later")) close();
		});
		window.webContents.once("did-fail-load", close);
		void window.loadURL(`data:text/html;charset=utf-8,${encodeURIComponent(decisionReaderHtml(input.offer, input.locale, continueUrl))}`).then(() => {
			if (!settled && !window.isDestroyed()) { window.show(); window.focus(); }
		}).catch(close);
	});
}
