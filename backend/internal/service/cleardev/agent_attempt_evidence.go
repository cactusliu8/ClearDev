package cleardev

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	agentFirstAttemptNumber  int64 = 1
	agentSecondAttemptNumber int64 = 2
)

func agentAttemptID(stepID string, attemptNumber int64) string {
	return fmt.Sprintf("%s:attempt:%d", stepID, attemptNumber)
}

func agentAttemptClientMessageID(stepClientMessageID string, attemptNumber int64, correction bool) string {
	id := strings.TrimSpace(stepClientMessageID)
	if attemptNumber == 2 || attemptNumber == 3 {
		id += fmt.Sprintf(":attempt:%d", attemptNumber)
	}
	if correction {
		id += ":parse-correction"
	}
	return id
}

func agentEvidenceID(attemptID string, parts ...string) string {
	return attemptID + ":evidence:" + coreDigest([]byte(strings.Join(parts, "\x00")))
}

func (s *Service) ensureAgentAttempt(ctx context.Context, requirementID string, category core.AgentStepCategory, step core.AgentStep, sessionID string, attemptNumber int64, triggerEventID string) (core.AgentStepAttempt, error) {
	requestedAt := s.now().UTC()
	attempt := core.AgentStepAttempt{
		ID: agentAttemptID(step.ID, attemptNumber), DevelopmentRequirementID: requirementID,
		LogicalStepID: step.ID, StepCategory: category, StepKind: step.Kind,
		AttemptNumber: attemptNumber, RoleBindingID: step.RoleBindingID,
		AOSessionID: sessionID, ClientMessageID: agentAttemptClientMessageID(step.ClientMessageID, attemptNumber, false),
		PromptSHA256: step.PromptSHA256, TriggerFailureEventID: triggerEventID, RequestedAt: requestedAt, CreatedAt: &requestedAt, RequestedAtSemantics: core.AttemptTimeActualCreation,
	}
	stored, _, err := s.attempts.EnsureClearDevAgentStepAttempt(ctx, attempt)
	return stored, err
}

func agentAttemptFromView(requirementID string, view core.AgentStepAttemptView) core.AgentStepAttempt {
	requestedAt := time.Time{}
	if view.RequestedAt != nil {
		requestedAt = *view.RequestedAt
	}
	return core.AgentStepAttempt{
		ID: view.ID, DevelopmentRequirementID: requirementID, LogicalStepID: view.LogicalStepID,
		StepCategory: view.StepCategory, StepKind: view.StepKind, AttemptNumber: view.AttemptNumber,
		RoleBindingID: view.RoleBindingID, AOSessionID: view.AOSessionID,
		ClientMessageID: view.ClientMessageID, PromptSHA256: view.PromptSHA256,
		TriggerFailureEventID: view.TriggerFailureEventID, RequestedAt: requestedAt, CreatedAt: view.CreatedAt, RequestedAtSemantics: view.RequestedAtSemantics,
	}
}

func logicalStepAttemptViews(views []core.AgentStepAttemptView, stepID string) []core.AgentStepAttemptView {
	items := make([]core.AgentStepAttemptView, 0, 2)
	for _, view := range views {
		if view.LogicalStepID == stepID {
			items = append(items, view)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AttemptNumber < items[j].AttemptNumber })
	return items
}

func (s *Service) agentAttemptView(ctx context.Context, requirementID, attemptID string) (core.AgentStepAttemptView, error) {
	return s.attempts.GetClearDevAgentAttemptState(ctx, requirementID, attemptID)
}

// ensureOriginalAgentAttemptDelivery sends the original logical request when
// the active recovery attempt has no delivery evidence. A saved unknown
// boundary is only reconciled; relayAgentTurn never sends through that state.
func (s *Service) ensureOriginalAgentAttemptDelivery(
	ctx context.Context,
	requirementID string,
	category core.AgentStepCategory,
	step core.AgentStep,
	attempt core.AgentStepAttempt,
	sessionID, prompt string,
) error {
	view, err := s.agentAttemptView(ctx, requirementID, attempt.ID)
	if err != nil {
		return err
	}
	if view.SendStatus != core.AgentAttemptPending && view.SendStatus != core.AgentAttemptDeliveryUnknown {
		return nil
	}
	return s.relayAgentTurn(ctx, requirementID, category, step, sessionID, prompt,
		step.ClientMessageID, core.AgentAttemptSent, s.now().UTC())
}

func canCreateSecondAgentAttempt(view core.AgentStepAttemptView, now time.Time) bool {
	if view.AttemptNumber != agentFirstAttemptNumber || !view.Retryable || view.LastEventID == "" {
		return false
	}
	terminal := view.SendStatus == core.AgentAttemptFailed || view.SendStatus == core.AgentAttemptInterrupted
	terminalTurn := view.TurnState == domain.TurnStateFailed || view.TurnState == domain.TurnStateInterrupted
	if !core.FailedBeforeSendViewValid(view) && (!terminal || !terminalTurn) {
		return false
	}
	return view.LastObservedAt != nil && !now.Before(*view.LastObservedAt) && (view.RetryAt == nil || !now.Before(*view.RetryAt))
}

func retryWindowPending(view core.AgentStepAttemptView, now time.Time) bool {
	return view.AttemptNumber == agentFirstAttemptNumber && view.Retryable &&
		(view.SendStatus == core.AgentAttemptFailed || view.SendStatus == core.AgentAttemptInterrupted) &&
		view.RetryAt != nil && now.Before(*view.RetryAt)
}

