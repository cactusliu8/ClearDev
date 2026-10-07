package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0111ClearDevDirectionChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 110)
	const at = "2026-08-26T09:00:00Z"
	const sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := db.Exec(`INSERT INTO cleardev_complex_requirements (
development_project_id, original_prd_text, original_prd_sha256, target_requirement_version_id, created_at
) VALUES ('dev-upgrade', 'original prd', ?, 'target-v1', ?)`, sha, at); err != nil {
		t.Fatalf("insert complex requirement: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_role_bindings (
id, development_project_id, role, session_creation_idempotency_key, status, requested_at
) VALUES ('bind-steward', 'dev-upgrade', 'STEWARD', 'spawn-steward', 'REQUESTED', ?)`, at); err != nil {
		t.Fatalf("insert steward binding: %v", err)
	}
	upTo(t, db, 111)

	var cdcBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_direction_intents (
request_id, development_project_id, requirement_version_id, requirement_sha256,
message, message_sha256, steward_role_binding_id, direction_request_id, agent_step_id, created_at
) VALUES ('intent-1', 'dev-upgrade', 'req-3', ?, 'only example.com', ?, 'bind-steward', 'dir-1', 'step-1', ?)`, sha, sha, at); err != nil {
		t.Fatalf("insert direction intent: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_direction_intents (
request_id, development_project_id, requirement_version_id, requirement_sha256,
message, message_sha256, steward_role_binding_id, direction_request_id, agent_step_id, created_at
) VALUES ('intent-2', 'dev-upgrade', 'req-3', ?, 'other', ?, 'bind-steward', 'dir-2', 'step-2', ?)`, sha, sha, at); err == nil {
		t.Fatal("second direction intent for the same version was accepted")
	}
	if _, err := db.Exec(`UPDATE cleardev_direction_intents SET message = 'changed' WHERE request_id = 'intent-1'`); err == nil {
		t.Fatal("direction intent update was accepted")
	}
	var cdcAfter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcAfter); err != nil {
		t.Fatal(err)
	}
	if cdcAfter <= cdcBefore {
		t.Fatalf("0111 CDC count = %d, before insert = %d", cdcAfter, cdcBefore)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate(db); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleardev_direction_intents WHERE request_id = 'intent-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("direction intents after reopen = %d", count)
	}
}

func TestDirectionChangeShippedMigrationHashesRemainFrozenThrough0110(t *testing.T) {
	for file, want := range map[string]string{
		"migrations/0106_cleardev_control_foundation.sql":           "44fb135471e67de24370f7a19f8254db180e0681181fc871912c31438834b8ca",
		"migrations/0107_cleardev_requirement_lifecycle.sql":        "a8bec65b948548f12ce27e098c80bda1916bea13dd0a2357daea90d3834adb7d",
		"migrations/0108_cleardev_standard_single_task.sql":         "ad29d76c72df0b98455a8ec21628ab981dd7bb2dd9899ae31552cd7952ca014e",
		"migrations/0109_cleardev_desktop_human_authority.sql":      "3e9ae8f524cb4ad521030e35cd2d367385b80f3b52e4ae0bcf8d06ec184d21b9",
		"migrations/0110_cleardev_complex_requirement_planning.sql": "62da5c58ab7a09bb386a867b0f1c812c7a6b1d618bdd29ace57c1a449da375fb",
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
