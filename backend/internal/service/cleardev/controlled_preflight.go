package cleardev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func normalizePreflightCatalog(models []ports.ChatModel) (string, string) {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if id := strings.TrimSpace(model.ID); id != "" {
			ids = append(ids, id)
		}
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		raw = []byte("[]")
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:])
}

func controlledPreflightIdle(status core.RoleBindingStatus, sessionID domain.SessionID) bool {
	return status == core.RoleBindingStatusRequested && sessionID == ""
}

func requestedChatModel(project domain.ProjectRecord, kind domain.SessionKind) string {
	if project.Config.ClearDev != nil {
		return project.Config.ClearDev.Model
	}
	model := strings.TrimSpace(project.Config.AgentConfig.Model)
	override := project.Config.Worker.AgentConfig
	if kind == domain.KindOrchestrator {
		override = project.Config.Orchestrator.AgentConfig
	}
	if strings.TrimSpace(override.Model) != "" {
		return strings.TrimSpace(override.Model)
	}
	return model
}

func (s *Service) spawnControlledChatSession(ctx context.Context, requirementID, roleBindingID string, cfg ports.SpawnConfig) (domain.Session, bool, error) {
	blocked, resolvedModel, err := s.runControlledPreflight(ctx, requirementID, roleBindingID, cfg.ProjectID, cfg.Kind)
	if err != nil {
		return domain.Session{}, false, err
	}
	if blocked {
		return domain.Session{}, true, nil
	}
	session, spawnErr := s.spawnResolvedChatSession(ctx, cfg, resolvedModel)
	return session, false, spawnErr
}

func (s *Service) inspectConfiguredPreflight(ctx context.Context, requirementID, roleBindingID string, projectID domain.ProjectID, kind domain.SessionKind) (bool, string, error) {
	if s.preflightChecker == nil || s.preflights == nil || s.ao == nil {
		return false, "", errors.New("ClearDev controlled preflight is not configured")
	}
	return s.runControlledPreflight(ctx, requirementID, roleBindingID, projectID, kind)
}

func (s *Service) spawnResolvedChatSession(ctx context.Context, cfg ports.SpawnConfig, resolvedModel string) (domain.Session, error) {
	project, err := s.controlledProject(ctx, string(cfg.ProjectID))
	if err != nil {
		return domain.Session{}, err
	}
	if choice := project.Config.ClearDev; choice != nil && choice.Model != resolvedModel {
		return domain.Session{}, errors.New("controlled project model changed after preflight")
	}
	cfg.Harness = project.Config.ClearDevHarness()
	cfg.AgentConfig.Model = resolvedModel
	var effort string
	var settingsWriter interface {
		SetTurnSettings(context.Context, domain.SessionID, domain.ConversationSettings) (domain.ConversationSettings, error)
	}
	if cfg.Harness == domain.HarnessCodex {
		effort = strings.TrimSpace(project.Config.AgentConfig.Mode)
		override := project.Config.Worker.AgentConfig.Mode
		if cfg.Kind == domain.KindOrchestrator {
			override = project.Config.Orchestrator.AgentConfig.Mode
		}
		if strings.TrimSpace(override) != "" {
			effort = strings.TrimSpace(override)
		}
		if project.Config.ClearDev != nil {
			effort = project.Config.ClearDev.Effort
		}
		if effort != "" {
			var supported bool
			settingsWriter, supported = s.chat.(interface {
				SetTurnSettings(context.Context, domain.SessionID, domain.ConversationSettings) (domain.ConversationSettings, error)
			})
			if !supported {
				return domain.Session{}, errors.New("controlled Codex reasoning effort cannot be bound before the first turn")
			}
		}
	}
	session, _, _, err := s.sessions.Spawn(ctx, cfg)
	if err != nil || effort == "" {
		return session, err
	}
	settings := domain.ConversationSettings{Model: resolvedModel, ReasoningEffort: effort, ApprovalMode: cfg.AgentConfig.Permissions}
	if _, err := settingsWriter.SetTurnSettings(ctx, session.ID, settings); err != nil {
		return session, fmt.Errorf("bind controlled Codex reasoning effort: %w", err)
	}
	return session, err
}

func (s *Service) runControlledPreflight(ctx context.Context, requirementID, roleBindingID string, projectID domain.ProjectID, kind domain.SessionKind) (bool, string, error) {
	if s.ao == nil || s.preflightChecker == nil || s.preflights == nil {
		return false, "", errors.New("ClearDev controlled preflight is not configured")
	}
	project, found, err := s.ao.GetProject(ctx, string(projectID))
	if err != nil {
		return false, "", err
	}
	if !found {
		return false, "", errors.New("AO project was not found for controlled preflight")
	}
	return s.runControlledModelPreflight(ctx, requirementID, roleBindingID, projectID, requestedChatModel(project, kind))
}

