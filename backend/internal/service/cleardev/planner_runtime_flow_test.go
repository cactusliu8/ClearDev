package cleardev

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// These tests use a real on-disk SQLite store and the explicitly deterministic
// provider/Git/check harness. They do not claim real-model or real-Git proof.
func TestPlannerRuntimeOrdinaryFlowAddsNoPlannerRound(t *testing.T) {
	f, h := newRuntimeFixture(t, "single")
	h.report = false
	f.confirm(t)
	execution := f.completed(t)
	if enabled, err := core.PlannerRuntimeRun(execution.Run); err != nil || !enabled {
		t.Fatalf("new execution did not freeze its policy: enabled=%v err=%v", enabled, err)
	}
	if history := execution.PlannerRuntime; history == nil || len(history.Events) != 0 || len(history.Requests) != 0 || len(history.Decisions) != 0 || len(history.Amendments) != 0 || h.runtimeTurns != 0 {
		t.Fatalf("routine execution added Planner coordination: history=%+v turns=%d", history, h.runtimeTurns)
	}
	planning, _, err := f.store.GetClearDevComplexPlanning(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	plans := 0
	for _, step := range planning.AgentSteps {
		if step.Kind == core.ComplexAgentStepEngineeringPlan {
			plans++
		}
	}
	if plans != 1 || len(planning.Plans) != 1 {
		t.Fatalf("routine flow re-planned: steps=%d plans=%d", plans, len(planning.Plans))
	}
}

func TestPlannerRuntimeContinueAndAmendCompleteExistingTransaction(t *testing.T) {
	for _, decision := range []string{core.PlannerRuntimeContinue, core.PlannerRuntimeAmend} {
		t.Run(decision, func(t *testing.T) {
			ctx := context.Background()
			f, h := newRuntimeFixture(t, "dependency")
			h.decision = decision
			var before core.ComplexExecutionSnapshot
			h.beforeRuntime = func(ctx context.Context, _ string) error {
				var err error
				before, _, err = f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
				if err != nil {
					return err
				}
				if len(before.Dispatches) != 1 || len(before.Verifications) != 1 || len(before.Reviews) != 1 || before.Reviews[0].Verdict != "PASS" || before.Tasks[1].CurrentDispatchID != "" {
					t.Fatalf("Planner did not receive the quiescent reviewed boundary: %+v", before)
				}
				if history := before.PlannerRuntime; history == nil || len(history.Events) != 1 || len(history.Requests) != 1 || len(history.Decisions) != 0 {
					t.Fatalf("request was not reserved before send: %+v", history)
				}
				return nil
			}
			f.confirm(t)
			execution, found, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
			if err != nil || !found {
				t.Fatalf("execution missing: %v %v", found, err)
			}
			phase, reason := core.DeriveComplexExecutionPhase(execution)
			if phase != core.ComplexExecutionCompleted || len(execution.Dispatches) != 2 || len(execution.Reviews) != 2 || len(execution.Verifications) != 2 || execution.FinalReview == nil || execution.FinalReview.Verdict != "PASS" || execution.Integration == nil {
				t.Fatalf("runtime flow did not finish protected completion: phase=%s reason=%s history=%+v dispatches=%+v reviews=%+v final=%+v", phase, reason, execution.PlannerRuntime, execution.Dispatches, execution.Reviews, execution.FinalReview)
			}
			history := execution.PlannerRuntime
			wantAmendments := 0
			if decision == core.PlannerRuntimeAmend {
				wantAmendments = 1
			}
			if history == nil || len(history.Events) != 1 || len(history.Requests) != 1 || len(history.Decisions) != 1 || len(history.Amendments) != wantAmendments || h.runtimeTurns != 1 {
				t.Fatalf("wrong coordination history: %+v turns=%d", history, h.runtimeTurns)
			}
			if history.Decisions[0].Source != "PLANNER" || history.Decisions[0].Outcome != decision {
				t.Fatalf("actual Planner decision was not applied: %+v", history.Decisions)
			}
			if !reflect.DeepEqual(before.Verifications[0], execution.Verifications[0]) || !reflect.DeepEqual(before.Reviews[0], execution.Reviews[0]) || before.Tasks[0].ExecutionPackageJSON != execution.Tasks[0].ExecutionPackageJSON || execution.Run.PlanSHA256 != before.Run.PlanSHA256 {
				t.Fatal("coordination rewrote source contracts, verified candidates, reviews or the original plan")
			}
			planning, _, err := f.store.GetClearDevComplexPlanning(ctx, f.view.Requirement.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(planning.Plans) != 1 || history.Requests[0].PlannerRoleBindingID != planning.Plans[0].PlannerRoleBindingID {
				t.Fatal("coordination replaced the original Stage Planner or appended a new initial plan")
			}
			for _, binding := range planning.RoleBindings {
				if binding.ID == history.Requests[0].PlannerRoleBindingID && binding.AOSessionID != history.Requests[0].AOSessionID {
					t.Fatal("coordination did not reuse the original Planner session")
				}
			}
			consumer := execution.Tasks[1]
			pkg, err := core.ParseComplexStandardExecutionPackage([]byte(consumer.ExecutionPackageJSON))
			if err != nil {
				t.Fatal(err)
			}
			if decision == core.PlannerRuntimeAmend {
				if pkg.RuntimeRevision == nil || !strings.Contains(consumer.ExecutionPackageJSON, runtimeAddedCriterion) || consumer.ExecutionPackageSHA256 == before.Tasks[1].ExecutionPackageSHA256 || consumer.ReworkCount != 0 || consumer.CurrentRound != 0 {
					t.Fatal("remaining engineering contract was not actually revised, or its attempt history was reset")
				}
			} else if consumer.ExecutionPackageJSON != before.Tasks[1].ExecutionPackageJSON || pkg.RuntimeRevision != nil {
				t.Fatal("CONTINUE silently amended a task")
			}
			for _, review := range execution.Reviews {
				if review.ComplexExecutionTaskID != consumer.ID {
					continue
				}
				var packet complexExecutionReviewPacket
				if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil || packet.ExecutionPackageSHA256 != consumer.ExecutionPackageSHA256 || string(packet.ExecutionPackage) != consumer.ExecutionPackageJSON {
					t.Fatalf("Reviewer did not receive exact effective contract: %v", err)
				}
			}
			var finalPacket core.RequirementFinalReviewPacket
			if err := json.Unmarshal([]byte(execution.FinalReview.ReviewPacketJSON), &finalPacket); err != nil || !reflect.DeepEqual(finalPacket.PlannerRuntime, history) || finalPacket.Tasks[1].ExecutionPackageSHA256 != consumer.ExecutionPackageSHA256 {
				t.Fatalf("Final Reviewer did not receive exact revision history: %v", err)
			}
			if err := core.ValidateRequirementFinalReviewBinding(*execution.FinalReview, execution.Run, execution.Integration.CandidateCommitSHA, execution.Integration.CheckRunIDs); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if changed, err := f.store.ApplyClearDevPlannerRuntime(ctx, history.Events[0].ID, f.clock()); err != nil || changed {
					t.Fatalf("duplicate result was reapplied: changed=%v err=%v", changed, err)
				}
			}
			counts := f.counts()
			f.reopen(t)
			attachRuntimeHarness(f, h)
			f.service.plannerRuntimeCoordination = false // frozen policy, not a mutable feature flag
			if err := f.service.ResumeComplexFlows(ctx); err != nil {
				t.Fatal(err)
			}
			after, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
			if err != nil || !reflect.DeepEqual(execution, after) || f.counts() != counts || h.runtimeTurns != 1 {
				t.Fatalf("real SQLite reopen changed history or resent work: %v", err)
			}
		})
	}
}

func TestPlannerRuntimeProductAndStartedContractStop(t *testing.T) {
	for _, scenario := range []string{"product", "started-contract", "explicit-stop"} {
		t.Run(scenario, func(t *testing.T) {
			f, h := newRuntimeFixture(t, "dependency")
			switch scenario {
			case "product":
				h.category, h.decision = "PRODUCT", core.PlannerRuntimeProduct
			case "started-contract":
				h.amendSource = true
			case "explicit-stop":
				h.decision = core.PlannerRuntimeStop
			}
			f.confirm(t)
			execution, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
			if err != nil || !found {
				t.Fatalf("execution missing: %v %v", found, err)
			}
			phase, reason := core.DeriveComplexExecutionPhase(execution)
			if phase != core.ComplexExecutionNeedsHuman || len(execution.Dispatches) != 1 || len(execution.Verifications) != 1 || execution.Integration != nil || execution.FinalReview != nil || execution.Run.CompletedAt != nil || h.runtimeTurns != 1 {
				t.Fatalf("unsafe coordination did not stop: phase=%s reason=%s history=%+v dispatches=%+v", phase, reason, execution.PlannerRuntime, execution.Dispatches)
			}
			if execution.PlannerRuntime == nil || len(execution.PlannerRuntime.Decisions) != 1 || len(execution.PlannerRuntime.Amendments) != 0 {
				t.Fatalf("stop was not durable: %+v", execution.PlannerRuntime)
			}
			if scenario == "product" && reason != core.ReasonProductClarificationRequired {
				t.Fatal("product question was not escalated to product authority")
			}
			if scenario == "started-contract" && execution.PlannerRuntime.Decisions[0].Source != "CONTROL_PLANE" {
				t.Fatal("unsafe proposed amendment was treated as an applied Planner decision")
			}
		})
	}
}
