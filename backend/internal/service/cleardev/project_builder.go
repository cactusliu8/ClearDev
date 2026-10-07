package cleardev

import (
	"context"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ensureProjectExecutionBuilder only adapts source/environment preparation.
// Dispatch, budgets, rework, candidate checks and review remain the existing
// STANDARD loop. An empty project is not represented as having passed tests.
func (s *Service) ensureProjectExecutionBuilder(ctx context.Context, execution core.ComplexExecutionSnapshot, projectID string, contract core.ProjectExecutionContract) (bool, bool, error) {
	bindings, ok := currentComplexExecutionBuilderBindings(execution)
	if !ok || len(bindings) != 1 || projectID != contract.Selection.AOProjectID {
		return false, true, errComplexExecutionStopped
	}
	binding := bindings[0]
	fail := func(reason core.ReasonCode, err error) (bool, bool, error) {
		return s.stopComplexBaseline(ctx, binding, &baselineGateError{reason: reason, err: err})
	}
	if !s.selectedProjectCurrent(ctx, &contract.Selection) {
		return fail("PROJECT_EXECUTION_SOURCE_CHANGED", errors.New("the selected repository no longer matches the admitted baseline"))
	}
	preparer, supported := s.checks.(ports.ClearDevProjectExecutionPreparer)
	if !supported {
		return fail(core.ReasonProjectRuntime, errors.New("the project runtime adapter is unavailable"))
	}
	if binding.Status == core.RoleBindingStatusBound {
		record, found, err := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
		if err != nil {
			return false, false, err
		}
		if !found || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexExecutionWorker(ctx, record, binding, projectID) ||
			binding.BaseCommitSHA != contract.BaseCommitSHA || record.Metadata.DiffBaseSHA != contract.BaseCommitSHA {
			return fail("PROJECT_EXECUTION_BASELINE_CHANGED", errors.New("the Builder is not bound to the admitted source"))
		}
		return false, false, nil
	}
	if binding.Status != core.RoleBindingStatusRequested {
		return false, true, errComplexExecutionStopped
	}
	if _, supported := s.complexExecution.(builderReplacementStore); supported {
		state, err := s.builderReplacementState(ctx, execution.Run.DevelopmentRequirementID)
		if err != nil {
			return false, false, err
		}
		if state.Handoff != nil && state.Handoff.Binding.NewRoleBindingID == binding.ID {
			// Only the explicit CONTINUE owns creation and snapshot restoration.
			// A scheduler wakeup cannot spawn a plain worker or retry a failed copy.
			return false, false, errAgentRecoveryDeferred
		}
	}
	if err := preparer.PrepareProjectExecution(ctx, contract); err != nil {
		return fail(core.ReasonProjectRuntime, err)
	}
	branch := complexExecutionBuilderBranch(execution.Run.ID, binding)
	if err := s.inspector.PrepareReviewBranch(ctx, contract.Selection.RepositoryPath, branch, contract.BaseCommitSHA); err != nil {
		return fail("PROJECT_EXECUTION_BASELINE_CHANGED", err)
	}
	session, blocked, err := s.spawnControlledChatSession(ctx, execution.Run.DevelopmentRequirementID, binding.ID, ports.SpawnConfig{
		ProjectID: domain.ProjectID(projectID), Kind: domain.KindWorker,
		Branch: branch, WorkspaceBaseCommitSHA: contract.BaseCommitSHA, RequestedMode: domain.SessionModeChat,
		AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto}, DisplayName: "ClearDev Project Builder",
		CreationIdempotencyKey: binding.SessionCreationIdempotencyKey,
	})
	if blocked {
		return false, false, nil
	}
	if err != nil {
		return fail(core.ReasonBuilderSpawnFailed, err)
	}
	if !s.validComplexExecutionWorker(ctx, session.SessionRecord, binding, projectID) || session.Metadata.DiffBaseSHA != contract.BaseCommitSHA || session.Metadata.WorkspacePath == "" {
		return fail("PROJECT_EXECUTION_BASELINE_CHANGED", errors.New("the spawned Builder lost its exact baseline"))
	}
	observed, err := s.inspector.InspectCandidate(ctx, session.Metadata.WorkspacePath, contract.BaseCommitSHA)
	if err != nil || observed.BaseSHA != contract.BaseCommitSHA || observed.CandidateSHA != contract.BaseCommitSHA || len(observed.Paths) != 0 {
		return fail("PROJECT_EXECUTION_BASELINE_CHANGED", errors.New("the Builder workspace changed before its first dispatch"))
	}
	changed, err := s.complexExecution.BindClearDevComplexExecutionRoleBinding(ctx, binding.ID, string(session.ID), session.Metadata.WorkspacePath, contract.BaseCommitSHA, s.now().UTC())
	return changed, false, err
}
