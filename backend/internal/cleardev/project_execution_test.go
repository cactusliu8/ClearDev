package cleardev

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func projectExecutionContractFixture(t *testing.T) ProjectExecutionContract {
	t.Helper()
	basis := projectBasisFixture()
	base := strings.Repeat("c", 40)
	contract, err := BuildProjectExecutionContract(ProjectExecutionAdmission{
		RequestID: "explicit-start", PlanID: "plan", PlanSHA256: strings.Repeat("a", 64),
		RequirementSHA256: strings.Repeat("b", 64), BaseCommitSHA: base,
	}, ProductStage{
		ID: "stage", ProductID: "product", DiscussionID: "discussion", DevelopmentRequirementID: "requirement",
		DefinitionSHA256: strings.Repeat("d", 64), BaseCommitSHA: base,
		Selection: &ProductSelection{AOProjectID: "project", RepositoryPath: "/managed/notes", BaseCommitSHA: base,
			SourceDiscussionID: "proposal", Reason: "Build the selected local notes application.",
			Option: ProductOption{Key: "empty", Title: "Local notes", Origin: "EMPTY", Description: "Create and retain notes.", Tradeoffs: []string{"Implement persistence from an empty baseline."}}},
		Definition: ProductStageDefinition{ExecutionBasis: &basis},
	}, "run", "version")
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func projectExecutionRunFixture(t *testing.T) ComplexExecutionRun {
	t.Helper()
	contract := projectExecutionContractFixture(t)
	raw, _, err := BuildComplexExecutionRunPackage(WorkModeStandard, "run", "version", contract.RequirementSHA256, "plan", contract.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	raw, digest, err := BindProjectExecutionPolicy(raw, contract)
	if err != nil {
		t.Fatal(err)
	}
	return ComplexExecutionRun{ID: "run", DevelopmentRequirementID: "requirement", RequirementVersionID: "version",
		RequirementSHA256: contract.RequirementSHA256, PlanID: "plan", PlanSHA256: contract.PlanSHA256,
		Mode: WorkModeStandard, FixedBuilderCount: 1, TaskSetVersion: ComplexStandardTaskSetVersion,
		ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest}
}

func TestProjectExecutionGrantRequiresExactImmutableAdmission(t *testing.T) {
	run := projectExecutionRunFixture(t)
	contract, required, err := ProjectContractFromRun(run)
	if err != nil || !required || contract.RequestID != "explicit-start" || contract.Basis.Launch.Argv[0] != "npm" {
		t.Fatalf("project grant: %+v %v %v", contract, required, err)
	}
	for name, mutate := range map[string]func(*ComplexExecutionRun, *ComplexExecutionRunPackage){
		"missing explicit request": func(_ *ComplexExecutionRun, p *ComplexExecutionRunPackage) { p.ProjectExecution.RequestID = "" },
		"changed selected source": func(_ *ComplexExecutionRun, p *ComplexExecutionRunPackage) {
			p.ProjectExecution.Selection.BaseCommitSHA = strings.Repeat("e", 40)
		},
		"different plan": func(r *ComplexExecutionRun, _ *ComplexExecutionRunPackage) { r.PlanID = "new-plan" },
		"different spec": func(r *ComplexExecutionRun, _ *ComplexExecutionRunPackage) {
			r.RequirementSHA256 = strings.Repeat("f", 64)
		},
		"missing final review":  func(_ *ComplexExecutionRun, p *ComplexExecutionRunPackage) { p.FinalReviewPolicy = "" },
		"mail reinterpretation": func(_ *ComplexExecutionRun, p *ComplexExecutionRunPackage) { p.DeliveryPolicy = MailDeliveryPolicyV2 },
		"partial grant":         func(_ *ComplexExecutionRun, p *ComplexExecutionRunPackage) { p.ProjectExecution = nil },
		"additional builders":   func(r *ComplexExecutionRun, _ *ComplexExecutionRunPackage) { r.FixedBuilderCount = 2 },
		"unavailable runtime": func(_ *ComplexExecutionRun, p *ComplexExecutionRunPackage) {
			p.ProjectExecution.Runtime.Environment = "PYTHON"
		},
		"changed runtime command": func(_ *ComplexExecutionRun, p *ComplexExecutionRunPackage) {
			p.ProjectExecution.Basis.Checks[0].Argv = []string{"sh", "-c", "true"}
		},
		"changed data binding": func(_ *ComplexExecutionRun, p *ComplexExecutionRunPackage) {
			p.ProjectExecution.Runtime.DataDirectoryVariable = "ANOTHER_DATA_DIR"
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := projectExecutionRunFixture(t)
			var p ComplexExecutionRunPackage
			if err := json.Unmarshal([]byte(r.ExecutionPackageJSON), &p); err != nil {
				t.Fatal(err)
			}
			mutate(&r, &p)
			raw, err := marshalCanonicalJSON(p)
			if err != nil {
				t.Fatal(err)
			}
			r.ExecutionPackageJSON, r.ExecutionPackageSHA256 = string(raw), sha256Hex(raw)
			if _, granted, err := ProjectContractFromRun(r); err == nil || granted {
				t.Fatalf("invalid grant accepted: granted=%v err=%v", granted, err)
			}
		})
	}
	run.ExecutionPackageSHA256 = strings.Repeat("f", 64)
	if _, granted, err := ProjectContractFromRun(run); err == nil || granted {
		t.Fatal("changed package digest accepted")
	}
}

func TestProjectExecutionDoesNotUpgradeHistoricalRuns(t *testing.T) {
	raw, digest, err := BuildComplexExecutionRunPackage(WorkModeStandard, "run", "version", strings.Repeat("a", 64), "plan", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, granted, err := ProjectContractFromRun(ComplexExecutionRun{ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest}); err != nil || granted {
		t.Fatalf("historical run acquired project execution: %v %v", granted, err)
	}
	if _, _, err := BindProjectExecutionPolicy(raw, projectExecutionContractFixture(t)); err == nil {
		t.Fatal("a different plan/spec acquired a project grant")
	}
	run := projectExecutionRunFixture(t)
	if _, _, err := BindProjectExecutionPolicy([]byte(run.ExecutionPackageJSON), projectExecutionContractFixture(t)); err == nil {
		t.Fatal("an already bound run acquired a second grant")
	}
	if _, required, err := MailPolicyFromRun(run); err != nil || required {
		t.Fatalf("project run silently acquired a mail policy: %v %v", required, err)
	}
}

func TestProjectExecutionKeepsAllConfirmedChecksAtIntegration(t *testing.T) {
	value, coverage := genericPlanFixture(t)
	raw, _ := json.Marshal(value)
	basis := projectBasisFixture()
	plan, _, _, err := ParseProjectEngineeringPlanResult(raw, "request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64), coverage, basis)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateExecutableProjectPlan(plan, basis); err != nil {
		t.Fatal(err)
	}
	plan.Tasks[0].SharedPathsRequireApproval = []string{"package.json"}
	if err := ValidateExecutableProjectPlan(plan, basis); err == nil || !strings.Contains(err.Error(), "shared-path approval") {
		t.Fatalf("unserviceable shared-path approval entered execution: %v", err)
	}
	plan.Tasks[0].SharedPathsRequireApproval = nil
	basis.Checks = append(basis.Checks, ProjectCheckSpec{ID: "smoke", Argv: []string{"node", "tests/smoke.mjs"}, TimeoutSeconds: 30, MainPaths: []string{"src/**"}})
	executionPlan := ProjectExecutionPlan(plan, basis)
	if !slices.Equal(executionPlan.IntegrationCheckIDs, []string{"project-tests", "smoke"}) || !slices.Equal(plan.IntegrationCheckIDs, []string{"project-tests"}) {
		t.Fatal("integration dropped a confirmed check or changed the original proposal")
	}
	plan.IntegrationCheckIDs = nil
	if err := ValidateExecutableProjectPlan(plan, basis); err == nil {
		t.Fatal("a plan without integration was admitted")
	}
}

func TestProjectExecutionBuildsV4OnlyWithExplicitContract(t *testing.T) {
	value, coverage := genericPlanFixture(t)
	raw, _ := json.Marshal(value)
	basis := projectBasisFixture()
	plan, _, _, err := ParseProjectEngineeringPlanResult(raw, "request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64), coverage, basis)
	if err != nil {
		t.Fatal(err)
	}
	contract := projectExecutionContractFixture(t)
	input := ComplexStandardExecutionPackageInput{
		ExecutionRunID: contract.ExecutionRunID, RequirementVersionID: contract.RequirementVersionID, RequirementSHA256: contract.RequirementSHA256,
		RequirementText: "Create a note and retain it across a restart.", PlanID: contract.PlanID, PlanSHA256: contract.PlanSHA256,
		TaskSetVersion: ComplexStandardTaskSetVersion, TaskID: "task", DependencyTaskIDs: []string{}, Task: plan.Tasks[0],
		PlanSchemaVersion: ProjectPlanningVersion, InterfaceContracts: plan.InterfaceContracts,
	}
	if _, _, _, err := BuildComplexStandardExecutionPackageWithCatalog(WorkModeStandard, input, basis.CheckCatalog()); err == nil {
		t.Fatal("planning-only V3 became executable without a project contract")
	}
	input.ProjectExecution = &contract
	pkg, encoded, digest, err := BuildComplexStandardExecutionPackageWithCatalog(WorkModeStandard, input, basis.CheckCatalog())
	if err != nil || pkg.SchemaVersion != ProjectExecutionProtocolVersion || digest != sha256Hex(encoded) {
		t.Fatalf("explicit V4: %+v %v", pkg, err)
	}
	if _, err := ParseComplexStandardExecutionPackage(encoded); err != nil {
		t.Fatalf("saved project package cannot be read: %v", err)
	}
	if err := ProjectTaskMatchesRun(pkg, projectExecutionRunFixture(t)); err != nil {
		t.Fatal(err)
	}
	// Configuration, migrations and ordinary old-test changes are scoped work,
	// not the mail template's blanket ban or append-only contract.
	pkg.GeneratedPaths = []string{"migrations/generated.sql"}
	if err := ValidateComplexStandardExecutionPackage(pkg); err != nil {
		t.Fatalf("confirmed generated configuration/SQL scope rejected: %v", err)
	}
	pkg.WritePaths = append(pkg.WritePaths, ".git/config")
	if err := ValidateComplexStandardExecutionPackage(pkg); err == nil {
		t.Fatal("Git metadata became writable")
	}
	pkg.WritePaths = input.Task.WritePaths
	pkg.RequiredChecks[0].Argv = []string{"npm", "run", "always-green"}
	catalog := basis.CheckCatalog()
	catalog[0].Argv = pkg.RequiredChecks[0].Argv
	if err := ValidateComplexStandardExecutionPackageWithCatalog(pkg, catalog); err == nil {
		t.Fatal("caller-supplied catalog replaced the admitted check command")
	}
}

func TestBuilderFirstFailurePolicyPreservesHistoricalGrant(t *testing.T) {
	run := projectExecutionRunFixture(t)
	if !BuilderFirstFailureRun(run) {
		t.Fatal("new project missing policy")
	}
	var pkg ComplexExecutionRunPackage
	if err := json.Unmarshal([]byte(run.ExecutionPackageJSON), &pkg); err != nil {
		t.Fatal(err)
	}
	pkg.BuilderFailurePolicy = ""
	raw, err := marshalCanonicalJSON(pkg)
	if err != nil {
		t.Fatal(err)
	}
	run.ExecutionPackageJSON, run.ExecutionPackageSHA256 = string(raw), sha256Hex(raw)
	if _, project, err := ProjectContractFromRun(run); err != nil || !project {
		t.Fatal("historical grant unreadable", err)
	}
	if BuilderFirstFailureRun(run) {
		t.Fatal("historical package marker changed")
	}
	if !BuilderFirstFailureEnabled(run) {
		t.Fatal("default repair excluded an intact historical admission")
	}
	pkg.BuilderFailurePolicy = "UNRECOGNIZED"
	raw, err = marshalCanonicalJSON(pkg)
	if err != nil {
		t.Fatal(err)
	}
	run.ExecutionPackageJSON, run.ExecutionPackageSHA256 = string(raw), sha256Hex(raw)
	if _, _, err := ProjectContractFromRun(run); err == nil {
		t.Fatal("unknown failure policy accepted")
	}
}

func TestProjectRevisionWindowHonorsHumanRepairExtension(t *testing.T) {
	value, coverage := genericPlanFixture(t)
	raw, _ := json.Marshal(value)
	basis := projectBasisFixture()
	plan, _, _, err := ParseProjectEngineeringPlanResult(raw, "request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64), coverage, basis)
	if err != nil {
		t.Fatal(err)
	}
	contract := projectExecutionContractFixture(t)
	input := ComplexStandardExecutionPackageInput{
		ExecutionRunID: contract.ExecutionRunID, RequirementVersionID: contract.RequirementVersionID, RequirementSHA256: contract.RequirementSHA256,
		RequirementText: "Create a note and retain it across a restart.", PlanID: contract.PlanID, PlanSHA256: contract.PlanSHA256,
		TaskSetVersion: ComplexStandardTaskSetVersion, TaskID: "task", DependencyTaskIDs: []string{}, Task: plan.Tasks[0],
		PlanSchemaVersion: ProjectPlanningVersion, InterfaceContracts: plan.InterfaceContracts,
		ProjectExecution: &contract,
	}
	pkg, _, _, err := BuildComplexStandardExecutionPackageWithCatalog(WorkModeStandard, input, basis.CheckCatalog())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := ProjectExecutionContractDigest(contract)
	if err != nil {
		t.Fatal(err)
	}
	pkg.RuntimeRevision = &PlannerTaskRevisionBinding{EventID: "event", DecisionSHA256: strings.Repeat("d", 64),
		PreviousPackageSHA256: strings.Repeat("e", 64), FirstRound: 7, ProjectExecutionSHA256: digest}
	if err := ValidateComplexStandardExecutionPackage(pkg); err == nil {
		t.Fatal("a revision beyond the default repair window was accepted without a human grant")
	}
	pkg.RuntimeRevision.HumanRepairExtension = 2
	if err := ValidateComplexStandardExecutionPackage(pkg); err != nil {
		t.Fatalf("human-granted repair rounds were rejected: %v", err)
	}
	pkg.RuntimeRevision.HumanRepairExtension = 7
	if err := ValidateComplexStandardExecutionPackage(pkg); err == nil {
		t.Fatal("an unbounded repair extension was accepted")
	}
}

func TestProjectReviewCriteriaCeilingHonorsHumanRepairExtension(t *testing.T) {
	value, coverage := genericPlanFixture(t)
	raw, _ := json.Marshal(value)
	basis := projectBasisFixture()
	plan, _, _, err := ParseProjectEngineeringPlanResult(raw, "request", "version", strings.Repeat("a", 64), strings.Repeat("b", 64), coverage, basis)
	if err != nil {
		t.Fatal(err)
	}
	contract := projectExecutionContractFixture(t)
	input := ComplexStandardExecutionPackageInput{
		ExecutionRunID: contract.ExecutionRunID, RequirementVersionID: contract.RequirementVersionID, RequirementSHA256: contract.RequirementSHA256,
		RequirementText: "Create a note and retain it across a restart.", PlanID: contract.PlanID, PlanSHA256: contract.PlanSHA256,
		TaskSetVersion: ComplexStandardTaskSetVersion, TaskID: "task", DependencyTaskIDs: []string{}, Task: plan.Tasks[0],
		PlanSchemaVersion: ProjectPlanningVersion, InterfaceContracts: plan.InterfaceContracts,
		ProjectExecution: &contract,
	}
	pkg, _, _, err := BuildComplexStandardExecutionPackageWithCatalog(WorkModeStandard, input, basis.CheckCatalog())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := ProjectExecutionContractDigest(contract)
	if err != nil {
		t.Fatal(err)
	}
	full := make([]string, 0, 18)
	full = append(full, pkg.ReviewCriteria...)
	for i := len(full); i < 18; i++ {
		full = append(full, "additional observable result "+strings.Repeat("x", i+1))
	}
	pkg.ReviewCriteria = full
	pkg.RuntimeRevision = &PlannerTaskRevisionBinding{EventID: "event", DecisionSHA256: strings.Repeat("d", 64),
		PreviousPackageSHA256: strings.Repeat("e", 64), FirstRound: 7, ProjectExecutionSHA256: digest, HumanRepairExtension: 2}
	if err := ValidateComplexStandardExecutionPackage(pkg); err != nil {
		t.Fatalf("human-authorized repair could not add one amendment of criteria: %v", err)
	}
	pkg.RuntimeRevision.HumanRepairExtension = 0
	if err := ValidateComplexStandardExecutionPackage(pkg); !errors.Is(err, ErrReviewCriteriaCeiling) {
		t.Fatalf("ungranted revision past the twelve-criteria ceiling accepted: %v", err)
	}
	pkg.RuntimeRevision.HumanRepairExtension = 2
	pkg.ReviewCriteria = append(pkg.ReviewCriteria, "one more "+strings.Repeat("y", 40))
	if err := ValidateComplexStandardExecutionPackage(pkg); !errors.Is(err, ErrReviewCriteriaCeiling) {
		t.Fatalf("criteria beyond one amendment of additions accepted: %v", err)
	}
}
