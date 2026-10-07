package cleardev

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// FixedRecoveryRequest binds one fixed recovery to an immutable original target.
type FixedRecoveryRequest struct {
	ID                     string    `json:"id"`
	RequirementID          string    `json:"requirementId"`
	ExecutionRunID         string    `json:"executionRunId"`
	TaskID                 string    `json:"taskId,omitempty"`
	DispatchID             string    `json:"dispatchId,omitempty"`
	LogicalStepID          string    `json:"logicalStepId,omitempty"`
	FirstAttemptID         string    `json:"firstAttemptId,omitempty"`
	FailureEventID         string    `json:"failureEventId"`
	RoleBindingID          string    `json:"roleBindingId,omitempty"`
	SessionID              string    `json:"sessionId,omitempty"`
	ProviderConversationID string    `json:"providerConversationId,omitempty"`
	ControllerGeneration   string    `json:"controllerGeneration,omitempty"`
	ProjectID              string    `json:"projectId"`
	WorkspacePath          string    `json:"workspacePath,omitempty"`
	Branch                 string    `json:"branch,omitempty"`
	ExpectedSHA            string    `json:"expectedSha"`
	Model                  string    `json:"model,omitempty"`
	ReviewID               string    `json:"reviewId,omitempty"`
	CandidateID            string    `json:"candidateId,omitempty"`
	CandidateSHA           string    `json:"candidateSha,omitempty"`
	ReviewPacketJSON       string    `json:"reviewPacketJson,omitempty"`
	ReviewPacketSHA256     string    `json:"reviewPacketSha256,omitempty"`
	PromptSHA256           string    `json:"promptSha256,omitempty"`
	CheckRunID             string    `json:"checkRunId,omitempty"`
	RetryCheckRunID        string    `json:"retryCheckRunId,omitempty"`
	CreatedAt              time.Time `json:"createdAt"`
}

// FixedRecoveryClaim is a durable external-action boundary, not evidence of success.
type FixedRecoveryClaim struct {
	RequestID   string    `json:"requestId"`
	ProposalID  string    `json:"proposalId"`
	Action      string    `json:"action"`
	OperationID string    `json:"operationId"`
	ClaimedAt   time.Time `json:"claimedAt"`
}

// FixedRecoveryResult records only the confirmed outcome of an actual operation.
type FixedRecoveryResult struct {
	RequestID              string    `json:"requestId"`
	Outcome                string    `json:"outcome"`
	ReasonCode             string    `json:"reasonCode,omitempty"`
	SessionID              string    `json:"sessionId,omitempty"`
	ProviderConversationID string    `json:"providerConversationId,omitempty"`
	ControllerGeneration   string    `json:"controllerGeneration,omitempty"`
	RoleBindingID          string    `json:"roleBindingId,omitempty"`
	WorkspacePath          string    `json:"workspacePath,omitempty"`
	CheckRunID             string    `json:"checkRunId,omitempty"`
	RecordedAt             time.Time `json:"recordedAt"`
}

// ReplacementReviewResult preserves the original review and binds its substitute result.
type ReplacementReviewResult struct {
	RecoveryRequestID string             `json:"recoveryRequestId"`
	OriginalReviewID  string             `json:"originalReviewId"`
	AttemptID         string             `json:"attemptId"`
	ResultID          string             `json:"resultId"`
	Verdict           LocalReviewVerdict `json:"verdict"`
	ReasonCode        ReasonCode         `json:"reasonCode,omitempty"`
	Summary           string             `json:"summary"`
	RecordedAt        time.Time          `json:"recordedAt"`
}

// FixedRecoveryEvidence keeps proposals, claims, and outcomes distinct in reads.
type FixedRecoveryEvidence struct {
	// ReviewStep is a read-only projection of the validated replacement raw
	// result and its actual attempt. It never overwrites the original step.
	ReviewStep   *AgentStep               `json:"-"`
	Refusal      *FixedRecoveryRefusal    `json:"refusal" nullable:"true"`
	Request      FixedRecoveryRequest     `json:"request"`
	Claim        *FixedRecoveryClaim      `json:"claim" nullable:"true"`
	Result       *FixedRecoveryResult     `json:"result" nullable:"true"`
	ReviewResult *ReplacementReviewResult `json:"reviewResult" nullable:"true"`
}

// FixedRecoveryFailureAllowed excludes semantic, configuration and unknown-delivery failures even if a retry flag is set.
func FixedRecoveryFailureAllowed(category domain.AgentFailureCategory) bool {
	switch category {
	case domain.AgentFailureProviderUnavailable, domain.AgentFailureSessionLost, domain.AgentFailureTurnInterrupted, domain.AgentFailureRateLimited:
		return true
	default:
		return false
	}
}

// FixedRecoveryStepAllowed keeps Recovery itself and other workflows out of fixed session recovery.
func FixedRecoveryStepAllowed(category AgentStepCategory, kind AgentStepKind) bool {
	return (category == AgentStepCategoryComplexExecution && (kind == ComplexExecutionAgentStepBuilderTask || kind == AgentStepLocalReview)) || (category == AgentStepCategoryException && kind == ComplexExceptionAgentStepBuilderContinue)
}

// FixedRecoveryRefusal records a refusal before an external action was claimed or invoked.
type FixedRecoveryRefusal struct {
	RequestID  string    `json:"requestId"`
	ReasonCode string    `json:"reasonCode"`
	RecordedAt time.Time `json:"recordedAt"`
}
