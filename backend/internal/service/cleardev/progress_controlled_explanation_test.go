package cleardev

import (
	"reflect"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestClearDevControlledProgressExplanationStalesWithoutRewriting(t *testing.T) {
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	f := core.TrustedProgressFacts{Now: now, Controlled: []core.ControlledProgressFacts{{BudgetVersion: core.MessageBudgetV1, Target: core.ControlledWork{RoleBindingID: "current"}, Preflight: &core.ControlledPreflight{ID: "failure", Outcome: core.ControlledPreflightFailed, ReasonCode: core.ReasonLoginRequired}}}}
	original := core.DeriveTrustedProgress(f)
	rows := []core.ProgressExplanationRequest{{ID: "note", FactSummarySHA256: original.FactSummarySHA256, Status: core.ProgressExplanationSettled, ResultJSON: `{"summary":"stored explanation"}`, CreatedAt: now}}
	before := append([]core.ProgressExplanationRequest(nil), rows...)
	attachTrustedExplanation(&original, rows)
	if original.Explanation == nil || original.Explanation.Stale {
		t.Fatal("own stored note was stale")
	}
	f.Controlled[0].Preflight = &core.ControlledPreflight{ID: "passed", Outcome: core.ControlledPreflightPassed}
	current := core.DeriveTrustedProgress(f)
	attachTrustedExplanation(&current, rows)
	if current.Explanation == nil || !current.Explanation.Stale || !reflect.DeepEqual(rows, before) {
		t.Fatal("changed facts did not stale immutable note")
	}
}

func TestClearDevControlledProgressProposalStalesExplanation(t *testing.T) {
	f := core.TrustedProgressFacts{Controlled: []core.ControlledProgressFacts{{BudgetVersion: core.MessageBudgetV1, Recovery: &core.FixedRecoveryEvidence{Request: core.FixedRecoveryRequest{ID: "request"}}}}}
	before := core.DeriveTrustedProgress(f)
	rows := []core.ProgressExplanationRequest{{ID: "old", FactSummarySHA256: before.FactSummarySHA256, Status: core.ProgressExplanationSettled, ResultJSON: `{"summary":"waiting for Recovery"}`}}
	saved := append([]core.ProgressExplanationRequest(nil), rows...)
	attachTrustedExplanation(&before, rows)
	f.Controlled[0].ProposalID = "proposal"
	after := core.DeriveTrustedProgress(f)
	attachTrustedExplanation(&after, rows)
	if before.Explanation == nil || before.Explanation.Stale || after.Explanation == nil || !after.Explanation.Stale || !reflect.DeepEqual(rows, saved) {
		t.Fatal("proposal must stale old immutable explanation")
	}
}
