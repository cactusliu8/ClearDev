//go:build linux

package controllers_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
	svc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type nativeReopenStore struct{ *sqlite.Store }

func (s nativeReopenStore) IssueClearDevHumanDecisionDispatch(ctx context.Context, c core.IssueHumanDecisionDispatchCommand) (core.HumanDecisionOffer, error) {
	c.ExpiresAt = c.IssuedAt.Add(6 * time.Second)
	return s.Store.IssueClearDevHumanDecisionDispatch(ctx, c)
}

type nativeReopenHandler struct {
	*svc.Service
	offers chan core.HumanDecisionOffer
}

func (s nativeReopenHandler) IssueHumanDecisionOffer(ctx context.Context, id string) (core.HumanDecisionOffer, bool, error) {
	offer, ok, err := s.Service.IssueHumanDecisionOffer(ctx, id)
	if ok {
		s.offers <- offer
	}
	return offer, ok, err
}

// Real SQLite/HTTP/React component/private transport/reader/native dialog. No provider
// or business execution is needed for this display recovery; no Approve input is sent.
func TestNativeElectronHumanDecisionReopen(t *testing.T) {
	if os.Getenv("CLEARDEV_NATIVE_REOPEN_TEST") != "1" {
		t.Skip("explicit isolated native reopen test required")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	electron := os.Getenv("CLEARDEV_NATIVE_ELECTRON")
	evidence := os.Getenv("CLEARDEV_NATIVE_REOPEN_ROOT")
	if !filepath.IsAbs(electron) || !filepath.IsAbs(evidence) {
		t.Fatal("absolute isolated evidence and trusted Electron paths required")
	}
	t.Logf("native isolated evidence=%s", evidence)
	if err := os.MkdirAll(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	bundler := filepath.Join(root, "frontend/node_modules/rolldown/bin/cli.mjs")
	mainBundle := filepath.Join(evidence, "main.cjs")
	rendererBundle := filepath.Join(evidence, "renderer.js")
	configPath := filepath.Join(evidence, "renderer.config.mjs")
	configBytes, err := json.Marshal(map[string]any{"input": filepath.Join(root, "frontend/test-native/human-reopen-renderer.tsx"), "platform": "browser", "define": map[string]string{"process.env.NODE_ENV": "\"production\""}, "output": map[string]string{"file": rendererBundle, "format": "es", "banner": "import.meta.env = {DEV:false};"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append([]byte("export default "), configBytes...), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, build := range [][]string{{bundler, filepath.Join(root, "frontend/test-native/human-reopen-main.ts"), "--platform", "node", "--format", "cjs", "--external", "electron", "--file", mainBundle}, {bundler, "--config", configPath}} {
		cmd := exec.Command("node", build...)
		cmd.Dir = filepath.Join(root, "frontend")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native bundle %v %s", err, output)
		}
	}

	source := exec.Command("git", "rev-parse", "HEAD")
	source.Dir = root
	sha, err := source.Output()
	if err != nil {
		t.Fatal(err)
	}
	statusCommand := exec.Command("git", "status", "--porcelain")
	statusCommand.Dir = root
	dirty, err := statusCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"sourceCommit": strings.TrimSpace(string(sha)), "sourceDirty": len(dirty) > 0, "realElectron": true, "productionComponent": true, "realHTTP": true, "realPrivateChannel": true, "realSQLite": true, "realProvider": false, "approvals": 0, "status": "RUNNING"}
	saveManifest := func() {
		raw, _ := json.MarshalIndent(manifest, "", "  ")
		if err := os.WriteFile(filepath.Join(evidence, "evidence.json"), raw, 0o600); err != nil {
			t.Error(err)
		}
	}
	saveManifest()
	defer func() {
		if t.Failed() {
			manifest["status"] = "FAILED"
		}
		saveManifest()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := sqlitetest.MustOpenAt(t, filepath.Join(evidence, "data"))
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "native-reopen", Path: filepath.Join(evidence, "project"), Kind: domain.ProjectKindSingleRepo, RegisteredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	seedService := svc.New(svc.Deps{Facts: store, HumanDecisions: store, AO: store})
	requirement, err := seedService.CreateRequirement(ctx, svc.CreateRequirementInput{AOProjectID: "native-reopen", Name: "Original confirmation", RequirementText: "Keep the original requirement and its acceptance; only reopen the confirmation window."})
	if err != nil {
		t.Fatal(err)
	}
	id := requirement.Requirement.ID
	version := requirement.RequirementVersions[0].ID
	if err := seedService.SubmitRequirementForConfirmation(ctx, version); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	cmd := exec.CommandContext(ctx, electron, mainBundle)
	cmd.Env = append(os.Environ(), "CLEARDEV_NATIVE_REOPEN_TEST=1", "CLEARDEV_NATIVE_REOPEN_ROOT="+evidence)
	cmd.ExtraFiles = []*os.File{writer}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(evidence, "stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	waited := false
	defer func() {
		if !waited {
			cancel()
			_ = cmd.Wait()
		}
	}()
	events := make(chan map[string]any, 64)
	eventFile, err := os.Create(filepath.Join(evidence, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		defer func() { _ = eventFile.Close() }()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			_, _ = eventFile.Write(append(line, '\n'))
			var value map[string]any
			if json.Unmarshal(line, &value) == nil {
				events <- value
			}
		}
	}()
	send := func(action string, extra map[string]any) {
		t.Helper()
		if extra == nil {
			extra = map[string]any{}
		}
		extra["action"] = action
		raw, _ := json.Marshal(extra)
		if _, err := fmt.Fprintln(stdin, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	await := func(name string) map[string]any {
		t.Helper()
		for {
			select {
			case e := <-events:
				if e["event"] == "failure" {
					t.Fatal(e)
				}
				if e["event"] == name {
					return e
				}
			case <-ctx.Done():
				t.Fatalf("native event %s unavailable", name)
			}
		}
	}
	type bootResult struct {
		b   humanauthority.Bootstrap
		err error
	}
	boot := make(chan bootResult, 1)
	go func() { b, e := humanauthority.ReadBootstrap(reader); boot <- bootResult{b, e} }()
	var bootstrap humanauthority.Bootstrap
	select {
	case b := <-boot:
		if b.err != nil {
			t.Fatal("private bootstrap unavailable; secrets withheld", b.err)
		}
		bootstrap = b.b
	case <-ctx.Done():
		t.Fatal("native startup unavailable")
	}
	_ = reader.Close()
	await("ready")
	state := &humanauthority.ConnectionState{}
	service := svc.New(svc.Deps{Facts: store, HumanDecisions: nativeReopenStore{store}, AO: store, DesktopRunID: bootstrap.DesktopRunID, DesktopChannelConnected: state.Connected})
	api := clearDevHTTPServer(t, service)
	shell := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, api.URL+r.URL.RequestURI(), r.Body)
			if err != nil {
				http.Error(w, "fixture proxy", http.StatusInternalServerError)
				return
			}
			proxyReq.Header = r.Header.Clone()
			response, err := http.DefaultClient.Do(proxyReq)
			if err != nil {
				http.Error(w, "fixture proxy", http.StatusInternalServerError)
				return
			}
			defer func() { _ = response.Body.Close() }()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
			return
		}
		if r.URL.Path == "/renderer.js" {
			http.ServeFile(w, r, rendererBundle)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><style>body{font:16px/1.6 system-ui;padding:30px}button{padding:10px;margin:12px}section{max-width:960px}details{margin:12px}</style><div id="root"></div><script type="module" src="/renderer.js"></script>`)
	}))
	defer shell.Close()
	offers := make(chan core.HumanDecisionOffer, 8)
	handler := nativeReopenHandler{service, offers}
	start := func() (context.CancelFunc, <-chan struct{}) {
		child, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		client := humanauthority.NewClient(bootstrap.HumanAuthorityEndpoint, bootstrap.HumanAuthorityToken, bootstrap.DesktopRunID, handler, slog.New(slog.NewTextHandler(io.Discard, nil)), state)
		go func() { defer close(done); client.Run(child) }()
		return stop, done
	}
	stop, done := start()
	defer func() { stop(); <-done }()
	nextOffer := func() core.HumanDecisionOffer {
		t.Helper()
		select {
		case o := <-offers:
			return o
		case <-ctx.Done():
			t.Fatal("private offer unavailable")
			return core.HumanDecisionOffer{}
		}
	}
	original := nextOffer()
	await("reader")
	send("open", map[string]any{"url": shell.URL + "/?id=" + id + "&locale=zh-CN", "locale": "zh-CN"})
	time.Sleep(300 * time.Millisecond)
	send("snapshot", map[string]any{"name": "zh-reader"})
	await("snapshot")
	waitOutcome := func(count int, outcome core.HumanDecisionDispatchOutcome) {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			history, e := store.ListClearDevHumanDecisionDispatchHistory(ctx, original.RequestID)
			if e != nil {
				t.Fatal(e)
			}
			if len(history) == count && history[count-1].Outcome == outcome {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("native outcome timeout")
			case <-time.After(100 * time.Millisecond):
			}
		}
		t.Fatal("native display outcome was not persisted")
	}
	waitOutcome(1, core.HumanDecisionDispatchExpired)
	time.Sleep(2300 * time.Millisecond)
	send("snapshot", map[string]any{"name": "zh-expired"})
	snapshot := await("snapshot")
	if snapshot["visibleWindows"] != float64(1) || !strings.Contains(fmt.Sprint(snapshot["text"]), "重新打开原待确认事项") {
		t.Fatal("expired reader remained or production reopen control missing", snapshot)
	}
	send("reopen", nil)
	if await("clicked")["clicked"] != true {
		t.Fatal("production reopen control unavailable")
	}
	fresh := nextOffer()
	await("reader")
	if fresh.RequestID != original.RequestID || fresh.ContentSHA256 != original.ContentSHA256 || string(fresh.Binding) != string(original.Binding) || fresh.Nonce == original.Nonce {
		t.Fatal("native reopen changed the original binding")
	}
	time.Sleep(250 * time.Millisecond)
	send("continue", nil)
	await("native")
	send("snapshot", map[string]any{"name": "zh-native-dialog"})
	await("snapshot")
	// Capture the real OS native modal on the isolated X display; no Approve key is sent.
	capture := exec.CommandContext(ctx, "xwd", "-silent", "-root", "-out", filepath.Join(evidence, "zh-native-dialog.xwd"))
	if out, e := capture.CombinedOutput(); e != nil {
		t.Fatalf("native capture %v %s", e, out)
	}
	waitOutcome(2, core.HumanDecisionDispatchExpired)
	await("native-closed")
	if err := service.ApplyHumanDecisionResult(ctx, core.HumanDecisionResult{ProtocolVersion: original.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: original.DesktopRunID, RequestID: original.RequestID, DecisionKind: original.DecisionKind, BindingSchemaVersion: original.BindingSchemaVersion, Binding: original.Binding, ContentSHA256: original.ContentSHA256, Nonce: original.Nonce, Decision: core.HumanDecisionApprove}); err == nil {
		t.Fatal("old native approval was accepted")
	}
	send("open", map[string]any{"url": shell.URL + "/?id=" + id + "&locale=en", "locale": "en"})
	time.Sleep(2300 * time.Millisecond)
	send("reopen", nil)
	if await("clicked")["clicked"] != true {
		t.Fatal("English production reopen unavailable")
	}
	nextOffer()
	await("reader")
	time.Sleep(250 * time.Millisecond)
	send("snapshot", map[string]any{"name": "en-reader"})
	await("snapshot")
	stop()
	<-done
	waitOutcome(3, core.HumanDecisionDispatchDisconnected)
	time.Sleep(2300 * time.Millisecond)
	send("snapshot", map[string]any{"name": "en-disconnected"})
	snapshot = await("snapshot")
	if snapshot["visibleWindows"] != float64(1) {
		t.Fatal("disconnected reader remained", snapshot)
	}
	stop, done = start()
	send("reopen", nil)
	if await("clicked")["clicked"] != true {
		t.Fatal("reconnect control unavailable")
	}
	nextOffer()
	await("reader")
	time.Sleep(250 * time.Millisecond)
	send("continue", nil)
	await("native")
	stop()
	<-done
	waitOutcome(4, core.HumanDecisionDispatchDisconnected)
	await("native-closed")
	req, found, err := store.GetClearDevHumanDecisionRequest(ctx, original.RequestID)
	if err != nil || !found || req.Status != core.HumanDecisionRequestPending {
		t.Fatal("native display recovery settled requirement")
	}
	if _, found, err := store.GetClearDevHumanDecisionEffect(ctx, original.RequestID); err != nil || found {
		t.Fatal("native display recovery created approval effect")
	}
	registered, _ := store.ListClearDevHumanDecisionReopens(ctx, original.RequestID)
	if len(registered) != 3 {
		t.Fatal("double clicks duplicated registrations")
	}
	send("quit", nil)
	finished := await("finished")
	if finished["approvals"] != float64(0) || finished["readers"] != float64(4) || finished["nativeDialogs"] != float64(2) {
		t.Fatal("native action counts changed", finished)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		waited = true
		t.Fatal("native test shell failed", err)
	}
	waited = true
	<-scanned
	manifest["status"] = "PASS_NATIVE_EN_ZH_PRIVATE_CHANNEL_SQLITE"
	manifest["readers"] = 4
	manifest["reopenRecords"] = 3
	manifest["cancelledNativeDialogs"] = 2
	t.Logf("PASS real Electron/production React/HTTP/private channel/SQLite, 4 windows, 3 reopen records, 2 cancelled native dialogs, approvals=0; evidence=%s", evidence)
}
