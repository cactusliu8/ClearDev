package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type fakeClearDevService struct {
	createInput           cleardevsvc.CreateRequirementInput
	createComplexInput    cleardevsvc.CreateComplexRequirementInput
	clarifyInput          cleardevsvc.SubmitComplexClarificationsInput
	getID                 string
	startID               string
	complexExecutionID    string
	clarifyID             string
	proposeID             string
	createCalls           int
	createComplexCalls    int
	getCalls              int
	startCalls            int
	complexExecutionCalls int
	clarifyCalls          int
	proposeCalls          int
	view                  cleardevsvc.RequirementView
	createErr             error
	createComplexErr      error
	getErr                error
	startErr              error
	complexExecutionErr   error
	clarifyErr            error
	proposeErr            error
	proposeInput          cleardevsvc.ProposeDirectionIntentInput
	progressID            string
	progressCalls         int
	progressView          cleardevsvc.ProjectProgressView
	progressErr           error
	explainID             string
	explainCalls          int
	explainErr            error
	reworkTaskID          string
	reworkCalls           int
	reworkErr             error
	budgetTaskID          string
	budgetCalls           int
	budgetErr             error
}

func (f *fakeClearDevService) CreateRequirement(_ context.Context, input cleardevsvc.CreateRequirementInput) (cleardevsvc.RequirementView, error) {
	f.createCalls++
	f.createInput = input
	return f.view, f.createErr
}

func (f *fakeClearDevService) CreateComplexRequirement(_ context.Context, input cleardevsvc.CreateComplexRequirementInput) (cleardevsvc.RequirementView, error) {
	f.createComplexCalls++
	f.createComplexInput = input
	return f.view, f.createComplexErr
}

func (f *fakeClearDevService) GetRequirement(_ context.Context, id string) (cleardevsvc.RequirementView, error) {
	f.getCalls++
	f.getID = id
	return f.view, f.getErr
}

func (f *fakeClearDevService) SubmitComplexClarifications(_ context.Context, id string, input cleardevsvc.SubmitComplexClarificationsInput) (cleardevsvc.RequirementView, error) {
	f.clarifyCalls++
	f.clarifyID = id
	f.clarifyInput = input
	return f.view, f.clarifyErr
}

func (f *fakeClearDevService) StartStandardFlow(_ context.Context, id string) (cleardevsvc.RequirementView, error) {
	f.startCalls++
	f.startID = id
	return f.view, f.startErr
}

func (f *fakeClearDevService) StartComplexStandardExecution(_ context.Context, id string) (cleardevsvc.RequirementView, error) {
	f.complexExecutionCalls++
	f.complexExecutionID = id
	return f.view, f.complexExecutionErr
}

func (f *fakeClearDevService) ProposeDirectionIntent(_ context.Context, id string, input cleardevsvc.ProposeDirectionIntentInput) (cleardevsvc.RequirementView, error) {
	f.proposeCalls++
	f.proposeID = id
	f.proposeInput = input
	return f.view, f.proposeErr
}

func (f *fakeClearDevService) ListProjectProgress(_ context.Context, projectID string) (cleardevsvc.ProjectProgressView, error) {
	f.progressCalls++
	f.progressID = projectID
	return f.progressView, f.progressErr
}

func (f *fakeClearDevService) RequestProgressExplanation(_ context.Context, id string) (cleardevsvc.RequirementView, error) {
	f.explainCalls++
	f.explainID = id
	return f.view, f.explainErr
}

func (f *fakeClearDevService) RequestDevelopmentTaskRework(_ context.Context, _ string, taskID string) error {
	f.reworkCalls++
	f.reworkTaskID = taskID
	return f.reworkErr
}

func (f *fakeClearDevService) RequestExtraReviewBudget(_ context.Context, _ string, taskID string) (cleardevsvc.RequirementView, error) {
	f.budgetCalls++
	f.budgetTaskID = taskID
	return f.view, f.budgetErr
}

