package store

import (
	"context"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// A malformed answer is recoverable only after every reserved message has a
// completed, parsed result and its original native identity remains intact.
func finalReviewInvalidResultProof(ctx context.Context, q *gen.Queries, id string) (bool, error) {
	review, err := q.GetClearDevRequirementFinalReviewByID(ctx, id)
	if err != nil {
		return false, err
	}
	if review.Status != "FAILED" || review.ReasonCode != "REQUIREMENT_FINAL_REVIEW_RESULT_INVALID" || !review.SentAt.Valid || !review.SettledAt.Valid {
		return false, nil
	}
	session, err := q.GetSession(ctx, domain.SessionID(review.AoSessionID.String))
	if err != nil {
		return false, err
	}
	if session.IsTerminated || session.ActivityState != domain.ActivityIdle || session.ProviderConversationID == "" || session.PreviewURL != "" || session.WorkspacePath != review.WorkspacePath.String || session.CreationIdempotencyKey != "cleardev-requirement-final-review:"+id {
		return false, nil
	}
	active, err := q.CountClearDevUnsettledSessionTurns(ctx, session.ID)
	if err != nil || active != 0 {
		return false, err
	}
	step := id + ":step"
	attempts, err := q.ListClearDevAgentStepAttemptStates(ctx, gen.ListClearDevAgentStepAttemptStatesParams{DevelopmentProjectID: review.DevelopmentProjectID, LogicalStepID: step})
	if err != nil {
		return false, err
	}
	if len(attempts) != 1 {
		return false, nil
	}
	a := attempts[0]
	if a.AttemptNumber != 1 || a.RoleBindingID != id || a.AoSessionID != string(session.ID) || a.PromptSha256 != review.PromptSha256 || a.ClientMessageID != id+":message" {
		return false, nil
	}
	correction, err := q.GetClearDevParseCorrection(ctx, step)
	if err != nil {
		return false, missingBuilderProof(err)
	}
	if correction.AttemptNumber != 1 || correction.ClientMessageID != a.ClientMessageID+":parse-correction" || correction.PromptSha256 != complexExecutionRawDigest([]byte(correction.PromptText)) {
		return false, nil
	}
	latest, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, a.ID)
	if err != nil {
		return false, missingBuilderProof(err)
	}
	if latest.Status != "FAILED" || latest.FailureCategory != "RESULT_INVALID" || latest.ClientMessageID != correction.ClientMessageID {
		return false, nil
	}
	reservations, err := q.ListClearDevStepMessageReservations(ctx, step)
	if err != nil || len(reservations) != 2 {
		return false, err
	}
	messages := []invalidBuilderMessage{
		{attempt: a, id: a.ClientMessageID, prompt: a.PromptSha256, source: string(core.AgentMessageOriginal), resultKind: string(core.AgentResultOriginal), result: 1, sendStatus: "SENT"},
		{attempt: a, id: correction.ClientMessageID, prompt: correction.PromptSha256, source: string(core.AgentMessageParseCorrection), resultKind: string(core.AgentResultCorrection), result: 2, sendStatus: "CORRECTION_SENT"},
	}
	for _, m := range messages {
		reservation, err := q.GetClearDevAgentMessageReservation(ctx, m.id)
		if err != nil {
			return false, missingBuilderProof(err)
		}
		if reservation.DevelopmentProjectID != review.DevelopmentProjectID || reservation.LogicalStepID != step || reservation.AttemptID != a.ID || reservation.AoSessionID != string(session.ID) || reservation.Source != m.source || reservation.PromptSha256 != m.prompt || reservation.BudgetVersion != string(core.MessageBudgetV1) {
			return false, nil
		}
		if _, known, err := invalidBuilderMessageEnded(ctx, q, m); err != nil || !known {
			return false, err
		}
		confirmation, err := q.GetClearDevAgentMessageConfirmation(ctx, m.id)
		if err != nil {
			return false, err
		}
		turn, err := q.SelectConversationTurnByID(ctx, confirmation.TurnID)
		if err != nil {
			return false, missingBuilderProof(err)
		}
		if turn.State != domain.TurnStateCompleted || !turn.CompletedAt.Valid || turn.RolledBackAt.Valid || turn.HandledBySessionID != session.ID {
			return false, nil
		}
		branch, err := q.SelectConversationBranch(ctx, gen.SelectConversationBranchParams{ConversationID: turn.ConversationID, BranchID: turn.BranchID})
		if err != nil {
			return false, err
		}
		if branch.ProviderConversationID != session.ProviderConversationID {
			return false, nil
		}
	}
	return true, nil
}
