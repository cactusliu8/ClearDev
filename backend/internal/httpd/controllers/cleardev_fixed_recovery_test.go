package controllers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

func TestClearDevFixedRecoveryHTTPPreservesUnknownAndReplacement(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{ComplexExecution: &core.ComplexExecutionSnapshot{
		Exception: &core.ComplexExceptionFacts{RecoveryActions: []core.ComplexRecoveryAction{{ID: "old-proposal", Outcome: "PASS"}}},
		FixedRecoveries: []core.FixedRecoveryEvidence{
			{Request: core.FixedRecoveryRequest{ID: "unknown", SessionID: "old"}, Claim: &core.FixedRecoveryClaim{RequestID: "unknown", ProposalID: "proposal"}},
			{Request: core.FixedRecoveryRequest{ID: "rebuild", ReviewID: "original-review", SessionID: "old"}, Result: &core.FixedRecoveryResult{RequestID: "rebuild", Outcome: "PASS", SessionID: "new"}, ReviewResult: &core.ReplacementReviewResult{RecoveryRequestID: "rebuild", OriginalReviewID: "original-review", AttemptID: "attempt-2", ResultID: "new-result", Verdict: core.LocalReviewPass}},
		},
	}}}
	server := clearDevHTTPServer(t, fake)
	body, status, _ := doRequest(t, server, http.MethodGet, "/api/v1/cleardev/requirements/req-1", "")
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	var decoded cleardevsvc.RequirementView
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	e := decoded.ComplexExecution
	if e == nil || e.Exception.RecoveryActions[0].ExecutionResult != nil || e.FixedRecoveries[0].Result != nil || e.FixedRecoveries[1].ReviewResult.OriginalReviewID != "original-review" || e.FixedRecoveries[1].Result.SessionID != "new" {
		t.Fatalf("evidence changed: %s", body)
	}
	var raw struct {
		Execution struct {
			Fixed []map[string]json.RawMessage `json:"fixedRecoveries"`
		} `json:"complexExecution"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw.Execution.Fixed[0]["result"]) != "null" {
		t.Fatal("unknown result was omitted or invented")
	}
}
