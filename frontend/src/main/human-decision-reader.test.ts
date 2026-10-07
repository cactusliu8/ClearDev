// @vitest-environment node
import { EventEmitter } from "node:events";
import { describe, it, expect, vi } from "vitest";
import type { BrowserWindow, BrowserWindowConstructorOptions } from "electron";
import { decisionReaderHtml, decisionSummary, reviewHumanDecision } from "./human-decision-reader";
import { offerDialogOptions, type HumanDecisionOffer } from "./human-authority";

const offer: HumanDecisionOffer = {
	protocolVersion: 1, kind: "HUMAN_DECISION_OFFER", desktopRunId: "fixture-desktop", requestId: "fixture-request", decisionKind: "CONFIRM_REQUIREMENT_VERSION", bindingSchemaVersion: 1,
	binding: { id: "unchanged-binding" }, contentSha256: "b".repeat(64), nonce: "unchanged-nonce", expiresAt: "2099-01-01T00:00:00Z",
	display: { title: "Confirm requirement version v1", summary: "仅作自动测试夹具", fullContent: JSON.stringify({ schemaVersion: 1, summary: "门店与商品", requirements: [{ id: "REQ-1", priority: "MUST", text: "进货后库存增加" }], acceptanceScenarios: [{ id: "ACC-1", text: "重启后库存仍在" }], constraints: ["事务失败要回滚"], nonGoals: ["本轮不做收银"], assumptions: ["请确认加权成本"], conflicts: ["不可悄悄忽略冲突"], futureField: "新增字段必须保留", malicious: '<script>alert(1)</script><a href="https://bad.test">fake</a>' }), changeSummary: "原始版本与任务编号" },
};

class ReaderWindow extends EventEmitter {
	options?: BrowserWindowConstructorOptions;
	destroyed = false;
	url = "";
	webContents = Object.assign(new EventEmitter(), {
		setWindowOpenHandler: vi.fn(),
		session: { setPermissionRequestHandler: vi.fn(), setPermissionCheckHandler: vi.fn() },
	});
	show = vi.fn(); focus = vi.fn();
	loadURL = vi.fn(async (url: string) => { this.url = url; });
	isDestroyed(): boolean { return this.destroyed; }
	close(): void { this.destroyed = true; this.emit("closed"); }
	factory = (options: BrowserWindowConstructorOptions): BrowserWindow => { this.options = options; return this as unknown as BrowserWindow; };
}

