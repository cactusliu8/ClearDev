package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0110ClearDevComplexRequirementPlanning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 109)
	upTo(t, db, 110)

	const at = "2026-08-25T09:00:00Z"
	const sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := db.Exec(`INSERT INTO cleardev_complex_requirements (
development_project_id, original_prd_text, original_prd_sha256, target_requirement_version_id, created_at
) VALUES ('dev-upgrade', 'original prd', ?, 'target-v1', ?)`, sha, at); err != nil {
		t.Fatalf("insert complex requirement: %v", err)
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_requirements SET original_prd_text = 'changed' WHERE development_project_id = 'dev-upgrade'`); err == nil {
		t.Fatal("complex requirement update was accepted")
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_role_bindings (
id, development_project_id, role, session_creation_idempotency_key, status, requested_at
) VALUES ('bind-steward', 'dev-upgrade', 'STEWARD', 'spawn-steward', 'REQUESTED', ?)`, at); err != nil {
		t.Fatalf("insert steward binding: %v", err)
	}
	var cdcAfterInsert int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcAfterInsert); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_project_events (
ao_project_id, development_project_id, subject_type, subject_id, action, outcome, reason_code, source, created_at
) VALUES ('ao-1', 'dev-upgrade', 'COMPLEX_REQUIREMENT', 'dev-upgrade', 'CREATE_COMPLEX_REQUIREMENT', 'ACCEPTED', '', 'CONTROL_PLANE', ?)`, at); err != nil {
		t.Fatalf("insert complex project event: %v", err)
	}
	var cdcAfterEvent int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcAfterEvent); err != nil {
		t.Fatal(err)
	}
	if cdcAfterEvent <= cdcAfterInsert {
		t.Fatalf("complex event CDC count = %d, before event = %d", cdcAfterEvent, cdcAfterInsert)
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
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleardev_complex_requirements WHERE development_project_id = 'dev-upgrade'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("complex requirements after reopen = %d", count)
	}
}
