package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0121PreservesLegacyTurnsAndAddsAttemptEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 120)
	const at = "2026-09-05T09:00:00Z"
	mustExec(t, db, `
INSERT INTO conversations (
    id, scope, project_id, session_id, current_session_id,
    active_branch_id, created_at, updated_at
) VALUES (
    'legacy-conversation', 'session', 'ao-1', 'session-1', 'session-1',
    'legacy-conversation:root', ?, ?
);
INSERT INTO conversation_branches (
    id, conversation_id, session_id, provider_conversation_id,
    fork_after_sequence, created_at
) VALUES (
    'legacy-conversation:root', 'legacy-conversation', 'session-1', '', 0, ?
);
INSERT INTO conversation_turns (
    id, conversation_id, handled_by_session_id, provider_turn_id,
    controller_generation, state, error_message, requested_at, completed_at,
    branch_id
) VALUES (
    'legacy-turn', 'legacy-conversation', 'session-1', 'provider-turn',
    'generation-1', 'failed', 'legacy localized error', ?, ?,
    'legacy-conversation:root'
);`, at, at, at, at, at)

	upTo(t, db, 121)
	for _, table := range []string{
		"cleardev_agent_step_attempts", "cleardev_agent_attempt_events",
		"cleardev_agent_step_results", "cleardev_agent_step_result_parses",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("missing 0121 table %s", table)
		}
	}
	var oldMessage, category, providerCode, summary string
	var retryable int
	var retryAt sql.NullTime
	if err := db.QueryRow(`SELECT error_message, failure_category, provider_error_code,
failure_summary, failure_retryable, failure_retry_at
FROM conversation_turns WHERE id = 'legacy-turn'`).Scan(
		&oldMessage, &category, &providerCode, &summary, &retryable, &retryAt,
	); err != nil {
		t.Fatal(err)
	}
	if oldMessage != "legacy localized error" || category != "" || providerCode != "" || summary != "" || retryable != 0 || retryAt.Valid {
		t.Fatalf("legacy turn changed: message=%q category=%q code=%q summary=%q retryable=%d retryAt=%v", oldMessage, category, providerCode, summary, retryable, retryAt)
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleardev_agent_step_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("migration fabricated %d attempts for legacy data", attempts)
	}
}

func TestMigration0122AllowsOneBoundRecoveryAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 121)
	const at = "2026-09-05T12:00:00Z"
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mustExec(t, db, `INSERT INTO cleardev_agent_step_attempts (
id, development_project_id, logical_step_id, step_category, step_kind,
attempt_number, role_binding_id, ao_session_id, client_message_id,
prompt_sha256, requested_at
) VALUES ('attempt-1', 'dev-upgrade', 'step-1', 'STANDARD', 'BUILDER_RESULT',
1, 'binding-1', 'session-1', 'message-1', ?, ?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_attempt_events (
id, attempt_id, status, client_message_id, prompt_sha256, turn_id, turn_state,
failure_category, retryable, error_summary, recorded_at
) VALUES ('failure-1', 'attempt-1', 'FAILED', 'message-1', ?, 'turn-1', 'failed',
'PROVIDER_UNAVAILABLE', 1, 'provider is unavailable', ?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_parse_corrections (
step_id, client_message_id, prompt_text, prompt_sha256, sent_at
) VALUES ('step-old-correction', 'old:parse-correction', 'retry JSON', ?, ?)`, digest, at)

	upTo(t, db, 122)
	var foreignKeyViolations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		t.Fatal(err)
	}
	if foreignKeyViolations != 0 {
		t.Fatalf("migration 0122 foreign key violations=%d", foreignKeyViolations)
	}
	mustExec(t, db, `INSERT INTO cleardev_agent_step_attempts (
id, development_project_id, logical_step_id, step_category, step_kind,
attempt_number, role_binding_id, ao_session_id, client_message_id,
prompt_sha256, trigger_failure_event_id, requested_at
) VALUES ('attempt-2', 'dev-upgrade', 'step-1', 'STANDARD', 'BUILDER_RESULT',
2, 'binding-1', 'session-1', 'message-1:attempt:2', ?, 'failure-1', ?)`, digest, at)

	var attempts int
	var trigger sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*), MAX(trigger_failure_event_id)
FROM cleardev_agent_step_attempts WHERE logical_step_id = 'step-1'`).Scan(&attempts, &trigger); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || !trigger.Valid || trigger.String != "failure-1" {
		t.Fatalf("attempts=%d trigger=%v", attempts, trigger)
	}
	var correctionAttempt int
	if err := db.QueryRow(`SELECT attempt_number FROM cleardev_parse_corrections
