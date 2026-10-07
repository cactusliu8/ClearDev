import type { components } from "../../api/schema";

type Requirement = components["schemas"]["ClearDevRequirementView"];
type Summary = components["schemas"]["ClearDevTrustedProgressSummary"];

// A QUICK run is the current successor only when its durable source points to
// this complex run. Both snapshots remain in the detail response as history.
export function currentExecution(detail: Requirement) {
	const complex = detail.complexExecution;
	const quick = detail.quickExecution;
	const quickCurrent = Boolean(quick && (!complex || quick.run.sourceExecutionRunId === complex.run.id));
	return { complex: quickCurrent ? undefined : complex, quick: quickCurrent ? quick : (!complex ? quick : undefined), previousComplex: quickCurrent ? complex : undefined };
}

export type WorkbenchTask = {
	id: string;
	title: string;
	objective?: string;
	status: string;
	reviewVerified: boolean;
	current: boolean;
	waitingOn: string[];
};

export type WorkbenchRole = {
	id: string;
	role: string;
	sessionId?: string;
	taskId?: string;
	taskTitle?: string;
	taskObjective?: string;
	candidateSha?: string;
	status: string;
	reasonCode?: string;
	historical: boolean;
};

function objectiveFromPackage(raw?: string): string | undefined {
	if (!raw) return undefined;
	try {
		const parsed: unknown = JSON.parse(raw);
		if (parsed && typeof parsed === "object" && "objective" in parsed && typeof parsed.objective === "string") {
			return parsed.objective.trim() || undefined;
		}
	} catch {
		// Old packages may not have this field. The task title remains authoritative.
	}
	return undefined;
}

export function deriveWorkbench(detail: Requirement, summary: Summary): { tasks: WorkbenchTask[]; roles: WorkbenchRole[] } {
	const { complex: execution, quick, previousComplex } = currentExecution(detail);
	const mappings = [...(previousComplex?.tasks ?? []), ...(execution?.tasks ?? (quick?.task ? [quick.task] : []))];
	const byDevelopmentId = new Map(mappings.map((task) => [task.developmentTaskId, task]));
	const byMappingId = new Map(mappings.map((task) => [task.id, task]));
	const byKey = new Map(mappings.map((task) => [task.taskKey, task]));
	const verified = new Set((execution?.verifications ?? []).filter((item) => {
		const task = byMappingId.get(item.complexExecutionTaskId);
		return task?.currentDispatchId === item.dispatchId && task?.currentRound === item.round;
	}).map((item) => item.complexExecutionTaskId));
	const tasks = summary.tasks.map((item) => {
		const mapping = byDevelopmentId.get(item.developmentTaskId);
		const waitingOn = mapping?.status === "PLANNED"
			? (mapping.dependencyTaskKeys ?? []).filter((key) => {
				const dependency = byKey.get(key);
				return !dependency || !verified.has(dependency.id);
			})
			: [];
		return {
			id: item.developmentTaskId,
			title: item.title,
			objective: objectiveFromPackage(mapping?.executionPackageJson),
			status: item.status,
			reviewVerified: item.current && item.status === "REVIEW" && Boolean(mapping && verified.has(mapping.id)),
			current: item.current,
			waitingOn,
		};
	});
	const taskTitle = new Map(tasks.map((task) => [task.id, task.title]));
	const taskObjective = new Map(tasks.map((task) => [task.id, task.objective]));
	const roles: WorkbenchRole[] = [];
	const add = (binding: { id: string; role: string; aoSessionId?: string; status: string; reasonCode?: string; endedAt?: string | null; taskMappingId?: string }, taskMappingId = binding.taskMappingId, historicalRun = false) => {
		const task = taskMappingId ? byMappingId.get(taskMappingId) : undefined;
		const source = historicalRun ? previousComplex : execution;
		const recovery = source?.fixedRecoveries?.find((item) => item.result?.roleBindingId === binding.id && item.request.candidateSha && item.request.reviewId);
		const recoveredReview = recovery && source?.reviews?.find((review) => review.id === recovery.request.reviewId
			&& review.reviewerRoleBindingId === recovery.request.roleBindingId
			&& review.candidateCommitSha === recovery.request.candidateSha);
		roles.push({
			id: task ? `${binding.id}:${task.id}` : binding.id,
			role: binding.role,
			sessionId: binding.aoSessionId || undefined,
			taskId: task?.developmentTaskId,
			taskTitle: task ? taskTitle.get(task.developmentTaskId) : undefined,
			taskObjective: task ? taskObjective.get(task.developmentTaskId) : undefined,
			candidateSha: source?.reviews?.filter((review) => review.reviewerRoleBindingId === binding.id).at(-1)?.candidateCommitSha
				?? (recoveredReview ? recovery?.request.candidateSha : undefined),
			status: binding.status,
			reasonCode: binding.reasonCode,
			historical: historicalRun || (binding.status !== "FAILED" && (binding.status === "ENDED" || Boolean(binding.endedAt))) || Boolean(task && !tasks.find((item) => item.id === task.developmentTaskId)?.current),
		});
	};
	for (const binding of detail.complexPlanning?.roleBindings ?? []) add(binding);
	for (const binding of previousComplex?.roleBindings ?? []) add(binding, binding.taskMappingId, true);
	for (const binding of execution?.roleBindings ?? quick?.roleBindings ?? []) {
		if (binding.role !== "BUILDER") {
			add(binding);
			continue;
		}
		const run = execution?.run ?? quick?.run;
		const dispatches = execution?.dispatches ?? quick?.dispatches ?? [];
		const owned = [...new Set(dispatches.filter((item) =>
			item.builderRoleBindingId === binding.id || (!item.builderRoleBindingId && run?.builderRoleBindingId === binding.id),
		).map((item) => item.complexExecutionTaskId))];
		if (owned.length === 0) add(binding);
		else for (const mappingId of owned) add(binding, mappingId);
	}
	const latestFinal = execution?.finalReview;
	if (previousComplex?.finalReview) {
		const prior = previousComplex.finalReview;
		roles.push({ id: prior.id, role: "FINAL_REVIEWER", sessionId: prior.aoSessionId,
			status: prior.status, candidateSha: prior.candidateCommitSha, historical: true });
	}
	if (latestFinal) {
		roles.push({ id: latestFinal.id, role: "FINAL_REVIEWER", sessionId: latestFinal.aoSessionId,
			status: latestFinal.status, candidateSha: latestFinal.candidateCommitSha, historical: false });
	}
	// Replacement reviewers and Steward continuations stay visible as history.
	// The latest binding for one responsibility is the current one even when an
	// older row has no endedAt timestamp yet.
	const latest = new Map<string, number>();
	roles.forEach((role, index) => latest.set(`${role.role}:${role.taskId ?? ""}`, index));
	roles.forEach((role, index) => {
		if (latest.get(`${role.role}:${role.taskId ?? ""}`) !== index) role.historical = true;
	});
	// A single session can serve multiple tasks. Keep each exact role binding and
	// its task mapping; a title or AO idle state cannot establish task ownership.
	return { tasks, roles };
}
