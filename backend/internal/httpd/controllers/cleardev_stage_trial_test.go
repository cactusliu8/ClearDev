package controllers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type stageTrialHTTPDouble struct {
	fakeClearDevService
	calls int
}

func (s *stageTrialHTTPDouble) StageReviewTrial(_ context.Context, id, session, capability, action string) (cleardevsvc.StageReviewTrialView, error) {
	s.calls++
	if id != "req" || session != "reviewer" || action != "start" {
		return cleardevsvc.StageReviewTrialView{}, apierr.Invalid("BAD_BINDING", "wrong binding", nil)
	}
	if capability != "token" {
		return cleardevsvc.StageReviewTrialView{}, apierr.Forbidden("STAGE_TRIAL_OWNER_REQUIRED", "owner required")
	}
	return cleardevsvc.StageReviewTrialView{CandidateSHA: "exact-candidate"}, nil
}

func TestStageTrialHTTPRejectsOverridesAndRequiresCapability(t *testing.T) {
	s := &stageTrialHTTPDouble{}
	server := clearDevHTTPServer(t, s)
	for _, body := range []string{`{"sessionId":"reviewer","action":"start","candidateSha":"override"}`, `{"sessionId":"reviewer","action":"start","argv":["npm","start"]}`, `{"sessionId":"reviewer","action":"start","workspace":"/tmp"}`} {
		_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/stage-trial", body)
		if status != 400 || s.calls != 0 {
			t.Fatal("caller override reached service")
		}
	}
	_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/requirements/req/stage-trial", `{"sessionId":"reviewer","action":"start"}`)
	if status != 403 {
		t.Fatalf("missing capability: %d", status)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/cleardev/requirements/req/stage-trial", strings.NewReader(`{"sessionId":"reviewer","action":"start"}`))
	req.Header.Set("X-AO-Browser-Capability", "token")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || s.calls != 2 {
		t.Fatalf("trial HTTP status %d calls %d", response.StatusCode, s.calls)
	}
}
