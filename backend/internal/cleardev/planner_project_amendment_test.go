package cleardev

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func cloneProjectContract(t *testing.T, c ProjectExecutionContract) ProjectExecutionContract {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var out ProjectExecutionContract
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestProjectEngineeringRevisionPreservesAcceptanceAndAuthority(t *testing.T) {
	original := projectExecutionContractFixture(t)
	original.Basis.Trial = &ProjectTrial{SchemaVersion: 1, Service: true, Steps: []ProjectTrialStep{{ID: "browser", Kind: "BROWSER", AcceptanceCriteria: []string{"Persistent notes survive restart."}, Observe: "Create a note, restart, read the same note in the actual browser."}}}
	for name, mutate := range map[string]func(*ProjectExecutionContract){
		"product":             func(c *ProjectExecutionContract) { c.StageID = "other" },
		"write scope":         func(c *ProjectExecutionContract) { c.Basis.WritePaths = append(c.Basis.WritePaths, "secrets/**") },
		"drop check":          func(c *ProjectExecutionContract) { c.Basis.Checks = nil },
		"substitute check":    func(c *ProjectExecutionContract) { c.Basis.Checks[0].Argv = []string{"npm", "run", "always-pass"} },
		"higher timeout":      func(c *ProjectExecutionContract) { c.Basis.Checks[0].TimeoutSeconds++ },
		"drop browser":        func(c *ProjectExecutionContract) { c.Basis.Trial = nil },
		"replace observation": func(c *ProjectExecutionContract) { c.Basis.Trial.Steps[0].Observe = "HTTP 200 suffices" },
		"mock instead of browser": func(c *ProjectExecutionContract) {
			s := &c.Basis.Trial.Steps[0]
			s.Kind = "COMMAND"
			s.Argv = []string{"npm", "test"}
			s.TimeoutSeconds = 60
		},
		"host command": func(c *ProjectExecutionContract) { c.Basis.Launch.Argv = []string{"sh", "-c", "true"} },
	} {
		t.Run(name, func(t *testing.T) {
			next := cloneProjectContract(t, original)
			mutate(&next)
			if err := ValidateProjectEngineeringRevision(original, next); err == nil {
				t.Fatal("accepted weakening or authority change")
			}
		})
	}
	next := cloneProjectContract(t, original)
	runtime := next.Runtime
	runtime.PrepareArgv = []string{"npm", "run", "prepare-trial"}
	next.Basis.Runtime = &runtime
	next.Runtime = runtime
	next.Basis.Trial.Steps = append(next.Basis.Trial.Steps, ProjectTrialStep{ID: "cleanup", Kind: "HTTP", AcceptanceCriteria: []string{"Persistent notes survive restart."}, Observe: "Release the project-owned fixture and confirm normal operation."})
	if err := ValidateProjectEngineeringRevision(original, next); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(original, next) {
		t.Fatal("fixture did not amend the agreement")
	}
}

func TestProjectPlannerPolicyRecognizesOriginalAdmissionWithoutRewritingIt(t *testing.T) {
	run := projectExecutionRunFixture(t)
	before := run
	if enabled, err := PlannerRuntimeRun(run); err != nil || !enabled {
		t.Fatalf("valid admitted project excluded: %v %v", enabled, err)
	}
	if !reflect.DeepEqual(before, run) {
		t.Fatal("recognition changed immutable admission")
	}
	run.ExecutionPackageSHA256 = "bad"
	if enabled, err := PlannerRuntimeRun(run); err == nil || enabled {
		t.Fatal("damaged admission enabled coordination")
	}
}

func TestProjectEngineeringLockfileRepairIsOnlyAnAdditiveCompanion(t *testing.T) {
	original := projectExecutionContractFixture(t)
	next := cloneProjectContract(t, original)
	next.Basis.WritePaths = append(next.Basis.WritePaths, "package-lock.json")
	next.Basis.Checks[0].MainPaths = append(next.Basis.Checks[0].MainPaths, "package-lock.json")
	if err := ValidateProjectEngineeringRevision(original, next); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ProjectExecutionContract, *ProjectExecutionContract){
		"other path": func(_, n *ProjectExecutionContract) { n.Basis.WritePaths = append(n.Basis.WritePaths, "secret.txt") },
		"other coverage": func(_, n *ProjectExecutionContract) {
			n.Basis.Checks[0].MainPaths = append(n.Basis.Checks[0].MainPaths, "secret.txt")
		},
		"changed command": func(_, n *ProjectExecutionContract) { n.Basis.Checks[0].Argv = []string{"npm", "run", "skip"} },
		"no dependencies": func(o, _ *ProjectExecutionContract) { o.Basis.DependencyNeeds = nil },
		"no manifest": func(o, _ *ProjectExecutionContract) {
			o.Basis.WritePaths = o.Basis.WritePaths[:len(o.Basis.WritePaths)-1]
		},
		"reorder": func(_, n *ProjectExecutionContract) {
			n.Basis.WritePaths[0], n.Basis.WritePaths[1] = n.Basis.WritePaths[1], n.Basis.WritePaths[0]
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, n := cloneProjectContract(t, original), cloneProjectContract(t, next)
			mutate(&o, &n)
			if ValidateProjectEngineeringRevision(o, n) == nil {
				t.Fatal("accepted authority change")
			}
		})
	}
}

