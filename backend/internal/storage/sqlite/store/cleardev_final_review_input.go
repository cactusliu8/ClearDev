package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// FinalReviewInputRejected is read-only. The same durable proof is checked
// again inside the recovery transaction, not supplied by the HTTP caller.
func (s *Store) FinalReviewInputRejected(ctx context.Context, id string) (bool, error) {
	return finalReviewInputRejected(ctx, s.qr, id)
}

func finalReviewInputRejected(ctx context.Context, q *gen.Queries, id string) (bool, error) {
	review, err := q.GetClearDevRequirementFinalReviewByID(ctx, id)
	if err != nil {
		return false, err
	}
	if review.Status == "FAILED" && review.ReasonCode == "REQUIREMENT_FINAL_REVIEW_RESULT_INVALID" {
		return finalReviewInvalidResultProof(ctx, q, id)
	}
	if review.Status != "FAILED" || review.ReasonCode != "REQUIREMENT_FINAL_REVIEWER_UNAVAILABLE" || !review.SentAt.Valid || !review.SettledAt.Valid || review.AoSessionID.String == "" {
		return false, nil
	}
	session, err := q.GetSession(ctx, domain.SessionID(review.AoSessionID.String))
	if err != nil {
		return false, err
	}
	if session.IsTerminated || session.ActivityState != domain.ActivityIdle || session.WorkspacePath != review.WorkspacePath.String || session.SessionMode != domain.SessionModeChat || session.ProviderConversationID == "" || session.CreationIdempotencyKey != "cleardev-requirement-final-review:"+id || session.PreviewURL != "" {
		return false, nil
	}
	preflight, err := q.GetLatestClearDevControlledPreflightForBinding(ctx, id)
	if err != nil {
		return false, err
	}
	if preflight.Outcome != "PASSED" || preflight.ResolvedModel != session.Model || preflight.AoProjectID != string(session.ProjectID) || preflight.CheckedAt.After(review.SentAt.Time) {
		return false, nil
	}
	conversation, err := q.SelectConversationBySession(ctx, &session.ID)
	if err != nil {
		return false, err
	}
	branch, err := q.SelectConversationBranch(ctx, gen.SelectConversationBranchParams{ConversationID: conversation.ID, BranchID: conversation.ActiveBranchID})
	if err != nil {
		return false, err
	}
	if branch.ProviderConversationID != session.ProviderConversationID || branch.ParentBranchID.Valid {
		return false, nil
	}
	turns, err := q.SelectConversationTurns(ctx, conversation.ID)
	if err != nil {
		return false, err
	}
	if len(turns) != 1 {
		return false, nil
	}
	turn := turns[0]
	if turn.State != domain.TurnStateFailed || !turn.CompletedAt.Valid || turn.ProviderTurnID == "" || turn.HandledBySessionID != session.ID || turn.ControllerGeneration != session.ControllerGeneration || turn.BranchID != branch.ID {
		return false, nil
	}
	messages, err := q.SelectConversationMessages(ctx, conversation.ID)
	if err != nil {
		return false, err
	}
	if len(messages) != 1 {
		return false, nil
	}
	message := messages[0]
	if message.Role != domain.MessageRoleUser || message.Origin != domain.MessageOriginAutomation || message.ClientMessageID != id+":message" || message.TurnID.String != turn.ID || complexExecutionRawDigest([]byte(message.Text)) != review.PromptSha256 {
		return false, nil
	}
	activities, err := q.SelectConversationActivities(ctx, conversation.ID)
	if err != nil {
		return false, err
	}
	for _, activity := range activities {
		if string(activity.Kind) != "error" || activity.TurnID.String != turn.ID || !contextLengthRejection(activity.Summary) {
			return false, nil
		}
	}
	attempts, err := q.ListClearDevAgentStepAttempts(ctx, review.DevelopmentProjectID)
	if err != nil {
		return false, err
	}
	count := 0
	var attemptID string
	for _, a := range attempts {
		if a.LogicalStepID == id+":step" {
			count++
			attemptID = a.ID
			if a.AttemptNumber != 1 || a.AoSessionID != string(session.ID) || a.PromptSha256 != review.PromptSha256 || a.ClientMessageID != message.ClientMessageID || a.RoleBindingID != id {
				return false, nil
			}
		}
	}
	if count != 1 {
		return false, nil
	}
	events, err := q.ListClearDevAgentAttemptEvents(ctx, review.DevelopmentProjectID)
	if err != nil {
		return false, err
	}
	sent, failed := false, false
	for _, event := range events {
		if event.AttemptID == attemptID {
			if event.Status == "SENT" && event.TurnID == turn.ID {
				sent = true
			}
			failed = event.Status == "FAILED" && event.TurnID == turn.ID && event.TurnState == "failed"
		}
	}
	if !sent || !failed {
		return false, nil
	}
	native, err := q.SelectConversationProviderEvents(ctx, gen.SelectConversationProviderEventsParams{ConversationID: conversation.ID, PageLimit: 4096})
	if err != nil {
		return false, err
	}
	if len(native) == 4096 {
		return false, nil
	}
	rejected, ended := false, false
	for _, event := range native {
		var payload struct {
			Kind   string `json:"kind"`
			TurnID string `json:"providerTurnId"`
			State  string `json:"turnState"`
			Error  string `json:"error"`
		}
		if decodeErr := json.Unmarshal([]byte(event.PayloadJson), &payload); decodeErr != nil {
			return false, decodeErr
		}
		if event.SessionID != session.ID {
			return false, nil
		}
		switch payload.Kind {
		case "controller.state":
		case "turn.started":
			if payload.TurnID != turn.ProviderTurnID {
				return false, nil
			}
		case "error":
			if payload.TurnID != turn.ProviderTurnID || !contextLengthRejection(payload.Error) {
				return false, nil
			}
			rejected = true
		case "turn.completed":
			if payload.TurnID != turn.ProviderTurnID || payload.State != "failed" {
				return false, nil
			}
			ended = true
		default:
			return false, nil
		}
	}
	return rejected && ended, nil
}

func contextLengthRejection(raw string) bool {
	var failure struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(raw), &failure) != nil {
		return false
	}
	return failure.Code == -32603 && strings.Contains(failure.Message, "The input is longer than the model's context length") && strings.Contains(failure.Message, "invalid_request_error")
}
