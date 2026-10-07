package cleardevlocal

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestTrustedMailFreezeConcurrentForeignBindingCannotPublish(t *testing.T) {
	runner, first := newMailFreezeFixture(t)
	second := first
	second.WorkspacePath = filepath.Join(runner.managedRoot, "other-builder")
	second.Branch += "-other"
	runGit(t, first.RepoPath, "worktree", "add", "-b", second.Branch, second.WorkspacePath, first.BaseSHA)
	for _, request := range []ports.ClearDevMailFreezeRequest{first, second} {
		appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "frontend/app.js"), "\n// exact dispatch binding fixture\n")
	}
	var group sync.WaitGroup
	errs := make([]error, 2)
	requests := []ports.ClearDevMailFreezeRequest{first, second}
	for i, request := range requests {
		group.Add(1)
		go func(i int, request ports.ClearDevMailFreezeRequest) {
			defer group.Done()
			_, errs[i] = runner.FreezeMailCandidate(context.Background(), request)
		}(i, request)
	}
	group.Wait()
	succeeded := 0
	for i, err := range errs {
		head := strings.TrimSpace(runGit(t, first.RepoPath, "rev-parse", requests[i].Branch))
		if err == nil {
			succeeded++
			if head == first.BaseSHA {
				t.Fatal("successful freeze did not publish")
			}
		} else if head != first.BaseSHA {
			t.Fatal("foreign dispatch binding published a second candidate")
		}
	}
	if succeeded != 1 {
		t.Fatalf("same dispatch authorized %d distinct workspaces", succeeded)
	}
}
