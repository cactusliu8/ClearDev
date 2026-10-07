package opencodeacp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	nativeagent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// This opt-in test uses the installed CLI and its existing configuration. It
// sends zero model prompts, touches no global config/auth and stores the new
// native session only in its temporary data directory. It is not an end-to-end
// product acceptance test or proof of remote authentication/credit.
func TestOpenCodeNativeHandshakeAndOriginalSessionResume(t *testing.T) {
	model := os.Getenv("CLEARDEV_NATIVE_OPENCODE_MODEL")
	if model == "" {
		t.Skip("set CLEARDEV_NATIVE_OPENCODE_MODEL to an explicitly configured provider/model")
	}
	choice := domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: model}
	if err := choice.Validate(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	env := map[string]string{
		"XDG_DATA_HOME":  filepath.Join(root, "data"),
		"XDG_STATE_HOME": filepath.Join(root, "state"),
	}
	driver := New(nativeagent.New(), nil)
	conv, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "cleardev-native-handshake", WorkspacePath: root, Env: env,
		Model: model, Permissions: domain.PermissionModeAuto,
		SystemPrompt: "This is a ClearDev transport handshake. Do not run tools, change files, send messages or grant approvals. No model turn has been requested.",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conv.Close()
	original := conv.ProviderConversationID()
	if original == "" {
		t.Fatal("native session identity was not returned")
	}
	assertNativeModel := func(c ports.ChatConversation) {
		t.Helper()
		controller, ok := c.(ports.ChatConfigOptionController)
		if !ok {
			t.Fatal("native session does not expose confirmed model configuration")
		}
		options, err := controller.ListConfigOptions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, option := range options {
			if option.ID == "model" && option.Current.Select == model {
				return
			}
		}
		t.Fatal("native ACP did not confirm the selected model")
	}
	assertNativeModel(conv)
	if !conv.Capabilities()[ports.ChatCapabilityResume] {
		t.Fatal("native OpenCode did not negotiate original-session recovery")
	}
	if err := conv.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: "cleardev-native-handshake", ProviderConversationID: original,
		WorkspacePath: root, Env: env, Model: model, Permissions: domain.PermissionModeAuto,
		SystemPrompt: "This is the original ClearDev transport handshake. No new model turn or approval is authorized.",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.ProviderConversationID() != original {
		t.Fatal("resume did not preserve the original native session identity")
	}
	assertNativeModel(resumed)
	t.Logf("native ACP confirmed model %s; resumed the original session %s; model prompts sent: 0", model, original)
}
