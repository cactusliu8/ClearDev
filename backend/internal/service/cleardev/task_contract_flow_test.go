package cleardev

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// Models, Git and checks are test doubles. All planning, native-authority
// boundary calls, admission, dispatch, review and completion facts use SQLite.
func enableContractFixture(f *autoExecutionFixture, scenario string) {
	f.service.plannerTaskContracts = true
	attachContractHarness(f, newContractAgentHarness(f.harness))
	f.harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
		plan.SchemaVersion = core.PlannerTaskContractVersion
		plan.Tasks[0].WritePaths = []string{"src/email.js", "test/email.test.js"}
		plan.Tasks[1].WritePaths = []string{"src/summary.js", "test/summary.test.js"}
		plan.Tasks[0].ReviewCriteria = []string{"Valid addresses normalize deterministically; invalid and empty inputs preserve documented validation behavior."}
		plan.Tasks[1].ReviewCriteria = []string{"The summary reports stable counts and preserves the specified behavior for empty and duplicate values."}
		switch scenario {
		case "single":
			plan.Tasks = plan.Tasks[:1]
		case "dependency", "reverse-dependency":
			plan.InterfaceContracts = []core.ComplexInterfaceContract{{
				Key: "normalized-addresses", ProviderTaskKey: plan.Tasks[0].Key,
				ConsumerTaskKeys: []string{plan.Tasks[1].Key},
				Expectations:     []string{"The core provides a deterministic normalized address collection; an empty collection is valid and the summary consumes it without changing normalization semantics."},
			}}
			if scenario == "reverse-dependency" {
				plan.Tasks[0], plan.Tasks[1] = plan.Tasks[1], plan.Tasks[0]
			}
		case "independent", "parallel":
			plan.Tasks[1].DependencyKeys = []string{}
			if scenario == "parallel" {
				plan.ParallelSuggestion.RecommendedBuilderCount = 2
				plan.ParallelSuggestion.Reason = "The two ready tasks have disjoint source and test boundaries with no shared interface."
			}
		}
	}
}

