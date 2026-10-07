package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func plannerAnswerFromRow(row gen.CleardevPlannerAnswer) (core.PlannerClarificationAnswer, error) {
	answer := core.PlannerClarificationAnswer{RequestID: row.RequestID, DevelopmentRequirementID: row.DevelopmentProjectID, PlanID: row.PlanID, PlanSHA256: row.PlanSha256, AnswersSHA256: row.AnswersSha256, NextPlanningRequestID: row.NextPlanningRequestID, CreatedAt: row.CreatedAt}
	err := json.Unmarshal([]byte(row.AnswersJson), &answer.Answers)
	return answer, err
}

// RecordClearDevPlannerAnswers registers one immutable, bounded planning successor.
func (s *Store) RecordClearDevPlannerAnswers(ctx context.Context, input core.PlannerClarificationAnswer) (core.PlannerClarificationAnswer, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	saved := input
	created := false
	err := s.inTx(ctx, "record Planner answers", func(q *gen.Queries) error {
		previous, err := q.GetClearDevPlannerAnswer(ctx, input.RequestID)
		if err == nil {
			saved, err = plannerAnswerFromRow(previous)
			if err != nil {
				return err
			}
			if saved.DevelopmentRequirementID != input.DevelopmentRequirementID || saved.PlanID != input.PlanID || saved.PlanSHA256 != input.PlanSHA256 || !reflect.DeepEqual(saved.Answers, input.Answers) {
				return complexExecutionRule("Planner answer request content changed")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		row, err := q.GetClearDevPlannerAnswerPlan(ctx, input.PlanID)
		if err != nil {
			return err
		}
		plan := clearDevComplexPlanFromGen(row)
		if plan.DevelopmentRequirementID != input.DevelopmentRequirementID || plan.PlanSHA256 != input.PlanSHA256 {
			return complexExecutionRule("Planner question binding changed")
		}
		clarification, ok := core.PlannerClarificationForPlan(plan)
		if !ok {
			return complexExecutionRule("plan is not a product clarification")
		}
		answers, digest, err := core.NormalizePlannerAnswers(clarification.Questions, input.Answers)
		if err != nil {
			return complexExecutionRule(err.Error())
		}
		if !reflect.DeepEqual(answers, input.Answers) || digest != input.AnswersSHA256 {
			return complexExecutionRule("Planner answer digest changed")
		}
		plans, err := q.ListClearDevComplexEngineeringPlans(ctx, input.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		round := 1
		for _, item := range plans {
			if item.RequirementVersionID == plan.RequirementVersionID {
				round++
			}
			if item.Version > plan.Version {
				return complexExecutionRule("Planner question is no longer current")
			}
		}
		if input.NextPlanningRequestID != fmt.Sprintf("cleardev-complex-plan-%s-%d", plan.RequirementVersionID, round) {
			return complexExecutionRule("Planner successor request changed")
		}
		if err := validatePlannerAnswerSource(ctx, q, plan); err != nil {
			return err
		}
		raw, err := core.CanonicalJSONBytes(input.Answers)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevPlannerAnswer(ctx, gen.InsertClearDevPlannerAnswerParams{RequestID: input.RequestID, DevelopmentProjectID: input.DevelopmentRequirementID, PlanID: input.PlanID, PlanSha256: input.PlanSHA256, AnswersJson: string(raw), AnswersSha256: input.AnswersSHA256, NextPlanningRequestID: input.NextPlanningRequestID, CreatedAt: input.CreatedAt}); err != nil {
			return complexExecutionRule("Planner question already answered or answer limit reached")
		}
		created = true
		return nil
	})
	return saved, created, err
}

func validatePlannerAnswerSource(ctx context.Context, q *gen.Queries, plan core.ComplexEngineeringPlan) error {
	requirement, _, err := loadComplexRequirement(ctx, q, plan.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	current, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, plan.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	if current.ID != plan.RequirementVersionID || current.Sha256 != plan.RequirementSHA256 {
		return complexExecutionRule("confirmed specification changed")
	}
	row, err := q.GetClearDevRequirement(ctx, plan.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	if row.PausedFromState.Valid {
		return complexExecutionRule("paused requirement cannot continue")
	}
	if requirement.CancelledAt != nil {
		return complexExecutionRule("cancelled requirements cannot continue planning")
	}
	stopped, err := activeDirectionStop(ctx, q, plan.RequirementVersionID)
	if err != nil {
		return err
	}
	if stopped {
		return complexExecutionRule("direction stop blocks Planner answers")
	}
	runs, err := q.ListClearDevComplexExecutionRuns(ctx, plan.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	if len(runs) > 0 {
		return complexExecutionRule("execution already started")
	}
	if err := validatePlannerContractCommand(ctx, q, core.CreateComplexPlanCommand{Plan: plan}, true); err != nil {
		return err
	}
	binding, err := q.GetClearDevComplexRoleBinding(ctx, plan.PlannerRoleBindingID)
	if err != nil {
		return err
	}
	if binding.Status != string(core.RoleBindingStatusBound) || binding.Role != string(core.StandardRoleEngineeringPlanner) {
		return complexExecutionRule("original Planner binding is unavailable")
	}
	budget, err := q.GetClearDevMessageBudgetVersion(ctx, plan.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	if budget.Version != string(core.MessageBudgetV1) {
		return complexExecutionRule("original message budget is unknown")
	}
	stageRow, stageErr := q.GetClearDevProductStageByRequirement(ctx, nullableString(plan.DevelopmentRequirementID))
	if stageErr == nil {
		parent, err := q.GetClearDevRequirement(ctx, stageRow.ProductID)
		if err != nil {
			return err
		}
		if parent.PausedFromState.Valid || parent.CancelledAt.Valid {
			return complexExecutionRule("parent product is stopped")
		}
	} else if !errors.Is(stageErr, sql.ErrNoRows) {
		return stageErr
	}
	return plannerQuestionProof(ctx, q, plan, binding)
}

// The reservation transaction repeats authority and current-source checks for
// every new message, including corrections and a controlled second attempt.
func validatePlannerAnswerBeforeSend(ctx context.Context, q *gen.Queries, attempt core.AgentStepAttempt) error {
	if attempt.StepCategory != core.AgentStepCategoryComplexPlanning || attempt.StepKind != core.ComplexAgentStepEngineeringPlan {
		return nil
	}
	answers, err := q.ListClearDevPlannerAnswers(ctx, attempt.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	step, err := q.GetClearDevComplexAgentStep(ctx, attempt.LogicalStepID)
	if err != nil {
		return err
	}
	for _, answer := range answers {
		if answer.NextPlanningRequestID != step.RequestID {
			continue
		}
		row, err := q.GetClearDevPlannerAnswerPlan(ctx, answer.PlanID)
		if err != nil {
			return err
		}
		plan := clearDevComplexPlanFromGen(row)
		if err := validatePlannerAnswerSource(ctx, q, plan); err != nil {
			return err
		}
		plans, err := q.ListClearDevComplexEngineeringPlans(ctx, plan.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		for _, later := range plans {
			if later.Version > plan.Version {
				return complexExecutionRule("Planner clarification is no longer current before send")
			}
		}
		binding, err := q.GetClearDevComplexRoleBinding(ctx, plan.PlannerRoleBindingID)
		if err != nil {
			return err
		}
		if step.RoleBindingID != binding.ID || attempt.RoleBindingID != binding.ID || attempt.AOSessionID != binding.AoSessionID.String {
			return complexExecutionRule("answer continuation changed its original Planner")
		}
		session, err := q.GetSession(ctx, domain.SessionID(attempt.AOSessionID))
		if err != nil {
			return err
		}
		matchingTool, err := q.IsClearDevPlanningContinuationSession(ctx, binding.AoSessionID.String)
		if err != nil {
			return err
		}
		if !matchingTool {
			return complexExecutionRule("original Planner tool or model changed")
		}
		if session.IsTerminated || session.ActivityState != domain.ActivityIdle {
			return complexExecutionRule("original Planner is not idle before sending answers")
		}
	}
	return nil
}

// ValidateClearDevPlannerAnswer is a read-only availability check. The write
// and message reservation transactions repeat it before accepting new work.
func (s *Store) ValidateClearDevPlannerAnswer(ctx context.Context, planID string) error {
	row, err := s.qr.GetClearDevPlannerAnswerPlan(ctx, planID)
	if err != nil {
		return err
	}
	return validatePlannerAnswerSource(ctx, s.qr, clearDevComplexPlanFromGen(row))
}

func plannerQuestionProof(ctx context.Context, q *gen.Queries, plan core.ComplexEngineeringPlan, binding gen.CleardevComplexRoleBinding) error {
	step, err := q.GetClearDevComplexAgentStep(ctx, plan.AgentStepID)
	if err != nil {
		return err
	}
	attempts, err := q.ListClearDevAgentStepAttemptStates(ctx, gen.ListClearDevAgentStepAttemptStatesParams{DevelopmentProjectID: plan.DevelopmentRequirementID, LogicalStepID: step.ID})
	if err != nil {
		return err
	}
	if len(attempts) < 1 || len(attempts) > 2 {
		return complexExecutionRule("Planner question delivery is unmeasured")
	}
	correction, err := q.GetClearDevParseCorrection(ctx, step.ID)
	hasCorrection := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if hasCorrection && (correction.ClientMessageID != step.ClientMessageID+":parse-correction" || correction.PromptSha256 != complexExecutionRawDigest([]byte(correction.PromptText)) || correction.AttemptNumber < 1 || correction.AttemptNumber > int64(len(attempts))) {
		return complexExecutionRule("Planner correction binding changed")
	}
	messages := []invalidBuilderMessage{}
	for i, attempt := range attempts {
		id, source := step.ClientMessageID, string(core.AgentMessageOriginal)
		if i == 1 {
			id += ":attempt:2"
			source = string(core.AgentMessageRecoveryOriginal)
		}
		if attempt.AttemptNumber != int64(i+1) || attempt.RoleBindingID != binding.ID || attempt.AoSessionID != binding.AoSessionID.String || attempt.ClientMessageID != id || attempt.PromptSha256 != step.PromptSha256 || attempt.StepCategory != string(core.AgentStepCategoryComplexPlanning) || attempt.StepKind != step.StepKind {
			return complexExecutionRule("Planner question attempt binding changed")
		}
		messages = append(messages, invalidBuilderMessage{attempt: attempt, id: id, prompt: step.PromptSha256, source: source, resultKind: string(core.AgentResultOriginal), result: 1, sendStatus: "SENT"})
		if hasCorrection && correction.AttemptNumber == attempt.AttemptNumber {
			messages = append(messages, invalidBuilderMessage{attempt: attempt, id: id + ":parse-correction", prompt: correction.PromptSha256, source: string(core.AgentMessageParseCorrection), resultKind: string(core.AgentResultCorrection), result: 2, sendStatus: "CORRECTION_SENT"})
		}
	}
	reservations, err := q.ListClearDevStepMessageReservations(ctx, step.ID)
	if err != nil {
		return err
	}
	if len(reservations) != len(messages) || len(messages) > 3 {
		return complexExecutionRule("Planner question message usage is unknown")
	}
	finalID, finalPrompt := "", ""
	for _, message := range messages {
		reservation, err := q.GetClearDevAgentMessageReservation(ctx, message.id)
		if err != nil {
			return err
		}
		if reservation.DevelopmentProjectID != plan.DevelopmentRequirementID || reservation.LogicalStepID != step.ID || reservation.AttemptID != message.attempt.ID || reservation.AoSessionID != binding.AoSessionID.String || reservation.Source != message.source || reservation.PromptSha256 != message.prompt || reservation.BudgetVersion != string(core.MessageBudgetV1) || reservation.BudgetID.Valid {
			return complexExecutionRule("Planner message reservation changed")
		}
		confirmation, err := q.GetClearDevAgentMessageConfirmation(ctx, message.id)
		if err != nil {
			return err
		}
		if confirmation.TurnID == "" {
			return complexExecutionRule("Planner question delivery not confirmed")
		}
		if _, err := q.GetClearDevBoundAgentSendEvent(ctx, gen.GetClearDevBoundAgentSendEventParams{AttemptID: message.attempt.ID, ClientMessageID: message.id, TurnID: confirmation.TurnID, PromptSha256: message.prompt, Status: message.sendStatus}); err != nil {
			return err
		}
		latest, err := q.GetLatestClearDevAgentMessageEvent(ctx, gen.GetLatestClearDevAgentMessageEventParams{AttemptID: message.attempt.ID, ClientMessageID: message.id})
		if err != nil {
			return err
		}
		result, resultErr := q.GetClearDevAgentStepResult(ctx, gen.GetClearDevAgentStepResultParams{AttemptID: message.attempt.ID, ResultIndex: message.result})
		completed := latest.Status == "COMPLETED" && latest.TurnState == "completed"
		invalid := latest.Status == "FAILED" && latest.FailureCategory == "RESULT_INVALID" && latest.TurnState == "failed"
		if completed || invalid {
			if resultErr != nil {
				return resultErr
			}
			if latest.TurnID != confirmation.TurnID || result.ClientMessageID != message.id || result.TurnID != confirmation.TurnID || result.Source != message.resultKind || result.FinalMessageID == "" || result.RawMessageSha256 != complexExecutionRawDigest([]byte(result.RawMessageText)) {
				return complexExecutionRule("Planner result binding changed")
			}
			if _, err := q.GetClearDevCompletedAgentResultEvent(ctx, gen.GetClearDevCompletedAgentResultEventParams{AttemptID: message.attempt.ID, ClientMessageID: message.id, TurnID: result.TurnID}); err != nil {
				return err
			}
			parsed, err := q.GetClearDevAgentStepResultParse(ctx, result.ID)
			if err != nil {
				return err
			}
			if parsed.Conclusion == "INVALID" && parsed.ErrorSummary != "" {
				continue
			}
			if parsed.Conclusion != "VALID" || !completed || result.TurnID != plan.TurnID || result.FinalMessageID != plan.FinalMessageID || result.RawMessageText != step.FinalMessageText.String || result.RawMessageSha256 != step.MessageSha256.String || finalID != "" {
				return complexExecutionRule("Planner question is not the unique final valid result")
			}
			finalID, finalPrompt = message.id, message.prompt
			continue
		}
		if resultErr != nil && !errors.Is(resultErr, sql.ErrNoRows) {
			return resultErr
		}
		if !errors.Is(resultErr, sql.ErrNoRows) || latest.TurnID != confirmation.TurnID || !planningFailureCanContinue(latest.FailureCategory) || (latest.Status != "FAILED" && latest.Status != "INTERRUPTED") || (latest.TurnState != "failed" && latest.TurnState != "interrupted") {
			return complexExecutionRule("Planner question has an unknown external result")
		}
	}
	if finalID == "" {
		return complexExecutionRule("Planner question has no confirmed valid result")
	}
	native, err := q.GetClearDevPlannerQuestionSession(ctx, gen.GetClearDevPlannerQuestionSessionParams{PlanID: plan.ID, ActualClientMessageID: finalID})
	if err != nil {
		return complexExecutionRule("original Planner conversation cannot be verified")
	}
	if string(native.AoSessionID) != binding.AoSessionID.String || complexExecutionRawDigest([]byte(native.QuestionPromptText)) != finalPrompt || native.QuestionFinalMessageText != step.FinalMessageText.String {
		return complexExecutionRule("original Planner conversation changed")
	}
	session, err := q.GetSession(ctx, domain.SessionID(binding.AoSessionID.String))
	if err != nil {
		return err
	}
	matchingTool, err := q.IsClearDevPlanningContinuationSession(ctx, binding.AoSessionID.String)
	if err != nil {
		return err
	}
	if !matchingTool {
		return complexExecutionRule("original Planner tool or model changed")
	}
	if session.IsTerminated || session.ActivityState != domain.ActivityIdle || session.CreationIdempotencyKey != binding.SessionCreationIdempotencyKey {
		return complexExecutionRule("original Planner session is unavailable")
	}
	active, err := q.CountClearDevUnsettledSessionTurns(ctx, domain.SessionID(binding.AoSessionID.String))
	if err != nil {
		return err
	}
	if active != 0 {
		return complexExecutionRule("original Planner has unsettled turns")
	}
	return nil
}
