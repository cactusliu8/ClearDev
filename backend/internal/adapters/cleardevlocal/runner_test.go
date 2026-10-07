package cleardevlocal

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestParseNameStatusZIncludesBothRenamePaths(t *testing.T) {
	paths, err := parseNameStatusZ([]byte("M\x00src/email.js\x00R100\x00old.js\x00test/email.test.js\x00"))
	if err != nil {
		t.Fatalf("parseNameStatusZ: %v", err)
	}
	if len(paths) != 2 || paths[1].OldPath != "old.js" || paths[1].Path != "test/email.test.js" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestInspectCandidateRequiresCleanDescendantAndUsesRealDiff(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "ClearDev Test")
	baseFile := "export const value = 1;\n" +
		"export const one = 1;\nexport const two = 2;\nexport const three = 3;\n" +
		"export const four = 4;\nexport const five = 5;\n"
	writeTestFile(t, filepath.Join(repo, "old.js"), baseFile)
	runGit(t, repo, "add", "old.js")
	runGit(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "mv", "old.js", "email.js")
	writeTestFile(t, filepath.Join(repo, "email.js"), strings.Replace(baseFile, "value = 1", "value = 2", 1))
	runGit(t, repo, "add", "email.js")
	runGit(t, repo, "commit", "-m", "candidate")

	inspection, err := New().InspectCandidate(context.Background(), repo, base)
	if err != nil {
		t.Fatalf("InspectCandidate: %v", err)
	}
	if len(inspection.CandidateSHA) != 40 || len(inspection.Paths) != 1 || inspection.Paths[0].OldPath != "old.js" || inspection.Paths[0].Path != "email.js" {
		t.Fatalf("inspection = %#v", inspection)
	}

	writeTestFile(t, filepath.Join(repo, "dirty.txt"), "dirty")
	if _, err := New().InspectCandidate(context.Background(), repo, base); err != ports.ErrClearDevWorkspaceDirty {
		t.Fatalf("dirty error = %v", err)
	}
}

func TestOutputCollectorStopsHashingAtSummaryLimit(t *testing.T) {
	var limitCalls int
	collector := newOutputCollector(4, func() { limitCalls++ })
	_, _ = collector.Write([]byte("abcdef"))
	wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte("abcd")))
	if collector.String() != "abcd" || !collector.Truncated() || !collector.LimitExceeded() || collector.SHA256() != wantHash || limitCalls != 1 {
		t.Fatalf("summary=%q truncated=%v limitExceeded=%v hash=%q limitCalls=%d", collector.String(), collector.Truncated(), collector.LimitExceeded(), collector.SHA256(), limitCalls)
	}
	_, _ = collector.Write([]byte("ignored"))
	if collector.SHA256() != wantHash || limitCalls != 1 {
		t.Fatalf("overflow after stop changed hash or callback: hash=%q limitCalls=%d", collector.SHA256(), limitCalls)
	}
}

func TestRunCandidateCheckStopsOnStdoutOrStderrOutputLimit(t *testing.T) {
	skipIfNestedDockerBindMounts(t)
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker is unavailable")
	}
	const image = "node:22-bookworm-slim"
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("required image %q is unavailable: %v", image, err)
	}
	repo, candidate := committedCheckRepo(t)
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			code := fmt.Sprintf("process.%s.write('x'.repeat(1048576)); setInterval(() => {}, 1000)", stream)
			result, err := New().RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{
				WorkspacePath: repo, CandidateSHA: candidate, Image: image,
				Argv: []string{"node", "-e", code}, Timeout: 20 * time.Second,
				MemoryBytes: 256 * 1024 * 1024, PidsLimit: 64, OutputLimit: 1024,
			})
			if err != nil {
				t.Fatalf("RunCandidateCheck: %v", err)
			}
			if result.Outcome != ports.ClearDevCheckFail || result.ExitCode != -1 || result.TimedOut || !result.OutputTruncated || len(result.OutputSummary) != 1024 {
				t.Fatalf("result = %#v", result)
			}
			wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte(result.OutputSummary)))
			if result.OutputSHA256 != wantHash {
				t.Fatalf("output hash = %q, want %q", result.OutputSHA256, wantHash)
			}
		})
	}
}

func TestProbeCheckExecutableUsesVersionStdoutOnly(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	installDockerShim(t)
	t.Setenv("CLEARDEV_DOCKER_PROBE_STDERR", strings.Repeat("docker warning ", 32))

	version, err := probeCheckExecutable(context.Background(), ports.ClearDevCheckPreflightRequest{
		Timeout: time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64,
	}, "sha256:shim-image", "node")
	if err != nil {
		t.Fatalf("probe executable: %v", err)
	}
	if version != "v22.0.0" {
		t.Fatalf("version = %q, want stdout version only", version)
	}
}

