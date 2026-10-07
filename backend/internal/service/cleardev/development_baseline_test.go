package cleardev

import (
	"context"
	"errors"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Explicit trusted-executor double for control-flow tests. Real npm, process,
// HTTP, Git and restart evidence are exercised in cleardevlocal tests.
type baselineGateHarness struct {
	*standardAgentHarness
	mode     string
	requests []ports.ClearDevBaselineRequest
	pins     int
}

func (h *baselineGateHarness) CheckDevelopmentBaseline(_ context.Context, request ports.ClearDevBaselineRequest) (ports.ClearDevBaselineResult, error) {
	h.requests = append(h.requests, request)
	check := ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckPass, CandidateSHA: forty("a"), Image: core.StandardCandidateCheckImage,
		ImageID: "sha256:baseline-test-image", CheckEnvironmentID: "baseline-test-environment", OutputSHA256: strings.Repeat("f", 64)}
	result := ports.ClearDevBaselineResult{Required: true, CandidateSHA: forty("a"), TestRunID: request.RunID + ":npm-test", HealthRunID: request.RunID + ":health", Tests: check, Health: check}
	switch h.mode {
	case "test-failure", "health-failure", "infra-failure", "dirty":
		result.ReasonCode = map[string]string{"test-failure": "BASELINE_TEST_FAILED", "health-failure": "BASELINE_HEALTH_FAILED", "infra-failure": "BASELINE_CHECKER_UNAVAILABLE", "dirty": "BASELINE_WORKSPACE_INVALID"}[h.mode]
		return result, errors.New("explicit baseline executor failure")
	case "stale":
		result.Health.CandidateSHA = forty("b")
	case "nonzero":
		result.Tests.ExitCode = 1
	case "truncated":
		result.Tests.OutputTruncated = true
	case "timed-out":
		result.Health.TimedOut = true
	case "wrong-run":
		result.TestRunID = "unrelated:npm-test"
	}
	return result, nil
}

func (h *baselineGateHarness) PrepareReviewBranch(ctx context.Context, workspace, branch, sha string) error {
	if strings.Contains(branch, "cleardev-builder-") || strings.Contains(branch, "cleardev-complex-builder-") {
		if sha != forty("a") {
			return errors.New("branch not pinned to the checked SHA")
		}
		h.pins++
		if h.mode == "branch-mismatch" {
			return ports.ErrClearDevCandidateInvalid
		}
		return nil
	}
	return h.standardAgentHarness.PrepareReviewBranch(ctx, workspace, branch, sha)
}

func (h *baselineGateHarness) InspectCandidate(ctx context.Context, workspace, sha string) (ports.ClearDevCandidateInspection, error) {
	h.mu.Lock()
	initial := h.currentCandidate == ""
	h.mu.Unlock()
	if initial && strings.Contains(workspace, "builder") {
		candidate := sha
		if h.mode == "spawn-mismatch" {
			candidate = forty("b")
		}
		return ports.ClearDevCandidateInspection{BaseSHA: sha, CandidateSHA: candidate}, nil
	}
	return h.standardAgentHarness.InspectCandidate(ctx, workspace, sha)
}

type noBaselineExecutor struct{ ports.ClearDevCheckRunner }

func TestStandardBaselineGateStopsBeforeAnyBuilderCreation(t *testing.T) {
	for _, mode := range []string{"test-failure", "health-failure", "infra-failure", "dirty", "stale", "nonzero", "truncated", "timed-out", "wrong-run", "missing-checker", "branch-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			store, harness, ids, clock, id := newStandardIntegrationFixture(t, false)
			gate := &baselineGateHarness{standardAgentHarness: harness, mode: mode}
			service := standardTestService(store, store, harness, ids, clock, context.Background())
			service.checks, service.inspector = gate, gate
			if mode == "missing-checker" {
				service.checks = noBaselineExecutor{harness}
			}
			if _, err := service.StartStandardFlow(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			flow, ok, err := store.GetClearDevStandardFlow(context.Background(), id)
			if err != nil || !ok || len(flow.Dispatches) != 1 || flow.Dispatches[0].Status != core.DispatchStatusFailed || !strings.HasPrefix(string(flow.Dispatches[0].ReasonCode), "BASELINE_") {
				t.Fatalf("failure is not durable: %#v %v", flow, err)
			}
			assertNoBuilderActions(t, harness)
			view, viewErr := service.GetRequirement(context.Background(), id)
			if viewErr != nil || view.TrustedProgress.Phase != core.TrustedPhaseBlocked || view.TrustedProgress.ReasonCode != flow.Dispatches[0].ReasonCode {
				t.Fatalf("baseline blocker missing: phase=%s reason=%s err=%v", view.TrustedProgress.Phase, view.TrustedProgress.ReasonCode, viewErr)
			}
			snapshot, ok, err := store.GetClearDevRequirement(context.Background(), id)
			if err != nil || !ok || len(snapshot.DevelopmentTasks) != 0 || len(snapshot.Candidates) != 0 {
				t.Fatal("baseline failure created development facts")
			}
			before := len(harness.spawnConfigs)
			// A new service instance resumes the same durable stopped workflow.
			restarted := standardTestService(store, store, harness, ids, clock, context.Background())
			restarted.checks, restarted.inspector = gate, gate
			if err := restarted.ResumeStandardFlows(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(harness.spawnConfigs) != before {
				t.Fatal("restart created a Builder after failed baseline")
			}
			assertNoBuilderActions(t, harness)
		})
	}
}

