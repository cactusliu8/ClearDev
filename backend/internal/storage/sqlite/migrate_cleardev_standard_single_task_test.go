package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0108ClearDevStandardSingleTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 107)
	upTo(t, db, 108)
	assertClearDev0108StandardFacts(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening proves that 0108 was recorded and that a normal startup does
	// not replay its schema changes or disturb append-only legacy facts.
	db, err = sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate(db); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	assertClearDev0108StandardFactsPersistAfterReopen(t, db)
}

func TestMigration0108StandardDoneGateRejectsDirectSQLWithoutIntegrationFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 107)
	upTo(t, db, 108)
	assertClearDev0108StandardFacts(t, db)
	seedClearDev0108AcceptedCompletionGateFixture(t, db, "scope")

	if _, err := db.Exec(`UPDATE cleardev_work_items SET state = 'DONE' WHERE id = 'standard-gate-task'`); err == nil {
		t.Fatal("direct S02 DONE without an integration candidate was accepted")
	}
	mustExec0108(t, db, `INSERT INTO cleardev_integration_candidates (
id, development_project_id, requirement_version_id, task_set_version,
sequence, ao_session_id, commit_sha, dispatch_id, source_candidate_commit_id, created_at
) VALUES ('standard-gate-integration', 'dev-upgrade', 'req-3', 1,
2, 'session-3', ?, 'standard-gate-dispatch', 'standard-gate-candidate', ?)`, standardGateSHA, standardGateAt)
	if _, err := db.Exec(`UPDATE cleardev_work_items SET state = 'DONE' WHERE id = 'standard-gate-task'`); err == nil {
		t.Fatal("direct S02 DONE without source-bound integration evidence was accepted")
	}
}

func TestMigration0108StandardDoneGateRejectsAuxiliaryScopeCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 107)
	upTo(t, db, 108)
	assertClearDev0108StandardFacts(t, db)
	seedClearDev0108AcceptedCompletionGateFixture(t, db, "review-worktree")

	mustExec0108(t, db, `INSERT INTO cleardev_integration_candidates (
id, development_project_id, requirement_version_id, task_set_version,
sequence, ao_session_id, commit_sha, dispatch_id, source_candidate_commit_id, created_at
) VALUES ('standard-gate-integration', 'dev-upgrade', 'req-3', 1,
2, 'session-3', ?, 'standard-gate-dispatch', 'standard-gate-candidate', ?)`, standardGateSHA, standardGateAt)
	mustExec0108(t, db, `INSERT INTO cleardev_evidence (
id, development_project_id, subject_type, subject_id, evidence_kind,
evidence_key, result, candidate_commit_id, integration_candidate_id,
commit_sha, source_type, source_ao_session_id, candidate_check_run_id,
local_review_id, created_at, expires_at
) VALUES ('standard-gate-integration-evidence', 'dev-upgrade', 'DEVELOPMENT_PROJECT', 'dev-upgrade', 'INTEGRATION',
'', 'PASS', NULL, 'standard-gate-integration', ?, 'CONTROL_PLANE_CHECKER', NULL,
'standard-gate-integration-run', NULL, ?, NULL)`, standardGateSHA, standardGateAt)
	if _, err := db.Exec(`UPDATE cleardev_work_items SET state = 'DONE' WHERE id = 'standard-gate-task'`); err == nil {
		t.Fatal("auxiliary review-worktree SCOPE pass satisfied the mandatory scope gate")
	}

	insertStandardGateScopeRun(t, db, "standard-gate-real-scope", "scope")
	if _, err := db.Exec(`UPDATE cleardev_work_items SET state = 'DONE' WHERE id = 'standard-gate-task'`); err != nil {
		t.Fatalf("real scope pass did not satisfy the otherwise complete DONE gate: %v", err)
	}
}

