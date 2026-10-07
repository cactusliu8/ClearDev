package cleardev

import (
	"encoding/json"
	"slices"
)

// ComplexExecutionCoordinating is a derived phase of the existing execution,
// not a separately persisted lifecycle or completion state.
const ComplexExecutionCoordinating ComplexExecutionPhase = "COORDINATING"

// PlannerRuntimeBarrier uses only durable history. Applied amendments must
// have every expected packet; a stop, absent request history or incomplete
// application never becomes permission to dispatch or complete.
func PlannerRuntimeBarrier(snapshot ComplexExecutionSnapshot) (bool, ReasonCode) {
	enabled, err := PlannerRuntimeRun(snapshot.Run)
	if err != nil || (!enabled && snapshot.PlannerRuntime != nil) {
		return true, ReasonPreconditionNotMet
	}
	if !enabled {
		return false, ReasonNone
	}
	if snapshot.PlannerRuntime == nil {
		// Historical project packets predate coordination and have no history.
		// Storage always loads current events and its barriers independently.
		if _, project, err := ProjectAdmissionContractFromRun(snapshot.Run); err == nil && project && snapshot.Run.RuntimeProjectExecution == nil {
			return false, ReasonNone
		}
		return true, ReasonPlannerRuntimePending
	}
	history := snapshot.PlannerRuntime
	pending := false
	for _, event := range history.Events {
		decision, decided := PlannerRuntimeEffectiveDecision(history, event.ID)
		if !decided {
			pending = true
			continue
		}
		if decision.ExecutionRunID != snapshot.Run.ID {
			return true, ReasonPreconditionNotMet
		}
		if decision.Outcome != PlannerRuntimeContinue && decision.Outcome != PlannerRuntimeAmend {
			if StoppedCheckRecoveryResolvesStop(snapshot, decision) {
				continue
			}
			// A control-plane STOP resolved by an explicit human coordination
			// repair authorization is decided history: the recorded STOP stays
			// and no longer blocks. Without such a grant a stop never becomes
			// permission to dispatch or complete.
			if decision.Source == "CONTROL_PLANE" && decision.Outcome == PlannerRuntimeStop && slices.Contains(history.RepairAuthorizations, event.ID) {
				continue
			}
			if decision.ReasonCode == ReasonNone {
				return true, ReasonPlannerRuntimeStopped
			}
			return true, decision.ReasonCode
		}
		if decision.Source != "PLANNER" || decision.ResultSHA256 != sha256Hex([]byte(decision.ResultJSON)) {
			return true, ReasonPreconditionNotMet
		}
		if decision.Outcome == PlannerRuntimeAmend {
			var result PlannerCoordinationResult
			if json.Unmarshal([]byte(decision.ResultJSON), &result) != nil || len(result.Amendments) == 0 {
				return true, ReasonPreconditionNotMet
			}
			count := 0
			for _, amendment := range history.Amendments {
				if amendment.EventID == event.ID {
					count++
				}
			}
			if count != len(result.Amendments) {
				return true, ReasonPlannerRuntimePending
			}
		}
	}
	if pending {
		return true, ReasonPlannerRuntimePending
	}
	return false, ReasonNone
}

// PlannerRuntimeQuiescent lets already-reserved work finish normally before a
// Planner observes a frozen coordination boundary. It never admits a new task.
func PlannerRuntimeQuiescent(snapshot ComplexExecutionSnapshot) bool {
	for _, check := range snapshot.CheckRuns {
		if check.Status == ComplexExecutionCheckRunPending || check.Status == ComplexExecutionCheckRunStarted {
			return false
		}
	}
	if snapshot.FinalReview != nil && snapshot.FinalReview.Status != "SETTLED" && snapshot.FinalReview.Status != "FAILED" {
		return false
	}
	for _, dispatch := range snapshot.Dispatches {
		switch dispatch.Status {
		case ComplexExecutionDispatchPending, ComplexExecutionDispatchRunning, ComplexExecutionDispatchObserved, ComplexExecutionDispatchReviewing:
			return false
		}
	}
	if snapshot.Exception != nil {
		for _, step := range snapshot.Exception.OnDemandSteps {
			if step.SendStatus == AgentStepSendStatusPending || step.SendStatus == AgentStepSendStatusSent {
				return false
			}
		}
	}
	return true
}
