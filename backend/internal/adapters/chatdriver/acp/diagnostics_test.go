package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestACPDiagnosticChild(t *testing.T) {
	mode := os.Getenv("AO_DIAGNOSTIC_CHILD")
	if mode == "rpc" || mode == "disconnect" {
		decoder := json.NewDecoder(os.Stdin)
		encoder := json.NewEncoder(os.Stdout)
		for {
			var request struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if decoder.Decode(&request) != nil {
				os.Exit(0)
			}
			var result any
			switch request.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": acpsdk.ProtocolVersionNumber, "agentCapabilities": map[string]any{}}
			case "session/new":
				result = map[string]any{"sessionId": "diagnostic-provider-session"}
			case "session/prompt":
				_, _ = fmt.Fprintln(os.Stderr, "REAL-ACP-STACK session handler:42")
				if mode == "disconnect" {
					os.Exit(23)
				}
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "Internal error: OpenCode service failure"}})
				continue
			default:
				result = map[string]any{}
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
	}

	if os.Getenv("AO_DIAGNOSTIC_CHILD") != "1" {
		return
	}
	for range 2500 {
		_, _ = fmt.Fprintln(os.Stderr, strings.Repeat("diagnostic output ", 80))
	}
	_, _ = fmt.Fprintln(os.Stderr, strings.Repeat("x", 16*1024*5))
	_, _ = fmt.Fprintln(os.Stderr, "Authorization: Bearer child-secret-value")
	_, _ = fmt.Fprint(os.Stderr, "TypeError: final-service-failure\n    at session/prompt:42")
	_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","result":"protocol-still-works"}`)
	os.Exit(17)
}

func TestACPDiagnosticsActualProcessRetainsTailWithoutBlockingProtocol(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	p, err := spawnAgent(Launch{Command: exe, Args: []string{"-test.run=^TestACPDiagnosticChild$"}, Env: map[string]string{"AO_DIAGNOSTIC_CHILD": "1", "TEST_API_KEY": "child-secret-value"}, diagnosticDataDir: data, diagnosticSession: "test2-18"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.stop() })
	output := make(chan string, 1)
	go func() {
		b, e := io.ReadAll(p.stdout)
		if e != nil {
			output <- e.Error()
			return
		}
		output <- string(b)
	}()
	select {
	case text := <-output:
		if !strings.Contains(text, "protocol-still-works") || strings.Contains(text, "diagnostic output") {
			t.Fatal("stderr contaminated or blocked protocol", text)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("stderr blocked protocol")
	}
	if err := p.stop(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(p.diagnostics.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("expected two bounded logs plus completion marker: %v", files)
	}
	var combined string
	for _, entry := range files {
		info, e := entry.Info()
		if e != nil {
			t.Fatal(e)
		}
		if info.Size() > 1024*1024 {
			t.Fatal("unbounded log", info.Size())
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("non-private log", info.Mode())
		}
		b, e := os.ReadFile(filepath.Join(p.diagnostics.Path(), entry.Name()))
		if e != nil {
			t.Fatal(e)
		}
		combined += string(b)
	}
	for _, want := range []string{"final-service-failure", "session/prompt:42", "exit status 17", "[overlong line omitted]", "[redacted]"} {
		if !strings.Contains(combined, want) {
			t.Fatal("missing diagnostic", want)
		}
	}
	if strings.Contains(combined, "child-secret-value") {
		t.Fatal("credential persisted")
	}
	// Error notification may arrive after process wait; preserve it and its cause.
	original := errors.New("protocol session failure")
	wrapped := p.diagnostics.Failure("turn=original-turn", original)
	if !errors.Is(wrapped, original) || !strings.Contains(wrapped.Error(), p.diagnostics.Path()) {
		t.Fatal("lost error identity or diagnostic reference", wrapped)
	}
	b, err := os.ReadFile(filepath.Join(p.diagnostics.Path(), "stderr.log"))
	if err != nil || !strings.Contains(string(b), "turn=original-turn") {
		t.Fatal("late protocol error missing", err)
	}
}

func TestACPDiagnosticsFailureEventFromRealProtocolProcess(t *testing.T) {
	for _, mode := range []string{"rpc", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			driver := New(Config{Harness: domain.HarnessOpenCode, Launch: func(context.Context, LaunchConfig) (Launch, error) {
				return Launch{Command: exe, Args: []string{"-test.run=^TestACPDiagnosticChild$"}, Env: map[string]string{"AO_DIAGNOSTIC_CHILD": mode}}, nil
			}}, nil)
			conv, err := driver.Start(context.Background(), ports.ChatStartConfig{SessionID: "diagnostic-session", DataDir: t.TempDir(), WorkspacePath: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conv.Close() })
			ref, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "fixture only", ClientMessageID: "original-client-message"})
			if err != nil {
				t.Fatal(err)
			}
			if err := conv.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
				t.Fatal(err)
			}
			var failure error
			timer := time.NewTimer(60 * time.Second)
			defer timer.Stop()
			for {
				select {
				case event, open := <-conv.Events():
					if !open && mode == "disconnect" {
						_ = conv.Close()
						b, readErr := os.ReadFile(filepath.Join(conv.(*conversation).proc.diagnostics.Path(), "stderr.log"))
						if readErr != nil || !strings.Contains(string(b), "REAL-ACP-STACK") || !strings.Contains(string(b), "exit status 23") {
							t.Fatal("disconnect lost diagnostic", readErr, string(b))
						}
						return
					}
					if !open {
						t.Fatal("event stream closed before failure")
					}
					if event.Kind == ports.ChatEventError {
						failure = event.Err
					}
					if event.Kind != ports.ChatEventTurnCompleted {
						continue
					}
					if event.TurnState != domain.TurnStateFailed || failure == nil || !strings.Contains(failure.Error(), "worker diagnostics:") {
						t.Fatal("failed turn lost its diagnostic reference", failure, event.TurnState)
					}
					if err := conv.Close(); err != nil {
						t.Fatal(err)
					}
					c := conv.(*conversation)
					b, err := os.ReadFile(filepath.Join(c.proc.diagnostics.Path(), "stderr.log"))
					if err != nil {
						t.Fatal(err)
					}
					for _, want := range []string{"REAL-ACP-STACK", "original-client-message", ref.ProviderTurnID, "failure", "exit"} {
						if !strings.Contains(string(b), want) {
							t.Fatal("missing correlated fact", want, string(b))
						}
					}
					return
				case <-timer.C:
					t.Fatal("protocol did not settle")
				}
			}
		})
	}
}
