package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// EnsureClearDevAgentStepAttempt inserts the immutable first attempt, or returns
// the identical row another wake or process already inserted.
func (s *Store) EnsureClearDevAgentStepAttempt(ctx context.Context, attempt cleardev.AgentStepAttempt) (cleardev.AgentStepAttempt, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	created := false
	err := s.inTx(ctx, "ensure ClearDev Agent step attempt", func(q *gen.Queries) error {
		existingRow, existingErr := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{
			LogicalStepID: attempt.LogicalStepID, AttemptNumber: attempt.AttemptNumber,
		})
		if existingErr == nil {
			stored := agentStepAttemptToDomain(existingRow)
			if !cleardev.SameAgentStepAttempt(stored, attempt) {
				return errors.New("saved ClearDev Agent attempt does not match immutable logical step")
			}
			attempt = stored
			return nil
		}
		if !errors.Is(existingErr, sql.ErrNoRows) {
			return existingErr
		}
		if !cleardev.ValidAgentAttemptCreation(attempt) {
			return errors.New("invalid attempt creation time")
		}
		if attempt.AttemptNumber == 2 {
			fixed, fixedErr := q.GetClearDevFixedRecoveryForStep(ctx, nullableString(attempt.LogicalStepID))
			if fixedErr == nil {
				result, resultErr := q.GetClearDevFixedRecoveryResult(ctx, fixed.ID)
				if resultErr != nil || result.Outcome != "PASS" {
					return complexExecutionRule("fixed recovery has not confirmed its external result")
				}
			} else if !errors.Is(fixedErr, sql.ErrNoRows) {
				return fixedErr
			}
			firstRow, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{
				LogicalStepID: attempt.LogicalStepID, AttemptNumber: 1,
			})
			if err != nil {
				return errors.New("second ClearDev Agent attempt has no first attempt")
			}
			triggerRow, err := q.GetClearDevAgentAttemptEvent(ctx, attempt.TriggerFailureEventID)
			if err != nil {
				return errors.New("second ClearDev Agent attempt has no triggering failure")
			}
			latestRow, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, firstRow.ID)
			if err != nil {
				return errors.New("second ClearDev Agent attempt has no latest first-attempt evidence")
			}
			first := agentStepAttemptToDomain(firstRow)
			if authorized, err := authorizedReplacementAttempt(ctx, q, attempt); err != nil {
				return err
			} else if authorized {
				first.RoleBindingID = attempt.RoleBindingID
				first.AOSessionID = attempt.AOSessionID
			}
			builderAuthorized, err := authorizedBuilderReplacementAttempt(ctx, q, attempt)
			if err != nil {
				return err
			}
			if !builderAuthorized && !cleardev.ValidSecondAgentAttempt(first, agentAttemptEventToDomain(triggerRow), agentAttemptEventToDomain(latestRow), attempt) {
				return errors.New("second ClearDev Agent attempt is not bound to the latest retryable failure")
			}
		} else if attempt.AttemptNumber != 1 || attempt.TriggerFailureEventID != "" {
			return errors.New("invalid ClearDev Agent attempt number or trigger")
		}
		rows, err := q.InsertClearDevAgentStepAttempt(ctx, gen.InsertClearDevAgentStepAttemptParams{
			ID: attempt.ID, DevelopmentProjectID: attempt.DevelopmentRequirementID,
			LogicalStepID: attempt.LogicalStepID, StepCategory: string(attempt.StepCategory),
			StepKind: string(attempt.StepKind), AttemptNumber: attempt.AttemptNumber,
			RoleBindingID: attempt.RoleBindingID, AoSessionID: attempt.AOSessionID,
			ClientMessageID: attempt.ClientMessageID, PromptSha256: attempt.PromptSHA256,
			TriggerFailureEventID: nullableString(attempt.TriggerFailureEventID),
			RequestedAt:           attempt.RequestedAt, CreatedAt: nullableTimePtr(attempt.CreatedAt), RequestedAtSemantics: string(attempt.RequestedAtSemantics),
		})
		if err != nil {
			return err
		}
		created = rows == 1
		row, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{
			LogicalStepID: attempt.LogicalStepID, AttemptNumber: attempt.AttemptNumber,
		})
		if err != nil {
			return err
		}
		stored := agentStepAttemptToDomain(row)
		if !cleardev.SameAgentStepAttempt(stored, attempt) {
			return errors.New("saved ClearDev Agent attempt does not match immutable logical step")
		}
		attempt = stored
		return nil
	})
	return attempt, created, err
}

