package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0114QuickExecutionAddsIndependentFacts(t *testing.T) {
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
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 113)
	upTo(t, db, 114)

	for _, table := range []string{
		"cleardev_complex_quick_runs",
		"cleardev_complex_quick_role_bindings",
		"cleardev_complex_quick_agent_steps",
		"cleardev_complex_quick_task_mappings",
		"cleardev_complex_quick_task_attempts",
		"cleardev_complex_quick_check_specs",
		"cleardev_complex_quick_check_runs",
		"cleardev_complex_quick_results",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("lookup %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("missing S08 table %s", table)
		}
	}
	for _, column := range []struct{ table, column string }{
		{"cleardev_work_items", "complex_quick_task_id"},
		{"cleardev_path_permission_versions", "complex_quick_task_id"},
		{"cleardev_required_checks", "complex_quick_check_spec_id"},
		{"cleardev_candidate_commits", "complex_quick_task_attempt_id"},
		{"cleardev_candidate_commits", "complex_quick_base_commit_sha"},
		{"cleardev_integration_candidates", "complex_quick_result_id"},
		{"cleardev_integration_candidates", "complex_quick_source_candidate_commit_id"},
		{"cleardev_evidence", "complex_quick_check_run_id"},
	} {
		if !migrationColumnExists(t, db, column.table, column.column) {
			t.Fatalf("missing S08 nullable binding %s.%s", column.table, column.column)
		}
	}

	if _, err := db.Exec(`INSERT INTO cleardev_complex_quick_runs (
id, development_project_id, requirement_version_id, requirement_sha256,
source_execution_run_id, plan_id, plan_sha256, source_task_key, integration_base_sha,
steward_role_binding_id, mode, expected_task_set_version, accepted_task_set_version,
execution_package_json, execution_package_sha256, status, requested_at
) VALUES ('bad-quick', 'dev-upgrade', 'req-3', ?, 'missing', 'missing', ?, 'deduplicate-email', ?,
'missing', 'QUICK', 1, 2, '{}', ?, 'PENDING', ?)`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"2026-08-27T12:00:00Z"); err == nil {
		t.Fatal("QuickExecution accepted a run without a completed current v2 integration")
	}
}
