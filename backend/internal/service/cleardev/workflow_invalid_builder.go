package cleardev

import (
	"context"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type invalidBuilderResultReader interface {
	ReadClearDevStoppedBuilderResult(context.Context, string, string) (string, bool, error)
}

func (s *Service) invalidBuilderSessionSettled(ctx context.Context, id domain.SessionID) bool {
	if s.chat == nil {
		return false
	}
	snapshot, err := s.chat.Snapshot(ctx, id)
	if err != nil || snapshot.SessionID != id || len(snapshot.Turns) == 0 {
		return false
	}
	for _, turn := range snapshot.Turns {
		if !turn.State.Terminal() {
			return false
		}
	}
	return true
}

// Before sending a new round, recheck the exact stopped result and native
// conversation recorded by its recovery. An accepted request is not a license
// to send after its source changes.
func (s *Service) validateInvalidBuilderContinuation(ctx context.Context, e core.ComplexExecutionSnapshot, dispatch core.ComplexExecutionDispatch) error {
	for _, recovery := range e.WorkflowRecoveries {
		if recovery.Action != core.RecoveryContinueBuilder || (recovery.OriginalReason != "BUILDER_RESULT_INVALID" && recovery.OriginalReason != "BUILDER_UNAVAILABLE") || recovery.TaskID != dispatch.ComplexExecutionTaskID {
			continue
		}
		for _, old := range e.Dispatches {
			if old.ID != recovery.DispatchID || old.Round+1 != dispatch.Round {
				continue
			}
			reader, supported := s.complexExecution.(invalidBuilderResultReader)
			if !supported || recovery.BindingID != dispatch.BuilderRoleBindingID {
				return ports.ErrClearDevCandidateInvalid
			}
			summary, known, err := reader.ReadClearDevStoppedBuilderResult(ctx, e.Run.ID, old.ID)
			if err != nil {
				return err
			}
			if !known || summary != recovery.OriginalSummary {
				return ports.ErrClearDevCandidateInvalid
			}
			record, reason := s.workflowRecoverySession(ctx, e, old, recovery.Action)
			if reason != "" || recovery.ProviderConversationID == "" || record.Metadata.ProviderConversationID != recovery.ProviderConversationID ||
				(record.Activity.State != domain.ActivityIdle && record.Activity.State != domain.ActivityExited) || !s.invalidBuilderSessionSettled(ctx, record.ID) {
				return ports.ErrClearDevCandidateInvalid
			}
		}
	}
	return nil
}

// A rejected JSON result is not a settled business answer. Enrich only this
// stop with independently read execution evidence, never a guessed success.
func (s *Service) workflowRecoveryOptions(ctx context.Context, e core.ComplexExecutionSnapshot) []core.WorkflowRecoveryOption {
	options := core.WorkflowRecoveryOptions(e)
	reader, supported := s.complexExecution.(invalidBuilderResultReader)
	for i := range options {
		o := &options[i]
		if o.Action == core.RecoveryRetryStage && o.UnavailableReason == "RESULT_NOT_SETTLED" {
			if allowed, err := s.finalReviewInputRejected(ctx, e.FinalReview); err == nil && allowed {
				o.UnavailableReason = ""
			}
		}
		if o.Action != core.RecoveryContinueBuilder || (o.Reason != "BUILDER_RESULT_INVALID" && o.Reason != "BUILDER_UNAVAILABLE") || !supported {
			continue
		}
		summary, known, err := reader.ReadClearDevStoppedBuilderResult(ctx, e.Run.ID, o.TargetID)
		if err != nil || !known {
			continue
		}
		o.Summary = summary
		for _, dispatch := range e.Dispatches {
			if dispatch.ID != o.TargetID {
				continue
			}
			record, reason := s.workflowRecoverySession(ctx, e, dispatch, o.Action)
			if reason != "" {
				o.UnavailableReason = reason
				break
			}
			if record.Metadata.ProviderConversationID == "" {
				o.UnavailableReason = "RECOVERY_NATIVE_IDENTITY_MISSING"
				break
			}
			if (record.Activity.State != domain.ActivityIdle && record.Activity.State != domain.ActivityExited) || !s.invalidBuilderSessionSettled(ctx, record.ID) {
				break
			}
			o.UnavailableReason = "BUILDER_BUDGET_EXHAUSTED"
			if e.Exception == nil {
				break
			}
			for _, budget := range e.Exception.Budgets {
				if budget.ComplexExecutionTaskID != dispatch.ComplexExecutionTaskID || budget.RoleKind != core.ComplexExceptionBudgetBuilder {
					continue
				}
				if budget.UsedTurns < budget.MaxTurns+budget.AuthorizedExtraTurns && dispatch.Round+1 <= budget.MaxReworkCount+budget.AuthorizedExtraTurns {
					o.UnavailableReason = ""
				} else if budget.AuthorizedExtraTurns >= 2 {
					o.UnavailableReason = "ROLE_BUDGET_LIMIT"
				}
			}
		}
	}
	return options
}
