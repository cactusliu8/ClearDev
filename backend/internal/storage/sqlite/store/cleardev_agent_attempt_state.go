package store

import (
	"context"
	"database/sql"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ListClearDevAgentStepAttemptStates reads only the chosen step's attempt metadata and last events.
func (s *Store) ListClearDevAgentStepAttemptStates(ctx context.Context, requirementID, stepID string) ([]core.AgentStepAttemptView, error) {
	rows, err := s.qr.ListClearDevAgentStepAttemptStates(ctx, gen.ListClearDevAgentStepAttemptStatesParams{DevelopmentProjectID: requirementID, LogicalStepID: stepID})
	if err != nil {
		return nil, err
	}
	return s.agentAttemptStates(ctx, rows)
}

// ListLatestClearDevAgentAttemptStates scans only the latest attempt metadata for each step.
func (s *Store) ListLatestClearDevAgentAttemptStates(ctx context.Context, requirementID string) ([]core.AgentStepAttemptView, error) {
	rows, err := s.qr.ListLatestClearDevAgentAttemptStates(ctx, requirementID)
	if err != nil {
		return nil, err
	}
	return s.agentAttemptStates(ctx, rows)
}

func (s *Store) agentAttemptStates(ctx context.Context, rows []gen.CleardevAgentStepAttempt) ([]core.AgentStepAttemptView, error) {
	views := make([]core.AgentStepAttemptView, 0, len(rows))
	for _, row := range rows {
		event, err := s.qr.GetLatestClearDevAgentAttemptEventForAttempt(ctx, row.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		views = append(views, agentAttemptState(row, event))
	}
	return views, nil
}

// GetClearDevAgentAttemptState loads one attempt's state without result text.
func (s *Store) GetClearDevAgentAttemptState(ctx context.Context, requirementID, attemptID string) (core.AgentStepAttemptView, error) {
	row, err := s.qr.GetClearDevAgentAttemptState(ctx, gen.GetClearDevAgentAttemptStateParams{DevelopmentProjectID: requirementID, ID: attemptID})
	if err != nil {
		return core.AgentStepAttemptView{}, err
	}
	views, err := s.agentAttemptStates(ctx, []gen.CleardevAgentStepAttempt{row})
	if err != nil {
		return core.AgentStepAttemptView{}, err
	}
	return views[0], nil
}

// GetClearDevAgentDeliveryState keeps original and correction message boundaries independent.
func (s *Store) GetClearDevAgentDeliveryState(ctx context.Context, requirementID, attemptID, messageID string) (core.AgentStepAttemptView, error) {
	row, err := s.qr.GetClearDevAgentAttemptState(ctx, gen.GetClearDevAgentAttemptStateParams{DevelopmentProjectID: requirementID, ID: attemptID})
	if err != nil {
		return core.AgentStepAttemptView{}, err
	}
	event, err := s.qr.GetLatestClearDevAgentMessageEvent(ctx, gen.GetLatestClearDevAgentMessageEventParams{AttemptID: attemptID, ClientMessageID: messageID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return core.AgentStepAttemptView{}, err
	}
	return agentAttemptState(row, event), nil
}

func agentAttemptState(row gen.CleardevAgentStepAttempt, event gen.CleardevAgentAttemptEvent) core.AgentStepAttemptView {
	attempt := agentStepAttemptToDomain(row)
	view := core.AgentStepAttemptView{ID: attempt.ID, LogicalStepID: attempt.LogicalStepID, StepCategory: attempt.StepCategory, StepKind: attempt.StepKind, AttemptNumber: attempt.AttemptNumber, RoleBindingID: attempt.RoleBindingID, AOSessionID: attempt.AOSessionID, ClientMessageID: attempt.ClientMessageID, PromptSHA256: attempt.PromptSHA256, TriggerFailureEventID: attempt.TriggerFailureEventID, RequestedAt: &attempt.RequestedAt, CreatedAt: attempt.CreatedAt, RequestedAtSemantics: attempt.RequestedAtSemantics, SendStatus: core.AgentAttemptPending, ParseConclusion: core.AgentResultParseUnparsed, Results: []core.AgentStepResultView{}}
	if event.ID != "" {
		fact := agentAttemptEventToDomain(event)
		view.LastEventID = fact.ID
		view.SendStatus = fact.Status
		view.LastClientMessageID = fact.ClientMessageID
		view.TurnID = fact.TurnID
		view.TurnState = fact.TurnState
		view.FailureCategory = fact.FailureCategory
		view.Retryable = fact.Retryable
		view.RetryAt = fact.RetryAt
		view.ProviderErrorCode = fact.ProviderErrorCode
		view.ErrorSummary = fact.ErrorSummary
		view.LastObservedAt = &fact.RecordedAt
	}
	return view
}
