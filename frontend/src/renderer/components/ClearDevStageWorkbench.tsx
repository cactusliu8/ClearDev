import { useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import type { MessageKey } from "../i18n/messages";
import { apiClient, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import type { ClearDevTrustedProgressSummary } from "../lib/cleardev-progress";
import { currentExecution, deriveWorkbench } from "../lib/cleardev-workbench";
import { hasCurrentFacts, useFreshnessClock } from "../lib/cleardev-freshness";
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";
import { ClearDevPreflight } from "./ClearDevExecutionChoice";
import { Button } from "./ui/button";

type Props = {
	summary: ClearDevTrustedProgressSummary;
	onOpenSession: (requirementId: string, sessionId: string, title: string) => void;
};

const roleNames: Record<string, MessageKey> = {
	STEWARD: "cleardevWorkbench.role.STEWARD", ENGINEERING_PLANNER: "cleardevWorkbench.role.PLANNER", PLANNER: "cleardevWorkbench.role.PLANNER",
	BUILDER: "cleardevWorkbench.role.BUILDER", REVIEWER: "cleardevWorkbench.role.REVIEWER", FINAL_REVIEWER: "cleardevWorkbench.role.FINAL_REVIEWER",
};

const taskNames: Record<string, MessageKey> = {
	PLANNED: "cleardevWorkbench.task.PLANNED", RUNNING: "cleardevWorkbench.task.RUNNING", REVIEW: "cleardevWorkbench.task.REVIEW",
	REWORK: "cleardevWorkbench.task.REWORK", NEEDS_HUMAN: "cleardevWorkbench.task.NEEDS_HUMAN", BLOCKED: "cleardevWorkbench.task.BLOCKED",
	DONE: "cleardevWorkbench.task.DONE", CANCELLED: "cleardevWorkbench.task.CANCELLED",
};

const phaseNames: Record<string, MessageKey> = {
	DEFINING_REQUIREMENT: "cleardevWorkbench.phase.DEFINING_REQUIREMENT", AWAITING_CLARIFICATION: "cleardevWorkbench.phase.AWAITING_CLARIFICATION",
	AWAITING_CONFIRMATION: "cleardevWorkbench.phase.AWAITING_CONFIRMATION", PLANNING: "cleardevWorkbench.phase.PLANNING",
	COORDINATING: "cleardevWorkbench.phase.COORDINATING", AWAITING_PLAN_REVIEW: "cleardevWorkbench.phase.AWAITING_PLAN_REVIEW",
	DEVELOPING: "cleardevWorkbench.phase.DEVELOPING", VERIFYING: "cleardevWorkbench.phase.VERIFYING", REWORKING: "cleardevWorkbench.phase.REWORKING",
	INTEGRATING: "cleardevWorkbench.phase.INTEGRATING", COMPLETED: "cleardevWorkbench.phase.COMPLETED", BLOCKED: "cleardevWorkbench.phase.BLOCKED",
};

function changedPaths(raw?: string): string[] {
	if (!raw) return [];
	try {
		const value: unknown = JSON.parse(raw);
		if (!Array.isArray(value)) return [];
		return value.flatMap((item) => item && typeof item === "object" && "path" in item && typeof item.path === "string" ? [item.path] : []);
	} catch { return []; }
}

export function ClearDevStageWorkbench({ summary, onOpenSession }: Props) {
	const { t } = useTranslation();
	const client = useQueryClient();
	const reworkInFlight = useRef(false);
	const budgetInFlight = useRef(false);
	const builderTurnInFlight = useRef(false);
	const roleName = (role: string) => roleNames[role] ? t(roleNames[role]) : role;
	const id = summary.developmentRequirementId;
	// The human DECIDE after a settled requirement final review: reuse the task's
	// existing rework budget and send the exact failed task back to its Builder.
	// One extra reviewer turn for a rework round needs the same explicit human
	// authorization the mail flow grants; this only records the request.
	const budgetAuthorization = useMutation({
		retry: false,
		mutationFn: async (taskId: string) => {
			const result = await apiClient.POST("/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/review-budget-authorizations", {
				params: { path: { id, taskId } },
			});
			if (result.error) throw result.error;
			return result.data;
		},
		onSettled: async () => {
			try { await client.invalidateQueries({ queryKey: ["cleardev-operate", id] }); }
			finally { budgetInFlight.current = false; }
		},
	});
	// Controlled recovery of a builder round that stopped because its rework
	// budget ran out: the request alone records the offer; only the native
	// Human Authority decision grants the extra turn.
	const builderTurnRecovery = useMutation({
		retry: false,
		mutationFn: async (taskId: string) => {
			const result = await apiClient.POST("/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/builder-turn-authorizations", {
				params: { path: { id, taskId } },
			});
			if (result.error) throw result.error;
			return result.data;
		},
		onSettled: async () => {
			try { await client.invalidateQueries({ queryKey: ["cleardev-operate", id] }); }
			finally { builderTurnInFlight.current = false; }
		},
	});
	const rework = useMutation({
		retry: false,
		mutationFn: async (taskId: string) => {
			const result = await apiClient.POST("/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/rework", {
				params: { path: { id, taskId } },
			});
			if (result.error) throw result.error;
			return result.data;
		},
		onSettled: async () => {
			try { await client.invalidateQueries({ queryKey: ["cleardev-operate", id] }); }
			finally { reworkInFlight.current = false; }
		},
	});
	const detail = useQuery({
		queryKey: ["cleardev-operate", id],
		enabled: hasTrustedApiBaseUrl(), retry: 1, refetchInterval: 3000,
		queryFn: async () => {
			const result = await apiClient.GET("/api/v1/cleardev/requirements/{id}", { params: { path: { id } } });
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevOperate.unavailable"));
			return result.data;
		},
	});
	const workspace = useWorkspaceQuery();
	const now = useFreshnessClock();
	const detailCurrent = hasCurrentFacts(detail, now) && detail.data?.trustedProgress.factSummarySha256 === summary.factSummarySha256;
	const model = detailCurrent && detail.data ? deriveWorkbench(detail.data, summary) : undefined;
	const currentRoleFailures = model?.roles.filter((role) => !role.historical && role.status === "FAILED") ?? [];
	const observationDelayed = summary.controlledWork?.some((work) => work.state === "OBSERVING" && work.reasonCode === "OBSERVATION_TIMEOUT") ?? false;
	const currentTasks = model?.tasks.filter((task) => task.current) ?? summary.tasks.filter((task) => task.current);
	const done = currentTasks.filter((task) => task.status === "DONE").length;
	const current = detailCurrent && detail.data ? currentExecution(detail.data) : undefined;
	const execution = current?.complex;
	const blockedFinalTaskId = execution?.finalReview?.status === "SETTLED" && execution.finalReview.verdict === "BLOCKED"
		? [...execution.tasks].sort((a, b) => b.ordinal - a.ordinal)[0]?.developmentTaskId : undefined;
	const checkerUnavailable = summary.reasonCode === "CHECKER_UNAVAILABLE";
	const checkerFailures = (execution?.checkRuns ?? []).filter((check) => check.status === "FAILED" && check.reasonCode === "CHECKER_UNAVAILABLE");
	const integration = execution?.integration ?? current?.quick?.integration;
	const deliveryChecks = (execution?.checkRuns ?? current?.quick?.checkRuns ?? []).filter((check) => integration?.checkRunIds?.includes(check.id));
	const deliveryPaths = [...new Set(deliveryChecks.flatMap((check) => changedPaths(check.changedPathsJson)))];
	// Keep state comparisons independent of the active language. Only the
	// returned key is translated at the rendering boundary.
	const sessionStatusKey = (sessionId?: string): MessageKey => {
		if (!sessionId) return "cleardevWorkbench.session.notStarted";
		if (!hasCurrentFacts(workspace, now)) return "cleardevWorkbench.session.statusUnavailable";
		const session = workspace.data?.flatMap((project) => project.sessions).find((item) => item.id === sessionId);
		if (!session) return "cleardevWorkbench.session.unavailable";
		if (session.isTerminated) return "cleardevWorkbench.session.ended";
		const state = session.activity?.state;
		if (state === "active") {
			const reportedAt = Date.parse(session.activity?.lastActivityAt ?? "");
			return Number.isFinite(reportedAt) && now - reportedAt <= 120_000 ? "cleardevWorkbench.session.active" : "cleardevWorkbench.session.stale";
		}
		if (state === "idle") return "cleardevWorkbench.session.idle";
		return "cleardevWorkbench.session.unknown";
	};

	return <section className="flex flex-col gap-4" data-testid="cleardev-stage-workbench">
		<div className="rounded-md border p-3 text-sm">
			<p className="font-medium">{t("cleardevWorkbench.currentStep", { phase: phaseNames[summary.phase] ? t(phaseNames[summary.phase]) : summary.phase })}</p>
			{observationDelayed ? <p className="text-warning" role="alert">{t("cleardevWorkbench.observationDelayed")}</p> : null}
			{summary.pendingDecisions.length ? <p className="text-warning">{t("cleardevWorkbench.decision", { decisions: summary.pendingDecisions.map((item) => item.kind).join(", ") })}</p> : null}
			{summary.blockers.length ? <p className="text-destructive">{t("cleardevWorkbench.blocker", { blockers: summary.blockers.map((item) => item.kind).join(", ") })}</p> : null}
			{checkerUnavailable ? <div className="mt-2 rounded-md border border-warning/40 bg-warning/5 p-3" role="alert">
				<p>{t("cleardevWorkbench.checkerUnavailable")}</p>
				<details className="mt-2">
					<summary className="cursor-pointer">{t("cleardevWorkbench.checkerDetails")}</summary>
					{checkerFailures.length ? checkerFailures.map((check) => <pre key={check.id} className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-words text-xs">{check.outputSummary || t("cleardevWorkbench.checkerNoDetails")}</pre>) : <p className="mt-2 text-muted-foreground">{t("cleardevWorkbench.checkerNoDetails")}</p>}
				</details>
			</div> : null}
			{currentRoleFailures.length ? <p className="text-destructive" role="alert">{t("cleardevWorkbench.roleFailure", { failures: currentRoleFailures.map((role) => `${roleName(role.role)}: ${role.reasonCode ?? t("cleardevWorkbench.reasonUnavailable")}`).join(", ") })}</p> : null}
			{summary.phase === "REWORKING" ? <p>{t("cleardevWorkbench.repair")}</p> : null}
			{!observationDelayed && !summary.pendingDecisions.length && !summary.blockers.length && !currentRoleFailures.length && detailCurrent && summary.phase !== "REWORKING" ? <p className="text-muted-foreground">{t("cleardevWorkbench.noBlocker")}</p> : null}
			<p className="font-medium">{t("cleardevWorkbench.tasksComplete", { done, total: currentTasks.length })}</p>
			<p className="text-muted-foreground">{t(summary.phase === "COMPLETED" ? "cleardevWorkbench.acceptanceComplete" : "cleardevWorkbench.acceptancePending")}</p>
		</div>
		{detail.isError ? <p role="alert">{t("cleardevWorkbench.unavailable", { error: apiErrorMessage(detail.error) })}</p> : null}
		{!detail.isError && detail.data && !detailCurrent ? <p role="alert">{t("cleardevWorkbench.stale")}</p> : null}
		{detail.isLoading ? <p className="text-sm text-muted-foreground">{t("cleardevWorkbench.loading")}</p> : null}
		{detailCurrent ? <ClearDevPreflight preflight={detail.data?.latestControlledPreflight} requirementId={id} current={!!detail.data?.latestControlledPreflight?.id && detail.data.trustedProgress.controlledWork?.some((work) => work.preflightId === detail.data?.latestControlledPreflight?.id && work.roleBindingId === detail.data?.latestControlledPreflight?.roleBindingId) === true} onSaved={() => {
			void client.invalidateQueries({ queryKey: ["cleardev-operate", id] });
			void client.invalidateQueries({ queryKey: ["cleardev-products"] });
		}} /> : null}
		<ClearDevWorkflowRecovery id={id} scope={execution ? "execution" : "compilation"} sourceKey={summary.factSummarySha256} onOpenSession={onOpenSession}
			completed={summary.phase === "COMPLETED"} current={detailCurrent} blocked={["BLOCKED", "NEEDS_HUMAN"].includes(summary.phase)} />
		{model ? <>
			<div>
				<h3 className="mb-2 text-xs font-semibold text-muted-foreground uppercase">{t("cleardevWorkbench.tasks")}</h3>
				{model.tasks.length === 0 ? <p className="text-sm text-muted-foreground">{t("cleardevWorkbench.noTasks")}</p> :
					<ul className="flex flex-col gap-2">{model.tasks.map((task) => <li key={task.id} className="rounded-md border px-3 py-2 text-sm" data-testid={`cleardev-workbench-task-${task.id}`}>
						<div className="flex flex-wrap items-start justify-between gap-2"><span className="font-medium">{task.title}</span><span>{task.reviewVerified ? t("cleardevWorkbench.task.VERIFIED") : taskNames[task.status] ? t(taskNames[task.status]) : task.status}{task.current ? "" : t("cleardevWorkbench.historySuffix")}</span></div>
					{task.objective ? <p className="mt-1 text-muted-foreground">{task.objective}</p> : null}
					{task.waitingOn.length ? <p className="mt-1 text-muted-foreground">{t("cleardevWorkbench.dependencies", { dependencies: task.waitingOn.join(", ") })}</p> : null}
					{(detail.data?.trustedProgress.phase === "NEEDS_HUMAN" || (detail.data?.trustedProgress.phase === "BLOCKED" && task.id === blockedFinalTaskId)) && task.current && ["REVIEW", "REWORK"].includes(task.status) ? <Button type="button" variant="outline" className="mt-2" disabled={rework.isPending || !hasTrustedApiBaseUrl()} onClick={() => {
						if (reworkInFlight.current) return;
						reworkInFlight.current = true;
						rework.mutate(task.id);
					}}>{rework.isError ? t("cleardevWorkbench.retryRework") : t("cleardevWorkbench.dispatchRework")}</Button> : null}
					{detail.data?.complexExecution?.phaseReason === "BUILDER_BUDGET_EXHAUSTED" ? <Button type="button" variant="outline" className="mt-2" disabled={builderTurnRecovery.isPending || !hasTrustedApiBaseUrl()} onClick={() => {
						if (builderTurnInFlight.current) return;
						builderTurnInFlight.current = true;
						builderTurnRecovery.mutate(task.id);
					}}>{builderTurnRecovery.isError ? t("cleardevWorkbench.retryBuilderTurnRecovery") : t("cleardevWorkbench.requestBuilderTurnRecovery")}</Button> : null}
					{detail.data?.complexExecution?.phaseReason === "REVIEWER_BUDGET_EXHAUSTED" ? <Button type="button" variant="outline" className="mt-2" disabled={budgetAuthorization.isPending || !hasTrustedApiBaseUrl()} onClick={() => {
						if (budgetInFlight.current) return;
						budgetInFlight.current = true;
						budgetAuthorization.mutate(task.id);
					}}>{budgetAuthorization.isError ? t("cleardevWorkbench.retryBudgetAuthorization") : t("cleardevWorkbench.requestBudgetAuthorization")}</Button> : null}
					{budgetAuthorization.isError ? <p role="alert">{apiErrorMessage(budgetAuthorization.error)}</p> : null}
					{rework.isError ? <p role="alert">{apiErrorMessage(rework.error)}</p> : null}
					</li>)}</ul>}
			</div>
			<div>
				<h3 className="mb-2 text-xs font-semibold text-muted-foreground uppercase">{t("cleardevWorkbench.team")}</h3>
			{model.roles.length === 0 ? <p className="text-sm text-muted-foreground">{t("cleardevWorkbench.noTeam")}</p> :
				<ul className="flex flex-col gap-2">{model.roles.map((role) => <li key={role.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2 text-sm" data-testid={`cleardev-workbench-role-${role.id}`}>
						<div><p className="font-medium">{roleName(role.role)}{role.taskTitle ? ` · ${role.taskTitle}` : ""}</p>
							{role.role === "BUILDER" && role.taskObjective ? <p className="text-xs">{role.taskObjective}</p> : null}
							{role.candidateSha ? <p className="text-xs">{t("cleardevWorkbench.candidate")} <code>{role.candidateSha}</code></p> : null}
							<p className="text-xs text-muted-foreground">{role.historical ? t("cleardevWorkbench.historicalPrefix") : ""}{role.status} · {t(sessionStatusKey(role.sessionId))}{role.role === "ENGINEERING_PLANNER" && sessionStatusKey(role.sessionId) === "cleardevWorkbench.session.idle" ? t("cleardevWorkbench.plannerIdleSuffix") : ""}</p></div>
						{role.sessionId ? <Button type="button" size="sm" variant="outline" onClick={() => onOpenSession(id, role.sessionId!, `${roleName(role.role)}${role.taskTitle ? ` · ${role.taskTitle}` : ""}`)}>{t("cleardevWorkbench.viewSession")}</Button> : null}
					</li>)}</ul>}
			</div>
			{integration ? <div className="rounded-md border p-3 text-sm" data-testid="cleardev-delivery-evidence">
				<h3 className="font-medium">{t("cleardevWorkbench.delivery")}</h3>
				<p>{t("cleardevWorkbench.integrated")} <code className="break-all">{integration.candidateCommitSha}</code></p>
				<p>{t("cleardevWorkbench.finalReview")} {execution?.finalReview?.verdict ?? t("cleardevWorkbench.notRecorded")}{execution?.finalReview?.summary ? ` · ${execution.finalReview.summary}` : ""}</p>
					{deliveryPaths.length ? <ul className="list-disc pl-5">{deliveryPaths.map((path) => <li key={path}><code>{path}</code></li>)}</ul> : <p className="text-muted-foreground">{t("cleardevWorkbench.noPaths")}</p>}
					{deliveryChecks.map((check) => <div key={check.id}>
						<p>{t("cleardevWorkbench.check", { kind: check.kind, result: check.result ?? t("cleardevWorkbench.pending") })}</p>
						{check.outputSummary ? check.outputSummary.trimStart().startsWith("{") ? <details><summary className="cursor-pointer text-muted-foreground">{t("cleardevWorkbench.receipt")}</summary><pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all text-xs">{check.outputSummary}</pre></details> : <p className="text-muted-foreground">{check.outputSummary}</p> : null}
					</div>)}
			</div> : null}
		</> : null}
	</section>;
}
