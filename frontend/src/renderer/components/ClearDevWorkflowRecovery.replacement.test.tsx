import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, expect, it, vi } from "vitest";
import { createAppI18n } from "../i18n/instance";
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed" }));
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";

const requestAction = "REQUEST_BUILDER_REPLACEMENT";
const continueAction = "CONTINUE_BUILDER_REPLACEMENT";
const run = "current-execution";
const target = "original-dispatch:replacement:exact-binding";
const storageKey = "cleardev-builder-replacement-requests:req";
const option = { action: requestAction, targetId: target, taskId: "original-task", role: "BUILDER", reason: "BUILDER_SESSION_UNAVAILABLE", summary: "Keep the original task, files and budget." };
const initial = { executionRunId: run, options: [option], history: [] };
const requestHistory = { id: "saved-replacement-request", action: requestAction, executionRunId: run, targetId: target, taskId: option.taskId, stepId: "original-step", bindingId: "original-builder", successorId: "exact-native-decision", originalStatus: "FAILED", originalReason: option.reason, originalSummary: option.summary, supplement: "原申请补充", createdAt: "2026-09-30T10:00:00Z", originalStoppedAt: "2026-09-30T09:00:00Z" };
const continuationHistory = { ...requestHistory, id: "saved-replacement-continuation", action: continueAction, successorId: "exact-handoff", supplement: "原交接续行补充" };
const authorized = { ...initial, options: [{ ...option, action: continueAction }], history: [requestHistory] };
const registered = { ...authorized, options: [{ ...option, action: continueAction, unavailableReason: "CONTINUATION_REGISTERED" }], history: [requestHistory, continuationHistory] };
function mount(locale: "en" | "zh-CN" = "zh-CN", copies = 1) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const i18n = createAppI18n(locale);
  const ui = (count = copies, current = true, sourceKey?: string) => <I18nextProvider i18n={i18n}><QueryClientProvider client={client}>{Array.from({ length: count }, (_, index) => <ClearDevWorkflowRecovery key={index} id="req" scope="execution" current={current} sourceKey={sourceKey} blocked />)}</QueryClientProvider></I18nextProvider>;
  return { client, i18n, ui, ...render(ui()) };
}
beforeEach(() => { vi.restoreAllMocks(); get.mockReset(); post.mockReset(); sessionStorage.clear(); get.mockResolvedValue({ data: initial }); });

it.each([
  ["en", "Request a replacement worker (decide in the native window)", "Decide in the native window"],
  ["zh-CN", "申请更换工作者（在原生窗口决定）", "请本人在原生窗口决定"],
] as const)("requests only five fields in %s without claiming a new worker or continuation", async (locale, label, waiting) => {
  post.mockImplementation(async (_path, { body }) => {
    const data = { ...initial, options: [], history: [{ ...requestHistory, id: body.requestId, supplement: "" }] };
    get.mockResolvedValue({ data });
    return { data };
  });
  mount(locale);
  fireEvent.click(await screen.findByRole("button", { name: label }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const [path, { body }] = post.mock.calls[0];
  expect(path).toBe("/api/v1/cleardev/requirements/{id}/recoveries");
  expect(Object.keys(body).sort()).toEqual(["action", "executionRunId", "requestId", "supplement", "targetId"]);
  expect(body).toMatchObject({ action: requestAction, executionRunId: run, targetId: target, supplement: "" });
  expect(await screen.findByRole("status")).toHaveTextContent(waiting);
  expect(screen.queryByText(/Processing will continue|正在继续处理/)).not.toBeInTheDocument();
  expect(sessionStorage.getItem(storageKey)).toBeNull();
});

it.each([
  ["en", "Continue with the authorized replacement worker"],
  ["zh-CN", "按已授权交接继续"],
] as const)("requires a separate explicit continuation in %s after the grant", async (locale, label) => {
  get.mockResolvedValue({ data: authorized });
  post.mockImplementation(async (_path, { body }) => {
    const data = { ...authorized, options: [], history: [requestHistory, { ...continuationHistory, id: body.requestId, supplement: body.supplement }] };
    get.mockResolvedValue({ data }); return { data };
  });
  mount(locale);
  const button = await screen.findByRole("button", { name: label });
  expect(post).not.toHaveBeenCalled();
  fireEvent.click(button); fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body).toMatchObject({ action: continueAction, executionRunId: run, targetId: target, supplement: "" });
  expect(post.mock.calls[0][1].body.requestId).not.toBe(requestHistory.id);
  expect(await screen.findByRole("status")).not.toHaveTextContent(/Processing will continue|正在继续处理/);
});

it.each([requestAction, continueAction])("keeps a lost %s body through actual unmount, cleared cache and a fresh QueryClient", async (action) => {
  get.mockResolvedValue({ data: action === requestAction ? initial : authorized });
  post.mockRejectedValue(new Error("unknown response"));
  const first = mount();
  const button = await screen.findByRole("button", { name: action === requestAction ? "申请更换工作者（在原生窗口决定）" : "按已授权交接继续" });
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "这条请求的原始非空补充" } });
  fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const body = { ...post.mock.calls[0][1].body };
  await waitFor(() => expect(screen.getByRole("button", { name: "重试同一恢复请求" })).toBeEnabled());
  expect(JSON.parse(sessionStorage.getItem(storageKey)!)).toEqual([body]);
  first.unmount(); first.client.clear();
  const second = mount();
  const retry = await screen.findByRole("button", { name: "重试同一恢复请求" });
  await waitFor(() => expect(retry).toBeEnabled());
  expect(second.client).not.toBe(first.client);
  expect(second.client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([body]);
  expect(screen.getByRole("textbox")).toBeDisabled();
  expect(post).toHaveBeenCalledTimes(1);
  post.mockImplementation(async () => {
    const data = { ...initial, options: [], history: [{ ...requestHistory, ...body, id: body.requestId, successorId: "saved-exact-successor" }] };
    get.mockResolvedValue({ data }); return { data };
  });
  fireEvent.click(retry);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body).toEqual(body);
  await waitFor(() => expect(sessionStorage.getItem(storageKey)).toBeNull());
  expect(second.client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([]);
});

