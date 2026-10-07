package controllers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type projectExecutionHTTPFake struct {
	fakeClearDevService
	input core.ProjectExecutionAdmission
	id    string
	calls int
	err   error
}

func (f *projectExecutionHTTPFake) StartProjectExecution(_ context.Context, id string, input core.ProjectExecutionAdmission) (cleardevsvc.RequirementView, error) {
	f.calls++
	f.id, f.input = id, input
	return f.view, f.err
}

func projectAdmissionJSON(t *testing.T) (core.ProjectExecutionAdmission, string) {
	t.Helper()
	input := core.ProjectExecutionAdmission{
		RequestID: "admit-1", PlanID: "plan-1", PlanSHA256: strings.Repeat("a", 64),
		RequirementSHA256: strings.Repeat("b", 64), BaseCommitSHA: strings.Repeat("c", 40),
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return input, string(raw)
}

func TestClearDevHTTPProjectExecutionCarriesOnlyExactAdmission(t *testing.T) {
	fake := &projectExecutionHTTPFake{}
	server := clearDevHTTPServer(t, fake)
	input, request := projectAdmissionJSON(t)
	// HTTP is a thin transport. The actual Service/SQLite duplicate-run test
	// owns idempotency; this test verifies that retries keep the supplied ID.
	for range 2 {
		body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/project-execution-runs", request)
		assertJSON(t, headers)
		if status != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", status, body)
		}
		assertRequirementResponseTopLevel(t, body)
		if fake.id != "req-1" || fake.input != input || fake.complexExecutionCalls != 0 || fake.startCalls != 0 {
			t.Fatalf("wrong admission or legacy execution called: %+v", fake)
		}
	}
	if fake.calls != 2 {
		t.Fatalf("calls=%d", fake.calls)
	}
}

func TestClearDevHTTPProjectExecutionRejectsRuntimeOverrides(t *testing.T) {
	fake := &projectExecutionHTTPFake{}
	server := clearDevHTTPServer(t, fake)
	_, valid := projectAdmissionJSON(t)
	for _, raw := range []string{"", "null", "[]", "{} {}", "invalid", strings.Repeat(" ", 4097),
		strings.TrimSuffix(valid, "}") + `,"command":["sh","-c","true"]}`,
		strings.TrimSuffix(valid, "}") + `,"candidateSha":"override"}`,
		strings.TrimSuffix(valid, "}") + `,"approved":true}`} {
		body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/project-execution-runs", raw)
		assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.calls != 0 {
		t.Fatalf("invalid input reached admission service %d times", fake.calls)
	}
}

func TestClearDevHTTPProjectExecutionPropagatesGuardAndUnsupported(t *testing.T) {
	fake := &projectExecutionHTTPFake{err: apierr.Conflict("PROJECT_EXECUTION_BINDING_CHANGED", "stale plan", nil)}
	server := clearDevHTTPServer(t, fake)
	_, raw := projectAdmissionJSON(t)
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/project-execution-runs", raw)
	assertErrorCode(t, body, status, http.StatusConflict, "PROJECT_EXECUTION_BINDING_CHANGED")
	stub := clearDevHTTPServer(t, &fakeClearDevService{})
	body, status, _ = doRequest(t, stub, http.MethodPost, "/api/v1/cleardev/requirements/req-1/project-execution-runs", raw)
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
	// The old route still rejects admission JSON rather than changing its
	// historical empty-body semantics or creating a generic execution.
	body, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/execution-runs", raw)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	if fake.calls != 1 || fake.complexExecutionCalls != 0 {
		t.Fatalf("legacy endpoint bypassed the admission boundary: %+v", fake)
	}
}