var errAgentRecoveryDeferred = errors.New("controlled Agent recovery is waiting for its prerequisites")
var errOriginalAgentSessionUnavailable = errors.New("the original controlled Agent session cannot be recovered")

func isAgentRecoveryDeferred(err error) bool { return errors.Is(err, errAgentRecoveryDeferred) }

func (s *Service) recoverOriginalAgentSession(ctx context.Context, requirementID string, view core.AgentStepAttemptView, checkPreflight bool) error {
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(view.AOSessionID))
	if err != nil {
		return err
	}
	if !found || record.IsTerminated || !s.controlledSessionMatches(ctx, record) ||
		domain.NormalizeSessionMode(record.Mode) != domain.SessionModeChat {
		return errOriginalAgentSessionUnavailable
	}
	if record.Activity.State == domain.ActivityExited {
		if core.FixedRecoveryStepAllowed(view.StepCategory, view.StepKind) {
			return errAgentRecoveryDeferred
		}
		if s.recoverAgentSession == nil {
			return fmt.Errorf("%w: recovery is not configured", errOriginalAgentSessionUnavailable)
		}
		recoverErr := s.recoverAgentSession(ctx, domain.SessionID(view.AOSessionID))
		record, found, err = s.ao.GetSession(ctx, domain.SessionID(view.AOSessionID))
		if err != nil {
			return err
		}
		if !found || record.IsTerminated || record.Activity.State == domain.ActivityExited {
			if recoverErr != nil {
				return fmt.Errorf("%w: %w", errOriginalAgentSessionUnavailable, recoverErr)
			}
			return fmt.Errorf("%w: session remained unavailable", errOriginalAgentSessionUnavailable)
		}
	}
	if !checkPreflight {
		return nil
	}
	blocked, _, err := s.runControlledPreflight(ctx, requirementID, view.RoleBindingID, record.ProjectID, record.Kind)
	if err != nil {
		return err
	}
	if blocked {
		return errAgentRecoveryDeferred
	}
	return nil
}

func (s *Service) createSecondAgentAttempt(ctx context.Context, requirementID string, first core.AgentStepAttemptView) (core.AgentStepAttempt, error) {
	if !canCreateSecondAgentAttempt(first, s.now().UTC()) {
		return core.AgentStepAttempt{}, errAgentRecoveryDeferred
	}
	if err := s.recoverOriginalAgentSession(ctx, requirementID, first, true); err != nil {
		return core.AgentStepAttempt{}, err
	}
	step := core.AgentStep{
		ID: first.LogicalStepID, RoleBindingID: first.RoleBindingID, Kind: first.StepKind,
		ClientMessageID: strings.TrimSuffix(first.ClientMessageID, ":attempt:2"),
		PromptSHA256:    first.PromptSHA256,
	}
	return s.ensureAgentAttempt(ctx, requirementID, first.StepCategory, step, first.AOSessionID,
		agentSecondAttemptNumber, first.LastEventID)
}

func (s *Service) activeAgentAttempt(ctx context.Context, requirementID string, category core.AgentStepCategory, step core.AgentStep, sessionID string) (core.AgentStepAttempt, error) {
	if category == core.AgentStepCategoryComplexExecution && step.Kind == core.ComplexExecutionAgentStepBuilderTask {
		if store, ok := s.complexExecution.(builderReplacementStore); ok {
			h, found, err := store.GetClearDevBuilderReplacementEffectiveBinding(ctx, step.ID)
			if err != nil {
				return core.AgentStepAttempt{}, err
			}
			if found {
				effective, err := s.replacementEffectiveStep(ctx, step)
				if err != nil || effective.RoleBindingID != h.Binding.NewRoleBindingID || sessionID != h.NewAOSessionID {
					return core.AgentStepAttempt{}, errors.New("builder replacement attempt changed its identity")
				}
				view, err := s.attempts.GetClearDevAgentAttemptState(ctx, requirementID, h.AttemptID)
				if err != nil {
					return core.AgentStepAttempt{}, err
				}
				if view.AttemptNumber != 2 || view.RoleBindingID != h.Binding.NewRoleBindingID || view.AOSessionID != h.NewAOSessionID || view.PromptSHA256 != h.Binding.PromptSHA256 {
					return core.AgentStepAttempt{}, errors.New("builder replacement attempt is not its exact successor")
				}
				return agentAttemptFromView(requirementID, view), nil
			}
		}
	}
	if category == core.AgentStepCategoryComplexExecution {
		states, err := s.attempts.ListClearDevAgentStepAttemptStates(ctx, requirementID, step.ID)
		if err != nil {
			return core.AgentStepAttempt{}, err
		}
		items := logicalStepAttemptViews(states, step.ID)
		if len(items) == 2 && items[0].RoleBindingID == step.RoleBindingID && items[0].PromptSHA256 == step.PromptSHA256 && (sessionID == items[0].AOSessionID || sessionID == items[1].AOSessionID) {
			sessionID = items[0].AOSessionID
		}
	}
	if _, err := s.ensureAgentAttempt(ctx, requirementID, category, step, sessionID,
		agentFirstAttemptNumber, ""); err != nil {
		return core.AgentStepAttempt{}, err
	}
	views, err := s.attempts.ListClearDevAgentStepAttemptStates(ctx, requirementID, step.ID)
	if err != nil {
		return core.AgentStepAttempt{}, err
	}
	items := logicalStepAttemptViews(views, step.ID)
	if len(items) == 0 {
		return core.AgentStepAttempt{}, errors.New("controlled Agent attempt evidence is missing")
	}
	latest := items[len(items)-1]
	if latest.AttemptNumber == agentSecondAttemptNumber {
		return agentAttemptFromView(requirementID, latest), nil
	}
	if retryWindowPending(latest, s.now().UTC()) {
		return core.AgentStepAttempt{}, errAgentRecoveryDeferred
	}
	if canCreateSecondAgentAttempt(latest, s.now().UTC()) {
		return s.createSecondAgentAttempt(ctx, requirementID, latest)
	}
	return agentAttemptFromView(requirementID, latest), nil
}

