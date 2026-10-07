package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// capturePlannerRuntimeEvent runs inside Builder settlement's transaction.
// The caller cannot supply an event body or overwrite a previously captured
// report. A crash cannot expose settlement without its scheduling barrier.
func capturePlannerRuntimeEvent(ctx context.Context, q *gen.Queries, stepID string) error {
	attempt, err := q.GetClearDevPlannerRuntimeSourceAttempt(ctx, stepID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Reviewer and Steward settlements are not Builder reports.
	}
	if err != nil {
		return err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, attempt.ExecutionRunID)
	if err != nil {
		return err
	}
	enabled, err := core.PlannerRuntimeRun(complexExecutionRunFromGen(run))
	if err != nil || !enabled {
		return err // Old executions never query or acquire new-policy history.
	}
	step, err := q.GetClearDevComplexExecutionAgentStep(ctx, stepID)
	if err != nil {
		return err
	}
	if step.SendStatus != "SETTLED" || step.RoleBindingID != attempt.BuilderRoleBindingID || step.RequestID != attempt.ID ||
		step.StepKind != string(core.ComplexExecutionAgentStepBuilderTask) ||
		complexExecutionRawDigest([]byte(step.FinalMessageText.String)) != step.MessageSha256.String {
		return complexExecutionRule("runtime report lost its settled Builder message binding")
	}
	var envelope struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(step.FinalMessageText.String), &envelope); err != nil {
		return err
	}
	if envelope.Kind != "BUILDER_RESULT" {
		return nil // Preserve the existing scope-request protocol unchanged.
	}
	mapping, err := q.GetClearDevComplexExecutionTaskMapping(ctx, attempt.TaskMappingID)
	if err != nil {
		return err
	}
	result, report, err := core.ParsePlannerRuntimeBuilderResult([]byte(step.FinalMessageText.String), attempt.ID, mapping.WorkItemID, int(attempt.Round))
	if err != nil {
		return err
	}
	if report == nil && core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) && result.Outcome == "BLOCKED" {
		// Only bridge historical final-review repair diagnoses. Other legacy
		// BLOCKED outcomes retain their original manual recovery semantics.
		review, reviewErr := q.GetClearDevRequirementFinalReview(ctx, run.ID)
		if reviewErr != nil && !errors.Is(reviewErr, sql.ErrNoRows) {
			return reviewErr
		}
		if reviewErr == nil && review.Status == "SETTLED" && (review.Verdict.String == "BLOCKED" || review.Verdict.String == "REWORK") {
			// The project execution agreement is shared by every task. This is
			// Control Plane's derived scope, not additional Builder testimony.
			mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
			if err != nil {
				return err
			}
			keys := make([]string, 0, len(mappings))
			for _, task := range mappings {
				keys = append(keys, task.PlanTaskKey)
			}
			report = &core.PlannerCoordinationReport{Category: "ENGINEERING", Summary: "Builder reported a final-review blocker. Control Plane derived all tasks sharing this project execution agreement as potentially affected; assess a bounded engineering repair without changing the product goal.", Evidence: []string{result.Summary}, AffectedTaskKeys: keys}
		}
	}
	if report == nil {
		return nil
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return err
	}
	eventID := attempt.ID + ":planner-coordination"
	if prior, err := q.GetClearDevPlannerRuntimeEvent(ctx, eventID); err == nil {
		if prior.ExecutionRunID != run.ID || prior.DispatchID != attempt.ID || prior.SourceStepID != step.ID ||
			prior.SourceMessageSha256 != step.MessageSha256.String || prior.ReportJson != string(reportJSON) {
			return complexExecutionRule("runtime event replay does not match the original Builder report")
		}
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return q.InsertClearDevPlannerRuntimeEvent(ctx, gen.InsertClearDevPlannerRuntimeEventParams{
		ID: eventID, ExecutionRunID: run.ID, DispatchID: attempt.ID, SourceStepID: step.ID,
		SourceMessageSha256: step.MessageSha256.String, ReportJson: string(reportJSON), CreatedAt: step.CompletedAt.Time,
	})
}

