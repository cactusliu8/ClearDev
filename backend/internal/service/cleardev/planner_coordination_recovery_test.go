package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// Only provider responses, Git/check execution and trial observations are
// doubles. Planner delivery, native turn settlement and all workflow facts use
// real Chat and SQLite. This test is not live-model or ACC-005 evidence.
type coordinationRecoveryConversation struct {
	beforeFailure        []ports.ChatEvent
	rawFailure           string
	completeWithoutReply bool
	events               chan ports.ChatEvent
	mu                   sync.Mutex
	sent                 []ports.ChatUserMessage
	fail                 bool
	reply                func() (string, error)
	once                 sync.Once
}

func (c *coordinationRecoveryConversation) ProviderConversationID() string {
	return "coordination-native"
}
func (c *coordinationRecoveryConversation) Capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{ports.ChatCapabilityStreaming: true, ports.ChatCapabilityApprovals: true, ports.ChatCapabilityInterrupt: true, ports.ChatCapabilityResume: true}
}
func (c *coordinationRecoveryConversation) Events() <-chan ports.ChatEvent { return c.events }
func (c *coordinationRecoveryConversation) Close() error {
	c.once.Do(func() { close(c.events) })
	return nil
}
func (*coordinationRecoveryConversation) Interrupt(context.Context, string) error { return nil }
func (*coordinationRecoveryConversation) ResolveRequest(context.Context, string, ports.ChatDecision) error {
	return errors.New("test must never approve")
}
func (c *coordinationRecoveryConversation) SendTurn(_ context.Context, msg ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, msg)
	id := fmt.Sprintf("coordination-provider-%d", len(c.sent))
	c.events <- ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: id}
	if c.fail && c.completeWithoutReply {
		c.events <- ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: id, TurnState: domain.TurnStateCompleted}
	} else if c.fail {
		for _, event := range c.beforeFailure {
			event.ProviderTurnID = id
			c.events <- event
		}
		if c.rawFailure != "" {
			c.events <- ports.ChatEvent{Kind: ports.ChatEventError, ProviderTurnID: id, Err: errors.New(c.rawFailure)}
		}
		c.events <- ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: id, TurnState: domain.TurnStateFailed, Failure: &domain.ConversationFailure{Category: domain.AgentFailureProvider, ErrorSummary: "native tool request timed out"}}
	} else {
		reply, err := c.reply()
		if err != nil {
			return ports.ChatTurnRef{}, err
		}
		c.events <- ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: id, ProviderItemID: id + "-answer", Text: reply}
		c.events <- ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: id, TurnState: domain.TurnStateCompleted}
	}
	return ports.ChatTurnRef{ProviderTurnID: id}, nil
}

type coordinationRecoveryDriver struct {
	c *coordinationRecoveryConversation
}

func (d coordinationRecoveryDriver) Harness() domain.AgentHarness { return domain.HarnessCodex }
func (d coordinationRecoveryDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	return d.c.Capabilities(), nil
}
func (d coordinationRecoveryDriver) Start(context.Context, ports.ChatStartConfig) (ports.ChatConversation, error) {
	return d.c, nil
}
func (d coordinationRecoveryDriver) Resume(context.Context, ports.ChatResumeConfig) (ports.ChatConversation, error) {
	return d.c, nil
}
func (d coordinationRecoveryDriver) Driver(domain.AgentHarness) (ports.ChatDriver, error) {
	return d, nil
}
func (d coordinationRecoveryDriver) SupportsChat(domain.AgentHarness) bool { return true }

type coordinationRecoveryHarness struct {
	*projectPlannerHarness
	local   *chatsvc.Service
	conv    *coordinationRecoveryConversation
	planner domain.SessionID
}

