package cleardev

import (
	"encoding/json"
	"errors"
	"strings"
)

// HumanDecisionKindProductPlan authorizes one immutable whole product plan.
const HumanDecisionKindProductPlan = "CONFIRM_PRODUCT_PLAN"

// ProductPlanBinding binds every stage through the immutable discussion digest.
type ProductPlanBinding struct {
	ProductID       string `json:"productId"`
	DiscussionID    string `json:"discussionId"`
	ResultSHA256    string `json:"resultSha256"`
	SelectionSHA256 string `json:"selectionSha256"`
}

// ProductPlanAuthorization projects the original decision and latest progression event.
type ProductPlanAuthorization struct {
	RequestID  string             `json:"requestId"`
	Binding    ProductPlanBinding `json:"binding"`
	Status     string             `json:"status"`
	Decision   string             `json:"decision,omitempty"`
	ReasonCode string             `json:"reasonCode,omitempty"`
	Reason     string             `json:"reason,omitempty"`
}

// ParseProductPlanBinding accepts only exact identifiers and SHA-256 digests.
func ParseProductPlanBinding(raw []byte) (ProductPlanBinding, error) {
	var b ProductPlanBinding
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	if strings.TrimSpace(b.ProductID) == "" || strings.TrimSpace(b.DiscussionID) == "" || !lowerHexDigest(b.ResultSHA256) || !lowerHexDigest(b.SelectionSHA256) {
		return b, errors.New("invalid product plan binding")
	}
	return b, nil
}
func lowerHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ProductPlanDecisionSpec registers the private desktop decision contract.
func ProductPlanDecisionSpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindProductPlan, BindingSchemaVersion: 1,
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, e := ParseProductPlanBinding(raw)
			if e != nil {
				return nil, e
			}
			return json.Marshal(b)
		},
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true}}
}

// ProductPlanCoversDocument preserves the approved acceptance set exactly. A
// compiler that asks questions or changes the set must return to the user.
func ProductPlanCoversDocument(stage ProductStageDefinition, doc NormalizedRequirementDocument) bool {
	if stage.ExecutionBasis == nil || len(stage.AcceptanceCriteria) == 0 || len(doc.Conflicts) != 0 || len(doc.AcceptanceScenarios) != len(stage.AcceptanceCriteria) || len(doc.NonGoals) != len(stage.NonGoals) {
		return false
	}
	wanted := map[string]int{}
	for _, s := range stage.AcceptanceCriteria {
		wanted[s]++
	}
	ids := map[string]bool{}
	for _, a := range doc.AcceptanceScenarios {
		if wanted[a.Text] == 0 {
			return false
		}
		wanted[a.Text]--
		if a.ID == "" || ids[a.ID] {
			return false
		}
		ids[a.ID] = true
	}
	goals := map[string]int{}
	for _, s := range stage.NonGoals {
		goals[s]++
	}
	for _, s := range doc.NonGoals {
		if goals[s] == 0 {
			return false
		}
		goals[s]--
	}
	covered := map[string]bool{}
	for _, r := range doc.Requirements {
		if r.Priority != "MUST" || len(r.AcceptanceIDs) == 0 {
			return false
		}
		textAllowed := r.Text == stage.Goal
		for _, text := range stage.AcceptanceCriteria {
			textAllowed = textAllowed || r.Text == text
		}
		if !textAllowed {
			return false
		}
		for _, id := range r.AcceptanceIDs {
			if !ids[id] {
				return false
			}
			covered[id] = true
		}
	}
	for id := range ids {
		if !covered[id] {
			return false
		}
	}
	basis := ProjectStageBasisConstraint(stage)
	for _, c := range doc.Constraints {
		if ProductBasisConstraintPreserved(c, basis) {
			return true
		}
	}
	return false
}