func TestRunCandidateCheckRunIDReturnsSettledResultAfterRestartWithoutSecondDockerAction(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("AO_DATA_DIR", dataDir)
	logPath := installDockerShim(t)
	repo, candidate := committedCheckRepo(t)
	request := ports.ClearDevCheckRequest{
		RunID: "complex-execution-check-1", WorkspacePath: repo, CandidateSHA: candidate,
		Image: "approved-node-image", Argv: []string{"node", "--test", "test/email.test.js"}, Timeout: time.Second,
	}

	first, err := New().RunCandidateCheck(context.Background(), request)
	if err != nil {
		t.Fatalf("first RunCandidateCheck: %v", err)
	}
	if first.Outcome != ports.ClearDevCheckPass || first.OutputSummary != "shim check output\n" {
		t.Fatalf("first result = %#v", first)
	}
	// A new runner models the daemon process after restart. It must return the
	// exact settled result without invoking either Docker image inspect or run.
	second, err := New().RunCandidateCheck(context.Background(), request)
	if err != nil {
		t.Fatalf("second RunCandidateCheck: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("replayed result = %#v, want %#v", second, first)
	}
	if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, []string{"image", "run", "run"}) {
		t.Fatalf("Docker actions = %#v, want one inspect, one executable preflight, and one formal run", got)
	}
	statePath, err := checkRunStatePath(request.RunID)
	if err != nil {
		t.Fatalf("checkRunStatePath: %v", err)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("state stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions = %o, want 600", info.Mode().Perm())
	}
	statePayload, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var settledState checkRunState
	if err := json.Unmarshal(statePayload, &settledState); err != nil {
		t.Fatal(err)
	}
	if settledState.SourceManifest == nil || settledState.SourceManifest.CandidateSHA != candidate || len(settledState.SourceManifest.Entries) == 0 {
		t.Fatalf("settled check did not save its full source manifest: %#v", settledState.SourceManifest)
	}

	mismatch := request
	mismatch.Argv = []string{"node", "--test", "test/another.test.js"}
	result, err := New().RunCandidateCheck(context.Background(), mismatch)
	if err == nil || result.Outcome != ports.ClearDevCheckInfraError {
		t.Fatalf("mismatched RunID result=%#v err=%v, want deterministic rejection", result, err)
	}
	if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, []string{"image", "run", "run"}) {
		t.Fatalf("mismatched RunID started Docker: %#v", got)
	}
}

func TestSettledCandidateCheckReplaysWithoutDependencyCache(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("AO_DATA_DIR", dataDir)
	logPath := installDockerShim(t)
	repo, candidate := committedCheckRepo(t)
	request := ports.ClearDevCheckRequest{
		RunID: "settled-check-with-removed-dependency", WorkspacePath: repo, CandidateSHA: candidate,
		Image: "approved-node-image", Argv: []string{"npm", "test"}, Timeout: time.Second,
	}
	normalized, err := normalizeCheckRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := checkRequestSHA256(normalized)
	if err != nil {
		t.Fatal(err)
	}
	statePath, err := checkRunStatePath(normalized.RunID)
	if err != nil {
		t.Fatal(err)
	}
	want := ports.ClearDevCheckResult{
		Outcome: ports.ClearDevCheckPass, CandidateSHA: candidate, Image: request.Image,
		ImageID: "sha256:removed-image", DependencyCacheKey: strings.Repeat("a", 64),
		DependencyEnvironmentID: strings.Repeat("b", 64), ExitCode: 0,
	}
	if err := writeCheckRunState(statePath, checkRunState{
		Version: checkRunStateVersion, Fingerprint: fingerprint, State: "settled", Result: &want,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := New().RunCandidateCheck(context.Background(), request)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("settled replay = %#v, %v; want %#v", got, err, want)
	}
	if actions := strings.TrimSpace(readTestFile(t, logPath)); actions != "" {
		t.Fatalf("settled replay invoked Docker: %q", actions)
	}
	if _, err := os.Stat(filepath.Join(dataDir, checkDependencyDirectory)); !os.IsNotExist(err) {
		t.Fatalf("settled replay touched the removed dependency cache: %v", err)
	}
}

func TestRunCandidateCheckClassifiesFullWritableAreaAsInfrastructure(t *testing.T) {
	if testing.Short() {
		t.Skip("production Docker capacity regression is not a short test")
	}
	skipIfNestedDockerBindMounts(t)
	const image = "node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436"
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("Docker is unavailable: %v", err)
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("required image %q is unavailable: %v", image, err)
	}
	dataDir := t.TempDir()
	t.Setenv("AO_DATA_DIR", dataDir)
	t.Cleanup(func() { cleanupPreparedTree(filepath.Join(dataDir, checkTemporaryDirectory)) })
	repo, candidate := committedCheckRepo(t)
	code := "const fs=require('fs');const fd=fs.openSync('full.bin','w');const b=Buffer.alloc(65536);for(;;)fs.writeSync(fd,b)"
	result, err := New().RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{
		WorkspacePath: repo, CandidateSHA: candidate, Image: image,
		Argv: []string{"node", "-e", code}, Timeout: 30 * time.Second,
		MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024,
	})
	if err == nil || result.Outcome != ports.ClearDevCheckInfraError || result.ExitCode != -1 || !strings.Contains(err.Error(), "capacity exhausted") {
		t.Fatalf("full writable area result=%#v err=%v", result, err)
	}
	temporaryRoot := filepath.Join(dataDir, checkTemporaryDirectory)
	entries, readErr := os.ReadDir(temporaryRoot)
	if readErr != nil {
		t.Fatalf("read temporary root after capacity failure: %v", readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "action-") {
			t.Fatalf("capacity failure left temporary action %q", entry.Name())
		}
	}
}