describe("owned human decision reader", () => {
	it.each([
		["en", "Replace worker", "Replace the lost Builder for the original task without changing its code, history or budgets."],
		["zh-CN", "更换工作者", "更换已丢失的工作者以继续原任务，保留代码、历史和预算。"],
	] as const)("shows the exact replacement title and summary in %s without modifying its signed details", (locale, title, summary) => {
		const replacement = { ...offer, decisionKind: "AUTHORIZE_BUILDER_REPLACEMENT", display: { ...offer.display, title: "Replace worker", summary: "Replace the lost Builder for the original task without changing its code, history or budgets.", fullContent: "Original task; old session; sealed code location; original budget; approve only grants permission. 原任务及代码保留，批准后仍需明确继续。" } };
		const original = structuredClone(replacement);
		const html = decisionReaderHtml(replacement, locale, "https://cleardev-human.invalid/fixture/continue");
		expect(html).toContain(`<h1>${title}</h1>`);
		expect(html).toContain(`<div class="card"><p>${summary}</p></div>`);
		expect(html).toContain(replacement.display.summary);
		expect(html).toContain(replacement.display.fullContent);
		expect(offerDialogOptions(replacement, locale, true)).toMatchObject({ title, defaultId: 2, cancelId: 2 });
		expect(offerDialogOptions(replacement, locale, true).detail).toContain(summary);
		expect(replacement).toEqual(original);
	});
	it.each([
		"Replace the lost Builder and automatically continue the task.",
		"Replace the lost Builder for the original task without changing its code, history or budgets.\n",
	])("does not translate an unmatched replacement summary: %s", (summary) => {
		const replacement = { ...offer, decisionKind: "AUTHORIZE_BUILDER_REPLACEMENT", display: { ...offer.display, summary } };
		expect(decisionSummary(replacement, "en")).toBe(summary);
		expect(decisionSummary(replacement, "zh-CN")).toBe(summary);
	});
	it("does not apply the replacement summary translation to another kind or locale", () => {
		const summary = "Replace the lost Builder for the original task without changing its code, history or budgets.";
		expect(decisionSummary({ ...offer, display: { ...offer.display, summary } }, "zh-CN")).toBe(summary);
		expect(decisionSummary({ ...offer, decisionKind: "AUTHORIZE_BUILDER_REPLACEMENT", display: { ...offer.display, summary } }, "ja")).toBe(summary);
	});
	const extraPlanningSummary = "Two original attempts failed. Authorize one additional original message, then continue the same request separately.\n\n原来的两次尝试均已明确失败。可批准追加一次原消息，再单独继续原事项。";
	it.each([
		["en", "Two original attempts failed. Authorize one additional original message, then continue the same request separately."],
		["zh-CN", "原来的两次尝试均已明确失败。可批准追加一次原消息，再单独继续原事项。"],
	] as const)("shows the exact extra planning summary in %s in the reader and native dialog without changing the signed display", (locale, summary) => {
		const extra = { ...offer, decisionKind: "AUTHORIZE_EXTRA_PLANNING_ATTEMPT", display: { ...offer.display, summary: extraPlanningSummary } };
		const original = structuredClone(extra);
		expect(decisionSummary(extra, locale)).toBe(summary);
		const html = decisionReaderHtml(extra, locale, "https://cleardev-human.invalid/fixture/continue");
		expect(html).toContain(`<div class="card"><p>${summary}</p></div>`);
		expect(html).toContain(`<p>${extraPlanningSummary}</p>`);
		expect(html).toContain(extra.display.changeSummary);
		const dialog = offerDialogOptions(extra, locale, true);
		expect(dialog.detail).toContain(summary);
		expect(dialog.detail).not.toContain(extraPlanningSummary);
		expect(extra).toEqual(original);
	});
	it.each([
		extraPlanningSummary.split("\n\n")[0],
		extraPlanningSummary.replace("Two original attempts", "Three original attempts"),
		extraPlanningSummary.replace("再单独继续原事项", "批准后自动继续原事项"),
		`${extraPlanningSummary}\n`,
	])("preserves unmatched extra planning summary verbatim: %s", (summary) => {
		const extra = { ...offer, decisionKind: "AUTHORIZE_EXTRA_PLANNING_ATTEMPT", display: { ...offer.display, summary } };
		expect(decisionSummary(extra, "en")).toBe(summary);
		expect(decisionSummary(extra, "zh-CN")).toBe(summary);
	});
	it("preserves the bilingual summary for other kinds and locales", () => {
		const otherKind = { ...offer, display: { ...offer.display, summary: extraPlanningSummary } };
		expect(decisionSummary(otherKind, "en")).toBe(extraPlanningSummary);
		expect(decisionSummary(otherKind, "zh-CN")).toBe(extraPlanningSummary);
		expect(decisionSummary({ ...otherKind, decisionKind: "AUTHORIZE_EXTRA_PLANNING_ATTEMPT" }, "ja")).toBe(extraPlanningSummary);
	});
	it.each([["en", "Authorize one extra discussion or compilation attempt"], ["zh-CN", "批准一次额外讨论或编译尝试"]] as const)("names the extra planning decision accurately in %s while preserving its signed content", (locale, title) => {
		const extra = { ...offer, decisionKind: "AUTHORIZE_EXTRA_PLANNING_ATTEMPT", display: { ...offer.display, title: "Original signed extra-attempt title" } };
		const original = structuredClone(extra);
		const html = decisionReaderHtml(extra, locale, "https://cleardev-human.invalid/fixture/continue");
		expect(html).toContain(`<h1>${title}</h1>`);
		expect(html).toContain(extra.display.title);
		expect(extra).toEqual(original);
	});
	it("renders Chinese business sections, unknown fields and original content without executable markup", () => {
		const original = structuredClone(offer);
		const html = decisionReaderHtml(offer, "zh-CN", "https://cleardev-human.invalid/fixture/continue");
		for (const text of ["功能要求", "验收条件", "本轮不做", "待确认的默认假设", "尚未解决的冲突", "进货后库存增加", "重启后库存仍在", "新增字段必须保留", "原始内容与技术详情", offer.contentSha256]) expect(html).toContain(text);
		expect(html).not.toContain("<script>");
		expect(html).not.toContain('<a href="https://bad.test">');
		expect(html).toContain("&lt;script&gt;");
		expect(html).toContain("default-src 'none'");
		expect(offer).toEqual(original);
	});
	it("keeps plain legacy text and deeply nested or future content available", () => {
		const plain = { ...offer, display: { ...offer.display, fullContent: "中文旧需求\n<不执行代码>" } };
		expect(decisionReaderHtml(plain, "zh-CN", "https://cleardev-human.invalid/fixture/continue")).toContain("中文旧需求\n&lt;不执行代码&gt;");
	});
	it("blocks external navigation, popup windows and permissions; only this reader can continue", async () => {
		const window = new ReaderWindow();
		const reviewing = reviewHumanDecision({ offer, locale: "zh-CN", createWindow: window.factory });
		await Promise.resolve();
		expect(window.options?.webPreferences).toMatchObject({ sandbox: true, contextIsolation: true, nodeIntegration: false, webviewTag: false });
		expect(window.options?.webPreferences?.preload).toBeUndefined();
		expect(window.options?.webPreferences?.partition).not.toMatch(/^persist:/);
		expect(window.webContents.setWindowOpenHandler.mock.calls[0][0]()).toEqual({ action: "deny" });
		const deny = vi.fn(); window.webContents.session.setPermissionRequestHandler.mock.calls[0][0](null, "camera", deny); expect(deny).toHaveBeenCalledWith(false);
		const blocked = { preventDefault: vi.fn() };
		window.webContents.emit("will-navigate", blocked, "https://bad.test");
		expect(blocked.preventDefault).toHaveBeenCalled(); expect(window.destroyed).toBe(false);
		const html = decodeURIComponent(window.url.split(",").slice(1).join(","));
		const url = html.match(/href="(https:\/\/cleardev-human\.invalid\/[^"]+\/continue)"/)![1];
		window.webContents.emit("will-navigate", blocked, url);
		expect(await reviewing).toBe(true);
	});
	it("close, later, failed load and expired offers never continue", async () => {
		for (const action of ["close", "later", "failed"] as const) {
			const window = new ReaderWindow();
			const reviewing = reviewHumanDecision({ offer, locale: "zh-CN", createWindow: window.factory });
			if (action === "close") window.close();
			if (action === "later") { const html = decodeURIComponent(window.url.split(",").slice(1).join(",")); const url = html.match(/href="(https:\/\/cleardev-human\.invalid\/[^"]+\/later)"/)![1]; window.webContents.emit("will-navigate", { preventDefault: vi.fn() }, url); }
			if (action === "failed") window.webContents.emit("did-fail-load");
			expect(await reviewing).toBe(false);
		}
		const factory = vi.fn();
		expect(await reviewHumanDecision({ offer: { ...offer, expiresAt: "2000-01-01T00:00:00Z" }, locale: "zh-CN", createWindow: factory })).toBe(false);
		expect(factory).not.toHaveBeenCalled();
	});
});

