package store

import (
	"context"
	"database/sql"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// A manual continuation is a new budgeted Builder round, never a resend or an
// assertion that the provider has been repaired. Every earlier message must end.
func failedBuilderTurnProof(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, dispatch gen.CleardevComplexExecutionTaskAttempt, step gen.CleardevComplexExecutionAgentStep, binding gen.CleardevComplexExecutionRoleBinding, attempts []gen.CleardevAgentStepAttempt) (string, bool, error) {
	if step.SendStatus != "SENT" && step.SendStatus != "PENDING" {
		return "", false, nil
	}
	if _, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(dispatch.ID)); !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	correction, err := q.GetClearDevParseCorrection(ctx, step.ID)
	hasCorrection := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	if hasCorrection && (correction.AttemptNumber < 1 || correction.AttemptNumber > int64(len(attempts)) || correction.ClientMessageID != step.ClientMessageID+":parse-correction" || correction.PromptSha256 != complexExecutionRawDigest([]byte(correction.PromptText))) {
		return "", false, nil
	}
	var messages []invalidBuilderMessage
	var final gen.CleardevAgentAttemptEvent
	for i, attempt := range attempts {
		id, source := step.ClientMessageID, string(core.AgentMessageOriginal)
		if i == 1 {
			id += ":attempt:2"
			source = string(core.AgentMessageRecoveryOriginal)
		}
		if attempt.AttemptNumber != int64(i+1) || attempt.RoleBindingID != binding.ID || attempt.ClientMessageID != id || attempt.AoSessionID != binding.AoSessionID.String || attempt.PromptSha256 != step.PromptSha256 || attempt.StepCategory != string(core.AgentStepCategoryComplexExecution) || attempt.StepKind != step.StepKind {
			return "", false, nil
		}
		messages = append(messages, invalidBuilderMessage{attempt: attempt, id: id, prompt: step.PromptSha256, source: source, resultKind: string(core.AgentResultOriginal), result: 1, sendStatus: "SENT"})
		lastMessage := id
		if hasCorrection && correction.AttemptNumber == attempt.AttemptNumber {
			lastMessage = id + ":parse-correction"
			messages = append(messages, invalidBuilderMessage{attempt: attempt, id: lastMessage, prompt: correction.PromptSha256, source: string(core.AgentMessageParseCorrection), resultKind: string(core.AgentResultCorrection), result: 2, sendStatus: "CORRECTION_SENT"})
		}
		latest, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, attempt.ID)
		if err != nil {
			return "", false, missingBuilderProof(err)
		}
		if latest.ClientMessageID != lastMessage || latest.Status != "FAILED" || latest.TurnState != "failed" || latest.TurnID == "" {
			return "", false, nil
		}
		final = latest
	}
	if final.FailureCategory != string(domain.AgentFailureProvider) || final.ErrorSummary == "" {
		return "", false, nil
	}

	// Bind the recorded failure to AO's actual terminal turn and its original
	// provider lineage, not merely today's editable session metadata.
	turn, err := q.SelectConversationTurnByID(ctx, final.TurnID)
	if err != nil {
		return "", false, missingBuilderProof(err)
	}
	if turn.HandledBySessionID != domain.SessionID(binding.AoSessionID.String) || turn.State != domain.TurnStateFailed || !turn.CompletedAt.Valid || turn.RolledBackAt.Valid || turn.FailureCategory != string(domain.AgentFailureProvider) {
		return "", false, nil
	}
	branch, err := q.SelectConversationBranch(ctx, gen.SelectConversationBranchParams{ConversationID: turn.ConversationID, BranchID: turn.BranchID})
	if err != nil {
		return "", false, missingBuilderProof(err)
	}
	session, err := q.GetSession(ctx, domain.SessionID(binding.AoSessionID.String))
	if err != nil {
		return "", false, missingBuilderProof(err)
	}
	if branch.ProviderConversationID == "" || branch.ProviderConversationID != session.ProviderConversationID {
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
	for _, message := range messages {
		reservation, err := q.GetClearDevAgentMessageReservation(ctx, message.id)
		if err != nil {
			return "", false, missingBuilderProof(err)
		}
		if reservation.DevelopmentProjectID != run.DevelopmentProjectID || reservation.LogicalStepID != step.ID || reservation.AttemptID != message.attempt.ID || reservation.AoSessionID != binding.AoSessionID.String || reservation.Source != message.source || reservation.PromptSha256 != message.prompt || reservation.BudgetVersion != string(core.MessageBudgetV1) || reservation.BudgetID.String != budget.ID {
			return "", false, nil
		}
		_, known, err := invalidBuilderMessageEnded(ctx, q, message)
		if err != nil || !known {
			return "", false, err
		}
	}
	// Bind the summary to the immutable failure identity as well as its text.
	return final.ErrorSummary + "\nFailure event: " + final.ID, true, nil
}