func plannerRuntimeEventFromGen(row gen.CleardevPlannerRuntimeEvent) (core.PlannerCoordinationEvent, error) {
	event := core.PlannerCoordinationEvent{ID: row.ID, ExecutionRunID: row.ExecutionRunID, DispatchID: row.DispatchID,
		SourceStepID: row.SourceStepID, SourceMessageSHA256: row.SourceMessageSha256, CreatedAt: row.CreatedAt}
	if err := json.Unmarshal([]byte(row.ReportJson), &event.Report); err != nil {
		return event, fmt.Errorf("decode runtime observation: %w", err)
	}
	return event, nil
}

func plannerRuntimeRequestFromGen(row gen.CleardevPlannerRuntimeRequest) core.PlannerCoordinationRequest {
	return core.PlannerCoordinationRequest{EventID: row.EventID, ExecutionRunID: row.ExecutionRunID, Ordinal: int(row.Ordinal),
		AgentStepID: row.AgentStepID, PlannerRoleBindingID: row.PlannerRoleBindingID, AOSessionID: row.AoSessionID,
		ContextJSON: row.ContextJson, ContextSHA256: row.ContextSha256, Prompt: row.Prompt, CreatedAt: row.CreatedAt}
}

func plannerRuntimeDecisionFromGen(row gen.CleardevPlannerRuntimeDecision) core.PlannerCoordinationDecision {
	return core.PlannerCoordinationDecision{EventID: row.EventID, ExecutionRunID: row.ExecutionRunID, Source: row.Source,
		Outcome: row.Outcome, ReasonCode: core.ReasonCode(row.ReasonCode), ResultJSON: row.ResultJson.String,
		ResultSHA256: row.ResultSha256.String, Summary: row.Summary, CreatedAt: row.CreatedAt}
}

