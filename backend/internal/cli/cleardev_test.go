package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type clearDevCapture struct {
	mu     sync.Mutex
	method string
	path   string
	body   []byte
	count  int
}

func clearDevServer(t *testing.T, status int, response string) (*httptest.Server, *clearDevCapture) {
	t.Helper()
	capture := &clearDevCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		capture.mu.Lock()
		capture.method = r.Method
		capture.path = r.URL.EscapedPath()
		capture.body = body
		capture.count++
		capture.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/api/v1/cleardev/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, response)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

func (c *clearDevCapture) snapshot() (method, path string, body []byte, count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.method, c.path, append([]byte(nil), c.body...), c.count
}

func TestClearDevRequirementCreateSuccess(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusCreated, `{"requirement":{"id":"req-1"}}`)
	writeRunFileFor(t, cfg, srv)
	input := `{"aoProjectId":"ao","name":"Demo","requirementText":"requirement text"}`
	file := filepath.Join(t.TempDir(), "requirement.json")
	if err := os.WriteFile(file, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "requirement", "create", "--file", file)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements" || count != 1 {
		t.Fatalf("request = %s %s (count %d), want POST /api/v1/cleardev/requirements once", method, path, count)
	}
	var got, want any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode request: %v; body=%s", err, body)
	}
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request object = %#v, want %#v", got, want)
	}
	if !strings.Contains(out, `"req-1"`) {
		t.Fatalf("formatted response missing id: %s", out)
	}
}

func TestClearDevRequirementShowSuccessEscapesID(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusOK, `{"requirement":{"id":"req/a"},"agentStepAttempts":[{"logicalStepId":"step-1","attemptNumber":1,"failureCategory":"RATE_LIMITED","retryable":true,"rawMessageSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","parseConclusion":"INVALID"}]}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "requirement", "show", "req/a")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	method, path, _, count := capture.snapshot()
	if method != http.MethodGet || path != "/api/v1/cleardev/requirements/req%2Fa" || count != 1 {
		t.Fatalf("request = %s %s (count %d), want escaped GET once", method, path, count)
	}
	if !strings.Contains(out, `"req/a"`) {
		t.Fatalf("formatted response missing id: %s", out)
	}
	for _, want := range []string{`"agentStepAttempts"`, `"step-1"`, `"RATE_LIMITED"`, `"INVALID"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("formatted response missing %s: %s", want, out)
		}
	}
}

func TestClearDevStandardStartSendsEmptyBody(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusAccepted, `{"requirement":{"id":"req-1"}}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "standard", "start", "req/a")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/req%2Fa/standard-runs" || count != 1 {
		t.Fatalf("request = %s %s (count %d), want escaped POST once", method, path, count)
	}
	if len(body) != 0 {
		t.Fatalf("request body = %q, want empty", body)
	}
	if !strings.Contains(out, `"req-1"`) {
		t.Fatalf("formatted response missing id: %s", out)
	}
}

func TestClearDevComplexExecutionStartSendsEmptyBody(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusAccepted, `{"requirement":{"id":"req-1"}}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "execution", "start", "req/a")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/req%2Fa/execution-runs" || count != 1 {
		t.Fatalf("request = %s %s (count %d), want escaped POST once", method, path, count)
	}
	if len(body) != 0 {
		t.Fatalf("request body = %q, want empty", body)
	}
	if !strings.Contains(out, `"req-1"`) {
		t.Fatalf("formatted response missing id: %s", out)
	}
}

