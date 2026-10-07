package cleardev

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestPlanningStepContinuationRejectsChangedOrUnknownWork(t *testing.T) {
	for _, mode := range []string{"unknown", "observation", "running", "missing-native-message", "source", "native", "cancelled", "wrong-target", "wrong-run"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newPlanningContinuationFixture(t, true)
			input := f.input()
			attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
			if err != nil {
				t.Fatal(err)
			}
			calls := len(f.h.relays)
			switch mode {
			case "unknown", "observation":
				status, category := core.AgentAttemptDeliveryUnknown, domain.AgentFailureDeliveryUnknown
				if mode == "observation" {
					status, category = core.AgentAttemptObservationTimedOut, domain.AgentFailureObservationTimeout
				}
				// Later correction evidence must not conceal the original
				// message's unknown or still-observed delivery.
				for _, e := range []core.AgentAttemptEvent{
					{ID: "original-became-unknown", AttemptID: attempts[0].ID, ClientMessageID: f.step.ClientMessageID, Status: status, FailureCategory: category, RecordedAt: time.Now().UTC()},
					{ID: "later-invalid-correction", AttemptID: attempts[0].ID, ClientMessageID: f.step.ClientMessageID + ":parse-correction", Status: core.AgentAttemptFailed, FailureCategory: domain.AgentFailureResultInvalid, TurnState: domain.TurnStateFailed, RecordedAt: time.Now().UTC()},
				} {
					if err := f.store.RecordClearDevAgentAttemptEvent(ctx, e); err != nil {
						t.Fatal(err)
					}
				}
			case "running":
				sid := domain.SessionID(attempts[0].AOSessionID)
				snapshot := f.h.snapshots[sid]
				snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{ID: "still-running", State: domain.TurnStateRunning, HandledBySessionID: sid})
				f.h.snapshots[sid] = snapshot
			case "missing-native-message":
				sid := domain.SessionID(attempts[0].AOSessionID)
				snapshot := f.h.snapshots[sid]
				snapshot.Messages = nil
				f.h.snapshots[sid] = snapshot
			case "source":
				f.h.dirty = true
			case "native":
				record, _, err := f.store.GetSession(ctx, domain.SessionID(attempts[0].AOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				record.Metadata.ProviderConversationID = "another-conversation"
				if err := f.store.UpdateSession(ctx, record); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				if err := f.s.CancelRequirement(ctx, f.id, "explicit fixture cancellation"); err != nil {
					t.Fatal(err)
				}
			case "wrong-target":
				input.TargetID += "changed"
			case "wrong-run":
				input.ExecutionRunID = "not-a-planning-run"
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, input); err == nil {
				t.Fatal("unsafe planning continuation accepted")
			}
			after, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
			view, readErr := f.s.GetWorkflowRecovery(ctx, f.id)
			if err != nil || readErr != nil || len(after) != 1 || len(view.History) != 0 || len(f.h.relays) != calls {
				t.Fatalf("rejection created a successor, history or send: %v %v", err, readErr)
			}
		})
	}
}

