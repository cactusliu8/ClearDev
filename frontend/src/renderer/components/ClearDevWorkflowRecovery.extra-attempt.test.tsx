import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, expect, it, vi } from "vitest";
import { createAppI18n } from "../i18n/instance";
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed" }));
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";

const requestAction = "REQUEST_EXTRA_PLANNING_ATTEMPT";
const continueAction = "CONTINUE_EXTRA_PLANNING_ATTEMPT";
const target = "original-step:extra:exact-binding";
const storageKey = "cleardev-extra-planning-requests:req";
const savedBody = { requestId: "unknown-extra-request", executionRunId: "", action: requestAction, targetId: target, supplement: "保留原非空补充" };
const option = { action: requestAction, targetId: target, role: "STEWARD", reason: "COMPILATION_INVALID", summary: "Original attempts: 2/2; original messages: 3/3; extra attempts: 0/1." };
const initial = { executionRunId: "", options: [option], history: [] };
const requestHistory = { id: "saved-extra-request", action: requestAction, executionRunId: "", targetId: target, stepId: "original-step", bindingId: "original-role", successorId: "exact-native-decision", originalStatus: "FAILED", originalReason: "COMPILATION_INVALID", originalSummary: option.summary, supplement: "原申请补充", createdAt: "2026-09-30T10:00:00Z", originalStoppedAt: "2026-09-30T09:00:00Z" };
const continuationHistory = { ...requestHistory, id: "saved-extra-continuation", action: continueAction, successorId: "original-step:attempt:3", supplement: "原续行补充" };
const authorized = { ...initial, options: [{ ...option, action: continueAction }], history: [requestHistory] };
const registered = { ...authorized, options: [{ ...option, action: continueAction, unavailableReason: "CONTINUATION_REGISTERED" }], history: [requestHistory, continuationHistory] };
function mount(scope: "discussion" | "compilation" = "compilation", locale: "en" | "zh-CN" = "zh-CN") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const i18n = createAppI18n(locale);
  const ui = (copies = 1, current = true, sourceKey?: string) => <I18nextProvider i18n={i18n}><QueryClientProvider client={client}>{Array.from({ length: copies }, (_, index) => <ClearDevWorkflowRecovery key={index} id="req" scope={scope} current={current} sourceKey={sourceKey} blocked />)}</QueryClientProvider></I18nextProvider>;
  return { client, i18n, ui, ...render(ui()) };
}
beforeEach(() => { vi.restoreAllMocks(); get.mockReset(); post.mockReset(); sessionStorage.clear(); get.mockResolvedValue({ data: initial }); });

it.each([
  ["en", "Request one extra attempt (decide in the native window)", "Decide in the native window"],
  ["zh-CN", "申请额外一次尝试（在原生窗口决定）", "请本人在原生窗口决定"],
] as const)("requests only the strict five-field receipt in %s without claiming continuation", async (locale, label, pendingText) => {
  post.mockImplementation(async (_path, { body }) => {
    const data = { ...initial, options: [], history: [{ ...requestHistory, id: body.requestId, supplement: "" }] };
    get.mockResolvedValue({ data });
    return { data };
  });
  mount("compilation", locale);
  expect(await screen.findByText(option.summary)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: label }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const [path, { body }] = post.mock.calls[0];
  expect(path).toBe("/api/v1/cleardev/requirements/{id}/recoveries");
  expect(Object.keys(body).sort()).toEqual(["action", "executionRunId", "requestId", "supplement", "targetId"]);
  expect(body).toMatchObject({ action: requestAction, executionRunId: "", targetId: target, supplement: "" });
  expect(await screen.findByRole("status")).toHaveTextContent(pendingText);
  expect(screen.queryByText(/Processing will continue|正在继续处理/)).not.toBeInTheDocument();
});

it("shows durable waiting, rejected and consumed states without opening a continuation", async () => {
  get.mockResolvedValue({ data: { ...initial, options: [{ ...option, action: continueAction, unavailableReason: "EXTRA_ATTEMPT_DECISION_PENDING" }], history: [requestHistory] } });
  const { client } = mount();
  expect(await screen.findByText(/额外尝试申请已保存，请本人在原生窗口决定/)).toBeInTheDocument();
  expect(screen.queryByText(/额外原请求已获授权/)).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "申请额外一次尝试（在原生窗口决定）" })).not.toBeInTheDocument();
  for (const [reason, text] of [["EXTRA_ATTEMPT_DECISION_REJECTED", "额外尝试已被拒绝"], ["EXTRA_ATTEMPT_CONSUMED", "一次额外尝试已用完"]]) {
    get.mockResolvedValue({ data: { ...initial, options: [{ ...option, unavailableReason: reason }], history: [requestHistory] } });
    await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "req"] }); });
    expect(await screen.findByText(new RegExp(text))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /使用已授权次数/ })).not.toBeInTheDocument();
  }
  expect(post).not.toHaveBeenCalled();
});

