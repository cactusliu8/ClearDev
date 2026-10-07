package controllers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	svc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type decisionReopenHTTPFake struct {
	fakeClearDevService
	calls int
	reads int
	input svc.ReopenHumanDecisionInput
	err   error
}

func (f *decisionReopenHTTPFake) GetHumanDecisionDisplays(context.Context, string) (svc.HumanDecisionDisplaysView, error) {
	f.reads++
	return svc.HumanDecisionDisplaysView{Items: []svc.HumanDecisionDisplayItem{}}, f.err
}
func (f *decisionReopenHTTPFake) ReopenHumanDecision(_ context.Context, _ string, in svc.ReopenHumanDecisionInput) (svc.HumanDecisionDisplaysView, error) {
	f.calls++
	f.input = in
	return svc.HumanDecisionDisplaysView{Items: []svc.HumanDecisionDisplayItem{}}, f.err
}
func TestDecisionReopenHTTPOnlyAcceptsDisplayIntent(t *testing.T) {
	f := &decisionReopenHTTPFake{}
	server := clearDevHTTPServer(t, f)
	path := "/api/v1/cleardev/requirements/req/decision-displays"
	body := `{"requestId":"r","decisionRequestId":"d","contentSha256":"` + strings.Repeat("a", 64) + `","previousDispatchId":"old"}`
	for _, field := range []string{`"approve":true`, `"decision":"APPROVE"`, `"nonce":"secret"`, `"token":"secret"`, `"display":{}`, `"desktopRunId":"spoof"`, `"budget":99`} {
		_, status, _ := doRequest(t, server, http.MethodPost, path, strings.TrimSuffix(body, "}")+","+field+"}")
		if status != 400 || f.calls != 0 {
			t.Fatal("decision capability reached display intent handler")
		}
	}
	_, status, _ := doRequest(t, server, http.MethodPost, path, body+` {}`)
	if status != 400 || f.calls != 0 {
		t.Fatal("trailing input accepted")
	}
	_, status, _ = doRequest(t, server, http.MethodGet, path, "")
	if status != 200 || f.calls != 0 || f.reads != 1 {
		t.Fatal("read invoked display action")
	}
	raw, status, _ := doRequest(t, server, http.MethodPost, path, body)
	if status != 200 || f.calls != 1 || f.input.PreviousDispatchID != "old" {
		t.Fatal("display binding lost")
	}
	if strings.Contains(string(raw), "nonce") || strings.Contains(string(raw), "desktopRunId") {
		t.Fatal("private capability in public response")
	}
	f.err = apierr.Conflict("DESKTOP_UNAVAILABLE", "no desktop", nil)
	_, status, _ = doRequest(t, server, http.MethodPost, path, body)
	if status != 409 {
		t.Fatal("missing desktop was not a conflict")
	}
}
