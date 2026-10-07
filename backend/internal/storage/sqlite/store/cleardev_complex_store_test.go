package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestClearDevComplexCreateIsAtomicAndBlocksStandard(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "ao-complex", Path: "/tmp/ao-complex", Kind: domain.ProjectKindSingleRepo, RegisteredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	prd := "original complex prd"
	if err := store.CreateClearDevComplexRequirement(ctx, core.CreateComplexRequirementCommand{
		Requirement: core.DevelopmentRequirement{
			ID: "dev-complex", AOProjectID: "ao-complex", Name: "complex", CreatedAt: now, UpdatedAt: now,
		},
		OriginalPRDText: prd, OriginalPRDSHA256: requirementDigest(prd),
		TargetRequirementVersionID: "target-v1",
		StewardRoleBinding: core.ComplexRoleBinding{
			ID: "bind-steward", DevelopmentRequirementID: "dev-complex", Role: core.StandardRoleSteward,
			SessionCreationIdempotencyKey: "cleardev-complex:steward:dev-complex:bind-steward",
			Status:                        core.RoleBindingStatusRequested, RequestedAt: now,
		},
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, ok, err := store.GetClearDevRequirement(ctx, "dev-complex")
	if err != nil || !ok {
		t.Fatalf("load requirement: ok=%v err=%v", ok, err)
	}
	if len(snapshot.RequirementVersions) != 0 || len(snapshot.DevelopmentTasks) != 0 {
		t.Fatalf("complex create left versions/tasks: %#v", snapshot)
	}
	planning, exists, err := store.GetClearDevComplexPlanning(ctx, "dev-complex")
	if err != nil || !exists {
		t.Fatalf("complex marker missing: exists=%v err=%v", exists, err)
	}
	if planning.Requirement.OriginalPRDText != prd || planning.Requirement.TargetRequirementVersionID != "target-v1" ||
		len(planning.RoleBindings) != 1 || planning.RoleBindings[0].Role != core.StandardRoleSteward {
		t.Fatalf("complex snapshot = %#v", planning)
	}
	if core.DeriveOverallProgress(snapshot, now).Phase != core.OverallPhaseDefiningRequirement {
		t.Fatalf("S01 phase = %s, want DEFINING_REQUIREMENT", core.DeriveOverallProgress(snapshot, now).Phase)
	}

	_, created, err := store.StartClearDevStandardFlow(ctx, core.StartStandardFlowCommand{
		DevelopmentRequirementID: "dev-complex", StewardRoleBindingID: "std-bind",
		StewardSessionIdempotencyKey: "standard-key", At: now,
	})
	if created || err == nil {
		t.Fatal("complex requirement started a STANDARD flow")
	}
	var rule *core.RuleError
	if !errors.As(err, &rule) || rule.Code != core.ReasonComplexPlanRequired {
		t.Fatalf("standard gate = %v", err)
	}
	if _, ok, err := store.GetClearDevStandardFlow(ctx, "dev-complex"); err != nil || ok {
		t.Fatalf("STANDARD facts were written: ok=%v err=%v", ok, err)
	}

	extra := core.RequirementVersion{
		ID: "extra-v1", DevelopmentRequirementID: "dev-complex", Version: 0,
		RequirementText: "not allowed", SHA256: requirementDigest("not allowed"),
		Status: core.RequirementVersionStatusDraft, CreatedAt: now,
	}
	if err := store.CreateClearDevRequirementVersion(ctx, extra); err == nil {
		t.Fatal("simple version create succeeded on a complex requirement")
	}
	snapshot, _, err = store.GetClearDevRequirement(ctx, "dev-complex")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RequirementVersions) != 0 {
		t.Fatalf("rejected extra version left a version: %#v", snapshot.RequirementVersions)
	}

	db := openClearDevRawDB(t, dataDir)
	t.Cleanup(func() { _ = db.Close() })
	var events, cdc, tasks, standardBindings int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cleardev_project_events WHERE development_project_id = 'dev-complex'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdc); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cleardev_work_items WHERE development_project_id = 'dev-complex'`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cleardev_standard_role_bindings WHERE development_project_id = 'dev-complex'`).Scan(&standardBindings); err != nil {
		t.Fatal(err)
	}
	if events == 0 || cdc == 0 || tasks != 0 || standardBindings != 0 {
		t.Fatalf("facts after create: events=%d cdc=%d tasks=%d standardBindings=%d", events, cdc, tasks, standardBindings)
	}
}

