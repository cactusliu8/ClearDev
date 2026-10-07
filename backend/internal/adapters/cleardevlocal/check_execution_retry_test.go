package cleardevlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCheckTemporaryBudgetAllowsOverlappingTrialReservations(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	const want int64 = 32 * 1024 * 1024 * 1024
	if got := productionCheckCapacityLimits().ActiveTemporaryBytes; got != want {
		t.Fatalf("active temporary allowance = %d, want %d", got, want)
	}
	ctx := context.Background()
	used := int64(0)
	for _, item := range []struct {
		id    string
		bytes int64
	}{
		{"first-trial", checkRunReservationBytes},
		{"second-trial", checkRunReservationBytes},
		{"third-source", checkSourceReservationBytes},
	} {
		lease, err := acquireCheckTemporary(ctx, item.id, "candidate-check", item.bytes)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lease.Release(ctx) })
		used += item.bytes
	}
	last, err := acquireCheckTemporary(ctx, "remaining-capacity", "candidate-check", want-used)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = last.Release(ctx) })
	if _, err := acquireCheckTemporary(ctx, "one-byte-over", "candidate-check", 1); !errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("over-capacity reservation = %v, want fixed-limit refusal", err)
	}
}

func newRetryableTrialFixture(t *testing.T) (*Runner, ports.ClearDevCheckRequest) {
	t.Helper()
	runner, freeze := newProjectFreezeFixture(t, false)
	if err := os.MkdirAll(filepath.Join(freeze.WorkspacePath, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(freeze.WorkspacePath, "app/trial.cjs"), "console.log('trial-ran');\n")
	runGit(t, freeze.WorkspacePath, "add", ".")
	runGit(t, freeze.WorkspacePath, "-c", "commit.gpgsign=false", "commit", "-qm", "bounded trial retry fixture")
	candidate := strings.TrimSpace(runGit(t, freeze.WorkspacePath, "rev-parse", "HEAD"))
	contract := *freeze.ProjectExecution
	contract.Basis.Runtime = nil
	contract.Basis.Launch.Argv = []string{}
	step := core.ProjectTrialStep{ID: "retry-cli", Kind: "COMMAND", Argv: []string{"node", "app/trial.cjs"}, TimeoutSeconds: 30, AcceptanceCriteria: []string{"ACC-001"}, Observe: "observe the real command output"}
	contract.Basis.Trial = &core.ProjectTrial{SchemaVersion: 1, Steps: []core.ProjectTrialStep{step}}
	contract.Runtime = core.DefaultProjectRuntimeV1()
	if err := core.ValidateProjectExecutionContract(contract); err != nil {
		t.Fatal(err)
	}
	return runner, ports.ClearDevCheckRequest{RunID: "stage-trial-retry-fixture", TrialStepID: step.ID, WorkspacePath: freeze.WorkspacePath, CandidateSHA: candidate, Image: core.StandardCandidateCheckImage, Argv: step.Argv, Timeout: 30 * time.Second, ProjectExecution: &contract}
}

func occupyRetryFixtureCapacity(t *testing.T) *checkTemporaryLease {
	t.Helper()
	lease, err := acquireCheckTemporary(context.Background(), "other-check", "candidate-check", productionCheckCapacityLimits().ActiveTemporaryBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release(context.Background()) })
	return lease
}

func TestStageTrialPreExecutionFailureCanContinueOnce(t *testing.T) {
	runner, req := newRetryableTrialFixture(t)
	logPath := installDockerShim(t)
	lease := occupyRetryFixtureCapacity(t)
	first, firstErr := runner.RunCandidateCheck(context.Background(), req)
	if firstErr == nil || first.Outcome != ports.ClearDevCheckInfraError || first.TrialCommandExecuted {
		t.Fatalf("first preparation should fail before execution: %+v %v", first, firstErr)
	}
	path, err := checkRunStatePath(req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(readTestFile(t, path))
	if _, found, err := runner.ReadCandidateCheck(context.Background(), req); !found || err == nil {
		t.Fatalf("read-only failure receipt: found=%v error=%v", found, err)
	}
	if !bytes.Equal(original, []byte(readTestFile(t, path))) || readTestFile(t, logPath) != "" {
		t.Fatal("reading a failed receipt mutated it or started Docker")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := NewWithRoot(runner.managedRoot).RunCandidateCheck(context.Background(), req)
	if err != nil || second.Outcome != ports.ClearDevCheckPass || !second.TrialCommandExecuted {
		t.Fatalf("same frozen trial remained poisoned after preparation recovered: %+v %v", second, err)
	}
	if archived := []byte(readTestFile(t, path+".attempt-1")); !bytes.Equal(original, archived) {
		t.Fatal("the first failure was not retained byte for byte")
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(readTestFile(t, path)), &state); err != nil {
		t.Fatal(err)
	}
	if state["attempt"] != float64(2) || state["executionState"] != "STARTED_OR_UNKNOWN" {
		t.Fatalf("execution boundary and attempt were not retained: %+v", state)
	}
	actions := readTestFile(t, logPath)
	third, err := runner.RunCandidateCheck(context.Background(), req)
	if err != nil || !reflect.DeepEqual(third, second) || actions != readTestFile(t, logPath) {
		t.Fatalf("settled execution was repeated: %+v %v", third, err)
	}
}