it.each([
  ["discussion", "en", "Use the authorized attempt to continue this discussion"],
  ["compilation", "zh-CN", "使用已授权次数，编译原阶段规格"],
] as const)("requires a separate explicit %s continuation after authorization", async (scope, locale, label) => {
  get.mockResolvedValue({ data: authorized });
  post.mockResolvedValue({ data: { ...authorized, options: [] } });
  mount(scope, locale);
  const button = await screen.findByRole("button", { name: label });
  expect(button).toBeEnabled();
  expect(post).not.toHaveBeenCalled();
  fireEvent.click(button); fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body).toMatchObject({ action: continueAction, targetId: target, executionRunId: "", supplement: "" });
  expect(post.mock.calls[0][1].body.requestId).not.toBe(requestHistory.id);
});

it("replays only the saved CONTINUE record with its complete original body", async () => {
  get.mockResolvedValue({ data: registered }); post.mockResolvedValue({ data: registered });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "继续已登记的步骤" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body).toEqual({ requestId: continuationHistory.id, action: continueAction, targetId: target, executionRunId: "", supplement: continuationHistory.supplement });
});

it.each(["missing", "ambiguous"])("never substitutes the REQUEST record when CONTINUE history is %s", async (kind) => {
  get.mockResolvedValue({ data: { ...registered, history: kind === "missing" ? [requestHistory] : [requestHistory, continuationHistory, { ...continuationHistory, id: "other-continuation" }] } });
  mount();
  expect(await screen.findByText(/恢复登记与历史尚未对应/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "继续已登记的步骤" })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it.each([requestAction, continueAction])("serializes two panels and retains the complete lost %s across language changes", async (action) => {
  get.mockResolvedValue({ data: action === requestAction ? initial : authorized });
  post.mockRejectedValue(new Error("lost request response"));
  const { rerender, ui, i18n } = mount(); rerender(ui(2));
  const label = action === requestAction ? "申请额外一次尝试（在原生窗口决定）" : "使用已授权次数，编译原阶段规格";
  const buttons = await screen.findAllByRole("button", { name: label });
  fireEvent.change(screen.getAllByRole("textbox")[0], { target: { value: "保留原申请内容" } });
  await act(async () => { buttons.forEach((button) => fireEvent.click(button)); });
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const original = post.mock.calls[0][1].body;
  expect(original.action).toBe(action);
  expect(original.supplement).toBe("保留原申请内容");
  rerender(ui(0)); await act(async () => { await i18n.changeLanguage("en"); }); rerender(ui());
  const retry = await screen.findByRole("button", { name: "Retry the same recovery request" });
  await waitFor(() => expect(retry).toBeEnabled());
  expect(screen.getByRole("textbox")).toBeDisabled();
  post.mockResolvedValue({ data: { ...initial, options: [], history: [{ ...(action === requestAction ? requestHistory : continuationHistory), id: original.requestId, supplement: original.supplement }] } });
  fireEvent.click(retry); fireEvent.click(retry);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body).toEqual(original);
});

it.each([requestAction, continueAction])("restores the complete unknown %s after a real query-client reload", async (action) => {
  get.mockResolvedValue({ data: action === requestAction ? initial : authorized });
  post.mockImplementation(async (_path, { body }) => {
    expect(JSON.parse(sessionStorage.getItem(storageKey)!)).toEqual([body]);
    throw new Error("lost request response");
  });
  const first = mount();
  const label = action === requestAction ? "申请额外一次尝试（在原生窗口决定）" : "使用已授权次数，编译原阶段规格";
  const button = await screen.findByRole("button", { name: label });
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "刷新后仍保留的非空补充" } });
  fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const original = { ...post.mock.calls[0][1].body };
  expect(original.supplement).toBe("刷新后仍保留的非空补充");
  await waitFor(() => expect(screen.getByRole("button", { name: "重试同一恢复请求" })).toBeEnabled());
  first.unmount(); first.client.clear();
  const reloaded = mount();
  expect(reloaded.client).not.toBe(first.client);
  expect(await screen.findByRole("button", { name: "重试同一恢复请求" })).toBeEnabled();
  expect(reloaded.client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([original]);
  expect(post).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("textbox")).toBeDisabled();
  post.mockResolvedValue({ data: { ...initial, options: [], history: [{ ...(action === requestAction ? requestHistory : continuationHistory), id: original.requestId, supplement: original.supplement }] } });
  fireEvent.click(screen.getByRole("button", { name: "重试同一恢复请求" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body).toEqual(original);
  await waitFor(() => expect(sessionStorage.getItem(storageKey)).toBeNull());
  expect(reloaded.client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([]);
});

it.each([requestAction, continueAction])("clears stored %s only when exact or competing committed history resolves it", async (action) => {
  for (const winner of [savedBody.requestId, "different-committed-winner"]) {
    const body = { ...savedBody, action };
    sessionStorage.setItem(storageKey, JSON.stringify([body]));
    const committed = { ...(action === requestAction ? requestHistory : continuationHistory), id: winner, supplement: winner === body.requestId ? body.supplement : "获胜请求的补充" };
    get.mockResolvedValue({ data: { ...initial, options: [{ ...option, action: continueAction, unavailableReason: action === requestAction ? "EXTRA_ATTEMPT_DECISION_PENDING" : "EXTRA_ATTEMPT_CONSUMED" }], history: [committed] } });
    const view = mount();
    await waitFor(() => expect(sessionStorage.getItem(storageKey)).toBeNull());
    expect(view.client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([]);
    expect(post).not.toHaveBeenCalled();
    view.unmount(); view.client.clear();
  }
});

it("keeps an old stored target through a source change while allowing a new target", async () => {
  sessionStorage.setItem(storageKey, JSON.stringify([savedBody]));
  const newTarget = "next-step:extra:next-binding";
  get.mockResolvedValue({ data: { ...initial, options: [{ ...option, targetId: newTarget }] } });
  post.mockRejectedValue(new Error("unknown new target response"));
  const { rerender, ui, client } = mount();
  rerender(ui(1, true, "changed-source"));
  const button = await screen.findByRole("button", { name: "申请额外一次尝试（在原生窗口决定）" });
  await waitFor(() => expect(button).toBeEnabled());
  expect(screen.queryByRole("button", { name: "重试同一恢复请求" })).not.toBeInTheDocument();
  expect(screen.getByRole("textbox")).toBeEnabled();
  expect(post).not.toHaveBeenCalled();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "新目标的独立补充" } });
  fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const next = post.mock.calls[0][1].body;
  expect(next).toMatchObject({ action: requestAction, executionRunId: "", targetId: newTarget, supplement: "新目标的独立补充" });
  expect(next.requestId).not.toBe(savedBody.requestId);
  expect(client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([savedBody, next]);
  expect(JSON.parse(sessionStorage.getItem(storageKey)!)).toEqual([savedBody, next]);
});

