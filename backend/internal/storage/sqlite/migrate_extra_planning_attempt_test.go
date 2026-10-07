package sqlite

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

func init() { shippedMigrations[184] = "0184_cleardev_extra_planning_attempt.sql" }

func TestMigration0184ExtraPlanningEmptyDownReplay(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 183)
	const schema = `SELECT group_concat(sql,';') FROM (SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' AND name<>'goose_db_version' ORDER BY name)`
	var before, after string
	if err := db.QueryRow(schema).Scan(&before); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 184)
	if _, err := db.Exec(`INSERT INTO cleardev_planning_extra_grants VALUES('unbound','unbound','2026-09-30T00:00:00Z')`); err == nil {
		t.Fatal("unbound native grant admitted")
	}
	downTo(t, db, 183)
	if err := db.QueryRow(schema).Scan(&after); err != nil || after != before {
		t.Fatal("empty downgrade changed original schema", err)
	}
	upTo(t, db, 184)
}

func TestMigration0184ExtraPlanningPreservesPopulatedHistory(t *testing.T) {
	db := openMigrationTestDB(t)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 183)
	const at = "2026-09-29T12:00:00Z"
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mustExec(t, db, `INSERT INTO cleardev_agent_step_attempts(id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,role_binding_id,ao_session_id,client_message_id,prompt_sha256,requested_at,created_at,requested_at_semantics) VALUES('old-one','dev-upgrade','old-step','STANDARD','BUILDER_RESULT',1,'old-role','old-session','old-message',?,?,?,'ACTUAL_CREATION')`, digest, at, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_message_reservations(client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,reserved_at) VALUES('old-message','dev-upgrade','MESSAGE_BUDGET_V1','old-step','old-one','ORIGINAL','old-session',?,?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_message_confirmations VALUES('old-message','old-turn',?)`, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_attempt_events(id,attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,recorded_at) VALUES('old-sent','old-one','SENT','old-message',?,'old-turn','completed',?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_step_results(id,attempt_id,result_index,source,client_message_id,turn_id,final_message_id,raw_message_text,raw_message_sha256,observed_at) VALUES('old-result','old-one',1,'ORIGINAL','old-message','old-turn','old-final','old invalid result',?,?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_step_result_parses VALUES('old-result','INVALID','old parse failure',?)`, at)
	mustExec(t, db, `INSERT INTO cleardev_human_decision_requests(id,development_project_id,decision_kind,binding_schema_version,binding_json,display_json,content_sha256,status,created_at) VALUES('old-decision','dev-upgrade','CONFIRM_REQUIREMENT_VERSION',1,'{"developmentRequirementId":"dev-upgrade","requirementVersionId":"old-version","requirementVersionSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","taskSetVersion":0}','{"title":"old","summary":"original","fullContent":"retain original","changeSummary":"none"}',?,'PENDING',?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_human_decision_dispatches(id,request_id,desktop_run_id,nonce_sha256,issued_at,expires_at,consumed_at,outcome) VALUES('old-dispatch','old-decision','old-desktop',?,'2026-09-29T11:00:00Z','2026-09-29T11:10:00Z',?,'EXPIRED')`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_human_decision_reopens VALUES('old-reopen','old-decision',?,'old-dispatch','old-desktop','next-dispatch',?)`, digest, at)
	tables := []string{"cleardev_human_decision_requests", "cleardev_human_decision_dispatches", "cleardev_human_decision_reopens", "cleardev_development_projects", "cleardev_agent_step_attempts", "cleardev_agent_attempt_events", "cleardev_agent_step_results", "cleardev_agent_step_result_parses", "cleardev_agent_message_reservations", "cleardev_agent_message_confirmations", "cleardev_message_budget_versions", "change_log"}
	read := func(table string) [][]string {
		query := "SELECT rowid,* FROM " + table + " ORDER BY rowid"
		if table == "cleardev_human_decision_reopens" {
			query = "SELECT * FROM " + table + " ORDER BY request_id"
		}
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		return migrationEvidenceRows(t, rows)
	}
	before := map[string][][]string{}
	for _, table := range tables {
		before[table] = read(table)
	}
	upTo(t, db, 184)
	for _, table := range tables {
		if !reflect.DeepEqual(read(table), before[table]) {
			t.Fatal("historical rows or CDC changed", table)
		}
	}
	var violations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatal("foreign keys", violations, err)
	}
	for _, stmt := range []string{`UPDATE cleardev_agent_step_attempts SET prompt_sha256=printf('%064d',0)`, `DELETE FROM cleardev_agent_message_reservations`, `INSERT INTO cleardev_agent_step_attempts(id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,ao_session_id,client_message_id,prompt_sha256,trigger_failure_event_id,requested_at,created_at,requested_at_semantics) VALUES('illegal-third','dev-upgrade','old-step','COMPLEX_PLANNING','REQUIREMENT_COMPILATION',3,'old-session','illegal-message',printf('%064d',0),'old-sent','2026-09-29T12:00:00Z','2026-09-29T12:00:00Z','ACTUAL_CREATION')`} {
		if _, err := db.Exec(stmt); err == nil {
			t.Fatal("protected boundary admitted", stmt)
		}
	}
	if err := migrateDown(183, db); err == nil {
		t.Fatal("populated downgrade did not refuse protected history")
	}

	for _, table := range tables {
		if !reflect.DeepEqual(read(table), before[table]) {
			t.Fatal("guard altered protected facts", table)
		}
	}
}

func TestMigration0184InterruptedRebuildKeepsOriginalSchemaAndRows(t *testing.T) {
	db := openMigrationTestDB(t)
	db.SetMaxOpenConns(1)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 183)
	const at = "2026-09-29T12:00:00Z"
	mustExec(t, db, `INSERT INTO cleardev_agent_step_attempts(id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,ao_session_id,client_message_id,prompt_sha256,requested_at,created_at,requested_at_semantics) VALUES('retained-attempt','dev-upgrade','retained-step','STANDARD','BUILDER_RESULT',1,'session','retained-message',printf('%064d',0),?,?,'ACTUAL_CREATION')`, at, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_message_reservations(client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,reserved_at) VALUES('retained-message','dev-upgrade','MESSAGE_BUDGET_V1','retained-step','retained-attempt','ORIGINAL','session',printf('%064d',0),?)`, at)
	const schema = `SELECT group_concat(sql,';') FROM (SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY name)`
	var before, after, dbName, dbPath string
	var sequence int
	if err := db.QueryRow(schema).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &dbName, &dbPath); err != nil {
		t.Fatal(err)
	}
	raw, err := migrationsFS.ReadFile("migrations/0184_cleardev_extra_planning_attempt.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(raw), "-- +goose Down")[0]
	boundary := strings.Index(up, "CREATE TABLE cleardev_planning_extra_requests")
	if boundary < 0 || !strings.Contains(up[:boundary], "BEGIN IMMEDIATE;") {
		t.Fatal("rebuild must begin one atomic transaction")
	}
	// Execute the real upgrade through both populated parent-table rebuilds,
	// then lose the connection before commit. This models interrupted startup.
	if _, err := db.Exec(up[:boundary]); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("sqlite", "file:"+dbPath+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.QueryRow(schema).Scan(&after); err != nil || after != before {
		t.Fatal("interrupted rebuild changed old schema", err)
	}
	for _, table := range []string{"cleardev_agent_step_attempts", "cleardev_agent_message_reservations"} {
		var count int
		if err := reopened.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatal("interruption lost old rows", table, count, err)
		}
	}
	var violations int
	if err := reopened.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatal("interruption broke foreign keys", violations, err)
	}
	upTo(t, reopened, 184)
}
