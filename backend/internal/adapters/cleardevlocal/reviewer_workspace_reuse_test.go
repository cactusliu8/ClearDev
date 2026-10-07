package cleardevlocal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewerWorkspaceReuseMovesOnlyKnownCleanCandidate(t *testing.T) {
	for _, mode := range []string{"clean", "dirty", "unexpected-head"} {
		t.Run(mode, func(t *testing.T) {
			repo := baselineGateRepo(t, false)
			old := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			workspace := filepath.Join(t.TempDir(), "reviewer")
			runGit(t, repo, "worktree", "add", "--detach", workspace, old)
			if err := os.WriteFile(filepath.Join(repo, "frontend", "increment.css"), []byte("/* new candidate */\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			candidate := mailCommit(t, repo)
			expected := old
			if mode == "dirty" {
				if err := os.WriteFile(filepath.Join(workspace, "keep-user-work"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unexpected-head" {
				expected = candidate
			}
			err := New().PrepareBaseWorkspace(context.Background(), workspace, expected, candidate)
			if mode == "clean" {
				if err != nil {
					t.Fatal(err)
				}
				observed, err := New().InspectCandidate(context.Background(), workspace, candidate)
				if err != nil || observed.CandidateSHA != candidate || len(observed.Paths) != 0 {
					t.Fatalf("new review tree=%+v err=%v", observed, err)
				}
			} else {
				if err == nil || strings.TrimSpace(runGit(t, workspace, "rev-parse", "HEAD")) != old {
					t.Fatal("unexpected or dirty reviewer tree was moved")
				}
			}
			if mode == "dirty" {
				data, err := os.ReadFile(filepath.Join(workspace, "keep-user-work"))
				if err != nil || string(data) != "keep" {
					t.Fatal("user work was overwritten")
				}
			}
			if strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")) != candidate || strings.TrimSpace(runGit(t, repo, "status", "--porcelain")) != "" {
				t.Fatal("Builder repository changed")
			}
		})
	}
}
