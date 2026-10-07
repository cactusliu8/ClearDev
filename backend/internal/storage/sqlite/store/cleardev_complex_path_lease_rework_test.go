package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// A failed first candidate releases its paths. The same task must then be
// allowed to lease those paths for a new dispatch without erasing the first
// dispatch's history. The held-path index still rejects overlapping owners.
func TestComplexReworkReacquiresReleasedPaths(t *testing.T) {
	store, state := seedControlledExceptionFlow(t)
	ctx := context.Background()
	task := state.tasks[0]
	first, created, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "path-lease-round-0")
	if err != nil || !created {
		t.Fatalf("first dispatch: created=%t err=%v", created, err)
	}
	if _, err := store.AppendClearDevComplexExecutionCandidate(ctx, core.AppendComplexExecutionCandidateCommand{
		ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: first.ID, Round: 0, BaseCommitSHA: state.initialBase,
		Candidate: core.CandidateObservation{ID: "path-lease-candidate-0", DevelopmentTaskID: task.DevelopmentTaskID, AOSessionID: state.builder.AOSessionID, CommitSHA: strings.Repeat("c", 40), ObservedAt: state.now.Add(4 * time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyClearDevComplexExecutionFailure(ctx, state.run.ID, task.ID, first.ID, false, "CHECK_FAILED", state.now.Add(5*time.Second)); err != nil {
		t.Fatalf("record first failed candidate: %v", err)
	}
	before, found, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
	if err != nil || !found || before.Exception == nil {
		t.Fatalf("read released leases: found=%t err=%v", found, err)
	}
	old := map[string]bool{}
	for _, lease := range before.Exception.PathLeases {
		if lease.ComplexExecutionTaskID == task.ID {
			if lease.Status != "RELEASED" {
				t.Fatalf("first lease stayed held: %+v", lease)
			}
			old[lease.ID] = true
		}
	}
	if len(old) == 0 {
		t.Fatal("fixture did not lease a shared or generated path")
	}
	second, created, err := dispatchComplexExecutionTask(ctx, store, state, task, 1, state.initialBase, "path-lease-round-1")
	if err != nil || !created {
		t.Fatalf("rework dispatch: created=%t err=%v", created, err)
	}
	if _, created, err := dispatchComplexExecutionTask(ctx, store, state, task, 1, state.initialBase, second.ID); err != nil || created {
		t.Fatalf("repeat dispatch: created=%t err=%v", created, err)
	}
	after, found, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
	if err != nil || !found || after.Exception == nil {
		t.Fatalf("read rework leases: found=%t err=%v", found, err)
	}
	held := 0
	var heldPath string
	for _, lease := range after.Exception.PathLeases {
		if old[lease.ID] && lease.Status != "RELEASED" {
			t.Fatalf("historical lease changed: %+v", lease)
		}
		if lease.Status == "HELD" && lease.ComplexExecutionTaskID == task.ID {
			if old[lease.ID] || !strings.Contains(lease.ID, second.ID) {
				t.Fatalf("rework reused old lease identity: %+v", lease)
			}
			held++
			heldPath = lease.Path
		}
	}
	if held != len(old) {
		t.Fatalf("rework held %d paths, want %d", held, len(old))
	}
	// A distinct holder cannot bypass the unique active-path constraint just
	// because the first round's row is now RELEASED.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(state.dataDir, "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, `INSERT INTO cleardev_complex_exception_path_leases (id,execution_run_id,complex_execution_task_id,path,status,created_at) VALUES (?,?,?,?,?,?)`, "competing-lease", state.run.ID, task.ID, heldPath, "HELD", state.now.Add(7*time.Second))
	if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("overlapping held lease was accepted or misclassified: %v", err)
	}
}
