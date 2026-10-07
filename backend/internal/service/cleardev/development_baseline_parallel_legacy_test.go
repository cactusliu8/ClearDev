package cleardev_test

import (
	"context"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The existing parallel fixtures are explicit fake, non-mail projects. The
// bounded mail V2 test opts into a production-shaped baseline receipt.
func (h *parallelAgentHarness) CheckDevelopmentBaseline(_ context.Context, request ports.ClearDevBaselineRequest) (ports.ClearDevBaselineResult, error) {
	if !h.mail {
		return ports.ClearDevBaselineResult{Required: false}, nil
	}
	check := func(suffix string) ports.ClearDevCheckResult {
		return ports.ClearDevCheckResult{
			Outcome: ports.ClearDevCheckPass, CandidateSHA: forty("a"), Image: core.StandardCandidateCheckImage,
			ImageID: "sha256:parallel-test-image", CheckEnvironmentID: strings.Repeat("f", 64),
			OutputSHA256: parallelDigest([]byte(request.RunID + suffix)), ExitCode: 0,
		}
	}
	return ports.ClearDevBaselineResult{
		Required: true, CandidateSHA: forty("a"), TestRunID: request.RunID + ":npm-test", HealthRunID: request.RunID + ":health",
		Tests: check(":npm-test"), Health: check(":health"),
	}, nil
}
