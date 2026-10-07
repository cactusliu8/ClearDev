package cleardev

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// seedPreSendAttempt preserves the original reservation and unknown boundary.
// The following explicit no-send proof never refunds either reservation.
func seedPreSendAttempt(t *testing.T, s *Service, requirementID string, step core.AgentStep, now time.Time) core.AgentAttemptEvent {
	t.Helper()
	ctx := context.Background()
	a, err := s.ensureAgentAttempt(ctx, requirementID, core.AgentStepCategoryStandard, step, "session-1", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	boundary := core.AgentAttemptEvent{ID: a.ID + ":unknown", AttemptID: a.ID, Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: a.ClientMessageID, PromptSHA256: a.PromptSHA256, FailureCategory: domain.AgentFailureDeliveryUnknown, RecordedAt: now}
	if claimed, err := s.attempts.ReserveClearDevAgentMessage(ctx, core.ReserveAgentMessageCommand{Attempt: a, Source: core.AgentMessageOriginal, Boundary: boundary}); err != nil || !claimed {
		t.Fatal("reserve first", claimed, err)
	}
	proof := core.AgentAttemptEvent{ID: a.ID + ":no-send", AttemptID: a.ID, Status: core.AgentAttemptSendStatus("FAILED_BEFORE_SEND"), ClientMessageID: a.ClientMessageID, PromptSHA256: a.PromptSHA256, Retryable: true, ErrorSummary: "message send did not start", RecordedAt: now}
	if err := s.attempts.RecordClearDevAgentAttemptEvent(ctx, proof); err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestPreSendRecordedFailureRecoversExactlyOnce(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	s, attempts, _, _, chat, step, requirementID := recoveryFixture(now)
	proof := seedPreSendAttempt(t, s, requirementID, step, now)
	if stopped, err := s.recoverRequirementAgentAttempts(ctx, requirementID); err != nil || stopped {
		t.Fatal("recover", stopped, err)
	}
	views, err := attempts.ListClearDevAgentStepAttempts(ctx, requirementID)
	if err != nil || len(views) != 2 {
		t.Fatalf("pre-send proof did not create the one second attempt: count=%d err=%v", len(views), err)
	}
	if views[1].TriggerFailureEventID != proof.ID || views[1].AOSessionID != views[0].AOSessionID || views[1].ClientMessageID != step.ClientMessageID+":attempt:2" {
		t.Fatal("second attempt changed its binding", views)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.relayAgentTurn(ctx, requirementID, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now)
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
	if chat.relays != 1 || len(chat.clientMessageIDs) != 1 || chat.clientMessageIDs[0] != step.ClientMessageID+":attempt:2" {
		t.Fatal("second message duplicated", chat.relays, chat.clientMessageIDs)
	}
	views, err = attempts.ListClearDevAgentStepAttempts(ctx, requirementID)
	if err != nil || len(views) != 2 || views[0].SendStatus != proof.Status || views[1].SendStatus != core.AgentAttemptSent {
		t.Fatal("history or send receipt changed", views, err)
	}
	ledger, err := attempts.GetClearDevMessageBudget(ctx, requirementID)
	if err != nil || len(ledger.Steps) != 1 || *ledger.Steps[0].ReservedMessages != 2 || *ledger.Steps[0].ConfirmedSentMessages != 1 {
		t.Fatal("reservation was refunded or double-counted", ledger, err)
	}
}

func TestPreSendProofStillHonorsLoginAndQuotaPreflight(t *testing.T) {
	for _, problem := range []error{ports.ErrChatAuthRequired, ports.ErrChatQuotaExhausted} {
		t.Run(problem.Error(), func(t *testing.T) {
			now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
			s, attempts, _, checker, chat, step, requirementID := recoveryFixture(now)
			seedPreSendAttempt(t, s, requirementID, step, now)
			checker.setError(problem)
			stopped, err := s.recoverRequirementAgentAttempts(context.Background(), requirementID)
			views, readErr := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
			if err != nil || !stopped || readErr != nil || len(views) != 1 || chat.relays != 0 {
				t.Fatal("preflight was bypassed", stopped, err, len(views), chat.relays)
			}
		})
	}
}

func TestPreSendUnknownWithoutSnapshotNeverRetries(t *testing.T) {
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	s, attempts, _, _, chat, step, requirementID := recoveryFixture(now)
	chat.err = errors.New("provider accepted but response lost")
	for range 3 {
		if err := s.relayAgentTurn(context.Background(), requirementID, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); err == nil {
			t.Fatal("unknown delivery became successful")
		}
	}
	views, err := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if err != nil || len(views) != 1 || views[0].SendStatus != core.AgentAttemptDeliveryUnknown || chat.relays != 1 {
		t.Fatal("unknown without a snapshot was resent", views, chat.relays, err)
	}
}

func TestPreSendLocalClassificationAndSecondFailureStop(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	s, attempts, _, _, chat, step, requirementID := recoveryFixture(now)
	chat.err = errors.Join(ports.ErrChatSendNotStarted, ports.ErrBuilderSessionOperationBusy)
	for range 4 {
		if err := s.relayAgentTurn(ctx, requirementID, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); err == nil {
			t.Fatal("local refusal became successful delivery")
		}
	}
	states, err := attempts.ListClearDevAgentStepAttempts(ctx, requirementID)
	if err != nil || len(states) != 2 || !core.FailedBeforeSendViewValid(states[0]) || !core.FailedBeforeSendViewValid(states[1]) || chat.relays != 2 {
		t.Fatal("second local failure was retried or misclassified", states, chat.relays, err)
	}
	if stopped, err := s.recoverRequirementAgentAttempts(ctx, requirementID); err != nil || !stopped {
		t.Fatal("second failure did not stop", stopped, err)
	}
	ledger, err := attempts.GetClearDevMessageBudget(ctx, requirementID)
	if err != nil || *ledger.Steps[0].ReservedMessages != 2 || *ledger.Steps[0].ConfirmedSentMessages != 0 {
		t.Fatal("no-send failure reset reservations", ledger, err)
	}
}

func TestPreSendMarkerCannotOverrideProviderOrCorrection(t *testing.T) {
	for _, provider := range []error{ports.ErrChatAuthRequired, ports.ErrChatQuotaExhausted, ports.ErrChatRateLimited} {
		t.Run(provider.Error(), func(t *testing.T) {
			now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
			s, attempts, _, _, chat, step, requirementID := recoveryFixture(now)
			chat.err = errors.Join(ports.ErrChatSendNotStarted, provider)
			if err := s.relayAgentTurn(context.Background(), requirementID, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); err == nil {
				t.Fatal("provider failure vanished")
			}
			states, err := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
			want := stableSendFailure(provider)
			if err != nil || len(states) != 1 || states[0].SendStatus != core.AgentAttemptFailed || states[0].FailureCategory != want.Category || states[0].Retryable != want.Retryable {
				t.Fatal("provider classification overridden", states, err)
			}
		})
	}
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	s, attempts, _, _, chat, step, requirementID := recoveryFixture(now)
	ctx := context.Background()
	if err := s.relayAgentTurn(ctx, requirementID, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); err != nil {
		t.Fatal(err)
	}
	chat.err = errors.Join(ports.ErrChatSendNotStarted, ports.ErrBuilderSessionOperationBusy)
	if err := s.relayAgentTurn(ctx, requirementID, core.AgentStepCategoryStandard, step, "session-1", "correction", step.ClientMessageID+":parse-correction", core.AgentAttemptCorrectionSent, now); err == nil {
		t.Fatal("correction refusal vanished")
	}
	if _, err := s.recoverRequirementAgentAttempts(ctx, requirementID); err != nil {
		t.Fatal(err)
	}
	states, err := attempts.ListClearDevAgentStepAttempts(ctx, requirementID)
	if err != nil || len(states) != 1 || states[0].SendStatus == core.AgentAttemptFailedBeforeSend || chat.relays != 2 {
		t.Fatal("correction gained a new original attempt", states, chat.relays, err)
	}
}

// SQLite reservations and attempt creation are real; preflight/provider are
// counting doubles. The separate project regression also uses real Chat admission.
func TestPreSendSQLiteConcurrentRecoveryAndRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	s, _, _, _, chat, step, _ := recoveryFixture(now)
	dir := t.TempDir()
	store, err := sqlitetest.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	requirementID := seedStandardRequirement(t, store, &standardTestIDs{}, func() time.Time { return now }, false)
	s.attempts, s.facts = store, store
	proof := seedPreSendAttempt(t, s, requirementID, step, now)
	concurrent := func(fn func() error) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); errs <- fn() }()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if failure, ok := ports.ChatFailureFromError(err); ok && failure.Category == domain.AgentFailureDeliveryUnknown {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	concurrent(func() error { _, err := s.recoverRequirementAgentAttempts(ctx, requirementID); return err })
	concurrent(func() error {
		return s.relayAgentTurn(ctx, requirementID, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now)
	})
	if chat.relays != 1 || chat.clientMessageIDs[0] != step.ClientMessageID+":attempt:2" {
		t.Fatal("concurrent retry repeated the provider send", chat.relays, chat.clientMessageIDs)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	restarted, _, _, _, _, _, _ := recoveryFixture(now)
	restarted.attempts, restarted.facts, restarted.chat = reopened, reopened, chat
	if err := restarted.relayAgentTurn(ctx, requirementID, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); err != nil {
		t.Fatal(err)
	}
	states, err := reopened.ListClearDevAgentStepAttemptStates(ctx, requirementID, step.ID)
	if err != nil || len(states) != 2 || states[0].LastEventID != proof.ID || states[1].TriggerFailureEventID != proof.ID || states[1].SendStatus != core.AgentAttemptSent || chat.relays != 1 {
		t.Fatal("restart lost proof or resent message", states, chat.relays, err)
	}
	ledger, err := reopened.GetClearDevMessageBudget(ctx, requirementID)
	if err != nil || *ledger.Steps[0].ReservedMessages != 2 || *ledger.Steps[0].ConfirmedSentMessages != 1 {
		t.Fatal("restart changed ledger", ledger, err)
	}
}
