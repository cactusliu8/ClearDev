package sqlite

import "testing"

func init() { shippedMigrations[134] = "0134_cleardev_mail_replacement_continuation.sql" }

func TestMigration0134ReplacementRelationsReapplyWithoutHistory(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 133)
	upTo(t, db, 134)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_mail_effective_review_outcomes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("new projection=%d err=%v", count, err)
	}
	downTo(t, db, 133)
	upTo(t, db, 134)
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("replacement migration damaged foreign keys")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