func TestPlanningStepContinuationConcurrentRequestsClaimOneSuccessor(t *testing.T) {
	ctx := context.Background()
	f := newPlanningContinuationFixture(t, true)
	// Do not run the synchronous model double from competing threads. Both
	// contenders still exercise real session reads and SQLite transactions.
	f.s.runBackground = func(func()) {}
	state, err := f.store.ReadClearDevPlanningStepRecovery(ctx, f.id, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- f.store.ApplyClearDevPlanningStepRecovery(ctx, fmt.Sprintf("competing-%d", i), "", state.Binding, time.Now().UTC())
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	after, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
	if success != 1 || err != nil || len(after) != 2 {
		t.Fatalf("concurrent requests claimed %d successors: %v", success, err)
	}
	f.h.fail = false
	f.h.replies = append(f.h.replies, f.reply)
	calls := len(f.h.relays)
	if err := f.s.runComplexFlow(ctx, f.id); err != nil || len(f.h.relays) != calls+1 {
		t.Fatalf("single successor did not run exactly once: %v", err)
	}
}

func TestPlanningStepContinuationSurvivesReopenWithoutNewDiscussion(t *testing.T) {
	ctx := context.Background()
	f := newPlanningContinuationFixture(t, true)
	f.s.runBackground = func(func()) {}
	input := f.input()
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, input); err != nil {
		t.Fatal(err)
	}
	old, err := f.store.ReadClearDevPlanningStepRecovery(ctx, f.id, time.Now().UTC())
	if err != nil || len(old.History) != 1 {
		t.Fatalf("missing registered continuation: %v", err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.h.store = f.store
	f.s = planningContinuationService(f.store, f.h, f.s.newID, f.s.now)
	f.h.fail = false
	f.h.replies = append(f.h.replies, f.reply)
	calls := len(f.h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, input); err != nil {
		t.Fatal(err)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, f.id)
	if err != nil || len(f.h.relays) != calls+1 || !reflect.DeepEqual(view.History, old.History) {
		t.Fatalf("reopen lost history or repeated work: %v", err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, input); err != nil || len(f.h.relays) != calls+1 {
		t.Fatalf("duplicate request resent completed attempt: %v", err)
	}
}

func TestPlanningStepContinuationRefusesThirdAttemptAndSecondCorrection(t *testing.T) {
	ctx := context.Background()
	f := newPlanningContinuationFixture(t, true)
	f.h.replies = append(f.h.replies, "still invalid after the recovery")
	calls := len(f.h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, f.input()); err != nil {
		t.Fatal(err)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, f.id)
	if err != nil || len(view.Options) != 2 || view.Options[0].UnavailableReason != string(core.ReasonMessageBudgetExhausted) || view.Options[1].Action != core.RecoveryRequestExtraPlanningAttempt || view.Options[1].UnavailableReason != "DESKTOP_UNAVAILABLE" || len(f.h.relays) != calls+1 {
		t.Fatalf("second failure did not retain its exhausted boundary: %+v %v", view, err)
	}
	for range 2 {
		if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, f.input()); err != nil {
			t.Fatal(err)
		}
	}
	attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
	if err != nil || len(attempts) != 2 || len(f.h.relays) != calls+1 {
		t.Fatal("repeat or reboot reset the attempt/correction budget")
	}
}

func TestPlanningStepContinuationRechecksBeforeUnsentRecovery(t *testing.T) {
	for _, mode := range []string{"source", "native", "preflight", "session-exited"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newPlanningContinuationFixture(t, true)
			var pending func()
			f.s.runBackground = func(fn func()) { pending = fn }
			input := f.input()
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, input); err != nil || pending == nil {
				t.Fatalf("did not register original continuation: %v", err)
			}
			state, err := f.store.ReadClearDevPlanningStepRecovery(ctx, f.id, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			b := state.History[0]
			calls := len(f.h.relays)
			record, _, err := f.store.GetSession(ctx, domain.SessionID(state.Binding.AOSessionID))
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source":
				f.h.dirty = true
			case "native":
				changed := record
				changed.Metadata.ProviderConversationID = "other-provider-conversation"
				if err := f.store.UpdateSession(ctx, changed); err != nil {
					t.Fatal(err)
				}
			case "preflight":
				f.s.preflightChecker = &scriptedControlledPreflight{}
			case "session-exited":
				changed := record
				changed.Activity.State = domain.ActivityExited
				if err := f.store.UpdateSession(ctx, changed); err != nil {
					t.Fatal(err)
				}
			}
			pending()
			if len(f.h.relays) != calls {
				t.Fatal("changed source/identity/environment still sent a recovery")
			}
			attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
			if err != nil || len(attempts) != 2 || attempts[1].SendStatus != core.AgentAttemptPending || attempts[1].LastEventID != "" {
				t.Fatalf("zero-send recovery acquired a terminal or delivery event: %+v %v", attempts, err)
			}
			planning, _, err := f.store.GetClearDevComplexPlanning(ctx, f.id)
			if err != nil {
				t.Fatal(err)
			}
			step, found := core.ComplexAgentStepByRequest(planning, core.ComplexAgentStepCompilation, f.step.RequestID)
			if !found || step.SendStatus != core.AgentStepSendStatusPending {
				t.Fatalf("unsent registered step became permanently failed: %+v", step)
			}
			f.h.dirty = false
			if mode == "session-exited" {
				f.s.recoverAgentSession = func(ctx context.Context, sid domain.SessionID) error {
					if sid != record.ID {
						t.Fatal("recovery changed the original session")
					}
					return f.store.UpdateSession(ctx, record)
				}
			} else if err := f.store.UpdateSession(ctx, record); err != nil {
				t.Fatal(err)
			}
			f.s.preflightChecker = alwaysPassControlledPreflight{}
			f.h.fail = false
			f.h.replies = append(f.h.replies, f.reply)
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, input); err != nil {
				t.Fatal(err)
			}
			pending()
			after, err := f.s.GetWorkflowRecovery(ctx, f.id)
			if err != nil || len(f.h.relays) != calls+1 || len(after.History) != 1 || !reflect.DeepEqual(after.History[0], b) {
				t.Fatalf("repaired environment cannot resume original registration: %+v %v", after, err)
			}
		})
	}
}

