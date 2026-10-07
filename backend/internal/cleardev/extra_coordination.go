package cleardev

import (
	"encoding/json"
	"errors"
	"strings"
)

// HumanDecisionKindExtraCoordination and its companion constants identify the
// sole native-human extension without changing the original two-round policy.
const (
	HumanDecisionKindExtraCoordination = "AUTHORIZE_EXTRA_PLANNER_COORDINATION"
	RecoveryRequestExtraCoordination   = "REQUEST_EXTRA_PLANNER_COORDINATION"
	ExtraCoordinationOrdinal           = 3
)

// ExtraCoordinationBinding authorizes one exact third business request, not a
// new attempt of an old request and not another task, revision or role budget.
// Every field is derived by the backend from current durable facts.
type ExtraCoordinationBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	ExecutionRunID           string `json:"executionRunId"`
	EventID                  string `json:"eventId"`
	StopSHA256               string `json:"stopSha256"`
	ContextSHA256            string `json:"contextSha256"`
	PlannerRoleBindingID     string `json:"plannerRoleBindingId"`
	AOSessionID              string `json:"aoSessionId"`
	ProviderConversationID   string `json:"providerConversationId"`
	WorkspacePath            string `json:"workspacePath"`
	SessionCreationKey       string `json:"sessionCreationKey"`
	CreationFingerprint      string `json:"creationFingerprint"`
	Harness                  string `json:"harness"`
	Model                    string `json:"model"`
	Ordinal                  int    `json:"ordinal"`
}

// ExtraCoordinationSpec registers the exact native desktop decision contract.
func ExtraCoordinationSpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindExtraCoordination, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParseExtraCoordinationBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		},
	}
}

// ParseExtraCoordinationBinding rejects loose or over-broad grant identities.
func ParseExtraCoordinationBinding(raw []byte) (ExtraCoordinationBinding, error) {
	var b ExtraCoordinationBinding
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	if _, err := requireJSONObjectFields(raw, "developmentRequirementId", "executionRunId", "eventId", "stopSha256", "contextSha256", "plannerRoleBindingId", "aoSessionId", "providerConversationId", "workspacePath", "sessionCreationKey", "creationFingerprint", "harness", "model", "ordinal"); err != nil {
		return b, err
	}
	for _, s := range []string{b.DevelopmentRequirementID, b.ExecutionRunID, b.EventID, b.PlannerRoleBindingID, b.AOSessionID, b.ProviderConversationID, b.SessionCreationKey, b.CreationFingerprint, b.Harness} {
		if s == "" || strings.TrimSpace(s) != s || len(s) > 512 {
			return b, errors.New("invalid extra coordination identity")
		}
	}
	if b.WorkspacePath == "" || len(b.WorkspacePath) > 4096 || strings.TrimSpace(b.WorkspacePath) != b.WorkspacePath || len(b.Model) > 512 ||
		!validProtocolSHA256(b.StopSHA256) || !validProtocolSHA256(b.ContextSHA256) || b.Ordinal != ExtraCoordinationOrdinal {
		return b, errors.New("invalid bounded extra coordination grant")
	}
	return b, nil
}

// ExtraCoordinationTarget pins a recovery intent to the backend-derived source.
func ExtraCoordinationTarget(b ExtraCoordinationBinding) (string, error) {
	raw, err := CanonicalJSONBytes(b)
	if err != nil {
		return "", err
	}
	return b.EventID + ":extra:" + sha256Hex(raw), nil
}

// ExtraCoordinationGrant is public historical evidence, not a client-supplied
// capability. The original LIMIT_REACHED remains in Decisions.
type ExtraCoordinationGrant struct {
	EventID           string `json:"eventId"`
	DecisionRequestID string `json:"decisionRequestId"`
	Ordinal           int    `json:"ordinal"`
}

// ExtraCoordinationState is internal admission state; only Option and History
// are returned by the existing workflow recovery endpoint.
type ExtraCoordinationState struct {
	Binding ExtraCoordinationBinding
	Option  WorkflowRecoveryOption
	History []WorkflowRecovery
}
