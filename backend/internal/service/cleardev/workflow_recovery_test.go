package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func stoppedWorkflow(t *testing.T, f *projectPlanningFixture, id string) core.ComplexExecutionSnapshot {
	t.Helper()
	for i := 0; i < 120; i++ {
		e, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		phase, _ := core.DeriveComplexExecutionPhase(e)
		if phase == core.ComplexExecutionBlocked || phase == core.ComplexExecutionNeedsHuman {
			return e
		}
		changed, stop, err := f.s.advanceComplexStandardExecution(context.Background(), id)
		if err != nil || (!changed && !stop) {
			t.Fatalf("advance: %v %v %v", changed, stop, err)
		}
	}
	t.Fatal("fixture never stopped")
	return core.ComplexExecutionSnapshot{}
}

func TestWorkflowRecoveryEnvironmentRetryPreservesFailureAndRestarts(t *testing.T) {
	f, h, id, before := blockedProjectDependencyFixture(t)
	h.eligible = false // Ordinary recovery must not depend on the old dependency-only exception.
	input := WorkflowRecoveryInput{RequestID: "environment-retry", ExecutionRunID: before.Run.ID, Action: core.RecoveryRetryCheck, TargetID: h.failedID, Supplement: "The isolated dependency environment is repaired."}
	view, err := f.s.RequestWorkflowRecovery(context.Background(), id, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.History) != 1 {
		t.Fatal("missing durable recovery")
	}
	if _, err = f.s.RequestWorkflowRecovery(context.Background(), id, input); err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.Supplement = "different"
	if _, err = f.s.RequestWorkflowRecovery(context.Background(), id, changed); err == nil {
		t.Fatal("changed idempotency payload accepted")
	}
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.service()
	h.store = f.store
	h.service = f.s
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h
	f.s.finalReviews, f.s.resultPreview = f.store, h.trial
	f.s.runBackground = func(func()) {}
	after := driveProjectFlow(t, f, id, false)
	if after.Run.CompletedAt == nil || len(after.WorkflowRecoveries) != 1 {
		t.Fatal("restart did not continue original check")
	}
	for _, old := range before.CheckRuns {
		for _, now := range after.CheckRuns {
			if old.ID == now.ID {
				a, _ := json.Marshal(old)
				b, _ := json.Marshal(now)
				if string(a) != string(b) {
					t.Fatal("original receipt changed")
				}
			}
		}
	}
}

func TestWorkflowRecoveryTaskReviewerKeepsCandidateAndOldVerdict(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := attachProjectFlow(f, preparer)
	h.reviewVerdicts = []string{"BLOCKED", "PASS"}
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, child.Requirement.ID)
	if len(before.Reviews) != 1 {
		t.Fatal("missing old reviewer")
	}
	input := WorkflowRecoveryInput{RequestID: "task-review-retry", ExecutionRunID: before.Run.ID, Action: core.RecoveryRetryReview, TargetID: before.Reviews[0].ID, Supplement: "Clarification within the approved acceptance contract."}
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), child.Requirement.ID, input); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, child.Requirement.ID)
	if len(after.Reviews) != 2 || after.Reviews[0].Verdict != core.LocalReviewBlocked || after.Reviews[1].Verdict != core.LocalReviewPass || after.Reviews[0].CandidateCommitSHA != after.Reviews[1].CandidateCommitSHA || len(after.Dispatches) != len(before.Dispatches) {
		t.Fatalf("review continuation lost history: %+v", after.Reviews)
	}
	if !strings.Contains(after.Reviews[1].ReviewPacketJSON, input.Supplement) {
		t.Fatal("reviewer did not receive supplementary context")
	}
}

func TestWorkflowRecoveryStageRequiresNewTrialAndPreservesOldReview(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := attachProjectFlow(f, preparer)
	h.finalVerdict = "BLOCKED"
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, child.Requirement.ID)
	if before.FinalReview == nil {
		t.Fatal("no final review")
	}
	input := WorkflowRecoveryInput{RequestID: "stage-retry", ExecutionRunID: before.Run.ID, Action: core.RecoveryRetryStage, TargetID: before.FinalReview.ID, Supplement: "The trial environment is now available; repeat every acceptance scenario."}
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), child.Requirement.ID, input); err != nil {
		t.Fatal(err)
	}
	pending, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Run.CompletedAt != nil || pending.FinalReview.ID == before.FinalReview.ID || pending.FinalReview.Verdict != "" {
		t.Fatal("supplement was mistaken for acceptance")
	}
	h.finalVerdict = "PASS"
	after := driveWorkflowRecovery(t, f, child.Requirement.ID)
	if after.Run.CompletedAt == nil || h.finalSends != 2 || after.FinalReview.AOSessionID == before.FinalReview.AOSessionID {
		t.Fatal("stage retry did not run independent fresh trial")
	}
	if !strings.Contains(after.FinalReview.ReviewPacketJSON, input.Supplement) {
		t.Fatal("stage reviewer lost supplement")
	}
}

