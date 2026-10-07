package cleardevlocal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestStageTrialCommandAndArtifactRealContainer(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	runner, freeze := newProjectFreezeFixture(t, false)
	workspace := freeze.WorkspacePath
	if err := os.MkdirAll(filepath.Join(workspace, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(workspace, "package.json"), `{"name":"trial-fixture","version":"1.0.0"}`)
	writeTestFile(t, filepath.Join(workspace, "app/trial.cjs"), `require('node:fs').writeFileSync('report.txt','total 3\ndone 1\noverdue 1\n');console.log('total 3\ndone 1\noverdue 1');process.exit(Number(process.argv[2]||0));`)
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "-c", "commit.gpgsign=false", "commit", "-qm", "CLI trial fixture")
	candidate := strings.TrimSpace(runGit(t, workspace, "rev-parse", "HEAD"))
	contract := *freeze.ProjectExecution
	contract.Basis.Runtime = nil
	contract.Basis.Launch.Argv = []string{}
	step := core.ProjectTrialStep{ID: "cli", Kind: "COMMAND", Argv: []string{"node", "app/trial.cjs"}, TimeoutSeconds: 30, AcceptanceCriteria: []string{"ACC-001"}, Observe: "compare all three counts", OutputFiles: []string{"report.txt"}}
	contract.Basis.Trial = &core.ProjectTrial{SchemaVersion: 1, Steps: []core.ProjectTrialStep{step}}
	contract.Runtime = core.DefaultProjectRuntimeV1()
	if err := core.ValidateProjectExecutionContract(contract); err != nil {
		t.Fatal(err)
	}
	req := ports.ClearDevCheckRequest{RunID: "stage-cli-fixture", TrialStepID: "cli", WorkspacePath: workspace, CandidateSHA: candidate, Image: core.StandardCandidateCheckImage, Argv: step.Argv, Timeout: 30 * time.Second, ProjectExecution: &contract}
	if _, found, err := runner.ReadCandidateCheck(context.Background(), req); err != nil || found {
		t.Fatalf("missing receipt: %v %v", found, err)
	}
	got, err := runner.RunCandidateCheck(context.Background(), req)
	var artifacts []core.TrialArtifact
	_ = json.Unmarshal([]byte(got.TrialArtifactsJSON), &artifacts)
	if err != nil || got.Outcome != ports.ClearDevCheckPass || !got.TrialCommandExecuted || len(artifacts) != 1 {
		t.Fatalf("CLI execution: %+v %v", got, err)
	}
	raw, err := base64.StdEncoding.DecodeString(artifacts[0].Base64)
	if err != nil || string(raw) != "total 3\ndone 1\noverdue 1\n" || artifacts[0].SHA256 != sha256Hex(raw) {
		t.Fatalf("actual artifact: %+v", artifacts)
	}
	restarted := NewWithRoot(runner.managedRoot)
	read, found, err := restarted.ReadCandidateCheck(context.Background(), req)
	if err != nil || !found || !reflect.DeepEqual(got, read) {
		t.Fatalf("restart receipt: %+v %v", read, err)
	}
	again, err := restarted.RunCandidateCheck(context.Background(), req)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal("same request did not replay its exact evidence", err)
	}
	req.CandidateSHA = strings.Repeat("a", 40)
	if _, _, err := restarted.ReadCandidateCheck(context.Background(), req); err == nil {
		t.Fatal("changed candidate reused trial")
	}
}

func TestStageTrialRejectsCommandSubstitution(t *testing.T) {
	_, freeze := newProjectFreezeFixture(t, false)
	contract := *freeze.ProjectExecution
	step := core.ProjectTrialStep{ID: "cli", Kind: "COMMAND", Argv: []string{"node", "app/trial.cjs"}, TimeoutSeconds: 30, AcceptanceCriteria: []string{"ACC-001"}, Observe: "actual output"}
	contract.Basis.Trial = &core.ProjectTrial{SchemaVersion: 1, Steps: []core.ProjectTrialStep{step}}
	req := ports.ClearDevCheckRequest{RunID: "trial", TrialStepID: "cli", WorkspacePath: freeze.WorkspacePath, CandidateSHA: freeze.BaseSHA, Image: core.StandardCandidateCheckImage, Argv: []string{"node", "checks/failure.cjs"}, Timeout: 10 * time.Second, ProjectExecution: &contract}
	if _, err := normalizeCheckRequest(req); err == nil {
		t.Fatal("ordinary check substituted for frozen trial")
	}
}
