package cleardev

import (
	"testing"
	"time"
)

func TestBuilderReplacementAttemptIsSpecificUnsentHandoff(t *testing.T) {
	now := time.Now().UTC()
	first := AgentStepAttempt{ID: "step:attempt:1", DevelopmentRequirementID: "req", LogicalStepID: "step", StepCategory: AgentStepCategoryComplexExecution, StepKind: ComplexExecutionAgentStepBuilderTask, AttemptNumber: 1, RoleBindingID: "old-role", AOSessionID: "old-session", ClientMessageID: "original", PromptSHA256: "prompt"}
	retired := AgentAttemptEvent{ID: "retired", AttemptID: first.ID, Status: AgentAttemptRetiredBeforeSend, ClientMessageID: first.ClientMessageID, PromptSHA256: first.PromptSHA256, RecordedAt: now}
	b := BuilderReplacementBinding{DevelopmentRequirementID: "req", LogicalStepID: "step", OldRoleBindingID: "old-role", OldAOSessionID: "old-session", NewRoleBindingID: "new-role", ClientMessageID: "original", PromptSHA256: "prompt"}
	h := BuilderReplacementHandoff{ID: "continue", RequestID: "request", DecisionRequestID: "decision", Binding: b, RetirementEventID: "retired", AttemptID: "step:attempt:2", NewAOSessionID: "new-session"}
	second := AgentStepAttempt{ID: h.AttemptID, DevelopmentRequirementID: "req", LogicalStepID: "step", StepCategory: first.StepCategory, StepKind: first.StepKind, AttemptNumber: 2, RoleBindingID: "new-role", AOSessionID: "new-session", ClientMessageID: "original:attempt:2", PromptSHA256: "prompt", TriggerFailureEventID: "retired", RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: AttemptTimeActualCreation}
	if !ValidBuilderReplacementAttempt(first, retired, second, h) {
		t.Fatal("accurate unsent handoff rejected")
	}
	if ValidSecondAgentAttempt(first, retired, retired, second) {
		t.Fatal("retirement leaked into provider-failure retry")
	}
	for _, change := range []string{"provider-failure", "provider-turn", "retryable", "wrong-message", "wrong-task", "same-session", "missing-grant", "third", "changed-prompt"} {
		t.Run(change, func(t *testing.T) {
			r, a, grant := retired, second, h
			switch change {
			case "provider-failure":
				r.Status = AgentAttemptFailed
			case "provider-turn":
				r.TurnID = "turn"
			case "retryable":
				r.Retryable = true
			case "wrong-message":
				r.ClientMessageID = "other"
			case "wrong-task":
				a.LogicalStepID = "other"
			case "same-session":
				a.AOSessionID = first.AOSessionID
				grant.NewAOSessionID = first.AOSessionID
			case "missing-grant":
				grant.DecisionRequestID = ""
			case "third":
				a.AttemptNumber = 3
			case "changed-prompt":
				a.PromptSHA256 = "other"
			}
			if ValidBuilderReplacementAttempt(first, r, a, grant) {
				t.Fatal("unrelated recovery accepted")
			}
		})
	}
}

func TestBuilderReplacementPhaseNeedsExactTransitionFacts(t *testing.T) {
	run := projectExecutionRunFixture(t)
	run.Decision = ComplexExecutionDecisionDispatch
	run.BuilderRoleBindingID = "step:replacement-builder"
	run.BuilderAOSessionID = "new"
	run.InitialBaseCommitSHA = "base"
	old := ComplexExecutionRoleBinding{ID: "old-role", Role: StandardRoleBuilder, Status: RoleBindingStatusEnded, ReasonCode: "BUILDER_REPLACED", BuilderSlot: 1, AOSessionID: "old"}
	next := ComplexExecutionRoleBinding{ID: "step:replacement-builder", Role: StandardRoleBuilder, Status: RoleBindingStatusBound, BuilderSlot: 1, AOSessionID: "new", ContinuationOfRoleBindingID: old.ID, SessionCreationIdempotencyKey: "step:replacement-create"}
	s := ComplexExecutionSnapshot{Run: run, RoleBindings: []ComplexExecutionRoleBinding{old, next}, AgentSteps: []AgentStep{{ID: "step", RoleBindingID: old.ID, Kind: ComplexExecutionAgentStepBuilderTask, RequestID: "dispatch"}}, Dispatches: []ComplexExecutionDispatch{{ID: "dispatch", AgentStepID: "step", BuilderRoleBindingID: old.ID}}, Tasks: []ComplexExecutionTask{{Status: DevelopmentTaskStatusRunning}}}
	if phase, _ := DeriveComplexExecutionPhase(s); phase != ComplexExecutionBlocked {
		t.Fatal("role reason alone bypassed old stop")
	}
	proof := BuilderReplacementTransition{HandoffID: "continue", DecisionRequestID: "exact-decision", LogicalStepID: "step", DispatchID: "dispatch", OldRoleBindingID: old.ID, NewRoleBindingID: next.ID, RetirementEventID: "step:replacement-retired", AttemptID: "step:attempt:2", NewAOSessionID: "new", AliasBound: true}
	s.BuilderReplacementTransitions = []BuilderReplacementTransition{proof}
	if phase, _ := DeriveComplexExecutionPhase(s); phase != ComplexExecutionBuilding {
		t.Fatalf("exact retired-role proof still blocked: %s", phase)
	}
	for _, change := range []string{"missing-grant", "wrong-dispatch", "no-alias", "wrong-new-session", "wrong-retirement"} {
		t.Run(change, func(t *testing.T) {
			bad := proof
			switch change {
			case "missing-grant":
				bad.DecisionRequestID = ""
			case "wrong-dispatch":
				bad.DispatchID = "other"
			case "no-alias":
				bad.AliasBound = false
			case "wrong-new-session":
				bad.NewAOSessionID = "other"
			case "wrong-retirement":
				bad.RetirementEventID = "other"
			}
			s.BuilderReplacementTransitions = []BuilderReplacementTransition{bad}
			if phase, _ := DeriveComplexExecutionPhase(s); phase != ComplexExecutionBlocked {
				t.Fatal("inexact proof bypassed old stop")
			}
		})
	}
}
