package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func projectBasisFixture() ProjectExecutionBasis {
	return ProjectExecutionBasis{
		WritePaths:      []string{"src/**", "tests/**", "migrations/**", "package.json"},
		DependencyNeeds: []string{"Add the project's chosen local SQLite driver; do not install it during discussion."},
		Checks:          []ProjectCheckSpec{{ID: "project-tests", Argv: []string{"npm", "test"}, TimeoutSeconds: 120, MainPaths: []string{"src/**", "tests/**", "migrations/**", "package.json"}}},
		Launch:          ProjectLaunch{Argv: []string{"npm", "start"}, WorkingDirectory: ".", Description: "Start the local application after future execution support is available."},
	}
}

func projectProposalFixture(t *testing.T) map[string]any {
	t.Helper()
	basis := projectBasisFixture()
	return map[string]any{
		"schemaVersion": 2, "kind": "PRODUCT_DISCOVERY", "outcome": "READY", "message": "Use the selected local notes project.",
		"feasibilitySummary": "The baseline is bound by the backend; persistence is proposed, not tested.",
		"questions":          []any{}, "features": []any{map[string]any{"key": "notes", "title": "Local notes", "description": "Create and read a note."}},
		"stages":            []any{map[string]any{"key": "notes", "title": "Notes", "goal": "Save and retrieve a note.", "featureKeys": []string{"notes"}, "acceptanceCriteria": []string{"A saved note survives a restart."}, "nonGoals": []string{"No remote accounts."}, "feasibility": "NEEDS_CAPABILITY", "feasibilityReason": "Generic execution is not yet available; planning is supported.", "executionBasis": basis}},
		"options":           []any{map[string]any{"key": "empty", "title": "Start from an empty project", "origin": "EMPTY", "description": "Build the minimal notes application.", "tradeoffs": []string{"No inherited code; persistence must be implemented."}}},
		"selectedOptionKey": "empty", "evidence": []any{},
	}
}

