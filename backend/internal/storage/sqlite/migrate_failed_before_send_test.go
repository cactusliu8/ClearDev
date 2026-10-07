package sqlite

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func init() { shippedMigrations[192] = "0192_cleardev_failed_before_send.sql" }

func preSendMigrationRows(t *testing.T, db *sql.DB, table string) [][]string {
	t.Helper()
	rows, err := db.Query("SELECT rowid,* FROM " + table + " ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	return migrationEvidenceRows(t, rows)
}

func TestPreSendMigrationPreservesRowsAndHistoricalDownGuard(t *testing.T) {
	db := openMigrationTestDB(t)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 191)
	const at = "2026-10-02T15:00:00Z"
	sha := strings.Repeat("a", 64)
	if _, err := db.Exec(`INSERT INTO cleardev_agent_step_attempts
(id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,role_binding_id,ao_session_id,client_message_id,prompt_sha256,requested_at,created_at,requested_at_semantics)
VALUES('presend-first','dev-upgrade','presend-step','STANDARD','BUILDER_RESULT',1,'builder','session-1','presend-message',?,?,?,'ACTUAL_CREATION')`, sha, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_agent_attempt_events
(id,attempt_id,status,client_message_id,prompt_sha256,failure_category,error_summary,recorded_at)
VALUES('presend-old-unknown','presend-first','DELIVERY_UNKNOWN','presend-message',?,'DELIVERY_UNKNOWN','original unknown is not rewritten',?)`, sha, at); err != nil {
		t.Fatal(err)
	}
	tables := []string{"cleardev_agent_step_attempts", "cleardev_agent_attempt_events", "cleardev_agent_message_reservations", "cleardev_human_decision_requests", "change_log"}
	before := map[string][][]string{}
	for _, table := range tables {
		before[table] = preSendMigrationRows(t, db, table)
	}
	upTo(t, db, 192)
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], preSendMigrationRows(t, db, table)) {
			t.Fatal("upgrade changed historical row bytes/rowids", table)
		}
	}
	var integrity string
	var invalid int
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatal(invalid, err)
	}
	if _, err := db.Exec(`UPDATE cleardev_agent_attempt_events SET error_summary='overwrite' WHERE id='presend-old-unknown'`); err == nil {
		t.Fatal("upgrade removed event immutability")
	}
	if err := migrateDown(190, db); err == nil {
		t.Fatal("protected old attempt history was allowed to downgrade")
	}
	if version := gooseVersion(t, db); version != 192 {
		t.Fatal("refused downgrade changed version", version)
	}
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], preSendMigrationRows(t, db, table)) {
			t.Fatal("refused downgrade changed history", table)
		}
	}
}

func TestPreSendMigrationEmptyRoundTrip(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 191)
	var old string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='cleardev_agent_attempt_events'`).Scan(&old); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 192)
	downTo(t, db, 191)
	var restored string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='cleardev_agent_attempt_events'`).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != old {
		t.Fatal("round trip changed the predecessor schema")
	}
	upTo(t, db, 192)
}

func TestPreSendMigrationFailureRollsBackSchemaAndVersion(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 191)
	var before string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='cleardev_agent_attempt_events'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// This isolated test fixture forces failure after 0192 edits the schema.
	if _, err := db.Exec(`CREATE TRIGGER cleardev_failed_before_send_binding BEFORE INSERT ON cleardev_agent_attempt_events BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	gooseMu.Lock()
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	err := goose.UpTo(db, "migrations", 192)
	gooseMu.Unlock()
	if err == nil {
		t.Fatal("expected deliberate duplicate-trigger failure")
	}
	if version := gooseVersion(t, db); version != 191 {
		t.Fatal("failed upgrade changed version", version)
	}
	var after string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='cleardev_agent_attempt_events'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("failed upgrade left a partial schema widening")
	}
	if _, err := db.Exec(`DROP TRIGGER cleardev_failed_before_send_binding`); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 192)
}
