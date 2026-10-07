// Package opencodeacp binds the user's own OpenCode installation to AO's
// reusable ACP Chat transport.
package opencodeacp

import (
	"log/slog"
	"os"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `opencode acp` from the exact binary resolved by the existing
// OpenCode agent plugin. AO adds only a per-session inline overlay for its
// standing instructions and an explicit bypass-permissions choice.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return &Driver{ChatDriver: nativeacp.New(plugin, nativeacp.Config{
		Harness:                domain.HarnessOpenCode,
		Configure:              configure,
		SessionOptions:         sessionOptions,
		RequireSessionSettings: true,
	}, log), plugin: plugin}
}

func configure(cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	if cfg.SystemPrompt == "" && ports.NormalizePermissionMode(cfg.Permissions) != ports.PermissionModeBypassPermissions {
		return []string{"acp"}, nil, nil
	}
	existing, overridden := cfg.Env["OPENCODE_CONFIG_CONTENT"]
	if !overridden {
		existing = os.Getenv("OPENCODE_CONFIG_CONTENT")
	}
	content, err := opencode.PrepareACPConfigContent(
		existing, cfg.SystemPrompt, string(cfg.SessionID), cfg.Permissions)
	if err != nil {
		return nil, nil, err
	}
	return []string{"acp"}, map[string]string{"OPENCODE_CONFIG_CONTENT": content}, nil
}

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	if settings.Model == "" {
		return nil
	}
	return []acpdriver.SessionOption{{ID: "model", Value: settings.Model}}
}
