import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, expect, it, vi } from "vitest";
import { createAppI18n } from "../i18n/instance";
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: () => "offline" }));
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";
const data = { executionRunId: "run", options: [{ action: "CONTINUE_BUILDER", targetId: "dispatch", reason: "BUILDER_BLOCKED", summary: "旧阻塞" }], history: [{ id: "history", action: "RETRY_STAGE", originalSummary: "当时缺少页面证据", supplement: "重新试用", createdAt: "2026-10-03T12:00:00Z" }] };
function mount(completed = true, current = false, locale = "zh-CN") {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
 const i18n = createAppI18n(locale as "zh-CN" | "en");
 const ui = (done: boolean, active: boolean) => <I18nextProvider i18n={i18n}><QueryClientProvider client={client}><ClearDevWorkflowRecovery id="req" completed={done} current={active} blocked={!done} /></QueryClientProvider></I18nextProvider>;
 return { ...render(ui(completed,current)), ui, client };
}
beforeEach(() => {get.mockReset();post.mockReset();get.mockResolvedValue({data});});
it.each(["zh-CN", "en"])("completed stage keeps collapsed history without recovery controls (%s)", async locale => {
 mount(true,false,locale);
 const text = locale === "en" ? "Recovery history (1)" : "查看恢复记录（1）";
 const summary = await screen.findByText(text);
 const details = summary.closest("details")!;
 expect(details).not.toHaveAttribute("open");
 expect(screen.queryByRole("button")).not.toBeInTheDocument();
 expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
 expect(screen.queryByRole("alert")).not.toBeInTheDocument();
 expect(screen.queryByText("处理阻塞并继续")).not.toBeInTheDocument();
 fireEvent.click(summary);
 expect(details).toHaveAttribute("open");
 expect(screen.getByText("当时缺少页面证据")).toBeVisible();
 expect(post).not.toHaveBeenCalled();
});
it("completion removes cached live actions; noncurrent unfinished stages still warn", async () => {
 const {rerender,ui} = mount(false,true);
 expect(await screen.findByRole("textbox")).toBeInTheDocument();
 rerender(ui(true,true));
 expect(await screen.findByText("查看恢复记录（1）")).toBeInTheDocument();
 expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
 expect(screen.queryByRole("button")).not.toBeInTheDocument();
 rerender(ui(false,false));
 expect(await screen.findByRole("alert")).toBeInTheDocument();
 expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
 expect(post).not.toHaveBeenCalled();
});
it("history failure is not a current blocker", async () => {
 get.mockRejectedValue(new Error("offline")); mount();
 expect(await screen.findByText(/历史恢复记录暂时无法读取/)).toBeInTheDocument();
 expect(screen.queryByRole("alert")).not.toBeInTheDocument();
 expect(screen.queryByRole("button")).not.toBeInTheDocument();
});
it("completed stage with no history has no recovery panel", async () => {
 get.mockResolvedValue({data:{...data,history:[]}});const {container,client}=mount();
 await vi.waitFor(()=>expect(client.getQueryState(["cleardev-recoveries","req"])?.status).toBe("success"));
 expect(container).toBeEmptyDOMElement();
});
