package cleardev

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func failedApprovedStoppedCheck(t *testing.T) (*projectPlanningFixture, *stoppedCheckHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	ctx := context.Background()
	f, h, before := stoppedCheckFixture(t)
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, stoppedCheckInput(t, f, h)); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ApplyHumanDecisionResult(ctx, coordinationRepairResult(t, f, stoppedCheckRequest(t, f), core.HumanDecisionApprove)); err != nil {
		t.Fatal(err)
	}
	h.failures = 1
	driveStoppedTechnicalFailure(t, f, h)
	return f, h, before
}
func driveStoppedTechnicalFailure(t *testing.T, f *projectPlanningFixture, h *stoppedCheckHarness) core.ComplexExecutionSnapshot {
	t.Helper()
	ctx := context.Background()
	for range 20 {
		x, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if x.Tasks[0].Status == core.DevelopmentTaskStatusBlocked {
			return x
		}
		_, _, _ = f.s.advanceComplexStandardExecution(ctx, h.requirementID)
	}
	t.Fatal("authorized check did not settle blocked")
	return core.ComplexExecutionSnapshot{}
}
func technicalCheckInput(t *testing.T, f *projectPlanningFixture, h *stoppedCheckHarness, requestID string) WorkflowRecoveryInput {
	t.Helper()
	v, err := f.s.GetWorkflowRecovery(context.Background(), h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range v.Options {
		if o.Action == core.RecoveryRetryCheck && o.UnavailableReason == "" {
			return WorkflowRecoveryInput{RequestID: requestID, ExecutionRunID: v.ExecutionRunID, Action: o.Action, TargetID: o.TargetID, Supplement: "Retry the same ended infrastructure check under the existing candidate continuation."}
		}
	}
	t.Fatal("missing technical retry", v.Options)
	return WorkflowRecoveryInput{}
}
func TestStoppedCheckTechnicalRetriesHaveNoCountCapOrAdditionalVote(t *testing.T) {
	ctx := context.Background()
	f, h, before := failedApprovedStoppedCheck(t)
	initial, _, _ := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	for ordinal := 2; ordinal <= 5; ordinal++ {
		input := technicalCheckInput(t, f, h, fmt.Sprintf("technical-retry-%d", ordinal))
		if input.TargetID != h.failedID {
			t.Fatal("selected an older source", input.TargetID, h.failedID)
		}
		calls := len(h.checkRequests)
		h.receiptSettled = false
		if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err == nil {
			t.Fatal("unknown executor result authorized retry")
		}
		h.receiptSettled = true
		for range 2 {
			if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
				t.Fatal("technical retry", ordinal, err)
			}
		}
		if len(h.checkRequests) != calls {
			t.Fatal("request directly executed a check")
		}
		pending, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending.PlannerRuntime.CheckRecoveries) != 1 {
			t.Fatal("new native grant required")
		}
		retry := ""
		for _, r := range pending.WorkflowRecoveries {
			if r.ID == input.RequestID {
				retry = r.SuccessorID
			}
		}
		if retry == "" {
			t.Fatal("technical successor missing")
		}
		reopened, err := sqlite.Open(f.dir)
		if err != nil {
			t.Fatal(err)
		}
		_, registered, checkErr := reopened.ValidateClearDevStoppedCheckBeforeRun(ctx, h.requirementID, retry)
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
		if checkErr != nil || !registered {
			t.Fatal("restart lost technical retry", ordinal, registered, checkErr)
		}
		if ordinal < 5 {
			h.failures = 1
			driveStoppedTechnicalFailure(t, f, h)
		} else {
			after := driveProjectFlow(t, f, h.requirementID, false)
			if after.Run.CompletedAt == nil {
				t.Fatal("normal review chain did not finish")
			}
		}
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Dispatches) != len(before.Dispatches) || len(after.PlannerRuntime.Requests) != len(before.PlannerRuntime.Requests) || !reflect.DeepEqual(after.PlannerRuntime.Decisions, before.PlannerRuntime.Decisions) || after.Tasks[0].ReworkCount != before.Tasks[0].ReworkCount {
		t.Fatal("technical retries changed STOP/round/counter")
	}
	for _, old := range initial.CheckRuns {
		for _, now := range after.CheckRuns {
			if old.ID == now.ID && !reflect.DeepEqual(old, now) {
				t.Fatal("original check rewritten", old.ID)
			}
		}
	}
	counts := map[string]int{}
	for _, call := range h.checkRequests {
		counts[call.RunID]++
		if counts[call.RunID] != 1 {
			t.Fatal("external check repeated", call.RunID)
		}
	}
	for _, old := range before.Exception.Budgets {
		for _, now := range after.Exception.Budgets {
			if old.ID == now.ID && (old.MaxTurns != now.MaxTurns || old.AuthorizedExtraTurns != now.AuthorizedExtraTurns || old.MaxReworkCount != now.MaxReworkCount || (old.RoleKind == core.ComplexExceptionBudgetBuilder && old.UsedTurns != now.UsedTurns)) {
				t.Fatal("budget refunded/widened")
			}
		}
	}
	db := stoppedCheckRawDB(t, f)
	var nativeCount, grants int
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_stopped_check_requests`).Scan(&nativeCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_stopped_check_grants`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if nativeCount != 1 || grants != 1 {
		t.Fatal("technical retries added human approval", nativeCount, grants)
	}
}

