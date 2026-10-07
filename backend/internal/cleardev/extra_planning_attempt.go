package cleardev

import (
	"encoding/json"
	"errors"
	"strings"
)

// HumanDecisionKindExtraPlanningAttempt grants one original message after the
// two measured attempts of a current product discussion or compilation fail.
const HumanDecisionKindExtraPlanningAttempt = "AUTHORIZE_EXTRA_PLANNING_ATTEMPT"

// Explicit planning actions separate requesting authority from consuming it.
const (
	RecoveryRequestExtraPlanningAttempt                     = "REQUEST_EXTRA_PLANNING_ATTEMPT"
	RecoveryContinueExtraPlanningAttempt                    = "CONTINUE_EXTRA_PLANNING_ATTEMPT"
	AgentMessageHumanAuthorizedOriginal  AgentMessageSource = "HUMAN_AUTHORIZED_ORIGINAL"
)

// ExtraPlanningAttemptBinding retains the original source and measured failure.
// These fields are produced by the backend, never selected by ordinary clients.
type ExtraPlanningAttemptBinding struct {
	DevelopmentRequirementID string                      `json:"developmentRequirementId"`
	Source                   PlanningStepRecoveryBinding `json:"source"`
	SecondAttemptID          string                      `json:"secondAttemptId"`
	FailureEventID           string                      `json:"failureEventId"`
	MessagesSHA256           string                      `json:"messagesSha256"`
}

// ExtraPlanningAttemptSpec registers a narrowly bound private desktop choice.
func ExtraPlanningAttemptSpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindExtraPlanningAttempt, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParseExtraPlanningAttemptBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		},
	}
}

// ParseExtraPlanningAttemptBinding rejects missing, unknown and changed fields.
func ParseExtraPlanningAttemptBinding(raw []byte) (ExtraPlanningAttemptBinding, error) {
	var b ExtraPlanningAttemptBinding
	if _, err := requireJSONObjectFields(raw, "developmentRequirementId", "source", "secondAttemptId", "failureEventId", "messagesSha256"); err != nil {
		return b, err
	}
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	var envelope struct {
		Source json.RawMessage `json:"source"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return b, err
	}
	if _, err := requireJSONObjectFields(envelope.Source, "requirementId", "productId", "discussionId", "stageId", "stageDefinitionSha256", "selectionSha256", "targetVersionId", "logicalStepId", "stepRequestId", "firstAttemptId", "failureEventId", "roleBindingId", "aoSessionId", "providerConversationId", "workspacePath", "sessionCreationKey", "harness", "model", "promptSha256", "clientMessageId"); err != nil {
		return b, err
	}
	for _, v := range []string{b.DevelopmentRequirementID, b.Source.ProductID, b.Source.DiscussionID, b.Source.LogicalStepID, b.Source.StepRequestID, b.Source.FirstAttemptID, b.Source.FailureEventID, b.Source.RoleBindingID, b.Source.AOSessionID, b.Source.ProviderConversationID, b.Source.WorkspacePath, b.Source.SessionCreationKey, b.Source.Harness, b.Source.ClientMessageID, b.SecondAttemptID, b.FailureEventID} {
		if strings.TrimSpace(v) == "" || strings.TrimSpace(v) != v {
			return b, errors.New("extra planning attempt binding is incomplete")
		}
	}
	if strings.TrimSpace(b.Source.Model) != b.Source.Model {
		return b, errors.New("invalid extra planning model binding")
	}
	if b.DevelopmentRequirementID != b.Source.RequirementID || (len(b.MessagesSHA256) != 64 || strings.Trim(b.MessagesSHA256, "0123456789abcdef") != "") || (len(b.Source.PromptSHA256) != 64 || strings.Trim(b.Source.PromptSHA256, "0123456789abcdef") != "") {
		return b, errors.New("extra planning attempt binding is invalid")
	}
	return b, nil
}

// ExtraPlanningAttemptTarget makes a public action stale when its proof changes.
func ExtraPlanningAttemptTarget(b ExtraPlanningAttemptBinding) (string, error) {
	raw, err := CanonicalJSONBytes(b)
	if err != nil {
		return "", err
	}
	return b.Source.LogicalStepID + ":extra:" + sha256Hex(raw), nil
}

// ExtraPlanningAttemptState is a read-only internal projection of exact facts.
type ExtraPlanningAttemptState struct {
	Binding           ExtraPlanningAttemptBinding
	Option            WorkflowRecoveryOption
	Selection         *ProductSelection
	Messages          []PlanningStepRecoveryMessage
	History           []WorkflowRecovery
	DecisionRequestID string
}