func TestClearDevRequirementInputErrorsDoNotCallAPI(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusCreated, `{}`)
	writeRunFileFor(t, cfg, srv)

	invalidFile := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalidFile, []byte(`[1] {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing file flag", args: []string{"cleardev", "requirement", "create"}, want: "--file is required"},
		{name: "invalid or multiple JSON values", args: []string{"cleardev", "requirement", "create", "--file", invalidFile}, want: "JSON object"},
		{name: "missing complex file flag", args: []string{"cleardev", "requirement", "create-complex"}, want: "--file is required"},
		{name: "invalid complex JSON values", args: []string{"cleardev", "requirement", "create-complex", "--file", invalidFile}, want: "JSON object"},
		{name: "missing clarify id", args: []string{"cleardev", "requirement", "clarify"}, want: "exactly 1 argument"},
		{name: "blank clarify id", args: []string{"cleardev", "requirement", "clarify", "   "}, want: "requirement id is required"},
		{name: "missing clarify file", args: []string{"cleardev", "requirement", "clarify", "req-1"}, want: "--file is required"},
		{name: "missing propose-direction id", args: []string{"cleardev", "requirement", "propose-direction"}, want: "exactly 1 argument"},
		{name: "blank propose-direction id", args: []string{"cleardev", "requirement", "propose-direction", "   "}, want: "requirement id is required"},
		{name: "missing propose-direction file", args: []string{"cleardev", "requirement", "propose-direction", "req-1"}, want: "--file is required"},
		{name: "missing show id", args: []string{"cleardev", "requirement", "show"}, want: "exactly 1 argument"},
		{name: "blank show id", args: []string{"cleardev", "requirement", "show", "   "}, want: "requirement id is required"},
		{name: "missing explain id", args: []string{"cleardev", "requirement", "explain"}, want: "exactly 1 argument"},
		{name: "blank explain id", args: []string{"cleardev", "requirement", "explain", "   "}, want: "requirement id is required"},
		{name: "missing progress id", args: []string{"cleardev", "progress"}, want: "exactly 1 argument"},
		{name: "blank progress id", args: []string{"cleardev", "progress", "   "}, want: "project id is required"},
		{name: "missing standard start id", args: []string{"cleardev", "standard", "start"}, want: "exactly 1 argument"},
		{name: "blank standard start id", args: []string{"cleardev", "standard", "start", "   "}, want: "requirement id is required"},
		{name: "missing complex execution start id", args: []string{"cleardev", "execution", "start"}, want: "exactly 1 argument"},
		{name: "blank complex execution start id", args: []string{"cleardev", "execution", "start", "   "}, want: "requirement id is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := executeWithDeps(Deps{
				Out: io.Discard, Err: io.Discard,
				ProcessAlive: func(int) bool { return true },
			}, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
			if got := ExitCode(err); got != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2", err, got)
			}
		})
	}
	_, _, _, count := capture.snapshot()
	if count != 0 {
		t.Fatalf("HTTP request count = %d, want 0", count)
	}
}

func TestClearDevLegacyProjectAliasIsAbsent(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)

	err := executeWithDeps(Deps{
		Out: io.Discard, Err: io.Discard,
		ProcessAlive: func(int) bool { return true },
	}, []string{"cleardev", "project", "show", "legacy"})
	if err == nil {
		t.Fatalf("legacy project alias error = %v", err)
	}
	if got := ExitCode(err); got != 2 {
		t.Fatalf("legacy project alias exit code = %d, want 2", got)
	}
	_, _, _, count := capture.snapshot()
	if count != 0 {
		t.Fatalf("legacy project alias made %d HTTP requests, want 0", count)
	}
}

func TestClearDevHumanDecisionCommandsAreAbsent(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)
	for _, args := range [][]string{
		{"cleardev", "requirement", "confirm", "req-1"},
		{"cleardev", "requirement", "reject", "req-1"},
		{"cleardev", "human-decision", "approve", "decision-1"},
	} {
		err := executeWithDeps(Deps{
			Out: io.Discard, Err: io.Discard,
			ProcessAlive: func(int) bool { return true },
		}, args)
		if err == nil {
			t.Fatalf("%v succeeded", args)
		}
		if got := ExitCode(err); got != 2 {
			t.Fatalf("%v exit code = %d, want 2", args, got)
		}
	}
	_, _, _, count := capture.snapshot()
	if count != 0 {
		t.Fatalf("absent human-decision commands made %d HTTP requests", count)
	}
}

func TestClearDevRequirementCreateBackendErrorPreservesCode(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusBadRequest, `{"error":"bad_request","code":"AO_PROJECT_UNSUPPORTED","message":"workspace is unsupported"}`)
	writeRunFileFor(t, cfg, srv)
	file := filepath.Join(t.TempDir(), "requirement.json")
	if err := os.WriteFile(file, []byte(`{"aoProjectId":"ao","name":"Demo","requirementText":"text"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "requirement", "create", "--file", file)
	if err == nil {
		t.Fatal("expected backend error")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, got)
	}
	if !strings.Contains(err.Error(), "AO_PROJECT_UNSUPPORTED") && !strings.Contains(errOut, "AO_PROJECT_UNSUPPORTED") {
		t.Fatalf("stable backend code missing: err=%v stderr=%s", err, errOut)
	}
	_, _, _, count := capture.snapshot()
	if count != 1 {
		t.Fatalf("ClearDev API request count = %d, want 1", count)
	}
}

