package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0115ControlledExceptionAddsIndependentFacts(t *testing.T) {
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
	upTo(t, db, 114)
	upTo(t, db, 115)

	for _, table := range []string{
		"cleardev_complex_exception_budgets",
		"cleardev_complex_exception_budget_occupancies",
		"cleardev_complex_exception_scope_requests",
		"cleardev_complex_exception_scope_decisions",
		"cleardev_complex_exception_generated_commands",
		"cleardev_complex_exception_generated_proofs",
		"cleardev_complex_exception_path_leases",
		"cleardev_complex_exception_ondemand_bindings",
		"cleardev_complex_exception_agent_steps",
		"cleardev_complex_exception_specialist_results",
		"cleardev_complex_exception_specialist_checks",
		"cleardev_complex_exception_recovery_actions",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("lookup %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("missing S09 table %s", table)
		}
	}
	if !migrationColumnExists(t, db, "cleardev_complex_execution_check_runs", "retry_ordinal") {
		t.Fatal("missing retry_ordinal on complex execution check runs")
	}

	if _, err := db.Exec(`INSERT INTO cleardev_complex_exception_budgets (
id, execution_run_id, role_kind, allowed_agent_types_json, model_selection, max_turns, used_turns, max_rework_count, created_at
) VALUES ('bad-budget', 'missing', 'RECOVERY', '["RECOVERY_SPECIALIST"]', 'codex-chat', 1, 0, 0, '2026-08-27T12:00:00Z')`); err == nil {
		t.Fatal("exception budget accepted a missing execution run")
	}
}