func loadPlannerRuntime(ctx context.Context, q *gen.Queries, run core.ComplexExecutionRun) (*core.PlannerRuntimeSnapshot, error) {
	enabled, err := core.PlannerRuntimeRun(run)
	if err != nil || !enabled {
		return nil, err
	}
	history := &core.PlannerRuntimeSnapshot{Events: []core.PlannerCoordinationEvent{}, Requests: []core.PlannerCoordinationRequest{},
		Decisions: []core.PlannerCoordinationDecision{}, Amendments: []core.PlannerTaskAmendment{}}
	events, err := q.ListClearDevPlannerRuntimeEvents(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	for _, row := range events {
		event, err := plannerRuntimeEventFromGen(row)
		if err != nil {
			return nil, err
		}
		history.Events = append(history.Events, event)
	}
	requests, err := q.ListClearDevPlannerRuntimeRequests(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	for _, row := range requests {
		if complexExecutionRawDigest([]byte(row.ContextJson)) != row.ContextSha256 {
			return nil, complexExecutionRule("runtime request context digest does not match its frozen bytes")
		}
		history.Requests = append(history.Requests, plannerRuntimeRequestFromGen(row))
	}
	decisions, err := q.ListClearDevPlannerRuntimeDecisions(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	for _, row := range decisions {
		if row.ResultJson.Valid && complexExecutionRawDigest([]byte(row.ResultJson.String)) != row.ResultSha256.String {
			return nil, complexExecutionRule("runtime decision digest does not match its frozen bytes")
		}
		history.Decisions = append(history.Decisions, plannerRuntimeDecisionFromGen(row))
	}
	amendments, err := q.ListClearDevPlannerRuntimeTaskAmendments(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	for _, row := range amendments {
		history.Amendments = append(history.Amendments, core.PlannerTaskAmendment{ID: row.ID, EventID: row.EventID,
			ExecutionRunID: row.ExecutionRunID, TaskMappingID: row.TaskMappingID, Ordinal: int(row.Ordinal),
			PreviousPackageSHA256: row.PreviousPackageSha256, ExecutionPackageJSON: row.TaskPacketJson,
			ExecutionPackageSHA256: row.TaskPacketSha256, CreatedAt: row.CreatedAt})
	}
	grants, err := coordinationRepairGrants(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, grant := range grants {
		if grant.ExecutionRunID == run.ID {
			history.RepairAuthorizations = append(history.RepairAuthorizations, grant.EventID)
		}
	}
	if err := loadPlannerCoordinationRecoveries(ctx, q, run.ID, history); err != nil {
		return nil, err
	}
	if err := loadStoppedCheckRecoveries(ctx, q, run.ID, history); err != nil {
		return nil, err
	}
	if err := loadExtraCoordination(ctx, q, run.ID, history); err != nil {
		return nil, err
	}
	return history, nil
}

// overlayPlannerRuntimePackages replays immutable additive revisions. It never
// changes persisted task state, attempts or original packets. SQL separately
// binds each revision to its decision and first applicable dispatch round.
func overlayPlannerRuntimePackages(snapshot *core.ComplexExecutionSnapshot) error {
	if snapshot.PlannerRuntime == nil {
		return nil
	}
	for _, amendment := range snapshot.PlannerRuntime.Amendments {
		index := -1
		for i := range snapshot.Tasks {
			if snapshot.Tasks[i].ID == amendment.TaskMappingID {
				index = i
				break
			}
		}
		if index < 0 || amendment.ExecutionRunID != snapshot.Run.ID {
			return complexExecutionRule("runtime amendment lost its original execution task")
		}
		task := &snapshot.Tasks[index]
		if task.ExecutionPackageSHA256 != amendment.PreviousPackageSHA256 {
			return complexExecutionRule("runtime revision predecessor changed")
		}
		decision, _ := core.PlannerRuntimeEffectiveDecision(snapshot.PlannerRuntime, amendment.EventID)
		if decision.Source != "PLANNER" || decision.Outcome != core.PlannerRuntimeAmend || complexExecutionRawDigest([]byte(decision.ResultJSON)) != decision.ResultSHA256 {
			return complexExecutionRule("runtime revision lost its Planner decision")
		}
		var result core.PlannerCoordinationResult
		if err := json.Unmarshal([]byte(decision.ResultJSON), &result); err != nil {
			return err
		}
		found := false
		for _, proposed := range result.Amendments {
			if proposed.TaskKey != task.TaskKey {
				continue
			}
			replay := *task
			replay.Status, replay.CurrentRound, replay.CurrentDispatchID, replay.ReworkCount = core.DevelopmentTaskStatusPlanned, 0, "", 0
			var saved core.ComplexStandardExecutionPackage
			if json.Unmarshal([]byte(amendment.ExecutionPackageJSON), &saved) != nil {
				return complexExecutionRule("invalid saved revision")
			}
			if saved.RuntimeRevision != nil && saved.RuntimeRevision.FirstRound > 0 {
				replay.Status, replay.ReworkCount = core.DevelopmentTaskStatusReview, saved.RuntimeRevision.FirstRound-1
			}
			replayExtension := 0
			if saved.RuntimeRevision != nil {
				replayExtension = saved.RuntimeRevision.HumanRepairExtension
			}
			raw, digest, err := core.BuildPlannerAmendedTaskPackage(replay, proposed, amendment.EventID, decision.ResultSHA256, replayExtension)
			if err != nil || string(raw) != amendment.ExecutionPackageJSON || digest != amendment.ExecutionPackageSHA256 {
				return complexExecutionRule("runtime revision differs from the exact bounded Planner proposal")
			}
			found = true
		}
		if !found {
			return complexExecutionRule("runtime revision has no reported task")
		}

		task.ExecutionPackageJSON, task.ExecutionPackageSHA256 = amendment.ExecutionPackageJSON, amendment.ExecutionPackageSHA256
	}
	for _, task := range snapshot.Tasks {
		pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
		if err != nil {
			return err
		}
		if pkg.ProjectExecution != nil && pkg.RuntimeRevision != nil {
			snapshot.Run.RuntimeProjectExecution = pkg.ProjectExecution
		}
	}

	return nil
}
