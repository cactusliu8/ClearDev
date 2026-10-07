package sqlite

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0107ClearDevRequirementLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	seedClearDev0106LifecycleFixture(t, db)

	var cdcBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcBefore); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 107)
	assertClearDev0107Lifecycle(t, db, cdcBefore)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// A reopened database has the same facts and goose does not replay 0107.
	db, err = sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate(db); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	assertClearDev0107Lifecycle(t, db, cdcBefore)
}

func seedClearDev0106LifecycleFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	upTo(t, db, 106)
	const at = "2026-08-24T09:00:00Z"
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.Exec(`INSERT INTO projects (id, path, registered_at, config, kind)
VALUES ('ao-1', '/repo', ?, '{}', 'single_repo')`, at); err != nil {
		t.Fatalf("seed AO project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (
id, project_id, num, activity_last_at, created_at, updated_at
) VALUES ('session-1', 'ao-1', 1, ?, ?, ?)`, at, at, at); err != nil {
		t.Fatalf("seed AO session: %v", err)
	}
	for _, row := range []struct{ id, state string }{
		{"dev-upgrade", "INTEGRATING"},
		{"dev-cancel", "CANCELLED"},
	} {
		if _, err := db.Exec(`INSERT INTO cleardev_development_projects (
id, ao_project_id, name, state, paused_from_state, created_at, updated_at
) VALUES (?, 'ao-1', ?, ?, NULL, ?, ?)`, row.id, row.id, row.state, at, at); err != nil {
			t.Fatalf("seed development project %s: %v", row.id, err)
		}
	}
	for _, row := range []struct {
		id, projectID, state, approvedAt string
		version                          int
	}{
		{"req-1", "dev-upgrade", "APPROVED", at, 1},
		{"req-2", "dev-upgrade", "DRAFT", "", 2},
		{"req-3", "dev-upgrade", "APPROVED", at, 3},
		{"req-4", "dev-upgrade", "IN_REVIEW", "", 4},
	} {
		if _, err := db.Exec(`INSERT INTO cleardev_contract_versions (
id, development_project_id, version, contract_text, sha256, state,
superseded_by_id, created_at, approved_at
) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, NULLIF(?, ''))`,
			row.id, row.projectID, row.version, row.id, sha, row.state, at, row.approvedAt); err != nil {
			t.Fatalf("seed requirement version %s: %v", row.id, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cleardev_work_items (
id, development_project_id, contract_version_id, title, mode, state,
paused_from_state, max_rework_count, rework_count, created_at, updated_at
) VALUES ('ready-legacy', 'dev-upgrade', 'req-3', 'ready', 'QUICK', 'READY', NULL, 1, 0, ?, ?)`, at, at); err != nil {
		t.Fatalf("seed READY work item: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_path_permission_versions (
id, work_item_id, version, write_paths, forbidden_paths,
shared_paths_require_approval, generated_paths, created_at
) VALUES ('permission-1', 'ready-legacy', 1, '[]', '[]', '[]', '[]', ?)`, at); err != nil {
		t.Fatalf("seed permission: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_candidate_commits (
id, work_item_id, sequence, ao_session_id, permission_version_id, commit_sha, created_at
) VALUES ('candidate-legacy', 'ready-legacy', 1, 'session-1', 'permission-1', ?, ?)`, sha[:40], at); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_evidence (
id, development_project_id, subject_type, subject_id, evidence_kind,
evidence_key, result, candidate_commit_id, integration_candidate_id,
commit_sha, source_type, source_ao_session_id, created_at, expires_at
) VALUES ('evidence-legacy', 'dev-upgrade', 'WORK_ITEM', 'ready-legacy', 'SCOPE',
'', 'PASS', 'candidate-legacy', NULL, ?, 'CONTROL_PLANE_CHECKER', NULL, ?, NULL)`, sha[:40], at); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_integration_candidates (
id, development_project_id, sequence, ao_session_id, commit_sha, created_at
) VALUES ('integration-legacy', 'dev-upgrade', 1, 'session-1', ?, ?)`, sha[:40], at); err != nil {
		t.Fatalf("seed legacy integration candidate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_project_events (
ao_project_id, development_project_id, subject_type, subject_id, action,
previous_state, target_state, outcome, reason_code, reason_text, source,
source_ao_session_id, created_at
) VALUES
('ao-1', 'dev-cancel', 'DEVELOPMENT_PROJECT', 'dev-cancel', 'CANCEL_PROJECT',
 'RUNNING', 'CANCELLED', 'ACCEPTED', '', 'older reason', 'CONTROL_PLANE', NULL, '2026-08-24T10:00:00Z'),
('ao-1', 'dev-cancel', 'DEVELOPMENT_PROJECT', 'dev-cancel', 'CANCEL_PROJECT',
 'BLOCKED', 'CANCELLED', 'ACCEPTED', 'USER_CANCELLED', 'latest reason', 'CONTROL_PLANE', NULL, '2026-08-24T11:00:00Z')`); err != nil {
		t.Fatalf("seed cancellation events: %v", err)
	}
}

func assertClearDev0107Lifecycle(t *testing.T, db *sql.DB, cdcBefore int) {
	t.Helper()
	var state, replacement string
	if err := db.QueryRow(`SELECT state, COALESCE(superseded_by_id, '')
FROM cleardev_contract_versions WHERE id = 'req-1'`).Scan(&state, &replacement); err != nil {
		t.Fatal(err)
	}
	if state != "SUPERSEDED" || replacement != "req-3" {
		t.Fatalf("legacy confirmed repair = (%s, %s), want (SUPERSEDED, req-3)", state, replacement)
	}
	for _, want := range []struct{ id, state string }{{"req-3", "APPROVED"}, {"req-2", "REJECTED"}, {"req-4", "IN_REVIEW"}} {
		if err := db.QueryRow(`SELECT state FROM cleardev_contract_versions WHERE id = ?`, want.id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != want.state {
			t.Fatalf("%s state = %s, want %s", want.id, state, want.state)
		}
	}
	var taskSet int
	if err := db.QueryRow(`SELECT task_set_version FROM cleardev_contract_versions WHERE id = 'req-3'`).Scan(&taskSet); err != nil {
		t.Fatal(err)
	}
	if taskSet != 0 {
		t.Fatalf("legacy task set version = %d, want 0", taskSet)
	}
	if err := db.QueryRow(`SELECT state FROM cleardev_work_items WHERE id = 'ready-legacy'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "READY" {
		t.Fatalf("legacy READY work item was rewritten to %q", state)
	}
	var text string
	if err := db.QueryRow(`SELECT contract_text FROM cleardev_contract_versions WHERE id = 'req-3'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "req-3" {
		t.Fatalf("legacy requirement text = %q, want req-3", text)
	}
	for table, id := range map[string]string{
		"cleardev_candidate_commits": "candidate-legacy",
		"cleardev_evidence":          "evidence-legacy",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("legacy row %s in %s count = %d, want 1", id, table, count)
		}
	}
	var requirementID sql.NullString
	var integrationTaskSet sql.NullInt64
	if err := db.QueryRow(`SELECT requirement_version_id, task_set_version
FROM cleardev_integration_candidates WHERE id = 'integration-legacy'`).Scan(&requirementID, &integrationTaskSet); err != nil {
		t.Fatal(err)
	}
	if requirementID.Valid || integrationTaskSet.Valid {
		t.Fatalf("legacy integration bindings = (%+v, %+v), want NULL, NULL", requirementID, integrationTaskSet)
	}
	var cancelledAt, reasonCode, reasonText string
	if err := db.QueryRow(`SELECT cancelled_at, cancel_reason_code, cancel_reason_text
FROM cleardev_development_projects WHERE id = 'dev-cancel'`).Scan(&cancelledAt, &reasonCode, &reasonText); err != nil {
		t.Fatal(err)
	}
	if cancelledAt != "2026-08-24T11:00:00Z" || reasonCode != "USER_CANCELLED" || reasonText != "latest reason" {
		t.Fatalf("cancellation backfill = (%q, %q, %q)", cancelledAt, reasonCode, reasonText)
	}

	var migrationEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleardev_project_events WHERE source = 'MIGRATION'`).Scan(&migrationEvents); err != nil {
		t.Fatal(err)
	}
	if migrationEvents != 3 {
		t.Fatalf("migration events = %d, want 3", migrationEvents)
	}
	var legacyCancelEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleardev_project_events
WHERE development_project_id = 'dev-cancel' AND action = 'CANCEL_PROJECT'`).Scan(&legacyCancelEvents); err != nil {
		t.Fatal(err)
	}
	if legacyCancelEvents != 2 {
		t.Fatalf("legacy cancellation events = %d, want 2", legacyCancelEvents)
	}
	for _, reason := range []string{"LEGACY_MULTIPLE_CONFIRMED", "LEGACY_OPEN_VERSION_RETIRED", "LEGACY_CANCELLED"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM cleardev_project_events WHERE source = 'MIGRATION' AND reason_code = ?`, reason).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("migration events with %s = %d, want 1", reason, count)
		}
	}
	var cdcAfter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcAfter); err != nil {
		t.Fatal(err)
	}
	if cdcAfter-cdcBefore != migrationEvents {
		t.Fatalf("migration CDC delta = %d, want %d", cdcAfter-cdcBefore, migrationEvents)
	}
	var payload string
	if err := db.QueryRow(`SELECT payload FROM change_log
WHERE event_type = 'cleardev_project_updated' ORDER BY seq DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode migration CDC payload: %v", err)
	}
	if decoded["outcome"] != "ACCEPTED" {
		t.Fatalf("migration CDC payload = %s", payload)
	}

	// The partial indexes are live after repair, and new integration candidates
	// cannot use the nullable legacy binding shape.
	if _, err := db.Exec(`INSERT INTO cleardev_contract_versions (
id, development_project_id, version, contract_text, sha256, state,
superseded_by_id, created_at, approved_at, task_set_version
) VALUES ('req-5', 'dev-upgrade', 5, 'req-5',
'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'APPROVED', NULL,
'2026-08-24T12:00:00Z', '2026-08-24T12:00:00Z', 0)`); err == nil {
		t.Fatal("expected current confirmed partial index to reject a second APPROVED version")
	}
	if _, err := db.Exec(`INSERT INTO cleardev_integration_candidates (
id, development_project_id, sequence, ao_session_id, commit_sha, created_at,
requirement_version_id, task_set_version
) VALUES ('integration-without-binding', 'dev-upgrade', 2, 'session-1',
'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', '2026-08-24T12:00:00Z', NULL, NULL)`); err == nil {
		t.Fatal("expected new integration candidate without v3 bindings to be rejected")
	}
}
