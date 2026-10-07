package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCodexDiagnosticChild(t *testing.T) {
	mode := os.Getenv("AO_CODEX_DIAGNOSTIC_CHILD")
	if mode == "" {
		return
	}
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
		if request.ID == nil {
			continue
		}
		if request.Method == "turn/start" {
			for range 2000 {
				_, _ = fmt.Fprintln(os.Stderr, strings.Repeat("diagnostic ", 100))
			}
			_, _ = fmt.Fprint(os.Stderr, "Authorization: Bearer worker-secret\nCodex terminal stack:42\n")
			if mode == "disconnect" {
				os.Exit(23)
			}
			_ = encoder.Encode(map[string]any{"id": request.ID, "error": map[string]any{"code": -32603, "message": "fixture failure"}})
			continue
		}
		_ = encoder.Encode(map[string]any{"id": request.ID, "result": map[string]any{}})
	}
}

func TestCodexDiagnosticsRealProcess(t *testing.T) {
	for _, mode := range []string{"rpc", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, diagnosticContextKey{}, diagnosticContext{t.TempDir(), "worker-session", nil})
			proc, err := spawnAppServer(ctx, exe, t.TempDir(), envSlice(map[string]string{"AO_CODEX_DIAGNOSTIC_CHILD": mode, "API_KEY": "worker-secret"}), []string{"-test.run=^TestCodexDiagnosticChild$"})
			if err != nil {
				t.Fatal(err)
			}
			conv := newConversation(proc, slog.New(slog.DiscardHandler))
			defer conv.Close()
			_, err = conv.SendTurn(ctx, ports.ChatUserMessage{Text: "fixture", ClientMessageID: "original-message"})
			if err == nil || !strings.Contains(err.Error(), "worker diagnostics:") {
				t.Fatal("missing failure reference", err)
			}
			if mode == "rpc" {
				var rpc *rpcError
				if !errors.As(err, &rpc) || rpc.Code != -32603 {
					t.Fatal("lost error class", err)
				}
			}
			if err := conv.Close(); err != nil {
				t.Fatal(err)
			}
			var logs string
			for _, name := range []string{"stderr.log", "stderr.previous.log"} {
				b, e := os.ReadFile(filepath.Join(proc.diagnostics.Path(), name))
				if e != nil && !errors.Is(e, os.ErrNotExist) {
					t.Fatal(e)
				}
				logs += string(b)
			}
			for _, want := range []string{"Codex terminal stack:42", "original-message", "exit"} {
				if !strings.Contains(logs, want) {
					t.Fatal("missing diagnostic", want)
				}
			}
			if strings.Contains(logs, "worker-secret") {
				t.Fatal("secret leaked")
			}
		})
	}
}

// A provider's final stdout bytes must remain readable after it exits.
func TestCodexDiagnosticsDoesNotCloseProtocolOnExit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), diagnosticContextKey{}, diagnosticContext{t.TempDir(), "exit-session", nil})
	proc, err := spawnAppServer(ctx, exe, t.TempDir(), envSlice(nil), []string{"-test.run=^$"})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.stop()
	deadline := time.Now().Add(5 * time.Second)
	for !proc.processStopped() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !proc.processStopped() {
		t.Fatal("child did not exit")
	}
	out, err := io.ReadAll(proc.stdout)
	if err != nil || !strings.Contains(string(out), "PASS") {
		t.Fatal("exit lost protocol", err, string(out))
	}
}
