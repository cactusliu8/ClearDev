import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, expect, it, vi } from "vitest";
import { createAppI18n } from "../i18n/instance";
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: (e: unknown) => e instanceof Error ? e.message : "request failed" }));
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";

const option = { action: "RETRY_PLANNING_STEP", targetId: "frozen-target", role: "STEWARD", reason: "COMPILATION_INVALID", summary: "summary has 10001 Unicode characters; maximum 10000" };
const planning = { executionRunId: "", options: [option], history: [] };
const history = { id: "registered-request", action: "RETRY_PLANNING_STEP", executionRunId: "", targetId: "frozen-target", stepId: "step", bindingId: "role", successorId: "step:attempt:2", originalStatus: "FAILED", originalReason: "COMPILATION_INVALID", originalSummary: "original failure", supplement: "原登记说明", createdAt: "2026-09-29T10:00:00Z", originalStoppedAt: "2026-09-29T09:00:00Z" };
const registered = { ...planning, options: [{ ...option, targetId: "step", unavailableReason: "CONTINUATION_REGISTERED" }], history: [history] };
function mount(scope: "discussion" | "compilation" | "execution" = "compilation", locale: "en" | "zh-CN" = "zh-CN") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const i18n = createAppI18n(locale);
  const ui = (id = "req", current = true, copies = 1, sourceKey?: string) => <I18nextProvider i18n={i18n}><QueryClientProvider client={client}>{Array.from({ length: copies }, (_, index) => <ClearDevWorkflowRecovery key={index} id={id} scope={scope} current={current} sourceKey={sourceKey} blocked />)}</QueryClientProvider></I18nextProvider>;
  return { client, i18n, ui, ...render(ui()) };
}
beforeEach(() => { get.mockReset(); post.mockReset(); get.mockResolvedValue({ data: planning }); });

it.each([ ["discussion", "继续本轮讨论"], ["compilation", "重新编译原阶段规格"] ] as const)("continues %s with an empty execution identity and optional context", async (scope, label) => {
  post.mockImplementation(async () => { get.mockResolvedValue({ data: { ...planning, options: [], history: [{ ...history, id: post.mock.calls[0][1].body.requestId, supplement: "" }] } }); return { data: { ...planning, options: [] } }; });
  mount(scope);
  fireEvent.click(await screen.findByRole("button", { name: label }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body).toMatchObject({ executionRunId: "", action: "RETRY_PLANNING_STEP", targetId: "frozen-target", supplement: "" });
  expect(await screen.findByRole("status")).toHaveTextContent("不表示检查或验收已通过");
});

it("replays a persisted registration exactly after a remount, without creating a request ID", async () => {
  get.mockResolvedValue({ data: registered }); post.mockResolvedValue({ data: registered });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "继续已登记的步骤" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body).toEqual({ requestId: history.id, executionRunId: "", action: history.action, targetId: history.targetId, supplement: history.supplement });
});

it("never invents a registration when persisted history is absent", async () => {
  get.mockResolvedValue({ data: { ...registered, history: [] } }); mount();
  expect(await screen.findByText(/恢复登记与历史尚未对应/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "继续已登记的步骤" })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("retains the exact body on a lost response and blocks editing or new actions until reconciled", async () => {
  post.mockRejectedValueOnce(new Error("connection lost")); mount();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "保留原输入" } });
  fireEvent.click(screen.getByRole("button", { name: "重新编译原阶段规格" }));
  const retry = await screen.findByRole("button", { name: "重试同一恢复请求" });
  await waitFor(() => expect(retry).toBeEnabled()); expect(screen.getByRole("textbox")).toBeDisabled();
  const original = post.mock.calls[0][1].body;
  post.mockResolvedValueOnce({ data: { ...planning, options: [] } });
  fireEvent.click(retry); fireEvent.click(retry);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2)); expect(post.mock.calls[1][1].body).toEqual(original);
});

