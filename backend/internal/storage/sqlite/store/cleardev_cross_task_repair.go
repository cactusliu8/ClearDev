package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// A repair grant names the task whose limit was reached, not necessarily the
// task that reported the problem. Keep a verified target intact and reuse the
// original blocked source's ordinary, budgeted continuation. This creates no
// new authority and never changes the preserved Planner decision.
func crossTaskCoordinationContinuation(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, event core.PlannerCoordinationEvent, requestID, targetID string, at time.Time) (*core.WorkflowRecovery, error) {
	source, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, event.DispatchID)
	if err != nil {
		return nil, err
	}
	if source.TaskMappingID == targetID {
		return nil, nil // Preserve the shipped same-task recovery semantics.
	}
	if !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) || source.ExecutionRunID != run.ID || source.Status != "BLOCKED" || source.ReasonCode != "BUILDER_BLOCKED" || !source.SettledAt.Valid || source.AgentStepID != event.SourceStepID {
		return nil, complexExecutionRule("cross-task repair requires its exact settled Builder source")
	}
	current, err := plannerRuntimeCurrent(ctx, q, run)
	if err != nil {
		return nil, err
	}
	if !current {
		return nil, complexExecutionRule("cross-task repair execution changed")
	}
	quiet, err := plannerProjectQuiescent(ctx, q, run)
	if err != nil {
		return nil, err
	}
	active, err := q.CountClearDevPlannerRuntimeActiveAttempts(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	if !quiet || active != 0 {
		return nil, complexExecutionRule("cross-task repair still has in-flight work")
	}
	request, err := q.GetClearDevPlannerRuntimeRequest(ctx, event.ID)
	if err != nil {
		return nil, err
	}
	contextJSON, err := plannerRuntimeContext(ctx, q, run, event)
	if err != nil {
		return nil, err
	}
	if !crossTaskRepairContextMatches(request, contextJSON, event.ID) {
		return nil, complexExecutionRule("cross-task repair proposal context changed")
	}
	target, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, targetID)
	if err != nil {
		return nil, err
	}
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, source.TaskMappingID)
	if err != nil {
		return nil, err
	}
	if target.State != "REVIEW" || item.State != "BLOCKED" || item.ReworkCount != source.Round {
		return nil, complexExecutionRule("cross-task repair target or source state changed")
	}
	attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	targetVerified := false
	expectedHead, previousRound := source.BaseCommitSha, int64(-1)
	for _, attempt := range attempts {
		if attempt.TaskMappingID == source.TaskMappingID && attempt.Round > source.Round {
			return nil, complexExecutionRule("cross-task repair targets a historical source")
		}
		if attempt.TaskMappingID == source.TaskMappingID && attempt.Round < source.Round && attempt.Round > previousRound {
			candidate, candidateErr := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(attempt.ID))
			if candidateErr != nil && !errors.Is(candidateErr, sql.ErrNoRows) {
				return nil, candidateErr
			}
			if candidateErr == nil {
				expectedHead, previousRound = candidate.CommitSha, attempt.Round
			}
		}
		if attempt.TaskMappingID == targetID && attempt.Round == target.ReworkCount {
			targetVerified = attempt.Status == "VERIFIED" && attempt.SettledAt.Valid
		}
	}
	if !targetVerified {
		return nil, complexExecutionRule("cross-task repair target is not verified")
	}
	_, proposal, targetKey, _, ok := plannerRuntimeRepairFacts(ctx, q, event.ID)
	if !ok {
		return nil, complexExecutionRule("cross-task repair lost its preserved proposal")
	}
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	sourceKey, targetMatches := "", false
	for _, mapping := range mappings {
		if mapping.ID == targetID {
			targetMatches = mapping.PlanTaskKey == targetKey
		}
		if mapping.ID == source.TaskMappingID {
			sourceKey = mapping.PlanTaskKey
		}
	}
	sourceAmended := false
	for _, amendment := range proposal.Amendments {
		sourceAmended = sourceAmended || amendment.TaskKey == sourceKey
	}
	if !targetMatches || !sourceAmended || !slices.Contains(event.Report.AffectedTaskKeys, sourceKey) || !slices.Contains(event.Report.AffectedTaskKeys, targetKey) {
		return nil, complexExecutionRule("cross-task repair source and target must share the original proposal")
	}
	step, err := q.GetClearDevComplexExecutionAgentStep(ctx, source.AgentStepID)
	if err != nil {
		return nil, err
	}
	if step.SendStatus != "SETTLED" || step.RoleBindingID != source.BuilderRoleBindingID || step.RequestID != source.ID || step.MessageSha256.String != event.SourceMessageSHA256 || complexExecutionRawDigest([]byte(step.FinalMessageText.String)) != event.SourceMessageSHA256 {
		return nil, complexExecutionRule("cross-task repair lost the exact source reply")
	}
	if _, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(source.ID)); !errors.Is(err, sql.ErrNoRows) {
		return nil, complexExecutionRule("cross-task repair source already published a candidate")
	}
	binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, source.BuilderRoleBindingID)
	if err != nil {
		return nil, err
	}
	if binding.Status != "BOUND" || !binding.AoSessionID.Valid {
		return nil, complexExecutionRule("cross-task repair original Builder is unavailable")
	}
	session, err := q.GetSession(ctx, domain.SessionID(binding.AoSessionID.String))
	if err != nil {
		return nil, err
	}
	turns, err := q.CountClearDevUnsettledSessionTurns(ctx, session.ID)
	if err != nil {
		return nil, err
	}
	if session.IsTerminated || session.ActivityState != domain.ActivityIdle || turns != 0 || session.ProviderConversationID == "" {
		return nil, complexExecutionRule("cross-task repair source delivery is not settled")
	}
	if err := workflowRecoveryBudget(ctx, q, run.ID, source.TaskMappingID, "BUILDER", source.Round+1); err != nil {
		return nil, err // The target grant never tops up the source's budget.
	}
	return &core.WorkflowRecovery{
		ID: requestID + ":source-continuation", ExecutionRunID: run.ID,
		Action: core.RecoveryContinueBuilder, TargetID: source.ID, DispatchID: source.ID,
		TaskID: source.TaskMappingID, StepID: step.ID, BindingID: binding.ID,
		SuccessorID: requestID + ":source-successor", CandidateSHA: expectedHead,
		ProviderConversationID: session.ProviderConversationID,
		OriginalStoppedAt:      source.SettledAt.Time, OriginalStatus: source.Status,
		OriginalReason: source.ReasonCode, OriginalSummary: step.FinalMessageText.String,
		Supplement: "The human authorized the bounded repair for the preserved coordination event " + event.ID + ". Continue diagnosing the original problem in this task's existing paths and budget, then ask the original Planner to revalidate the engineering revision. Do not treat the grant as a new write-path permission or as a passed candidate.",
		CreatedAt:  at,
	}, nil
}

