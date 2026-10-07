package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	_ "modernc.org/sqlite"
)

func openMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close database: %v", closeErr)
		}
	})
	return db
}

func readRunTrigger(t *testing.T, db *sql.DB) string {
	t.Helper()
	var trigger string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'trigger' AND name = 'cleardev_complex_execution_run_insert_valid'`).Scan(&trigger); err != nil {
		t.Fatalf("read run validity trigger: %v", err)
	}
	return trigger
}

func gooseVersion(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&version); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	return version
}

func migrateDown(version int64, db *sql.DB) error {
	gooseMu.Lock()
	defer gooseMu.Unlock()
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	return goose.DownTo(db, "migrations", version)
}

func TestMigration0129AllowsOneTaskAndKeepsOneTaskStandard(t *testing.T) {
	upgrade := openMigrationTestDB(t)
	upTo(t, upgrade, 128)
	if trigger := readRunTrigger(t, upgrade); !strings.Contains(trigger, "BETWEEN 2 AND 6") {
		t.Fatalf("pre-0129 trigger did not gate two tasks: %s", trigger)
	}
	upTo(t, upgrade, 129)
	assertSingleTaskTrigger(t, readRunTrigger(t, upgrade))

	fresh := openMigrationTestDB(t)
	upTo(t, fresh, 129)
	assertSingleTaskTrigger(t, readRunTrigger(t, fresh))
}

func assertSingleTaskTrigger(t *testing.T, trigger string) {
	t.Helper()
	if !strings.Contains(trigger, "BETWEEN 1 AND 6") || strings.Contains(trigger, "BETWEEN 2 AND 6") {
		t.Fatalf("0129 trigger does not allow the natural one-task range: %s", trigger)
	}
	if count := strings.Count(trigger, "json_array_length(plan.plan_json, '$.tasks') > 1"); count != 2 {
		t.Fatalf("0129 trigger single-task mode guards = %d, want 2: %s", count, trigger)
	}
	if !strings.Contains(trigger, "json_array_length(plan.plan_json, '$.tasks') BETWEEN 1 AND 6") {
		t.Fatalf("0129 trigger lacks the one-to-six count: %s", trigger)
	}
}

func TestMigration0129DownRefusesSingleTaskExecutionHistory(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 129)
	// Simulate durable single-task history without rebuilding every upstream
	// fixture table: foreign keys are off and the insert trigger is removed,
	// exactly the rows the Down guard must inspect.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER IF EXISTS cleardev_complex_execution_run_insert_valid`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER IF EXISTS cleardev_complex_plan_insert_valid`); err != nil {
		t.Fatal(err)
	}
	planJSON := `{"tasks":[{"key":"only-task"}]}`
	if _, err := db.Exec(`INSERT INTO cleardev_complex_engineering_plans (
id, planning_request_id, development_project_id, requirement_version_id, requirement_sha256,
compilation_sha256, version, planner_role_binding_id, agent_step_id, turn_id, final_message_id,
plan_json, plan_sha256, created_at
) VALUES ('down-plan', 'down-request', 'down-project', 'down-version', ?, ?, 1, 'down-planner', 'down-step', 'down-turn', 'down-message', ?, ?, '2026-09-10T12:00:00Z')`,
		strings.Repeat("a", 64), strings.Repeat("b", 64), planJSON, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_execution_runs (
id, development_project_id, requirement_version_id, requirement_sha256, plan_id, plan_review_id,
plan_sha256, steward_role_binding_id, mode, selection_reason_code, fixed_builder_count,
expected_task_set_version, accepted_task_set_version, execution_package_json,
execution_package_sha256, status, requested_at
) VALUES ('down-run', 'down-project', 'down-version', ?, 'down-plan', 'down-review', ?,
'down-steward', 'STANDARD', 'ONE_BUILDER_REQUIRED', 1, 0, 1, '{}', ?, 'PENDING', '2026-09-10T12:00:00Z')`,
		strings.Repeat("a", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(128, db); err == nil {
		t.Fatal("down migration accepted durable single-task execution history")
	}
	if version := gooseVersion(t, db); version != 129 {
		t.Fatalf("refused down migration changed the goose version to %d", version)
	}
}

func TestMigration0129DownRestoresTheOldTriggerWithoutSingleTaskHistory(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 129)
	downTo(t, db, 128)
	if trigger := readRunTrigger(t, db); !strings.Contains(trigger, "BETWEEN 2 AND 6") {
		t.Fatalf("down migration did not restore the previous trigger: %s", trigger)
	}
}