func (h *coordinationRecoveryHarness) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	if !strings.HasPrefix(prompt, "你是本阶段原工程 Planner") {
		return h.projectPlannerHarness.RelayChatTurnWithID(ctx, id, prompt, key)
	}
	if h.planner == "" || !h.local.HasLiveChatController(id) {
		h.planner = id
		rec, _, err := h.store.GetSession(ctx, id)
		if err != nil {
			return "", err
		}
		_, err = h.local.StartChat(ctx, chatsvc.StartRequest{SessionID: id, ProjectID: rec.ProjectID, Kind: rec.Kind, Harness: rec.Harness, Permissions: rec.PermissionMode, WorkspacePath: rec.Metadata.WorkspacePath, ProviderConversationID: rec.Metadata.ProviderConversationID,
			ControllerReady: func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
				rec.Activity.State = domain.ActivityIdle
				rec.Metadata.ProviderConversationID = started.ProviderConversationID
				rec.Metadata.ControllerGeneration = started.ControllerGeneration
				return chatsvc.ControllerCommit{}, h.store.UpdateSession(ctx, rec)
			}})
		if err != nil {
			return "", err
		}
	}
	return h.local.RelayChatTurnWithID(ctx, id, prompt, key)
}
func (h *coordinationRecoveryHarness) Snapshot(ctx context.Context, id domain.SessionID) (chatsvc.Snapshot, error) {
	if id == h.planner {
		return h.local.Snapshot(ctx, id)
	}
	return h.projectPlannerHarness.Snapshot(ctx, id)
}
func newStoppedCoordinationFixture(t *testing.T) (*projectPlanningFixture, *coordinationRecoveryHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	return newStoppedCoordinationFixtureMode(t, false)
}
func newStoppedCoordinationFixtureMode(t *testing.T, noReply bool) (*projectPlanningFixture, *coordinationRecoveryHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, base, before := settledProjectFailure(t, "BLOCKED")
	h := &coordinationRecoveryHarness{projectPlannerHarness: &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}}
	h.candidateSHAs = append(h.candidateSHAs, forty("d"))
	h.builderDiagnosis = true
	h.finalVerdict = "PASS"
	h.conv = &coordinationRecoveryConversation{events: make(chan ports.ChatEvent, 32), fail: true, completeWithoutReply: noReply}
	h.conv.reply = func() (string, error) {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
		if err != nil {
			return "", err
		}
		r := map[string]any{"schemaVersion": 1, "kind": core.PlannerRuntimeResultKind, "decision": core.PlannerRuntimeAmend, "summary": "Keep the original acceptance and recheck the repaired candidate.", "questions": []string{}, "amendments": []core.PlannerRemainingAmendment{{TaskKey: x.Tasks[0].TaskKey, AdditionalReviewCriteria: []string{"Personally verify the original required failure behavior without changing acceptance."}}}}
		raw, err := json.Marshal(r)
		return string(raw), err
	}
	var ids atomic.Int64
	h.local = chatsvc.New(chatsvc.Options{Store: f.store, Sessions: f.store, Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, id string) (chatsvc.ConversationRows, error) {
		r, e := f.store.LoadConversationSnapshot(ctx, id)
		return chatsvc.ConversationRows{Conversation: r.Conversation, Turns: r.Turns, Messages: r.Messages, Activities: r.Activities}, e
	}), Drivers: coordinationRecoveryDriver{h.conv}, Now: time.Now, NewID: func() string { return fmt.Sprintf("native-test-%d", ids.Add(1)) }, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() {
		if h.planner != "" {
			_ = h.local.Stop(context.Background(), h.planner)
		}
	})
	f.s.chat = h
	f.s.stepTimeout = 5 * time.Second
	for range 80 {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if x.PlannerRuntime != nil && len(x.PlannerRuntime.Decisions) == 1 {
			d := x.PlannerRuntime.Decisions[0]
			if d.ReasonCode != core.ReasonPlannerRuntimeUnavailable || d.ResultJSON != "" {
				t.Fatalf("not the intended technical stop: %+v", d)
			}
			return f, h, x
		}
		_, _, err = f.s.advanceComplexStandardExecution(context.Background(), h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
	}
	t.Fatal("Planner failure did not become a durable STOP")
	return nil, nil, core.ComplexExecutionSnapshot{}
}

