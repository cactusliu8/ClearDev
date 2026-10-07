package cleardev

import (
	"sort"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func mergeLegacyAgentAttempts(view RequirementView, stored []core.AgentStepAttemptView, progressRows []core.ProgressExplanationRequest) []core.AgentStepAttemptView {
	result := append([]core.AgentStepAttemptView(nil), stored...)
	seen := make(map[string]bool, len(stored))
	for _, attempt := range stored {
		seen[attempt.LogicalStepID] = true
	}
	sessions := legacyRoleSessions(view)
	appendSteps := func(category core.AgentStepCategory, steps []core.AgentStep) {
		for _, step := range steps {
			if seen[step.ID] {
				continue
			}
			actualCategory := category
			if category == core.AgentStepCategoryComplexExecution && isExceptionStepKind(step.Kind) {
				actualCategory = core.AgentStepCategoryException
			}
			result = append(result, legacyAgentAttempt(step, actualCategory, sessions[step.RoleBindingID]))
			seen[step.ID] = true
		}
	}
	if view.StandardFlow != nil {
		appendSteps(core.AgentStepCategoryStandard, view.StandardFlow.AgentSteps)
	}
	if view.ComplexPlanning != nil {
		appendSteps(core.AgentStepCategoryComplexPlanning, view.ComplexPlanning.AgentSteps)
	}
	if view.DirectionChange != nil {
		appendSteps(core.AgentStepCategoryDirection, view.DirectionChange.AgentSteps)
	}
	if view.ComplexExecution != nil {
		appendSteps(core.AgentStepCategoryComplexExecution, view.ComplexExecution.AgentSteps)
	}
	if view.QuickExecution != nil {
		appendSteps(core.AgentStepCategoryQuickExecution, view.QuickExecution.AgentSteps)
	}
	for _, row := range progressRows {
		if seen[row.ID] {
			continue
		}
		result = append(result, legacyProgressAttempt(row))
		seen[row.ID] = true
	}
	if explanation := view.TrustedProgress.Explanation; explanation != nil && !seen[explanation.RequestID] {
		result = append(result, legacyProgressAttempt(core.ProgressExplanationRequest{
			ID: explanation.RequestID, Status: explanation.Status,
		}))
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].RequestedAt == nil || result[j].RequestedAt == nil {
			return result[i].RequestedAt != nil
		}
		if result[i].RequestedAt.Equal(*result[j].RequestedAt) {
			return result[i].LogicalStepID < result[j].LogicalStepID
		}
		return result[i].RequestedAt.Before(*result[j].RequestedAt)
	})
	if result == nil {
		return []core.AgentStepAttemptView{}
	}
	return result
}

func legacyProgressAttempt(row core.ProgressExplanationRequest) core.AgentStepAttemptView {
	requestedAt := row.CreatedAt
	attempt := core.AgentStepAttemptView{
		LogicalStepID: row.ID, StepCategory: core.AgentStepCategoryProgress,
		StepKind: core.AgentStepStatusReport, AttemptNumber: 1,
		AOSessionID: row.AOSessionID, ClientMessageID: row.ClientMessageID,
		PromptSHA256: row.PromptSHA256, SendStatus: legacyProgressStatus(row.Status),
		ParseConclusion: legacyProgressParse(row.Status), LegacyEvidenceMissing: true, RequestedAtSemantics: core.AttemptTimeUnknown,
		RequestedAt: &requestedAt, Results: []core.AgentStepResultView{},
	}
	if row.SentAt != nil {
		attempt.LastObservedAt = row.SentAt
	}
	if row.Status == core.ProgressExplanationSettled {
		attempt.TurnState = domain.TurnStateCompleted
		attempt.RawMessageSHA256 = row.ResultSHA256
		attempt.Results = append(attempt.Results, core.AgentStepResultView{
			ResultIndex: 1, Source: core.AgentResultOriginal,
			ClientMessageID: row.ClientMessageID, RawMessageText: row.ResultJSON,
			RawMessageSHA256: row.ResultSHA256, ParseConclusion: core.AgentResultParseValid,
			ObservedAt: row.SettledAt,
		})
		attempt.LastObservedAt = row.SettledAt
	}
	if row.Status == core.ProgressExplanationFailed {
		attempt.TurnState = domain.TurnStateFailed
		attempt.ErrorSummary = "旧数据未保存稳定失败分类和原始结果"
		attempt.LastObservedAt = row.SettledAt
	}
	return attempt
}

