package cleardev

import (
	"context"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestStoppedCheckRecoveryRevalidatesLateSourceBeforeActualCheck(t *testing.T) {
	ctx := context.Background()
	f, h, _ := stoppedCheckFixture(t)
	input := stoppedCheckInput(t, f, h)
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	request := stoppedCheckRequest(t, f)
	result := coordinationRepairResult(t, f, request, core.HumanDecisionApprove)
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	state, err := f.store.ReadClearDevStoppedCheckRecovery(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	b := state.Binding
	original, _, err := f.store.GetSession(ctx, domain.SessionID(b.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.Metadata.ProviderConversationID = "changed-after-real-grant"
	if err := f.store.UpdateSession(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.checkStoppedCheckBeforeRun(ctx, h.requirementID, b.RetryCheckRunID); err == nil {
		t.Fatal("changed native identity reached check executor")
	}
	if _, err := f.store.StartClearDevComplexExecutionCheckRun(ctx, b.RetryCheckRunID, f.s.now()); err == nil {
		t.Fatal("SQL start accepted a stale native binding")
	}
	if err := f.store.UpdateSession(ctx, original); err != nil {
		t.Fatal(err)
	}
	h.receiptSettled = false
	if _, err := f.s.checkStoppedCheckBeforeRun(ctx, h.requirementID, b.RetryCheckRunID); err == nil {
		t.Fatal("unknown original receipt reached executor")
	}
	h.receiptSettled = true
	if _, err := f.s.checkStoppedCheckBeforeRun(ctx, h.requirementID, b.RetryCheckRunID); err != nil {
		t.Fatal("same settled source rejected", err)
	}
	x, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range x.CheckRuns {
		if c.ID == b.RetryCheckRunID {
			found = true
			if c.Status != core.ComplexExecutionCheckRunPending {
				t.Fatal("invalid start changed check status")
			}
		}
	}
	if !found {
		t.Fatal("reserved retry missing")
	}
	for _, call := range h.checkRequests {
		if call.RunID == b.RetryCheckRunID {
			t.Fatal("admission check itself executed external work")
		}
	}
}