func TestPrepareCandidateChecksRunIDReplaysBoundEnvironment(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	logPath := installDockerShim(t)
	repo, candidate := committedCheckRepo(t)
	request := ports.ClearDevCheckPreflightRequest{
		RunID: "preflight-1", WorkspacePath: repo, CandidateSHA: candidate,
		Image: "approved-node-image", Argv: [][]string{{"node", "--test", "test/email.test.js"}}, Timeout: time.Second,
	}
	first, err := New().PrepareCandidateChecks(context.Background(), request)
	if err != nil {
		t.Fatalf("first preflight: %v", err)
	}
	second, err := New().PrepareCandidateChecks(context.Background(), request)
	if err != nil {
		t.Fatalf("replayed preflight: %v", err)
	}
	if !reflect.DeepEqual(first, second) || first.CheckEnvironmentID == "" || first.ImageID != "sha256:shim-image" {
		t.Fatalf("preflight replay first=%#v second=%#v", first, second)
	}
	if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, []string{"image", "run"}) {
		t.Fatalf("preflight Docker actions = %#v, want one inspect and one executable probe", got)
	}
	mismatch := request
	mismatch.Argv = [][]string{{"node", "--test", "test/other.test.js"}}
	if _, err := New().PrepareCandidateChecks(context.Background(), mismatch); err == nil {
		t.Fatal("preflight RunID accepted a different approved argv")
	}
	if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, []string{"image", "run"}) {
		t.Fatalf("mismatched preflight started Docker: %#v", got)
	}
}

func TestPrepareCandidateChecksRejectsMissingLockBeforeDocker(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	logPath := installDockerShim(t)
	repo, candidate := committedCheckRepo(t)
	_, err := New().PrepareCandidateChecks(context.Background(), ports.ClearDevCheckPreflightRequest{
		RunID: "preflight-missing-lock", WorkspacePath: repo, CandidateSHA: candidate,
		Image: "approved-node-image", Argv: [][]string{{"npm", "test"}}, Timeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "package-lock.json") {
		t.Fatalf("missing lock error = %v", err)
	}
	if got := strings.TrimSpace(readTestFile(t, logPath)); got != "" {
		t.Fatalf("missing lock reached Docker: %q", got)
	}
}

func TestFormalExitClassificationAfterSuccessfulPreflight(t *testing.T) {
	for _, testCase := range []struct {
		exitCode int
		outcome  ports.ClearDevCheckOutcome
		wantErr  bool
	}{
		{125, ports.ClearDevCheckInfraError, true},
		{126, ports.ClearDevCheckFail, false},
		{127, ports.ClearDevCheckFail, false},
	} {
		t.Run(strconv.Itoa(testCase.exitCode), func(t *testing.T) {
			t.Setenv("AO_DATA_DIR", t.TempDir())
			installDockerExitShim(t, testCase.exitCode)
			repo, candidate := committedCheckRepo(t)
			result, err := New().RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{
				WorkspacePath: repo, CandidateSHA: candidate, Image: "approved-node-image",
				Argv: []string{"node", "-e", "process.exit(1)"}, Timeout: time.Second,
			})
			if (err != nil) != testCase.wantErr || result.Outcome != testCase.outcome || result.ExitCode != testCase.exitCode {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestPreflightCancellationForceRemovesDockerContainer(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		hangOn      string
		argv        [][]string
		wantActions []string
	}{
		{name: "executable probe", hangOn: "probe", argv: [][]string{{"node", "--test", "test/email.test.js"}}, wantActions: []string{"image", "run", "rm", "ps"}},
		{name: "offline dependency preparation", hangOn: "prepare", argv: [][]string{{"npm", "test"}}, wantActions: []string{"image", "run", "run", "run", "exec", "exec", "exec", "rm", "ps"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dataDir := t.TempDir()
			t.Setenv("AO_DATA_DIR", dataDir)
			t.Setenv("CLEARDEV_NPM_CACHE_DIR", t.TempDir())
			logPath := installDockerCancellationShim(t, testCase.hangOn)
			var repo, candidate string
			if testCase.hangOn == "prepare" {
				repo, candidate = committedGeneratedProofRepo(t)
			} else {
				repo, candidate = committedCheckRepo(t)
			}
			before, err := filepath.Glob(filepath.Join(os.TempDir(), "cleardev-check-preflight-*"))
			if err != nil {
				t.Fatal(err)
			}
			request := ports.ClearDevCheckPreflightRequest{
				RunID: "cancelled-" + testCase.hangOn, WorkspacePath: repo, CandidateSHA: candidate,
				Image: "approved-node-image", Argv: testCase.argv, Timeout: 30 * time.Millisecond,
			}
			if _, err := New().PrepareCandidateChecks(context.Background(), request); err == nil {
				t.Fatal("cancelled preflight unexpectedly passed")
			}
			after, err := filepath.Glob(filepath.Join(os.TempDir(), "cleardev-check-preflight-*"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("preflight container directories before=%#v after=%#v", before, after)
			}
			if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, testCase.wantActions) {
				t.Fatalf("Docker actions = %#v, want %#v", got, testCase.wantActions)
			}
			if testCase.hangOn == "prepare" {
				entries, readErr := os.ReadDir(filepath.Join(dataDir, checkDependencyDirectory))
				if readErr != nil {
					t.Fatalf("read cancelled dependency cache: %v", readErr)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".prepare-") || validLowerHex(entry.Name(), 64) {
						t.Fatalf("cancelled dependency preparation left %q", entry.Name())
					}
				}
			}
			if _, err := New().PrepareCandidateChecks(context.Background(), request); err == nil {
				t.Fatal("cancelled preflight replay unexpectedly passed")
			}
			if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, testCase.wantActions) {
				t.Fatalf("cancelled replay started Docker: %#v", got)
			}
		})
	}
}

func TestFormalCheckControlCancellationIsInfrastructureAndReplaysWithoutDocker(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	logPath := installDockerCancellationShim(t, "formal")
	repo, candidate := committedCheckRepo(t)
	request := ports.ClearDevCheckRequest{
		RunID: "formal-control-cancellation", WorkspacePath: repo, CandidateSHA: candidate,
		Image: "approved-node-image", Argv: []string{"node", "-e", "setInterval(() => {}, 1000)"},
		Timeout: time.Minute, MemoryBytes: 256 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024,
	}
	ctx, cancel := context.WithCancel(context.Background())
	type checkResponse struct {
		result ports.ClearDevCheckResult
		err    error
	}
	response := make(chan checkResponse, 1)
	go func() {
		result, err := New().RunCandidateCheck(ctx, request)
		response <- checkResponse{result: result, err: err}
	}()
	readyPath := os.Getenv("CLEARDEV_DOCKER_READY")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("formal Docker shim did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	var first checkResponse
	select {
	case first = <-response:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled formal check did not return")
	}
	if first.err == nil || first.result.Outcome != ports.ClearDevCheckInfraError || first.result.ExitCode != -1 {
		t.Fatalf("canceled formal result=%#v err=%v", first.result, first.err)
	}
	if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, []string{"image", "run", "run", "exec", "exec", "rm"}) {
		t.Fatalf("canceled formal Docker actions = %#v", got)
	}
	second, err := New().RunCandidateCheck(context.Background(), request)
	if err == nil || !reflect.DeepEqual(second, first.result) {
		t.Fatalf("canceled formal replay result=%#v err=%v", second, err)
	}
	if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, []string{"image", "run", "run", "exec", "exec", "rm"}) {
		t.Fatalf("canceled formal replay started Docker: %#v", got)
	}
}

func TestLegacyNodeSingleFileProductionCheckStillPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("production Docker regression is not a short test")
	}
	const image = "node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436"
	requireProductionCheckPrerequisites(t, image)
	t.Setenv("AO_DATA_DIR", t.TempDir())
	repo, candidate := committedLegacyNodeCheckRepo(t)
	command := []string{"node", "--test", "test/email.test.js"}
	environment, err := New().PrepareCandidateChecks(context.Background(), ports.ClearDevCheckPreflightRequest{
		RunID: "legacy-node-single-file-preflight", WorkspacePath: repo, CandidateSHA: candidate,
		Image: image, Argv: [][]string{command}, Timeout: 2 * time.Minute,
		MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64,
	})
	if err != nil {
		t.Fatalf("legacy Node preflight: %v", err)
	}
	result, err := New().RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{
		RunID: "legacy-node-single-file-check", WorkspacePath: repo, CandidateSHA: candidate,
		Image: image, Argv: command, Timeout: time.Minute,
		MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024,
	})
	if err != nil || result.Outcome != ports.ClearDevCheckPass || result.ExitCode != 0 {
		t.Fatalf("legacy Node result=%#v err=%v", result, err)
	}
	if environment.DependencyEnvironmentID != "" || result.DependencyEnvironmentID != "" || result.ImageID != environment.ImageID || result.ApprovedArgvSHA256 != environment.ApprovedArgvSHA256 {
		t.Fatalf("legacy Node environment=%#v result=%#v", environment, result)
	}
	t.Logf("legacy candidate=%s argvSha=%s image=%s outcome=%s exit=%d", candidate, result.ApprovedArgvSHA256, result.ImageID, result.Outcome, result.ExitCode)
}

