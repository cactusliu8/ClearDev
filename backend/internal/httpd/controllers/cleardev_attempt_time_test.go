package controllers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

func TestClearDevHTTPAttemptTimeSemantics(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{AgentStepAttempts: []core.AgentStepAttemptView{
		{RequestedAt: &now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation},
		{RequestedAt: &now, RequestedAtSemantics: core.AttemptTimeLegacyStepRequest},
		{RequestedAt: &now, RequestedAtSemantics: core.AttemptTimeLegacyRetryBoundary},
		{RequestedAt: &now, RequestedAtSemantics: core.AttemptTimeUnknown, LegacyEvidenceMissing: true},
	}}}
	server := clearDevHTTPServer(t, fake)
	body, status, _ := doRequest(t, server, http.MethodGet, "/api/v1/cleardev/requirements/req-1", "")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var response struct {
		Attempts []map[string]json.RawMessage `json:"agentStepAttempts"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Attempts) != 4 {
		t.Fatalf("body=%s", body)
	}
	for i, a := range response.Attempts {
		if _, ok := a["createdAt"]; !ok {
			t.Fatal("nullable creation field omitted")
		}
		if i == 0 {
			if string(a["createdAt"]) != string(a["requestedAt"]) {
				t.Fatal("actual time mismatch")
			}
		} else if string(a["createdAt"]) != "null" {
			t.Fatal("invented legacy creation time")
		}
		expected, _ := json.Marshal(fake.view.AgentStepAttempts[i].RequestedAtSemantics)
		if string(a["requestedAtSemantics"]) != string(expected) {
			t.Fatalf("semantics=%s", a["requestedAtSemantics"])
		}
	}
}
