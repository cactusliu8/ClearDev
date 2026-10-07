package cleardev

import (
	"context"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SQLite is real; mail scope/check/provider/authority proofs are explicit test
// doubles. This exercises the production bounded-attempt policy combination,
// not real health, candidate-freezer or live-model acceptance.
func TestPlannerRuntimeBoundedMailPolicyCombination(t *testing.T) {
	for _, scenario := range []string{"single", "dependency", "parallel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f, h := newRuntimeFixture(t, scenario)
			f.service.boundedMailAttempts = true
			f.service.checks = h.final
			// Production mail flow pins the project baseline before spawning a
			// Builder; represent that root in the workspace-local Git double too.
			h.workspaces["/tmp/s04-project"] = &contractFakeWorkspace{base: forty("a"), head: forty("a")}
			if scenario == "single" {
				h.report = false
			}
			if scenario == "parallel" {
				h.decision = core.PlannerRuntimeContinue
			}
			mutate := f.harness.complexPlanMutator
			f.harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
				mutate(plan)
				for i := range plan.Tasks {
					task := &plan.Tasks[i]
					task.WritePaths = []string{"backend/src/" + task.Key + ".js", "test/" + task.Key + ".test.js"}
					task.RequiredCheckIDs = []string{"demo-integration"}
				}
				plan.IntegrationCheckIDs = []string{"demo-integration"}
			}
			h.taskPaths = map[string][]ports.ClearDevDiffPath{
				"implement-core":   {{Status: "M", Path: "backend/src/implement-core.js"}, {Status: "A", Path: "test/implement-core.test.js"}},
				"complete-summary": {{Status: "M", Path: "backend/src/complete-summary.js"}, {Status: "A", Path: "test/complete-summary.test.js"}},
			}
			f.confirm(t)
			execution, found, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
			if err != nil || !found {
				t.Fatalf("bounded policy run absent: found=%v err=%v", found, err)
			}
			phase, reason := core.DeriveComplexExecutionPhase(execution)
			if phase != core.ComplexExecutionCompleted || execution.FinalReview == nil || execution.FinalReview.Verdict != "PASS" {
				t.Fatalf("bounded policy combination failed: phase=%s reason=%s dispatches=%d reviews=%d", phase, reason, len(execution.Dispatches), len(execution.Reviews))
			}
			if scenario == "parallel" && !h.lastCompose.CompleteTaskDeltas {
				t.Fatal("runtime coordination dropped the complete-task-delta integration policy")
			}
			if !core.BoundedMailAttempts(execution.Run) {
				t.Fatal("test did not bind the actual bounded attempt policy")
			}
			slots, err := f.store.ListClearDevMailAttempts(ctx, execution.Run.ID)
			if err != nil || len(slots) != len(execution.Tasks) {
				t.Fatalf("runtime coordination replaced or added development attempts: slots=%d tasks=%d err=%v", len(slots), len(execution.Tasks), err)
			}
			wantTurns, wantAmendments := 1, 0
			if scenario == "single" {
				wantTurns = 0
			}
			if scenario == "dependency" {
				wantAmendments = 1
			}
			if h.runtimeTurns != wantTurns || len(execution.PlannerRuntime.Amendments) != wantAmendments {
				t.Fatalf("wrong coordination: turns=%d amendments=%d", h.runtimeTurns, len(execution.PlannerRuntime.Amendments))
			}
		})
	}
}
