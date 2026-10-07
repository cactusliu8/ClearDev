package sqlite

import "testing"

func init() { shippedMigrations[197] = "0197_cleardev_product_plan_progression.sql" }

func TestProductPlanMigrationRoundTrip(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 196)
	names := []string{"cleardev_product_stage_bind_guard", "cleardev_project_admission_insert_guard"}
	before := map[string]string{}
	for _, name := range names {
		var ddl string
		if e := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&ddl); e != nil {
			t.Fatal(e)
		}
		before[name] = ddl
	}
	upTo(t, db, 197)
	downTo(t, db, 196)
	for _, name := range names {
		var ddl string
		if e := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&ddl); e != nil || ddl != before[name] {
			t.Fatal("prior admission guard changed after downgrade", name, e)
		}
	}
	upTo(t, db, 197)
	var integrity string
	if e := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); e != nil || integrity != "ok" {
		t.Fatal(integrity, e)
	}
}