type workflowBlockedBuilderHarness struct {
	*projectExecutionFlowHarness
	block bool
}

func (h *workflowBlockedBuilderHarness) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	turn, err := h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, id, prompt, key)
	if err != nil || !h.block || !strings.Contains(prompt, `"kind":"BUILDER_RESULT"`) {
		return turn, err
	}
	h.block = false
	h.currentCandidate = ""
	snapshot := h.snapshots[id]
	for i := range snapshot.Messages {
		m := &snapshot.Messages[i]
		if m.TurnID == turn && m.Role == domain.MessageRoleAssistant {
			m.Text = `{"schemaVersion":1,"kind":"BUILDER_RESULT","outcome":"BLOCKED","summary":"Need the original environment failure output."}`
		}
	}
	h.snapshots[id] = snapshot
	return turn, nil
}
func TestWorkflowRecoveryBuilderStartsNewBudgetedRound(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &workflowBlockedBuilderHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), block: true}
	f.s.chat = h
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, child.Requirement.ID)
	input := WorkflowRecoveryInput{RequestID: "builder-context", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: before.Dispatches[0].ID, Supplement: "The full error is EACCES in /workspace/test/artifacts. Use the approved writable runtime directory."}
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), child.Requirement.ID, input); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, child.Requirement.ID)
	if len(after.Dispatches) != 2 || after.Dispatches[0].Status != core.ComplexExecutionDispatchBlocked || after.Dispatches[1].Round != 1 {
		t.Fatal("Builder history was overwritten")
	}
	sent := false
	for _, relay := range h.relays {
		if strings.Contains(relay.prompt, input.Supplement) {
			sent = true
		}
	}
	if !sent {
		t.Fatal("Builder did not receive context")
	}
	for _, b := range after.Exception.Budgets {
		if b.RoleKind == core.ComplexExceptionBudgetBuilder && b.UsedTurns != 2 {
			t.Fatal("recovery bypassed Builder budget")
		}
	}
}

func driveWorkflowRecovery(t *testing.T, f *projectPlanningFixture, id string) core.ComplexExecutionSnapshot {
	t.Helper()
	for i := 0; i < 150; i++ {
		e, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if e.Run.CompletedAt != nil {
			return e
		}
		changed, stop, err := f.s.advanceComplexStandardExecution(context.Background(), id)
		if err != nil || stop || !changed {
			raw, _ := json.Marshal(e)
			t.Fatalf("step %d %v %v %v snapshot %s", i, changed, stop, err, raw)
		}
	}
	t.Fatal("too many steps")
	return core.ComplexExecutionSnapshot{}
}

// Explicit executor-receipt double. It is not a real container execution.
func (h *projectDependencyRecoveryHarness) CanRetryWorkflowCheck(_ context.Context, r ports.ClearDevCheckRequest) (bool, error) {
	return r.RunID == h.failedID && r.ProjectExecution != nil, nil
}

func TestWorkflowRecoveryRejectsStaleCancelledChangedAndWrongTarget(t *testing.T) {
	for _, mode := range []string{"source", "cancelled", "candidate", "wrong-target", "wrong-run", "empty"} {
		t.Run(mode, func(t *testing.T) {
			f, h, id, before := blockedProjectDependencyFixture(t)
			input := WorkflowRecoveryInput{RequestID: "reject-" + mode, ExecutionRunID: before.Run.ID, Action: core.RecoveryRetryCheck, TargetID: h.failedID, Supplement: "environment repaired"}
			switch mode {
			case "source":
				h.source.BaseCommitSHA = forty("d")
			case "cancelled":
				if err := f.s.CancelRequirement(context.Background(), id, "test cancellation"); err != nil {
					t.Fatal(err)
				}
			case "candidate":
				h.currentCandidate = forty("f")
			case "wrong-target":
				input.TargetID = "other-check"
			case "wrong-run":
				input.ExecutionRunID = "old-run"
			case "empty":
				input.Supplement = " "
			}
			if _, err := f.s.RequestWorkflowRecovery(context.Background(), id, input); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if len(after.WorkflowRecoveries) != 0 || len(after.CheckRuns) != len(before.CheckRuns) {
				t.Fatal("rejected request changed history")
			}
		})
	}
}

