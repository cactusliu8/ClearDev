package cleardev

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func runtimeProposal(t *testing.T, feasibility string, argv []string) string {
	t.Helper()
	var result core.ProductDiscoveryResult
	if err := json.Unmarshal([]byte(genericProjectReply("empty")), &result); err != nil {
		t.Fatal(err)
	}
	result.Stages[0].Feasibility = feasibility
	result.Stages[0].ExecutionBasis.Checks[0].Argv = argv
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestProjectRuntimeProposalPreservesHistoricalDecoding(t *testing.T) {
	raw := runtimeProposal(t, "SUPPORTED", []string{"sh", "-c", "test -f package.json"})
	if _, err := core.ParseProductDiscoveryResult([]byte(raw)); err != nil {
		t.Fatalf("historical proposal unreadable: %v", err)
	}
	err := validateProjectDiscussion([]byte(raw), core.ProductDiscussion{ProtocolVersion: 2})
	if err == nil || !strings.Contains(err.Error(), "stage notes") || !strings.Contains(err.Error(), "check notes-tests") {
		t.Fatalf("missing precise early error: %v", err)
	}
	raw = runtimeProposal(t, "NEEDS_CAPABILITY", []string{"sh", "-c", "test -f package.json"})
	var discussion core.ProductDiscoveryResult
	if err := json.Unmarshal([]byte(raw), &discussion); err != nil {
		t.Fatal(err)
	}
	discussion.Outcome = "DISCUSS"
	discussion.SelectedOptionKey = ""
	encoded, err := json.Marshal(discussion)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProjectDiscussion(encoded, core.ProductDiscussion{ProtocolVersion: 2}); err != nil {
		t.Fatalf("capability discussion rejected: %v", err)
	}
}

func TestProjectRuntimeProposalCorrectionBeforeSave(t *testing.T) {
	ctx := context.Background()
	f := newProjectPlanningFixture(t, "EMPTY")
	initial := f.create(t)
	bad := runtimeProposal(t, "SUPPORTED", []string{"sh", "-c", "test -f package.json"})
	good := runtimeProposal(t, "SUPPORTED", []string{"node", "-e", "const fs=require('fs'); if(!fs.existsSync('package.json')) process.exit(1)"})
	f.h.replies = []string{bad, good}
	next, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, projectChoice(initial, "empty"))
	if err != nil || next.Phase != "READY" {
		t.Fatalf("correction failed: %s %v", next.Phase, err)
	}
	if len(f.h.relays) != 3 {
		t.Fatalf("expected initial, rejected proposal and correction: %d", len(f.h.relays))
	}
	if !strings.Contains(f.h.relays[2].prompt, "check notes-tests") || !strings.Contains(f.h.relays[2].prompt, "stage notes") {
		t.Fatal("Steward did not receive the precise runtime error")
	}
	if len(next.Stages) != 1 || next.Stages[0].Stage.Definition.ExecutionBasis.Checks[0].Argv[1] != "-e" {
		t.Fatal("wrong proposal saved")
	}
	if _, err := core.ResolveProjectRuntime(*next.Stages[0].Stage.Definition.ExecutionBasis); err != nil {
		t.Fatalf("saved SUPPORTED proposal rejected by runtime: %v", err)
	}
	assertNoDevelopmentWork(t, f.store, f.h.standardAgentHarness, next.Goal.ID)
}
