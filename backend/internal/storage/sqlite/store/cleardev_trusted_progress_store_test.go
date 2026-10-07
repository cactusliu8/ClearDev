package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestOccupyClearDevProgressExplanationIsOncePerSnapshot(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpenAt(t, t.TempDir())
	seedClearDevAO(t, store, "ao-progress")
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("req-progress", "ao-progress", now)); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	first := core.ProgressExplanationRequest{
		ID: "exp-1", DevelopmentRequirementID: "req-progress", FactSummarySHA256: hash,
		MaxEventSequence: 2, SourceStewardSessionID: "session-steward",
		SessionCreationIdempotencyKey: "cleardev-progress-explanation:exp-1",
		ClientMessageID:               "cleardev-progress-explanation-msg-exp-1",
		PromptText:                    "explain", PromptSHA256: hash, CreatedAt: now,
	}
	stored, created, err := store.OccupyClearDevProgressExplanation(ctx, core.OccupyProgressExplanationCommand{Request: first})
	if err != nil || !created || stored.ID != "exp-1" || stored.Status != core.ProgressExplanationPending {
		t.Fatalf("first occupy = %+v created=%v err=%v", stored, created, err)
	}
	second := first
	second.ID = "exp-2"
	second.SessionCreationIdempotencyKey = "cleardev-progress-explanation:exp-2"
	second.ClientMessageID = "cleardev-progress-explanation-msg-exp-2"
	again, created, err := store.OccupyClearDevProgressExplanation(ctx, core.OccupyProgressExplanationCommand{Request: second})
	if err != nil || created || again.ID != "exp-1" {
		t.Fatalf("second occupy = %+v created=%v err=%v", again, created, err)
	}
	if ok, bindErr := store.BindClearDevProgressExplanationSession(ctx, "exp-1", "session-live", ""); bindErr != nil || !ok {
		t.Fatalf("bind = %v err=%v", ok, bindErr)
	}
	if ok, sentErr := store.MarkClearDevProgressExplanationSent(ctx, "exp-1", now.Add(time.Second)); sentErr != nil || !ok {
		t.Fatalf("sent = %v err=%v", ok, sentErr)
	}
	if ok, settleErr := store.SettleClearDevProgressExplanation(ctx, "exp-1", `{"summary":"ok"}`, hash, now.Add(2*time.Second)); settleErr != nil || !ok {
		t.Fatalf("settle = %v err=%v", ok, settleErr)
	}
	got, ok, err := store.GetClearDevProgressExplanationBySnapshot(ctx, "req-progress", hash)
	if err != nil || !ok || got.Status != core.ProgressExplanationSettled || got.PromptText != "explain" {
		t.Fatalf("snapshot row = %+v ok=%v err=%v", got, ok, err)
	}
	ids, err := store.ListClearDevRequirementIDsByAOProject(ctx, "ao-progress")
	if err != nil || len(ids) != 1 || ids[0] != "req-progress" {
		t.Fatalf("requirement ids = %v err=%v", ids, err)
	}
}

func TestFailClearDevProgressExplanationFromPending(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpenAt(t, t.TempDir())
	seedClearDevAO(t, store, "ao-progress-fail")
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("req-progress-fail", "ao-progress-fail", now)); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("b", 64)
	req := core.ProgressExplanationRequest{
		ID: "exp-fail", DevelopmentRequirementID: "req-progress-fail", FactSummarySHA256: hash,
		MaxEventSequence: 1, SourceStewardSessionID: "session-steward",
		SessionCreationIdempotencyKey: "cleardev-progress-explanation:exp-fail",
		ClientMessageID:               "cleardev-progress-explanation-msg-exp-fail",
		PromptText:                    "explain", PromptSHA256: hash, CreatedAt: now,
	}
	if _, created, err := store.OccupyClearDevProgressExplanation(ctx, core.OccupyProgressExplanationCommand{Request: req}); err != nil || !created {
		t.Fatalf("occupy err=%v created=%v", err, created)
	}
	if ok, err := store.FailClearDevProgressExplanation(ctx, "exp-fail", core.ReasonCode("STEWARD_RESULT_INVALID"), now.Add(time.Second)); err != nil || !ok {
		t.Fatalf("fail = %v err=%v", ok, err)
	}
	runnable, err := store.ListClearDevRunnableProgressExplanations(ctx)
	if err != nil || len(runnable) != 0 {
		t.Fatalf("runnable after fail = %v err=%v", runnable, err)
	}
}
