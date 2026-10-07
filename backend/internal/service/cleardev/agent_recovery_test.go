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
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

type recoveryAOState struct {
	mu      sync.Mutex
	project domain.ProjectRecord
	session domain.SessionRecord
}

func (a *recoveryAOState) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.project, a.project.ID == id, nil
}

func (a *recoveryAOState) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.session, a.session.ID == id, nil
}

func (a *recoveryAOState) setActivity(state domain.ActivityState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session.Activity.State = state
}

type recoveryPreflight struct {
	mu  sync.Mutex
	err error
}

func (p *recoveryPreflight) CheckControlledPreflight(_ context.Context, _ domain.AgentHarness, requested string) (ports.ChatControlledPreflight, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ports.ChatControlledPreflight{RequestedModel: requested, ResolvedModel: requested, Provider: "codex"}, p.err
}

func (p *recoveryPreflight) setError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

type recoveryChat struct {
	mu               sync.Mutex
	relays           int
	clientMessageIDs []string
	snapshot         chatsvc.Snapshot
	err              error
}

func (c *recoveryChat) RelayChatTurnWithID(_ context.Context, sessionID domain.SessionID, prompt, clientMessageID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.relays++
	c.clientMessageIDs = append(c.clientMessageIDs, clientMessageID)
	if c.err != nil {
		return "", c.err
	}
	return "turn-recovery", nil
}

func (c *recoveryChat) Snapshot(_ context.Context, sessionID domain.SessionID) (chatsvc.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := c.snapshot
	if result.SessionID == "" {
		result.SessionID = sessionID
	}
	return result, nil
}

func (*recoveryChat) Interrupt(context.Context, domain.SessionID) error { return nil }

func recoveryFixture(now time.Time) (*Service, *memoryAgentAttempts, *recoveryAOState, *recoveryPreflight, *recoveryChat, core.AgentStep, string) {
	attempts := newMemoryAgentAttempts()
	ao := &recoveryAOState{
		project: domain.ProjectRecord{ID: "project-1", Kind: domain.ProjectKindSingleRepo},
		session: domain.SessionRecord{
			ID: "session-1", ProjectID: "project-1", Kind: domain.KindWorker,
			Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
			Activity: domain.Activity{State: domain.ActivityIdle},
		},
	}
	preflight := &recoveryPreflight{}
	chat := &recoveryChat{}
	service := New(Deps{
		ParseCorrections: newMemoryParseCorrections(),

		AgentAttempts: attempts, ControlledPreflights: newMemoryControlledPreflights(),
		ControlledPreflightChecker: preflight, AO: ao, Chat: chat,
		Clock: func() time.Time { return now },
		RecoverAgentSession: func(_ context.Context, _ domain.SessionID) error {
			ao.setActivity(domain.ActivityIdle)
			return nil
		},
	})
	step := core.AgentStep{
		ID: "step-1", RoleBindingID: "binding-1", Kind: core.AgentStepBuilderResult,
		ClientMessageID: "message-1", PromptSHA256: coreDigest([]byte("prompt")), RequestedAt: now,
	}
	return service, attempts, ao, preflight, chat, step, "requirement-1"
}

