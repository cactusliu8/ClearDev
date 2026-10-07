package sqlite

import (
	"strings"
	"testing"
)

func init() { shippedMigrations[140] = "0140_cleardev_parallel_builder_continuation.sql" }

func TestMigration0140ScopesBuilderSlotUniquenessToActiveBindings(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 140)
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='index' AND name='idx_cleardev_complex_execution_builder_slots'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"builder_slot", "status IN ('REQUESTED', 'BOUND')"} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("active Builder slot index omitted %q: %s", want, ddl)
		}
	}

	downTo(t, db, 139)
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='index' AND name='idx_cleardev_complex_execution_builder_slots'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ddl, "status IN") {
		t.Fatalf("downgrade kept active-only uniqueness: %s", ddl)
	}

	upTo(t, db, 140)
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='index' AND name='idx_cleardev_complex_execution_builder_slots'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "status IN ('REQUESTED', 'BOUND')") {
		t.Fatalf("migration replay changed Builder continuation contract: %s", ddl)
	}
}
