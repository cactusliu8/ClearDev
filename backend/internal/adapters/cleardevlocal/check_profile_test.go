package cleardevlocal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestNodeCheckProfileBindsBothPoolsAndRejectsConflictingRuntime(t *testing.T) {
	runtime := core.DefaultProjectRuntimeV1()
	request := CheckContainerRequest{ExecutionProfile: core.NodeCheckSmallThreadsV1, Image: core.StandardCandidateCheckImage, ProjectRuntime: &runtime, SourceDir: "/trusted/source", Argv: []string{"npm", "run", "verify"}, Timeout: 180 * time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024}
	args, err := CheckContainerRunArgs(request, "/tmp/profile.cid")
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"--env=UV_THREADPOOL_SIZE=1", "--env=NODE_OPTIONS=--v8-pool-size=1", "--pids-limit=64", "--memory=536870912"} {
		if !containsString(args, arg) {
			t.Fatal("profile changed limit or omitted pool", arg)
		}
	}
	runtime.EnvironmentVariables["UV_THREADPOOL_SIZE"] = "4"
	if _, err := CheckContainerRunArgs(request, "/tmp/profile.cid"); err == nil {
		t.Fatal("overrode frozen runtime env")
	}
	delete(runtime.EnvironmentVariables, "UV_THREADPOOL_SIZE")
	request.ExecutionProfile = "UNKNOWN"
	if _, err := CheckContainerRunArgs(request, "/tmp/profile.cid"); err == nil {
		t.Fatal("unknown profile accepted")
	}
	request.ExecutionProfile = core.NodeCheckSmallThreadsV1
	request.ProjectRuntime = nil
	if _, err := CheckContainerRunArgs(request, "/tmp/profile.cid"); err == nil {
		t.Fatal("Node profile escaped project checks")
	}
}

