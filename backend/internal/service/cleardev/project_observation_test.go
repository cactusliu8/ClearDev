package cleardev

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// The provider completion below is a double. SQLite, task identity, candidate
// checks and review transitions still use the existing full project fixture.
type delayedProjectObservationChat struct {
	*projectExecutionFlowHarness
	release <-chan struct{}
}

func (h *delayedProjectObservationChat) Snapshot(ctx context.Context, id domain.SessionID) (chatsvc.Snapshot, error) {
	snapshot, err := h.projectExecutionFlowHarness.Snapshot(ctx, id)
	if err != nil {
		return snapshot, err
	}
	select {
	case <-h.release:
		return snapshot, nil
	default:
	}
	for _, message := range snapshot.Messages {
		if message.Role != domain.MessageRoleAssistant || !strings.Contains(message.Text, `"kind":"BUILDER_RESULT"`) {
			continue
		}
		for i := range snapshot.Turns {
			if snapshot.Turns[i].ID == message.TurnID {
				snapshot.Turns[i].State = domain.TurnStateRunning
				snapshot.Turns[i].CompletedAt = nil
			}
		}
	}
	return snapshot, nil
}

func waitForProjectTimeout(t *testing.T, f *projectPlanningFixture, id string) core.AgentStepAttemptView {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		views, err := f.store.ListLatestClearDevAgentAttemptStates(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range views {
			if v.StepKind == core.ComplexExecutionAgentStepBuilderTask && v.SendStatus == core.AgentAttemptObservationTimedOut {
				return v
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("original Builder did not reach the observation checkpoint")
	return core.AgentStepAttemptView{}
}

func TestProjectObservationLateBuilderAutomaticallyReachesReview(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	h := attachProjectFlow(f, preparer)
	release := make(chan struct{})
	f.s.chat = &delayedProjectObservationChat{projectExecutionFlowHarness: h, release: release}
	ctx, cancel := context.WithCancel(context.Background())
	f.s.backgroundContext, f.s.stepTimeout = ctx, time.Second
	var run func()
	f.s.runBackground = func(fn func()) { run = fn }
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); run() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("observer did not stop on shutdown")
		}
	})
	original := waitForProjectTimeout(t, f, child.Requirement.ID)
	close(release)
	select {
	case <-done:
	// SQLite and full Stage checks are much slower under the race detector.
	case <-time.After(45 * time.Second):
		t.Fatal("late result required an external wake instead of automatic review")
	}
	execution, found, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	if err != nil || !found || len(execution.Reviews) != 1 || len(execution.Verifications) != 1 || execution.Run.CompletedAt == nil {
		t.Fatalf("late result not fully reviewed: %+v err=%v", execution, err)
	}
	views, err := f.store.ListLatestClearDevAgentAttemptStates(context.Background(), child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.LogicalStepID == original.LogicalStepID && (v.ID != original.ID || v.AttemptNumber != 1 || v.SendStatus != core.AgentAttemptCompleted) {
			t.Fatalf("observer created/replaced attempt: %+v", v)
		}
	}
	builderSends := 0
	for _, relay := range h.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) {
			builderSends++
		}
	}
	if builderSends != 1 || len(execution.Dispatches) != 1 {
		t.Fatalf("late completion repeated development: sends=%d dispatches=%d", builderSends, len(execution.Dispatches))
	}
	if len(h.checkRequests) == 0 {
		t.Fatal("late result skipped trusted candidate checks")
	}
}

