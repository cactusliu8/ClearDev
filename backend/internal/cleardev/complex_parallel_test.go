package cleardev

import (
	"strings"
	"testing"
	"time"
)

func TestComplexParallelSelectModeOneBuilderStaysStandard(t *testing.T) {
	tasks := disjointParallelPlanTasks()
	selection, err := SelectComplexExecutionMode(tasks, 1)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != WorkModeStandard || selection.BuilderCount != 1 || selection.ReasonCode != ReasonOneBuilderRequired || selection.SafeConcurrent < 2 || len(selection.Batches) != 0 {
		t.Fatalf("suggested 1 = %#v", selection)
	}
}

func TestComplexParallelSelectModeDisjointTwoBuilders(t *testing.T) {
	tasks := disjointParallelPlanTasks()
	selection, err := SelectComplexExecutionMode(tasks, 2)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != WorkModeParallel || selection.BuilderCount != 2 || selection.ReasonCode != ReasonParallelPlanApproved || selection.SafeConcurrent != 2 {
		t.Fatalf("disjoint 2 = %#v", selection)
	}
	if len(selection.Batches) != 2 || strings.Join(selection.Batches[0], ",") != "normalize-email,deduplicate-email" || strings.Join(selection.Batches[1], ",") != "build-summary" {
		t.Fatalf("batches = %#v", selection.Batches)
	}
}

func TestComplexParallelSelectModeThreeBuildersUsesSafeCount(t *testing.T) {
	tasks := []ComplexPlanTask{
		parallelPlanTask("one", []string{"src/email.js"}, nil),
		parallelPlanTask("two", []string{"src/deduplicate.js"}, nil),
		parallelPlanTask("three", []string{"src/summary.js"}, nil),
	}
	selection, err := SelectComplexExecutionMode(tasks, 3)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != WorkModeParallel || selection.BuilderCount != 3 || selection.SafeConcurrent != 3 || selection.ReasonCode != ReasonParallelPlanApproved || len(selection.Batches) != 1 || len(selection.Batches[0]) != 3 {
		t.Fatalf("three independent = %#v", selection)
	}
	capped, err := SelectComplexExecutionMode(tasks, 2)
	if err != nil || capped.BuilderCount != 2 || capped.Mode != WorkModeParallel || len(capped.Batches) != 2 || len(capped.Batches[0]) != 2 || len(capped.Batches[1]) != 1 {
		t.Fatalf("suggested 2 with 3 safe = %#v err=%v", capped, err)
	}
}

func TestComplexParallelSelectModeOverlapDegradesToStandard(t *testing.T) {
	tasks := []ComplexPlanTask{
		parallelPlanTask("left", []string{"src/**"}, nil),
		parallelPlanTask("right", []string{"src/email.js"}, nil),
		parallelPlanTask("summary", []string{"docs/summary.md"}, []string{"left", "right"}),
	}
	selection, err := SelectComplexExecutionMode(tasks, 2)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != WorkModeStandard || selection.BuilderCount != 1 || selection.ReasonCode != ReasonParallelUnsafeDegrade || selection.SafeConcurrent < 1 {
		t.Fatalf("overlap degrade = %#v", selection)
	}
}

func TestComplexParallelSelectModeSharedPathsError(t *testing.T) {
	tasks := disjointParallelPlanTasks()
	tasks[0].GeneratedPaths = []string{"src/generated.js"}
	if _, err := SelectComplexExecutionMode(tasks, 2); err == nil {
		t.Fatal("unfrozen generated-path plan was accepted")
	}
	tasks[0].GeneratedPaths = nil
	tasks[0].SharedPathsRequireApproval = []string{"package.json"}
	selection, err := SelectComplexExecutionMode(tasks, 2)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != WorkModeParallel {
		t.Fatalf("listed shared path was not accepted: %#v", selection)
	}
	tasks[1].SharedPathsRequireApproval = []string{"package.json"}
	overlap, err := SelectComplexExecutionMode(tasks, 2)
	if err != nil {
		t.Fatal(err)
	}
	if overlap.SafeConcurrent != 1 {
		t.Fatalf("overlapping shared path stayed concurrent: %#v", overlap)
	}
}

func TestComplexParallelWritePathsDisjointIsConservative(t *testing.T) {
	left := parallelPlanTask("left", []string{"src/email.js"}, nil)
	right := parallelPlanTask("right", []string{"src/deduplicate.js"}, nil)
	if !ComplexTaskWritePathsDisjoint(left, right) {
		t.Fatal("distinct files were treated as overlapping")
	}
	if ComplexTaskWritePathsDisjoint(left, parallelPlanTask("same", []string{"src/email.js"}, nil)) {
		t.Fatal("identical files were treated as disjoint")
	}
	if ComplexTaskWritePathsDisjoint(parallelPlanTask("glob", []string{"*"}, nil), right) {
		t.Fatal("unprovable glob was treated as disjoint")
	}
	if ComplexTaskWritePathsDisjoint(parallelPlanTask("tree", []string{"src/**"}, nil), left) {
		t.Fatal("directory glob was treated as disjoint from a child path")
	}
}

