package cleardevlocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func sourcePreparationFixture(t *testing.T) (string, string, ports.ClearDevSourcePreparation) {
	t.Helper()
	target, upstream := t.TempDir(), t.TempDir()
	for _, repo := range []string{target, upstream} {
		runGit(t, repo, "init", "-b", "main")
		runGit(t, repo, "config", "user.name", "Test")
		runGit(t, repo, "config", "user.email", "test@example.invalid")
		runGit(t, repo, "commit", "--allow-empty", "-m", repo)
	}
	if err := os.WriteFile(filepath.Join(upstream, "README.md"), []byte("upstream source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, upstream, "add", "README.md")
	runGit(t, upstream, "commit", "-m", "source")
	return target, upstream, ports.ClearDevSourcePreparation{RequestID: "choose-source", Workspace: target, Branch: "refs/heads/main", RepositoryURL: "file://" + upstream, ExpectedBase: strings.TrimSpace(runGit(t, target, "rev-parse", "HEAD")), Current: func(context.Context) error { return nil }}
}

func TestPrepareProjectSourcePreservesHistoryAndReplays(t *testing.T) {
	target, upstream, in := sourcePreparationFixture(t)
	source := strings.TrimSpace(runGit(t, upstream, "rev-parse", "HEAD"))
	result, err := New().PrepareProjectSource(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if result.Empty || result.BaseCommitSHA == in.ExpectedBase {
		t.Fatalf("not prepared: %+v", result)
	}
	if got := strings.TrimSpace(runGit(t, target, "show", "-s", "--format=%P", result.BaseCommitSHA)); got != in.ExpectedBase+" "+source {
		t.Fatalf("history lost: %s", got)
	}
	data, err := os.ReadFile(filepath.Join(target, "README.md"))
	if err != nil || string(data) != "upstream source\n" {
		t.Fatalf("source missing: %s %v", data, err)
	}
	again, err := New().PrepareProjectSource(context.Background(), in)
	if err != nil || again != result {
		t.Fatalf("replay changed source: %+v %v", again, err)
	}
}

func TestPrepareProjectSourceProtectsUserFiles(t *testing.T) {
	for _, name := range []string{"README.md", ".hidden", "unrelated.txt"} {
		t.Run(name, func(t *testing.T) {
			target, _, in := sourcePreparationFixture(t)
			if err := os.WriteFile(filepath.Join(target, name), []byte("user work"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := New().PrepareProjectSource(context.Background(), in); err == nil {
				t.Fatal("accepted user files")
			}
			if data, err := os.ReadFile(filepath.Join(target, name)); err != nil || string(data) != "user work" {
				t.Fatal("user file changed")
			}
			if got := strings.TrimSpace(runGit(t, target, "rev-parse", "HEAD")); got != in.ExpectedBase {
				t.Fatal("moved target")
			}
		})
	}
}

func TestPrepareProjectSourceResumesDownloadWithoutChangingSource(t *testing.T) {
	target, upstream, in := sourcePreparationFixture(t)
	checks := 0
	in.Current = func(context.Context) error {
		checks++
		if checks > 1 {
			return errors.New("cancelled product")
		}
		return nil
	}
	if _, err := New().PrepareProjectSource(context.Background(), in); err == nil {
		t.Fatal("published cancelled preparation")
	}
	if got := strings.TrimSpace(runGit(t, target, "rev-parse", "HEAD")); got != in.ExpectedBase {
		t.Fatal("cancelled preparation changed checkout")
	}
	runGit(t, upstream, "commit", "--allow-empty", "-m", "later upstream")
	in.Current = func(context.Context) error { return nil }
	result, err := New().PrepareProjectSource(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	parents := strings.Fields(runGit(t, target, "show", "-s", "--format=%P", result.BaseCommitSHA))
	if len(parents) != 2 || parents[1] == strings.TrimSpace(runGit(t, upstream, "rev-parse", "HEAD")) {
		t.Fatal("retry fetched a different source")
	}
}

func TestPrepareProjectSourceRejectsUnrelatedExistingCode(t *testing.T) {
	target, _, in := sourcePreparationFixture(t)
	if err := os.WriteFile(filepath.Join(target, "mine.txt"), []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, target, "add", "mine.txt")
	runGit(t, target, "commit", "-m", "mine")
	in.ExpectedBase = strings.TrimSpace(runGit(t, target, "rev-parse", "HEAD"))
	if _, err := New().PrepareProjectSource(context.Background(), in); err == nil {
		t.Fatal("accepted unrelated existing source")
	}
	if data, err := os.ReadFile(filepath.Join(target, "mine.txt")); err != nil || string(data) != "mine" {
		t.Fatal("changed existing code")
	}
}

func TestPrepareProjectSourceConcurrentRequestsImportOnce(t *testing.T) {
	target, _, in := sourcePreparationFixture(t)
	type outcome struct {
		result ports.ClearDevProjectSource
		err    error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, err := New().PrepareProjectSource(context.Background(), in)
			results <- outcome{result, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.result != second.result {
		t.Fatalf("concurrent results: %+v %+v", first, second)
	}
	if got := strings.TrimSpace(runGit(t, target, "rev-list", "--first-parent", "--count", "HEAD")); got != "2" {
		t.Fatalf("import occurred more than once: %s", got)
	}
}

func TestPrepareProjectSourceFetchFailureCanResume(t *testing.T) {
	target, upstream, in := sourcePreparationFixture(t)
	parked := upstream + "-offline"
	if err := os.Rename(upstream, parked); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(parked, upstream) })
	if _, err := New().PrepareProjectSource(context.Background(), in); err == nil || !strings.Contains(err.Error(), "does not appear to be a git repository") {
		t.Fatalf("fetch failure lacked cause: %v", err)
	}
	if got := strings.TrimSpace(runGit(t, target, "rev-parse", "HEAD")); got != in.ExpectedBase {
		t.Fatal("failed fetch changed source")
	}
	if err := os.Rename(parked, upstream); err != nil {
		t.Fatal(err)
	}
	if _, err := New().PrepareProjectSource(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareProjectSourceAllowsForkOriginWithRealAncestry(t *testing.T) {
	target, upstream, in := sourcePreparationFixture(t)
	if _, err := New().PrepareProjectSource(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	runGit(t, target, "remote", "add", "origin", "https://example.org/my-fork.git")
	runGit(t, target, "commit", "--allow-empty", "-m", "local development")
	in.RequestID = "choose-existing-fork"
	in.ExpectedBase = strings.TrimSpace(runGit(t, target, "rev-parse", "HEAD"))
	result, err := New().PrepareProjectSource(context.Background(), in)
	if err != nil || result.BaseCommitSHA != in.ExpectedBase || result.RepositoryURL != "https://example.org/my-fork.git" {
		t.Fatalf("fork was not preserved: %+v %v", result, err)
	}
	if got := strings.TrimSpace(runGit(t, upstream, "rev-parse", "HEAD")); got == in.ExpectedBase {
		t.Fatal("fixture did not diverge")
	}
}

func TestPrepareProjectSourceRechecksFilesAfterDownload(t *testing.T) {
	target, _, in := sourcePreparationFixture(t)
	calls := 0
	in.Current = func(context.Context) error {
		calls++
		if calls == 2 {
			return os.WriteFile(filepath.Join(target, "README.md"), []byte("concurrent user edit"), 0600)
		}
		return nil
	}
	if _, err := New().PrepareProjectSource(context.Background(), in); err == nil {
		t.Fatal("overwrote concurrently created file")
	}
	if data, err := os.ReadFile(filepath.Join(target, "README.md")); err != nil || string(data) != "concurrent user edit" {
		t.Fatal("user edit lost")
	}
}

func TestPrepareProjectSourceRejectsForgedCachedSource(t *testing.T) {
	target, _, in := sourcePreparationFixture(t)
	result, err := New().PrepareProjectSource(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	refs := strings.Fields(runGit(t, target, "for-each-ref", "--format=%(refname)", "refs/cleardev/source/"))
	if len(refs) != 1 {
		t.Fatal("missing exact source ref")
	}
	runGit(t, target, "update-ref", refs[0], in.ExpectedBase)
	if _, err := New().PrepareProjectSource(context.Background(), in); err == nil {
		t.Fatal("accepted tampered source receipt")
	}
	if head := strings.TrimSpace(runGit(t, target, "rev-parse", "HEAD")); head != result.BaseCommitSHA {
		t.Fatal("tampered receipt moved checkout")
	}
}

func TestPrepareProjectSourceRejectsRemoteTrackingTargetBeforeImport(t *testing.T) {
	target, _, in := sourcePreparationFixture(t)
	runGit(t, target, "update-ref", "refs/remotes/origin/main", in.ExpectedBase)
	in.Branch = "main"
	if _, err := New().PrepareProjectSource(context.Background(), in); err == nil || !strings.Contains(err.Error(), "remote tracking ref") {
		t.Fatalf("remote tracking target: %v", err)
	}
	if refs := strings.TrimSpace(runGit(t, target, "for-each-ref", "--format=%(refname)", "refs/cleardev/source/")); refs != "" {
		t.Fatal("download started before target validation")
	}
	if head := strings.TrimSpace(runGit(t, target, "rev-parse", "HEAD")); head != in.ExpectedBase {
		t.Fatal("moved local branch for a remote tracking selection")
	}
}

func TestPrepareProjectSourceDoesNotRunCheckoutHooks(t *testing.T) {
	target, _, in := sourcePreparationFixture(t)
	sentinel := filepath.Join(t.TempDir(), "hook-ran")
	hook := "#!/bin/sh\nprintf ran > '" + sentinel + "'\n"
	if err := os.WriteFile(filepath.Join(target, ".git", "hooks", "post-merge"), []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := New().PrepareProjectSource(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("project checkout hook ran: %v", err)
	}
}
