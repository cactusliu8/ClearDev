package cleardev

import (
	"context"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const checkPreflightTimeout = 2 * time.Minute

func (s *Service) prepareCandidateCheckEnvironment(ctx context.Context, runID, workspace, candidateSHA, image string, argv [][]string) error {
	commands := make([][]string, 0, len(argv))
	for _, command := range argv {
		if len(command) != 0 {
			commands = append(commands, append([]string(nil), command...))
		}
	}
	if len(commands) == 0 {
		return errComplexExecutionStopped
	}
	_, err := s.checks.PrepareCandidateChecks(ctx, ports.ClearDevCheckPreflightRequest{
		RunID: runID, WorkspacePath: workspace, CandidateSHA: candidateSHA,
		Image: image, Argv: commands, Timeout: checkPreflightTimeout,
		MemoryBytes: standardCheckMemoryBytes, PidsLimit: standardCheckPidsLimit,
	})
	return err
}

func (s *Service) releaseCandidateCheckEnvironment(ctx context.Context, runID string) error {
	releaser, ok := s.checks.(ports.ClearDevCheckReferenceReleaser)
	if !ok {
		return nil
	}
	return releaser.ReleaseCandidateChecks(ctx, runID)
}

func standardPlanCheckArgv(plan core.EngineeringPlanResult) [][]string {
	commands := make([][]string, 0, len(plan.Task.RequiredChecks)+1)
	for _, check := range plan.Task.RequiredChecks {
		commands = append(commands, append([]string(nil), check.Argv...))
	}
	commands = append(commands, append([]string(nil), plan.Task.IntegrationCheck.Argv...))
	return commands
}

func complexCheckArgv(specs []core.ComplexExecutionCheckSpecFact) [][]string {
	commands := make([][]string, 0, len(specs))
	for _, spec := range specs {
		if len(spec.Argv) != 0 {
			commands = append(commands, append([]string(nil), spec.Argv...))
		}
	}
	return commands
}
