import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, expect, it, vi } from "vitest";
import { createAppI18n } from "../i18n/instance";
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed" }));
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";

const current = { executionRunId: "run", options: [{ action: "CONTINUE_BUILDER", targetId: "dispatch", taskId: "task", role: "BUILDER", reason: "BUILDER_BLOCKED", summary: '{"kind":"BUILDER_RESULT","summary":"请提供完整检查错误"}' }], history: [] };
function mount() {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
 return render(<I18nextProvider i18n={createAppI18n("zh-CN")}><QueryClientProvider client={client}><ClearDevWorkflowRecovery id="requirement" /></QueryClientProvider></I18nextProvider>);
}
beforeEach(() => {get.mockReset();post.mockReset();get.mockResolvedValue({ data: current });});
it("shows a readable blocker and sends one bound request on repeated clicks", async () => {
 let finish: (value: unknown) => void = () => {};
 post.mockImplementation(() => new Promise((resolve) => {finish=resolve;}));
 mount();
 expect(await screen.findByText("请提供完整检查错误")).toBeInTheDocument();
 expect(screen.queryByText(/BUILDER_RESULT/)).not.toBeInTheDocument();
 const button = screen.getByRole("button", {name:"补充说明，继续开发"});
 expect(button).toBeDisabled();
 fireEvent.change(screen.getByRole("textbox"), {target:{value:"完整错误：EACCES，写入位置应为临时目录。"}});
 fireEvent.click(button);fireEvent.click(button);
 await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
 expect(post.mock.calls[0][1].body).toMatchObject({executionRunId:"run",targetId:"dispatch",action:"CONTINUE_BUILDER",supplement:"完整错误：EACCES，写入位置应为临时目录。"});
 get.mockResolvedValue({data:{...current,options:[]}});finish({data:{...current,options:[]}});
 expect(await screen.findByRole("status")).toHaveTextContent("不表示检查或验收已通过");
});
it("removes cached actions when refresh fails and explains unavailable budget", async () => {
 get.mockResolvedValue({data:{...current,options:[{...current.options[0],unavailableReason:"BUILDER_BUDGET_EXHAUSTED"}]}});
 mount();
 expect(await screen.findByText(/当前工作者预算已用完/)).toBeInTheDocument();
 expect(screen.queryByRole("button", {name:"补充说明，继续开发"})).not.toBeInTheDocument();
});