func TestPlanningStepContinuationUsesRemainingCorrectionOnlyOnce(t *testing.T) {
	ctx := context.Background()
	f := newPlanningContinuationFixture(t, false)
	f.h.fail = false
	f.h.replies = append(f.h.replies, "invalid recovered reply", f.reply)
	calls := len(f.h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, f.input()); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.store.GetClearDevProduct(ctx, f.product)
	if err != nil || after.Discussions[0].Result == nil || len(f.h.relays) != calls+2 {
		t.Fatalf("unused correction could not complete the second attempt: %v", err)
	}
	correction, found, err := f.store.GetClearDevParseCorrection(ctx, f.step.ID)
	if err != nil || !found || correction.AttemptNumber != 2 {
		t.Fatalf("correction was not bound to the actual second attempt: %+v %v", correction, err)
	}
	budget, err := f.store.GetClearDevMessageBudget(ctx, f.id)
	if err != nil || budget.ReservedMessages == nil || *budget.ReservedMessages != 3 {
		t.Fatalf("original + recovered original + one correction accounting changed: %+v %v", budget, err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, f.input()); err != nil || len(f.h.relays) != calls+2 {
		t.Fatalf("completed correction was repeated: %v", err)
	}
}

func TestPlanningStepContinuationReservationRequiresRestoredSession(t *testing.T) {
	ctx := context.Background()
	f := newPlanningContinuationFixture(t, true)
	f.s.runBackground = func(func()) {}
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, f.input()); err != nil {
		t.Fatal(err)
	}
	attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("missing registered second attempt: %v", err)
	}
	second := agentAttemptFromView(f.id, attempts[1])
	record, _, err := f.store.GetSession(ctx, domain.SessionID(second.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	record.Activity.State = domain.ActivityExited
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.store.ReserveClearDevAgentMessage(ctx, core.ReserveAgentMessageCommand{Attempt: second, Source: core.AgentMessageRecoveryOriginal,
		Boundary: core.AgentAttemptEvent{ID: "must-not-reserve-exited-session", AttemptID: second.ID, ClientMessageID: second.ClientMessageID,
			PromptSHA256: second.PromptSHA256, Status: core.AgentAttemptDeliveryUnknown, RecordedAt: time.Now().UTC()}})
	if err == nil || claimed {
		t.Fatal("direct message transaction admitted an exited original session")
	}
	after, err := f.store.GetClearDevAgentAttemptState(ctx, f.id, second.ID)
	if err != nil || after.SendStatus != core.AgentAttemptPending || after.LastEventID != "" {
		t.Fatalf("rejected reservation added a delivery boundary: %+v %v", after, err)
	}
}

func TestPlanningStepContinuationTransactionRejectsLateEvidence(t *testing.T) {
	ctx := context.Background()
	f := newPlanningContinuationFixture(t, true)
	state, err := f.store.ReadClearDevPlanningStepRecovery(ctx, f.id, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: "late-unknown-before-transaction", AttemptID: state.Binding.FirstAttemptID,
		ClientMessageID: f.step.ClientMessageID, Status: core.AgentAttemptDeliveryUnknown, FailureCategory: domain.AgentFailureDeliveryUnknown, RecordedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ApplyClearDevPlanningStepRecovery(ctx, "stale-proof", "", state.Binding, time.Now().UTC()); err == nil {
		t.Fatal("direct transaction trusted earlier eligibility")
	}
}