func TestRunGeneratedProofRunIDReturnsSettledResultAfterRestartWithoutSecondDockerAction(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("AO_DATA_DIR", dataDir)
	logPath := installGeneratedProofDockerShim(t)
	repo, candidate := committedGeneratedProofRepo(t)
	request := ports.ClearDevGeneratedProofRequest{
		RunID: "complex-generated-proof-1", WorkspacePath: repo, CandidateSHA: candidate,
		Image: "node:22-bookworm-slim", Argv: []string{"node", "-e", "process.exit(0)"},
		OutputPaths: []string{"package-lock.json"}, Timeout: time.Second,
	}

	first, err := New().RunGeneratedProof(context.Background(), request)
	if err != nil {
		t.Fatalf("first RunGeneratedProof: %v", err)
	}
	if first.Outcome != ports.ClearDevCheckPass || first.ImageID != "sha256:shim-image" {
		t.Fatalf("first result = %#v", first)
	}
	second, err := New().RunGeneratedProof(context.Background(), request)
	if err != nil {
		t.Fatalf("second RunGeneratedProof: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("replayed result = %#v, want %#v", second, first)
	}
	if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, []string{"image", "run"}) {
		t.Fatalf("Docker actions = %#v, want one inspect and one run", got)
	}
	statePath, err := generatedProofRunStatePath(request.RunID)
	if err != nil {
		t.Fatalf("generatedProofRunStatePath: %v", err)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("state stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions = %o, want 600", info.Mode().Perm())
	}

	mismatch := request
	mismatch.Argv = []string{"node", "-e", "process.exit(1)"}
	result, err := New().RunGeneratedProof(context.Background(), mismatch)
	if err == nil || result.Outcome != ports.ClearDevCheckInfraError {
		t.Fatalf("mismatched RunID result=%#v err=%v, want deterministic rejection", result, err)
	}
	if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, []string{"image", "run"}) {
		t.Fatalf("mismatched RunID started Docker: %#v", got)
	}
}

