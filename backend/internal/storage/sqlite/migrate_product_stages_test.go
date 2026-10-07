package sqlite

import "testing"

func init() { shippedMigrations[141] = "0141_cleardev_product_stages.sql" }

func TestMigration0141ProductStageUpgradeAndDowngrade(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 140)
	upTo(t, db, 141)
	for _, name := range []string{"cleardev_product_goals", "cleardev_product_discussions", "cleardev_product_stages", "cleardev_product_not_executable", "cleardev_product_stage_bind_guard", "cleardev_product_discussion_settle_guard"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing product schema %s: count=%d err=%v", name, count, err)
		}
	}
	downTo(t, db, 140)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE 'cleardev_product_%'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("product downgrade leaves objects: count=%d err=%v", count, err)
	}
	upTo(t, db, 141)
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='cleardev_product_not_executable'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replayed migration did not restore product guard: count=%d err=%v", count, err)
	}
}

func TestMigration0141ProductDowngradeKeepsRecordedHistory(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 141)
	at := "2026-09-22 00:00:00"
	for _, statement := range []string{
		`INSERT INTO projects (id, path, registered_at) VALUES ('ao-product', '/tmp/ao-product', '` + at + `')`,
		`INSERT INTO cleardev_development_projects (id, ao_project_id, name, state, created_at, updated_at) VALUES ('product-1', 'ao-product', 'Mail', 'INTAKE', '` + at + `', '` + at + `')`,
		`INSERT INTO cleardev_product_goals (id, request_id, created_at) VALUES ('product-1', 'request-1', '` + at + `')`,
		`INSERT INTO cleardev_product_discussions (id, product_id, ordinal, user_message, created_at) VALUES ('disc-1', 'product-1', 0, 'goal', '` + at + `')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed product history: %v", err)
		}
	}
	if err := migrateDown(140, db); err == nil {
		t.Fatal("downgrade erased recorded product history")
	}
	for _, name := range []string{"cleardev_product_goals", "cleardev_product_discussions", "cleardev_product_stages", "cleardev_product_not_executable"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("refused downgrade changed %s: count=%d err=%v", name, count, err)
		}
	}
	var goals int
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_product_goals`).Scan(&goals); err != nil || goals != 1 {
		t.Fatalf("refused downgrade lost product rows: goals=%d err=%v", goals, err)
	}
}