func TestProjectDiscoveryRequiresExplicitOptionsAndTruthLabels(t *testing.T) {
	proposal := projectProposalFixture(t)
	data, _ := json.Marshal(proposal)
	parsed, err := ParseProductDiscoveryResult(data)
	if err != nil || parsed.Stages[0].ExecutionBasis == nil {
		t.Fatalf("project proposal: %+v %v", parsed, err)
	}
	proposal["outcome"], proposal["stages"], proposal["features"] = "DISCUSS", []any{}, []any{}
	delete(proposal, "selectedOptionKey")
	data, _ = json.Marshal(proposal)
	if _, err := ParseProductDiscoveryResult(data); err != nil {
		t.Fatalf("a real option comparison does not require a forced question: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"ready without choice": func(v map[string]any) { delete(v, "selectedOptionKey") },
		"missing options":      func(v map[string]any) { delete(v, "options") },
		"missing evidence":     func(v map[string]any) { delete(v, "evidence") },
		"invented confirmed fact": func(v map[string]any) {
			v["evidence"] = []any{map[string]any{"status": "CONFIRMED", "claim": "All tests pass", "source": "not run"}}
		},
		"observation without source": func(v map[string]any) {
			v["evidence"] = []any{map[string]any{"status": "OBSERVED", "claim": "There is a notes module", "source": ""}}
		},
		"credentials in URL": func(v map[string]any) {
			v["options"].([]any)[0].(map[string]any)["repositoryUrl"] = "https://token@example.org/notes.git"
		},
		"discovered without URL":  func(v map[string]any) { v["options"].([]any)[0].(map[string]any)["origin"] = "DISCOVERED" },
		"legacy reinterpretation": func(v map[string]any) { v["schemaVersion"] = 1 },
		"unknown grant":           func(v map[string]any) { v["approved"] = true },
		"missing basis":           func(v map[string]any) { delete(v["stages"].([]any)[0].(map[string]any), "executionBasis") },
	} {
		t.Run(name, func(t *testing.T) {
			v := projectProposalFixture(t)
			mutate(v)
			raw, _ := json.Marshal(v)
			if _, err := ParseProductDiscoveryResult(raw); err == nil {
				t.Fatal("invalid proposal accepted")
			}
		})
	}
}

func TestProjectExecutionBasisBoundsCommandsAndPaths(t *testing.T) {
	for _, p := range []string{"src/**", "package.json", "go.mod", "migrations/001.sql", "tests/test_notes.py"} {
		if !ProjectPath(p, false) {
			t.Errorf("safe proposed path rejected: %s", p)
		}
	}
	for _, p := range []string{"", ".", "**", "../outside", "src/../../outside", "/tmp/file", "C:/file", "src\\file", "src/*", ".git/**", "src/.git/config", "src//file", "src/./file"} {
		if ProjectPath(p, false) {
			t.Errorf("unsafe path accepted: %s", p)
		}
	}
	for name, mutate := range map[string]func(*ProjectExecutionBasis){
		"no check":          func(b *ProjectExecutionBasis) { b.Checks = nil },
		"empty argv":        func(b *ProjectExecutionBasis) { b.Checks[0].Argv = []string{} },
		"duplicate check":   func(b *ProjectExecutionBasis) { b.Checks = append(b.Checks, b.Checks[0]) },
		"unsafe write":      func(b *ProjectExecutionBasis) { b.WritePaths = []string{"../outside"} },
		"unsafe check":      func(b *ProjectExecutionBasis) { b.Checks[0].MainPaths = []string{".git/**"} },
		"uncovered write":   func(b *ProjectExecutionBasis) { b.Checks[0].MainPaths = []string{"src/**", "tests/**", "package.json"} },
		"unbounded timeout": func(b *ProjectExecutionBasis) { b.Checks[0].TimeoutSeconds = 3601 },
		"unknown launch":    func(b *ProjectExecutionBasis) { b.Launch.Argv = nil },
		"launch traversal":  func(b *ProjectExecutionBasis) { b.Launch.WorkingDirectory = "../outside" },
		"command NUL":       func(b *ProjectExecutionBasis) { b.Launch.Argv = []string{"run\x00extra"} },
	} {
		t.Run(name, func(t *testing.T) {
			basis := projectBasisFixture()
			mutate(&basis)
			if err := ValidateProjectExecutionBasis(basis); err == nil {
				t.Fatal("invalid project basis accepted")
			}
		})
	}
	basis := projectBasisFixture()
	basis.Launch.Argv = []string{}
	basis.Launch.Description = "Library only; no application process is needed."
	if err := ValidateProjectExecutionBasis(basis); err != nil {
		t.Fatalf("an explicit no-process launch is valid: %v", err)
	}
}

func genericPlanFixture(t *testing.T) (map[string]any, ComplexCoverage) {
	t.Helper()
	v, coverage := taskContractFixture(t)
	v["schemaVersion"] = ProjectPlanningVersion
	v["integrationCheckIds"] = []string{"project-tests"}
	tasks := v["tasks"].([]any)
	for i, task := range tasks {
		t := task.(map[string]any)
		t["requiredCheckIds"] = []string{"project-tests"}
		t["forbiddenPaths"] = []string{".git/**"}
		if i == 0 {
			t["writePaths"] = []string{"src/storage.ts", "migrations/001.sql", "package.json"}
		} else {
			t["writePaths"] = []string{"src/notes.ts", "tests/notes.test.ts"}
		}
	}
	return v, coverage
}

func TestProjectEngineeringPlanUsesConfirmedBasisWithoutMailGrant(t *testing.T) {
	value, coverage := genericPlanFixture(t)
	parse := func(v map[string]any, b ProjectExecutionBasis, c ComplexCoverage) (ComplexEngineeringPlanResult, []byte, string, error) {
		raw, _ := json.Marshal(v)
		return ParseProjectEngineeringPlanResult(raw, "request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64), c, b)
	}
	plan, data, digest, err := parse(value, projectBasisFixture(), coverage)
	if err != nil || digest != sha256Hex(data) || plan.SchemaVersion != ProjectPlanningVersion {
		t.Fatalf("generic plan: %+v %v", plan, err)
	}
	if err := NormalizeAndValidatePlannerTaskContracts(&plan, coverage, projectBasisFixture().CheckCatalog()); err == nil {
		t.Fatal("planning-only result acquired V2 execution admission")
	}
	for name, mutate := range map[string]func(map[string]any, *ComplexCoverage){
		"outside selected basis": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[0].(map[string]any)["writePaths"] = []string{"secrets/config"}
		},
		"unapproved generated path": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[0].(map[string]any)["generatedPaths"] = []string{"outside/**"}
		},
		"unapproved check":   func(v map[string]any, _ *ComplexCoverage) { v["integrationCheckIds"] = []string{"demo-integration"} },
		"missing acceptance": func(_ map[string]any, c *ComplexCoverage) { c.AcceptanceIDs["ACC-003"] = "Must not disappear" },
		"missing criteria": func(v map[string]any, _ *ComplexCoverage) {
			delete(v["tasks"].([]any)[0].(map[string]any), "reviewCriteria")
		},
		"cycle": func(v map[string]any, _ *ComplexCoverage) {
			v["tasks"].([]any)[0].(map[string]any)["dependencyKeys"] = []string{"contact-page"}
		},
		"legacy protocol":     func(v map[string]any, _ *ComplexCoverage) { v["schemaVersion"] = 1 },
		"executable protocol": func(v map[string]any, _ *ComplexCoverage) { v["schemaVersion"] = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			v, c := genericPlanFixture(t)
			mutate(v, &c)
			if _, _, _, err := parse(v, projectBasisFixture(), c); err == nil {
				t.Fatal("invalid generic plan accepted")
			}
		})
	}
	tasks := value["tasks"].([]any)
	value["tasks"] = []any{tasks[1], tasks[0]}
	ordered, _, reorderedDigest, err := parse(value, projectBasisFixture(), coverage)
	if err != nil || ordered.Tasks[0].Key != "contact-api" || digest != reorderedDigest {
		t.Fatalf("generic dependency order lost: %+v %v", ordered, err)
	}
}