// RecordClearDevAgentAttemptEvent appends one idempotent delivery fact.
func (s *Store) RecordClearDevAgentAttemptEvent(ctx context.Context, event cleardev.AgentAttemptEvent) error {
	_, err := s.EnsureClearDevAgentAttemptEvent(ctx, event)
	return err
}

// EnsureClearDevAgentAttemptEvent claims one immutable delivery boundary. Its
// boolean result lets concurrent wake-ups choose exactly one provider send.
func (s *Store) EnsureClearDevAgentAttemptEvent(ctx context.Context, event cleardev.AgentAttemptEvent) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	created := false
	err := s.inTx(ctx, "record ClearDev Agent attempt event", func(q *gen.Queries) error {
		rows, err := q.InsertClearDevAgentAttemptEvent(ctx, gen.InsertClearDevAgentAttemptEventParams{
			ID: event.ID, AttemptID: event.AttemptID, Status: string(event.Status),
			ClientMessageID: event.ClientMessageID, PromptSha256: event.PromptSHA256,
			TurnID: event.TurnID, TurnState: string(event.TurnState),
			FailureCategory: string(event.FailureCategory), Retryable: boolInt64(event.Retryable),
			RetryAt: nullableTimePtr(event.RetryAt), ProviderErrorCode: event.ProviderErrorCode,
			ErrorSummary: event.ErrorSummary, RecordedAt: event.RecordedAt,
		})
		if err != nil {
			return err
		}
		if rows == 1 {
			created = true
			return nil
		}
		row, err := q.GetClearDevAgentAttemptEvent(ctx, event.ID)
		if err != nil {
			return err
		}
		if !cleardev.SameAgentAttemptEvent(agentAttemptEventToDomain(row), event) {
			return errors.New("saved ClearDev Agent attempt event does not match immutable evidence")
		}
		return nil
	})
	return created, err
}

// RecordClearDevAgentStepResult saves the provider text before parsing it.
func (s *Store) RecordClearDevAgentStepResult(ctx context.Context, result cleardev.AgentStepResult) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record ClearDev Agent step result", func(q *gen.Queries) error {
		rows, err := q.InsertClearDevAgentStepResult(ctx, gen.InsertClearDevAgentStepResultParams{
			ID: result.ID, AttemptID: result.AttemptID, ResultIndex: result.ResultIndex,
			Source: string(result.Source), ClientMessageID: result.ClientMessageID,
			TurnID: result.TurnID, FinalMessageID: result.FinalMessageID,
			RawMessageText: result.RawMessageText, RawMessageSha256: result.RawMessageSHA256,
			ObservedAt: result.ObservedAt,
		})
		if err != nil {
			return err
		}
		if rows == 1 {
			return nil
		}
		row, err := q.GetClearDevAgentStepResult(ctx, gen.GetClearDevAgentStepResultParams{
			AttemptID: result.AttemptID, ResultIndex: result.ResultIndex,
		})
		if err != nil {
			return err
		}
		if !cleardev.SameAgentStepResult(agentStepResultToDomain(row), result) {
			return errors.New("saved ClearDev Agent result does not match immutable evidence")
		}
		return nil
	})
}

