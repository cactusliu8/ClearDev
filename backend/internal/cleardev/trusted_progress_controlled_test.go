package cleardev

import (
	"testing"
	"time"
)

func TestClearDevControlledProgressHistoricalExecutionCannotCompleteCurrent(t *testing.T) {
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	facts := TrustedProgressFacts{Snapshot: RequirementSnapshot{Requirement: DevelopmentRequirement{ID: "req"}, RequirementVersions: []RequirementVersion{{ID: "current", Version: 2, Status: RequirementVersionStatusConfirmed, TaskSetVersion: 2}}, DevelopmentTasks: []DevelopmentTask{{ID: "task", RequirementVersionID: "current", Status: DevelopmentTaskStatusRunning}}}, QuickExecution: &ComplexQuickSnapshot{Run: ComplexQuickRun{ID: "old", RequirementVersionID: "old", CompletedAt: &now}}, Now: now}
	got := DeriveTrustedProgress(facts)
	if got.Phase == TrustedPhaseCompleted {
		t.Fatal("historical QUICK completed current work")
	}
}

func TestClearDevControlledProgressUnrelatedPreflightCannotBlockCurrent(t *testing.T) {
	facts := TrustedProgressFacts{Snapshot: RequirementSnapshot{Requirement: DevelopmentRequirement{ID: "req"}, RequirementVersions: []RequirementVersion{{ID: "current", Status: RequirementVersionStatusConfirmed}}, DevelopmentTasks: []DevelopmentTask{{ID: "task", RequirementVersionID: "current", Status: DevelopmentTaskStatusRunning}}}, LatestControlledPreflight: &ControlledPreflightView{RoleBindingID: "old-role", Outcome: ControlledPreflightFailed, ReasonCode: "LOGIN_REQUIRED"}, Now: time.Now().UTC()}
	got := DeriveTrustedProgress(facts)
	if got.Attention == OverallAttentionBlocked {
		t.Fatal("unrelated historical preflight blocked current work")
	}
}

