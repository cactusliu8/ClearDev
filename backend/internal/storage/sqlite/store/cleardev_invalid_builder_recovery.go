package store

import (
	"context"
	"database/sql"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ReadClearDevStoppedBuilderResult reports a rejected result or confirmed failed turn.
// The original failure is preserved. It never starts an attempt, corrects a reply, or grants a budget.
// ApplyClearDevWorkflowRecovery repeats this proof inside its write transaction.
func (s *Store) ReadClearDevStoppedBuilderResult(ctx context.Context, runID, dispatchID string) (string, bool, error) {
	run, err := s.qr.GetClearDevComplexExecutionRun(ctx, runID)
	if err != nil {
		return "", false, err
	}
	dispatch, err := s.qr.GetClearDevComplexExecutionTaskAttempt(ctx, dispatchID)
	if err != nil {
		return "", false, err
	}
	return stoppedBuilderResultProof(ctx, s.qr, run, dispatch)
}

type invalidBuilderMessage struct {
	attempt    gen.CleardevAgentStepAttempt
	id         string
	prompt     string
	source     string
	resultKind string
	result     int64
	sendStatus string
}

func stoppedBuilderResultProof(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, dispatch gen.CleardevComplexExecutionTaskAttempt) (string, bool, error) {
	if dispatch.ExecutionRunID != run.ID || (dispatch.ReasonCode != "BUILDER_RESULT_INVALID" && dispatch.ReasonCode != "BUILDER_UNAVAILABLE") ||
		(dispatch.Status != "NEEDS_HUMAN" && dispatch.Status != "BLOCKED") || !dispatch.SettledAt.Valid {
		return "", false, nil
	}
	step, err := q.GetClearDevComplexExecutionAgentStep(ctx, dispatch.AgentStepID)
	if err != nil {
		return "", false, missingBuilderProof(err)
	}
	binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, dispatch.BuilderRoleBindingID)
	if err != nil {
		return "", false, missingBuilderProof(err)
	}
	if step.RoleBindingID != binding.ID || step.StepKind != string(core.ComplexExecutionAgentStepBuilderTask) ||
		step.RequestID != dispatch.ID || binding.ExecutionRunID != run.ID || binding.Role != "BUILDER" ||
		binding.Status != "BOUND" || binding.AoSessionID.String == "" {
		return "", false, nil
	}
	active, err := q.CountClearDevUnsettledSessionTurns(ctx, domain.SessionID(binding.AoSessionID.String))
	if err != nil || active != 0 {
		return "", false, err
	}
	attempts, err := q.ListClearDevAgentStepAttemptStates(ctx, gen.ListClearDevAgentStepAttemptStatesParams{DevelopmentProjectID: run.DevelopmentProjectID, LogicalStepID: step.ID})
	if err != nil {
		return "", false, err
	}
	if len(attempts) < 1 || len(attempts) > 2 {
		return "", false, nil
	}
	if dispatch.ReasonCode == "BUILDER_UNAVAILABLE" {
		return failedBuilderTurnProof(ctx, q, run, dispatch, step, binding, attempts)
	}
	correction, err := q.GetClearDevParseCorrection(ctx, step.ID)
	if err != nil {
		return "", false, missingBuilderProof(err)
	}
	// The correction occupancy stores the STEP-level ID. The sender maps it
	// onto its attempt-specific ID; do not change or reinterpret saved prompts.
	if correction.AttemptNumber < 1 || correction.AttemptNumber > int64(len(attempts)) ||
		correction.ClientMessageID != step.ClientMessageID+":parse-correction" ||
		correction.PromptSha256 != complexExecutionRawDigest([]byte(correction.PromptText)) {
		return "", false, nil
	}
	messages := make([]invalidBuilderMessage, 0, 3)
	var final gen.CleardevAgentAttemptEvent
	for i, attempt := range attempts {
		id, source := step.ClientMessageID, string(core.AgentMessageOriginal)
		if i == 1 {
			id, source = id+":attempt:2", string(core.AgentMessageRecoveryOriginal)
		}
		if attempt.AttemptNumber != int64(i+1) || attempt.RoleBindingID != binding.ID || attempt.ClientMessageID != id ||
			attempt.AoSessionID != binding.AoSessionID.String || attempt.PromptSha256 != step.PromptSha256 ||
			attempt.StepCategory != string(core.AgentStepCategoryComplexExecution) || attempt.StepKind != step.StepKind {
			return "", false, nil
		}
		messages = append(messages, invalidBuilderMessage{attempt: attempt, id: id, prompt: step.PromptSha256, source: source, resultKind: string(core.AgentResultOriginal), result: 1, sendStatus: "SENT"})
		if correction.AttemptNumber == attempt.AttemptNumber {
			messages = append(messages, invalidBuilderMessage{attempt: attempt, id: id + ":parse-correction", prompt: correction.PromptSha256, source: string(core.AgentMessageParseCorrection), resultKind: string(core.AgentResultCorrection), result: 2, sendStatus: "CORRECTION_SENT"})
		}
		latest, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, attempt.ID)
		if err != nil {
			return "", false, missingBuilderProof(err)
		}
		if (latest.Status != "FAILED" && latest.Status != "INTERRUPTED") ||
			(latest.ClientMessageID != id && (correction.AttemptNumber != attempt.AttemptNumber || latest.ClientMessageID != id+":parse-correction")) {
			return "", false, nil
		}
		final = latest
	}
	if final.Status != "FAILED" || final.FailureCategory != "RESULT_INVALID" {
		return "", false, nil
	}
	reservations, err := q.ListClearDevStepMessageReservations(ctx, step.ID)
	if err != nil {
		return "", false, err
	}
	if len(reservations) != len(messages) {
		return "", false, nil
	}
	budget, err := q.GetClearDevMessageRoleBudget(ctx, step.ID)
	if err != nil {
		return "", false, missingBuilderProof(err)
	}
	if budget.ExecutionRunID != run.ID || budget.ComplexExecutionTaskID.String != dispatch.TaskMappingID || budget.RoleKind != "BUILDER" {
		return "", false, nil
	}
	summary := ""
	for _, message := range messages {
		reservation, err := q.GetClearDevAgentMessageReservation(ctx, message.id)
		if err != nil {
			return "", false, missingBuilderProof(err)
		}
		if reservation.DevelopmentProjectID != run.DevelopmentProjectID || reservation.LogicalStepID != step.ID ||
			reservation.AttemptID != message.attempt.ID || reservation.AoSessionID != binding.AoSessionID.String ||
			reservation.Source != message.source || reservation.PromptSha256 != message.prompt ||
			reservation.BudgetVersion != string(core.MessageBudgetV1) || reservation.BudgetID.String != budget.ID {
			return "", false, nil
		}
		detail, known, err := invalidBuilderMessageEnded(ctx, q, message)
		if err != nil || !known {
			return "", false, err
		}
		if message.attempt.ID == attempts[len(attempts)-1].ID && message.id == final.ClientMessageID {
			summary = detail
		}
	}
	return summary, summary != "", nil
}

