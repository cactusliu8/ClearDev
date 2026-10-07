package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
	_ "modernc.org/sqlite"
)

func TestClearDevRequirementVersionConcurrentCreateAcrossStores(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	first := sqlitetest.MustOpenAt(t, dataDir)
	second := openSecondClearDevStore(t, dataDir)
	seedClearDevAO(t, first, "ao-concurrent-create")
	now := time.Now().UTC().Truncate(time.Second)
	if err := first.CreateClearDevRequirement(ctx, initialRequirement("req-concurrent-create", "ao-concurrent-create", now)); err != nil {
		t.Fatal(err)
	}
	confirmVersion(t, first, "req-concurrent-create-v1", now.Add(time.Minute))

	errs := concurrentClearDevCalls(
		func() error {
			text := "requirement v2 first"
			return first.CreateClearDevRequirementVersion(ctx, core.RequirementVersion{
				ID: "req-concurrent-create-v2-a", DevelopmentRequirementID: "req-concurrent-create",
				RequirementText: text, SHA256: requirementDigest(text), Status: core.RequirementVersionStatusDraft,
				CreatedAt: now.Add(2 * time.Minute),
			})
		},
		func() error {
			text := "requirement v2 second"
			return second.CreateClearDevRequirementVersion(ctx, core.RequirementVersion{
				ID: "req-concurrent-create-v2-b", DevelopmentRequirementID: "req-concurrent-create",
				RequirementText: text, SHA256: requirementDigest(text), Status: core.RequirementVersionStatusDraft,
				CreatedAt: now.Add(2 * time.Minute),
			})
		},
	)
	assertConcurrentClearDevResults(t, errs, "create requirement version")

	snapshot, ok, err := first.GetClearDevRequirement(ctx, "req-concurrent-create")
	if err != nil || !ok {
		t.Fatalf("get requirement: ok=%v err=%v", ok, err)
	}
	open := make([]core.RequirementVersion, 0, 1)
	for _, version := range snapshot.RequirementVersions {
		if version.Status == core.RequirementVersionStatusDraft || version.Status == core.RequirementVersionStatusPendingConfirmation {
			open = append(open, version)
		}
	}
	if len(open) != 1 || open[0].Version != 2 {
		t.Fatalf("open requirement versions = %#v, want exactly v2", open)
	}
}

func TestClearDevRequirementVersionConcurrentConfirmationAcrossStores(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	first := sqlitetest.MustOpenAt(t, dataDir)
	second := openSecondClearDevStore(t, dataDir)
	seedClearDevAO(t, first, "ao-concurrent-confirm")
	now := time.Now().UTC().Truncate(time.Second)
	if err := first.CreateClearDevRequirement(ctx, initialRequirement("req-concurrent-confirm", "ao-concurrent-confirm", now)); err != nil {
		t.Fatal(err)
	}
	if err := applyClearDev(t, first, core.ActionSubmitRequirementConfirmation, "req-concurrent-confirm-v1", now.Add(time.Minute), false, ""); err != nil {
		t.Fatal(err)
	}

	errs := concurrentClearDevCalls(
		func() error {
			return applyClearDev(t, first, core.ActionConfirmRequirementVersion, "req-concurrent-confirm-v1", now.Add(2*time.Minute), true, "")
		},
		func() error {
			return applyClearDev(t, second, core.ActionConfirmRequirementVersion, "req-concurrent-confirm-v1", now.Add(2*time.Minute), true, "")
		},
	)
	assertConcurrentClearDevResults(t, errs, "confirm requirement version")

	snapshot, ok, err := first.GetClearDevRequirement(ctx, "req-concurrent-confirm")
	if err != nil || !ok {
		t.Fatalf("get requirement: ok=%v err=%v", ok, err)
	}
	confirmed := 0
	accepted, rejected := 0, 0
	for _, version := range snapshot.RequirementVersions {
		if version.Status == core.RequirementVersionStatusConfirmed {
			confirmed++
		}
	}
	for _, event := range snapshot.Events {
		if event.Action != core.ActionConfirmRequirementVersion {
			continue
		}
		if event.Outcome == core.EventAccepted {
			accepted++
		}
		if event.Outcome == core.EventRejected {
			rejected++
		}
	}
	if confirmed != 1 || accepted != 1 {
		t.Fatalf("confirmation facts: confirmed=%d accepted=%d events=%#v", confirmed, accepted, snapshot.Events)
	}
	// A normal loser reaches the CAS guard and is auditable. SQLite may instead
	// return SQLITE_BUSY before its transaction can write that rejected event.
	if !containsSQLiteBusy(errs) && rejected != 1 {
		t.Fatalf("expected one rejected concurrent confirmation event, got %d", rejected)
	}
}

