package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
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
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// Only model replies, Git, checks and trial observations are doubles. Every
// coordination request/decision and Human Authority transaction is real SQLite;
// Planner delivery and terminal-turn evidence use the production Chat service.
type extraCoordinationHarness struct {
	*coordinationRecoveryHarness
	taskKey string
}

func (h *extraCoordinationHarness) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	if strings.HasPrefix(prompt, "你是本阶段原工程 Planner") {
		return h.coordinationRecoveryHarness.RelayChatTurnWithID(ctx, id, prompt, key)
	}
	turn, err := h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, id, prompt, key)
	if err != nil {
		return turn, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := h.snapshots[id]
	for i, msg := range snapshot.Messages {
		if msg.TurnID != turn || msg.Role != domain.MessageRoleAssistant {
			continue
		}
		var reply map[string]any
		if json.Unmarshal([]byte(msg.Text), &reply) != nil || reply["kind"] != "BUILDER_RESULT" || reply["outcome"] != "CANDIDATE_READY" {
			continue
		}
		reply["coordination"] = core.PlannerCoordinationReport{Category: "ENGINEERING", Summary: "A downstream engineering observation requires the original Planner's judgment.", Evidence: []string{"src/storage.ts: retained failure-handling contract"}, AffectedTaskKeys: []string{h.taskKey}}
		raw, err := json.Marshal(reply)
		if err != nil {
			return turn, err
		}
		snapshot.Messages[i].Text = string(raw)
	}
	h.snapshots[id] = snapshot
	return turn, nil
}

func extraCoordinationFixture(t *testing.T) (*projectPlanningFixture, *extraCoordinationHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, base, before := settledProjectFailure(t, "BLOCKED")
	base.candidateSHAs = append(base.candidateSHAs, forty("d"), forty("e"), forty("f"))
	parent := &coordinationRecoveryHarness{projectPlannerHarness: &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}}
	parent.conv = &coordinationRecoveryConversation{events: make(chan ports.ChatEvent, 64)}
	parent.conv.reply = func() (string, error) {
		return `{"schemaVersion":1,"kind":"PLANNER_RUNTIME_COORDINATION","decision":"CONTINUE","summary":"The original contract already covers this engineering observation; do not change any budget or acceptance.","questions":[],"amendments":[]}`, nil
	}
	var ids atomic.Int64
	parent.local = chatsvc.New(chatsvc.Options{Store: f.store, Sessions: f.store, Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, id string) (chatsvc.ConversationRows, error) {
		r, err := f.store.LoadConversationSnapshot(ctx, id)
		return chatsvc.ConversationRows{Conversation: r.Conversation, Turns: r.Turns, Messages: r.Messages, Activities: r.Activities}, err
	}), Drivers: coordinationRecoveryDriver{parent.conv}, Now: time.Now, NewID: func() string { return fmt.Sprintf("extra-native-%d", ids.Add(1)) }, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() {
		if parent.planner != "" {
			_ = parent.local.Stop(context.Background(), parent.planner)
		}
	})
	h := &extraCoordinationHarness{coordinationRecoveryHarness: parent, taskKey: before.Tasks[0].TaskKey}
	f.s.chat = h
	f.s.stepTimeout = 5 * time.Second
	f.s.runBackground = func(func()) {}
	for range 180 {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), parent.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if x.PlannerRuntime != nil && len(x.PlannerRuntime.Decisions) == 3 {
			last := x.PlannerRuntime.Decisions[2]
			if last.Outcome != "LIMIT_REACHED" || len(x.PlannerRuntime.Requests) != 2 {
				t.Fatalf("wrong stop: %+v", x.PlannerRuntime)
			}
			return f, h, x
		}
		_, _, err = f.s.advanceComplexStandardExecution(context.Background(), parent.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
	}
	x, _, _ := f.store.GetClearDevComplexExecution(context.Background(), parent.requirementID)
	t.Fatalf("did not reach third coordination limit: phase=%v history=%+v tasks=%+v", x.Run, x.PlannerRuntime, x.Tasks)
	return nil, nil, core.ComplexExecutionSnapshot{}
}

func extraCoordinationInput(t *testing.T, f *projectPlanningFixture, h *extraCoordinationHarness) WorkflowRecoveryInput {
	t.Helper()
	view, err := f.s.GetWorkflowRecovery(context.Background(), h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range view.Options {
		if o.Action == core.RecoveryRequestExtraCoordination && o.UnavailableReason == "" {
			return WorkflowRecoveryInput{RequestID: "one-extra-coordination", ExecutionRunID: view.ExecutionRunID, Action: o.Action, TargetID: o.TargetID, Supplement: "Ask the original Planner to judge the exact current observation; do not add any task or revision budget."}
		}
	}
	t.Fatalf("missing native extra-coordination request: %+v", view.Options)
	return WorkflowRecoveryInput{}
}

func extraCoordinationRequest(t *testing.T, f *projectPlanningFixture) core.HumanDecisionRequest {
	t.Helper()
	requests, err := f.store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		if request.DecisionKind == core.HumanDecisionKindExtraCoordination {
			return request
		}
	}
	t.Fatal("missing native decision")
	return core.HumanDecisionRequest{}
}

