package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const parseCorrectionPromptPrefix = "The previous JSON result was rejected by the protocol parser."

var errComplexAgentStepTimeout = errors.New("complex agent step exceeded its timeout")

type parseCorrectionFail func(context.Context, core.AgentStep, core.ReasonCode) error

func mechanicalParseCorrectionPrompt(parseErr error) string {
	detail := "unknown parse error"
	if parseErr != nil {
		detail = strings.TrimSpace(parseErr.Error())
	}
	if detail == "" {
		detail = "unknown parse error"
	}
	return parseCorrectionPromptPrefix + "\n\nParser error:\n" + detail + "\n\nReturn exactly one complete valid JSON object for the current request. Do not modify files."
}

func parseCorrectionClientMessageID(step core.AgentStep) string {
	return strings.TrimSpace(step.ClientMessageID) + ":parse-correction"
}

func peekAgentResultKind(raw []byte) string {
	var envelope struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Kind)
}

func agentObservationReason(err error, timeout, unavailable core.ReasonCode) core.ReasonCode {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errComplexAgentStepTimeout) {
		return timeout
	}
	return unavailable
}

func (s *Service) getParseCorrection(ctx context.Context, stepID string) (core.ParseCorrection, bool, error) {
	if s.corrections == nil {
		return core.ParseCorrection{}, false, nil
	}
	return s.corrections.GetClearDevParseCorrection(ctx, stepID)
}

func (s *Service) recordParseCorrection(ctx context.Context, correction core.ParseCorrection) error {
	if s.corrections == nil {
		return errors.New("ClearDev parse correction store is not configured")
	}
	return s.corrections.RecordClearDevParseCorrection(ctx, correction)
}

func (s *Service) failAgentObservation(ctx context.Context, requirementID string, step core.AgentStep, clientMessageID string, observationErr error, reasons standardStepReasons, fail parseCorrectionFail, stopErr error) (standardMessage, bool, error) {
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return standardMessage{}, false, ctx.Err()
	}
	if isMessageBudgetError(observationErr) {
		return standardMessage{}, false, observationErr
	}
	if fail == nil {
		return standardMessage{}, true, stopErr
	}
	if failure, ok := ports.ChatFailureFromError(observationErr); ok && failure.Category == domain.AgentFailureDeliveryUnknown {
		return standardMessage{}, true, stopErr
	}
	failCtx := ctx
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		failCtx = context.WithoutCancel(ctx)
	}
	if err := s.recordAgentObservationFailure(failCtx, requirementID, step, clientMessageID, observationErr); err != nil {
		return standardMessage{}, false, err
	}
	views, err := s.attempts.ListClearDevAgentStepAttemptStates(failCtx, requirementID, step.ID)
	if err != nil {
		return standardMessage{}, false, err
	}
	items := logicalStepAttemptViews(views, step.ID)
	if len(items) != 0 {
		latest := items[len(items)-1]
		if latest.SendStatus == core.AgentAttemptObservationTimedOut || retryWindowPending(latest, s.now().UTC()) || canCreateSecondAgentAttempt(latest, s.now().UTC()) {
			return standardMessage{}, true, stopErr
		}
	}
	if err := fail(failCtx, step, agentObservationReason(observationErr, reasons.Timeout, reasons.Unavailable)); err != nil {
		return standardMessage{}, false, err
	}
	return standardMessage{}, true, stopErr
}