func (s *Service) relayAgentTurn(ctx context.Context, requirementID string, category core.AgentStepCategory, step core.AgentStep, sessionID, prompt, clientMessageID string, sentStatus core.AgentAttemptSendStatus, recordedAt time.Time) error {
	attempt, err := s.activeAgentAttempt(ctx, requirementID, category, step, sessionID)
	if err != nil {
		return err
	}
	sessionID = attempt.AOSessionID
	correction := clientMessageID != step.ClientMessageID
	clientMessageID = agentAttemptClientMessageID(step.ClientMessageID, attempt.AttemptNumber, correction)
	view, err := s.attempts.GetClearDevAgentDeliveryState(ctx, requirementID, attempt.ID, clientMessageID)
	if err != nil {
		return err
	}
	if category == core.AgentStepCategoryComplexExecution && step.Kind == core.ComplexExecutionAgentStepBuilderTask && attempt.AttemptNumber == 2 && view.SendStatus == core.AgentAttemptPending && view.LastEventID == "" {
		if err := s.checkBuilderReplacementBeforeSend(ctx, requirementID, step.ID); err != nil {
			return err
		}
	}
	if category == core.AgentStepCategoryComplexPlanning && step.Kind == core.ComplexAgentStepEngineeringPlan && view.SendStatus == core.AgentAttemptPending && view.LastEventID == "" {
		if err := s.checkExtraCoordinationBeforeSend(ctx, requirementID, step.ID); err != nil {
			return err
		}
		if err := s.checkPlannerAnswersBeforeSend(ctx, requirementID, step.ID); err != nil {
			return err
		}
	}
	if category == core.AgentStepCategoryComplexPlanning && step.Kind == core.ComplexAgentStepCompilation &&
		attempt.AttemptNumber == agentSecondAttemptNumber && view.SendStatus == core.AgentAttemptPending && view.LastEventID == "" {
		if err := s.checkPlanningRecoveryBeforeSend(ctx, requirementID, step.ID); err != nil {
			return err
		}
	}
	if category == core.AgentStepCategoryComplexPlanning && step.Kind == core.ComplexAgentStepCompilation && attempt.AttemptNumber == 3 && view.SendStatus == core.AgentAttemptPending && view.LastEventID == "" {
		if err := s.checkExtraPlanningAttemptBeforeSend(ctx, requirementID, step.ID); err != nil {
			return err
		}
	}
	if !correction && category == core.AgentStepCategoryComplexPlanning && step.Kind == core.ComplexAgentStepEngineeringPlan && attempt.AttemptNumber == 2 && view.SendStatus == core.AgentAttemptPending && view.LastEventID == "" {
		if err := s.checkPlannerCoordinationRecoveryBeforeSend(ctx, requirementID, step.ID); err != nil {
			return err
		}
	}
	// A native grant can persist attempt2 before a process restart. Recheck
	// recovery prerequisites before its first delivery reservation, rather than
	// assuming that creation of an existing attempt already ran preflight.
	if !correction && category == core.AgentStepCategoryComplexPlanning && step.Kind == core.ComplexAgentStepPlanReview &&
		attempt.AttemptNumber == agentSecondAttemptNumber && view.SendStatus == core.AgentAttemptPending && view.LastEventID == "" {
		if err := s.recoverOriginalAgentSession(ctx, requirementID, view, true); err != nil {
			return err
		}
	}
	// The evidence recheck reuses an existing reviewer, so no spawn preflight ran.
	// Check only its original first delivery, never a parse-correction message.
	if !correction && category == core.AgentStepCategoryComplexExecution && step.Kind == core.AgentStepRequirementFinalReview && strings.HasSuffix(step.RequestID, ":evidence-recheck") && view.SendStatus == core.AgentAttemptPending && view.LastEventID == "" {
		if err := s.recoverOriginalAgentSession(ctx, requirementID, view, true); err != nil {
			return err
		}
	}
	boundaryFailure := domain.ConversationFailure{
		Category: domain.AgentFailureDeliveryUnknown, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureDeliveryUnknown),
	}
	boundary := core.AgentAttemptEvent{
		ID: agentEvidenceID(attempt.ID, "delivery-boundary", clientMessageID), AttemptID: attempt.ID,
		Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: clientMessageID,
		PromptSHA256: coreDigest([]byte(prompt)), FailureCategory: boundaryFailure.Category,
		ErrorSummary: boundaryFailure.ErrorSummary, RecordedAt: recordedAt,
	}
	source := core.AgentMessageOriginal
	if correction {
		source = core.AgentMessageParseCorrection
	} else if attempt.AttemptNumber == 3 {
		source = core.AgentMessageHumanAuthorizedOriginal
	} else if attempt.AttemptNumber == 2 {
		source = core.AgentMessageRecoveryOriginal
	}
	found, savedErr := savedAgentDelivery([]core.AgentStepAttemptView{view}, attempt.ID, clientMessageID)
	claimed, err := s.attempts.ReserveClearDevAgentMessage(ctx, core.ReserveAgentMessageCommand{Attempt: attempt, Source: source, Boundary: boundary})
	if err != nil && (!found || !messageBudgetReason(err, core.ReasonMessageBudgetUnknown)) {
		if messageBudgetReason(err, core.ReasonMessageBudgetUnknown) && (step.SendStatus == core.AgentStepSendStatusSent || correction) {
			if reconciled := s.reconcileUnknownAgentDelivery(ctx, attempt, prompt, clientMessageID, sentStatus, recordedAt); reconciled == nil {
				return nil
			}
		}
		return err
	}
	if found {
		if failure, ok := ports.ChatFailureFromError(savedErr); ok && failure.Category == domain.AgentFailureDeliveryUnknown {
			return s.reconcileUnknownAgentDelivery(ctx, attempt, prompt, clientMessageID, sentStatus, recordedAt)
		}
		return savedErr
	}
	if !claimed {
		view, err = s.attempts.GetClearDevAgentDeliveryState(ctx, requirementID, attempt.ID, clientMessageID)
		if err != nil {
			return err
		}
		if found, savedErr := savedAgentDelivery([]core.AgentStepAttemptView{view}, attempt.ID, clientMessageID); found {
			return savedErr
		}
		return ports.WithChatFailure(errAgentDeliveryUnknown, boundaryFailure)
	}
	turnID, sendErr := s.chat.RelayChatTurnWithID(ctx, domain.SessionID(sessionID), prompt, clientMessageID)
	if sendErr == nil && strings.TrimSpace(turnID) == "" {
		sendErr = errAgentDeliveryUnknown
	}
	if sendErr != nil {
		if !correction && attempt.AttemptNumber <= agentSecondAttemptNumber && turnID == "" && provenLocalPreSendFailure(sendErr) {
			if recordErr := s.recordAgentBeforeSendFailure(ctx, attempt); recordErr != nil {
				return ports.WithChatFailure(recordErr, boundaryFailure)
			}
			return sendErr
		}
		failure := stableSendFailure(sendErr)
		if recordErr := s.recordAgentFailure(ctx, attempt, clientMessageID, "", failure, recordedAt); recordErr != nil {
			return ports.WithChatFailure(recordErr, boundaryFailure)
		}
		return ports.WithChatFailure(sendErr, failure)
	}
	event := core.AgentAttemptEvent{
		ID: agentEvidenceID(attempt.ID, string(sentStatus), clientMessageID, turnID), AttemptID: attempt.ID,
		Status: sentStatus, ClientMessageID: clientMessageID, PromptSHA256: coreDigest([]byte(prompt)),
		TurnID: turnID, TurnState: domain.TurnStateRunning, RecordedAt: recordedAt,
	}
	if err := s.attempts.ConfirmClearDevAgentMessage(ctx, event); err != nil {
		return ports.WithChatFailure(err, boundaryFailure)
	}
	return nil
}

