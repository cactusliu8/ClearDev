package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMailAttemptPolicyKeepsIndependentFiniteCounters(t *testing.T) {
	cases := []struct {
		name    string
		kinds   []string
		rework  bool
		want    string
		allowed bool
	}{
		{"initial", nil, false, MailAttemptDevelopment, true},
		{"third", []string{MailAttemptDevelopment, MailAttemptDevelopment}, false, MailAttemptDevelopment, true},
		{"normal-exhausted", []string{MailAttemptDevelopment, MailAttemptDevelopment, MailAttemptDevelopment}, false, "", false},
		{"review-repair", []string{MailAttemptDevelopment, MailAttemptDevelopment, MailAttemptDevelopment}, true, MailAttemptReviewRepair, true},
		{"second-review-rework", []string{MailAttemptDevelopment, MailAttemptReviewRepair}, true, "", false},
		{"repair-tests-failed", []string{MailAttemptDevelopment, MailAttemptReviewRepair}, false, "", false},
		{"human-does-not-reset-development", []string{MailAttemptDevelopment, MailAttemptDevelopment, MailAttemptDevelopment, MailAttemptHumanExtra}, false, "", false},
		{"all-five-used", []string{MailAttemptDevelopment, MailAttemptDevelopment, MailAttemptDevelopment, MailAttemptReviewRepair, MailAttemptHumanExtra}, true, "", false},
		{"unknown-kind", []string{"AGENT_SELECTED"}, false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slots := []MailAttemptSlot{}
			for _, k := range tc.kinds {
				slots = append(slots, MailAttemptSlot{Kind: k})
			}
			kind, allowed := NextAutomaticMailAttempt(slots, tc.rework)
			if kind != tc.want || allowed != tc.allowed {
				t.Fatalf("kind=%s allowed=%v", kind, allowed)
			}
		})
	}
}

func TestExtraMailAttemptBindingIsStrictAndRegistered(t *testing.T) {
	b := ExtraMailAttemptBinding{DevelopmentRequirementID: "req", RequirementVersionID: "version", RequirementVersionSHA256: strings.Repeat("a", 64), ExecutionRunID: "run", PlanSHA256: strings.Repeat("b", 64), TaskID: "task", DispatchID: "dispatch", CandidateSHA: strings.Repeat("c", 40), NextRound: 3}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := ProductionDecisionRegistry().Lookup(HumanDecisionKindExtraMailAttempt)
	if !ok {
		t.Fatal("extra attempt is not registered in production authority")
	}
	if _, err := spec.ParseBinding(raw); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { delete(m, "dispatchId") },
		func(m map[string]any) { m["nextRound"] = 5 },
		func(m map[string]any) { m["nextRound"] = 0 },
		func(m map[string]any) { m["candidateSha"] = "HEAD" },
		func(m map[string]any) { m["planSha256"] = "" },
		func(m map[string]any) { m["maxAttempts"] = 100 },
		func(m map[string]any) { m["taskId"] = " another-task " },
	} {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		mutate(m)
		changed, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := spec.ParseBinding(changed); err == nil {
			t.Fatalf("accepted invalid authority: %s", changed)
		}
	}
}