func TestRunGeneratedProofRunIDWithUnsettledExternalActionFailsClosed(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	logPath := installGeneratedProofDockerShim(t)
	repo, candidate := committedGeneratedProofRepo(t)
	request := ports.ClearDevGeneratedProofRequest{
		RunID: "complex-generated-proof-unsettled", WorkspacePath: repo, CandidateSHA: candidate,
		Image: "node:22-bookworm-slim", Argv: []string{"node", "-e", "process.exit(0)"},
		OutputPaths: []string{"package-lock.json"}, Timeout: time.Second,
	}
	normalized, err := normalizeGeneratedProofRequest(request)
	if err != nil {
		t.Fatalf("normalizeGeneratedProofRequest: %v", err)
	}
	fingerprint, err := generatedProofRequestSHA256(normalized)
	if err != nil {
		t.Fatalf("generatedProofRequestSHA256: %v", err)
	}
	statePath, err := generatedProofRunStatePath(normalized.RunID)
	if err != nil {
		t.Fatalf("generatedProofRunStatePath: %v", err)
	}
	created, _, err := createOrReadGeneratedProofRunState(statePath, generatedProofRunState{Version: 1, Fingerprint: fingerprint, State: "started"})
	if err != nil || !created {
		t.Fatalf("create started state: created=%v err=%v", created, err)
	}

	result, err := New().RunGeneratedProof(context.Background(), request)
	if err == nil || result.Outcome != ports.ClearDevCheckInfraError {
		t.Fatalf("result=%#v err=%v, want unsettled infra error", result, err)
	}
	if got := strings.TrimSpace(readTestFile(t, logPath)); got != "" {
		t.Fatalf("unsettled external action invoked Docker: %q", got)
	}
}

func TestNormalizeGeneratedProofRequestRejectsMalformedCandidate(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	valid := strings.Repeat("a", 40)
	base := ports.ClearDevGeneratedProofRequest{
		RunID: "generated-proof-malformed", WorkspacePath: t.TempDir(), CandidateSHA: valid,
		Image: "node:22-bookworm-slim", Argv: []string{"node", "-e", "process.exit(0)"},
		OutputPaths: []string{"package-lock.json"}, Timeout: time.Second,
	}
	if _, err := normalizeGeneratedProofRequest(base); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{"abc", strings.ToUpper(valid), "HEAD", valid + "^{}", "refs/heads/main", valid[:39] + "G"} {
		req := base
		req.CandidateSHA = sha
		if _, err := normalizeGeneratedProofRequest(req); err == nil {
			t.Fatalf("normalize accepted %q", sha)
		}
		if _, err := New().RunGeneratedProof(context.Background(), req); err == nil {
			t.Fatalf("RunGeneratedProof accepted %q", sha)
		}
		statePath, pathErr := generatedProofRunStatePath(req.RunID)
		if pathErr != nil {
			continue
		}
		if _, err := os.Stat(statePath); !os.IsNotExist(err) {
			t.Fatalf("malformed candidate %q wrote run state: %v", sha, err)
		}
	}
}

func TestRunCandidateCheckRunIDWithUnsettledExternalActionFailsClosed(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	logPath := installDockerShim(t)
	repo, candidate := committedCheckRepo(t)
	request := ports.ClearDevCheckRequest{
		RunID: "complex-execution-check-unsettled", WorkspacePath: repo, CandidateSHA: candidate,
		Image: "approved-node-image", Argv: []string{"node", "--test", "test/email.test.js"}, Timeout: time.Second,
	}
	normalized, err := normalizeCheckRequest(request)
	if err != nil {
		t.Fatalf("normalizeCheckRequest: %v", err)
	}
	fingerprint, err := checkRequestSHA256(normalized)
	if err != nil {
		t.Fatalf("checkRequestSHA256: %v", err)
	}
	statePath, err := checkRunStatePath(normalized.RunID)
	if err != nil {
		t.Fatalf("checkRunStatePath: %v", err)
	}
	created, _, err := createOrReadCheckRunState(statePath, checkRunState{Version: checkRunStateVersion, Fingerprint: fingerprint, State: "started"})
	if err != nil || !created {
		t.Fatalf("create started state: created=%v err=%v", created, err)
	}

	result, err := New().RunCandidateCheck(context.Background(), request)
	if err == nil || result.Outcome != ports.ClearDevCheckInfraError || result.ExitCode != -1 {
		t.Fatalf("result=%#v err=%v, want unsettled infra error", result, err)
	}
	if got := strings.TrimSpace(readTestFile(t, logPath)); got != "" {
		t.Fatalf("unsettled external action invoked Docker: %q", got)
	}
}

func TestValidDockerIDRequiresFullLowerHex(t *testing.T) {
	if !validDockerID(strings.Repeat("a", 64)) {
		t.Fatal("full lowercase Docker id was rejected")
	}
	for _, value := range []string{"short", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if validDockerID(value) {
			t.Fatalf("invalid Docker id %q was accepted", value)
		}
	}
}