func TestExtraCoordinationApprovalOpensExactlyOneThirdRequest(t *testing.T) {
	ctx := context.Background()
	f, h, before := extraCoordinationFixture(t)
	input := extraCoordinationInput(t, f, h)
	for range 2 {
		if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
			t.Fatal(err)
		}
	}
	request := extraCoordinationRequest(t, f)
	pending, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.PlannerRuntime.Requests) != 2 || len(pending.PlannerRuntime.ExtraCoordinationGrants) != 0 || !reflect.DeepEqual(pending.Exception.Budgets, before.Exception.Budgets) {
		t.Fatal("intent granted work or changed budgets")
	}
	if _, _, err := f.store.PrepareClearDevPlannerRuntime(ctx, before.PlannerRuntime.Events[2].ID, f.s.now()); err != nil {
		t.Fatal(err)
	}
	h.conv.mu.Lock()
	sent := len(h.conv.sent)
	h.conv.mu.Unlock()
	if sent != 2 {
		t.Fatal("unapproved third send", sent)
	}
	result := coordinationRepairResult(t, f, request, core.HumanDecisionApprove)
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
		t.Fatal("approve", err)
	}
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err == nil {
		t.Fatal("consumed native approval nonce was accepted twice")
	}
	granted, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(granted.PlannerRuntime.ExtraCoordinationGrants) != 1 || !reflect.DeepEqual(granted.PlannerRuntime.Decisions, before.PlannerRuntime.Decisions) || !reflect.DeepEqual(granted.Exception.Budgets, before.Exception.Budgets) || !reflect.DeepEqual(granted.Tasks, before.Tasks) {
		t.Fatal("grant rewrote source or task budgets")
	}
	if blocked, reason := core.PlannerRuntimeBarrier(granted); !blocked || reason != core.ReasonPlannerRuntimePending {
		t.Fatal("grant treated as a completed coordination", blocked, reason)
	}
	// Reopen the production store before creating the third request; no in-memory
	// approval or reset is required, and the original evidence remains present.
	reopened, err := sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	third, created, err := reopened.PrepareClearDevPlannerRuntime(ctx, before.PlannerRuntime.Events[2].ID, f.s.now())
	if err != nil || !created || third.Ordinal != 3 || third.AOSessionID != before.PlannerRuntime.Requests[1].AOSessionID {
		t.Fatal("prepare exact third", third, created, err)
	}
	if again, created, err := reopened.PrepareClearDevPlannerRuntime(ctx, third.EventID, f.s.now()); err != nil || created || again.AgentStepID != third.AgentStepID {
		t.Fatal("request replay", created, err)
	}
	if !strings.Contains(third.Prompt, "第 3/3 轮") || !strings.Contains(third.Prompt, request.ID) {
		t.Fatal("third prompt hides its bounded authority")
	}
	planning, _, err := f.store.GetClearDevComplexPlanning(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	var step core.AgentStep
	for _, item := range planning.AgentSteps {
		if item.ID == third.AgentStepID {
			step = item
		}
	}
	attempt, err := f.s.ensureAgentAttempt(ctx, h.requirementID, core.AgentStepCategoryComplexPlanning, step, third.AOSessionID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := f.store.GetSession(ctx, h.planner)
	if err != nil {
		t.Fatal(err)
	}
	changedIdentity := original
	changedIdentity.Metadata.ProviderConversationID = "changed-after-approval"
	if err := f.store.UpdateSession(ctx, changedIdentity); err != nil {
		t.Fatal(err)
	}
	if _, registered, err := f.store.ValidateClearDevExtraCoordinationBeforeSend(ctx, h.requirementID, third.AgentStepID); err == nil || !registered {
		t.Fatal("late native drift reached the sender", registered, err)
	}
	boundary := core.AgentAttemptEvent{ID: attempt.ID + ":late-boundary", AttemptID: attempt.ID, Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: attempt.ClientMessageID, PromptSHA256: attempt.PromptSHA256, FailureCategory: domain.AgentFailureDeliveryUnknown, ErrorSummary: "delivery unknown", RecordedAt: f.s.now()}
	if _, err := f.store.ReserveClearDevAgentMessage(ctx, core.ReserveAgentMessageCommand{Attempt: attempt, Source: core.AgentMessageOriginal, Boundary: boundary}); err == nil || !strings.Contains(err.Error(), "extra coordination message") {
		t.Fatal("SQLite did not independently fence changed native identity", err)
	}
	if err := f.store.UpdateSession(ctx, original); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		x, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.PlannerRuntime.ExtraCoordinationDecisions) == 1 {
			if x.PlannerRuntime.ExtraCoordinationDecisions[0].Outcome != core.PlannerRuntimeContinue || len(x.PlannerRuntime.Requests) != 3 || !reflect.DeepEqual(x.PlannerRuntime.Decisions, before.PlannerRuntime.Decisions) || !reflect.DeepEqual(x.Exception.Budgets, before.Exception.Budgets) {
				t.Fatal("third result changed history or budgets", x.PlannerRuntime.ExtraCoordinationDecisions)
			}
			assertExtraCoordinationHistoryImmutable(t, f)
			h.conv.mu.Lock()
			defer h.conv.mu.Unlock()
			if len(h.conv.sent) != 3 {
				t.Fatal("third request was not sent exactly once", len(h.conv.sent))
			}
			return
		}
		_, _, err = f.s.advanceComplexStandardExecution(ctx, h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
	}
	t.Fatal("approved original Planner did not settle third coordination")
}

