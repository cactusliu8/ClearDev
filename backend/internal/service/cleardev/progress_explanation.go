package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

const (
	progressExplanationIdempotencyPrefix = "cleardev-progress-explanation:"
	progressExplanationMessagePrefix     = "cleardev-progress-explanation-msg-"
)

// RequestProgressExplanation occupies one steward note for the current facts.
func (s *Service) RequestProgressExplanation(ctx context.Context, requirementID string) (RequirementView, error) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return RequirementView{}, err
	}

	if s.progress == nil || s.sessions == nil || s.chat == nil || s.ao == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev service is not fully configured")
	}
	view, err := s.GetRequirement(ctx, requirementID)
	if err != nil {
		return RequirementView{}, err
	}
	if !view.TrustedProgress.CanRequestExplanation {
		return RequirementView{}, apierr.Conflict("STEWARD_UNAVAILABLE", "No original Project Steward session is bound to this requirement", nil)
	}
	stewardSessionID := progressStewardSessionID(view)
	if stewardSessionID == "" {
		return RequirementView{}, apierr.Conflict("STEWARD_UNAVAILABLE", "No original Project Steward session is bound to this requirement", nil)
	}
	packet := core.BuildTrustedProgressFactPacket(view.TrustedProgress)
	prompt, err := core.TrustedProgressExplanationPrompt(packet)
	if err != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_PROGRESS_EXPLAIN_FAILED", "Could not build the progress fact packet")
	}
	now := s.now().UTC()
	requestID := s.newID()
	stored, _, err := s.progress.OccupyClearDevProgressExplanation(ctx, core.OccupyProgressExplanationCommand{
		Request: core.ProgressExplanationRequest{
			ID: requestID, DevelopmentRequirementID: view.Requirement.ID,
			FactSummarySHA256: view.TrustedProgress.FactSummarySHA256, MaxEventSequence: view.TrustedProgress.LatestFactSequence,
			SourceStewardSessionID:        stewardSessionID,
			SessionCreationIdempotencyKey: progressExplanationIdempotencyPrefix + requestID,
			ClientMessageID:               progressExplanationMessagePrefix + requestID,
			PromptText:                    prompt, PromptSHA256: coreDigest([]byte(prompt)), CreatedAt: now,
		},
	})
	if err != nil {
		return RequirementView{}, mapStoreError(err, "CLEARDEV_PROGRESS_EXPLAIN_FAILED")
	}
	s.scheduleProgressExplanation(stored.ID)
	return s.GetRequirement(ctx, view.Requirement.ID)
}

// ResumeProgressExplanations schedules unfinished notes after daemon boot.
func (s *Service) ResumeProgressExplanations(ctx context.Context) error {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return err
	}

	if s.progress == nil {
		return nil
	}
	rows, err := s.progress.ListClearDevRunnableProgressExplanations(ctx)
	if err != nil {
		return fmt.Errorf("list runnable ClearDev progress explanations: %w", err)
	}
	for _, row := range rows {
		s.scheduleProgressExplanation(row.ID)
	}
	return nil
}

func (s *Service) scheduleProgressExplanation(requestID string) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		s.logger.Error("ClearDev controlled scheduling rejected", "err", err)
		return
	}

	s.progressMu.Lock()
	if s.progressRunning[requestID] {
		s.progressMu.Unlock()
		return
	}
	s.progressRunning[requestID] = true
	s.progressMu.Unlock()
	s.runBackground(func() {
		defer func() {
			s.progressMu.Lock()
			delete(s.progressRunning, requestID)
			s.progressMu.Unlock()
		}()
		if err := s.runProgressExplanation(s.backgroundContext, requestID); err != nil {
			s.logger.Error("ClearDev progress explanation stopped with an internal error", "requestID", requestID, "error", err)
		}
	})
}

