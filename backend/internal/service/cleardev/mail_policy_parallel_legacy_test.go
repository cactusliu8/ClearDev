package cleardev_test

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Historical fixtures remain explicit non-mail projects. The bounded mail V2
// service test opts into the same harness without changing the S07 defaults.
func (h *parallelAgentHarness) IdentifyMailProject(context.Context, string) (ports.ClearDevBaselineResult, error) {
	if !h.mail {
		return ports.ClearDevBaselineResult{}, nil
	}
	return ports.ClearDevBaselineResult{Required: true, CandidateSHA: forty("a")}, nil
}