func TestClearDevRequirementShowBackendErrorPreservesCode(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusNotFound, `{"error":"not_found","code":"CLEARDEV_REQUIREMENT_NOT_FOUND","message":"requirement was not found"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "requirement", "show", "req-404")
	if err == nil {
		t.Fatal("expected backend error")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, got)
	}
	if !strings.Contains(err.Error(), "CLEARDEV_REQUIREMENT_NOT_FOUND") && !strings.Contains(errOut, "CLEARDEV_REQUIREMENT_NOT_FOUND") {
		t.Fatalf("stable backend code missing: err=%v stderr=%s", err, errOut)
	}
	method, path, _, count := capture.snapshot()
	if method != http.MethodGet || path != "/api/v1/cleardev/requirements/req-404" || count != 1 {
		t.Fatalf("request = %s %s (count %d), want GET /api/v1/cleardev/requirements/req-404 once", method, path, count)
	}
}

func TestClearDevStandardStartBackendErrorPreservesCode(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusConflict, `{"error":"conflict","code":"CLEARDEV_STANDARD_ALREADY_STARTED","message":"already started"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "standard", "start", "req-1")
	if err == nil {
		t.Fatal("expected backend error")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, got)
	}
	if !strings.Contains(err.Error(), "CLEARDEV_STANDARD_ALREADY_STARTED") && !strings.Contains(errOut, "CLEARDEV_STANDARD_ALREADY_STARTED") {
		t.Fatalf("stable backend code missing: err=%v stderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/req-1/standard-runs" || len(body) != 0 || count != 1 {
		t.Fatalf("request = %s %s body=%q count=%d, want empty POST once", method, path, body, count)
	}
}

func TestClearDevCLIComplexParallelExecutionStartSendsEmptyBody(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusAccepted, `{"requirement":{"id":"req-1"}}`)
	writeRunFileFor(t, cfg, srv)

	if _, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "execution", "start", "req-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/req-1/execution-runs" || len(body) != 0 || count != 1 {
		t.Fatalf("request = %s %s body=%q count=%d, want empty POST once", method, path, body, count)
	}
}

func TestClearDevCLIQuickExecutionStartSendsEmptyBody(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusAccepted, `{"requirement":{"id":"req-1"},"quickExecution":{"run":{"mode":"QUICK"}}}`)
	writeRunFileFor(t, cfg, srv)

	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "execution", "start", "req-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"quickExecution"`) || !strings.Contains(out, `"QUICK"`) {
		t.Fatalf("CLI output missing QUICK facts: %s", out)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/req-1/execution-runs" || len(body) != 0 || count != 1 {
		t.Fatalf("request = %s %s body=%q count=%d, want empty POST once", method, path, body, count)
	}
}

func TestClearDevCLIControlledExceptionStartSendsEmptyBody(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusAccepted, `{"requirement":{"id":"req-1"},"complexExecution":{"exception":{"budgets":[{"roleKind":"BUILDER"}]}}}`)
	writeRunFileFor(t, cfg, srv)

	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "execution", "start", "req-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"exception"`) || !strings.Contains(out, `"budgets"`) {
		t.Fatalf("CLI output missing exception facts: %s", out)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/req-1/execution-runs" || len(body) != 0 || count != 1 {
		t.Fatalf("request = %s %s body=%q count=%d, want empty POST once", method, path, body, count)
	}
}

