package sqlite

import (
	"strings"
	"testing"
)

// Append the newly allocated migration number without rewriting shipped tests.
func init() {
	shippedMigrations[130] = "0130_cleardev_mail_reviewer_reuse.sql"
}

func TestMigration0130MailReviewerReuseUpgradeAndDown(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 129)
	upTo(t, db, 130)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='cleardev_mail_reviewer_rework_sources'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("new authorization view=%d err=%v", count, err)
	}
	if err := migrateDown(129, db); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='cleardev_mail_reviewer_rework_sources'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("down left authorization view=%d err=%v", count, err)
	}
	var index string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='idx_cleardev_complex_execution_session'`).Scan(&index); err != nil || strings.Contains(index, "rework") {
		t.Fatalf("down did not restore unique session index: %s %v", index, err)
	}
	upTo(t, db, 130)
}
