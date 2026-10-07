package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func init() {
	shippedMigrations[213] = "0213_cleardev_workflow_failure_receipts.sql"
	shippedMigrations[214] = "0214_cleardev_failure_coordination.sql"
	shippedMigrations[215] = "0215_cleardev_invalid_discussion_continuation.sql"
}

func TestUnifiedFailureMigrationsPreserveAndRestorePriorGuards(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 202)
	names := []string{"cleardev_planner_runtime_event_insert", "cleardev_planner_runtime_amendment_insert", "cleardev_product_discussion_settle_guard", "cleardev_planning_step_recovery_current", "cleardev_planner_runtime_decision_insert", "cleardev_complex_execution_attempt_insert_valid", "cleardev_product_discussion_cdc_update"}
	old := map[string]string{}
	for _, name := range names {
		var body string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name=?`, name).Scan(&body); err != nil {
			t.Fatal(name, err)
		}
		old[name] = body
	}
	upTo(t, db, 215)
	for _, name := range []string{"cleardev_workflow_failure_receipts", "cleardev_failure_coordination_sources"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=? AND type='table'`, name).Scan(&count); err != nil || count != 1 {
			t.Fatal(name, count, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cleardev_failure_coordination_sources(event_id,execution_run_id,dispatch_id,source_step_id,source_message_sha256,working_tree_sha256,bridge_kind,created_at) VALUES('fake','fake','fake','fake',printf('%064d',0),printf('%064d',0),'BUILDER_DIAGNOSIS','2026-10-06')`); err == nil {
		t.Fatal("unbound source became a Planner request")
	}
	if err := migrateDown(202, db); err != nil {
		t.Fatal(err)
	}
	for name, want := range old {
		var body string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name=?`, name).Scan(&body); err != nil || body != want {
			t.Fatal("original guard was not restored", name, err)
		}
	}
	upTo(t, db, 215)
	var fk int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Fatal("foreign key violations", fk, err)
	}
}

// Run a deliberately broken copy, not the production migration. All schema
// changes and the version row must roll back even when the last statement fails.
func TestUnifiedFailureMigrationsAtomicFailure(t *testing.T) {
	for _, version := range []int64{213, 214, 215} {
		t.Run(shippedMigrations[version], func(t *testing.T) {
			db := openMigrationTestDB(t)
			upTo(t, db, version-1)
			before := workflowMigrationSchema(t, db)
			previousVersion := gooseVersion(t, db)
			files := fstest.MapFS{}
			entries, err := migrationsFS.ReadDir("migrations")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				raw, err := migrationsFS.ReadFile("migrations/" + entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				if entry.Name() == shippedMigrations[version] {
					marker := "-- +goose StatementEnd\n\n-- +goose Down"
					if strings.Count(string(raw), marker) != 1 {
						t.Fatal("migration no longer has the expected transaction boundary")
					}
					raw = []byte(strings.Replace(string(raw), marker, "SELECT * FROM deliberately_missing_failure_migration_table;\n"+marker, 1))
				}
				files[entry.Name()] = &fstest.MapFile{Data: raw}
			}
			provider, err := goose.NewProvider(goose.DialectSQLite3, db, files)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.UpTo(context.Background(), version); err == nil {
				t.Fatal("injected late failure was not reached")
			}
			if gooseVersion(t, db) != previousVersion || !reflect.DeepEqual(before, workflowMigrationSchema(t, db)) {
				t.Fatal("failed migration changed schema or version")
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed migration damaged foreign keys", count, err)
			}
		})
	}
}

func workflowMigrationSchema(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT type||':'||name,sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		result[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestUnifiedFailureMigrationsPreserveRowsAndRefuseEvidenceLoss(t *testing.T) {
	db := openMigrationTestDB(t)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 202)
	tables := []string{"sessions", "cleardev_development_projects", "cleardev_contract_versions", "cleardev_work_items", "cleardev_human_decision_requests", "cleardev_agent_attempt_events", "change_log"}
	before := map[string][][]string{}
	for _, table := range tables {
		before[table] = preSendMigrationRows(t, db, table)
	}
	upTo(t, db, 215)
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], preSendMigrationRows(t, db, table)) {
			t.Fatal("migration changed historical row or rowid", table)
		}
	}
	var requirementID string
	if err := db.QueryRow(`SELECT id FROM cleardev_development_projects LIMIT 1`).Scan(&requirementID); err != nil {
		t.Fatal(err)
	}
	receipt := core.WorkflowFailureReceipt{
		RequirementID: requirementID, SourceKind: core.FailureSourceOperation, SourceID: "test-operation:version",
		Role: core.TrustedOwnerControlPlane, ReasonCode: "CONTROL_OPERATION_FAILED", ProblemCode: "UNKNOWN",
		Summary: "Synthetic internal failure; not authority to retry.", BindingSHA256: strings.Repeat("a", 64),
		ObservedAt: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
	}
	receipt.ID = core.FailureSourceKey(receipt.RequirementID, receipt.SourceKind, receipt.SourceID)
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO cleardev_workflow_failure_receipts(id,requirement_id,source_kind,source_id,receipt_json,receipt_sha256,created_at) VALUES(?,?,?,?,?,?,?)`
	args := []any{receipt.ID, requirementID, receipt.SourceKind, receipt.SourceID, string(raw), strings.Repeat("b", 64), receipt.ObservedAt}
	if _, err := db.Exec(insert, args...); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE cleardev_workflow_failure_receipts SET receipt_json='{}' WHERE id=?`,
		`DELETE FROM cleardev_workflow_failure_receipts WHERE id=?`,
	} {
		if _, err := db.Exec(query, receipt.ID); err == nil {
			t.Fatal("failure evidence was mutable")
		}
	}
	if _, err := db.Exec(strings.Replace(insert, "INSERT INTO", "INSERT OR REPLACE INTO", 1), args...); err == nil {
		t.Fatal("replacement erased original failure evidence")
	}
	schema := workflowMigrationSchema(t, db)
	if err := migrateDown(202, db); err == nil {
		t.Fatal("downgrade erased failure evidence")
	}
	if gooseVersion(t, db) != 215 || !reflect.DeepEqual(schema, workflowMigrationSchema(t, db)) {
		t.Fatal("refused downgrade partially changed schema or version")
	}
	var saved string
	if err := db.QueryRow(`SELECT receipt_json FROM cleardev_workflow_failure_receipts WHERE id=?`, receipt.ID).Scan(&saved); err != nil || saved != string(raw) {
		t.Fatal("refused downgrade changed the original receipt", err)
	}
}
