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

func TestDemoBaselineProductionChecksWithIsolatedHome(t *testing.T) {
	if testing.Short() {
		t.Skip("real Docker baseline checks are not a short test")
	}
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	hostCache, err := trustedNPMCachePath()
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := core.CopyDemoBaseline(repo); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "--quiet", "-b", "demo-baseline")
	runGit(t, repo, "add", ".")
	t.Setenv("GIT_AUTHOR_DATE", "2026-09-19T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-09-19T00:00:00Z")
	runGit(t, repo, "-c", "user.name=ClearDev Baseline", "-c", "user.email=baseline@localhost", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Freeze healthy complex-mail-app baseline")
	candidate := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	dataDir := t.TempDir()
	t.Setenv("AO_DATA_DIR", dataDir)
	t.Cleanup(func() { cleanupPreparedTree(filepath.Join(dataDir, checkDependencyDirectory)) })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLEARDEV_NPM_CACHE_DIR", "")
	if _, err := trustedNPMCachePath(); err == nil {
		t.Fatal("isolated HOME unexpectedly found the host cache without explicit wiring")
	}
	// The daemon receives this trusted host setting BEFORE HOME is isolated.
	// Never discover a cache from candidate-controlled .npmrc or enable network.
	t.Setenv("CLEARDEV_NPM_CACHE_DIR", hostCache)
	commands := [][]string{{"npm", "run", "test:template"}, {"npm", "test"}}
	environment, err := New().PrepareCandidateChecks(context.Background(), ports.ClearDevCheckPreflightRequest{
		RunID: "demo-baseline-preflight", WorkspacePath: repo, CandidateSHA: candidate,
		Image: core.StandardCandidateCheckImage, Argv: commands, Timeout: 2 * time.Minute,
		MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if environment.CandidateSHA != candidate || environment.CheckEnvironmentID == "" || environment.DependencyEnvironmentID == "" {
		t.Fatalf("unbound environment: %#v", environment)
	}
	for index, command := range commands {
		id := "demo-baseline-sandbox"
		if index == 1 {
			id = "demo-baseline-full-test"
		}
		result, err := New().RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{
			RunID: id, WorkspacePath: repo, CandidateSHA: candidate,
			Image: core.StandardCandidateCheckImage, Argv: command, Timeout: time.Minute,
			MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024,
		})
		if err != nil || result.Outcome != ports.ClearDevCheckPass || result.ExitCode != 0 {
			t.Fatalf("%v result=%#v err=%v", command, result, err)
		}
		if result.CandidateSHA != candidate || result.ImageID != environment.ImageID || result.DependencyEnvironmentID != environment.DependencyEnvironmentID || result.PackageJSONSHA256 != environment.PackageJSONSHA256 || result.PackageLockSHA256 != environment.PackageLockSHA256 {
			t.Fatalf("check lost its frozen candidate binding: %#v", result)
		}
		t.Logf("candidate=%s argv=%q outcome=%s exit=%d environment=%s image=%s node=%s npm=%s output=%s", candidate, command, result.Outcome, result.ExitCode, result.CheckEnvironmentID, result.ImageID, result.NodeVersion, result.NPMVersion, result.OutputSummary)
		if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain", "--untracked-files=all")); status != "" {
			t.Fatalf("trusted check changed baseline: %s", status)
		}
		if head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")); head != candidate {
			t.Fatalf("trusted check moved baseline: %s", head)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("check leaked installed dependencies into baseline: %v", err)
	}
}

func TestDependencyExportDirectoryCannotHidePreparationFailures(t *testing.T) {
	for _, mode := range []string{"success", "install-failure", "mkdir-failure"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"package.json", "package-lock.json"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(dir, "actions")
			script := "#!/bin/sh\n" +
				"case \"$*\" in\n" +
				"  *' cp /input/'*) exit 0 ;;\n" +
				"  *' npm ci '*) printf 'install\\n' >> \"$DEMO_ACTIONS\"; [ \"$DEMO_MODE\" != 'install-failure' ]; exit $? ;;\n" +
				"  *' mkdir -p /prepare/node_modules') printf 'mkdir\\n' >> \"$DEMO_ACTIONS\"; [ \"$DEMO_MODE\" != 'mkdir-failure' ]; exit $? ;;\n" +
				"  *) exit 1 ;;\n" +
				"esac\n"
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DEMO_ACTIONS", log)
			t.Setenv("DEMO_MODE", mode)
			err := runDependencyPreparation(context.Background(), ports.ClearDevCheckPreflightRequest{Timeout: time.Second}, "test-container", dir)
			actions, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantActions := "install\nmkdir\n"
			switch mode {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "install-failure":
				wantActions = "install\n"
				if err == nil || !strings.Contains(err.Error(), "offline npm dependency preparation failed") {
					t.Fatalf("install failure was hidden: %v", err)
				}
			case "mkdir-failure":
				if err == nil || !strings.Contains(err.Error(), "prepare dependency export directory") {
					t.Fatalf("directory failure was hidden: %v", err)
				}
			}
			if string(actions) != wantActions {
				t.Fatalf("actions = %q, want %q", actions, wantActions)
			}
		})
	}
}