func legacyAgentAttempt(step core.AgentStep, category core.AgentStepCategory, sessionID string) core.AgentStepAttemptView {
	requestedAt := step.RequestedAt
	attempt := core.AgentStepAttemptView{
		LogicalStepID: step.ID, StepCategory: category, StepKind: step.Kind,
		AttemptNumber: 1, RoleBindingID: step.RoleBindingID, AOSessionID: sessionID,
		ClientMessageID: step.ClientMessageID, PromptSHA256: step.PromptSHA256,
		SendStatus: legacyStepStatus(step.SendStatus), ParseConclusion: core.AgentResultParseUnparsed,
		LegacyEvidenceMissing: true, RequestedAtSemantics: core.AttemptTimeUnknown, RequestedAt: &requestedAt, Results: []core.AgentStepResultView{},
	}
	if step.SendStatus == core.AgentStepSendStatusSettled {
		attempt.TurnID, attempt.TurnState = step.TurnID, domain.TurnStateCompleted
		attempt.RawMessageSHA256, attempt.ParseConclusion = step.MessageSHA256, core.AgentResultParseValid
		attempt.Results = append(attempt.Results, core.AgentStepResultView{
			ResultIndex: 1, Source: core.AgentResultOriginal, ClientMessageID: step.ClientMessageID,
			TurnID: step.TurnID, FinalMessageID: step.FinalMessageID,
			RawMessageText: step.FinalMessageText, RawMessageSHA256: step.MessageSHA256,
			ParseConclusion: core.AgentResultParseValid, ObservedAt: step.CompletedAt,
		})
		attempt.LastObservedAt = step.CompletedAt
	}
	if step.SendStatus == core.AgentStepSendStatusFailed {
		attempt.TurnState = domain.TurnStateFailed
		attempt.LastObservedAt = step.FailedAt
		attempt.ErrorSummary = "旧数据未保存稳定失败分类和原始结果"
	}
	return attempt
}

func legacyStepStatus(status core.AgentStepSendStatus) core.AgentAttemptSendStatus {
	switch status {
	case core.AgentStepSendStatusPending:
		return core.AgentAttemptPending
	case core.AgentStepSendStatusSent:
		return core.AgentAttemptSent
	case core.AgentStepSendStatusSettled:
		return core.AgentAttemptCompleted
	case core.AgentStepSendStatusFailed:
		return core.AgentAttemptFailed
	default:
		return core.AgentAttemptPending
	}
}

func legacyProgressStatus(status string) core.AgentAttemptSendStatus {
	switch status {
	case core.ProgressExplanationSent:
		return core.AgentAttemptSent
	case core.ProgressExplanationSettled:
		return core.AgentAttemptCompleted
	case core.ProgressExplanationFailed:
		return core.AgentAttemptFailed
	default:
		return core.AgentAttemptPending
	}
}

func legacyProgressParse(status string) core.AgentResultParseConclusion {
	if status == core.ProgressExplanationSettled {
		return core.AgentResultParseValid
	}
	return core.AgentResultParseUnparsed
}

func isExceptionStepKind(kind core.AgentStepKind) bool {
	switch kind {
	case core.ComplexExceptionAgentStepScopeDecision, core.ComplexExceptionAgentStepSpecialist,
		core.ComplexExceptionAgentStepRecovery, core.ComplexExceptionAgentStepBuilderContinue:
		return true
	default:
		return false
	}
}

func legacyRoleSessions(view RequirementView) map[string]string {
	sessions := map[string]string{}
	if view.StandardFlow != nil {
		for _, binding := range view.StandardFlow.RoleBindings {
			sessions[binding.ID] = binding.AOSessionID
		}
	}
	if view.ComplexPlanning != nil {
		for _, binding := range view.ComplexPlanning.RoleBindings {
			sessions[binding.ID] = binding.AOSessionID
		}
	}
	if view.ComplexExecution != nil {
		for _, binding := range view.ComplexExecution.RoleBindings {
			sessions[binding.ID] = binding.AOSessionID
		}
	}
	if view.QuickExecution != nil {
		for _, binding := range view.QuickExecution.RoleBindings {
			sessions[binding.ID] = binding.AOSessionID
		}
	}
	return sessions
}
