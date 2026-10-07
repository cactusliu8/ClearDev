package cleardev

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Builder replacement actions require an exact private grant before continuation.
const (
	HumanDecisionKindBuilderReplacement                        = "AUTHORIZE_BUILDER_REPLACEMENT"
	RecoveryRequestBuilderReplacement                          = "REQUEST_BUILDER_REPLACEMENT"
	RecoveryContinueBuilderReplacement                         = "CONTINUE_BUILDER_REPLACEMENT"
	AgentAttemptRetiredBeforeSend       AgentAttemptSendStatus = "RETIRED_BEFORE_SEND"
)

// BuilderReplacementBinding is an exact daemon-owned handoff, never a client
// selected session, workspace, prompt, configuration or budget.
type BuilderReplacementBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	ExecutionRunID           string `json:"executionRunId"`
	RequirementVersionID     string `json:"requirementVersionId"`
	RequirementSHA256        string `json:"requirementSha256"`
	TaskID                   string `json:"taskId"`
	DispatchID               string `json:"dispatchId"`
	LogicalStepID            string `json:"logicalStepId"`
	Round                    int64  `json:"round"`
	TargetID                 string `json:"targetId"`
	SourceSHA256             string `json:"sourceSha256"`
	OldRoleBindingID         string `json:"oldRoleBindingId"`
	OldAOSessionID           string `json:"oldAOSessionId"`
	OldNativeSessionID       string `json:"oldNativeSessionId"`
	OldWorkspacePath         string `json:"oldWorkspacePath"`
	OldBranch                string `json:"oldBranch"`
	OldHeadSHA               string `json:"oldHeadSha"`
	BaseCommitSHA            string `json:"baseCommitSha"`
	SessionIdentitySHA256    string `json:"sessionIdentitySha256"`
	LossEvidenceSHA256       string `json:"lossEvidenceSha256"`
	PromptSHA256             string `json:"promptSha256"`
	ClientMessageID          string `json:"clientMessageId"`
	BudgetID                 string `json:"budgetId"`
	OccupancyID              string `json:"occupancyId"`
	BudgetSHA256             string `json:"budgetSha256"`
	SnapshotJSON             string `json:"snapshotJson"`
	SnapshotSHA256           string `json:"snapshotSha256"`
	SnapshotPath             string `json:"snapshotPath"`
	HandoffPath              string `json:"handoffPath"`
	HandoffSHA256            string `json:"handoffSha256"`
	NewRoleBindingID         string `json:"newRoleBindingId"`
	SessionCreationKey       string `json:"sessionCreationKey"`
	AgentConfigJSON          string `json:"agentConfigJson"`
	AgentConfigSHA256        string `json:"agentConfigSha256"`
	FileCount                int64  `json:"fileCount"`
	TotalBytes               int64  `json:"totalBytes"`
}

// BuilderReplacementSpec defines the private desktop decision contract.
func BuilderReplacementSpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindBuilderReplacement, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParseBuilderReplacementBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		},
	}
}

// ParseBuilderReplacementBinding rejects incomplete or client-extended bindings.
func ParseBuilderReplacementBinding(raw []byte) (BuilderReplacementBinding, error) {
	var b BuilderReplacementBinding
	fields := []string{"developmentRequirementId", "executionRunId", "requirementVersionId", "requirementSha256", "taskId", "dispatchId", "logicalStepId", "round", "targetId", "sourceSha256", "oldRoleBindingId", "oldAOSessionId", "oldNativeSessionId", "oldWorkspacePath", "oldBranch", "oldHeadSha", "baseCommitSha", "sessionIdentitySha256", "lossEvidenceSha256", "promptSha256", "clientMessageId", "budgetId", "occupancyId", "budgetSha256", "snapshotJson", "snapshotSha256", "snapshotPath", "handoffPath", "handoffSha256", "newRoleBindingId", "sessionCreationKey", "agentConfigJson", "agentConfigSha256", "fileCount", "totalBytes"}
	if _, err := requireJSONObjectFields(raw, fields...); err != nil {
		return b, err
	}
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	for _, v := range []string{b.DevelopmentRequirementID, b.ExecutionRunID, b.RequirementVersionID, b.TaskID, b.DispatchID, b.LogicalStepID, b.TargetID, b.OldRoleBindingID, b.OldAOSessionID, b.OldWorkspacePath, b.OldBranch, b.ClientMessageID, b.BudgetID, b.SnapshotJSON, b.SnapshotPath, b.HandoffPath, b.NewRoleBindingID, b.SessionCreationKey, b.AgentConfigJSON} {
		if v == "" || strings.TrimSpace(v) != v {
			return b, errors.New("incomplete Builder replacement binding")
		}
	}
	for _, v := range []string{b.RequirementSHA256, b.SourceSHA256, b.SessionIdentitySHA256, b.LossEvidenceSHA256, b.PromptSHA256, b.BudgetSHA256, b.SnapshotSHA256, b.HandoffSHA256, b.AgentConfigSHA256} {
		if len(v) != 64 || strings.Trim(v, "0123456789abcdef") != "" {
			return b, errors.New("invalid Builder replacement digest")
		}
	}
	if b.Round < 0 || b.FileCount < 0 || b.FileCount > 2048 || b.TotalBytes < 0 || b.TotalBytes > 82<<20 || len(b.OldHeadSHA) != 40 || len(b.BaseCommitSHA) != 40 || !json.Valid([]byte(b.SnapshotJSON)) || !json.Valid([]byte(b.AgentConfigJSON)) {
		return b, errors.New("invalid Builder replacement source")
	}
	if strings.Trim(b.OldHeadSHA, "0123456789abcdef") != "" || strings.Trim(b.BaseCommitSHA, "0123456789abcdef") != "" || b.OldRoleBindingID == b.NewRoleBindingID {
		return b, errors.New("invalid Builder replacement identity")
	}
	return b, nil
}

