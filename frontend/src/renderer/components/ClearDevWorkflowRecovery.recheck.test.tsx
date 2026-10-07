import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, expect, it, vi } from "vitest";
import { createAppI18n } from "../i18n/instance";
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: (e: unknown) => e instanceof Error ? e.message : "request failed" }));
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";

const option = { action: "RETRY_BUILDER_SESSION", targetId: "dispatch:resume:old-time", taskId: "task", role: "BUILDER", reason: "BUILDER_SPAWN_FAILED", summary: "" };
const response = { executionRunId: "run", options: [option], history: [] };
const recovery = { id: "saved-recheck", executionRunId: "run", action: option.action, targetId: option.targetId, taskId: "task", stepId: "original-step", bindingId: "original-builder", dispatchId: "dispatch", successorId: "unused-id", originalStatus: "BLOCKED", originalReason: option.reason, originalSummary: "", originalStoppedAt: "2026-09-29T10:00:00Z", createdAt: "2026-09-29T11:00:00Z", supplement: "This exact original recheck request." };
const failed = { recoveryId: recovery.id, checkpoint: "FINISHED", stage: "RESTORE", outcome: "FAILED", reasonCode: "LOGIN_REQUIRED", checkedAt: "2026-09-29T11:00:01Z" };
function mount(locale: "en" | "zh-CN" = "zh-CN") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const i18n = createAppI18n(locale);
  return { client, i18n, ...render(<I18nextProvider i18n={i18n}><QueryClientProvider client={client}><ClearDevWorkflowRecovery id="requirement" scope="execution" blocked /></QueryClientProvider></I18nextProvider>) };
}
beforeEach(() => { get.mockReset(); post.mockReset(); get.mockResolvedValue({ data: response }); post.mockResolvedValue({ data: response }); });

it.each([ ["zh-CN", "重新检查并恢复原开发步骤"], ["en", "Recheck and restore the original development step"] ] as const)("allows an empty technical recheck in %s without claiming an environment fix", async (locale, label) => {
  const { i18n } = mount(locale);
  const button = await screen.findByRole("button", { name: label });
  expect(button).toBeEnabled();
  expect(screen.getByRole("textbox")).toHaveValue("");
  expect(screen.getByText(i18n.t("cleardevRecheck.help"))).toBeInTheDocument();
  fireEvent.click(button); fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body).toMatchObject({ action: option.action, targetId: option.targetId, executionRunId: "run", supplement: i18n.t("cleardevRecheck.defaultContext") });
});

it("replays the registered Builder request instead of creating another recheck", async () => {
  const registered = { ...response, options: [{ ...option, unavailableReason: "BUILDER_RECHECK_REGISTERED" }], history: [recovery] };
  get.mockResolvedValue({ data: registered }); post.mockResolvedValue({ data: registered });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "继续已登记的步骤" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body).toEqual({ requestId: recovery.id, executionRunId: "run", action: recovery.action, targetId: recovery.targetId, supplement: recovery.supplement });
});

it("does not manufacture a request when an existing registration cannot be matched", async () => {
  get.mockResolvedValue({ data: { ...response, options: [{ ...option, unavailableReason: "BUILDER_RECHECK_REGISTERED" }] } });
  mount();
  expect(await screen.findByText(/恢复登记与历史尚未对应/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "继续已登记的步骤" })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("shows the actual failed recheck stage and cause without automatically resending", async () => {
  get.mockResolvedValue({ data: { ...response, history: [recovery], builderSessionChecks: [failed] } });
  mount();
  expect(await screen.findByText("恢复原生对话")).toBeInTheDocument();
  expect(screen.getByText("LOGIN_REQUIRED")).toBeInTheDocument();
  expect(screen.getByText(/通过该工具自己的正常登录入口处理/)).toBeInTheDocument();
  expect(screen.getByText(/本次检查在新任务消息发送前停止/)).toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("never displays a readiness receipt as a delivered task or new approval", async () => {
  get.mockResolvedValue({ data: { ...response, options: [], history: [recovery], builderSessionChecks: [{ ...failed, stage: "READY", outcome: "READY", reasonCode: "" }] } });
  mount();
  expect(await screen.findByText(/实际投递仍使用原发送器和预算/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "重新检查并恢复原开发步骤" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /批准/ })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("keeps a lost recheck request unchanged across language changes and refresh failures", async () => {
  post.mockRejectedValue(new Error("lost response"));
  const { client, i18n } = mount();
  fireEvent.click(await screen.findByRole("button", { name: "重新检查并恢复原开发步骤" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const original = post.mock.calls[0][1].body;
  await waitFor(() => expect(screen.getByRole("button", { name: "重试同一恢复请求" })).toBeEnabled());
  await act(async () => { await i18n.changeLanguage("en"); });
  get.mockRejectedValue(new Error("unavailable"));
  await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "requirement"] }); });
  await waitFor(() => expect(screen.queryByRole("button", { name: "Retry the same recovery request" })).not.toBeInTheDocument());
  get.mockResolvedValue({ data: response });
  fireEvent.click(screen.getByRole("button", { name: "Refresh recovery status" }));
  const retry = await screen.findByRole("button", { name: "Retry the same recovery request" });
  await waitFor(() => expect(retry).toBeEnabled()); fireEvent.click(retry);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body).toEqual(original);
});
