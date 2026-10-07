package cleardev

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func blockerProjectionFixture() (RequirementView, core.WorkflowRecoveryView) {
	at := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	failure := at.Add(-time.Minute)
	exit := 1
	view := RequirementView{
		Requirement: core.DevelopmentRequirement{ID: "requirement"},
		TrustedProgress: core.TrustedProgressSummary{
			Phase: core.TrustedPhaseBlocked,
			Tasks: []core.TrustedTaskItem{{DevelopmentTaskID: "task", Title: "Keep existing work"}},
		},
		ComplexExecution: &core.ComplexExecutionSnapshot{
			Run:   core.ComplexExecutionRun{ID: "run"},
			Tasks: []core.ComplexExecutionTask{{ID: "mapping", DevelopmentTaskID: "task", CurrentDispatchID: "current"}},
			Dispatches: []core.ComplexExecutionDispatch{
				{ID: "previous", ComplexExecutionTaskID: "mapping", Round: 0, CandidateCommitSHA: "candidate-a"},
				{ID: "current", ComplexExecutionTaskID: "mapping", Round: 1, AgentStepID: "pending-step", BuilderRoleBindingID: "builder", Status: core.ComplexExecutionDispatchBlocked, SettledAt: &at},
			},
			AgentSteps:   []core.AgentStep{{ID: "pending-step", SendStatus: core.AgentStepSendStatusPending}},
			RoleBindings: []core.ComplexExecutionRoleBinding{{ID: "builder", AOSessionID: "original", WorkspacePath: "/original/work"}},
			CheckRuns: []core.ComplexExecutionCheckRun{
				{ID: "older-infra", DispatchID: "previous", CandidateCommitSHA: "candidate-a", CheckSpecFactID: "verify", Status: core.ComplexExecutionCheckRunFailed, ReasonCode: "CHECKER_UNAVAILABLE", CreatedAt: failure.Add(-time.Minute), OutputSummary: "old preparation failure"},
				{ID: "failed-check", DispatchID: "previous", CandidateCommitSHA: "candidate-a", CheckSpecFactID: "verify", Status: core.ComplexExecutionCheckRunSettled, Result: core.EvidenceResultFail, ReasonCode: "CHECK_FAILED", Argv: []string{"npm", "run", "verify"}, CreatedAt: failure, SettledAt: &failure, ExitCode: &exit, OutputSummary: "mkdir /workspace/node_modules/better-sqlite3/build/Release failed"},
				{ID: "other-task", DispatchID: "unrelated", CandidateCommitSHA: "unrelated-sha", CheckSpecFactID: "verify", Status: core.ComplexExecutionCheckRunFailed, CreatedAt: at, OutputSummary: "unrelated failure"},
			},
		},
	}
	recovery := core.WorkflowRecoveryView{ExecutionRunID: "run", Options: []core.WorkflowRecoveryOption{{Action: core.RecoveryRetryBuilderSession, TargetID: core.WorkflowBuilderRetryTarget("current", at), TaskID: "mapping", Role: "BUILDER", Reason: "BUILDER_SPAWN_FAILED"}}}
	return view, recovery
}

