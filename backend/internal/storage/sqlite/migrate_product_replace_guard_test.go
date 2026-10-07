package sqlite

import "testing"

func init() { shippedMigrations[142] = "0142_cleardev_product_replace_guard.sql" }

func TestMigration0142ProductReplaceGuardUpgradeAndDown(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 141)
	upTo(t, db, 142)
	guards := []string{"cleardev_product_goal_insert_replace_guard", "cleardev_product_discussion_insert_replace_guard", "cleardev_product_stage_insert_replace_guard", "cleardev_product_stage_rebind_replace_guard"}
	for _, name := range guards {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing replace guard %s: count=%d err=%v", name, count, err)
		}
	}
	downTo(t, db, 141)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE '%replace_guard'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("down left replace guards: count=%d err=%v", count, err)
	}
	upTo(t, db, 142)
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='cleardev_product_stage_rebind_replace_guard'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replayed migration missing guard: count=%d err=%v", count, err)
	}
}
