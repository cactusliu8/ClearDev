package project_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/service/project"
)

func TestMailProductInitializationFreezesCommitIdentity(t *testing.T) {
	manager := newManager(t)
	var first string
	for _, at := range []string{"2026-01-01T00:00:00Z", "2026-09-20T23:00:00Z"} {
		t.Setenv("GIT_AUTHOR_DATE", at)
		t.Setenv("GIT_COMMITTER_DATE", at)
		dir := isolatedPlainFolder(t)
		if _, err := manager.InitializeRepository(context.Background(), project.InitializeRepositoryInput{Path: dir, Template: "complex-mail-app"}); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		sha := strings.TrimSpace(string(out))
		if first != "" && first != sha {
			t.Fatalf("fixed product changed baseline identity with wall clock: %s != %s", first, sha)
		}
		first = sha
		out, err = exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=all").CombinedOutput()
		if err != nil || len(out) != 0 {
			t.Fatalf("baseline is not clean: %v %s", err, out)
		}
	}
}
