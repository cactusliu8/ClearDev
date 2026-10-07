package sqlite

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func init() { shippedMigrations[185] = "0185_cleardev_builder_replacement.sql" }

func TestMigration0185BuilderReplacementEmptyDownReplay(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 184)
	const schema = `SELECT group_concat(sql,';') FROM (SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' AND name<>'goose_db_version' ORDER BY name)`
	var before, after string
	if err := db.QueryRow(schema).Scan(&before); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 185)
	for _, statement := range []string{`INSERT INTO cleardev_builder_replacement_grants VALUES('missing','missing','missing','2026-09-30T00:00:00Z')`, `INSERT INTO cleardev_builder_session_fences VALUES('missing','missing','missing','2026-09-30T00:00:00Z')`} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatal("unbound authority admitted")
		}
	}
	downTo(t, db, 184)
	if err := db.QueryRow(schema).Scan(&after); err != nil || before != after {
		i := 0
		for i < len(before) && i < len(after) && before[i] == after[i] {
			i++
		}
		end := i + 450
		if end > len(before) {
			end = len(before)
		}
		end2 := i + 450
		if end2 > len(after) {
			end2 = len(after)
		}
		t.Fatalf("empty downgrade changed original schema at %d: before=%q after=%q err=%v", i, before[i:end], after[i:end2], err)
	}
	upTo(t, db, 185)
}

func TestMigration0185BuilderReplacementPreservesPopulatedHistory(t *testing.T) {
	db := openMigrationTestDB(t)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 184)
	const at = "2026-09-30T00:00:00Z"
	digest := strings.Repeat("a", 64)
	mustExec(t, db, `INSERT INTO cleardev_agent_step_attempts(id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,ao_session_id,client_message_id,prompt_sha256,requested_at,created_at,requested_at_semantics) VALUES('retained-attempt','dev-upgrade','retained-step','STANDARD','BUILDER_RESULT',1,'old-session','old-message',?,?,?,'ACTUAL_CREATION')`, digest, at, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_attempt_events(id,attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,recorded_at) VALUES('retained-sent','retained-attempt','SENT','old-message',?,'old-turn','completed',?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_message_reservations(client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,reserved_at) VALUES('old-message','dev-upgrade','MESSAGE_BUDGET_V1','retained-step','retained-attempt','ORIGINAL','old-session',?,?)`, digest, at)
	tables := []string{"cleardev_agent_step_attempts", "cleardev_agent_attempt_events", "cleardev_agent_message_reservations", "cleardev_development_projects", "change_log"}
	read := func(table string) [][]string {
		rows, err := db.Query("SELECT rowid,* FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		return migrationEvidenceRows(t, rows)
	}
	before := map[string][][]string{}
	for _, table := range tables {
		before[table] = read(table)
	}
	upTo(t, db, 185)
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], read(table)) {
			t.Fatal("old rows or CDC changed", table)
		}
	}
	for _, statement := range []string{
		`INSERT OR REPLACE INTO cleardev_agent_attempt_events SELECT * FROM cleardev_agent_attempt_events WHERE id='retained-sent'`,
		`INSERT OR IGNORE INTO cleardev_agent_attempt_events SELECT * FROM cleardev_agent_attempt_events WHERE id='retained-sent'`,
		`INSERT OR REPLACE INTO cleardev_agent_attempt_events(rowid,id,attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at) SELECT rowid,id||':different-id',attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at FROM cleardev_agent_attempt_events WHERE id='retained-sent'`,
		`INSERT OR IGNORE INTO cleardev_agent_attempt_events(rowid,id,attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at) SELECT rowid,id||':different-id',attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at FROM cleardev_agent_attempt_events WHERE id='retained-sent'`,
	} {
		if _, err := db.Exec(statement); err == nil || !strings.Contains(err.Error(), "agent attempt event identity already exists") {
			t.Fatal("upgrade left retained event identity replaceable or silently ignored", statement, err)
		}
		for _, table := range tables {
			if !reflect.DeepEqual(before[table], read(table)) {
				t.Fatal("rejected historical replacement changed original facts, rowid, time or CDC", table)
			}
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n); err != nil || n != 0 {
		t.Fatal("foreign keys", n, err)
	}
	const schema = `SELECT group_concat(sql,';') FROM (SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY name)`
	var beforeDown, afterDown string
	if err := db.QueryRow(schema).Scan(&beforeDown); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownTo(db, "migrations", 184); err == nil {
		t.Fatal("protected original history admitted a downgrade")
	}
	var version int64
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != 185 {
		t.Fatal("refused historical downgrade changed migration version", version, err)
	}
	if err := db.QueryRow(schema).Scan(&afterDown); err != nil || afterDown != beforeDown {
		t.Fatal("refused historical downgrade changed schema", err)
	}
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], read(table)) {
			t.Fatal("down changed retained rows or CDC", table)
		}
	}
}

func TestMigration0185BuilderReplacementInterruptedUpgradeIsAtomic(t *testing.T) {
	db := openMigrationTestDB(t)
	db.SetMaxOpenConns(1)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 184)
	const at = "2026-09-30T00:00:00Z"
	mustExec(t, db, `INSERT INTO cleardev_agent_step_attempts(id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,ao_session_id,client_message_id,prompt_sha256,requested_at,created_at,requested_at_semantics) VALUES('original-one','dev-upgrade','original-step','STANDARD','BUILDER_RESULT',1,'original-session','original-client',printf('%064d',0),?,?,'ACTUAL_CREATION')`, at, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_attempt_events(id,attempt_id,status,failure_category,recorded_at) VALUES('original-failure','original-one','FAILED','PROVIDER_FAILURE',?)`, at)
	const schema = `SELECT group_concat(sql,';') FROM (SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY name)`
	var before, after, name, path string
	var sequence int
	if err := db.QueryRow(schema).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT rowid,* FROM cleardev_agent_attempt_events ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	original := migrationEvidenceRows(t, rows)
	raw, err := migrationsFS.ReadFile("migrations/0185_cleardev_builder_replacement.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(raw), "-- +goose Down")[0]
	boundary := strings.Index(up, "CREATE TABLE cleardev_builder_replacement_requests")
	if boundary < 0 || !strings.Contains(up[:boundary], "BEGIN IMMEDIATE;") {
		t.Fatal("upgrade must be atomic")
	}
	if _, err := db.Exec(up[:boundary]); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.QueryRow(schema).Scan(&after); err != nil || after != before {
		t.Fatal("interruption changed original schema", err)
	}
	rows, err = reopened.Query(`SELECT rowid,* FROM cleardev_agent_attempt_events ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, migrationEvidenceRows(t, rows)) {
		t.Fatal("interruption changed original event rowid or values")
	}
	upTo(t, reopened, 185)
}
