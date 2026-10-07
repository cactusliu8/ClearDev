package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// coordinationRepairDisplay explains the narrowly bound human grant: one
// coordination repair round for one stopped engineering revision. The grant
// never rewrites the recorded STOP, other tasks, other budgets or history.
func coordinationRepairDisplay(budgetID string) core.HumanDecisionDisplay {
	return core.HumanDecisionDisplay{
		Title:   "Authorize one coordination repair round",
		Summary: "A Planner engineering revision reached a repair limit (rounds or review criteria).",
		FullContent: "The control plane preserved the exact Planner proposal but did not apply it. " +
			"Approving authorizes the bounded repair for the named task. If another task reported the blocker, " +
			"that source continues only within its existing budget; the already verified target is not rewritten. " +
			"Continuation still requires a valid Planner decision, a new candidate and independent checks. " +
			"This grants no new write paths or additional Planner coordination rounds. Original failures and other budgets stay unchanged.",
		ChangeSummary: "Authorize one coordination repair round on budget " + budgetID + " and apply the preserved revision of the stopped coordination event.",
	}
}

// coordinationRepairRequestParams resolves the exact task and builder budget
// one refused proposal named. The desktop names tasks by work item id.
func coordinationRepairRequestParams(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, event core.PlannerCoordinationEvent, taskKey string) (core.CoordinationRepairBinding, string, error) {
	var binding core.CoordinationRepairBinding
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return binding, "", err
	}
	mappingID, workItemID := "", ""
	for _, mapping := range mappings {
		if mapping.PlanTaskKey == taskKey {
			mappingID, workItemID = mapping.ID, mapping.WorkItemID
			break
		}
	}
	if mappingID == "" {
		return binding, "", complexExecutionRule("coordination repair offer names no task of this execution")
	}
	budgets, err := q.ListClearDevComplexExceptionBudgets(ctx, run.ID)
	if err != nil {
		return binding, "", err
	}
	budgetID := ""
	for _, budget := range budgets {
		if budget.RoleKind == core.ComplexExceptionBudgetBuilder && budget.ComplexExecutionTaskID.Valid && budget.ComplexExecutionTaskID.String == mappingID {
			budgetID = budget.ID
			break
		}
	}
	if budgetID == "" {
		return binding, "", complexExecutionRule("coordination repair offer found no builder budget for its task")
	}
	return core.CoordinationRepairBinding{DevelopmentRequirementID: run.DevelopmentProjectID, ExecutionRunID: run.ID,
		EventID: event.ID, TaskID: workItemID, BudgetID: budgetID}, budgetID, nil
}

// insertCoordinationRepairRequest records the pending human decision offer for
// one refused coordination revision. Dedupe keeps at most one pending offer
// per event; nothing is granted here.
func insertCoordinationRepairRequest(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, event core.PlannerCoordinationEvent, taskKey string, at time.Time) error {
	binding, budgetID, err := coordinationRepairRequestParams(ctx, q, run, event, taskKey)
	if err != nil {
		return err
	}
	// One offer per event ever: a rejected or approved grant is decided
	// history, and repeated derivations must not re-ask the human.
	prior, err := q.ListClearDevHumanDecisionRequestsByKind(ctx, core.HumanDecisionKindCoordinationRepair)
	if err != nil {
		return err
	}
	for _, row := range prior {
		if strings.Contains(row.BindingJson, event.ID) {
			return nil
		}
	}
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	normalized, err := core.ParseCoordinationRepairBinding(bindingJSON)
	if err != nil {
		return err
	}
	bindingJSON, err = json.Marshal(normalized)
	if err != nil {
		return err
	}
	display := coordinationRepairDisplay(budgetID)
	displayJSON, err := json.Marshal(display)
	if err != nil {
		return err
	}
	digest, err := core.HumanDecisionContentSHA256(core.HumanDecisionKindCoordinationRepair, bindingJSON, display)
	if err != nil {
		return err
	}
	requestID := uuid.NewString()
	if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{
		ID: requestID, DevelopmentProjectID: run.DevelopmentProjectID, DecisionKind: core.HumanDecisionKindCoordinationRepair,
		BindingSchemaVersion: 1, BindingJson: string(bindingJSON), DisplayJson: string(displayJSON), ContentSha256: digest, CreatedAt: at,
	}); err != nil {
		if isUniqueConstraint(err) {
			return nil
		}
		return err
	}
	requirement, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
	if err != nil {
		return err
	}
	return insertClearDevEvent(ctx, q, core.RequirementEvent{
		AOProjectID: requirement.AoProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: core.SubjectHumanDecisionRequest, SubjectID: requestID,
		Action: core.ActionCreateHumanDecisionRequest, TargetState: string(core.HumanDecisionRequestPending),
		Outcome: core.EventAccepted, Source: core.EventSourceControlPlane, CreatedAt: at,
	})
}