WHERE step_id = 'step-old-correction'`).Scan(&correctionAttempt); err != nil {
		t.Fatal(err)
	}
	if correctionAttempt != 1 {
		t.Fatalf("legacy correction attempt=%d, want 1", correctionAttempt)
	}

	_, err = db.Exec(`INSERT INTO cleardev_agent_step_attempts (
id, development_project_id, logical_step_id, step_category, step_kind,
attempt_number, role_binding_id, ao_session_id, client_message_id,
prompt_sha256, trigger_failure_event_id, requested_at
) VALUES ('attempt-3', 'dev-upgrade', 'step-1', 'STANDARD', 'BUILDER_RESULT',
3, 'binding-1', 'session-1', 'message-1:attempt:3', ?, 'failure-1', ?)`, digest, at)
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("third attempt error=%v", err)
	}
	// Preserve populated legacy evidence, including event insertion order and CDC rows.
	mustExec(t, db, `INSERT INTO cleardev_agent_step_results (id, attempt_id, result_index, source, client_message_id, turn_id, final_message_id, raw_message_text, raw_message_sha256, observed_at)
 VALUES ('result-1', 'attempt-1', 1, 'ORIGINAL', 'message-1', 'turn-1', 'final-1', 'legacy raw result', ?, ?)`, digest, at)
	mustExec(t, db, `INSERT INTO cleardev_agent_step_result_parses (result_id, conclusion, error_summary, parsed_at) VALUES ('result-1', 'INVALID', 'legacy parse', ?)`, at)
	tables := []string{"cleardev_agent_step_attempts", "cleardev_agent_attempt_events", "cleardev_agent_step_results", "cleardev_agent_step_result_parses", "change_log"}
	columns := make(map[string][]string)
	before := make(map[string][][]string)
	for _, table := range tables {
		rows, readErr := db.Query("SELECT rowid, * FROM " + table + " ORDER BY rowid")
		if readErr != nil {
			t.Fatal(readErr)
		}
		columns[table], readErr = rows.Columns()
		if readErr != nil {
			t.Fatal(readErr)
		}
		before[table] = migrationEvidenceRows(t, rows)
	}
	upTo(t, db, 123)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for range 2 {
		for _, table := range tables {
			rows, readErr := db.Query("SELECT " + strings.Join(columns[table], ",") + " FROM " + table + " ORDER BY rowid")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if got := migrationEvidenceRows(t, rows); !reflect.DeepEqual(got, before[table]) {
				t.Fatalf("migration changed %s", table)
			}
		}
		var firstSemantics, secondSemantics string
		var unknown int
		if err := db.QueryRow(`SELECT requested_at_semantics FROM cleardev_agent_step_attempts WHERE id='attempt-1'`).Scan(&firstSemantics); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT requested_at_semantics FROM cleardev_agent_step_attempts WHERE id='attempt-2'`).Scan(&secondSemantics); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM cleardev_agent_step_attempts WHERE created_at IS NULL`).Scan(&unknown); err != nil {
			t.Fatal(err)
		}
		if firstSemantics != "LEGACY_STEP_REQUEST" || secondSemantics != "LEGACY_RETRY_BOUNDARY" || unknown != 2 {
			t.Fatalf("legacy semantics: %s %s null=%d", firstSemantics, secondSemantics, unknown)
		}
	}
	for _, statement := range []string{
		`UPDATE cleardev_agent_step_attempts SET created_at=requested_at`,
		`DELETE FROM cleardev_agent_step_attempts`,
		`INSERT INTO cleardev_agent_step_attempts (id, development_project_id, logical_step_id, step_category, step_kind, attempt_number, role_binding_id, ao_session_id, client_message_id, prompt_sha256, requested_at)
   SELECT 'new', development_project_id, 'new-step', step_category, step_kind, 1, role_binding_id, ao_session_id, 'new-message', prompt_sha256, requested_at FROM cleardev_agent_step_attempts WHERE id='attempt-1'`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("constraint accepted %s", statement)
		}
	}
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil || foreignKeyViolations != 0 {
		t.Fatalf("foreign keys=%d err=%v", foreignKeyViolations, err)
	}

}

func migrationEvidenceRows(t *testing.T, rows *sql.Rows) [][]string {
	t.Helper()
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]string
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(values))
		for i, value := range values {
			row[i] = fmt.Sprint(value)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
