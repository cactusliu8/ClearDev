package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// Only provider answers, Git, checks and trial execution are doubles. The STOP,
// native authority transaction, same-candidate successor and normal task/final
// review orchestration use production SQLite and service code. Not ACC-005.
type stoppedCheckHarness struct {
	*extraCoordinationHarness
	failures        int
	failedID        string
	receiptSettled  bool
	businessFailure bool
}

func (h *stoppedCheckHarness) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	if h.failures > 0 {
		h.failures--
		h.failedID = request.RunID
		h.checkRequests = append(h.checkRequests, request)
		output := "explicit fixture: native checker monitor could not start; no complete command result"
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, CandidateSHA: request.CandidateSHA, OutputSummary: output, OutputSHA256: coreDigest([]byte(output))}, errors.New(output)
	}
	result, err := h.projectExecutionFlowHarness.RunCandidateCheck(ctx, request)
	if h.businessFailure && err == nil {
		result.Outcome, result.ExitCode = ports.ClearDevCheckFail, 1
		result.OutputSummary = "explicit fixture: business assertion failed"
		result.OutputSHA256 = coreDigest([]byte(result.OutputSummary))
	}
	return result, err
}
func (h *stoppedCheckHarness) CanRetryWorkflowCheck(_ context.Context, request ports.ClearDevCheckRequest) (bool, error) {
	return h.receiptSettled && request.RunID == h.failedID, nil
}
func (h *stoppedCheckHarness) CanRetryProjectDependencyCheck(context.Context, ports.ClearDevCheckRequest) (bool, error) {
	return false, nil
}

func stoppedCheckFixture(t *testing.T) (*projectPlanningFixture, *stoppedCheckHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, base, before := settledProjectFailure(t, "BLOCKED")
	// The existing Git/model double predates native Builder identities. Supply
	// its explicit stable provider handle, without weakening production admission.
	for _, role := range before.RoleBindings {
		if role.Role == core.StandardRoleBuilder {
			rec, _, err := f.store.GetSession(context.Background(), domain.SessionID(role.AOSessionID))
			if err != nil {
				t.Fatal(err)
			}
			rec.Metadata.ProviderConversationID = "stopped-check-builder-native"
			if err := f.store.UpdateSession(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
		}
	}

	base.candidateSHAs = append(base.candidateSHAs, forty("d"))
	base.finalVerdict = "PASS"
	parent := &coordinationRecoveryHarness{projectPlannerHarness: &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}}
	parent.conv = &coordinationRecoveryConversation{events: make(chan ports.ChatEvent, 64), reply: func() (string, error) {
		return `{"schemaVersion":1,"kind":"PLANNER_RUNTIME_COORDINATION","decision":"STOP","summary":"The engineering contract is complete. Only the ended infrastructure check failed; it requires a separate exact human recovery, not another Builder or Planner message.","questions":[],"amendments":[]}`, nil
	}}
	var ids atomic.Int64
	parent.local = chatsvc.New(chatsvc.Options{Store: f.store, Sessions: f.store, Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, id string) (chatsvc.ConversationRows, error) {
		r, err := f.store.LoadConversationSnapshot(ctx, id)
		return chatsvc.ConversationRows{Conversation: r.Conversation, Turns: r.Turns, Messages: r.Messages, Activities: r.Activities}, err
	}), Drivers: coordinationRecoveryDriver{parent.conv}, Now: time.Now, NewID: func() string { return fmt.Sprintf("stopped-check-native-%d", ids.Add(1)) }, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() {
		if parent.planner != "" {
			_ = parent.local.Stop(context.Background(), parent.planner)
		}
	})
	h := &stoppedCheckHarness{extraCoordinationHarness: &extraCoordinationHarness{coordinationRecoveryHarness: parent, taskKey: before.Tasks[0].TaskKey}, failures: 1, receiptSettled: true}
	f.s.chat, f.s.checks = h, h
	f.s.stepTimeout = 5 * time.Second
	f.s.runBackground = func(func()) {}
	for range 110 {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), parent.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if x.PlannerRuntime != nil && len(x.PlannerRuntime.Decisions) == 1 {
			d := x.PlannerRuntime.Decisions[0]
			if d.Source != "PLANNER" || d.Outcome != "STOP" || h.failedID == "" {
				t.Fatal("wrong stopped check fixture", d, h.failedID)
			}
			return f, h, x
		}
		_, _, err = f.s.advanceComplexStandardExecution(context.Background(), parent.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
	}
	t.Fatal("did not reach Planner STOP after an ended unavailable checker")
	return nil, nil, core.ComplexExecutionSnapshot{}
}

