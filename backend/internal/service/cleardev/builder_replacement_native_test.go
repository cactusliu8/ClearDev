//go:build linux

package cleardev

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// The shortened expiry exists only in this test wrapper. Secrets and offers
// stay in memory; the renderer receives only public recovery/display facts.
type builderHandoffNativeStore struct{ *sqlitestore.Store }

func (s builderHandoffNativeStore) IssueClearDevHumanDecisionDispatch(ctx context.Context, input core.IssueHumanDecisionDispatchCommand) (core.HumanDecisionOffer, error) {
	input.ExpiresAt = input.IssuedAt.Add(12 * time.Second)
	return s.Store.IssueClearDevHumanDecisionDispatch(ctx, input)
}

type builderHandoffNativeHandler struct {
	*Service
	mu     *sync.Mutex
	offers chan core.HumanDecisionOffer
}

func (s builderHandoffNativeHandler) IssueHumanDecisionOffer(ctx context.Context, desktop string) (core.HumanDecisionOffer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	offer, found, err := s.Service.IssueHumanDecisionOffer(ctx, desktop)
	if found {
		s.offers <- offer
	}
	return offer, found, err
}

func (s builderHandoffNativeHandler) ApplyHumanDecisionResult(ctx context.Context, result core.HumanDecisionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Service.ApplyHumanDecisionResult(ctx, result)
}

// The isolated fixture defers the production scheduler. Drive its unchanged
// control-plane transitions only after the actual UI continuation succeeded.
// A durable progress/stop receipt can need another transition; an unchanged
// stop or a missing candidate/check/review boundary remains a real failure.
func advanceBuilderHandoffNativeCandidate(ctx context.Context, service *Service, id string) (int, error) {
	for transitions := 0; transitions < 40; transitions++ {
		execution, found, err := service.complexExecution.GetClearDevComplexExecution(ctx, id)
		if err != nil {
			return transitions, err
		}
		if !found {
			return transitions, errors.New("isolated handoff execution unavailable")
		}
		if len(execution.Dispatches) > 0 && execution.Dispatches[0].CandidateCommitID != "" && len(execution.CheckRuns) > 0 && len(execution.Reviews) > 0 {
			return transitions, nil
		}
		changed, stop, err := service.advanceComplexStandardExecution(ctx, id)
		if err != nil {
			return transitions + 1, err
		}
		if stop && !changed {
			phase, reason := core.DeriveComplexExecutionPhase(execution)
			return transitions + 1, fmt.Errorf("normal handoff candidate/check/review stopped: phase=%s reason=%s", phase, reason)
		}
	}
	return 40, errors.New("normal handoff candidate/check/review exceeded its bounded transitions")
}

func TestBuilderHandoffNativeInitialProductionProjection(t *testing.T) {
	f, _, id := newBuilderReplacementFixture(t)
	f.s.desktopRunID = "explicit-native-handoff-projection-desktop"
	f.s.humanDecisions = builderHandoffNativeStore{f.store}
	f.s.desktopChannelConnected = func() bool { return true }
	assertAvailable := func(action string) {
		t.Helper()
		view, err := f.s.GetWorkflowRecovery(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if view.Diagnosis == nil || !view.Diagnosis.Current || view.Diagnosis.ReadError != "" {
			t.Fatalf("native production UI is not current for %s: diagnosis=%+v", action, view.Diagnosis)
		}
		available := false
		for _, option := range view.Options {
			available = available || option.Action == action && option.UnavailableReason == ""
		}
		if view.ExecutionRunID == "" || !available {
			t.Fatalf("native production UI has no %s: run=%s options=%+v", action, view.ExecutionRunID, view.Options)
		}
	}
	assertAvailable(core.RecoveryRequestBuilderReplacement)
	input := builderReplacementInput(t, f, id, "native-projection-request", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), id, input); err != nil {
		t.Fatal(err)
	}
	approveBuilderReplacementStorage(t, f) // Explicit isolated synthetic result.
	assertAvailable(core.RecoveryContinueBuilderReplacement)
}