func savedAgentDelivery(views []core.AgentStepAttemptView, attemptID, clientMessageID string) (bool, error) {
	for _, view := range views {
		if view.ID != attemptID || view.LastClientMessageID != clientMessageID {
			continue
		}
		switch view.SendStatus {
		case core.AgentAttemptSent, core.AgentAttemptCorrectionSent, core.AgentAttemptCompleted:
			return true, nil
		case core.AgentAttemptFailedBeforeSend:
			return true, ports.ErrChatSendNotStarted
		case core.AgentAttemptDeliveryUnknown:
			failure := domain.ConversationFailure{
				Category: domain.AgentFailureDeliveryUnknown, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureDeliveryUnknown),
			}
			return true, ports.WithChatFailure(errAgentDeliveryUnknown, failure)
		case core.AgentAttemptFailed, core.AgentAttemptInterrupted, core.AgentAttemptObservationTimedOut:
			failure := domain.ConversationFailure{
				Category: view.FailureCategory, ProviderErrorCode: view.ProviderErrorCode,
				ErrorSummary: view.ErrorSummary, Retryable: view.Retryable, RetryAt: view.RetryAt,
			}
			return true, ports.WithChatFailure(errors.New("controlled Agent attempt already has terminal evidence"), failure)
		}
	}
	return false, nil
}

