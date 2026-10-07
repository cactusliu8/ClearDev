package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// validateDecisionReopen only reads facts. It never settles a choice or changes a budget.
func validateDecisionReopen(ctx context.Context, q *gen.Queries, request gen.CleardevHumanDecisionRequest, at time.Time) error {
	if request.Status != "PENDING" {
		return errors.New("decision is no longer pending")
	}
	project, err := q.GetClearDevRequirement(ctx, request.DevelopmentProjectID)
	if err != nil {
		return err
	}
	if project.CancelledAt.Valid || project.PausedFromState.Valid {
		return errors.New("decision requirement is stopped")
	}
	current, err := productPlanDecisionCurrent(ctx, q, request)
	if err != nil {
		return err
	}
	if !current {
		return errors.New("decision product source changed")
	}
	var display core.HumanDecisionDisplay
	if err := json.Unmarshal([]byte(request.DisplayJson), &display); err != nil {
		return err
	}
	digest, err := core.HumanDecisionContentSHA256(request.DecisionKind, json.RawMessage(request.BindingJson), display)
	if err != nil {
		return err
	}
	if digest != request.ContentSha256 || request.BindingSchemaVersion != 1 {
		return errors.New("decision content changed")
	}
	var b struct {
		RequirementVersionID     string `json:"requirementVersionId"`
		DevelopmentRequirementID string `json:"developmentRequirementId"`
	}
	if err := json.Unmarshal([]byte(request.BindingJson), &b); err != nil {
		return err
	}
	if request.DecisionKind == core.HumanDecisionKindProductPlan {
		binding, err := core.ParseProductPlanBinding([]byte(request.BindingJson))
		if err != nil {
			return err
		}
		if binding.ProductID != project.ID {
			return errors.New("decision product binding changed")
		}
		return nil // Exact current product binding was checked above.
	}
	if b.DevelopmentRequirementID != project.ID {
		return errors.New("decision requirement binding changed")
	}
	if b.RequirementVersionID != "" {
		version, err := q.GetClearDevRequirementVersion(ctx, b.RequirementVersionID)
		if err != nil {
			return err
		}
		if version.DevelopmentProjectID != project.ID || version.SupersededByID.Valid {
			return errors.New("decision version changed")
		}
		if request.DecisionKind == core.HumanDecisionKindConfirmVersion {
			open, err := q.GetOpenClearDevRequirementVersion(ctx, project.ID)
			if err != nil {
				return err
			}
			if open.ID != version.ID {
				return errors.New("decision version is not current")
			}
		} else {
			confirmed, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, project.ID)
			if err != nil {
				return err
			}
			if confirmed.ID != version.ID {
				return errors.New("decision confirmed version changed")
			}
		}
		if request.DecisionKind != core.HumanDecisionKindApproveDirectionChange {
			stopped, err := activeDirectionStop(ctx, q, version.ID)
			if err != nil {
				return err
			}
			if stopped {
				return errors.New("decision direction stopped")
			}
		}
	}
	result := core.HumanDecisionResult{Binding: json.RawMessage(request.BindingJson)}
	switch request.DecisionKind {
	case core.HumanDecisionKindConfirmVersion:
		return validateReopenConfirm(ctx, q, result)
	case core.HumanDecisionKindApproveDirectionChange:
		return validateReopenDirection(ctx, q, result)
	case core.HumanDecisionKindPlanningRecovery:
		return validateReopenPlanning(ctx, q, result, request)
	case core.HumanDecisionKindExtraPlanningAttempt:
		return validateReopenExtraPlanning(ctx, q, result, request, at)
	case core.HumanDecisionKindBuilderReplacement:
		return validateReopenBuilderReplacement(ctx, q, result, request)
	case core.HumanDecisionKindFinalReviewRecheck:
		return validateReopenFinal(ctx, q, result, request, at)
	case core.HumanDecisionKindExtraMailAttempt:
		return validateReopenMail(ctx, q, result, request)
	case core.HumanDecisionKindExtraReviewBudget:
		return validateReopenBudget(ctx, q, result, "REVIEWER", "reviewer")
	case core.HumanDecisionKindExtraBuilderTurn:
		return validateReopenBudget(ctx, q, result, "BUILDER", "builder")
	case core.HumanDecisionKindStoppedCheckRecovery:
		return validateReopenStoppedCheck(ctx, q, result, request)
	case core.HumanDecisionKindExtraCoordination:
		return validateReopenExtraCoordination(ctx, q, result, request)
	case core.HumanDecisionKindCoordinationRepair:
		return validateReopenCoordinationRepair(ctx, q, result)
	case core.HumanDecisionKindControlledEngineChange:
		return validateReopenEngineChange(ctx, q, result)
	default:
		return errors.New("decision kind is not enabled")
	}
}