// ensureCoordinationRepairRequests backfills the repair offer for historical
// coordination stops recorded before the offer existed. A stop qualifies when
// its preserved proposal still prepares cleanly, or refuses only on the repair
// round or budget limit the grant itself resolves. Anything else keeps its
// stop and never asks the human for authority it cannot use.
func ensureCoordinationRepairRequests(ctx context.Context, q *gen.Queries, at time.Time) error {
	// Backfill is bounded by recorded coordination stops; it never dispatches.
	rows, err := q.ListClearDevEffectivePlannerStoppedEvents(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		event, err := plannerRuntimeEventFromGen(row)
		if err != nil {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, event.ExecutionRunID)
		if err != nil {
			return err
		}
		_, result, taskKey, decisionSHA, ok := plannerRuntimeRepairFacts(ctx, q, event.ID)
		if !ok {
			continue
		}
		applicable, limitOnly := plannerRuntimeRepairApplicable(ctx, q, run, event, result, decisionSHA)
		if !applicable && !limitOnly {
			continue
		}
		if err := insertCoordinationRepairRequest(ctx, q, run, event, taskKey, at); err != nil {
			return err
		}
	}
	return nil
}

// plannerRuntimeRepairFacts re-reads one stopped coordination event and its
// preserved Planner proposal. The recorded STOP stays untouched.
func plannerRuntimeRepairFacts(ctx context.Context, q *gen.Queries, eventID string) (core.PlannerCoordinationEvent, core.PlannerCoordinationResult, string, string, bool) {
	var event core.PlannerCoordinationEvent
	var result core.PlannerCoordinationResult
	row, err := q.GetClearDevPlannerRuntimeEvent(ctx, eventID)
	if err != nil {
		return event, result, "", "", false
	}
	if event, err = plannerRuntimeEventFromGen(row); err != nil {
		return event, result, "", "", false
	}
	decision, err := currentPlannerRuntimeDecision(ctx, q, eventID)
	if err != nil || decision.Source != "CONTROL_PLANE" || decision.Outcome != "STOP" || !decision.ResultJson.Valid || !decision.ResultSha256.Valid {
		return event, result, "", "", false
	}
	// The preserved bytes are the exact canonical result the original strict
	// parse accepted; the recorded digest pins them. Canonical form carries the
	// event and context bindings, so decode it directly instead of re-parsing
	// the six-field agent reply shape.
	if complexExecutionRawDigest([]byte(decision.ResultJson.String)) != decision.ResultSha256.String {
		return event, result, "", "", false
	}
	request, err := q.GetClearDevPlannerRuntimeRequest(ctx, eventID)
	if err != nil {
		return event, result, "", "", false
	}
	if err := json.Unmarshal([]byte(decision.ResultJson.String), &result); err != nil {
		return event, result, "", "", false
	}
	if result.SchemaVersion != 1 || result.Kind != core.PlannerRuntimeResultKind ||
		result.Decision != core.PlannerRuntimeAmend || len(result.Amendments) == 0 || len(result.Amendments) > 3 ||
		result.EventID != event.ID || result.ContextSHA256 != request.ContextSha256 {
		return event, result, "", "", false
	}
	seen := map[string]bool{}
	for _, amendment := range result.Amendments {
		if amendment.TaskKey == "" || seen[amendment.TaskKey] {
			return event, result, "", "", false
		}
		seen[amendment.TaskKey] = true
	}
	return event, result, result.Amendments[0].TaskKey, decision.ResultSha256.String, true
}

