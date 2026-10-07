package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// CaptureClearDevProjectPlannerEvent bridges a settled historical Builder
// blocker without editing its message or issuing another provider turn.
func (s *Store) CaptureClearDevProjectPlannerEvent(ctx context.Context, runID string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	changed := false
	err := s.inTx(ctx, "capture current project Builder coordination", func(q *gen.Queries) error {
		run, err := q.GetClearDevComplexExecutionRun(ctx, runID)
		if err != nil {
			return err
		}
		current, err := plannerRuntimeCurrent(ctx, q, run)
		if err != nil || !current || !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) {
			return err
		}
		attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, runID)
		if err != nil {
			return err
		}
		latest := map[string]gen.CleardevComplexExecutionTaskAttempt{}
		for _, a := range attempts {
			if old := latest[a.TaskMappingID]; old.ID == "" || old.Round < a.Round {
				latest[a.TaskMappingID] = a
			}
		}
		for _, a := range latest {
			if a.Status != "BLOCKED" || a.ReasonCode != "BUILDER_BLOCKED" || !a.SettledAt.Valid {
				continue
			}
			if _, err := q.GetClearDevPlannerRuntimeEvent(ctx, a.ID+":planner-coordination"); err == nil {
				continue
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := capturePlannerRuntimeEvent(ctx, q, a.AgentStepID); err != nil {
				return err
			}
			if _, err := q.GetClearDevPlannerRuntimeEvent(ctx, a.ID+":planner-coordination"); err == nil {
				changed = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		return nil
	})
	return changed, err
}

func effectiveProjectRun(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun) (core.ComplexExecutionRun, error) {
	out := complexExecutionRunFromGen(run)
	history, err := loadPlannerRuntime(ctx, q, out)
	if err != nil {
		return out, err
	}
	if history == nil || len(history.Amendments) == 0 {
		return out, nil
	}
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return out, err
	}
	snapshot := core.ComplexExecutionSnapshot{Run: out, PlannerRuntime: history}
	for _, m := range mappings {
		snapshot.Tasks = append(snapshot.Tasks, core.ComplexExecutionTask{ID: m.ID, ExecutionRunID: m.ExecutionRunID, TaskKey: m.PlanTaskKey, DevelopmentTaskID: m.WorkItemID, ExecutionPackageJSON: m.TaskPacketJson, ExecutionPackageSHA256: m.TaskPacketSha256})
	}
	if err := overlayPlannerRuntimePackages(&snapshot); err != nil {
		return out, err
	}
	return snapshot.Run, nil
}

func validateProjectPlannerProposal(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, result core.PlannerCoordinationResult) error {
	if !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) {
		if len(result.Amendments) > 2 {
			return complexExecutionRule("legacy coordination allows at most two tasks")
		}
		return nil
	}
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return err
	}
	var basis *core.ProjectExecutionBasis
	for _, a := range result.Amendments {
		if a.ExecutionBasis != nil {
			basis = a.ExecutionBasis
			break
		}
	}
	if basis == nil {
		return nil
	}
	stage, err := projectPlanningStage(ctx, q, run.DevelopmentProjectID)
	if err != nil {
		return err
	}
	if err := core.ValidateProjectTrialCoverage(basis.Trial, stage.Definition.AcceptanceCriteria); err != nil {
		return complexExecutionRule(err.Error())
	}
	if len(result.Amendments) != len(mappings) {
		return complexExecutionRule("a project execution change must revalidate all affected tasks")
	}
	for _, a := range result.Amendments {
		if !reflect.DeepEqual(a.ExecutionBasis, basis) {
			return complexExecutionRule("all affected tasks must use the same execution agreement")
		}
	}
	return nil
}

// plannerRevisionLimitRule marks a repair-round or repair-budget refusal as a
// limit stop: a human may grant it through the coordination repair offer.
func plannerRevisionLimitRule(message string) error {
	return &core.RuleError{Code: core.ReasonPlannerRuntimeBudget, Message: message}
}

func validatePlannerProjectTaskRework(ctx context.Context, q *gen.Queries, runID, mappingID string) error {
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mappingID)
	if err != nil {
		return err
	}
	if item.State == "PLANNED" && item.ReworkCount == 0 {
		return nil
	}
	unpublished, err := unpublishedFailureCoordination(ctx, q, runID, mappingID)
	if err != nil {
		return err
	}
	if item.State != "BLOCKED" && item.State != "REVIEW" && item.State != "REWORK" && (!unpublished || item.State != "NEEDS_HUMAN") {
		return complexExecutionRule("engineering revision requires a settled repairable task")
	}
	granted := int64(0)
	budgets, err := q.ListClearDevComplexExceptionBudgets(ctx, runID)
	if err != nil {
		return err
	}
	for _, budget := range budgets {
		if budget.RoleKind == "BUILDER" && budget.ComplexExecutionTaskID.Valid && budget.ComplexExecutionTaskID.String == mappingID && budget.AuthorizedExtraTurns > granted {
			granted = budget.AuthorizedExtraTurns
		}
	}
	// An approved human coordination repair grant is durable permission to
	// finish this task's repairs: the repair-round and repair-budget gates
	// step aside. The task's own rework limit and every structural check
	// still apply.
	repaired, err := coordinationRepairGrantedForTask(ctx, q, runID, mappingID, item.ID)
	if err != nil {
		return err
	}
	if !repaired {
		if item.ReworkCount >= item.MaxReworkCount || item.ReworkCount >= 6+granted {
			return plannerRevisionLimitRule("engineering revision exhausted its existing task rounds")
		}
	} else if item.ReworkCount >= item.MaxReworkCount {
		return complexExecutionRule("engineering revision exhausted its existing task rounds")
	}
	available, err := builderRepairBudgetAvailable(ctx, q, runID, mappingID, item.ReworkCount+1)
	if err != nil {
		return err
	}
	if !available && !repaired {
		return plannerRevisionLimitRule("engineering revision exhausted its existing Builder budget")
	}
	return nil
}