func TestWorkflowBlockerDiagnosisSentObservationIsNotCurrentFailure(t *testing.T) {
	for _, reason := range []core.ReasonCode{"SENT", "CORRECTION_SENT"} {
		t.Run(string(reason), func(t *testing.T) {
			one := int64(1)
			view := RequirementView{Requirement: core.DevelopmentRequirement{ID: "running"},
				TrustedProgress: core.TrustedProgressSummary{Phase: core.TrustedPhaseDeveloping, ReasonCode: reason,
					ControlledWork: []core.ControlledWork{{State: "OBSERVING", ReasonCode: reason, LogicalStepID: "step", AttemptID: "attempt", AttemptNumber: &one, RoleBindingID: "builder", AOSessionID: "original", TurnID: "known-turn", EvidenceID: "sent-event"}}},
				AgentStepAttempts: []core.AgentStepAttemptView{{ID: "attempt", LogicalStepID: "step", AttemptNumber: 1, RoleBindingID: "builder", AOSessionID: "original", LastEventID: "sent-event"}}}
			diagnosis := workflowDiagnosisFromFacts(view, core.WorkflowRecoveryView{}, time.Now())
			if !diagnosis.Current || diagnosis.ReadError != "" || len(diagnosis.Issues) != 0 {
				t.Fatalf("normal observed turn was shown as a current blocker: %+v", diagnosis)
			}
			view.TrustedProgress.Blockers = []core.TrustedIssue{{Kind: "TASK_BLOCKED", ReasonCode: "REWORK_LIMIT_REACHED", SubjectID: "other-task"}}
			diagnosis = workflowDiagnosisFromFacts(view, core.WorkflowRecoveryView{}, time.Now())
			if len(diagnosis.Issues) != 1 || diagnosis.Issues[0].ReasonCode != "REWORK_LIMIT_REACHED" {
				t.Fatal("observing one turn hid a real other blocker", diagnosis)
			}
			view.TrustedProgress.Blockers = nil
			view.TrustedProgress.ControlledWork[0].TurnID = ""
			diagnosis = workflowDiagnosisFromFacts(view, core.WorkflowRecoveryView{}, time.Now())
			if len(diagnosis.Issues) == 0 {
				t.Fatal("missing original turn evidence was hidden", diagnosis)
			}
			view.TrustedProgress.ControlledWork[0].TurnID = "known-turn"
			view.TrustedProgress.ControlledWork[0].State = "UNKNOWN"
			diagnosis = workflowDiagnosisFromFacts(view, core.WorkflowRecoveryView{}, time.Now())
			if len(diagnosis.Issues) == 0 {
				t.Fatal("unknown execution was hidden", diagnosis)
			}
			view.TrustedProgress.ControlledWork[0].State = "OBSERVING"
			view.AgentStepAttempts[0].LastEventID = "changed-event"
			diagnosis = workflowDiagnosisFromFacts(view, core.WorkflowRecoveryView{}, time.Now())
			if diagnosis.Current || diagnosis.ReadError != "CURRENT_BINDINGS_CHANGED" {
				t.Fatal("routine observation skipped stale binding detection", diagnosis)
			}
		})
	}
}

func TestWorkflowBlockerDiagnosisSeparatesPrecedingCheckFromCurrentStop(t *testing.T) {
	view, recovery := blockerProjectionFixture()
	diagnosis := workflowDiagnosisFromFacts(view, recovery, time.Now())
	if !diagnosis.Current || len(diagnosis.Issues) != 2 {
		t.Fatalf("expected exact current blocker and one preceding failure: %+v", diagnosis)
	}
	first, previous := diagnosis.Issues[0], diagnosis.Issues[1]
	if first.ReasonCode != "BUILDER_SPAWN_FAILED" || first.Relationship != "CURRENT" || previous.SubjectID != "failed-check" || previous.Relationship != "PRECEDING_FAILURE" || previous.Category != "CHECK_FAILURE" {
		t.Fatalf("mixed task or historical cause: %+v", diagnosis.Issues)
	}
	for _, fact := range first.Evidence {
		if fact.Kind == "CHECK_OUTPUT" {
			t.Fatal("old candidate output was called a current step failure")
		}
	}
	for i := range view.ComplexExecution.CheckRuns {
		if view.ComplexExecution.CheckRuns[i].ID == "failed-check" {
			view.ComplexExecution.CheckRuns[i].Result = core.EvidenceResultPass
		}
	}
	diagnosis = workflowDiagnosisFromFacts(view, recovery, time.Now())
	if len(diagnosis.Issues) != 1 {
		t.Fatal("a later successful check must supersede an older infra failure for the same candidate/spec")
	}
}

func TestWorkflowBlockerDiagnosisRejectsChangedCurrentTarget(t *testing.T) {
	view, recovery := blockerProjectionFixture()
	recovery.Options[0].TargetID = "a-different-old-dispatch:resume:old-time"
	diagnosis := workflowDiagnosisFromFacts(view, recovery, time.Now())
	if diagnosis.Current || diagnosis.ReadError != "CURRENT_BINDINGS_CHANGED" || len(diagnosis.Issues) != 0 {
		t.Fatal("new pending-step evidence attached to an old operation")
	}
}