// plannerRuntimeRepairApplicable reports whether every preserved amendment
// prepares against current facts. limitOnly means the sole refusals are the
// repair round or budget limits that a human grant resolves.
func plannerRuntimeRepairApplicable(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, event core.PlannerCoordinationEvent, result core.PlannerCoordinationResult, decisionSHA string) (applicable, limitOnly bool) {
	if len(result.Amendments) == 0 {
		return false, false
	}
	if err := validateProjectPlannerProposal(ctx, q, run, result); err != nil {
		return false, false
	}
	limitOnly = true
	for _, proposed := range result.Amendments {
		if _, err := preparePlannerTaskAmendment(ctx, q, run.ID, event.ID, decisionSHA, proposed, time.Time{}); err != nil {
			var rule *core.RuleError
			if !errors.As(err, &rule) || rule.Code != core.ReasonPlannerRuntimeBudget {
				return false, false
			}
			continue
		}
	}
	return true, limitOnly
}

// settleCoordinationRepair resolves the human coordination repair decision.
// Approval tops up only the named task budget and registers its bounded source
// continuation atomically. The old proposal and STOP remain history; a later
// valid Planner decision still owns any applied amendment.
func settleCoordinationRepair(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*core.RuleError, error) {
	b, err := core.ParseCoordinationRepairBinding(result.Binding)
	if err != nil {
		return nil, err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, b.ExecutionRunID)
	if err != nil {
		return nil, err
	}
	if run.Status != "ACCEPTED" || run.DevelopmentProjectID != b.DevelopmentRequirementID {
		return nil, complexExecutionRule("coordination repair authorization is stale")
	}
	event, _, taskKey, _, ok := plannerRuntimeRepairFacts(ctx, q, b.EventID)
	if !ok {
		return nil, complexExecutionRule("coordination repair authorization names no preserved proposal")
	}
	if event.ExecutionRunID != run.ID || taskKey == "" {
		return nil, complexExecutionRule("coordination repair authorization is bound to another execution")
	}
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, b.ExecutionRunID)
	if err != nil {
		return nil, err
	}
	mappingID := ""
	for _, mapping := range mappings {
		if mapping.ID == b.TaskID || mapping.WorkItemID == b.TaskID {
			mappingID = mapping.ID
		}
	}
	if mappingID == "" {
		return nil, complexExecutionRule("coordination repair authorization names no task of this execution")
	}
	budgets, err := q.ListClearDevComplexExceptionBudgets(ctx, b.ExecutionRunID)
	if err != nil {
		return nil, err
	}
	matched := false
	for _, budget := range budgets {
		if budget.ID == b.BudgetID && budget.RoleKind == core.ComplexExceptionBudgetBuilder &&
			budget.ComplexExecutionTaskID.Valid && budget.ComplexExecutionTaskID.String == mappingID {
			matched = true
		}
	}
	if !matched {
		return nil, complexExecutionRule("coordination repair authorization does not match a recorded task budget")
	}
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mappingID)
	if err != nil {
		return nil, err
	}
	if item.State == "DONE" || item.State == "CANCELLED" {
		return nil, complexExecutionRule("coordination repair authorization target is finished")
	}
	var continuation *core.WorkflowRecovery
	if result.Decision == core.HumanDecisionApprove {
		continuation, err = crossTaskCoordinationContinuation(ctx, q, run, event, request.ID, mappingID, at)
		if err != nil {
			return nil, err
		}
	}
	changed, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{
		ID: request.ID, Decision: string(result.Decision), ResolvedAt: nullableTime(at)})
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errors.New("coordination repair authorization already decided")
	}
	outcome := core.HumanDecisionDispatchRejected
	if result.Decision == core.HumanDecisionApprove {
		outcome = core.HumanDecisionDispatchApproved
		// One repair round pair needs the repair diagnosis and the repaired
		// rework: grant up to two extra builder turns, bounded by the
		// recorded authorization cap. Budgets already at the cap keep their
		// exact values and rely on the turns they already carry.
		for range 2 {
			rows, err := q.AuthorizeClearDevComplexExceptionBudgetExtra(ctx, b.BudgetID)
			if err != nil {
				return nil, err
			}
			if rows > 1 {
				return nil, fmt.Errorf("coordination repair budget grant applied to %d budgets", rows)
			}
		}
		if continuation != nil {
			// The authorized task may already be VERIFIED. Never rewrite that
			// history or transfer its extra budget to the reporting task.
			if err := applyCrossTaskCoordinationContinuation(ctx, q, *continuation); err != nil {
				return nil, err
			}
		} else if err := reopenCoordinationRepairTask(ctx, q, b, mappingID, at); err != nil {
			return nil, err
		}
	}
	if err := consumeDispatchCAS(ctx, q, result.Nonce, result.DesktopRunID, outcome, at); err != nil {
		return nil, err
	}
	project, err := q.GetClearDevRequirement(ctx, b.DevelopmentRequirementID)
	if err != nil {
		return nil, err
	}
	settleEvent := core.RequirementEvent{AOProjectID: project.AoProjectID, DevelopmentRequirementID: project.ID,
		SubjectType: core.SubjectHumanDecisionRequest, SubjectID: request.ID, Action: core.ActionSettleHumanDecision,
		PreviousState: "PENDING", TargetState: "RESOLVED", Outcome: core.EventAccepted, Source: core.EventSourceHumanDecision, CreatedAt: at}
	if err := insertClearDevEvent(ctx, q, settleEvent); err != nil {
		return nil, err
	}
	seq, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: request.ID, Action: string(core.ActionSettleHumanDecision)})
	if err != nil {
		return nil, err
	}
	return nil, q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: request.ID, Decision: string(result.Decision), EventSequence: seq, CreatedAt: at})
}

