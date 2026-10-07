package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// The provider and Git are explicit doubles; recovery, failed attempts and
// budget records use real SQLite. These reads must never try to fix the world.
func TestWorkflowBlockerDiagnosisExplainsInvalidBuilderWithoutSending(t *testing.T) {
	f, h, id, before := invalidBuilderWorkflowFixture(t)
	ctx := context.Background()
	calls := len(h.relays)
	attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		view, err := f.s.GetWorkflowRecovery(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		probe := decodeBlockerDiagnosis(t, view)
		if len(probe.Issues) == 0 {
			t.Fatal("invalid result has an action but no explanation of the stop")
		}
		found := false
		for _, issue := range probe.Issues {
			found = found || issue.ReasonCode == "BUILDER_RESULT_INVALID" && issue.Category == "FORMAT" && issue.Relationship == "CURRENT"
		}
		if !found {
			t.Fatalf("missing exact current invalid-result diagnosis: %+v", probe.Issues)
		}
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	afterAttempts, err := f.store.ListClearDevAgentStepAttempts(ctx, id)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(attempts, afterAttempts) || calls != len(h.relays) {
		t.Fatal("diagnosis changed the failure, attempts, budget or provider messages")
	}
}

func TestWorkflowBlockerDiagnosisExplainsUnsentExitedBuilder(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &workflowBlockedBuilderHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), block: true}
	f.s.chat = h
	ctx, id := context.Background(), child.Requirement.ID
	if _, err := f.s.StartProjectExecution(ctx, id, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, id)
	binding, _ := complexExecutionBindingByID(before, before.Run.BuilderRoleBindingID)
	rec, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	rec.Activity.State = domain.ActivityExited
	rec.Metadata.ProviderConversationID = "original-native-conversation"
	if err := f.store.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	restores := 0
	f.s.restoreOriginalAgentSession = func(context.Context, domain.SessionID) (string, error) {
		restores++
		return "", errors.New("explicit test native environment failure")
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: "create-unsent-fixture", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: before.Dispatches[0].ID, Supplement: "Preserve the original task."}); err != nil {
		t.Fatal(err)
	}
	stopped := stoppedWorkflow(t, f, id)
	calls, restoreCalls := len(h.relays), restores
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Options) != 1 || view.Options[0].Summary != "" || view.Options[0].Reason != "BUILDER_SPAWN_FAILED" {
		t.Fatalf("fixture must reproduce the empty-summary stop: %+v", view.Options)
	}
	probe := decodeBlockerDiagnosis(t, view)
	found := false
	for _, issue := range probe.Issues {
		if issue.ReasonCode != "BUILDER_SPAWN_FAILED" || issue.Relationship != "CURRENT" {
			continue
		}
		facts := map[string]string{}
		for _, e := range issue.Evidence {
			facts[e.Kind] = e.Value
		}
		found = issue.Category == "SESSION" && facts["SESSION_STATE"] == "exited" && facts["STEP_STATUS"] == "PENDING" && facts["CONFIRMED_MESSAGES"] == "0"
	}
	if !found {
		t.Fatalf("no current session/unsent evidence for an empty-summary stop: %+v", probe.Issues)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
	if err != nil || !reflect.DeepEqual(stopped, after) || len(h.relays) != calls || restores != restoreCalls {
		t.Fatal("read-only diagnosis tried session restoration or changed a workflow")
	}
}

type blockerDiagnosisProbe struct {
	Phase  string `json:"phase"`
	Issues []struct {
		ReasonCode   string `json:"reasonCode"`
		Category     string `json:"category"`
		Relationship string `json:"relationship"`
		Evidence     []struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		} `json:"evidence"`
	} `json:"issues"`
}

func decodeBlockerDiagnosis(t *testing.T, view core.WorkflowRecoveryView) blockerDiagnosisProbe {
	t.Helper()
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Diagnosis *blockerDiagnosisProbe `json:"diagnosis"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Diagnosis == nil {
		t.Fatal("recovery response has no current-state, cause or evidence diagnosis")
	}
	return *out.Diagnosis
}