func (s *Service) sendParseCorrection(ctx context.Context, requirementID string, category core.AgentStepCategory, sessionID string, step core.AgentStep, parseErr error) (core.ParseCorrection, error) {
	attempt, err := s.activeAgentAttempt(ctx, requirementID, category, step, sessionID)
	if err != nil {
		return core.ParseCorrection{}, err
	}
	if attempt.AttemptNumber == 3 {
		return core.ParseCorrection{}, errors.New("human-authorized original has no correction allowance")
	}
	prompt := mechanicalParseCorrectionPrompt(parseErr)
	correction := core.ParseCorrection{
		StepID: step.ID, AttemptNumber: attempt.AttemptNumber, ClientMessageID: parseCorrectionClientMessageID(step),
		PromptText: prompt, PromptSHA256: coreDigest([]byte(prompt)), SentAt: s.now().UTC(),
	}
	if err := s.recordParseCorrection(ctx, correction); err != nil {
		return core.ParseCorrection{}, err
	}
	stored, found, err := s.getParseCorrection(ctx, step.ID)
	if err != nil {
		return core.ParseCorrection{}, err
	}
	if !found || stored.AttemptNumber != attempt.AttemptNumber {
		return core.ParseCorrection{}, errors.New("logical Agent step already used its parse correction")
	}
	correction = stored
	if err := s.relayAgentTurn(ctx, requirementID, category, step, sessionID, correction.PromptText, correction.ClientMessageID, core.AgentAttemptCorrectionSent, correction.SentAt); err != nil {
		return correction, err
	}
	return correction, nil
}

func (s *Service) awaitCorrectedAgentJSON(
	ctx context.Context,
	requirementID string,
	category core.AgentStepCategory,
	sessionID string,
	step core.AgentStep,
	correction core.ParseCorrection,
	validate func([]byte) error,
	reasons standardStepReasons,
	fail parseCorrectionFail,
	stopErr error,
) (standardMessage, bool, error) {
	if err := s.relayAgentTurn(ctx, requirementID, category, step, sessionID, correction.PromptText, correction.ClientMessageID, core.AgentAttemptCorrectionSent, correction.SentAt); err != nil {
		return s.failAgentObservation(ctx, requirementID, step, correction.ClientMessageID, err, reasons, fail, stopErr)
	}
	attempt, err := s.activeAgentAttempt(ctx, requirementID, category, step, sessionID)
	if err != nil {
		if isAgentRecoveryDeferred(err) {
			return standardMessage{}, true, stopErr
		}
		return standardMessage{}, false, err
	}
	awaitStep := step
	awaitStep.ClientMessageID = agentAttemptClientMessageID(step.ClientMessageID, attempt.AttemptNumber, true)
	message, observationErr := s.awaitAgentMessage(ctx, core.RoleSessionBinding{AOSessionID: attempt.AOSessionID}, awaitStep, correction.PromptText)
	if observationErr != nil {
		return s.failAgentObservation(ctx, requirementID, step, correction.ClientMessageID, observationErr, reasons, fail, stopErr)
	}
	resultID, recordErr := s.recordAgentResult(ctx, requirementID, step, message, core.AgentResultCorrection)
	if recordErr != nil {
		return standardMessage{}, false, recordErr
	}
	parseErr := validate([]byte(message.Text))
	if recordErr := s.recordAgentParse(ctx, resultID, parseErr); recordErr != nil {
		return standardMessage{}, false, recordErr
	}
	if parseErr != nil {
		if recordErr := s.recordAgentObservationFailure(ctx, requirementID, step, correction.ClientMessageID, ports.WithChatFailure(parseErr, domain.ConversationFailure{Category: domain.AgentFailureResultInvalid, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureResultInvalid)})); recordErr != nil {
			return standardMessage{}, false, recordErr
		}
		reason := reasons.Invalid
		if reasons.MapInvalid != nil {
			reason = reasons.MapInvalid(parseErr)
		}
		if failErr := fail(ctx, step, reason); failErr != nil {
			return standardMessage{}, false, failErr
		}
		return standardMessage{}, true, stopErr
	}
	return message, false, nil
}

