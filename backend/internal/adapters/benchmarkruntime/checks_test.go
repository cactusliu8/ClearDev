package benchmarkruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeChecks struct {
	preflight        ports.ClearDevCheckEnvironment
	result           ports.ClearDevCheckResult
	err              error
	requests         []ports.ClearDevCheckPreflightRequest
	runs             []ports.ClearDevCheckRequest
	projectContracts []core.ProjectExecutionContract
	projectResults   []string
}

func (f *fakeChecks) PrepareCandidateChecks(_ context.Context, request ports.ClearDevCheckPreflightRequest) (ports.ClearDevCheckEnvironment, error) {
	f.requests = append(f.requests, request)
	return f.preflight, f.err
}

func (f *fakeChecks) RunCandidateCheck(_ context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	f.runs = append(f.runs, request)
	return f.result, f.err
}

func (f *fakeChecks) PrepareProjectExecution(_ context.Context, contract core.ProjectExecutionContract) error {
	f.projectContracts = append(f.projectContracts, contract)
	return f.err
}

func (f *fakeChecks) PrepareProjectResult(_ context.Context, workspace, candidate string, contract core.ProjectExecutionContract) (ports.ClearDevProjectResultSource, error) {
	f.projectContracts = append(f.projectContracts, contract)
	f.projectResults = append(f.projectResults, workspace+"@"+candidate)
	return ports.ClearDevProjectResultSource{WorkspacePath: workspace, CandidateSHA: candidate}, f.err
}

func boundCheckWorkspace(t *testing.T) (string, string) {
	t.Helper()
	data := t.TempDir()
	workspace := filepath.Join(data, "worktrees", "builder")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return data, workspace
}

func TestBoundChecksRemapsFrozenSpecialistAndCandidateImages(t *testing.T) {
	data, workspace := boundCheckWorkspace(t)
	fake := &fakeChecks{preflight: ports.ClearDevCheckEnvironment{Image: CheckImage, ImageID: CheckImage, NodeVersion: "v22.23.1"}}
	checker := NewChecks(Binding{RecordClass: "DEV_LIVE", PlanCommit: PlanCommit, DataRoot: data}, fake)
	for _, image := range []string{core.StandardCandidateCheckImage, core.StandardRequiredImage} {
		fake.requests = nil
		env, err := checker.PrepareCandidateChecks(context.Background(), ports.ClearDevCheckPreflightRequest{
			RunID: "live-check", WorkspacePath: workspace, CandidateSHA: "abc", Image: image,
			Argv: [][]string{{"npm", "run", "check:migrations"}},
		})
		if err != nil {
			t.Fatalf("%s: %v", image, err)
		}
		if env.Image != CheckImage || len(fake.requests) != 1 || fake.requests[0].Image != CheckImage {
			t.Fatalf("%s not remapped: env=%#v requests=%#v", image, env, fake.requests)
		}
	}
	if _, err := checker.RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{
		RunID: "live-check", WorkspacePath: workspace, CandidateSHA: "abc", Image: core.StandardRequiredImage,
		Argv: []string{"npm", "run", "check:migrations"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != 1 || fake.runs[0].Image != CheckImage {
		t.Fatalf("run image not remapped: %#v", fake.runs)
	}
}

func TestBoundChecksPreservesGenericProjectCapabilitiesAndStandardImage(t *testing.T) {
	data, workspace := boundCheckWorkspace(t)
	contract := core.ProjectExecutionContract{Policy: core.ProjectExecutionPolicyV1}
	fake := &fakeChecks{
		preflight: ports.ClearDevCheckEnvironment{Image: core.StandardCandidateCheckImage},
		result:    ports.ClearDevCheckResult{Image: core.StandardCandidateCheckImage},
	}
	checker := NewChecks(Binding{RecordClass: "DEV_LIVE", PlanCommit: PlanCommit, DataRoot: data}, fake)
	var wrapped ports.ClearDevCheckRunner = checker
	execution, executionOK := wrapped.(ports.ClearDevProjectExecutionPreparer)
	result, resultOK := wrapped.(ports.ClearDevProjectResultPreparer)
	if !executionOK || !resultOK {
		t.Fatalf("benchmark wrapper erased project capabilities: execution=%v result=%v", executionOK, resultOK)
	}
	if err := execution.PrepareProjectExecution(context.Background(), contract); err != nil {
		t.Fatal(err)
	}
	if _, err := checker.PrepareCandidateChecks(context.Background(), ports.ClearDevCheckPreflightRequest{
		RunID: "project-check", WorkspacePath: workspace, CandidateSHA: "abc",
		Image: core.StandardCandidateCheckImage, Argv: [][]string{{"npm", "test"}}, ProjectExecution: &contract,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := checker.RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{
		RunID: "project-check", WorkspacePath: workspace, CandidateSHA: "abc",
		Image: core.StandardCandidateCheckImage, Argv: []string{"npm", "test"}, ProjectExecution: &contract,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := result.PrepareProjectResult(context.Background(), workspace, "abc", contract); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 1 || fake.requests[0].Image != core.StandardCandidateCheckImage ||
		len(fake.runs) != 1 || fake.runs[0].Image != core.StandardCandidateCheckImage {
		t.Fatalf("project check image was remapped by benchmark policy: preflight=%#v run=%#v", fake.requests, fake.runs)
	}
	if len(fake.projectContracts) != 2 || len(fake.projectResults) != 1 || fake.projectResults[0] != workspace+"@abc" {
		t.Fatalf("project capabilities were not forwarded: contracts=%d results=%v", len(fake.projectContracts), fake.projectResults)
	}
	if _, err := checker.RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{
		RunID: "project-check-wrong-image", WorkspacePath: workspace,
		Image: core.StandardRequiredImage, ProjectExecution: &contract,
	}); err == nil || err.Error() != "bound project checker request mismatch" {
		t.Fatalf("project request acquired the benchmark-required image: %v", err)
	}
}

func TestBoundChecksRejectsUnboundImageAndWorkspace(t *testing.T) {
	data, workspace := boundCheckWorkspace(t)
	fake := &fakeChecks{}
	checker := NewChecks(Binding{RecordClass: "DEV_LIVE", PlanCommit: PlanCommit, DataRoot: data}, fake)
	if _, err := checker.PrepareCandidateChecks(context.Background(), ports.ClearDevCheckPreflightRequest{
		RunID: "live-check", WorkspacePath: workspace, Image: "alpine:latest",
	}); err == nil || err.Error() != "bound candidate checker request mismatch" {
		t.Fatalf("foreign image: %v", err)
	}
	outside := filepath.Join(data, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := checker.PrepareCandidateChecks(context.Background(), ports.ClearDevCheckPreflightRequest{
		RunID: "live-check", WorkspacePath: outside, Image: core.StandardRequiredImage,
	}); err == nil || err.Error() != "bound candidate checker request mismatch" {
		t.Fatalf("outside workspace: %v", err)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("rejected request reached delegate: %#v", fake.requests)
	}
}
