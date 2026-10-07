package store_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestClearDevAgentAttemptEvidenceIsAppendOnlyConcurrentAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := sqlitetest.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC)
	seedClearDevAO(t, store, "ao-attempt")
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("requirement-attempt", "ao-attempt", now)); err != nil {
		t.Fatal(err)
	}
	attempt := core.AgentStepAttempt{
		ID: "attempt-1", DevelopmentRequirementID: "requirement-attempt",
		LogicalStepID: "step-1", StepCategory: core.AgentStepCategoryStandard,
		StepKind: core.AgentStepEngineeringPlan, AttemptNumber: 1,
		RoleBindingID: "planner-1", AOSessionID: "session-1",
		ClientMessageID: "message-1", PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation,
	}

	var created atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, wasCreated, ensureErr := store.EnsureClearDevAgentStepAttempt(ctx, attempt)
			if wasCreated {
				created.Add(1)
			}
			errs <- ensureErr
		}()
	}
	wg.Wait()
	close(errs)
	for ensureErr := range errs {
		if ensureErr != nil {
			t.Fatal(ensureErr)
		}
	}
	if created.Load() != 1 {
		t.Fatalf("created attempts = %d, want 1", created.Load())
	}
	boundary := core.AgentAttemptEvent{
		ID: "attempt-1:delivery-boundary", AttemptID: attempt.ID, Status: core.AgentAttemptDeliveryUnknown,
		ClientMessageID: attempt.ClientMessageID, PromptSHA256: attempt.PromptSHA256,
		FailureCategory: domain.AgentFailureDeliveryUnknown, ErrorSummary: "message delivery outcome is unknown",
		RecordedAt: now.Add(30 * time.Second),
	}
	created.Store(0)
	errs = make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wasCreated, ensureErr := store.EnsureClearDevAgentAttemptEvent(ctx, boundary)
			if wasCreated {
				created.Add(1)
			}
			errs <- ensureErr
		}()
	}
	wg.Wait()
	close(errs)
	for ensureErr := range errs {
		if ensureErr != nil {
			t.Fatal(ensureErr)
		}
	}
	if created.Load() != 1 {
		t.Fatalf("created delivery boundaries = %d, want 1", created.Load())
	}

	if err := store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{
		ID: "attempt-1:sent", AttemptID: attempt.ID, Status: core.AgentAttemptSent,
		ClientMessageID: attempt.ClientMessageID, PromptSHA256: attempt.PromptSHA256,
		TurnID: "turn-1", TurnState: domain.TurnStateRunning, RecordedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	first := core.AgentStepResult{
		ID: "attempt-1:result:1", AttemptID: attempt.ID, ResultIndex: 1,
		Source: core.AgentResultOriginal, ClientMessageID: attempt.ClientMessageID,
		TurnID: "turn-1", FinalMessageID: "final-1", RawMessageText: "not json",
		RawMessageSHA256: strings.Repeat("b", 64), ObservedAt: now.Add(2 * time.Minute),
	}
	if err := store.RecordClearDevAgentStepResult(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordClearDevAgentStepResultParse(ctx, core.AgentStepResultParse{
		ResultID: first.ID, Conclusion: core.AgentResultParseInvalid,
		ErrorSummary: "invalid JSON", ParsedAt: now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{
		ID: "attempt-1:correction", AttemptID: attempt.ID, Status: core.AgentAttemptCorrectionSent,
		ClientMessageID: "message-1:parse-correction", PromptSHA256: strings.Repeat("c", 64),
		TurnID: "turn-2", TurnState: domain.TurnStateRunning, RecordedAt: now.Add(4 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	second := core.AgentStepResult{
		ID: "attempt-1:result:2", AttemptID: attempt.ID, ResultIndex: 2,
		Source: core.AgentResultCorrection, ClientMessageID: "message-1:parse-correction",
		TurnID: "turn-2", FinalMessageID: "final-2", RawMessageText: `{"kind":"ok"}`,
		RawMessageSHA256: strings.Repeat("d", 64), ObservedAt: now.Add(5 * time.Minute),
	}
	if err := store.RecordClearDevAgentStepResult(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordClearDevAgentStepResultParse(ctx, core.AgentStepResultParse{
		ResultID: second.ID, Conclusion: core.AgentResultParseValid, ParsedAt: now.Add(6 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{
		ID: "attempt-1:completed", AttemptID: attempt.ID, Status: core.AgentAttemptCompleted,
		ClientMessageID: second.ClientMessageID, TurnID: second.TurnID, TurnState: domain.TurnStateCompleted,
		RecordedAt: now.Add(7 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	changed := first
	changed.RawMessageText = "different"
	if err := store.RecordClearDevAgentStepResult(ctx, changed); err == nil {
		t.Fatal("immutable result accepted different raw text")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.RecordClearDevAgentStepResultParse(ctx, core.AgentStepResultParse{
		ResultID: first.ID, Conclusion: core.AgentResultParseInvalid,
		ErrorSummary: "invalid JSON", ParsedAt: now.Add(30 * time.Minute),
	}); err != nil {
		t.Fatalf("replay same parse after restart: %v", err)
	}
	if err := reopened.RecordClearDevAgentStepResultParse(ctx, core.AgentStepResultParse{
		ResultID: first.ID, Conclusion: core.AgentResultParseInvalid,
		ErrorSummary: "different error", ParsedAt: now.Add(31 * time.Minute),
	}); err == nil {
		t.Fatal("immutable parse accepted a different error summary")
	}
	views, err := reopened.ListClearDevAgentStepAttempts(ctx, "requirement-attempt")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || len(views[0].Results) != 2 {
		t.Fatalf("restarted evidence = %#v", views)
	}
	view := views[0]
	if view.AttemptNumber != 1 || view.SendStatus != core.AgentAttemptCompleted ||
		view.TurnState != domain.TurnStateCompleted ||
		view.Results[0].ParseConclusion != core.AgentResultParseInvalid ||
		view.Results[1].ParseConclusion != core.AgentResultParseValid {
		t.Fatalf("restarted attempt = %#v", view)
	}
}

func TestClearDevAgentAttemptEventClaimAcrossStores(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	first, err := sqlitetest.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	now := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	seedClearDevAO(t, first, "ao-event-claim")
	if err := first.CreateClearDevRequirement(ctx, initialRequirement("requirement-event-claim", "ao-event-claim", now)); err != nil {
		t.Fatal(err)
	}
	attempt := core.AgentStepAttempt{ID: "event-claim-attempt", DevelopmentRequirementID: "requirement-event-claim", LogicalStepID: "event-claim-step", StepCategory: core.AgentStepCategoryStandard, StepKind: core.AgentStepEngineeringPlan, AttemptNumber: 1, RoleBindingID: "event-claim-role", AOSessionID: "event-claim-session", ClientMessageID: "event-claim-message", PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation}
	if _, created, err := first.EnsureClearDevAgentStepAttempt(ctx, attempt); err != nil || !created {
		t.Fatal("first attempt", created, err)
	}
	second, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	event := core.AgentAttemptEvent{ID: "event-claim-boundary", AttemptID: attempt.ID, Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: attempt.ClientMessageID, PromptSHA256: attempt.PromptSHA256, FailureCategory: domain.AgentFailureDeliveryUnknown, ErrorSummary: "explicit claim test boundary", RecordedAt: now}
	var winners atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 8)
	for i := range 8 {
		writer := first
		if i%2 != 0 {
			writer = second
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			created, err := writer.EnsureClearDevAgentAttemptEvent(ctx, event)
			if created {
				winners.Add(1)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("independent Store event claim", err)
		}
	}
	if winners.Load() != 1 {
		t.Fatal("event claim can authorize multiple provider sends", winners.Load())
	}
	event.RecordedAt = now.Add(time.Hour)
	if created, err := second.EnsureClearDevAgentAttemptEvent(ctx, event); err != nil || created {
		t.Fatal("exact cross-Store replay", created, err)
	}
	views, err := second.ListClearDevAgentStepAttempts(ctx, attempt.DevelopmentRequirementID)
	if err != nil || len(views) != 1 || views[0].LastEventID != event.ID || views[0].LastObservedAt == nil || !views[0].LastObservedAt.Equal(now) {
		t.Fatal("replay changed the original event identity or time", views, err)
	}
	event.ErrorSummary = "different immutable evidence"
	if _, err := second.EnsureClearDevAgentAttemptEvent(ctx, event); err == nil {
		t.Fatal("conflicting event claim was admitted")
	}
}

func TestClearDevSecondAgentAttemptIsFailureBoundConcurrentAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := sqlitetest.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	seedClearDevAO(t, store, "ao-recovery")
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("requirement-recovery", "ao-recovery", now)); err != nil {
		t.Fatal(err)
	}
	first := core.AgentStepAttempt{
		ID: "recovery-attempt-1", DevelopmentRequirementID: "requirement-recovery",
		LogicalStepID: "recovery-step", StepCategory: core.AgentStepCategoryStandard,
		StepKind: core.AgentStepBuilderResult, AttemptNumber: 1,
		RoleBindingID: "builder-1", AOSessionID: "session-1",
		ClientMessageID: "recovery-message", PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation,
	}
	if _, _, err := store.EnsureClearDevAgentStepAttempt(ctx, first); err != nil {
		t.Fatal(err)
	}
	failureAt := now.Add(time.Minute)
	retryAt := failureAt.Add(time.Minute)
	failure := core.AgentAttemptEvent{
		ID: "recovery-failure-1", AttemptID: first.ID, Status: core.AgentAttemptFailed,
		ClientMessageID: first.ClientMessageID, TurnID: "turn-1", TurnState: domain.TurnStateFailed,
		FailureCategory: domain.AgentFailureRateLimited, Retryable: true, RetryAt: &retryAt,
		ErrorSummary: "provider rate limit is active", RecordedAt: failureAt,
	}
	if err := store.RecordClearDevAgentAttemptEvent(ctx, failure); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	second := core.AgentStepAttempt{
		ID: "recovery-attempt-2", DevelopmentRequirementID: first.DevelopmentRequirementID,
		LogicalStepID: first.LogicalStepID, StepCategory: first.StepCategory, StepKind: first.StepKind,
		AttemptNumber: 2, RoleBindingID: first.RoleBindingID, AOSessionID: first.AOSessionID,
		ClientMessageID: "recovery-message:attempt:2", PromptSHA256: first.PromptSHA256,
		TriggerFailureEventID: failure.ID, RequestedAt: retryAt, CreatedAt: &retryAt, RequestedAtSemantics: core.AttemptTimeActualCreation,
	}
	var created atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, wasCreated, ensureErr := reopened.EnsureClearDevAgentStepAttempt(ctx, second)
			if wasCreated {
				created.Add(1)
			}
			errs <- ensureErr
		}()
	}
	wg.Wait()
	close(errs)
	for ensureErr := range errs {
		if ensureErr != nil {
			t.Fatal(ensureErr)
		}
	}
	if created.Load() != 1 {
		t.Fatalf("created second attempts=%d, want 1", created.Load())
	}
	views, err := reopened.ListClearDevAgentStepAttempts(ctx, first.DevelopmentRequirementID)
	if err != nil || len(views) != 2 || views[1].TriggerFailureEventID != failure.ID {
		t.Fatalf("attempts=%#v err=%v", views, err)
	}

	third := second
	third.ID, third.AttemptNumber, third.ClientMessageID = "recovery-attempt-3", 3, "recovery-message:attempt:3"
	if _, _, err := reopened.EnsureClearDevAgentStepAttempt(ctx, third); err == nil {
		t.Fatal("store accepted a third Agent attempt")
	}
}