func reopenCoordinationRepairTask(ctx context.Context, q *gen.Queries, b core.CoordinationRepairBinding, mappingID string, at time.Time) error {
	// The grant reopens the settled Builder-blocked attempt as its
	// rework successor and the task for the next round, exactly like the
	// native builder-turn recovery. The preserved revision itself applies
	// later through the ordinary Planner coordination decision, under the
	// shipped history rules.
	attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, b.ExecutionRunID)
	if err != nil {
		return err
	}
	attemptRound := int64(-1)
	for _, attempt := range attempts {
		if attempt.TaskMappingID == mappingID && attempt.Round > attemptRound {
			attemptRound = attempt.Round
		}
	}
	if attemptRound < 0 {
		return complexExecutionRule("coordination repair authorization found no Builder attempt")
	}
	reopened, err := q.RecoverClearDevCoordinationRepairBuilderAttempt(ctx, gen.RecoverClearDevCoordinationRepairBuilderAttemptParams{
		ExecutionRunID: b.ExecutionRunID, TaskMappingID: mappingID, Round: attemptRound,
	})
	if err != nil {
		return err
	}
	if reopened != 1 {
		return complexExecutionRule("coordination repair authorization names a round that is no longer recoverable")
	}
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mappingID)
	if err != nil {
		return err
	}
	if item.State != "BLOCKED" {
		return complexExecutionRule("coordination repair authorization target is not blocked")
	}
	task, err := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
		ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount,
		NextState: "REWORK", ReworkCount: item.ReworkCount + 1, UpdatedAt: at,
	})
	if err != nil {
		return err
	}
	if task != 1 {
		return complexExecutionRule("coordination repair authorization target changed concurrently")
	}
	return nil
}

// coordinationRepairGrants lists the approved human coordination repair
// authorizations. Each approval is durable permission for exactly the stopped
// event and task it names; nothing else is implied.
func coordinationRepairGrants(ctx context.Context, q *gen.Queries) ([]core.CoordinationRepairBinding, error) {
	rows, err := q.ListClearDevHumanDecisionRepairGrants(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]core.CoordinationRepairBinding, 0, len(rows))
	for _, row := range rows {
		binding, err := core.ParseCoordinationRepairBinding([]byte(row.BindingJson))
		if err != nil {
			continue
		}
		out = append(out, binding)
	}
	return out, nil
}

// coordinationRepairGrantedForTask reports whether a human approved the
// coordination repair of exactly this task.
func coordinationRepairGrantedForTask(ctx context.Context, q *gen.Queries, runID, mappingID, workItemID string) (bool, error) {
	grants, err := coordinationRepairGrants(ctx, q)
	if err != nil {
		return false, err
	}
	for _, grant := range grants {
		if grant.ExecutionRunID == runID && (grant.TaskID == mappingID || grant.TaskID == workItemID) {
			return true, nil
		}
	}
	return false, nil
}
