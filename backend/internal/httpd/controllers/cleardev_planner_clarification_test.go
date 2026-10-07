package controllers_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	svc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type plannerAnswerHTTPFake struct {
	fakeClearDevService
	calls int
	input svc.SubmitPlannerClarificationsInput
	err   error
}

func (f *plannerAnswerHTTPFake) SubmitPlannerClarifications(_ context.Context, _ string, input svc.SubmitPlannerClarificationsInput) (svc.RequirementView, error) {
	f.calls++
	f.input = input
	return svc.RequirementView{}, f.err
}
func TestPlannerAnswerHTTPRejectsAuthorityAndMapsErrors(t *testing.T) {
	f := &plannerAnswerHTTPFake{}
	server := clearDevHTTPServer(t, f)
	for _, body := range []string{`{"requestId":"r","planId":"p","planSha256":"s","answers":["a"],"approve":true}`, `{"requestId":"r","planId":"p","planSha256":"s","answers":["a"],"prompt":"override"}`, `{"requestId":"r","answers":["a"]} {}`} {
		_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/planner-clarifications", body)
		if status != 400 || f.calls != 0 {
			t.Fatal("untrusted authority reached Planner service")
		}
	}
	payload := `{"requestId":"original","planId":"question","planSha256":"s","answers":["a"]}`
	_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/planner-clarifications", payload)
	if status != 200 || f.calls != 1 || f.input.RequestID != "original" {
		t.Fatal("Planner answers lost exact request")
	}
	f.err = apierr.Conflict("PLANNER_QUESTION_CHANGED", "question changed", nil)
	_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/planner-clarifications", payload)
	if status != 409 {
		t.Fatal("stale Planner answer was not a conflict")
	}
}
