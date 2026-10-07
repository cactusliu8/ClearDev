package cleardev

import (
	"context"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Approval never repairs an unknown or moving cross-task source. Observe the
// original native reply and clean candidate before the transactional grant;
// normal workflow recovery and actual dispatch repeat their source gates.
func (s *Service) crossTaskCoordinationRepairReady(ctx context.Context, binding core.CoordinationRepairBinding) error {
	if s.complexExecution == nil {
		return errors.New("coordination repair source store is unavailable")
	}
	e, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, binding.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	if !found || e.Run.ID != binding.ExecutionRunID || e.PlannerRuntime == nil {
		return errors.New("coordination repair execution is unavailable")
	}
	var event core.PlannerCoordinationEvent
	for _, candidate := range e.PlannerRuntime.Events {
		if candidate.ID == binding.EventID {
			event = candidate
		}
	}
	dispatch, found := complexExecutionDispatchByID(e, event.DispatchID)
	if !found {
		return errors.New("coordination repair source is unavailable")
	}
	if dispatch.ComplexExecutionTaskID == binding.TaskID || dispatch.DevelopmentTaskID == binding.TaskID {
		return nil // The same-task path retains its existing authorization gates.
	}
	if err := s.workflowRecoveryCurrent(ctx, e); err != nil {
		return err
	}
	if !core.PlannerRuntimeQuiescent(e) || dispatch.Status != core.ComplexExecutionDispatchBlocked || dispatch.ReasonCode != "BUILDER_BLOCKED" || dispatch.SettledAt == nil {
		return errors.New("cross-task coordination source has not settled")
	}
	record, reason := s.workflowRecoverySession(ctx, e, dispatch, core.RecoveryContinueBuilder)
	if reason != "" || record.Activity.State != domain.ActivityIdle || record.Metadata.ProviderConversationID == "" || s.chat == nil || s.inspector == nil {
		return errors.New("cross-task coordination original Builder is unavailable or busy")
	}
	var step core.AgentStep
	for _, candidate := range e.AgentSteps {
		if candidate.ID == dispatch.AgentStepID {
			step = candidate
		}
	}
	if step.SendStatus != core.AgentStepSendStatusSettled || step.MessageSHA256 != event.SourceMessageSHA256 || coreDigest([]byte(step.FinalMessageText)) != event.SourceMessageSHA256 {
		return errors.New("cross-task coordination source reply changed")
	}
	snapshot, err := s.chat.Snapshot(ctx, record.ID)
	if err != nil || snapshot.SessionID != record.ID {
		return errors.New("cross-task coordination native delivery is unknown")
	}
	completed, replied := false, false
	for _, turn := range snapshot.Turns {
		if !turn.State.Terminal() {
			return errors.New("cross-task coordination still has an active native turn")
		}
		completed = completed || turn.ID == step.TurnID && turn.State == domain.TurnStateCompleted
	}
	for _, message := range snapshot.Messages {
		replied = replied || message.ID == step.FinalMessageID && message.TurnID == step.TurnID && message.Role == domain.MessageRoleAssistant && message.Text == step.FinalMessageText
	}
	if !completed || !replied {
		return errors.New("cross-task coordination original reply is unconfirmed")
	}
	expected := dispatch.BaseCommitSHA
	if previous, found := lastFrozenDispatchCandidate(e, dispatch.ComplexExecutionTaskID, dispatch.Round); found {
		expected = previous
	}
	inspection, err := s.inspectExecutionCandidate(ctx, e.Run, record.Metadata.WorkspacePath, dispatch.BaseCommitSHA)
	if err != nil || inspection.BaseSHA != dispatch.BaseCommitSHA || inspection.CandidateSHA != expected {
		return errors.New("cross-task coordination original candidate changed")
	}
	if e.FinalReview != nil {
		settled, err := s.plannerHistoricalFinalCommandsSettled(ctx, e)
		if err != nil || !settled {
			return errors.New("cross-task coordination trial actions are not settled")
		}
	}
	return nil
}