func (s *Service) reconcileUnknownAgentDelivery(ctx context.Context, attempt core.AgentStepAttempt, prompt, clientMessageID string, sentStatus core.AgentAttemptSendStatus, recordedAt time.Time) error {
	snapshot, err := s.chat.Snapshot(ctx, domain.SessionID(attempt.AOSessionID))
	if err != nil || string(snapshot.SessionID) != attempt.AOSessionID {
		return ports.WithChatFailure(errAgentDeliveryUnknown, domain.ConversationFailure{
			Category: domain.AgentFailureDeliveryUnknown, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureDeliveryUnknown),
		})
	}
	var turnID string
	var turnState domain.TurnState
	for _, message := range snapshot.Messages {
		if message.ClientMessageID != clientMessageID {
			continue
		}
		if turnID != "" || message.Role != domain.MessageRoleUser || message.Origin != domain.MessageOriginAutomation ||
			message.Text != prompt || message.TurnID == "" {
			return ports.WithChatFailure(errAgentDeliveryUnknown, domain.ConversationFailure{
				Category: domain.AgentFailureDeliveryUnknown, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureDeliveryUnknown),
			})
		}
		turnID = message.TurnID
	}
	if turnID == "" {
		return ports.WithChatFailure(errAgentDeliveryUnknown, domain.ConversationFailure{
			Category: domain.AgentFailureDeliveryUnknown, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureDeliveryUnknown),
		})
	}
	for _, turn := range snapshot.Turns {
		if turn.ID == turnID {
			if turnState != "" || (turn.HandledBySessionID != "" && string(turn.HandledBySessionID) != attempt.AOSessionID) {
				return ports.WithChatFailure(errAgentDeliveryUnknown, domain.ConversationFailure{Category: domain.AgentFailureDeliveryUnknown})
			}
			turnState = turn.State
		}
	}
	if turnState == "" {
		return ports.WithChatFailure(errAgentDeliveryUnknown, domain.ConversationFailure{Category: domain.AgentFailureDeliveryUnknown})
	}
	event := core.AgentAttemptEvent{
		ID: agentEvidenceID(attempt.ID, "delivery-reconciled", clientMessageID, turnID), AttemptID: attempt.ID,
		Status: sentStatus, ClientMessageID: clientMessageID, PromptSHA256: coreDigest([]byte(prompt)),
		TurnID: turnID, TurnState: turnState, RecordedAt: recordedAt,
	}
	return s.attempts.ConfirmClearDevAgentMessage(ctx, event)
}

func (s *Service) recordAgentResult(ctx context.Context, requirementID string, step core.AgentStep, message standardMessage, source core.AgentResultSource) (string, error) {
	index := int64(1)
	if source == core.AgentResultCorrection {
		index = 2
	}
	views, err := s.attempts.ListClearDevAgentStepAttemptStates(ctx, requirementID, step.ID)
	if err != nil {
		return "", err
	}
	items := logicalStepAttemptViews(views, step.ID)
	if len(items) == 0 {
		return "", errors.New("controlled Agent attempt evidence is missing")
	}
	active := items[len(items)-1]
	attemptID := active.ID
	resultID := fmt.Sprintf("%s:result:%d", attemptID, index)
	result := core.AgentStepResult{
		ID: resultID, AttemptID: attemptID, ResultIndex: index, Source: source,
		ClientMessageID: agentAttemptClientMessageID(step.ClientMessageID, active.AttemptNumber, false), TurnID: message.TurnID,
		FinalMessageID: message.MessageID, RawMessageText: message.Text,
		RawMessageSHA256: coreDigest([]byte(message.Text)), ObservedAt: s.now().UTC(),
	}
	if source == core.AgentResultCorrection {
		result.ClientMessageID = agentAttemptClientMessageID(step.ClientMessageID, active.AttemptNumber, true)
	}
	if err := s.attempts.RecordClearDevAgentStepResult(ctx, result); err != nil {
		return "", err
	}
	event := core.AgentAttemptEvent{
		ID:        agentEvidenceID(attemptID, string(core.AgentAttemptCompleted), result.ClientMessageID, message.TurnID),
		AttemptID: attemptID, Status: core.AgentAttemptCompleted,
		ClientMessageID: result.ClientMessageID, TurnID: message.TurnID,
		TurnState: domain.TurnStateCompleted, RecordedAt: result.ObservedAt,
	}
	if err := s.attempts.RecordClearDevAgentAttemptEvent(ctx, event); err != nil {
		return "", err
	}
	return resultID, nil
}

type savedAgentParseReader interface {
	ReadClearDevAgentStepResultParse(context.Context, string) (core.AgentStepResultParse, bool, error)
}

func (s *Service) recordAgentParse(ctx context.Context, resultID string, parseErr error) error {
	parse := core.AgentStepResultParse{
		ResultID: resultID, Conclusion: core.AgentResultParseValid, ParsedAt: s.now().UTC(),
	}
	if parseErr != nil {
		parse.Conclusion = core.AgentResultParseInvalid
		parse.ErrorSummary = boundedDiagnosticSummary(parseErr.Error())
	}
	if reader, ok := s.attempts.(savedAgentParseReader); ok {
		prior, found, err := reader.ReadClearDevAgentStepResultParse(ctx, resultID)
		if err != nil {
			return err
		}
		if found {
			// The raw result was checked by recordAgentResult before this call.
			// Preserve its original parse text, even when a newer parser explains
			// the same rejection more precisely. A changed conclusion still fails.
			if prior.ResultID != resultID || prior.Conclusion != parse.Conclusion {
				return errors.New("saved ClearDev Agent parse has a different conclusion")
			}
			return nil
		}
	}
	return s.attempts.RecordClearDevAgentStepResultParse(ctx, parse)
}

