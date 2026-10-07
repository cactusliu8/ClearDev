package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0116TrustedProgressExplanationRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close database: %v", closeErr)
		}
	})
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 115)
	upTo(t, db, 116)

	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'cleardev_progress_explanation_requests'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 {
		t.Fatal("missing 0116 explanation request table")
	}

	var cdcBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcBefore); err != nil {
		t.Fatal(err)
	}
	const at = "2026-08-28T09:00:00Z"
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.Exec(`INSERT INTO cleardev_progress_explanation_requests (
id, development_project_id, fact_summary_sha256, max_event_sequence,
source_steward_session_id, session_creation_idempotency_key, client_message_id,
prompt_text, prompt_sha256, status, reason_code, created_at
) VALUES ('exp-1', 'dev-upgrade', ?, 3, 'session-1', 'cleardev-progress-explanation:exp-1',
'cleardev-progress-explanation-msg-exp-1', 'explain these facts', ?, 'PENDING', '', ?)`, sha, sha, at); err != nil {
		t.Fatalf("insert explanation request: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_progress_explanation_requests (
id, development_project_id, fact_summary_sha256, max_event_sequence,
source_steward_session_id, session_creation_idempotency_key, client_message_id,
prompt_text, prompt_sha256, status, reason_code, created_at
) VALUES ('exp-2', 'dev-upgrade', ?, 3, 'session-1', 'cleardev-progress-explanation:exp-2',
'cleardev-progress-explanation-msg-exp-2', 'explain these facts', ?, 'PENDING', '', ?)`, sha, sha, at); err == nil {
		t.Fatal("second explanation request for the same snapshot was accepted")
	}
	if _, err := db.Exec(`UPDATE cleardev_progress_explanation_requests SET status = 'SETTLED' WHERE id = 'exp-1'`); err == nil {
		t.Fatal("PENDING to SETTLED was accepted")
	}
	if _, err := db.Exec(`DELETE FROM cleardev_progress_explanation_requests WHERE id = 'exp-1'`); err == nil {
		t.Fatal("append-only delete was accepted")
	}
	if _, err := db.Exec(`UPDATE cleardev_progress_explanation_requests
SET ao_session_id = 'session-1' WHERE id = 'exp-1'`); err != nil {
		t.Fatalf("bind pending session: %v", err)
	}
	if _, err := db.Exec(`UPDATE cleardev_progress_explanation_requests
SET status = 'SENT', sent_at = ? WHERE id = 'exp-1'`, at); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if _, err := db.Exec(`UPDATE cleardev_progress_explanation_requests
SET status = 'SETTLED', result_json = '{"summary":"ok"}', result_sha256 = ?, settled_at = ? WHERE id = 'exp-1'`, sha, at); err != nil {
		t.Fatalf("settle: %v", err)
	}
	var cdcAfter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcAfter); err != nil {
		t.Fatal(err)
	}
	if cdcAfter <= cdcBefore {
		t.Fatalf("0116 CDC count = %d, before insert = %d", cdcAfter, cdcBefore)
	}
}

func TestTrustedProgressShippedMigrationHashesRemainFrozenThrough0115(t *testing.T) {
	for file, want := range map[string]string{
		"migrations/0106_cleardev_control_foundation.sql":           "44fb135471e67de24370f7a19f8254db180e0681181fc871912c31438834b8ca",
		"migrations/0107_cleardev_requirement_lifecycle.sql":        "a8bec65b948548f12ce27e098c80bda1916bea13dd0a2357daea90d3834adb7d",
		"migrations/0108_cleardev_standard_single_task.sql":         "ad29d76c72df0b98455a8ec21628ab981dd7bb2dd9899ae31552cd7952ca014e",
		"migrations/0109_cleardev_desktop_human_authority.sql":      "3e9ae8f524cb4ad521030e35cd2d367385b80f3b52e4ae0bcf8d06ec184d21b9",
		"migrations/0110_cleardev_complex_requirement_planning.sql": "62da5c58ab7a09bb386a867b0f1c812c7a6b1d618bdd29ace57c1a449da375fb",
		"migrations/0111_cleardev_direction_change.sql":             "7a5f237dcec9afa9f059c421f45329d4b62d467cf88552b976d417d2001c189c",
		"migrations/0112_cleardev_complex_standard_execution.sql":   "416b5fbfc482b4831a57dc42c98b61400fa5ebe11a55d3822d30b20a102563ef",
		"migrations/0113_cleardev_parallel_execution.sql":           "79611ed495f120180ec0368e7bf6e3306580849d6e0a092e6e632c49383e79c3",
		"migrations/0114_cleardev_quick_execution.sql":              "7cac1611547c65cf0faaac82ae4ef4eea2376dc5db06814b48d9708060b8e6d9",
		"migrations/0115_cleardev_controlled_exceptions.sql":        "d62135dc2a2c750b3d637c1eb25f267b83c67704a2506f584afea514860a6891",
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
