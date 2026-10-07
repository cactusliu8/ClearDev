package cleardev

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestPlannerRuntimeLateReplyAfterCancellationCannotApply(t *testing.T) {
	ctx := context.Background()
	f, h := newRuntimeFixture(t, "dependency")
	h.afterRuntime = func(ctx context.Context, _ string) error {
		return f.service.CancelRequirement(ctx, f.view.Requirement.ID, "deterministic test: product cancelled while Planner turn was in flight")
	}
	f.confirm(t)
	execution, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil || execution.PlannerRuntime == nil || len(execution.PlannerRuntime.Requests) != 1 || h.runtimeTurns != 1 {
		t.Fatalf("did not reach in-flight cancellation: %v", err)
	}
	eventID := execution.PlannerRuntime.Events[0].ID
	// Re-enter through the store's guarded preparation boundary. Whether the
	// cancelled service drained the late reply or stopped first, no old decision
	// may become permission to amend, dispatch, integrate or complete.
	if _, _, err := f.store.PrepareClearDevPlannerRuntime(ctx, eventID, f.clock()); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil || len(after.PlannerRuntime.Decisions) != 1 || after.PlannerRuntime.Decisions[0].Outcome != "STALE" ||
		len(after.PlannerRuntime.Amendments) != 0 || len(after.Dispatches) != 1 || after.FinalReview != nil || after.Integration != nil || after.Run.CompletedAt != nil {
		t.Fatalf("late decision survived cancellation: %v history=%+v", err, after.PlannerRuntime)
	}
	f.reopen(t)
	attachRuntimeHarness(f, h)
	if _, changed, err := f.store.PrepareClearDevPlannerRuntime(ctx, eventID, f.clock()); err != nil || changed {
		t.Fatalf("restart regenerated stale coordination: changed=%v err=%v", changed, err)
	}
}

func TestPlannerRuntimeQuotaSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	f, h := newRuntimeFixture(t, "dependency")
	h.reportAll, h.decision = true, core.PlannerRuntimeContinue
	mutate := f.harness.complexPlanMutator
	f.harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
		mutate(plan)
		third := plan.Tasks[1]
		third.Key = "final-summary"
		third.DependencyKeys = []string{plan.Tasks[1].Key}
		// Reuse the approved summary check/path catalog for this serial quota
		// fixture; inventing a check for a new path must fail initial admission.
		third.Objective = "Prove the summary's remaining boundary behavior without changing its interface."
		plan.Tasks = append(plan.Tasks, third)
	}
	f.harness.candidateSHAs = []string{forty("b"), forty("c"), forty("d"), forty("e")}
	f.confirm(t)
	before, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	phase, reason := core.DeriveComplexExecutionPhase(before)
	if phase != core.ComplexExecutionNeedsHuman || reason != core.ReasonPlannerRuntimeBudget || before.PlannerRuntime == nil ||
		len(before.PlannerRuntime.Events) != 3 || len(before.PlannerRuntime.Requests) != 2 || len(before.PlannerRuntime.Decisions) != 3 ||
		h.runtimeTurns != 2 || before.FinalReview != nil || before.Integration != nil || before.Run.CompletedAt != nil {
		t.Fatalf("coordination exceeded quota or falsely completed: phase=%s reason=%s turns=%d dispatches=%d reviews=%d", phase, reason, h.runtimeTurns, len(before.Dispatches), len(before.Reviews))
	}
	counts := f.counts()
	f.reopen(t)
	attachRuntimeHarness(f, h)
	if err := f.service.ResumeComplexFlows(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ResumeComplexStandardExecutions(ctx); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil || !reflect.DeepEqual(before, after) || f.counts() != counts || h.runtimeTurns != 2 {
		t.Fatalf("restart refunded coordination quota or dispatched work: %v", err)
	}
}

func TestPlannerRuntimePreservesTaskReviewerRework(t *testing.T) {
	f, h := newRuntimeFixture(t, "dependency")
	h.decision = core.PlannerRuntimeContinue
	f.harness.reworkOnce = true
	f.confirm(t)
	execution, _, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	phase, reason := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionCompleted || len(execution.Dispatches) != 3 || len(execution.Reviews) != 3 ||
		execution.Tasks[0].ReworkCount != 1 || h.runtimeTurns != 2 || execution.FinalReview == nil || execution.FinalReview.Verdict != "PASS" {
		t.Fatalf("coordination lost task-level rework: phase=%s reason=%s dispatches=%d reviews=%d history=%+v", phase, reason, len(execution.Dispatches), len(execution.Reviews), execution.PlannerRuntime)
	}
	if execution.Reviews[0].Verdict != "REWORK" || execution.Dispatches[0].ID == execution.Dispatches[1].ID {
		t.Fatal("original failure or consumed attempt was replaced")
	}
}

// Positive records are obtained through the normal fixture. These SQL calls
// are attacks against a temporary test DB, not a production approval bypass.
func TestPlannerRuntimeSQLHistoryRejectsRewrite(t *testing.T) {
	ctx := context.Background()
	f, _ := newRuntimeFixture(t, "dependency")
	f.confirm(t)
	before, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil || before.PlannerRuntime == nil || len(before.PlannerRuntime.Amendments) != 1 {
		t.Fatalf("fixture did not apply an actual amendment: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	var beforeEvents int
	if err := db.QueryRow("SELECT count(*) FROM change_log").Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"cleardev_planner_runtime_events", "cleardev_planner_runtime_requests", "cleardev_planner_runtime_decisions", "cleardev_planner_runtime_task_amendments"} {
		for _, statement := range []string{
			fmt.Sprintf("UPDATE %s SET created_at='2099-01-01'", table),
			fmt.Sprintf("DELETE FROM %s", table),
			fmt.Sprintf("INSERT OR REPLACE INTO %s SELECT * FROM %s", table, table),
		} {
			if _, err := db.Exec(statement); err == nil {
				t.Fatalf("runtime history was rewritable: %s", statement)
			}
		}
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("forbidden writes changed contracts or completion: %v", err)
	}
	var afterEvents int
	if err := db.QueryRow("SELECT count(*) FROM change_log").Scan(&afterEvents); err != nil || afterEvents != beforeEvents {
		t.Fatalf("rejected writes emitted CDC: before=%d after=%d err=%v", beforeEvents, afterEvents, err)
	}
	var violations int
	if err := db.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("runtime history lost referential integrity: count=%d err=%v", violations, err)
	}
}