func (f *fakeClearDevService) RequestControlledEngineChange(_ context.Context, _ string, _ core.ControlledEngineChangeBinding) (cleardevsvc.RequirementView, error) {
	return f.view, nil
}

func (f *fakeClearDevService) RequestExtraBuilderTurn(_ context.Context, _ string, taskID string) (cleardevsvc.RequirementView, error) {
	f.budgetCalls++
	f.budgetTaskID = taskID
	return f.view, f.budgetErr
}

func clearDevHTTPServer(t *testing.T, service cleardevsvc.ClearDevHTTPService) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{ClearDev: service}, httpd.ControlDeps{}))
	t.Cleanup(server.Close)
	return server
}

func TestClearDevHTTPCreateAndGet(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{}}
	server := clearDevHTTPServer(t, fake)
	request := `{"aoProjectId":"ao-1","name":"demo","requirementText":"requirement"}`
	body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements", request)
	assertJSON(t, headers)
	if status != http.StatusCreated || !strings.Contains(string(body), `"requirement"`) {
		t.Fatalf("POST status=%d body=%s", status, body)
	}
	assertRequirementResponseTopLevel(t, body)
	if fake.createCalls != 1 || fake.createInput.AOProjectID != "ao-1" || fake.createInput.Name != "demo" || fake.createInput.RequirementText != "requirement" {
		t.Fatalf("create input = %+v calls=%d", fake.createInput, fake.createCalls)
	}

	body, status, headers = doRequest(t, server, http.MethodGet, "/api/v1/cleardev/requirements/dev-1", "")
	assertJSON(t, headers)
	if status != http.StatusOK || fake.getCalls != 1 || fake.getID != "dev-1" || !strings.Contains(string(body), `"requirement"`) {
		t.Fatalf("GET status=%d body=%s id=%q calls=%d", status, body, fake.getID, fake.getCalls)
	}
	assertRequirementResponseTopLevel(t, body)

	_, status, _ = doRequest(t, server, http.MethodGet, "/api/v1/cleardev/projects/dev-1", "")
	if status != http.StatusNotFound {
		t.Fatalf("legacy project route status = %d, want 404", status)
	}
	if fake.getCalls != 1 {
		t.Fatalf("legacy project route reached service; get calls = %d", fake.getCalls)
	}
}

func assertRequirementResponseTopLevel(t *testing.T, body []byte) {
	t.Helper()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, body)
	}
	for _, key := range []string{"requirement", "requirementVersions", "developmentTasks", "overallProgress", "trustedProgress"} {
		if _, ok := response[key]; !ok {
			t.Fatalf("response misses top-level %q: %s", key, body)
		}
	}
}

func TestClearDevHTTPRejectsInvalidJSONBeforeService(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	for _, body := range []string{
		`{"aoProjectId":"ao","unknown":true}`,
		`{"aoProjectId":"ao"} {"aoProjectId":"second"}`,
		`[1,2,3]`,
	} {
		response, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements", body)
		assertErrorCode(t, response, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.createCalls != 0 {
		t.Fatalf("invalid JSON called service %d times", fake.createCalls)
	}
}

func TestClearDevHTTPStableErrorsAndStubs(t *testing.T) {
	fake := &fakeClearDevService{
		createErr: apierr.Invalid("AO_PROJECT_KIND_UNSUPPORTED", "unsupported", nil),
		getErr:    apierr.NotFound("CLEARDEV_REQUIREMENT_NOT_FOUND", "missing"),
	}
	server := clearDevHTTPServer(t, fake)
	request := `{"aoProjectId":"ao","name":"demo","requirementText":"requirement"}`
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements", request)
	assertErrorCode(t, body, status, http.StatusBadRequest, "AO_PROJECT_KIND_UNSUPPORTED")
	body, status, _ = doRequest(t, server, http.MethodGet, "/api/v1/cleardev/requirements/missing", "")
	assertErrorCode(t, body, status, http.StatusNotFound, "CLEARDEV_REQUIREMENT_NOT_FOUND")

	stub := clearDevHTTPServer(t, nil)
	body, status, _ = doRequest(t, stub, http.MethodGet, "/api/v1/cleardev/requirements/missing", "")
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}

func TestClearDevHTTPStartStandardAcceptsEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{}}
	server := clearDevHTTPServer(t, fake)
	for _, requestBody := range []string{"", " ", "\n\t"} {
		body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/standard-runs", requestBody)
		assertJSON(t, headers)
		if status != http.StatusAccepted {
			t.Fatalf("body %q: status=%d body=%s", requestBody, status, body)
		}
		assertRequirementResponseTopLevel(t, body)
	}
	if fake.startCalls != 3 || fake.startID != "req-1" {
		t.Fatalf("start id/calls = %q/%d, want req-1/3", fake.startID, fake.startCalls)
	}
}

