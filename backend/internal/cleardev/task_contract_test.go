package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func taskContractFixture(t *testing.T) (map[string]any, ComplexCoverage) {
	t.Helper()
	provider := planTask("contact-api", []string{"REQ-001"}, []string{"ACC-001"}, []string{"backend/src/server.ts"}, []string{"demo-api"}, []string{})
	provider["reviewCriteria"] = []string{"GET /api/contact-count returns the persisted count, including zero; existing health JSON is unchanged."}
	consumer := planTask("contact-page", []string{"REQ-002"}, []string{"ACC-002"}, []string{"frontend/app.js"}, []string{"demo-frontend"}, []string{"contact-api"})
	consumer["reviewCriteria"] = []string{"The page displays the API count after refresh and presents failures without inventing a count."}
	return map[string]any{
			"schemaVersion": PlannerTaskContractVersion, "kind": "COMPLEX_ENGINEERING_PLAN",
			"technicalApproach": "The existing local server provides the count; the existing page consumes its stable response. Builders choose their implementation.",
			"tasks":             []any{provider, consumer}, "integrationCheckIds": []string{"demo-integration"},
			"parallelSuggestion": map[string]any{"recommendedBuilderCount": 1, "reason": "The page consumes the verified API candidate."},
			"risks":              []string{},
			"interfaceContracts": []any{map[string]any{
				"key": "contact-count", "providerTaskKey": "contact-api", "consumerTaskKeys": []string{"contact-page"},
				"expectations": []string{"GET /api/contact-count returns 200 JSON {contactCount: nonnegative integer}; it does not mutate contacts or change /api/health."},
			}},
		}, ComplexCoverage{
			MUSTIDs: []string{"REQ-001", "REQ-002"}, RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST"},
			AcceptanceIDs: map[string]string{"ACC-001": "Persisted count API", "ACC-002": "Count displayed on page"},
		}
}

func parseTaskContractFixture(t *testing.T, value map[string]any, coverage ComplexCoverage) (ComplexEngineeringPlanResult, []byte, string, error) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return ParsePlannerTaskContractResult(raw, "planning-1", "version-1", strings.Repeat("a", 64), strings.Repeat("b", 64), coverage, FrozenComplexCheckCatalog())
}

func TestPlannerTaskContractSingleDependencyAndParallel(t *testing.T) {
	for _, scenario := range []string{"single", "dependency", "independent", "parallel"} {
		t.Run(scenario, func(t *testing.T) {
			value, coverage := taskContractFixture(t)
			tasks := value["tasks"].([]any)
			if scenario == "single" {
				task := tasks[0].(map[string]any)
				task["requirementIds"] = []string{"REQ-001", "REQ-002"}
				task["acceptanceIds"] = []string{"ACC-001", "ACC-002"}
				value["tasks"] = []any{task}
				delete(value, "interfaceContracts")
			}
			if scenario == "independent" || scenario == "parallel" {
				tasks[1].(map[string]any)["dependencyKeys"] = []string{}
				delete(value, "interfaceContracts")
			}
			if scenario == "parallel" {
				value["parallelSuggestion"].(map[string]any)["recommendedBuilderCount"] = 2
			}
			plan, raw, digest, err := parseTaskContractFixture(t, value, coverage)
			if err != nil {
				t.Fatal(err)
			}
			if digest != sha256Hex(raw) || len(plan.Tasks[0].ReviewCriteria) == 0 {
				t.Fatal("contract was not included in the immutable plan")
			}
			selection, err := SelectComplexExecutionMode(plan.Tasks, plan.ParallelSuggestion.RecommendedBuilderCount)
			if err != nil || (scenario == "parallel" && selection.BuilderCount != 2) {
				t.Fatalf("mode selection: %+v %v", selection, err)
			}
		})
	}
}