func TestProjectObservationRestartReceivesOriginalResultWithoutResend(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	h := attachProjectFlow(f, preparer)
	release := make(chan struct{})
	f.s.chat = &delayedProjectObservationChat{projectExecutionFlowHarness: h, release: release}
	f.s.stepTimeout = 20 * time.Millisecond
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	// Reproduce a saved checkpoint from the old one-shot observer.
	if err := f.s.runComplexStandardExecution(context.Background(), child.Requirement.ID); err != nil {
		t.Fatal(err)
	}
	original := waitForProjectTimeout(t, f, child.Requirement.ID)
	before := len(h.relays)
	close(release)
	f.service()
	restarted := f.s
	restarted.sessions, restarted.chat, restarted.inspector, restarted.checks = h, h, h, h
	restarted.finalReviews, restarted.resultPreview = f.store, h.trial
	h.service = restarted
	if err := restarted.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	execution, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	if err != nil || len(execution.Reviews) != 1 || execution.Run.CompletedAt == nil {
		t.Fatalf("restart did not receive late result: %+v err=%v", execution, err)
	}
	views, err := f.store.ListLatestClearDevAgentAttemptStates(context.Background(), child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.LogicalStepID == original.LogicalStepID && (v.ID != original.ID || v.AttemptNumber != 1) {
			t.Fatalf("restart replaced original round: %+v", v)
		}
	}
	for _, relay := range h.relays[before:] {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) {
			t.Fatal("restart resent original Builder")
		}
	}
}

func TestProjectObservationShutdownAndTerminalFailureDoNotWake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForProjectObservation(ctx) {
		t.Fatal("shutdown left observer alive")
	}
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	h := attachProjectFlow(f, preparer)
	release := make(chan struct{})
	f.s.chat = &delayedProjectObservationChat{projectExecutionFlowHarness: h, release: release}
	f.s.stepTimeout = 20 * time.Millisecond
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	if err := f.s.runComplexStandardExecution(context.Background(), child.Requirement.ID); err != nil {
		t.Fatal(err)
	}
	original := waitForProjectTimeout(t, f, child.Requirement.ID)
	if observe, err := f.s.shouldContinueProjectObservation(context.Background(), child.Requirement.ID); err != nil || !observe {
		t.Fatalf("live checkpoint not observed: %v %v", observe, err)
	}
	f.h.source.BaseCommitSHA = forty("b")
	if observe, err := f.s.shouldContinueProjectObservation(context.Background(), child.Requirement.ID); err != nil || observe {
		t.Fatalf("changed source resumed observation: %v %v", observe, err)
	}
	f.h.source.BaseCommitSHA = forty("a")
	err := f.store.RecordClearDevAgentAttemptEvent(context.Background(), core.AgentAttemptEvent{
		ID: original.ID + ":terminal-fixture", AttemptID: original.ID, Status: core.AgentAttemptFailed,
		ClientMessageID: original.ClientMessageID, TurnState: domain.TurnStateFailed,
		FailureCategory: domain.AgentFailureResultInvalid, ErrorSummary: "Explicit terminal fixture", RecordedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if observe, err := f.s.shouldContinueProjectObservation(context.Background(), child.Requirement.ID); err != nil || observe {
		t.Fatalf("terminal failure became observation retry: %v %v", observe, err)
	}
	if observe, err := f.s.shouldContinueProjectObservation(ctx, child.Requirement.ID); !errors.Is(err, context.Canceled) || observe {
		t.Fatal("shutdown continued observation")
	}
}

func TestProjectObservationCancelledRequirementDoesNotWake(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	h := attachProjectFlow(f, preparer)
	release := make(chan struct{})
	f.s.chat = &delayedProjectObservationChat{projectExecutionFlowHarness: h, release: release}
	f.s.stepTimeout = 20 * time.Millisecond
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	if err := f.s.runComplexStandardExecution(context.Background(), child.Requirement.ID); err != nil {
		t.Fatal(err)
	}
	waitForProjectTimeout(t, f, child.Requirement.ID)
	if err := f.s.CancelRequirement(context.Background(), child.Requirement.ID, "explicit observation cancellation fixture"); err != nil {
		t.Fatal(err)
	}
	if observe, err := f.s.shouldContinueProjectObservation(context.Background(), child.Requirement.ID); err != nil || observe {
		t.Fatalf("cancelled work resumed: %v %v", observe, err)
	}
	close(release)
}
