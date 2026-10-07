package sqlite

import "testing"

func init() { shippedMigrations[143] = "0143_cleardev_product_pending_replace_guard.sql" }

func TestMigration0143PendingDiscussionReplaceGuardUpgradeAndDown(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 142)
	upTo(t, db, 143)
	const guard = "cleardev_product_pending_discussion_insert_replace_guard"
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, guard).Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing pending replace guard: count=%d err=%v", count, err)
	}
	downTo(t, db, 142)
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, guard).Scan(&count); err != nil || count != 0 {
		t.Fatalf("down left pending replace guard: count=%d err=%v", count, err)
	}
	upTo(t, db, 143)
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, guard).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replayed migration missing guard: count=%d err=%v", count, err)
	}
}