func reworkPlannerProjectTask(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, mappingID, eventID string, at time.Time) error {
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mappingID)
	if err != nil {
		return err
	}
	if item.State == "PLANNED" {
		return nil
	}
	unpublished, err := unpublishedFailureCoordination(ctx, q, run.ID, mappingID)
	if err != nil {
		return err
	}
	if unpublished {
		// A new engineering packet is saved, but the task remains stopped.
		// Existing workflow recovery must seal the pending files, recheck the
		// original source and debit the actual next Builder round before send.
		return nil
	}
	if item.ReworkCount >= item.MaxReworkCount {
		return complexExecutionRule("Planner revision cannot reset task rework budget")
	}
	repaired, err := coordinationRepairGrantedForTask(ctx, q, run.ID, mappingID, item.ID)
	if err != nil {
		return err
	}
	if !repaired {
		available, err := builderRepairBudgetAvailable(ctx, q, run.ID, mappingID, item.ReworkCount+1)
		if err != nil {
			return err
		}
		if !available {
			return complexExecutionRule("Planner revision cannot reset Builder message budget")
		}
	}
	if item.State != "BLOCKED" && item.State != "REVIEW" && item.State != "REWORK" {
		return complexExecutionRule("engineering rework requires a settled task")
	}
	rows, err := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount, NextState: "RUNNING", ReworkCount: item.ReworkCount + 1, UpdatedAt: at})
	if err != nil {
		return err
	}
	if rows != 1 {
		return complexExecutionRule("engineering rework lost its current task")
	}
	requirement, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
	if err != nil {
		return err
	}
	return insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: run.DevelopmentProjectID, SubjectType: core.SubjectDevelopmentTask, SubjectID: item.ID, Action: core.ActionRestartDevelopmentTask, PreviousState: item.State, TargetState: "RUNNING", Outcome: core.EventAccepted, ReasonText: "planner-engineering:" + eventID, Source: core.EventSourceControlPlane, CreatedAt: at})
}

func projectPlannerTaskReturned(ctx context.Context, q *gen.Queries, attempt gen.CleardevComplexExecutionTaskAttempt) (bool, error) {
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, attempt.TaskMappingID)
	if err != nil || item.State != "RUNNING" || item.ReworkCount != attempt.Round+1 {
		return false, err
	}
	amendments, err := q.ListClearDevPlannerRuntimeTaskAmendments(ctx, attempt.ExecutionRunID)
	if err != nil {
		return false, err
	}
	for _, a := range amendments {
		if a.TaskMappingID != attempt.TaskMappingID {
			continue
		}
		var pkg core.ComplexStandardExecutionPackage
		if json.Unmarshal([]byte(a.TaskPacketJson), &pkg) != nil || pkg.ProjectExecution == nil {
			continue
		}
		// Applied in the same transaction after the stopped attempt settled.
		if pkg.RuntimeRevision != nil && pkg.RuntimeRevision.FirstRound == int(attempt.Round)+1 && attempt.SettledAt.Valid && !a.CreatedAt.Before(attempt.SettledAt.Time) {
			return true, nil
		}
	}
	return false, nil
}

func plannerProjectQuiescent(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun) (bool, error) {
	if !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) {
		return true, nil
	}
	steps, err := q.ListClearDevComplexExceptionAgentSteps(ctx, run.ID)
	if err != nil {
		return false, err
	}
	for _, step := range steps {
		if step.SendStatus == "PENDING" || step.SendStatus == "SENT" {
			return false, nil
		}
	}
	checks, err := q.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
	if err != nil {
		return false, err
	}
	for _, check := range checks {
		if check.Status == "PENDING" || check.Status == "STARTED" {
			return false, nil
		}
	}
	review, err := q.GetClearDevRequirementFinalReview(ctx, run.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil && review.Status != "SETTLED" && review.Status != "FAILED" {
		return false, nil
	}
	bindings, err := q.ListClearDevComplexExecutionRoleBindings(ctx, run.ID)
	if err != nil {
		return false, err
	}
	for _, binding := range bindings {
		if binding.Role != "BUILDER" || binding.Status != "BOUND" {
			continue
		}
		session, err := q.GetSession(ctx, domain.SessionID(binding.AoSessionID.String))
		if err != nil {
			return false, err
		}
		if session.IsTerminated || session.ActivityState != domain.ActivityIdle || session.WorkspacePath != binding.WorkspacePath {
			return false, nil
		}
	}
	return true, nil
}

func validateProjectAttemptRevision(ctx context.Context, q *gen.Queries, dispatchID string) error {
	a, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, dispatchID)
	if err != nil {
		return err
	}
	task, err := q.GetClearDevPlannerRuntimeEffectiveTask(ctx, a.TaskMappingID)
	if err != nil {
		return err
	}
	pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.TaskPacketJson))
	if err != nil {
		return err
	}
	if pkg.ProjectExecution != nil && pkg.RuntimeRevision != nil && int(a.Round) < pkg.RuntimeRevision.FirstRound {
		return complexExecutionRule("old task verification cannot satisfy the new engineering agreement")
	}
	return nil
}
