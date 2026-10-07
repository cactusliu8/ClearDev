package cleardev

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

type memoryAgentAttempts struct {
	messages      map[string]core.AgentMessageReservation
	confirmations map[string]string
	mu            sync.Mutex
	attempts      map[string]core.AgentStepAttempt
	events        map[string]core.AgentAttemptEvent
	eventIDs      []string
	results       map[string]core.AgentStepResult
	parses        map[string]core.AgentStepResultParse
}

func newMemoryAgentAttempts() *memoryAgentAttempts {
	return &memoryAgentAttempts{
		messages: map[string]core.AgentMessageReservation{}, confirmations: map[string]string{},
		attempts: map[string]core.AgentStepAttempt{}, events: map[string]core.AgentAttemptEvent{},
		results: map[string]core.AgentStepResult{}, parses: map[string]core.AgentStepResultParse{},
	}
}

func (m *memoryAgentAttempts) EnsureClearDevAgentStepAttempt(_ context.Context, attempt core.AgentStepAttempt) (core.AgentStepAttempt, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := attempt.LogicalStepID + ":" + fmt.Sprint(attempt.AttemptNumber)
	if existing, ok := m.attempts[key]; ok {
		if !core.SameAgentStepAttempt(existing, attempt) {
			return core.AgentStepAttempt{}, false, errors.New("saved ClearDev Agent attempt does not match immutable logical step")
		}
		return existing, false, nil
	}
	if !core.ValidAgentAttemptCreation(attempt) {
		return core.AgentStepAttempt{}, false, errors.New("invalid attempt creation time")
	}
	if attempt.AttemptNumber == agentSecondAttemptNumber {
		first, ok := m.attempts[attempt.LogicalStepID+":"+fmt.Sprint(agentFirstAttemptNumber)]
		trigger, triggerOK := m.events[attempt.TriggerFailureEventID]
		var latest core.AgentAttemptEvent
		for _, id := range m.eventIDs {
			if event := m.events[id]; event.AttemptID == first.ID {
				latest = event
			}
		}
		if !ok || !triggerOK || !core.ValidSecondAgentAttempt(first, trigger, latest, attempt) {
			return core.AgentStepAttempt{}, false, errors.New("second ClearDev Agent attempt is not bound to the latest retryable failure")
		}
	} else if attempt.AttemptNumber != agentFirstAttemptNumber || attempt.TriggerFailureEventID != "" {
		return core.AgentStepAttempt{}, false, errors.New("invalid ClearDev Agent attempt number or trigger")
	}
	m.attempts[key] = attempt
	return attempt, true, nil
}

func (m *memoryAgentAttempts) EnsureClearDevAgentAttemptEvent(_ context.Context, event core.AgentAttemptEvent) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.events[event.ID]; ok {
		if !core.SameAgentAttemptEvent(existing, event) {
			return false, errors.New("saved ClearDev Agent event does not match immutable evidence")
		}
		return false, nil
	}
	m.events[event.ID] = event
	m.eventIDs = append(m.eventIDs, event.ID)
	return true, nil
}

func (m *memoryAgentAttempts) RecordClearDevAgentAttemptEvent(ctx context.Context, event core.AgentAttemptEvent) error {
	_, err := m.EnsureClearDevAgentAttemptEvent(ctx, event)
	return err
}

func (m *memoryAgentAttempts) RecordClearDevAgentStepResult(_ context.Context, result core.AgentStepResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := result.AttemptID + ":" + fmt.Sprint(result.ResultIndex)
	if existing, ok := m.results[key]; ok {
		if !core.SameAgentStepResult(existing, result) {
			return errors.New("saved ClearDev Agent result does not match immutable evidence")
		}
		return nil
	}
	m.results[key] = result
	return nil
}

func (m *memoryAgentAttempts) RecordClearDevAgentStepResultParse(_ context.Context, parse core.AgentStepResultParse) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.parses[parse.ResultID]; ok {
		if !core.SameAgentStepResultParse(existing, parse) {
			return errors.New("saved ClearDev Agent parse does not match immutable evidence")
		}
		return nil
	}
	m.parses[parse.ResultID] = parse
	return nil
}

func (m *memoryAgentAttempts) ListClearDevAgentStepAttempts(_ context.Context, requirementID string) ([]core.AgentStepAttemptView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	attempts := make([]core.AgentStepAttempt, 0, len(m.attempts))
	for _, attempt := range m.attempts {
		if attempt.DevelopmentRequirementID == requirementID {
			attempts = append(attempts, attempt)
		}
	}
	events := make([]core.AgentAttemptEvent, 0, len(m.eventIDs))
	for _, id := range m.eventIDs {
		events = append(events, m.events[id])
	}
	results := make([]core.AgentStepResult, 0, len(m.results))
	for _, result := range m.results {
		results = append(results, result)
	}
	parses := make(map[string]core.AgentStepResultParse, len(m.parses))
	for key, parse := range m.parses {
		parses[key] = parse
	}
	return assembleAgentAttemptViews(attempts, events, results, parses), nil
}

