package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// CaptureClearDevFailureCoordination appends an explicitly control-derived
// source bridge. It never edits a Builder report to pretend coordination was
// requested there, and it never unblocks a task or adds a role/message budget.
func (s *Store) CaptureClearDevFailureCoordination(ctx context.Context, runID, dispatchID, workingDigest string, at time.Time) (bool, error) {
	if !validSHA256Digest(workingDigest) {
		return false, complexExecutionRule("failure coordination needs a current workspace receipt")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	changed := false
	err := s.inTx(ctx, "capture original failure engineering escalation", func(q *gen.Queries) error {
		eventID := dispatchID + ":planner-coordination"
		if _, err := q.GetClearDevPlannerRuntimeEvent(ctx, eventID); err == nil {
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, runID)
		if err != nil {
			return err
		}
		current, err := plannerRuntimeCurrent(ctx, q, run)
		if err != nil || !current || !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) {
			return err
		}
		events, err := q.ListClearDevPlannerRuntimeEvents(ctx, runID)
		if err != nil {
			return err
		}
		for _, event := range events {
			decision, err := currentPlannerRuntimeDecision(ctx, q, event.ID)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if decision.Outcome != core.PlannerRuntimeContinue && decision.Outcome != core.PlannerRuntimeAmend {
				return nil
			}
		}
		a, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, dispatchID)
		if err != nil {
			return err
		}
		if a.ExecutionRunID != runID || !a.SettledAt.Valid {
			return complexExecutionRule("failure coordination source is not settled")
		}
		step, err := q.GetClearDevComplexExecutionAgentStep(ctx, a.AgentStepID)
		if err != nil {
			return err
		}
		mapping, err := q.GetClearDevComplexExecutionTaskMapping(ctx, a.TaskMappingID)
		if err != nil {
			return err
		}
		result, _, err := core.ParsePlannerRuntimeBuilderResult([]byte(step.FinalMessageText.String), a.ID, mapping.WorkItemID, int(a.Round))
		if err != nil || step.SendStatus != "SETTLED" || step.MessageSha256.String != complexExecutionRawDigest([]byte(step.FinalMessageText.String)) {
			return complexExecutionRule("failure escalation requires the original valid settled Builder result")
		}
		kind, receiptID := "BUILDER_DIAGNOSIS", ""
		facts := []string{result.Summary}
		if a.ReasonCode == "CANDIDATE_INVALID" && a.Status == "NEEDS_HUMAN" && result.Outcome == "CANDIDATE_READY" {
			kind = "REPEATED_HANDOFF"
			receiptID = core.FailureSourceKey(run.DevelopmentProjectID, core.FailureSourceHandoff, a.ID)
			row, err := q.GetClearDevWorkflowFailureReceipt(ctx, receiptID)
			if err != nil {
				return err
			}
			receipt, err := decodeWorkflowFailure(row)
			if err != nil || !receipt.BeforePublish || receipt.ProblemCode != "NO_IMPLEMENTATION_CHANGE" && receipt.ProblemCode != "SOURCE_LIMIT" && receipt.ProblemCode != "HANDOFF_CONTENT" {
				return complexExecutionRule("unsafe or unproven handoff cannot be coordinated automatically")
			}
			if receipt.BindingSHA256 != core.HandoffFailureBinding(complexExecutionRunFromGen(run), core.ComplexExecutionTask{ID: mapping.ID}, complexExecutionDispatchFromGen(a), complexExecutionAgentStepFromGen(step)) {
				return complexExecutionRule("handoff failure source changed")
			}
			facts = append(facts, receipt.Summary, "Failure receipt: "+receipt.ID)
		} else if a.ReasonCode != "BUILDER_BLOCKED" || a.Status != "BLOCKED" || result.Outcome != "BLOCKED" {
			return complexExecutionRule("human decisions and other failures cannot acquire engineering authority")
		}
		mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, runID)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(mappings))
		for _, item := range mappings {
			keys = append(keys, item.PlanTaskKey)
		}
		if len(keys) == 0 || len(keys) > 3 {
			return complexExecutionRule("failure coordination exceeds the original task bounds")
		}
		facts = append(facts, "Original workspace receipt: "+workingDigest)
		report := core.PlannerCoordinationReport{Category: "ENGINEERING", Summary: "Control Plane routed an exact settled task failure to the original Planner. Diagnose the engineering agreement within its existing authority; permissions, paths and product decisions remain human-only. This is not a Builder-authored coordination report or proof of successful delivery.", Evidence: facts, AffectedTaskKeys: keys}
		raw, err := json.Marshal(report)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevFailureCoordinationSource(ctx, gen.InsertClearDevFailureCoordinationSourceParams{
			EventID: eventID, ExecutionRunID: runID, DispatchID: a.ID, SourceStepID: step.ID, SourceMessageSha256: step.MessageSha256.String,
			WorkingTreeSha256: workingDigest, FailureReceiptID: nullableString(receiptID), BridgeKind: kind, CreatedAt: at,
		}); err != nil {
			return err
		}
		if err := q.InsertClearDevPlannerRuntimeEvent(ctx, gen.InsertClearDevPlannerRuntimeEventParams{ID: eventID, ExecutionRunID: runID, DispatchID: a.ID, SourceStepID: step.ID, SourceMessageSha256: step.MessageSha256.String, ReportJson: string(raw), CreatedAt: at}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// GetClearDevFailureCoordinationSource reads the immutable original failure bridge.
func (s *Store) GetClearDevFailureCoordinationSource(ctx context.Context, eventID string) (core.FailureCoordinationSource, bool, error) {
	row, err := s.qr.GetClearDevFailureCoordinationSource(ctx, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.FailureCoordinationSource{}, false, nil
	}
	if err != nil {
		return core.FailureCoordinationSource{}, false, err
	}
	return core.FailureCoordinationSource{EventID: row.EventID, ExecutionRunID: row.ExecutionRunID, DispatchID: row.DispatchID, StepID: row.SourceStepID, MessageSHA256: row.SourceMessageSha256, WorkingTreeSHA256: row.WorkingTreeSha256}, true, nil
}

// No-candidate tasks retain their stop until the original recovery executor
// binds the dirty workspace and claims the actual Builder round atomically.
func unpublishedFailureCoordination(ctx context.Context, q *gen.Queries, runID, taskID string) (bool, error) {
	_, err := q.GetClearDevUnpublishedFailureCoordinationForTask(ctx, gen.GetClearDevUnpublishedFailureCoordinationForTaskParams{ExecutionRunID: runID, TaskMappingID: taskID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// An automatically routed repair may not use legacy path-completion exceptions.
// A Planner has broader engineering responsibility, not broader file authority.
func validateFailurePlannerAuthority(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, event core.PlannerCoordinationEvent, result core.PlannerCoordinationResult) error {
	automatic := false
	if _, err := q.GetClearDevFailureCoordinationSource(ctx, event.ID); err == nil {
		automatic = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !automatic {
		attempt, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, event.DispatchID)
		if err != nil {
			return err
		}
		history, err := q.ListClearDevWorkflowRecoveries(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, recovery := range history {
			if core.IsAutomaticFailureRecovery(recovery.ID) && recovery.TaskID == attempt.TaskMappingID && recovery.Action == core.RecoveryContinueBuilder {
				automatic = true
			}
		}
	}
	if !automatic {
		return nil
	}
	effective, err := effectiveProjectRun(ctx, q, run)
	if err != nil {
		return err
	}
	contract, project, err := core.ProjectContractFromRun(effective)
	if err != nil || !project {
		return complexExecutionRule("automatic engineering repair has no current project authority")
	}
	for _, amendment := range result.Amendments {
		if amendment.ExecutionBasis != nil && !slices.Equal(amendment.ExecutionBasis.WritePaths, contract.Basis.WritePaths) {
			return complexExecutionRule("automatic failure coordination cannot expand path authority")
		}
	}
	return nil
}
