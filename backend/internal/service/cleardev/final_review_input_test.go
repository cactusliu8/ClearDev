package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

type finalInputHarness struct {
	*projectExecutionFlowHarness
	local *chatsvc.Service
	conv  *coordinationRecoveryConversation
	owner domain.SessionID
}

func (h *finalInputHarness) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	final := strings.HasPrefix(prompt, "你是独立的 ClearDev Requirement-level Final Reviewer")
	if !final {
		return h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, id, prompt, key)
	}
	if h.owner != "" && h.owner != id {
		return h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, id, prompt, key)
	}
	if h.owner == "" {
		h.owner = id
		rec, _, err := h.store.GetSession(ctx, id)
		if err != nil {
			return "", err
		}
		_, err = h.local.StartChat(ctx, chatsvc.StartRequest{SessionID: id, ProjectID: rec.ProjectID, Kind: rec.Kind, Harness: rec.Harness, Permissions: rec.PermissionMode, WorkspacePath: rec.Metadata.WorkspacePath, ProviderConversationID: rec.Metadata.ProviderConversationID, ControllerReady: func(started chatsvc.StartResult) (chatsvc.ControllerCommit, error) {
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
func (h *finalInputHarness) Snapshot(ctx context.Context, id domain.SessionID) (chatsvc.Snapshot, error) {
	if id == h.owner {
		return h.local.Snapshot(ctx, id)
	}
	return h.projectExecutionFlowHarness.Snapshot(ctx, id)
}

func finalInputFixture(t *testing.T, raw string, before ...ports.ChatEvent) (*projectPlanningFixture, *finalInputHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	base := attachProjectFlow(f, preparer)
	h := &finalInputHarness{projectExecutionFlowHarness: base, conv: &coordinationRecoveryConversation{events: make(chan ports.ChatEvent, 32), fail: true, rawFailure: raw, beforeFailure: before}}
	var ids atomic.Int64
	h.local = chatsvc.New(chatsvc.Options{Store: f.store, Sessions: f.store, Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, id string) (chatsvc.ConversationRows, error) {
		r, e := f.store.LoadConversationSnapshot(ctx, id)
		return chatsvc.ConversationRows{Conversation: r.Conversation, Turns: r.Turns, Messages: r.Messages, Activities: r.Activities}, e
	}), Drivers: coordinationRecoveryDriver{h.conv}, Now: time.Now, NewID: func() string { return fmt.Sprintf("final-input-native-%d", ids.Add(1)) }, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() {
		if h.owner != "" {
			_ = h.local.Stop(context.Background(), h.owner)
		}
	})
	f.s.chat = h
	f.s.stepTimeout = 3 * time.Second
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	var e core.ComplexExecutionSnapshot
	for range 80 {
		var err error
		e, _, err = f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		if e.FinalReview != nil && e.FinalReview.Status == "FAILED" {
			break
		}
		_, _, err = f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
	}
	if e.FinalReview == nil || e.FinalReview.Status != "FAILED" {
		t.Fatalf("missing native failure: %+v", e.FinalReview)
	}
	return f, h, e
}

const finalContextError = `{"code":-32603,"message":"Internal error: The input is longer than the model's context length invalid_request_error"}`

func TestFinalReviewInputRejectionRecoversWithoutRewritingOldReview(t *testing.T) {
	f, _, e := finalInputFixture(t, finalContextError)
	ctx := context.Background()
	old := *e.FinalReview
	known, err := f.store.FinalReviewInputRejected(ctx, old.ID)
	if err != nil || !known {
		t.Fatalf("proof=%v %v", known, err)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, e.Run.DevelopmentRequirementID)
	if err != nil {
		t.Fatal(err)
	}
	var option core.WorkflowRecoveryOption
	for _, o := range view.Options {
		if o.Action == core.RecoveryRetryStage {
			option = o
		}
	}
	if option.TargetID != old.ID || option.UnavailableReason != "" {
		t.Fatalf("option=%+v", option)
	}
	input := WorkflowRecoveryInput{RequestID: "final-input-recovery", ExecutionRunID: e.Run.ID, Action: core.RecoveryRetryStage, TargetID: old.ID, Supplement: "Use smaller initial prompt, retaining complete evidence."}
	if _, err = f.s.RequestWorkflowRecovery(ctx, e.Run.DevelopmentRequirementID, input); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.RequestWorkflowRecovery(ctx, e.Run.DevelopmentRequirementID, input); err != nil {
		t.Fatal(err)
	}
	after, _, readErr := f.store.GetClearDevComplexExecution(ctx, e.Run.DevelopmentRequirementID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if after.FinalReview == nil || after.FinalReview.ID == old.ID || len(after.WorkflowRecoveries) != 1 {
		t.Fatalf("recovery=%+v", after.FinalReview)
	}
	var packet core.RequirementFinalReviewPacket
	if err = json.Unmarshal([]byte(after.FinalReview.ReviewPacketJSON), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Recovery == nil || packet.Recovery.StepID != old.Step().ID || packet.Recovery.OriginalStatus != "FAILED" {
		t.Fatalf("lost original failure: %+v", packet.Recovery)
	}
	if known, err = f.store.FinalReviewInputRejected(ctx, old.ID); err != nil || !known {
		t.Fatalf("old evidence changed: %v %v", known, err)
	}
}
func TestFinalReviewInputGenericFailureStillUnavailable(t *testing.T) {
	f, _, e := finalInputFixture(t, `{"code":-32603,"message":"unclassified service error"}`)
	if known, err := f.store.FinalReviewInputRejected(context.Background(), e.FinalReview.ID); err != nil || known {
		t.Fatalf("generic failure recovered: %v %v", known, err)
	}
	view, err := f.s.GetWorkflowRecovery(context.Background(), e.Run.DevelopmentRequirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range view.Options {
		if o.Action == core.RecoveryRetryStage && o.UnavailableReason == "" {
			t.Fatal("generic failure exposed retry")
		}
	}
}

func TestFinalReviewInputOutputAndToolsPreventRecovery(t *testing.T) {
	for _, event := range []ports.ChatEvent{
		{Kind: ports.ChatEventMessageCompleted, ProviderItemID: "answer", Text: "A partial model answer"},
		{Kind: ports.ChatEventActivityStarted, ProviderItemID: "tool", ActivityKind: domain.ActivityKindCommand, ActivityStatus: domain.ActivityStatusRunning, Summary: "A tool was started"},
	} {
		t.Run(string(event.Kind), func(t *testing.T) {
			f, _, e := finalInputFixture(t, finalContextError, event)
			if known, err := f.store.FinalReviewInputRejected(context.Background(), e.FinalReview.ID); err != nil || known {
				t.Fatalf("unsafe recovery: %v %v", known, err)
			}
		})
	}
}
func TestFinalReviewInputChangedSessionPreventsRecovery(t *testing.T) {
	f, h, e := finalInputFixture(t, finalContextError)
	ctx := context.Background()
	record, _, err := f.store.GetSession(ctx, h.owner)
	if err != nil {
		t.Fatal(err)
	}
	record.Metadata.ProviderConversationID = "different-native-thread"
	if err = f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	if known, err := f.store.FinalReviewInputRejected(ctx, e.FinalReview.ID); err != nil || known {
		t.Fatalf("changed native identity accepted: %v %v", known, err)
	}
	if _, err = f.s.RequestWorkflowRecovery(ctx, e.Run.DevelopmentRequirementID, WorkflowRecoveryInput{RequestID: "changed", ExecutionRunID: e.Run.ID, Action: core.RecoveryRetryStage, TargetID: e.FinalReview.ID, Supplement: "invalid stale request"}); err == nil {
		t.Fatal("changed source submitted")
	}
}

func TestFinalReviewInputConcurrentRecoveryCreatesOneSuccessor(t *testing.T) {
	f, _, e := finalInputFixture(t, finalContextError)
	ctx := context.Background()
	result := make(chan error, 2)
	for i := range 2 {
		go func() {
			_, err := f.s.RequestWorkflowRecovery(ctx, e.Run.DevelopmentRequirementID, WorkflowRecoveryInput{RequestID: fmt.Sprintf("concurrent-input-%d", i), ExecutionRunID: e.Run.ID, Action: core.RecoveryRetryStage, TargetID: e.FinalReview.ID, Supplement: "Retry exact rejected input with complete on-demand evidence."})
			result <- err
		}()
	}
	successes := 0
	for range 2 {
		if <-result == nil {
			successes++
		}
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, e.Run.DevelopmentRequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if successes != 1 || len(after.WorkflowRecoveries) != 1 {
		t.Fatalf("successes=%d recoveries=%d", successes, len(after.WorkflowRecoveries))
	}
	if after.FinalReview.CandidateCommitSHA != e.FinalReview.CandidateCommitSHA || len(after.Dispatches) != len(e.Dispatches) {
		t.Fatal("recovery changed candidate or dispatched Builder")
	}
}
