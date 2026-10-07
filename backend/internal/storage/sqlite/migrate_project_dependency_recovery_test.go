package sqlite

import "testing"

func init() {
	shippedMigrations[168] = "0168_cleardev_project_rework_turn_budgets.sql"
	shippedMigrations[169] = "0169_cleardev_project_rework_rounds.sql"
	shippedMigrations[170] = "0170_cleardev_builder_recovery_attempt.sql"
	shippedMigrations[171] = "0171_cleardev_builder_recovery_edge.sql"
	shippedMigrations[172] = "0172_cleardev_work_item_recovery_counter.sql"
	shippedMigrations[173] = "0173_cleardev_project_dependency_check_recovery.sql"
}

func TestProjectDependencyRecoveryMigrationRestoresExistingAttemptGuard(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 172)
	var before string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_complex_execution_attempt_update_valid'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 173)
	var after string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_complex_execution_attempt_update_valid'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("project check recovery edge was not installed")
	}
	if err := migrateDown(172, db); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_complex_execution_attempt_update_valid'`).Scan(&restored); err != nil || restored != before {
		t.Fatalf("existing recovery guard changed: %v", err)
	}
}

func init() { shippedMigrations[174] = "0174_cleardev_project_check_continuation.sql" }

func TestProjectDependencyContinuationMigrationRestoresExistingAttemptGuard(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 173)
	var before string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_complex_execution_attempt_update_valid'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 174)
	if err := migrateDown(173, db); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_complex_execution_attempt_update_valid'`).Scan(&restored); err != nil || restored != before {
		t.Fatalf("existing guard changed: %v", err)
	}
}
