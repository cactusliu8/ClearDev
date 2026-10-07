package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

// A settled final-review REWORK verdict stops the run for a person, who may
// dispatch each verdict's round once. After the stage review's own three
// send-back rounds are spent, the run stops blocked instead of offering the
// dispatch again.
func TestRequirementFinalReviewStopCapsReworkDispatches(t *testing.T) {
	run := ComplexExecutionRun{ID: "run-1", RequirementVersionID: "v1", RequirementSHA256: strings.Repeat("a", 64), PlanID: "plan-1", PlanSHA256: strings.Repeat("b", 64)}
	pkg := ComplexExecutionRunPackage{SchemaVersion: ComplexExecutionProtocolVersion, Mode: string(WorkModeStandard), ExecutionRunID: run.ID, RequirementVersionID: run.RequirementVersionID, RequirementSHA256: run.RequirementSHA256, PlanID: run.PlanID, PlanSHA256: run.PlanSHA256, TaskSetVersion: ComplexStandardTaskSetVersion, FinalReviewPolicy: RequirementFinalReviewPolicyV1}
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	run.ExecutionPackageJSON = string(raw)
	run.ExecutionPackageSHA256 = sha256Hex(raw)

	for _, count := range []int{1, 2, 3, 4} {
		snapshot := ComplexExecutionSnapshot{
			Run:                    run,
			FinalReviewReworkCount: count,
			Tasks:                  []ComplexExecutionTask{{ID: "task-1", TaskKey: "implement-core", Status: DevelopmentTaskStatusReview, CurrentRound: count, ReworkCount: count}},
			FinalReview:            &RequirementFinalReview{ID: "final-1", Status: "SETTLED", Verdict: "REWORK", ReasonCode: "FINAL_REVIEW_REWORK", ReviewPacketSHA256: strings.Repeat("c", 64)},
		}
		phase, reason, stop := requirementFinalReviewStop(snapshot)
		if !stop {
			t.Fatalf("count=%d: the rework stop was released without a dispatched round", count)
		}
		if count <= ComplexFinalReviewMaxReworkCount && phase != ComplexExecutionNeedsHuman {
			t.Fatalf("count=%d: phase=%s reason=%s, want the human dispatch stop", count, phase, reason)
		}
		if count > ComplexFinalReviewMaxReworkCount && (phase != ComplexExecutionBlocked || reason != ReasonFinalReviewReworkLimit) {
			t.Fatalf("count=%d: phase=%s reason=%s, want the blocked rework limit", count, phase, reason)
		}
	}
}