func TestComplexParallelRejectsDependencyCycle(t *testing.T) {
	tasks := []ComplexPlanTask{
		parallelPlanTask("left", []string{"src/left.js"}, []string{"right"}),
		parallelPlanTask("right", []string{"src/right.js"}, []string{"left"}),
	}
	if _, err := SelectComplexExecutionMode(tasks, 2); err == nil {
		t.Fatal("cyclic dependency graph was accepted")
	}
}

func TestComplexParallelPlanBatchesKeepsPlanOrderInsideWaves(t *testing.T) {
	batches := PlanComplexExecutionBatches(disjointParallelPlanTasks())
	if len(batches) != 2 || strings.Join(batches[0], ",") != "normalize-email,deduplicate-email" || batches[1][0] != "build-summary" {
		t.Fatalf("plan-order batches = %#v", batches)
	}
}

func TestDeriveComplexParallelPhaseAllowsOnePreDispatchInfrastructureBuilderContinuation(t *testing.T) {
	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	snapshot := ComplexExecutionSnapshot{
		Run: ComplexExecutionRun{ID: "run", Mode: WorkModeParallel, FixedBuilderCount: 2, Decision: ComplexExecutionDecisionDispatch},
		RoleBindings: []ComplexExecutionRoleBinding{
			{ID: "slot-1", Role: StandardRoleBuilder, BuilderSlot: 1, Status: RoleBindingStatusRequested, RequestedAt: now},
			{ID: "slot-2-old", Role: StandardRoleBuilder, BuilderSlot: 2, Status: RoleBindingStatusEnded, ReasonCode: ReasonBuilderSpawnFailed, RequestedAt: now},
		},
		Tasks:   []ComplexExecutionTask{{TaskKey: "a", Status: DevelopmentTaskStatusPlanned}, {TaskKey: "b", Status: DevelopmentTaskStatusPlanned}},
		Batches: []ComplexExecutionBatch{{ID: "batch", Ordinal: 0, TaskKeys: []string{"a", "b"}, Status: ComplexExecutionBatchPending}},
	}
	if phase, reason := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionBindingBuilder || reason != ReasonNone {
		t.Fatalf("pre-dispatch infra terminal phase=%s reason=%s", phase, reason)
	}

	snapshot.RoleBindings = append(snapshot.RoleBindings, ComplexExecutionRoleBinding{
		ID: "slot-2-new", ContinuationOfRoleBindingID: "slot-2-old", Role: StandardRoleBuilder, BuilderSlot: 2,
		Status: RoleBindingStatusRequested, RequestedAt: now.Add(time.Second),
	})
	if phase, reason := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionBindingBuilder || reason != ReasonNone {
		t.Fatalf("active continuation phase=%s reason=%s", phase, reason)
	}

	snapshot.RoleBindings[0].Status = RoleBindingStatusBound
	snapshot.RoleBindings[2].Status = RoleBindingStatusBound
	snapshot.Batches[0].Status = ComplexExecutionBatchRunning
	snapshot.Tasks[0].Status = DevelopmentTaskStatusRunning
	snapshot.Dispatches = []ComplexExecutionDispatch{{ID: "dispatch"}}
	if phase, reason := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionBuilding || reason != ReasonNone {
		t.Fatalf("active continuation after dispatch phase=%s reason=%s", phase, reason)
	}

	snapshot.RoleBindings[2].Status = RoleBindingStatusEnded
	snapshot.RoleBindings[2].ReasonCode = ReasonBuilderSpawnFailed
	if phase, reason := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionBlocked || reason != ReasonBuilderSpawnFailed {
		t.Fatalf("second infra terminal phase=%s reason=%s", phase, reason)
	}

	snapshot.RoleBindings = snapshot.RoleBindings[:2]
	if phase, reason := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionBlocked || reason != ReasonBuilderSpawnFailed {
		t.Fatalf("post-dispatch infra terminal phase=%s reason=%s", phase, reason)
	}
}