func assembleAgentAttemptViews(attempts []core.AgentStepAttempt, events []core.AgentAttemptEvent, results []core.AgentStepResult, parses map[string]core.AgentStepResultParse) []core.AgentStepAttemptView {
	sort.Slice(attempts, func(i, j int) bool {
		if attempts[i].RequestedAt.Equal(attempts[j].RequestedAt) {
			return attempts[i].ID < attempts[j].ID
		}
		return attempts[i].RequestedAt.Before(attempts[j].RequestedAt)
	})
	sort.Slice(results, func(i, j int) bool {
		if results[i].ResultIndex == results[j].ResultIndex {
			return results[i].ID < results[j].ID
		}
		return results[i].ResultIndex < results[j].ResultIndex
	})
	views := make([]core.AgentStepAttemptView, 0, len(attempts))
	byID := make(map[string]int, len(attempts))
	for _, attempt := range attempts {
		requestedAt := attempt.RequestedAt
		views = append(views, core.AgentStepAttemptView{
			ID:            attempt.ID,
			LogicalStepID: attempt.LogicalStepID, StepCategory: attempt.StepCategory,
			StepKind: attempt.StepKind, AttemptNumber: attempt.AttemptNumber,
			RoleBindingID: attempt.RoleBindingID, AOSessionID: attempt.AOSessionID,
			ClientMessageID: attempt.ClientMessageID, PromptSHA256: attempt.PromptSHA256,
			TriggerFailureEventID: attempt.TriggerFailureEventID,
			SendStatus:            core.AgentAttemptPending, ParseConclusion: core.AgentResultParseUnparsed,
			CreatedAt: attempt.CreatedAt, RequestedAtSemantics: attempt.RequestedAtSemantics, RequestedAt: &requestedAt, Results: []core.AgentStepResultView{},
		})
		byID[attempt.ID] = len(views) - 1
	}
	for _, event := range events {
		index, ok := byID[event.AttemptID]
		if !ok {
			continue
		}
		observedAt := event.RecordedAt
		view := &views[index]
		view.LastEventID = event.ID
		view.SendStatus, view.LastClientMessageID = event.Status, event.ClientMessageID
		view.TurnID, view.TurnState = event.TurnID, event.TurnState
		view.FailureCategory, view.Retryable = event.FailureCategory, event.Retryable
		view.RetryAt, view.ProviderErrorCode = event.RetryAt, event.ProviderErrorCode
		view.ErrorSummary, view.LastObservedAt = event.ErrorSummary, &observedAt
	}
	for _, result := range results {
		index, ok := byID[result.AttemptID]
		if !ok {
			continue
		}
		observedAt := result.ObservedAt
		item := core.AgentStepResultView{
			ResultIndex: result.ResultIndex, Source: result.Source,
			ClientMessageID: result.ClientMessageID, TurnID: result.TurnID,
			FinalMessageID: result.FinalMessageID, RawMessageText: result.RawMessageText,
			RawMessageSHA256: result.RawMessageSHA256, ParseConclusion: core.AgentResultParseUnparsed,
			ObservedAt: &observedAt,
		}
		if parse, ok := parses[result.ID]; ok {
			item.ParseConclusion, item.ParseErrorSummary = parse.Conclusion, parse.ErrorSummary
		}
		view := &views[index]
		view.Results = append(view.Results, item)
		view.RawMessageSHA256, view.ParseConclusion = item.RawMessageSHA256, item.ParseConclusion
	}
	return views
}

