package cleardev

import (
	"context"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// shouldContinueProjectObservation recognizes only a timed-out observation of
// the current admitted project. It does not recover a failed provider, replace
// a session, or resend a message. Each advance still applies the normal gates.
func (s *Service) shouldContinueProjectObservation(ctx context.Context, requirementID string) (bool, error) {
	if s.attempts == nil || s.facts == nil {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil || !found {
		return false, err
	}
	_, admitted, err := core.ProjectContractFromRun(execution.Run)
	if err != nil || !admitted {
		return false, err
	}
	phase, _ := core.DeriveComplexExecutionPhase(execution)
	if phase == core.ComplexExecutionCompleted || phase == core.ComplexExecutionBlocked || phase == core.ComplexExecutionNeedsHuman {
		return false, nil
	}
	snapshot, found, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil || !found || snapshot.Requirement.CancelledAt != nil {
		return false, err
	}
	version, confirmed := currentConfirmedVersion(snapshot)
	if !confirmed || version.ID != execution.Run.RequirementVersionID || version.SHA256 != execution.Run.RequirementSHA256 {
		return false, nil
	}
	state, stateErr := s.projectPlanningState(ctx, requirementID)
	if stateErr != nil || state == nil || !state.Current || !state.SourceCurrent || state.ReasonCode != core.ReasonNone {
		return false, stateErr
	}
	if s.direction != nil {
		stopped, stopErr := s.direction.HasActiveClearDevDirectionStop(ctx, execution.Run.RequirementVersionID)
		if stopErr != nil || stopped {
			return false, stopErr
		}
	}
	views, err := s.attempts.ListLatestClearDevAgentAttemptStates(ctx, requirementID)
	if err != nil {
		return false, err
	}
	for _, view := range views {
		if view.SendStatus != core.AgentAttemptObservationTimedOut {
			continue
		}
		if view.StepCategory != core.AgentStepCategoryComplexExecution && view.StepCategory != core.AgentStepCategoryException && view.StepCategory != core.AgentStepCategoryQuickExecution {
			continue
		}
		runnable, readErr := s.agentAttemptStillRunnable(ctx, requirementID, view)
		if readErr != nil || runnable {
			return runnable, readErr
		}
	}
	return false, nil
}

func waitForProjectObservation(ctx context.Context) bool {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
