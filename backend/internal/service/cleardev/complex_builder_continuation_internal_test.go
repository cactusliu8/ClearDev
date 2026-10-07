package cleardev

import (
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestComplexExecutionBuilderBranchKeepsInitialNamesAndIsolatesContinuation(t *testing.T) {
	runID := "run"
	if got := complexExecutionBuilderBranch(runID, core.ComplexExecutionRoleBinding{BuilderSlot: 1}); got != "cleardev-complex-builder-run" {
		t.Fatalf("slot 1 branch = %q", got)
	}
	if got := complexExecutionBuilderBranch(runID, core.ComplexExecutionRoleBinding{BuilderSlot: 2}); got != "cleardev-complex-builder-run-2" {
		t.Fatalf("slot 2 branch = %q", got)
	}
	continued := core.ComplexExecutionRoleBinding{ID: "binding-new", BuilderSlot: 2, ContinuationOfRoleBindingID: "binding-old"}
	if got := complexExecutionBuilderBranch(runID, continued); got != "cleardev-complex-builder-run-2-cont-binding-new" {
		t.Fatalf("continuation branch = %q", got)
	}
}

func TestCurrentComplexExecutionBuilderBindingsPrefersLatestContinuation(t *testing.T) {
	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	execution := core.ComplexExecutionSnapshot{
		Run: core.ComplexExecutionRun{ID: "run", Mode: core.WorkModeParallel, FixedBuilderCount: 2},
		RoleBindings: []core.ComplexExecutionRoleBinding{
			{ID: "slot-1-old", ExecutionRunID: "run", Role: core.StandardRoleBuilder, BuilderSlot: 1, Status: core.RoleBindingStatusEnded, ReasonCode: core.ReasonBuilderSpawnFailed, RequestedAt: now},
			{ID: "slot-2", ExecutionRunID: "run", Role: core.StandardRoleBuilder, BuilderSlot: 2, Status: core.RoleBindingStatusBound, RequestedAt: now.Add(time.Second)},
			{ID: "slot-1-new", ExecutionRunID: "run", ContinuationOfRoleBindingID: "slot-1-old", Role: core.StandardRoleBuilder, BuilderSlot: 1, Status: core.RoleBindingStatusRequested, RequestedAt: now.Add(2 * time.Second)},
		},
	}
	got, ok := currentComplexExecutionBuilderBindings(execution)
	if !ok || len(got) != 2 {
		t.Fatalf("current bindings = %#v ok=%t", got, ok)
	}
	if got[0].ID != "slot-1-new" || got[1].ID != "slot-2" {
		t.Fatalf("current bindings = %#v", got)
	}
}

func TestCanContinuePreDispatchParallelBuilderIsNarrow(t *testing.T) {
	base := core.ComplexExecutionSnapshot{Run: core.ComplexExecutionRun{ID: "run", Mode: core.WorkModeParallel, FixedBuilderCount: 2}}
	binding := core.ComplexExecutionRoleBinding{
		ID: "builder-old", ExecutionRunID: "run", Role: core.StandardRoleBuilder, BuilderSlot: 2,
		Status: core.RoleBindingStatusEnded, ReasonCode: core.ReasonBuilderSpawnFailed,
	}
	if !canContinuePreDispatchParallelBuilder(base, binding) {
		t.Fatal("pre-dispatch infrastructure-ended Builder was not continuable")
	}
	withDispatch := base
	withDispatch.Dispatches = []core.ComplexExecutionDispatch{{ID: "dispatch"}}
	if canContinuePreDispatchParallelBuilder(withDispatch, binding) {
		t.Fatal("Builder continuation remained available after task dispatch")
	}
	second := binding
	second.ContinuationOfRoleBindingID = "older"
	if canContinuePreDispatchParallelBuilder(base, second) {
		t.Fatal("second Builder infrastructure continuation was allowed")
	}
	business := binding
	business.ReasonCode = core.ReasonCode("BUILDER_BLOCKED")
	if canContinuePreDispatchParallelBuilder(base, business) {
		t.Fatal("business failure was treated as infrastructure continuation")
	}
	standard := base
	standard.Run.Mode = core.WorkModeStandard
	if canContinuePreDispatchParallelBuilder(standard, binding) {
		t.Fatal("STANDARD Builder used PARALLEL pre-dispatch continuation")
	}
}