// RecordClearDevAgentStepResultParse appends the parser conclusion after the
// corresponding raw result is durable.
func (s *Store) RecordClearDevAgentStepResultParse(ctx context.Context, parse cleardev.AgentStepResultParse) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record ClearDev Agent result parse", func(q *gen.Queries) error {
		rows, err := q.InsertClearDevAgentStepResultParse(ctx, gen.InsertClearDevAgentStepResultParseParams{
			ResultID: parse.ResultID, Conclusion: string(parse.Conclusion),
			ErrorSummary: parse.ErrorSummary, ParsedAt: parse.ParsedAt,
		})
		if err != nil {
			return err
		}
		if rows == 1 {
			return nil
		}
		row, err := q.GetClearDevAgentStepResultParse(ctx, parse.ResultID)
		if err != nil {
			return err
		}
		if !cleardev.SameAgentStepResultParse(cleardev.AgentStepResultParse{ResultID: row.ResultID, Conclusion: cleardev.AgentResultParseConclusion(row.Conclusion), ErrorSummary: row.ErrorSummary, ParsedAt: row.ParsedAt}, parse) {
			return errors.New("saved ClearDev Agent parse does not match immutable evidence")
		}
		return nil
	})
}

// ListClearDevAgentStepAttempts assembles the append-only rows into the public
// evidence view. The last event is presentation only, not a second business state.
func (s *Store) ListClearDevAgentStepAttempts(ctx context.Context, requirementID string) ([]cleardev.AgentStepAttemptView, error) {
	attempts, err := s.qr.ListClearDevAgentStepAttempts(ctx, requirementID)
	if err != nil {
		return nil, err
	}
	events, err := s.qr.ListClearDevAgentAttemptEvents(ctx, requirementID)
	if err != nil {
		return nil, err
	}
	results, err := s.qr.ListClearDevAgentStepResults(ctx, requirementID)
	if err != nil {
		return nil, err
	}
	parses, err := s.qr.ListClearDevAgentStepResultParses(ctx, requirementID)
	if err != nil {
		return nil, err
	}

	parseByResult := make(map[string]gen.CleardevAgentStepResultParse, len(parses))
	for _, parse := range parses {
		parseByResult[parse.ResultID] = parse
	}
	views := make([]cleardev.AgentStepAttemptView, 0, len(attempts))
	indexByID := make(map[string]int, len(attempts))
	for _, row := range attempts {
		requestedAt := row.RequestedAt
		views = append(views, cleardev.AgentStepAttemptView{
			ID:            row.ID,
			LogicalStepID: row.LogicalStepID, StepCategory: cleardev.AgentStepCategory(row.StepCategory),
			StepKind: cleardev.AgentStepKind(row.StepKind), AttemptNumber: row.AttemptNumber,
			RoleBindingID: row.RoleBindingID, AOSessionID: row.AoSessionID,
			ClientMessageID: row.ClientMessageID, PromptSHA256: row.PromptSha256,
			TriggerFailureEventID: row.TriggerFailureEventID.String,
			SendStatus:            cleardev.AgentAttemptPending, ParseConclusion: cleardev.AgentResultParseUnparsed,
			CreatedAt: nullTimePtr(row.CreatedAt), RequestedAtSemantics: cleardev.AttemptTimeSemantics(row.RequestedAtSemantics), RequestedAt: &requestedAt, Results: []cleardev.AgentStepResultView{},
		})
		indexByID[row.ID] = len(views) - 1
	}
	for _, row := range events {
		index, ok := indexByID[row.AttemptID]
		if !ok {
			return nil, fmt.Errorf("ClearDev Agent event %s has no attempt", row.ID)
		}
		view := &views[index]
		view.LastEventID = row.ID
		view.SendStatus = cleardev.AgentAttemptSendStatus(row.Status)
		view.LastClientMessageID = row.ClientMessageID
		view.TurnID = row.TurnID
		view.TurnState = domain.TurnState(row.TurnState)
		view.FailureCategory = domain.AgentFailureCategory(row.FailureCategory)
		view.Retryable = row.Retryable != 0
		view.RetryAt = nullTimePtr(row.RetryAt)
		view.ProviderErrorCode = row.ProviderErrorCode
		view.ErrorSummary = row.ErrorSummary
		recordedAt := row.RecordedAt
		view.LastObservedAt = &recordedAt
	}
	for _, row := range results {
		index, ok := indexByID[row.AttemptID]
		if !ok {
			return nil, fmt.Errorf("ClearDev Agent result %s has no attempt", row.ID)
		}
		resultView := cleardev.AgentStepResultView{
			ResultIndex: row.ResultIndex, Source: cleardev.AgentResultSource(row.Source),
			ClientMessageID: row.ClientMessageID, TurnID: row.TurnID,
			FinalMessageID: row.FinalMessageID, RawMessageText: row.RawMessageText,
			RawMessageSHA256: row.RawMessageSha256,
			ParseConclusion:  cleardev.AgentResultParseUnparsed,
		}
		observedAt := row.ObservedAt
		resultView.ObservedAt = &observedAt
		if parse, ok := parseByResult[row.ID]; ok {
			resultView.ParseConclusion = cleardev.AgentResultParseConclusion(parse.Conclusion)
			resultView.ParseErrorSummary = parse.ErrorSummary
		}
		view := &views[index]
		view.Results = append(view.Results, resultView)
		view.RawMessageSHA256 = row.RawMessageSha256
		view.ParseConclusion = resultView.ParseConclusion
	}
	for index := range views {
		sort.Slice(views[index].Results, func(i, j int) bool {
			return views[index].Results[i].ResultIndex < views[index].Results[j].ResultIndex
		})
	}
	return views, nil
}

