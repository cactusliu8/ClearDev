package cleardev

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// The failed call traverses real Chat Send and the real SQLite lifecycle claim.
// Subsequent provider replies, Git and checks remain the project's explicit
// doubles. This test proves workflow recovery, not live model/browser acceptance.
type projectPreSendChat struct {
	*workflowBlockedBuilderHarness
	local        *chatsvc.Service
	firstMessage string
	blockedCalls int
}

func (h *projectPreSendChat) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	if key == h.firstMessage {
		h.blockedCalls++
		return h.local.RelayChatTurnWithID(ctx, id, prompt, key)
	}
	return h.workflowBlockedBuilderHarness.RelayChatTurnWithID(ctx, id, prompt, key)
}

func TestPreSendProjectRecheckResumesOriginalBuilderIntoReview(t *testing.T) {
	ctx := context.Background()
	f, provider, id, before, original := newBuilderSessionRecheckFixture(t)
	pending := before.Dispatches[len(before.Dispatches)-1]
	step, found := complexExecutionStepByID(before, pending.AgentStepID)
	if !found || step.SendStatus != core.AgentStepSendStatusPending {
		t.Fatal("expected the original unsent recovery step", step)
	}
	f.s.restoreOriginalAgentSession = func(ctx context.Context, sid domain.SessionID) (string, error) {
		if sid != original.ID {
			return "", errors.New("recovery changed original identity")
		}
		record, _, err := f.store.GetSession(ctx, sid)
		if err != nil {
			return "", err
		}
		record.Activity.State = domain.ActivityIdle
		return "native", f.store.UpdateSession(ctx, record)
	}
	input := builderSessionRecheckInput(t, f, id, "presend-original-recheck")
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	const operation = "presend-existing-switch"
	if claimed, err := f.store.BeginClearDevBuilderSessionOperation(ctx, original.ID, operation, "switch", time.Now().UTC()); err != nil || !claimed {
		t.Fatal("claim existing lifecycle operation", claimed, err)
	}
	local := chatsvc.New(chatsvc.Options{Store: f.store, Sessions: f.store})
	chat := &projectPreSendChat{workflowBlockedBuilderHarness: provider, local: local, firstMessage: step.ClientMessageID}
	f.s.chat = chat
	if err := f.s.runComplexStandardExecution(ctx, id); !errors.Is(err, ports.ErrChatSendNotStarted) || !errors.Is(err, ports.ErrBuilderSessionOperationBusy) {
		t.Fatal("real local claim failure was not preserved", err)
	}
	first, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, step.ID)
	if err != nil || len(first) != 1 || !core.FailedBeforeSendViewValid(first[0]) || chat.blockedCalls != 1 {
		t.Fatal("first attempt lacks durable local refusal proof", first, chat.blockedCalls, err)
	}
	for _, relay := range provider.relays {
		if relay.clientMessageID == step.ClientMessageID {
			t.Fatal("local refusal reached provider double")
		}
	}
	if err := f.store.EndClearDevBuilderSessionOperation(ctx, operation, "FAILED_BEFORE_ACTION", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	provider.block = false
	// Scheduler wake performs the original-session preflight, creates only the
	// second attempt, then still requires the existing candidate/review gates.
	if err := f.s.runComplexStandardExecution(ctx, id); err != nil {
		t.Fatal("resume after confirmed local refusal", err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
	if err != nil || after.Run.CompletedAt == nil || len(after.Dispatches) != len(before.Dispatches) || len(after.Reviews) == 0 || after.FinalReview == nil || len(provider.checkRequests) == 0 {
		t.Fatalf("original workflow did not complete its check/review gates: completed=%v dispatches=%d reviews=%d final=%v err=%v", after.Run.CompletedAt, len(after.Dispatches), len(after.Reviews), after.FinalReview != nil, err)
	}
	states, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, step.ID)
	if err != nil || len(states) != 2 || states[0].LastEventID != first[0].LastEventID || states[1].TriggerFailureEventID != first[0].LastEventID || states[1].AOSessionID != first[0].AOSessionID || states[1].SendStatus != core.AgentAttemptCompleted {
		t.Fatal("recovery changed original proof/session or lost its result", states, err)
	}
	count := 0
	for _, relay := range provider.relays {
		if relay.clientMessageID == step.ClientMessageID {
			t.Fatal("old message was resent")
		}
		if relay.clientMessageID == step.ClientMessageID+":attempt:2" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("second provider delivery count", count)
	}
	ledger, err := f.store.GetClearDevMessageBudget(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, usage := range ledger.Steps {
		if usage.LogicalStepID == step.ID {
			matched = true
			if *usage.ReservedMessages != 2 || *usage.ConfirmedSentMessages != 1 {
				t.Fatal("reservation/refund changed", usage)
			}
		}
	}
	if !matched {
		t.Fatal("missing logical step budget")
	}
	beforeReplay := len(provider.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	if err := f.s.runComplexStandardExecution(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(provider.relays) != beforeReplay || chat.blockedCalls != 1 {
		t.Fatal("completed wake duplicated delivery")
	}
	if strings.Contains(states[0].ErrorSummary, "PROVIDER") {
		t.Fatal("local proof fabricated provider failure")
	}
}