func TestClearDevComplexCreateInvalidLeavesNoFacts(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	err := store.CreateClearDevComplexRequirement(ctx, core.CreateComplexRequirementCommand{
		Requirement: core.DevelopmentRequirement{
			ID: "missing-project", AOProjectID: "missing", Name: "complex", CreatedAt: now, UpdatedAt: now,
		},
		OriginalPRDText: "prd", OriginalPRDSHA256: requirementDigest("prd"),
		TargetRequirementVersionID: "target-v1",
		StewardRoleBinding: core.ComplexRoleBinding{
			ID: "bind", DevelopmentRequirementID: "missing-project", Role: core.StandardRoleSteward,
			SessionCreationIdempotencyKey: "spawn-key", Status: core.RoleBindingStatusRequested, RequestedAt: now,
		},
	})
	if err == nil {
		t.Fatal("missing AO project created a complex requirement")
	}
	if _, ok, getErr := store.GetClearDevRequirement(ctx, "missing-project"); getErr != nil || ok {
		t.Fatalf("half-created requirement: ok=%v err=%v", ok, getErr)
	}
}

func TestClearDevComplexConcurrentCreatesAreDistinct(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "ao-race", Path: "/tmp/ao-race", Kind: domain.ProjectKindSingleRepo, RegisteredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("dev-race-%d", i)
			prd := "prd-" + id
			errs[i] = store.CreateClearDevComplexRequirement(ctx, core.CreateComplexRequirementCommand{
				Requirement: core.DevelopmentRequirement{
					ID: id, AOProjectID: "ao-race", Name: id, CreatedAt: now, UpdatedAt: now,
				},
				OriginalPRDText: prd, OriginalPRDSHA256: requirementDigest(prd),
				TargetRequirementVersionID: id + "-v1",
				StewardRoleBinding: core.ComplexRoleBinding{
					ID: id + "-bind", DevelopmentRequirementID: id, Role: core.StandardRoleSteward,
					SessionCreationIdempotencyKey: "spawn-" + id, Status: core.RoleBindingStatusRequested, RequestedAt: now,
				},
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	first, ok, err := store.GetClearDevComplexPlanning(ctx, "dev-race-0")
	if err != nil || !ok {
		t.Fatalf("first requirement missing: ok=%v err=%v", ok, err)
	}
	second, ok, err := store.GetClearDevComplexPlanning(ctx, "dev-race-1")
	if err != nil || !ok {
		t.Fatalf("second requirement missing: ok=%v err=%v", ok, err)
	}
	if first.Requirement.DevelopmentRequirementID == second.Requirement.DevelopmentRequirementID {
		t.Fatal("concurrent creates shared one development requirement")
	}
}

func TestClearDevComplexStandardGateWritesNoRejectedEvent(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "ao-gate", Path: "/tmp/ao-gate", Kind: domain.ProjectKindSingleRepo, RegisteredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	prd := "prd"
	if err := store.CreateClearDevComplexRequirement(ctx, core.CreateComplexRequirementCommand{
		Requirement: core.DevelopmentRequirement{
			ID: "dev-gate", AOProjectID: "ao-gate", Name: "gate", CreatedAt: now, UpdatedAt: now,
		},
		OriginalPRDText: prd, OriginalPRDSHA256: requirementDigest(prd),
		TargetRequirementVersionID: "target-v1",
		StewardRoleBinding: core.ComplexRoleBinding{
			ID: "bind", DevelopmentRequirementID: "dev-gate", Role: core.StandardRoleSteward,
			SessionCreationIdempotencyKey: "spawn-gate", Status: core.RoleBindingStatusRequested, RequestedAt: now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	db := openClearDevRawDB(t, dataDir)
	t.Cleanup(func() { _ = db.Close() })
	var eventsBefore int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cleardev_project_events WHERE development_project_id = 'dev-gate'`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.StartClearDevStandardFlow(ctx, core.StartStandardFlowCommand{
		DevelopmentRequirementID: "dev-gate", StewardRoleBindingID: "std",
		StewardSessionIdempotencyKey: "std-key", At: now,
	})
	if err == nil {
		t.Fatal("standard flow started")
	}
	var eventsAfter, standardBindings int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cleardev_project_events WHERE development_project_id = 'dev-gate'`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cleardev_standard_role_bindings WHERE development_project_id = 'dev-gate'`).Scan(&standardBindings); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore || standardBindings != 0 {
		t.Fatalf("standard gate mutated facts: events %d->%d bindings=%d", eventsBefore, eventsAfter, standardBindings)
	}
}
