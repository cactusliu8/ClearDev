package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0112ComplexStandardExecutionAddsIndependentFacts(t *testing.T) {
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
	upTo(t, db, 111)
	upTo(t, db, 112)

	for _, table := range []string{
		"cleardev_complex_execution_runs",
		"cleardev_complex_execution_role_bindings",
		"cleardev_complex_execution_agent_steps",
		"cleardev_complex_execution_task_mappings",
		"cleardev_complex_execution_dependencies",
		"cleardev_complex_execution_task_attempts",
		"cleardev_complex_execution_check_specs",
		"cleardev_complex_execution_check_runs",
		"cleardev_complex_execution_reviews",
		"cleardev_complex_execution_verified_candidates",
		"cleardev_complex_execution_results",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("lookup %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("missing S06 table %s", table)
		}
	}

	for _, column := range []struct{ table, column string }{
		{"cleardev_complex_execution_check_specs", "timeout_seconds"},
		{"cleardev_complex_execution_dependencies", "dependency_ordinal"},
		{"cleardev_work_items", "complex_execution_task_id"},
		{"cleardev_path_permission_versions", "complex_execution_task_id"},
		{"cleardev_required_checks", "complex_execution_check_spec_id"},
		{"cleardev_candidate_commits", "complex_execution_task_attempt_id"},
		{"cleardev_candidate_commits", "complex_execution_base_commit_sha"},
		{"cleardev_integration_candidates", "complex_execution_result_id"},
		{"cleardev_integration_candidates", "complex_source_candidate_commit_id"},
		{"cleardev_evidence", "complex_execution_check_run_id"},
		{"cleardev_evidence", "complex_execution_review_id"},
	} {
		if !migrationColumnExists(t, db, column.table, column.column) {
			t.Fatalf("missing S06 nullable binding %s.%s", column.table, column.column)
		}
	}

	if _, err := db.Exec(`INSERT INTO cleardev_complex_execution_runs (
id, development_project_id, requirement_version_id, requirement_sha256,
plan_id, plan_review_id, plan_sha256, steward_role_binding_id, mode,
selection_reason_code, expected_task_set_version, accepted_task_set_version,
execution_package_json, execution_package_sha256, status, requested_at
) VALUES ('bad-run', 'dev-upgrade', 'req-3', ?, 'missing', 'missing', ?, 'missing',
'STANDARD', 'ONE_BUILDER_REQUIRED', 0, 1, '{}', ?, 'PENDING', ?)`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"2026-08-26T12:00:00Z"); err == nil {
		t.Fatal("ComplexExecution accepted a run without the exact approved plan")
	}

	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_cleardev_complex_execution_one_active_attempt'`).Scan(&indexSQL); err != nil {
		t.Fatalf("read active-attempt index: %v", err)
	}
	if !strings.Contains(indexSQL, "'PENDING'") {
		t.Fatalf("active-attempt index must reserve a persisted PENDING dispatch: %s", indexSQL)
	}
	var stewardIndex string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_cleardev_complex_execution_one_steward'`).Scan(&stewardIndex); err != nil {
		t.Fatalf("read active-Steward index: %v", err)
	}
	if !strings.Contains(stewardIndex, "'REQUESTED', 'BOUND'") {
		t.Fatalf("Steward index does not allow one historical continuation: %s", stewardIndex)
	}
	var runTrigger string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'cleardev_complex_execution_run_insert_valid'`).Scan(&runTrigger); err != nil {
		t.Fatalf("read run validity trigger: %v", err)
	}
	for _, want := range []string{"recommendedBuilderCount", "BETWEEN 2 AND 6", "generatedPaths", "sharedPathsRequireApproval"} {
		if !strings.Contains(runTrigger, want) {
			t.Fatalf("run trigger lacks unsupported-plan guard %q", want)
		}
	}
	var checkSpecTable string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'cleardev_complex_execution_check_specs'`).Scan(&checkSpecTable); err != nil {
		t.Fatalf("read fixed check-spec table: %v", err)
	}
	for _, want := range []string{"timeout_seconds", "test/email.test.js", "test/deduplicate.test.js", "test/summary.test.js", "82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586"} {
		if !strings.Contains(checkSpecTable, want) {
			t.Fatalf("fixed check-spec table lacks catalog guard %q", want)
		}
	}
	var attemptTrigger string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'cleardev_complex_execution_attempt_insert_valid'`).Scan(&attemptTrigger); err != nil {
		t.Fatalf("read attempt validity trigger: %v", err)
	}
	for _, want := range []string{"step.request_id = NEW.id", "verified.task_mapping_id", "prior_task.ordinal < task.ordinal", "ORDER BY prior_task.ordinal DESC", "LIMIT 1"} {
		if !strings.Contains(attemptTrigger, want) {
			t.Fatalf("attempt trigger lacks exact preceding verified base clause %q", want)
		}
	}
	var dependencyTrigger string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'cleardev_complex_execution_dependency_insert_valid'`).Scan(&dependencyTrigger); err != nil {
		t.Fatalf("read dependency validity trigger: %v", err)
	}
	for _, want := range []string{"dependency_ordinal", "dependencyTaskKeys", "dependencyTaskIds", "dependency.ordinal < task.ordinal"} {
		if !strings.Contains(dependencyTrigger, want) {
			t.Fatalf("dependency trigger lacks ordered package binding %q", want)
		}
	}
	var reviewTrigger string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'cleardev_complex_execution_review_insert_valid'`).Scan(&reviewTrigger); err != nil {
		t.Fatalf("read review validity trigger: %v", err)
	}
	if !strings.Contains(reviewTrigger, "step.request_id = NEW.id") {
		t.Fatal("review trigger does not bind the exact Reviewer request")
	}

	var resultTrigger string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'cleardev_complex_execution_result_update_valid'`).Scan(&resultTrigger); err != nil {
		t.Fatalf("read result validity trigger: %v", err)
	}
	for _, want := range []string{"version.version = 2", "version.state = 'APPROVED'", "version.task_set_version = run.accepted_task_set_version", "run.accepted_task_set_version = 1"} {
		if !strings.Contains(resultTrigger, want) {
			t.Fatalf("result completion trigger lacks %q", want)
		}
	}
	if !strings.Contains(resultTrigger, "run.status = 'ACCEPTED'") {
		t.Fatal("result completion trigger does not require the accepted execution run")
	}
	var finalizerTrigger string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'cleardev_complex_execution_completion_finalize'`).Scan(&finalizerTrigger); err != nil {
		t.Fatalf("read atomic completion trigger: %v", err)
	}
	for _, want := range []string{"OLD.completion_status = 'PENDING'", "NEW.completion_status = 'COMMITTING'", "UPDATE cleardev_work_items", "completion_status = 'COMPLETED'", "status = 'COMPLETED'", "RAISE(ABORT"} {
		if !strings.Contains(finalizerTrigger, want) {
			t.Fatalf("atomic completion trigger lacks %q", want)
		}
	}
}

func migrationColumnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) WHERE name = ?`, table, column)
	if err != nil {
		t.Fatalf("read %s.%s: %v", table, column, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			t.Errorf("close %s.%s rows: %v", table, column, closeErr)
		}
	}()
	found := rows.Next()
	if rowsErr := rows.Err(); rowsErr != nil {
		t.Fatalf("read %s.%s rows: %v", table, column, rowsErr)
	}
	return found
}
