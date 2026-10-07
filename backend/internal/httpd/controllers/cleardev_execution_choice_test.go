package controllers_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type executionChoiceHTTPFake struct {
	fakeClearDevService
	projectID     string
	requirementID string
	choice        domain.ClearDevExecutionConfig
	retry         cleardevsvc.RetryPreflightInput
	calls         int
	err           error
}

func (f *executionChoiceHTTPFake) SetProjectExecution(_ context.Context, id string, choice domain.ClearDevExecutionConfig) (*cleardevsvc.ExecutionChoiceView, error) {
	f.projectID, f.choice = id, choice
	f.calls++
	return &cleardevsvc.ExecutionChoiceView{Agent: choice.Harness, Model: choice.Model, Known: true}, f.err
}

func (f *executionChoiceHTTPFake) RetryControlledPreflight(_ context.Context, id string, input cleardevsvc.RetryPreflightInput) (cleardevsvc.RequirementView, error) {
	f.requirementID, f.retry = id, input
	f.calls++
	return cleardevsvc.RequirementView{}, f.err
}

func TestExecutionChoiceHTTPBindsOnlySelectionAndCurrentFailure(t *testing.T) {
	fake := &executionChoiceHTTPFake{}
	server := clearDevHTTPServer(t, fake)
	body, status, _ := doRequest(t, server, http.MethodPut, "/api/v1/cleardev/projects/p/execution", `{"agent":"opencode","model":"example/model"}`)
	if status != http.StatusOK || fake.calls != 1 || fake.projectID != "p" || fake.choice.Harness != domain.HarnessOpenCode || fake.choice.Model != "example/model" {
		t.Fatalf("selection routing: status=%d fake=%+v body=%s", status, fake, body)
	}
	body, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/r/preflight-retries", `{"preflightId":"current-failure"}`)
	if status != http.StatusAccepted || fake.calls != 2 || fake.requirementID != "r" || fake.retry.PreflightID != "current-failure" {
		t.Fatalf("retry routing: status=%d fake=%+v body=%s", status, fake, body)
	}
	fake.err = apierr.Conflict("EXECUTION_CHOICE_FROZEN", "fixed project", nil)
	body, status, _ = doRequest(t, server, http.MethodPut, "/api/v1/cleardev/projects/p/execution", `{"agent":"codex","model":"other"}`)
	assertErrorCode(t, body, status, http.StatusConflict, "EXECUTION_CHOICE_FROZEN")
}

func TestExecutionChoiceHTTPRefusesAuthorityAndConfigurationInjection(t *testing.T) {
	fake := &executionChoiceHTTPFake{}
	server := clearDevHTTPServer(t, fake)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/cleardev/projects/p/execution", `{"agent":"opencode","model":"example/model","env":{"SECRET":"value"}}`},
		{http.MethodPut, "/api/v1/cleardev/projects/p/execution", `{"agent":"opencode","model":"example/model","permissions":"bypass-permissions"}`},
		{http.MethodPut, "/api/v1/cleardev/projects/p/execution", `{"agent":"opencode","model":"example/model"} {}`},
		{http.MethodPost, "/api/v1/cleardev/requirements/r/preflight-retries", `{"preflightId":"x","approved":true}`},
		{http.MethodPost, "/api/v1/cleardev/requirements/r/preflight-retries", `{"preflightId":"x","candidateSha":"caller"}`},
		{http.MethodPost, "/api/v1/cleardev/requirements/r/preflight-retries", `{"preflightId":"x","command":"arbitrary"}`},
	} {
		body, status, _ := doRequest(t, server, tc.method, tc.path, tc.body)
		assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.calls != 0 {
		t.Fatalf("untrusted configuration reached service: calls=%d", fake.calls)
	}
	stub := clearDevHTTPServer(t, &fakeClearDevService{})
	body, status, _ := doRequest(t, stub, http.MethodPut, "/api/v1/cleardev/projects/p/execution", `{"agent":"opencode","model":"example/model"}`)
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}
