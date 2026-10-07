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
)

type stoppedProviderBuilder struct{ *projectExecutionFlowHarness }

func (h *stoppedProviderBuilder) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, key string) (string, error) {
	id, err := h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, session, prompt, key)
	if err != nil {
		return id, err
	}
	snap := h.snapshots[session]
	for i := range snap.Turns {
		if snap.Turns[i].ID == id && snap.Turns[i].Failure != nil {
			snap.Turns[i].Failure = &domain.ConversationFailure{Category: domain.AgentFailureProvider, ErrorSummary: "explicit generic provider failure", Retryable: false}
			rec, _, err := h.store.GetSession(ctx, session)
			if err != nil {
				return id, err
			}
			rec.Metadata.ProviderConversationID = "failed-builder-original-native"
			if err := h.store.UpdateSession(ctx, rec); err != nil {
				return id, err
			}
			conv, err := h.store.CreateConversation(ctx, "failed-conversation-"+string(session), domain.ConversationScopeSession, rec.ProjectID, session, time.Now().UTC())
			if err != nil {
				return id, err
			}
			if err := h.store.AdoptProviderTurn(ctx, conv.ID, session, "fixture-generation", id, "native-"+id, time.Now().UTC()); err != nil {
				return id, err
			}
			if err := h.store.SettleTurnByIDWithFailure(ctx, id, domain.TurnStateFailed, "fixture failure", *snap.Turns[i].Failure, time.Now().UTC()); err != nil {
				return id, err
			}
		}
	}
	h.snapshots[session] = snap
	return id, nil
}
func failedBuilderFixture(t *testing.T) (*projectPlanningFixture, *projectExecutionFlowHarness, string, core.ComplexExecutionSnapshot) {
	t.Helper()
	ctx := context.Background()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	f.s.stepTimeout = 10 * time.Second
	h := attachProjectFlow(f, preparer)
	h.builderTurnFailures = 1
	f.s.chat = &stoppedProviderBuilder{h}
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedInvalidBuilderWorkflow(t, f, child.Requirement.ID)
	if len(before.Dispatches) != 1 || before.Dispatches[0].ReasonCode != "BUILDER_UNAVAILABLE" {
		t.Fatal("wrong failure", before.Dispatches)
	}
	binding, _ := complexExecutionBindingByID(before, before.Dispatches[0].BuilderRoleBindingID)
	rec, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	rec.Metadata.ProviderConversationID = "failed-builder-original-native"
	if err := f.store.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	return f, h, child.Requirement.ID, before
}
func TestWorkflowFailedBuilderContinuesWithoutRewritingFailure(t *testing.T) {
	ctx := context.Background()
	f, h, id, before := failedBuilderFixture(t)
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.Options) != 1 || view.Options[0].Action != core.RecoveryContinueBuilder || view.Options[0].UnavailableReason != "" {
		t.Fatalf("confirmed failed Builder has no continuation: %+v %v", view.Options, err)
	}
	input := invalidBuilderRecoveryInput(before)
	input.RequestID = "failed-builder-continue"
	calls := len(h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 || len(after.WorkflowRecoveries) != 1 || after.Dispatches[1].BuilderRoleBindingID != before.Dispatches[0].BuilderRoleBindingID {
		t.Fatal("original Builder did not continue once")
	}
	if !reflect.DeepEqual(before.Dispatches[0], after.Dispatches[0]) {
		t.Fatal("old failure changed")
	}
	old, _ := complexExecutionStepByID(before, before.Dispatches[0].AgentStepID)
	for _, r := range h.relays[calls:] {
		if r.clientMessageID == old.ClientMessageID {
			t.Fatal("resent old request")
		}
	}
	for _, b := range after.Exception.Budgets {
		if b.RoleKind == "BUILDER" && b.UsedTurns != 2 {
			t.Fatal("wrong budget", b)
		}
	}
}

