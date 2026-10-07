package cleardev

import (
	"strings"
	"testing"
	"time"
)

func repairBarrierRun(t *testing.T) ComplexExecutionRun {
	t.Helper()
	raw, _, err := BuildComplexExecutionRunPackage(WorkModeStandard, "run", "version", strings.Repeat("a", 64), "plan", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if raw, _, err = BindPlannerTaskContractPolicy(raw); err != nil {
		t.Fatal(err)
	}
	if raw, _, err = BindRequirementFinalReviewPolicy(raw); err != nil {
		t.Fatal(err)
	}
	bound, digest, err := BindPlannerRuntimePolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ComplexExecutionRun{ID: "run", RequirementVersionID: "version", RequirementSHA256: strings.Repeat("a", 64),
		PlanID: "plan", PlanSHA256: strings.Repeat("b", 64), Mode: WorkModeStandard, FixedBuilderCount: 1, TaskSetVersion: 1,
		ExecutionPackageJSON: string(bound), ExecutionPackageSHA256: digest}
}

func repairBarrierSnapshot(t *testing.T, outcome string, reason ReasonCode, grants []string) ComplexExecutionSnapshot {
	t.Helper()
	event := PlannerCoordinationEvent{ID: "event-1", ExecutionRunID: "run", CreatedAt: time.Unix(1, 0)}
	decision := PlannerCoordinationDecision{EventID: "event-1", ExecutionRunID: "run", Source: "CONTROL_PLANE",
		Outcome: outcome, ReasonCode: reason, Summary: "the control plane refused a bounded revision", CreatedAt: time.Unix(2, 0)}
	history := PlannerRuntimeSnapshot{Events: []PlannerCoordinationEvent{event}, Decisions: []PlannerCoordinationDecision{decision}}
	if len(grants) > 0 {
		history.RepairAuthorizations = grants
	}
	return ComplexExecutionSnapshot{Run: repairBarrierRun(t), PlannerRuntime: &history}
}

func TestPlannerRuntimeBarrierHumanRepairGrantResolvesStop(t *testing.T) {
	blocked, reason := PlannerRuntimeBarrier(repairBarrierSnapshot(t, PlannerRuntimeStop, ReasonPlannerRuntimeStopped, nil))
	if !blocked || reason != ReasonPlannerRuntimeStopped {
		t.Fatalf("an ungranted stop must keep blocking: blocked=%v reason=%s", blocked, reason)
	}
	if blocked, _ := PlannerRuntimeBarrier(repairBarrierSnapshot(t, PlannerRuntimeStop, ReasonPlannerRuntimeStopped, []string{"event-1"})); blocked {
		t.Fatal("a human-approved coordination repair must resolve the recorded stop")
	}
	blocked, reason = PlannerRuntimeBarrier(repairBarrierSnapshot(t, "STALE", ReasonPlannerRuntimeStale, []string{"event-1"}))
	if !blocked || reason != ReasonPlannerRuntimeStale {
		t.Fatalf("a stale stop must keep blocking: blocked=%v reason=%s", blocked, reason)
	}
	blocked, reason = PlannerRuntimeBarrier(repairBarrierSnapshot(t, PlannerRuntimeStop, ReasonPlannerRuntimeStopped, []string{"other-event"}))
	if !blocked || reason != ReasonPlannerRuntimeStopped {
		t.Fatalf("a grant for another event must not resolve this stop: blocked=%v reason=%s", blocked, reason)
	}
}
