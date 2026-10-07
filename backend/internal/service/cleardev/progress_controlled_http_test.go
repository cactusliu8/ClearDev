package cleardev_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cli"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

// Real router and thin CLI consume the same service at a paused workflow checkpoint.
func controlledProgressHTTPCLI(t *testing.T, svc *cleardevsvc.Service, id, project, hash string) {
	t.Helper()
	server := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{ClearDev: svc}, httpd.ControlDeps{}))
	defer server.Close()
	uri, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(uri.Port())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.json")
	t.Setenv("AO_RUN_FILE", path)
	t.Setenv("AO_DATA_DIR", t.TempDir())
	if err = runfile.Write(path, runfile.Info{PID: os.Getpid(), Port: port, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path    string
		args    []string
		project bool
	}{
		{"/api/v1/cleardev/requirements/" + id, []string{"cleardev", "requirement", "show", id}, false},
		{"/api/v1/cleardev/projects/" + project + "/progress", []string{"cleardev", "progress", project}, true},
	} {
		resp, err := http.Get(server.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		var direct map[string]any
		err = json.NewDecoder(resp.Body).Decode(&direct)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("HTTP %d: %v %v", resp.StatusCode, err, direct)
		}
		var out bytes.Buffer
		cmd := cli.NewRootCommand(cli.Deps{Out: &out, Err: io.Discard, ProcessAlive: func(int) bool { return true }})
		cmd.SetArgs(c.args)
		if err = cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err = json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		summary := func(v map[string]any) map[string]any {
			if c.project {
				return v["requirements"].([]any)[0].(map[string]any)
			}
			return v["trustedProgress"].(map[string]any)
		}
		for _, v := range []map[string]any{summary(direct), summary(got)} {
			if v["factSummarySha256"] != hash {
				t.Fatal("HTTP/CLI hash differs")
			}
			if _, ok := v["controlledWork"]; !ok {
				t.Fatal("controlledWork missing")
			}
		}
	}
}
