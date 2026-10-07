package cleardev

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func plannerContractCoverage() ComplexCoverage {
	return ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002", "REQ-003"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST", "REQ-003": "MUST"},
		AcceptanceIDs:  map[string]string{"ACC-001": "normalize", "ACC-002": "deduplicate", "ACC-003": "summary"},
	}
}

func plannerContractJSON(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(planJSON(t, nil), &value); err != nil {
		t.Fatal(err)
	}
	value["schemaVersion"] = PlannerTaskContractVersion
	tasks := value["tasks"].([]any)
	tasks[0].(map[string]any)["reviewCriteria"] = []string{"Normalization is stable and duplicate input cannot produce duplicate output."}
	tasks[1].(map[string]any)["reviewCriteria"] = []string{"Summary counts reflect the normalized unique collection, including empty input."}
	value["interfaceContracts"] = []any{map[string]any{
		"key": "normalized-emails", "providerTaskKey": "normalize-and-deduplicate",
		"consumerTaskKeys": []string{"build-summary"},
		"expectations":     []string{"The provider exports a deterministic ordered collection of unique normalized addresses; empty input yields an empty collection."},
	}}
	if mutate != nil {
		mutate(value)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func parsePlannerContract(t *testing.T, raw []byte) (ComplexEngineeringPlanResult, []byte, string) {
	t.Helper()
	plan, canonical, digest, err := ParsePlannerTaskContractResult(raw, "planning-request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64), plannerContractCoverage(), FrozenComplexCheckCatalog())
	if err != nil {
		t.Fatal(err)
	}
	return plan, canonical, digest
}

func TestPlannerTaskContractValidShapes(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(map[string]any)
		count      int
		interfaces int
	}{
		{name: "provider and dependent consumer", count: 2, interfaces: 1},
		{name: "independent parallel tasks", count: 2, mutate: func(p map[string]any) {
			delete(p, "interfaceContracts")
			p["tasks"].([]any)[1].(map[string]any)["dependencyKeys"] = []string{}
			p["parallelSuggestion"] = map[string]any{"recommendedBuilderCount": 2, "reason": "Disjoint independent results can be reviewed and composed independently."}
		}},
		{name: "small single task", count: 1, mutate: func(p map[string]any) {
			delete(p, "interfaceContracts")
			task := p["tasks"].([]any)[0].(map[string]any)
			task["requirementIds"] = []string{"REQ-001", "REQ-002", "REQ-003"}
			task["acceptanceIds"] = []string{"ACC-001", "ACC-002", "ACC-003"}
			p["tasks"] = []any{task}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, canonical, digest := parsePlannerContract(t, plannerContractJSON(t, tc.mutate))
			if len(plan.Tasks) != tc.count || len(plan.InterfaceContracts) != tc.interfaces || len(digest) != 64 {
				t.Fatalf("unexpected contract shape: %#v", plan)
			}
			if tc.interfaces == 0 && strings.Contains(string(canonical), `"interfaceContracts"`) {
				t.Fatal("a plan without shared interfaces acquired an unnecessary contract object")
			}
			if plan.PlanningRequestID != "planning-request" || plan.RequirementVersionID != "version" || plan.RequirementVersionSHA256 != strings.Repeat("a", 64) {
				t.Fatal("control-plane identities were not bound to the plan")
			}
		})
	}
}

func TestPlannerTaskContractRejectsInvalidResults(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing criteria", func(p map[string]any) { delete(p["tasks"].([]any)[0].(map[string]any), "reviewCriteria") }},
		{"empty criteria", func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["reviewCriteria"] = []string{} }},
		{"null criteria", func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["reviewCriteria"] = nil }},
		{"blank criteria", func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["reviewCriteria"] = []string{" \t "} }},
		{"uncovered acceptance", func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["acceptanceIds"] = []string{"ACC-001"} }},
		{"unknown acceptance", func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["acceptanceIds"] = []string{"ACC-404"} }},
		{"unknown check", func(p map[string]any) {
			p["tasks"].([]any)[0].(map[string]any)["requiredCheckIds"] = []string{"arbitrary-command"}
		}},
		{"escaping write scope", func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["writePaths"] = []string{"../outside"} }},
		{"cyclic dependencies", func(p map[string]any) {
			p["tasks"].([]any)[0].(map[string]any)["dependencyKeys"] = []string{"build-summary"}
		}},
		{"unknown provider", func(p map[string]any) {
			p["interfaceContracts"].([]any)[0].(map[string]any)["providerTaskKey"] = "missing"
		}},
		{"unknown consumer", func(p map[string]any) {
			p["interfaceContracts"].([]any)[0].(map[string]any)["consumerTaskKeys"] = []string{"missing"}
		}},
		{"self consumer", func(p map[string]any) {
			p["interfaceContracts"].([]any)[0].(map[string]any)["consumerTaskKeys"] = []string{"normalize-and-deduplicate"}
		}},
		{"consumer without provider dependency", func(p map[string]any) { p["tasks"].([]any)[1].(map[string]any)["dependencyKeys"] = []string{} }},
		{"missing interface expectations", func(p map[string]any) { delete(p["interfaceContracts"].([]any)[0].(map[string]any), "expectations") }},
		{"empty interface expectations", func(p map[string]any) {
			p["interfaceContracts"].([]any)[0].(map[string]any)["expectations"] = []string{}
		}},
		{"duplicate interface identity", func(p map[string]any) {
			p["interfaceContracts"] = append(p["interfaceContracts"].([]any), p["interfaceContracts"].([]any)[0])
		}},
		{"extra interface field", func(p map[string]any) {
			p["interfaceContracts"].([]any)[0].(map[string]any)["implementationSteps"] = []string{"Do exactly this."}
		}},
		{"three builders", func(p map[string]any) {
			p["parallelSuggestion"] = map[string]any{"recommendedBuilderCount": 3, "reason": "Not an approved production bound."}
		}},
		{"legacy protocol cannot stand in for contract", func(p map[string]any) { p["schemaVersion"] = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := plannerContractJSON(t, tc.mutate)
			if _, _, _, err := ParsePlannerTaskContractResult(raw, "planning-request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64), plannerContractCoverage(), FrozenComplexCheckCatalog()); err == nil {
				t.Fatal("invalid Task Contract was admitted")
			}
		})
	}
}

