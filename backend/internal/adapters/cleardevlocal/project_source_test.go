package cleardevlocal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/gitworktree"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestProjectSourceObservesEmptyExistingAndSelectedBranch(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "project-test@example.invalid")
	runGit(t, repo, "config", "user.name", "Project Source Test")
	runGit(t, repo, "commit", "--allow-empty", "-m", "Empty project baseline")
	initial := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runner := New()
	observed, err := runner.InspectProjectSource(context.Background(), repo, "main")
	if err != nil || !observed.Empty || observed.BaseCommitSHA != initial || observed.RepositoryURL != "" {
		t.Fatalf("empty project: %+v %v", observed, err)
	}

	runGit(t, repo, "checkout", "-b", "selected")
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{"name":"notes","scripts":{"test":"exit 1"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "package.json")
	runGit(t, repo, "commit", "-m", "Proposed notes project")
	selected := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "remote", "add", "origin", "https://example.org/notes/notes.git")
	runGit(t, repo, "checkout", "main")
	observed, err = runner.InspectProjectSource(context.Background(), repo, "selected")
	if err != nil || observed.Empty || observed.BaseCommitSHA != selected || observed.RepositoryURL != "https://example.org/notes/notes.git" {
		t.Fatalf("selected branch, not current checkout: %+v %v", observed, err)
	}
	if head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")); head != initial {
		t.Fatal("inspection changed the current checkout")
	}
	if _, err := os.Stat(filepath.Join(repo, "package.json")); !os.IsNotExist(err) {
		t.Fatal("inspection materialized or ran selected project files")
	}
	if _, err := runner.InspectProjectSource(context.Background(), repo, "missing"); !errors.Is(err, ports.ErrClearDevCandidateInvalid) {
		t.Fatalf("unknown branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "unfinished.txt"), []byte("user work"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.InspectProjectSource(context.Background(), repo, "selected"); !errors.Is(err, ports.ErrClearDevWorkspaceDirty) {
		t.Fatalf("untracked work must not silently become a baseline: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "unfinished.txt"))
	if err != nil || string(data) != "user work" {
		t.Fatal("inspection modified user work")
	}
}

func TestProjectSourceDoesNotInitializeOrCommitAnUnbornRepository(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	if _, err := New().InspectProjectSource(context.Background(), repo, "main"); !errors.Is(err, ports.ErrClearDevCandidateInvalid) {
		t.Fatalf("unborn repository needs the existing registration workflow: %v", err)
	}
	if refs := strings.TrimSpace(runGit(t, repo, "for-each-ref", "--format=%(refname)")); refs != "" {
		t.Fatal("read-only inspection created a commit or branch")
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain")); status != "" {
		t.Fatal("read-only inspection created files")
	}
}

func TestProjectSourceUsesWorkspaceDefaultBranchResolution(t *testing.T) {
	ctx := context.Background()
	repo, remote := t.TempDir(), t.TempDir()
	runGit(t, remote, "init", "--bare", "-b", "main")
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "source-test@example.invalid")
	runGit(t, repo, "config", "user.name", "Source Test")
	runGit(t, repo, "commit", "--allow-empty", "-m", "Local main A")
	local := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "checkout", "-b", "remote-tip")
	runGit(t, repo, "commit", "--allow-empty", "-m", "Remote main B")
	remoteSHA := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "push", "origin", "HEAD:main")
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	runGit(t, repo, "checkout", "-b", "feature")
	runGit(t, repo, "commit", "--allow-empty", "-m", "Unrelated checkout C")
	checkout := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	ws, err := gitworktree.New(gitworktree.Options{ManagedRoot: t.TempDir(), RepoResolver: gitworktree.StaticRepoResolver{"source-project": repo}})
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ configured, want string }{
		{"main", remoteSHA}, {"origin/main", remoteSHA}, {"refs/remotes/origin/main", remoteSHA},
		{"auto", remoteSHA}, {"", remoteSHA}, {"refs/heads/main", local},
	} {
		t.Run(fmt.Sprintf("%d-%s", i, tc.configured), func(t *testing.T) {
			beforeRefs := runGit(t, repo, "for-each-ref", "--format=%(refname) %(objectname)")
			beforeConfig := runGit(t, repo, "config", "--local", "--list")
			observed, err := New().InspectProjectSource(ctx, repo, tc.configured)
			if err != nil || observed.BaseCommitSHA != tc.want {
				t.Fatalf("selected source: %+v err=%v want=%s", observed, err, tc.want)
			}
			if beforeRefs != runGit(t, repo, "for-each-ref", "--format=%(refname) %(objectname)") || beforeConfig != runGit(t, repo, "config", "--local", "--list") {
				t.Fatal("source observation modified refs or local configuration")
			}
			if got := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")); got != checkout {
				t.Fatal("observation changed the unrelated checkout")
			}
			info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: "source-project", SessionID: domain.SessionID(fmt.Sprintf("source-test-%d", i)), Kind: domain.KindWorker, Branch: fmt.Sprintf("source-test-%d", i), BaseBranch: (domain.ProjectConfig{DefaultBranch: tc.configured}).WorktreeBaseBranch()})
			if err != nil {
				t.Fatal(err)
			}
			if actual := strings.TrimSpace(runGit(t, info.Path, "rev-parse", "HEAD")); actual != observed.BaseCommitSHA {
				t.Fatalf("native workspace selected %s instead of saved %s", actual, observed.BaseCommitSHA)
			}
		})
	}
}

func TestProjectSourceAutomaticEmptyUsesRegisteredDefaultNotCheckout(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "empty-test@example.invalid")
	runGit(t, repo, "config", "user.name", "Empty Test")
	runGit(t, repo, "commit", "--allow-empty", "-m", "Registered empty baseline")
	initial := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "config", "ao.defaultBranch", "main")
	runGit(t, repo, "checkout", "-b", "unrelated")
	runGit(t, repo, "commit", "--allow-empty", "-m", "Unrelated checkout")
	for _, configured := range []string{"", "auto"} {
		observed, err := New().InspectProjectSource(context.Background(), repo, configured)
		if err != nil || !observed.Empty || observed.BaseCommitSHA != initial {
			t.Fatalf("registered empty source: %+v %v", observed, err)
		}
	}
}