func TestPlannerTaskContractCanonicalDependencyOrder(t *testing.T) {
	value, coverage := taskContractFixture(t)
	tasks := value["tasks"].([]any)
	export := planTask("contact-export", []string{"REQ-002"}, []string{"ACC-002"}, []string{"frontend/export.js"}, []string{"demo-frontend"}, []string{"contact-page"})
	export["reviewCriteria"] = []string{"Export preserves the count semantics supplied by the page."}
	tasks = append(tasks, export)
	var wantJSON, wantDigest string
	for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		value["tasks"] = []any{tasks[order[0]], tasks[order[1]], tasks[order[2]]}
		plan, raw, digest, err := parseTaskContractFixture(t, value, coverage)
		if err != nil {
			t.Fatal(err)
		}
		for i, key := range []string{"contact-api", "contact-page", "contact-export"} {
			if plan.Tasks[i].Key != key {
				t.Fatalf("order %v cannot be materialized: %+v", order, plan.Tasks)
			}
		}
		if wantJSON == "" {
			wantJSON, wantDigest = string(raw), digest
		}
		if string(raw) != wantJSON || digest != wantDigest {
			t.Fatalf("equivalent dependency order changed the canonical contract: %v", order)
		}
		if err := NormalizeAndValidatePlannerTaskContracts(&plan, coverage, FrozenComplexCheckCatalog()); err != nil {
			t.Fatal(err)
		}
		replayed, err := marshalCanonicalJSON(plan)
		if err != nil || string(replayed) != wantJSON {
			t.Fatalf("normalization changed a frozen contract on replay: %v", err)
		}
	}

	// Independent tasks retain the Planner's original order, not key sorting.
	value, coverage = taskContractFixture(t)
	tasks = value["tasks"].([]any)
	tasks[1].(map[string]any)["dependencyKeys"] = []string{}
	value["tasks"] = []any{tasks[1], tasks[0]}
	delete(value, "interfaceContracts")
	plan, _, _, err := parseTaskContractFixture(t, value, coverage)
	if err != nil || plan.Tasks[0].Key != "contact-page" || plan.Tasks[1].Key != "contact-api" {
		t.Fatalf("independent order was changed: %+v %v", plan.Tasks, err)
	}
}

func TestPlannerTaskContractRejectsInvalidAdmission(t *testing.T) {
	cases := map[string]func(map[string]any, *ComplexCoverage){
		"missing criteria": func(v map[string]any, _ *ComplexCoverage) {
			delete(v["tasks"].([]any)[0].(map[string]any), "reviewCriteria")
		},
		"empty criteria": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[0].(map[string]any)["reviewCriteria"] = []string{}
		},
		"blank criteria": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[0].(map[string]any)["reviewCriteria"] = []string{"  "}
		},
		"duplicate criteria": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[0].(map[string]any)["reviewCriteria"] = []string{"count", " count "}
		},
		"null criteria": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[0].(map[string]any)["reviewCriteria"] = nil
		},
		"recipe field": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[0].(map[string]any)["implementationSteps"] = []string{"edit line 10"}
		},
		"acceptance hole": func(_ map[string]any, c *ComplexCoverage) { c.AcceptanceIDs["ACC-003"] = "No task covers this" },
		"three builders": func(v map[string]any, _ *ComplexCoverage) {
			v["parallelSuggestion"].(map[string]any)["recommendedBuilderCount"] = 3
		},
		"unknown check": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[0].(map[string]any)["requiredCheckIds"] = []string{"arbitrary-command"}
		},
		"missing dependency": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[1].(map[string]any)["dependencyKeys"] = []string{}
		},
		"dependency cycle": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[0].(map[string]any)["dependencyKeys"] = []string{"contact-page"}
		},
		"unknown provider": func(v map[string]any, _ *ComplexCoverage) {
			v["interfaceContracts"].([]any)[0].(map[string]any)["providerTaskKey"] = "missing"
		},
		"unknown consumer": func(v map[string]any, _ *ComplexCoverage) {
			v["interfaceContracts"].([]any)[0].(map[string]any)["consumerTaskKeys"] = []string{"missing"}
		},
		"self consumer": func(v map[string]any, _ *ComplexCoverage) {
			v["interfaceContracts"].([]any)[0].(map[string]any)["consumerTaskKeys"] = []string{"contact-api"}
		},
		"empty interface": func(v map[string]any, _ *ComplexCoverage) {
			v["interfaceContracts"].([]any)[0].(map[string]any)["expectations"] = []string{}
		},
		"unknown interface field": func(v map[string]any, _ *ComplexCoverage) {
			v["interfaceContracts"].([]any)[0].(map[string]any)["rpcSchema"] = "unapproved"
		},
		"legacy downgrade": func(v map[string]any, _ *ComplexCoverage) { v["schemaVersion"] = 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			value, coverage := taskContractFixture(t)
			mutate(value, &coverage)
			if _, _, _, err := parseTaskContractFixture(t, value, coverage); err == nil {
				t.Fatal("invalid contract was admitted")
			}
		})
	}
}