// Every reserved message needs its own send/turn/result proof. Another
// message's later INVALID event cannot hide this message's DELIVERY_UNKNOWN.
func invalidBuilderMessageEnded(ctx context.Context, q *gen.Queries, message invalidBuilderMessage) (string, bool, error) {
	confirmation, err := q.GetClearDevAgentMessageConfirmation(ctx, message.id)
	if err != nil {
		return "", false, missingBuilderProof(err)
	}
	if confirmation.TurnID == "" {
		return "", false, nil
	}
	if _, err := q.GetClearDevBoundAgentSendEvent(ctx, gen.GetClearDevBoundAgentSendEventParams{AttemptID: message.attempt.ID, ClientMessageID: message.id, TurnID: confirmation.TurnID, PromptSha256: message.prompt, Status: message.sendStatus}); err != nil {
		return "", false, missingBuilderProof(err)
	}
	latest, err := q.GetLatestClearDevAgentMessageEvent(ctx, gen.GetLatestClearDevAgentMessageEventParams{AttemptID: message.attempt.ID, ClientMessageID: message.id})
	if err != nil {
		return "", false, missingBuilderProof(err)
	}
	if latest.FailureCategory == "DELIVERY_UNKNOWN" || latest.FailureCategory == "OBSERVATION_TIMEOUT" || latest.TurnID != "" && latest.TurnID != confirmation.TurnID {
		return "", false, nil
	}
	if latest.Status == "COMPLETED" && (latest.TurnState != "completed" || latest.TurnID != confirmation.TurnID) ||
		latest.Status == "FAILED" && latest.FailureCategory == "RESULT_INVALID" && latest.TurnState != "failed" {
		return "", false, nil
	}
	result, resultErr := q.GetClearDevAgentStepResult(ctx, gen.GetClearDevAgentStepResultParams{AttemptID: message.attempt.ID, ResultIndex: message.result})
	if latest.Status == "COMPLETED" || latest.Status == "FAILED" && latest.FailureCategory == "RESULT_INVALID" {
		if resultErr != nil {
			return "", false, missingBuilderProof(resultErr)
		}
		if result.Source != message.resultKind || result.ClientMessageID != message.id || result.TurnID != confirmation.TurnID || result.FinalMessageID == "" ||
			result.RawMessageSha256 != complexExecutionRawDigest([]byte(result.RawMessageText)) {
			return "", false, nil
		}
		if _, err := q.GetClearDevCompletedAgentResultEvent(ctx, gen.GetClearDevCompletedAgentResultEventParams{AttemptID: message.attempt.ID, ClientMessageID: message.id, TurnID: result.TurnID}); err != nil {
			return "", false, missingBuilderProof(err)
		}
		parse, err := q.GetClearDevAgentStepResultParse(ctx, result.ID)
		if err != nil {
			return "", false, missingBuilderProof(err)
		}
		if parse.Conclusion != string(core.AgentResultParseInvalid) || parse.ErrorSummary == "" {
			return "", false, nil
		}
		return parse.ErrorSummary, true, nil
	}
	// A provider failure has a confirmed failed/interrupted turn and no
	// purported completed result. Unconfirmed failures remain unavailable.
	if (latest.Status == "FAILED" || latest.Status == "INTERRUPTED") &&
		(latest.TurnState == "failed" || latest.TurnState == "interrupted") && latest.TurnID == confirmation.TurnID && errors.Is(resultErr, sql.ErrNoRows) {
		return "", true, nil
	}
	if resultErr != nil && !errors.Is(resultErr, sql.ErrNoRows) {
		return "", false, resultErr
	}
	return "", false, nil
}

func invalidBuilderRecoverySessionCurrent(ctx context.Context, q *gen.Queries, r core.WorkflowRecovery) (bool, error) {
	binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, r.BindingID)
	if err != nil {
		return false, missingBuilderProof(err)
	}
	session, err := q.GetSession(ctx, domain.SessionID(binding.AoSessionID.String))
	if err != nil {
		return false, missingBuilderProof(err)
	}
	return !session.IsTerminated && session.ProviderConversationID != "" && session.ProviderConversationID == r.ProviderConversationID &&
		session.WorkspacePath == binding.WorkspacePath && session.CreationIdempotencyKey == binding.SessionCreationIdempotencyKey &&
		(session.ActivityState == domain.ActivityIdle || session.ActivityState == domain.ActivityExited), nil
}

func missingBuilderProof(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