func TestClearDevHTTPStartStandardRejectsNonEmptyBodyBeforeService(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	for _, requestBody := range []string{"null", "{}", "[]", "{} {}", "not-json", strings.Repeat(" ", 4*1024+1)} {
		body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/standard-runs", requestBody)
		assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.startCalls != 0 {
		t.Fatalf("invalid bodies called service %d times", fake.startCalls)
	}
}

func TestClearDevHTTPStartStandardErrorsAndStub(t *testing.T) {
	fake := &fakeClearDevService{startErr: apierr.NotFound("CLEARDEV_REQUIREMENT_NOT_FOUND", "missing")}
	server := clearDevHTTPServer(t, fake)
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/missing/standard-runs", "")
	assertErrorCode(t, body, status, http.StatusNotFound, "CLEARDEV_REQUIREMENT_NOT_FOUND")

	stub := clearDevHTTPServer(t, nil)
	body, status, _ = doRequest(t, stub, http.MethodPost, "/api/v1/cleardev/requirements/req-1/standard-runs", "")
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}

func TestClearDevHTTPComplexParallelExecutionAcceptsEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{}}
	server := clearDevHTTPServer(t, fake)
	body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/execution-runs", "")
	assertJSON(t, headers)
	if status != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", status, body)
	}
	assertRequirementResponseTopLevel(t, body)
	if fake.complexExecutionCalls != 1 || fake.complexExecutionID != "req-1" {
		t.Fatalf("execution id/calls = %q/%d, want req-1/1", fake.complexExecutionID, fake.complexExecutionCalls)
	}
}

func TestClearDevHTTPComplexParallelExecutionRejectsNonEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/execution-runs", `{"mode":"PARALLEL"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	if fake.complexExecutionCalls != 0 {
		t.Fatalf("non-empty body called execution service %d times", fake.complexExecutionCalls)
	}
}

func TestClearDevHTTPComplexStandardExecutionAcceptsEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{}}
	server := clearDevHTTPServer(t, fake)
	for _, requestBody := range []string{""} {
		body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/execution-runs", requestBody)
		assertJSON(t, headers)
		if status != http.StatusAccepted {
			t.Fatalf("body %q: status=%d body=%s", requestBody, status, body)
		}
		assertRequirementResponseTopLevel(t, body)
	}
	if fake.complexExecutionCalls != 1 || fake.complexExecutionID != "req-1" {
		t.Fatalf("execution id/calls = %q/%d, want req-1/1", fake.complexExecutionID, fake.complexExecutionCalls)
	}
}

