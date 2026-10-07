package sqlite

import "testing"

func init() { shippedMigrations[175] = "0175_cleardev_workflow_recovery.sql" }

func TestWorkflowRecoveryMigrationRoundTrip(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 175)
	if err := migrateDown(174, db); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 175)
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity: %s %v", integrity, err)
	}
}
