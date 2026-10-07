package cleardev

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// SameAgentStepAttempt compares immutable business evidence for idempotent replay.
func SameAgentStepAttempt(left, right AgentStepAttempt) bool {
	return left.ID == right.ID && left.DevelopmentRequirementID == right.DevelopmentRequirementID &&
		left.LogicalStepID == right.LogicalStepID && left.StepCategory == right.StepCategory &&
		left.StepKind == right.StepKind && left.AttemptNumber == right.AttemptNumber &&
		left.RoleBindingID == right.RoleBindingID && left.AOSessionID == right.AOSessionID &&
		left.ClientMessageID == right.ClientMessageID && left.PromptSHA256 == right.PromptSHA256 &&
		left.TriggerFailureEventID == right.TriggerFailureEventID
}

// SameAgentAttemptEvent compares immutable business evidence for idempotent replay.
func SameAgentAttemptEvent(left, right AgentAttemptEvent) bool {
	return left.ID == right.ID && left.AttemptID == right.AttemptID && left.Status == right.Status &&
		left.ClientMessageID == right.ClientMessageID && left.PromptSHA256 == right.PromptSHA256 &&
		left.TurnID == right.TurnID && left.TurnState == right.TurnState &&
		left.FailureCategory == right.FailureCategory && left.Retryable == right.Retryable &&
		sameOptionalTime(left.RetryAt, right.RetryAt) && left.ProviderErrorCode == right.ProviderErrorCode &&
		left.ErrorSummary == right.ErrorSummary
}

// SameAgentStepResult compares immutable business evidence for idempotent replay.
func SameAgentStepResult(left, right AgentStepResult) bool {
	return left.ID == right.ID && left.AttemptID == right.AttemptID && left.ResultIndex == right.ResultIndex &&
		left.Source == right.Source && left.ClientMessageID == right.ClientMessageID &&
		left.TurnID == right.TurnID && left.FinalMessageID == right.FinalMessageID &&
		left.RawMessageText == right.RawMessageText && left.RawMessageSHA256 == right.RawMessageSHA256
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

// SameAgentStepResultParse compares immutable parser evidence; replay keeps the first time.
func SameAgentStepResultParse(left, right AgentStepResultParse) bool {
	return left.ResultID == right.ResultID && left.Conclusion == right.Conclusion && left.ErrorSummary == right.ErrorSummary
}

// ValidAgentAttemptCreation rejects invented or inconsistent new creation evidence.
func ValidAgentAttemptCreation(attempt AgentStepAttempt) bool {
	return attempt.CreatedAt != nil && !attempt.CreatedAt.IsZero() && attempt.RequestedAt.Equal(*attempt.CreatedAt) && attempt.RequestedAtSemantics == AttemptTimeActualCreation
}

// ValidSecondAgentAttempt binds a new second attempt to the latest retryable terminal fact.
func ValidSecondAgentAttempt(first AgentStepAttempt, trigger, latest AgentAttemptEvent, second AgentStepAttempt) bool {
	terminal := trigger.Status == AgentAttemptFailed || trigger.Status == AgentAttemptInterrupted
	terminalTurn := trigger.TurnState == domain.TurnStateFailed || trigger.TurnState == domain.TurnStateInterrupted
	preSend := FailedBeforeSendEventValid(first, trigger) && SameAgentAttemptEvent(trigger, latest) && trigger.RecordedAt.Equal(latest.RecordedAt)
	return ValidAgentAttemptCreation(second) && first.AttemptNumber == 1 && second.AttemptNumber == 2 && second.TriggerFailureEventID == trigger.ID &&
		trigger.ID == latest.ID && trigger.AttemptID == first.ID && ((terminal && terminalTurn && trigger.Retryable) || preSend) &&
		second.DevelopmentRequirementID == first.DevelopmentRequirementID && second.LogicalStepID == first.LogicalStepID && second.StepCategory == first.StepCategory && second.StepKind == first.StepKind &&
		second.RoleBindingID == first.RoleBindingID && second.AOSessionID == first.AOSessionID &&
		second.PromptSHA256 == first.PromptSHA256 && second.ClientMessageID != first.ClientMessageID &&
		!second.CreatedAt.Before(trigger.RecordedAt) && (trigger.RetryAt == nil || !second.CreatedAt.Before(*trigger.RetryAt))
}