func (m *memoryAgentAttempts) attemptStates(requirementID, stepID, attemptID, messageID string, latest bool) []core.AgentStepAttemptView {
	m.mu.Lock()
	defer m.mu.Unlock()
	var attempts []core.AgentStepAttempt
	for _, attempt := range m.attempts {
		if attempt.DevelopmentRequirementID != requirementID || (stepID != "" && attempt.LogicalStepID != stepID) || (attemptID != "" && attempt.ID != attemptID) {
			continue
		}
		if latest {
			newer := false
			for _, other := range m.attempts {
				if other.LogicalStepID == attempt.LogicalStepID && other.AttemptNumber > attempt.AttemptNumber {
					newer = true
				}
			}
			if newer {
				continue
			}
		}
		attempts = append(attempts, attempt)
	}
	var events []core.AgentAttemptEvent
	for _, id := range m.eventIDs {
		event := m.events[id]
		if messageID == "" || event.ClientMessageID == messageID {
			events = append(events, event)
		}
	}
	return assembleAgentAttemptViews(attempts, events, nil, nil)
}
func (m *memoryAgentAttempts) ListClearDevAgentStepAttemptStates(_ context.Context, requirementID, stepID string) ([]core.AgentStepAttemptView, error) {
	views := m.attemptStates(requirementID, stepID, "", "", false)
	sort.Slice(views, func(i, j int) bool { return views[i].AttemptNumber < views[j].AttemptNumber })
	return views, nil
}
func (m *memoryAgentAttempts) ListLatestClearDevAgentAttemptStates(_ context.Context, requirementID string) ([]core.AgentStepAttemptView, error) {
	return m.attemptStates(requirementID, "", "", "", true), nil
}
func (m *memoryAgentAttempts) GetClearDevAgentAttemptState(_ context.Context, requirementID, attemptID string) (core.AgentStepAttemptView, error) {
	views := m.attemptStates(requirementID, "", attemptID, "", false)
	if len(views) != 1 {
		return core.AgentStepAttemptView{}, errors.New("missing attempt")
	}
	return views[0], nil
}
func (m *memoryAgentAttempts) GetClearDevAgentDeliveryState(_ context.Context, requirementID, attemptID, messageID string) (core.AgentStepAttemptView, error) {
	views := m.attemptStates(requirementID, "", attemptID, messageID, false)
	if len(views) != 1 {
		return core.AgentStepAttemptView{}, errors.New("missing attempt")
	}
	return views[0], nil
}

func (m *memoryAgentAttempts) ReserveClearDevAgentMessage(_ context.Context, c core.ReserveAgentMessageCommand) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, e := c.Attempt, c.Boundary
	if !core.ValidAgentMessageIdentity(a, c.Source, e.ClientMessageID, e.PromptSHA256) {
		return false, errors.New("invalid message binding")
	}
	r := core.AgentMessageReservation{ClientMessageID: e.ClientMessageID, DevelopmentRequirementID: a.DevelopmentRequirementID, BudgetVersion: core.MessageBudgetV1, LogicalStepID: a.LogicalStepID, AttemptID: a.ID, Source: c.Source, AOSessionID: a.AOSessionID, PromptSHA256: e.PromptSHA256, ReservedAt: e.RecordedAt}
	if old, ok := m.messages[r.ClientMessageID]; ok {
		if !core.SameAgentMessageReservation(old, r) {
			return false, errors.New("reservation conflict")
		}
		return false, nil
	}
	for _, old := range m.messages {
		if old.LogicalStepID == r.LogicalStepID && old.Source == r.Source {
			return false, &core.RuleError{Code: core.ReasonMessageBudgetExhausted, Message: "message source already reserved"}
		}
	}
	if old, ok := m.events[e.ID]; ok {
		if !core.SameAgentAttemptEvent(old, e) {
			return false, errors.New("event conflict")
		}
		return false, nil
	}
	m.messages[r.ClientMessageID] = r
	m.events[e.ID] = e
	m.eventIDs = append(m.eventIDs, e.ID)
	return true, nil
}
func (m *memoryAgentAttempts) ConfirmClearDevAgentMessage(_ context.Context, e core.AgentAttemptEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.TurnID == "" {
		return errors.New("missing turn")
	}
	if old, ok := m.confirmations[e.ClientMessageID]; ok && old != e.TurnID {
		return errors.New("confirmation conflict")
	}
	if old, ok := m.events[e.ID]; ok {
		if !core.SameAgentAttemptEvent(old, e) {
			return errors.New("event conflict")
		}
		return nil
	}
	m.confirmations[e.ClientMessageID] = e.TurnID
	m.events[e.ID] = e
	m.eventIDs = append(m.eventIDs, e.ID)
	return nil
}
func (m *memoryAgentAttempts) GetClearDevMessageBudget(_ context.Context, requirementID string) (core.MessageBudgetView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	view := core.MessageBudgetView{BudgetVersion: core.MessageBudgetV1, Steps: []core.MessageBudgetUsage{}, Roles: []core.MessageBudgetUsage{}}
	seen := map[string]bool{}
	for _, a := range m.attempts {
		if a.DevelopmentRequirementID != requirementID || seen[a.LogicalStepID] {
			continue
		}
		seen[a.LogicalStepID] = true
		maximum, one, zero, reserved, sent := int64(3), int64(1), int64(0), int64(0), int64(0)
		for _, r := range m.messages {
			if r.LogicalStepID == a.LogicalStepID {
				reserved++
				if m.confirmations[r.ClientMessageID] != "" {
					sent++
				}
			}
		}
		remaining := maximum - reserved
		view.Steps = append(view.Steps, core.MessageBudgetUsage{Scope: "LOGICAL_STEP", LogicalStepID: a.LogicalStepID, BudgetVersion: core.MessageBudgetV1, MaxSteps: &one, ReservedSteps: &one, RemainingSteps: &zero, MaxMessages: &maximum, ReservedMessages: &reserved, ConfirmedSentMessages: &sent, RemainingMessages: &remaining})
	}
	return view, nil
}
