package modelcatalog

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type catalogCodexBinary string

func (b catalogCodexBinary) ResolveBinary(context.Context) (string, error) { return string(b), nil }
func (catalogCodexBinary) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusUnknown, nil
}
func discoverCodexModels(ctx context.Context, binary, workdir string, env map[string]string) (ports.AgentModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	models, err := codexappserver.New(catalogCodexBinary(binary), nil).InspectModels(ctx, workdir, env)
	if err != nil {
		return Manual("codex"), err
	}
	result := ports.AgentModelCatalog{AgentID: "codex", SelectionMode: ports.ModelSelectionCatalog, AllowCustom: true, Source: "app-server", FetchedAt: time.Now().UTC()}
	for _, m := range models {
		result.Models = append(result.Models, ports.AgentModelInfo{ID: m.ID, Label: m.DisplayName, IsDefault: m.Default, Efforts: m.Efforts})
	}
	return result, nil
}