func validateReopenConfirm(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult) error {
	binding, err := core.ParseConfirmRequirementBinding(result.Binding)
	if err != nil {
		return err
	}
	versionRow, err := q.GetClearDevRequirementVersion(ctx, binding.RequirementVersionID)
	if err != nil {
		return err
	}
	version := clearDevRequirementVersionFromGen(versionRow)
	if version.DevelopmentRequirementID != binding.DevelopmentRequirementID ||
		version.SHA256 != binding.RequirementVersionSHA256 || version.TaskSetVersion != binding.TaskSetVersion ||
		version.Status != core.RequirementVersionStatusPendingConfirmation {
		return errors.New("human decision binding no longer matches the requirement version")
	}

	return nil
}

func validateReopenDirection(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult) error {
	binding, err := core.ParseApproveDirectionChangeBinding(result.Binding)
	if err != nil {
		return err
	}
	gateRow, err := q.GetClearDevDirectionStopGateByRequest(ctx, binding.DirectionRequestID)
	if err != nil {
		return err
	}
	gate := clearDevDirectionStopGateFromGen(gateRow)
	if gate.Status != core.DirectionStopGateActive ||
		gate.DevelopmentRequirementID != binding.DevelopmentRequirementID ||
		gate.RequirementVersionID != binding.RequirementVersionID ||
		gate.TaskSetVersion != binding.TaskSetVersion ||
		gate.SnapshotSHA256 != binding.SnapshotSHA256 {
		return errors.New("human decision binding no longer matches the active direction stop gate")
	}
	versionRow, err := q.GetClearDevRequirementVersion(ctx, binding.RequirementVersionID)
	if err != nil {
		return err
	}
	version := clearDevRequirementVersionFromGen(versionRow)
	if version.SHA256 != binding.RequirementVersionSHA256 {
		return errors.New("human decision binding no longer matches the requirement version")
	}

	return nil
}

func validateReopenPlanning(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest) error {
	b, err := core.ParsePlanningRecoveryBinding(result.Binding)
	if err != nil {
		return err
	}
	if request.ID != planningRecoveryRequestID(b.DevelopmentRequirementID) {
		return errors.New("invalid planning recovery request identity")
	}
	candidates, err := q.ListClearDevPlanningRecoveryCandidates(ctx)
	if err != nil {
		return err
	}
	var match *gen.ListClearDevPlanningRecoveryCandidatesRow
	for i := range candidates {
		if planningRecoveryBinding(candidates[i]) == b {
			match = &candidates[i]
			break
		}
	}
	if match == nil {
		return errors.New("interrupted planning review is no longer eligible for recovery")
	}

	return nil
}

func validateReopenFinal(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) error {
	b, err := core.ParseFinalReviewRecheckBinding(result.Binding)
	if err != nil {
		return err
	}
	if request.ID != finalRecheckRequestID(b.ExecutionRunID) {
		return errors.New("invalid final recheck request")
	}
	row, err := q.GetClearDevRequirementFinalReview(ctx, b.ExecutionRunID)
	if err != nil {
		return err
	}
	r, current, err := prepareFinalRecheck(ctx, q, row, at)
	if err != nil {
		return err
	}
	if current != b {
		return errors.New("final recheck evidence changed since native offer")
	}

	_ = r
	return nil
}

