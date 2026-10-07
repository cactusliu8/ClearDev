package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func validateProjectCandidateVerification(ctx context.Context, q *gen.Queries, verification core.ComplexExecutionVerification) error {
	run, err := q.GetClearDevComplexExecutionRun(ctx, verification.ExecutionRunID)
	if err != nil {
		return err
	}
	effective, err := effectiveProjectRun(ctx, q, run)
	if err != nil {
		return err
	}
	contract, project, err := core.ProjectContractFromRun(effective)
	if err != nil || !project {
		return err
	}
	if err := validateProjectAttemptRevision(ctx, q, verification.DispatchID); err != nil {
		return err
	}
	return validateProjectCheckSet(ctx, q, contract, verification.ComplexExecutionTaskID, verification.CandidateCommitSHA, verification.RequiredCheckRunIDs)
}

// An exact set is required: duplicates, an unrelated task's green result or a
// subset that drops a failed required check cannot complete a task or Stage.
// Empty taskID selects the full integration catalog, not a Builder candidate.
func validateProjectCheckSet(ctx context.Context, q *gen.Queries, contract core.ProjectExecutionContract, taskID, candidateSHA string, checkRunIDs []string) error {
	specs, err := q.ListClearDevComplexExecutionCheckSpecs(ctx, contract.ExecutionRunID)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, spec := range specs {
		if taskID == "" && spec.CheckKind == string(core.CandidateCheckIntegration) ||
			taskID != "" && spec.TaskMappingID.String == taskID && spec.CheckKind == string(core.CandidateCheckRequired) {
			wanted[spec.ID] = true
		}
	}
	if len(wanted) == 0 || len(checkRunIDs) != len(wanted) || taskID == "" && len(wanted) != len(contract.Basis.Checks) {
		return complexExecutionRule("project verification needs the entire exact admitted check set")
	}
	for _, id := range checkRunIDs {
		check, err := q.GetClearDevComplexExecutionCheckRun(ctx, id)
		if err != nil {
			return err
		}
		if !wanted[check.CheckSpecID] {
			return complexExecutionRule("project verification repeats or substitutes a check")
		}
		delete(wanted, check.CheckSpecID)
		if err := validateProjectStoredCheck(ctx, q, contract, id, candidateSHA); err != nil {
			return err
		}
	}
	return nil
}

// The existing completion transaction still owns result creation, independent
// final-review validation and DONE transitions. This adds generic source and
// candidate-bound evidence validation without weakening any historical policy.
func validateProjectCompletion(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, integration core.ComplexExecutionIntegration) error {
	domainRun, err := effectiveProjectRun(ctx, q, run)
	if err != nil {
		return err
	}
	contract, project, err := core.ProjectContractFromRun(domainRun)
	if err != nil || !project {
		return err
	}
	if integration.ExecutionRunID != run.ID {
		return complexExecutionRule("project completion belongs to another execution")
	}
	if run.Status == "COMPLETED" {
		return validateProjectCompletionReplay(ctx, q, run, integration)
	}
	plan, err := q.GetClearDevProjectExecutionSourcePlan(ctx, contract.PlanID)
	if err != nil {
		return err
	}
	if err := validateProjectExecutionRunAdmission(ctx, q, domainRun, plan); err != nil {
		return err
	}
	// The helper's name is historical; it enforces only the common cancelled,
	// paused and Human Authority direction rules, not mail business behavior.
	if err := validateMailRequirementCanComplete(ctx, q, run); err != nil {
		return err
	}
	if _, err := q.GetOpenClearDevRequirementVersion(ctx, run.DevelopmentProjectID); err == nil {
		return complexExecutionRule("project completion has an unresolved specification")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	pending, err := q.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		return err
	}
	for _, decision := range pending {
		if decision.DevelopmentProjectID == run.DevelopmentProjectID || decision.DevelopmentProjectID == contract.ProductID {
			superseded, err := supersededLegacyEngineChange(ctx, q, decision)
			if err != nil {
				return err
			}
			if superseded {
				continue
			}
			return complexExecutionRule("project completion has an unresolved human decision")
		}
	}
	scopes, err := q.ListClearDevComplexExceptionScopeRequests(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if scope.Status == "PENDING" {
			return complexExecutionRule("project completion has an unresolved scope request")
		}
	}
	tasks, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return err
	}
	verified, err := q.ListClearDevComplexExecutionVerifiedCandidates(ctx, run.ID)
	if err != nil {
		return err
	}
	latestByTask := map[string]string{}
	for _, verification := range verified {
		latestByTask[verification.TaskMappingID] = verification.ID
	}
	if len(tasks) == 0 || len(tasks) > 3 || len(latestByTask) != len(tasks) {
		return complexExecutionRule("project completion requires every admitted task to be independently verified")
	}
	for _, task := range tasks {
		effectiveTask, err := q.GetClearDevPlannerRuntimeEffectiveTask(ctx, task.ID)
		if err != nil {
			return err
		}
		pkg, err := core.ParseComplexStandardExecutionPackage([]byte(effectiveTask.TaskPacketJson))
		if err != nil {
			return err
		}
		if err := core.ProjectTaskMatchesRun(pkg, domainRun); err != nil {
			return err
		}
		found := false
		for _, item := range verified {
			if item.TaskMappingID != task.ID || item.ID != latestByTask[task.ID] {
				continue
			}
			if err := validateProjectAttemptRevision(ctx, q, item.TaskAttemptID); err != nil {
				return err
			}
			found = true
			candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(item.TaskAttemptID))
			if err != nil {
				return err
			}
			if candidate.ID != item.CandidateCommitID {
				return complexExecutionRule("project task verification refers to a different candidate")
			}
			var checkIDs []string
			if err := json.Unmarshal([]byte(item.RequiredCheckRunsJson), &checkIDs); err != nil {
				return err
			}
			if err := validateProjectCheckSet(ctx, q, contract, task.ID, candidate.CommitSha, checkIDs); err != nil {
				return err
			}
			// Reuse the actual independent/replacement review and requested-check
			// validator. It does not impose a mailbox template or npm command.
			if _, err := validateMailVerifiedReview(ctx, q, run, item, candidate); err != nil {
				return err
			}
		}
		if !found {
			return complexExecutionRule("project task has no independent verified candidate")
		}
	}
	return validateProjectCheckSet(ctx, q, contract, "", integration.CandidateCommitSHA, integration.CheckRunIDs)
}

func validateProjectCompletionReplay(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, integration core.ComplexExecutionIntegration) error {
	result, err := q.GetClearDevComplexExecutionResult(ctx, run.ID)
	if err != nil {
		return err
	}
	candidate, err := q.GetClearDevIntegrationCandidate(ctx, result.IntegrationCandidateID)
	if err != nil {
		return err
	}
	if result.CompletionStatus != "COMPLETED" || candidate.CommitSha != integration.CandidateCommitSHA {
		return complexExecutionRule("completed project delivery cannot acknowledge another candidate")
	}
	rows, err := q.ListClearDevComplexExecutionResultChecks(ctx, result.ID)
	if err != nil {
		return err
	}
	expected := make([]string, 0, len(rows))
	for _, row := range rows {
		expected = append(expected, row.CheckRunID)
	}
	actual := append([]string(nil), integration.CheckRunIDs...)
	slices.Sort(expected)
	slices.Sort(actual)
	if !slices.Equal(expected, actual) {
		return complexExecutionRule("completed project delivery cannot acknowledge another check set")
	}
	return nil
}
