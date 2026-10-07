package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestClearDevControlledPreflightIsAppendOnlyAndLatestWins(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpenAt(t, t.TempDir())
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "ao-preflight", Path: "/tmp/ao-preflight", Kind: domain.ProjectKindSingleRepo, RegisteredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("req-preflight", "ao-preflight", now)); err != nil {
		t.Fatal(err)
	}
	catalog, _ := json.Marshal([]string{"gpt-5.4"})
	sum := sha256.Sum256(catalog)
	first := core.ControlledPreflight{
		ID: "pf-1", DevelopmentRequirementID: "req-preflight", RoleBindingID: "bind-steward",
		AOProjectID: "ao-preflight", RequestedModel: "gpt-5.6-sol", Provider: "codex",
		CatalogJSON: string(catalog), CatalogSHA256: hex.EncodeToString(sum[:]),
		Outcome: core.ControlledPreflightFailed, ReasonCode: core.ReasonModelNotAvailable,
		Retryable: true, ErrorSummary: "requested model is not in the provider catalog", CheckedAt: now,
	}
	if err := store.RecordClearDevControlledPreflight(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "pf-2"
	second.RequestedModel = "gpt-5.4"
	second.ResolvedModel = "gpt-5.4"
	second.Outcome = core.ControlledPreflightPassed
	second.ReasonCode = core.ReasonNone
	second.ErrorSummary = ""
	second.CheckedAt = now.Add(time.Second)
	if err := store.RecordClearDevControlledPreflight(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetLatestClearDevControlledPreflight(ctx, "req-preflight")
	if err != nil || !ok || got.ID != "pf-2" || got.Outcome != core.ControlledPreflightPassed || got.ResolvedModel != "gpt-5.4" {
		t.Fatalf("latest = %+v ok=%v err=%v", got, ok, err)
	}
	byBinding, ok, err := store.GetLatestClearDevControlledPreflightForBinding(ctx, "bind-steward")
	if err != nil || !ok || byBinding.ID != "pf-2" {
		t.Fatalf("binding latest = %+v ok=%v err=%v", byBinding, ok, err)
	}
}
