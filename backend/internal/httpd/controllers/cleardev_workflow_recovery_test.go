package controllers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	svc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type recoveryHTTPFake struct {
	fakeClearDevService
	calls     int
	input     svc.WorkflowRecoveryInput
	err       error
	diagnosis *core.WorkflowDiagnosis
	checks    []core.BuilderSessionCheck
	handling  []core.WorkflowFailureHandling
}

func (f *recoveryHTTPFake) GetWorkflowRecovery(context.Context, string) (core.WorkflowRecoveryView, error) {
	return core.WorkflowRecoveryView{ExecutionRunID: "run", Options: []core.WorkflowRecoveryOption{}, History: []core.WorkflowRecovery{}, Diagnosis: f.diagnosis, BuilderSessionChecks: f.checks, FailureHandling: f.handling}, f.err
}
func (f *recoveryHTTPFake) RequestWorkflowRecovery(_ context.Context, _ string, i svc.WorkflowRecoveryInput) (core.WorkflowRecoveryView, error) {
	f.calls++
	f.input = i
	return core.WorkflowRecoveryView{ExecutionRunID: "run", Options: []core.WorkflowRecoveryOption{}, History: []core.WorkflowRecovery{}}, f.err
}
func TestWorkflowRecoveryHTTPDiagnosisIsOptionalAndReadOnly(t *testing.T) {
	f := &recoveryHTTPFake{}
	server := clearDevHTTPServer(t, f)
	body, status, _ := doRequest(t, server, http.MethodGet, "/api/v1/cleardev/requirements/req/recoveries", "")
	if status != 200 || strings.Contains(string(body), `"diagnosis"`) {
		t.Fatal("old response shape no longer omits an absent diagnosis")
	}
	f.diagnosis = &core.WorkflowDiagnosis{Current: true, Phase: "BLOCKED", Issues: []core.WorkflowDiagnosisIssue{{ID: "stop", ReasonCode: "BUILDER_SPAWN_FAILED", Category: "SESSION", Relationship: "CURRENT", Evidence: []core.WorkflowDiagnosisEvidence{{Kind: "STEP_STATUS", FactID: "step", Value: "PENDING"}}}}}
	body, status, _ = doRequest(t, server, http.MethodGet, "/api/v1/cleardev/requirements/req/recoveries", "")
	if status != 200 || !strings.Contains(string(body), `"diagnosis"`) || !strings.Contains(string(body), `"BUILDER_SPAWN_FAILED"`) || f.calls != 0 {
		t.Fatalf("diagnosis omitted, changed request semantics or invoked recovery: %d %s", status, body)
	}
	payload := `{"requestId":"request","executionRunId":"run","action":"RETRY_CHECK","targetId":"check","supplement":"context","diagnosis":{"current":true}}`
	_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", payload)
	if status != 400 || f.calls != 0 {
		t.Fatal("client-supplied diagnosis became workflow authority")
	}
}

func TestWorkflowRecoveryHTTPRejectsScopeAndApprovalFields(t *testing.T) {
	f := &recoveryHTTPFake{}
	server := clearDevHTTPServer(t, f)
	body, status, _ := doRequest(t, server, http.MethodGet, "/api/v1/cleardev/requirements/req/recoveries", "")
	if status != 200 || !strings.Contains(string(body), `"executionRunId":"run"`) {
		t.Fatalf("get: %d %s", status, body)
	}
	payload := `{"requestId":"request","executionRunId":"run","action":"RETRY_CHECK","targetId":"check","supplement":"environment fixed"}`
	_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", payload)
	if status != 200 || f.calls != 1 || f.input.TargetID != "check" {
		t.Fatal("recovery payload was not delivered")
	}
	for _, field := range []string{`"approval":"APPROVE"`, `"command":"arbitrary"`, `"candidateSha":"changed"`} {
		_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", strings.TrimSuffix(payload, "}")+","+field+"}")
		if status != 400 || f.calls != 1 {
			t.Fatal("extra authority accepted")
		}
	}
	f.err = apierr.Conflict("RECOVERY_NOT_CURRENT", "stale", nil)
	_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", payload)
	if status != 409 {
		t.Fatalf("stale action status=%d", status)
	}
}

