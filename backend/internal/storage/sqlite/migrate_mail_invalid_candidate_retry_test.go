package sqlite

import (
	"strings"
	"testing"
)

func init() { shippedMigrations[139] = "0139_cleardev_mail_invalid_candidate_retry.sql" }

func TestMigration0139AllowsInvalidCandidateRetryFromLastFrozenSHA(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 139)
	readTrigger := func(name string) string {
		t.Helper()
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		return ddl
	}
	slot := readTrigger("cleardev_mail_attempt_slot_insert")
	for _, want := range []string{
		"CANDIDATE_INVALID",
		"attempt_kind='DEVELOPMENT'",
		"c.commit_sha=json_extract(h.binding_json,'$.candidateSha')",
		"MAIL_ATTEMPTS_EXHAUSTED",
	} {
		if !strings.Contains(slot, want) {
			t.Fatalf("slot trigger omitted %q: %s", want, slot)
		}
	}
	attempt := readTrigger("cleardev_complex_execution_attempt_insert_valid")
	if !strings.Contains(attempt, "CANDIDATE_INVALID") {
		t.Fatalf("attempt trigger omitted invalid-candidate continuation: %s", attempt)
	}
	downTo(t, db, 138)
	legacy := readTrigger("cleardev_mail_attempt_slot_insert")
	if strings.Contains(legacy, "CANDIDATE_INVALID") {
		t.Fatal("downgrade kept invalid-candidate retry")
	}
	upTo(t, db, 139)
	if readTrigger("cleardev_mail_attempt_slot_insert") != slot || readTrigger("cleardev_complex_execution_attempt_insert_valid") != attempt {
		t.Fatal("migration replay changed the retry contract")
	}
	if !strings.Contains(readTrigger("cleardev_mail_attempt_after_extra_forbidden"), "mail human extra attempt is terminal") {
		t.Fatal("invalid-candidate retry dropped the extra-terminal guard")
	}
}
