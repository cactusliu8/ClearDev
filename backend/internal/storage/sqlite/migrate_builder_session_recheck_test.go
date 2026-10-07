package sqlite

import "testing"

func init() { shippedMigrations[181] = "0181_cleardev_builder_session_recheck.sql" }

func TestBuilderSessionRecheckMigrationPreservesExistingSchema(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 180)
	const schema = `SELECT group_concat(sql, char(10)) FROM (SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL ORDER BY name)`
	var before, after string
	if err := db.QueryRow(schema).Scan(&before); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 181)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name IN ('cleardev_builder_session_checks','cleardev_builder_session_checks_immutable','cleardev_builder_session_checks_keep_history','cleardev_builder_session_checks_bound','cleardev_builder_session_checks_cdc')`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("missing immutable recheck schema: %d %v", count, err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_builder_session_checks(recovery_id,checkpoint,stage,outcome,binding_sha256,checked_at) VALUES('unbound','STARTED','RECHECK','PENDING',printf('%064d',0),'2026-09-30T00:00:00Z')`); err == nil {
		t.Fatal("an unbound recheck could create restoration evidence")
	}
	downTo(t, db, 180)
	if err := db.QueryRow(schema).Scan(&after); err != nil || before != after {
		t.Fatalf("empty upgrade/downgrade changed previous schema bytes: %v", err)
	}
	upTo(t, db, 181)
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration introduced invalid references: %d %v", count, err)
	}
}