describe("complete reader boundaries", () => {
	it("keeps the full execution proposal behind its labelled disclosure", () => {
		const fullContent = JSON.stringify({ constraints: ['项目执行依据（仅规划，不授权运行命令）：{"writePaths":["src/**"],"checks":[{"argv":["npm","run","verify"]}]}'] });
		const html = decisionReaderHtml({ ...offer, display: { ...offer.display, fullContent } }, "zh-CN", "https://cleardev-human.invalid/test/continue");
		expect(html).toContain("<details><summary>项目执行依据（仅规划，不授权运行命令）：</summary>");
		expect(html).toContain("允许修改的路径"); expect(html).toContain("src/**"); expect(html).toContain("verify");
	});
	it("does not continue if an offer expired while its reader was open", async () => {
		const window = new ReaderWindow();
		const reviewing = reviewHumanDecision({ offer, locale: "zh-CN", createWindow: window.factory });
		const html = decodeURIComponent(window.url.split(",").slice(1).join(","));
		const url = html.match(/href="(https:\/\/cleardev-human\.invalid\/[^"]+\/continue)"/)![1];
		const clock = vi.spyOn(Date, "now").mockReturnValue(Date.parse("2100-01-01T00:00:00Z"));
		try { window.webContents.emit("will-navigate", { preventDefault: vi.fn() }, url); expect(await reviewing).toBe(false); }
		finally { clock.mockRestore(); }
	});
});

describe("reader cancellation", () => {
 it("closes an unanswered reader on disconnect without continuing", async () => {
  const window = new ReaderWindow(); const controller = new AbortController();
  const result = reviewHumanDecision({ offer, locale: "en", signal: controller.signal, createWindow: window.factory });
  controller.abort(); expect(await result).toBe(false); expect(window.destroyed).toBe(true);
 });
 it("keeps the trusted reader as native parent, then closes it on offer cancellation", async () => {
  const window = new ReaderWindow(); const controller = new AbortController();
  const result = reviewHumanDecision({ offer, locale: "zh-CN", signal: controller.signal, keepOpenOnContinue: true, createWindow: window.factory });
  await Promise.resolve();
  const html = decodeURIComponent(window.url.split(",").slice(1).join(","));
  const url = html.match(/href="(https:\/\/cleardev-human\.invalid\/[^\"]+\/continue)"/)![1];
  window.webContents.emit("will-navigate", { preventDefault: vi.fn() }, url);
  expect(await result).toBe(true); expect(window.destroyed).toBe(false);
  controller.abort(); expect(window.destroyed).toBe(true);
 });
});

it("shows whole-plan authorization scope in both maintained languages",()=>{
 const plan={...offer,decisionKind:"CONFIRM_PRODUCT_PLAN",display:{...offer.display,summary:"只在下面已列明的阶段、功能验收和非目标内自动推进。每阶段仍须真实独立验收；新增功能或改变目标和验收时再次询问。"}};
 expect(decisionReaderHtml(plan,"en","https://cleardev-human.invalid/test/continue")).toContain("Confirm the whole product plan");
 expect(decisionSummary(plan,"en")).toContain("New features or changed goals");
 expect(decisionReaderHtml(plan,"zh-CN","https://cleardev-human.invalid/test/continue")).toContain("确认整体产品计划");
});
