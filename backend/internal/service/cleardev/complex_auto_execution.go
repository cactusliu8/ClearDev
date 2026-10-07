package cleardev

import (
	"context"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// advanceApprovedComplexPlan follows a deterministic V2 admission or a legacy
// V1 review transaction, and is also reached by ResumeComplexFlows after a crash. The execution store's
// existing (requirement version, plan) identity is the durable idempotency key;
// no separate automatic-start flag or terminal state is persisted.
func (s *Service) advanceApprovedComplexPlan(ctx context.Context, snapshot core.RequirementSnapshot, planning core.ComplexPlanningSnapshot) (bool, bool, error) {
	stage, _, sourceErr := s.productStageSource(ctx, snapshot.Requirement.ID)
	if sourceErr != nil || stage.Definition.ExecutionBasis != nil {
		return false, true, sourceErr
	}
	if !s.autoAdvanceComplexPlans {
		return false, true, nil
	}
	version, confirmed := currentConfirmedVersion(snapshot)
	if !confirmed || snapshot.Requirement.CancelledAt != nil {
		return false, true, nil
	}
	_, bound, err := s.benchmarkBindingForRequirement(ctx, snapshot.Requirement)
	if err != nil || bound {
		// A benchmark's frozen external campaign, including its QUICK policy,
		// cannot be started by ordinary product automation.
		return false, true, err
	}
	plan, _, approved := approvedComplexExecutionPlan(planning, version.ID)
	if !approved {
		return false, true, nil
	}
	parsed, err := parseComplexExecutionPlan(plan)
	if err != nil {
		return false, true, err
	}
	if parsed.SchemaVersion != core.PlannerTaskContractVersion && (len(parsed.Tasks) != 1 || parsed.ParallelSuggestion.RecommendedBuilderCount != 1) {
		// Only the bounded mail contract extends the ordinary auto handoff.
		// Legacy/non-mail multi-task and benchmark campaigns remain excluded.
		if !s.boundedMailAttempts {
			return false, true, nil
		}
		if _, supported := s.checks.(ports.ClearDevMailProjectIdentifier); !supported {
			return false, true, nil
		}
		_, mail, identityErr := s.mailRequirementBaseline(ctx, snapshot.Requirement)
		if identityErr != nil || !mail {
			return false, true, identityErr
		}
		if err := core.ValidateMailPlan(parsed); err != nil {
			return false, true, err
		}
	}
	// Start rechecks the current version/admission and the store repeats all
	// authority/stop gates transactionally. Repeated completed starts in this
	// assembly are reads, never requests to create QUICK.
	_, err = s.StartComplexStandardExecution(ctx, snapshot.Requirement.ID)
	return false, true, err
}
