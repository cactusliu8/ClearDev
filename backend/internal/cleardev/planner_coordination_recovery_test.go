package cleardev

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCoordinationRecoveryBarrierRetainsStopAndRequiresAtomicResult(t *testing.T) {
	snapshot := repairBarrierSnapshot(t, PlannerRuntimeStop, ReasonPlannerRuntimeUnavailable, nil)
	original := snapshot.PlannerRuntime.Decisions[0]
	snapshot.PlannerRuntime.Recoveries = []PlannerCoordinationRecovery{{EventID: original.EventID, Recovery: WorkflowRecovery{ID: "retry", ExecutionRunID: snapshot.Run.ID, Action: RecoveryRetryPlannerCoordination}}}
	if blocked, reason := PlannerRuntimeBarrier(snapshot); !blocked || reason != ReasonPlannerRuntimePending {
		t.Fatal("registration removed the barrier", blocked, reason)
	}
	result := PlannerCoordinationResult{SchemaVersion: 1, Kind: PlannerRuntimeResultKind, EventID: original.EventID, ContextSHA256: strings.Repeat("a", 64), Decision: PlannerRuntimeAmend, Summary: "recheck within original contract", Amendments: []PlannerRemainingAmendment{{TaskKey: "one", AdditionalReviewCriteria: []string{"verify original failure behavior"}}}}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.PlannerRuntime.RecoveryDecisions = []PlannerCoordinationDecision{{EventID: original.EventID, ExecutionRunID: snapshot.Run.ID, Source: "PLANNER", Outcome: PlannerRuntimeAmend, ResultJSON: string(raw), ResultSHA256: sha256Hex(raw)}}
	if blocked, reason := PlannerRuntimeBarrier(snapshot); !blocked || reason != ReasonPlannerRuntimePending {
		t.Fatal("decision without applied contracts opened the barrier", blocked, reason)
	}
	snapshot.PlannerRuntime.Amendments = []PlannerTaskAmendment{{EventID: original.EventID}}
	if blocked, reason := PlannerRuntimeBarrier(snapshot); blocked {
		t.Fatal("complete recovery still blocked", reason)
	}
	if !reflect.DeepEqual(original, snapshot.PlannerRuntime.Decisions[0]) {
		t.Fatal("effective decision changed historical STOP")
	}
	snapshot.PlannerRuntime.RecoveryDecisions[0].Source = "CONTROL_PLANE"
	snapshot.PlannerRuntime.RecoveryDecisions[0].Outcome = PlannerRuntimeStop
	snapshot.PlannerRuntime.RecoveryDecisions[0].ReasonCode = ReasonPlannerRuntimeUnavailable
	if blocked, _ := PlannerRuntimeBarrier(snapshot); !blocked {
		t.Fatal("second failure became success")
	}
}

func TestCoordinationRecoveryProgressKeepsOldAndCurrentOutcomesDistinct(t *testing.T) {
	snapshot := repairBarrierSnapshot(t, PlannerRuntimeStop, ReasonPlannerRuntimeUnavailable, nil)
	snapshot.PlannerRuntime.Recoveries = []PlannerCoordinationRecovery{{EventID: "event-1", Recovery: WorkflowRecovery{ID: "retry", ExecutionRunID: "run", Action: RecoveryRetryPlannerCoordination}}}
	var summary TrustedProgressSummary
	applyTrustedPlannerRuntime(&summary, TrustedProgressFacts{ComplexExecution: &snapshot})
	if len(summary.PlannerCoordination) != 1 || summary.PlannerCoordination[0].Decision != PlannerRuntimeStop || summary.PlannerCoordination[0].Recovery == nil || len(summary.CurrentWork) != 1 || len(summary.Blockers) != 0 {
		t.Fatal("pending recovery hid STOP or remained a failed current decision", summary)
	}
	before := trustedProgressFactHash(summary)
	summary.PlannerCoordination[0].RecoveryDecision = &PlannerCoordinationDecision{Outcome: PlannerRuntimeContinue}
	if trustedProgressFactHash(summary) == before {
		t.Fatal("new recovery outcome was omitted from progress freshness")
	}
}
