package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"

	_ "modernc.org/sqlite"
)

func TestMigration0109ClearDevDesktopHumanAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 108)
	upTo(t, db, 109)
	const at = "2026-08-24T09:00:00Z"
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.Exec(`INSERT INTO cleardev_human_decision_requests (
id, development_project_id, decision_kind, binding_schema_version, binding_json,
display_json, content_sha256, status, decision, resolved_at, created_at
) VALUES (
'hd-1', 'dev-upgrade', 'CONFIRM_REQUIREMENT_VERSION', 1,
'{"developmentRequirementId":"dev-upgrade","requirementVersionId":"req-open","requirementVersionSha256":"`+sha+`","taskSetVersion":0}',
'{"title":"t","summary":"s","fullContent":"f","changeSummary":"c"}',
?, 'PENDING', '', NULL, ?)`, sha, at); err != nil {
		t.Fatalf("insert human decision request: %v", err)
	}
	if _, err := db.Exec(`UPDATE cleardev_human_decision_requests SET binding_json = '{}' WHERE id = 'hd-1'`); err == nil {
		t.Fatal("request content update was accepted")
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
}

func TestClearDevShippedMigrationHashesRemainFrozenThrough0109(t *testing.T) {
	for file, want := range map[string]string{
		"migrations/0106_cleardev_control_foundation.sql":      "44fb135471e67de24370f7a19f8254db180e0681181fc871912c31438834b8ca",
		"migrations/0107_cleardev_requirement_lifecycle.sql":   "a8bec65b948548f12ce27e098c80bda1916bea13dd0a2357daea90d3834adb7d",
		"migrations/0108_cleardev_standard_single_task.sql":    "ad29d76c72df0b98455a8ec21628ab981dd7bb2dd9899ae31552cd7952ca014e",
		"migrations/0109_cleardev_desktop_human_authority.sql": "3e9ae8f524cb4ad521030e35cd2d367385b80f3b52e4ae0bcf8d06ec184d21b9",
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

func TestMigration0109BackfillCreatesRequestForExistingPendingConfirmation(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 108)
	var cdcBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcBefore); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 109)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.BackfillClearDevHumanDecisionRequests(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	pending, err := store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending human decision requests = %d, want 1", len(pending))
	}
	requestID := pending[0].ID
	db, err = sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cdcAfterFirst int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcAfterFirst); err != nil {
		t.Fatal(err)
	}
	if cdcAfterFirst <= cdcBefore {
		t.Fatalf("backfill CDC count = %d, before = %d", cdcAfterFirst, cdcBefore)
	}
	if err := store.BackfillClearDevHumanDecisionRequests(context.Background()); err != nil {
		t.Fatalf("repeat backfill: %v", err)
	}
	again, err := store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].ID != requestID {
		t.Fatalf("repeat backfill request = %+v, want id %s", again, requestID)
	}
	var cdcAfterRepeat int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdcAfterRepeat); err != nil {
		t.Fatal(err)
	}
	if cdcAfterRepeat != cdcAfterFirst {
		t.Fatalf("repeat backfill CDC count = %d, after first = %d", cdcAfterRepeat, cdcAfterFirst)
	}
	if pending[0].DevelopmentRequirementID != "dev-upgrade" || pending[0].DecisionKind != core.HumanDecisionKindConfirmVersion {
		t.Fatalf("backfilled request = %+v", pending[0])
	}
	if !strings.Contains(pending[0].BindingJSON, `"requirementVersionId":"req-4"`) {
		t.Fatalf("backfilled binding = %s", pending[0].BindingJSON)
	}
	nonce := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	issuedAt := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	offer, err := store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{
		RequestID: requestID, DesktopRunID: "deskrun-bare-boot", Nonce: nonce,
		IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(core.HumanDecisionOfferTTL),
	})
	if err != nil {
		t.Fatalf("issue after bare backfill: %v", err)
	}
	if offer.RequestID != requestID {
		t.Fatalf("desktop offer request id = %s, want %s", offer.RequestID, requestID)
	}
}
