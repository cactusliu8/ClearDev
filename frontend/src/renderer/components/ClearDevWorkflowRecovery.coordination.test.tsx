import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, expect, it, vi } from "vitest";
import { createAppI18n } from "../i18n/instance";
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: (e: unknown) => e instanceof Error ? e.message : "request failed" }));
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";

const option = { action: "RETRY_PLANNER_COORDINATION", targetId: "original-step:retry:digest", role: "ENGINEERING_PLANNER", reason: "PLANNER_COORDINATION_UNAVAILABLE", summary: "Original STOP remains recorded." };
const recovery = { id: "same-recorded-request", executionRunId: "run", action: option.action, targetId: option.targetId, stepId: "original-step", bindingId: "original-planner", successorId: "original-step:attempt:2", originalStatus: "STOP", originalReason: option.reason, originalSummary: option.summary, originalStoppedAt: "2026-10-02T18:23:33Z", createdAt: "2026-10-03T03:00:00Z", supplement: "The native tool-permission configuration was repaired." };
function mount(locale: "en" | "zh-CN" = "zh-CN") {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
 const i18n = createAppI18n(locale);
 return { i18n, ...render(<I18nextProvider i18n={i18n}><QueryClientProvider client={client}><ClearDevWorkflowRecovery id="requirement" scope="execution" blocked /></QueryClientProvider></I18nextProvider>) };
}
beforeEach(() => { get.mockReset(); post.mockReset(); get.mockResolvedValue({ data: { executionRunId: "run", options: [option], history: [] } }); post.mockResolvedValue({ data: { executionRunId: "run", options: [], history: [recovery] } }); });

it.each(["en", "zh-CN"] as const)("uses the original coordination target and asks for context in %s", async (locale) => {
 const { i18n } = mount(locale);
 const button = await screen.findByRole("button", { name: i18n.t("cleardevRecovery.coordination") });
 expect(button).toBeDisabled();
 expect(screen.getByText(i18n.t("cleardevRecovery.coordinationHelp"))).toBeInTheDocument();
 fireEvent.change(screen.getByRole("textbox"), { target: { value: recovery.supplement } });
 fireEvent.click(button); fireEvent.click(button);
 await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
 expect(post.mock.calls[0][0]).toBe("/api/v1/cleardev/requirements/{id}/recoveries");
 expect(post.mock.calls[0][1].body).toMatchObject({ executionRunId: "run", action: option.action, targetId: option.targetId, supplement: recovery.supplement });
 expect(post.mock.calls[0][1].body).not.toHaveProperty("approval");
});

it("continues a durably registered retry using its exact original request and supplement", async () => {
 get.mockResolvedValue({ data: { executionRunId: "run", options: [{ ...option, unavailableReason: "COORDINATION_RECOVERY_REGISTERED" }], history: [recovery] } });
 const { i18n } = mount();
 fireEvent.click(await screen.findByRole("button", { name: i18n.t("cleardevRecovery.resumeRegistered") }));
 await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
 expect(post.mock.calls[0][1].body).toEqual({ requestId: recovery.id, executionRunId: recovery.executionRunId, action: recovery.action, targetId: recovery.targetId, supplement: recovery.supplement });
});

it.each(["RESULT_NOT_SETTLED", "MESSAGE_BUDGET_EXHAUSTED", "PLANNER_COORDINATION_CONTEXT_CHANGED"])("does not convert %s into approval or a fresh attempt", async (reason) => {
 get.mockResolvedValue({ data: { executionRunId: "run", options: [{ ...option, unavailableReason: reason }], history: [recovery] } });
 const { i18n } = mount();
 await screen.findByText(i18n.t("cleardevRecovery.coordination"));
 expect(screen.queryByRole("button", { name: i18n.t("cleardevRecovery.coordination") })).not.toBeInTheDocument();
 expect(screen.queryByRole("button", { name: i18n.t("cleardevRecovery.requestBudget") })).not.toBeInTheDocument();
 expect(post).not.toHaveBeenCalled();
});

it("refuses a malformed or third-attempt registered receipt", async () => {
 get.mockResolvedValue({ data: { executionRunId: "run", options: [{ ...option, unavailableReason: "COORDINATION_RECOVERY_REGISTERED" }], history: [{ ...recovery, successorId: "original-step:attempt:3" }] } });
 const { i18n } = mount();
 await screen.findByText(i18n.t("cleardevRecovery.registrationMissing"));
 expect(screen.queryByRole("button", { name: i18n.t("cleardevRecovery.resumeRegistered") })).not.toBeInTheDocument();
 expect(post).not.toHaveBeenCalled();
});