func TestClearDevLegacyReadyCompatibilityDoesNotExposeReady(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	seedClearDevAO(t, store, "ao-legacy-ready")
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("req-legacy-ready", "ao-legacy-ready", now)); err != nil {
		t.Fatal(err)
	}
	confirmVersion(t, store, "req-legacy-ready-v1", now.Add(time.Minute))

	db := openClearDevRawDB(t, dataDir)
	for _, id := range []string{"ready-start", "ready-resume"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO cleardev_work_items (
id, development_project_id, contract_version_id, title, mode, state,
paused_from_state, max_rework_count, rework_count, created_at, updated_at
) VALUES (?, 'req-legacy-ready', 'req-legacy-ready-v1', ?, 'QUICK', 'READY', NULL, 1, 0, ?, ?)`, id, id, now, now); err != nil {
			t.Fatalf("insert legacy READY task %s: %v", id, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot, _, err := store.GetClearDevRequirement(ctx, "req-legacy-ready")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range snapshot.DevelopmentTasks {
		if task.Status != core.DevelopmentTaskStatusPlanned {
			t.Fatalf("legacy task %s public status = %q, want PLANNED", task.ID, task.Status)
		}
	}
	if err := applyClearDev(t, store, core.ActionStartDevelopmentTask, "ready-start", now.Add(2*time.Minute), false, ""); err != nil {
		t.Fatal(err)
	}
	if err := applyClearDev(t, store, core.ActionPauseDevelopmentTaskNeedsHuman, "ready-resume", now.Add(3*time.Minute), false, "operator pause"); err != nil {
		t.Fatal(err)
	}
	if err := applyClearDev(t, store, core.ActionResumeDevelopmentTask, "ready-resume", now.Add(4*time.Minute), false, ""); err != nil {
		t.Fatal(err)
	}

	db = openClearDevRawDB(t, dataDir)
	defer func() { _ = db.Close() }()
	var startState, resumedState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM cleardev_work_items WHERE id = 'ready-start'`).Scan(&startState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT state FROM cleardev_work_items WHERE id = 'ready-resume'`).Scan(&resumedState); err != nil {
		t.Fatal(err)
	}
	if startState != "RUNNING" || resumedState != "READY" {
		t.Fatalf("legacy physical states = start:%s resume:%s", startState, resumedState)
	}
	snapshot, _, err = store.GetClearDevRequirement(ctx, "req-legacy-ready")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range snapshot.DevelopmentTasks {
		if task.ID == "ready-resume" && task.Status != core.DevelopmentTaskStatusPlanned {
			t.Fatalf("resumed legacy task public status = %q, want PLANNED", task.Status)
		}
	}
	for _, event := range snapshot.Events {
		if event.PreviousState == "READY" || event.TargetState == "READY" {
			t.Fatalf("public event leaked legacy READY: %#v", event)
		}
	}
}

func TestClearDevConfirmedRequirementVersionContentIsImmutable(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	seedClearDevAO(t, store, "ao-immutable")
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("req-immutable", "ao-immutable", now)); err != nil {
		t.Fatal(err)
	}
	confirmVersion(t, store, "req-immutable-v1", now.Add(time.Minute))

	db := openClearDevRawDB(t, dataDir)
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, `UPDATE cleardev_contract_versions
SET contract_text = 'mutated', sha256 = ?
WHERE id = 'req-immutable-v1'`, requirementDigest("mutated")); err == nil {
		t.Fatal("confirmed requirement version content update unexpectedly succeeded")
	}
	var text string
	if err := db.QueryRowContext(ctx, `SELECT contract_text FROM cleardev_contract_versions WHERE id = 'req-immutable-v1'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "requirement v1" {
		t.Fatalf("confirmed requirement text = %q, want original", text)
	}
}

func openSecondClearDevStore(t *testing.T, dataDir string) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("open second SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close second SQLite store: %v", err)
		}
	})
	return store
}

func openClearDevRawDB(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open raw SQLite database: %v", err)
	}
	return db
}

func concurrentClearDevCalls(calls ...func() error) []error {
	start := make(chan struct{})
	errs := make([]error, len(calls))
	var wait sync.WaitGroup
	for index, call := range calls {
		wait.Add(1)
		go func(index int, call func() error) {
			defer wait.Done()
			<-start
			errs[index] = call()
		}(index, call)
	}
	close(start)
	wait.Wait()
	return errs
}

func assertConcurrentClearDevResults(t *testing.T, errs []error, operation string) {
	t.Helper()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
			continue
		}
		var rule *core.RuleError
		if errors.As(err, &rule) || isSQLiteBusy(err) {
			continue
		}
		t.Fatalf("concurrent %s unexpected error: %v", operation, err)
	}
	if successes != 1 {
		t.Fatalf("concurrent %s successes = %d, errors=%v", operation, successes, errs)
	}
}

func containsSQLiteBusy(errs []error) bool {
	for _, err := range errs {
		if isSQLiteBusy(err) {
			return true
		}
	}
	return false
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "sqlite_busy") || strings.Contains(text, "database is locked") || strings.Contains(text, "database is busy")
}