func TestClearDevHTTPComplexStandardExecutionRejectsNonEmptyBodyBeforeService(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	for _, requestBody := range []string{" ", "\n\t", "null", "{}", "[]", "{} {}", "not-json", strings.Repeat(" ", 4*1024+1)} {
		body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/execution-runs", requestBody)
		assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.complexExecutionCalls != 0 {
		t.Fatalf("invalid bodies called execution service %d times", fake.complexExecutionCalls)
	}
}

func TestClearDevHTTPComplexStandardExecutionErrorsAndStub(t *testing.T) {
	fake := &fakeClearDevService{complexExecutionErr: apierr.NotFound("CLEARDEV_REQUIREMENT_NOT_FOUND", "missing")}
	server := clearDevHTTPServer(t, fake)
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/missing/execution-runs", "")
	assertErrorCode(t, body, status, http.StatusNotFound, "CLEARDEV_REQUIREMENT_NOT_FOUND")

	fake.complexExecutionErr = apierr.Conflict("COMPLEX_EXECUTION_PLAN_NOT_READY", "plan is not ready", nil)
	body, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/execution-runs", "")
	assertErrorCode(t, body, status, http.StatusConflict, "COMPLEX_EXECUTION_PLAN_NOT_READY")

	stub := clearDevHTTPServer(t, nil)
	body, status, _ = doRequest(t, stub, http.MethodPost, "/api/v1/cleardev/requirements/req-1/execution-runs", "")
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}

func TestClearDevHTTPQuickExecutionAcceptsEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{
		QuickExecution: &core.ComplexQuickSnapshot{Run: core.ComplexQuickRun{Mode: core.WorkModeQuick, ModeReason: string(core.ReasonQuickFollowUpApproved)}},
	}}
	server := clearDevHTTPServer(t, fake)
	body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/execution-runs", "")
	assertJSON(t, headers)
	if status != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", status, body)
	}
	assertRequirementResponseTopLevel(t, body)
	if !strings.Contains(string(body), `"quickExecution"`) || !strings.Contains(string(body), `"QUICK"`) {
		t.Fatalf("QUICK read model missing from empty-body start: %s", body)
	}
	if fake.complexExecutionCalls != 1 || fake.complexExecutionID != "req-1" {
		t.Fatalf("execution id/calls = %q/%d, want req-1/1", fake.complexExecutionID, fake.complexExecutionCalls)
	}
}

func TestClearDevHTTPQuickExecutionRejectsNonEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/execution-runs", `{"mode":"QUICK"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	if fake.complexExecutionCalls != 0 {
		t.Fatalf("non-empty QUICK body called execution service %d times", fake.complexExecutionCalls)
	}
}

func TestClearDevHTTPControlledExceptionAcceptsEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{
		ComplexExecution: &core.ComplexExecutionSnapshot{
			Exception: &core.ComplexExceptionFacts{
				Budgets:         []core.ComplexExceptionBudget{{RoleKind: core.ComplexExceptionBudgetBuilder, MaxTurns: 3}},
				ScopeRequests:   []core.ComplexScopeExpansionRequest{{Status: "APPROVED"}},
				GeneratedProofs: []core.ComplexGeneratedProof{{Status: "SETTLED", Result: core.EvidenceResultPass}},
			},
		},
	}}
	server := clearDevHTTPServer(t, fake)
	body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/execution-runs", "")
	assertJSON(t, headers)
	if status != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", status, body)
	}
	assertRequirementResponseTopLevel(t, body)
	if !strings.Contains(string(body), `"exception"`) || !strings.Contains(string(body), `"scopeRequests"`) || !strings.Contains(string(body), `"generatedProofs"`) {
		t.Fatalf("exception read model missing from empty-body start: %s", body)
	}
	if fake.complexExecutionCalls != 1 || fake.complexExecutionID != "req-1" {
		t.Fatalf("execution id/calls = %q/%d, want req-1/1", fake.complexExecutionID, fake.complexExecutionCalls)
	}
}

func TestClearDevHTTPHasNoHumanDecisionWriteRoutes(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	for _, path := range []string{
		"/api/v1/cleardev/requirements/req-1/confirm",
		"/api/v1/cleardev/requirements/req-1/reject",
		"/api/v1/cleardev/requirements/req-1/approve",
		"/api/v1/cleardev/human-decisions",
		"/api/v1/cleardev/human-decisions/decision-1",
	} {
		_, status, _ := doRequest(t, server, http.MethodPost, path, `{}`)
		if status != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, status)
		}
	}
	if fake.createCalls != 0 || fake.createComplexCalls != 0 || fake.getCalls != 0 || fake.startCalls != 0 || fake.complexExecutionCalls != 0 || fake.clarifyCalls != 0 || fake.proposeCalls != 0 {
		t.Fatalf("human-decision routes reached the service: %+v", fake)
	}
}