func TestBuilderSessionRecheckHTTPKeepsChecksReadOnly(t *testing.T) {
	f := &recoveryHTTPFake{checks: []core.BuilderSessionCheck{{RecoveryID: "request", Checkpoint: "FINISHED", Stage: "RESTORE", Outcome: "FAILED", ReasonCode: "LOGIN_REQUIRED", BindingSHA256: strings.Repeat("b", 64)}}}
	server := clearDevHTTPServer(t, f)
	body, status, _ := doRequest(t, server, http.MethodGet, "/api/v1/cleardev/requirements/req/recoveries", "")
	if status != 200 || !strings.Contains(string(body), `"builderSessionChecks"`) || !strings.Contains(string(body), `"LOGIN_REQUIRED"`) || strings.Contains(string(body), strings.Repeat("b", 64)) || f.calls != 0 {
		t.Fatal("checkpoint missing, private binding exposed or read invoked recovery")
	}
	payload := `{"requestId":"request","executionRunId":"run","action":"RETRY_BUILDER_SESSION","targetId":"dispatch:resume:time","supplement":""}`
	_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", payload)
	if status != 200 || f.calls != 1 || f.input.Supplement != "" {
		t.Fatal("HTTP transport required a technical repair claim instead of letting the service check")
	}
	for _, field := range []string{`"builderSessionChecks":[{"outcome":"READY"}]`, `"checkpoint":"FINISHED"`, `"providerConversationId":"replacement"`, `"approval":"APPROVE"`} {
		_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", strings.TrimSuffix(payload, "}")+","+field+"}")
		if status != 400 || f.calls != 1 {
			t.Fatal("client injected recheck evidence or approval")
		}
	}
}

func TestPlanningStepContinuationHTTPUsesExistingNarrowRequest(t *testing.T) {
	f := &recoveryHTTPFake{}
	server := clearDevHTTPServer(t, f)
	payload := `{"requestId":"planning-retry","executionRunId":"","action":"RETRY_PLANNING_STEP","targetId":"step:retry:exact-source","supplement":""}`
	_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", payload)
	if status != 200 || f.calls != 1 || f.input.ExecutionRunID != "" || f.input.Supplement != "" || f.input.Action != core.RecoveryRetryPlanningStep {
		t.Fatal("pre-execution recovery was forced into an execution or user-message request")
	}
	for _, field := range []string{`"prompt":"replace the original request"`, `"attemptNumber":3`, `"approval":"APPROVE"`, `"providerConversationId":"other-session"`} {
		_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", strings.TrimSuffix(payload, "}")+","+field+"}")
		if status != 400 || f.calls != 1 {
			t.Fatalf("client-controlled continuation binding accepted: %s", field)
		}
	}
	f.err = apierr.Conflict("MESSAGE_BUDGET_EXHAUSTED", "original step has no remaining attempt", nil)
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", payload)
	if status != 409 || !strings.Contains(string(body), "MESSAGE_BUDGET_EXHAUSTED") {
		t.Fatalf("budget refusal lost its concrete reason: %d %s", status, body)
	}
}