func TestExtraCoordinationLaterRejectAndSQLGuards(t *testing.T) {
	ctx := context.Background()
	f, h, before := extraCoordinationFixture(t)
	input := extraCoordinationInput(t, f, h)
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	request := extraCoordinationRequest(t, f)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, query := range []string{
		`INSERT INTO cleardev_extra_coordination_grants(event_id,execution_run_id,decision_request_id,created_at) SELECT event_id,execution_run_id,decision_request_id,created_at FROM cleardev_extra_coordination_requests`,
		`INSERT INTO cleardev_planner_runtime_requests(event_id,execution_run_id,ordinal,agent_step_id,planner_role_binding_id,ao_session_id,context_json,context_sha256,prompt,created_at) SELECT event_id,execution_run_id,3,event_id||':planner',json_extract(binding_json,'$.plannerRoleBindingId'),json_extract(binding_json,'$.aoSessionId'),context_json,json_extract(binding_json,'$.contextSha256'),prompt,created_at FROM cleardev_extra_coordination_requests`,
		`UPDATE cleardev_extra_coordination_requests SET supplement='changed'`,
		`DELETE FROM cleardev_extra_coordination_requests`,
		`INSERT OR REPLACE INTO cleardev_extra_coordination_requests SELECT * FROM cleardev_extra_coordination_requests`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatal("unapproved or mutable authority accepted", query)
		}
	}
	later := coordinationRepairResult(t, f, request, core.HumanDecisionLater)
	if err := f.s.ApplyHumanDecisionResult(ctx, later); err != nil {
		t.Fatal(err)
	}
	current, found, err := f.store.GetClearDevHumanDecisionRequest(ctx, request.ID)
	if err != nil || !found || current.Status != "PENDING" {
		t.Fatal("Later became a grant", current, err)
	}
	now := f.s.now().UTC()
	offer, err := f.store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: request.ID, DesktopRunID: "extra-decline-new-desktop", Nonce: mustComplexNonce(t), IssuedAt: now, ExpiresAt: now.Add(core.HumanDecisionOfferTTL)})
	if err != nil {
		t.Fatal(err)
	}
	reject := core.HumanDecisionResult{ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind, BindingSchemaVersion: offer.BindingSchemaVersion, Binding: offer.Binding, ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionReject}
	if err := f.s.ApplyHumanDecisionResult(ctx, reject); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil || len(after.PlannerRuntime.ExtraCoordinationGrants) != 0 || !reflect.DeepEqual(before.PlannerRuntime, after.PlannerRuntime) || !reflect.DeepEqual(before.Exception.Budgets, after.Exception.Budgets) {
		t.Fatal("Later/rejection changed workflow", err)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range view.Options {
		if o.Action == core.RecoveryRequestExtraCoordination && o.UnavailableReason != "EXTRA_COORDINATION_DECISION_REJECTED" {
			t.Fatal("rejected authority was offered again", o)
		}
	}
	input.RequestID = "another-request"
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err == nil {
		t.Fatal("rejection silently created another decision")
	}
	h.conv.mu.Lock()
	defer h.conv.mu.Unlock()
	if len(h.conv.sent) != 2 {
		t.Fatal("Later/rejection sent an extra message", len(h.conv.sent))
	}
}

