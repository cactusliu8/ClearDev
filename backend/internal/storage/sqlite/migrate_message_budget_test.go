package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClearDevMessageBudgetMigrationPreservesUnknownUsage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 122)
	const at = "2026-09-05T12:00:00Z"
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mustExec(t, db, `INSERT INTO cleardev_agent_step_attempts (id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,role_binding_id,ao_session_id,client_message_id,prompt_sha256,requested_at) VALUES ('legacy-attempt','dev-upgrade','legacy-step','STANDARD','BUILDER_RESULT',1,'binding','session','legacy-message',?,?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_attempt_events (id,attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,recorded_at) VALUES ('legacy-sent','legacy-attempt','SENT','legacy-message',?,'legacy-turn','completed',?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_parse_corrections(step_id,client_message_id,prompt_text,prompt_sha256,sent_at,attempt_number) VALUES ('legacy-step','legacy-message:parse-correction','原始改正提示',?,?,1)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_step_results(id,attempt_id,result_index,source,client_message_id,turn_id,final_message_id,raw_message_text,raw_message_sha256,observed_at) VALUES ('legacy-result','legacy-attempt',1,'ORIGINAL','legacy-message','legacy-turn','legacy-final','原始结果',?,?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_step_result_parses(result_id,conclusion,error_summary,parsed_at) VALUES ('legacy-result','VALID','',?)`, at)
	upTo(t, db, 123)
	before := map[string][][]string{}
	for _, table := range []string{"cleardev_development_projects", "cleardev_agent_step_attempts", "cleardev_agent_attempt_events", "cleardev_complex_exception_budgets", "cleardev_parse_corrections", "cleardev_agent_step_results", "cleardev_agent_step_result_parses", "change_log"} {
		rows, err := db.Query("SELECT rowid,* FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		before[table] = migrationEvidenceRows(t, rows)
	}
	upTo(t, db, 124)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	for range 2 {
		view, err := reopened.GetClearDevMessageBudget(ctx, "dev-upgrade")
		if err != nil || view.BudgetVersion != "LEGACY_UNMEASURED" || view.UnknownReason == "" || view.ReservedMessages != nil || view.ConfirmedSentMessages != nil || view.RemainingMessages != nil {
			t.Fatalf("legacy=%#v err=%v", view, err)
		}
	}
	raw, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	for table, want := range before {
		rows, err := raw.Query("SELECT rowid,* FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		if got := migrationEvidenceRows(t, rows); !reflect.DeepEqual(got, want) {
			t.Fatalf("old facts changed in %s", table)
		}
	}
	for _, statement := range []string{`UPDATE cleardev_message_budget_versions SET version='MESSAGE_BUDGET_V1'`, `DELETE FROM cleardev_message_budget_versions`} {
		if _, err := raw.Exec(statement); err == nil {
			t.Fatal("legacy version was mutable")
		}
	}
	var violations int
	if err := raw.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign keys=%d err=%v", violations, err)
	}
}