func agentStepAttemptToDomain(row gen.CleardevAgentStepAttempt) cleardev.AgentStepAttempt {
	return cleardev.AgentStepAttempt{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID,
		LogicalStepID: row.LogicalStepID, StepCategory: cleardev.AgentStepCategory(row.StepCategory),
		StepKind: cleardev.AgentStepKind(row.StepKind), AttemptNumber: row.AttemptNumber,
		RoleBindingID: row.RoleBindingID, AOSessionID: row.AoSessionID,
		ClientMessageID: row.ClientMessageID, PromptSHA256: row.PromptSha256,
		TriggerFailureEventID: row.TriggerFailureEventID.String,
		RequestedAt:           row.RequestedAt, CreatedAt: nullTimePtr(row.CreatedAt), RequestedAtSemantics: cleardev.AttemptTimeSemantics(row.RequestedAtSemantics),
	}
}

func agentAttemptEventToDomain(row gen.CleardevAgentAttemptEvent) cleardev.AgentAttemptEvent {
	return cleardev.AgentAttemptEvent{
		ID: row.ID, AttemptID: row.AttemptID, Status: cleardev.AgentAttemptSendStatus(row.Status),
		ClientMessageID: row.ClientMessageID, PromptSHA256: row.PromptSha256,
		TurnID: row.TurnID, TurnState: domain.TurnState(row.TurnState),
		FailureCategory: domain.AgentFailureCategory(row.FailureCategory), Retryable: row.Retryable != 0,
		RetryAt: nullTimePtr(row.RetryAt), ProviderErrorCode: row.ProviderErrorCode,
		ErrorSummary: row.ErrorSummary, RecordedAt: row.RecordedAt,
	}
}

func agentStepResultToDomain(row gen.CleardevAgentStepResult) cleardev.AgentStepResult {
	return cleardev.AgentStepResult{
		ID: row.ID, AttemptID: row.AttemptID, ResultIndex: row.ResultIndex,
		Source: cleardev.AgentResultSource(row.Source), ClientMessageID: row.ClientMessageID,
		TurnID: row.TurnID, FinalMessageID: row.FinalMessageID,
		RawMessageText: row.RawMessageText, RawMessageSHA256: row.RawMessageSha256,
		ObservedAt: row.ObservedAt,
	}
}

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
