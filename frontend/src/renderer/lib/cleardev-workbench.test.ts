import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import { currentExecution, deriveWorkbench } from "./cleardev-workbench";

type Requirement = components["schemas"]["ClearDevRequirementView"];
type Summary = components["schemas"]["ClearDevTrustedProgressSummary"];

describe("deriveWorkbench", () => {
	it("uses a linked QUICK successor for current task and delivery facts", () => {
		const detail = { complexExecution: {
			run: { id: "complex-run" }, tasks: [{ id: "old-map", developmentTaskId: "old-task", taskKey: "old", executionPackageJson: '{"objective":"Old work"}' }],
			verifications: [{ complexExecutionTaskId: "old-map" }],
			roleBindings: [{ id: "old-builder", role: "BUILDER", taskMappingId: "old-map", aoSessionId: "old-session", status: "BOUND" }],
			integration: { candidateCommitSha: "old-sha" }, finalReview: { id: "old-final", aoSessionId: "old-final-session", status: "PASSED", candidateCommitSha: "old-sha" },
		}, quickExecution: {
			run: { id: "quick-run", sourceExecutionRunId: "complex-run" }, task: { id: "new-map", developmentTaskId: "new-task", taskKey: "new", executionPackageJson: '{"objective":"Current work"}' },
			roleBindings: [{ id: "new-builder", role: "BUILDER", taskMappingId: "new-map", aoSessionId: "new-session", status: "BOUND" }],
			integration: { candidateCommitSha: "new-sha" },
		} } as unknown as Requirement;
		const summary = { tasks: [{ developmentTaskId: "new-task", title: "Current task", status: "REVIEW", current: true },
			{ developmentTaskId: "old-task", title: "Old task", status: "DONE", current: false }] } as unknown as Summary;
		const model = deriveWorkbench(detail, summary);
		expect(currentExecution(detail).quick?.integration?.candidateCommitSha).toBe("new-sha");
		expect(currentExecution(detail).complex).toBeUndefined();
		expect(model.tasks.find((task) => task.id === "new-task")?.objective).toBe("Current work");
		expect(model.tasks.find((task) => task.id === "new-task")?.reviewVerified).toBe(false);
		expect(model.roles.find((role) => role.id === "new-builder:new-map")).toMatchObject({ sessionId: "new-session", historical: false, taskId: "new-task" });
		expect(model.roles.find((role) => role.id === "old-builder:old-map")?.historical).toBe(true);
		expect(model.roles.find((role) => role.id === "old-final")?.historical).toBe(true);
	});
	it("binds a replacement Reviewer to the exact recovery review candidate", () => {
		const detail = { complexExecution: {
			tasks: [{ id: "mapping", developmentTaskId: "task", taskKey: "task" }],
			roleBindings: [
				{ id: "original", role: "REVIEWER", taskMappingId: "mapping", aoSessionId: "old-session", status: "FAILED" },
				{ id: "replacement", role: "REVIEWER", taskMappingId: "mapping", continuationOfRoleBindingId: "original", aoSessionId: "new-session", status: "BOUND" },
			],
			reviews: [{ id: "review", reviewerRoleBindingId: "original", candidateCommitSha: "candidate-sha" }],
			fixedRecoveries: [{ request: { reviewId: "review", roleBindingId: "original", candidateSha: "candidate-sha" }, result: { roleBindingId: "replacement" } }],
		} } as unknown as Requirement;
		const summary = { tasks: [{ developmentTaskId: "task", title: "Task", status: "REVIEW", current: true }] } as unknown as Summary;
		const roles = deriveWorkbench(detail, summary).roles;
		expect(roles.find((role) => role.id === "replacement:mapping")).toMatchObject({ candidateSha: "candidate-sha", historical: false });
		expect(roles.find((role) => role.id === "original:mapping")).toMatchObject({ candidateSha: "candidate-sha", historical: true });
	});
	it("keeps the latest failed Steward visible as a current failure", () => {
		const detail = { complexPlanning: { roleBindings: [
			{ id: "failed-steward", role: "STEWARD", status: "FAILED", reasonCode: "STEWARD_UNAVAILABLE", endedAt: "2026-01-01T00:00:00Z" },
		] } } as unknown as Requirement;
		const result = deriveWorkbench(detail, { tasks: [] } as unknown as Summary);
		expect(result.roles[0]).toMatchObject({ id: "failed-steward", historical: false, reasonCode: "STEWARD_UNAVAILABLE" });
	});
	it("accepts the database's null dependency list for an independent task", () => {
		const detail = { complexExecution: { tasks: [{
			id: "mapping", developmentTaskId: "task", taskKey: "only", status: "PLANNED", dependencyTaskKeys: null,
		}] } } as unknown as Requirement;
		const summary = { tasks: [{ developmentTaskId: "task", title: "Only task", status: "PLANNED", current: true }] } as unknown as Summary;
		expect(deriveWorkbench(detail, summary).tasks[0].waitingOn).toEqual([]);
	});

	it("distinguishes an exact verified review from a still-running review", () => {
		const summary = { tasks: [
			{ developmentTaskId: "task-a", title: "Verified", status: "REVIEW", current: true },
			{ developmentTaskId: "task-b", title: "Review running", status: "REVIEW", current: true },
			{ developmentTaskId: "old-task", title: "Old review", status: "REVIEW", current: false },
		] } as unknown as Summary;
		const detail = { complexExecution: {
			tasks: [
				{ id: "mapping-a", developmentTaskId: "task-a", currentDispatchId: "dispatch-a-1", currentRound: 1 },
				{ id: "mapping-b", developmentTaskId: "task-b" },
				{ id: "old-mapping", developmentTaskId: "old-task" },
			],
			verifications: [{ complexExecutionTaskId: "mapping-a", dispatchId: "dispatch-a-0", round: 0 }, { complexExecutionTaskId: "old-mapping" }],
		} } as unknown as Requirement;
		expect(deriveWorkbench(detail, summary).tasks.map((task) => task.reviewVerified)).toEqual([false, false, false]);
		(detail.complexExecution as NonNullable<Requirement["complexExecution"]>).verifications.push({
			complexExecutionTaskId: "mapping-a", dispatchId: "dispatch-a-1", round: 1,
		} as NonNullable<Requirement["complexExecution"]>["verifications"][number]);
		expect(deriveWorkbench(detail, summary).tasks.map((task) => task.reviewVerified)).toEqual([true, false, false]);
	});

	it("keeps parallel task ownership, dependency waiting, and final review separate", () => {
		const summary = { phase: "INTEGRATING", tasks: [
			{ developmentTaskId: "a", title: "Provider", status: "DONE", current: true },
			{ developmentTaskId: "b", title: "Consumer", status: "PLANNED", current: true },
			{ developmentTaskId: "old", title: "Old task", status: "BLOCKED", current: false },
		] } as Summary;
		const detail = { complexPlanning: { roleBindings: [
			{ id: "planner", role: "ENGINEERING_PLANNER", aoSessionId: "planner-session", status: "BOUND" },
		] }, complexExecution: {
			tasks: [
				{ id: "task-a", developmentTaskId: "a", taskKey: "provider", status: "DONE", currentDispatchId: "dispatch-a", currentRound: 0, dependencyTaskKeys: [], executionPackageJson: '{"objective":"Produce exact output"}' },
			{ id: "task-b", developmentTaskId: "b", taskKey: "consumer", status: "PLANNED", dependencyTaskKeys: ["provider"], executionPackageJson: '{"objective":"Consume output"}' },
		],
			verifications: [],
			dispatches: [
				{ id: "dispatch-a", complexExecutionTaskId: "task-a", builderRoleBindingId: "builder-a" },
				{ id: "dispatch-b", complexExecutionTaskId: "task-b", builderRoleBindingId: "builder-b" },
			],
			roleBindings: [
				{ id: "builder-a", role: "BUILDER", taskMappingId: "task-a", aoSessionId: "shared-session", status: "BOUND" },
				{ id: "builder-b", role: "BUILDER", taskMappingId: "task-b", aoSessionId: "shared-session", status: "BOUND" },
				{ id: "review-a-old", role: "REVIEWER", taskMappingId: "task-a", aoSessionId: "old-review-session", status: "ENDED", endedAt: "2026-01-01T00:00:00Z" },
			],
			finalReview: { id: "final", aoSessionId: "final-session", status: "PENDING" },
		} } as unknown as Requirement;
		const first = deriveWorkbench(detail, summary);
		expect(first.tasks.find((task) => task.id === "b")?.waitingOn).toEqual(["provider"]);
		expect(first.tasks.find((task) => task.id === "b")?.objective).toBe("Consume output");
		expect(first.roles.filter((role) => role.sessionId === "shared-session").map((role) => role.taskId)).toEqual(["a", "b"]);
		expect(first.roles.find((role) => role.id === "review-a-old:task-a")?.historical).toBe(true);
		expect(first.roles.find((role) => role.id === "final")?.role).toBe("FINAL_REVIEWER");
		expect(first.tasks.filter((task) => task.current && task.status === "DONE")).toHaveLength(1);
		(detail.complexExecution as NonNullable<Requirement["complexExecution"]>).verifications = [
			{ complexExecutionTaskId: "task-a", dispatchId: "dispatch-a", round: 0 },
		] as Requirement["complexExecution"] extends { verifications: infer V } ? V : never;
		expect(deriveWorkbench(detail, summary).tasks.find((task) => task.id === "b")?.waitingOn).toEqual([]);
	});
});