func TestPlannerTaskContractControlPlaneExecution(t *testing.T) {
	for _, scenario := range []string{"single", "dependency", "reverse-dependency", "independent", "parallel"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAutoExecutionFixture(t)
			enableContractFixture(f, scenario)
			f.confirm(t)
			planning, found, err := f.store.GetClearDevComplexPlanning(context.Background(), f.view.Requirement.ID)
			if err != nil || !found || len(planning.Plans) != 1 || len(planning.Validations) != 1 || len(planning.Reviews) != 0 {
				t.Fatalf("contract was not independently admitted: found=%v plans=%d validations=%d reviews=%d err=%v", found, len(planning.Plans), len(planning.Validations), len(planning.Reviews), err)
			}
			for _, step := range planning.AgentSteps {
				if step.Kind == core.ComplexAgentStepPlanReview {
					t.Fatal("new contract created a routine Steward plan-review step")
				}
			}
			execution, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
			if err != nil || !found {
				t.Fatalf("no execution after deterministic admission: %v %v", found, err)
			}
			phase, reason := core.DeriveComplexExecutionPhase(execution)
			wantTasks, wantBuilders, wantInterfaces := 2, 1, 0
			if scenario == "single" {
				wantTasks = 1
			}
			if scenario == "dependency" || scenario == "reverse-dependency" {
				wantInterfaces = 1
			}
			if scenario == "parallel" {
				wantBuilders = 2
			}
			if phase != core.ComplexExecutionCompleted || len(execution.Tasks) != wantTasks || len(execution.Reviews) != wantTasks || execution.Integration == nil || execution.Run.FixedBuilderCount != wantBuilders {
				t.Fatalf("phase=%s reason=%s tasks=%d reviews=%d builders=%d dispatches=%+v", phase, reason, len(execution.Tasks), len(execution.Reviews), execution.Run.FixedBuilderCount, execution.Dispatches)
			}
			final := execution.FinalReview
			if final == nil || final.Status != "SETTLED" || final.Verdict != "PASS" || final.ResultID == "" || final.CandidateCommitSHA != execution.Integration.CandidateCommitSHA {
				t.Fatalf("final completion lacks an independent review of exact F: %+v", final)
			}
			if err := core.ValidateRequirementFinalReviewBinding(*final, execution.Run, execution.Integration.CandidateCommitSHA, execution.Integration.CheckRunIDs); err != nil {
				t.Fatal(err)
			}
			var finalPacket core.RequirementFinalReviewPacket
			if err := json.Unmarshal([]byte(final.ReviewPacketJSON), &finalPacket); err != nil || len(finalPacket.Tasks) != wantTasks || len(finalPacket.TaskReviews) != wantTasks || finalPacket.Plan.PlanSHA256 != planning.Plans[0].PlanSHA256 {
				t.Fatalf("Final Reviewer lacks exact contract/task evidence: %v", err)
			}
			if !strings.Contains(core.RequirementFinalReviewPrompt(*final), "Planner Task Contract 补充") || !strings.Contains(finalPacket.Plan.PlanJSON, `"reviewCriteria"`) {
				t.Fatal("Final Reviewer did not receive its contract acceptance instructions")
			}
			if wantInterfaces > 0 && !strings.Contains(finalPacket.Plan.PlanJSON, `"interfaceContracts"`) {
				t.Fatal("Final Reviewer lost cross-task interface facts")
			}
			if admitted, err := core.PlannerTaskContractRun(execution.Run); err != nil || !admitted || execution.Run.PlanReviewID != "" {
				t.Fatalf("execution lost its admission route: %v %v", admitted, err)
			}
			for _, binding := range execution.RoleBindings {
				if binding.Role == core.StandardRoleSteward {
					t.Fatal("new contract created an execution approval role")
				}
			}
			for _, step := range execution.AgentSteps {
				if step.Kind == core.AgentStepDispatchRequest {
					t.Fatal("new contract created a Steward dispatch-request message")
				}
			}
			for _, task := range execution.Tasks {
				pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
				if err != nil || pkg.SchemaVersion != core.PlannerTaskContractVersion || len(pkg.ReviewCriteria) == 0 || len(pkg.InterfaceContracts) != wantInterfaces || pkg.PlanSHA256 != planning.Plans[0].PlanSHA256 {
					t.Fatalf("task contract lost its exact plan binding: %s %v", task.ExecutionPackageJSON, err)
				}
			}
			for _, review := range execution.Reviews {
				var packet complexExecutionReviewPacket
				if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
					t.Fatal(err)
				}
				if !isTaskContractPackage(packet.ExecutionPackage) || packet.ExecutionPackageSHA256 != coreDigest(packet.ExecutionPackage) || packet.PlanSHA256 != planning.Plans[0].PlanSHA256 || packet.CandidateSHA == "" {
					t.Fatal("Reviewer packet is not bound to the exact contract and candidate")
				}
			}
			builderPrompts, reviewerPrompts := 0, 0
			f.harness.mu.Lock()
			for _, relay := range f.harness.relays {
				if strings.Contains(relay.prompt, taskContractBuilderInstructions) {
					builderPrompts++
				}
				if strings.Contains(relay.prompt, taskContractReviewerInstructions) {
					reviewerPrompts++
				}
			}
			f.harness.mu.Unlock()
			if builderPrompts != wantTasks || reviewerPrompts != wantTasks {
				t.Fatalf("contract prompts: builders=%d reviewers=%d want=%d", builderPrompts, reviewerPrompts, wantTasks)
			}
			f.noQuick(t)
			counts := f.counts()
			harness := f.service.inspector.(*contractAgentHarness)
			if scenario == "parallel" && harness.composeCalls == 0 {
				t.Fatal("parallel candidates were not composed")
			}
			f.reopen(t)
			f.service.plannerTaskContracts = true
			attachContractHarness(f, harness)
			if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.StartComplexStandardExecution(context.Background(), f.view.Requirement.ID); err != nil {
				t.Fatal(err)
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
			if err != nil || !reflect.DeepEqual(execution, after) || counts != f.counts() {
				t.Fatalf("restart changed contract, review or execution history: %v", err)
			}
		})
	}
}

func TestPlannerTaskContractDoesNotReuseChangedPlan(t *testing.T) {
	f := newAutoExecutionFixture(t)
	enableContractFixture(f, "single")
	f.confirm(t)
	planning, _, err := f.store.GetClearDevComplexPlanning(context.Background(), f.view.Requirement.ID)
	if err != nil || len(planning.Plans) != 1 || len(planning.Validations) != 1 {
		t.Fatalf("missing admitted contract: %v", err)
	}
	plan, validation := planning.Plans[0], planning.Validations[0]
	plan.PlanJSON = strings.Replace(plan.PlanJSON, "Valid addresses normalize deterministically", "A different compatibility obligation applies", 1)
	plan.PlanSHA256 = coreDigest([]byte(plan.PlanJSON))
	validation.PlanSHA256 = plan.PlanSHA256
	if err := f.store.CreateClearDevComplexEngineeringPlan(context.Background(), core.CreateComplexPlanCommand{Plan: plan, Validation: &validation}); err == nil {
		t.Fatal("changed contract reused the old plan identity and reviews")
	}
	after, _, err := f.store.GetClearDevComplexPlanning(context.Background(), f.view.Requirement.ID)
	if err != nil || !reflect.DeepEqual(planning, after) {
		t.Fatalf("failed replacement rewrote historical planning facts: %v", err)
	}
}

func TestPlannerTaskContractRejectsLegacyResultOnFreshTurn(t *testing.T) {
	f := newAutoExecutionFixture(t)
	f.service.plannerTaskContracts = true // The test provider still returns V1.
	f.confirm(t)
	planning, _, err := f.store.GetClearDevComplexPlanning(context.Background(), f.view.Requirement.ID)
	if err != nil || len(planning.Plans) != 0 || len(planning.Validations) != 0 || len(planning.Reviews) != 0 {
		t.Fatalf("V1 output bypassed mandatory Task Contract validation: %v", err)
	}
	f.noExecution(t)
}