func (s *Service) runProgressExplanation(ctx context.Context, requestID string) error {
	request, found, err := s.progress.GetClearDevProgressExplanation(ctx, requestID)
	if err != nil || !found {
		return err
	}
	blocked, err := s.recoverRequirementAgentAttempts(ctx, request.DevelopmentRequirementID)
	if err != nil || blocked {
		return err
	}
	for iteration := 0; iteration < 16; iteration++ {
		progressed, done, err := s.advanceProgressExplanation(ctx, requestID)
		if err != nil || done {
			return err
		}
		if !progressed {
			return nil
		}
	}
	return errors.New("ClearDev progress explanation exceeded its bounded advance count")
}

func (s *Service) advanceProgressExplanation(ctx context.Context, requestID string) (bool, bool, error) {
	if s.progress == nil {
		return false, true, nil
	}
	request, ok, err := s.progress.GetClearDevProgressExplanation(ctx, requestID)
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, true, nil
	}
	if request.Status == core.ProgressExplanationSettled || request.Status == core.ProgressExplanationFailed {
		return false, true, nil
	}
	view, err := s.GetRequirement(ctx, request.DevelopmentRequirementID)
	if err != nil {
		return false, false, err
	}
	now := s.now().UTC()
	if request.Status == core.ProgressExplanationPending && request.FactSummarySHA256 != view.TrustedProgress.FactSummarySHA256 {
		_, failErr := s.progress.FailClearDevProgressExplanation(ctx, request.ID, core.ReasonCode("FACTS_CHANGED"), now)
		return failErr == nil, true, failErr
	}
	if request.Status == core.ProgressExplanationPending && request.AOSessionID == "" {
		return s.bindProgressExplanationSession(ctx, request, view)
	}
	if request.Status == core.ProgressExplanationPending {
		return s.sendProgressExplanation(ctx, request, now)
	}
	packet, err := core.PacketFromTrustedProgressPrompt(request.PromptText)
	if err != nil {
		_, failErr := s.progress.FailClearDevProgressExplanation(ctx, request.ID, core.ReasonCode("STEWARD_RESULT_INVALID"), s.now().UTC())
		return failErr == nil, true, failErr
	}
	step := core.AgentStep{
		ID: request.ID, Kind: core.AgentStepStatusReport,
		ClientMessageID: request.ClientMessageID, PromptSHA256: request.PromptSHA256,
		SendStatus: core.AgentStepSendStatusSent, RequestedAt: request.CreatedAt, SentAt: request.SentAt,
	}
	validate := func(raw []byte) error {
		_, parseErr := core.ParseTrustedProgressExplanationResult(raw, packet)
		return parseErr
	}
	fail := func(ctx context.Context, item core.AgentStep, reason core.ReasonCode) error {
		_, failErr := s.progress.FailClearDevProgressExplanation(ctx, item.ID, reason, s.now().UTC())
		return failErr
	}
	message, stopped, err := s.awaitValidAgentJSON(ctx, request.DevelopmentRequirementID, core.AgentStepCategoryProgress, request.AOSessionID, step, request.PromptText, validate, standardStepReasons{
		Invalid: "STEWARD_RESULT_INVALID", Timeout: "EXPLANATION_TIMEOUT", Unavailable: "STEWARD_UNAVAILABLE",
	}, fail, nil)
	if err != nil {
		return false, false, err
	}
	if stopped {
		return true, true, nil
	}
	parsed, err := core.ParseTrustedProgressExplanationResult([]byte(message.Text), packet)
	if err != nil {
		_, failErr := s.progress.FailClearDevProgressExplanation(ctx, request.ID, core.ReasonCode("STEWARD_RESULT_INVALID"), s.now().UTC())
		return failErr == nil, true, failErr
	}
	bound, err := json.Marshal(parsed)
	if err != nil {
		return false, false, err
	}
	changed, settleErr := s.progress.SettleClearDevProgressExplanation(ctx, request.ID, string(bound), coreDigest(bound), s.now().UTC())
	return changed, true, settleErr
}