func TestClearDevControlledProgressStatesAndTimeHash(t *testing.T) {
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	zero, two := int64(0), int64(2)
	base := func() ControlledProgressFacts {
		return ControlledProgressFacts{Target: ControlledWork{LogicalStepID: "step", StepCategory: AgentStepCategoryComplexExecution, RoleBindingID: "role", AOSessionID: "session"}, Step: &AgentStep{ID: "step", RoleBindingID: "role", PromptSHA256: "prompt", SendStatus: AgentStepSendStatusSent}, BudgetVersion: MessageBudgetV1, Attempts: []AgentStepAttemptView{{ID: "first", LogicalStepID: "step", StepCategory: AgentStepCategoryComplexExecution, AttemptNumber: 1, RoleBindingID: "role", AOSessionID: "session", PromptSHA256: "prompt", ClientMessageID: "msg", LastEventID: "failed", TurnID: "turn", TurnState: "failed", SendStatus: AgentAttemptFailed, Retryable: true, FailureCategory: "PROVIDER_UNAVAILABLE", RetryAt: &later}}}
	}
	for _, c := range []struct {
		name, want string
		change     func(*ControlledProgressFacts)
	}{
		{"future", "WAITING_RETRY", func(*ControlledProgressFacts) {}},
		{"unknown terminal", "UNKNOWN", func(f *ControlledProgressFacts) { f.Attempts[0].TurnState = "" }},
		{"replacement missing", "UNKNOWN", func(f *ControlledProgressFacts) { f.BindingUnknown = true }},
		{"correction rejected", "FAILED", func(f *ControlledProgressFacts) {
			f.Attempts[0].SendStatus = AgentAttemptCompleted
			f.Attempts[0].ParseConclusion = AgentResultParseInvalid
			f.Step.SendStatus = AgentStepSendStatusFailed
		}},
		{"eligible", "RETRY_ELIGIBLE", func(f *ControlledProgressFacts) { f.Attempts[0].RetryAt = nil }},
		{"unknown delivery zero", "DELIVERY_UNCONFIRMED", func(f *ControlledProgressFacts) {
			f.Attempts[0].SendStatus = AgentAttemptDeliveryUnknown
			f.Target.StepBudget = &MessageBudgetUsage{RemainingMessages: &zero}
		}},
		{"observing zero", "OBSERVING", func(f *ControlledProgressFacts) {
			f.Attempts[0].SendStatus = AgentAttemptObservationTimedOut
			f.Target.StepBudget = &MessageBudgetUsage{RemainingMessages: &zero}
		}},
		{"budget", "BUDGET_BLOCKED", func(f *ControlledProgressFacts) { f.Target.StepBudget = &MessageBudgetUsage{RemainingMessages: &zero} }},
		{"legacy", "BUDGET_BLOCKED", func(f *ControlledProgressFacts) { f.BudgetVersion = MessageBudgetLegacy }},
		{"login", "AWAITING_USER", func(f *ControlledProgressFacts) {
			f.Attempts = nil
			f.Step = nil
			f.Preflight = &ControlledPreflight{ID: "login", Outcome: ControlledPreflightFailed, ReasonCode: ReasonLoginRequired, Retryable: true, RetryAt: &later}
		}},
		{"quota", "WAITING_RETRY", func(f *ControlledProgressFacts) {
			f.Attempts = nil
			f.Step = nil
			f.Preflight = &ControlledPreflight{ID: "quota", Outcome: ControlledPreflightFailed, ReasonCode: ReasonQuotaExhausted, RetryAt: &later}
		}},
		{"quota unknown time", "AWAITING_USER", func(f *ControlledProgressFacts) {
			f.Attempts = nil
			f.Step = nil
			f.Preflight = &ControlledPreflight{Outcome: ControlledPreflightFailed, ReasonCode: ReasonQuotaExhausted}
		}},
		{"unavailable", "RETRY_ELIGIBLE", func(f *ControlledProgressFacts) {
			f.Attempts = nil
			f.Step = nil
			f.Preflight = &ControlledPreflight{Outcome: ControlledPreflightFailed, ReasonCode: ReasonProviderUnavailable}
		}},
		{"final", "FAILED", func(f *ControlledProgressFacts) { f.Attempts[0].Retryable = false }},
		{"missing", "UNKNOWN", func(f *ControlledProgressFacts) { f.Attempts = nil }},
		{"claimed", "RECOVERY_UNCONFIRMED", func(f *ControlledProgressFacts) {
			f.Recovery = &FixedRecoveryEvidence{Request: FixedRecoveryRequest{ID: "recovery"}, Claim: &FixedRecoveryClaim{OperationID: "op"}}
		}},
		{"actual failed", "RECOVERY_FAILED", func(f *ControlledProgressFacts) {
			f.Recovery = &FixedRecoveryEvidence{Result: &FixedRecoveryResult{Outcome: "FAIL"}}
		}},
		{"pending", "RECOVERY_PENDING", func(f *ControlledProgressFacts) {
			f.Recovery = &FixedRecoveryEvidence{Request: FixedRecoveryRequest{ID: "recovery"}}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := base()
			c.change(&f)
			w, active := deriveControlledWork(f, now)
			if !active || w.State != c.want {
				t.Fatalf("got %+v active=%v", w, active)
			}
		})
	}
	f := base()
	facts := TrustedProgressFacts{Now: now, Controlled: []ControlledProgressFacts{f}}
	first := DeriveTrustedProgress(facts)
	facts.Now = now.Add(time.Minute)
	if first.FactSummarySHA256 != DeriveTrustedProgress(facts).FactSummarySHA256 {
		t.Fatal("countdown entered hash")
	}
	facts.Now = later
	if first.FactSummarySHA256 == DeriveTrustedProgress(facts).FactSummarySHA256 {
		t.Fatal("eligible boundary absent from hash")
	}
	f.Target.StepBudget = &MessageBudgetUsage{RemainingMessages: &two}
	facts.Controlled = []ControlledProgressFacts{f}
	if first.FactSummarySHA256 == DeriveTrustedProgress(facts).FactSummarySHA256 {
		t.Fatal("budget absent from hash")
	}
}

