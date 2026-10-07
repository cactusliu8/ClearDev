package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0113ComplexParallelExecutionAddsIndependentFacts(t *testing.T) {
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
	upTo(t, db, 112)
	upTo(t, db, 113)

	for _, table := range []string{
		"cleardev_complex_execution_batches",
		"cleardev_complex_execution_compositions",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("lookup %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("missing S07 table %s", table)
		}
	}
	for _, column := range []struct{ table, column string }{
		{"cleardev_complex_execution_runs", "fixed_builder_count"},
		{"cleardev_complex_execution_role_bindings", "builder_slot"},
		{"cleardev_complex_execution_task_attempts", "batch_id"},
	} {
		if !migrationColumnExists(t, db, column.table, column.column) {
			t.Fatalf("missing S07 column %s.%s", column.table, column.column)
		}
	}

	var builderIndex string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_cleardev_complex_execution_builder_active_attempt'`).Scan(&builderIndex); err != nil {
		t.Fatalf("read per-builder active-attempt index: %v", err)
	}
	if !strings.Contains(builderIndex, "builder_role_binding_id") || !strings.Contains(builderIndex, "'PENDING'") {
		t.Fatalf("per-builder active-attempt index is wrong: %s", builderIndex)
	}
	var dropped int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_cleardev_complex_execution_one_active_attempt'`).Scan(&dropped); err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Fatal("S06 one-active-attempt index survived 0113")
	}

	var runSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'cleardev_complex_execution_runs'`).Scan(&runSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runSQL, "'STANDARD', 'PARALLEL'") || !strings.Contains(runSQL, "PARALLEL_PLAN_APPROVED") {
		t.Fatalf("runs table did not accept PARALLEL: %s", runSQL)
	}
}
