package httpd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type controlledSessionLookupStub struct {
	controlled bool
	err        error
}

func (s controlledSessionLookupStub) IsClearDevControlledSession(context.Context, string) (bool, error) {
	return s.controlled, s.err
}

func TestControlledSessionWriteGuard(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		lookup             controlledSessionLookupStub
		want               int
	}{
		{"controlled chat", "POST", "/api/v1/sessions/worker/conversation/messages", controlledSessionLookupStub{controlled: true}, http.StatusForbidden},
		{"controlled settings", "PATCH", "/api/v1/sessions/worker/conversation/settings", controlledSessionLookupStub{controlled: true}, http.StatusForbidden},
		{"controlled lifecycle", "POST", "/api/v1/sessions/worker/kill", controlledSessionLookupStub{controlled: true}, http.StatusForbidden},
		{"controlled read", "GET", "/api/v1/sessions/worker/conversation", controlledSessionLookupStub{controlled: true}, http.StatusNoContent},
		{"trusted activity path", "POST", "/api/v1/sessions/worker/activity", controlledSessionLookupStub{controlled: true}, http.StatusNoContent},
		{"authorized engine switch", "POST", "/api/v1/sessions/worker/switch-agent", controlledSessionLookupStub{controlled: true}, http.StatusNoContent},
		{"ordinary AO session", "POST", "/api/v1/sessions/ordinary/conversation/messages", controlledSessionLookupStub{}, http.StatusNoContent},
		{"lookup failure", "POST", "/api/v1/sessions/worker/send", controlledSessionLookupStub{err: errors.New("database unavailable")}, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hit := false
			handler := controlledSessionWriteGuard(tc.lookup)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hit = true
				w.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.want || (hit != (tc.want == http.StatusNoContent)) {
				t.Fatalf("status=%d next=%v, want status=%d", response.Code, hit, tc.want)
			}
		})
	}
}