func TestProjectEngineeringLockfileRepairDerivesOnlyDependencyTaskPermission(t *testing.T) {
	value, coverage := genericPlanFixture(t)
	raw, _ := json.Marshal(value)
	original := projectExecutionContractFixture(t)
	plan, _, _, err := ParseProjectEngineeringPlanResult(raw, "request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64), coverage, original.Basis)
	if err != nil {
		t.Fatal(err)
	}
	next := cloneProjectContract(t, original)
	next.Basis.WritePaths = append(next.Basis.WritePaths, "package-lock.json")
	next.Basis.Checks[0].MainPaths = append(next.Basis.Checks[0].MainPaths, "package-lock.json")
	for _, consumer := range []bool{false, true} {
		taskPlan := plan.Tasks[0]
		if consumer {
			taskPlan.WritePaths = []string{"src/**", "tests/**"}
		}
		_, encoded, digest, err := BuildComplexStandardExecutionPackageWithCatalog(WorkModeStandard, ComplexStandardExecutionPackageInput{
			ExecutionRunID: original.ExecutionRunID, RequirementVersionID: original.RequirementVersionID, RequirementSHA256: original.RequirementSHA256,
			RequirementText: "Create a note and retain it across a restart.", PlanID: original.PlanID, PlanSHA256: original.PlanSHA256, TaskSetVersion: ComplexStandardTaskSetVersion,
			TaskID: "task", DependencyTaskIDs: []string{}, Task: taskPlan, PlanSchemaVersion: ProjectPlanningVersion, InterfaceContracts: plan.InterfaceContracts, ProjectExecution: &original,
		}, original.Basis.CheckCatalog())
		if err != nil {
			t.Fatal(err)
		}
		task := ComplexExecutionTask{TaskKey: taskPlan.Key, DevelopmentTaskID: "task", ExecutionRunID: original.ExecutionRunID, ExecutionPackageJSON: string(encoded), ExecutionPackageSHA256: digest, Status: DevelopmentTaskStatusBlocked}
		amended, _, err := BuildPlannerAmendedTaskPackage(task, PlannerRemainingAmendment{TaskKey: task.TaskKey, ExecutionBasis: &next.Basis}, "event", strings.Repeat("e", 64), 0)
		if err != nil {
			t.Fatal(err)
		}
		pkg, err := ParseComplexStandardExecutionPackage(amended)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(pkg.WritePaths, "package-lock.json") == consumer {
			t.Fatalf("wrong derived lock permission for consumer=%v: %v", consumer, pkg.WritePaths)
		}
		if task.ExecutionPackageJSON != string(encoded) || pkg.RuntimeRevision.PreviousPackageSHA256 != digest {
			t.Fatal("original package was lost")
		}
	}
}
