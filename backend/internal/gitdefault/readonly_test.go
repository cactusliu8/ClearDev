package gitdefault

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func readOnlyGitRunner(t *testing.T) Runner {
	t.Helper()
	return func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if len(args) < 3 || args[0] != "-C" {
			t.Fatalf("unexpected Git inspection: %v", args)
		}
		switch args[2] {
		case "rev-parse", "check-ref-format", "log":
		case "remote":
			if len(args) != 3 {
				t.Fatalf("inspection attempted a remote operation: %v", args)
			}
		case "config":
			if !slices.Contains(args[3:], "--get") {
				t.Fatalf("inspection attempted to write config: %v", args)
			}
		case "symbolic-ref":
			if len(args) != 5 || args[3] != "--quiet" {
				t.Fatalf("inspection attempted to write a symbolic ref: %v", args)
			}
		default:
			t.Fatalf("inspection attempted a network or mutating command: %v", args)
		}
		return runCommand(ctx, binary, args...)
	}
}

func TestInspectReadOnlyPreservesLegacyAOConfiguration(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	run(t, "git", "init", "-b", "main", repo)
	runGit(t, repo, "config", "user.name", "Agent Orchestrator")
	runGit(t, repo, "config", "user.email", "ao@example.com")
	runGit(t, repo, "commit", "--allow-empty", "-m", legacyInitialCommitSubject)
	runGit(t, repo, "switch", "-c", "unrelated")
	before := gitOutput(t, repo, "config", "--local", "--list")
	resolution, err := New("", readOnlyGitRunner(t)).InspectReadOnly(context.Background(), repo)
	if err != nil || resolution.Ref != "refs/heads/main" || resolution.Source != SourceAOInitialized {
		t.Fatalf("read-only legacy resolution: %+v err=%v", resolution, err)
	}
	if after := gitOutput(t, repo, "config", "--local", "--list"); after != before {
		t.Fatal("read-only inspection backfilled legacy configuration")
	}
	// Existing registration semantics still backfill; only the new read-only
	// inspection entry point suppresses that side effect.
	if _, err := New("", nil).Inspect(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, repo, "config", "--local", "--get", ManagedDefaultConfigKey); got != "main" {
		t.Fatalf("legacy Inspect stopped backfilling: %q", got)
	}
}

func TestInspectReadOnlyNeverContactsOfflineRemote(t *testing.T) {
	origin, repo := remoteRepo(t, "trunk")
	if err := os.Rename(origin, origin+".offline"); err != nil {
		t.Fatal(err)
	}
	before := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)")
	resolution, err := New("", readOnlyGitRunner(t)).InspectReadOnly(context.Background(), repo)
	if err != nil || resolution.Ref != "refs/remotes/origin/trunk" || resolution.Source != SourceCachedRemoteHead {
		t.Fatalf("read-only cached resolution: %+v err=%v", resolution, err)
	}
	if after := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)"); after != before {
		t.Fatal("read-only inspection updated cached refs")
	}
}