func stoppedCheckInput(t *testing.T, f *projectPlanningFixture, h *stoppedCheckHarness) WorkflowRecoveryInput {
	t.Helper()
	v, err := f.s.GetWorkflowRecovery(context.Background(), h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range v.Options {
		if o.Action == core.RecoveryRequestStoppedCheck && o.UnavailableReason == "" {
			return WorkflowRecoveryInput{RequestID: "one-stopped-check", ExecutionRunID: v.ExecutionRunID, Action: o.Action, TargetID: o.TargetID, Supplement: "The original environment action is confirmed ended; request one exact same-candidate check without changing quotas or source."}
		}
	}
	state, _ := f.store.ReadClearDevStoppedCheckRecovery(context.Background(), h.requirementID)
	x, _, _ := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
	for _, role := range x.RoleBindings {
		if role.Role == core.StandardRoleBuilder {
			rec, _, _ := f.store.GetSession(context.Background(), domain.SessionID(role.AOSessionID))
			t.Logf("builder role=%+v session id=%s harness=%s mode=%s permission=%s native=%s activity=%s key=%s fingerprint=%s workspace=%s", role, rec.ID, rec.Harness, rec.Mode, rec.PermissionMode, rec.Metadata.ProviderConversationID, rec.Activity.State, rec.CreationIdempotencyKey, rec.CreationRequestFingerprint, rec.Metadata.WorkspacePath)
		}
	}

	t.Fatalf("no safe stopped-check option: options=%+v stored=%+v", v.Options, state)
	return WorkflowRecoveryInput{}
}

func stoppedCheckRequest(t *testing.T, f *projectPlanningFixture) core.HumanDecisionRequest {
	t.Helper()
	rows, err := f.store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.DecisionKind == core.HumanDecisionKindStoppedCheckRecovery {
			return r
		}
	}
	t.Fatal("missing exact native check recovery request")
	return core.HumanDecisionRequest{}
}

func TestStoppedCheckRecoveryNativeApprovalKeepsCounterAndCompletesSameCandidate(t *testing.T) {
	ctx := context.Background()
	f, h, before := stoppedCheckFixture(t)
	input := stoppedCheckInput(t, f, h)
	for range 2 {
		if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
			t.Fatal(err)
		}
	}
	req := stoppedCheckRequest(t, f)
	pending, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pending, before) {
		t.Fatal("request/read granted execution or changed prior facts")
	}
	result := coordinationRepairResult(t, f, req, core.HumanDecisionApprove)
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
		t.Fatal("approve exact checker", err)
	}
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err == nil {
		t.Fatal("replayed consumed native approval")
	}
	granted, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(granted.PlannerRuntime.CheckRecoveries) != 1 || !reflect.DeepEqual(granted.Exception.Budgets, before.Exception.Budgets) || !reflect.DeepEqual(granted.PlannerRuntime.Decisions, before.PlannerRuntime.Decisions) || granted.Tasks[0].ReworkCount != before.Tasks[0].ReworkCount {
		t.Fatal("grant erased STOP/counters or changed role budgets")
	}
	recovery := granted.PlannerRuntime.CheckRecoveries[0]
	if len(granted.WorkflowRecoveries) != 1 || granted.WorkflowRecoveries[0].OriginalStatus != "REWORK" || granted.WorkflowRecoveries[0].SuccessorID != recovery.RetryCheckRunID {
		t.Fatal("original attempt not preserved in exact retry", granted.WorkflowRecoveries)
	}
	if _, registered, err := f.store.ValidateClearDevStoppedCheckBeforeRun(ctx, h.requirementID, recovery.RetryCheckRunID); err != nil || !registered {
		t.Fatal("validate granted source", registered, err)
	}

	// Restart the actual Store and service, not just a read model. The retry is
	// already reserved, so the new scheduler must reuse it without another grant.
	if err := h.local.Stop(ctx, h.planner); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.service()
	h.store = f.store
	h.service = f.s
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h
	f.s.finalReviews, f.s.resultPreview = f.store, h.trial
	f.s.runBackground = func(func()) {}
	after := driveProjectFlow(t, f, h.requirementID, false)
	if after.Run.CompletedAt == nil || after.Tasks[0].ReworkCount != before.Tasks[0].ReworkCount || len(after.Dispatches) != len(before.Dispatches) || len(after.PlannerRuntime.Requests) != len(before.PlannerRuntime.Requests) || !reflect.DeepEqual(after.PlannerRuntime.Decisions, before.PlannerRuntime.Decisions) {
		t.Fatal("continuation erased history, sent new work or failed to finish")
	}
	for _, old := range before.CheckRuns {
		for _, now := range after.CheckRuns {
			if old.ID == now.ID && !reflect.DeepEqual(old, now) {
				t.Fatal("original check rewritten", old.ID)
			}
		}
	}
	retries := 0
	for _, call := range h.checkRequests {
		if call.RunID == recovery.RetryCheckRunID {
			retries++
			if call.CandidateSHA != recovery.CandidateSHA {
				t.Fatal("new candidate used original grant")
			}
		}
	}
	if retries != 1 {
		t.Fatal("same candidate check calls", retries)
	}
	for _, budget := range before.Exception.Budgets {
		for _, now := range after.Exception.Budgets {
			if now.ID != budget.ID {
				continue
			}
			if now.MaxTurns != budget.MaxTurns || now.AuthorizedExtraTurns != budget.AuthorizedExtraTurns || now.MaxReworkCount != budget.MaxReworkCount || (now.RoleKind == core.ComplexExceptionBudgetBuilder && now.UsedTurns != budget.UsedTurns) {
				t.Fatal("role budget was widened or Builder re-sent")
			}
		}
	}
	if !strings.Contains(req.DisplayJSON, "累计返工数保持") {
		t.Fatal("native display hid spent counter")
	}
	// The independent task/final-review doubles must actually have received work;
	// a native authorization alone must never stand in for their PASS evidence.
	if len(after.Reviews) <= len(before.Reviews) || after.FinalReview == nil || after.FinalReview.Verdict != "PASS" {
		t.Fatal("check approval bypassed independent review")
	}
	raw, _ := json.Marshal(recovery)
	t.Logf("same-candidate recovery=%s", raw)
}
