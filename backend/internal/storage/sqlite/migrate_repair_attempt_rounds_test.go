package sqlite

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func init() { shippedMigrations[191] = "0191_cleardev_repair_attempt_rounds.sql" }

func TestRepairAttemptRoundsMigrationPreservesHistoricalFacts(t *testing.T) {
	db := openMigrationTestDB(t)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 190)
	read := func() map[string][][]string {
		out := map[string][][]string{}
		for _, table := range []string{"cleardev_development_projects", "cleardev_contract_versions", "cleardev_work_items", "cleardev_candidate_commits", "cleardev_evidence", "cleardev_project_events"} {
			rows, err := db.Query("SELECT rowid,* FROM " + table + " ORDER BY rowid")
			if err != nil {
				t.Fatal(err)
			}
			out[table] = migrationEvidenceRows(t, rows)
		}
		return out
	}
	before := read()
	upTo(t, db, 191)
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("upgrade changed historical row bytes or identities")
	}
	var invalid int
	var integrity string
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatal("upgrade changed foreign-key integrity", invalid, err)
	}
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("upgrade changed SQLite integrity", integrity, err)
	}
	if err := goose.DownTo(db, "migrations", 190); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("downgrade changed old history")
	}
	version, err := goose.GetDBVersion(db)
	if err != nil || version != 190 {
		t.Fatal("unexpected migration version", version, err)
	}
	upTo(t, db, 191)
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("re-upgrade changed old history")
	}
}

func TestRepairAttemptRoundsMigrationEmptyDatabaseCanDowngrade(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 191)
	if err := goose.DownTo(db, "migrations", 190); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 191)
}

func TestRepairAttemptRoundsMigrationDownRefusesGrantedHorizon(t *testing.T) {
	for _, seed := range []struct {
		name string
		sql  string
	}{
		{"attempt past the old bound", `INSERT INTO cleardev_complex_execution_task_attempts (
id, execution_run_id, task_mapping_id, builder_role_binding_id, agent_step_id, round,
base_commit_sha, status, reason_code, dispatched_at, settled_at
) VALUES ('round7-attempt', 'round7-run', 'round7-mapping', 'round7-builder', 'round7-step', 7,
'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'REWORK', 'BUILDER_BLOCKED', '2026-10-01T00:00:00Z', '2026-10-01T00:00:01Z')`},
		{"granted builder budget", `INSERT INTO cleardev_complex_exception_budgets (
id, execution_run_id, complex_execution_task_id, role_kind, allowed_agent_types_json,
model_selection, max_turns, used_turns, max_rework_count, created_at, authorized_extra_turns
) VALUES ('round7-budget', 'round7-run', 'round7-mapping', 'BUILDER', '["BUILDER"]',
'codex-chat', 4, 4, 3, '2026-10-01T00:00:00Z', 2)`},
		{"recorded repair authorization", `INSERT INTO cleardev_human_decision_requests (
id, development_project_id, decision_kind, binding_schema_version, binding_json,
display_json, content_sha256, status, decision, resolved_at, created_at
) VALUES ('round7-repair', 'dev-upgrade', 'AUTHORIZE_COORDINATION_REPAIR', 1, '{}',
'{}', '` + strings.Repeat("a", 64) + `', 'RESOLVED', 'APPROVE', '2026-10-01T00:00:01Z', '2026-10-01T00:00:00Z')`},
	} {
		t.Run(seed.name, func(t *testing.T) {
			db := openMigrationTestDB(t)
			upTo(t, db, 191)
			// Foreign keys and the insert trigger are dropped only to place
			// the exact history row the Down guard must inspect, following
			// the 0129 refusal test.
			if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TRIGGER IF EXISTS cleardev_complex_execution_attempt_insert_valid`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TRIGGER IF EXISTS cleardev_mail_role_budget_insert`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TRIGGER IF EXISTS cleardev_complex_exception_budget_update_valid`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(seed.sql); err != nil {
				t.Fatal(err)
			}
			if err := migrateDown(190, db); err == nil {
				t.Fatal("down migration accepted granted repair-round history")
			}
			if version := gooseVersion(t, db); version != 191 {
				t.Fatalf("refused down migration changed the goose version to %d", version)
			}
		})
	}
}
