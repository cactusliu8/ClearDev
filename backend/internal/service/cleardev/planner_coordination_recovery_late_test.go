package cleardev

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestCoordinationRecoveryAnyLateAssistantOutputKeepsOriginalStop(t *testing.T) {
	for _, noReply := range []bool{false, true} {
		name := "failed"
		if noReply {
			name = "completed-without-reply"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, h, before := newStoppedCoordinationFixtureMode(t, noReply)
			f.s.runBackground = func(func()) {}
			input := coordinationRecoveryInput(t, f, h)
			state, err := f.store.ReadClearDevPlannerCoordinationRecovery(ctx, h.requirementID, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := h.local.Snapshot(ctx, h.planner)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := f.store.TurnByID(ctx, state.Binding.NativeTurnID)
			if err != nil {
				t.Fatal(err)
			}
			// Even an unfinished/invalid answer is contrary evidence. Do not
			// silently spend another original message instead of inspecting it.
			if err := f.store.AppendAssistantDelta(ctx, snapshot.Conversation.ID, "late-partial-answer", turn.ProviderTurnID, "partial output", "late-stream", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err == nil {
				t.Fatal("late assistant output was ignored by original-message recovery")
			}
			after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
			if err != nil || len(after.PlannerRuntime.Recoveries) != 0 || !reflect.DeepEqual(before.PlannerRuntime.Decisions, after.PlannerRuntime.Decisions) {
				t.Fatal("refusal changed original STOP", err)
			}
		})
	}
}

func TestCoordinationRecoveryRegisteredAttemptRechecksNativeEvidence(t *testing.T) {
	for _, change := range []string{"late-answer", "native-identity", "paused", "late-terminal"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			f, h, before := newStoppedCoordinationFixtureMode(t, true)
			f.s.runBackground = func(func()) {}
			input := coordinationRecoveryInput(t, f, h)
			state, err := f.store.ReadClearDevPlannerCoordinationRecovery(ctx, h.requirementID, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "late-answer":
				snapshot, err := h.local.Snapshot(ctx, h.planner)
				if err != nil {
					t.Fatal(err)
				}
				turn, err := f.store.TurnByID(ctx, state.Binding.NativeTurnID)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.AppendAssistantDelta(ctx, snapshot.Conversation.ID, "registered-late-answer", turn.ProviderTurnID, "partial output", "registered-late-stream", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			case "native-identity":
				rec, _, err := f.store.GetSession(ctx, h.planner)
				if err != nil {
					t.Fatal(err)
				}
				rec.Metadata.ProviderConversationID = "unexpected-native"
				if err := f.store.UpdateSession(ctx, rec); err != nil {
					t.Fatal(err)
				}
			case "paused":
				stopBuilderFirstFixture(t, f, before, "paused")
			case "late-terminal":
				db := coordinationFixtureDB(t, f)
				if _, err := db.Exec("UPDATE conversation_provider_events SET received_at='2099-01-01T00:00:00Z' WHERE id=?", state.Binding.TerminalEventID); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.s.checkPlannerCoordinationRecoveryBeforeSend(ctx, h.requirementID, state.Binding.LogicalStepID); err == nil {
				t.Fatal("changed evidence was admitted after registration")
			}
			after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
			if err != nil || !reflect.DeepEqual(before.PlannerRuntime.Decisions, after.PlannerRuntime.Decisions) {
				t.Fatal("pre-send refusal changed old STOP", err)
			}
			if blocked, _ := core.PlannerRuntimeBarrier(after); !blocked {
				t.Fatal("pre-send refusal opened execution")
			}
			budget, err := f.store.GetClearDevMessageBudget(ctx, h.requirementID)
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range budget.Steps {
				if step.LogicalStepID == state.Binding.LogicalStepID && (step.ReservedMessages == nil || *step.ReservedMessages != 1) {
					t.Fatal("pre-send refusal spent another message", step)
				}
			}
			h.conv.mu.Lock()
			defer h.conv.mu.Unlock()
			if len(h.conv.sent) != 1 {
				t.Fatal("pre-send refusal reached provider")
			}
		})
	}
}

func TestCoordinationRecoveryConcurrentSecondDeliveryIsUnique(t *testing.T) {
	ctx := context.Background()
	f, h, before := newStoppedCoordinationFixture(t)
	f.s.runBackground = func(func()) {}
	input := coordinationRecoveryInput(t, f, h)
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	planning, found, err := f.store.GetClearDevComplexPlanning(ctx, h.requirementID)
	if err != nil || !found {
		t.Fatal("missing planning", err)
	}
	request := before.PlannerRuntime.Requests[0]
	step, found := core.ComplexAgentStepByRequest(planning, core.ComplexAgentStepEngineeringPlan, request.EventID)
	if !found {
		t.Fatal("original step missing")
	}
	var ids atomic.Int64
	f.s.newID = func() string { return fmt.Sprintf("concurrent-retry-preflight-%d", ids.Add(1)) }
	h.conv.mu.Lock()
	h.conv.fail = false
	h.conv.mu.Unlock()
	start := make(chan struct{})
	results := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- f.s.relayAgentTurn(ctx, h.requirementID, core.AgentStepCategoryComplexPlanning, step, string(h.planner), request.Prompt, step.ClientMessageID, core.AgentAttemptSent, time.Now().UTC())
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Fatal("no concurrent caller delivered the registered retry")
	}
	h.conv.mu.Lock()
	defer h.conv.mu.Unlock()
	if len(h.conv.sent) != 2 || h.conv.sent[1].ClientMessageID != h.conv.sent[0].ClientMessageID+":attempt:2" || h.conv.sent[1].Text != h.conv.sent[0].Text {
		t.Fatal("concurrent send repeated or changed the original Planner message", len(h.conv.sent))
	}
	budget, err := f.store.GetClearDevMessageBudget(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range budget.Steps {
		if b.LogicalStepID == step.ID && (b.ReservedMessages == nil || *b.ReservedMessages != 2 || b.ConfirmedSentMessages == nil || *b.ConfirmedSentMessages != 2) {
			t.Fatal("concurrent delivery changed message accounting", b)
		}
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil || !reflect.DeepEqual(before.PlannerRuntime.Decisions, after.PlannerRuntime.Decisions) || len(after.PlannerRuntime.Requests) != len(before.PlannerRuntime.Requests) {
		t.Fatal("concurrent send changed original STOP or coordination budget", err)
	}
}
