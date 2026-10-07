package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClearDevStageTrialUsesOwningCapabilityAndThinHTTP(t *testing.T) {
	for _, action := range []string{"start", "status", "stop", "run:default-input"} {
		t.Run(action, func(t *testing.T) {
			cfg := setConfigEnv(t)
			t.Setenv("AO_SESSION_ID", "review-session")
			t.Setenv("AO_BROWSER_CAPABILITY", "test-capability")
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/v1/cleardev/requirements/req%2Fone/stage-trial" || r.Header.Get(browserCapabilityHeader) != "test-capability" || len(body) != 2 || body["sessionId"] != "review-session" || body["action"] != action {
					t.Error("wrong trial request")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"candidateSha":"bound-candidate","preview":{"state":"ready"}}`))
			}))
			defer srv.Close()
			writeRunFileFor(t, cfg, srv)
			args := []string{"cleardev", "stage-trial", action, "req/one"}
			if strings.HasPrefix(action, "run:") {
				args = []string{"cleardev", "stage-trial", "run", "req/one", "default-input"}
			}
			out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, args...)
			if err != nil || calls != 1 || !strings.Contains(out, "bound-candidate") || strings.Contains(out, "test-capability") {
				t.Fatalf("trial CLI: %s %v calls=%d", out, err, calls)
			}
		})
	}
}

func TestClearDevStageTrialRejectsMissingOwnerAndBackendFailure(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusConflict, `{"error":{"code":"STAGE_TRIAL_BINDING_INVALID","message":"current review required"}}`)
	writeRunFileFor(t, cfg, srv)
	t.Setenv("AO_SESSION_ID", "")
	t.Setenv("AO_BROWSER_CAPABILITY", "")
	if _, _, err := executeCLI(t, Deps{}, "cleardev", "stage-trial", "start", "req"); err == nil {
		t.Fatal("outside session accepted")
	}
	_, _, _, calls := capture.snapshot()
	if calls != 0 {
		t.Fatal("unauthorized CLI sent request")
	}
	t.Setenv("AO_SESSION_ID", "review-session")
	t.Setenv("AO_BROWSER_CAPABILITY", "token")
	if _, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "stage-trial", "start", "req"); err == nil {
		t.Fatal("backend failure hidden")
	}
}