func TestStandardBaselineGatePinsSHAAndDoesNotRepeatAfterCompletion(t *testing.T) {
	store, harness, ids, clock, id := newStandardIntegrationFixture(t, false)
	gate := &baselineGateHarness{standardAgentHarness: harness}
	service := standardTestService(store, store, harness, ids, clock, context.Background())
	service.checks, service.inspector = gate, gate
	if _, err := service.StartStandardFlow(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	assertCompletedStandardFlow(t, service, id)
	if len(gate.requests) == 0 || gate.pins != 1 {
		t.Fatalf("checks=%d branch pins=%d", len(gate.requests), gate.pins)
	}
	for _, request := range gate.requests {
		if request != gate.requests[0] {
			t.Fatal("pending dispatch changed baseline binding across advances")
		}
	}
	checks, spawns := len(gate.requests), len(harness.spawnConfigs)
	restarted := standardTestService(store, store, harness, ids, clock, context.Background())
	restarted.checks, restarted.inspector = gate, gate
	if err := restarted.ResumeStandardFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.StartStandardFlow(context.Background(), id); err == nil {
		t.Fatal("existing duplicate-start conflict was lost")
	}
	if len(gate.requests) != checks || len(harness.spawnConfigs) != spawns {
		t.Fatal("completed replay started new work")
	}
}

func TestStandardBaselineGateRejectsChangedSpawnBeforeBuilderMessage(t *testing.T) {
	store, harness, ids, clock, id := newStandardIntegrationFixture(t, false)
	gate := &baselineGateHarness{standardAgentHarness: harness, mode: "spawn-mismatch"}
	service := standardTestService(store, store, harness, ids, clock, context.Background())
	service.checks, service.inspector = gate, gate
	if _, err := service.StartStandardFlow(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	flow, _, err := store.GetClearDevStandardFlow(context.Background(), id)
	if err != nil || len(flow.Dispatches) != 1 || flow.Dispatches[0].ReasonCode != "BASELINE_WORKSPACE_CHANGED" {
		t.Fatalf("%#v %v", flow, err)
	}
	builders := 0
	for _, spawn := range harness.spawnConfigs {
		if strings.Contains(spawn.Branch, "builder-") {
			builders++
		}
	}
	if builders != 1 || gate.pins != 1 {
		t.Fatal("mismatch test did not actually reach Builder creation")
	}
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) {
			t.Fatal("changed spawn received a task")
		}
	}
}

