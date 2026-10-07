package cleardev

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestBoundedMailStorageCannotDispatchWithoutGrantOrRewriteUsage(t *testing.T) {
	f, _ := newBoundedMailFixture(t, 3)
	f.confirm(t)
	e := boundedExecution(t, f)
	now := f.clock()
	stepID := "unauthorized-extra-step"
	command := core.CreateComplexExecutionDispatchCommand{
		Dispatch:  core.ComplexExecutionDispatch{ID: "unauthorized-extra", ExecutionRunID: e.Run.ID, ComplexExecutionTaskID: e.Tasks[0].ID, DevelopmentTaskID: e.Tasks[0].DevelopmentTaskID, Round: 3, BaseCommitSHA: e.Dispatches[0].BaseCommitSHA, AgentStepID: stepID, Status: core.ComplexExecutionDispatchPending, CreatedAt: now},
		AgentStep: core.AgentStep{ID: stepID, RoleBindingID: e.Run.BuilderRoleBindingID, Kind: core.ComplexExecutionAgentStepBuilderTask, RequestID: "unauthorized-extra", ClientMessageID: "unauthorized-extra-message", PromptSHA256: coreDigest([]byte("test rejected request")), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now},
	}
	if _, _, err := f.store.CreateClearDevComplexExecutionDispatch(context.Background(), command); err == nil {
		t.Fatal("fourth implementation accepted without desktop approval")
	}
	after := boundedExecution(t, f)
	if !reflect.DeepEqual(e.Dispatches, after.Dispatches) || !reflect.DeepEqual(e.AgentSteps, after.AgentSteps) {
		t.Fatal("rejected dispatch left partial steps")
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range []string{
		`UPDATE cleardev_mail_attempt_slots SET attempt_kind='DEVELOPMENT'`,
		`DELETE FROM cleardev_mail_attempt_slots`,
		`UPDATE cleardev_complex_exception_budgets SET used_turns=0 WHERE used_turns>0`,
		`UPDATE cleardev_complex_exception_budgets SET max_turns=100`,
		`UPDATE cleardev_complex_execution_task_attempts SET round=0 WHERE round=2`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("accepted forbidden history rewrite: %s", statement)
		}
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), e.Run.ID)
	if err != nil || len(slots) != 3 {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	assertMailNotCompleted(t, f)
}

func TestBoundedMailDowngradeCannotEraseAttemptOrGrantHistory(t *testing.T) {
	f, _ := newBoundedMailFixture(t, 3)
	f.confirm(t)
	applyFakeDesktopDecision(t, f.store, f.service, f.clock, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt, core.HumanDecisionApprove)
	before := boundedExecution(t, f)
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), before.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var beforeVersion int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(context.Background(), 131); err == nil {
		t.Fatal("downgrade erased bounded attempt authorization")
	}
	var version int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != beforeVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	after := boundedExecution(t, f)
	afterSlots, err := f.store.ListClearDevMailAttempts(context.Background(), before.Run.ID)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(slots, afterSlots) {
		t.Fatal("refused downgrade changed persisted evidence")
	}
}