func TestDockerCheckArgsFreezeRestrictedContainerAndApprovedArgv(t *testing.T) {
	request := CheckContainerRequest{
		Image: "sha256:immutable", Argv: []string{"node", "--test", "test/email.test.js"},
		SourceDir: "/tmp/candidate", SourceBytes: 1024, SourceItems: 3,
		DependencyDir: "/tmp/dependencies/node_modules", Timeout: time.Minute,
		MemoryBytes: 256 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024,
	}
	got, err := CheckContainerRunArgs(request, "/tmp/container.cid")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run", "--rm", "--detach", "--stop-timeout=1", "--network=none", "--read-only",
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--cidfile=/tmp/container.cid",
		"--memory=268435456", "--memory-swap=268435456", "--pids-limit=64",
		"--user=65532:65532", "--env=HOME=/tmp", "--env=CLEARDEV_CHECK_SANDBOX=1",
		"--tmpfs=/tmp:rw,noexec,nosuid,size=16777216",
		"--tmpfs=/workspace:rw,mode=1777,size=268436480,nr_inodes=100003",
		"--mount=type=bind,src=/tmp/candidate,dst=/candidate-source,readonly",
		"--mount=type=bind,src=/tmp/dependencies/node_modules,dst=/workspace/node_modules,readonly",
		"sha256:immutable", "sleep", "infinity",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("docker args = %#v, want %#v", got, want)
	}
	execArgs, err := CheckContainerExecArgs(strings.Repeat("a", 64), request.Argv)
	if err != nil {
		t.Fatal(err)
	}
	wantExec := []string{"exec", "--user=65532:65532", "--workdir=/workspace", strings.Repeat("a", 64), "node", "--test", "test/email.test.js"}
	if !reflect.DeepEqual(execArgs, wantExec) {
		t.Fatalf("docker exec args = %#v, want %#v", execArgs, wantExec)
	}
}

