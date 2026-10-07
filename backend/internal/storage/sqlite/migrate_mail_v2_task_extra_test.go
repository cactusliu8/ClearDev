package sqlite

import (
	"strings"
	"testing"
)

func init() { shippedMigrations[138] = "0138_cleardev_mail_v2_task_extra_terminal.sql" }

func TestMigration0138KeepsGlobalGrantAndTaskLocalV2Terminality(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 138)
	readTrigger := func(name string) string {
		t.Helper()
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		return ddl
	}
	terminal := readTrigger("cleardev_mail_attempt_after_extra_forbidden")
	for _, want := range []string{"prior.task_id=NEW.task_id", "cleardev_bounded_mail_runs", "MAIL_INCREMENT_V2", "prior.attempt_kind='HUMAN_EXTRA'"} {
		if !strings.Contains(terminal, want) {
			t.Fatalf("terminal trigger omitted %q: %s", want, terminal)
		}
	}
	grant := readTrigger("cleardev_mail_attempt_slot_insert")
	if !strings.Contains(grant, "execution_run_id=NEW.execution_run_id AND attempt_kind='HUMAN_EXTRA'") {
		t.Fatal("migration loosened execution-wide one-extra authorization")
	}
	downTo(t, db, 137)
	legacy := readTrigger("cleardev_mail_attempt_after_extra_forbidden")
	if strings.Contains(legacy, "MAIL_INCREMENT_V2") || !strings.Contains(legacy, "execution_run_id=NEW.execution_run_id AND attempt_kind='HUMAN_EXTRA'") {
		t.Fatal("empty-history downgrade did not restore exact global terminal behavior")
	}
	upTo(t, db, 138)
	if readTrigger("cleardev_mail_attempt_after_extra_forbidden") != terminal || readTrigger("cleardev_mail_attempt_slot_insert") != grant {
		t.Fatal("migration replay changed the terminal or authorization contract")
	}
}
