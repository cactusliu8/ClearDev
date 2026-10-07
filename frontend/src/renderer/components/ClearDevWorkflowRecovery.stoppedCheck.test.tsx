import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, expect, it, vi } from "vitest";
import { createAppI18n } from "../i18n/instance";
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: (e: unknown) => e instanceof Error ? e.message : "request failed" }));
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";

const option = { action: "REQUEST_STOPPED_CHECK_RECOVERY", targetId: "check:stopped:exact-context-digest", role: "CHECKER", reason: "CHECKER_UNAVAILABLE", summary: "Original Planner STOP, spent counter 9 and all role budgets remain unchanged." };
const supplement = "Request one same-candidate check after the preserved Planner STOP.";
function mount(locale: "en" | "zh-CN" = "zh-CN") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const i18n = createAppI18n(locale);
  return { client, i18n, ...render(<I18nextProvider i18n={i18n}><QueryClientProvider client={client}><ClearDevWorkflowRecovery id="requirement" scope="execution" blocked /></QueryClientProvider></I18nextProvider>) };
}
beforeEach(() => {
  get.mockReset(); post.mockReset(); sessionStorage.clear();
  get.mockResolvedValue({ data: { executionRunId: "run", options: [option], history: [] } });
});

it.each(["en", "zh-CN"] as const)("requests a native decision, not an approval or a send, in %s", async (locale) => {
  post.mockImplementation(async (_path, { body }) => {
    const data = { executionRunId: "run", options: [{ ...option, unavailableReason: "STOPPED_CHECK_DECISION_PENDING" }], history: [{ id: body.requestId, executionRunId: "run", action: option.action, targetId: option.targetId, stepId: "event:planner", bindingId: "original-planner", successorId: "native-decision", originalStatus: "STOP", originalReason: option.reason, originalSummary: option.summary, originalStoppedAt: "2026-10-03T04:45:11Z", createdAt: "2026-10-03T06:00:00Z", supplement: body.supplement }] };
    get.mockResolvedValue({ data });
    return { data };
  });
  const { i18n } = mount(locale);
  const button = await screen.findByRole("button", { name: i18n.t("cleardevRecovery.stoppedCheckRequest") });
  expect(button).toBeDisabled();
  expect(screen.getByText(i18n.t("cleardevRecovery.stoppedCheckHelp"))).toBeInTheDocument();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: supplement } });
  fireEvent.click(button); fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const [path, request] = post.mock.calls[0];
  expect(path).toBe("/api/v1/cleardev/requirements/{id}/recoveries");
  expect(Object.keys(request.body).sort()).toEqual(["requestId", "executionRunId", "action", "targetId", "supplement"].sort());
  expect(request.body).toMatchObject({ executionRunId: "run", action: option.action, targetId: option.targetId, supplement });
  await screen.findByText(i18n.t("cleardevRecovery.stoppedCheckPending"));
  expect(screen.queryByRole("button", { name: i18n.t("cleardevRecovery.stoppedCheckRequest") })).not.toBeInTheDocument();
  expect(screen.queryByText(i18n.t("cleardevRecovery.accepted"))).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: i18n.t("cleardevRecovery.requestBudget") })).not.toBeInTheDocument();
});

it.each(["STOPPED_CHECK_DECISION_PENDING", "STOPPED_CHECK_DECISION_REJECTED", "EXECUTION_NOT_CURRENT", "RESULT_NOT_SETTLED"])("cannot turn %s into another check or task-budget request", async (reason) => {
  get.mockResolvedValue({ data: { executionRunId: "run", options: [{ ...option, unavailableReason: reason }], history: [] } });
  const { i18n } = mount();
  await screen.findByText(i18n.t("cleardevRecovery.stoppedCheckRequest"));
  expect(screen.queryByRole("button", { name: i18n.t("cleardevRecovery.stoppedCheckRequest") })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: i18n.t("cleardevRecovery.requestBudget") })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("does not keep a pending-authority claim after the current offer disappears", async () => {
  let saved: Record<string, unknown> | undefined;
  post.mockImplementation(async (_path, { body }) => {
    saved = { id: body.requestId, executionRunId: "run", action: option.action, targetId: option.targetId, stepId: "event:planner", bindingId: "original-planner", successorId: "native-decision", originalStatus: "STOP", originalReason: option.reason, originalSummary: option.summary, originalStoppedAt: "2026-10-03T04:45:11Z", createdAt: "2026-10-03T06:00:00Z", supplement: body.supplement };
    const data = { executionRunId: "run", options: [{ ...option, unavailableReason: "STOPPED_CHECK_DECISION_PENDING" }], history: [saved] };
    get.mockResolvedValue({ data });
    return { data };
  });
  const { client, i18n } = mount();
  const button = await screen.findByRole("button", { name: i18n.t("cleardevRecovery.stoppedCheckRequest") });
  fireEvent.change(screen.getByRole("textbox"), { target: { value: supplement } });
  fireEvent.click(button);
  await screen.findByText(i18n.t("cleardevRecovery.stoppedCheckPending"));
  expect(saved).toBeDefined();
  // Approval removes the pending offer. A saved successful submission is not
  // current evidence of whether the native decision is still pending.
  get.mockResolvedValue({ data: { executionRunId: "run", options: [], history: [saved] } });
  await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "requirement"] });
  await waitFor(() => expect(screen.queryByText(i18n.t("cleardevRecovery.stoppedCheckPending"))).not.toBeInTheDocument());
  expect(screen.queryByText(i18n.t("cleardevRecovery.accepted"))).not.toBeInTheDocument();
  expect(screen.getByText(i18n.t("cleardevRecovery.stoppedCheckRecorded"))).toBeInTheDocument();
  expect(post).toHaveBeenCalledTimes(1);
});