// BuilderReplacementHandoff binds one successor to its original step and grant.
type BuilderReplacementHandoff struct {
	ID                string
	RequestID         string
	DecisionRequestID string
	Binding           BuilderReplacementBinding
	RetirementEventID string
	AttemptID         string
	NewAOSessionID    string
	NewWorkspacePath  string
	LaunchSHA256      string
	CreatedAt         time.Time
}

// BuilderReplacementObservation records one external stage outcome without retrying it.
type BuilderReplacementObservation struct {
	ID             string
	HandoffID      string
	Stage          string
	OperationKey   string
	Outcome        string
	ReasonCode     string
	AOSessionID    string
	WorkspacePath  string
	LaunchSHA256   string
	SnapshotSHA256 string
	ObservedAt     time.Time
}

// BuilderReplacementState contains storage source facts; external observation
// belongs to the service at explicit write boundaries.
type BuilderReplacementState struct {
	Execution         ComplexExecutionSnapshot
	Task              ComplexExecutionTask
	Dispatch          ComplexExecutionDispatch
	Step              AgentStep
	OldBinding        ComplexExecutionRoleBinding
	Binding           BuilderReplacementBinding
	Option            WorkflowRecoveryOption
	RequestID         string
	DecisionRequestID string
	Handoff           *BuilderReplacementHandoff
	Observations      []BuilderReplacementObservation
	History           []WorkflowRecovery
	Budget            ComplexExceptionBudget
	OccupancyID       string
	SourceSHA256      string
	OldAttempt        *AgentStepAttempt
}

// BuilderReplacementView exposes preserved identities and measured handoff
// stages, without private bindings, native capability or snapshot contents.
type BuilderReplacementView struct {
	ID                string `json:"id"`
	TargetID          string `json:"targetId"`
	ExecutionRunID    string `json:"executionRunId"`
	TaskID            string `json:"taskId"`
	DispatchID        string `json:"dispatchId"`
	StepID            string `json:"stepId"`
	DecisionRequestID string `json:"decisionRequestId"`
	ContinueRequestID string `json:"continueRequestId"`
	OldRoleBindingID  string `json:"oldRoleBindingId"`
	OldAOSessionID    string `json:"oldAOSessionId"`
	OldWorkspacePath  string `json:"oldWorkspacePath"`
	OldHeadSHA        string `json:"oldHeadSha"`
	SnapshotSHA256    string `json:"snapshotSha256"`
	SnapshotPath      string `json:"snapshotPath"`
	FileCount         int64  `json:"fileCount"`
	TotalBytes        int64  `json:"totalBytes"`
	NewRoleBindingID  string `json:"newRoleBindingId"`
	NewAOSessionID    string `json:"newAOSessionId"`
	NewWorkspacePath  string `json:"newWorkspacePath"`
	State             string `json:"state"`
	LastStage         string `json:"lastStage"`
	LastOutcome       string `json:"lastOutcome"`
	ReasonCode        string `json:"reasonCode"`
	BudgetSummary     string `json:"budgetSummary"`
}

