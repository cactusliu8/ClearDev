package cleardev

import "time"

// MessageBudgetVersion fixes the accounting contract for a requirement.
type MessageBudgetVersion string

// Fixed accounting versions never upgrade historical requirements automatically.
const (
	MessageBudgetV1     MessageBudgetVersion = "MESSAGE_BUDGET_V1"
	MessageBudgetLegacy MessageBudgetVersion = "LEGACY_UNMEASURED"
)

// AgentMessageSource distinguishes the three permitted logical-step messages.
type AgentMessageSource string

// Stable sources and refusal reasons govern controlled message admission.
const (
	AgentMessageOriginal         AgentMessageSource = "ORIGINAL"
	AgentMessageRecoveryOriginal AgentMessageSource = "RECOVERY_ORIGINAL"
	AgentMessageParseCorrection  AgentMessageSource = "PARSE_CORRECTION"
	ReasonMessageBudgetUnknown   ReasonCode         = "MESSAGE_BUDGET_UNKNOWN"
	ReasonMessageBudgetExhausted ReasonCode         = "MESSAGE_BUDGET_EXHAUSTED"
	ReasonMessageBudgetBinding   ReasonCode         = "MESSAGE_BUDGET_BINDING_INVALID"
)

// AgentMessageReservation binds one send permission to immutable evidence.
type AgentMessageReservation struct {
	ClientMessageID          string
	DevelopmentRequirementID string
	BudgetVersion            MessageBudgetVersion
	LogicalStepID            string
	AttemptID                string
	Source                   AgentMessageSource
	AOSessionID              string
	PromptSHA256             string
	BudgetID                 string
	ReservedAt               time.Time
}

// ReserveAgentMessageCommand claims a message and its unknown-delivery boundary atomically.
type ReserveAgentMessageCommand struct {
	Attempt  AgentStepAttempt
	Source   AgentMessageSource
	Boundary AgentAttemptEvent
}

// SameAgentMessageReservation compares all business bindings, excluding the generated time.
func SameAgentMessageReservation(a, b AgentMessageReservation) bool {
	return a.ClientMessageID == b.ClientMessageID && a.DevelopmentRequirementID == b.DevelopmentRequirementID && a.BudgetVersion == b.BudgetVersion && a.LogicalStepID == b.LogicalStepID && a.AttemptID == b.AttemptID && a.Source == b.Source && a.AOSessionID == b.AOSessionID && a.PromptSHA256 == b.PromptSHA256 && a.BudgetID == b.BudgetID
}

// ValidAgentMessageIdentity enforces the stable message identifier and source per attempt.
func ValidAgentMessageIdentity(a AgentStepAttempt, source AgentMessageSource, messageID, digest string) bool {
	switch source {
	case AgentMessageOriginal:
		return a.AttemptNumber == 1 && messageID == a.ClientMessageID && digest == a.PromptSHA256
	case AgentMessageHumanAuthorizedOriginal:
		return a.AttemptNumber == 3 && messageID == a.ClientMessageID && digest == a.PromptSHA256
	case AgentMessageRecoveryOriginal:
		return a.AttemptNumber == 2 && messageID == a.ClientMessageID && digest == a.PromptSHA256
	case AgentMessageParseCorrection:
		if a.AttemptNumber == 3 {
			return false
		}
		return messageID == a.ClientMessageID+":parse-correction" && len(digest) == 64
	default:
		return false
	}
}

// MessageBudgetUsage separates step reservations from messages and confirmed sends.
type MessageBudgetUsage struct {
	Scope                  string               `json:"scope"`
	LogicalStepID          string               `json:"logicalStepId,omitempty"`
	BudgetID               string               `json:"budgetId,omitempty"`
	ExecutionRunID         string               `json:"executionRunId,omitempty"`
	ComplexExecutionTaskID string               `json:"complexExecutionTaskId,omitempty"`
	RoleKind               string               `json:"roleKind,omitempty"`
	BudgetVersion          MessageBudgetVersion `json:"budgetVersion"`
	MaxSteps               *int64               `json:"maxSteps"`
	ReservedSteps          *int64               `json:"reservedSteps"`
	RemainingSteps         *int64               `json:"remainingSteps"`
	MaxMessages            *int64               `json:"maxMessages"`
	ReservedMessages       *int64               `json:"reservedMessages"`
	ConfirmedSentMessages  *int64               `json:"confirmedSentMessages"`
	RemainingMessages      *int64               `json:"remainingMessages"`
	UnknownReason          string               `json:"unknownReason,omitempty"`
}

// MessageBudgetView is computed from one database snapshot; legacy usage stays unknown.
type MessageBudgetView struct {
	ReservedMessages      *int64               `json:"reservedMessages"`
	ConfirmedSentMessages *int64               `json:"confirmedSentMessages"`
	RemainingMessages     *int64               `json:"remainingMessages"`
	MaxMessages           *int64               `json:"maxMessages"`
	BudgetVersion         MessageBudgetVersion `json:"budgetVersion"`
	UnknownReason         string               `json:"unknownReason,omitempty"`
	Steps                 []MessageBudgetUsage `json:"steps"`
	Roles                 []MessageBudgetUsage `json:"roles"`
}
