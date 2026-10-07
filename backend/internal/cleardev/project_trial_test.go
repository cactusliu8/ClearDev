package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func trialCommandBasis() ProjectExecutionBasis {
	return ProjectExecutionBasis{WritePaths: []string{"src/**"}, DependencyNeeds: []string{}, Checks: []ProjectCheckSpec{{ID: "project-tests", Argv: []string{"npm", "test"}, TimeoutSeconds: 120, MainPaths: []string{"src/**"}}}, Launch: ProjectLaunch{Argv: []string{}, WorkingDirectory: ".", Description: "一次性命令，检查输出"}, Trial: &ProjectTrial{SchemaVersion: 1, Steps: []ProjectTrialStep{{ID: "default-input", Kind: "COMMAND", AcceptanceCriteria: []string{"ACC-001"}, Argv: []string{"node", "src/taskstat.mjs"}, TimeoutSeconds: 30, Observe: "打印 total/done/overdue 并与输入逐项核对"}}}}
}
func TestCommandTrialDoesNotRequireHTTPLaunch(t *testing.T) {
	basis := trialCommandBasis()
	if _, err := ResolveProjectRuntime(basis); err != nil {
		t.Fatal(err)
	}
	if ProjectTrialNeedsService(basis) {
		t.Fatal("one-shot CLI must not wait for an HTTP server")
	}
	basis.Trial = nil
	if _, err := ResolveProjectRuntime(basis); err == nil {
		t.Fatal("legacy contract was silently reinterpreted")
	}
}
func TestTrialContractRejectsUnsupportedAndUnboundOperations(t *testing.T) {
	for _, mutate := range []func(*ProjectTrial){
		func(p *ProjectTrial) { p.Steps[0].Kind = "HARDWARE" },
		func(p *ProjectTrial) {
			p.Steps[0].Kind = "BROWSER"
			p.Steps[0].Argv = nil
			p.Steps[0].TimeoutSeconds = 0
		},
		func(p *ProjectTrial) { p.Steps[0].AcceptanceCriteria = nil },
		func(p *ProjectTrial) { p.Steps[0].Argv = []string{"sh", "-c", "echo bypass"} },
		func(p *ProjectTrial) { p.Steps[0].TimeoutSeconds = 0 },
		func(p *ProjectTrial) { p.Steps[0].ExpectedExitCode = 125 },
	} {
		p := trialCommandBasis().Trial
		mutate(p)
		if err := ValidateProjectTrial(p); err == nil || !strings.Contains(err.Error(), "STAGE_TRIAL_UNSUPPORTED") {
			t.Fatalf("unsupported trial accepted: %v", err)
		}
	}
}
func TestAbsentTrialPreservesLegacyBasisJSON(t *testing.T) {
	basis := trialCommandBasis()
	basis.Trial = nil
	raw, err := json.Marshal(basis)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"trial"`) {
		t.Fatal("legacy JSON acquired a field")
	}
	if !ProjectTrialNeedsService(basis) {
		t.Fatal("legacy health policy changed")
	}
}

func TestTrialCoverageUsesEveryOriginalAcceptanceCriterion(t *testing.T) {
	trial := trialCommandBasis().Trial
	trial.Steps[0].AcceptanceCriteria = []string{"原功能一"}
	if err := ValidateProjectTrialCoverage(trial, []string{"原功能一", "原功能二"}); err == nil {
		t.Fatal("uncovered acceptance allowed")
	}
	trial.Steps[0].AcceptanceCriteria = append(trial.Steps[0].AcceptanceCriteria, "原功能二")
	if err := ValidateProjectTrialCoverage(trial, []string{"原功能一", "原功能二"}); err != nil {
		t.Fatal(err)
	}
	trial.Steps[0].AcceptanceCriteria[1] = "新增功能"
	if err := ValidateProjectTrialCoverage(trial, []string{"原功能一", "原功能二"}); err == nil {
		t.Fatal("substituted requirement allowed")
	}
}
