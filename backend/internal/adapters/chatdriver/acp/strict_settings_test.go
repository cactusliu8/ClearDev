package acp

import (
	"context"
	"errors"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestACPStrictModelBindingOnStartAndResume(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, tc := range []struct {
			name, confirmed string
			missing         bool
			want            error
		}{
			{name: "setter unsupported", missing: true, want: ErrACPSetterUnsupported},
			{name: "empty response", want: ports.ErrChatDriverIncompatible},
			{name: "wrong model", confirmed: "other/model", want: ports.ErrChatDriverIncompatible},
			{name: "exact model", confirmed: "user/model"},
		} {
			name := "start/" + tc.name
			if resume {
				name = "resume/" + tc.name
			}
			t.Run(name, func(t *testing.T) {
				agent := &fakeAgent{configNotFound: tc.missing, capabilities: &acpsdk.AgentCapabilities{LoadSession: true}}
				if tc.confirmed != "" {
					agent.setConfig = []acpsdk.SessionConfigOption{selectConfigOption("model", "Model", "model", tc.confirmed, "user/model", "other/model")}
				}
				driver := New(Config{
					Harness:      domain.HarnessOpenCode,
					Capabilities: ports.ChatCapabilities{ports.ChatCapabilityResume: true},
					Launch:       func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
					SessionOptions: func(settings ports.ChatTurnSettings) []SessionOption {
						return []SessionOption{{ID: "model", Value: settings.Model}}
					},
					RequireSessionSettings: true,
				}, nil)
				driver.spawn = fakeSpawn(agent)
				var conv ports.ChatConversation
				var err error
				if resume {
					conv, err = driver.Resume(context.Background(), ports.ChatResumeConfig{WorkspacePath: t.TempDir(), Model: "user/model", ProviderConversationID: "original-native-session"})
				} else {
					conv, err = driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir(), Model: "user/model"})
				}
				if conv != nil {
					defer conv.Close()
				}
				if !errors.Is(err, tc.want) {
					t.Fatalf("error=%v, want %v", err, tc.want)
				}
				if tc.want != nil && conv != nil {
					t.Fatal("an unbound model must not expose a usable conversation")
				}
				if tc.want == nil && !conv.Capabilities()[ports.ChatCapabilityResume] {
					t.Fatal("session/load is a supported original-session recovery path")
				}
				if resume {
					agent.mu.Lock()
					defer agent.mu.Unlock()
					if agent.loadCalls != 1 || agent.resumeCalls != 0 || agent.newParams.Cwd != "" || agent.loadParams.SessionId != "original-native-session" {
						t.Fatalf("resume must load the original session, not create another: load=%d resume=%d new=%q id=%q", agent.loadCalls, agent.resumeCalls, agent.newParams.Cwd, agent.loadParams.SessionId)
					}
				}
			})
		}
	}
}
