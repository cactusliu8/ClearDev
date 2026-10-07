package cleardev

import (
	"context"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// resumePendingComplexBuilderDelivery only observes a delivery already reserved
// by the trusted sender. No attempt, message, budget or provider call is created.
// In particular, an unknown delivery is reconciled against the exact original
// session/message/prompt, not resent because the process has restarted.
func (s *Service) resumePendingComplexBuilderDelivery(ctx context.Context, requirementID string, step core.AgentStep, sessionID, prompt string) (bool, error) {
	var bindingErr error
	step, bindingErr = s.replacementEffectiveStep(ctx, step)
	if bindingErr != nil {
		return true, bindingErr
	}
	views, err := s.attempts.ListClearDevAgentStepAttemptStates(ctx, requirementID, step.ID)
	if err != nil {
		return false, err
	}
	items := logicalStepAttemptViews(views, step.ID)
	if len(items) == 0 {
		return false, nil // first send still requires the pristine-base check
	}
	view := items[len(items)-1]
	if view.AOSessionID != sessionID || view.RoleBindingID != step.RoleBindingID || view.StepCategory != core.AgentStepCategoryComplexExecution || view.StepKind != step.Kind || view.PromptSHA256 != coreDigest([]byte(prompt)) || view.ID != agentAttemptID(step.ID, view.AttemptNumber) || view.ClientMessageID != agentAttemptClientMessageID(step.ClientMessageID, view.AttemptNumber, false) {
		return true, errors.New("pending Builder delivery no longer matches its frozen binding")
	}
	delivery, err := s.attempts.GetClearDevAgentDeliveryState(ctx, requirementID, view.ID, view.ClientMessageID)
	if err != nil {
		return true, err
	}
	found, deliveryErr := savedAgentDelivery([]core.AgentStepAttemptView{delivery}, view.ID, view.ClientMessageID)
	if !found {
		return false, nil // merely allocating an attempt does not authorize a send
	}
	if delivery.SendStatus == core.AgentAttemptDeliveryUnknown {
		deliveryErr = s.reconcileUnknownAgentDelivery(ctx, agentAttemptFromView(requirementID, view), prompt, view.ClientMessageID, core.AgentAttemptSent, s.now().UTC())
	} else if deliveryErr == nil && delivery.TurnID == "" {
		deliveryErr = errors.New("saved Builder delivery has no provider turn")
	}
	if deliveryErr != nil {
		return true, deliveryErr
	}
	_, err = s.complexExecution.MarkClearDevComplexExecutionAgentStepSent(ctx, step.ID, s.now().UTC())
	return true, err
}