func TestTaskContractPackageBindsCriteriaInterfacesAndPlan(t *testing.T) {
	value, coverage := taskContractFixture(t)
	plan, _, planSHA, err := parseTaskContractFixture(t, value, coverage)
	if err != nil {
		t.Fatal(err)
	}
	input := ComplexStandardExecutionPackageInput{
		ExecutionRunID: "run-1", RequirementVersionID: "version-1", RequirementSHA256: plan.RequirementVersionSHA256,
		RequirementText: "The confirmed Stage", PlanID: "plan-1", PlanSHA256: planSHA, TaskSetVersion: 1,
		TaskID: "task-page", DependencyTaskIDs: []string{"task-api"}, Task: plan.Tasks[1],
		PlanSchemaVersion: plan.SchemaVersion, InterfaceContracts: plan.InterfaceContracts,
	}
	pkg, raw, digest, err := BuildComplexStandardExecutionPackage(WorkModeStandard, input)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseComplexStandardExecutionPackage(raw)
	if err != nil || parsed.SchemaVersion != 2 || len(parsed.InterfaceContracts) != 1 || parsed.ReviewCriteria[0] != input.Task.ReviewCriteria[0] {
		t.Fatalf("contract packet roundtrip: %+v %v", parsed, err)
	}
	input.Task.ReviewCriteria = []string{"New completion obligation"}
	_, _, changed, err := BuildComplexStandardExecutionPackage(WorkModeStandard, input)
	if err != nil || changed == digest {
		t.Fatal("criterion change reused the old task package digest")
	}
	pkg.DependencyTaskKeys = []string{}
	pkg.DependencyTaskIDs = []string{}
	if err := ValidateComplexStandardExecutionPackage(pkg); err == nil {
		t.Fatal("consumer packet without provider dependency accepted")
	}
	value["interfaceContracts"].([]any)[0].(map[string]any)["expectations"] = []string{"A different response contract"}
	_, _, newPlanSHA, err := parseTaskContractFixture(t, value, coverage)
	if err != nil || newPlanSHA == planSHA {
		t.Fatal("interface change reused the old plan digest")
	}
}

func TestPlannerProductClarificationIsBoundAndNonExecutable(t *testing.T) {
	raw := []byte(`{"schemaVersion":2,"kind":"PRODUCT_CLARIFICATION_REQUIRED","summary":"The persistence scope is unspecified.","questions":["Must the chosen view survive an application restart?"]}`)
	result, encoded, digest, err := ParsePlannerProductClarification(raw, "request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	plan := ComplexEngineeringPlan{PlanningRequestID: "request", RequirementVersionID: "version", RequirementSHA256: result.RequirementVersionSHA256, CompilationSHA256: result.CompilationSHA256, PlanJSON: string(encoded), PlanSHA256: digest}
	if _, ok := PlannerClarificationForPlan(plan); !ok {
		t.Fatal("product clarification lost its exact Stage binding")
	}
	plan.RequirementVersionID = "different-version"
	if _, ok := PlannerClarificationForPlan(plan); ok {
		t.Fatal("product question accepted for a different Stage")
	}
	if _, _, _, err := ParsePlannerTaskContractResult(raw, "request", "version", result.RequirementVersionSHA256, result.CompilationSHA256, ComplexCoverage{}, FrozenComplexCheckCatalog()); err == nil {
		t.Fatal("product clarification is not an executable plan")
	}
}