func TestClearDevControlledProgressParallelPreflightAndHistory(t *testing.T) {
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	e := ComplexExecutionSnapshot{Run: ComplexExecutionRun{ID: "run", RequirementVersionID: "v", TaskSetVersion: 2}, RoleBindings: []ComplexExecutionRoleBinding{{ID: "a", Role: StandardRoleBuilder, Status: RoleBindingStatusRequested}, {ID: "b", Role: StandardRoleBuilder, Status: RoleBindingStatusRequested}, {ID: "ended", Role: StandardRoleReviewer, Status: RoleBindingStatusEnded}}}
	f := TrustedProgressFacts{Snapshot: RequirementSnapshot{RequirementVersions: []RequirementVersion{{ID: "v", Status: RequirementVersionStatusConfirmed, TaskSetVersion: 2}}}, ComplexExecution: &e, Now: now}
	targets := CurrentControlledTargets(f)
	if len(targets) != 2 {
		t.Fatalf("pending role targets=%+v", targets)
	}
	for i := range targets {
		targets[i].BudgetVersion = MessageBudgetV1
		targets[i].Preflight = &ControlledPreflight{ID: targets[i].Target.RoleBindingID + "-preflight", RoleBindingID: targets[i].Target.RoleBindingID, Outcome: ControlledPreflightFailed, ReasonCode: ReasonLoginRequired}
	}
	targets[0].Preflight.Outcome = ControlledPreflightPassed
	f.Controlled = targets
	first := DeriveTrustedProgress(f)
	if first.Phase != TrustedPhaseBlocked {
		t.Fatal("other role cleared login block")
	}
	found := false
	for _, w := range first.ControlledWork {
		if w.RoleBindingID == "b" && w.State == "AWAITING_USER" {
			found = true
		}
	}
	if !found {
		t.Fatal("per-role preflight lost")
	}
	targets[0], targets[1] = targets[1], targets[0]
	f.Controlled = targets
	if first.FactSummarySHA256 != DeriveTrustedProgress(f).FactSummarySHA256 {
		t.Fatal("input ordering changed hash")
	}
	// A historical QUICK from another source cannot displace this current run.
	f.QuickExecution = &ComplexQuickSnapshot{Run: ComplexQuickRun{ID: "old-quick", RequirementVersionID: "v", TaskSetVersion: 2, SourceExecutionRunID: "old-run", CompletedAt: &now}}
	selected := SelectTrustedProgressFacts(f)
	if selected.ComplexExecution == nil || selected.QuickExecution != nil {
		t.Fatal("historical quick displaced current execution")
	}
	f.QuickExecution.Run.SourceExecutionRunID = "run"
	if selected = SelectTrustedProgressFacts(f); selected.QuickExecution == nil || selected.ComplexExecution != nil {
		t.Fatal("explicit successor not selected")
	}
	f.Snapshot.RequirementVersions = append(f.Snapshot.RequirementVersions, RequirementVersion{ID: "pending", Version: 2, Status: RequirementVersionStatusDraft})
	if got := DeriveTrustedProgress(f); got.Phase == TrustedPhaseCompleted {
		t.Fatal("old completion displaced pending direction")
	}
}

func TestClearDevFailedPlanningRoleIsCurrentBlocker(t *testing.T) {
	f := TrustedProgressFacts{
		Snapshot: RequirementSnapshot{Requirement: DevelopmentRequirement{ID: "requirement"}},
		ComplexPlanning: &ComplexPlanningSnapshot{RoleBindings: []ComplexRoleBinding{{
			ID: "steward", Role: StandardRoleSteward, Status: RoleBindingStatusFailed,
			ReasonCode: "STEWARD_UNAVAILABLE",
		}}},
		Now: time.Now().UTC(),
	}
	f.Controlled = CurrentControlledTargets(f)
	if len(f.Controlled) != 1 || f.Controlled[0].Target.State != "FAILED" {
		t.Fatalf("failed planning target = %+v", f.Controlled)
	}
	f.Controlled[0].BudgetVersion = MessageBudgetV1
	got := DeriveTrustedProgress(f)
	if got.Phase != TrustedPhaseBlocked || len(got.Blockers) != 1 || got.Blockers[0].ReasonCode != "STEWARD_UNAVAILABLE" || got.ControlledWork[0].State != "FAILED" {
		t.Fatalf("failed planning role was hidden: %+v", got)
	}
}