func (s *Service) recordAgentObservationFailure(ctx context.Context, requirementID string, step core.AgentStep, clientMessageID string, observationErr error) error {
	views, err := s.attempts.ListClearDevAgentStepAttemptStates(ctx, requirementID, step.ID)
	if err != nil {
		return err
	}
	items := logicalStepAttemptViews(views, step.ID)
	if len(items) == 0 {
		return errors.New("controlled Agent attempt evidence is missing")
	}
	active := items[len(items)-1]
	clientMessageID = agentAttemptClientMessageID(step.ClientMessageID, active.AttemptNumber, clientMessageID != step.ClientMessageID)
	failure := stableObservationFailure(observationErr)
	status := core.AgentAttemptFailed
	switch failure.Category {
	case domain.AgentFailureTurnInterrupted:
		status = core.AgentAttemptInterrupted
	case domain.AgentFailureObservationTimeout:
		status = core.AgentAttemptObservationTimedOut
	case domain.AgentFailureDeliveryUnknown:
		status = core.AgentAttemptDeliveryUnknown
	}
	event := core.AgentAttemptEvent{
		ID:        agentEvidenceID(active.ID, string(status), clientMessageID, string(failure.Category)),
		AttemptID: active.ID, Status: status, ClientMessageID: clientMessageID,
		FailureCategory: failure.Category, Retryable: failure.Retryable, RetryAt: failure.RetryAt,
		ProviderErrorCode: failure.ProviderErrorCode, ErrorSummary: failure.ErrorSummary,
		TurnState: domain.TurnStateFailed, RecordedAt: s.now().UTC(),
	}
	var terminal *agentTerminalError
	if errors.As(observationErr, &terminal) {
		event.TurnID = terminal.turn.ID
		event.TurnState = terminal.turn.State
	}
	return s.attempts.RecordClearDevAgentAttemptEvent(ctx, event)
}

// Only an explicit local refusal can settle the conservative reservation as
// no-send. A sentinel must not override a more specific provider/authority error.
func provenLocalPreSendFailure(err error) bool {
	if !errors.Is(err, ports.ErrChatSendNotStarted) || errors.Is(err, ports.ErrBuilderSessionFenced) {
		return false
	}
	if _, known := ports.ChatFailureFromError(err); known {
		return false
	}
	_, known := classifiedPortFailure(err)
	return !known
}

func (s *Service) recordAgentBeforeSendFailure(ctx context.Context, attempt core.AgentStepAttempt) error {
	event := core.AgentAttemptEvent{
		ID:        agentEvidenceID(attempt.ID, string(core.AgentAttemptFailedBeforeSend), attempt.ClientMessageID),
		AttemptID: attempt.ID, Status: core.AgentAttemptFailedBeforeSend,
		ClientMessageID: attempt.ClientMessageID, PromptSHA256: attempt.PromptSHA256,
		Retryable: true, ErrorSummary: "message send did not start", RecordedAt: s.now().UTC(),
	}
	if !core.FailedBeforeSendEventValid(attempt, event) {
		return errors.New("invalid failed-before-send evidence")
	}
	return s.attempts.RecordClearDevAgentAttemptEvent(ctx, event)
}

func (s *Service) recordAgentFailure(ctx context.Context, attempt core.AgentStepAttempt, clientMessageID, turnID string, failure domain.ConversationFailure, at time.Time) error {
	status := core.AgentAttemptFailed
	if failure.Category == domain.AgentFailureDeliveryUnknown {
		status = core.AgentAttemptDeliveryUnknown
	}
	event := core.AgentAttemptEvent{
		ID:        agentEvidenceID(attempt.ID, string(status), clientMessageID, turnID, string(failure.Category)),
		AttemptID: attempt.ID, Status: status, ClientMessageID: clientMessageID,
		TurnID: turnID, TurnState: domain.TurnStateFailed,
		FailureCategory: failure.Category, Retryable: failure.Retryable, RetryAt: failure.RetryAt,
		ProviderErrorCode: failure.ProviderErrorCode, ErrorSummary: failure.ErrorSummary, RecordedAt: at,
	}
	if failure.Category == domain.AgentFailureDeliveryUnknown {
		event.TurnState = ""
	}
	return s.attempts.RecordClearDevAgentAttemptEvent(ctx, event)
}

// recoverRequirementAgentAttempts runs before a durable workflow advances.
// It restores only the exact bound AO session. A retry attempt is created only
// from the first attempt's latest, explicitly retryable terminal evidence.
func (s *Service) recoverRequirementAgentAttempts(ctx context.Context, requirementID string) (bool, error) {
	views, err := s.attempts.ListLatestClearDevAgentAttemptStates(ctx, requirementID)
	if err != nil {
		return false, err
	}
	latestByStep := make(map[string]core.AgentStepAttemptView)
	for _, view := range views {
		latest, ok := latestByStep[view.LogicalStepID]
		if !ok || view.AttemptNumber > latest.AttemptNumber {
			latestByStep[view.LogicalStepID] = view
		}
	}
	for _, view := range latestByStep {
		runnable, err := s.agentAttemptStillRunnable(ctx, requirementID, view)
		if err != nil {
			return false, err
		}
		if !runnable {
			continue
		}
		if registered, err := s.registeredUnsentCoordinationRecovery(ctx, requirementID, view); err != nil {
			return false, err
		} else if registered {
			continue
		}
		if registered, err := s.registeredUnsentExtraPlanningAttempt(ctx, requirementID, view); err != nil {
			return false, err
		} else if registered {
			continue
		}
		if registered, err := s.registeredUnsentPlanningRecovery(ctx, requirementID, view); err != nil {
			return false, err
		} else if registered {
			continue
		}
		if view.AttemptNumber >= agentSecondAttemptNumber &&
			(view.SendStatus == core.AgentAttemptFailed || view.SendStatus == core.AgentAttemptInterrupted || view.SendStatus == core.AgentAttemptFailedBeforeSend) {
			return true, nil
		}
		if retryWindowPending(view, s.now().UTC()) {
			return true, nil
		}
		if core.FixedRecoveryStepAllowed(view.StepCategory, view.StepKind) {
			record, found, err := s.ao.GetSession(ctx, domain.SessionID(view.AOSessionID))
			if err != nil {
				return false, err
			}
			if !found || record.IsTerminated || record.Activity.State == domain.ActivityExited {
				continue
			}
		}
		if canCreateSecondAgentAttempt(view, s.now().UTC()) {
			if _, err := s.createSecondAgentAttempt(ctx, requirementID, view); err != nil {
				if isAgentRecoveryDeferred(err) {
					return true, nil
				}
				if !errors.Is(err, errOriginalAgentSessionUnavailable) {
					return false, err
				}
				if recordErr := s.recordAgentRecoveryStop(ctx, view); recordErr != nil {
					return false, recordErr
				}
				return true, nil
			}
			continue
		}
		switch view.SendStatus {
		case core.AgentAttemptPending, core.AgentAttemptSent, core.AgentAttemptCorrectionSent,
			core.AgentAttemptObservationTimedOut, core.AgentAttemptDeliveryUnknown:
			record, found, readErr := s.ao.GetSession(ctx, domain.SessionID(view.AOSessionID))
			if readErr != nil {
				return false, readErr
			}
			if found && !record.IsTerminated && record.Activity.State != domain.ActivityExited {
				continue
			}
			if err := s.recoverOriginalAgentSession(ctx, requirementID, view, false); err != nil {
				if !errors.Is(err, errOriginalAgentSessionUnavailable) {
					return false, err
				}
				if view.SendStatus != core.AgentAttemptDeliveryUnknown && view.SendStatus != core.AgentAttemptObservationTimedOut {
					if recordErr := s.recordAgentRecoveryStop(ctx, view); recordErr != nil {
						return false, recordErr
					}
				}
				return true, nil
			}
		}
	}
	return false, nil
}

