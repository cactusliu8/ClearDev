package cleardev

import (
	"context"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Two bounded read samples detect a moving binding without advancing any workflow.
// Both samples use the same clock. Full attempt text is only read for the detail response.
func (s *Service) getProgressRequirement(ctx context.Context, id string, now time.Time, history bool) (RequirementView, error) {
	first, err := s.readProgressRequirement(ctx, id, now)
	if err != nil {
		return RequirementView{}, err
	}
	second, err := s.readProgressRequirement(ctx, id, now)
	if err != nil {
		return RequirementView{}, err
	}
	// Compare only current business facts: explanation/history writes must not invalidate their own summary.
	if first.TrustedProgress.FactSummarySHA256 != second.TrustedProgress.FactSummarySHA256 {
		second.TrustedProgress = core.UncertainTrustedProgress(second.TrustedProgress)
		if second.ComplexExecution != nil {
			second.ComplexExecution.Phase, second.ComplexExecution.PhaseReason, second.ComplexExecution.MissingEvidence = core.ComplexExecutionBlocked, "CURRENT_BINDINGS_CHANGED", []string{"CONSISTENT_CURRENT_BINDINGS"}
		}
		if second.QuickExecution != nil {
			second.QuickExecution.Phase, second.QuickExecution.PhaseReason, second.QuickExecution.MissingEvidence = core.ComplexExecutionBlocked, "CURRENT_BINDINGS_CHANGED", []string{"CONSISTENT_CURRENT_BINDINGS"}
		}
	}
	if history && s.attempts != nil {
		attempts, err := s.attempts.ListClearDevAgentStepAttempts(ctx, id)
		if err != nil {
			return RequirementView{}, apierr.Internal("CLEARDEV_AGENT_ATTEMPT_READ_FAILED", "Could not read ClearDev Agent attempt evidence")
		}
		var rows []core.ProgressExplanationRequest
		if s.progress != nil {
			rows, err = s.progress.ListClearDevProgressExplanations(ctx, id)
			if err != nil {
				return RequirementView{}, err
			}
		}
		second.AgentStepAttempts = mergeLegacyAgentAttempts(second, attempts, rows)
	}
	return second, nil
}

func (s *Service) readControlledProgress(ctx context.Context, id string, facts core.TrustedProgressFacts, budget *core.MessageBudgetView) ([]core.ControlledProgressFacts, error) {
	targets := core.CurrentControlledTargets(facts)
	for i := range targets {
		f := &targets[i]
		if budget != nil {
			f.BudgetVersion = budget.BudgetVersion
			if budget.BudgetVersion != core.MessageBudgetV1 {
				f.BudgetUnknown = budget.UnknownReason
			}
			for _, usage := range budget.Steps {
				if usage.LogicalStepID == f.Target.LogicalStepID {
					u := usage
					f.Target.StepBudget = &u
				}
			}
			for _, usage := range budget.Roles {
				if usage.ExecutionRunID == f.Target.ExecutionRunID && usage.ComplexExecutionTaskID == f.Target.TaskMappingID && usage.RoleKind == f.Target.Role {
					u := usage
					f.Target.RoleBudget = &u
				}
			}
		} else {
			f.BudgetUnknown = "MESSAGE_BUDGET_STORE_UNAVAILABLE"
		}
		if f.Target.LogicalStepID != "" {
			if s.attempts == nil {
				f.BudgetUnknown = "ATTEMPT_STORE_UNAVAILABLE"
			} else {
				rows, err := s.attempts.ListClearDevAgentStepAttemptStates(ctx, id, f.Target.LogicalStepID)
				if err != nil {
					return nil, apierr.Internal("CLEARDEV_AGENT_ATTEMPT_READ_FAILED", "Could not read current ClearDev attempt states")
				}
				f.Attempts = rows
			}
		}
		// Reviewer replacement is usable only with the exact persisted action result.
		if r := f.Recovery; r != nil && r.Claim != nil && r.Claim.Action == core.ComplexRecoveryActionRebuildReviewer && r.Result != nil && r.Result.Outcome == "PASS" {
			for _, a := range f.Attempts {
				if a.AttemptNumber == 2 {
					f.Target.RoleBindingID, f.Target.AOSessionID = r.Result.RoleBindingID, r.Result.SessionID
				}
			}
			f.BindingUnknown = true
			if facts.ComplexExecution != nil && r.Result.RoleBindingID == r.Claim.OperationID+":reviewer" {
				for _, b := range facts.ComplexExecution.RoleBindings {
					if b.ID == r.Result.RoleBindingID && b.AOSessionID == r.Result.SessionID && b.Role == core.StandardRoleReviewer && b.TaskMappingID == r.Request.TaskID && b.CandidateCommitID == r.Request.CandidateID {
						f.BindingUnknown = false
					}
				}
			}
		}
		if s.preflights != nil && f.Target.RoleBindingID != "" {
			p, ok, err := s.preflights.GetLatestClearDevControlledPreflightForBinding(ctx, f.Target.RoleBindingID)
			if err != nil {
				return nil, apierr.Internal("CLEARDEV_PREFLIGHT_READ_FAILED", "Could not read the current role preflight")
			}
			if ok {
				f.Preflight = &p
			}
		}
	}
	return targets, nil
}
