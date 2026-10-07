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

func TestMailCompositionPreservesReworkHistoryAndBuilderBranch(t *testing.T) {
	ctx := context.Background()
	runner, request := newMailFreezeFixture(t)
	initialBase := request.BaseSHA
	appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "frontend/app.js"), "\n// first implementation must survive rework\n")
	first, err := runner.FreezeMailCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.RunID, request.ParentSHA = "task-one-repair", first.CandidateSHA
	appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "test/app.test.js"), "\n// repair on a different file\n")
	repaired, err := runner.FreezeMailCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.InspectDeliveryBranch(ctx, request.WorkspacePath, request.Branch, repaired.CandidateSHA); err != nil {
		t.Fatalf("standard delivery branch was not bound to its completed SHA: %v", err)
	}
	if err := runner.InspectDeliveryBranch(ctx, request.WorkspacePath, request.Branch, first.CandidateSHA); err == nil {
		t.Fatal("moved standard delivery branch accepted an older candidate")
	}
	compose := ports.ClearDevComposeRequest{RequestID: "full-task-history", RepoPath: request.WorkspacePath, BaseSHA: initialBase, CandidateSHAs: []string{repaired.CandidateSHA}, CompleteTaskDeltas: true}
	final, err := runner.ComposeCandidates(ctx, compose)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"frontend/app.js": "first implementation must survive rework", "test/app.test.js": "repair on a different file"} {
		contents, err := os.ReadFile(filepath.Join(final.WorkspacePath, path))
		if err != nil || !strings.Contains(string(contents), want) {
			t.Fatalf("composition lost reviewed task history in %s: %v", path, err)
		}
	}
	for _, ancestor := range []string{initialBase, first.CandidateSHA, repaired.CandidateSHA} {
		runGit(t, final.WorkspacePath, "merge-base", "--is-ancestor", ancestor, final.OutputSHA)
	}
	branch := core.ComplexCompositionBranch(compose.RequestID)
	if strings.TrimSpace(runGit(t, final.WorkspacePath, "symbolic-ref", "--short", "HEAD")) != branch {
		t.Fatal("composition lacks its exact continuation branch")
	}
	replayed, err := NewWithRoot(runner.managedRoot).ComposeCandidates(ctx, compose)
	if err != nil || !reflect.DeepEqual(replayed, final) {
		t.Fatalf("composition did not replay exact receipt: %+v %v", replayed, err)
	}
	baseRequest := ports.ClearDevMailBuilderBaseRequest{RepoPath: request.RepoPath, WorkspacePath: request.WorkspacePath, Branch: request.Branch, ExpectedHeadSHA: repaired.CandidateSHA, BaseSHA: final.OutputSHA}
	for i := 0; i < 2; i++ {
		if err := runner.PrepareMailBuilderBase(ctx, baseRequest); err != nil {
			t.Fatal(err)
		}
	}
	if strings.TrimSpace(runGit(t, request.WorkspacePath, "symbolic-ref", "--short", "HEAD")) != request.Branch {
		t.Fatal("dependent task base preparation detached the trusted Builder")
	}
	request.RunID, request.BaseSHA, request.ParentSHA = "dependent-task", final.OutputSHA, final.OutputSHA
	appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "frontend/app.js"), "\n// dependent task from trusted composition\n")
	followup, err := runner.FreezeMailCandidate(ctx, request)
	if err != nil || followup.BaseSHA != final.OutputSHA {
		t.Fatalf("dependent trusted freeze failed: %+v %v", followup, err)
	}
	if strings.TrimSpace(runGit(t, request.RepoPath, "rev-parse", "main")) != initialBase {
		t.Fatal("mail composition or base preparation moved main")
	}
	appendFreezeTestFile(t, filepath.Join(final.WorkspacePath, "frontend/app.js"), "\n// retained local edit\n")
	if _, err := runner.ComposeCandidates(ctx, compose); err == nil {
		t.Fatal("dirty composition replay was accepted or silently recreated")
	}
	if !strings.Contains(runGit(t, final.WorkspacePath, "diff"), "retained local edit") {
		t.Fatal("composition replay discarded existing work")
	}
}