func seedRetryableFirstAttempt(t *testing.T, service *Service, requirementID string, step core.AgentStep, at time.Time) string {
	t.Helper()
	attempt, err := service.ensureAgentAttempt(context.Background(), requirementID, core.AgentStepCategoryStandard,
		step, "session-1", agentFirstAttemptNumber, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.attempts.RecordClearDevAgentAttemptEvent(context.Background(), core.AgentAttemptEvent{
		ID: agentEvidenceID(attempt.ID, "sent", "turn-1"), AttemptID: attempt.ID, Status: core.AgentAttemptSent,
		ClientMessageID: step.ClientMessageID, PromptSHA256: step.PromptSHA256,
		TurnID: "turn-1", TurnState: domain.TurnStateRunning, RecordedAt: at.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	eventID := agentEvidenceID(attempt.ID, "failed", "provider")
	if err := service.attempts.RecordClearDevAgentAttemptEvent(context.Background(), core.AgentAttemptEvent{
		ID: eventID, AttemptID: attempt.ID, Status: core.AgentAttemptFailed,
		ClientMessageID: step.ClientMessageID, TurnID: "turn-1", TurnState: domain.TurnStateFailed,
		FailureCategory: domain.AgentFailureProviderUnavailable, Retryable: true,
		ErrorSummary: safeAgentFailureSummary(domain.AgentFailureProviderUnavailable), RecordedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	return eventID
}

func TestControlledRecoveryPollSendsPendingSecondAttempt(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, attempts, _, _, chat, step, requirementID := recoveryFixture(now)
	seedRetryableFirstAttempt(t, service, requirementID, step, now)
	if blocked, err := service.recoverRequirementAgentAttempts(context.Background(), requirementID); err != nil || blocked {
		t.Fatalf("create second blocked=%v err=%v", blocked, err)
	}

	result, err := service.pollValidAgentJSON(
		context.Background(), requirementID, core.AgentStepCategoryStandard, "session-1", step, "prompt",
		func([]byte) error { return nil }, "TIMED_OUT", "UNAVAILABLE", "INVALID",
	)
	if err != nil || result.ready || result.stopped {
		t.Fatalf("poll result=%#v err=%v", result, err)
	}
	views, err := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if err != nil || len(views) != 2 || views[1].SendStatus != core.AgentAttemptSent {
		t.Fatalf("attempts=%#v err=%v", views, err)
	}
	if chat.relays != 1 || len(chat.clientMessageIDs) != 1 || chat.clientMessageIDs[0] != "message-1:attempt:2" {
		t.Fatalf("relays=%d ids=%v", chat.relays, chat.clientMessageIDs)
	}
}

func TestControlledRecoveryConcurrentResumeCreatesAndSendsSecondAttemptOnce(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, attempts, _, _, chat, step, requirementID := recoveryFixture(now)
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

func TestControlledRecoveryWaitsForLoginFix(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, attempts, _, preflight, _, step, requirementID := recoveryFixture(now)
	seedRetryableFirstAttempt(t, service, requirementID, step, now)
	preflight.setError(ports.ErrChatAuthRequired)

	blocked, err := service.recoverRequirementAgentAttempts(context.Background(), requirementID)
	if err != nil || !blocked {
		t.Fatalf("blocked=%v err=%v", blocked, err)
	}
	views, _ := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if len(views) != 1 {
		t.Fatalf("attempts before login fix=%#v", views)
	}

	preflight.setError(nil)
	blocked, err = service.recoverRequirementAgentAttempts(context.Background(), requirementID)
	views, _ = attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if err != nil || blocked || len(views) != 2 {
		t.Fatalf("after login fix blocked=%v err=%v attempts=%#v", blocked, err, views)
	}
}

func TestControlledRecoveryDoesNotRetryObservationTimeout(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, attempts, ao, _, _, step, requirementID := recoveryFixture(now)
	attempt, err := service.ensureAgentAttempt(context.Background(), requirementID, core.AgentStepCategoryStandard,
		step, "session-1", agentFirstAttemptNumber, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := attempts.RecordClearDevAgentAttemptEvent(context.Background(), core.AgentAttemptEvent{
		ID: agentEvidenceID(attempt.ID, "timeout"), AttemptID: attempt.ID,
		Status: core.AgentAttemptObservationTimedOut, ClientMessageID: step.ClientMessageID,
		TurnID: "turn-1", TurnState: domain.TurnStateRunning,
		FailureCategory: domain.AgentFailureObservationTimeout, Retryable: true,
		ErrorSummary: safeAgentFailureSummary(domain.AgentFailureObservationTimeout), RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	ao.setActivity(domain.ActivityExited)

	blocked, err := service.recoverRequirementAgentAttempts(context.Background(), requirementID)
	views, _ := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if err != nil || blocked || len(views) != 1 || views[0].SendStatus != core.AgentAttemptObservationTimedOut {
		t.Fatalf("blocked=%v err=%v attempts=%#v", blocked, err, views)
	}
}

func TestControlledRecoveryDoesNotWaitForFutureRetryWindow(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, attempts, _, _, _, step, requirementID := recoveryFixture(now)
	attempt, err := service.ensureAgentAttempt(context.Background(), requirementID, core.AgentStepCategoryStandard,
		step, "session-1", agentFirstAttemptNumber, "")
	if err != nil {
		t.Fatal(err)
	}
	retryAt := now.Add(time.Hour)
	if err := attempts.RecordClearDevAgentAttemptEvent(context.Background(), core.AgentAttemptEvent{
		ID: agentEvidenceID(attempt.ID, "future-rate-limit"), AttemptID: attempt.ID,
		Status: core.AgentAttemptFailed, ClientMessageID: step.ClientMessageID,
		TurnID: "turn-1", TurnState: domain.TurnStateFailed,
		FailureCategory: domain.AgentFailureRateLimited, Retryable: true, RetryAt: &retryAt,
		ErrorSummary: safeAgentFailureSummary(domain.AgentFailureRateLimited), RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	blocked, err := service.recoverRequirementAgentAttempts(context.Background(), requirementID)
	views, _ := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if err != nil || !blocked || len(views) != 1 || time.Since(started) > time.Second {
		t.Fatalf("blocked=%v err=%v attempts=%#v elapsed=%s", blocked, err, views, time.Since(started))
	}

	createdAt := retryAt.Add(time.Minute)
	service.now = func() time.Time { return createdAt }
	blocked, err = service.recoverRequirementAgentAttempts(context.Background(), requirementID)
	views, _ = attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if err != nil || blocked || len(views) != 2 || views[1].RequestedAt == nil || !views[1].RequestedAt.Equal(createdAt) {
		t.Fatalf("after retry window blocked=%v err=%v attempts=%#v", blocked, err, views)
	}
}

func TestControlledRecoveryReconcilesUnknownDeliveryWithoutResend(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, attempts, _, _, chat, step, requirementID := recoveryFixture(now)
	attempt, err := service.ensureAgentAttempt(context.Background(), requirementID, core.AgentStepCategoryStandard,
		step, "session-1", agentFirstAttemptNumber, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attempts.EnsureClearDevAgentAttemptEvent(context.Background(), core.AgentAttemptEvent{
		ID: agentEvidenceID(attempt.ID, "delivery-boundary", step.ClientMessageID), AttemptID: attempt.ID,
		Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: step.ClientMessageID,
		PromptSHA256: step.PromptSHA256, FailureCategory: domain.AgentFailureDeliveryUnknown,
		ErrorSummary: safeAgentFailureSummary(domain.AgentFailureDeliveryUnknown), RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	chat.snapshot = chatsvc.Snapshot{
		SessionID: "session-1",
		Turns:     []domain.ConversationTurn{{ID: "turn-1", HandledBySessionID: "session-1", State: domain.TurnStateRunning}},
		Messages: []domain.ConversationMessage{{
			ID: "user-1", TurnID: "turn-1", Role: domain.MessageRoleUser,
			Origin: domain.MessageOriginAutomation, Text: "prompt", ClientMessageID: step.ClientMessageID,
		}},
	}
	if err := service.relayAgentTurn(context.Background(), requirementID, core.AgentStepCategoryStandard,
		step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); err != nil {
		t.Fatal(err)
	}
	views, _ := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if chat.relays != 0 || len(views) != 1 || views[0].SendStatus != core.AgentAttemptSent || views[0].TurnID != "turn-1" {
		t.Fatalf("relays=%d attempts=%#v", chat.relays, views)
	}
}

func TestControlledRecoveryStopsWhenOriginalSessionCannotResume(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, attempts, ao, _, _, step, requirementID := recoveryFixture(now)
	seedRetryableFirstAttempt(t, service, requirementID, step, now)
	ao.setActivity(domain.ActivityExited)
	service.recoverAgentSession = func(context.Context, domain.SessionID) error { return errors.New("resume failed") }

	blocked, err := service.recoverRequirementAgentAttempts(context.Background(), requirementID)
	if err != nil || !blocked {
		t.Fatalf("blocked=%v err=%v", blocked, err)
	}
	views, _ := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if len(views) != 1 || views[0].Retryable || views[0].FailureCategory != domain.AgentFailureSessionLost {
		t.Fatalf("stopped attempt=%#v", views)
	}
}

func TestControlledRecoverySecondFailureDoesNotCreateThirdAttempt(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, attempts, _, _, _, step, requirementID := recoveryFixture(now)
	seedRetryableFirstAttempt(t, service, requirementID, step, now)
	if blocked, err := service.recoverRequirementAgentAttempts(context.Background(), requirementID); err != nil || blocked {
		t.Fatalf("create second blocked=%v err=%v", blocked, err)
	}
	if err := attempts.RecordClearDevAgentAttemptEvent(context.Background(), core.AgentAttemptEvent{
		ID:        agentEvidenceID(agentAttemptID(step.ID, 2), "failed", "again"),
		AttemptID: agentAttemptID(step.ID, 2), Status: core.AgentAttemptFailed,
		ClientMessageID: "message-1:attempt:2", TurnID: "turn-2", TurnState: domain.TurnStateFailed,
		FailureCategory: domain.AgentFailureRateLimited, Retryable: true,
		ErrorSummary: safeAgentFailureSummary(domain.AgentFailureRateLimited), RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	blocked, err := service.recoverRequirementAgentAttempts(context.Background(), requirementID)
	views, _ := attempts.ListClearDevAgentStepAttempts(context.Background(), requirementID)
	if err != nil || !blocked || len(views) != 2 {
		t.Fatalf("blocked=%v err=%v attempts=%#v", blocked, err, views)
	}
}

func TestControlledRecoveryDoesNotResetParseCorrectionBudget(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, _, _, _, chat, step, requirementID := recoveryFixture(now)
	seedRetryableFirstAttempt(t, service, requirementID, step, now)
	if err := service.recordParseCorrection(context.Background(), core.ParseCorrection{
		StepID: step.ID, AttemptNumber: 1, ClientMessageID: parseCorrectionClientMessageID(step),
		PromptText: "correction", PromptSHA256: coreDigest([]byte("correction")), SentAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if blocked, err := service.recoverRequirementAgentAttempts(context.Background(), requirementID); err != nil || blocked {
		t.Fatalf("create second blocked=%v err=%v", blocked, err)
	}
	if _, err := service.sendParseCorrection(context.Background(), requirementID, core.AgentStepCategoryStandard,
		"session-1", step, errors.New("invalid")); err == nil {
		t.Fatal("second attempt received a second parse correction")
	}
	if chat.relays != 0 {
		t.Fatalf("parse correction relays=%d, want 0", chat.relays)
	}
}
