import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { I18nextProvider } from "react-i18next";
import { createAppI18n } from "../i18n/instance";

const { get, post, workspace } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), workspace: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true,
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed",
}));
vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceQuery: workspace,
}));

vi.mock("./ClearDevWorkflowRecovery", () => ({ ClearDevWorkflowRecovery: () => null }));

import { ClearDevStageWorkbench } from "./ClearDevStageWorkbench";

type Summary = components["schemas"]["ClearDevTrustedProgressSummary"];

const requirementId = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
const oldSha = "a".repeat(40);
function summary(hash = "old-facts"): Summary {
	return { developmentRequirementId: requirementId, phase: "VERIFYING", factSummarySha256: hash,
		tasks: [], pendingDecisions: [], blockers: [] } as unknown as Summary;
}
function detail() {
	return { trustedProgress: { factSummarySha256: "old-facts" }, complexExecution: {
		run: { id: "run" }, tasks: [], roleBindings: [
			{ id: "old-reviewer", role: "REVIEWER", aoSessionId: "old-session", status: "FAILED", reasonCode: "REVIEWER_UNAVAILABLE" },
		], finalReview: { id: "old-final", aoSessionId: "old-final-session", status: "SETTLED", verdict: "PASS", candidateCommitSha: oldSha },
		integration: { candidateCommitSha: oldSha, checkRunIds: [] },
	} };
}
function mount(client: QueryClient, facts = summary(), locale: "en" | "zh-CN" = "en") {
	return render(<I18nextProvider i18n={createAppI18n(locale)}><QueryClientProvider client={client}><ClearDevStageWorkbench summary={facts} onOpenSession={vi.fn()} /></QueryClientProvider></I18nextProvider>);
}

beforeEach(() => {
	get.mockReset();
	post.mockReset();
	workspace.mockReset();
	workspace.mockReturnValue({ isSuccess: false, isError: false, dataUpdatedAt: 0 });
});

describe("ClearDevStageWorkbench freshness", () => {
	it("returns only the final task for a current settled BLOCKED review and suppresses duplicate clicks", async () => {
		const facts = summary();
		facts.phase = "BLOCKED";
		facts.tasks = [{ developmentTaskId: "first", title: "First task", status: "REVIEW", current: true }, { developmentTaskId: "last", title: "Final task", status: "REVIEW", current: true }] as any;
		const value = detail() as any;
		value.trustedProgress.phase = "BLOCKED";
		value.complexExecution.tasks = [{ id: "m1", developmentTaskId: "first", ordinal: 0 }, { id: "m2", developmentTaskId: "last", ordinal: 1 }];
		value.complexExecution.finalReview = { id: "blocked-review", status: "SETTLED", verdict: "BLOCKED" };
		get.mockResolvedValue({ data: value });
		post.mockReturnValue(new Promise(() => {}));
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const view = mount(client, facts, "zh-CN");
		const button = await screen.findByRole("button", { name: "派发返工给开发者" });
		expect(screen.getAllByRole("button", { name: "派发返工给开发者" })).toHaveLength(1);
		fireEvent.click(button);
		fireEvent.click(button);
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post).toHaveBeenCalledWith("/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/rework", { params: { path: { id: requirementId, taskId: "last" } } });
		view.rerender(<I18nextProvider i18n={createAppI18n("zh-CN")}><QueryClientProvider client={client}><ClearDevStageWorkbench summary={summary("new-facts")} onOpenSession={vi.fn()} /></QueryClientProvider></I18nextProvider>);
		expect(screen.queryByRole("button", { name: "派发返工给开发者" })).not.toBeInTheDocument();
	});

	it("shows a verified task as awaiting overall delivery without marking it complete", async () => {
		const facts = summary();
		facts.phase = "DEVELOPING";
		facts.tasks = [{ developmentTaskId: "task-1", title: "Backend task", status: "REVIEW", current: true }] as any;
		const value = detail() as any;
		value.complexExecution.tasks = [{ id: "mapping-1", developmentTaskId: "task-1", taskKey: "backend", currentDispatchId: "dispatch-1", currentRound: 0 }];
		value.complexExecution.verifications = [{ complexExecutionTaskId: "mapping-1", dispatchId: "dispatch-1", round: 0 }];
		value.complexExecution.integration = undefined;
		get.mockResolvedValue({ data: value });
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const view = mount(client, facts, "zh-CN");
		expect(await screen.findByText("审核已通过，待整体交付")).toBeInTheDocument();
		expect(screen.getByText("工程任务 · 已完成 0/1")).toBeInTheDocument();
		expect(screen.queryByText("任务已完成")).not.toBeInTheDocument();
		view.rerender(<I18nextProvider i18n={createAppI18n("zh-CN")}><QueryClientProvider client={client}><ClearDevStageWorkbench summary={summary("new-facts")} onOpenSession={vi.fn()} /></QueryClientProvider></I18nextProvider>);
		expect(screen.queryByText("审核已通过，待整体交付")).not.toBeInTheDocument();
	});

	it("keeps Chinese idle Planner guidance and candidate freshness independent of translations", async () => {
		const value = detail();
		value.complexExecution.roleBindings.push({ id: "planner", role: "ENGINEERING_PLANNER", aoSessionId: "planner-session", status: "BOUND", reasonCode: "" });
		get.mockResolvedValueOnce({ data: value }).mockResolvedValue({ error: new Error("offline") });
		workspace.mockReturnValue({ isSuccess: true, isError: false, dataUpdatedAt: Date.now(), data: [{ sessions: [{ id: "planner-session", isTerminated: false, activity: { state: "idle" } }] }] });
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		mount(client, summary(), "zh-CN");
		expect(await screen.findByText(/AO 报告空闲 · 等待下一个规划步骤/)).toBeInTheDocument();
		expect(screen.getByText("当前步骤：任务审核中")).toBeInTheDocument();
		expect(screen.getByText(/当前角色失败 · 任务审核者: REVIEWER_UNAVAILABLE/)).toBeInTheDocument();
		expect(screen.getByText(/任务完成不代表阶段通过/)).toBeInTheDocument();
		expect(screen.getAllByRole("button", { name: "查看会话" }).length).toBeGreaterThan(0);
		await client.invalidateQueries({ queryKey: ["cleardev-operate", requirementId] }).catch(() => undefined);
		await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("无法读取任务、团队和交付详情：offline"));
		expect(screen.queryByText(oldSha)).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "查看会话" })).not.toBeInTheDocument();
	});

	it("removes cached current roles and delivery after a detail refresh fails", async () => {
		get.mockResolvedValueOnce({ data: detail() }).mockResolvedValue({ error: new Error("offline") });
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		mount(client);
		expect(await screen.findByText(/Current role failure · Task Reviewer: REVIEWER_UNAVAILABLE/)).toBeInTheDocument();
		expect(screen.getAllByText(oldSha).length).toBeGreaterThan(0);
		await client.invalidateQueries({ queryKey: ["cleardev-operate", requirementId] }).catch(() => undefined);
		await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("offline"));
		expect(screen.queryByText(/Current role failure/)).not.toBeInTheDocument();
		expect(screen.queryByText(oldSha)).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "View session" })).not.toBeInTheDocument();
	});

	it("does not attach an older detail to a newer trusted summary", async () => {
		get.mockResolvedValue({ data: detail() });
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const view = mount(client);
		expect(await screen.findByText(/Current role failure · Task Reviewer: REVIEWER_UNAVAILABLE/)).toBeInTheDocument();
		view.rerender(<QueryClientProvider client={client}><ClearDevStageWorkbench summary={summary("new-facts")} onOpenSession={vi.fn()} /></QueryClientProvider>);
		expect(screen.getByRole("alert")).toHaveTextContent("out of date");
		expect(screen.queryByText(oldSha)).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "View session" })).not.toBeInTheDocument();
	});

	it("requests the controlled builder recovery for the exact blocked task", async () => {
		const value = detail() as any;
		value.complexExecution.phaseReason = "BUILDER_BUDGET_EXHAUSTED";
		value.complexExecution.tasks = [{
			id: "mapping-1", developmentTaskId: "task-1", taskKey: "task-1", status: "BLOCKED",
			dependencyTaskKeys: [], executionPackageJson: JSON.stringify({ objective: "Recover the stopped builder round." }),
		}];
		const facts = summary();
		facts.phase = "BLOCKED";
		facts.tasks = [{ developmentTaskId: "task-1", title: "Recover task", status: "BLOCKED", current: true }] as any;
		get.mockResolvedValue({ data: value });
		post.mockResolvedValue({ data: value });
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		mount(client, facts, "zh-CN");
		const button = await screen.findByRole("button", { name: "申请本轮回控恢复（在原生窗口确认）" });
		fireEvent.click(button);
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post).toHaveBeenCalledWith(
			"/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/builder-turn-authorizations",
			{ params: { path: { id: requirementId, taskId: "task-1" } } },
		);
	});
});


