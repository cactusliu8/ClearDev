package sqlite

import (
	"testing"
)

func init() { shippedMigrations[131] = "0131_cleardev_reviewer_requested_checks.sql" }

func TestMigration0131ReviewerChecksUpgradeAndDown(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 130)
	upTo(t, db, 131)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name IN ('cleardev_review_check_requests','cleardev_review_check_results')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("tables=%d err=%v", count, err)
	}
	if err := migrateDown(130, db); err != nil {
		t.Fatal(err)
	}
	if version := gooseVersion(t, db); version != 130 {
		t.Fatalf("version=%d", version)
	}
	upTo(t, db, 131)
}