func TestClearDevHTTPCreateComplexAndClarify(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{}}
	server := clearDevHTTPServer(t, fake)
	request := `{"aoProjectId":"ao-1","name":"complex","prdText":"original prd"}`
	body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/complex", request)
	assertJSON(t, headers)
	if status != http.StatusCreated {
		t.Fatalf("POST complex status=%d body=%s", status, body)
	}
	assertRequirementResponseTopLevel(t, body)
	if fake.createComplexCalls != 1 || fake.createComplexInput.AOProjectID != "ao-1" || fake.createComplexInput.Name != "complex" || fake.createComplexInput.PRDText != "original prd" {
		t.Fatalf("complex create input = %+v calls=%d", fake.createComplexInput, fake.createComplexCalls)
	}
	if fake.createCalls != 0 {
		t.Fatalf("complex create used the simple create path %d times", fake.createCalls)
	}

	clarify := `{"compilationRequestId":"req-1","clarificationRound":0,"answers":[{"questionKey":"invalid-address","text":"计入拒绝"}]}`
	body, status, headers = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/dev-1/complex-clarifications", clarify)
	assertJSON(t, headers)
	if status != http.StatusOK || fake.clarifyCalls != 1 || fake.clarifyID != "dev-1" {
		t.Fatalf("clarify status=%d body=%s id=%q calls=%d", status, body, fake.clarifyID, fake.clarifyCalls)
	}
	if fake.clarifyInput.CompilationRequestID != "req-1" || fake.clarifyInput.ClarificationRound != 0 || len(fake.clarifyInput.Answers) != 1 {
		t.Fatalf("clarify input = %+v", fake.clarifyInput)
	}
}

