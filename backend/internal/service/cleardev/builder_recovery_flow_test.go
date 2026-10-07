package cleardev

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestProjectBuilderBudgetRecoverySettlesDecisionRedispatchesAndSurvivesRestart(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := attachProjectFlow(f, preparer)
	h.benchmarkSequentialCandidates = true
	h.reviewFindingPath = "src/storage.ts"
	h.reviewVerdicts = []string{"REWORK", "REWORK", "REWORK", "REWORK", "PASS"}

	ctx := context.Background()
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}

	var blocked core.ComplexExecutionSnapshot
	for transition := 0; transition < 180; transition++ {
		execution, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
		if err != nil || !found {
			t.Fatalf("read project execution: found=%t err=%v", found, err)
		}
		if len(execution.Tasks) == 1 && execution.Tasks[0].Status == core.DevelopmentTaskStatusBlocked {
			current := execution.Tasks[0]
			for _, dispatch := range execution.Dispatches {
				if dispatch.ID == current.CurrentDispatchID &&
					dispatch.Status == core.ComplexExecutionDispatchBlocked &&
					dispatch.ReasonCode == core.ReasonCode("BUILDER_BUDGET_EXHAUSTED") {
					blocked = execution
					break
				}
			}
			if blocked.Run.ID != "" {
				break
			}
		}
		progressed, stopped, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
		if err != nil || stopped || !progressed {
			phase, reason := core.DeriveComplexExecutionPhase(execution)
			after, _, _ := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
			t.Fatalf("transition %d phase=%s reason=%s task=%+v dispatches=%+v reviews=%+v afterTask=%+v afterDispatches=%+v: progressed=%t stopped=%t err=%v", transition, phase, reason, execution.Tasks, execution.Dispatches, execution.Reviews, after.Tasks, after.Dispatches, progressed, stopped, err)
		}
	}
	if blocked.Run.ID == "" {
		t.Fatal("project did not reach the recoverable builder-budget block")
	}
	if got := blocked.Tasks[0].CurrentRound; got != core.ComplexProjectMaxReworkCount {
		t.Fatalf("blocked round=%d, want %d", got, core.ComplexProjectMaxReworkCount)
	}
	if got := blocked.Tasks[0].ReworkCount; got != core.ComplexProjectMaxReworkCount {
		t.Fatalf("blocked rework count=%d, want %d", got, core.ComplexProjectMaxReworkCount)
	}
	if len(blocked.Dispatches) != core.ComplexProjectMaxReworkCount+1 || len(blocked.Reviews) != core.ComplexProjectMaxReworkCount+1 {
		t.Fatalf("blocked history dispatches=%d reviews=%d", len(blocked.Dispatches), len(blocked.Reviews))
	}
	originalDispatch := blocked.Dispatches[len(blocked.Dispatches)-1]
	originalReview := blocked.Reviews[len(blocked.Reviews)-1]
	if originalReview.DispatchID != originalDispatch.ID || originalReview.Verdict != core.LocalReviewRework {
		t.Fatalf("budget exhaustion did not preserve the final REWORK review: dispatch=%+v review=%+v", originalDispatch, originalReview)
	}

	beforeBuilderSends := countProjectBuilderSends(h)
	if _, err := f.s.RequestExtraBuilderTurn(ctx, child.Requirement.ID, blocked.Tasks[0].DevelopmentTaskID); err != nil {
		t.Fatalf("request controlled recovery: %v", err)
	}
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindExtraBuilderTurn, core.HumanDecisionApprove)

	reopened, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
	if err != nil || !found {
		t.Fatalf("read recovered execution: found=%t err=%v", found, err)
	}
	if reopened.Tasks[0].Status != core.DevelopmentTaskStatusRework ||
		reopened.Tasks[0].ReworkCount != core.ComplexProjectMaxReworkCount+1 {
		t.Fatalf("recovered task=%+v", reopened.Tasks[0])
	}
	recoveredOldDispatch, ok := projectDispatchByID(reopened, originalDispatch.ID)
	if !ok || recoveredOldDispatch.Status != core.ComplexExecutionDispatchRework ||
		recoveredOldDispatch.ReasonCode != core.ReasonCode("BUILDER_BUDGET_EXHAUSTED") ||
		recoveredOldDispatch.SettledAt == nil {
		t.Fatalf("original blocked attempt was not retained as the recovery predecessor: %+v", recoveredOldDispatch)
	}
	if review, ok := projectReviewByID(reopened, originalReview.ID); !ok ||
		review.Verdict != core.LocalReviewRework || review.ReasonCode != originalReview.ReasonCode || review.SettledAt == nil {
		t.Fatalf("original failure review was rewritten: %+v", review)
	}
	builderBudget := projectBuilderBudget(reopened, reopened.Tasks[0].ID)
	if builderBudget == nil || builderBudget.AuthorizedExtraTurns != 1 {
		t.Fatalf("builder budget after decision settlement=%+v", builderBudget)
	}

	for transition := 0; transition < 40 && countProjectBuilderSends(h) == beforeBuilderSends; transition++ {
		progressed, stopped, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
		if err != nil || stopped || !progressed {
			t.Fatalf("redispatch transition %d: progressed=%t stopped=%t err=%v", transition, progressed, stopped, err)
		}
	}
	if got := countProjectBuilderSends(h); got != beforeBuilderSends+1 {
		t.Fatalf("authorized recovery builder sends=%d, want %d", got, beforeBuilderSends+1)
	}
	redispatched, _, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(redispatched.Dispatches) != len(blocked.Dispatches)+1 {
		t.Fatalf("dispatch count after recovery=%d, want %d", len(redispatched.Dispatches), len(blocked.Dispatches)+1)
	}
	newDispatch := redispatched.Dispatches[len(redispatched.Dispatches)-1]
	if newDispatch.Round != core.ComplexProjectMaxReworkCount+1 || newDispatch.ID == originalDispatch.ID {
		t.Fatalf("recovery dispatch=%+v", newDispatch)
	}
	newStep, ok := complexExecutionStepByID(redispatched, newDispatch.AgentStepID)
	if !ok || newStep.SendStatus != core.AgentStepSendStatusSent {
		t.Fatalf("recovery builder was not durably sent before restart: %+v", newStep)
	}

	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	reopenedStore := f.store
	t.Cleanup(func() { _ = reopenedStore.Close() })
	f.h.store = f.store
	f.service()
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h
	f.s.finalReviews = f.store
	f.s.runBackground = func(func()) {}

	sendsAtRestart := countProjectBuilderSends(h)
	progressed, stopped, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
	if err != nil || stopped || !progressed {
		t.Fatalf("resume recovered builder after restart: progressed=%t stopped=%t err=%v", progressed, stopped, err)
	}
	if got := countProjectBuilderSends(h); got != sendsAtRestart {
		t.Fatalf("restart resent recovered builder round: before=%d after=%d", sendsAtRestart, got)
	}
	resumed, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
	if err != nil || !found {
		t.Fatalf("read restarted execution: found=%t err=%v", found, err)
	}
	resumedStep, ok := complexExecutionStepByID(resumed, newDispatch.AgentStepID)
	if !ok || resumedStep.SendStatus != core.AgentStepSendStatusSettled {
		t.Fatalf("restart did not resume the durable sent step: %+v", resumedStep)
	}
	if len(resumed.Dispatches) != len(blocked.Dispatches)+1 {
		t.Fatalf("restart duplicated recovery dispatches: got=%d want=%d", len(resumed.Dispatches), len(blocked.Dispatches)+1)
	}
	if prior, ok := projectDispatchByID(resumed, originalDispatch.ID); !ok ||
		prior.ReasonCode != core.ReasonCode("BUILDER_BUDGET_EXHAUSTED") || prior.SettledAt == nil {
		t.Fatalf("restart lost original budget-exhausted failure: %+v", prior)
	}
	if review, ok := projectReviewByID(resumed, originalReview.ID); !ok ||
		review.Verdict != core.LocalReviewRework || review.SettledAt == nil {
		t.Fatalf("restart lost original REWORK review: %+v", review)
	}
	if budget := projectBuilderBudget(resumed, resumed.Tasks[0].ID); budget == nil || budget.AuthorizedExtraTurns != 1 {
		t.Fatalf("restart changed granted recovery budget: %+v", budget)
	}
	pending, err := f.store.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range pending {
		if request.DevelopmentRequirementID == child.Requirement.ID && request.DecisionKind == core.HumanDecisionKindExtraBuilderTurn {
			t.Fatalf("settled recovery request became pending after restart: %+v", request)
		}
	}
}

