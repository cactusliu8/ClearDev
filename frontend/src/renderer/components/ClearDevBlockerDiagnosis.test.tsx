import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { createAppI18n } from "../i18n/instance";
import enGuide from "../i18n/blockers.en.json";
import zhGuide from "../i18n/blockers.zh-CN.json";
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: (e: unknown) => e instanceof Error ? e.message : "request failed" }));
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";

type Diagnosis = components["schemas"]["ClearDevWorkflowDiagnosis"];
const diagnosis = (): Diagnosis => ({
  phase: "BLOCKED", observedAt: "2026-09-30T00:00:00Z", current: true, nextOwner: { role: "CONTROL_PLANE", action: "RECHECK_PREREQUISITES" },
  issues: [
    { id: "current", category: "SESSION", relationship: "CURRENT", reasonCode: "BUILDER_SPAWN_FAILED", aoSessionId: "original-session", evidence: [
      { kind: "TASK", factId: "task", value: "保留门店数据" }, { kind: "STEP_STATUS", factId: "step", value: "PENDING" },
      { kind: "SESSION_STATE", factId: "original-session", value: "exited" }, { kind: "RESERVED_MESSAGES", factId: "step", value: "0" },
      { kind: "CONFIRMED_MESSAGES", factId: "step", value: "0" }, { kind: "ROLE_USED", factId: "budget", value: "1" },
      { kind: "ROLE_MAX", factId: "budget", value: "4" }, { kind: "WORKSPACE", factId: "original-session", value: "/original/worktree" },
    ] },
    { id: "previous", category: "CHECK_FAILURE", relationship: "PRECEDING_FAILURE", reasonCode: "CHECK_FAILED", evidence: [
      { kind: "CANDIDATE", factId: "old-check", value: "old-candidate-sha" }, { kind: "CHECK_EXIT", factId: "old-check", value: "1" },
      { kind: "CHECK_OUTPUT", factId: "old-check", value: "mkdir /workspace/node_modules/better-sqlite3/build/Release failed", truncated: true },
    ] },
  ],
});
const option = { action: "RETRY_BUILDER_SESSION", targetId: "current-target", taskId: "task", role: "BUILDER", reason: "BUILDER_SPAWN_FAILED", summary: "" };
function mount(locale: "en" | "zh-CN" = "zh-CN") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const i18n = createAppI18n(locale), open = vi.fn();
  const ui = (current = true) => <I18nextProvider i18n={i18n}><QueryClientProvider client={client}><ClearDevWorkflowRecovery id="req" scope="execution" current={current} blocked onOpenSession={open} /></QueryClientProvider></I18nextProvider>;
  return { client, open, ui, ...render(ui()) };
}
beforeEach(() => { get.mockReset(); post.mockReset(); get.mockResolvedValue({ data: { executionRunId: "run", options: [option], history: [], diagnosis: diagnosis() } }); });

it.each(["en", "zh-CN"] as const)("explains the empty-summary session stop, evidence and risks in %s", async (locale) => {
  const words = locale === "en" ? enGuide : zhGuide;
  const { open } = mount(locale);
  expect(await screen.findByText(words["cleardevBlocker.title"])).toBeVisible();
  expect(screen.getByText(words["cleardevBlocker.SESSION.cause"], { exact: false })).toBeVisible();
  expect(screen.getByText(words["cleardevBlocker.noDetail"])).toBeVisible();
  expect(screen.getByText(words["cleardevBlocker.SESSION.steps"], { exact: false })).toBeVisible();
  expect(screen.getByText(words["cleardevBlocker.SESSION.risk"], { exact: false })).toBeVisible();
  expect(screen.getByText(words["cleardevBlocker.SESSION.done"], { exact: false })).toBeVisible();
  expect(screen.getByText(words["cleardevBlocker.value.exited"])).toBeVisible();
  expect(screen.getByText("/original/worktree")).toBeVisible();
  expect(screen.getByText("保留门店数据")).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: words["cleardevBlocker.openSession"] }));
  expect(open).toHaveBeenCalledWith("req", "original-session", words["cleardevBlocker.sessionTitle"]);
  expect(post).not.toHaveBeenCalled();
});

