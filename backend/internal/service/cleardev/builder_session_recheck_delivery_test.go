package cleardev

import (
	"context"
	"errors"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

var errRecheckTestReceiptLoss = errors.New("explicit test: delivery receipt could not be saved")

type recheckConfirmationLoss struct {
	AgentAttemptStore
	client string
	lost   bool
}

func (s *recheckConfirmationLoss) ConfirmClearDevAgentMessage(ctx context.Context, event core.AgentAttemptEvent) error {
	if event.ClientMessageID == s.client && !s.lost {
		s.lost = true
		return errRecheckTestReceiptLoss
	}
	return s.AgentAttemptStore.ConfirmClearDevAgentMessage(ctx, event)
}

type recheckStepMarkerLoss struct {
	*sqlitestore.Store
	step string
	lost bool
}

func (s *recheckStepMarkerLoss) MarkClearDevComplexExecutionAgentStepSent(ctx context.Context, step string, at time.Time) (bool, error) {
	if step == s.step && !s.lost {
		s.lost = true
		return false, errRecheckTestReceiptLoss
	}
	return s.Store.MarkClearDevComplexExecutionAgentStepSent(ctx, step, at)
}

func TestBuilderSessionRecheckLostDeliveryReceiptNeverResends(t *testing.T) {
	for _, where := range []string{"confirmation", "step-marker"} {
		t.Run(where, func(t *testing.T) {
			ctx := context.Background()
			f, h, id, before, _ := newBuilderSessionRecheckFixture(t)
			input := builderSessionRecheckInput(t, f, id, "recheck-receipt-loss")
			step, _ := complexExecutionStepByID(before, before.Dispatches[1].AgentStepID)
			restores := 0
			f.s.restoreOriginalAgentSession = func(ctx context.Context, sid domain.SessionID) (string, error) {
				restores++
				return restoreTestRecheckBuilder(f)(ctx, sid)
			}
			if where == "confirmation" {
				f.s.attempts = &recheckConfirmationLoss{AgentAttemptStore: f.store, client: step.ClientMessageID}
			} else {
				f.s.complexExecution = &recheckStepMarkerLoss{Store: f.store, step: step.ID}
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
				t.Fatal(err)
			}
			interrupted := false
			for range 20 {
				_, _, err := f.s.advanceComplexStandardExecution(ctx, id)
				if err != nil {
					if !errors.Is(err, errRecheckTestReceiptLoss) {
						t.Fatalf("unexpected stop before injected receipt loss: %v", err)
					}
					interrupted = true
					break
				}
			}
			if !interrupted || restores != 1 {
				t.Fatal("fixture did not lose the receipt after one native restoration")
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
				t.Fatal(err)
			}
			after := driveWorkflowRecovery(t, f, id)
			count := 0
			for _, relay := range h.relays {
				if relay.clientMessageID == step.ClientMessageID {
					count++
				}
			}
			if count != 1 || restores != 1 || after.Run.CompletedAt == nil || len(after.Dispatches) != len(before.Dispatches) {
				t.Fatalf("recheck replay duplicated work or lost continuation: sends=%d restores=%d", count, restores)
			}
		})
	}
}