func validateReopenMail(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest) error {
	b, err := core.ParseExtraMailAttemptBinding(result.Binding)
	if err != nil {
		return err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, b.ExecutionRunID)
	if err != nil {
		return err
	}
	if !core.BoundedMailAttempts(complexExecutionRunFromGen(run)) || run.Status != "ACCEPTED" || run.DevelopmentProjectID != b.DevelopmentRequirementID || run.RequirementVersionID != b.RequirementVersionID || run.RequirementSha256 != b.RequirementVersionSHA256 || run.PlanSha256 != b.PlanSHA256 || request.ID != mailExtraRequestID(run.ID) {
		return complexExecutionRule("extra attempt policy or plan is stale")
	}
	project, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
	if err != nil {
		return err
	}
	version, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, run.DevelopmentProjectID)
	if err != nil {
		return err
	}
	if project.CancelledAt.Valid || project.State == "PAUSED" || version.ID != run.RequirementVersionID || version.Sha256 != run.RequirementSha256 || version.TaskSetVersion != run.AcceptedTaskSetVersion {
		return complexExecutionRule("extra attempt requirement changed")
	}
	if stopped, err := activeDirectionStop(ctx, q, run.RequirementVersionID); err != nil {
		return err
	} else if stopped {
		return directionStoppedError()
	}
	if _, err := q.GetOpenClearDevRequirementVersion(ctx, run.DevelopmentProjectID); err == nil {
		return complexExecutionRule("extra attempt has an unresolved requirement version")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if intent, err := q.GetClearDevDirectionIntentByVersion(ctx, run.RequirementVersionID); err == nil {
		decision, err := q.GetClearDevHumanDecisionRequestByDirectionRequestID(ctx, intent.DirectionRequestID)
		if err != nil || decision.Status != "RESOLVED" || decision.Decision != "REJECT" {
			return complexExecutionRule("extra attempt is blocked by an unresolved direction intent")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	taskSlots, err := mailAttemptSlotsForTask(ctx, q, run.ID, b.TaskID)
	if err != nil {
		return err
	}
	if len(taskSlots) != b.NextRound || len(taskSlots) == 0 || taskSlots[len(taskSlots)-1].DispatchID != b.DispatchID {
		return complexExecutionRule("extra attempt is no longer the next task round")
	}
	allSlots, err := mailAttemptSlots(ctx, q, run.ID)
	if err != nil {
		return err
	}
	for _, s := range allSlots {
		if s.Kind == core.MailAttemptHumanExtra {
			return complexExecutionRule("extra attempt already consumed")
		}
	}
	failed, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, b.DispatchID)
	if err != nil {
		return err
	}
	candidate, err := mailCandidateForExtra(ctx, q, run.ID, b.TaskID, failed.ID)
	if err != nil {
		return err
	}
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, b.TaskID)
	if err != nil {
		return err
	}
	if failed.ExecutionRunID != run.ID || failed.TaskMappingID != b.TaskID || failed.Status != "NEEDS_HUMAN" || failed.ReasonCode != string(core.MailAttemptLimitReason) || candidate.CommitSha != b.CandidateSHA || item.State != "NEEDS_HUMAN" {
		return complexExecutionRule("extra attempt failure or candidate changed")
	}

	return nil
}