it("removes stale write controls on a refresh error, then allows a read-only refresh", async () => {
  const { client } = mount();
  expect(await screen.findByRole("button", { name: "重新编译原阶段规格" })).toBeEnabled();
  get.mockRejectedValue(new Error("offline"));
  await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "req"] }); });
  expect(await screen.findByRole("alert")).toHaveTextContent("恢复入口读取失败");
  expect(screen.queryByRole("button", { name: "重新编译原阶段规格" })).not.toBeInTheDocument();
  get.mockResolvedValue({ data: planning }); fireEvent.click(screen.getByRole("button", { name: "刷新恢复状态" }));
  expect(await screen.findByRole("button", { name: "重新编译原阶段规格" })).toBeEnabled(); expect(post).not.toHaveBeenCalled();
});

it("does not retain success or editable drafts when the requirement or source changes", async () => {
  const { rerender, ui } = mount();
  fireEvent.change(await screen.findByRole("textbox"), { target: { value: "old draft" } });
  rerender(ui("req", false)); expect(screen.queryByRole("button", { name: "重新编译原阶段规格" })).not.toBeInTheDocument();
  rerender(ui("next", true)); await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue(""));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "next draft" } });
  rerender(ui("next", true, 1, "new-source")); await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue(""));
  expect(post).not.toHaveBeenCalled();
});

it.each([ ["RESULT_NOT_SETTLED", "不能重复发送"], ["MESSAGE_BUDGET_EXHAUSTED", "本步骤的有限尝试或消息次数已用完"], ["MESSAGE_BUDGET_UNKNOWN", "历史消息用量无法确认"], ["RETRY_NOT_DUE", "服务方要求的重试等待期尚未结束"], ["RECOVERY_NATIVE_IDENTITY_MISSING", "原生会话身份"], ["PREFLIGHT_OR_DELIVERY_REQUIRED", "预检查或消息投递"], ["PLANNING_SOURCE_CHANGED", "原讨论、阶段或所选来源已变化"] ])("explains %s without granting a new action", async (reason, text) => {
  get.mockResolvedValue({ data: { ...planning, options: [{ ...option, unavailableReason: reason }] } }); mount();
  expect(await screen.findByText(new RegExp(text))).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "重新编译原阶段规格" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /申请额外一轮/ })).not.toBeInTheDocument();
});

it("provides a visible neutral default for invalid Builder results without claiming an environment repair", async () => {
  get.mockResolvedValue({ data: { ...planning, executionRunId: "run", options: [{ ...option, action: "CONTINUE_BUILDER", reason: "BUILDER_RESULT_INVALID", taskId: "task", role: "BUILDER" }] } });
  post.mockResolvedValue({ data: { ...planning, executionRunId: "run", options: [] } }); mount("execution");
  expect(await screen.findByText(/继续会使用新的开发轮次/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "保留已有工作，继续原任务" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body.supplement).toBe("保留原任务、原会话和已有工作，按原规格继续并提交有效结果。");
});

it("keeps context mandatory for environment checks and actual human questions", async () => {
  get.mockResolvedValue({ data: { ...planning, executionRunId: "run", options: [{ ...option, action: "RETRY_CHECK", reason: "CHECKER_UNAVAILABLE" }] } });
  post.mockResolvedValue({ data: { ...planning, executionRunId: "run", options: [] } }); mount("execution");
  const button = await screen.findByRole("button", { name: "重试已结算的原检查" });
  expect(button).toBeDisabled(); fireEvent.change(screen.getByRole("textbox"), { target: { value: "依赖缓存已准备" } });
  expect(button).toBeEnabled(); fireEvent.click(button); await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body.supplement).toBe("依赖缓存已准备");
});

it("shows a stopped flow without a supported action instead of hiding it", async () => {
  get.mockResolvedValue({ data: { ...planning, options: [] } }); mount();
  expect(await screen.findByText(/目前没有可安全执行的恢复操作/)).toBeInTheDocument(); expect(post).not.toHaveBeenCalled();
});

it("does not render arbitrary future action strings as executable buttons", async () => {
  get.mockResolvedValue({ data: { ...planning, options: [{ ...option, action: "UNKNOWN_ACTION" }] } }); mount();
  expect(await screen.findByText(/当前界面不支持这项恢复操作/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "UNKNOWN_ACTION" })).not.toBeInTheDocument();
});

