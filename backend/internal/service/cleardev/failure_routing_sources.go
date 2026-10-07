package cleardev

import (
	"context"
	"errors"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Generic role-unavailable codes must not hide an account, authority or unknown
// delivery failure recorded by the native transport. Never infer credentials or
// quotas from a model's prose or treat missing transport evidence as success.
func protectNativeFailure(input *core.WorkflowFailureInput, attempt core.AgentStepAttemptView) {
	switch attempt.FailureCategory {
	case domain.AgentFailureAuthentication, domain.AgentFailureQuotaExhausted, domain.AgentFailureModelUnavailable, domain.AgentFailureDriverIncompatible:
		input.ProblemCode = "AUTHORITY"
		input.ReasonCode = string(attempt.FailureCategory)
	case domain.AgentFailureDeliveryUnknown, domain.AgentFailureObservationTimeout:
		input.ReasonCode = string(attempt.FailureCategory)
		input.Eligible = false
	case domain.AgentFailureTurnInterrupted, domain.AgentFailureSessionLost:
		// An interrupted provider turn may be the person's stop. Only an
		// existing explicitly permitted recovery may establish otherwise.
		input.Eligible = false
	}
}

func (s *Service) protectPlanningFailure(ctx context.Context, id string, c *workflowFailureCandidate) {
	if s.attempts == nil {
		c.input.Eligible = false
		return
	}
	var attemptID string
	switch c.option.Action {
	case core.RecoveryRetryPlanningStep:
		store, ok := s.complex.(planningStepRecoveryStore)
		if !ok {
			c.input.Eligible = false
			return
		}
		state, err := store.ReadClearDevPlanningStepRecovery(ctx, id, s.now().UTC())
		if err != nil || state.Option.TargetID != c.option.TargetID {
			c.input.Eligible = false
			return
		}
		attemptID = state.Binding.FirstAttemptID
	case core.RecoveryRetryPlannerCoordination:
		store, ok := s.complexExecution.(plannerCoordinationRecoveryStore)
		if !ok {
			c.input.Eligible = false
			return
		}
		state, err := store.ReadClearDevPlannerCoordinationRecovery(ctx, id, s.now().UTC())
		if err != nil || state.Option.TargetID != c.option.TargetID {
			c.input.Eligible = false
			return
		}
		attemptID = state.Binding.FirstAttemptID
	default:
		return
	}
	first, err := s.attempts.GetClearDevAgentAttemptState(ctx, id, attemptID)
	if err != nil || first.ID == "" {
		c.input.Eligible = false
		return
	}
	protectNativeFailure(&c.input, first)
	if c.input.ReasonCode == "" {
		c.input.ReasonCode = string(first.FailureCategory)
	}
}

// An independently settled task BLOCKED returns the artifact to its Builder.
// A still-running requested check is not settled merely because a Reviewer
// replied. Original receipt/resource and both native sessions remain barriers.
func (s *Service) reviewFailureRepairReady(ctx context.Context, e core.ComplexExecutionSnapshot, d core.ComplexExecutionDispatch) (bool, error) {
	if d.ReasonCode != "REVIEW_BLOCKED" || d.CandidateCommitID == "" || d.CandidateCommitSHA == "" {
		return false, nil
	}
	if _, err := s.failureCoordinationWorkspace(ctx, e, d.ID); err != nil {
		return false, err
	}
	for _, review := range e.Reviews {
		if review.DispatchID != d.ID || review.Status != core.LocalReviewStatusSettled || review.Verdict != core.LocalReviewBlocked || review.CandidateCommitID != d.CandidateCommitID || review.CandidateCommitSHA != d.CandidateCommitSHA {
			continue
		}
		binding, bound := complexExecutionBindingByID(e, review.ReviewerRoleBindingID)
		builder, builderFound := complexExecutionBindingByID(e, d.BuilderRoleBindingID)
		if !bound || !builderFound || binding.AOSessionID == "" || binding.AOSessionID == builder.AOSessionID || binding.WorkspacePath == builder.WorkspacePath {
			return false, nil
		}
		snapshot, err := s.chat.Snapshot(ctx, domain.SessionID(binding.AOSessionID))
		if err != nil || snapshot.SessionID != domain.SessionID(binding.AOSessionID) {
			return false, err
		}
		for _, turn := range snapshot.Turns {
			if !turn.State.Terminal() {
				return false, nil
			}
		}
		stepID := review.AgentStepID
		store, ok := s.complexExecution.(ReviewerCheckStore)
		if !ok {
			return false, errors.New("reviewer check evidence is unavailable")
		}
		request, results, requested, err := store.GetClearDevReviewerChecks(ctx, review.ID)
		if err != nil {
			return false, err
		}
		if requested {
			if request.CandidateSHA != review.CandidateCommitSHA || request.PacketSHA256 != review.ReviewPacketSHA256 || len(results) != len(request.CheckIDs) {
				return false, nil
			}
			stepID = core.ReviewCheckFollowupID(review.ID)
			for _, result := range results {
				if evidenceErr := core.ValidateReviewCheckEvidence(request, result); evidenceErr != nil {
					return false, evidenceErr
				}
				if result.Outcome != "INFRA_ERROR" && result.Outcome != "TIMED_OUT" {
					continue
				}
				spec, valid := core.ReviewCheckSpec(request, result.CheckID)
				if !valid {
					return false, nil
				}
				reader, ok := s.checks.(interface {
					CanRetryWorkflowCheck(context.Context, ports.ClearDevCheckRequest) (bool, error)
				})
				if !ok {
					return false, nil
				}
				check := ports.ClearDevCheckRequest{RunID: core.ReviewCheckRunID(review.ID, result.CheckID), WorkspacePath: binding.WorkspacePath, CandidateSHA: review.CandidateCommitSHA, ProjectExecution: request.ProjectExecution, Image: core.StandardCandidateCheckImage, Argv: spec.Argv, Timeout: time.Duration(spec.TimeoutSeconds) * time.Second, MemoryBytes: standardCheckMemoryBytes, PidsLimit: standardCheckPidsLimit, OutputLimit: standardCheckOutputLimit}
				settled, err := reader.CanRetryWorkflowCheck(ctx, check)
				if err != nil || !settled {
					return false, err
				}
			}
		}
		step, found := complexExecutionStepByID(e, stepID)
		if !found || step.SendStatus != core.AgentStepSendStatusSettled || step.MessageSHA256 != coreDigest([]byte(step.FinalMessageText)) {
			return false, nil
		}
		completed, replied := false, false
		for _, turn := range snapshot.Turns {
			completed = completed || turn.ID == step.TurnID && turn.State == domain.TurnStateCompleted
		}
		for _, message := range snapshot.Messages {
			replied = replied || message.ID == step.FinalMessageID && message.TurnID == step.TurnID && message.Role == domain.MessageRoleAssistant && message.Text == step.FinalMessageText
		}
		return completed && replied, nil
	}
	return false, nil
}