func TestComplexBaselineGateStopsBeforeAnyBuilderCreation(t *testing.T) {
	for _, mode := range []string{"test-failure", "health-failure", "infra-failure", "stale", "branch-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			store, harness, ids, clock, _ := newComplexFixture(t)
			harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
				single := plan.Tasks[0]
				single.DependencyKeys = []string{}
				plan.Tasks = []core.ComplexPlanTask{single}
				plan.ParallelSuggestion.RecommendedBuilderCount = 1
			}
			service := complexTestService(store, harness, ids, clock, context.Background())
			view := answerComplexQuestions(t, service, createComplexUntilClarification(t, service))
			if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
				t.Fatal(err)
			}
			gate := &baselineGateHarness{standardAgentHarness: harness, mode: mode}
			service.checks, service.inspector = gate, gate
			if _, err := service.StartComplexStandardExecution(context.Background(), view.Requirement.ID); err != nil {
				t.Fatal(err)
			}
			if err := service.runComplexStandardExecution(context.Background(), view.Requirement.ID); err != nil {
				t.Fatal(err)
			}
			execution, ok, err := store.GetClearDevComplexExecution(context.Background(), view.Requirement.ID)
			if err != nil || !ok {
				t.Fatal(err)
			}
			found := false
			for _, binding := range execution.RoleBindings {
				if binding.Role == core.StandardRoleBuilder && binding.Status == core.RoleBindingStatusFailed && strings.HasPrefix(string(binding.ReasonCode), "BASELINE_") {
					found = true
				}
			}
			if !found || len(execution.Dispatches) != 0 {
				t.Fatalf("baseline failure did not prevent dispatch: %#v", execution)
			}
			assertNoBuilderActions(t, harness)
			blocked, err := service.GetRequirement(context.Background(), view.Requirement.ID)
			if err != nil || blocked.TrustedProgress.Phase != core.TrustedPhaseBlocked || !strings.HasPrefix(string(blocked.TrustedProgress.ReasonCode), "BASELINE_") {
				t.Fatalf("complex blocker not visible: phase=%s reason=%s err=%v", blocked.TrustedProgress.Phase, blocked.TrustedProgress.ReasonCode, err)
			}
			before := len(harness.spawnConfigs)
			restarted := complexTestService(store, harness, ids, clock, context.Background())
			restarted.checks, restarted.inspector = gate, gate
			if err := restarted.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(harness.spawnConfigs) != before {
				t.Fatal("restart re-created a failed Builder")
			}
		})
	}
}

func TestComplexBaselineGateHealthyBindsOnceAcrossRestart(t *testing.T) {
	store, harness, ids, clock, _ := newComplexFixture(t)
	harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
		single := plan.Tasks[0]
		single.DependencyKeys = []string{}
		plan.Tasks = []core.ComplexPlanTask{single}
		plan.ParallelSuggestion.RecommendedBuilderCount = 1
	}
	service := complexTestService(store, harness, ids, clock, context.Background())
	view := answerComplexQuestions(t, service, createComplexUntilClarification(t, service))
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	gate := &baselineGateHarness{standardAgentHarness: harness}
	service.checks, service.inspector = gate, gate
	service.runBackground = func(func()) {}
	if _, err := service.StartComplexStandardExecution(context.Background(), view.Requirement.ID); err != nil {
		t.Fatal(err)
	}
	var execution core.ComplexExecutionSnapshot
	bound := false
	for attempt := 0; attempt < 50 && !bound; attempt++ {
		if _, _, err := service.advanceComplexStandardExecution(context.Background(), view.Requirement.ID); err != nil {
			t.Fatal(err)
		}
		var err error
		execution, _, err = store.GetClearDevComplexExecution(context.Background(), view.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, role := range execution.RoleBindings {
			if role.Role == core.StandardRoleBuilder && role.Status == core.RoleBindingStatusBound {
				bound = true
			}
		}
	}
	if !bound || gate.pins != 1 || len(gate.requests) == 0 {
		t.Fatalf("healthy gate failed: bound=%t pins=%d checks=%d", bound, gate.pins, len(gate.requests))
	}
	spawns := len(harness.spawnConfigs)
	restarted := complexTestService(store, harness, ids, clock, context.Background())
	restarted.checks, restarted.inspector = gate, gate
	if changed, done, err := restarted.ensureComplexExecutionBuilder(context.Background(), execution, view.Requirement.AOProjectID); changed || done || err != nil {
		t.Fatalf("bound replay: changed=%t done=%t err=%v", changed, done, err)
	}
	if len(harness.spawnConfigs) != spawns || gate.pins != 1 {
		t.Fatal("bound replay spawned or pinned a second Builder")
	}
	for _, request := range gate.requests {
		if request != gate.requests[0] {
			t.Fatal("restart changed baseline identity")
		}
	}
}

func assertNoBuilderActions(t *testing.T, harness *standardAgentHarness) {
	t.Helper()
	for _, spawn := range harness.spawnConfigs {
		if strings.Contains(strings.ToLower(spawn.DisplayName), "builder") || strings.Contains(spawn.Branch, "builder-") {
			t.Fatalf("failed baseline created Builder: %#v", spawn)
		}
	}
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) {
			t.Fatal("failed baseline sent a Builder task")
		}
	}
}