func TestMailCompositionConflictPreservesUnmergedWorktreeAcrossReplay(t *testing.T) {
	ctx := context.Background()
	runner, request := newMailFreezeFixture(t)
	file := filepath.Join(request.WorkspacePath, "frontend/app.js")
	appendFreezeTestFile(t, file, "\n// conflicting left append\n")
	left, err := runner.FreezeMailCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	right := request
	right.RunID, right.Branch = "right-task", request.Branch+"-two"
	right.WorkspacePath = filepath.Join(runner.managedRoot, "second-builder")
	runGit(t, request.RepoPath, "worktree", "add", "-b", right.Branch, right.WorkspacePath, right.BaseSHA)
	appendFreezeTestFile(t, filepath.Join(right.WorkspacePath, "frontend/app.js"), "\n// conflicting right append\n")
	rightCandidate, err := runner.FreezeMailCandidate(ctx, right)
	if err != nil {
		t.Fatal(err)
	}
	compose := ports.ClearDevComposeRequest{RequestID: "conflicted-composition", RepoPath: request.WorkspacePath, BaseSHA: request.BaseSHA, CandidateSHAs: []string{left.CandidateSHA, rightCandidate.CandidateSHA}, CompleteTaskDeltas: true}
	result, err := runner.ComposeCandidates(ctx, compose)
	if !errors.Is(err, ports.ErrClearDevCompositionConflict) || len(result.ConflictPaths) != 1 || result.ConflictPaths[0] != "frontend/app.js" {
		t.Fatalf("missing composition conflict: %+v %v", result, err)
	}
	before, err := os.ReadFile(filepath.Join(result.WorkspacePath, "frontend/app.js"))
	if err != nil || !strings.Contains(string(before), "<<<<<<<") {
		t.Fatal("conflict markers were not preserved", err)
	}
	runGit(t, result.WorkspacePath, "rev-parse", "--verify", "MERGE_HEAD")
	replayed, err := NewWithRoot(runner.managedRoot).ComposeCandidates(ctx, compose)
	if !errors.Is(err, ports.ErrClearDevCompositionConflict) || !reflect.DeepEqual(replayed, result) {
		t.Fatal("conflict replay changed original evidence", err)
	}
	after, err := os.ReadFile(filepath.Join(result.WorkspacePath, "frontend/app.js"))
	if err != nil || string(before) != string(after) {
		t.Fatal("conflict replay rewrote the preserved worktree", err)
	}
}

func TestMailCompositionConcurrentReplayIsSingleCandidate(t *testing.T) {
	ctx := context.Background()
	runner, request := newMailFreezeFixture(t)
	appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "frontend/app.js"), "\n// concurrent composition input\n")
	candidate, err := runner.FreezeMailCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	compose := ports.ClearDevComposeRequest{RequestID: "concurrent-mail-composition", RepoPath: request.WorkspacePath, BaseSHA: request.BaseSHA, CandidateSHAs: []string{candidate.CandidateSHA}, CompleteTaskDeltas: true}
	start := make(chan struct{})
	var wait sync.WaitGroup
	results := make([]ports.ClearDevComposeResult, 2)
	errorsFound := make([]error, 2)
	for i := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errorsFound[index] = NewWithRoot(runner.managedRoot).ComposeCandidates(ctx, compose)
		}(i)
	}
	close(start)
	wait.Wait()
	if errorsFound[0] != nil || errorsFound[1] != nil || !reflect.DeepEqual(results[0], results[1]) {
		t.Fatalf("concurrent composition did not settle once: %+v errors=%v", results, errorsFound)
	}
	if strings.TrimSpace(runGit(t, results[0].WorkspacePath, "rev-list", "--count", compose.BaseSHA+"..HEAD")) != "2" {
		t.Fatal("concurrent replay duplicated a merge or lost the input history")
	}
}

func TestMailBuilderBaseRejectsDirtyAndNonDescendantWithoutChangingBranch(t *testing.T) {
	ctx := context.Background()
	runner, request := newMailFreezeFixture(t)
	appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "frontend/app.js"), "\n// candidate\n")
	candidate, err := runner.FreezeMailCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	base := ports.ClearDevMailBuilderBaseRequest{RepoPath: request.RepoPath, WorkspacePath: request.WorkspacePath, Branch: request.Branch, ExpectedHeadSHA: candidate.CandidateSHA, BaseSHA: request.BaseSHA}
	if err := runner.PrepareMailBuilderBase(ctx, base); !errors.Is(err, ports.ErrClearDevCandidateNotDescendant) {
		t.Fatal("base preparation rewound a reviewed candidate", err)
	}
	base.BaseSHA = candidate.CandidateSHA
	appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "frontend/app.js"), "\n// unfinished local work\n")
	if err := runner.PrepareMailBuilderBase(ctx, base); !errors.Is(err, ports.ErrClearDevWorkspaceDirty) {
		t.Fatal("dirty Builder was rebased", err)
	}
	if strings.TrimSpace(runGit(t, request.WorkspacePath, "rev-parse", "HEAD")) != candidate.CandidateSHA || !strings.Contains(runGit(t, request.WorkspacePath, "diff"), "unfinished local work") {
		t.Fatal("rejected base preparation changed the candidate or local work")
	}
}