func TestClearDevControlledProgressCompletionRequiresCurrentEvidence(t *testing.T) {
	now := time.Now().UTC()
	e := ComplexExecutionSnapshot{Run: ComplexExecutionRun{ID: "run", CompletedAt: &now}, Integration: &ComplexExecutionIntegration{ExecutionRunID: "run"}}
	phase, _, missing := CurrentExecutionProgress(e, nil, now)
	if phase == ComplexExecutionCompleted || len(missing) == 0 {
		t.Fatal("timestamp completed without tasks/checks/review")
	}
	q := ComplexQuickSnapshot{Run: ComplexQuickRun{ID: "quick", CompletedAt: &now}}
	phase, _, missing = CurrentQuickProgress(q)
	if phase == ComplexExecutionCompleted || len(missing) == 0 {
		t.Fatal("quick timestamp completed without evidence")
	}
}

func TestClearDevControlledProgressDirectionExcludesOldExecutionTargets(t *testing.T) {
	f := TrustedProgressFacts{Snapshot: RequirementSnapshot{RequirementVersions: []RequirementVersion{{ID: "v", Status: RequirementVersionStatusConfirmed}}}, ComplexPlanning: &ComplexPlanningSnapshot{RoleBindings: []ComplexRoleBinding{{ID: "steward", Role: StandardRoleSteward, Status: RoleBindingStatusBound, AOSessionID: "session"}}, AgentSteps: []AgentStep{{ID: "old-planning", RoleBindingID: "steward", SendStatus: AgentStepSendStatusFailed}}}, ComplexExecution: &ComplexExecutionSnapshot{Run: ComplexExecutionRun{ID: "run", RequirementVersionID: "v"}, RoleBindings: []ComplexExecutionRoleBinding{{ID: "old-builder", Role: StandardRoleBuilder, Status: RoleBindingStatusRequested}}}, DirectionChange: &DirectionChangeSnapshot{Intent: &DirectionIntent{RequestID: "direction", RequirementVersionID: "v"}, AgentSteps: []AgentStep{{ID: "direction-step", RoleBindingID: "steward", SendStatus: AgentStepSendStatusPending}}}}
	targets := CurrentControlledTargets(f)
	if len(targets) != 1 || targets[0].Target.LogicalStepID != "direction-step" {
		t.Fatalf("old work entered active direction: %+v", targets)
	}
}

func TestClearDevControlledProgressReplacementPassIsNotMissingReview(t *testing.T) {
	e := ComplexExecutionSnapshot{Run: ComplexExecutionRun{ID: "run", Decision: ComplexExecutionDecisionDispatch, Mode: WorkModeStandard, BuilderAOSessionID: "builder", InitialBaseCommitSHA: "base"}, Tasks: []ComplexExecutionTask{{ID: "task", Status: DevelopmentTaskStatusReview, CurrentDispatchID: "dispatch"}}, Dispatches: []ComplexExecutionDispatch{{ID: "dispatch", ComplexExecutionTaskID: "task", CandidateCommitID: "candidate", CandidateCommitSHA: "sha"}}, Reviews: []ComplexExecutionReview{{ID: "review", DispatchID: "dispatch", CandidateCommitID: "candidate", CandidateCommitSHA: "sha", AgentStepID: "step"}}, FixedRecoveries: []FixedRecoveryEvidence{{Request: FixedRecoveryRequest{ID: "recovery", ReviewID: "review", DispatchID: "dispatch", CandidateID: "candidate", CandidateSHA: "sha", LogicalStepID: "step"}, Claim: &FixedRecoveryClaim{Action: ComplexRecoveryActionRebuildReviewer}, Result: &FixedRecoveryResult{Outcome: "PASS"}, ReviewResult: &ReplacementReviewResult{RecoveryRequestID: "recovery", OriginalReviewID: "review", AttemptID: "second", ResultID: "result", Verdict: LocalReviewPass}}}}
	_, _, missing := CurrentExecutionProgress(e, nil, time.Now().UTC())
	for _, kind := range missing {
		if kind == "REVIEWER_PASS" {
			t.Fatal("persisted replacement PASS still reported as missing review")
		}
	}
	if e.Reviews[0].Verdict != "" {
		t.Fatal("read projection overwrote original review")
	}
}