it("keeps the preceding candidate failure distinct and its output accessible", async () => {
  mount();
  await screen.findByText(zhGuide["cleardevBlocker.title"]);
  const previous = screen.getByText(new RegExp(zhGuide["cleardevBlocker.previous"]));
  fireEvent.click(previous);
  expect(screen.getByText(zhGuide["cleardevBlocker.previousNotice"])).toBeVisible();
  const facts = screen.getAllByText(zhGuide["cleardevBlocker.facts"]);
  fireEvent.click(facts[1]);
  expect(screen.getByText("mkdir /workspace/node_modules/better-sqlite3/build/Release failed")).toBeVisible();
  expect(screen.getByText("old-candidate-sha")).toBeVisible();
  expect(screen.getByText(zhGuide["cleardevBlocker.excerpt"])).toBeVisible();
  expect(post).not.toHaveBeenCalled();
});

it("does not claim the environment is repaired when technical recheck context is optional", async () => {
  mount();
  const retry = await screen.findByRole("button", { name: "重新检查并恢复原开发步骤" });
  expect(retry).toBeEnabled();
  expect(screen.getByRole("textbox")).toHaveValue("");
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "请先核对原会话恢复条件，保留已有代码；旧检查问题仍须返工。" } });
  await waitFor(() => expect(retry).toBeEnabled());
  expect(screen.queryByRole("button", { name: "环境已修复，重试原开发步骤" })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("shows evidence and unknown guidance even without any available operation", async () => {
  const data = diagnosis();
  data.issues = [{ id: "unknown", reasonCode: "UNCLASSIFIED_FUTURE_ERROR", category: "FUTURE", relationship: "CURRENT", evidence: [] }];
  get.mockResolvedValue({ data: { executionRunId: "run", options: [], history: [], diagnosis: data } });
  mount();
  expect(await screen.findByText(zhGuide["cleardevBlocker.UNKNOWN.cause"], { exact: false })).toBeVisible();
  expect(screen.getByText(zhGuide["cleardevBlocker.noOperation"])).toBeVisible();
  expect(screen.getByText("UNCLASSIFIED_FUTURE_ERROR")).toBeVisible();
  expect(screen.queryByRole("button", { name: /恢复原开发步骤/ })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it.each(["DIAGNOSIS_UNAVAILABLE", "CURRENT_BINDINGS_CHANGED"])("disables writes when detailed facts report %s", async (readError) => {
  get.mockResolvedValue({ data: { executionRunId: "run", options: [option], history: [], diagnosis: { ...diagnosis(), current: false, readError } } });
  mount();
  expect(await screen.findByRole("button", { name: "重新检查并恢复原开发步骤" })).toBeDisabled();
  expect(screen.getAllByRole("alert").length).toBeGreaterThan(0);
  expect(screen.queryByText("/original/worktree")).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("removes the diagnosis recommendations when the parent source becomes stale", async () => {
  const { rerender, ui } = mount();
  expect(await screen.findByText(zhGuide["cleardevBlocker.SESSION.steps"], { exact: false })).toBeVisible();
  rerender(ui(false));
  expect(screen.queryByText(zhGuide["cleardevBlocker.SESSION.steps"], { exact: false })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "重新检查并恢复原开发步骤" })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("does not mistake an error loading recovery options for a lack of blockers", async () => {
  const { client } = mount();
  await screen.findByText(zhGuide["cleardevBlocker.title"]);
  get.mockRejectedValue(new Error("offline"));
  await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-recoveries", "req"] }); });
  expect(await screen.findByRole("alert")).toHaveTextContent("恢复入口读取失败");
  expect(screen.queryByText(zhGuide["cleardevBlocker.SESSION.steps"], { exact: false })).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("maintains exact English/Chinese guidance key and interpolation parity", () => {
  expect(Object.keys(zhGuide).sort()).toEqual(Object.keys(enGuide).sort());
  for (const key of Object.keys(enGuide) as (keyof typeof enGuide)[]) {
    expect(zhGuide[key].trim().length).toBeGreaterThan(0);
    expect([...zhGuide[key].matchAll(/{{([^}]+)}}/g)].map((m) => m[1]).sort()).toEqual([...enGuide[key].matchAll(/{{([^}]+)}}/g)].map((m) => m[1]).sort());
  }
  const categories = Object.keys(enGuide).filter((key) => /^cleardevBlocker\.[A-Z_]+\.title$/.test(key)).map((key) => key.split(".")[1]);
  expect(categories.length).toBeGreaterThan(20);
  for (const category of categories) {
    for (const part of ["title", "cause", "risk", "steps", "done"]) expect(enGuide).toHaveProperty(`cleardevBlocker.${category}.${part}`);
  }
});
