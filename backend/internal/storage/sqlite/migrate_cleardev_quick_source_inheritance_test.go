package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0118PreservesExistingQuickFactsAndWidensSourceBindings(t *testing.T) {
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

	upTo(t, db, 117)
	seedDemoExecutionPlan(t, db, "dev-v2", "req-v2", 2)
	if _, err := db.Exec(`DROP TRIGGER cleardev_complex_quick_run_insert_valid`); err != nil {
		t.Fatalf("remove old fixed run trigger for legacy fixture: %v", err)
	}
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const at = "2026-08-29T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO cleardev_complex_quick_runs (
id, development_project_id, requirement_version_id, requirement_sha256,
source_execution_run_id, plan_id, plan_sha256, source_task_key, integration_base_sha,
steward_role_binding_id, mode, expected_task_set_version, accepted_task_set_version,
execution_package_json, execution_package_sha256, status, requested_at
) VALUES (
'legacy-quick', 'dev-v2', 'req-v2', ?, 'run-v2', 'plan-dev-v2', ?, '', ?,
'steward-dev-v2', 'QUICK', 1, 2, '{}', ?, 'PENDING', ?
)`, digest, digest, strings.Repeat("b", 40), digest, at); err != nil {
		t.Fatalf("seed legacy QUICK run: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_quick_check_specs (
id, quick_run_id, task_mapping_id, required_check_id, check_kind, check_name,
check_spec_sha256, argv_json, timeout_seconds, created_at
) VALUES (
'legacy-quick-integration', 'legacy-quick', NULL, NULL, 'INTEGRATION', 'all-tests',
'82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586',
'["node","--test"]', 60, ?
)`, at); err != nil {
		t.Fatalf("seed legacy QUICK check: %v", err)
	}

	upTo(t, db, 118)

	var expected, accepted int64
	if err := db.QueryRow(`SELECT expected_task_set_version, accepted_task_set_version
FROM cleardev_complex_quick_runs WHERE id = 'legacy-quick'`).Scan(&expected, &accepted); err != nil {
		t.Fatalf("read migrated QUICK run: %v", err)
	}
	if expected != 1 || accepted != 2 {
		t.Fatalf("migrated QUICK task set = %d -> %d", expected, accepted)
	}
	var kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleardev_complex_quick_check_specs
WHERE id = 'legacy-quick-integration' AND check_name = 'all-tests'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatal("0118 dropped the existing QUICK check fact")
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("0118 left a foreign-key violation")
	}

	var runTable, checkTable, insertTrigger, updateTrigger, checkTrigger string
	queries := []struct {
		name string
		kind string
		out  *string
	}{
		{name: "cleardev_complex_quick_runs", kind: "table", out: &runTable},
		{name: "cleardev_complex_quick_check_specs", kind: "table", out: &checkTable},
		{name: "cleardev_complex_quick_run_insert_valid", kind: "trigger", out: &insertTrigger},
		{name: "cleardev_complex_quick_run_update_valid", kind: "trigger", out: &updateTrigger},
		{name: "cleardev_complex_quick_check_spec_insert_valid", kind: "trigger", out: &checkTrigger},
	}
	for _, query := range queries {
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = ? AND name = ?`, query.kind, query.name).Scan(query.out); err != nil {
			t.Fatalf("read %s: %v", query.name, err)
		}
	}
	if !strings.Contains(runTable, "expected_task_set_version > 0") ||
		!strings.Contains(runTable, "accepted_task_set_version = expected_task_set_version + 1") {
		t.Fatal("0118 QUICK run table does not derive the successor task set")
	}
	for _, sqlText := range []string{insertTrigger, updateTrigger} {
		if strings.Contains(sqlText, "version.version = 2") || strings.Contains(sqlText, "accepted_task_set_version = 1") {
			t.Fatal("0118 QUICK run trigger still fixes a requirement or task-set version")
		}
		for _, required := range []string{
			"version.superseded_by_id IS NULL",
			"project.cancelled_at IS NULL",
			"plan.requirement_version_id = version.id",
			"steward.status = 'BOUND'",
			"integration.complex_execution_result_id = result.id",
			"integration.requirement_version_id = version.id",
		} {
			if !strings.Contains(sqlText, required) {
				t.Fatalf("0118 QUICK run trigger is missing %q", required)
			}
		}
	}
	for _, oldName := range []string{"email-unit", "deduplicate-unit", "summary-unit", "all-tests"} {
		if strings.Contains(checkTable, oldName) || strings.Contains(checkTrigger, "check_name = '"+oldName+"'") {
			t.Fatalf("0118 QUICK check schema still contains the old %q whitelist", oldName)
		}
	}
	for _, required := range []string{
		"source_spec.check_name = NEW.check_name",
		"source_spec.check_spec_sha256 = NEW.check_spec_sha256",
		"json(source_spec.argv_json) = json(NEW.argv_json)",
		"source_spec.timeout_seconds = NEW.timeout_seconds",
	} {
		if !strings.Contains(checkTrigger, required) {
			t.Fatalf("0118 QUICK check trigger is missing %q", required)
		}
	}
}
