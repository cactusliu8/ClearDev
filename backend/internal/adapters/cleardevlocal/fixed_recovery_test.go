package cleardevlocal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestClearDevFixedRecoveryManagedDirectory(t *testing.T) {
	repo, sha := committedCandidateMaterializationRepo(t, map[string]string{"README.md": "original"})
	root := t.TempDir()
	path := filepath.Join(root, "reviewer")
	runGit(t, repo, "worktree", "add", "-b", "review", path, sha)
	runner := NewWithRoot(root)
	ctx := context.Background()
	if err := runner.ValidateRecoveryWorkspace(ctx, path, "review", sha); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ path, branch, sha string }{{repo, "main", sha}, {path, "another", sha}, {path, "review", "0000000000000000000000000000000000000000"}} {
		if err := runner.ValidateRecoveryWorkspace(ctx, input.path, input.branch, input.sha); err == nil {
			t.Fatal("unsafe worktree binding accepted")
		}
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := runner.ValidateRecoveryWorkspace(ctx, link, "review", sha); err == nil {
		t.Fatal("symlink alias accepted")
	}
	dirty := filepath.Join(path, "keep.txt")
	if err := os.WriteFile(dirty, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.ValidateRecoveryWorkspace(ctx, path, "review", sha); err == nil {
		t.Fatal("dirty directory accepted")
	}
	if content, err := os.ReadFile(dirty); err != nil || string(content) != "keep" {
		t.Fatal("recovery inspection modified dirty content")
	}
}
