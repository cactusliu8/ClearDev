package cleardevlocal

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestSelectedMailBaselineUsesDeliveredBranchWithoutMovingMain(t *testing.T) {
	runner, request := newMailFreezeFixture(t)
	appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "frontend/app.js"), "\n// first delivered feature retained by the next candidate\n")
	appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "test/app.test.js"), "\ntest('first delivered regression',()=>assert.ok(true));\n")
	first, err := runner.FreezeMailCandidate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := runner.IdentifyMailProjectAtBranch(context.Background(), request.RepoPath, request.Branch)
	if err != nil || identity.CandidateSHA != first.CandidateSHA {
		t.Fatalf("selected=%+v %v", identity, err)
	}
	original, err := runner.IdentifyMailProject(context.Background(), request.RepoPath)
	if err != nil || original.CandidateSHA != request.BaseSHA {
		t.Fatal("main moved while selecting delivered branch")
	}
	second := request
	second.RunID = "second-requirement"
	second.Branch += "-next"
	second.WorkspacePath = filepath.Join(runner.managedRoot, "next-builder")
	second.BaseSHA, second.ParentSHA = first.CandidateSHA, first.CandidateSHA
	runGit(t, request.RepoPath, "worktree", "add", "-b", second.Branch, second.WorkspacePath, first.CandidateSHA)
	appendFreezeTestFile(t, filepath.Join(second.WorkspacePath, "frontend/app.js"), "\n// second increment\n")
	frozen, err := runner.FreezeMailCandidate(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if parent := strings.TrimSpace(runGit(t, request.RepoPath, "rev-parse", frozen.CandidateSHA+"^")); parent != first.CandidateSHA {
		t.Fatal("next requirement lost the delivered parent")
	}
	proof, err := runner.CheckMailCandidateScope(context.Background(), ports.ClearDevDeliveryRequest{RunID: "second-scope", WorkspacePath: second.WorkspacePath, BaseSHA: first.CandidateSHA, CandidateSHA: frozen.CandidateSHA})
	if err != nil || proof.BaseSHA != first.CandidateSHA {
		t.Fatal("next scope is not relative to the last delivery", err)
	}
	// A test introduced in the first delivery is now protected as an OLD test.
	third := second
	third.RunID = "third-invalid"
	third.ParentSHA = frozen.CandidateSHA
	writeTestFile(t, filepath.Join(third.WorkspacePath, "test/app.test.js"), "// deleted first-delivery assertions\n")
	if _, err := runner.FreezeMailCandidate(context.Background(), third); err == nil {
		t.Fatal("next increment could delete prior delivery tests")
	}
	if head := strings.TrimSpace(runGit(t, request.RepoPath, "rev-parse", "HEAD")); head != request.BaseSHA {
		t.Fatal("registered root changed")
	}
}

func TestSelectedMailBaselineRejectsRevisionExpressionsAndUnknownBranches(t *testing.T) {
	for _, branch := range []string{"missing", "HEAD~1", "--all", "../outside"} {
		t.Run(branch, func(t *testing.T) {
			runner, request := newMailFreezeFixture(t)
			if _, err := runner.IdentifyMailProjectAtBranch(context.Background(), request.RepoPath, branch); err == nil {
				t.Fatal("invalid selected branch accepted")
			}
		})
	}
}

func TestSelectedMailBaselineActuallyTestsSelectedSHA(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy-delivery", true: "bad-delivery"}[fails], func(t *testing.T) {
			runner, request := newMailFreezeFixture(t)
			assertion := "true"
			if fails {
				assertion = "false"
			}
			appendFreezeTestFile(t, filepath.Join(request.WorkspacePath, "test/app.test.js"), "\ntest('selected delivered regression',()=>assert.ok("+assertion+"));\n")
			first, err := runner.FreezeMailCandidate(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := runner.CheckDevelopmentBaseline(context.Background(), ports.ClearDevBaselineRequest{RunID: "selected-baseline-check", WorkspacePath: request.RepoPath, BaseBranch: request.Branch})
			if baseline.CandidateSHA != first.CandidateSHA {
				t.Fatal("checker used main instead of selected SHA")
			}
			if fails {
				if err == nil || baseline.ReasonCode != "BASELINE_TEST_FAILED" || baseline.Health.Outcome != "" {
					t.Fatalf("bad selected baseline reached health: %+v %v", baseline, err)
				}
			} else {
				if err != nil || baseline.Tests.Outcome != ports.ClearDevCheckPass || baseline.Health.Outcome != ports.ClearDevCheckPass {
					t.Fatalf("healthy selected baseline: %+v %v", baseline, err)
				}
			}
			if head := strings.TrimSpace(runGit(t, request.RepoPath, "rev-parse", "HEAD")); head != request.BaseSHA {
				t.Fatal("baseline test moved main")
			}
		})
	}
}
