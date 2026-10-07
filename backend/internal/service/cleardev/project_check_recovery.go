package cleardev

import (
	"context"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type projectDependencyCheckRecoveryRunner interface {
	CanRetryProjectDependencyCheck(context.Context, ports.ClearDevCheckRequest) (bool, error)
}

type projectDependencyCheckRecoveryStore interface {
	RecoverClearDevProjectDependencyCheck(context.Context, string, time.Time) (bool, error)
	ContinueClearDevProjectDependencyCheck(context.Context, string, time.Time) (bool, error)
}

// Only a settled dependency-preparation receipt grants this one retry. An
// unknown external action, command failure or changed source never does.
func (s *Service) advanceProjectDependencyCheckRecovery(ctx context.Context, execution core.ComplexExecutionSnapshot) (bool, bool, error) {
	contract, project, err := core.ProjectContractFromRun(execution.Run)
	if err != nil || !project || execution.Run.Mode != core.WorkModeStandard || execution.Run.CompletedAt != nil {
		return false, false, err
	}
	runner, ok := s.checks.(projectDependencyCheckRecoveryRunner)
	store, stored := s.complexExecution.(projectDependencyCheckRecoveryStore)
	if !ok || !stored {
		return false, false, nil
	}
	for _, task := range execution.Tasks {
		if task.Status != core.DevelopmentTaskStatusBlocked {
			continue
		}
		for _, dispatch := range execution.Dispatches {
			continuing := dispatch.ReasonCode == core.ReasonBuilderSpawnFailed
			if dispatch.ID != task.CurrentDispatchID || (!continuing && dispatch.ReasonCode != "CHECKER_UNAVAILABLE") || dispatch.Status != core.ComplexExecutionDispatchBlocked || dispatch.CandidateCommitID == "" {
				continue
			}
			retried := false
			for _, prior := range execution.CheckRuns {
				if prior.DispatchID == dispatch.ID && prior.CandidateCommitID == dispatch.CandidateCommitID && prior.Kind == core.CandidateCheckRequired && prior.RetryOrdinal > 0 {
					retried = true
					break
				}
			}
			if retried && !continuing {
				continue
			}
			for _, spec := range complexRequiredCheckSpecs(execution, task.ID) {
				check, found := complexCheckRun(execution, dispatch.ID, dispatch.CandidateCommitID, spec.ID)
				if !found || (!continuing && (check.RetryOrdinal != 0 || check.Status != core.ComplexExecutionCheckRunFailed || check.ReasonCode != "CHECKER_UNAVAILABLE")) || (continuing && (check.RetryOrdinal != 1 || check.Status != core.ComplexExecutionCheckRunPending)) {
					continue
				}
				snapshot, exists, lookupErr := s.facts.GetClearDevRequirement(ctx, execution.Run.DevelopmentRequirementID)
				if lookupErr != nil {
					return true, false, lookupErr
				}
				version, confirmed := currentConfirmedVersion(snapshot)
				if !exists || snapshot.Requirement.CancelledAt != nil || !confirmed || version.ID != execution.Run.RequirementVersionID || version.SHA256 != execution.Run.RequirementSHA256 || version.TaskSetVersion != execution.Run.TaskSetVersion || !s.selectedProjectCurrent(ctx, &contract.Selection) {
					return true, false, errComplexExecutionStopped
				}
				binding, bound := complexExecutionBindingByID(execution, dispatch.BuilderRoleBindingID)
				if !bound || binding.Status != core.RoleBindingStatusBound {
					return true, false, errComplexExecutionStopped
				}
				record, found, lookupErr := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
				if lookupErr != nil || !found {
					return true, false, lookupErr
				}
				if record.IsTerminated || !s.validComplexExecutionWorker(ctx, record, binding, snapshot.Requirement.AOProjectID) {
					return true, false, errComplexExecutionStopped
				}
				receiptID := check.ID
				if continuing {
					var bound bool
					receiptID, bound = strings.CutSuffix(check.ID, ":project-dependency-retry")
					step, settled := complexExecutionStepByID(execution, dispatch.AgentStepID)
					if !bound || record.Activity.State != domain.ActivityExited || !settled || step.SendStatus != core.AgentStepSendStatusSettled {
						return true, false, errComplexExecutionStopped
					}
				}
				inspection, inspectErr := s.inspectExecutionCandidate(ctx, execution.Run, record.Metadata.WorkspacePath, dispatch.BaseCommitSHA)
				if inspectErr != nil || inspection.CandidateSHA != dispatch.CandidateCommitSHA || inspection.BaseSHA != dispatch.BaseCommitSHA {
					return true, false, errComplexExecutionStopped
				}
				request := complexCandidateCheckRequest(receiptID, record.Metadata.WorkspacePath, dispatch.CandidateCommitSHA, spec)
				request.ProjectExecution = &contract
				eligible, eligibleErr := runner.CanRetryProjectDependencyCheck(ctx, request)
				if eligibleErr != nil || !eligible {
					if eligibleErr == nil && !continuing && core.BuilderFirstFailureEnabled(execution.Run) {
						return false, false, nil
					}
					return true, false, eligibleErr
				}
				if continuing {
					changed, continueErr := store.ContinueClearDevProjectDependencyCheck(ctx, check.ID, s.now().UTC())
					return true, changed, continueErr
				}
				changed, recoverErr := store.RecoverClearDevProjectDependencyCheck(ctx, check.ID, s.now().UTC())
				return true, changed, recoverErr
			}
		}
	}
	return false, false, nil
}