func TestClearDevComplexExecutionStartBackendErrorPreservesCode(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusConflict, `{"error":"conflict","code":"CLEARDEV_COMPLEX_EXECUTION_ALREADY_STARTED","message":"already started"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "execution", "start", "req-1")
	if err == nil {
		t.Fatal("expected backend error")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, got)
	}
	if !strings.Contains(err.Error(), "CLEARDEV_COMPLEX_EXECUTION_ALREADY_STARTED") && !strings.Contains(errOut, "CLEARDEV_COMPLEX_EXECUTION_ALREADY_STARTED") {
		t.Fatalf("stable backend code missing: err=%v stderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/req-1/execution-runs" || len(body) != 0 || count != 1 {
		t.Fatalf("request = %s %s body=%q count=%d, want empty POST once", method, path, body, count)
	}
}

func TestClearDevRequirementCreateComplexSuccess(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusCreated, `{"requirement":{"id":"req-c"}}`)
	writeRunFileFor(t, cfg, srv)
	input := `{"aoProjectId":"ao","name":"Demo","prdText":"original prd"}`
	file := filepath.Join(t.TempDir(), "complex.json")
	if err := os.WriteFile(file, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "requirement", "create-complex", "--file", file)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/complex" || count != 1 {
		t.Fatalf("request = %s %s (count %d), want POST /api/v1/cleardev/requirements/complex once", method, path, count)
	}
	var got, want any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode request: %v; body=%s", err, body)
	}
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request object = %#v, want %#v", got, want)
	}
	if !strings.Contains(out, `"req-c"`) {
		t.Fatalf("formatted response missing id: %s", out)
	}
}

func TestClearDevRequirementClarifySuccessEscapesID(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusOK, `{"requirement":{"id":"req/a"}}`)
	writeRunFileFor(t, cfg, srv)
	input := `{"compilationRequestId":"comp-1","clarificationRound":0,"answers":[{"questionKey":"invalid-address","text":"计入拒绝"}]}`
	file := filepath.Join(t.TempDir(), "answers.json")
	if err := os.WriteFile(file, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "requirement", "clarify", "req/a", "--file", file)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/req%2Fa/complex-clarifications" || count != 1 {
		t.Fatalf("request = %s %s (count %d), want escaped POST once", method, path, count)
	}
	var got, want any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode request: %v; body=%s", err, body)
	}
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request object = %#v, want %#v", got, want)
	}
	if !strings.Contains(out, `"req/a"`) {
		t.Fatalf("formatted response missing id: %s", out)
	}
}

func TestClearDevRequirementProposeDirectionChangeSuccessEscapesID(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusAccepted, `{"requirement":{"id":"req/a"}}`)
	writeRunFileFor(t, cfg, srv)
	input := `{"requestId":"dir-1","developmentRequirementId":"req/a","message":"只接受 example.com 域名"}`
	file := filepath.Join(t.TempDir(), "direction.json")
	if err := os.WriteFile(file, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "requirement", "propose-direction", "req/a", "--file", file)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/req%2Fa/direction-intents" || count != 1 {
		t.Fatalf("request = %s %s (count %d), want escaped POST once", method, path, count)
	}
	var got, want any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode request: %v; body=%s", err, body)
	}
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request object = %#v, want %#v", got, want)
	}
	if !strings.Contains(out, `"req/a"`) {
		t.Fatalf("formatted response missing id: %s", out)
	}
}

func TestClearDevProgressShowEscapesProjectID(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusOK, `{"aoProjectId":"ao/a","requirements":[]}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "progress", "ao/a")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	method, path, _, count := capture.snapshot()
	if method != http.MethodGet || path != "/api/v1/cleardev/projects/ao%2Fa/progress" || count != 1 {
		t.Fatalf("request = %s %s (count %d), want escaped GET once", method, path, count)
	}
	if !strings.Contains(out, `"ao/a"`) {
		t.Fatalf("formatted response missing project id: %s", out)
	}
}

func TestClearDevRequirementExplainSendsEmptyBody(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, http.StatusAccepted, `{"requirement":{"id":"req/a"}}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "requirement", "explain", "req/a")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/cleardev/requirements/req%2Fa/progress-explanations" || count != 1 {
		t.Fatalf("request = %s %s (count %d), want escaped POST once", method, path, count)
	}
	if len(body) != 0 {
		t.Fatalf("request body = %q, want empty", body)
	}
	if !strings.Contains(out, `"req/a"`) {
		t.Fatalf("formatted response missing id: %s", out)
	}
}
