package sqlite

import (
	"testing"
)

func init() { shippedMigrations[196] = "0196_cleardev_technical_check_recovery.sql" }
func TestStoppedCheckTechnicalMigrationRoundTripPreservesExistingSchema(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 195)
	names := []string{"cleardev_stopped_check_requests", "cleardev_stopped_check_grants", "cleardev_stopped_check_retry_exact", "cleardev_stopped_check_start_current", "cleardev_planner_runtime_barriers", "cleardev_stopped_check_recovery_current", "cleardev_complex_execution_attempt_update_valid"}
	before := map[string]string{}
	for _, name := range names {
		var sql string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&sql); err != nil {
			t.Fatal(err)
		}
		before[name] = sql
	}
	upTo(t, db, 196)
	downTo(t, db, 195)
	for _, name := range names {
		var actual string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&actual); err != nil || actual != before[name] {
			t.Fatal("195 schema changed after roundtrip", name, err)
		}
	}
	upTo(t, db, 196)
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil || result != "ok" {
		t.Fatal("migration integrity", result, err)
	}
}