func (s *Service) acceptOrCorrectAgentJSON(
	ctx context.Context,
	requirementID string,
	category core.AgentStepCategory,
	sessionID string,
	step core.AgentStep,
	message standardMessage,
	validate func([]byte) error,
	reasons standardStepReasons,
	fail parseCorrectionFail,
	stopErr error,
) (standardMessage, bool, error) {
	resultID, recordErr := s.recordAgentResult(ctx, requirementID, step, message, core.AgentResultOriginal)
	if recordErr != nil {
		return standardMessage{}, false, recordErr
	}
	parseErr := validate([]byte(message.Text))
	if recordErr := s.recordAgentParse(ctx, resultID, parseErr); recordErr != nil {
		return standardMessage{}, false, recordErr
	}
	if parseErr == nil {
		return message, false, nil
	}
	views, viewErr := s.attempts.ListClearDevAgentStepAttemptStates(ctx, requirementID, step.ID)
	if viewErr != nil {
		return standardMessage{}, false, viewErr
	}
	items := logicalStepAttemptViews(views, step.ID)
	if len(items) > 0 && items[len(items)-1].AttemptNumber == 3 {
		if err := s.recordAgentObservationFailure(ctx, requirementID, step, step.ClientMessageID, ports.WithChatFailure(parseErr, domain.ConversationFailure{Category: domain.AgentFailureResultInvalid, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureResultInvalid)})); err != nil {
			return standardMessage{}, false, err
		}
		reason := reasons.Invalid
		if reasons.MapInvalid != nil {
			reason = reasons.MapInvalid(parseErr)
		}
		if fail != nil {
			if err := fail(ctx, step, reason); err != nil {
				return standardMessage{}, false, err
			}
		}
		return standardMessage{}, true, stopErr
	}
	existing, found, loadErr := s.getParseCorrection(ctx, step.ID)
	if loadErr != nil {
		return standardMessage{}, false, loadErr
	}
	if found {
		views, viewErr := s.attempts.ListClearDevAgentStepAttemptStates(ctx, requirementID, step.ID)
		if viewErr != nil {
			return standardMessage{}, false, viewErr
		}
		items := logicalStepAttemptViews(views, step.ID)
		if len(items) != 0 && existing.AttemptNumber == items[len(items)-1].AttemptNumber {
			return s.awaitCorrectedAgentJSON(ctx, requirementID, category, sessionID, step, existing, validate, reasons, fail, stopErr)
		}
		if recordErr := s.recordAgentObservationFailure(ctx, requirementID, step, step.ClientMessageID, ports.WithChatFailure(parseErr, domain.ConversationFailure{Category: domain.AgentFailureResultInvalid, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureResultInvalid)})); recordErr != nil {
			return standardMessage{}, false, recordErr
		}
		reason := reasons.Invalid
		if reasons.MapInvalid != nil {
			reason = reasons.MapInvalid(parseErr)
		}
		if failErr := fail(ctx, step, reason); failErr != nil {
			return standardMessage{}, false, failErr
		}
		return standardMessage{}, true, stopErr
	}
	correction, sendErr := s.sendParseCorrection(ctx, requirementID, category, sessionID, step, parseErr)
	if sendErr != nil {
		if ctx.Err() != nil {
			return standardMessage{}, false, ctx.Err()
		}
		return s.failAgentObservation(ctx, requirementID, step, correction.ClientMessageID, sendErr, reasons, fail, stopErr)
	}
	return s.awaitCorrectedAgentJSON(ctx, requirementID, category, sessionID, step, correction, validate, reasons, fail, stopErr)
}

