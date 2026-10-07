package opencodeacp

import (
	"encoding/json"
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Chat derives the managed-role runtime choice from durable identity. The
// adapter must keep Auto's ordinary meaning and preserve unrelated agents.
func TestControlledRuntimeNativePermissionOverlayIsExplicitAndLocal(t *testing.T) {
	const existing = `{"permission":{"external_directory":"ask","edit":"deny"},"agent":{"independent-review":{"permission":{"*":"deny","read":"allow"}}},"provider":{"example":{"name":"kept"}}}`
	for _, mode := range []ports.PermissionMode{ports.PermissionModeAuto, ports.PermissionModeAcceptEdits, ports.PermissionModeBypassPermissions} {
		t.Run(string(mode), func(t *testing.T) {
			input := acpdriver.LaunchConfig{SessionID: "controlled-worker", SystemPrompt: "Unchanged role instructions.", Permissions: mode,
				Env: map[string]string{"OPENCODE_CONFIG": "/operator/config.jsonc", "OPENCODE_CONFIG_CONTENT": existing},
			}
			args, env, err := configure(input)
			if err != nil || !reflect.DeepEqual(args, []string{"acp"}) {
				t.Fatal(args, err)
			}
			var output map[string]any
			if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &output); err != nil {
				t.Fatal(err)
			}
			if mode == ports.PermissionModeBypassPermissions {
				if output["permission"] != "allow" {
					t.Fatal("full runtime still needs external-directory approval", output["permission"])
				}
			} else if !reflect.DeepEqual(output["permission"], map[string]any{"external_directory": "ask", "edit": "deny"}) {
				t.Fatal("ordinary or restricted policy broadened", output["permission"])
			}
			agents := output["agent"].(map[string]any)
			review := agents["independent-review"].(map[string]any)
			if !reflect.DeepEqual(review["permission"], map[string]any{"*": "deny", "read": "allow"}) || output["provider"].(map[string]any)["example"] == nil {
				t.Fatal("independent reviewer or provider configuration changed")
			}
			if input.Env["OPENCODE_CONFIG_CONTENT"] != existing || env["OPENCODE_CONFIG"] != "" || len(env) != 1 {
				t.Fatal("policy mutated operator config rather than producing a local overlay")
			}
		})
	}
}
