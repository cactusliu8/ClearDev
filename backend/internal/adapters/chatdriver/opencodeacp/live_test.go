package opencodeacp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Run explicitly with AO_LIVE_OPENCODE_ACP=1. It uses the user's existing
// OpenCode executable, configuration, providers, and credentials; CI never
// depends on any of them.
func TestLiveOpenCodeACP(t *testing.T) {
	if os.Getenv("AO_LIVE_OPENCODE_ACP") != "1" {
		t.Skip("set AO_LIVE_OPENCODE_ACP=1 to run against the local OpenCode account")
	}

	driver := New(opencode.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	model := os.Getenv("CLEARDEV_NATIVE_OPENCODE_MODEL")
	if model == "" {
		t.Fatal("live tests require an explicit CLEARDEV_NATIVE_OPENCODE_MODEL; never use a provider default")
	}
	workspace := t.TempDir()
	dataDir := t.TempDir()
	env := envMap()
	env["XDG_DATA_HOME"] = filepath.Join(dataDir, "data")
	env["XDG_STATE_HOME"] = filepath.Join(dataDir, "state")
	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-opencode-acp", DataDir: dataDir, WorkspacePath: workspace,
		Env: env, Model: model, SystemPrompt: "Answer in one short sentence. Do not use tools or inspect environment variables.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.Close()

	ref, err := conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text: "Reply with exactly: AO OpenCode ACP works", ClientMessageID: "live-1",
		Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conversation.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}

	var answer strings.Builder
	for {
		select {
		case event, ok := <-conversation.Events():
			if !ok {
				t.Fatalf("controller closed before completion; answer=%q", answer.String())
			}
			switch event.Kind {
			case ports.ChatEventError:
				// Do not print provider payloads: they can contain credentials.
				var rpc *acpsdk.RequestError
				if errors.As(event.Err, &rpc) {
					raw, _ := json.Marshal(rpc.Data)
					names := regexp.MustCompile(`\b[A-Z][A-Za-z0-9]+Error\b`).FindAllString(string(raw), 8)
					statuses := regexp.MustCompile(`"statusCode"\s*:\s*[1-5][0-9]{2}`).FindAllString(string(raw), 4)
					t.Logf("native failure: JSON-RPC code=%d error types=%v HTTP statuses=%v", rpc.Code, names, statuses)
				} else {
					t.Logf("native failure: type=%T authRequired=%t", event.Err, errors.Is(event.Err, ports.ErrChatAuthRequired))
				}
			case ports.ChatEventMessageDelta:
				answer.WriteString(event.Delta)
			case ports.ChatEventTurnCompleted:
				if event.TurnState != domain.TurnStateCompleted {
					t.Fatalf("turn state = %q; answer=%q", event.TurnState, answer.String())
				}
				if !strings.Contains(answer.String(), "AO OpenCode ACP works") {
					t.Fatalf("answer = %q", answer.String())
				}
				t.Logf("one completed native ACP model turn; explicitly selected model: %s", model)
				return
			}
		case <-ctx.Done():
			t.Fatalf("live turn timed out: %v; answer=%q", ctx.Err(), answer.String())
		}
	}
}

func envMap() map[string]string {
	out := make(map[string]string)
	for _, pair := range os.Environ() {
		name, value, ok := strings.Cut(pair, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}