func TestClearDevHTTPCreateComplexRejectsUnknownFieldsBeforeService(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	for _, body := range []string{
		`{"aoProjectId":"ao","name":"n","prdText":"p","unknown":true}`,
		`{"aoProjectId":"ao","name":"n","requirementText":"p"}`,
		`{"aoProjectId":"ao"} {"aoProjectId":"second"}`,
	} {
		response, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/complex", body)
		assertErrorCode(t, response, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.createComplexCalls != 0 || fake.createCalls != 0 {
		t.Fatalf("invalid complex JSON called service create=%d complex=%d", fake.createCalls, fake.createComplexCalls)
	}
}

func TestClearDevHTTPComplexStubAndErrors(t *testing.T) {
	fake := &fakeClearDevService{
		createComplexErr: apierr.Invalid("PRD_TEXT_INVALID", "empty", nil),
		clarifyErr:       apierr.Conflict("PRECONDITION_NOT_MET", "unanswered round mismatch", nil),
	}
	server := clearDevHTTPServer(t, fake)
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/complex", `{"aoProjectId":"ao","name":"n","prdText":"p"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "PRD_TEXT_INVALID")
	body, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/complex-clarifications", `{"compilationRequestId":"c","clarificationRound":0,"answers":[]}`)
	assertErrorCode(t, body, status, http.StatusConflict, "PRECONDITION_NOT_MET")

	stub := clearDevHTTPServer(t, nil)
	body, status, _ = doRequest(t, stub, http.MethodPost, "/api/v1/cleardev/requirements/complex", `{"aoProjectId":"ao","name":"n","prdText":"p"}`)
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
	body, status, _ = doRequest(t, stub, http.MethodPost, "/api/v1/cleardev/requirements/req-1/complex-clarifications", `{"compilationRequestId":"c","clarificationRound":0,"answers":[]}`)
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}

func TestClearDevHTTPProposeDirectionChangeIntent(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{}}
	server := clearDevHTTPServer(t, fake)
	request := `{"requestId":"dir-1","developmentRequirementId":"dev-1","message":"只接受 example.com 域名"}`
	body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/dev-1/direction-intents", request)
	assertJSON(t, headers)
	if status != http.StatusAccepted || fake.proposeCalls != 1 || fake.proposeID != "dev-1" {
		t.Fatalf("propose status=%d body=%s id=%q calls=%d", status, body, fake.proposeID, fake.proposeCalls)
	}
	if fake.proposeInput.RequestID != "dir-1" || fake.proposeInput.DevelopmentRequirementID != "dev-1" || fake.proposeInput.Message != "只接受 example.com 域名" {
		t.Fatalf("propose input = %+v", fake.proposeInput)
	}
	assertRequirementResponseTopLevel(t, body)
}

func TestClearDevHTTPProposeDirectionChangeRejectsUnknownFieldsBeforeService(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	for _, body := range []string{
		`{"requestId":"dir-1","developmentRequirementId":"dev-1","message":"m","unknown":true}`,
		`{"requestId":"dir-1"} {"requestId":"dir-2"}`,
		`[]`,
	} {
		response, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/dev-1/direction-intents", body)
		assertErrorCode(t, response, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.proposeCalls != 0 {
		t.Fatalf("invalid direction JSON called service %d times", fake.proposeCalls)
	}
}

func TestClearDevHTTPProposeDirectionChangeStubAndErrors(t *testing.T) {
	fake := &fakeClearDevService{proposeErr: apierr.Conflict("PRECONDITION_NOT_MET", "already has a direction change", nil)}
	server := clearDevHTTPServer(t, fake)
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/direction-intents", `{"requestId":"dir-1","developmentRequirementId":"req-1","message":"change"}`)
	assertErrorCode(t, body, status, http.StatusConflict, "PRECONDITION_NOT_MET")

	stub := clearDevHTTPServer(t, nil)
	body, status, _ = doRequest(t, stub, http.MethodPost, "/api/v1/cleardev/requirements/req-1/direction-intents", `{"requestId":"dir-1","developmentRequirementId":"req-1","message":"change"}`)
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}

func TestClearDevHTTPListProjectProgress(t *testing.T) {
	fake := &fakeClearDevService{progressView: cleardevsvc.ProjectProgressView{
		AOProjectID: "ao-1",
		Requirements: []core.TrustedProgressSummary{
			{
				DevelopmentRequirementID: "req-1", AOProjectID: "ao-1", Name: "Needs human",
				Phase: core.TrustedPhaseAwaitingConfirmation, Attention: core.OverallAttentionNeedsHuman,
				NextOwner: core.TrustedOwner{Role: core.TrustedOwnerHuman, Action: core.TrustedActionConfirmRequirement},
				SortRank:  core.TrustedSortNeedsHuman,
			},
			{
				DevelopmentRequirementID: "req-2", AOProjectID: "ao-1", Name: "Scope wait",
				Phase: core.TrustedPhaseAwaitingScope, Attention: core.OverallAttentionNone,
				NextOwner: core.TrustedOwner{Role: core.TrustedOwnerSteward, Action: core.TrustedActionDecideScope},
				SortRank:  core.TrustedSortInProgress,
			},
		},
	}}
	server := clearDevHTTPServer(t, fake)
	body, status, headers := doRequest(t, server, http.MethodGet, "/api/v1/cleardev/projects/ao-1/progress", "")
	assertJSON(t, headers)
	if status != http.StatusOK || fake.progressCalls != 1 || fake.progressID != "ao-1" {
		t.Fatalf("progress status=%d body=%s id=%q calls=%d", status, body, fake.progressID, fake.progressCalls)
	}
	if !strings.Contains(string(body), `"aoProjectId"`) || !strings.Contains(string(body), `"requirements"`) {
		t.Fatalf("progress body = %s", body)
	}
	if !strings.Contains(string(body), `"AWAITING_SCOPE"`) || !strings.Contains(string(body), `"STEWARD"`) || !strings.Contains(string(body), `"DECIDE_SCOPE"`) {
		t.Fatalf("scope wait progress body = %s", body)
	}
	_, status, _ = doRequest(t, server, http.MethodGet, "/api/v1/cleardev/projects/dev-1", "")
	if status != http.StatusNotFound {
		t.Fatalf("legacy project route status = %d, want 404", status)
	}
}

func TestClearDevHTTPRequestProgressExplanationAcceptsEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{}}
	server := clearDevHTTPServer(t, fake)
	body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/progress-explanations", "")
	assertJSON(t, headers)
	if status != http.StatusAccepted || fake.explainCalls != 1 || fake.explainID != "req-1" {
		t.Fatalf("explain status=%d body=%s id=%q calls=%d", status, body, fake.explainID, fake.explainCalls)
	}
	assertRequirementResponseTopLevel(t, body)
}

func TestClearDevHTTPRequestProgressExplanationRejectsNonEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	for _, requestBody := range []string{" ", "\n\t", "null", "{}", `{"phase":"COMPLETED"}`, "[]"} {
		body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/progress-explanations", requestBody)
		assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.explainCalls != 0 {
		t.Fatalf("non-empty explanation body called service %d times", fake.explainCalls)
	}
}

func TestClearDevHTTPReworkDevelopmentTaskAcceptsEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{}}
	server := clearDevHTTPServer(t, fake)
	body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/development-tasks/task-9/rework", "")
	assertJSON(t, headers)
	if status != http.StatusAccepted || fake.reworkCalls != 1 || fake.reworkTaskID != "task-9" {
		t.Fatalf("rework status=%d body=%s task=%q calls=%d", status, body, fake.reworkTaskID, fake.reworkCalls)
	}
	assertRequirementResponseTopLevel(t, body)
}

