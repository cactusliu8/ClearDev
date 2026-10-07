package cleardev

import (
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestMergeLegacyAgentAttemptsUsesOnlySavedEvidence(t *testing.T) {
	now := time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC)
	completed := now.Add(time.Minute)
	failed := now.Add(2 * time.Minute)
	view := RequirementView{
		StandardFlow: &core.StandardFlowSnapshot{
			RoleBindings: []core.RoleSessionBinding{{ID: "planner", AOSessionID: "session-planner"}},
			AgentSteps: []core.AgentStep{
				{
					ID: "settled", RoleBindingID: "planner", Kind: core.AgentStepEngineeringPlan,
					ClientMessageID: "message-settled", PromptSHA256: strings.Repeat("a", 64),
					SendStatus: core.AgentStepSendStatusSettled, RequestedAt: now,
					TurnID: "turn-settled", FinalMessageID: "final-settled", FinalMessageText: `{"kind":"ENGINEERING_PLAN"}`,
					MessageSHA256: strings.Repeat("b", 64), CompletedAt: &completed,
				},
				{
					ID: "failed", RoleBindingID: "planner", Kind: core.AgentStepPlanReview,
					ClientMessageID: "message-failed", PromptSHA256: strings.Repeat("c", 64),
					SendStatus: core.AgentStepSendStatusFailed, RequestedAt: now.Add(time.Second), FailedAt: &failed,
				},
			},
		},
	}
	attempts := mergeLegacyAgentAttempts(view, nil, nil)
	if len(attempts) != 2 {
		t.Fatalf("legacy attempts = %#v", attempts)
	}
	settled := attempts[0]
	if !settled.LegacyEvidenceMissing || settled.AttemptNumber != 1 || settled.AOSessionID != "session-planner" ||
		settled.SendStatus != core.AgentAttemptCompleted || settled.TurnState != domain.TurnStateCompleted ||
		len(settled.Results) != 1 || settled.Results[0].RawMessageText == "" || settled.Results[0].ParseConclusion != core.AgentResultParseValid {
		t.Fatalf("settled legacy attempt = %#v", settled)
	}
	failedAttempt := attempts[1]
	if !failedAttempt.LegacyEvidenceMissing || failedAttempt.SendStatus != core.AgentAttemptFailed ||
		failedAttempt.FailureCategory != "" || len(failedAttempt.Results) != 0 || failedAttempt.ErrorSummary == "" {
		t.Fatalf("failed legacy attempt fabricated evidence: %#v", failedAttempt)
	}
}

func TestMergeLegacyAgentAttemptsIncludesAllProgressExplanations(t *testing.T) {
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	settled := now.Add(time.Minute)
	rows := []core.ProgressExplanationRequest{
		{ID: "progress-settled", AOSessionID: "session-steward", ClientMessageID: "message-progress",
			PromptSHA256: strings.Repeat("d", 64), Status: core.ProgressExplanationSettled,
			ResultJSON: `{"summary":"ready"}`, ResultSHA256: strings.Repeat("e", 64),
			CreatedAt: now, SettledAt: &settled},
		{ID: "progress-failed", Status: core.ProgressExplanationFailed, CreatedAt: now.Add(2 * time.Minute), SettledAt: &settled},
	}
	attempts := mergeLegacyAgentAttempts(RequirementView{}, nil, rows)
	if len(attempts) != 2 {
		t.Fatalf("legacy progress attempts = %#v", attempts)
	}
	if attempts[0].AOSessionID != "session-steward" || attempts[0].RawMessageSHA256 == "" ||
		len(attempts[0].Results) != 1 || attempts[0].Results[0].RawMessageText == "" {
		t.Fatalf("settled legacy progress attempt = %#v", attempts[0])
	}
	if attempts[1].FailureCategory != "" || attempts[1].ErrorSummary == "" || len(attempts[1].Results) != 0 {
		t.Fatalf("failed legacy progress attempt fabricated evidence: %#v", attempts[1])
	}
}
