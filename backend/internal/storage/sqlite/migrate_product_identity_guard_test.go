package sqlite

import "testing"

func init() { shippedMigrations[144] = "0144_cleardev_product_identity_guard.sql" }

func TestMigration0144ProductIdentityGuardUpgradeAndDown(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 143)
	upTo(t, db, 144)
	guards := []string{"cleardev_product_goal_identity_guard", "cleardev_product_discussion_identity_guard", "cleardev_product_stage_identity_guard", "cleardev_product_discussion_rowid_guard", "cleardev_product_stage_rowid_guard", "cleardev_product_stage_rebind_nullsafe_guard"}
	for _, name := range guards {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing identity guard %s: count=%d err=%v", name, count, err)
		}
	}
	downTo(t, db, 143)
	for _, name := range guards {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("down left identity guard %s: count=%d err=%v", name, count, err)
		}
	}
	upTo(t, db, 144)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='cleardev_product_stage_rebind_nullsafe_guard'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replayed migration missing guard: count=%d err=%v", count, err)
	}
}
