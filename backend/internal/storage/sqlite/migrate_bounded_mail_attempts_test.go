package sqlite

import (
	"database/sql"
	"strings"
	"testing"
)

func init() { shippedMigrations[132] = "0132_cleardev_bounded_mail_attempts.sql" }

func TestMigration0132PreservesForeignKeysAndReapplies(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 131)
	before := mailAttemptIndexes(t, db)
	upTo(t, db, 132)
	after := mailAttemptIndexes(t, db)
	for name, sql := range before {
		if after[name] != sql {
			t.Fatalf("old safety index changed or disappeared: %s", name)
		}
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("migration left a dangling foreign key")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys=%d err=%v", enabled, err)
	}
	downTo(t, db, 131)
	upTo(t, db, 132)
}

func mailAttemptIndexes(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name,sql FROM sqlite_schema WHERE type='index' AND sql IS NOT NULL AND tbl_name IN ('cleardev_complex_execution_task_attempts','cleardev_complex_exception_budgets')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	result := map[string]string{}
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		result[name] = strings.ToLower(strings.Join(strings.Fields(ddl), ""))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(result) < 3 {
		t.Fatal("old attempt/budget safety indexes missing")
	}
	return result
}