func countProjectBuilderSends(h *projectExecutionFlowHarness) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, relay := range h.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) {
			count++
		}
	}
	return count
}

func projectDispatchByID(snapshot core.ComplexExecutionSnapshot, id string) (core.ComplexExecutionDispatch, bool) {
	for _, dispatch := range snapshot.Dispatches {
		if dispatch.ID == id {
			return dispatch, true
		}
	}
	return core.ComplexExecutionDispatch{}, false
}

func projectReviewByID(snapshot core.ComplexExecutionSnapshot, id string) (core.ComplexExecutionReview, bool) {
	for _, review := range snapshot.Reviews {
		if review.ID == id {
			return review, true
		}
	}
	return core.ComplexExecutionReview{}, false
}

func projectBuilderBudget(snapshot core.ComplexExecutionSnapshot, taskID string) *core.ComplexExceptionBudget {
	if snapshot.Exception == nil {
		return nil
	}
	for i := range snapshot.Exception.Budgets {
		budget := &snapshot.Exception.Budgets[i]
		if budget.RoleKind == core.ComplexExceptionBudgetBuilder && budget.ComplexExecutionTaskID == taskID {
			return budget
		}
	}
	return nil
}

// Run the actual scheduling entry points, rather than manually advancing the
// executor after approval. Model, Git and check adapters remain explicit doubles.
func TestProjectBuilderNativeApprovalAutomaticallyWakesExecution(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := attachProjectFlow(f, preparer)
	h.benchmarkSequentialCandidates = true
	h.reviewFindingPath = "src/storage.ts"
	h.reviewVerdicts = []string{"REWORK", "REWORK", "REWORK", "REWORK", "PASS"}
	f.s.runBackground = func(run func()) { run() }
	ctx := context.Background()
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	blocked, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
	if err != nil || !found || len(blocked.Tasks) != 1 || len(blocked.Dispatches) != 4 {
		t.Fatalf("automatic execution did not reach four attempts: found=%t err=%v tasks=%+v dispatches=%+v", found, err, blocked.Tasks, blocked.Dispatches)
	}
	prior := blocked.Dispatches[3]
	if blocked.Tasks[0].Status != core.DevelopmentTaskStatusBlocked ||
		prior.Status != core.ComplexExecutionDispatchBlocked || prior.ReasonCode != "BUILDER_BUDGET_EXHAUSTED" {
		t.Fatalf("automatic execution did not stop at Builder exhaustion: task=%+v prior=%+v", blocked.Tasks[0], prior)
	}
	beforeSends := countProjectBuilderSends(h)
	if beforeSends != 4 {
		t.Fatalf("initial Builder sends=%d, want 4", beforeSends)
	}
	if _, err := f.s.RequestExtraBuilderTurn(ctx, child.Requirement.ID, blocked.Tasks[0].DevelopmentTaskID); err != nil {
		t.Fatal(err)
	}
	if countProjectBuilderSends(h) != beforeSends {
		t.Fatal("recovery request sent a Builder before native approval")
	}
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindExtraBuilderTurn, core.HumanDecisionApprove)
	if got := countProjectBuilderSends(h); got != beforeSends+1 {
		t.Fatalf("native approval did not automatically wake the execution: Builder sends=%d, want %d", got, beforeSends+1)
	}
	recovered, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
	if err != nil || !found || len(recovered.Dispatches) != 5 {
		t.Fatalf("recovery did not produce exactly one extra attempt: found=%t err=%v dispatches=%+v", found, err, recovered.Dispatches)
	}
	budget := projectBuilderBudget(recovered, recovered.Tasks[0].ID)
	if budget == nil || budget.AuthorizedExtraTurns != 1 || budget.UsedTurns != 5 || budget.MaxTurns != 4 || budget.MaxReworkCount != 3 {
		t.Fatalf("automatic recovery changed frozen budgets or missed the extra turn: %+v", budget)
	}
	old, ok := projectDispatchByID(recovered, prior.ID)
	if !ok || old.Status != core.ComplexExecutionDispatchRework || old.ReasonCode != prior.ReasonCode ||
		old.CandidateCommitSHA != prior.CandidateCommitSHA || old.SettledAt == nil || !old.SettledAt.Equal(*prior.SettledAt) {
		t.Fatalf("automatic recovery rewrote the original failure: before=%+v after=%+v", prior, old)
	}
	if len(recovered.Reviews) != len(blocked.Reviews)+1 {
		t.Fatal("automatic recovery lost the previous review history")
	}
	for _, review := range blocked.Reviews {
		retained, ok := projectReviewByID(recovered, review.ID)
		if !ok || retained.Verdict != review.Verdict || retained.ReasonCode != review.ReasonCode || retained.SettledAt == nil ||
			!retained.SettledAt.Equal(*review.SettledAt) {
			t.Fatalf("automatic recovery rewrote a previous review: before=%+v after=%+v", review, retained)
		}
	}
}
