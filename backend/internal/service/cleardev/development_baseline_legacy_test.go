package cleardev

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Existing deterministic role fixtures model non-mail projects. This explicit
// checker double preserves their original scope; it is never a production bypass.
func (h *standardAgentHarness) CheckDevelopmentBaseline(context.Context, ports.ClearDevBaselineRequest) (ports.ClearDevBaselineResult, error) {
	return ports.ClearDevBaselineResult{Required: false}, nil
}
