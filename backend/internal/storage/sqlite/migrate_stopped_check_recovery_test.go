package sqlite

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func init() { shippedMigrations[195] = "0195_cleardev_stopped_check_recovery.sql" }

func TestStoppedCheckMigrationRoundTripAndAtomicRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "roundtrip"
		if fail {
			name = "late_failure_rolls_back"
		}
		t.Run(name, func(t *testing.T) {
			db := openMigrationTestDB(t)
			upTo(t, db, 194)
			names := []string{"cleardev_complex_execution_task_attempts", "cleardev_complex_execution_attempt_update_valid", "cleardev_planner_runtime_barriers", "cleardev_planner_runtime_current_decisions", "cleardev_workflow_recoveries", "cleardev_workflow_recovery_current"}
			before := map[string]string{}
			for _, n := range names {
				var s string
				if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, n).Scan(&s); err != nil {
					t.Fatal(err)
				}
				before[n] = s
			}
			if fail {
				if _, err := db.Exec(`CREATE TRIGGER cleardev_stopped_check_start_current BEFORE UPDATE OF status ON cleardev_complex_execution_check_runs BEGIN SELECT 1; END`); err != nil {
					t.Fatal(err)
				}
				if err := goose.UpTo(db, "migrations", 195); err == nil {
					t.Fatal("late migration failure missing")
				}
				if gooseVersion(t, db) != 194 {
					t.Fatal("failed migration changed version")
				}
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name LIKE 'cleardev_stopped_check_%'`).Scan(&count); err != nil || count != 0 {
					t.Fatal("partial authority tables", count, err)
				}
			} else {
				upTo(t, db, 195)
				downTo(t, db, 194)
			}
			for n, want := range before {
				var actual string
				if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, n).Scan(&actual); err != nil || actual != want {
					t.Fatal("old schema not restored exactly", n, err)
				}
			}
			if !fail {
				upTo(t, db, 195)
			}
		})
	}
}

func TestStoppedCheckMigrationPreservesRowsAndRefusesAuthorityLoss(t *testing.T) {
	db := openMigrationTestDB(t)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 194)
	tables := []string{"sessions", "cleardev_development_projects", "cleardev_contract_versions", "cleardev_work_items", "cleardev_human_decision_requests", "cleardev_agent_attempt_events", "change_log"}
	before := map[string][][]string{}
	for _, table := range tables {
		before[table] = preSendMigrationRows(t, db, table)
	}
	upTo(t, db, 195)
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], preSendMigrationRows(t, db, table)) {
			t.Fatal("historical row/rowid changed", table)
		}
	}
	var project string
	if err := db.QueryRow(`SELECT id FROM cleardev_development_projects LIMIT 1`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_human_decision_requests(id,development_project_id,decision_kind,binding_schema_version,binding_json,display_json,content_sha256,status,created_at) VALUES('stopped-check-history',?,'AUTHORIZE_STOPPED_CHECK_RECOVERY',1,'{}','{}',?,'PENDING','2026-10-03T00:00:00Z')`, project, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(194, db); err == nil {
		t.Fatal("downgrade erased pending authority")
	}
	if gooseVersion(t, db) != 195 {
		t.Fatal("refused downgrade changed version")
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
}