func TestClearDevHTTPReworkDevelopmentTaskRejectsNonEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	for _, requestBody := range []string{" ", "null", "{}", "[]"} {
		body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/development-tasks/task-9/rework", requestBody)
		assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.reworkCalls != 0 {
		t.Fatalf("non-empty rework body called service %d times", fake.reworkCalls)
	}
}

func TestClearDevHTTPRequestExtraReviewBudgetAcceptsEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{view: cleardevsvc.RequirementView{}}
	server := clearDevHTTPServer(t, fake)
	body, status, headers := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/development-tasks/task-9/review-budget-authorizations", "")
	assertJSON(t, headers)
	if status != http.StatusAccepted || fake.budgetCalls != 1 || fake.budgetTaskID != "task-9" {
		t.Fatalf("budget request status=%d body=%s task=%q calls=%d", status, body, fake.budgetTaskID, fake.budgetCalls)
	}
	assertRequirementResponseTopLevel(t, body)
}

func TestClearDevHTTPRequestExtraReviewBudgetRejectsNonEmptyBody(t *testing.T) {
	fake := &fakeClearDevService{}
	server := clearDevHTTPServer(t, fake)
	for _, requestBody := range []string{" ", "null", "{}", "[]"} {
		body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/development-tasks/task-9/review-budget-authorizations", requestBody)
		assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.budgetCalls != 0 {
		t.Fatalf("non-empty budget body called service %d times", fake.budgetCalls)
	}
}

func TestClearDevHTTPProgressStubAndErrors(t *testing.T) {
	fake := &fakeClearDevService{
		progressErr: apierr.NotFound("AO_PROJECT_NOT_FOUND", "missing"),
		explainErr:  apierr.Conflict("STEWARD_UNAVAILABLE", "no steward", nil),
	}
	server := clearDevHTTPServer(t, fake)
	body, status, _ := doRequest(t, server, http.MethodGet, "/api/v1/cleardev/projects/missing/progress", "")
	assertErrorCode(t, body, status, http.StatusNotFound, "AO_PROJECT_NOT_FOUND")
	body, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req-1/progress-explanations", "")
	assertErrorCode(t, body, status, http.StatusConflict, "STEWARD_UNAVAILABLE")

	stub := clearDevHTTPServer(t, nil)
	body, status, _ = doRequest(t, stub, http.MethodGet, "/api/v1/cleardev/projects/ao-1/progress", "")
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
	body, status, _ = doRequest(t, stub, http.MethodPost, "/api/v1/cleardev/requirements/req-1/progress-explanations", "")
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}
