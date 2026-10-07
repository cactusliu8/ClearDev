package cleardev

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// HumanDecisionKindControlledEngineChange authorizes one deliberate mid-project
// engine change for a controlled project: every controlled worker session may
// switch its underlying provider agent to the named harness and model, and the
// project's frozen cleardev execution choice follows the same target. History,
// tasks, budgets, candidates and checks stay untouched. Without this decision a
// controlled project's engine choice stays frozen from controlled-work start.
const HumanDecisionKindControlledEngineChange = "AUTHORIZE_CONTROLLED_ENGINE_CHANGE"

// ControlledEngineChangeBinding names the exact project and target engine a
// human may authorize.
type ControlledEngineChangeBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	AOProjectID              string `json:"aoProjectId"`
	TargetHarness        string `json:"targetHarness"`
	Model                string `json:"model"`
	Effort               string `json:"effort,omitempty"`
}

// ControlledEngineChangeSpec registers the narrowly bound private desktop decision.
func ControlledEngineChangeSpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindControlledEngineChange, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParseControlledEngineChangeBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		},
	}
}

// ParseControlledEngineChangeBinding accepts only the exact identifiers the
// requesting service observed; anything extra is a protocol failure.
func ParseControlledEngineChangeBinding(raw []byte) (ControlledEngineChangeBinding, error) {
	var b ControlledEngineChangeBinding
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	if _, err := requireJSONObjectFieldsOptional(raw,
		[]string{"developmentRequirementId", "aoProjectId", "targetHarness", "model"},
		[]string{"effort"}); err != nil {
		return b, err
	}
	for _, id := range []string{b.DevelopmentRequirementID, b.AOProjectID} {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 200 {
			return b, errors.New("invalid engine change project identity")
		}
	}
	if b.TargetHarness != string(domain.HarnessCodex) && b.TargetHarness != string(domain.HarnessOpenCode) {
		return b, errors.New("invalid engine change target harness")
	}
	if b.Model == "" || strings.TrimSpace(b.Model) != b.Model || len(b.Model) > 240 ||
		strings.ContainsAny(b.Model, " \t\r\n\x00") {
		return b, errors.New("invalid engine change model")
	}
	if b.TargetHarness == string(domain.HarnessOpenCode) && (b.Effort != "" || !strings.Contains(b.Model, "/")) {
		return b, errors.New("opencode engine change needs provider/model and its configured default effort")
	}
	if b.Effort != "" && b.TargetHarness != string(domain.HarnessCodex) {
		return b, errors.New("engine change effort belongs to codex")
	}
	return b, nil
}