// runControlledModelPreflight checks an already bound model without resolving a new project default.
func (s *Service) runControlledModelPreflight(ctx context.Context, requirementID, roleBindingID string, projectID domain.ProjectID, requested string) (bool, string, error) {
	if s.preflightChecker == nil || s.preflights == nil {
		return false, "", errors.New("ClearDev controlled preflight is not configured")
	}
	project, err := s.controlledProject(ctx, string(projectID))
	if err != nil {
		return false, "", err
	}
	harness := project.Config.ClearDevHarness()
	if choice := project.Config.ClearDev; choice != nil && choice.Model != requested {
		return false, "", errors.New("bound model does not match the project's fixed execution choice")
	}
	now := s.now().UTC()
	var result ports.ChatControlledPreflight
	var checkErr error
	if checker, supported := s.preflightChecker.(interface {
		CheckControlledProjectPreflight(context.Context, ports.ChatPreflightContext) (ports.ChatControlledPreflight, error)
	}); supported {
		result, checkErr = checker.CheckControlledProjectPreflight(ctx, ports.ChatPreflightContext{
			Harness: harness, RequestedModel: requested, WorkspacePath: project.Path, Env: project.Config.Env,
		})
	} else if harness == domain.HarnessCodex {
		result, checkErr = s.preflightChecker.CheckControlledPreflight(ctx, harness, requested)
	} else {
		checkErr = ports.ErrChatDriverIncompatible
	}
	record := core.ControlledPreflight{
		ID:                       s.newID(),
		DevelopmentRequirementID: requirementID,
		RoleBindingID:            roleBindingID,
		AOProjectID:              string(projectID),
		RequestedModel:           requested,
		ResolvedModel:            result.ResolvedModel,
		Provider:                 firstNonEmpty(result.Provider, string(harness)),
		CatalogJSON:              result.CatalogJSON,
		CatalogSHA256:            result.CatalogSHA256,
		Outcome:                  core.ControlledPreflightPassed,
		Retryable:                false,
		ProviderErrorCode:        result.ProviderErrorCode,
		ErrorSummary:             result.ErrorSummary,
		Evidence:                 result.Evidence,
		CheckedAt:                now,
	}
	if record.CatalogJSON == "" {
		record.CatalogJSON, record.CatalogSHA256 = normalizePreflightCatalog(result.Models)
	}
	if checkErr != nil {
		reason, retryAt, summary, providerCode := classifyControlledPreflight(checkErr, result, now)
		record.Outcome = core.ControlledPreflightFailed
		record.ReasonCode = reason
		record.Retryable = true
		record.RetryAt = retryAt
		record.ErrorSummary = summary
		record.ProviderErrorCode = providerCode
		if err := s.preflights.RecordClearDevControlledPreflight(ctx, record); err != nil {
			return false, "", err
		}
		s.logger.Error("ClearDev controlled preflight failed", "requirementID", requirementID, "roleBindingID", roleBindingID, "reason", reason)
		return true, "", nil
	}
	if err := s.preflights.RecordClearDevControlledPreflight(ctx, record); err != nil {
		if errors.Is(err, core.ErrControlledExecutionChoiceChanged) {
			return true, "", nil // The store committed a retryable FAILED record.
		}
		return false, "", err
	}
	return false, record.ResolvedModel, nil
}

func classifyControlledPreflight(err error, result ports.ChatControlledPreflight, now time.Time) (core.ReasonCode, *time.Time, string, string) {
	providerCode := result.ProviderErrorCode
	if providerCode == "" {
		providerCode = result.RateLimits.ProviderErrorCode
	}
	summaryFor := func(fallback string) string {
		if result.ErrorSummary != "" {
			return result.ErrorSummary
		}
		return fallback
	}
	switch {
	case errors.Is(err, ports.ErrChatToolNotInstalled):
		return core.ReasonCode("EXECUTION_TOOL_NOT_INSTALLED"), nil, summaryFor("the selected local tool is not installed"), providerCode
	case errors.Is(err, ports.ErrChatConfigurationInvalid):
		return core.ReasonCode("EXECUTION_TOOL_CONFIG_INVALID"), nil, summaryFor("the selected tool could not read its project configuration"), providerCode
	case errors.Is(err, ports.ErrChatModelNotAvailable):
		return core.ReasonModelNotAvailable, nil, summaryFor("requested model is not in the provider catalog"), providerCode
	case errors.Is(err, ports.ErrChatAuthRequired):
		return core.ReasonLoginRequired, nil, summaryFor("provider authentication is required"), providerCode
	case errors.Is(err, ports.ErrChatQuotaExhausted):
		retryAt := result.RetryAt
		if retryAt == nil {
			retryAt = retryAtFromChatLimits(result.RateLimits, now)
		}
		return core.ReasonQuotaExhausted, retryAt, summaryFor("provider reported quota exhausted"), providerCode
	case errors.Is(err, ports.ErrChatRateLimited):
		retryAt := result.RetryAt
		if retryAt == nil {
			retryAt = retryAtFromChatLimits(result.RateLimits, now)
		}
		return core.ReasonRateLimited, retryAt, summaryFor("provider reported a rate limit"), providerCode
	case errors.Is(err, ports.ErrChatProviderUnavailable), errors.Is(err, ports.ErrChatDriverUnavailable):
		return core.ReasonProviderUnavailable, nil, summaryFor("provider account inspection is unavailable"), providerCode
	case errors.Is(err, ports.ErrChatDriverIncompatible), errors.Is(err, ports.ErrChatUnsupported):
		return core.ReasonDriverIncompatible, nil, summaryFor("chat driver cannot inspect the live catalog"), providerCode
	default:
		return core.ReasonProviderUnavailable, nil, "controlled preflight could not be completed", providerCode
	}
}

func retryAtFromChatLimits(limits ports.ChatRateLimits, now time.Time) *time.Time {
	seconds := limits.PrimaryResetsInSeconds
	if limits.QuotaExhausted && limits.SecondaryResetsInSeconds > seconds {
		seconds = limits.SecondaryResetsInSeconds
	}
	if seconds <= 0 {
		return nil
	}
	at := now.Add(time.Duration(seconds) * time.Second)
	return &at
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
