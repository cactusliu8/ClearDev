package cleardevlocal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func baselineGateRepo(t *testing.T, legacy bool) string {
	t.Helper()
	repo := t.TempDir()
	copyProduct := core.CopyDemoBaseline
	if legacy {
		copyProduct = core.CopyDemoTemplate
	}
	if err := copyProduct(repo); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.name", "Baseline Test")
	runGit(t, repo, "config", "user.email", "baseline@localhost")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "initial mail product")
	return repo
}

func baselineGateData(t *testing.T) {
	t.Helper()
	data := t.TempDir()
	t.Setenv("AO_DATA_DIR", data)
	t.Cleanup(func() { cleanupPreparedTree(filepath.Join(data, checkDependencyDirectory)) })
}

func TestDevelopmentBaselineHealthyAndRestartReplay(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	baselineGateData(t)
	repo := baselineGateRepo(t, false)
	request := ports.ClearDevBaselineRequest{RunID: "baseline-start", WorkspacePath: repo}
	first, err := New().CheckDevelopmentBaseline(context.Background(), request)
	if err != nil || !first.Required || first.Tests.Outcome != ports.ClearDevCheckPass || first.Health.Outcome != ports.ClearDevCheckPass {
		t.Fatalf("baseline=%#v err=%v", first, err)
	}
	if first.CandidateSHA != strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")) {
		t.Fatal("baseline was checked at the wrong SHA")
	}
	t.Logf("candidate=%s npm=%s health=%s testRun=%s healthRun=%s", first.CandidateSHA, first.Tests.Outcome, first.Health.Outcome, first.TestRunID, first.HealthRunID)
	// A restarted runner must use its durable records, not run Docker again.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\necho 'unexpected repeated Docker action' >&2\nexit 93\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	second, err := New().CheckDevelopmentBaseline(context.Background(), request)
	if err != nil || first != second {
		t.Fatalf("restart changed evidence: %#v err=%v", second, err)
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain", "--untracked-files=all")); status != "" {
		t.Fatalf("baseline became dirty: %s", status)
	}
	file := filepath.Join(repo, "frontend", "app.js")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "new candidate")
	stale, err := New().CheckDevelopmentBaseline(context.Background(), request)
	if err == nil || stale.ReasonCode != "BASELINE_CHECKER_UNAVAILABLE" {
		t.Fatalf("stale SHA reused old baseline checks: %#v %v", stale, err)
	}
}

func TestDevelopmentBaselineLegacyFailureDoesNotStartHealth(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	baselineGateData(t)
	repo := baselineGateRepo(t, true)
	result, err := New().CheckDevelopmentBaseline(context.Background(), ports.ClearDevBaselineRequest{RunID: "legacy-unhealthy", WorkspacePath: repo})
	if err == nil || result.ReasonCode != "BASELINE_TEST_FAILED" || result.Tests.Outcome != ports.ClearDevCheckFail || result.Health.CheckEnvironmentID != "" {
		t.Fatalf("old unfinished template was not stopped after full npm test: %#v %v", result, err)
	}
}

func TestDevelopmentBaselineRejectsDirtyAndRenamedPackageBeforeDocker(t *testing.T) {
	for _, mode := range []string{"dirty", "staged", "renamed", "removed"} {
		t.Run(mode, func(t *testing.T) {
			baselineGateData(t)
			repo := baselineGateRepo(t, false)
			name := filepath.Join(repo, "package.json")
			if mode == "dirty" || mode == "staged" {
				if err := os.WriteFile(filepath.Join(repo, "user-work"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				if mode == "staged" {
					runGit(t, repo, "add", ".")
				}
			} else {
				if mode == "renamed" {
					data, err := os.ReadFile(name)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(name, []byte(strings.Replace(string(data), "complex-mail-app", "disguised", 1)), 0o600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				runGit(t, repo, "add", ".")
				runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "cannot opt out")
			}
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 93\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			result, err := New().CheckDevelopmentBaseline(context.Background(), ports.ClearDevBaselineRequest{RunID: "invalid-baseline", WorkspacePath: repo})
			want := "BASELINE_WORKSPACE_INVALID"
			if mode == "renamed" || mode == "removed" {
				want = "BASELINE_CONTRACT_CHANGED"
			}
			if err == nil || !result.Required || result.ReasonCode != want {
				t.Fatalf("%s: %#v %v", mode, result, err)
			}
		})
	}
}

func TestTrustedBaselineHealthRejectsFalseHealthAndStartupFailure(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	baselineGateData(t)
	for _, mode := range []string{"404", "500", "wrong-body", "startup-failure"} {
		t.Run(mode, func(t *testing.T) {
			repo := baselineGateRepo(t, false)
			status, body := "200", `{"status":"wrong","application":"complex-mail-app"}`
			if mode == "404" || mode == "500" {
				status, body = mode, `{"status":"ok","application":"complex-mail-app"}`
			}
			source := `import http from "node:http"; http.createServer((req,res)=>{res.writeHead(` + status + `,{"content-type":"application/json"});res.end('` + body + `');}).listen(Number(process.env.PORT),"127.0.0.1");`
			if mode == "startup-failure" {
				source = `throw new Error("intentional startup failure");`
			}
			if err := os.WriteFile(filepath.Join(repo, "backend/src/server.ts"), []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", ".")
			runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "bad startup fixture")
			sha := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			result, err := New().RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{
				RunID: "bad-health-" + mode, WorkspacePath: repo, CandidateSHA: sha, Image: core.StandardCandidateCheckImage,
				Argv: []string{"node", "--input-type=module", "-e", baselineHealthProbe}, Timeout: 20 * time.Second,
				MemoryBytes: baselineMemoryBytes, PidsLimit: 64, OutputLimit: 64 * 1024,
			})
			if err != nil || result.Outcome != ports.ClearDevCheckFail || result.ExitCode == 0 {
				t.Fatalf("false health passed or was not really executed: %#v %v", result, err)
			}
		})
	}
}
