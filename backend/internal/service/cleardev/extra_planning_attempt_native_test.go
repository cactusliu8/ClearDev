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
type extraNativeStore struct{ *sqlitestore.Store }

func (s extraNativeStore) IssueClearDevHumanDecisionDispatch(ctx context.Context, input core.IssueHumanDecisionDispatchCommand) (core.HumanDecisionOffer, error) {
	input.ExpiresAt = input.IssuedAt.Add(6 * time.Second)
	return s.Store.IssueClearDevHumanDecisionDispatch(ctx, input)
}

type extraNativeHandler struct {
	*Service
	mu     *sync.Mutex
	offers chan core.HumanDecisionOffer
}

func (s extraNativeHandler) IssueHumanDecisionOffer(ctx context.Context, desktop string) (core.HumanDecisionOffer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	offer, found, err := s.Service.IssueHumanDecisionOffer(ctx, desktop)
	if found {
		s.offers <- offer
	}
	return offer, found, err
}

func (s extraNativeHandler) ApplyHumanDecisionResult(ctx context.Context, result core.HumanDecisionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Service.ApplyHumanDecisionResult(ctx, result)
}

// TestNativeElectronExtraPlanningAttempt is opt-in and uses a test-only HTTP
// bridge into the real service, not the production controller/router. Provider,
// Git, preflight and the final APPROVE are explicit doubles. Production React,
// the private Client/Host, trusted reader and native modal run unchanged.
func TestNativeElectronExtraPlanningAttempt(t *testing.T) {
	if os.Getenv("AO_EXTRA_ATTEMPT_NATIVE") != "1" {
		t.Skip("explicit isolated native extra-attempt check required")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	electron, evidence := os.Getenv("AO_EXTRA_ATTEMPT_NATIVE_ELECTRON"), os.Getenv("AO_EXTRA_ATTEMPT_NATIVE_ROOT")
	if !filepath.IsAbs(electron) || !filepath.IsAbs(evidence) || !strings.HasPrefix(filepath.Base(evidence), "extra-attempt-") {
		t.Fatal("existing trusted Electron and isolated extra-attempt evidence paths required")
	}
	if err := os.MkdirAll(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated extra-attempt evidence=%s", evidence)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	bundler := filepath.Join(root, "frontend/node_modules/rolldown/bin/cli.mjs")
	mainBundle, rendererBundle := filepath.Join(evidence, "main.cjs"), filepath.Join(evidence, "renderer.js")
	configPath := filepath.Join(evidence, "renderer.config.mjs")
	config, err := json.Marshal(map[string]any{"input": filepath.Join(root, "frontend/test-native/extra-attempt-renderer.tsx"), "platform": "browser", "define": map[string]string{"process.env.NODE_ENV": "\"production\""}, "output": map[string]string{"file": rendererBundle, "format": "es", "banner": "import.meta.env = {DEV:false};"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append([]byte("export default "), config...), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{bundler, filepath.Join(root, "frontend/test-native/extra-attempt-main.ts"), "--platform", "node", "--format", "cjs", "--external", "electron", "--file", mainBundle}, {bundler, "--config", configPath}} {
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
	manifest := map[string]any{"sourceCommit": gitOutput("rev-parse", "HEAD"), "sourceDirty": gitOutput("status", "--porcelain") != "", "realElectron": false, "productionComponents": false, "realPrivateChannel": false, "realSQLite": false, "realHTTP": false, "productionController": false, "strictTestServiceBridge": true, "fakeProvider": true, "fakeGit": true, "fakePreflight": true, "syntheticHumanApprovals": 0, "realNativeApprovals": 0, "cases": cases, "status": "RUNNING"}
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
	command.Env = append(os.Environ(), "AO_EXTRA_ATTEMPT_NATIVE=1", "AO_EXTRA_ATTEMPT_NATIVE_ROOT="+evidence)
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
	for _, compilation := range []bool{false, true} {
		for _, locale := range []string{"en", "zh-CN"} {
			scope := "discussion"
			if compilation {
				scope = "compilation"
			}
			name := scope + "-" + strings.ToLower(locale)
			f := exhaustedExtraPlanningFixture(t, compilation)
			updateManifest(map[string]any{"realSQLite": true})
			f.s.desktopRunID = bootstrap.DesktopRunID
			f.s.humanDecisions = extraNativeStore{f.store}
			state := &humanauthority.ConnectionState{}
			f.s.desktopChannelConnected = state.Connected
			beforeAttempts, beforeBudget := extraPlanningAttempts(t, f), extraPlanningBudget(t, f)
			beforeHistory := extraPlanningHistory(t, f, core.RecoveryRetryPlanningStep)
			productBefore, _, err := f.store.GetClearDevProduct(ctx, f.product)
			if err != nil {
				t.Fatal(err)
			}
			calls := len(f.h.relays)
			var mu sync.Mutex
			requests := []WorkflowRecoveryInput{}
			reopens := 0
			type recoveryCompletion struct {
				action  string
				success bool
			}
			completed := make(chan recoveryCompletion, 8)
			bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/api/") {
					if r.URL.Path == "/renderer.js" {
						http.ServeFile(w, r, rendererBundle)
						return
					}
					w.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><style>body{font:16px/1.5 system-ui;padding:24px}button{padding:10px;margin:8px}section{max-width:980px}details{margin:8px}</style><div id="root"></div><script type="module" src="/renderer.js"></script>`)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				var value any
				var bridgeErr error
				var completion *recoveryCompletion
				decode := func(target any, fields ...string) error {
					decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
					token, err := decoder.Token()
					if err != nil || token != json.Delim('{') {
						return errors.New("exact test request object required")
					}
					values := make(map[string]string, len(fields))
					for decoder.More() {
						token, err := decoder.Token()
						key, valid := token.(string)
						allowed := false
						for _, field := range fields {
							allowed = allowed || field == key
						}
						_, duplicate := values[key]
						if err != nil || !valid || !allowed || duplicate {
							return errors.New("unknown or duplicate test field")
						}
						valueToken, err := decoder.Token()
						value, valid := valueToken.(string)
						if err != nil || !valid {
							return errors.New("test field must be a JSON string")
						}
						values[key] = value
					}
					token, err = decoder.Token()
					if err != nil || token != json.Delim('}') || len(values) != len(fields) {
						return errors.New("complete test request fields required")
					}
					if decoder.Decode(new(any)) != io.EOF {
						return errors.New("test request has trailing data")
					}
					raw, err := json.Marshal(values)
					if err != nil {
						return err
					}
					return json.Unmarshal(raw, target)
				}
				path := "/api/v1/cleardev/requirements/" + f.id
				switch {
				case r.URL.Path == path+"/recoveries" && r.Method == http.MethodGet:
					value, bridgeErr = f.s.GetWorkflowRecovery(ctx, f.id)
				case r.URL.Path == path+"/recoveries" && r.Method == http.MethodPost:
					var input WorkflowRecoveryInput
					bridgeErr = decode(&input, "requestId", "executionRunId", "action", "targetId", "supplement")
					completion = &recoveryCompletion{action: input.Action}
					if bridgeErr == nil && (input.Action != extraPlanningRequestAction && input.Action != extraPlanningContinueAction || input.ExecutionRunID != "") {
						bridgeErr = errors.New("unregistered isolated recovery action")
					}
					if bridgeErr == nil {
						requests = append(requests, input)
						value, bridgeErr = f.s.RequestWorkflowRecovery(ctx, f.id, input)
						completion.success = bridgeErr == nil
					}
				case r.URL.Path == path+"/decision-displays" && r.Method == http.MethodGet:
					value, bridgeErr = f.s.GetHumanDecisionDisplays(ctx, f.id)
				case r.URL.Path == path+"/decision-displays" && r.Method == http.MethodPost:
					var input ReopenHumanDecisionInput
					bridgeErr = decode(&input, "requestId", "decisionRequestId", "contentSha256", "previousDispatchId")
					if bridgeErr == nil {
						reopens++
						value, bridgeErr = f.s.ReopenHumanDecision(ctx, f.id, input)
					}
				default:
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if bridgeErr != nil {
					w.WriteHeader(http.StatusConflict)
					value = map[string]any{"error": map[string]string{"code": "ISOLATED_BRIDGE_REFUSED", "message": "The exact test request was refused; inspect the bound service evidence."}}
				}
				encodeErr := json.NewEncoder(w).Encode(value)
				if completion != nil {
					completion.success = completion.success && encodeErr == nil
					completed <- *completion
				}
			}))
			defer bridge.Close()
			awaitRecovery := func(action string) {
				t.Helper()
				deadline := time.NewTimer(15 * time.Second)
				defer deadline.Stop()
				select {
				case result := <-completed:
					if result.action != action || !result.success {
						t.Fatalf("actual UI recovery completion action=%s success=%t; expected action=%s", result.action, result.success, action)
					}
				case <-ctx.Done():
					t.Fatalf("actual UI recovery action %s did not complete", action)
				case <-deadline.C:
					t.Fatalf("actual UI recovery action %s did not complete within its deadline", action)
				}
			}
			send("open", map[string]any{"url": bridge.URL + "/?id=" + f.id + "&scope=" + scope + "&locale=" + locale, "locale": locale})
			await("opened")
			offers := make(chan core.HumanDecisionOffer, 8)
			handler := extraNativeHandler{f.s, &mu, offers}
			clientCtx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			client := humanauthority.NewClient(bootstrap.HumanAuthorityEndpoint, bootstrap.HumanAuthorityToken, bootstrap.DesktopRunID, handler, slog.New(slog.NewTextHandler(io.Discard, nil)), state)
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
				select {
				case offer := <-offers:
					if offer.DecisionKind != extraPlanningDecisionKind {
						t.Fatal("unexpected native decision kind")
					}
					return offer
				case <-ctx.Done():
					t.Fatal("native extra-attempt offer unavailable")
					return core.HumanDecisionOffer{}
				}
			}
			waitOutcome := func(request string, count int, expected core.HumanDecisionDispatchOutcome) {
				t.Helper()
				deadline := time.Now().Add(15 * time.Second)
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
						t.Fatal("native outcome unavailable")
					case <-time.After(100 * time.Millisecond):
					}
				}
				t.Fatal("native display outcome not persisted")
			}
			requestLabel, reopenLabel := "Request one extra attempt (decide in the native window)", "Reopen pending confirmation"
			continueLabel := "Use the authorized attempt to continue this discussion"
			if compilation {
				continueLabel = "Use the authorized attempt to compile the original stage"
			}
			if locale == "zh-CN" {
				requestLabel, reopenLabel = "申请额外一次尝试（在原生窗口决定）", "重新打开原待确认事项"
				continueLabel = "使用已授权次数，继续本轮讨论"
				if compilation {
					continueLabel = "使用已授权次数，编译原阶段规格"
				}
			}
			click(requestLabel)
			awaitRecovery(extraPlanningRequestAction)
			original := nextOffer()
			await("reader")
			updateManifest(map[string]any{"productionComponents": true, "realHTTP": true, "realPrivateChannel": true})
			if len(f.h.relays) != calls || !reflect.DeepEqual(beforeAttempts, extraPlanningAttempts(t, f)) || !reflect.DeepEqual(beforeBudget, extraPlanningBudget(t, f)) {
				t.Fatal("actual UI request sent or reset an original attempt")
			}
			snapshot(name+"-pending-reader", 2)
			outcome := core.HumanDecisionDispatchLater
			if locale == "en" {
				send("reader-later", nil)
			} else {
				send("reader-continue", nil)
				await("native")
				snapshot(name+"-native-dialog", 2)
				outcome = core.HumanDecisionDispatchExpired
			}
			waitOutcome(original.RequestID, 1, outcome)
			if locale == "zh-CN" {
				await("native-closed")
			}
			click(reopenLabel)
			fresh := nextOffer()
			await("reader")
			if fresh.RequestID != original.RequestID || fresh.ContentSHA256 != original.ContentSHA256 || string(fresh.Binding) != string(original.Binding) || fresh.Nonce == original.Nonce {
				t.Fatal("actual UI reopen changed the original authority binding")
			}
			snapshot(name+"-reopened-reader", 2)
			send("reader-later", nil)
			waitOutcome(original.RequestID, 2, core.HumanDecisionDispatchLater)
			stop()
			<-done
			stopped = true
			if !reflect.DeepEqual(beforeAttempts, extraPlanningAttempts(t, f)) || !reflect.DeepEqual(beforeBudget, extraPlanningBudget(t, f)) || len(f.h.relays) != calls {
				t.Fatal("native Later/expiry/reopen changed attempts or budget")
			}
			// Explicit synthetic substitution after the real client has stopped.
			// It uses a newly registered/issued exact offer, never an expired nonce.
			history, err := f.store.ListClearDevHumanDecisionDispatchHistory(ctx, original.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.ReopenHumanDecision(ctx, f.id, ReopenHumanDecisionInput{RequestID: "synthetic-only-" + name, DecisionRequestID: original.RequestID, ContentSHA256: original.ContentSHA256, PreviousDispatchID: history[1].ID}); err != nil {
				t.Fatal("synthetic test reopen", err)
			}
			approved, found, err := f.s.IssueHumanDecisionOffer(ctx, bootstrap.DesktopRunID)
			if err != nil || !found || approved.RequestID != original.RequestID {
				t.Fatal("synthetic test offer unavailable")
			}
			result := core.HumanDecisionResult{ProtocolVersion: approved.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: approved.DesktopRunID, RequestID: approved.RequestID, DecisionKind: approved.DecisionKind, BindingSchemaVersion: approved.BindingSchemaVersion, Binding: approved.Binding, ContentSHA256: approved.ContentSHA256, Nonce: approved.Nonce, Decision: core.HumanDecisionApprove}
			if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
				t.Fatal("explicit synthetic test approval", err)
			}
			syntheticApprovals++
			updateManifest(map[string]any{"syntheticHumanApprovals": syntheticApprovals})
			if !reflect.DeepEqual(beforeAttempts, extraPlanningAttempts(t, f)) || len(f.h.relays) != calls {
				t.Fatal("test authorization auto-created attempt or sent")
			}
			f.h.fail = false
			f.h.replies = append(f.h.replies, f.reply)
			click(continueLabel)
			awaitRecovery(extraPlanningContinueAction)
			mu.Lock()
			after := extraPlanningAttempts(t, f)
			afterBudget := extraPlanningBudget(t, f)
			if len(after) != 3 || !reflect.DeepEqual(beforeAttempts, after[:2]) || len(f.h.relays) != calls+1 || f.h.relays[calls].clientMessageID != f.step.ClientMessageID+":attempt:3" || string(f.h.relays[calls].sessionID) != beforeAttempts[1].AOSessionID {
				attemptSummary := []string{}
				for _, attempt := range after {
					attemptSummary = append(attemptSummary, fmt.Sprintf("%d:%s:%s", attempt.AttemptNumber, attempt.SendStatus, attempt.FailureCategory))
				}
				mu.Unlock()
				t.Fatalf("actual UI continuation mismatch: attempts=%d (%v), newProviderSends=%d, recoveryPOSTs=%d", len(after), attemptSummary, len(f.h.relays)-calls, len(requests))
			}
			originalPrompt := ""
			for _, relay := range f.h.relays[:calls] {
				if relay.clientMessageID == f.step.ClientMessageID {
					originalPrompt = relay.prompt
				}
			}
			if originalPrompt == "" || f.h.relays[calls].prompt != originalPrompt || *afterBudget.ReservedMessages != *beforeBudget.ReservedMessages+1 || *afterBudget.ConfirmedSentMessages != *beforeBudget.ConfirmedSentMessages+1 || *afterBudget.MaxMessages != 4 || !reflect.DeepEqual(beforeHistory, extraPlanningHistory(t, f, core.RecoveryRetryPlanningStep)) {
				mu.Unlock()
				t.Fatal("continuation changed original prompt, history or message accounting")
			}
			if len(requests) != 2 || requests[0].Action != extraPlanningRequestAction || requests[1].Action != extraPlanningContinueAction || requests[0].RequestID == requests[1].RequestID || requests[0].TargetID != requests[1].TargetID || reopens != 1 {
				mu.Unlock()
				t.Fatal("actual UI repeated or mixed its exact request identities")
			}
			productAfter, _, err := f.store.GetClearDevProduct(ctx, f.product)
			if err != nil || len(productAfter.Discussions) != len(productBefore.Discussions) || productBefore.Discussions[0].ID != productAfter.Discussions[0].ID || productBefore.Discussions[0].UserMessage != productAfter.Discussions[0].UserMessage {
				mu.Unlock()
				t.Fatal("native test replaced original discussion")
			}
			if compilation && !reflect.DeepEqual(productBefore.Stages[0].Definition, productAfter.Stages[0].Definition) {
				mu.Unlock()
				t.Fatal("native test changed original stage definition")
			}
			if _, found, err := f.store.GetClearDevComplexExecution(ctx, f.id); err != nil || found {
				mu.Unlock()
				t.Fatal("planning permission started execution")
			}
			if compilation {
				view, err := f.s.GetRequirement(ctx, f.id)
				if err != nil || len(view.RequirementVersions) != 1 || view.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation {
					mu.Unlock()
					t.Fatal("third compilation bypassed normal pending confirmation")
				}
			} else if productAfter.Discussions[0].Result == nil || productAfter.Discussions[0].Result.Outcome != "READY" {
				mu.Unlock()
				t.Fatal("third reply did not settle the original discussion")
			}
			caseEvidence := map[string]any{"name": name, "uiRequestCount": len(requests), "uiReopenCount": reopens, "originalAttempts": len(beforeAttempts), "finalAttempts": len(after), "additionalProviderSends": len(f.h.relays) - calls, "originalFactsPreserved": true, "originalPromptAndSessionPreserved": true, "stableThirdMessage": f.h.relays[calls].clientMessageID, "originalMessageBudget": beforeBudget, "finalMessageBudget": afterBudget, "nativeInitialOutcome": outcome, "nativeReopenedOutcome": core.HumanDecisionDispatchLater, "syntheticHumanApprovals": 1, "realNativeApprovals": 0}
			mu.Unlock()
			snapshot(name+"-after-continue", 1)
			cases = append(cases, caseEvidence)
			updateManifest(map[string]any{"cases": cases})
		}
	}
	send("quit", nil)
	finished := await("finished")
	if finished["realApprovals"] != float64(0) {
		t.Fatal("isolated native input unexpectedly approved")
	}
	updateManifest(map[string]any{"nativeReaders": finished["readers"], "nativeDialogs": finished["nativeDialogs"], "realNativeApprovals": finished["realApprovals"]})
	if err := command.Wait(); err != nil {
		t.Fatal("isolated Electron failed", err)
	}
	waited = true
	<-scanned
	updateManifest(map[string]any{"status": "PASS"})
}
