package codexappserver

import (
	"encoding/json"
	"sort"
	"strconv"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// conversationFailureFromTurnError reads only the typed codexErrorInfo union.
// TurnError.Message is localized display text and never participates in the
// stable classification.
func conversationFailureFromTurnError(turnErr *codexproto.TurnError) *domain.ConversationFailure {
	if turnErr == nil || turnErr.CodexErrorInfo == nil {
		return nil
	}
	raw := *turnErr.CodexErrorInfo
	var code string
	if json.Unmarshal(raw, &code) == nil {
		return codexFailureForCode(code, 0)
	}
	var variants map[string]struct {
		HTTPStatusCode *int `json:"httpStatusCode"`
	}
	if json.Unmarshal(raw, &variants) != nil || len(variants) == 0 {
		return &domain.ConversationFailure{
			Category: domain.AgentFailureProvider, ErrorSummary: "provider reported an unclassified failure",
		}
	}
	keys := make([]string, 0, len(variants))
	for key := range variants {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	variant := variants[keys[0]]
	status := 0
	if variant.HTTPStatusCode != nil {
		status = *variant.HTTPStatusCode
	}
	return codexFailureForCode(keys[0], status)
}

func codexFailureForCode(code string, httpStatus int) *domain.ConversationFailure {
	failure := &domain.ConversationFailure{ProviderErrorCode: code}
	switch code {
	case "usageLimitExceeded", "sessionBudgetExceeded":
		failure.Category = domain.AgentFailureQuotaExhausted
		failure.ErrorSummary = "provider quota is exhausted"
	case "rateLimitExceeded":
		failure.Category = domain.AgentFailureRateLimited
		failure.ErrorSummary = "provider rate limit is active"
		failure.Retryable = true
	case "unauthorized":
		failure.Category = domain.AgentFailureAuthentication
		failure.ErrorSummary = "provider login is required"
	case "serverOverloaded", "internalServerError", "httpConnectionFailed",
		"responseStreamConnectionFailed", "responseStreamDisconnected", "responseTooManyFailedAttempts":
		failure.Category = domain.AgentFailureProviderUnavailable
		failure.ErrorSummary = "provider is unavailable"
		failure.Retryable = true
	default:
		failure.Category = domain.AgentFailureProvider
		failure.ErrorSummary = "provider reported an unclassified failure"
	}
	if httpStatus != 0 {
		failure.ProviderErrorCode += ":http_status_" + strconv.Itoa(httpStatus)
	}
	return failure
}
