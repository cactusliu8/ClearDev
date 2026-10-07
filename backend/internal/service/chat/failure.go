package chat

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type stableTurnFailureStore interface {
	SettleTurnWithFailure(context.Context, string, string, domain.TurnState, string, domain.ConversationFailure, time.Time) error
	SettleTurnByIDWithFailure(context.Context, string, domain.TurnState, string, domain.ConversationFailure, time.Time) error
}

func (c *Controller) settleTurn(ctx context.Context, providerTurnID string, state domain.TurnState, errMessage string, failure *domain.ConversationFailure, now time.Time) error {
	if failure == nil && state != domain.TurnStateCompleted {
		fallback := terminalConversationFailure(state)
		failure = &fallback
	}
	if failure != nil {
		normalized := normalizeConversationFailure(*failure)
		if store, ok := c.store.(stableTurnFailureStore); ok {
			return store.SettleTurnWithFailure(ctx, c.conversation.ID, providerTurnID, state, errMessage, normalized, now)
		}
	}
	return c.store.SettleTurn(ctx, c.conversation.ID, providerTurnID, state, errMessage, now)
}

func (c *Controller) settleTurnByID(ctx context.Context, turnID, errMessage string, failure *domain.ConversationFailure, now time.Time) error {
	if failure == nil {
		fallback := terminalConversationFailure(domain.TurnStateFailed)
		failure = &fallback
	}
	if failure != nil {
		normalized := normalizeConversationFailure(*failure)
		if store, ok := c.store.(stableTurnFailureStore); ok {
			return store.SettleTurnByIDWithFailure(ctx, turnID, domain.TurnStateFailed, errMessage, normalized, now)
		}
	}
	return c.store.SettleTurnByID(ctx, turnID, domain.TurnStateFailed, errMessage, now)
}

func sendConversationFailure(err error) domain.ConversationFailure {
	if failure, ok := ports.ChatFailureFromError(err); ok {
		return normalizeConversationFailure(failure)
	}
	if failure, ok := typedConversationFailure(err); ok {
		return failure
	}
	return domain.ConversationFailure{
		Category:     domain.AgentFailureDeliveryUnknown,
		ErrorSummary: conversationFailureSummary(domain.AgentFailureDeliveryUnknown),
	}
}

func typedConversationFailure(err error) (domain.ConversationFailure, bool) {
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
	return domain.ConversationFailure{
		Category: category, ErrorSummary: conversationFailureSummary(category), Retryable: retryable,
	}, true
}

func terminalConversationFailure(state domain.TurnState) domain.ConversationFailure {
	category := domain.AgentFailureProvider
	if state == domain.TurnStateInterrupted {
		category = domain.AgentFailureTurnInterrupted
	}
	return domain.ConversationFailure{Category: category, ErrorSummary: conversationFailureSummary(category)}
}

func normalizeConversationFailure(failure domain.ConversationFailure) domain.ConversationFailure {
	if conversationFailureSummary(failure.Category) == "" {
		failure.Category = domain.AgentFailureProvider
	}
	failure.ProviderErrorCode = strings.TrimSpace(failure.ProviderErrorCode)
	if len(failure.ProviderErrorCode) > 200 {
		failure.ProviderErrorCode = failure.ProviderErrorCode[:200]
	}
	failure.ErrorSummary = conversationFailureSummary(failure.Category)
	if failure.Category == domain.AgentFailureDeliveryUnknown {
		failure.Retryable, failure.RetryAt = false, nil
	}
	return failure
}

func conversationFailureSummary(category domain.AgentFailureCategory) string {
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
		return "conversation controller lost the Agent session"
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
