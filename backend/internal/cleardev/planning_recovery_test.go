package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanningRecoveryBindingStrictAndRegistered(t *testing.T) {
	b := PlanningRecoveryBinding{DevelopmentRequirementID: "requirement", RequirementVersionID: "version", RequirementVersionSHA256: strings.Repeat("a", 64), PlanID: "plan", PlanSHA256: strings.Repeat("b", 64), LogicalStepID: "step", FirstAttemptID: "attempt", FailureEventID: "failure", RoleBindingID: "role", AOSessionID: "session", PromptSHA256: strings.Repeat("c", 64)}
	raw, _ := json.Marshal(b)
	if parsed, err := ParsePlanningRecoveryBinding(raw); err != nil || parsed != b {
		t.Fatalf("binding roundtrip: %v", err)
	}
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	for key := range fields {
		bad := make(map[string]any)
		for k, v := range fields {
			if k != key {
				bad[k] = v
			}
		}
		data, _ := json.Marshal(bad)
		if _, err := ParsePlanningRecoveryBinding(data); err == nil {
			t.Fatalf("missing %s accepted", key)
		}
	}
	for _, bad := range []string{strings.Replace(string(raw), `"plan"`, `" plan"`, 1), strings.Replace(string(raw), strings.Repeat("a", 64), strings.Repeat("A", 64), 1), strings.TrimSuffix(string(raw), "}") + `,"resetBudget":true}`} {
		if _, err := ParsePlanningRecoveryBinding([]byte(bad)); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
}