it("saves all five fields before POST and shares the lock between two panels", async () => {
  post.mockImplementation(async (_path, { body }) => {
    expect(JSON.parse(sessionStorage.getItem(storageKey)!)).toEqual([body]);
    throw new Error("unknown response");
  });
  mount("zh-CN", 2);
  const buttons = await screen.findAllByRole("button", { name: "申请更换工作者（在原生窗口决定）" });
  fireEvent.click(buttons[0]); fireEvent.click(buttons[1]);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const retries = await screen.findAllByRole("button", { name: "重试同一恢复请求" });
  await waitFor(() => expect(retries[0]).toBeEnabled());
  fireEvent.click(retries[0]); fireEvent.click(retries[1]);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body).toEqual(post.mock.calls[0][1].body);
});

it("replays the exact CONTINUE history and never the allowance request", async () => {
  get.mockResolvedValue({ data: registered }); post.mockResolvedValue({ data: registered });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "继续已登记的工作交接" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body).toEqual({ requestId: continuationHistory.id, executionRunId: run, action: continueAction, targetId: target, supplement: continuationHistory.supplement });
});

it.each(["missing", "ambiguous", "different run"])("does not invent a continuation when saved history is %s", async (kind) => {
  const history = kind === "missing" ? [requestHistory] : kind === "ambiguous" ? [requestHistory, continuationHistory, { ...continuationHistory, id: "other-continuation" }] : [requestHistory, { ...continuationHistory, executionRunId: "old-run" }];
  get.mockResolvedValue({ data: { ...registered, history } });
  mount();
  expect(await screen.findByText(/恢复登记与历史尚未对应/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "继续已登记的工作交接" })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

const savedBody = { requestId: "unknown-replacement", executionRunId: run, action: requestAction, targetId: target, supplement: "原目标的补充" };
it("keeps an unknown old source isolated while permitting a different current execution", async () => {
  sessionStorage.setItem(storageKey, JSON.stringify([savedBody]));
  get.mockResolvedValue({ data: { ...initial, executionRunId: "new-execution", options: [{ ...option, targetId: "new-exact-target" }] } });
  post.mockRejectedValue(new Error("unknown response"));
  const { client } = mount();
  const button = await screen.findByRole("button", { name: "申请更换工作者（在原生窗口决定）" });
  await waitFor(() => expect(button).toBeEnabled());
  expect(screen.queryByRole("button", { name: "重试同一恢复请求" })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "新目标独立补充" } }); fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const next = post.mock.calls[0][1].body;
  expect(next).toMatchObject({ executionRunId: "new-execution", action: requestAction, targetId: "new-exact-target", supplement: "新目标独立补充" });
  expect(next.requestId).not.toBe(savedBody.requestId);
  expect(client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([savedBody, next]);
  expect(JSON.parse(sessionStorage.getItem(storageKey)!)).toEqual([savedBody, next]);
});

it("clears a losing unknown request only when exact current history has a distinct committed winner", async () => {
  sessionStorage.setItem(storageKey, JSON.stringify([savedBody]));
  get.mockResolvedValue({ data: { ...initial, options: [], history: [requestHistory] } });
  const { client } = mount();
  await screen.findByText(option.summary);
  await waitFor(() => expect(sessionStorage.getItem(storageKey)).toBeNull());
  expect(client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([]);
  expect(screen.queryByRole("status")).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it.each([
  ["broken JSON", "{"],
  ["unknown field", JSON.stringify([{ ...savedBody, nonce: "must-not-be-stored" }])],
  ["missing field", JSON.stringify([{ ...savedBody, supplement: undefined }])],
  ["other action", JSON.stringify([{ ...savedBody, action: "RETRY_BUILDER_SESSION" }])],
  ["empty run", JSON.stringify([{ ...savedBody, executionRunId: "" }])],
  ["long run", JSON.stringify([{ ...savedBody, executionRunId: "x".repeat(201) }])],
  ["long supplement", JSON.stringify([{ ...savedBody, supplement: "x".repeat(16001) }])],
  ["long target", JSON.stringify([{ ...savedBody, targetId: "x".repeat(601) }])],
  ["long request ID", JSON.stringify([{ ...savedBody, requestId: "x".repeat(101) }])],
  ["too many requests", JSON.stringify(Array.from({ length: 17 }, (_, index) => ({ ...savedBody, targetId: `target-${index}` })))],
  ["oversized storage", " ".repeat(512 * 1024 + 1)],
  ["ambiguous target", JSON.stringify([savedBody, { ...savedBody, requestId: "another-unknown" }])],
])("ignores %s storage without approving, posting or erasing it", async (_name, encoded) => {
  sessionStorage.setItem(storageKey, encoded);
  mount();
  const button = await screen.findByRole("button", { name: "申请更换工作者（在原生窗口决定）" });
  await waitFor(() => expect(button).toBeEnabled());
  expect(screen.getByRole("textbox")).toBeEnabled();
  expect(screen.queryByRole("button", { name: "重试同一恢复请求" })).not.toBeInTheDocument();
  expect(sessionStorage.getItem(storageKey)).toBe(encoded);
  expect(post).not.toHaveBeenCalled();
});

it("keeps the exact shared request when storage is unavailable", async () => {
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("storage unavailable"); });
  post.mockRejectedValue(new Error("unknown response"));
  const { client, rerender, ui } = mount();
  fireEvent.click(await screen.findByRole("button", { name: "申请更换工作者（在原生窗口决定）" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const body = post.mock.calls[0][1].body;
  await waitFor(() => expect(screen.getByRole("button", { name: "重试同一恢复请求" })).toBeEnabled());
  rerender(ui(0)); rerender(ui(2));
  const retries = await screen.findAllByRole("button", { name: "重试同一恢复请求" });
  await waitFor(() => expect(retries[0]).toBeEnabled());
  expect(client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([body]);
  expect(post).toHaveBeenCalledTimes(1);
});

const replacement = {
  id: "handoff-request", targetId: target, executionRunId: run, taskId: option.taskId, dispatchId: "original-dispatch", stepId: requestHistory.stepId,
  decisionRequestId: requestHistory.successorId, continueRequestId: "", oldRoleBindingId: requestHistory.bindingId, oldAOSessionId: "original-ao-session",
  oldWorkspacePath: "/managed/original", oldHeadSha: "a".repeat(40), snapshotSha256: "b".repeat(64), snapshotPath: "/isolated/sealed-code",
  fileCount: 2, totalBytes: 73, newRoleBindingId: "replacement-role", newAOSessionId: "", newWorkspacePath: "", state: "PENDING_DECISION",
  lastStage: "SOURCE", lastOutcome: "READY", reasonCode: "", budgetSummary: "Builder: 1/3; messages: 0/9",
};
it.each(["en", "zh-CN"] as const)("confirms the accepted continuation is handed off in %s without claiming it is still unsent", async (locale) => {
  get.mockResolvedValue({ data: authorized });
  post.mockImplementation(async (_path, { body }) => {
    const data = { ...authorized, options: [], history: [requestHistory, { ...continuationHistory, id: body.requestId, supplement: body.supplement }], builderReplacements: [{ ...replacement, continueRequestId: body.requestId, state: "HANDED_OFF", lastStage: "SEND", lastOutcome: "CONFIRMED", newAOSessionId: "actual-new-session", newWorkspacePath: "/managed/replacement" }] };
    get.mockResolvedValue({ data }); return { data };
  });
  const { i18n } = mount(locale);
  fireEvent.click(await screen.findByRole("button", { name: i18n.t("cleardevReplacement.continue") }));
  expect(await screen.findByText(i18n.t("cleardevReplacement.state.HANDED_OFF"))).toBeInTheDocument();
  await waitFor(() => expect(screen.queryByText(i18n.t("cleardevReplacement.registered"))).not.toBeInTheDocument());
  expect(post).toHaveBeenCalledTimes(1);
});

it.each(["REGISTERED", "COPY_FAILED", "UNKNOWN", "different target", "different run", "different request", "different body", "different current execution"])(
  "keeps the accepted continuation unconfirmed when public facts are %s", async (kind) => {
    get.mockResolvedValue({ data: authorized });
    post.mockImplementation(async (_path, { body }) => {
      const data = { ...authorized, executionRunId: kind === "different current execution" ? "another-execution" : run, options: [],
        history: [requestHistory, { ...continuationHistory, id: body.requestId, supplement: kind === "different body" ? "a different recorded explanation" : body.supplement }],
        builderReplacements: [{ ...replacement, state: ["REGISTERED", "COPY_FAILED", "UNKNOWN"].includes(kind) ? kind : "HANDED_OFF",
          executionRunId: kind === "different run" ? "old-execution" : run, targetId: kind === "different target" ? "another-target" : target,
          continueRequestId: kind === "different request" ? "another-continuation" : body.requestId }] };
      get.mockResolvedValue({ data }); return { data };
    });
    const { i18n } = mount();
    fireEvent.click(await screen.findByRole("button", { name: i18n.t("cleardevReplacement.continue") }));
    expect(await screen.findByTestId("cleardev-replacement-registered")).toHaveTextContent(i18n.t("cleardevReplacement.registered"));
    expect(post).toHaveBeenCalledTimes(1);
  },
);

it("does not use stale handed-off query data to confirm an accepted continuation", async () => {
  get.mockResolvedValue({ data: authorized });
  let completed: typeof authorized;
  post.mockImplementation(async (_path, { body }) => {
    const data = { ...authorized, options: [], history: [requestHistory, { ...continuationHistory, id: body.requestId, supplement: body.supplement }], builderReplacements: [{ ...replacement, continueRequestId: body.requestId, state: "HANDED_OFF" }] };
    completed = data; get.mockResolvedValue({ data }); return { data };
  });
  const { client, i18n } = mount();
  fireEvent.click(await screen.findByRole("button", { name: i18n.t("cleardevReplacement.continue") }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(client.isMutating()).toBe(0));
  await waitFor(() => expect(screen.queryByText(i18n.t("cleardevReplacement.registered"))).not.toBeInTheDocument());
  await act(async () => { client.setQueryData(["cleardev-recoveries", "req"], completed!, { updatedAt: Date.now() - 31_000 }); });
  expect(await screen.findByTestId("cleardev-replacement-registered")).toHaveTextContent(i18n.t("cleardevReplacement.registered"));
});

it("keeps an unknown continuation body despite another target's handed-off history", async () => {
  get.mockResolvedValue({ data: authorized });
  post.mockImplementation(async () => {
    get.mockResolvedValue({ data: { ...authorized, history: [requestHistory, { ...continuationHistory, targetId: "old-target" }], builderReplacements: [{ ...replacement, targetId: "old-target", continueRequestId: continuationHistory.id, state: "HANDED_OFF" }] } });
    throw new Error("unknown response");
  });
  const { client, i18n } = mount();
  fireEvent.click(await screen.findByRole("button", { name: i18n.t("cleardevReplacement.continue") }));
  await waitFor(() => expect(screen.getByRole("button", { name: i18n.t("cleardevRecovery.retryRequest") })).toBeEnabled());
  const body = post.mock.calls[0][1].body;
  expect(screen.getByText(i18n.t("cleardevRecovery.requestUnconfirmed"))).toBeInTheDocument();
  expect(client.getQueryData(["cleardev-recovery-pending-requests", "req"])).toEqual([body]);
  expect(JSON.parse(sessionStorage.getItem(storageKey)!)).toEqual([body]);
  expect(post).toHaveBeenCalledTimes(1);
});

it.each(["en", "zh-CN"] as const)("shows saved identities, code and all handoff states in %s without deriving a write from them", async (locale) => {
  get.mockResolvedValue({ data: { ...initial, options: [], history: [requestHistory], builderReplacements: [replacement] } });
  const { client, i18n } = mount(locale);
  const section = await screen.findByTestId("cleardev-builder-replacement");
  expect(within(section).getByText(replacement.oldAOSessionId)).toBeInTheDocument();
  expect(within(section).getByText(replacement.oldWorkspacePath)).toBeInTheDocument();
  expect(within(section).getByText(replacement.snapshotPath)).toBeInTheDocument();
  expect(within(section).getByText(replacement.budgetSummary)).toBeInTheDocument();
  for (const state of ["PENDING_DECISION", "APPROVED_AWAITING_CONTINUE", "REGISTERED", "CREATE_FAILED", "COPY_FAILED", "UNKNOWN", "HANDED_OFF"] as const) {
    get.mockResolvedValue({ data: { ...initial, options: [], history: [requestHistory], builderReplacements: [{ ...replacement, state, newAOSessionId: state === "HANDED_OFF" ? "actual-new-session" : "", newWorkspacePath: state === "HANDED_OFF" ? "/managed/replacement" : "" }] } });
    await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "req"] }); });
    expect(await within(section).findByText(i18n.t(`cleardevReplacement.state.${state}`))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /申请更换|按已授权|Request a replacement|Continue with/ })).not.toBeInTheDocument();
  }
  expect(within(section).getByText("actual-new-session")).toBeInTheDocument();
  expect(within(section).getByText("/managed/replacement")).toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("marks the original READY receipt as history when an exact replacement exists", async () => {
  const oldCheck = { ...requestHistory, id: "old-check", action: "RETRY_BUILDER_SESSION", successorId: "unused", targetId: "old-recheck-target" };
  get.mockResolvedValue({ data: { ...initial, history: [oldCheck, requestHistory], builderReplacements: [replacement], builderSessionChecks: [{ recoveryId: oldCheck.id, checkpoint: "FINISHED", stage: "READY", outcome: "READY", reasonCode: "", checkedAt: "2026-09-30T09:00:00Z" }] } });
  mount();
  expect(await screen.findByText(/旧 READY 不证明替代工作者已创建或接手/)).toBeInTheDocument();
  expect(screen.queryByText(/新工作者已接手这条任务消息/)).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it.each([
  ["BUILDER_REPLACEMENT_DECISION_PENDING", /请本人在原生窗口决定/],
  ["BUILDER_REPLACEMENT_DECISION_REJECTED", /更换工作者申请已被拒绝/],
  ["BUILDER_LOSS_NOT_CONFIRMED", /尚未确认原工作者已丢失/],
  ["BUILDER_OPERATION_PENDING", /原工作者仍有操作待核对/],
  ["BUILDER_REPLACED", /原步骤的一次更换已使用/],
] as const)("explains %s without enabling either new handoff action", async (reason, text) => {
  get.mockResolvedValue({ data: { ...initial, options: [{ ...option, unavailableReason: reason }], history: [requestHistory] } });
  mount();
  expect(await screen.findByText(text)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /申请更换|按已授权/ })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it.each(["parent", "diagnosis", "read error"])("blocks both handoff writes when %s is stale or unavailable", async (kind) => {
  get.mockResolvedValue({ data: kind === "parent" ? authorized : { ...authorized, diagnosis: { current: kind !== "diagnosis", readError: kind === "read error" ? "DIAGNOSIS_UNAVAILABLE" : "", issues: [] } } });
  const { rerender, ui, i18n } = mount();
  if (kind === "parent") rerender(ui(1, false));
  if (kind === "parent") expect(await screen.findByRole("alert")).toHaveTextContent(i18n.t("cleardevRecovery.stale"));
  else expect(await screen.findByRole("button", { name: "按已授权交接继续" })).toBeDisabled();
  expect(post).not.toHaveBeenCalled();
});