func TestExtraCoordinationRejectsChangedIdentityAndExpiredBinding(t *testing.T) {
	ctx := context.Background()
	f, h, before := extraCoordinationFixture(t)
	input := extraCoordinationInput(t, f, h)
	changed := input
	changed.TargetID += "changed"
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, changed); err == nil {
		t.Fatal("forged target admitted")
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	request := extraCoordinationRequest(t, f)
	result := coordinationRepairResult(t, f, request, core.HumanDecisionApprove)
	forged := result
	var b core.ExtraCoordinationBinding
	if err := json.Unmarshal(result.Binding, &b); err != nil {
		t.Fatal(err)
	}
	b.Ordinal = 4
	forged.Binding, _ = json.Marshal(b)
	if err := f.s.ApplyHumanDecisionResult(ctx, forged); err == nil {
		t.Fatal("fourth round grant accepted")
	}
	planner, _, err := f.store.GetSession(ctx, h.planner)
	if err != nil {
		t.Fatal(err)
	}
	original := planner
	planner.Metadata.ProviderConversationID = "different-native-thread"
	if err := f.store.UpdateSession(ctx, planner); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err == nil {
		t.Fatal("approval followed a different native Planner")
	}
	if err := f.store.UpdateSession(ctx, original); err != nil {
		t.Fatal(err)
	}
	// An exact but expired native offer is still not authority.
	if err := f.store.SettleClearDevHumanDecision(ctx, result, f.s.now().Add(core.HumanDecisionOfferTTL+time.Second)); err == nil {
		t.Fatal("expired approval accepted")
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil || len(after.PlannerRuntime.ExtraCoordinationGrants) != 0 || !reflect.DeepEqual(after.PlannerRuntime.Decisions, before.PlannerRuntime.Decisions) || !reflect.DeepEqual(after.Exception.Budgets, before.Exception.Budgets) {
		t.Fatal("invalid approval changed facts", err)
	}
}

func TestExtraCoordinationConcurrentGrantAndFailureStayBounded(t *testing.T) {
	ctx := context.Background()
	started := time.Now()
	f, h, before := extraCoordinationFixture(t)
	t.Logf("production-shaped fixture ready after %s", time.Since(started))
	at := f.s.now().UTC()
	state, err := f.store.ReadClearDevExtraCoordination(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	var wg sync.WaitGroup
	var successes atomic.Int32
	for _, store := range []*sqlite.Store{f.store, reopened} {
		wg.Add(1)
		go func(store *sqlite.Store) {
			defer wg.Done()
			if store.RequestClearDevExtraCoordination(ctx, "concurrent-extra", "Request only the exact third coordination", state.Binding, at) == nil {
				successes.Add(1)
			}
		}(store)
	}
	wg.Wait()
	if successes.Load() == 0 {
		t.Fatal("neither request registered")
	}
	t.Logf("concurrent native-decision request settled after %s", time.Since(started))
	request := extraCoordinationRequest(t, f)
	result := coordinationRepairResult(t, f, request, core.HumanDecisionApprove)
	successes.Store(0)
	for _, store := range []*sqlite.Store{f.store, reopened} {
		wg.Add(1)
		go func(store *sqlite.Store) {
			defer wg.Done()
			if store.SettleClearDevHumanDecision(ctx, result, at) == nil {
				successes.Add(1)
			}
		}(store)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("native nonce must settle once", successes.Load())
	}
	var prepares atomic.Int32
	for _, store := range []*sqlite.Store{f.store, reopened} {
		wg.Add(1)
		go func(store *sqlite.Store) {
			defer wg.Done()
			_, created, err := store.PrepareClearDevPlannerRuntime(ctx, before.PlannerRuntime.Events[2].ID, at)
			if err == nil && created {
				prepares.Add(1)
			}
		}(store)
	}
	wg.Wait()
	if prepares.Load() != 1 {
		t.Fatal("third request must be created once", prepares.Load())
	}
	t.Logf("concurrent approval and unique third request settled after %s", time.Since(started))
	h.conv.mu.Lock()
	h.conv.fail = true
	h.conv.mu.Unlock()
	for range 15 {
		x, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.PlannerRuntime.ExtraCoordinationDecisions) == 1 {
			if x.PlannerRuntime.ExtraCoordinationDecisions[0].Outcome != "STOP" || len(x.PlannerRuntime.Requests) != 3 || len(x.PlannerRuntime.ExtraCoordinationGrants) != 1 || !reflect.DeepEqual(before.PlannerRuntime.Decisions, x.PlannerRuntime.Decisions) || !reflect.DeepEqual(before.Exception.Budgets, x.Exception.Budgets) {
				t.Fatal("failed extra coordination erased history or refunded", x.PlannerRuntime)
			}
			view, err := f.s.GetWorkflowRecovery(ctx, h.requirementID)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range view.Options {
				if o.Action == core.RecoveryRequestExtraCoordination {
					t.Fatal("failed third offered a fourth", o)
				}
			}
			return
		}
		_, _, err = f.s.advanceComplexStandardExecution(ctx, h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
	}
	t.Fatal("failed extra coordination was not retained")
}
