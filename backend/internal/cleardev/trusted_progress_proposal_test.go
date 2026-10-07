package cleardev

import "testing"

func TestClearDevControlledProgressProposalBinding(t *testing.T) {
	r := FixedRecoveryRequest{ExecutionRunID: "run", TaskID: "task", FailureEventID: "failure"}
	p := ComplexRecoveryAction{ID: "proposal", ExecutionRunID: "run", ComplexExecutionTaskID: "task", TriggerFactID: "failure", Outcome: "PASS", Action: ComplexRecoveryActionRestoreSession}
	for _, tc := range []struct {
		name   string
		change func(*ComplexRecoveryAction)
	}{
		{"valid", func(*ComplexRecoveryAction) {}},
		{"other failure", func(p *ComplexRecoveryAction) { p.TriggerFactID = "other" }},
		{"request instead of failure", func(p *ComplexRecoveryAction) { p.TriggerFactID = "request" }},
		{"other run", func(p *ComplexRecoveryAction) { p.ExecutionRunID = "other" }},
		{"other task", func(p *ComplexRecoveryAction) { p.ComplexExecutionTaskID = "other" }},
		{"failed proposal", func(p *ComplexRecoveryAction) { p.Outcome = "FAIL" }},
		{"wrong action", func(p *ComplexRecoveryAction) { p.Action = ComplexRecoveryActionRebuildReviewer }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proposal := p
			tc.change(&proposal)
			e := ComplexExecutionSnapshot{Run: ComplexExecutionRun{ID: "run"}, Exception: &ComplexExceptionFacts{RecoveryActions: []ComplexRecoveryAction{proposal}}}
			got := controlledRecoveryProposal(e, r)
			if (got != "") != (tc.name == "valid") {
				t.Fatalf("unexpected proposal %q", got)
			}
		})
	}
}