func (s *Service) awaitValidAgentJSON(
	ctx context.Context,
	requirementID string,
	category core.AgentStepCategory,
	sessionID string,
	step core.AgentStep,
	originalPrompt string,
	validate func([]byte) error,
	reasons standardStepReasons,
	fail parseCorrectionFail,
	stopErr error,
) (standardMessage, bool, error) {
	waitCtx, cancel := context.WithTimeout(ctx, s.stepTimeout)
	defer cancel()
	attempt, err := s.activeAgentAttempt(waitCtx, requirementID, category, step, sessionID)
	if err != nil {
		if isAgentRecoveryDeferred(err) {
			return standardMessage{}, true, stopErr
		}
		return standardMessage{}, false, err
	}
	existing, found, err := s.getParseCorrection(waitCtx, step.ID)
	if err != nil {
		return standardMessage{}, false, err
	}
	if found && existing.AttemptNumber == attempt.AttemptNumber {
		return s.awaitCorrectedAgentJSON(waitCtx, requirementID, category, sessionID, step, existing, validate, reasons, fail, stopErr)
	}
	if sendErr := s.ensureOriginalAgentAttemptDelivery(waitCtx, requirementID, category, step, attempt, sessionID, originalPrompt); sendErr != nil {
		if waitCtx.Err() != nil {
			return standardMessage{}, false, waitCtx.Err()
		}
		failure, classified := ports.ChatFailureFromError(sendErr)
		if !classified {
			return standardMessage{}, false, sendErr
		}
		if failure.Category == domain.AgentFailureDeliveryUnknown {
			return standardMessage{}, true, stopErr
		}
		if fail == nil {
			return standardMessage{}, true, stopErr
		}
		if failErr := fail(waitCtx, step, agentObservationReason(sendErr, reasons.Timeout, reasons.Unavailable)); failErr != nil {
			return standardMessage{}, false, failErr
		}
		return standardMessage{}, true, stopErr
	}
	awaitStep := step
	awaitStep.ClientMessageID = agentAttemptClientMessageID(step.ClientMessageID, attempt.AttemptNumber, false)
	message, observationErr := s.awaitAgentMessage(waitCtx, core.RoleSessionBinding{AOSessionID: attempt.AOSessionID}, awaitStep, originalPrompt)
	if observationErr != nil {
		return s.failAgentObservation(waitCtx, requirementID, step, step.ClientMessageID, observationErr, reasons, fail, stopErr)
	}
	return s.acceptOrCorrectAgentJSON(waitCtx, requirementID, category, sessionID, step, message, validate, reasons, fail, stopErr)
}

type pollJSONResult struct {
	message         standardMessage
	ready           bool
	stopped         bool
	deliveryUnknown bool
	failCode        core.ReasonCode
}

