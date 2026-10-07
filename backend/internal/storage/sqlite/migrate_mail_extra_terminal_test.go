package sqlite

import "testing"

func init() { shippedMigrations[133] = "0133_cleardev_mail_extra_attempt_terminal.sql" }

func TestMigration0133KeepsExistingGuardsAndReapplies(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 132)
	before := mailAttemptIndexes(t, db)
	upTo(t, db, 133)
	after := mailAttemptIndexes(t, db)
	for name, ddl := range before {
		if after[name] != ddl {
			t.Fatalf("existing guard changed: %s", name)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND name='cleardev_mail_attempt_after_extra_forbidden'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("terminal guard count=%d err=%v", count, err)
	}
	downTo(t, db, 132)
	upTo(t, db, 133)
}