// ValidBuilderReplacementAttempt grants precisely the unsent retired step's
// second attempt; it does not relax retryable provider-failure rules.
func ValidBuilderReplacementAttempt(first AgentStepAttempt, retired AgentAttemptEvent, second AgentStepAttempt, h BuilderReplacementHandoff) bool {
	b := h.Binding
	return ValidAgentAttemptCreation(second) && first.AttemptNumber == 1 && second.AttemptNumber == 2 &&
		h.RequestID != "" && h.DecisionRequestID != "" && first.DevelopmentRequirementID == b.DevelopmentRequirementID && first.LogicalStepID == b.LogicalStepID && first.ClientMessageID == b.ClientMessageID &&
		first.StepCategory == AgentStepCategoryComplexExecution && first.StepKind == ComplexExecutionAgentStepBuilderTask && retired.ClientMessageID == first.ClientMessageID && retired.PromptSHA256 == first.PromptSHA256 &&
		first.ID == b.LogicalStepID+":attempt:1" && retired.AttemptID == first.ID && retired.ID == h.RetirementEventID && retired.Status == AgentAttemptRetiredBeforeSend &&
		retired.TurnID == "" && retired.TurnState == "" && retired.FailureCategory == "" && !retired.Retryable && retired.RetryAt == nil && retired.ProviderErrorCode == "" &&
		first.RoleBindingID == b.OldRoleBindingID && first.AOSessionID == b.OldAOSessionID && first.PromptSHA256 == b.PromptSHA256 &&
		second.ID == h.AttemptID && second.LogicalStepID == first.LogicalStepID && second.DevelopmentRequirementID == first.DevelopmentRequirementID &&
		second.StepCategory == first.StepCategory && second.StepKind == first.StepKind && second.RoleBindingID == b.NewRoleBindingID && second.RoleBindingID != first.RoleBindingID && second.AOSessionID == h.NewAOSessionID && h.NewAOSessionID != "" && h.NewAOSessionID != first.AOSessionID &&
		second.ClientMessageID == first.ClientMessageID+":attempt:2" && second.PromptSHA256 == first.PromptSHA256 && second.TriggerFailureEventID == retired.ID && !second.CreatedAt.Before(retired.RecordedAt)
}

// BuilderReplacementTransition is an internal projection of the grant, handoff
// and retirement facts. It contains no private binding or capability.
type BuilderReplacementTransition struct {
	HandoffID, DecisionRequestID, LogicalStepID, DispatchID                          string
	OldRoleBindingID, NewRoleBindingID, RetirementEventID, AttemptID, NewAOSessionID string
	AliasBound                                                                       bool
}

func builderReplacementResolvesEndedRole(s ComplexExecutionSnapshot, old ComplexExecutionRoleBinding) bool {
	if old.Role != StandardRoleBuilder || old.Status != RoleBindingStatusEnded || old.ReasonCode != "BUILDER_REPLACED" || s.Run.Mode != WorkModeStandard {
		return false
	}
	if _, project, err := ProjectContractFromRun(s.Run); err != nil || !project {
		return false
	}
	for _, proof := range s.BuilderReplacementTransitions {
		if proof.HandoffID == "" || proof.DecisionRequestID == "" || proof.OldRoleBindingID != old.ID || proof.LogicalStepID == "" || proof.NewRoleBindingID != proof.LogicalStepID+":replacement-builder" || proof.RetirementEventID != proof.LogicalStepID+":replacement-retired" || proof.AttemptID != proof.LogicalStepID+":attempt:2" {
			continue
		}
		matchedStep, matchedDispatch := false, false
		for _, step := range s.AgentSteps {
			if step.ID == proof.LogicalStepID && step.RoleBindingID == old.ID && step.Kind == ComplexExecutionAgentStepBuilderTask && step.RequestID == proof.DispatchID {
				matchedStep = true
			}
		}
		for _, dispatch := range s.Dispatches {
			if dispatch.ID == proof.DispatchID && dispatch.AgentStepID == proof.LogicalStepID && dispatch.BuilderRoleBindingID == old.ID {
				matchedDispatch = true
			}
		}
		if !matchedStep || !matchedDispatch {
			continue
		}
		for _, next := range s.RoleBindings {
			if next.ID != proof.NewRoleBindingID || next.Role != StandardRoleBuilder || next.BuilderSlot != old.BuilderSlot || next.ContinuationOfRoleBindingID != old.ID || next.SessionCreationIdempotencyKey != proof.LogicalStepID+":replacement-create" {
				continue
			}
			if next.Status == RoleBindingStatusRequested && !proof.AliasBound && next.AOSessionID == "" {
				return true
			}
			if next.Status == RoleBindingStatusBound && proof.AliasBound && proof.NewAOSessionID != "" && next.AOSessionID == proof.NewAOSessionID && next.AOSessionID != old.AOSessionID {
				return true
			}
		}
	}
	return false
}