func (s *Service) pollValidAgentJSON(
	ctx context.Context,
	requirementID string,
	category core.AgentStepCategory,
	sessionID string,
	step core.AgentStep,
	originalPrompt string,
	validate func([]byte) error,
	timeoutReason, unavailableReason, invalidReason core.ReasonCode,
) (pollJSONResult, error) {
	attempt, err := s.activeAgentAttempt(ctx, requirementID, category, step, sessionID)
	if err != nil {
		if isAgentRecoveryDeferred(err) {
			return pollJSONResult{}, nil
		}
		return pollJSONResult{}, err
	}
	awaitStep := step
	prompt := originalPrompt
	existing, found, err := s.getParseCorrection(ctx, step.ID)
	if err != nil {
		return pollJSONResult{}, err
	}
	useCorrection := found && existing.AttemptNumber == attempt.AttemptNumber
	if useCorrection {
		awaitStep.ClientMessageID = agentAttemptClientMessageID(step.ClientMessageID, attempt.AttemptNumber, true)
		prompt = existing.PromptText
	} else {
		awaitStep.ClientMessageID = agentAttemptClientMessageID(step.ClientMessageID, attempt.AttemptNumber, false)
	}
	if !useCorrection {
		if sendErr := s.ensureOriginalAgentAttemptDelivery(ctx, requirementID, category, step, attempt, sessionID, originalPrompt); sendErr != nil {
			if ctx.Err() != nil {
				return pollJSONResult{}, ctx.Err()
			}
			failure, classified := ports.ChatFailureFromError(sendErr)
			if !classified {
				return pollJSONResult{}, sendErr
			}
			if failure.Category == domain.AgentFailureDeliveryUnknown {
				return pollJSONResult{stopped: true, deliveryUnknown: true}, nil
			}
			return pollJSONResult{stopped: true, failCode: agentObservationReason(sendErr, timeoutReason, unavailableReason)}, nil
		}
	}
	message, ready, pollErr := s.pollComplexAgentMessage(ctx, attempt.AOSessionID, awaitStep, prompt)
	if pollErr != nil {
		if recordErr := s.recordAgentObservationFailure(ctx, requirementID, step, awaitStep.ClientMessageID, pollErr); recordErr != nil {
			return pollJSONResult{}, recordErr
		}
		views, viewErr := s.attempts.ListClearDevAgentStepAttemptStates(ctx, requirementID, step.ID)
		if viewErr != nil {
			return pollJSONResult{}, viewErr
		}
		items := logicalStepAttemptViews(views, step.ID)
		if len(items) != 0 {
			latest := items[len(items)-1]
			if latest.SendStatus == core.AgentAttemptObservationTimedOut || retryWindowPending(latest, s.now().UTC()) || canCreateSecondAgentAttempt(latest, s.now().UTC()) {
				return pollJSONResult{}, nil
			}
		}
		return pollJSONResult{stopped: true, failCode: agentObservationReason(pollErr, timeoutReason, unavailableReason)}, nil
	}
	if !ready {
		return pollJSONResult{}, nil
	}
	source := core.AgentResultOriginal
	if useCorrection {
		source = core.AgentResultCorrection
	}
	resultID, recordErr := s.recordAgentResult(ctx, requirementID, step, message, source)
	if recordErr != nil {
		return pollJSONResult{}, recordErr
	}
	parseErr := validate([]byte(message.Text))
	if recordErr := s.recordAgentParse(ctx, resultID, parseErr); recordErr != nil {
		return pollJSONResult{}, recordErr
	}
	if parseErr == nil {
		return pollJSONResult{message: message, ready: true}, nil
	} else if found {
		failure := domain.ConversationFailure{Category: domain.AgentFailureResultInvalid, ErrorSummary: safeAgentFailureSummary(domain.AgentFailureResultInvalid)}
		if recordErr := s.recordAgentObservationFailure(ctx, requirementID, step, awaitStep.ClientMessageID, ports.WithChatFailure(parseErr, failure)); recordErr != nil {
			return pollJSONResult{}, recordErr
		}
		return pollJSONResult{stopped: true, failCode: invalidReason}, nil
	}
	if _, sendErr := s.sendParseCorrection(ctx, requirementID, category, sessionID, step, parseErr); sendErr != nil {
		if ctx.Err() != nil {
			return pollJSONResult{}, ctx.Err()
		}
		if failure, ok := ports.ChatFailureFromError(sendErr); ok && failure.Category == domain.AgentFailureDeliveryUnknown {
			return pollJSONResult{stopped: true, deliveryUnknown: true}, nil
		}
		return pollJSONResult{stopped: true, failCode: agentObservationReason(sendErr, timeoutReason, unavailableReason)}, nil
	}
	return pollJSONResult{}, nil
}

func validateBuilderOrScopeRequest(raw []byte, dispatchID, taskID string, round int, versionID, versionSHA, planID, planSHA string) error {
	if peekAgentResultKind(raw) == core.ComplexScopeExpansionRequestKind {
		_, err := core.ParseComplexScopeExpansionRequest(raw, dispatchID, taskID, round, versionID, versionSHA, planID, planSHA)
		return err
	}
	_, err := core.ParseComplexExecutionBuilderResult(raw, dispatchID, taskID, round)
	return err
}

func builderOrScopeInvalidReason(raw []byte) core.ReasonCode {
	if peekAgentResultKind(raw) == core.ComplexScopeExpansionRequestKind {
		return core.ReasonScopeRequestInvalid
	}
	return core.ReasonCode("BUILDER_RESULT_INVALID")
}
