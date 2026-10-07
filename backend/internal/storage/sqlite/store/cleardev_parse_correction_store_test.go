package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestClearDevParseCorrectionIsOncePerStep(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpenAt(t, t.TempDir())
	now := time.Now().UTC().Truncate(time.Second)
	first := core.ParseCorrection{
		StepID: "step-parse-1", ClientMessageID: "msg-1:parse-correction",
		PromptText:   "The previous JSON result was rejected by the protocol parser.",
		PromptSHA256: strings.Repeat("c", 64), SentAt: now,
	}
	if err := store.RecordClearDevParseCorrection(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetClearDevParseCorrection(ctx, "step-parse-1")
	if err != nil || !ok || got.ClientMessageID != first.ClientMessageID || got.PromptText != first.PromptText {
		t.Fatalf("get = %+v ok=%v err=%v", got, ok, err)
	}
	second := first
	second.PromptText = "changed"
	second.ClientMessageID = "msg-1:parse-correction-2"
	if err := store.RecordClearDevParseCorrection(ctx, second); err != nil {
		t.Fatal(err)
	}
	again, ok, err := store.GetClearDevParseCorrection(ctx, "step-parse-1")
	if err != nil || !ok || again.PromptText != first.PromptText || again.ClientMessageID != first.ClientMessageID {
		t.Fatalf("second get = %+v ok=%v err=%v", again, ok, err)
	}
	missing, ok, err := store.GetClearDevParseCorrection(ctx, "step-missing")
	if err != nil || ok || missing.StepID != "" {
		t.Fatalf("missing = %+v ok=%v err=%v", missing, ok, err)
	}
}