func TestWorkflowRecoverySchedulesExecutionAndRepeatedFailureCanBeRetried(t *testing.T) {
	f, h, id, before := blockedProjectDependencyFixture(t)
	h.eligible = false
	h.failures = 1
	f.s.complexExecutionRunning = map[string]bool{}
	f.s.complexExecutionWake = map[string]bool{}
	var scheduled []func()
	f.s.runBackground = func(fn func()) { scheduled = append(scheduled, fn) }
	first := WorkflowRecoveryInput{RequestID: "first-environment-retry", ExecutionRunID: before.Run.ID, Action: core.RecoveryRetryCheck, TargetID: h.failedID, Supplement: "First environment repair."}
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), id, first); err != nil {
		t.Fatal(err)
	}
	if len(scheduled) != 1 {
		t.Fatal("request did not schedule execution")
	}
	scheduled[0]()
	stopped, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if phase, _ := core.DeriveComplexExecutionPhase(stopped); phase != core.ComplexExecutionBlocked {
		t.Fatal("second failure did not stop")
	}
	second := first
	second.RequestID = "second-environment-retry"
	second.TargetID = h.failedID
	second.Supplement = "Second repair with new diagnosis."
	if second.TargetID == first.TargetID {
		t.Fatal("executor reused an old receipt")
	}
	if _, err = f.s.RequestWorkflowRecovery(context.Background(), id, second); err != nil {
		t.Fatal(err)
	}
	if len(scheduled) != 2 {
		t.Fatal("second explicit request did not schedule execution")
	}
	scheduled[1]()
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Run.CompletedAt == nil || len(after.WorkflowRecoveries) != 2 {
		t.Fatal("repeated failure became a dead end")
	}
}

func TestWorkflowRecoveryConcurrentRequestsAppendOneSuccessor(t *testing.T) {
	f, h, id, before := blockedProjectDependencyFixture(t)
	d := before.Dispatches[0]
	summary := ""
	for _, check := range before.CheckRuns {
		if check.ID == h.failedID {
			summary = check.OutputSummary
		}
	}
	r := core.WorkflowRecovery{ID: "same-recovery", ExecutionRunID: before.Run.ID, Action: core.RecoveryRetryCheck, TargetID: h.failedID, DispatchID: d.ID, TaskID: d.ComplexExecutionTaskID, StepID: d.AgentStepID, BindingID: d.BuilderRoleBindingID, SuccessorID: "same-retry", CandidateSHA: d.CandidateCommitSHA, OriginalStatus: string(d.Status), OriginalReason: string(d.ReasonCode), OriginalSummary: summary, OriginalStoppedAt: *d.SettledAt, Supplement: "Known settled environment failure repaired.", CreatedAt: time.Now().UTC()}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.store.ApplyClearDevWorkflowRecovery(context.Background(), r, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.WorkflowRecoveries) != 1 || len(after.CheckRuns) != len(before.CheckRuns)+1 {
		t.Fatal("duplicate recovery created extra external work")
	}
}

func TestWorkflowRecoveryRetriesReviewerEnvironmentBeforeAnySend(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := attachProjectFlow(f, preparer)
	h.reviewerSpawnErr = errors.New("review environment unavailable")
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, child.Requirement.ID)
	options := core.WorkflowRecoveryOptions(before)
	if len(options) != 1 || options[0].Action != core.RecoveryRetryReview || len(before.Reviews) != 0 {
		t.Fatalf("missing never-sent reviewer recovery: %+v", options)
	}
	h.reviewerSpawnErr = nil
	input := WorkflowRecoveryInput{RequestID: "review-environment", ExecutionRunID: before.Run.ID, Action: options[0].Action, TargetID: options[0].TargetID, Supplement: "The reviewer environment is repaired."}
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), child.Requirement.ID, input); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, child.Requirement.ID)
	if len(after.Reviews) != 1 || len(after.WorkflowRecoveries) != 1 || len(after.Dispatches) != len(before.Dispatches) {
		t.Fatal("review retry redid Builder work")
	}
}

