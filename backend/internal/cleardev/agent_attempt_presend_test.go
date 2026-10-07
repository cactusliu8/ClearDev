package cleardev

import (
	"strings"
	"testing"
	"time"
)

func TestPreSendProofRequiresBoundSecondAttempt(t *testing.T) {
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	first := AgentStepAttempt{ID: "first", DevelopmentRequirementID: "requirement", LogicalStepID: "step", StepCategory: AgentStepCategoryStandard, StepKind: AgentStepBuilderResult, AttemptNumber: 1, RoleBindingID: "builder", AOSessionID: "session", ClientMessageID: "original", PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: AttemptTimeActualCreation}
	proof := AgentAttemptEvent{ID: "proof", AttemptID: first.ID, Status: AgentAttemptSendStatus("FAILED_BEFORE_SEND"), ClientMessageID: first.ClientMessageID, PromptSHA256: first.PromptSHA256, Retryable: true, RecordedAt: now}
	second := first
	second.ID, second.AttemptNumber, second.ClientMessageID, second.TriggerFailureEventID = "second", 2, "original:attempt:2", proof.ID
	if !ValidSecondAgentAttempt(first, proof, proof, second) {
		t.Fatal("a proven local pre-send failure did not admit the one bound second attempt")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*AgentAttemptEvent, *AgentAttemptEvent, *AgentStepAttempt)
	}{
		{"turn", func(e, _ *AgentAttemptEvent, _ *AgentStepAttempt) { e.TurnID = "invented" }},
		{"turn state", func(e, _ *AgentAttemptEvent, _ *AgentStepAttempt) { e.TurnState = "failed" }},
		{"provider failure", func(e, _ *AgentAttemptEvent, _ *AgentStepAttempt) { e.FailureCategory = "PROVIDER_FAILURE" }},
		{"provider code", func(e, _ *AgentAttemptEvent, _ *AgentStepAttempt) { e.ProviderErrorCode = "invented" }},
		{"wrong message", func(e, _ *AgentAttemptEvent, _ *AgentStepAttempt) { e.ClientMessageID += ":parse-correction" }},
		{"wrong prompt", func(e, _ *AgentAttemptEvent, _ *AgentStepAttempt) { e.PromptSHA256 = strings.Repeat("b", 64) }},
		{"retry date", func(e, _ *AgentAttemptEvent, _ *AgentStepAttempt) { e.RetryAt = &now }},
		{"not retryable", func(e, _ *AgentAttemptEvent, _ *AgentStepAttempt) { e.Retryable = false }},
		{"unknown", func(e, _ *AgentAttemptEvent, _ *AgentStepAttempt) { e.Status = AgentAttemptDeliveryUnknown }},
		{"later evidence", func(_, latest *AgentAttemptEvent, _ *AgentStepAttempt) { latest.ID = "later" }},
		{"changed latest contents", func(_, latest *AgentAttemptEvent, _ *AgentStepAttempt) { latest.Status = AgentAttemptDeliveryUnknown }},
		{"other session", func(_, _ *AgentAttemptEvent, a *AgentStepAttempt) { a.AOSessionID = "other" }},
		{"other role", func(_, _ *AgentAttemptEvent, a *AgentStepAttempt) { a.RoleBindingID = "other" }},
		{"third attempt", func(_, _ *AgentAttemptEvent, a *AgentStepAttempt) { a.AttemptNumber = 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, latest, candidate := proof, proof, second
			tc.mutate(&e, &latest, &candidate)
			if ValidSecondAgentAttempt(first, e, latest, candidate) {
				t.Fatal("invalid proof or changed binding admitted another attempt")
			}
		})
	}
}

func TestPreSendProgressHonorsBudgetsPreflightAndBinding(t *testing.T) {
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	zero := int64(0)
	base := func() ControlledProgressFacts {
		sha := strings.Repeat("a", 64)
		return ControlledProgressFacts{Target: ControlledWork{LogicalStepID: "step", StepCategory: AgentStepCategoryComplexExecution, RoleBindingID: "role", AOSessionID: "session"}, Step: &AgentStep{ID: "step", RoleBindingID: "role", PromptSHA256: sha, SendStatus: AgentStepSendStatusPending}, BudgetVersion: MessageBudgetV1, Attempts: []AgentStepAttemptView{{ID: "first", LogicalStepID: "step", StepCategory: AgentStepCategoryComplexExecution, AttemptNumber: 1, RoleBindingID: "role", AOSessionID: "session", PromptSHA256: sha, ClientMessageID: "original", LastClientMessageID: "original", LastEventID: "no-send", SendStatus: AgentAttemptFailedBeforeSend, Retryable: true, LastObservedAt: &now}}}
	}
	second := func(f *ControlledProgressFacts) {
		a := f.Attempts[0]
		a.ID, a.AttemptNumber, a.ClientMessageID, a.LastClientMessageID, a.LastEventID, a.TriggerFailureEventID, a.SendStatus = "second", 2, "original:attempt:2", "", "", "no-send", AgentAttemptPending
		a.Retryable = false
		f.Attempts = append(f.Attempts, a)
	}
	for _, tc := range []struct {
		name, want string
		change     func(*ControlledProgressFacts)
	}{
		{"first", "RETRY_ELIGIBLE", func(*ControlledProgressFacts) {}},
		{"empty budget", "BUDGET_BLOCKED", func(f *ControlledProgressFacts) { f.Target.StepBudget = &MessageBudgetUsage{RemainingMessages: &zero} }},
		{"legacy budget", "BUDGET_BLOCKED", func(f *ControlledProgressFacts) { f.BudgetVersion = MessageBudgetLegacy }},
		{"login", "AWAITING_USER", func(f *ControlledProgressFacts) {
			f.Preflight = &ControlledPreflight{Outcome: ControlledPreflightFailed, ReasonCode: ReasonLoginRequired}
		}},
		{"bound second", "READY", second},
		{"wrong second session", "UNKNOWN", func(f *ControlledProgressFacts) { second(f); f.Attempts[1].AOSessionID = "other" }},
		{"contrary first turn", "UNKNOWN", func(f *ControlledProgressFacts) { second(f); f.Attempts[0].TurnID = "accepted" }},
		{"second local failure", "FAILED", func(f *ControlledProgressFacts) {
			second(f)
			a := &f.Attempts[1]
			a.LastClientMessageID = a.ClientMessageID
			a.LastEventID = "second-no-send"
			a.SendStatus = AgentAttemptFailedBeforeSend
			a.Retryable = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := base()
			tc.change(&f)
			w, active := deriveControlledWork(f, now)
			if !active || w.State != tc.want {
				t.Fatalf("state=%s active=%v want=%s reason=%s", w.State, active, tc.want, w.ReasonCode)
			}
		})
	}
}