describe("ClearDevStageWorkbench observation checkpoint", () => {
 it("shows a visible Chinese warning for an observation timeout instead of no-blocker guidance", async () => {
  const facts = summary();
  facts.controlledWork = [{ state: "OBSERVING", reasonCode: "OBSERVATION_TIMEOUT" }] as any;
  const value = detail();
  value.complexExecution.roleBindings = [];
  get.mockResolvedValue({ data: value });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mount(client, facts, "zh-CN");
  expect(await screen.findByRole("alert")).toHaveTextContent("结果观察已超过等待窗口");
  expect(screen.getByRole("alert")).toHaveTextContent("本轮结果尚未接收");
  await screen.findByRole("button", { name: "查看会话" });
  expect(screen.queryByText("尚未记录当前阻塞。")).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
 });
 it("keeps normal ongoing work free of observation warnings", async () => {
  const facts = summary();
  facts.controlledWork = [{ state: "OBSERVING", reasonCode: "" }] as any;
  const value = detail();
  value.complexExecution.roleBindings = [];
  get.mockResolvedValue({ data: value });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mount(client, facts);
  expect(await screen.findByText("No current blocker is recorded.")).toBeInTheDocument();
  expect(screen.queryByText(/Result observation exceeded/)).not.toBeInTheDocument();
 });
});

describe("ClearDevStageWorkbench check environment failure", () => {
 it("explains the infrastructure block and exposes its saved error without writing", async () => {
  const facts = summary(); facts.phase = "BLOCKED"; facts.reasonCode = "CHECKER_UNAVAILABLE";
  facts.blockers = [{ kind: "EXECUTION_BLOCKED", reasonCode: "CHECKER_UNAVAILABLE" }];
  const value: any = detail(); value.complexExecution.roleBindings = [];
  value.complexExecution.checkRuns = [{ id: "failed-check", status: "FAILED", reasonCode: "CHECKER_UNAVAILABLE", outputSummary: "project npm dependency preparation failed: ENOTCACHED" }];
  get.mockResolvedValue({ data: value });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mount(client, facts, "zh-CN");
  expect(await screen.findByRole("alert")).toHaveTextContent("代码检查尚未完成");
  expect(await screen.findByText("project npm dependency preparation failed: ENOTCACHED")).toBeInTheDocument();
  expect(screen.getByText("检查环境错误详情").closest("details")).not.toHaveAttribute("open");
  expect(post).not.toHaveBeenCalled();
 });
});