func TestWorkflowRecoveryExtraBuilderBudgetDoesNotInventAnswer(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &workflowBlockedBuilderHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), block: true}
	h.benchmarkSequentialCandidates = true
	f.s.chat = h
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	var stopped core.ComplexExecutionSnapshot
	for round := 0; round < 4; round++ {
		stopped = stoppedWorkflow(t, f, child.Requirement.ID)
		if round == 3 {
			break
		}
		h.block = true
		input := WorkflowRecoveryInput{RequestID: fmt.Sprintf("question-%d", round), ExecutionRunID: stopped.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: stopped.Tasks[0].CurrentDispatchID, Supplement: "Additional in-scope diagnostic context."}
		if _, err := f.s.RequestWorkflowRecovery(context.Background(), child.Requirement.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	options := core.WorkflowRecoveryOptions(stopped)
	if len(options) != 1 || options[0].UnavailableReason != "BUILDER_BUDGET_EXHAUSTED" {
		t.Fatalf("budget was not enforced: %+v", options)
	}
	if _, err := f.s.RequestExtraBuilderTurn(context.Background(), child.Requirement.ID, stopped.Tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	// Explicit test-only native authority double, never a real user's approval.
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindExtraBuilderTurn, core.HumanDecisionApprove)
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Tasks[0].Status != core.DevelopmentTaskStatusBlocked || len(after.Dispatches) != 4 {
		t.Fatal("budget approval invented a Builder answer")
	}
	input := WorkflowRecoveryInput{RequestID: "approved-question-context", ExecutionRunID: after.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: after.Tasks[0].CurrentDispatchID, Supplement: "The missing diagnostic fact is now supplied."}
	if _, err = f.s.RequestWorkflowRecovery(context.Background(), child.Requirement.ID, input); err != nil {
		t.Fatal(err)
	}
	final := driveWorkflowRecovery(t, f, child.Requirement.ID)
	if len(final.Dispatches) != 5 || final.Run.CompletedAt == nil {
		t.Fatal("authorized round did not continue")
	}
}

func TestWorkflowRecoveryRestoresOriginalBuilderAndRetriesNeverSentStep(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &workflowBlockedBuilderHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), block: true}
	f.s.chat = h
	ctx := context.Background()
	id := child.Requirement.ID
	if _, err := f.s.StartProjectExecution(ctx, id, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, id)
	binding, _ := complexExecutionBindingByID(before, before.Run.BuilderRoleBindingID)
	rec, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	rec.Activity.State = domain.ActivityExited
	rec.Metadata.ProviderConversationID = ""
	if err = f.store.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	unavailable, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(unavailable.Options) != 1 || unavailable.Options[0].UnavailableReason != "RECOVERY_NATIVE_IDENTITY_MISSING" {
		t.Fatalf("unavailable native recovery displayed as usable: %+v", unavailable.Options)
	}
	rec.Metadata.ProviderConversationID = "original-provider-conversation"
	rec.Activity.State = domain.ActivityExited
	if err = f.store.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	restores := 0
	f.s.restoreOriginalAgentSession = func(ctx context.Context, sid domain.SessionID) (string, error) {
		restores++
		if sid != rec.ID {
			t.Fatal("restored a different session")
		}
		if restores == 1 {
			return "", errors.New("environment still unavailable")
		}
		current, _, err := f.store.GetSession(ctx, sid)
		if err != nil {
			return "", err
		}
		current.Activity.State = domain.ActivityIdle
		return "native", f.store.UpdateSession(ctx, current)
	}
	if _, err = f.s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: "restore-builder", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: before.Dispatches[0].ID, Supplement: "Complete diagnostic context."}); err != nil {
		t.Fatal(err)
	}
	failed := stoppedWorkflow(t, f, id)
	options := core.WorkflowRecoveryOptions(failed)
	if len(options) != 1 || options[0].Action != core.RecoveryRetryBuilderSession || restores != 1 {
		t.Fatalf("missing original step retry: %+v restores=%d", options, restores)
	}
	if _, err = f.s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: "retry-original-builder", ExecutionRunID: before.Run.ID, Action: options[0].Action, TargetID: options[0].TargetID, Supplement: "Native runtime repaired."}); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if restores != 2 || len(after.Dispatches) != 2 || len(after.WorkflowRecoveries) != 2 {
		t.Fatalf("retry created another round: restores=%d dispatches=%d", restores, len(after.Dispatches))
	}
	for _, b := range after.Exception.Budgets {
		if b.RoleKind == core.ComplexExceptionBudgetBuilder && b.UsedTurns != 2 {
			t.Fatal("unsent restore consumed a Builder turn")
		}
	}
}