func agentStepStillRunnable(steps []core.AgentStep, stepID string) bool {
	for _, step := range steps {
		if step.ID == stepID {
			return step.SendStatus == core.AgentStepSendStatusPending || step.SendStatus == core.AgentStepSendStatusSent
		}
	}
	return false
}

func (s *Service) agentAttemptStillRunnable(ctx context.Context, requirementID string, view core.AgentStepAttemptView) (bool, error) {
	switch view.StepCategory {
	case core.AgentStepCategoryStandard:
		if s.standard == nil {
			return true, nil
		}
		flow, found, err := s.standard.GetClearDevStandardFlow(ctx, requirementID)
		return found && agentStepStillRunnable(flow.AgentSteps, view.LogicalStepID), err
	case core.AgentStepCategoryComplexPlanning:
		if s.complex == nil {
			return true, nil
		}
		planning, found, err := s.complex.GetClearDevComplexPlanning(ctx, requirementID)
		return found && agentStepStillRunnable(planning.AgentSteps, view.LogicalStepID), err
	case core.AgentStepCategoryDirection:
		if s.direction == nil {
			return true, nil
		}
		change, found, err := s.direction.GetClearDevDirectionChange(ctx, requirementID)
		return found && agentStepStillRunnable(change.AgentSteps, view.LogicalStepID), err
	case core.AgentStepCategoryComplexExecution, core.AgentStepCategoryException, core.AgentStepCategoryQuickExecution:
		if s.complexExecution == nil {
			return true, nil
		}
		execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
		if err != nil {
			return false, err
		}
		if found {
			if view.StepKind == core.AgentStepRequirementFinalReview {
				review := execution.FinalReview
				phase, _ := core.DeriveComplexExecutionPhase(execution)
				return review != nil && review.Step().ID == view.LogicalStepID && review.ID == view.RoleBindingID && review.AOSessionID == view.AOSessionID &&
					(review.Status == "PENDING" || review.Status == "SENT") && phase != core.ComplexExecutionBlocked && phase != core.ComplexExecutionNeedsHuman && phase != core.ComplexExecutionCompleted, nil
			}
			if agentStepStillRunnable(execution.AgentSteps, view.LogicalStepID) {
				return true, nil
			}
			if execution.Exception != nil && agentStepStillRunnable(execution.Exception.OnDemandSteps, view.LogicalStepID) {
				return true, nil
			}
		}
		quick, found, err := s.complexExecution.GetClearDevComplexQuickExecution(ctx, requirementID)
		return found && agentStepStillRunnable(quick.AgentSteps, view.LogicalStepID), err
	case core.AgentStepCategoryProgress:
		if s.progress == nil {
			return true, nil
		}
		request, found, err := s.progress.GetClearDevProgressExplanation(ctx, view.LogicalStepID)
		return found && (request.Status == core.ProgressExplanationPending || request.Status == core.ProgressExplanationSent), err
	default:
		return false, errors.New("unknown controlled Agent attempt category")
	}
}

func (s *Service) recordAgentRecoveryStop(ctx context.Context, view core.AgentStepAttemptView) error {
	clientMessageID := view.LastClientMessageID
	if clientMessageID == "" {
		clientMessageID = view.ClientMessageID
	}
	event := core.AgentAttemptEvent{
		ID: agentEvidenceID(view.ID, "recovery-stop", clientMessageID), AttemptID: view.ID,
		Status: core.AgentAttemptFailed, ClientMessageID: clientMessageID,
		TurnID: view.TurnID, TurnState: domain.TurnStateFailed,
		FailureCategory: domain.AgentFailureSessionLost,
		ErrorSummary:    safeAgentFailureSummary(domain.AgentFailureSessionLost), RecordedAt: s.now().UTC(),
	}
	return s.attempts.RecordClearDevAgentAttemptEvent(ctx, event)
}

type agentTerminalError struct{ turn domain.ConversationTurn }