func TestStoppedCheckTechnicalRetryConcurrentAndLateIdentity(t *testing.T) {
	ctx := context.Background()
	f, h, _ := failedApprovedStoppedCheck(t)
	input := technicalCheckInput(t, f, h, "technical-race")
	oldCandidate := h.currentCandidate
	h.currentCandidate = forty("f")
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err == nil {
		t.Fatal("changed candidate allowed retry")
	}
	h.currentCandidate = oldCandidate
	results := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			call := input
			call.RequestID = fmt.Sprintf("technical-race-%d", i)
			_, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, call)
			results <- err
		}(i)
	}
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("competing requests must create one successor", successes)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	retry := ""
	for _, r := range after.WorkflowRecoveries {
		if r.TargetID == input.TargetID {
			retry = r.SuccessorID
		}
	}
	if retry == "" {
		t.Fatal("technical successor missing")
	}
	grant, err := f.store.ReadClearDevStoppedCheckRecovery(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := f.store.GetSession(ctx, domain.SessionID(grant.Binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.Metadata.ProviderConversationID = "changed-after-technical-retry"
	if err := f.store.UpdateSession(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.checkStoppedCheckBeforeRun(ctx, h.requirementID, retry); err == nil {
		t.Fatal("technical check admitted stale identity")
	}
	if _, err := f.store.StartClearDevComplexExecutionCheckRun(ctx, retry, f.s.now()); err == nil {
		t.Fatal("SQL started technical check with stale identity")
	}
	if err := f.store.UpdateSession(ctx, original); err != nil {
		t.Fatal(err)
	}
	h.receiptSettled = false
	if _, err := f.s.checkStoppedCheckBeforeRun(ctx, h.requirementID, retry); err == nil {
		t.Fatal("technical check admitted unknown predecessor")
	}
	h.receiptSettled = true
	profile, err := f.s.checkStoppedCheckBeforeRun(ctx, h.requirementID, retry)
	if err != nil || profile != core.NodeCheckSmallThreadsV1 {
		t.Fatal("technical check lost fixed profile", profile, err)
	}
}

func TestStoppedCheckTechnicalRetryDoesNotRepeatBusinessFailure(t *testing.T) {
	ctx := context.Background()
	f, h, before := failedApprovedStoppedCheck(t)
	input := technicalCheckInput(t, f, h, "technical-business-failure")
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	h.businessFailure = true
	after := driveStoppedTechnicalFailure(t, f, h)
	if len(after.Dispatches) != len(before.Dispatches) || after.Tasks[0].ReworkCount != before.Tasks[0].ReworkCount {
		t.Fatal("business failure dispatched another Builder or refunded a round")
	}
	v, err := f.s.GetWorkflowRecovery(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range v.Options {
		if o.Action == core.RecoveryRetryCheck || o.Action == core.RecoveryRequestStoppedCheck {
			t.Fatal("business failure became technical retry", o)
		}
	}
	found := false
	for _, c := range after.CheckRuns {
		if c.RetryOrdinal == 2 {
			found = c.Status == core.ComplexExecutionCheckRunSettled && c.Result == core.EvidenceResultFail
		}
	}
	if !found {
		t.Fatal("business result not preserved as failed assertion")
	}
}