func TestDeriveComplexParallelPhaseComposingBlockedAndIntegrating(t *testing.T) {
	snapshot := ComplexExecutionSnapshot{
		Run: ComplexExecutionRun{ID: "run", Mode: WorkModeParallel, FixedBuilderCount: 2, Decision: ComplexExecutionDecisionDispatch},
		RoleBindings: []ComplexExecutionRoleBinding{
			{Role: StandardRoleBuilder, BuilderSlot: 1, Status: RoleBindingStatusBound},
			{Role: StandardRoleBuilder, BuilderSlot: 2, Status: RoleBindingStatusBound},
		},
		Tasks: []ComplexExecutionTask{
			{TaskKey: "normalize-email", Status: DevelopmentTaskStatusReview},
			{TaskKey: "deduplicate-email", Status: DevelopmentTaskStatusReview},
		},
		Batches: []ComplexExecutionBatch{{ID: "batch-0", Ordinal: 0, TaskKeys: []string{"normalize-email", "deduplicate-email"}, Status: ComplexExecutionBatchComposing}},
	}
	phase, _ := DeriveComplexExecutionPhase(snapshot)
	if phase != ComplexExecutionComposing {
		t.Fatalf("composing phase = %s", phase)
	}

	snapshot.Batches[0].Status = ComplexExecutionBatchRunning
	snapshot.Tasks[0].Status = DevelopmentTaskStatusPlanned
	snapshot.Tasks[1].Status = DevelopmentTaskStatusPlanned
	phase, _ = DeriveComplexExecutionPhase(snapshot)
	if phase != ComplexExecutionReadyToDispatch {
		t.Fatalf("planned wave phase = %s", phase)
	}
	snapshot.Tasks[0].Status = DevelopmentTaskStatusReview
	snapshot.Tasks[1].Status = DevelopmentTaskStatusReview
	snapshot.Batches[0].Status = ComplexExecutionBatchComposing

	snapshot.Batches[0].Status = ComplexExecutionBatchBlocked
	snapshot.Compositions = []ComplexExecutionComposition{{BatchID: "batch-0", Status: ComplexExecutionCompositionBlocked, ReasonCode: ReasonCompositionConflict}}
	phase, reason := DeriveComplexExecutionPhase(snapshot)
	if phase != ComplexExecutionBlocked || reason != ReasonCompositionConflict {
		t.Fatalf("blocked phase = %s reason=%s", phase, reason)
	}

	snapshot.Batches[0].Status = ComplexExecutionBatchComposed
	snapshot.Compositions = []ComplexExecutionComposition{{BatchID: "batch-0", Status: ComplexExecutionCompositionComposed, OutputCommitSHA: strings.Repeat("c", 40)}}
	snapshot.Batches = append(snapshot.Batches, ComplexExecutionBatch{ID: "batch-1", Ordinal: 1, TaskKeys: []string{"build-summary"}, Status: ComplexExecutionBatchComposed})
	snapshot.Compositions = append(snapshot.Compositions, ComplexExecutionComposition{BatchID: "batch-1", Status: ComplexExecutionCompositionComposed, OutputCommitSHA: strings.Repeat("d", 40)})
	snapshot.Tasks = append(snapshot.Tasks, ComplexExecutionTask{TaskKey: "build-summary", Status: DevelopmentTaskStatusReview})
	phase, _ = DeriveComplexExecutionPhase(snapshot)
	if phase != ComplexExecutionIntegrating {
		t.Fatalf("integrating phase = %s", phase)
	}
	final, ok := ComplexExecutionFinalComposition(snapshot)
	if !ok || final.OutputCommitSHA != strings.Repeat("d", 40) {
		t.Fatalf("final composition = %#v ok=%t", final, ok)
	}
}

func TestComplexParallelExecutionPackageBindsMode(t *testing.T) {
	task := ComplexPlanTask{
		Key: "normalize", Title: "规范化", Objective: "实现邮箱规范化。",
		RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"},
		WritePaths: []string{"src/email.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{},
		ForbiddenPaths: []string{".git/**", "package.json"}, RequiredCheckIDs: []string{"email-unit"}, DependencyKeys: []string{},
	}
	pkg, raw, digest, err := BuildComplexStandardExecutionPackage(WorkModeParallel, ComplexStandardExecutionPackageInput{
		ExecutionRunID: "run-1", RequirementVersionID: "v2", RequirementSHA256: strings.Repeat("b", 64),
		RequirementText: "实现邮件名单。", PlanID: "plan-1", PlanSHA256: strings.Repeat("c", 64),
		TaskSetVersion: 1, TaskID: "task-1", Task: task,
	})
	if err != nil || pkg.Mode != string(WorkModeParallel) || digest == "" || !strings.Contains(string(raw), `"mode":"PARALLEL"`) {
		t.Fatalf("parallel package = %#v digest=%q err=%v", pkg, digest, err)
	}
}

func disjointParallelPlanTasks() []ComplexPlanTask {
	return []ComplexPlanTask{
		parallelPlanTask("normalize-email", []string{"src/email.js"}, nil),
		parallelPlanTask("deduplicate-email", []string{"src/deduplicate.js"}, nil),
		parallelPlanTask("build-summary", []string{"src/summary.js"}, []string{"normalize-email", "deduplicate-email"}),
	}
}

func parallelPlanTask(key string, writes, deps []string) ComplexPlanTask {
	if deps == nil {
		deps = []string{}
	}
	return ComplexPlanTask{Key: key, WritePaths: writes, DependencyKeys: deps}
}
