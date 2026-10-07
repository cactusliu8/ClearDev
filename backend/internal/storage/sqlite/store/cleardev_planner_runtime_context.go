package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// plannerRuntimeCurrent checks the current requirement, not merely the version
// that originally created the execution. Old replies cannot revive a cancelled,
// superseded or stopped Stage. It is always called on the transaction handle.
func plannerRuntimeCurrent(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun) (bool, error) {
	enabled, err := core.PlannerRuntimeRun(complexExecutionRunFromGen(run))
	if err != nil || !enabled || run.Status != "ACCEPTED" {
		return false, err
	}
	project, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
	if err != nil {
		return false, err
	}
	if project.CancelledAt.Valid || project.PausedFromState.Valid || project.State == "PAUSED" {
		return false, nil
	}
	version, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, run.DevelopmentProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if version.ID != run.RequirementVersionID || version.Sha256 != run.RequirementSha256 || version.TaskSetVersion != run.AcceptedTaskSetVersion {
		return false, nil
	}
	stopped, err := activeDirectionStop(ctx, q, version.ID)
	if err != nil || stopped {
		return false, err
	}
	intents, err := q.CountClearDevPlannerRuntimeDirectionIntents(ctx, version.ID)
	if err != nil || intents != 0 {
		return false, err
	}
	if review, err := q.GetClearDevRequirementFinalReview(ctx, run.ID); err == nil {
		if !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) || review.Status != "SETTLED" || (review.Verdict.String != "BLOCKED" && review.Verdict.String != "REWORK") {
			return false, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return true, nil
}

// plannerRuntimeContext is intentionally assembled from one SQLite write
// transaction, not a sequence of public snapshot reads. It includes historical
// failures and exact current packets, candidates, reviews, checks, budgets and
// permissions. Only the coordination request's own changing delivery state is
// excluded so sending/settling that request cannot invalidate itself.
func plannerRuntimeContext(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, event core.PlannerCoordinationEvent) ([]byte, error) {
	version, err := q.GetClearDevRequirementVersion(ctx, run.RequirementVersionID)
	if err != nil {
		return nil, err
	}
	plan, err := q.GetClearDevPlannerRuntimePlan(ctx, run.PlanID)
	if err != nil {
		return nil, err
	}
	if plan.PlanSha256 != run.PlanSha256 || plan.RequirementVersionID != run.RequirementVersionID ||
		complexExecutionRawDigest([]byte(plan.PlanJson)) != plan.PlanSha256 {
		return nil, complexExecutionRule("runtime context lost its immutable source plan")
	}
	planner, err := q.GetClearDevComplexRoleBinding(ctx, plan.PlannerRoleBindingID)
	if err != nil {
		return nil, err
	}
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	tasks := make([]map[string]any, 0, len(mappings))
	for _, mapping := range mappings {
		item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mapping.ID)
		if err != nil {
			return nil, err
		}
		effective, err := q.GetClearDevPlannerRuntimeEffectiveTask(ctx, mapping.ID)
		if err != nil {
			return nil, err
		}
		if complexExecutionRawDigest([]byte(effective.TaskPacketJson)) != effective.TaskPacketSha256 {
			return nil, complexExecutionRule("runtime context task packet hash changed")
		}
		tasks = append(tasks, map[string]any{"mappingId": mapping.ID, "taskKey": mapping.PlanTaskKey, "state": item.State,
			"reworkCount": item.ReworkCount, "originalPackageSha256": mapping.TaskPacketSha256,
			"effectivePackageSha256": effective.TaskPacketSha256, "effectivePackage": json.RawMessage(effective.TaskPacketJson)})
	}
	attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	candidates := []gen.CleardevCandidateCommit{}
	for _, attempt := range attempts {
		candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(attempt.ID))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	checks, err := q.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	specs, err := q.ListClearDevComplexExecutionCheckSpecs(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	reviewRows, err := q.ListClearDevComplexExecutionReviews(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	reviews := make([]map[string]any, 0, len(reviewRows))
	for _, review := range reviewRows {
		reviews = append(reviews, map[string]any{"id": review.ID, "dispatchId": review.TaskAttemptID,
			"candidateId": review.CandidateCommitID, "reviewPacketSha256": review.ReviewPacketSha256,
			"status": review.Status, "verdict": review.Verdict.String, "reasonCode": review.ReasonCode,
			"summary": review.Summary.String, "agentStepId": review.AgentStepID})
	}
	verified, err := q.ListClearDevComplexExecutionVerifiedCandidates(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	bindings, err := q.ListClearDevComplexExecutionRoleBindings(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	batches, err := q.ListClearDevComplexExecutionBatches(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	compositions, err := q.ListClearDevComplexExecutionCompositions(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	exceptions, err := loadComplexExceptionFacts(ctx, q, run.ID)
	if err != nil {
		return nil, err
	}
	recoveries, err := fixedRecoveryEvidence(ctx, q, run.ID)
	if err != nil {
		return nil, err
	}
	history, err := loadPlannerRuntime(ctx, q, complexExecutionRunFromGen(run))
	if err != nil {
		return nil, err
	}
	contextFacts := map[string]any{
		"schemaVersion": 1, "event": event, "run": complexExecutionRunFromGen(run), "runStatus": run.Status,
		"requirement": clearDevRequirementVersionFromGen(version), "originalPlan": json.RawMessage(plan.PlanJson),
		"logicalPlanner": clearDevComplexRoleBindingFromGen(planner), "tasks": tasks, "dispatchHistory": attempts,
		"trustedCandidates": candidates, "trustedChecks": checks, "checkSpecifications": specs, "taskReviews": reviews,
		"verifiedCandidates": verified, "executionRoleBindings": bindings, "batches": batches, "compositions": compositions,
		"exceptionFacts": exceptions, "fixedRecoveries": recoveries, "priorDecisions": history.Decisions, "priorAmendments": history.Amendments,
	}
	if core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) {
		finalReview, err := q.GetClearDevRequirementFinalReview(ctx, run.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		contextFacts["finalReview"] = finalReview
	}
	plannerRecoveryContextHistory(history, event.ID, contextFacts)
	return json.Marshal(contextFacts)
}

// PrepareClearDevPlannerRuntime atomically freezes the complete request and
// reserves one business round. It returns an existing exact request on replay;
// a stale context or exhausted quota is recorded as a stop, never regenerated.
func (s *Store) PrepareClearDevPlannerRuntime(ctx context.Context, eventID string, at time.Time) (core.PlannerCoordinationRequest, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var request core.PlannerCoordinationRequest
	var changed bool
	err := s.inTx(ctx, "prepare bounded Planner runtime coordination", func(q *gen.Queries) error {
		row, err := q.GetClearDevPlannerRuntimeEvent(ctx, eventID)
		if err != nil {
			return err
		}
		event, err := plannerRuntimeEventFromGen(row)
		if err != nil {
			return err
		}
		if _, err := currentPlannerRuntimeDecision(ctx, q, eventID); err == nil {
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
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
		if !current {
			changed = true
			return insertPlannerRuntimeControlStop(ctx, q, event, "STALE", core.ReasonPlannerRuntimeStale,
				"The confirmed requirement, execution or direction changed before coordination; no old decision was applied.", nil, at)
		}
		active, err := q.CountClearDevPlannerRuntimeActiveAttempts(ctx, run.ID)
		if err != nil || active != 0 {
			return err // Existing work must first reach its ordinary durable boundary.
		}
		ready, err := plannerProjectQuiescent(ctx, q, run)
		if err != nil || !ready {
			return err
		}
		contextJSON, err := plannerRuntimeContext(ctx, q, run, event)
		if err != nil {
			return err
		}
		contextSHA := complexExecutionRawDigest(contextJSON)
		if prior, err := q.GetClearDevPlannerRuntimeRequest(ctx, eventID); err == nil {
			if prior.ContextJson != string(contextJSON) || prior.ContextSha256 != contextSHA {
				changed = true
				return insertPlannerRuntimeControlStop(ctx, q, event, "STALE", core.ReasonPlannerRuntimeStale,
					"Trusted candidate, task, review, recovery or execution facts changed after the frozen coordination request; it cannot be reused.", nil, at)
			}
			request = plannerRuntimeRequestFromGen(prior)
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		priorRequests, err := q.ListClearDevPlannerRuntimeRequests(ctx, run.ID)
		if err != nil {
			return err
		}
		var extraOffer *gen.CleardevExtraCoordinationRequest
		if len(priorRequests) == core.PlannerRuntimeMaxRounds {
			if _, grantErr := q.GetClearDevExtraCoordinationGrant(ctx, event.ID); grantErr == nil {
				offer, err := q.GetClearDevExtraCoordinationRequestForEvent(ctx, event.ID)
				if err != nil {
					return err
				}
				b, err := core.ParseExtraCoordinationBinding([]byte(offer.BindingJson))
				if err != nil {
					return err
				}
				proof, err := extraCoordinationEvidence(ctx, q, run.DevelopmentProjectID, false)
				if err != nil {
					return err
				}
				if proof.state.Binding != b || proof.state.Option.UnavailableReason != "" || offer.ContextJson != string(contextJSON) {
					changed = true
					return insertPlannerRuntimeControlStop(ctx, q, event, "STALE", core.ReasonPlannerRuntimeStale, "The exact approved coordination context changed; no additional message was sent.", nil, at)
				}
				extraOffer = &offer
			} else if !errors.Is(grantErr, sql.ErrNoRows) {
				return grantErr
			}
		}
		if len(priorRequests) >= core.PlannerRuntimeMaxRounds && extraOffer == nil {
			changed = true
			return insertPlannerRuntimeControlStop(ctx, q, event, "LIMIT_REACHED", core.ReasonPlannerRuntimeBudget,
				"The execution exhausted its frozen Planner coordination rounds; restarting or changing sessions cannot create another round.", nil, at)
		}
		plan, err := q.GetClearDevPlannerRuntimePlan(ctx, run.PlanID)
		if err != nil {
			return err
		}
		planner, err := q.GetClearDevComplexRoleBinding(ctx, plan.PlannerRoleBindingID)
		if err != nil {
			return err
		}
		if planner.Role != string(core.StandardRoleEngineeringPlanner) || planner.Status != "BOUND" || !planner.AoSessionID.Valid {
			changed = true
			return insertPlannerRuntimeControlStop(ctx, q, event, "STOP", core.ReasonPlannerRuntimeUnavailable,
				"The original Stage Planner binding is unavailable; this execution cannot silently allocate a different Planner or new quota.", nil, at)
		}
		ordinal := len(priorRequests) + 1
		stepID := event.ID + ":planner"
		prompt := core.BuildPlannerRuntimePrompt(event, string(contextJSON), contextSHA, ordinal)
		if core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) {
			prompt = core.BuildProjectPlannerRuntimePrompt(event, string(contextJSON), contextSHA, ordinal)
		}
		if extraOffer != nil {
			prompt = extraOffer.Prompt
		}
		request = core.PlannerCoordinationRequest{EventID: event.ID, ExecutionRunID: run.ID, Ordinal: ordinal, AgentStepID: stepID,
			PlannerRoleBindingID: planner.ID, AOSessionID: planner.AoSessionID.String, ContextJSON: string(contextJSON),
			ContextSHA256: contextSHA, Prompt: prompt, CreatedAt: at}
		if err := q.InsertClearDevPlannerRuntimeRequest(ctx, gen.InsertClearDevPlannerRuntimeRequestParams{
			EventID: request.EventID, ExecutionRunID: request.ExecutionRunID, Ordinal: int64(ordinal), AgentStepID: stepID,
			PlannerRoleBindingID: planner.ID, AoSessionID: planner.AoSessionID.String, ContextJson: request.ContextJSON,
			ContextSha256: contextSHA, Prompt: prompt, CreatedAt: at,
		}); err != nil {
			return err
		}
		step := core.AgentStep{ID: stepID, RoleBindingID: planner.ID, Kind: core.ComplexAgentStepEngineeringPlan, RequestID: event.ID,
			ClientMessageID: "cleardev-complex-step-" + stepID, PromptSHA256: complexExecutionRawDigest([]byte(prompt)),
			SendStatus: core.AgentStepSendStatusPending, RequestedAt: at}
		if err := insertClearDevComplexAgentStep(ctx, q, step); err != nil {
			return fmt.Errorf("reserve runtime Planner controlled step: %w", err)
		}
		changed = true
		return nil
	})
	return request, changed, err
}
