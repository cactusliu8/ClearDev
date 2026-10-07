package cleardev

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func runtimeContractTask(t *testing.T) ComplexExecutionTask {
	t.Helper()
	plan, _, planSHA := parsePlannerContract(t, plannerContractJSON(t, nil))
	_, raw, digest, err := BuildComplexStandardExecutionPackage(WorkModeStandard, ComplexStandardExecutionPackageInput{
		ExecutionRunID: "run", RequirementVersionID: "version", RequirementSHA256: strings.Repeat("a", 64),
		RequirementText: "Normalize addresses and summarize the unique collection.", PlanID: "plan", PlanSHA256: planSHA,
		TaskSetVersion: 1, TaskID: "consumer", DependencyTaskIDs: []string{"provider"}, Task: plan.Tasks[1],
		PlanSchemaVersion: plan.SchemaVersion, InterfaceContracts: plan.InterfaceContracts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ComplexExecutionTask{ID: "mapping", ExecutionRunID: "run", TaskKey: plan.Tasks[1].Key, DevelopmentTaskID: "consumer", Ordinal: 1,
		ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest, Status: DevelopmentTaskStatusPlanned}
}

func TestPlannerRuntimePolicyIsOptInAndBound(t *testing.T) {
	raw, _, err := BuildComplexExecutionRunPackage(WorkModeStandard, "run", "version", strings.Repeat("a", 64), "plan", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := BindPlannerRuntimePolicy(raw); err == nil {
		t.Fatal("legacy run was admitted to runtime coordination")
	}
	raw, _, err = BindPlannerTaskContractPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = BindRequirementFinalReviewPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	run := ComplexExecutionRun{ID: "run", RequirementVersionID: "version", RequirementSHA256: strings.Repeat("a", 64), PlanID: "plan", PlanSHA256: strings.Repeat("b", 64), Mode: WorkModeStandard, FixedBuilderCount: 1, TaskSetVersion: 1, ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: sha256Hex(raw)}
	if enabled, err := PlannerRuntimeRun(run); err != nil || enabled {
		t.Fatalf("old V2 run changed behavior: enabled=%v err=%v", enabled, err)
	}
	bound, digest, err := BindPlannerRuntimePolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	run.ExecutionPackageJSON, run.ExecutionPackageSHA256 = string(bound), digest
	if enabled, err := PlannerRuntimeRun(run); err != nil || !enabled {
		t.Fatalf("new runtime policy not recognized: enabled=%v err=%v", enabled, err)
	}
	for _, field := range []string{"plannerCoordinationMaxRounds", "plannerCoordinationMaxRevisions"} {
		var changed map[string]any
		if err := json.Unmarshal(bound, &changed); err != nil {
			t.Fatal(err)
		}
		changed[field] = 99
		bad, _ := json.Marshal(changed)
		copyRun := run
		copyRun.ExecutionPackageJSON, copyRun.ExecutionPackageSHA256 = string(bad), sha256Hex(bad)
		if _, err := PlannerRuntimeRun(copyRun); err == nil {
			t.Fatalf("changed frozen bound %s accepted", field)
		}
	}
	run.PlanSHA256 = strings.Repeat("c", 64)
	if _, err := PlannerRuntimeRun(run); err == nil {
		t.Fatal("policy recognized against a different plan")
	}
}

func TestPlannerRuntimeBuilderReportPreservesLegacyProtocol(t *testing.T) {
	prompt := PlannerRuntimeBuilderPromptSuffix()
	for _, required := range []string{
		"the matching coordination object is REQUIRED",
		"Do not leave that request only in summary prose",
		"Environment/provider failure, a task-local implementation failure, or an ordinary failed check does not by itself require coordination",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("runtime Builder prompt lost blocked-result classification rule %q", required)
		}
	}
	base := `{"schemaVersion":1,"kind":"BUILDER_RESULT","outcome":"CANDIDATE_READY","summary":"Implemented the provider contract."}`
	if _, report, err := ParsePlannerRuntimeBuilderResult([]byte(base), "dispatch", "task", 0); err != nil || report != nil {
		t.Fatalf("routine completion acquired coordination: %v %v", report, err)
	}
	raw := strings.TrimSuffix(base, "}") + `,"coordination":{"category":"ENGINEERING","summary":"The consumer must preserve normalized ordering.","evidence":["The provider returns stable first-occurrence order; see src/email.ts."],"affectedTaskKeys":["build-summary"]}}`
	result, report, err := ParsePlannerRuntimeBuilderResult([]byte(raw), "dispatch", "task", 0)
	if err != nil || report == nil || result.DispatchID != "dispatch" || report.Category != "ENGINEERING" {
		t.Fatalf("report not bound: %#v %#v %v", result, report, err)
	}
	if _, err := ParseComplexExecutionBuilderResult([]byte(raw), "dispatch", "task", 0); err == nil {
		t.Fatal("legacy parser accepted a new-run report")
	}
	for _, bad := range []string{
		strings.Replace(raw, `"category":"ENGINEERING"`, `"category":"TASK_DONE"`, 1),
		strings.Replace(raw, `"affectedTaskKeys":["build-summary"]`, `"affectedTaskKeys":[]`, 1),
		strings.TrimSuffix(base, "}") + `,"coordination":null}`,
		strings.Replace(raw, `"category":"ENGINEERING"`, `"category":"ENGINEERING","category":"PRODUCT"`, 1),
		strings.Replace(raw, `"category":"ENGINEERING"`, `"category":"ENGINEERING","approve":true`, 1),
	} {
		if _, _, err := ParsePlannerRuntimeBuilderResult([]byte(bad), "dispatch", "task", 0); err == nil {
			t.Fatalf("invalid report accepted: %s", bad)
		}
	}
}

func TestPlannerRuntimeDecisionSeparatesProductAndEngineering(t *testing.T) {
	event := PlannerCoordinationEvent{ID: "event", Report: PlannerCoordinationReport{Category: "ENGINEERING", AffectedTaskKeys: []string{"build-summary"}}}
	contextSHA := strings.Repeat("d", 64)
	continueRaw := `{"schemaVersion":1,"kind":"PLANNER_RUNTIME_COORDINATION","decision":"CONTINUE","summary":"The existing contract already preserves the observed order.","questions":[],"amendments":[]}`
	result, _, _, err := ParsePlannerCoordinationResult([]byte(continueRaw), event, contextSHA)
	if err != nil || result.EventID != event.ID || result.ContextSHA256 != contextSHA {
		t.Fatalf("continue not bound: %#v %v", result, err)
	}
	amendRaw := `{"schemaVersion":1,"kind":"PLANNER_RUNTIME_COORDINATION","decision":"AMEND_REMAINING","summary":"Make the remaining consumer's order expectation explicit without changing the provider.","questions":[],"amendments":[{"taskKey":"build-summary","additionalReviewCriteria":["The summary preserves the provider's stable first-occurrence order."]}]}`
	if _, _, _, err := ParsePlannerCoordinationResult([]byte(amendRaw), event, contextSHA); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(amendRaw, `"taskKey":"build-summary"`, `"taskKey":"another-task"`, 1),
		strings.Replace(amendRaw, `"additionalReviewCriteria":`, `"writePaths":["**"],"additionalReviewCriteria":`, 1),
		strings.Replace(continueRaw, `"questions":[]`, `"questions":["Change product behavior?"]`, 1),
		strings.Replace(continueRaw, `"amendments":[]`, `"amendments":null`, 1),
		strings.Replace(continueRaw, `"decision":"CONTINUE"`, `"decision":"REOPEN"`, 1),
	} {
		if _, _, _, err := ParsePlannerCoordinationResult([]byte(bad), event, contextSHA); err == nil {
			t.Fatalf("invalid decision accepted: %s", bad)
		}
	}
	event.Report.Category = "PRODUCT"
	for _, technical := range []string{continueRaw, amendRaw} {
		if _, _, _, err := ParsePlannerCoordinationResult([]byte(technical), event, contextSHA); err == nil {
			t.Fatal("Planner converted a product question into a technical decision")
		}
	}
	product := strings.Replace(continueRaw, `"decision":"CONTINUE"`, `"decision":"PRODUCT_CLARIFICATION_REQUIRED"`, 1)
	product = strings.Replace(product, `"questions":[]`, `"questions":["Which existing order must the product preserve?"]`, 1)
	if _, _, _, err := ParsePlannerCoordinationResult([]byte(product), event, contextSHA); err != nil {
		t.Fatal(err)
	}
}

func TestPlannerRuntimeAmendmentPreservesOriginalContractAndBudget(t *testing.T) {
	task := runtimeContractTask(t)
	originalJSON := task.ExecutionPackageJSON
	amendment := PlannerRemainingAmendment{TaskKey: task.TaskKey, AdditionalReviewCriteria: []string{"The summary preserves the provider's stable first-occurrence order."}}
	raw, digest, err := BuildPlannerAmendedTaskPackage(task, amendment, "event", strings.Repeat("d", 64), 0)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := ParseComplexStandardExecutionPackage([]byte(originalJSON))
	after, err := ParseComplexStandardExecutionPackage(raw)
	if err != nil || digest == task.ExecutionPackageSHA256 || after.RuntimeRevision == nil || after.RuntimeRevision.PreviousPackageSHA256 != task.ExecutionPackageSHA256 {
		t.Fatalf("new contract not bound: %#v %v", after, err)
	}
	if task.ExecutionPackageJSON != originalJSON || len(after.ReviewCriteria) != len(before.ReviewCriteria)+1 {
		t.Fatal("amendment changed the input or did not actually revise the review contract")
	}
	after.ReviewCriteria, after.RuntimeRevision = before.ReviewCriteria, nil
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("amendment changed more than the added criteria: before=%#v after=%#v", before, after)
	}
	for name, mutate := range map[string]func(*ComplexExecutionTask){
		"dispatched": func(v *ComplexExecutionTask) { v.CurrentDispatchID = "reserved-before-send" },
		"running":    func(v *ComplexExecutionTask) { v.Status = DevelopmentTaskStatusRunning },
		"reviewed":   func(v *ComplexExecutionTask) { v.Status = DevelopmentTaskStatusDone },
		"attempted":  func(v *ComplexExecutionTask) { v.ReworkCount = 1 },
		"new round":  func(v *ComplexExecutionTask) { v.CurrentRound = 1 },
		"wrong hash": func(v *ComplexExecutionTask) { v.ExecutionPackageSHA256 = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			copyTask := task
			mutate(&copyTask)
			if _, _, err := BuildPlannerAmendedTaskPackage(copyTask, amendment, "event", strings.Repeat("d", 64), 0); err == nil {
				t.Fatal("unsafe amendment accepted")
			}
		})
	}
	amendment.AdditionalReviewCriteria = []string{before.ReviewCriteria[0]}
	if _, _, err := BuildPlannerAmendedTaskPackage(task, amendment, "event", strings.Repeat("d", 64), 0); err == nil {
		t.Fatal("empty effective revision accepted")
	}
}