func TestWorkflowRecoveryReviewerExitBeforeSendCanRetry(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	attachProjectFlow(f, preparer)
	ctx := context.Background()
	id := child.Requirement.ID
	if _, err := f.s.StartProjectExecution(ctx, id, admission); err != nil {
		t.Fatal(err)
	}
	var pending core.ComplexExecutionSnapshot
	for i := 0; i < 80; i++ {
		e, _, err := f.store.GetClearDevComplexExecution(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(e.Reviews) == 1 {
			pending = e
			break
		}
		if _, _, err = f.s.advanceComplexStandardExecution(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(pending.Reviews) != 1 {
		t.Fatal("no pending review")
	}
	review := pending.Reviews[0]
	binding, _ := complexExecutionBindingByID(pending, review.ReviewerRoleBindingID)
	rec, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	rec.Activity.State = domain.ActivityExited
	if err = f.store.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	failed := stoppedWorkflow(t, f, id)
	options := core.WorkflowRecoveryOptions(failed)
	if len(options) != 1 || options[0].TargetID != review.ID {
		t.Fatalf("missing reviewer retry: %+v review=%+v", options, failed.Reviews)
	}
	if _, err = f.s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: "reviewer-before-send", ExecutionRunID: failed.Run.ID, Action: options[0].Action, TargetID: options[0].TargetID, Supplement: "Reviewer runtime repaired."}); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if len(after.Dispatches) != 1 || len(after.Reviews) != 2 || after.Reviews[0].Status != core.LocalReviewStatusFailed {
		t.Fatal("review retry lost history or repeated Builder")
	}
}

func TestWorkflowRecoveryRejectsReviewerWithUnknownDelivery(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	attachProjectFlow(f, preparer)
	ctx := context.Background()
	id := child.Requirement.ID
	if _, err := f.s.StartProjectExecution(ctx, id, admission); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80; i++ {
		e, _, err := f.store.GetClearDevComplexExecution(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(e.Reviews) == 0 {
			if _, _, err = f.s.advanceComplexStandardExecution(ctx, id); err != nil {
				t.Fatal(err)
			}
			continue
		}
		review := e.Reviews[0]
		binding, _ := complexExecutionBindingByID(e, review.ReviewerRoleBindingID)
		step, _ := complexExecutionStepByID(e, review.AgentStepID)
		if err := f.s.occupyExceptionBudget(ctx, e, e.Tasks[0].ID, core.ComplexExceptionBudgetReviewer, review.ID, step.ID); err != nil {
			t.Fatal(err)
		}
		a, err := f.s.ensureAgentAttempt(ctx, id, core.AgentStepCategoryComplexExecution, step, binding.AOSessionID, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.store.ReserveClearDevAgentMessage(ctx, core.ReserveAgentMessageCommand{Attempt: a, Source: core.AgentMessageOriginal, Boundary: core.AgentAttemptEvent{ID: "unknown-review-send", AttemptID: a.ID, ClientMessageID: step.ClientMessageID, PromptSHA256: step.PromptSHA256, Status: core.AgentAttemptDeliveryUnknown, FailureCategory: domain.AgentFailureDeliveryUnknown, RecordedAt: time.Now().UTC()}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = f.s.failComplexExecutionDispatch(ctx, e, e.Tasks[0], e.Dispatches[0], true, "REVIEWER_UNAVAILABLE"); err != nil {
			t.Fatal(err)
		}
		view, err := f.s.GetWorkflowRecovery(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(view.Options) != 1 || view.Options[0].UnavailableReason != "RESULT_NOT_SETTLED" {
			t.Fatalf("unknown delivery actionable: %+v", view.Options)
		}
		if _, err = f.s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: "unsafe-review-retry", ExecutionRunID: e.Run.ID, Action: core.RecoveryRetryReview, TargetID: review.ID, Supplement: "Environment repaired."}); err == nil {
			t.Fatal("unknown delivery was retried")
		}
		return
	}
	t.Fatal("no review")
}

type unfinishedWorkflowBuilder struct {
	*workflowBlockedBuilderHarness
	dirty  bool
	digest string
	reads  int
}

func (h *unfinishedWorkflowBuilder) InspectCandidate(ctx context.Context, path, base string) (ports.ClearDevCandidateInspection, error) {
	if h.dirty {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevWorkspaceDirty
	}
	return h.projectExecutionFlowHarness.InspectCandidate(ctx, path, base)
}
func (h *unfinishedWorkflowBuilder) InspectWorkflowWorkingTree(_ context.Context, r ports.ClearDevMailFreezeRequest) (string, error) {
	h.reads++
	if r.ProjectExecution == nil || r.ParentSHA == "" || len(r.WritePaths) == 0 {
		return "", ports.ErrClearDevCandidateInvalid
	}
	return h.digest, nil
}
func (h *unfinishedWorkflowBuilder) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	if !h.block && strings.Contains(prompt, `"kind":"BUILDER_RESULT"`) {
		h.dirty = false
	}
	return h.workflowBlockedBuilderHarness.RelayChatTurnWithID(ctx, id, prompt, key)
}
func TestWorkflowRecoveryPreservesAndRechecksUnfinishedBuilder(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
			h := &unfinishedWorkflowBuilder{workflowBlockedBuilderHarness: &workflowBlockedBuilderHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), block: true}, digest: strings.Repeat("a", 64)}
			f.s.chat = h
			f.s.inspector = h
			ctx := context.Background()
			id := child.Requirement.ID
			if _, err := f.s.StartProjectExecution(ctx, id, admission); err != nil {
				t.Fatal(err)
			}
			before := stoppedWorkflow(t, f, id)
			h.dirty = true
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: "unfinished-context", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: before.Dispatches[0].ID, Supplement: "Continue with the preserved incomplete work."}); err != nil {
				t.Fatal(err)
			}
			if changed {
				sent := len(h.relays)
				h.digest = strings.Repeat("b", 64)
				stopped := stoppedWorkflow(t, f, id)
				if len(h.relays) != sent || len(stopped.WorkflowRecoveries) != 1 {
					t.Fatal("changed unfinished work was sent")
				}
				return
			}
			after := driveWorkflowRecovery(t, f, id)
			if len(after.WorkflowRecoveries) != 1 || after.WorkflowRecoveries[0].WorkingTreeSHA256 != h.digest || h.reads < 2 {
				t.Fatal("unfinished work was not saved and rechecked")
			}
		})
	}
}

