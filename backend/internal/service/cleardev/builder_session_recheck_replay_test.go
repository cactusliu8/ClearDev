package cleardev

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func restoreTestRecheckBuilder(f *projectPlanningFixture) func(context.Context, domain.SessionID) (string, error) {
	return func(ctx context.Context, sid domain.SessionID) (string, error) {
		record, _, err := f.store.GetSession(ctx, sid)
		if err != nil {
			return "", err
		}
		record.Activity.State = domain.ActivityIdle
		return "native", f.store.UpdateSession(ctx, record)
	}
}

func reopenBuilderRecheck(t *testing.T, f *projectPlanningFixture, h *workflowBlockedBuilderHarness) {
	t.Helper()
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
	f.s.stepTimeout = 10 * time.Second
	f.s.runBackground = func(func()) {}
	h.store, h.service = f.store, f.s
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h
	f.s.finalReviews, f.s.resultPreview = f.store, h.trial
}

func TestBuilderSessionRecheckReopenReplaysExistingRegistration(t *testing.T) {
	ctx := context.Background()
	f, h, id, before, _ := newBuilderSessionRecheckFixture(t)
	input := builderSessionRecheckInput(t, f, id, "reopen-unsent-registration")
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	reopenBuilderRecheck(t, f, h)
	f.s.restoreOriginalAgentSession = restoreTestRecheckBuilder(f)
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.Options) != 1 || view.Options[0].UnavailableReason != "BUILDER_RECHECK_REGISTERED" {
		t.Fatalf("no way to continue the original registration: %+v %v", view.Options, err)
	}
	if view.Diagnosis == nil || !view.Diagnosis.Current || view.Diagnosis.ReadError != "" {
		t.Fatalf("registered continuation was disabled by its diagnosis: %+v", view.Diagnosis)
	}
	other := input
	other.RequestID = "different-registration"
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, other); err == nil {
		t.Fatal("registered continuation admitted a new request")
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if len(after.Dispatches) != len(before.Dispatches) || after.Run.CompletedAt == nil || len(after.WorkflowRecoveries) != len(before.WorkflowRecoveries)+1 {
		t.Fatal("reopen did not reuse the original registration and dispatch")
	}
}

func TestBuilderSessionRecheckInterruptedRestoreNeverRestoresTwice(t *testing.T) {
	ctx := context.Background()
	f, h, id, _, _ := newBuilderSessionRecheckFixture(t)
	input := builderSessionRecheckInput(t, f, id, "interrupted-original-restore")
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	e, _, _ := f.store.GetClearDevComplexExecution(ctx, id)
	task, dispatch, _ := activeComplexExecutionDispatch(e)
	binding, _ := complexExecutionBindingByID(e, dispatch.BuilderRoleBindingID)
	cancelled, cancel := context.WithCancel(ctx)
	defer cancel()
	restores := 0
	f.s.restoreOriginalAgentSession = func(context.Context, domain.SessionID) (string, error) {
		restores++
		cancel()
		return "", cancelled.Err()
	}
	if _, _, _, err := f.s.recheckWorkflowBuilder(cancelled, e, task, dispatch, binding, "notes-project"); err == nil {
		t.Fatal("cancelled restore was recorded as completed")
	}
	checks, err := f.store.ListClearDevBuilderSessionChecks(ctx, e.Run.ID)
	if err != nil || len(checks) != 1 || checks[0].Checkpoint != "STARTED" {
		t.Fatalf("no durable restoration boundary: %+v %v", checks, err)
	}
	reopenBuilderRecheck(t, f, h)
	f.s.restoreOriginalAgentSession = func(ctx context.Context, sid domain.SessionID) (string, error) {
		restores++
		return restoreTestRecheckBuilder(f)(ctx, sid)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	stoppedWorkflow(t, f, id)
	assertBuilderRecheckFailure(t, f, id, input.RequestID, "RESTORE", "BUILDER_RESTORE_UNCONFIRMED")
	if restores != 1 {
		t.Fatal("replay repeated an unconfirmed native restoration")
	}
	retry := builderSessionRecheckInput(t, f, id, "explicit-after-interrupted-restore")
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, retry); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if restores != 2 || after.Run.CompletedAt == nil {
		t.Fatal("explicit recheck could not recover after an interrupted native restore")
	}
}

