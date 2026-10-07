package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CheckControlledPreflight inspects the live provider catalog, login, and quota
// without creating a session or sending a billable turn. Callers must classify
// the returned error with errors.Is; ErrorSummary is a fixed English sentence.
func (s *Service) CheckControlledPreflight(ctx context.Context, harness domain.AgentHarness, requestedModel string) (ports.ChatControlledPreflight, error) {
	return s.CheckControlledProjectPreflight(ctx, ports.ChatPreflightContext{Harness: harness, RequestedModel: requestedModel})
}

// CheckControlledProjectPreflight preserves native per-project configuration.
// A local-only inspection explicitly leaves remote account facts unknown.
func (s *Service) CheckControlledProjectPreflight(ctx context.Context, cfg ports.ChatPreflightContext) (ports.ChatControlledPreflight, error) {
	harness, requestedModel := cfg.Harness, strings.TrimSpace(cfg.RequestedModel)
	cfg.RequestedModel = requestedModel
	result := ports.ChatControlledPreflight{
		RequestedModel: requestedModel,
		Provider:       string(harness),
	}
	if s.drivers == nil {
		fillNormalizedCatalog(&result, nil)
		result.ErrorSummary = preflightErrorSummary(ports.ErrChatDriverIncompatible)
		return result, ports.ErrChatDriverIncompatible
	}
	driver, err := s.drivers.Driver(harness)
	if err != nil {
		fillNormalizedCatalog(&result, nil)
		result.ErrorSummary = preflightErrorSummary(ports.ErrChatDriverIncompatible)
		return result, fmt.Errorf("%w: %s has no chat driver", ports.ErrChatUnsupported, harness)
	}
	inspector, ok := driver.(ports.ChatAccountInspector)
	if !ok {
		fillNormalizedCatalog(&result, nil)
		result.ErrorSummary = preflightErrorSummary(ports.ErrChatDriverIncompatible)
		return result, ports.ErrChatDriverIncompatible
	}
	var inspection ports.ChatAccountInspection
	var inspectErr error
	if contextual, supported := driver.(ports.ChatProjectAccountInspector); supported {
		inspection, inspectErr = contextual.InspectProjectAccount(ctx, cfg)
	} else {
		inspection, inspectErr = inspector.InspectAccount(ctx)
	}
	result.Evidence = inspection.Evidence
	result.Models = inspection.Models
	result.RateLimits = inspection.RateLimits
	result.ProviderErrorCode = inspection.RateLimits.ProviderErrorCode
	fillNormalizedCatalog(&result, inspection.Models)
	if inspectErr != nil {
		result.ErrorSummary = preflightErrorSummary(inspectErr)
		return result, inspectErr
	}
	// OpenCode's local catalog is not permission to silently select its first
	// provider. Controlled projects must name provider/model explicitly.
	if harness == domain.HarnessOpenCode && requestedModel == "" {
		result.ErrorSummary = "select an explicit OpenCode provider/model"
		return result, ports.ErrChatModelNotAvailable
	}
	resolved, found := resolveLiveModel(inspection.Models, requestedModel)
	result.ResolvedModel = resolved
	if requestedModel != "" && !found {
		result.ErrorSummary = preflightErrorSummary(ports.ErrChatModelNotAvailable)
		return result, ports.ErrChatModelNotAvailable
	}
	if requestedModel == "" && resolved == "" {
		result.ErrorSummary = preflightErrorSummary(ports.ErrChatModelNotAvailable)
		return result, ports.ErrChatModelNotAvailable
	}
	now := s.now().UTC()
	if inspection.RateLimits.QuotaExhausted {
		result.RetryAt = retryAtFromLimits(inspection.RateLimits, now)
		result.ErrorSummary = preflightErrorSummary(ports.ErrChatQuotaExhausted)
		return result, ports.ErrChatQuotaExhausted
	}
	if inspection.RateLimits.RateLimited {
		result.RetryAt = retryAtFromLimits(inspection.RateLimits, now)
		result.ErrorSummary = preflightErrorSummary(ports.ErrChatRateLimited)
		return result, ports.ErrChatRateLimited
	}
	return result, nil
}

func fillNormalizedCatalog(result *ports.ChatControlledPreflight, models []ports.ChatModel) {
	result.CatalogJSON, result.CatalogSHA256 = normalizeLiveCatalog(models)
}

func normalizeLiveCatalog(models []ports.ChatModel) (string, string) {
	ids := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		raw = []byte("[]")
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:])
}

func resolveLiveModel(models []ports.ChatModel, requested string) (string, bool) {
	if requested != "" {
		for _, model := range models {
			if model.ID == requested {
				return requested, true
			}
		}
		return "", false
	}
	var first string
	for _, model := range models {
		if first == "" {
			first = model.ID
		}
		if model.Default {
			return model.ID, true
		}
	}
	return first, first != ""
}

func retryAtFromLimits(limits ports.ChatRateLimits, now time.Time) *time.Time {
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

func preflightErrorSummary(err error) string {
	switch {
	case errors.Is(err, ports.ErrChatToolNotInstalled):
		return "the selected local tool is not installed"
	case errors.Is(err, ports.ErrChatConfigurationInvalid):
		return "the selected tool could not read its project configuration"
	case errors.Is(err, ports.ErrChatModelNotAvailable):
		return "requested model is not in the provider catalog"
	case errors.Is(err, ports.ErrChatAuthRequired):
		return "provider authentication is required"
	case errors.Is(err, ports.ErrChatQuotaExhausted):
		return "provider reported quota exhausted"
	case errors.Is(err, ports.ErrChatRateLimited):
		return "provider reported a rate limit"
	case errors.Is(err, ports.ErrChatProviderUnavailable), errors.Is(err, ports.ErrChatDriverUnavailable):
		return "provider account inspection is unavailable"
	case errors.Is(err, ports.ErrChatDriverIncompatible), errors.Is(err, ports.ErrChatUnsupported):
		return "chat driver cannot inspect the live catalog"
	default:
		return "controlled preflight could not be completed"
	}
}
