package cleardev

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func TestPlanningStepContinuationPreservesSnapshotAndBlocksDowngrade(t *testing.T) {
	ctx := context.Background()
	f := newPlanningContinuationFixture(t, true)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q := gen.New(db)
	before, err := q.GetClearDevComplexAgentStep(ctx, f.step.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	f.s.runBackground = func(func()) {}
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, f.input()); err != nil {
		t.Fatal(err)
	}
	var saved []byte
	if err := db.QueryRow(`SELECT old_step_json FROM cleardev_planning_step_recoveries WHERE id=?`, f.input().RequestID).Scan(&saved); err != nil || !bytes.Equal(raw, saved) {
		t.Fatalf("reopen lost the exact original compatibility step: %v", err)
	}
	for _, statement := range []string{
		`UPDATE cleardev_planning_step_recoveries SET original_summary='changed' WHERE logical_step_id=?`,
		`DELETE FROM cleardev_planning_step_recoveries WHERE logical_step_id=?`,
		`UPDATE cleardev_complex_agent_steps SET send_status='SETTLED' WHERE id=?`,
	} {
		if _, err := db.Exec(statement, f.step.ID); err == nil {
			t.Fatal("history rewrite or forged completion succeeded")
		}
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var beforeVersion int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 179); err == nil {
		t.Fatal("downgrade removed stored planning recovery history")
	}
	var version, facts, invalid int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_planning_step_recoveries`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&invalid); err != nil || version != beforeVersion || facts != 1 || invalid != 0 {
		t.Fatalf("failed downgrade altered version/history/foreign keys: %d %d %d %v", version, facts, invalid, err)
	}
}
