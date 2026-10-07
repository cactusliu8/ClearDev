package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func insertPlannerRuntimeControlStop(ctx context.Context, q *gen.Queries, event core.PlannerCoordinationEvent, outcome string, reason core.ReasonCode, summary string, proposed []byte, at time.Time) error {
	if outcome != "STOP" && outcome != "STALE" && outcome != "LIMIT_REACHED" {
		return complexExecutionRule("the control plane cannot impersonate a Planner engineering decision")
	}
	var resultJSON, digest sql.NullString
	if len(proposed) != 0 {
		resultJSON = nullableString(string(proposed))
		digest = nullableString(complexExecutionRawDigest(proposed))
	}
	if request, err := q.GetClearDevPlannerRuntimeRequest(ctx, event.ID); err == nil {
		if _, err := q.FailClearDevComplexAgentStepCAS(ctx, gen.FailClearDevComplexAgentStepCASParams{
			ID: request.AgentStepID, ReasonCode: string(reason), FailedAt: nullableTime(at),
		}); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return insertCurrentPlannerRuntimeDecision(ctx, q, gen.InsertClearDevPlannerRuntimeDecisionParams{
		EventID: event.ID, ExecutionRunID: event.ExecutionRunID, Source: "CONTROL_PLANE", Outcome: outcome,
		ReasonCode: string(reason), ResultJson: resultJSON, ResultSha256: digest, Summary: summary, CreatedAt: at,
	})
}

// StopClearDevPlannerRuntime records a technical inability to obtain a safe
// decision. It does not create a Planner verdict or grant product authority.
func (s *Store) StopClearDevPlannerRuntime(ctx context.Context, eventID string, reason core.ReasonCode, summary string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "stop Planner runtime coordination", func(q *gen.Queries) error {
		if _, err := currentPlannerRuntimeDecision(ctx, q, eventID); err == nil {
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if reason != core.ReasonPlannerRuntimeUnavailable && reason != core.ReasonPlannerRuntimeStopped && reason != core.ReasonPlannerRuntimeStale {
			return complexExecutionRule("unsupported control-plane runtime stop reason")
		}
		row, err := q.GetClearDevPlannerRuntimeEvent(ctx, eventID)
		if err != nil {
			return err
		}
		event, err := plannerRuntimeEventFromGen(row)
		if err != nil {
			return err
		}
		if err := insertPlannerRuntimeControlStop(ctx, q, event, "STOP", reason, summary, nil, at); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// ApplyClearDevPlannerRuntime reads the actual settled Planner reply from its
// reserved controlled step. Callers cannot pass replacement JSON, stale hashes
// or an approval flag. Every check and all amendments share this transaction.
func (s *Store) ApplyClearDevPlannerRuntime(ctx context.Context, eventID string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "apply bounded Planner runtime decision", func(q *gen.Queries) error {
		if _, err := currentPlannerRuntimeDecision(ctx, q, eventID); err == nil {
			return nil // Immutable exact event replay cannot apply twice.
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		row, err := q.GetClearDevPlannerRuntimeEvent(ctx, eventID)
		if err != nil {
			return err
		}
		event, err := plannerRuntimeEventFromGen(row)
		if err != nil {
			return err
		}
		request, err := q.GetClearDevPlannerRuntimeRequest(ctx, eventID)
		if err != nil {
			return err
		}
		step, err := q.GetClearDevComplexAgentStep(ctx, request.AgentStepID)
		if err != nil {
			return err
		}
		if step.SendStatus != "SETTLED" || step.RoleBindingID != request.PlannerRoleBindingID || step.RequestID != event.ID ||
			step.StepKind != string(core.ComplexAgentStepEngineeringPlan) ||
			step.PromptSha256 != complexExecutionRawDigest([]byte(request.Prompt)) ||
			step.MessageSha256.String != complexExecutionRawDigest([]byte(step.FinalMessageText.String)) {
			return complexExecutionRule("runtime decision is not the exact settled Planner controlled step")
		}
		result, canonical, digest, err := core.ParsePlannerCoordinationResult([]byte(step.FinalMessageText.String), event, request.ContextSha256)
		if err != nil {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, event.ExecutionRunID)
		if err != nil {
			return err
		}
		current, err := plannerRuntimeCurrent(ctx, q, run)
		if err != nil {
			return err
		}
		contextJSON, err := plannerRuntimeContext(ctx, q, run, event)
		if err != nil {
			return err
		}
		if !current || request.ContextJson != string(contextJSON) || request.ContextSha256 != complexExecutionRawDigest(contextJSON) {
			changed = true
			return insertPlannerRuntimeControlStop(ctx, q, event, "STALE", core.ReasonPlannerRuntimeStale,
				"The actual Planner reply was retained but not applied: its requirement, contract, candidate or execution context changed.", canonical, at)
		}
		if result.Decision == core.PlannerRuntimeContinue || result.Decision == core.PlannerRuntimeAmend {
			active, err := q.CountClearDevPlannerRuntimeActiveAttempts(ctx, run.ID)
			if err != nil {
				return err
			}
			stops, err := q.CountClearDevPlannerRuntimeUnresolvedStops(ctx, run.ID)
			if err != nil {
				return err
			}
			ready, readyErr := plannerProjectQuiescent(ctx, q, run)
			if readyErr != nil {
				return readyErr
			}
			bridge, bridgeErr := q.GetClearDevFailureCoordinationSource(ctx, event.ID)
			if bridgeErr != nil && !errors.Is(bridgeErr, sql.ErrNoRows) {
				return bridgeErr
			}
			boundedDiagnosis := bridgeErr == nil && bridge.DispatchID == event.DispatchID && bridge.ExecutionRunID == run.ID
			if !ready || active != 0 || stops != 0 && (!core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) || result.Decision != core.PlannerRuntimeAmend && !boundedDiagnosis) {
				changed = true
				return insertPlannerRuntimeControlStop(ctx, q, event, "STOP", core.ReasonPlannerRuntimeStopped,
					"Planner coordination cannot reopen an in-flight or stopped task or consume an unapproved recovery attempt. The proposed decision was not applied.", canonical, at)
			}
		}
		if result.Decision == core.PlannerRuntimeAmend {
			if err := validateFailurePlannerAuthority(ctx, q, run, event, result); err != nil {
				var rule *core.RuleError
				if !errors.As(err, &rule) {
					return err
				}
				changed = true
				return insertPlannerRuntimeControlStop(ctx, q, event, "STOP", core.ReasonPlannerRuntimeStopped, "The proposed repair changes frozen path authority. A human decision is required; the original contract and failure were retained.", canonical, at)
			}
		}
		if err := validateProjectPlannerProposal(ctx, q, run, result); err != nil {
			var rule *core.RuleError
			if !errors.As(err, &rule) {
				return err
			}
			changed = true
			return insertPlannerRuntimeControlStop(ctx, q, event, "STOP", core.ReasonPlannerRuntimeStopped, err.Error(), canonical, at)
		}
		amendments := make([]gen.InsertClearDevPlannerRuntimeTaskAmendmentParams, 0, len(result.Amendments))
		for _, proposed := range result.Amendments {
			amendment, err := preparePlannerTaskAmendment(ctx, q, run.ID, event.ID, digest, proposed, at)
			if err != nil {
				var rule *core.RuleError
				if !errors.As(err, &rule) {
					return err
				}
				changed = true
				if err := insertPlannerRuntimeControlStop(ctx, q, event, "STOP", core.ReasonPlannerRuntimeStopped,
					"The proposed remaining-work revision is outside the safe contract boundary: "+err.Error(), canonical, at); err != nil {
					return err
				}
				// A repair-round or repair-budget refusal is a permission
				// stop: ask the human before the run stays parked. The
				// recorded STOP and the preserved proposal both stay.
				if rule.Code == core.ReasonPlannerRuntimeBudget {
					return insertCoordinationRepairRequest(ctx, q, run, event, proposed.TaskKey, at)
				}
				return nil
			}
			amendments = append(amendments, amendment)
		}
		reason := core.ReasonNone
		switch result.Decision {
		case core.PlannerRuntimeProduct:
			reason = core.ReasonProductClarificationRequired
		case core.PlannerRuntimeStop:
			reason = core.ReasonPlannerRuntimeStopped
		}
		if err := insertCurrentPlannerRuntimeDecision(ctx, q, gen.InsertClearDevPlannerRuntimeDecisionParams{
			EventID: event.ID, ExecutionRunID: run.ID, Source: "PLANNER", Outcome: result.Decision, ReasonCode: string(reason),
			ResultJson: nullableString(string(canonical)), ResultSha256: nullableString(digest), Summary: result.Summary, CreatedAt: at,
		}); err != nil {
			return err
		}
		for _, amendment := range amendments {
			if err := q.InsertClearDevPlannerRuntimeTaskAmendment(ctx, amendment); err != nil {
				return err // Rolls back the decision as well; no partial application.
			}
		}
		if core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) {
			for _, amendment := range amendments {
				if err := reworkPlannerProjectTask(ctx, q, run, amendment.TaskMappingID, event.ID, at); err != nil {
					return err
				}
			}
		}
		changed = true
		return nil
	})
	return changed, err
}

func preparePlannerTaskAmendment(ctx context.Context, q *gen.Queries, runID, eventID, decisionSHA string, proposed core.PlannerRemainingAmendment, at time.Time) (gen.InsertClearDevPlannerRuntimeTaskAmendmentParams, error) {
	var out gen.InsertClearDevPlannerRuntimeTaskAmendmentParams
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, runID)
	if err != nil {
		return out, err
	}
	var mapping gen.CleardevComplexExecutionTaskMapping
	for _, item := range mappings {
		if item.PlanTaskKey == proposed.TaskKey {
			mapping = item
			break
		}
	}
	if mapping.ID == "" {
		return out, complexExecutionRule("amendment references a task outside this execution")
	}
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mapping.ID)
	if err != nil {
		return out, err
	}
	attempts, err := q.CountClearDevPlannerRuntimeTaskAttempts(ctx, mapping.ID)
	if err != nil {
		return out, err
	}
	reservations, err := q.CountClearDevPlannerRuntimeTaskReservations(ctx, mapping.ID)
	if err != nil {
		return out, err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, runID)
	if err != nil {
		return out, err
	}
	project := core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run))
	if project {
		if err := validatePlannerProjectTaskRework(ctx, q, runID, mapping.ID); err != nil {
			return out, err
		}
	}
	if !project && (attempts != 0 || reservations != 0 || item.State != "PLANNED" || item.ReworkCount != 0) {
		return out, complexExecutionRule("task " + proposed.TaskKey + " has already started, reserved an attempt or stopped; its contract is immutable")
	}
	effective, err := q.GetClearDevPlannerRuntimeEffectiveTask(ctx, mapping.ID)
	if err != nil {
		return out, err
	}
	task := core.ComplexExecutionTask{ID: mapping.ID, ExecutionRunID: runID, TaskKey: mapping.PlanTaskKey,
		DevelopmentTaskID: mapping.WorkItemID, Ordinal: int(mapping.Ordinal), Status: core.DevelopmentTaskStatus(item.State),
		ReworkCount: int(item.ReworkCount), ExecutionPackageJSON: effective.TaskPacketJson, ExecutionPackageSHA256: effective.TaskPacketSha256}
	// The revision window extends by the repair rounds humans already
	// authorized for this task: every recorded extra budget turn and every
	// approved coordination repair grant (one repair pair each). This is the
	// same grant signal the round-bound triggers honor, so a historical stop
	// whose only obstacle is the window can still be offered a grant.
	grantedRounds := 0
	budgets, err := q.ListClearDevComplexExceptionBudgets(ctx, runID)
	if err != nil {
		return out, err
	}
	for _, budget := range budgets {
		if budget.RoleKind == "BUILDER" && budget.ComplexExecutionTaskID.Valid && budget.ComplexExecutionTaskID.String == mapping.ID {
			grantedRounds += int(budget.AuthorizedExtraTurns)
		}
	}
	grants, err := coordinationRepairGrants(ctx, q)
	if err != nil {
		return out, err
	}
	for _, grant := range grants {
		if grant.ExecutionRunID == runID && (grant.TaskID == mapping.ID || grant.TaskID == mapping.WorkItemID) {
			grantedRounds += 2
		}
	}
	var source *core.FailureCoordinationSource
	if task.Status == core.DevelopmentTaskStatusNeedsHuman {
		bridge, err := q.GetClearDevUnpublishedFailureCoordinationForTask(ctx, gen.GetClearDevUnpublishedFailureCoordinationForTaskParams{ExecutionRunID: runID, TaskMappingID: mapping.ID})
		if err != nil || bridge.EventID != eventID {
			return out, complexExecutionRule("unpublished revision lacks its exact failure source")
		}
		task.CurrentDispatchID = bridge.DispatchID
		source = &core.FailureCoordinationSource{EventID: bridge.EventID, ExecutionRunID: bridge.ExecutionRunID, DispatchID: bridge.DispatchID}
	}
	raw, digest, err := core.BuildPlannerAmendedTaskPackage(task, proposed, eventID, decisionSHA, grantedRounds, source)
	if err != nil {
		if errors.Is(err, core.ErrReviewCriteriaCeiling) || errors.Is(err, core.ErrRepairWindowExceeded) {
			return out, plannerRevisionLimitRule("engineering revision needs human-authorized repair rounds: " + err.Error())
		}
		return out, complexExecutionRule(fmt.Sprintf("task %s amendment is invalid: %v", proposed.TaskKey, err))
	}
	prior, err := q.ListClearDevPlannerRuntimeTaskAmendments(ctx, runID)
	if err != nil {
		return out, err
	}
	ordinal := 1
	for _, amendment := range prior {
		if amendment.TaskMappingID == mapping.ID {
			ordinal++
		}
	}
	if ordinal > core.PlannerRuntimeMaxRevisions {
		return out, complexExecutionRule("the task exhausted the frozen remaining-work revision limit")
	}
	return gen.InsertClearDevPlannerRuntimeTaskAmendmentParams{ID: eventID + ":" + mapping.ID, EventID: eventID,
		ExecutionRunID: runID, TaskMappingID: mapping.ID, Ordinal: int64(ordinal), PreviousPackageSha256: effective.TaskPacketSha256,
		TaskPacketJson: string(raw), TaskPacketSha256: digest, CreatedAt: at}, nil
}