const (
	standardGateAt     = "2026-08-24T14:00:00Z"
	standardGateSHA    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	standardGateDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// seedClearDev0108AcceptedCompletionGateFixture constructs one accepted S02
// review round by SQL so the migration-level DONE trigger is tested without
// relying on a Go storage method to provide the protection being asserted.
func seedClearDev0108AcceptedCompletionGateFixture(t *testing.T, db *sql.DB, scopeCheckName string) {
	t.Helper()
	mustExec0108(t, db, `INSERT INTO sessions (
id, project_id, num, harness, session_mode, permission_mode,
creation_idempotency_key, creation_request_fingerprint,
activity_last_at, created_at, updated_at
) VALUES ('session-3', 'ao-1', 3, 'codex', 'chat', 'auto',
'builder-gate-spawn', 'builder-gate-fingerprint', ?, ?, ?),
('session-4', 'ao-1', 4, 'codex', 'chat', 'auto',
'reviewer-gate-spawn', 'reviewer-gate-fingerprint', ?, ?, ?)`,
		standardGateAt, standardGateAt, standardGateAt, standardGateAt, standardGateAt, standardGateAt)

	settleStep0108(t, db, "planner-gate-step", "planner-binding", "ENGINEERING_PLAN", "planner-gate-request", "planner-gate-message", "planner-gate-turn", "planner-gate-final")
	mustExec0108(t, db, `INSERT INTO cleardev_standard_engineering_plans (
id, requirement_version_id, requirement_sha256, version, planner_role_binding_id,
agent_step_id, turn_id, final_message_id, plan_json, plan_sha256, created_at
) VALUES ('standard-gate-plan', 'req-3', ?, 1, 'planner-binding',
'planner-gate-step', 'planner-gate-turn', 'planner-gate-final', '{}', ?, ?)`, standardGateDigest, standardGateDigest, standardGateAt)

	settleStep0108(t, db, "steward-gate-review-step", "steward-binding", "PLAN_REVIEW", "plan-gate", "steward-gate-review-message", "steward-gate-review-turn", "steward-gate-review-final")
	mustExec0108(t, db, `INSERT INTO cleardev_standard_plan_reviews (
id, engineering_plan_id, steward_role_binding_id, agent_step_id,
turn_id, final_message_id, verdict, reason_code, summary, created_at
) VALUES ('standard-gate-plan-review', 'standard-gate-plan', 'steward-binding', 'steward-gate-review-step',
'steward-gate-review-turn', 'steward-gate-review-final', 'APPROVED', 'PLAN_ACCEPTABLE', 'approved', ?)`, standardGateAt)

	settleStep0108(t, db, "steward-gate-dispatch-step", "steward-binding", "DISPATCH_REQUEST", "dispatch-gate", "steward-gate-dispatch-message", "steward-gate-dispatch-turn", "steward-gate-dispatch-final")
	mustExec0108(t, db, `INSERT INTO cleardev_standard_role_bindings (
id, development_project_id, requirement_version_id, role, dispatch_id,
session_creation_idempotency_key, status, requested_at
) VALUES ('builder-gate-binding', 'dev-upgrade', 'req-3', 'BUILDER', 'standard-gate-dispatch',
'builder-gate-spawn', 'REQUESTED', ?)`, standardGateAt)
	mustExec0108(t, db, `INSERT INTO cleardev_standard_dispatches (
id, requirement_version_id, engineering_plan_id, plan_review_id, steward_role_binding_id,
agent_step_id, mode, preallocated_work_item_id, expected_task_set_version,
execution_package_json, execution_package_sha256, builder_session_idempotency_key,
builder_role_binding_id, base_commit_sha, status, reason_code, requested_at, decided_at
) VALUES ('standard-gate-dispatch', 'req-3', 'standard-gate-plan', 'standard-gate-plan-review', 'steward-binding',
'steward-gate-dispatch-step', 'STANDARD', 'standard-gate-task', 0,
'{}', ?, 'builder-gate-spawn', 'builder-gate-binding', NULL, 'PENDING', '', ?, NULL)`, standardGateDigest, standardGateAt)
	mustExec0108(t, db, `INSERT INTO cleardev_work_items (
id, development_project_id, contract_version_id, title, mode, state,
paused_from_state, max_rework_count, rework_count, accepted_dispatch_id,
dispatch_base_commit_sha, created_at, updated_at
) VALUES ('standard-gate-task', 'dev-upgrade', 'req-3', 'standard gate', 'STANDARD', 'RUNNING',
NULL, 1, 0, 'standard-gate-dispatch', ?, ?, ?)`, standardGateSHA, standardGateAt, standardGateAt)
	mustExec0108(t, db, `UPDATE cleardev_contract_versions SET task_set_version = 1 WHERE id = 'req-3'`)
	mustExec0108(t, db, `UPDATE cleardev_standard_role_bindings
SET ao_session_id = 'session-3', status = 'BOUND', bound_at = ?
WHERE id = 'builder-gate-binding'`, standardGateAt)
	mustExec0108(t, db, `UPDATE cleardev_standard_dispatches
SET status = 'ACCEPTED', base_commit_sha = ?, decided_at = ?
WHERE id = 'standard-gate-dispatch'`, standardGateSHA, standardGateAt)
	mustExec0108(t, db, `INSERT INTO cleardev_path_permission_versions (
id, work_item_id, version, write_paths, forbidden_paths,
shared_paths_require_approval, generated_paths, created_at
) VALUES ('standard-gate-permission', 'standard-gate-task', 1, '[]', '[]', '[]', '[]', ?)`, standardGateAt)
	mustExec0108(t, db, `INSERT INTO cleardev_candidate_commits (
id, work_item_id, sequence, ao_session_id, permission_version_id,
dispatch_id, base_commit_sha, commit_sha, created_at
) VALUES ('standard-gate-candidate', 'standard-gate-task', 1, 'session-3', 'standard-gate-permission',
'standard-gate-dispatch', ?, ?, ?)`, standardGateSHA, standardGateSHA, standardGateAt)
	mustExec0108(t, db, `INSERT INTO cleardev_required_checks (
id, work_item_id, name, check_kind, created_at
) VALUES ('standard-gate-required', 'standard-gate-task', 'email-unit', 'REQUIRED_CHECK', ?)`, standardGateAt)
	for _, invalid := range []struct {
		id, result, reason string
	}{
		{"standard-gate-pass-with-reason", "PASS", "SCOPE_CHECK_FAILED"},
		{"standard-gate-fail-without-reason", "FAIL", ""},
	} {
		if _, err := db.Exec(`INSERT INTO cleardev_candidate_check_runs (
id, work_item_id, candidate_commit_id, dispatch_id, base_commit_sha, candidate_commit_sha,
check_kind, check_name, check_spec_sha256, argv_json, exit_code,
status, timed_out, output_summary, output_sha256, changed_paths_json, result,
created_at, settled_at, reason_code
) VALUES (?, 'standard-gate-task', 'standard-gate-candidate', 'standard-gate-dispatch', ?, ?,
'SCOPE', 'scope-reason-gate', ?, '[]', 1,
'SETTLED', 0, 'observed result', ?, '[]', ?, ?, ?, ?)`, invalid.id, standardGateSHA, standardGateSHA,
			standardGateDigest, standardGateDigest, invalid.result, standardGateAt, standardGateAt, invalid.reason); err == nil {
			t.Fatalf("candidate check accepted result=%s reason=%q", invalid.result, invalid.reason)
		}
	}
	mustExec0108(t, db, `INSERT INTO cleardev_candidate_check_runs (
id, work_item_id, candidate_commit_id, dispatch_id, base_commit_sha, candidate_commit_sha,
check_kind, check_name, check_spec_sha256, argv_json, exit_code,
status, timed_out, output_summary, output_sha256, changed_paths_json, result,
created_at, settled_at, reason_code
) VALUES ('standard-gate-failed-scope', 'standard-gate-task', 'standard-gate-candidate', 'standard-gate-dispatch', ?, ?,
'SCOPE', 'scope-reason-gate', ?, '[]', 1,
'SETTLED', 0, 'observed failure', ?, '[]', 'FAIL', ?, ?, 'SCOPE_CHECK_FAILED')`,
		standardGateSHA, standardGateSHA, standardGateDigest, standardGateDigest, standardGateAt, standardGateAt)
	for _, check := range []struct {
		id, kind, name, argv, image string
	}{
		{"standard-gate-scope", "SCOPE", scopeCheckName, "[]", ""},
		{"standard-gate-required-run", "REQUIRED_CHECK", "email-unit", `["node","--test","email"]`, "sha256:test-image"},
		{"standard-gate-integration-run", "INTEGRATION", "node-all", `["node","--test"]`, "sha256:test-image"},
	} {
		mustExec0108(t, db, `INSERT INTO cleardev_candidate_check_runs (
id, work_item_id, candidate_commit_id, dispatch_id, base_commit_sha, candidate_commit_sha,
check_kind, check_name, check_spec_sha256, argv_json, container_image_id, exit_code,
status, timed_out, output_summary, output_sha256, changed_paths_json, result,
created_at, settled_at, reason_code
) VALUES (?, 'standard-gate-task', 'standard-gate-candidate', 'standard-gate-dispatch', ?, ?,
?, ?, ?, ?, NULLIF(?, ''), 0,
'SETTLED', 0, 'pass', ?, '[]', 'PASS', ?, ?, '')`,
			check.id, standardGateSHA, standardGateSHA, check.kind, check.name, standardGateDigest, check.argv, check.image,
			standardGateDigest, standardGateAt, standardGateAt)
	}
	mustExec0108(t, db, `INSERT INTO cleardev_standard_role_bindings (
id, development_project_id, requirement_version_id, role, dispatch_id, candidate_commit_id,
session_creation_idempotency_key, ao_session_id, status, requested_at, bound_at
) VALUES ('reviewer-gate-binding', 'dev-upgrade', 'req-3', 'REVIEWER', 'standard-gate-dispatch', 'standard-gate-candidate',
'reviewer-gate-spawn', 'session-4', 'BOUND', ?, ?)`, standardGateAt, standardGateAt)
	settleStep0108(t, db, "reviewer-gate-step", "reviewer-gate-binding", "LOCAL_REVIEW", "review-gate", "reviewer-gate-message", "reviewer-gate-turn", "reviewer-gate-final")
	mustExec0108(t, db, `INSERT INTO cleardev_standard_local_reviews (
id, candidate_commit_id, dispatch_id, review_packet_json, review_packet_sha256,
reviewer_role_binding_id, agent_step_id, status, turn_id, final_message_id,
verdict, reason_code, summary, created_at, settled_at
) VALUES ('standard-gate-review', 'standard-gate-candidate', 'standard-gate-dispatch', '{}', ?,
'reviewer-gate-binding', 'reviewer-gate-step', 'SETTLED', 'reviewer-gate-turn', 'reviewer-gate-final',
'PASS', 'REVIEW_PASSED', 'pass', ?, ?)`, standardGateDigest, standardGateAt, standardGateAt)
}

func insertStandardGateScopeRun(t *testing.T, db *sql.DB, id, name string) {
	t.Helper()
	mustExec0108(t, db, `INSERT INTO cleardev_candidate_check_runs (
id, work_item_id, candidate_commit_id, dispatch_id, base_commit_sha, candidate_commit_sha,
check_kind, check_name, check_spec_sha256, argv_json, exit_code,
status, timed_out, output_summary, output_sha256, changed_paths_json, result,
created_at, settled_at, reason_code
) VALUES (?, 'standard-gate-task', 'standard-gate-candidate', 'standard-gate-dispatch', ?, ?,
'SCOPE', ?, ?, '[]', 0,
'SETTLED', 0, 'pass', ?, '[]', 'PASS', ?, ?, '')`, id, standardGateSHA, standardGateSHA,
		name, standardGateDigest, standardGateDigest, standardGateAt, standardGateAt)
}

func settleStep0108(t *testing.T, db *sql.DB, id, bindingID, kind, requestID, messageID, turnID, finalID string) {
	t.Helper()
	mustExec0108(t, db, `INSERT INTO cleardev_standard_agent_steps (
id, role_binding_id, step_kind, request_id, client_message_id, prompt_sha256,
send_status, requested_at
) VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?)`, id, bindingID, kind, requestID, messageID, standardGateDigest, standardGateAt)
	mustExec0108(t, db, `UPDATE cleardev_standard_agent_steps SET send_status = 'SENT', sent_at = ? WHERE id = ?`, standardGateAt, id)
	mustExec0108(t, db, `UPDATE cleardev_standard_agent_steps
SET send_status = 'SETTLED', turn_id = ?, final_message_id = ?, final_message_text = '{}',
message_sha256 = ?, completed_at = ? WHERE id = ?`, turnID, finalID, standardGateDigest, standardGateAt, id)
}

func assertClearDev0108StandardFacts(t *testing.T, db *sql.DB) {
	t.Helper()
	const at = "2026-08-24T13:00:00Z"
	const sha256Hex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// Existing S01 rows must retain the legacy NULL shape after the S02 columns
	// are added. The ALTER ... REFERENCES columns also prove SQLite accepts a
	// parent table created later in the same migration.
	var dispatchID, baseCommit sql.NullString
	if err := db.QueryRow(`SELECT accepted_dispatch_id, dispatch_base_commit_sha
FROM cleardev_work_items WHERE id = 'ready-legacy'`).Scan(&dispatchID, &baseCommit); err != nil {
		t.Fatal(err)
	}
	if dispatchID.Valid || baseCommit.Valid {
		t.Fatalf("legacy work item S02 bindings = (%+v, %+v), want NULL", dispatchID, baseCommit)
	}
	var foreignParent string
	rows, err := db.Query(`PRAGMA foreign_key_list(cleardev_work_items)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if from == "accepted_dispatch_id" {
			foreignParent = table
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if foreignParent != "cleardev_standard_dispatches" {
		t.Fatalf("accepted_dispatch_id FK parent = %q, want cleardev_standard_dispatches", foreignParent)
	}

	// The session identity columns are optional for existing sessions but remain
	// strict for a new spawned session.
	if _, err := db.Exec(`UPDATE sessions
SET harness = 'codex', session_mode = 'chat', permission_mode = 'auto'
WHERE id = 'session-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions
SET creation_idempotency_key = 'only-key'
WHERE id = 'session-1'`); err == nil {
		t.Fatal("expected session key/fingerprint pairing check to reject one-sided key")
	}
	if _, err := db.Exec(`UPDATE sessions SET permission_mode = 'unsafe' WHERE id = 'session-1'`); err == nil {
		t.Fatal("expected permission mode check to reject an unknown mode")
	}
	if _, err := db.Exec(`INSERT INTO sessions (
id, project_id, num, harness, session_mode, permission_mode,
creation_idempotency_key, creation_request_fingerprint,
activity_last_at, created_at, updated_at
) VALUES ('session-2', 'ao-1', 2, 'codex', 'chat', 'auto',
'planner-spawn', 'planner-fingerprint', ?, ?, ?)`, at, at, at); err != nil {
		t.Fatalf("seed spawned Planner session: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (
id, project_id, num, harness, session_mode, permission_mode,
creation_idempotency_key, creation_request_fingerprint,
activity_last_at, created_at, updated_at
) VALUES ('session-3', 'ao-1', 3, 'codex', 'chat', 'auto',
'planner-spawn', 'planner-fingerprint', ?, ?, ?)`, at, at, at); err == nil {
		t.Fatal("expected duplicate non-empty creation idempotency key to be rejected")
	}

	// An existing auto Codex Chat Steward is allowed even though it predates the
	// creation key. Non-Steward bindings must match their spawned session key.
	mustExec0108(t, db, `INSERT INTO cleardev_standard_role_bindings (
id, development_project_id, requirement_version_id, role,
session_creation_idempotency_key, ao_session_id, status, requested_at, bound_at
) VALUES ('steward-binding', 'dev-upgrade', 'req-3', 'STEWARD',
'steward-spawn', 'session-1', 'BOUND', ?, ?)`, at, at)
	if _, err := db.Exec(`INSERT INTO cleardev_standard_role_bindings (
id, development_project_id, requirement_version_id, role,
session_creation_idempotency_key, ao_session_id, status, requested_at, bound_at
) VALUES ('planner-wrong-key', 'dev-upgrade', 'req-3', 'ENGINEERING_PLANNER',
'wrong-key', 'session-1', 'BOUND', ?, ?)`, at, at); err == nil {
		t.Fatal("expected Planner binding to require the session creation key")
	}
	mustExec0108(t, db, `INSERT INTO cleardev_standard_role_bindings (
id, development_project_id, requirement_version_id, role,
session_creation_idempotency_key, ao_session_id, status, requested_at, bound_at
) VALUES ('planner-binding', 'dev-upgrade', 'req-3', 'ENGINEERING_PLANNER',
'planner-spawn', 'session-2', 'BOUND', ?, ?)`, at, at)
	if _, err := db.Exec(`INSERT INTO cleardev_standard_role_bindings (
id, development_project_id, requirement_version_id, role,
session_creation_idempotency_key, status, requested_at
) VALUES ('reviewer-without-candidate', 'dev-upgrade', 'req-3', 'REVIEWER',
'reviewer-spawn', 'REQUESTED', ?)`, at); err == nil {
		t.Fatal("expected Reviewer binding without candidate to be rejected")
	}

	// A role can fail before a message was sent. This persists the unavailable
	// reason rather than fabricating a SENT state.
	mustExec0108(t, db, `INSERT INTO cleardev_standard_agent_steps (
id, role_binding_id, step_kind, request_id, client_message_id, prompt_sha256,
send_status, requested_at
) VALUES ('planner-step', 'planner-binding', 'ENGINEERING_PLAN', 'request-1',
'message-1', ?, 'PENDING', ?)`, sha256Hex, at)
	mustExec0108(t, db, `UPDATE cleardev_standard_agent_steps
SET send_status = 'FAILED', failed_at = ?, reason_code = 'PLANNER_UNAVAILABLE'
WHERE id = 'planner-step'`, at)
	var status, reason string
	var sentAtNull sql.NullString
	if err := db.QueryRow(`SELECT send_status, sent_at, reason_code
FROM cleardev_standard_agent_steps WHERE id = 'planner-step'`).Scan(&status, &sentAtNull, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || sentAtNull.Valid || reason != "PLANNER_UNAVAILABLE" {
		t.Fatalf("pre-send failure = (%q, %+v, %q), want FAILED, NULL, PLANNER_UNAVAILABLE", status, sentAtNull, reason)
	}
	// A protocol request can intentionally span two role steps (for example the
	// Steward's planning request and the Planner's response request). Only the
	// client message is globally unique; request identity is scoped to its role
	// and step kind.
	mustExec0108(t, db, `INSERT INTO cleardev_standard_agent_steps (
id, role_binding_id, step_kind, request_id, client_message_id, prompt_sha256,
send_status, requested_at
) VALUES ('steward-step', 'steward-binding', 'REQUEST_PLANNING', 'request-1',
'message-steward', ?, 'PENDING', ?)`, sha256Hex, at)
	mustExec0108(t, db, `UPDATE cleardev_standard_agent_steps
SET send_status = 'SENT', sent_at = ? WHERE id = 'steward-step'`, at)
	if _, err := db.Exec(`UPDATE cleardev_standard_agent_steps
SET send_status = 'SETTLED', turn_id = 'turn-steward', final_message_id = 'final-steward',
message_sha256 = ?, completed_at = ? WHERE id = 'steward-step'`, sha256Hex, at); err == nil {
		t.Fatal("expected settled step without final message text to be rejected")
	}
	mustExec0108(t, db, `UPDATE cleardev_standard_agent_steps
SET send_status = 'SETTLED', turn_id = 'turn-steward', final_message_id = 'final-steward',
final_message_text = '{"version":1}', message_sha256 = ?, completed_at = ?
WHERE id = 'steward-step'`, sha256Hex, at)
	var finalText string
	if err := db.QueryRow(`SELECT final_message_text FROM cleardev_standard_agent_steps
WHERE id = 'steward-step'`).Scan(&finalText); err != nil {
		t.Fatal(err)
	}
	if finalText != `{"version":1}` {
		t.Fatalf("persisted final message text = %q", finalText)
	}

	// Existing CDC remains database-triggered. S02 storage emits project events,
	// so this is the relevant end-to-end schema assertion instead of a manual
	// change_log write.
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	mustExec0108(t, db, `INSERT INTO cleardev_project_events (
ao_project_id, development_project_id, subject_type, subject_id, action,
previous_state, target_state, outcome, reason_code, reason_text, source,
source_ao_session_id, created_at
) VALUES ('ao-1', 'dev-upgrade', 'AGENT_STEP', 'planner-step', 'FAIL_STEP',
NULL, NULL, 'ACCEPTED', 'PLANNER_UNAVAILABLE', '', 'CONTROL_PLANE', 'session-2', ?)`, at)
	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("CDC delta = %d, want 1", after-before)
	}
}

func assertClearDev0108StandardFactsPersistAfterReopen(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleardev_standard_role_bindings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("role binding count after reopen = %d, want 2", count)
	}
	var status string
	var sentAt sql.NullString
	if err := db.QueryRow(`SELECT send_status, sent_at
FROM cleardev_standard_agent_steps WHERE id = 'planner-step'`).Scan(&status, &sentAt); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || sentAt.Valid {
		t.Fatalf("planner step after reopen = (%q, %+v), want FAILED, NULL", status, sentAt)
	}
	var foreignParent string
	rows, err := db.Query(`PRAGMA foreign_key_list(cleardev_work_items)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if from == "accepted_dispatch_id" {
			foreignParent = table
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if foreignParent != "cleardev_standard_dispatches" {
		t.Fatalf("accepted_dispatch_id FK parent after reopen = %q", foreignParent)
	}
}

func mustExec0108(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("exec 0108 fixture statement: %v", err)
	}
}

func TestClearDevShippedMigrationHashesRemainFrozen(t *testing.T) {
	t.Helper()
	for file, want := range map[string]string{
		"migrations/0106_cleardev_control_foundation.sql":    "44fb135471e67de24370f7a19f8254db180e0681181fc871912c31438834b8ca",
		"migrations/0107_cleardev_requirement_lifecycle.sql": "a8bec65b948548f12ce27e098c80bda1916bea13dd0a2357daea90d3834adb7d",
	} {
		content, err := migrationsFS.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(content)); got != want {
			t.Errorf("%s SHA-256 = %s, want %s", file, got, want)
		}
	}
}