func TestWorkflowRecoveryUnfinishedRestartAndReplay(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &unfinishedWorkflowBuilder{workflowBlockedBuilderHarness: &workflowBlockedBuilderHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), block: true}, digest: strings.Repeat("a", 64)}
	f.s.chat, f.s.inspector = h, h
	ctx := context.Background()
	id := child.Requirement.ID
	if _, err := f.s.StartProjectExecution(ctx, id, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, id)
	h.dirty = true
	f.s.runBackground = func(func()) {} // Exit after durable registration, before dispatch.
	input := WorkflowRecoveryInput{RequestID: "unfinished-restart", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: before.Dispatches[0].ID, Supplement: "Resume preserved work after shutdown."}
	sent := len(h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	if len(h.relays) != sent {
		t.Fatal("registration sent before restart")
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.service()
	h.store = f.store
	h.service = f.s
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h
	f.s.resultPreview = h.trial
	f.s.finalReviews = f.store
	f.s.runBackground = func(func()) {}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if len(after.WorkflowRecoveries) != 1 || after.WorkflowRecoveries[0].WorkingTreeSHA256 != h.digest {
		t.Fatal("restart lost original recovery")
	}
	count := len(h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	if len(h.relays) != count {
		t.Fatal("duplicate recovery sent another turn")
	}
	if len(after.Dispatches) != 2 || after.Dispatches[0].Status != core.ComplexExecutionDispatchBlocked {
		t.Fatal("restart rewrote original failure")
	}
}

// The freeze failure is real service state; only the Git implementation is a double.
type invalidCandidateRecoveryHarness struct {
	*projectExecutionFlowHarness
	reject bool
}

func (h *invalidCandidateRecoveryHarness) FreezeMailCandidate(ctx context.Context, r ports.ClearDevMailFreezeRequest) (ports.ClearDevCandidateInspection, error) {
	if h.reject {
		h.currentCandidate = "" // Rejected uncommitted handoff retains the original HEAD.
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	return h.projectExecutionFlowHarness.FreezeMailCandidate(ctx, r)
}
func TestWorkflowRecoverySettledCandidateHandoffReturnsToBuilder(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &invalidCandidateRecoveryHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), reject: true}
	f.s.inspector = h
	ctx := context.Background()
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, child.Requirement.ID)
	if before.Dispatches[0].ReasonCode != "CANDIDATE_INVALID" || before.Dispatches[0].CandidateCommitID != "" {
		t.Fatalf("wrong failure: %+v", before.Dispatches)
	}
	for _, mode := range []string{"frozen", "unsettled"} {
		probe := before
		probe.Dispatches = append([]core.ComplexExecutionDispatch(nil), before.Dispatches...)
		probe.AgentSteps = append([]core.AgentStep(nil), before.AgentSteps...)
		if mode == "frozen" {
			probe.Dispatches[0].CandidateCommitID = "already-frozen"
		} else {
			for i := range probe.AgentSteps {
				if probe.AgentSteps[i].ID == probe.Dispatches[0].AgentStepID {
					probe.AgentSteps[i].SendStatus = core.AgentStepSendStatusSent
				}
			}
		}
		for _, option := range core.WorkflowRecoveryOptions(probe) {
			if option.Action == core.RecoveryContinueBuilder && option.UnavailableReason == "" {
				t.Fatalf("%s became repeatable", mode)
			}
		}
	}
	input := WorkflowRecoveryInput{RequestID: "candidate-handoff-repair", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: before.Dispatches[0].ID, Supplement: "Inspect the rejected lockfile handoff and ask the original Planner to repair the missing companion-file agreement."}
	for range 2 {
		if _, err := f.s.RequestWorkflowRecovery(ctx, child.Requirement.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	h.reject = false
	after := driveWorkflowRecovery(t, f, child.Requirement.ID)
	if len(after.Dispatches) != 2 || len(after.WorkflowRecoveries) != 1 || after.Dispatches[0].ReasonCode != "CANDIDATE_INVALID" {
		t.Fatal("recovery duplicated work or erased original failure")
	}
	found := false
	for _, r := range h.relays {
		found = found || strings.Contains(r.prompt, input.Supplement)
	}
	if !found {
		t.Fatal("Builder never received handoff failure")
	}
}

func TestWorkflowRecoveryUnsentDirtyWorkspaceReusesOriginalDispatch(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
			h := &unfinishedWorkflowBuilder{workflowBlockedBuilderHarness: &workflowBlockedBuilderHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer)}, digest: strings.Repeat("b", 64)}
			f.s.inspector = h
			f.s.chat = h
			ctx := context.Background()
			if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
				t.Fatal(err)
			}
			for range 60 {
				e, _, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(e.Dispatches) == 1 {
					h.dirty = true
					break
				}
				if _, _, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID); err != nil {
					t.Fatal(err)
				}
			}
			stop := func() core.ComplexExecutionSnapshot {
				for range 120 {
					e, _, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
					if err != nil {
						t.Fatal(err)
					}
					phase, _ := core.DeriveComplexExecutionPhase(e)
					if phase == core.ComplexExecutionBlocked || phase == core.ComplexExecutionNeedsHuman {
						return e
					}
					_, _, err = f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
					if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
						t.Fatal(err)
					}
				}
				t.Fatal("fixture did not stop")
				return core.ComplexExecutionSnapshot{}
			}
			before := stop()
			if len(before.Dispatches) != 1 || before.Dispatches[0].ReasonCode != "BUILDER_WORKTREE_DIRTY" {
				t.Fatalf("wrong stop: %+v", before.Dispatches)
			}
			binding, _ := complexExecutionBindingByID(before, before.Run.BuilderRoleBindingID)
			session, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
			if err != nil {
				t.Fatal(err)
			}
			session.Metadata.ProviderConversationID = "original-dirty-workspace-fixture"
			session.Activity.State = domain.ActivityIdle
			if err := f.store.UpdateSession(ctx, session); err != nil {
				t.Fatal(err)
			}
			input := builderSessionRecheckInput(t, f, child.Requirement.ID, "keep-unsent-work")
			for range 2 {
				if _, err := f.s.RequestWorkflowRecovery(ctx, child.Requirement.ID, input); err != nil {
					t.Fatal(err)
				}
			}
			if changed {
				h.digest = strings.Repeat("c", 64)
				calls := len(h.relays)
				after := stop()
				if len(h.relays) != calls || len(after.Dispatches) != 1 {
					t.Fatal("changed workspace resent or allocated another dispatch")
				}
				return
			}
			after := driveWorkflowRecovery(t, f, child.Requirement.ID)
			if len(after.Dispatches) != 1 || after.Dispatches[0].ID != before.Dispatches[0].ID || after.Dispatches[0].AgentStepID != before.Dispatches[0].AgentStepID || len(after.WorkflowRecoveries) != 1 || after.WorkflowRecoveries[0].WorkingTreeSHA256 != h.digest {
				t.Fatal("lost original unsent step or workspace")
			}
			for _, b := range after.Exception.Budgets {
				if b.RoleKind == core.ComplexExceptionBudgetBuilder && b.UsedTurns != 1 {
					t.Fatal("unsent failure spent an extra Builder turn")
				}
			}
		})
	}
}

