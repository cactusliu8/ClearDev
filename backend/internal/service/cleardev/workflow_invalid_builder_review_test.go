package cleardev

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestWorkflowInvalidBuilderRejectsInterleavedMessageUnknown(t *testing.T) {
	ctx := context.Background()
	f, _, id, before := invalidBuilderWorkflowFixture(t)
	states, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, before.Dispatches[0].AgentStepID)
	if err != nil || len(states) != 1 {
		t.Fatalf("missing attempt: %v", err)
	}
	a := states[0]
	for _, event := range []core.AgentAttemptEvent{
		{ID: "original-late-unknown", AttemptID: a.ID, Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: a.ClientMessageID, FailureCategory: domain.AgentFailureDeliveryUnknown, RecordedAt: time.Now().UTC()},
		{ID: "correction-later-invalid", AttemptID: a.ID, Status: core.AgentAttemptFailed, ClientMessageID: a.ClientMessageID + ":parse-correction", FailureCategory: domain.AgentFailureResultInvalid, TurnState: domain.TurnStateFailed, RecordedAt: time.Now().UTC()},
	} {
		if err := f.store.RecordClearDevAgentAttemptEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, known, err := f.store.ReadClearDevStoppedBuilderResult(ctx, before.Run.ID, before.Dispatches[0].ID); err != nil || known {
		t.Fatalf("last correction event hid original-message uncertainty: known=%v err=%v", known, err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, invalidBuilderRecoveryInput(before)); err == nil {
		t.Fatal("interleaved unknown message allowed another round")
	}
}

func TestWorkflowInvalidBuilderDoesNotTrustIdleWhenTurnIsUnsettled(t *testing.T) {
	for _, source := range []string{"database", "snapshot"} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			f, h, id, before := invalidBuilderWorkflowFixture(t)
			r := invalidBuilderStoreRecovery(t, f, before)
			binding, _ := complexExecutionBindingByID(before, before.Dispatches[0].BuilderRoleBindingID)
			record, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
			if err != nil || record.Activity.State != domain.ActivityIdle {
				t.Fatalf("fixture must have a stale idle label: %v", err)
			}
			if source == "database" {
				conversation, err := f.store.CreateConversation(ctx, "inflight-conversation", domain.ConversationScopeSession, record.ProjectID, record.ID, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.AdoptProviderTurn(ctx, conversation.ID, record.ID, "fixture-generation", "active-turn", "native-active-turn", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if err := f.store.ApplyClearDevWorkflowRecovery(ctx, r, nil); err == nil {
					t.Fatal("storage ignored an actual unsettled conversation turn")
				}
			} else {
				snapshot := h.snapshots[record.ID]
				snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{ID: "active-turn", HandledBySessionID: record.ID, State: domain.TurnStateRunning})
				h.snapshots[record.ID] = snapshot
			}
			view, err := f.s.GetWorkflowRecovery(ctx, id)
			if err != nil || len(view.Options) != 1 || view.Options[0].UnavailableReason != "RESULT_NOT_SETTLED" {
				t.Fatalf("idle label hid an unsettled turn: %+v %v", view, err)
			}
			calls := len(h.relays)
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, invalidBuilderRecoveryInput(before)); err == nil || calls != len(h.relays) {
				t.Fatal("unsettled turn acquired new work")
			}
		})
	}
}

// Explicitly omit one part of first-reply persistence while retaining the real
// SQLite schema and later correction records. This models incomplete legacy
// evidence; it is not a claim that today's writer drops successful writes.
type incompleteOriginalBuilderStore struct {
	AgentAttemptStore
	missing   string
	resultID  string
	attemptID string
}

func (s *incompleteOriginalBuilderStore) RecordClearDevAgentStepResult(ctx context.Context, r core.AgentStepResult) error {
	if r.Source == core.AgentResultOriginal && r.RawMessageText == "{}" {
		s.resultID, s.attemptID = r.ID, r.AttemptID
		if s.missing == "original-result" {
			return nil
		}
	}
	return s.AgentAttemptStore.RecordClearDevAgentStepResult(ctx, r)
}

func (s *incompleteOriginalBuilderStore) RecordClearDevAgentAttemptEvent(ctx context.Context, event core.AgentAttemptEvent) error {
	if s.missing == "original-completed" && event.AttemptID == s.attemptID && event.Status == core.AgentAttemptCompleted && !strings.HasSuffix(event.ClientMessageID, ":parse-correction") {
		return nil
	}
	return s.AgentAttemptStore.RecordClearDevAgentAttemptEvent(ctx, event)
}

func (s *incompleteOriginalBuilderStore) RecordClearDevAgentStepResultParse(ctx context.Context, parse core.AgentStepResultParse) error {
	if parse.ResultID == s.resultID && (s.missing == "original-result" || s.missing == "original-parse") {
		return nil
	}
	return s.AgentAttemptStore.RecordClearDevAgentStepResultParse(ctx, parse)
}

func (s *incompleteOriginalBuilderStore) ConfirmClearDevAgentMessage(ctx context.Context, event core.AgentAttemptEvent) error {
	if s.missing == "correction-confirmation" && event.Status == core.AgentAttemptCorrectionSent {
		return s.AgentAttemptStore.RecordClearDevAgentAttemptEvent(ctx, event)
	}
	return s.AgentAttemptStore.ConfirmClearDevAgentMessage(ctx, event)
}

func TestWorkflowInvalidBuilderRequiresOriginalReplyBeforeCorrection(t *testing.T) {
	for _, missing := range []string{"original-result", "original-completed", "original-parse", "correction-confirmation"} {
		t.Run(missing, func(t *testing.T) {
			f, _, id, before := invalidBuilderWorkflowFixtureSetup(t, "invalid", func(f *projectPlanningFixture) {
				f.s.attempts = &incompleteOriginalBuilderStore{AgentAttemptStore: f.store, missing: missing}
			})
			f.s.attempts = f.store
			if _, known, err := f.store.ReadClearDevStoppedBuilderResult(context.Background(), before.Run.ID, before.Dispatches[0].ID); err != nil || known {
				t.Fatalf("missing %s was hidden by the final invalid correction: %v %v", missing, known, err)
			}
			if _, err := f.s.RequestWorkflowRecovery(context.Background(), id, invalidBuilderRecoveryInput(before)); err == nil {
				t.Fatal("incomplete original evidence allowed recovery")
			}
		})
	}
}
