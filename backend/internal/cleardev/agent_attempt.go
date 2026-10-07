package cleardev

import (
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// AgentStepCategory identifies which durable ClearDev workflow owns a logical
// step. It is evidence routing, not another business state machine.
type AgentStepCategory string

// Agent step categories identify the workflow that owns the evidence.
const (
	AgentStepCategoryStandard         AgentStepCategory = "STANDARD"
	AgentStepCategoryComplexPlanning  AgentStepCategory = "COMPLEX_PLANNING"
	AgentStepCategoryDirection        AgentStepCategory = "DIRECTION_CHANGE"
	AgentStepCategoryComplexExecution AgentStepCategory = "COMPLEX_EXECUTION"
	AgentStepCategoryQuickExecution   AgentStepCategory = "QUICK_EXECUTION"
	AgentStepCategoryException        AgentStepCategory = "CONTROLLED_EXCEPTION"
	AgentStepCategoryProgress         AgentStepCategory = "PROGRESS_EXPLANATION"
)

// AgentAttemptSendStatus is the latest append-only delivery observation. A
// delivery-unknown status is intentionally neither SENT nor FAILED.
type AgentAttemptSendStatus string

// Agent attempt send statuses are append-only delivery observations.
const (
	AgentAttemptPending             AgentAttemptSendStatus = "PENDING"
	AgentAttemptSent                AgentAttemptSendStatus = "SENT"
	AgentAttemptCorrectionSent      AgentAttemptSendStatus = "CORRECTION_SENT"
	AgentAttemptCompleted           AgentAttemptSendStatus = "COMPLETED"
	AgentAttemptFailed              AgentAttemptSendStatus = "FAILED"
	AgentAttemptInterrupted         AgentAttemptSendStatus = "INTERRUPTED"
	AgentAttemptObservationTimedOut AgentAttemptSendStatus = "OBSERVATION_TIMEOUT"
	AgentAttemptDeliveryUnknown     AgentAttemptSendStatus = "DELIVERY_UNKNOWN"
	AgentAttemptFailedBeforeSend    AgentAttemptSendStatus = "FAILED_BEFORE_SEND"
)

// FailedBeforeSendEventValid validates the shape and immutable original-message
// binding of local no-send proof. The storage gate additionally requires its
// existing reservation/boundary and rejects all contrary delivery evidence.
func FailedBeforeSendEventValid(attempt AgentStepAttempt, event AgentAttemptEvent) bool {
	return event.ID != "" && attempt.ID != "" && event.AttemptID == attempt.ID &&
		event.Status == AgentAttemptFailedBeforeSend && (attempt.AttemptNumber == 1 || attempt.AttemptNumber == 2) &&
		attempt.ClientMessageID != "" && event.ClientMessageID == attempt.ClientMessageID &&
		len(attempt.PromptSHA256) == 64 && strings.Trim(attempt.PromptSHA256, "0123456789abcdef") == "" && event.PromptSHA256 == attempt.PromptSHA256 &&
		event.TurnID == "" && event.TurnState == "" && event.FailureCategory == "" &&
		event.Retryable && event.RetryAt == nil && event.ProviderErrorCode == "" &&
		!event.RecordedAt.IsZero() && !event.RecordedAt.Before(attempt.RequestedAt)
}

// FailedBeforeSendViewValid validates persisted no-send proof without inventing
// a provider turn or treating an absent snapshot as positive no-send evidence.
func FailedBeforeSendViewValid(view AgentStepAttemptView) bool {
	return view.ID != "" && view.LastEventID != "" && view.SendStatus == AgentAttemptFailedBeforeSend &&
		(view.AttemptNumber == 1 || view.AttemptNumber == 2) && view.ClientMessageID != "" &&
		view.LastClientMessageID == view.ClientMessageID && len(view.PromptSHA256) == 64 &&
		strings.Trim(view.PromptSHA256, "0123456789abcdef") == "" &&
		view.TurnID == "" && view.TurnState == "" && view.FailureCategory == "" &&
		view.Retryable && view.RetryAt == nil && view.ProviderErrorCode == "" &&
		view.LastObservedAt != nil && !view.LastObservedAt.IsZero()
}

// AgentResultSource distinguishes the original reply from its one correction.
type AgentResultSource string

// Agent result sources are fixed by ADR-0012.
const (
	AgentResultOriginal   AgentResultSource = "ORIGINAL"
	AgentResultCorrection AgentResultSource = "PARSE_CORRECTION"
)

// AgentResultParseConclusion is the parser outcome for one saved raw result.
type AgentResultParseConclusion string

// Agent result parse conclusions never rewrite the saved raw result.
const (
	AgentResultParseUnparsed AgentResultParseConclusion = "UNPARSED"
	AgentResultParseValid    AgentResultParseConclusion = "VALID"
	AgentResultParseInvalid  AgentResultParseConclusion = "INVALID"
)

// AttemptTimeSemantics identifies what the preserved requestedAt records.
type AttemptTimeSemantics string

// Attempt time semantics distinguish actual creation from preserved legacy timestamps.
const (
	AttemptTimeActualCreation      AttemptTimeSemantics = "ACTUAL_CREATION"
	AttemptTimeLegacyStepRequest   AttemptTimeSemantics = "LEGACY_STEP_REQUEST"
	AttemptTimeLegacyRetryBoundary AttemptTimeSemantics = "LEGACY_RETRY_BOUNDARY"
	AttemptTimeUnknown             AttemptTimeSemantics = "UNKNOWN"
)

// AgentStepAttempt is created before a message is sent. Controlled recovery
// permits at most one second attempt for the same logical step.
type AgentStepAttempt struct {
	ID                       string
	DevelopmentRequirementID string
	LogicalStepID            string
	StepCategory             AgentStepCategory
	StepKind                 AgentStepKind
	AttemptNumber            int64
	RoleBindingID            string
	AOSessionID              string
	ClientMessageID          string
	PromptSHA256             string
	TriggerFailureEventID    string
	RequestedAt              time.Time
	CreatedAt                *time.Time
	RequestedAtSemantics     AttemptTimeSemantics
}

// AgentAttemptEvent is one immutable delivery or observation fact.
type AgentAttemptEvent struct {
	ID                string
	AttemptID         string
	Status            AgentAttemptSendStatus
	ClientMessageID   string
	PromptSHA256      string
	TurnID            string
	TurnState         domain.TurnState
	FailureCategory   domain.AgentFailureCategory
	Retryable         bool
	RetryAt           *time.Time
	ProviderErrorCode string
	ErrorSummary      string
	RecordedAt        time.Time
}

// AgentStepResult stores exact provider output before protocol parsing.
type AgentStepResult struct {
	ID               string
	AttemptID        string
	ResultIndex      int64
	Source           AgentResultSource
	ClientMessageID  string
	TurnID           string
	FinalMessageID   string
	RawMessageText   string
	RawMessageSHA256 string
	ObservedAt       time.Time
}

// AgentStepResultParse is appended after parsing; it never rewrites the result.
type AgentStepResultParse struct {
	ResultID     string
	Conclusion   AgentResultParseConclusion
	ErrorSummary string
	ParsedAt     time.Time
}

// AgentStepResultView is the public evidence for one raw Agent reply.
type AgentStepResultView struct {
	ResultIndex       int64                      `json:"resultIndex"`
	Source            AgentResultSource          `json:"source"`
	ClientMessageID   string                     `json:"clientMessageId"`
	TurnID            string                     `json:"turnId,omitempty"`
	FinalMessageID    string                     `json:"finalMessageId,omitempty"`
	RawMessageText    string                     `json:"rawMessageText,omitempty"`
	RawMessageSHA256  string                     `json:"rawMessageSha256,omitempty"`
	ParseConclusion   AgentResultParseConclusion `json:"parseConclusion"`
	ParseErrorSummary string                     `json:"parseErrorSummary,omitempty"`
	ObservedAt        *time.Time                 `json:"observedAt,omitempty"`
}

// AgentStepAttemptView is the read model returned with a requirement. Legacy
// compatibility rows explicitly report missing evidence instead of inventing it.
type AgentStepAttemptView struct {
	ID                    string                      `json:"attemptId,omitempty"`
	LogicalStepID         string                      `json:"logicalStepId"`
	StepCategory          AgentStepCategory           `json:"stepCategory"`
	StepKind              AgentStepKind               `json:"stepKind"`
	AttemptNumber         int64                       `json:"attemptNumber"`
	RoleBindingID         string                      `json:"roleBindingId,omitempty"`
	AOSessionID           string                      `json:"aoSessionId,omitempty"`
	ClientMessageID       string                      `json:"clientMessageId,omitempty"`
	LastClientMessageID   string                      `json:"lastClientMessageId,omitempty"`
	PromptSHA256          string                      `json:"promptSha256,omitempty"`
	TriggerFailureEventID string                      `json:"triggerFailureEventId,omitempty"`
	LastEventID           string                      `json:"lastEventId,omitempty"`
	SendStatus            AgentAttemptSendStatus      `json:"sendStatus"`
	TurnID                string                      `json:"turnId,omitempty"`
	TurnState             domain.TurnState            `json:"turnState,omitempty"`
	FailureCategory       domain.AgentFailureCategory `json:"failureCategory,omitempty"`
	Retryable             bool                        `json:"retryable"`
	RetryAt               *time.Time                  `json:"retryAt,omitempty"`
	ProviderErrorCode     string                      `json:"providerErrorCode,omitempty"`
	ErrorSummary          string                      `json:"errorSummary,omitempty"`
	RawMessageSHA256      string                      `json:"rawMessageSha256,omitempty"`
	ParseConclusion       AgentResultParseConclusion  `json:"parseConclusion"`
	LegacyEvidenceMissing bool                        `json:"legacyEvidenceMissing,omitempty"`
	RequestedAt           *time.Time                  `json:"requestedAt,omitempty"`
	CreatedAt             *time.Time                  `json:"createdAt"`
	RequestedAtSemantics  AttemptTimeSemantics        `json:"requestedAtSemantics"`
	LastObservedAt        *time.Time                  `json:"lastObservedAt,omitempty"`
	Results               []AgentStepResultView       `json:"results"`
}
