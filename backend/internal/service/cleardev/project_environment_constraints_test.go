package cleardev

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func environmentProposal(t *testing.T, withLock bool) string {
	t.Helper()
	var p core.ProductDiscoveryResult
	if err := json.Unmarshal([]byte(genericProjectReply("empty")), &p); err != nil {
		t.Fatal(err)
	}
	p.Stages[0].Feasibility = "SUPPORTED"
	if !withLock {
		b := p.Stages[0].ExecutionBasis
		paths := []string{}
		for _, v := range b.WritePaths {
			if v != "package-lock.json" {
				paths = append(paths, v)
			}
		}
		b.WritePaths = paths
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestEnvironmentProposalCorrectsBeforeConfirmation(t *testing.T) {
	f := newProjectPlanningFixture(t, "EMPTY")
	initial := f.create(t)
	bad := environmentProposal(t, false)
	// Historical documents remain readable without acquiring new permissions.
	if _, err := core.ParseProductDiscoveryResult([]byte(bad)); err != nil {
		t.Fatal(err)
	}
	f.h.replies = []string{bad, environmentProposal(t, true)}
	next, err := f.s.SubmitProductDiscussion(context.Background(), initial.Goal.ID, projectChoice(initial, "empty"))
	if err != nil || next.Phase != "READY" {
		t.Fatalf("corrected proposal: %s %v", next.Phase, err)
	}
	if len(f.h.relays) != 3 || !strings.Contains(f.h.relays[2].prompt, "package-lock.json") {
		t.Fatal("Steward did not receive precise correction")
	}
	if !strings.Contains(f.h.relays[1].prompt, projectEnvironmentGuidance) {
		t.Fatal("missing Steward environment guidance")
	}
	assertNoDevelopmentWork(t, f.store, f.h.standardAgentHarness, next.Goal.ID)
}

func TestEnvironmentGuidancePreservesOldBuilderMessages(t *testing.T) {
	f, child, admission, _ := plannedProjectExecutionFixture(t, "EXISTING")
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	e, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{
		executionBuilderPromptBeforeEnvironmentGuidance(e.Run, []byte("{}"), "d", "t", 0, ""),
		executionBuilderPromptBeforeLockfileRepair(e.Run, []byte("{}"), "d", "t", 0, ""),
		executionBuilderPromptForRun(e.Run, []byte("{}"), "d", "t", 0, ""),
	} {
		got := executionBuilderPromptForExistingStep(e.Run, []byte("{}"), "d", "t", 0, "", coreDigest([]byte(msg)))
		if got != msg {
			t.Fatal("existing prompt bytes changed")
		}
	}
	planner := projectEngineeringPrompt("r", &core.RequirementVersion{}, core.ComplexCompilation{}, core.NormalizedRequirementDocument{}, "", core.ProductStage{}, nil)
	if !strings.Contains(planner, projectEnvironmentGuidance) {
		t.Fatal("Planner missing common constraints")
	}
}

func TestEnvironmentPlannerCorrectsDependencyTaskBeforeSave(t *testing.T) {
	f := newProjectPlanningFixture(t, "EMPTY")
	initial := f.create(t)
	selected := f.choose(t, initial, "empty")
	_, child := f.prepare(t, selected)
	good := genericEngineeringReply(t, child.RequirementVersions[0])
	var plan core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(good), &plan); err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, p := range plan.Tasks[0].WritePaths {
		if p != "package-lock.json" {
			paths = append(paths, p)
		}
	}
	plan.Tasks[0].WritePaths = paths

	// Marshal the original response shape; the core struct also has bound fields.
	var payload map[string]any
	if err := json.Unmarshal([]byte(good), &payload); err != nil {
		t.Fatal(err)
	}
	payload["tasks"].([]any)[0].(map[string]any)["writePaths"] = paths
	bad, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	f.h.replies = append(f.h.replies, string(bad), good)
	before := len(f.h.relays)
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	child = mustGetComplex(t, f.s, child.Requirement.ID)
	if len(child.ComplexPlanning.Plans) != 1 {
		t.Fatal("corrected plan was not saved")
	}
	if len(f.h.relays) != before+2 || !strings.Contains(f.h.relays[before+1].prompt, "package-lock.json") {
		t.Fatal("Planner did not receive dependency scope correction")
	}
	assertNoDevelopmentWork(t, f.store, f.h.standardAgentHarness, child.Requirement.ID)
}

func TestEnvironmentPlannerPromptRetainsIssuedVersion(t *testing.T) {
	next := projectEngineeringPrompt("r", &core.RequirementVersion{}, core.ComplexCompilation{}, core.NormalizedRequirementDocument{}, "", core.ProductStage{}, nil)
	old := strings.TrimPrefix(next, projectEnvironmentGuidance)
	for _, original := range []string{old, next} {
		step := core.AgentStep{ID: "issued", PromptSHA256: coreDigest([]byte(original))}
		got, _, err := chooseComplexPlannerPrompt(false, step, next, next)
		if err != nil || got != original {
			t.Fatalf("issued Planner message changed: %v", err)
		}
	}
}