func TestWorkflowBlockerDiagnosisBindsRegisteredRecheckToOriginalStep(t *testing.T) {
	view, recovery := blockerProjectionFixture()
	view.ComplexExecution.Dispatches[1].SettledAt = nil
	view.ComplexExecution.Dispatches[1].Status = core.ComplexExecutionDispatchRunning
	recovery.Options[0].UnavailableReason = "BUILDER_RECHECK_REGISTERED"
	registered := core.WorkflowRecovery{ID: "registered", ExecutionRunID: "run", Action: core.RecoveryRetryBuilderSession,
		TargetID: recovery.Options[0].TargetID, TaskID: "mapping", DispatchID: "current", StepID: "pending-step", BindingID: "builder"}
	view.ComplexExecution.WorkflowRecoveries = []core.WorkflowRecovery{registered}
	diagnosis := workflowDiagnosisFromFacts(view, recovery, time.Now())
	if !diagnosis.Current || diagnosis.ReadError != "" {
		t.Fatalf("the original registered step lost its current diagnosis: %+v", diagnosis)
	}
	for _, field := range []string{"target", "run", "task", "dispatch", "step", "binding"} {
		t.Run(field, func(t *testing.T) {
			changed := registered
			switch field {
			case "target":
				changed.TargetID = "another-stop"
			case "run":
				changed.ExecutionRunID = "another-run"
			case "task":
				changed.TaskID = "another-task"
			case "dispatch":
				changed.DispatchID = "another-dispatch"
			case "step":
				changed.StepID = "another-step"
			case "binding":
				changed.BindingID = "another-binding"
			}
			view.ComplexExecution.WorkflowRecoveries = []core.WorkflowRecovery{changed}
			diagnosis := workflowDiagnosisFromFacts(view, recovery, time.Now())
			if diagnosis.Current || diagnosis.ReadError != "CURRENT_BINDINGS_CHANGED" {
				t.Fatalf("a different %s was attached to the registered step: %+v", field, diagnosis)
			}
		})
	}
}

func TestWorkflowBlockerDiagnosisDoesNotHideOtherBlockedTask(t *testing.T) {
	view, recovery := blockerProjectionFixture()
	view.TrustedProgress.Blockers = []core.TrustedIssue{
		{Kind: "TASK_BLOCKED", SubjectType: "DEVELOPMENT_TASK", SubjectID: "task"},
		{Kind: "TASK_BLOCKED", SubjectType: "DEVELOPMENT_TASK", SubjectID: "another-task"},
	}
	diagnosis := workflowDiagnosisFromFacts(view, recovery, time.Now())
	found := false
	for _, issue := range diagnosis.Issues {
		if issue.ReasonCode == "TASK_BLOCKED" {
			if issue.SubjectID != "another-task" {
				t.Fatal("same task's explained blocker was duplicated")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("one task's recoverable stop hid another task's unexplained blocker")
	}
}

func TestWorkflowBlockerDiagnosisExplainsNoOptionAndFutureReason(t *testing.T) {
	view := RequirementView{TrustedProgress: core.TrustedProgressSummary{Phase: core.TrustedPhaseBlocked, Blockers: []core.TrustedIssue{{Kind: "EXECUTION_BLOCKED", ReasonCode: "FUTURE_DEPENDENCY_FAILURE"}}}}
	diagnosis := workflowDiagnosisFromFacts(view, core.WorkflowRecoveryView{}, time.Now())
	if len(diagnosis.Issues) != 1 || diagnosis.Issues[0].Category != "UNKNOWN" || diagnosis.Issues[0].ReasonCode != "FUTURE_DEPENDENCY_FAILURE" {
		t.Fatalf("unknown code was hidden or guessed to be infrastructure: %+v", diagnosis)
	}
}

func TestWorkflowBlockerDiagnosisCheckExcerptRetainsUTF8AndErrorTail(t *testing.T) {
	issue := core.WorkflowDiagnosisIssue{}
	addWorkflowCheckEvidence(&issue, core.ComplexExecutionCheckRun{ID: "check", OutputSummary: strings.Repeat("依赖已准备\n", 2000) + "fatal mkdir target: EACCES", OutputTruncated: true})
	for _, fact := range issue.Evidence {
		if fact.Kind == "CHECK_OUTPUT" {
			if !fact.Truncated || !utf8.ValidString(fact.Value) || utf8.RuneCountInString(fact.Value) > 3000 || !strings.Contains(fact.Value, "fatal mkdir target: EACCES") {
				t.Fatal("bounded display lost the actionable tail or broke UTF-8")
			}
			return
		}
	}
	t.Fatal("check output missing")
}
