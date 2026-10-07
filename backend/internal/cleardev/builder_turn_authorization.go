package cleardev

import (
	"encoding/json"
)

// HumanDecisionKindExtraBuilderTurn authorizes exactly one more builder turn
// on one task's builder budget: the controlled recovery of a round that
// stopped because its rework budget ran out. It never touches other budgets,
// tasks or recorded history.
const HumanDecisionKindExtraBuilderTurn = "AUTHORIZE_EXTRA_BUILDER_TURN"

// ExtraBuilderTurnBinding names the exact budget a human may extend. It is
// the same narrowly bound offer shape as the reviewer budget authorization.
type ExtraBuilderTurnBinding = ExtraReviewBudgetBinding

// ExtraBuilderTurnSpec registers the narrowly bound private desktop decision.
func ExtraBuilderTurnSpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindExtraBuilderTurn, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParseExtraBuilderTurnBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		},
	}
}

// ParseExtraBuilderTurnBinding accepts only the exact identifiers the
// requesting service observed; anything extra is a protocol failure.
func ParseExtraBuilderTurnBinding(raw []byte) (ExtraBuilderTurnBinding, error) {
	return ParseExtraReviewBudgetBinding(raw)
}