func (e *agentTerminalError) Error() string {
	return "controlled Agent turn ended without a completed result"
}

var errAgentSessionLost = errors.New("controlled Agent session was lost")

var errAgentDeliveryUnknown = errors.New("controlled Agent message delivery is unknown")

func stableSendFailure(err error) domain.ConversationFailure {
	if failure, ok := ports.ChatFailureFromError(err); ok {
		return normalizeAgentFailure(failure)
	}
	if failure, ok := classifiedPortFailure(err); ok {
		return failure
	}
	return domain.ConversationFailure{
		Category: domain.AgentFailureDeliveryUnknown, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureDeliveryUnknown),
		Retryable: false,
	}
}

func stableObservationFailure(err error) domain.ConversationFailure {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errComplexAgentStepTimeout) {
		return domain.ConversationFailure{Category: domain.AgentFailureObservationTimeout, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureObservationTimeout), Retryable: true}
	}
	if errors.Is(err, errAgentSessionLost) {
		return domain.ConversationFailure{Category: domain.AgentFailureSessionLost, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureSessionLost), Retryable: true}
	}
	var terminal *agentTerminalError
	if errors.As(err, &terminal) {
		if terminal.turn.Failure != nil {
			return normalizeAgentFailure(*terminal.turn.Failure)
		}
		if terminal.turn.State == domain.TurnStateInterrupted {
			return domain.ConversationFailure{Category: domain.AgentFailureTurnInterrupted, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureTurnInterrupted)}
		}
	}
	if failure, ok := ports.ChatFailureFromError(err); ok {
		return normalizeAgentFailure(failure)
	}
	if failure, ok := classifiedPortFailure(err); ok {
		return failure
	}
	return domain.ConversationFailure{Category: domain.AgentFailureProvider, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureProvider)}
}

func classifiedPortFailure(err error) (domain.ConversationFailure, bool) {
	var category domain.AgentFailureCategory
	retryable := false
	switch {
	case errors.Is(err, ports.ErrChatModelNotAvailable):
		category = domain.AgentFailureModelUnavailable
	case errors.Is(err, ports.ErrChatAuthRequired):
		category = domain.AgentFailureAuthentication
	case errors.Is(err, ports.ErrChatQuotaExhausted):
		category = domain.AgentFailureQuotaExhausted
	case errors.Is(err, ports.ErrChatRateLimited):
		category, retryable = domain.AgentFailureRateLimited, true
	case errors.Is(err, ports.ErrChatProviderUnavailable), errors.Is(err, ports.ErrChatDriverUnavailable):
		category, retryable = domain.AgentFailureProviderUnavailable, true
	case errors.Is(err, ports.ErrChatDriverIncompatible), errors.Is(err, ports.ErrChatUnsupported):
		category = domain.AgentFailureDriverIncompatible
	case errors.Is(err, ports.ErrChatResumeFailed):
		category, retryable = domain.AgentFailureSessionLost, true
	default:
		return domain.ConversationFailure{}, false
	}
	return domain.ConversationFailure{Category: category, ErrorSummary: safeAgentFailureSummary(category), Retryable: retryable}, true
}

func normalizeAgentFailure(failure domain.ConversationFailure) domain.ConversationFailure {
	if safeAgentFailureSummary(failure.Category) == "" {
		failure.Category = domain.AgentFailureProvider
	}
	failure.ProviderErrorCode = boundedEvidenceSummary(failure.ProviderErrorCode)
	failure.ErrorSummary = safeAgentFailureSummary(failure.Category)
	if failure.Category == domain.AgentFailureDeliveryUnknown {
		failure.Retryable, failure.RetryAt = false, nil
	}
	return failure
}

func safeAgentFailureSummary(category domain.AgentFailureCategory) string {
	switch category {
	case domain.AgentFailureModelUnavailable:
		return "requested model is unavailable"
	case domain.AgentFailureAuthentication:
		return "provider login is required"
	case domain.AgentFailureQuotaExhausted:
		return "provider quota is exhausted"
	case domain.AgentFailureRateLimited:
		return "provider rate limit is active"
	case domain.AgentFailureProviderUnavailable:
		return "provider is unavailable"
	case domain.AgentFailureDriverIncompatible:
		return "chat driver is incompatible"
	case domain.AgentFailureSessionLost:
		return "bound Agent session was lost"
	case domain.AgentFailureTurnInterrupted:
		return "Agent turn was interrupted"
	case domain.AgentFailureObservationTimeout:
		return "Agent result observation timed out"
	case domain.AgentFailureDeliveryUnknown:
		return "message delivery outcome is unknown"
	case domain.AgentFailureProvider:
		return "provider reported an unclassified failure"
	case domain.AgentFailureResultInvalid:
		return "Agent result did not satisfy the frozen protocol"
	default:
		return ""
	}
}

func boundedEvidenceSummary(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1000 {
		value = value[:1000]
	}
	return value
}

func boundedDiagnosticSummary(value string) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "\uFFFD")
	if len(value) > 1000 {
		end := 1000
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		value = value[:end]
	}
	return value
}

func messageBudgetReason(err error, code core.ReasonCode) bool {
	var rule *core.RuleError
	return errors.As(err, &rule) && rule.Code == code
}

func isMessageBudgetError(err error) bool {
	return messageBudgetReason(err, core.ReasonMessageBudgetUnknown) || messageBudgetReason(err, core.ReasonMessageBudgetExhausted) || messageBudgetReason(err, core.ReasonMessageBudgetBinding)
}
