package cleardev

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestSQLiteRecoveryWaitsSendsOnceAndReplaysAfterReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlitetest.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ids := &standardTestIDs{}
	requirementID := seedConfirmedStandardRequirement(t, store, ids, clock)
	harness := newStandardAgentHarness(store, false)
	harness.builderTurnFailures = 1
	retryAt := now.Add(time.Hour)
	harness.builderRetryAt = &retryAt
	service := standardTestService(store, store, harness, ids, clock, ctx)
	if _, err := service.StartStandardFlow(ctx, requirementID); err != nil {
		t.Fatal(err)
	}
	countBuilder := func() int {
		n := 0
		for _, r := range harness.relays {
			if strings.Contains(r.prompt, `"kind":"BUILDER_RESULT"`) {
				n++
			}
		}
		return n
	}
	if countBuilder() != 1 {
		t.Fatalf("first sends=%d", countBuilder())
	}
	if err := service.ResumeStandardFlows(ctx); err != nil {
		t.Fatal(err)
	}
	if countBuilder() != 1 {
		t.Fatal("sent before window")
	}
	now = retryAt.Add(time.Minute)
	if err := service.ResumeStandardFlows(ctx); err != nil {
		t.Fatal(err)
	}
	view := assertCompletedStandardFlow(t, service, requirementID)
	var builder []core.AgentStepAttemptView
	for _, a := range view.AgentStepAttempts {
		if a.StepKind == core.AgentStepBuilderResult {
			builder = append(builder, a)
		}
	}
	if len(builder) != 2 || builder[1].CreatedAt == nil || !builder[1].CreatedAt.Equal(now) || !builder[1].RequestedAt.Equal(now) || builder[1].TriggerFailureEventID != builder[0].LastEventID || builder[1].AOSessionID != builder[0].AOSessionID || builder[1].ClientMessageID == builder[0].ClientMessageID || builder[1].SendStatus != core.AgentAttemptCompleted {
		t.Fatalf("builder evidence=%#v", builder)
	}
	if countBuilder() != 2 || len(harness.checkRequests) == 0 {
		t.Fatalf("sends=%d checks=%d", countBuilder(), len(harness.checkRequests))
	}
	for _, a := range builder {
		count := 0
		for _, r := range harness.relays {
			if r.clientMessageID == a.ClientMessageID {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("message %s sends=%d", a.ClientMessageID, count)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	harness.store = reopened
	now = now.Add(time.Hour)
	restarted := standardTestService(reopened, reopened, harness, ids, clock, ctx)
	if err := restarted.ResumeStandardFlows(ctx); err != nil {
		t.Fatal(err)
	}
	assertCompletedStandardFlow(t, restarted, requirementID)
	// Explicit attempt replay also preserves first creation and never grants a send.
	replay := agentAttemptFromView(requirementID, builder[1])
	replay.CreatedAt = &now
	replay.RequestedAt = now
	saved, created, err := reopened.EnsureClearDevAgentStepAttempt(ctx, replay)
	if err != nil || created || !saved.CreatedAt.Equal(*builder[1].CreatedAt) || countBuilder() != 2 {
		t.Fatalf("replay=%#v created=%v sends=%d err=%v", saved, created, countBuilder(), err)
	}
	t.Logf("two original-session messages each sent once; second created %s after retry %s; checks=%d; reopen replay sends=0", builder[1].CreatedAt, retryAt, len(harness.checkRequests))
}

func TestSQLiteConcurrentRecoveryCreatesAndSendsOnce(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, _, _, _, chat, step, _ := recoveryFixture(now)
	attempts := sqlitetest.MustOpen(t)
	requirementID := seedStandardRequirement(t, attempts, &standardTestIDs{}, func() time.Time { return now }, false)
	service.attempts = attempts
	triggerID := seedRetryableFirstAttempt(t, service, requirementID, step, now)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.recoverRequirementAgentAttempts(context.Background(), requirementID)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	views, err := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if err != nil || len(views) != 2 {
		t.Fatalf("attempts=%#v err=%v", views, err)
	}
	if views[1].AttemptNumber != 2 || views[1].TriggerFailureEventID != triggerID ||
		views[1].ClientMessageID != "message-1:attempt:2" || views[1].PromptSHA256 != step.PromptSHA256 {
		t.Fatalf("second attempt=%#v", views[1])
	}

	errs = make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := service.relayAgentTurn(context.Background(), requirementID, core.AgentStepCategoryStandard,
				step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now)
			if failure, ok := ports.ChatFailureFromError(err); ok && failure.Category == domain.AgentFailureDeliveryUnknown {
				err = nil
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if chat.relays != 1 || len(chat.clientMessageIDs) != 1 || chat.clientMessageIDs[0] != "message-1:attempt:2" {
		t.Fatalf("relays=%d ids=%v", chat.relays, chat.clientMessageIDs)
	}
}

func TestSQLiteOriginalAndCorrectionDeliveryReplayIndependently(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, _, _, _, chat, step, _ := recoveryFixture(now)
	store := sqlitetest.MustOpen(t)
	requirementID := seedStandardRequirement(t, store, &standardTestIDs{}, func() time.Time { return now }, false)
	service.attempts = store
	for range 2 {
		for _, correction := range []bool{false, true} {
			message, prompt, status := step.ClientMessageID, "prompt", core.AgentAttemptSent
			if correction {
				message += ":parse-correction"
				prompt = "correction"
				status = core.AgentAttemptCorrectionSent
			}
			if err := service.relayAgentTurn(context.Background(), requirementID, core.AgentStepCategoryStandard, step, "session-1", prompt, message, status, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if chat.relays != 2 {
		t.Fatalf("original/correction sends=%d", chat.relays)
	}
}