func validateReopenBudget(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, roleKind, label string) error {
	b, err := core.ParseExtraReviewBudgetBinding(result.Binding)
	if err != nil {
		return err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, b.ExecutionRunID)
	if err != nil {
		return err
	}
	if run.Status != "ACCEPTED" || run.DevelopmentProjectID != b.DevelopmentRequirementID {
		return complexExecutionRule(label + " authorization is stale")
	}
	// The desktop names tasks by work item; older offers may carry the
	// mapping id instead, so accept either identifier.
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, b.ExecutionRunID)
	if err != nil {
		return err
	}
	mappingID := ""
	for _, mapping := range mappings {
		if mapping.ID == b.TaskID || mapping.WorkItemID == b.TaskID {
			mappingID = mapping.ID
		}
	}
	if mappingID == "" {
		return complexExecutionRule(label + " authorization names no task of this execution")
	}
	budgets, err := q.ListClearDevComplexExceptionBudgets(ctx, b.ExecutionRunID)
	if err != nil {
		return err
	}
	matched := false
	for _, budget := range budgets {
		if budget.ID == b.BudgetID && budget.RoleKind == roleKind &&
			budget.ComplexExecutionTaskID.Valid && budget.ComplexExecutionTaskID.String == mappingID {
			matched = budget.AuthorizedExtraTurns < 2
		}
	}
	if !matched {
		return complexExecutionRule(label + " authorization does not match a recorded task budget")
	}
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mappingID)
	if err != nil {
		return err
	}
	if item.State == "DONE" || item.State == "CANCELLED" {
		return complexExecutionRule(label + " authorization target is finished")
	}
	attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, b.ExecutionRunID)
	if err != nil {
		return err
	}
	currentRound := -1
	for _, attempt := range attempts {
		if attempt.TaskMappingID == mappingID && int(attempt.Round) > currentRound {
			currentRound = int(attempt.Round)
		}
	}
	if currentRound != b.Round {
		return complexExecutionRule(label + " authorization names a stale round")
	}

	version, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, run.DevelopmentProjectID)
	if err != nil {
		return err
	}
	if version.ID != run.RequirementVersionID || version.Sha256 != run.RequirementSha256 || version.TaskSetVersion != run.AcceptedTaskSetVersion {
		return errors.New("decision execution version changed")
	}
	stopped, err := activeDirectionStop(ctx, q, version.ID)
	if err != nil {
		return err
	}
	if stopped {
		return errors.New("decision direction stopped")
	}

	return nil
}

// validateReopenCoordinationRepair confirms the stopped coordination event
// still carries its preserved, unapplied Planner proposal.
func validateReopenCoordinationRepair(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult) error {
	b, err := core.ParseCoordinationRepairBinding(result.Binding)
	if err != nil {
		return err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, b.ExecutionRunID)
	if err != nil {
		return err
	}
	if run.Status != "ACCEPTED" || run.DevelopmentProjectID != b.DevelopmentRequirementID {
		return complexExecutionRule("coordination repair authorization is stale")
	}
	event, _, taskKey, _, ok := plannerRuntimeRepairFacts(ctx, q, b.EventID)
	if !ok || event.ExecutionRunID != b.ExecutionRunID || taskKey == "" {
		return complexExecutionRule("coordination repair authorization names no preserved proposal")
	}
	return nil
}

// validateReopenEngineChange confirms the controlled project still differs
// from the exact authorized target engine.
func validateReopenEngineChange(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult) error {
	b, err := core.ParseControlledEngineChangeBinding(result.Binding)
	if err != nil {
		return err
	}
	project, err := q.GetProject(ctx, domain.ProjectID(b.AOProjectID))
	if err != nil {
		return err
	}
	record := projectRowFromGen(project)
	if record.ID == "" || b.DevelopmentRequirementID == "" {
		return complexExecutionRule("engine change authorization is stale")
	}
	choice := record.Config.ClearDev
	current := domain.HarnessCodex
	currentModel := ""
	if choice != nil {
		current, currentModel = choice.Harness, choice.Model
	}
	if current == domain.AgentHarness(b.TargetHarness) && currentModel == b.Model {
		return complexExecutionRule("engine change target already in use")
	}
	return nil
}