func TestWorkflowFailedBuilderRejectsChangedEvidence(t *testing.T) {
	for _, mode := range []string{"unknown", "interrupted", "turn-mismatch", "authentication", "native", "busy", "unsettled", "summary"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, h, id, before := failedBuilderFixture(t)
			d := before.Dispatches[0]
			b, _ := complexExecutionBindingByID(before, d.BuilderRoleBindingID)
			summary, known, err := f.store.ReadClearDevStoppedBuilderResult(ctx, before.Run.ID, d.ID)
			if err != nil || !known {
				t.Fatal("missing original proof", err)
			}
			r := core.WorkflowRecovery{ID: "direct-failed", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: d.ID, DispatchID: d.ID, TaskID: d.ComplexExecutionTaskID, StepID: d.AgentStepID, BindingID: d.BuilderRoleBindingID, SuccessorID: "successor", CandidateSHA: d.BaseCommitSHA, ProviderConversationID: "failed-builder-original-native", OriginalStoppedAt: *d.SettledAt, OriginalStatus: string(d.Status), OriginalReason: string(d.ReasonCode), OriginalSummary: summary, Supplement: "Continue original work", CreatedAt: time.Now().UTC()}
			switch mode {
			case "native", "busy":
				rec, _, err := f.store.GetSession(ctx, domain.SessionID(b.AOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				if mode == "native" {
					rec.Metadata.ProviderConversationID = "other-native"
				} else {
					rec.Activity.State = domain.ActivityActive
				}
				if err := f.store.UpdateSession(ctx, rec); err != nil {
					t.Fatal(err)
				}
			case "unsettled":
				rec, _, err := f.store.GetSession(ctx, domain.SessionID(b.AOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				conv, err := f.store.CreateConversation(ctx, "inflight-failed-builder", domain.ConversationScopeSession, rec.ProjectID, rec.ID, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.AdoptProviderTurn(ctx, conv.ID, rec.ID, "generation", "active-turn", "native-active", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			case "summary":
				r.OriginalSummary = "changed failure proof" // Exact proof is rechecked even for direct store calls.
			default:
				states, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, d.AgentStepID)
				if err != nil {
					t.Fatal(err)
				}
				a := states[len(states)-1]
				ev := core.AgentAttemptEvent{ID: "late-" + mode, AttemptID: a.ID, Status: core.AgentAttemptFailed, ClientMessageID: a.ClientMessageID, TurnID: a.TurnID, TurnState: domain.TurnStateFailed, FailureCategory: domain.AgentFailureProvider, ErrorSummary: "changed evidence", RecordedAt: time.Now().UTC()}
				switch mode {
				case "unknown":
					ev.Status = core.AgentAttemptDeliveryUnknown
					ev.FailureCategory = domain.AgentFailureDeliveryUnknown
				case "interrupted":
					ev.Status = core.AgentAttemptInterrupted
					ev.TurnState = domain.TurnStateInterrupted
				case "turn-mismatch":
					ev.TurnID = "other-turn"
				case "authentication":
					ev.FailureCategory = domain.AgentFailureAuthentication
				}
				if err := f.store.RecordClearDevAgentAttemptEvent(ctx, ev); err != nil {
					t.Fatal(err)
				}
			}
			calls := len(h.relays)
			if err := f.store.ApplyClearDevWorkflowRecovery(ctx, r, nil); err == nil {
				t.Fatal("transaction accepted changed evidence", mode)
			}
			if mode != "summary" {
				if _, err := f.s.RequestWorkflowRecovery(ctx, id, invalidBuilderRecoveryInput(before)); err == nil {
					t.Fatal("service accepted unsafe recovery", mode)
				}
			}
			if len(h.relays) != calls {
				t.Fatal("rejected recovery sent message")
			}
		})
	}
}

func TestWorkflowFailedBuilderConcurrentRequestsCreateOneSuccessor(t *testing.T) {
	f, _, id, before := failedBuilderFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := invalidBuilderRecoveryInput(before)
			input.RequestID = fmt.Sprintf("failed-race-%d", i)
			_, err := f.s.RequestWorkflowRecovery(context.Background(), id, input)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatal("wrong accepted count", accepted)
	}
	after := driveWorkflowRecovery(t, f, id)
	if len(after.WorkflowRecoveries) != 1 || len(after.Dispatches) != 2 {
		t.Fatal("multiple successors")
	}
}

func TestWorkflowFailedBuilderBudgetAndBeforeSendGuard(t *testing.T) {
	for _, mode := range []string{"budget", "late-unknown"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, h, id, stopped := failedBuilderFixture(t)
			if mode == "budget" {
				for round := 0; round < 3; round++ {
					h.builderTurnFailures = 1
					input := invalidBuilderRecoveryInput(stopped)
					input.RequestID = fmt.Sprintf("failed-round-%d", round)
					input.TargetID = stopped.Tasks[0].CurrentDispatchID
					if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
						t.Fatal(err)
					}
					stopped = stoppedInvalidBuilderWorkflow(t, f, id)
				}
				view, err := f.s.GetWorkflowRecovery(ctx, id)
				if err != nil || len(view.Options) != 1 || view.Options[0].UnavailableReason != "BUILDER_BUDGET_EXHAUSTED" {
					t.Fatalf("budget bypassed: %+v %v", view.Options, err)
				}
				input := invalidBuilderRecoveryInput(stopped)
				input.RequestID = "beyond-budget"
				input.TargetID = stopped.Tasks[0].CurrentDispatchID
				if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err == nil {
					t.Fatal("budget exhausted but resumed")
				}
				return
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, invalidBuilderRecoveryInput(stopped)); err != nil {
				t.Fatal(err)
			}
			states, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, stopped.Dispatches[0].AgentStepID)
			if err != nil {
				t.Fatal(err)
			}
			a := states[0]
			if err := f.store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: "late-original-unknown", AttemptID: a.ID, Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: a.ClientMessageID, FailureCategory: domain.AgentFailureDeliveryUnknown, RecordedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			calls := len(h.relays)
			after := stoppedInvalidBuilderWorkflow(t, f, id)
			if len(h.relays) != calls || after.Run.CompletedAt != nil {
				t.Fatal("sent after evidence changed")
			}
		})
	}
}
