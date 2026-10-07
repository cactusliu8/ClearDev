package cleardevlocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func newMailFreezeFixture(t *testing.T) (*Runner, ports.ClearDevMailFreezeRequest) {
	t.Helper()
	baselineGateData(t)
	repo := baselineGateRepo(t, false)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	managed := t.TempDir()
	workspace := filepath.Join(managed, "builder")
	branch := "cleardev-complex-builder-freeze-test"
	runGit(t, repo, "worktree", "add", "-b", branch, workspace, base)
	return NewWithRoot(managed), ports.ClearDevMailFreezeRequest{RunID: "explicit-nonformal-dispatch", RepoPath: repo, WorkspacePath: workspace, Branch: branch, BaseSHA: base, ParentSHA: base,
		WritePaths: []string{"backend/src/**", "frontend/**", "test/**"}}
}

func appendFreezeTestFile(t *testing.T, path, addition string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte(addition)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedMailFreezePublishesOnlyBoundBuilderAndReplays(t *testing.T) {
	runner, request := newMailFreezeFixture(t)
	appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "frontend/app.js"), "\n// explicit nonformal implementation fixture\n")
	if _, err := runner.InspectCandidate(context.Background(), request.WorkspacePath, request.BaseSHA); !errors.Is(err, ports.ErrClearDevWorkspaceDirty) {
		t.Fatal("original clean-HEAD-only entry did not reproduce the handoff blocker", err)
	}
	first, err := runner.FreezeMailCandidate(context.Background(), request)
	if err != nil || first.CandidateSHA == request.BaseSHA || len(first.Paths) != 1 {
		t.Fatalf("freeze=%+v err=%v", first, err)
	}
	if got := strings.TrimSpace(runGit(t, request.RepoPath, "rev-parse", "main")); got != request.BaseSHA {
		t.Fatal("freezer moved user's main branch")
	}
	if got := runGit(t, request.WorkspacePath, "status", "--porcelain"); got != "" {
		t.Fatal("frozen Builder is not clean", got)
	}
	second, err := NewWithRoot(runner.managedRoot).FreezeMailCandidate(context.Background(), request)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	if count := strings.TrimSpace(runGit(t, request.WorkspacePath, "rev-list", "--count", request.BaseSHA+"..HEAD")); count != "1" {
		t.Fatal("repeated freeze added commits", count)
	}
	request.WritePaths = []string{"frontend/**"}
	if _, err := runner.FreezeMailCandidate(context.Background(), request); err == nil {
		t.Fatal("same dispatch accepted a different authorization")
	}
}

func TestTrustedMailFreezeRejectsScopeHistoryAndIdentityChanges(t *testing.T) {
	for _, mode := range []string{"package", "unlisted", "old-test-edit", "old-test-delete", "old-test-rename", "old-test-mode", "symlink", "unsupported-test", "parent", "branch", "project", "metadata", "staged", "empty"} {
		t.Run(mode, func(t *testing.T) {
			runner, request := newMailFreezeFixture(t)
			repo := request.RepoPath
			file := func(name string) string { return filepath.Join(request.WorkspacePath, name) }
			if mode != "empty" {
				appendFreezeTestFile(t, file("frontend/app.js"), "\n// changed\n")
			}
			switch mode {
			case "package":
				appendFreezeTestFile(t, file("package.json"), "\n")
			case "unlisted":
				request.WritePaths = []string{"test/**"}
			case "old-test-edit":
				writeTestFile(t, file("test/app.test.js"), "// no assertions\n")
			case "old-test-delete":
				if err := os.Remove(file("test/app.test.js")); err != nil {
					t.Fatal(err)
				}
			case "old-test-rename":
				if err := os.Rename(file("test/app.test.js"), file("test/renamed.test.js")); err != nil {
					t.Fatal(err)
				}
			case "old-test-mode":
				if err := os.Chmod(file("test/app.test.js"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(request.RepoPath, "package.json"), file("frontend/escape.js")); err != nil {
					t.Fatal(err)
				}
			case "unsupported-test":
				writeTestFile(t, file("test/never-run.py"), "assert False\n")
			case "parent":
				request.ParentSHA = strings.Repeat("f", 40)
			case "branch":
				request.Branch += "-wrong"
			case "project":
				request.RepoPath = t.TempDir()
			case "metadata":
				writeTestFile(t, file(".git"), "gitdir: "+filepath.Join(request.RepoPath, ".git")+"\n")
			case "staged":
				runGit(t, request.WorkspacePath, "add", "frontend/app.js")
			}
			if got, err := runner.FreezeMailCandidate(context.Background(), request); err == nil {
				t.Fatalf("invalid %s accepted: %+v", mode, got)
			}
			if got := strings.TrimSpace(runGit(t, repo, "rev-parse", "main")); got != request.BaseSHA {
				t.Fatal("invalid freeze moved main")
			}
		})
	}
}

func TestTrustedMailFreezeCrashWindowsAndConcurrentReplay(t *testing.T) {
	for _, stage := range []string{"prepared", "ref-published", "index-published"} {
		t.Run(stage, func(t *testing.T) {
			runner, request := newMailFreezeFixture(t)
			appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "test/app.test.js"), "\n// retained original prefix\n")
			hit := false
			_, err := runner.freezeMailCandidate(context.Background(), request, func(current string) error {
				if current == stage {
					hit = true
					return errors.New("explicit crash")
				}
				return nil
			})
			if err == nil || !hit {
				t.Fatal("crash boundary not reached")
			}
			var group sync.WaitGroup
			results := make([]ports.ClearDevCandidateInspection, 8)
			errs := make([]error, 8)
			for i := range results {
				group.Add(1)
				go func(i int) {
					defer group.Done()
					results[i], errs[i] = NewWithRoot(runner.managedRoot).FreezeMailCandidate(context.Background(), request)
				}(i)
			}
			group.Wait()
			for i, result := range results {
				if errs[i] != nil || !reflect.DeepEqual(result, results[0]) {
					t.Fatalf("replay[%d]=%+v %v", i, result, errs[i])
				}
			}
			if count := strings.TrimSpace(runGit(t, request.WorkspacePath, "rev-list", "--count", request.BaseSHA+"..HEAD")); count != "1" {
				t.Fatal("duplicate publication")
			}
		})
	}
}