func TestBuilderSessionRecheckReadyThenExitRequiresNewRequest(t *testing.T) {
	ctx := context.Background()
	f, h, id, _, original := newBuilderSessionRecheckFixture(t)
	input := builderSessionRecheckInput(t, f, id, "ready-before-exit")
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	restores := 0
	f.s.restoreOriginalAgentSession = func(ctx context.Context, sid domain.SessionID) (string, error) {
		restores++
		return restoreTestRecheckBuilder(f)(ctx, sid)
	}
	e, _, _ := f.store.GetClearDevComplexExecution(ctx, id)
	task, dispatch, _ := activeComplexExecutionDispatch(e)
	binding, _ := complexExecutionBindingByID(e, dispatch.BuilderRoleBindingID)
	_, ready, _, err := f.s.recheckWorkflowBuilder(ctx, e, task, dispatch, binding, "notes-project")
	if err != nil || !ready || restores != 1 {
		t.Fatalf("initial restore did not finish: %v", err)
	}
	record, _, _ := f.store.GetSession(ctx, original.ID)
	record.Activity.State = domain.ActivityExited
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	calls := len(h.relays)
	stoppedWorkflow(t, f, id)
	assertBuilderRecheckFailure(t, f, id, input.RequestID, "RESTORE", "BUILDER_RESTORE_UNCONFIRMED")
	if restores != 1 || len(h.relays) != calls {
		t.Fatal("old READY receipt restored or sent again after the session exited")
	}
}

type synchronizedRecheckInspector struct {
	*workflowBlockedBuilderHarness
	mu sync.Mutex
}

func (h *synchronizedRecheckInspector) InspectProjectSource(ctx context.Context, path, branch string) (ports.ClearDevProjectSource, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.workflowBlockedBuilderHarness.InspectProjectSource(ctx, path, branch)
}

func TestBuilderSessionRecheckConcurrentRequestsKeepOneRegistration(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(map[bool]string{true: "same-request", false: "different-requests"}[same], func(t *testing.T) {
			ctx := context.Background()
			f, h, id, before, _ := newBuilderSessionRecheckFixture(t)
			f.s.inspector = &synchronizedRecheckInspector{workflowBlockedBuilderHarness: h}
			input := builderSessionRecheckInput(t, f, id, "concurrent-recheck")
			other := input
			if !same {
				other.RequestID = "competing-recheck"
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, request := range []WorkflowRecoveryInput{input, other} {
				go func() {
					<-start
					_, err := f.s.RequestWorkflowRecovery(ctx, id, request)
					results <- err
				}()
			}
			close(start)
			errorsCount := 0
			for range 2 {
				if <-results != nil {
					errorsCount++
				}
			}
			if same && errorsCount != 0 || !same && errorsCount != 1 {
				t.Fatalf("unexpected idempotency results: errors=%d same=%v", errorsCount, same)
			}
			e, _, err := f.store.GetClearDevComplexExecution(ctx, id)
			if err != nil || len(e.WorkflowRecoveries) != len(before.WorkflowRecoveries)+1 || len(e.Dispatches) != len(before.Dispatches) {
				t.Fatal("concurrent recheck changed the original step or added registrations")
			}
			f.s.restoreOriginalAgentSession = restoreTestRecheckBuilder(f)
			after := driveWorkflowRecovery(t, f, id)
			if after.Run.CompletedAt == nil {
				t.Fatal("winning request did not continue the task")
			}
		})
	}
}

func TestBuilderSessionRecheckHistoryCannotBeOverwritten(t *testing.T) {
	ctx := context.Background()
	f, _, id, before, _ := newBuilderSessionRecheckFixture(t)
	input := builderSessionRecheckInput(t, f, id, "immutable-recheck")
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	stoppedWorkflow(t, f, id)
	checks, err := f.store.ListClearDevBuilderSessionChecks(ctx, before.Run.ID)
	if err != nil || len(checks) != 2 {
		t.Fatal("recheck history was not saved")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, query := range []string{
		`UPDATE cleardev_builder_session_checks SET outcome='READY',reason_code='' WHERE recovery_id=?`,
		`DELETE FROM cleardev_builder_session_checks WHERE recovery_id=?`,
	} {
		if _, err := db.Exec(query, input.RequestID); err == nil {
			t.Fatal("a stored recheck could be overwritten or erased")
		}
	}
	changed := checks[1]
	changed.Outcome, changed.Stage, changed.ReasonCode = "READY", "READY", ""
	if _, err := f.store.RecordClearDevBuilderSessionCheck(ctx, changed); err == nil {
		t.Fatal("the store changed an old failure into readiness")
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var beforeVersion, afterVersion int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 180); err == nil {
		t.Fatal("downgrade erased original Builder recheck evidence")
	}
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&afterVersion); err != nil || afterVersion != beforeVersion {
		t.Fatal("refused downgrade changed the schema ledger")
	}
	after, err := f.store.ListClearDevBuilderSessionChecks(ctx, before.Run.ID)
	if err != nil || !reflect.DeepEqual(checks, after) {
		t.Fatal("failed history edits or downgrade changed evidence")
	}
}