func (s *Service) bindProgressExplanationSession(ctx context.Context, request core.ProgressExplanationRequest, view RequirementView) (bool, bool, error) {
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(request.SourceStewardSessionID))
	if err != nil {
		return false, false, err
	}
	if !found {
		_, failErr := s.progress.FailClearDevProgressExplanation(ctx, request.ID, core.ReasonCode("STEWARD_UNAVAILABLE"), s.now().UTC())
		return failErr == nil, true, failErr
	}
	if !sessionClearlyEnded(record) {
		if !progressExplanationCanUseOriginal(record) {
			return false, true, nil
		}
		changed, bindErr := s.progress.BindClearDevProgressExplanationSession(ctx, request.ID, request.SourceStewardSessionID, "")
		return changed, false, bindErr
	}
	spawned, blocked, spawnErr := s.spawnControlledChatSession(ctx, request.DevelopmentRequirementID, request.ID, ports.SpawnConfig{
		ProjectID: domain.ProjectID(view.Requirement.AOProjectID), Kind: domain.KindOrchestrator,
		Prompt: "", RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		DisplayName: "ClearDev Progress Steward Continuation", CreationIdempotencyKey: request.SessionCreationIdempotencyKey,
	})
	if blocked {
		return false, false, nil
	}
	if spawnErr != nil {
		_, failErr := s.progress.FailClearDevProgressExplanation(ctx, request.ID, core.ReasonCode("STEWARD_UNAVAILABLE"), s.now().UTC())
		return failErr == nil, true, failErr
	}
	if sessionClearlyEnded(spawned.SessionRecord) {
		_, failErr := s.progress.FailClearDevProgressExplanation(ctx, request.ID, core.ReasonCode("STEWARD_UNAVAILABLE"), s.now().UTC())
		return failErr == nil, true, failErr
	}
	changed, bindErr := s.progress.BindClearDevProgressExplanationSession(ctx, request.ID, string(spawned.ID), request.SourceStewardSessionID)
	return changed, false, bindErr
}

func (s *Service) sendProgressExplanation(ctx context.Context, request core.ProgressExplanationRequest, now time.Time) (bool, bool, error) {
	step := core.AgentStep{
		ID: request.ID, Kind: core.AgentStepStatusReport,
		ClientMessageID: request.ClientMessageID, PromptSHA256: request.PromptSHA256,
		SendStatus: core.AgentStepSendStatusPending, RequestedAt: request.CreatedAt,
	}
	if err := s.relayAgentTurn(ctx, request.DevelopmentRequirementID, core.AgentStepCategoryProgress, step, request.AOSessionID, request.PromptText, request.ClientMessageID, core.AgentAttemptSent, now); err != nil {
		if isMessageBudgetError(err) {
			return false, true, err
		}
		return false, true, nil //nolint:nilerr // append-only attempt evidence preserves the unknown send outcome
	}
	changed, markErr := s.progress.MarkClearDevProgressExplanationSent(ctx, request.ID, now)
	return changed, false, markErr
}

func sessionClearlyEnded(record domain.SessionRecord) bool {
	return record.IsTerminated || record.Activity.State == domain.ActivityExited
}

func progressExplanationCanUseOriginal(record domain.SessionRecord) bool {
	if sessionClearlyEnded(record) {
		return false
	}
	switch record.Activity.State {
	case domain.ActivityActive, domain.ActivityBlocked:
		return false
	default:
		return true
	}
}

func progressExplanationPromptPresent(snapshot chatsvc.Snapshot, clientMessageID, prompt string) bool {
	for _, message := range snapshot.Messages {
		if message.ClientMessageID == clientMessageID && message.Role == domain.MessageRoleUser &&
			message.Origin == domain.MessageOriginAutomation && message.Text == prompt {
			return true
		}
	}
	return false
}