it("serializes two panels and keeps the same request across panel and language changes", async () => {
  post.mockRejectedValue(new Error("lost response"));
  const { rerender, ui, i18n } = mount("discussion"); rerender(ui("req", true, 2));
  const buttons = await screen.findAllByRole("button", { name: "继续本轮讨论" });
  await act(async () => { buttons.forEach((button) => fireEvent.click(button)); });
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1)); const original = post.mock.calls[0][1].body;
  rerender(ui("req", true, 0)); await act(async () => { await i18n.changeLanguage("en"); }); rerender(ui());
  const retry = await screen.findByRole("button", { name: "Retry the same recovery request" });
  await waitFor(() => expect(retry).toBeEnabled()); fireEvent.click(retry);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2)); expect(post.mock.calls[1][1].body).toEqual(original);
});

it("offers English actions and only requests native budget authorization", async () => {
  get.mockResolvedValue({ data: { ...planning, executionRunId: "run", options: [{ ...option, action: "CONTINUE_BUILDER", reason: "BUILDER_RESULT_INVALID", taskId: "task", role: "BUILDER", unavailableReason: "BUILDER_BUDGET_EXHAUSTED" }] } });
  post.mockResolvedValue({ data: {} }); mount("execution", "en");
  fireEvent.click(await screen.findByRole("button", { name: "Request one extra turn (decide in the native window)" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0]).toEqual(["/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/builder-turn-authorizations", { params: { path: { id: "req", taskId: "task" } } }]);
  expect(await screen.findByRole("status")).toHaveTextContent("Make the decision in the native window");
});

it("keeps an unknown old request without blocking a demonstrably new current step", async () => {
  post.mockRejectedValue(new Error("lost response"));
  const { client, rerender, ui } = mount();
  fireEvent.click(await screen.findByRole("button", { name: "重新编译原阶段规格" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const oldBody = post.mock.calls[0][1].body;
  await waitFor(() => expect(screen.getByRole("button", { name: "重试同一恢复请求" })).toBeEnabled());
  const next = { ...planning, options: [{ ...option, targetId: "new-current-target" }] };
  get.mockResolvedValue({ data: next });
  rerender(ui("req", true, 1, "new-current-source"));
  const nextAction = await screen.findByRole("button", { name: "重新编译原阶段规格" });
  await waitFor(() => expect(nextAction).toBeEnabled());
  expect(screen.getByRole("textbox")).toBeEnabled();
  fireEvent.click(nextAction);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body.targetId).toBe("new-current-target");
  expect(post.mock.calls[1][1].body.requestId).not.toBe(oldBody.requestId);
  await waitFor(() => expect(client.isMutating()).toBe(0));
  // The old outcome is genuinely unknown, not deleted or declared accepted.
  // If its exact binding is exposed again, only its original body is retried.
  get.mockResolvedValue({ data: planning });
  await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "req"] }); });
  await waitFor(() => expect(screen.getByRole("button", { name: "重试同一恢复请求" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "重试同一恢复请求" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  expect(post.mock.calls[2][1].body).toEqual(oldBody);
});

it("uses the persisted winner when a competing request already registered this continuation", async () => {
  post.mockRejectedValueOnce(new Error("lost competing response"));
  const { client } = mount();
  fireEvent.click(await screen.findByRole("button", { name: "重新编译原阶段规格" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const rejected = post.mock.calls[0][1].body;
  get.mockResolvedValue({ data: registered });
  await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "req"] }); });
  const resume = await screen.findByRole("button", { name: "继续已登记的步骤" });
  await waitFor(() => expect(resume).toBeEnabled());
  post.mockResolvedValue({ data: registered });
  fireEvent.click(resume);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body).toEqual({ requestId: history.id, executionRunId: "", action: history.action, targetId: history.targetId, supplement: history.supplement });
  expect(post.mock.calls[1][1].body.requestId).not.toBe(rejected.requestId);
});

it("does not trust expired recovery facts while offline", async () => {
  const { client } = mount(); const button = await screen.findByRole("button", { name: "重新编译原阶段规格" });
  await act(async () => { client.setQueryData(["cleardev-recoveries", "req"], planning, { updatedAt: Date.now() - 31_000 }); });
  await waitFor(() => expect(button).toBeDisabled()); fireEvent.click(button); expect(post).not.toHaveBeenCalled();
});
