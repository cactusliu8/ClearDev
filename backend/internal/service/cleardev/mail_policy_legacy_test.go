package cleardev

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The historical harness models src/email.js, not the mail product. Separate
// identity from its injected health outcomes so old boundary tests stay intact.
func (h *standardAgentHarness) IdentifyMailProject(context.Context, string) (ports.ClearDevBaselineResult, error) {
	return ports.ClearDevBaselineResult{}, nil
}

func (autoUnhealthyBaseline) IdentifyMailProject(context.Context, string) (ports.ClearDevBaselineResult, error) {
	return ports.ClearDevBaselineResult{}, nil
}