func TestPlannerTaskContractPackageCarriesExactProviderAndConsumerFacts(t *testing.T) {
	plan, _, digest := parsePlannerContract(t, plannerContractJSON(t, nil))
	for i, task := range plan.Tasks {
		dependencies := []string{}
		if i == 1 {
			dependencies = []string{"task-provider"}
		}
		pkg, raw, packageDigest, err := BuildComplexStandardExecutionPackage(WorkModeStandard, ComplexStandardExecutionPackageInput{
			ExecutionRunID: "run", RequirementVersionID: "version", RequirementSHA256: strings.Repeat("a", 64), RequirementText: "Confirmed Stage acceptance.",
			PlanID: "plan", PlanSHA256: digest, TaskSetVersion: ComplexStandardTaskSetVersion, TaskID: "task-" + task.Key,
			DependencyTaskIDs: dependencies, Task: task, PlanSchemaVersion: plan.SchemaVersion, InterfaceContracts: plan.InterfaceContracts,
		})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseComplexStandardExecutionPackage(raw)
		if err != nil {
			t.Fatal(err)
		}
		if pkg.SchemaVersion != PlannerTaskContractVersion || !slices.Equal(parsed.ReviewCriteria, task.ReviewCriteria) || len(parsed.InterfaceContracts) != 1 || parsed.PlanSHA256 != digest || len(packageDigest) != 64 {
			t.Fatalf("task %s lost its immutable contract: %s", task.Key, raw)
		}
	}
	if got := InterfaceContractsForTask(plan.InterfaceContracts, "unrelated-task"); len(got) != 0 {
		t.Fatalf("unrelated task received interfaces: %#v", got)
	}
}

func TestPlannerTaskContractChangesAffectPlanAndPackageHashes(t *testing.T) {
	plan, _, originalDigest := parsePlannerContract(t, plannerContractJSON(t, nil))
	changed, _, changedDigest := parsePlannerContract(t, plannerContractJSON(t, func(p map[string]any) {
		p["interfaceContracts"].([]any)[0].(map[string]any)["expectations"] = []string{"The ordered collection now uses an explicitly different ordering guarantee."}
	}))
	if originalDigest == changedDigest {
		t.Fatal("a changed interface reused the old plan hash")
	}
	build := func(p ComplexEngineeringPlanResult, digest string) string {
		t.Helper()
		_, _, hash, err := BuildComplexStandardExecutionPackage(WorkModeStandard, ComplexStandardExecutionPackageInput{
			ExecutionRunID: "run", RequirementVersionID: "version", RequirementSHA256: strings.Repeat("a", 64), RequirementText: "Confirmed Stage acceptance.",
			PlanID: "plan", PlanSHA256: digest, TaskSetVersion: ComplexStandardTaskSetVersion, TaskID: "task-provider", Task: p.Tasks[0],
			PlanSchemaVersion: p.SchemaVersion, InterfaceContracts: p.InterfaceContracts,
		})
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	if build(plan, originalDigest) == build(changed, changedDigest) {
		t.Fatal("changed interface reused the old task package hash")
	}
	criteriaChanged, _, criteriaDigest := parsePlannerContract(t, plannerContractJSON(t, func(p map[string]any) {
		p["tasks"].([]any)[0].(map[string]any)["reviewCriteria"] = []string{"Normalization must additionally preserve a newly specified compatibility behavior."}
	}))
	if originalDigest == criteriaDigest || build(plan, originalDigest) == build(criteriaChanged, criteriaDigest) {
		t.Fatal("changed Task Review Criteria reused old immutable bindings")
	}
}

func TestPlannerProductClarificationIsBoundAndCannotBeExecuted(t *testing.T) {
	raw := []byte(`{"schemaVersion":2,"kind":"PRODUCT_CLARIFICATION_REQUIRED","summary":"The Stage does not define retention semantics.","questions":["Should removed contacts be recoverable?"]}`)
	result, canonical, digest, err := ParsePlannerProductClarification(raw, "planning-request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	plan := ComplexEngineeringPlan{ID: "plan", PlanningRequestID: result.PlanningRequestID, RequirementVersionID: result.RequirementVersionID, RequirementSHA256: result.RequirementVersionSHA256, CompilationSHA256: result.CompilationSHA256, PlanJSON: string(canonical), PlanSHA256: digest}
	if clarification, ok := PlannerClarificationForPlan(plan); !ok || !slices.Equal(clarification.Questions, result.Questions) {
		t.Fatal("clarification lost its exact planning request binding")
	}
	if _, _, _, err := ParsePlannerTaskContractResult(raw, "planning-request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64), plannerContractCoverage(), FrozenComplexCheckCatalog()); err == nil {
		t.Fatal("product clarification became an executable plan")
	}
	plan.RequirementVersionID = "different-version"
	if _, ok := PlannerClarificationForPlan(plan); ok {
		t.Fatal("clarification was reused for another version")
	}
}
