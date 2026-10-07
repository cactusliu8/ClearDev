package sqlite

import "testing"

func init() { shippedMigrations[147] = "0147_cleardev_final_review_evidence_recheck.sql" }

func TestMigration0147UpgradeDownReplay(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 146)
	upTo(t, db, 147)
	downTo(t, db, 146)
	upTo(t, db, 147)
	var check string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity %s %v", check, err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN ('cleardev_final_review_recheck_insert_guard','cleardev_final_review_recheck_session_guard','cleardev_execution_requires_final_review')").Scan(&n); err != nil || n != 3 {
		t.Fatalf("missing guards %d %v", n, err)
	}
}
