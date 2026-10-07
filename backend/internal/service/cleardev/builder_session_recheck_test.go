package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SQLite and workflow mutations are real. Provider, workspace and native
// restoration are explicit doubles; no live project is repaired by this test.
func newBuilderSessionRecheckFixture(t *testing.T) (*projectPlanningFixture, *workflowBlockedBuilderHarness, string, core.ComplexExecutionSnapshot, domain.SessionRecord) {
	t.Helper()
	ctx := context.Background()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	f.s.stepTimeout = 10 * time.Second
	h := &workflowBlockedBuilderHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), block: true}
	f.s.chat = h
	id := child.Requirement.ID
	if _, err := f.s.StartProjectExecution(ctx, id, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, id)
	binding, _ := complexExecutionBindingByID(before, before.Run.BuilderRoleBindingID)
	record, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	record.Activity.State = domain.ActivityExited
	record.Metadata.ProviderConversationID = "original-native-recheck-fixture"
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	f.s.restoreOriginalAgentSession = func(context.Context, domain.SessionID) (string, error) {
		return "", ports.ErrChatProviderUnavailable
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: "prepare-unsent-builder", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: before.Dispatches[0].ID, Supplement: "Keep original work and address the outstanding task."}); err != nil {
		t.Fatal(err)
	}
	stopped := stoppedWorkflow(t, f, id)
	return f, h, id, stopped, record
}

func builderSessionRecheckInput(t *testing.T, f *projectPlanningFixture, id, request string) WorkflowRecoveryInput {
	t.Helper()
	view, err := f.s.GetWorkflowRecovery(context.Background(), id)
	if err != nil || len(view.Options) != 1 || view.Options[0].Action != core.RecoveryRetryBuilderSession || view.Options[0].UnavailableReason != "" {
		t.Fatalf("original unsent recovery unavailable: %+v %v", view.Options, err)
	}
	return WorkflowRecoveryInput{RequestID: request, ExecutionRunID: view.ExecutionRunID, Action: view.Options[0].Action, TargetID: view.Options[0].TargetID}
}

func TestBuilderSessionRecheckAllowsEmptyAndContinuesOriginalStep(t *testing.T) {
	f, h, id, before, original := newBuilderSessionRecheckFixture(t)
	ctx := context.Background()
	restores := 0
	f.s.restoreOriginalAgentSession = func(ctx context.Context, sid domain.SessionID) (string, error) {
		restores++
		if sid != original.ID {
			t.Fatal("not the original Builder")
		}
		record, _, err := f.store.GetSession(ctx, sid)
		if err != nil {
			return "", err
		}
		record.Activity.State = domain.ActivityIdle
		return "native", f.store.UpdateSession(ctx, record)
	}
	input := builderSessionRecheckInput(t, f, id, "recheck-empty-context")
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatalf("checking a known technical stop must not require a claim that it is repaired: %v", err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if restores != 1 || len(after.Dispatches) != len(before.Dispatches) || after.Run.CompletedAt == nil {
		t.Fatalf("did not continue the original pending dispatch: restores=%d dispatches=%d completed=%v", restores, len(after.Dispatches), after.Run.CompletedAt)
	}
	if !reflect.DeepEqual(before.Dispatches[0], after.Dispatches[0]) || before.Dispatches[1].AgentStepID != after.Dispatches[1].AgentStepID {
		t.Fatal("recheck discarded original result or created another pending step")
	}
	messages := len(h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatalf("empty request replay lost canonical identity: %v", err)
	}
	if restores != 1 || len(h.relays) != messages {
		t.Fatal("replaying a saved request repeated restoration or sending")
	}
}

func TestBuilderSessionRecheckPersistsRestoreFailure(t *testing.T) {
	f, h, id, before, _ := newBuilderSessionRecheckFixture(t)
	ctx := context.Background()
	input := builderSessionRecheckInput(t, f, id, "recheck-native-failure")
	// Non-empty here lets the old implementation reach its bool-only failure.
	input.Supplement = "Check the original worker; no environment repair is claimed."
	calls := len(h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	after := stoppedWorkflow(t, f, id)
	if len(after.Dispatches) != len(before.Dispatches) || len(h.relays) != calls {
		t.Fatal("failed recheck sent a message or consumed another dispatch")
	}
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Checks []struct {
			RecoveryID string `json:"recoveryId"`
			Stage      string `json:"stage"`
			Outcome    string `json:"outcome"`
			Reason     string `json:"reasonCode"`
		} `json:"builderSessionChecks"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range probe.Checks {
		found = found || check.RecoveryID == input.RequestID && check.Stage == "RESTORE" && check.Outcome == "FAILED" && check.Reason == "PROVIDER_UNAVAILABLE"
	}
	if !found {
		t.Fatal("the exact restore stage and provider cause were lost behind BUILDER_SPAWN_FAILED")
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(h.relays) != calls {
		t.Fatal("failed registration replay sent a task")
	}
}
