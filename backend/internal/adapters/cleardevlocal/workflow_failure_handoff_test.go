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

func TestUnifiedFailureProjectFreezeLeavesUntrackedDependenciesOutsideCandidate(t *testing.T) {
	r, req := newProjectFreezeFixture(t, false)
	writeFailureFixture(t, filepath.Join(req.WorkspacePath, "app/main.cjs"), "module.exports = 42\n")
	writeFailureFixture(t, filepath.Join(req.WorkspacePath, "node_modules/vite/bin/vite.js"), "throw new Error('never run me')\n")
	if err := os.MkdirAll(filepath.Join(req.WorkspacePath, "node_modules/.bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../vite/bin/vite.js", filepath.Join(req.WorkspacePath, "node_modules/.bin/vite")); err != nil {
		t.Fatal(err)
	}
	frozen, err := r.FreezeMailCandidate(context.Background(), req)
	if err != nil {
		t.Fatalf("non-delivery npm entry prevented candidate handoff: %v", err)
	}
	if observed, err := r.InspectProjectCandidate(context.Background(), req.WorkspacePath, req.BaseSHA); err != nil || observed.CandidateSHA != frozen.CandidateSHA {
		t.Fatal("project post-freeze inspection lost candidate", err)
	}
	if _, err := r.InspectCandidate(context.Background(), req.WorkspacePath, req.BaseSHA); !errors.Is(err, ports.ErrClearDevWorkspaceDirty) {
		t.Fatal("ordinary repository inspection was weakened", err)
	}
	if err := r.PrepareMailBuilderBase(context.Background(), ports.ClearDevMailBuilderBaseRequest{RepoPath: req.RepoPath, WorkspacePath: req.WorkspacePath, Branch: req.Branch, ExpectedHeadSHA: frozen.CandidateSHA, BaseSHA: frozen.CandidateSHA, ProjectExecution: req.ProjectExecution}); err != nil {
		t.Fatal("next project task cannot reuse its trusted base", err)
	}
	for _, p := range frozen.Paths {
		if strings.HasPrefix(p.Path, "node_modules/") {
			t.Fatal("dependency entered the candidate")
		}
	}
	if _, err := os.Lstat(filepath.Join(req.WorkspacePath, "node_modules/.bin/vite")); err != nil {
		t.Fatal("dependency was deleted", err)
	}
	if head := strings.TrimSpace(runGit(t, req.RepoPath, "rev-parse", "HEAD")); head != req.BaseSHA {
		t.Fatal("main moved")
	}
	if _, err := r.FreezeMailCandidate(context.Background(), req); err != nil {
		t.Fatal("replay must use the same source policy", err)
	}
}

func TestUnifiedFailureEmptyHandoffProvidesTypedUnpublishedEvidence(t *testing.T) {
	r, req := newProjectFreezeFixture(t, false)
	_, err := r.FreezeMailCandidate(context.Background(), req)
	var detail *ports.ClearDevCandidateHandoffError
	if !errors.As(err, &detail) || detail.ProblemCode != "NO_IMPLEMENTATION_CHANGE" || !detail.BeforePublication || !errors.Is(err, ports.ErrClearDevCandidateInvalid) {
		t.Fatalf("empty handoff cannot reach the original Builder with exact evidence: %v", err)
	}
}

func TestUnifiedFailureSourceSymlinkAndOutOfScopeRemainHumanBoundaries(t *testing.T) {
	for _, mode := range []string{"source-link", "out-of-scope", "tracked-dependency", "dependency-root-link"} {
		t.Run(mode, func(t *testing.T) {
			r, req := newProjectFreezeFixture(t, false)
			writeFailureFixture(t, filepath.Join(req.WorkspacePath, "app/main.cjs"), "module.exports = 42\n")
			switch mode {
			case "dependency-root-link":
				if err := os.Symlink(t.TempDir(), filepath.Join(req.WorkspacePath, "node_modules")); err != nil {
					t.Fatal(err)
				}
			case "source-link":
				if err := os.Symlink("../package.json", filepath.Join(req.WorkspacePath, "app/link.cjs")); err != nil {
					t.Fatal(err)
				}
			case "out-of-scope":
				writeFailureFixture(t, filepath.Join(req.WorkspacePath, "unauthorized.cjs"), "do not delete me")
			case "tracked-dependency":
				writeFailureFixture(t, filepath.Join(req.WorkspacePath, "node_modules/tracked.cjs"), "not an installed-only file")
				runGit(t, req.WorkspacePath, "add", "node_modules/tracked.cjs")
			}
			_, err := r.FreezeMailCandidate(context.Background(), req)
			if err == nil {
				t.Fatal("unsafe or tracked source was silently excluded")
			}
			if head := strings.TrimSpace(runGit(t, req.WorkspacePath, "rev-parse", "HEAD")); head != req.BaseSHA {
				t.Fatal("failed freeze published source")
			}
		})
	}
}

func writeFailureFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, content)
}