func TestComplexParallelExecutionComposeDisjointAndConflict(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "ClearDev Test")
	writeTestFile(t, filepath.Join(repo, "README.md"), "base\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	runGit(t, repo, "checkout", "-b", "left")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, "src", "email.js"), "export const email = 1;\n")
	runGit(t, repo, "add", "src/email.js")
	runGit(t, repo, "commit", "-m", "email")
	left := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	runGit(t, repo, "checkout", "--detach", base)
	runGit(t, repo, "checkout", "-b", "right")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, "src", "deduplicate.js"), "export const deduplicate = 1;\n")
	runGit(t, repo, "add", "src/deduplicate.js")
	runGit(t, repo, "commit", "-m", "deduplicate")
	right := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	root := t.TempDir()
	runner := NewWithRoot(root)
	result, err := runner.ComposeCandidates(context.Background(), ports.ClearDevComposeRequest{
		RequestID: "compose-disjoint", RepoPath: repo, BaseSHA: base, CandidateSHAs: []string{left, right},
	})
	if err != nil {
		t.Fatalf("disjoint compose: %v", err)
	}
	if result.OutputSHA == "" || result.OutputSHA == base || result.OutputSHA == left || result.OutputSHA == right {
		t.Fatalf("composed sha = %q", result.OutputSHA)
	}
	if !strings.HasPrefix(result.WorkspacePath, root) {
		t.Fatalf("compose workspace %q is not under managed root %q", result.WorkspacePath, root)
	}
	if _, err := os.Stat(filepath.Join(result.WorkspacePath, "src", "email.js")); err != nil {
		t.Fatalf("composed email.js: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.WorkspacePath, "src", "deduplicate.js")); err != nil {
		t.Fatalf("composed deduplicate.js: %v", err)
	}

	runGit(t, repo, "checkout", "--detach", base)
	runGit(t, repo, "checkout", "-b", "conflict-a")
	writeTestFile(t, filepath.Join(repo, "README.md"), "left\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "conflict-a")
	conflictA := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "checkout", "--detach", base)
	runGit(t, repo, "checkout", "-b", "conflict-b")
	writeTestFile(t, filepath.Join(repo, "README.md"), "right\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "conflict-b")
	conflictB := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	conflict, err := runner.ComposeCandidates(context.Background(), ports.ClearDevComposeRequest{
		RequestID: "compose-conflict", RepoPath: repo, BaseSHA: base, CandidateSHAs: []string{conflictA, conflictB},
	})
	if !errors.Is(err, ports.ErrClearDevCompositionConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	if conflict.OutputSHA != "" || len(conflict.ConflictPaths) == 0 {
		t.Fatalf("conflict result = %#v", conflict)
	}
	if conflict.WorkspacePath == "" {
		t.Fatal("conflict compose lost its managed worktree")
	}
}

func TestQuickExecutionPrepareBaseWorkspaceUsesIntegrationSHA(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "ClearDev Test")
	writeTestFile(t, filepath.Join(repo, "README.md"), "base\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "base")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	runGit(t, repo, "checkout", "-b", "s07-integration")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, "src", "deduplicate.js"), "export const deduplicate = 1;\n")
	runGit(t, repo, "add", "src/deduplicate.js")
	runGit(t, repo, "commit", "-m", "s07")
	integration := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	workspace := filepath.Join(t.TempDir(), "quick-builder")
	runGit(t, repo, "worktree", "add", "--detach", workspace, head)
	runner := NewWithRoot(t.TempDir())
	if err := runner.PrepareBaseWorkspace(context.Background(), workspace, head, integration); err != nil {
		t.Fatalf("prepare QUICK base: %v", err)
	}
	got := strings.TrimSpace(runGit(t, workspace, "rev-parse", "HEAD"))
	if got != integration {
		t.Fatalf("QUICK workspace HEAD = %s, want S07 integration %s", got, integration)
	}
	status := strings.TrimSpace(runGit(t, workspace, "status", "--porcelain"))
	if status != "" {
		t.Fatalf("QUICK workspace is dirty: %q", status)
	}
}

func runGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", repo}, args...)
	output, err := exec.Command("git", argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func installDockerShim(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	logPath := filepath.Join(directory, "docker.log")
	script := filepath.Join(directory, "docker")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	content := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"image) printf 'image\\n' >> \"$CLEARDEV_DOCKER_LOG\"; printf 'sha256:shim-image\\n' ;;\n" +
		"run)\n" +
		"  printf 'run\\n' >> \"$CLEARDEV_DOCKER_LOG\"\n" +
		"  cidfile=''\n" +
		"  for arg in \"$@\"; do\n" +
		"    case \"$arg\" in --cidfile=*) cidfile=${arg#--cidfile=} ;; esac\n" +
		"    if [ \"$arg\" = '--version' ]; then\n" +
		"      [ -n \"$CLEARDEV_DOCKER_PROBE_STDERR\" ] && printf '%s\\n' \"$CLEARDEV_DOCKER_PROBE_STDERR\" >&2\n" +
		"      printf 'v22.0.0\\n'; exit 0\n" +
		"    fi\n" +
		"  done\n" +
		"  id=$(printf '%064d' 0 | tr '0' 'a')\n" +
		"  [ -n \"$cidfile\" ] && printf '%s\\n' \"$id\" > \"$cidfile\"\n" +
		"  printf '%s\\n' \"$id\"\n" +
		"  ;;\n" +
		"exec)\n" +
		"  for arg in \"$@\"; do\n" +
		"    [ \"$arg\" = 'stat' ] && { printf '1 4096 1\\n'; exit 0; }\n" +
		"    [ \"$arg\" = 'cp' ] && exit 0\n" +
		"  done\n" +
		"  printf 'shim check output\\n'\n" +
		"  ;;\n" +
		"inspect) printf '[]\\n' ;;\n" +
		"rm|ps) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLEARDEV_DOCKER_LOG", logPath)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func installDockerExitShim(t *testing.T, exitCode int) {
	t.Helper()
	directory := t.TempDir()
	script := filepath.Join(directory, "docker")
	content := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"image) printf 'sha256:shim-image\\n' ;;\n" +
		"run)\n" +
		"  cidfile=''\n" +
		"  for arg in \"$@\"; do\n" +
		"    if [ \"$arg\" = \"--version\" ]; then printf 'v22.0.0\\n'; exit 0; fi\n" +
		"    case \"$arg\" in --cidfile=*) cidfile=${arg#--cidfile=} ;; esac\n" +
		"  done\n" +
		"  id=$(printf '%064d' 0 | tr '0' 'a')\n" +
		"  [ -n \"$cidfile\" ] && printf '%s\\n' \"$id\" > \"$cidfile\"\n" +
		"  printf '%s\\n' \"$id\"\n" +
		"  ;;\n" +
		"exec)\n" +
		"  for arg in \"$@\"; do\n" +
		"    [ \"$arg\" = 'stat' ] && { printf '1 4096 1\\n'; exit 0; }\n" +
		"    [ \"$arg\" = 'cp' ] && exit 0\n" +
		"  done\n" +
		"  exit " + strconv.Itoa(exitCode) + "\n" +
		"  ;;\n" +
		"inspect) printf '[]\\n' ;;\n" +
		"rm|ps) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installDockerCancellationShim(t *testing.T, hangOn string) string {
	t.Helper()
	directory := t.TempDir()
	logPath := filepath.Join(directory, "docker.log")
	readyPath := filepath.Join(directory, "docker.ready")
	script := filepath.Join(directory, "docker")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	content := "#!/bin/sh\n" +
		"printf '%s\\n' \"$1\" >> \"$CLEARDEV_DOCKER_LOG\"\n" +
		"case \"$1\" in\n" +
		"image) printf 'sha256:shim-image\\n' ;;\n" +
		"inspect) printf '[]\\n' ;;\n" +
		"rm|ps) exit 0 ;;\n" +
		"run)\n" +
		"  cidfile=''\n" +
		"  version=0\n" +
		"  for arg in \"$@\"; do\n" +
		"    case \"$arg\" in --cidfile=*) cidfile=${arg#--cidfile=} ;; esac\n" +
		"    [ \"$arg\" = '--version' ] && version=1\n" +
		"  done\n" +
		"  if [ \"$CLEARDEV_HANG_ON\" = 'probe' ] && [ \"$version\" = 1 ]; then\n" +
		"    printf '%064d\\n' 0 | tr '0' 'a' > \"$cidfile\"\n" +
		"    : > \"$CLEARDEV_DOCKER_READY\"\n" +
		"    while :; do :; done\n" +
		"  fi\n" +
		"  if [ \"$version\" = 1 ]; then printf 'v22.0.0\\n'; exit 0; fi\n" +
		"  id=$(printf '%064d' 0 | tr '0' 'a')\n" +
		"  [ -n \"$cidfile\" ] && printf '%s\\n' \"$id\" > \"$cidfile\"\n" +
		"  printf '%s\\n' \"$id\"\n" +
		"  ;;\n" +
		"exec)\n" +
		"  prepare=0\n" +
		"  setup=0\n" +
		"  for arg in \"$@\"; do\n" +
		"    [ \"$arg\" = 'stat' ] && { printf '1 4096 1\\n'; exit 0; }\n" +
		"    [ \"$arg\" = 'ci' ] && prepare=1\n" +
		"    [ \"$arg\" = 'cp' ] && setup=1\n" +
		"  done\n" +
		"  if { [ \"$CLEARDEV_HANG_ON\" = 'prepare' ] && [ \"$prepare\" = 1 ]; } || { [ \"$CLEARDEV_HANG_ON\" = 'formal' ] && [ \"$prepare\" = 0 ] && [ \"$setup\" = 0 ]; }; then\n" +
		"    : > \"$CLEARDEV_DOCKER_READY\"\n" +
		"    while :; do :; done\n" +
		"  fi\n" +
		"  exit 0\n" +
		"  ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLEARDEV_DOCKER_LOG", logPath)
	t.Setenv("CLEARDEV_DOCKER_READY", readyPath)
	t.Setenv("CLEARDEV_HANG_ON", hangOn)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func skipIfNestedDockerBindMounts(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/.dockerenv"); err == nil {
		t.Skip("real Docker bind mounts are not nested-container safe")
	}
}

func requireProductionCheckPrerequisites(t *testing.T, image string) {
	t.Helper()
	require := os.Getenv("CLEARDEV_REQUIRE_DOCKER_CHECKS") == "1"
	failOrSkip := func(message string, args ...any) {
		if require {
			t.Fatalf(message, args...)
		}
		t.Skipf(message, args...)
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		failOrSkip("production Docker checks need host Docker, not a nested CI container")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		failOrSkip("Docker is unavailable: %v", err)
	}
	if output, err := exec.Command("docker", "image", "inspect", image).CombinedOutput(); err != nil {
		failOrSkip("required immutable image is unavailable: %v: %s", err, strings.TrimSpace(string(output)))
	}
	if _, err := trustedNPMCachePath(); err != nil {
		failOrSkip("trusted offline npm cache is unavailable: %v", err)
	}
}

func runnerRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve runner test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}

func installGeneratedProofDockerShim(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	logPath := filepath.Join(directory, "docker.log")
	lockPath := filepath.Join(directory, "package-lock.json")
	script := filepath.Join(directory, "docker")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, lockPath, frozenGeneratedLockFixture())
	content := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"image) printf 'image\\n' >> \"$CLEARDEV_DOCKER_LOG\"; printf 'sha256:shim-image\\n' ;;\n" +
		"run)\n" +
		"  printf 'run\\n' >> \"$CLEARDEV_DOCKER_LOG\"\n" +
		"  cidfile=''\n" +
		"  for arg in \"$@\"; do\n" +
		"    case \"$arg\" in --cidfile=*) cidfile=${arg#--cidfile=} ;; esac\n" +
		"  done\n" +
		"  id=$(printf '%064d' 0 | tr '0' 'a')\n" +
		"  [ -n \"$cidfile\" ] && printf '%s\\n' \"$id\" > \"$cidfile\"\n" +
		"  printf '%s\\n' \"$id\"\n" +
		"  ;;\n" +
		"exec)\n" +
		"  archive=0\n" +
		"  for arg in \"$@\"; do\n" +
		"    [ \"$arg\" = 'stat' ] && { printf '1 4096 1\\n'; exit 0; }\n" +
		"    [ \"$arg\" = 'tar' ] && archive=1\n" +
		"  done\n" +
		"  if [ \"$archive\" = 1 ]; then tar -cf - -C \"$(dirname \"$CLEARDEV_GENERATED_LOCK\")\" \"$(basename \"$CLEARDEV_GENERATED_LOCK\")\"; exit 0; fi\n" +
		"  exit 0\n" +
		"  ;;\n" +
		"inspect) printf '[]\\n' ;;\n" +
		"rm|ps) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLEARDEV_DOCKER_LOG", logPath)
	t.Setenv("CLEARDEV_GENERATED_LOCK", lockPath)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func committedCheckRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "ClearDev Test")
	writeTestFile(t, filepath.Join(repo, "package.json"), "{\"private\":true}\n")
	runGit(t, repo, "add", "package.json")
	runGit(t, repo, "commit", "-m", "candidate")
	return repo, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func committedLegacyNodeCheckRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "ClearDev Test")
	if err := os.MkdirAll(filepath.Join(repo, "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, "package.json"), "{\"private\":true}\n")
	writeTestFile(t, filepath.Join(repo, "test", "email.test.js"), "const assert = require('node:assert/strict');\nconst test = require('node:test');\ntest('legacy single file', () => assert.equal(' A@B.COM '.trim().toLowerCase(), 'a@b.com'));\n")
	runGit(t, repo, "add", "package.json", "test/email.test.js")
	runGit(t, repo, "commit", "-m", "legacy Node single-file check")
	return repo, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func committedMismatchedNPMRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "ClearDev Test")
	writeTestFile(t, filepath.Join(repo, "package.json"), "{\"name\":\"mismatch\",\"version\":\"1.0.0\",\"private\":true,\"devDependencies\":{\"typescript\":\"5.8.3\"}}\n")
	writeTestFile(t, filepath.Join(repo, "package-lock.json"), frozenGeneratedLockFixture())
	runGit(t, repo, "add", "package.json", "package-lock.json")
	runGit(t, repo, "commit", "-m", "mismatched npm manifests")
	return repo, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func committedGeneratedProofRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "ClearDev Test")
	writeTestFile(t, filepath.Join(repo, "package.json"), "{\"private\":true}\n")
	writeTestFile(t, filepath.Join(repo, "package-lock.json"), frozenGeneratedLockFixture())
	runGit(t, repo, "add", "package.json", "package-lock.json")
	runGit(t, repo, "commit", "-m", "candidate")
	return repo, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func frozenGeneratedLockFixture() string {
	return "{\n  \"name\": \"app\",\n  \"version\": \"0.0.0\",\n  \"lockfileVersion\": 3,\n  \"requires\": true,\n  \"packages\": {\n    \"\": {\n      \"name\": \"app\",\n      \"version\": \"0.0.0\",\n      \"private\": true\n    }\n  }\n}\n"
}