func TestCoordinationRecoveryOffersUnusedSecondAttempt(t *testing.T) {
	f, h, before := newStoppedCoordinationFixture(t)
	view, err := f.s.GetWorkflowRecovery(context.Background(), h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range view.Options {
		if o.Action == "RETRY_PLANNER_COORDINATION" {
			found = true
			if o.UnavailableReason != "" {
				state, _ := f.store.ReadClearDevPlannerCoordinationRecovery(context.Background(), h.requirementID, time.Now().UTC())
				attempts, _ := f.store.ListClearDevAgentStepAttemptStates(context.Background(), h.requirementID, before.PlannerRuntime.Requests[0].AgentStepID)
				t.Logf("proof=%+v attempts=%+v", state, attempts)
				snapshot, _ := h.local.Snapshot(context.Background(), h.planner)
				t.Logf("native turns=%+v", snapshot.Turns)
				t.Fatalf("valid original retry unavailable: %+v", o)
			}
		}
	}
	if !found {
		t.Fatalf("known failed original Planner has no bounded recovery: %+v", view.Options)
	}
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read-only option changed execution", err)
	}
	h.conv.mu.Lock()
	defer h.conv.mu.Unlock()
	if len(h.conv.sent) != 1 {
		t.Fatal("option read sent a new message", len(h.conv.sent))
	}
}