func TestNodeCheckProfileReadsLegacyAndNewReceiptsWithoutRewriting(t *testing.T) {
	runner, freeze := newProjectFreezeFixture(t, true)
	check := freeze.ProjectExecution.Basis.Checks[0]
	request, err := normalizeCheckRequest(ports.ClearDevCheckRequest{RunID: "profile-receipt", WorkspacePath: freeze.WorkspacePath, CandidateSHA: freeze.BaseSHA, Image: core.StandardCandidateCheckImage, Argv: check.Argv, Timeout: time.Duration(check.TimeoutSeconds) * time.Second, ProjectExecution: freeze.ProjectExecution})
	if err != nil {
		t.Fatal(err)
	}
	path, err := checkRunStatePath(request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"", core.NodeCheckSmallThreadsV1} {
		bound := request
		bound.ExecutionProfile = profile
		fingerprint, err := checkRequestSHA256(bound)
		if err != nil {
			t.Fatal(err)
		}
		state := checkRunState{Version: checkRunStateVersion, ExecutionProfile: profile, Fingerprint: fingerprint, State: "settled", Result: &ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, CandidateSHA: request.CandidateSHA}}
		if err := writeCheckRunState(path, state); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		allowed, err := runner.CanRetryWorkflowCheck(context.Background(), request)
		if err != nil || !allowed {
			t.Fatal("receipt compatibility", profile, allowed, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(before) != string(after) {
			t.Fatal("historical receipt rewritten")
		}
		if profile == "" {
			explicit := request
			explicit.ExecutionProfile = core.NodeCheckSmallThreadsV1
			if allowed, _ := runner.CanRetryWorkflowCheck(context.Background(), explicit); allowed {
				t.Fatal("new explicit profile borrowed legacy evidence")
			}
			if _, err := runner.RunCandidateCheck(context.Background(), request); err == nil {
				t.Fatal("reused old RunID under different execution profile")
			}
		}
		state.ExecutionProfile = "unknown"
		if err := writeCheckRunState(path, state); err != nil {
			t.Fatal(err)
		}
		if allowed, _ := runner.CanRetryWorkflowCheck(context.Background(), request); allowed {
			t.Fatal("unknown receipt profile accepted")
		}
	}
}

// Opt-in real-candidate regression: read-only inputs, disposable source/data,
// actual production container monitor and lifecycle rebuild. Not a live project
// check, model review, or ACC-005 browser acceptance.
func TestNodeCheckProfileCandidateRealContainer(t *testing.T) {
	workspace := os.Getenv("CLEARDEV_PROFILE_REGRESSION_WORKSPACE")
	if workspace == "" {
		t.Skip("explicit fixed candidate regression not requested")
	}
	sha := os.Getenv("CLEARDEV_PROFILE_REGRESSION_CANDIDATE")
	dependencies := os.Getenv("CLEARDEV_PROFILE_REGRESSION_DEPENDENCIES")
	runtimeJSON := os.Getenv("CLEARDEV_PROFILE_REGRESSION_RUNTIME")
	if !validCommit(sha) || !filepath.IsAbs(workspace) || !filepath.IsAbs(dependencies) || !filepath.IsAbs(runtimeJSON) {
		t.Fatal("invalid regression inputs")
	}
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	before := strings.TrimSpace(runGit(t, workspace, "status", "--porcelain=v1"))
	if before != "" || strings.TrimSpace(runGit(t, workspace, "rev-parse", "HEAD")) != sha {
		t.Fatal("candidate changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	t.Cleanup(func() { cleanupPreparedTree(source) })
	facts, err := materializeCandidateSource(ctx, workspace, sha, filepath.Join(root, "git"), source, filepath.Join(root, "manifest.json"), defaultCandidateSourceLimits())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(runtimeJSON)
	if err != nil {
		t.Fatal(err)
	}
	var runtime core.ProjectRuntime
	if err := json.Unmarshal(raw, &runtime); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_DATA_DIR", t.TempDir())
	requireProductionCheckPrerequisites(t, projectInstallImage)
	prepared := preparedCheckEnvironment{dependencyDirectory: filepath.Dir(dependencies), public: ports.ClearDevCheckEnvironment{SourceBytes: facts.Manifest.BlobBytes, SourceItems: int64(facts.Manifest.ItemCount)}}
	installed, err := prepareProjectResultDependencies(ctx, prepared, source, root, runtime)
	if err != nil {
		t.Fatal("formal dependency preparation", err)
	}
	t.Cleanup(func() { cleanupPreparedTree(installed) })
	if err := sealDependencyTree(installed); err != nil {
		t.Fatal(err)
	}
	if log, err := os.ReadFile(filepath.Join(root, "dependency-download.log")); err == nil {
		t.Logf("actual dependency preparation: %s", log)
	} else {
		t.Fatal(err)
	}
	result, err := RunCheckContainer(ctx, CheckContainerRequest{ExecutionProfile: core.NodeCheckSmallThreadsV1, DependenciesInstalled: true, Image: core.StandardCandidateCheckImage, Argv: []string{"npm", "run", "verify"}, SourceDir: source, SourceBytes: facts.Manifest.BlobBytes, SourceItems: int64(facts.Manifest.ItemCount), DependencyDir: installed, ProjectRuntime: &runtime, Timeout: 180 * time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024, Offline: true})
	t.Logf("actual monitor/lifecycle/container result: outcome=%s exit=%d command=%v output=%s", result.Outcome, result.ExitCode, result.CommandExecuted, result.Output)
	if err != nil || result.Outcome != CheckContainerPass || result.ExitCode != 0 || !result.CommandExecuted {
		t.Fatal("formal container regression", err)
	}
	if strings.TrimSpace(runGit(t, workspace, "status", "--porcelain=v1")) != before || strings.TrimSpace(runGit(t, workspace, "rev-parse", "HEAD")) != sha {
		t.Fatal("regression changed candidate")
	}
}
