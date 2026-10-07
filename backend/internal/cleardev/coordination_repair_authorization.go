package cleardev

import (
	"encoding/json"
	"errors"
	"strings"
)

// HumanDecisionKindCoordinationRepair authorizes exactly one coordination
// repair round after the control plane refused a Planner engineering revision
// only because the task's repair rounds were exhausted. Approval applies the
// preserved proposal to the unchanged task contract. It never rewrites the
// recorded STOP, other tasks, other budgets or any recorded history.
const HumanDecisionKindCoordinationRepair = "AUTHORIZE_COORDINATION_REPAIR"

// CoordinationRepairBinding names the exact stopped coordination event a human
// may resume. It is the same narrowly bound offer shape as the budget offers.
type CoordinationRepairBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	ExecutionRunID           string `json:"executionRunId"`
	EventID                  string `json:"eventId"`
	TaskID                   string `json:"taskId"`
	BudgetID                 string `json:"budgetId"`
}

// CoordinationRepairSpec registers the narrowly bound private desktop decision.
func CoordinationRepairSpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindCoordinationRepair, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParseCoordinationRepairBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		},
	}
}

// ParseCoordinationRepairBinding accepts only the exact identifiers the
// requesting service observed; anything extra is a protocol failure.
func ParseCoordinationRepairBinding(raw []byte) (CoordinationRepairBinding, error) {
	var b CoordinationRepairBinding
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	if _, err := requireJSONObjectFields(raw, "developmentRequirementId", "executionRunId", "eventId", "taskId", "budgetId"); err != nil {
		return b, err
	}
	for _, id := range []string{b.DevelopmentRequirementID, b.ExecutionRunID, b.EventID, b.TaskID, b.BudgetID} {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 200 {
			return b, errors.New("invalid coordination repair identity")
		}
	}
	return b, nil
}