func TestWorkflowRecoveryCandidateBudgetApprovalPreservesStoppedHandoff(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &invalidCandidateRecoveryHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), reject: true}
	h.benchmarkSequentialCandidates = true
	f.s.inspector = h
	ctx := context.Background()
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	var stopped core.ComplexExecutionSnapshot
	for round := 0; round < 4; round++ {
		stopped = stoppedWorkflow(t, f, child.Requirement.ID)
		if round == 3 {
			break
		}
		input := WorkflowRecoveryInput{RequestID: fmt.Sprintf("candidate-budget-%d", round), ExecutionRunID: stopped.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: stopped.Tasks[0].CurrentDispatchID, Supplement: "Preserve the original files and repair the candidate handoff."}
		if _, err := f.s.RequestWorkflowRecovery(ctx, child.Requirement.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	options := core.WorkflowRecoveryOptions(stopped)
	if len(options) != 1 || options[0].UnavailableReason != "BUILDER_BUDGET_EXHAUSTED" {
		t.Fatalf("expected exhausted candidate recovery: %+v", options)
	}
	if _, err := f.s.RequestExtraBuilderTurn(ctx, child.Requirement.ID, stopped.Tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	// Only this isolated test uses an explicit native-decision double.
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindExtraBuilderTurn, core.HumanDecisionApprove)
	after, _, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stopped.Dispatches, after.Dispatches) || !reflect.DeepEqual(stopped.AgentSteps, after.AgentSteps) || !reflect.DeepEqual(stopped.Tasks, after.Tasks) {
		t.Fatal("budget grant rewrote stopped work or invented continuation")
	}
	budget := projectBuilderBudget(after, after.Tasks[0].ID)
	if budget == nil || budget.AuthorizedExtraTurns != 1 || budget.UsedTurns != 4 {
		t.Fatalf("incorrect grant: %+v", budget)
	}
	options = core.WorkflowRecoveryOptions(after)
	if len(options) != 1 || options[0].UnavailableReason != "" {
		t.Fatalf("original recovery unavailable after grant: %+v", options)
	}
	h.reject = false
	input := WorkflowRecoveryInput{RequestID: "candidate-budget-approved", ExecutionRunID: after.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: after.Tasks[0].CurrentDispatchID, Supplement: "The control-plane handoff bug is repaired; preserve the existing implementation and resubmit the original task."}
	for range 2 {
		if _, err := f.s.RequestWorkflowRecovery(ctx, child.Requirement.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	final := driveWorkflowRecovery(t, f, child.Requirement.ID)
	if len(final.Dispatches) != 5 || final.Run.CompletedAt == nil {
		t.Fatal("approved original recovery did not complete")
	}
}

func TestWorkflowRecoveryInfrastructureCheckAfterBuilderBudget(t *testing.T) {
	testWorkflowRecoveryAfterBudget(t, false)
}
func TestWorkflowRecoveryTerminatedCheckAfterBuilderBudget(t *testing.T) {
	testWorkflowRecoveryAfterBudget(t, true)
}
func testWorkflowRecoveryAfterBudget(t *testing.T, terminated bool) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &projectDependencyRecoveryHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), failures: 4}
	h.terminated = terminated
	h.freshInstall = terminated
	h.benchmarkSequentialCandidates = true
	f.s.checks = h
	ctx := context.Background()
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	var before core.ComplexExecutionSnapshot
	for range 200 {
		var err error
		before, _, err = f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(before.Dispatches) > 0 && before.Dispatches[len(before.Dispatches)-1].ReasonCode == "BUILDER_BUDGET_EXHAUSTED" {
			break
		}
		changed, stop, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
		if err != nil || stop || !changed {
			t.Fatalf("advance %v %v %v", changed, stop, err)
		}
	}
	if len(before.Dispatches) != 4 {
		t.Fatalf("did not reach last Builder: %+v", before.Dispatches)
	}
	if terminated {
		var found bool
		for _, check := range before.CheckRuns {
			if check.ID == h.failedID && check.Result == core.EvidenceResultFail && check.ExitCode != nil && *check.ExitCode == 137 {
				found = true
			}
		}
		if !found {
			t.Fatal("fixture did not preserve terminated failure")
		}
	}
	sends := countProjectBuilderSends(h.projectExecutionFlowHarness)
	options, err := f.s.GetWorkflowRecovery(ctx, child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range options.Options {
		if o.Action == core.RecoveryRetryCheck && o.TargetID == h.failedID && o.UnavailableReason == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("settled checker failure hidden by Builder budget: %+v", options.Options)
	}
	input := WorkflowRecoveryInput{RequestID: "budget-check-retry", ExecutionRunID: before.Run.ID, Action: core.RecoveryRetryCheck, TargetID: h.failedID, Supplement: "Dependency import repaired; repeat this exact candidate check, not Builder work."}
	for range 2 {
		if _, err := f.s.RequestWorkflowRecovery(ctx, child.Requirement.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	after := driveWorkflowRecovery(t, f, child.Requirement.ID)
	if len(after.Dispatches) != 4 || countProjectBuilderSends(h.projectExecutionFlowHarness) != sends || after.Run.CompletedAt == nil {
		t.Fatal("same-candidate check recovery repeated Builder")
	}
	for _, old := range before.CheckRuns {
		for _, current := range after.CheckRuns {
			if old.ID == current.ID && !reflect.DeepEqual(old, current) {
				t.Fatal("original failed check was rewritten")
			}
		}
	}
	if !reflect.DeepEqual(before.Exception.Budgets, after.Exception.Budgets) {
		// Reviewer spends its first turn after checks succeed; Builder must not spend.
		b1, b2 := projectBuilderBudget(before, before.Tasks[0].ID), projectBuilderBudget(after, after.Tasks[0].ID)
		if !reflect.DeepEqual(b1, b2) {
			t.Fatal("check recovery changed Builder budget")
		}
	}
}
