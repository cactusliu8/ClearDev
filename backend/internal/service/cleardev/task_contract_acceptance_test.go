package cleardev

import (
	"context"
	"reflect"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestPlannerTaskContractProductClarificationStopsBeforeExecution(t *testing.T) {
	f := newAutoExecutionFixture(t)
	enableContractFixture(f, "single")
	f.harness.complexPlannerOutcome = `{"schemaVersion":2,"kind":"PRODUCT_CLARIFICATION_REQUIRED","summary":"The confirmed Stage does not define whether removed addresses should be recoverable.","questions":["Should removal be permanent, or should users be able to restore removed addresses?"]}`
	f.confirm(t)
	planning, _, err := f.store.GetClearDevComplexPlanning(context.Background(), f.view.Requirement.ID)
	if err != nil || len(planning.Plans) != 1 || len(planning.Validations) != 0 || len(planning.Reviews) != 0 {
		t.Fatalf("product clarification produced executable admission or review: %+v %v", planning, err)
	}
	clarification, ok := core.PlannerClarificationForPlan(planning.Plans[0])
	if !ok || len(clarification.Questions) != 1 {
		t.Fatalf("clarification lost exact plan/Stage binding: %+v", planning.Plans)
	}
	view, err := f.service.GetRequirement(context.Background(), f.view.Requirement.ID)
	if err != nil || view.ComplexPlanning.Phase != core.ComplexPlanningNeedsHuman || view.ComplexPlanning.ReasonCode != core.ReasonProductClarificationRequired || view.ComplexPlanning.ProductClarification == nil || view.TrustedProgress.Phase != core.TrustedPhaseNeedsHuman {
		t.Fatalf("product questions are not visible at the safe stop: %+v %v", view.ComplexPlanning, err)
	}
	f.noExecution(t)
	if _, err := f.service.StartComplexStandardExecution(context.Background(), f.view.Requirement.ID); err == nil {
		t.Fatal("a product clarification authorized execution")
	}
	counts := f.counts()
	f.reopen(t)
	f.service.plannerTaskContracts = true
	if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.store.GetClearDevComplexPlanning(context.Background(), f.view.Requirement.ID)
	if err != nil || !reflect.DeepEqual(planning, after) || counts != f.counts() {
		t.Fatalf("restart silently replanned or rewrote the product clarification: %v", err)
	}
	f.noExecution(t)
}

func TestPlannerTaskContractTaskPassDoesNotReplaceFinalAcceptance(t *testing.T) {
	for _, verdict := range []string{"REWORK", "BLOCKED", "NEEDS_HUMAN"} {
		t.Run(verdict, func(t *testing.T) {
			f := newAutoExecutionFixture(t)
			enableContractFixture(f, "dependency")
			harness := f.service.inspector.(*contractAgentHarness)
			harness.final.verdict = verdict
			f.confirm(t)
			execution, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
			if err != nil || !found || len(execution.Reviews) != 2 || len(execution.Verifications) != 2 || execution.FinalReview == nil || execution.FinalReview.Verdict != verdict {
				t.Fatalf("expected task PASS followed by final %s: %+v %v", verdict, execution, err)
			}
			if execution.Integration != nil || execution.Run.CompletedAt != nil {
				t.Fatal("Task Reviewer PASS replaced final functional acceptance")
			}
			for _, task := range execution.Tasks {
				if task.Status == core.DevelopmentTaskStatusDone {
					t.Fatal("partial task DONE escaped protected completion")
				}
			}
			view, err := f.service.GetRequirement(context.Background(), f.view.Requirement.ID)
			if err != nil || view.TrustedProgress.Phase == core.TrustedPhaseCompleted {
				t.Fatalf("false Stage completion: %v", err)
			}
		})
	}
}

func TestPlannerTaskContractPromptRequiresReadOnlyInvestigation(t *testing.T) {
	prompt := plannerTaskContractPrompt("request", &core.RequirementVersion{ID: "version", RequirementText: "confirmed Stage"}, core.ComplexCompilation{}, core.NormalizedRequirementDocument{}, "product context", nil, nil)
	for _, required := range []string{
		"Read-only tool calls are permitted and required",
		"JSON-only rule below applies to your final response",
		"repository-relative files and test entry you actually inspected",
		"Distinguish existing behavior from behavior this Stage must add",
		"Do not modify files, execute project scripts or tests",
		"Do not split work to hit a task or Builder count",
		"Builders independently investigate, plan their implementation",
		"field limits are ceilings, not quotas",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("Planner investigation contract omitted %q", required)
		}
	}
	if strings.Contains(prompt, initialPlannerRepositoryInvestigation) {
		t.Fatal("new Planner turn retained the ambiguous executable-command prohibition")
	}
}

func TestPlannerTaskContractPromptRecoveryPreservesLegacyProtocol(t *testing.T) {
	legacy := "legacy Planner prompt bytes"
	// User context can contain the instruction text too; only the template's
	// first occurrence may be reconstructed for an already frozen V2 turn.
	context := "\nProduct context: " + plannerRepositoryInvestigation + "\nMail policy: unchanged"
	contract := "Planner Task Contract prompt\n" + plannerRepositoryInvestigation + context
	initialContract := "Planner Task Contract prompt\n" + initialPlannerRepositoryInvestigation + context
	for _, tc := range []struct {
		name     string
		step     core.AgentStep
		enabled  bool
		want     string
		contract bool
	}{
		{name: "new production turn", enabled: true, want: contract, contract: true},
		{name: "in-flight legacy turn after upgrade", enabled: true, step: core.AgentStep{ID: "step", PromptSHA256: coreDigest([]byte(legacy))}, want: legacy},
		{name: "persisted contract after flag change", step: core.AgentStep{ID: "step", PromptSHA256: coreDigest([]byte(contract))}, want: contract, contract: true},
		{name: "initial V2 turn survives investigation clarification", enabled: true, step: core.AgentStep{ID: "step", PromptSHA256: coreDigest([]byte(initialContract))}, want: initialContract, contract: true},
		{name: "initial V2 after flag change", step: core.AgentStep{ID: "step", PromptSHA256: coreDigest([]byte(initialContract))}, want: initialContract, contract: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, isContract, err := chooseComplexPlannerPrompt(tc.enabled, tc.step, legacy, contract)
			if err != nil || got != tc.want || isContract != tc.contract {
				t.Fatalf("changed a persisted prompt protocol: %q %v %v", got, isContract, err)
			}
		})
	}
	if _, _, err := chooseComplexPlannerPrompt(true, core.AgentStep{ID: "step", PromptSHA256: coreDigest([]byte("different prompt"))}, legacy, contract); err == nil {
		t.Fatal("an unknown persisted prompt was silently reinterpreted")
	}
}