func TestExtraPlanningAttemptHTTPKeepsFiveFieldIntent(t *testing.T) {
	for _, action := range []string{core.RecoveryRequestExtraPlanningAttempt, core.RecoveryContinueExtraPlanningAttempt} {
		t.Run(action, func(t *testing.T) {
			f := &recoveryHTTPFake{}
			server := clearDevHTTPServer(t, f)
			payload := `{"requestId":"extra-one","executionRunId":"","action":"` + action + `","targetId":"step:extra:proof","supplement":""}`
			_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", payload)
			if status != 200 || f.calls != 1 || f.input.Action != action || f.input.ExecutionRunID != "" || f.input.Supplement != "" {
				t.Fatal("narrow extra planning intent not delivered")
			}
			for _, field := range []string{`"decision":"APPROVE"`, `"nonce":"client"`, `"attemptNumber":3`, `"extraAttempts":99`, `"prompt":"replace original"`, `"providerConversationId":"new"`, `"binding":{}`, `"budgetVersion":"MESSAGE_BUDGET_V1"`} {
				_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", strings.TrimSuffix(payload, "}")+","+field+"}")
				if status != 400 || f.calls != 1 {
					t.Fatal("client injected authority", field)
				}
			}
			f.err = apierr.Conflict("EXTRA_ATTEMPT_DECISION_PENDING", "pending native authority", nil)
			body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", payload)
			if status != 409 || !strings.Contains(string(body), "EXTRA_ATTEMPT_DECISION_PENDING") {
				t.Fatal("native pending reason lost")
			}
		})
	}
}

func TestCoordinationRecoveryHTTPPreservesNarrowIntent(t *testing.T) {
	f := &recoveryHTTPFake{}
	server := clearDevHTTPServer(t, f)
	payload := `{"requestId":"original-retry","executionRunId":"run","action":"RETRY_PLANNER_COORDINATION","targetId":"step:retry:exact-proof","supplement":"native tool permissions repaired"}`
	_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", payload)
	if status != 200 || f.calls != 1 || f.input.Action != core.RecoveryRetryPlannerCoordination || f.input.TargetID != "step:retry:exact-proof" {
		t.Fatal("bounded coordination intent not delivered")
	}
	for _, field := range []string{`"approval":"APPROVE"`, `"attemptNumber":3`, `"extraTurns":1`, `"prompt":"replacement"`, `"binding":{}`, `"failureEventId":"forged"`, `"providerConversationId":"new-session"`} {
		_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", strings.TrimSuffix(payload, "}")+","+field+"}")
		if status != 400 || f.calls != 1 {
			t.Fatal("recovery accepted client authority", field)
		}
	}
	f.err = apierr.Conflict("RESULT_NOT_SETTLED", "source is not safely terminal", nil)
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", payload)
	if status != 409 || !strings.Contains(string(body), "RESULT_NOT_SETTLED") {
		t.Fatal("source refusal hidden", status, string(body))
	}
}

func TestUnifiedFailureHTTPProjectionNeverAcceptsRoutingAuthority(t *testing.T) {
	f := &recoveryHTTPFake{handling: []core.WorkflowFailureHandling{{ID: "failure:1", SourceKind: "CANDIDATE_HANDOFF", SourceID: "dispatch", SourceRole: "BUILDER", Owner: "HUMAN", Action: "ASK_HUMAN", Reason: "PATH_SCOPE", Status: "NEEDS_HUMAN"}}}
	server := clearDevHTTPServer(t, f)
	body, status, _ := doRequest(t, server, http.MethodGet, "/api/v1/cleardev/requirements/req/recoveries", "")
	if status != 200 || !strings.Contains(string(body), `"failureHandling"`) || !strings.Contains(string(body), `"owner":"HUMAN"`) || f.calls != 0 {
		t.Fatal("failure ownership missing or read caused an action")
	}
	base := `{"requestId":"request","executionRunId":"run","action":"CONTINUE_BUILDER","targetId":"dispatch","supplement":"existing authority only"}`
	for _, field := range []string{`"failureHandling":[{"owner":"BUILDER","action":"REPAIR_ORIGINAL"}]`, `"owner":"BUILDER"`, `"beforePublish":true`, `"problemCode":"HANDOFF_CONTENT"`, `"bindingSha256":"changed"`, `"workingTreeSha256":"changed"`, `"approval":"APPROVE"`, `"automatic":true`} {
		_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/recoveries", strings.TrimSuffix(base, "}")+","+field+"}")
		if status != 400 || f.calls != 0 {
			t.Fatal("display/source/authority fields became recovery permission", field)
		}
	}
}
