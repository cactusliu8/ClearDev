package opencode

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/nativeconfig"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	_ ports.AgentContinuationCapabilityProvider = (*Plugin)(nil)
	_ ports.AgentNativeSessionConfigProvider    = (*Plugin)(nil)
)

// ContinuationCapabilities reports that opencode assigns fresh conversation
// identities itself (AO captures them through the session activity hooks and
// chat driver events) and that it accepts current developer instructions on a
// fresh launch: AO's system prompt — including the agent continuation
// protocol — is injected through the generated per-session opencode config, so
// a session switch can hand context to a fresh opencode conversation.
func (p *Plugin) ContinuationCapabilities() ports.ContinuationCapabilities {
	return ports.ContinuationCapabilities{
		FreshNativeSessionID: ports.FreshNativeSessionIDProviderAssigned,
	}
}

// NativeSessionConfigDir returns opencode's native state root, where its
// session database lives. OPENCODE_DATA_DIR from the invocation takes
// precedence; the standard ~/.local/share/opencode is the fallback. Transcripts
// for opencode conversations live in that state database rather than files, so
// no transcript locator is declared.
func (p *Plugin) NativeSessionConfigDir(ctx context.Context, env map[string]string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return nativeconfig.Resolve(env, "OPENCODE_DATA_DIR", ".local/share/opencode")
}
