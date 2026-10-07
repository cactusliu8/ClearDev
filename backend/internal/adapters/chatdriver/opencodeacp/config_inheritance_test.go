package opencodeacp

import (
	"encoding/json"
	"strings"
	"testing"

	nativeagent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestNativeRoleOverlayPreservesInheritedInlineConfiguration(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model":"user/default","permission":{"bash":"ask"},"provider":{"user":{"options":{"apiKey":"synthetic-secret"}}},"mcp":{"local":{"type":"local","command":["local-mcp"]}}}`)
	_, overlay, err := configure(acpdriver.LaunchConfig{SessionID: "role-test", SystemPrompt: "role instructions", Permissions: domain.PermissionModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(overlay["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "provider", "permission", "mcp"} {
		if len(config[key]) == 0 {
			t.Fatalf("role injection lost inherited %s", key)
		}
	}
	if !strings.Contains(string(config["permission"]), "ask") || !strings.Contains(string(config["agent"]), "role instructions") {
		t.Fatal("role injection overwrote user permissions or omitted standing instructions")
	}
	_, overlay, err = configure(acpdriver.LaunchConfig{SystemPrompt: "role", Env: map[string]string{"OPENCODE_CONFIG_CONTENT": `{ "model": "project/override" }`}})
	if err != nil {
		t.Fatal(err)
	}
	config = nil
	if err := json.Unmarshal([]byte(overlay["OPENCODE_CONFIG_CONTENT"]), &config); err != nil || string(config["model"]) != `"project/override"` || len(config["provider"]) != 0 {
		t.Fatalf("explicit project environment must replace inherited inline overlay: %v", err)
	}
}

func TestNativeNullInlineConfigFailsWithoutPanic(t *testing.T) {
	if _, err := nativeagent.PrepareACPConfigContent("null", "role", "test", domain.PermissionModeAuto); err == nil {
		t.Fatal("null inline config must report a configuration error")
	}
}
