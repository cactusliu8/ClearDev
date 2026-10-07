package opencodeacp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// Driver preserves the native ACP transport and adds non-billable, project-aware
// inspection. No account API is inferred from the presence of an unrelated key.
type Driver struct {
	ports.ChatDriver
	plugin nativeacp.Plugin
	run    func(context.Context, string, ports.ChatPreflightContext, ...string) ([]byte, error)
}

var _ ports.ChatAccountInspector = (*Driver)(nil)
var _ ports.ChatProjectAccountInspector = (*Driver)(nil)
var _ ports.ChatModelLister = (*Driver)(nil)

// InspectAccount supplies the native local catalog, not a remote credit check.
func (d *Driver) InspectAccount(ctx context.Context) (ports.ChatAccountInspection, error) {
	return d.InspectProjectAccount(ctx, ports.ChatPreflightContext{Harness: domain.HarnessOpenCode})
}

// ListModels reports the user's native catalog without starting a conversation.
func (d *Driver) ListModels(ctx context.Context) ([]ports.ChatModel, error) {
	inspection, err := d.InspectAccount(ctx)
	return inspection.Models, err
}

// InspectProjectAccount uses OpenCode's own config resolution, including project
// files, environment, plugins and configured providers. It never starts a model
// turn, edits config/auth, chooses a fallback provider, or refreshes models.dev.
func (d *Driver) InspectProjectAccount(ctx context.Context, cfg ports.ChatPreflightContext) (ports.ChatAccountInspection, error) {
	evidence := &domain.ControlledPreflightEvidence{
		Scope: "LOCAL_CONFIGURATION", Installation: "UNKNOWN", Configuration: "UNKNOWN",
		Model: "UNKNOWN", Authentication: "UNKNOWN", Service: "UNKNOWN", Quota: "UNKNOWN",
	}
	result := ports.ChatAccountInspection{Evidence: evidence, RateLimits: ports.ChatRateLimits{PrimaryUsedPercent: -1, SecondaryUsedPercent: -1}}
	if d.plugin == nil {
		evidence.Installation = "UNAVAILABLE"
		return result, ports.ErrChatToolNotInstalled
	}
	bin, err := d.plugin.ResolveBinary(ctx)
	if err != nil {
		evidence.Installation = "UNAVAILABLE"
		return result, ports.ErrChatToolNotInstalled
	}
	evidence.Installation = "AVAILABLE"
	inspectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	run := d.run
	if run == nil {
		run = runInspection
	}
	raw, err := run(inspectCtx, bin, cfg, "debug", "config")
	if err != nil {
		evidence.Configuration = "UNAVAILABLE"
		if inspectCtx.Err() != nil {
			return result, ports.ErrChatProviderUnavailable
		}
		return result, ports.ErrChatConfigurationInvalid
	}
	var config struct {
		Model    string `json:"model"`
		Provider map[string]struct {
			Options struct {
				APIKey string `json:"apiKey"`
			} `json:"options"`
		} `json:"provider"`
	}
	if json.Unmarshal(raw, &config) != nil {
		evidence.Configuration = "UNAVAILABLE"
		return result, ports.ErrChatConfigurationInvalid
	}
	evidence.Configuration = "VALID"
	provider, _, _ := strings.Cut(cfg.RequestedModel, "/")
	if strings.TrimSpace(config.Provider[provider].Options.APIKey) != "" {
		// Configured is deliberately not "authenticated": an expired or wrong
		// key can only be rejected authoritatively by the provider itself.
		evidence.Authentication = "CONFIGURED"
	}
	raw, err = run(inspectCtx, bin, cfg, "models")
	if err != nil {
		return result, ports.ErrChatProviderUnavailable
	}
	for _, line := range strings.Split(string(raw), "\n") {
		id := strings.TrimSpace(line)
		provider, model, ok := strings.Cut(id, "/")
		if !ok || provider == "" || model == "" || strings.ContainsAny(id, " \t\r\x1b") {
			continue
		}
		result.Models = append(result.Models, ports.ChatModel{ID: id, DisplayName: id, Default: id == config.Model})
		if id == cfg.RequestedModel {
			evidence.Model = "LISTED"
		}
	}
	if len(result.Models) == 0 {
		return result, ports.ErrChatModelNotAvailable
	}
	if cfg.RequestedModel != "" && evidence.Model != "LISTED" {
		evidence.Model = "UNAVAILABLE"
		return result, ports.ErrChatModelNotAvailable
	}
	return result, nil
}

// inspectionOutput bounds captured output and never includes raw output in an
// error. In particular debug config can contain resolved credentials.
type inspectionOutput struct{ bytes.Buffer }

func (b *inspectionOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, errors.New("native inspection output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func runInspection(ctx context.Context, binary string, cfg ports.ChatPreflightContext, args ...string) ([]byte, error) {
	cmd := aoprocess.CommandContext(ctx, binary, args...)
	cmd.Dir = cfg.WorkspacePath
	cmd.Env = mergedInspectionEnv(os.Environ(), cfg.Env)
	var output inspectionOutput
	cmd.Stdout = &output
	// Do not capture/relay stderr: plugins and config errors can quote secrets.
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("native OpenCode inspection failed: %w", err)
	}
	return output.Bytes(), nil
}

// mergedInspectionEnv overlays the project overrides on the inherited
// environment. Sorted keys keep repeated inspections byte-identical.
func mergedInspectionEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(base)+len(keys))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := overrides[key]; !replaced {
			out = append(out, item)
		}
	}
	for _, key := range keys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}