func coordinationRecoveryInput(t *testing.T, f *projectPlanningFixture, h *coordinationRecoveryHarness) WorkflowRecoveryInput {
	t.Helper()
	view, err := f.s.GetWorkflowRecovery(context.Background(), h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range view.Options {
		if o.Action == "RETRY_PLANNER_COORDINATION" && o.UnavailableReason == "" {
			return WorkflowRecoveryInput{RequestID: "original-coordination-retry", ExecutionRunID: view.ExecutionRunID, Action: o.Action, TargetID: o.TargetID, Supplement: "The provider tool-permission configuration was repaired; keep the original contract and budget."}
		}
	}
	t.Fatalf("no available Planner retry: %+v", view.Options)
	return WorkflowRecoveryInput{}
}

func TestCoordinationRecoveryKeepsStopAndRechecksNewCandidate(t *testing.T) {
	ctx := context.Background()
	f, h, before := newStoppedCoordinationFixture(t)
	f.s.runBackground = func(func()) {}
	input := coordinationRecoveryInput(t, f, h)
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal("register", err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal("replay", err)
	}
	registered, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(registered.PlannerRuntime.Decisions, before.PlannerRuntime.Decisions) || !reflect.DeepEqual(registered.PlannerRuntime.Requests, before.PlannerRuntime.Requests) || len(registered.PlannerRuntime.Recoveries) != 1 {
		t.Fatal("registration rewrote original history")
	}
	if blocked, reason := core.PlannerRuntimeBarrier(registered); !blocked || reason != core.ReasonPlannerRuntimePending {
		t.Fatalf("pending retry opened execution: %v %s", blocked, reason)
	}
	h.conv.mu.Lock()
	h.conv.fail = false
	h.conv.mu.Unlock()
	var after core.ComplexExecutionSnapshot
	for range 80 {
		after, _, err = f.store.GetClearDevComplexExecution(ctx, h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Run.CompletedAt != nil {
			break
		}
		_, _, err = f.s.advanceComplexStandardExecution(ctx, h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal("advance", err)
		}
	}
	if after.Run.CompletedAt == nil || len(after.PlannerRuntime.RecoveryDecisions) != 1 || after.PlannerRuntime.RecoveryDecisions[0].Outcome != core.PlannerRuntimeAmend || len(after.Dispatches) != 3 || after.FinalReview == nil || after.FinalReview.Verdict != "PASS" {
		phase, reason := core.DeriveComplexExecutionPhase(after)
		t.Fatalf("recovery did not reach original checks/review: phase=%s reason=%s history=%+v", phase, reason, after.PlannerRuntime)
	}
	if !reflect.DeepEqual(before.PlannerRuntime.Decisions, after.PlannerRuntime.Decisions) || !reflect.DeepEqual(before.PlannerRuntime.Requests, after.PlannerRuntime.Requests) || len(after.PlannerRuntime.Recoveries) != 1 || len(after.PlannerRuntime.Amendments) != 1 {
		t.Fatal("success erased stop, changed request count or applied twice")
	}
	h.conv.mu.Lock()
	defer h.conv.mu.Unlock()
	if len(h.conv.sent) != 2 || h.conv.sent[1].ClientMessageID != h.conv.sent[0].ClientMessageID+":attempt:2" || h.conv.sent[1].Text != h.conv.sent[0].Text {
		t.Fatal("original native prompt/session was not retried exactly once")
	}
}

func TestCoordinationRecoverySecondFailureStopsWithoutRefundOrThirdAttempt(t *testing.T) {
	ctx := context.Background()
	f, h, before := newStoppedCoordinationFixture(t)
	f.s.runBackground = func(func()) {}
	input := coordinationRecoveryInput(t, f, h)
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		_, _, err := f.s.advanceComplexStandardExecution(ctx, h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.PlannerRuntime.Decisions, after.PlannerRuntime.Decisions) || len(after.PlannerRuntime.RecoveryDecisions) != 1 || after.PlannerRuntime.RecoveryDecisions[0].Outcome != "STOP" {
		t.Fatal("failed second attempt erased STOP or failed to stop", after.PlannerRuntime.RecoveryDecisions)
	}
	if blocked, _ := core.PlannerRuntimeBarrier(after); !blocked {
		t.Fatal("second failure opened execution")
	}
	view, err := f.s.GetWorkflowRecovery(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range view.Options {
		if o.Action == input.Action && o.UnavailableReason == "" {
			t.Fatal("failure offered a third attempt")
		}
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal("exact replay should remain idempotent", err)
	}
	attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, a := range attempts {
		if a.LogicalStepID == before.PlannerRuntime.Requests[0].AgentStepID {
			count++
		}
	}
	budget, err := f.store.GetClearDevMessageBudget(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range budget.Steps {
		if b.LogicalStepID == before.PlannerRuntime.Requests[0].AgentStepID {
			if b.ReservedMessages == nil || *b.ReservedMessages != 2 || b.ConfirmedSentMessages == nil || *b.ConfirmedSentMessages != 2 {
				t.Fatal("failed sends were refunded", b)
			}
		}
	}
	h.conv.mu.Lock()
	defer h.conv.mu.Unlock()
	if count != 2 || len(h.conv.sent) != 2 {
		t.Fatal("second failure repeated work", count, len(h.conv.sent))
	}
}

func TestCoordinationRecoveryRejectsChangedOrUnknownSources(t *testing.T) {
	for _, name := range []string{"unknown-delivery", "running-session", "terminated-session", "native-identity", "stale-target", "paused-project", "changed-context", "future-retry"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, h, before := newStoppedCoordinationFixture(t)
			input := coordinationRecoveryInput(t, f, h)
			f.s.runBackground = func(func()) {}
			rec, _, err := f.store.GetSession(ctx, h.planner)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "unknown-delivery", "future-retry":
				states, err := f.store.ListClearDevAgentStepAttemptStates(ctx, h.requirementID, before.PlannerRuntime.Requests[0].AgentStepID)
				if err != nil {
					t.Fatal(err)
				}
				a := states[0]
				ev := core.AgentAttemptEvent{ID: "new-contradictory-evidence", AttemptID: a.ID, Status: core.AgentAttemptDeliveryUnknown, FailureCategory: domain.AgentFailureDeliveryUnknown, ClientMessageID: a.ClientMessageID, PromptSHA256: a.PromptSHA256, RecordedAt: time.Now().UTC(), ErrorSummary: "uncertain delivery"}
				if name == "future-retry" {
					future := time.Now().UTC().Add(time.Hour)
					ev.Status = core.AgentAttemptFailed
					ev.FailureCategory = domain.AgentFailureProvider
					ev.TurnState = "failed"
					ev.RetryAt = &future
				}
				if err := f.store.RecordClearDevAgentAttemptEvent(ctx, ev); err != nil {
					t.Fatal(err)
				}
			case "running-session":
				rec.Activity.State = domain.ActivityActive
				err = f.store.UpdateSession(ctx, rec)
			case "terminated-session":
				rec.IsTerminated = true
				err = f.store.UpdateSession(ctx, rec)
			case "native-identity":
				rec.Metadata.ProviderConversationID = "another-native"
				err = f.store.UpdateSession(ctx, rec)
			case "stale-target":
				// The production update API intentionally cannot change this immutable
				// field; test a stale public target instead of rewriting history.
				input.TargetID += "stale"
			case "paused-project":
				stopBuilderFirstFixture(t, f, before, "paused")
			case "changed-context":
				// Mutable source observation is rechecked by the service even though all
				// stored protocol hashes remain intact.
				stopBuilderFirstFixture(t, f, before, "stale-spec")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err == nil {
				t.Fatal("changed source was admitted")
			}
			after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.PlannerRuntime.Decisions, after.PlannerRuntime.Decisions) || len(after.PlannerRuntime.Recoveries) != 0 {
				t.Fatal("refusal changed old STOP or registered new work")
			}
			h.conv.mu.Lock()
			defer h.conv.mu.Unlock()
			if len(h.conv.sent) != 1 {
				t.Fatal("refusal reached provider")
			}
		})
	}
}

func TestCoordinationRecoveryConcurrentRegistrationIsUnique(t *testing.T) {
	ctx := context.Background()
	f, h, before := newStoppedCoordinationFixture(t)
	f.s.runBackground = func(func()) {}
	state, err := f.store.ReadClearDevPlannerCoordinationRecovery(ctx, h.requirementID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- f.store.ApplyClearDevPlannerCoordinationRecovery(ctx, "same-request", "Original provider environment repaired.", state.Binding, time.Now().UTC())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.ApplyClearDevPlannerCoordinationRecovery(ctx, "different-request", "Original provider environment repaired.", state.Binding, time.Now().UTC()); err == nil {
		t.Fatal("different request acquired an already registered target")
	}
	if err := f.store.ApplyClearDevPlannerCoordinationRecovery(ctx, "same-request", "different supplement", state.Binding, time.Now().UTC()); err == nil {
		t.Fatal("same request changed supplement")
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.PlannerRuntime.Recoveries) != 1 || !reflect.DeepEqual(before.PlannerRuntime.Decisions, after.PlannerRuntime.Decisions) {
		t.Fatal("concurrent registration changed history")
	}
	states, err := f.store.ListClearDevAgentStepAttemptStates(ctx, h.requirementID, state.Binding.LogicalStepID)
	if err != nil || len(states) != 2 || states[1].TriggerFailureEventID != states[0].LastEventID {
		t.Fatal("concurrent registration created wrong attempts", states, err)
	}
}

func TestCoordinationRecoveryEndedWithoutAnswerRequiresPositiveTerminalProof(t *testing.T) {
	ctx := context.Background()
	f, h, before := newStoppedCoordinationFixtureMode(t, true)
	f.s.runBackground = func(func()) {}
	state, err := f.store.ReadClearDevPlannerCoordinationRecovery(ctx, h.requirementID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if state.Option.UnavailableReason != "" || state.Binding.NativeTurnState != "completed" || state.Binding.TerminalEventID == 0 {
		t.Fatal("no-reply turn not bound to its positive terminal evidence", state)
	}
	input := coordinationRecoveryInput(t, f, h)
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	turn, err := f.store.TurnByID(ctx, state.Binding.NativeTurnID)
	if err != nil || turn.State != domain.TurnStateCompleted {
		t.Fatal("recovery rewrote native completed history", turn, err)
	}
	h.conv.mu.Lock()
	h.conv.fail = false
	h.conv.mu.Unlock()
	for range 40 {
		x, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.PlannerRuntime.RecoveryDecisions) > 0 {
			if x.PlannerRuntime.RecoveryDecisions[0].Outcome != core.PlannerRuntimeAmend || !reflect.DeepEqual(x.PlannerRuntime.Decisions, before.PlannerRuntime.Decisions) {
				t.Fatal("recovery did not keep original stop", x.PlannerRuntime)
			}
			return
		}
		_, _, err = f.s.advanceComplexStandardExecution(ctx, h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
	}
	t.Fatal("ended no-reply recovery never produced a decision")
}

func TestCoordinationRecoveryLateAssistantOutputRefusesNoReplyRecovery(t *testing.T) {
	ctx := context.Background()
	f, h, _ := newStoppedCoordinationFixtureMode(t, true)
	f.s.runBackground = func(func()) {}
	input := coordinationRecoveryInput(t, f, h)
	snapshot, err := h.local.Snapshot(ctx, h.planner)
	if err != nil {
		t.Fatal(err)
	}
	turn := snapshot.Turns[0]
	if err := f.store.SettleAssistantMessage(ctx, snapshot.Conversation.ID, "late-provider-output", turn.ProviderTurnID, "late result must be reviewed, not silently resent", "late-result", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err == nil {
		t.Fatal("completed turn with an assistant result was resent")
	}
}
