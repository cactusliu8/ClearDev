package cleardev

import (
	"encoding/json"
	"errors"
	"strings"
)

// HumanDecisionKindExtraReviewBudget authorizes exactly one more reviewer
// turn on one task's reviewer budget: the review of a rework round after a
// requirement final review REWORK. It never touches other budgets or tasks.
const HumanDecisionKindExtraReviewBudget = "AUTHORIZE_EXTRA_REVIEW_BUDGET"

// ExtraReviewBudgetBinding names the exact budget a human may extend.
type ExtraReviewBudgetBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	ExecutionRunID           string `json:"executionRunId"`
	TaskID                   string `json:"taskId"`
	BudgetID                 string `json:"budgetId"`
	Round                    int    `json:"round"`
}

// ExtraReviewBudgetSpec registers the narrowly bound private desktop decision.
func ExtraReviewBudgetSpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindExtraReviewBudget, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParseExtraReviewBudgetBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		},
	}
}

// ParseExtraReviewBudgetBinding accepts only the exact identifiers the
// requesting service observed; anything extra is a protocol failure.
func ParseExtraReviewBudgetBinding(raw []byte) (ExtraReviewBudgetBinding, error) {
	var b ExtraReviewBudgetBinding
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	if _, err := requireJSONObjectFields(raw, "developmentRequirementId", "executionRunId", "taskId", "budgetId", "round"); err != nil {
		return b, err
	}
	for _, id := range []string{b.DevelopmentRequirementID, b.ExecutionRunID, b.TaskID, b.BudgetID} {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 200 {
			return b, errors.New("invalid review budget identity")
		}
	}
	if b.Round < 0 {
		return b, errors.New("invalid review budget round")
	}
	return b, nil
}
