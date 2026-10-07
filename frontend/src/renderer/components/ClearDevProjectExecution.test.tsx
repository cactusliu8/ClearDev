import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nextProvider } from "react-i18next";
import { createAppI18n } from "../i18n/instance";
import type { components } from "../../api/schema";

const { post } = vi.hoisted(() => ({ post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed" }));
import { ClearDevProjectExecution } from "./ClearDevProjectExecution";

type Requirement = components["schemas"]["ClearDevRequirementView"];
// Focused rendering double: no AO runtime, model, or service acceptance is
// implied by these intentionally omitted, unrelated requirement collections.
function facts(): Requirement {
	return {
		requirement: { id: "req" },
		requirementVersions: [{ id: "version", status: "CONFIRMED", sha256: "b".repeat(64) }],
		complexPlanning: { plans: [{ id: "plan", version: 1, planSha256: "a".repeat(64), requirementVersionId: "version", requirementSha256: "b".repeat(64), planJson: '{"schemaVersion":3,"tasks":[]}' }] },
		trustedProgress: { currentRequirementVersionId: "version", phase: "BLOCKED", reasonCode: "PROJECT_EXECUTION_ADMISSION_REQUIRED", projectPlanning: {
			productId: "product", stageId: "stage", discussionId: "discussion", latestDiscussionId: "discussion", current: true, sourceCurrent: true,
			baseCommitSha: "c".repeat(40), planReady: true, executionAvailable: true, executionAdmitted: false,
			reasonCode: "PROJECT_EXECUTION_ADMISSION_REQUIRED", executionRuntime: { environment: "NODE_NPM_V1", prepareArgv: [], env: {}, hostVariable: "HOST", portVariable: "PORT", dataDirectoryVariable: "CLEARDEV_DATA_DIR", healthPath: "/", healthStatus: 200, healthTimeoutSeconds: 30 },
		} },
	} as unknown as Requirement;
}
function mount(view: Requirement, locale: "en" | "zh-CN" = "en") {
	const i18n = createAppI18n(locale);
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	const saved = vi.fn();
	const node = (value: Requirement) => <I18nextProvider i18n={i18n}><QueryClientProvider client={client}><ClearDevProjectExecution view={value} onSaved={saved} /></QueryClientProvider></I18nextProvider>;
	const mounted = render(node(view));
	return { saved, update: (value: Requirement) => mounted.rerender(node(value)) };
}
beforeEach(() => { post.mockReset(); post.mockResolvedValue({ data: facts() }); });

describe("Explicit generic execution admission", () => {
	it("shows Chinese runtime and retry guidance without changing admission authority", async () => {
		post.mockRejectedValueOnce(new Error("connection lost"));
		mount(facts(), "zh-CN");
		expect(screen.getByText("工程计划已保存。未经显式准入不会开始开发。")).toBeInTheDocument();
		expect(screen.getByTestId("project-admission-runtime")).toHaveTextContent("HTTP 健康检查：/ → 200；超时 30 秒");
		expect(screen.getByTestId("project-admission-runtime")).not.toHaveTextContent("{{");
		const start = screen.getByRole("button", { name: "开始项目执行" });
		fireEvent.click(start); fireEvent.click(start);
		const retry = await screen.findByRole("button", { name: "重试同一执行请求" });
		expect(screen.getByRole("alert")).toHaveTextContent("当前不代表检查通过或交付成功");
		expect(post).toHaveBeenCalledTimes(1);
		fireEvent.click(retry);
		await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
		expect(post.mock.calls[1][1].body).toEqual(post.mock.calls[0][1].body);
		expect(screen.queryByRole("button", { name: /批准|Approve/i })).not.toBeInTheDocument();
	});

	it("keeps unconfirmed Chinese specifications blocked rather than offering approval", () => {
		const view = facts();
		view.requirementVersions[0].status = "PENDING_CONFIRMATION";
		mount(view, "zh-CN");
		expect(screen.getByRole("button", { name: "开始项目执行" })).toBeDisabled();
		expect(screen.getByRole("alert")).toHaveTextContent("执行条件未满足");
		expect(post).not.toHaveBeenCalled();
	});

	it("never starts on rendering and submits one exact request on double click", async () => {
		mount(facts());
		expect(post).not.toHaveBeenCalled();
		expect(screen.getByTestId("project-admission-runtime")).toHaveTextContent("CLEARDEV_DATA_DIR");
		const start = screen.getByRole("button", { name: "Start project execution" });
		fireEvent.click(start); fireEvent.click(start);
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post.mock.calls[0]).toEqual(["/api/v1/cleardev/requirements/{id}/project-execution-runs", { params: { path: { id: "req" } }, body: {
			requestId: expect.any(String), planId: "plan", planSha256: "a".repeat(64), requirementSha256: "b".repeat(64), baseCommitSha: "c".repeat(40),
		} }]);
		expect(screen.queryByRole("button", { name: /approve/i })).not.toBeInTheDocument();
	});

	it("retries an unknown response with the identical request and no automatic retry", async () => {
		post.mockRejectedValueOnce(new Error("connection lost"));
		mount(facts());
		fireEvent.click(screen.getByRole("button", { name: "Start project execution" }));
		const retry = await screen.findByRole("button", { name: "Retry the same execution request" });
		expect(post).toHaveBeenCalledTimes(1);
		fireEvent.click(retry);
		await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
		expect(post.mock.calls[1][1].body).toEqual(post.mock.calls[0][1].body);
	});

	it.each(["source", "discussion", "capability", "plan", "specification", "other-blocker"])("blocks a missing or stale %s without a write", (failure) => {
		const view = facts();
		const state = view.trustedProgress.projectPlanning!;
		if (failure === "source") state.sourceCurrent = false;
		if (failure === "discussion") state.current = false;
		if (failure === "capability") state.executionAvailable = false;
		if (failure === "plan") state.planReady = false;
		if (failure === "specification") view.requirementVersions[0].status = "PENDING_CONFIRMATION";
		if (failure === "other-blocker") view.trustedProgress.reasonCode = "PRODUCT_CANCELLED";
		mount(view);
		expect(screen.getByRole("button", { name: "Start project execution" })).toBeDisabled();
		expect(post).not.toHaveBeenCalled();
	});

	it("does not rebind an unknown request to a changed plan", async () => {
		post.mockRejectedValueOnce(new Error("unknown response"));
		const view = facts();
		const mounted = mount(view);
		fireEvent.click(screen.getByRole("button", { name: "Start project execution" }));
		await screen.findByRole("button", { name: "Retry the same execution request" });
		const changed = structuredClone(view);
		changed.complexPlanning!.plans![0].planSha256 = "d".repeat(64);
		mounted.update(changed);
		expect(screen.getByRole("button", { name: "Retry the same execution request" })).toBeDisabled();
		expect(post).toHaveBeenCalledTimes(1);
	});

	it("uses persisted admission/completion rather than a successful mutation as delivery", () => {
		const view = facts();
		view.trustedProgress.projectPlanning!.executionAdmitted = true;
		view.trustedProgress.projectPlanning!.executionRunId = "run-1";
		view.trustedProgress.phase = "VERIFYING";
		const mounted = mount(view);
		expect(screen.queryByRole("button", { name: "Start project execution" })).not.toBeInTheDocument();
		expect(screen.getByRole("status")).toHaveTextContent("Execution admitted");
		view.trustedProgress.phase = "COMPLETED";
		mounted.update(view);
		expect(screen.getByRole("status")).toHaveTextContent("completed after final review");
		expect(post).not.toHaveBeenCalled();
	});
});

it("shows one-shot trial operations without promising an HTTP health check", () => {
 const view=facts();
 view.trustedProgress.projectPlanning!.executionBasis={writePaths:["src/**"],dependencyNeeds:[],checks:[{id:"project-tests",argv:["npm","test"],timeoutSeconds:120,mainPaths:["src/**"]}],launch:{argv:[],workingDirectory:".",description:"一次性任务统计"},trial:{schemaVersion:1,service:false,steps:[{id:"sample",kind:"COMMAND",acceptanceCriteria:["统计正确"],argv:["node","src/taskstat.mjs"],timeoutSeconds:30,expectedExitCode:0,observe:"核对三行统计结果",outputFiles:["report.txt"]}]}};
 mount(view,"zh-CN");
 expect(screen.getByTestId("project-admission-runtime")).not.toHaveTextContent("HTTP 健康检查：");
 expect(screen.getByText("本阶段采用命令试用，不等待 HTTP 健康检查。")).toBeInTheDocument();
 expect(screen.getByText("检查生成文件：report.txt")).toBeInTheDocument();
 expect(post).not.toHaveBeenCalled();
});
