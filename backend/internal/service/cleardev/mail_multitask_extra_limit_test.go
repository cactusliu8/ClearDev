package cleardev_test

import (
	"context"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestMailV2LaterTaskCannotReceiveSecondExecutionWideExtra(t *testing.T) {
	f := newMailParallelBoundaryFixture(t)
	failMailCandidates(f.harness, "frontend/app.js", 3)
	firstFailures := f.harness.checkFailureFor
	f.harness.checkFailureFor = func(request ports.ClearDevCheckRequest) bool {
		if firstFailures(request) {
			return true
		}
		workspace := f.harness.workspaces[request.WorkspacePath]
		return workspace != nil && len(workspace.paths) != 0 && workspace.paths[0].Path == "frontend/index.html"
	}
	execution := f.advance(t, true, nil)
	phase, _ := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionNeedsHuman {
		t.Fatalf("first task did not exhaust its budget: %s", phase)
	}
	approveMailExtraForFixture(t, f)
	execution = f.advance(t, false, nil)
	if execution.Integration != nil || execution.FinalReview != nil {
		t.Fatal("failed later task was treated as whole-requirement success")
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), execution.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	extra, laterNormal := 0, 0
	later := execution.Tasks[2].ID
	for _, slot := range slots {
		if slot.Kind == core.MailAttemptHumanExtra {
			extra++
		}
		if slot.TaskID == later && slot.Kind == core.MailAttemptDevelopment {
			laterNormal++
		}
	}
	if extra != 1 || laterNormal != 3 {
		t.Fatalf("global extra limit or task-local normal budget changed: extra=%d later-normal=%d", extra, laterNormal)
	}
	pending, err := f.store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range pending {
		if request.DevelopmentRequirementID == f.prep.RequirementID && request.DecisionKind == core.HumanDecisionKindExtraMailAttempt {
			t.Fatal("a second task was offered another execution-wide extra grant")
		}
	}
}