it("does not restore another requirement's stored body", async () => {
  sessionStorage.setItem("cleardev-extra-planning-requests:other-requirement", JSON.stringify([savedBody]));
  mount();
  const button = await screen.findByRole("button", { name: "申请额外一次尝试（在原生窗口决定）" });
  await waitFor(() => expect(button).toBeEnabled());
  expect(screen.getByRole("textbox")).toBeEnabled();
  expect(screen.queryByRole("button", { name: "重试同一恢复请求" })).not.toBeInTheDocument();
  expect(sessionStorage.getItem("cleardev-extra-planning-requests:other-requirement")).toBe(JSON.stringify([savedBody]));
  expect(post).not.toHaveBeenCalled();
});

it.each([
  ["broken JSON", "{"],
  ["unknown field", JSON.stringify([{ ...savedBody, nonce: "invalid-test-field" }])],
  ["missing field", JSON.stringify([{ ...savedBody, supplement: undefined }])],
  ["other action", JSON.stringify([{ ...savedBody, action: "RETRY_PLANNING_STEP" }])],
  ["execution run", JSON.stringify([{ ...savedBody, executionRunId: "old-run" }])],
  ["long supplement", JSON.stringify([{ ...savedBody, supplement: "x".repeat(4001) }])],
  ["long target", JSON.stringify([{ ...savedBody, targetId: "x".repeat(513) }])],
  ["long request ID", JSON.stringify([{ ...savedBody, requestId: "x".repeat(201) }])],
  ["too many requests", JSON.stringify(Array.from({ length: 17 }, (_, index) => ({ ...savedBody, targetId: `target-${index}` })))],
  ["oversized storage", " ".repeat(512 * 1024 + 1)],
  ["ambiguous target", JSON.stringify([savedBody, { ...savedBody, requestId: "competing-unknown" }])],
])("ignores %s stored data without writing or erasing it", async (_name, encoded) => {
  sessionStorage.setItem(storageKey, encoded);
  mount();
  const button = await screen.findByRole("button", { name: "申请额外一次尝试（在原生窗口决定）" });
  await waitFor(() => expect(button).toBeEnabled());
  expect(screen.getByRole("textbox")).toBeEnabled();
  expect(screen.queryByRole("button", { name: "重试同一恢复请求" })).not.toBeInTheDocument();
  expect(sessionStorage.getItem(storageKey)).toBe(encoded);
  expect(post).not.toHaveBeenCalled();
});