// The current event's newly saved STOP is the only permitted difference from
// the pre-decision context. All other source, budget, task and review facts must
// still match the exact request; do not replace history with a fresh snapshot.
func crossTaskRepairContextMatches(request gen.CleardevPlannerRuntimeRequest, current []byte, eventID string) bool {
	if request.ContextSha256 != complexExecutionRawDigest([]byte(request.ContextJson)) {
		return false
	}
	var facts map[string]json.RawMessage
	if json.Unmarshal(current, &facts) != nil {
		return false
	}
	var decisions []core.PlannerCoordinationDecision
	if json.Unmarshal(facts["priorDecisions"], &decisions) != nil {
		return false
	}
	kept := []core.PlannerCoordinationDecision{}
	for _, decision := range decisions {
		if decision.EventID == eventID && decision.Source == "CONTROL_PLANE" && decision.Outcome == "STOP" {
			continue
		}
		kept = append(kept, decision)
	}
	facts["priorDecisions"], _ = json.Marshal(kept)
	raw, err := json.Marshal(facts)
	return err == nil && string(raw) == request.ContextJson
}

func applyCrossTaskCoordinationContinuation(ctx context.Context, q *gen.Queries, recovery core.WorkflowRecovery) error {
	event, proposal, _, digest, ok := plannerRuntimeRepairFacts(ctx, q, recovery.DispatchID+":planner-coordination")
	if !ok {
		return complexExecutionRule("cross-task repair lost the preserved proposal")
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, recovery.ExecutionRunID)
	if err != nil {
		return err
	}
	if err := validateFailurePlannerAuthority(ctx, q, run, event, proposal); err != nil {
		return err
	}
	effective, err := effectiveProjectRun(ctx, q, run)
	if err != nil {
		return err
	}
	contract, project, err := core.ProjectContractFromRun(effective)
	if err != nil || !project {
		return complexExecutionRule("cross-task repair has no original project authority")
	}
	for _, amendment := range proposal.Amendments {
		if amendment.ExecutionBasis != nil && !slices.Equal(amendment.ExecutionBasis.WritePaths, contract.Basis.WritePaths) {
			return complexExecutionRule("a coordination repair limit grant cannot expand paths")
		}
	}
	// Revalidate every amendment with the now-visible grant in this same
	// transaction. A structural failure rolls back the approval and budget too.
	for _, proposed := range proposal.Amendments {
		if _, err := preparePlannerTaskAmendment(ctx, q, run.ID, event.ID, digest, proposed, recovery.CreatedAt); err != nil {
			return err
		}
	}
	if err := insertWorkflowRecovery(ctx, q, recovery); err != nil {
		return err
	}
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, recovery.TaskID)
	if err != nil {
		return err
	}
	rows, err := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
		ID: item.ID, ExpectedState: "BLOCKED", ExpectedReworkCount: item.ReworkCount,
		NextState: "REWORK", ReworkCount: item.ReworkCount + 1, UpdatedAt: recovery.CreatedAt,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return complexExecutionRule("cross-task repair source changed concurrently")
	}
	return nil
}
