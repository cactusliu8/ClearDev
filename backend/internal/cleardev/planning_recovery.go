package cleardev

import (
	"encoding/json"
	"errors"
	"strings"
)

// HumanDecisionKindPlanningRecovery authorizes one interrupted planning review retry.
const HumanDecisionKindPlanningRecovery = "AUTHORIZE_PLANNING_REVIEW_RECOVERY"

// PlanningRecoveryBinding pins the approved specification, plan and failed attempt.
type PlanningRecoveryBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	RequirementVersionID     string `json:"requirementVersionId"`
	RequirementVersionSHA256 string `json:"requirementVersionSha256"`
	PlanID                   string `json:"planId"`
	PlanSHA256               string `json:"planSha256"`
	LogicalStepID            string `json:"logicalStepId"`
	FirstAttemptID           string `json:"firstAttemptId"`
	FailureEventID           string `json:"failureEventId"`
	RoleBindingID            string `json:"roleBindingId"`
	AOSessionID              string `json:"aoSessionId"`
	PromptSHA256             string `json:"promptSha256"`
}

// ParsePlanningRecoveryBinding rejects unknown, absent or invalid binding fields.
func ParsePlanningRecoveryBinding(raw []byte) (PlanningRecoveryBinding, error) {
	var b PlanningRecoveryBinding
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	if _, err := requireJSONObjectFields(raw, "developmentRequirementId", "requirementVersionId", "requirementVersionSha256", "planId", "planSha256", "logicalStepId", "firstAttemptId", "failureEventId", "roleBindingId", "aoSessionId", "promptSha256"); err != nil {
		return b, err
	}
	for _, id := range []string{b.DevelopmentRequirementID, b.RequirementVersionID, b.PlanID, b.LogicalStepID, b.FirstAttemptID, b.FailureEventID, b.RoleBindingID, b.AOSessionID} {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 300 {
			return b, errors.New("invalid planning recovery identity")
		}
	}
	for _, digest := range []string{b.RequirementVersionSHA256, b.PlanSHA256, b.PromptSHA256} {
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			return b, errors.New("invalid planning recovery digest")
		}
	}
	return b, nil
}

// PlanningRecoverySpec uses the existing private desktop authority envelope.
func PlanningRecoverySpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindPlanningRecovery, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParsePlanningRecoveryBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		},
	}
}