func TestTrustedMailFreezeNeverRecapturesChangedPreparedTree(t *testing.T) {
	runner, request := newMailFreezeFixture(t)
	file := filepath.Join(request.WorkspacePath, "frontend/app.js")
	appendFreezeTestFile(t, file, "\n// prepared\n")
	_, err := runner.freezeMailCandidate(context.Background(), request, func(stage string) error {
		if stage == "prepared" {
			appendFreezeTestFile(t, file, "// concurrent edit\n")
		}
		return nil
	})
	if err == nil {
		t.Fatal("changed snapshot was published")
	}
	if _, err := NewWithRoot(runner.managedRoot).FreezeMailCandidate(context.Background(), request); err == nil {
		t.Fatal("restart silently recaptured different bytes")
	}
	if head := strings.TrimSpace(runGit(t, request.WorkspacePath, "rev-parse", "HEAD")); head != request.ParentSHA {
		t.Fatal("raced candidate moved branch")
	}
}

func TestTrustedMailFreezeDoesNotExecuteGitHooksOrFilters(t *testing.T) {
	runner, request := newMailFreezeFixture(t)
	marker := filepath.Join(t.TempDir(), "untrusted-executed")
	hook := filepath.Join(t.TempDir(), "hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+marker+"'\ntr '\\0' ' ' < /proc/$PPID/cmdline >> '"+marker+"'\nexit 91\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"core.fsmonitor", "filter.evil.clean", "filter.evil.smudge"} {
		runGit(t, request.RepoPath, "config", key, hook)
	}
	writeTestFile(t, filepath.Join(request.RepoPath, ".git/info/attributes"), "*.js filter=evil\n")
	for _, name := range []string{"pre-commit", "post-commit", "reference-transaction"} {
		if err := os.Symlink(hook, filepath.Join(request.RepoPath, ".git/hooks", name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("test preparation executed a hook before freezing", readTestFile(t, marker))
	}
	appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "frontend/app.js"), "\n// raw bytes, no filter\n")
	if _, err := runner.FreezeMailCandidate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("freezer executed untrusted Git code", err, readTestFile(t, marker))
	}
}

func TestTrustedMailFreezeFeedsProductionChecksWithExactSHA(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	for _, failing := range []bool{false, true} {
		t.Run(map[bool]string{false: "pass", true: "test-fail"}[failing], func(t *testing.T) {
			runner, request := newMailFreezeFixture(t)
			assertion := "true"
			if failing {
				assertion = "false"
			}
			writeTestFile(t, filepath.Join(request.WorkspacePath, "test/trusted-freeze.test.js"), "import test from 'node:test'; import assert from 'node:assert/strict'; test('explicit frozen handoff regression',()=>assert.ok("+assertion+"));\n")
			frozen, err := runner.FreezeMailCandidate(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			check := ports.ClearDevDeliveryRequest{RunID: "nonformal-frozen-check", WorkspacePath: request.WorkspacePath, BaseSHA: request.BaseSHA, CandidateSHA: frozen.CandidateSHA}
			result, err := runner.RunMailDeliveryCheck(context.Background(), check)
			if err != nil {
				t.Fatal(err)
			}
			if failing {
				if result.Outcome != ports.ClearDevCheckFail {
					t.Fatal("new unlisted npm test failure did not stop delivery", result.Outcome)
				}
			} else if result.Outcome != ports.ClearDevCheckPass || core.ValidateMailDeliveryProof(result.OutputSummary, request.BaseSHA, frozen.CandidateSHA, check.RunID, result.ImageID) != nil {
				t.Fatal("missing same-SHA full npm test, extra test or real HTTP health", result.OutputSummary)
			}
			t.Logf("frozen=%s production delivery=%s", frozen.CandidateSHA, result.Outcome)
		})
	}
}