// TestNativeElectronBuilderReplacementHandoff is opt-in and uses a test-only HTTP
// bridge into the real service, not the production controller/router. Git
// transfer and SQLite are real; provider/process/native loss, preflight/checks
// and final APPROVE are explicit doubles. Production React,
// the private Client/Host, trusted reader and native modal run unchanged.
func TestNativeElectronBuilderReplacementHandoff(t *testing.T) {
	if os.Getenv("AO_BUILDER_HANDOFF_NATIVE") != "1" {
		t.Skip("explicit isolated native Builder handoff check required")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	electron, evidence := os.Getenv("AO_BUILDER_HANDOFF_NATIVE_ELECTRON"), os.Getenv("AO_BUILDER_HANDOFF_NATIVE_ROOT")
	if !filepath.IsAbs(electron) || !filepath.IsAbs(evidence) || !strings.HasPrefix(filepath.Base(evidence), "builder-handoff-") {
		t.Fatal("existing trusted Electron and isolated Builder handoff evidence paths required")
	}
	if err := os.MkdirAll(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated Builder handoff evidence=%s", evidence)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	bundler := filepath.Join(root, "frontend/node_modules/rolldown/bin/cli.mjs")
	mainBundle, rendererBundle := filepath.Join(evidence, "main.cjs"), filepath.Join(evidence, "renderer.js")
	configPath := filepath.Join(evidence, "renderer.config.mjs")
	config, err := json.Marshal(map[string]any{"input": filepath.Join(root, "frontend/test-native/builder-handoff-renderer.tsx"), "platform": "browser", "transform": map[string]any{"define": map[string]string{"process.env.NODE_ENV": "\"production\""}}, "output": map[string]string{"file": rendererBundle, "format": "es", "banner": "import.meta.env = {DEV:false};"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append([]byte("export default "), config...), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{bundler, filepath.Join(root, "frontend/test-native/builder-handoff-main.ts"), "--platform", "node", "--format", "cjs", "--external", "electron", "--file", mainBundle}, {bundler, "--config", configPath}} {
		command := exec.CommandContext(ctx, "node", args...)
		command.Dir = filepath.Join(root, "frontend")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated bundle failed: %v %s", err, output)
		}
	}
	gitOutput := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = root
		output, err := command.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(output))
	}
	cases := []map[string]any{}
	syntheticApprovals := 0
	var manifestMu sync.Mutex
	// This harness belongs to this batch's frozen v3 plan, independently of a
	// later code candidate or development-record commit. A dirty run is a probe.
	const planCommit = "c4065ec9eb2a3443a85ea70bbcb66aa098eb58c5"
	sourceDirty := gitOutput("status", "--porcelain") != ""
	manifest := map[string]any{"sourceCommit": gitOutput("rev-parse", "HEAD"), "sourceDirty": sourceDirty, "planCommit": planCommit, "planVersion": 3, "diagnosticProbe": sourceDirty, "realElectron": false, "productionComponents": false, "realPrivateChannel": false, "realSQLite": false, "realHTTP": false, "productionController": false, "strictTestServiceBridge": true, "fakeProvider": true, "fakeGit": false, "realGit": false, "fakeProcessLaunch": true, "fakeNativeLoss": true, "fakeChecks": true, "backgroundDeferred": true, "approvalCountersScope": "AUTHORIZE_BUILDER_REPLACEMENT only; fixture specification confirmation is synthetic and excluded", "fakePreflight": true, "syntheticHumanApprovals": 0, "realNativeApprovals": 0, "cases": cases, "status": "RUNNING"}
	saveLocked := func() {
		raw, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile(filepath.Join(evidence, "evidence.json"), raw, 0o600); err != nil {
			t.Error(err)
		}
	}
	updateManifest := func(fields map[string]any) {
		manifestMu.Lock()
		defer manifestMu.Unlock()
		for key, value := range fields {
			manifest[key] = value
		}
		saveLocked()
	}
	updateManifest(nil)
	defer func() {
		manifestMu.Lock()
		defer manifestMu.Unlock()
		if t.Failed() {
			manifest["status"] = "FAILED"
		}
		saveLocked()
	}()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	command := exec.CommandContext(ctx, electron, mainBundle)
	command.Env = append(os.Environ(), "AO_BUILDER_HANDOFF_NATIVE=1", "AO_BUILDER_HANDOFF_NATIVE_ROOT="+evidence)
	command.ExtraFiles = []*os.File{writer}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(evidence, "stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	waited := false
	defer func() {
		if !waited {
			cancel()
			_ = command.Wait()
		}
	}()
	events := make(chan map[string]any, 128)
	eventFile, err := os.Create(filepath.Join(evidence, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		defer close(events)
		defer func() { _ = eventFile.Close() }()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 65536), 1048576)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			_, _ = eventFile.Write(append(line, '\n'))
			var event map[string]any
			if json.Unmarshal(line, &event) == nil {
				counts := map[string]any{}
				for source, target := range map[string]string{"readers": "nativeReaders", "nativeDialogs": "nativeDialogs", "realApprovals": "realNativeApprovals"} {
					if value, ok := event[source]; ok {
						counts[target] = value
					}
				}
				if len(counts) > 0 {
					updateManifest(counts)
				}
				events <- event
			}
		}
	}()
	send := func(action string, fields map[string]any) {
		t.Helper()
		if fields == nil {
			fields = map[string]any{}
		}
		fields["action"] = action
		raw, _ := json.Marshal(fields)
		if _, err := fmt.Fprintln(stdin, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	await := func(name string) map[string]any {
		t.Helper()
		deadline := time.NewTimer(25 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case event, open := <-events:
				if !open {
					t.Fatalf("native event %s unavailable: isolated shell exited", name)
				}
				if event["event"] == "failure" {
					t.Fatal("isolated native shell failed; inspect its bounded event record")
				}
				if event["event"] == "reader-unavailable" {
					t.Fatalf("trusted reader unavailable: destroyed=%v visible=%v loading=%v", event["destroyed"], event["visible"], event["loading"])
				}
				if event["event"] == name {
					return event
				}
			case <-ctx.Done():
				t.Fatalf("native event %s unavailable", name)
			case <-deadline.C:
				t.Fatalf("native event %s unavailable within its deadline", name)
			}
		}
	}
	bootstrap, err := humanauthority.ReadBootstrap(reader)
	if err != nil {
		t.Fatal("private bootstrap unavailable; secrets withheld")
	}
	_ = reader.Close()
	await("ready")
	updateManifest(map[string]any{"realElectron": true})
	click := func(label string) {
		t.Helper()
		send("click", map[string]any{"label": label})
		if await("clicked")["clicked"] != true {
			t.Fatalf("production button unavailable: %s", label)
		}
	}
	snapshot := func(name string, minimumWindows int) map[string]any {
		t.Helper()
		send("snapshot", map[string]any{"name": name})
		observed := await("snapshot")
		visible, _ := observed["visibleWindows"].(float64)
		if visible < float64(minimumWindows) {
			t.Fatalf("snapshot %s observed %d visible windows; need at least %d", name, int(visible), minimumWindows)
		}
		return observed
	}
	for _, locale := range []string{"en", "zh-CN"} {
		name := "builder-" + strings.ToLower(locale)
		f, h, id := newBuilderReplacementFixture(t)
		f.s.now = func() time.Time { return time.Now().UTC() }
		f.s.desktopRunID = bootstrap.DesktopRunID
		f.s.humanDecisions = builderHandoffNativeStore{f.store}
		connection := &humanauthority.ConnectionState{}
		f.s.desktopChannelConnected = connection.Connected
		updateManifest(map[string]any{"realSQLite": true})
		before, found, err := f.store.GetClearDevComplexExecution(ctx, id)
		if err != nil || !found || len(before.Dispatches) != 1 {
			t.Fatal("original native fixture unavailable")
		}
		dispatch := before.Dispatches[0]
		originalStep := core.AgentStep{}
		for _, step := range before.AgentSteps {
			if step.ID == dispatch.AgentStepID {
				originalStep = step
			}
		}
		if originalStep.ID == "" {
			t.Fatal("original unsent step missing")
		}
		beforeAttempts, err := f.store.ListClearDevAgentStepAttempts(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		beforeBudget, err := f.store.GetClearDevMessageBudget(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		beforeSpawns, beforeSends := h.spawnCalls, len(h.relays)
		oldCode, err := os.ReadFile(filepath.Join(h.oldWorkspace, "src", "storage.ts"))
		if err != nil {
			t.Fatal(err)
		}
		oldHead := replacementGit(t, h.oldWorkspace, "rev-parse", "HEAD")
		oldStatus := replacementGit(t, h.oldWorkspace, "status", "--porcelain")
		if oldStatus == "" {
			t.Fatal("native fixture must retain dirty allowed code")
		}
		var mu sync.Mutex
		requests := []WorkflowRecoveryInput{}
		reopens, normalAdvances := 0, 0
		type completion struct {
			action           string
			success          bool
			publicReason     string
			attempts, spawns int
			sends            int
		}
		completed := make(chan completion, 8)
		bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				w.Header().Set("Content-Type", "text/html")
				http.ServeFile(w, r, filepath.Join(root, "frontend/test-native/builder-handoff-renderer.html"))
				return
			}
			if r.URL.Path == "/renderer.js" {
				w.Header().Set("Content-Type", "text/javascript")
				http.ServeFile(w, r, rendererBundle)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			var value any
			var bridgeErr error
			var ack *completion
			defer func() {
				if ack != nil {
					attempts, _ := f.store.ListClearDevAgentStepAttempts(ctx, id)
					ack.attempts, ack.spawns, ack.sends = len(attempts), h.replacementSpawns, len(h.relays)-beforeSends
					if execution, found, err := f.store.GetClearDevComplexExecution(ctx, id); err == nil && found {
						_, reason := core.DeriveComplexExecutionPhase(execution)
						ack.publicReason = string(reason)
					}
					completed <- *ack
				}
			}()
			decode := func(target any, allowed ...string) error {
				decoder := json.NewDecoder(io.LimitReader(r.Body, 65537))
				var values map[string]json.RawMessage
				if err := decoder.Decode(&values); err != nil {
					return err
				}
				if len(values) != len(allowed) {
					return errors.New("test request fields differ")
				}
				for key := range values {
					if !slices.Contains(allowed, key) {
						return errors.New("unknown isolated request field")
					}
				}
				if decoder.Decode(new(any)) != io.EOF {
					return errors.New("trailing isolated request data")
				}
				raw, err := json.Marshal(values)
				if err != nil {
					return err
				}
				return json.Unmarshal(raw, target)
			}
			path := "/api/v1/cleardev/requirements/" + id
			switch {
			case r.URL.Path == path+"/recoveries" && r.Method == http.MethodGet:
				value, bridgeErr = f.s.GetWorkflowRecovery(ctx, id)
			case r.URL.Path == path+"/recoveries" && r.Method == http.MethodPost:
				var input WorkflowRecoveryInput
				bridgeErr = decode(&input, "requestId", "executionRunId", "action", "targetId", "supplement")
				ack = &completion{action: input.Action}
				if bridgeErr == nil && (input.ExecutionRunID != before.Run.ID || (input.Action != core.RecoveryRequestBuilderReplacement && input.Action != core.RecoveryContinueBuilderReplacement)) {
					bridgeErr = errors.New("unregistered isolated handoff action")
				}
				if bridgeErr == nil {
					requests = append(requests, input)
					value, bridgeErr = f.s.RequestWorkflowRecovery(ctx, id, input)
					// The fixture deliberately defers its background scheduler. Only a
					// successful actual UI CONTINUE triggers normal control-plane steps.
					// This does not manufacture a role, attempt, relay or candidate.
					if bridgeErr == nil && input.Action == core.RecoveryContinueBuilderReplacement {
						var advances int
						advances, bridgeErr = advanceBuilderHandoffNativeCandidate(ctx, f.s, id)
						normalAdvances += advances
						if bridgeErr == nil {
							value, bridgeErr = f.s.GetWorkflowRecovery(ctx, id)
						}
					}
					ack.success = bridgeErr == nil
				}
			case r.URL.Path == path+"/decision-displays" && r.Method == http.MethodGet:
				value, bridgeErr = f.s.GetHumanDecisionDisplays(ctx, id)
			case r.URL.Path == path+"/decision-displays" && r.Method == http.MethodPost:
				var input ReopenHumanDecisionInput
				bridgeErr = decode(&input, "requestId", "decisionRequestId", "contentSha256", "previousDispatchId")
				if bridgeErr == nil {
					value, bridgeErr = f.s.ReopenHumanDecision(ctx, id, input)
					if bridgeErr == nil {
						reopens++
					}
				}
			default:
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if bridgeErr != nil {
				w.WriteHeader(http.StatusConflict)
				value = map[string]any{"error": map[string]string{"code": "HANDOFF_TEST_BRIDGE_REFUSED", "message": "Isolated worker handoff refused; inspect its saved public reason"}}
			}
			_ = json.NewEncoder(w).Encode(value)
		}))
		defer bridge.Close()
		send("open", map[string]any{"url": bridge.URL + "/?id=" + id + "&locale=" + locale, "locale": locale})
		await("opened")
		snapshot(name+"-initial-production-panel", 1)
		offers := make(chan core.HumanDecisionOffer, 8)
		handler := builderHandoffNativeHandler{f.s, &mu, offers}
		clientCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		client := humanauthority.NewClient(bootstrap.HumanAuthorityEndpoint, bootstrap.HumanAuthorityToken, bootstrap.DesktopRunID, handler, slog.New(slog.NewTextHandler(io.Discard, nil)), connection)
		go func() { defer close(done); client.Run(clientCtx) }()
		stopped := false
		defer func() {
			if !stopped {
				stop()
				<-done
			}
		}()
		nextOffer := func() core.HumanDecisionOffer {
			t.Helper()
			timer := time.NewTimer(25 * time.Second)
			defer timer.Stop()
			select {
			case offer := <-offers:
				if offer.DecisionKind != core.HumanDecisionKindBuilderReplacement {
					t.Fatal("unexpected native handoff kind")
				}
				return offer
			case <-ctx.Done():
				t.Fatal("native handoff offer unavailable")
			case <-timer.C:
				t.Fatal("native handoff offer missed its deadline")
			}
			return core.HumanDecisionOffer{}
		}
		awaitRecovery := func(action string) {
			t.Helper()
			timer := time.NewTimer(30 * time.Second)
			defer timer.Stop()
			select {
			case result := <-completed:
				if result.action != action || !result.success {
					t.Fatalf("actual handoff completion action=%s success=%t attempts=%d spawns=%d sends=%d reason=%s", result.action, result.success, result.attempts, result.spawns, result.sends, result.publicReason)
				}
			case <-ctx.Done():
				t.Fatal("actual handoff POST unavailable")
			case <-timer.C:
				t.Fatal("actual handoff POST missed its deadline")
			}
		}
		waitOutcome := func(request string, count int, expected core.HumanDecisionDispatchOutcome) {
			t.Helper()
			deadline := time.Now().Add(25 * time.Second)
			for time.Now().Before(deadline) {
				history, err := f.store.ListClearDevHumanDecisionDispatchHistory(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				if len(history) == count && history[count-1].Outcome == expected {
					return
				}
				select {
				case <-ctx.Done():
					t.Fatal("native handoff result unavailable")
				case <-time.After(50 * time.Millisecond):
				}
			}
			t.Fatal("native handoff result was not saved before its deadline")
		}
		assertNoEffects := func() {
			t.Helper()
			mu.Lock()
			defer mu.Unlock()
			after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			budget, err := f.store.GetClearDevMessageBudget(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			code, err := os.ReadFile(filepath.Join(h.oldWorkspace, "src", "storage.ts"))
			if err != nil {
				t.Fatal(err)
			}
			if h.spawnCalls != beforeSpawns || h.replacementSpawns != 0 || len(h.relays) != beforeSends || !reflect.DeepEqual(beforeAttempts, attempts) || !reflect.DeepEqual(beforeBudget, budget) || !reflect.DeepEqual(before.RoleBindings, after.RoleBindings) || !reflect.DeepEqual(before.Dispatches, after.Dispatches) || string(code) != string(oldCode) {
				t.Fatalf("native request/decision changed original facts: newSpawns=%d newSends=%d attempts=%d", h.spawnCalls-beforeSpawns, len(h.relays)-beforeSends, len(attempts))
			}
		}
		requestLabel, continueLabel, reopenLabel := "Request a replacement worker (decide in the native window)", "Continue with the authorized replacement worker", "Reopen pending confirmation"
		if locale == "zh-CN" {
			requestLabel, continueLabel, reopenLabel = "申请更换工作者（在原生窗口决定）", "按已授权交接继续", "重新打开原待确认事项"
		}
		click(requestLabel)
		awaitRecovery(core.RecoveryRequestBuilderReplacement)
		original := nextOffer()
		await("reader")
		updateManifest(map[string]any{"productionComponents": true, "realPrivateChannel": true, "realHTTP": true})
		assertNoEffects()
		snapshot(name+"-pending-reader", 2)
		// Both locales show the real OS modal and expire without approval.
		send("reader-continue", nil)
		await("native")
		snapshot(name+"-native-dialog", 2)
		waitOutcome(original.RequestID, 1, core.HumanDecisionDispatchExpired)
		await("native-closed")
		click(reopenLabel)
		reopened := nextOffer()
		await("reader")
		if reopened.RequestID != original.RequestID || reopened.ContentSHA256 != original.ContentSHA256 || string(reopened.Binding) != string(original.Binding) || reopened.Nonce == original.Nonce {
			t.Fatal("actual UI reopen changed its immutable handoff or reused a nonce")
		}
		snapshot(name+"-reopened-reader", 2)
		send("reader-later", nil)
		waitOutcome(original.RequestID, 2, core.HumanDecisionDispatchLater)
		stop()
		<-done
		stopped = true
		assertNoEffects()
		// Explicit synthetic substitution after the real private client stops.
		// It uses a freshly issued offer, never an expired/stale display nonce.
		history, err := f.store.ListClearDevHumanDecisionDispatchHistory(ctx, original.RequestID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ReopenHumanDecision(ctx, id, ReopenHumanDecisionInput{RequestID: "synthetic-handoff-" + name, DecisionRequestID: original.RequestID, ContentSHA256: original.ContentSHA256, PreviousDispatchID: history[1].ID}); err != nil {
			t.Fatal("synthetic handoff display registration failed")
		}
		approved, found, err := f.s.IssueHumanDecisionOffer(ctx, bootstrap.DesktopRunID)
		if err != nil || !found || approved.RequestID != original.RequestID {
			t.Fatal("synthetic handoff offer unavailable")
		}
		result := core.HumanDecisionResult{ProtocolVersion: approved.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: approved.DesktopRunID, RequestID: approved.RequestID, DecisionKind: approved.DecisionKind, BindingSchemaVersion: approved.BindingSchemaVersion, Binding: approved.Binding, ContentSHA256: approved.ContentSHA256, Nonce: approved.Nonce, Decision: core.HumanDecisionApprove}
		if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
			t.Fatal("explicit synthetic handoff approval failed")
		}
		syntheticApprovals++
		updateManifest(map[string]any{"syntheticHumanApprovals": syntheticApprovals})
		assertNoEffects()
		// Actual page reload creates a new QueryClient while keeping its session.
		send("reload", nil)
		await("reloaded")
		click(continueLabel)
		awaitRecovery(core.RecoveryContinueBuilderReplacement)
		caseEvidence := func() map[string]any {
			mu.Lock()
			defer mu.Unlock()
			after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			state, err := f.s.builderReplacementState(ctx, id)
			if err != nil || state.Handoff == nil {
				t.Fatal("actual handoff binding missing")
			}
			attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			originalAttempts := []core.AgentStepAttemptView{}
			for _, attempt := range attempts {
				if attempt.LogicalStepID == originalStep.ID {
					originalAttempts = append(originalAttempts, attempt)
				}
			}
			if len(originalAttempts) != 2 || originalAttempts[0].AOSessionID != h.oldSessionID || originalAttempts[1].AOSessionID != state.Handoff.NewAOSessionID || originalAttempts[1].ClientMessageID != originalStep.ClientMessageID+":attempt:2" {
				t.Fatalf("actual handoff attempt mismatch: originalStepAttempts=%d replacementSpawns=%d", len(originalAttempts), h.replacementSpawns)
			}
			builderSends := 0
			for _, relay := range h.relays[beforeSends:] {
				if relay.clientMessageID == originalStep.ClientMessageID+":attempt:2" {
					builderSends++
					if string(relay.sessionID) != state.Handoff.NewAOSessionID || coreDigest([]byte(relay.prompt)) != originalStep.PromptSHA256 {
						t.Fatal("handoff sent a different task or to the wrong worker")
					}
				}
			}
			if h.replacementSpawns != 1 || builderSends != 1 || state.Handoff.NewAOSessionID == h.oldSessionID || state.Handoff.NewWorkspacePath == h.oldWorkspace {
				t.Fatalf("actual UI handoff mismatch: replacementSpawns=%d originalTaskSends=%d", h.replacementSpawns, builderSends)
			}
			newCode, err := os.ReadFile(filepath.Join(state.Handoff.NewWorkspacePath, "src", "storage.ts"))
			if err != nil || !strings.Contains(string(newCode), string(oldCode)) {
				t.Fatal("new worker lost original dirty code")
			}
			preservedCode, err := os.ReadFile(filepath.Join(h.oldWorkspace, "src", "storage.ts"))
			if err != nil || string(preservedCode) != string(oldCode) || replacementGit(t, h.oldWorkspace, "rev-parse", "HEAD") != oldHead || replacementGit(t, h.oldWorkspace, "status", "--porcelain") != oldStatus {
				t.Fatal("old Git directory was changed or cleared")
			}
			if len(after.Dispatches) == 0 || after.Dispatches[0].ID != dispatch.ID || after.Dispatches[0].AgentStepID != originalStep.ID || after.Dispatches[0].BuilderRoleBindingID != dispatch.BuilderRoleBindingID || after.Dispatches[0].CandidateCommitID == "" || after.Dispatches[0].CandidateCommitSHA != replacementGit(t, state.Handoff.NewWorkspacePath, "rev-parse", "HEAD") || len(after.CheckRuns) == 0 || len(after.Reviews) == 0 {
				t.Fatalf("normal candidate/check/review path missing: dispatches=%d checks=%d reviews=%d advances=%d", len(after.Dispatches), len(after.CheckRuns), len(after.Reviews), normalAdvances)
			}
			public, err := f.s.GetWorkflowRecovery(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			handedOff := false
			for _, replacement := range public.BuilderReplacements {
				if replacement.ID == state.RequestID && replacement.TargetID == state.Binding.TargetID && replacement.DispatchID == dispatch.ID && replacement.StepID == originalStep.ID && replacement.NewAOSessionID == state.Handoff.NewAOSessionID {
					handedOff = replacement.State == "HANDED_OFF"
				}
			}
			if !handedOff || public.Diagnosis == nil || !public.Diagnosis.Current || public.Diagnosis.ReadError != "" {
				t.Fatalf("completed handoff has no current HANDED_OFF projection: handedOff=%t diagnosis=%+v", handedOff, public.Diagnosis)
			}
			for _, issue := range public.Diagnosis.Issues {
				if issue.Relationship == "CURRENT" && issue.ReasonCode == "CONTINUATION_REGISTERED" {
					t.Fatal("completed handoff still tells the user its original message has not been sent")
				}
			}
			afterBudget, err := f.store.GetClearDevMessageBudget(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var usage *core.MessageBudgetUsage
			for i := range afterBudget.Steps {
				if afterBudget.Steps[i].LogicalStepID == originalStep.ID {
					usage = &afterBudget.Steps[i]
				}
			}
			if usage == nil || usage.MaxMessages == nil || *usage.MaxMessages != 3 || usage.ReservedMessages == nil || *usage.ReservedMessages != 1 || usage.ConfirmedSentMessages == nil || *usage.ConfirmedSentMessages != 1 {
				t.Fatal("original handoff step did not retain its three-message ledger with one send")
			}
			if len(requests) != 2 || requests[0].Action != core.RecoveryRequestBuilderReplacement || requests[1].Action != core.RecoveryContinueBuilderReplacement || requests[0].RequestID == requests[1].RequestID || requests[0].TargetID != requests[1].TargetID || reopens != 1 {
				t.Fatal("actual UI mixed its request and continuation identities")
			}
			return map[string]any{"name": name, "uiRequestCount": len(requests), "uiReopenCount": reopens, "originalTaskMessageSends": builderSends, "replacementSpawns": h.replacementSpawns, "allAdditionalProviderSends": len(h.relays) - beforeSends, "oldAOSessionId": h.oldSessionID, "newAOSessionId": state.Handoff.NewAOSessionID, "oldWorkspacePath": h.oldWorkspace, "newWorkspacePath": state.Handoff.NewWorkspacePath, "originalDirtyCodePreserved": true, "oldGitHeadAndStatusPreserved": true, "handedOffProjection": handedOff, "diagnosisCurrentRegisteredAfterSend": false, "originalPromptHash": originalStep.PromptSHA256, "stableSecondMessage": originalAttempts[1].ClientMessageID, "originalMessageBudget": beforeBudget, "finalMessageBudget": afterBudget, "originalStepBudget": usage, "candidateId": after.Dispatches[0].CandidateCommitID, "candidateSha": after.Dispatches[0].CandidateCommitSHA, "checkCount": len(after.CheckRuns), "reviewCount": len(after.Reviews), "normalControlPlaneAdvances": normalAdvances, "backgroundDeferred": true, "nativeInitialOutcome": core.HumanDecisionDispatchExpired, "nativeReopenedOutcome": core.HumanDecisionDispatchLater, "syntheticHumanApprovals": 1, "realNativeApprovals": 0, "realGit": true}
		}()
		handedOffLabel := "The new worker has taken over the recorded task message. The original checks and independent review still apply."
		if locale == "zh-CN" {
			handedOffLabel = "新工作者已接手这条任务消息，随后仍须完成原检查和独立审核。"
		}
		send("await-handoff", map[string]any{"label": handedOffLabel})
		visibleHandoff := await("handoff-visible")
		continued := snapshot(name+"-after-continue", 1)
		if visibleHandoff["handedOff"] != true || visibleHandoff["replacementRegisteredNotice"] != false || continued["replacementRegisteredNotice"] != false {
			t.Fatal("completed production UI handoff is missing or still displays its unsent registration notice")
		}
		caseEvidence["uiRegisteredNoticeAfterSend"] = false
		cases = append(cases, caseEvidence)
		updateManifest(map[string]any{"cases": cases, "realGit": true})
	}
	send("quit", nil)
	finished := await("finished")
	if finished["realApprovals"] != float64(0) {
		t.Fatal("isolated native input unexpectedly approved")
	}
	updateManifest(map[string]any{"nativeReaders": finished["readers"], "nativeDialogs": finished["nativeDialogs"], "realNativeApprovals": finished["realApprovals"]})
	if err := command.Wait(); err != nil {
		t.Fatal("isolated handoff Electron failed", err)
	}
	waited = true
	<-scanned
	updateManifest(map[string]any{"status": "PASS"})
}