it("retains the shared exact request when storage writes and reads throw", async () => {
  const write = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("storage unavailable"); });
  post.mockRejectedValue(new Error("unknown response"));
  const { rerender, ui, client } = mount();
  const button = await screen.findByRole("button", { name: "申请额外一次尝试（在原生窗口决定）" });
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "存储失败时保留的原补充" } });
  fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const original = { ...post.mock.calls[0][1].body };
  await waitFor(() => expect(screen.getByRole("button", { name: "重试同一恢复请求" })).toBeEnabled());
  write.mockRestore();
  const read = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("storage unavailable"); });
  rerender(ui(0)); rerender(ui(2));
  const retries = await screen.findAllByRole("button", { name: "重试同一恢复请求" });
  await waitFor(() => expect(retries[0]).toBeEnabled());
  expect(client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([original]);
  expect(post).toHaveBeenCalledTimes(1);
  read.mockRestore();
  post.mockResolvedValue({ data: { ...initial, options: [] } });
  await act(async () => { retries.forEach((retry) => fireEvent.click(retry)); });
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body).toEqual(original);
});

it("keeps REQUEST and CONTINUE identities distinct when the native decision overtakes a lost response", async () => {
  post.mockRejectedValueOnce(new Error("lost request response"));
  const { client } = mount();
  fireEvent.click(await screen.findByRole("button", { name: "申请额外一次尝试（在原生窗口决定）" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const oldRequest = post.mock.calls[0][1].body;
  get.mockResolvedValue({ data: { ...authorized, history: [{ ...requestHistory, id: oldRequest.requestId, supplement: oldRequest.supplement }] } });
  await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "req"] }); });
  const button = await screen.findByRole("button", { name: "使用已授权次数，编译原阶段规格" });
  await waitFor(() => expect(button).toBeEnabled());
  expect(screen.queryByRole("button", { name: "重试同一恢复请求" })).not.toBeInTheDocument();
  expect(post).toHaveBeenCalledTimes(1);
  post.mockResolvedValue({ data: registered }); fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body.action).toBe(continueAction);
  expect(post.mock.calls[1][1].body.requestId).not.toBe(oldRequest.requestId);
});

it("keeps an unresolved REQUEST without preventing a separately offered CONTINUE", async () => {
  post.mockRejectedValueOnce(new Error("unknown request result"));
  const { client } = mount();
  fireEvent.click(await screen.findByRole("button", { name: "申请额外一次尝试（在原生窗口决定）" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const original = post.mock.calls[0][1].body;
  get.mockResolvedValue({ data: { ...authorized, history: [] } });
  await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "req"] }); });
  const button = await screen.findByRole("button", { name: "使用已授权次数，编译原阶段规格" });
  await waitFor(() => expect(button).toBeEnabled());
  expect(client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([original]);
  post.mockResolvedValue({ data: registered }); fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body.action).toBe(continueAction);
});

it.each([requestAction, continueAction])("disables %s when facts expire, the parent changes or reads fail", async (action) => {
  const data = { ...initial, options: [{ ...option, action }] };
  get.mockResolvedValue({ data });
  const { client, rerender, ui } = mount();
  const label = action === requestAction ? "申请额外一次尝试（在原生窗口决定）" : "使用已授权次数，编译原阶段规格";
  const button = await screen.findByRole("button", { name: label });
  await act(async () => { client.setQueryData(["cleardev-recoveries", "req"], data, { updatedAt: Date.now() - 31_000 }); });
  await waitFor(() => expect(button).toBeDisabled()); fireEvent.click(button);
  rerender(ui(1, false)); expect(screen.queryByRole("button", { name: label })).not.toBeInTheDocument();
  rerender(ui()); get.mockRejectedValue(new Error("offline"));
  await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "req"] }); });
  expect(await screen.findByRole("alert")).toHaveTextContent("恢复入口读取失败");
  expect(screen.queryByRole("button", { name: label })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});
