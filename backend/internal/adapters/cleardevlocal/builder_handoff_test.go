package cleardevlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func handoffFixture(t *testing.T) (*Runner, ports.ClearDevBuilderHandoffSnapshotRequest) {
	t.Helper()
	r, f := newProjectFreezeFixture(t, true)
	return r, ports.ClearDevBuilderHandoffSnapshotRequest{SnapshotKey: "explicit-handoff-fixture", RepoPath: f.RepoPath, WorkspacePath: f.WorkspacePath, Branch: f.Branch, BaseSHA: f.BaseSHA, WritePaths: f.WritePaths, ForbiddenPaths: f.ForbiddenPaths, ProjectExecution: f.ProjectExecution, HandoffText: "Original task and budget remain unchanged. This is explicit test material."}
}
func handoffTarget(t *testing.T, r *Runner, req ports.ClearDevBuilderHandoffSnapshotRequest) ports.ClearDevBuilderHandoffTarget {
	t.Helper()
	target := ports.ClearDevBuilderHandoffTarget{RepoPath: req.RepoPath, WorkspacePath: filepath.Join(r.managedRoot, "replacement"), Branch: "cleardev-complex-builder-handoff-test", BaseSHA: req.BaseSHA}
	runGit(t, req.RepoPath, "worktree", "add", "-b", target.Branch, target.WorkspacePath, target.BaseSHA)
	return target
}

func TestBuilderHandoffPreservesHeadIndexDirtyDeletesAndUntracked(t *testing.T) {
	r, req := handoffFixture(t)
	file := filepath.Join(req.WorkspacePath, "app/store.cjs")
	writeTestFile(t, file, "committed bytes\n")
	runGit(t, req.WorkspacePath, "add", "app/store.cjs")
	runGit(t, req.WorkspacePath, "-c", "commit.gpgsign=false", "commit", "-qm", "prior Builder work")
	writeTestFile(t, file, "staged bytes\n")
	runGit(t, req.WorkspacePath, "add", "app/store.cjs")
	writeTestFile(t, file, "unstaged bytes\n")
	if err := os.Remove(filepath.Join(req.WorkspacePath, "config/application.json")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(req.WorkspacePath, "app/new.cjs"), "untracked executable\n")
	if err := os.Chmod(filepath.Join(req.WorkspacePath, "app/new.cjs"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := runGit(t, req.WorkspacePath, "status", "--porcelain=v1")
	head := strings.TrimSpace(runGit(t, req.WorkspacePath, "rev-parse", "HEAD"))
	snapshot, err := r.CaptureBuilderHandoff(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HeadSHA != head || snapshot.IndexSHA256 == "" || snapshot.ObjectCount < 5 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if got := runGit(t, req.WorkspacePath, "status", "--porcelain=v1"); got != before {
		t.Fatal("capture changed source", got, before)
	}
	replay, err := r.CaptureBuilderHandoff(context.Background(), req)
	if err != nil || !reflect.DeepEqual(replay, snapshot) {
		t.Fatal("capture did not reuse exact sealed snapshot", err)
	}
	// Import only the seal into a repository with no source refs or objects.
	// HEAD/base trees and every staged blob must remain independently readable.
	independent := t.TempDir()
	runGit(t, independent, "init", "-q")
	material, err := os.ReadFile(snapshot.ObjectsPath)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := parseBuilderHandoffObjects(material)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		command := exec.Command("git", "-C", independent, "-c", "core.hooksPath=/dev/null", "hash-object", "-t", object.kind, "-w", "--stdin")
		command.Stdin = bytes.NewReader(object.data)
		out, err := command.Output()
		if err != nil || strings.TrimSpace(string(out)) != object.oid {
			t.Fatal("sealed object depends on original repository", object.oid, err)
		}
	}
	runGit(t, independent, "diff", "--raw", snapshot.BaseSHA, snapshot.HeadSHA)
	runGit(t, independent, "cat-file", "blob", strings.TrimSpace(runGit(t, req.WorkspacePath, "rev-parse", ":app/store.cjs")))
	target := handoffTarget(t, r, req)
	marker := filepath.Join(t.TempDir(), "hook-was-run")
	hook := filepath.Join(req.RepoPath, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restore executed a checkout hook")
	}
	if got := strings.TrimSpace(runGit(t, target.WorkspacePath, "rev-parse", "HEAD")); got != head {
		t.Fatal("HEAD not restored", got)
	}
	if got := runGit(t, target.WorkspacePath, "status", "--porcelain=v1"); got != before {
		t.Fatal("index/dirty/delete/untracked mismatch", got, before)
	}
	if data, err := os.ReadFile(filepath.Join(target.WorkspacePath, "app/store.cjs")); err != nil || string(data) != "unstaged bytes\n" {
		t.Fatal("working bytes not retained", err)
	}
	if data := runGit(t, target.WorkspacePath, "show", ":app/store.cjs"); data != "staged bytes\n" {
		t.Fatal("staged bytes not retained", data)
	}
	if err := r.VerifyBuilderHandoffTarget(context.Background(), snapshot, target); err != nil {
		t.Fatal(err)
	}
	if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); err != nil {
		t.Fatal("exact copy not idempotent", err)
	}
	writeTestFile(t, filepath.Join(target.WorkspacePath, "app/store.cjs"), "new worker progress\n")
	if err := r.VerifyBuilderHandoffTarget(context.Background(), snapshot, target); err == nil {
		t.Fatal("new worker changes mistaken for initial copy")
	}
	if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); err == nil {
		t.Fatal("restore overwrote later worker progress")
	}
	if got := runGit(t, req.WorkspacePath, "status", "--porcelain=v1"); got != before {
		t.Fatal("restore changed source")
	}
}

func TestBuilderHandoffLaterDispatchRestoresIntoInitialAdmissionBase(t *testing.T) {
	r, req := handoffFixture(t)
	initialBase := req.BaseSHA
	writeTestFile(t, filepath.Join(req.WorkspacePath, "app/store.cjs"), "earlier accepted task\n")
	runGit(t, req.WorkspacePath, "add", "app/store.cjs")
	runGit(t, req.WorkspacePath, "-c", "commit.gpgsign=false", "commit", "-qm", "earlier accepted task")
	req.BaseSHA = strings.TrimSpace(runGit(t, req.WorkspacePath, "rev-parse", "HEAD"))
	writeTestFile(t, filepath.Join(req.WorkspacePath, "app/later.cjs"), "later committed work\n")
	runGit(t, req.WorkspacePath, "add", "app/later.cjs")
	runGit(t, req.WorkspacePath, "-c", "commit.gpgsign=false", "commit", "-qm", "current task committed work")
	writeTestFile(t, filepath.Join(req.WorkspacePath, "app/later.cjs"), "later dirty work\n")
	snapshot, err := r.CaptureBuilderHandoff(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	material, err := os.ReadFile(snapshot.ObjectsPath)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := parseBuilderHandoffObjects(material)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if object.oid == initialBase {
			t.Fatal("seal exported history older than the dispatch base")
		}
	}
	target := ports.ClearDevBuilderHandoffTarget{RepoPath: req.RepoPath, WorkspacePath: filepath.Join(r.managedRoot, "later-replacement"), Branch: "cleardev-complex-builder-later-handoff", BaseSHA: req.BaseSHA, InitialBaseSHA: initialBase}
	runGit(t, req.RepoPath, "worktree", "add", "-b", target.Branch, target.WorkspacePath, initialBase)
	if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); err != nil {
		t.Fatal("later dispatch required a different initial admission base", err)
	}
	if target.InitialBaseSHA != initialBase || target.BaseSHA != req.BaseSHA || snapshot.BaseSHA != req.BaseSHA {
		t.Fatal("dispatch/admission bases were conflated")
	}
	if got := runGit(t, target.WorkspacePath, "status", "--porcelain=v1"); got != runGit(t, req.WorkspacePath, "status", "--porcelain=v1") {
		t.Fatal("later working state differs", got)
	}
	if err := r.VerifyBuilderHandoffTarget(context.Background(), snapshot, target); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(target.WorkspacePath, "app/later.cjs"), "new worker progress\n")
	if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); !errors.Is(err, ports.ErrBuilderHandoffBeforeCopy) {
		t.Fatal("pre-copy refusal was not classified", err)
	}
	if data, err := os.ReadFile(filepath.Join(target.WorkspacePath, "app/later.cjs")); err != nil || string(data) != "new worker progress\n" {
		t.Fatal("later progress was overwritten", err)
	}
}

func TestBuilderHandoffPartialCopyErrorCannotBeClassifiedBeforeCopy(t *testing.T) {
	r, req := handoffFixture(t)
	writeTestFile(t, filepath.Join(req.WorkspacePath, "app/store.cjs"), "committed source progress\n")
	runGit(t, req.WorkspacePath, "add", "app/store.cjs")
	runGit(t, req.WorkspacePath, "-c", "commit.gpgsign=false", "commit", "-qm", "source progress")
	snapshot, err := r.CaptureBuilderHandoff(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	target := handoffTarget(t, r, req)
	gitDir := strings.TrimSpace(runGit(t, target.WorkspacePath, "rev-parse", "--absolute-git-dir"))
	indexLock := filepath.Join(gitDir, "index.lock")
	if err := os.WriteFile(indexLock, []byte("occupied index"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = r.RestoreBuilderHandoff(context.Background(), snapshot, target)
	if err == nil || errors.Is(err, ports.ErrBuilderHandoffBeforeCopy) {
		t.Fatal("error after HEAD mutation was made replayable", err)
	}
	if got := strings.TrimSpace(runGit(t, target.WorkspacePath, "rev-parse", "HEAD")); got != snapshot.HeadSHA {
		t.Fatal("fixture did not exercise a partial copy", got)
	}
	if err := os.Remove(indexLock); err != nil {
		t.Fatal(err)
	}
	if err := r.VerifyBuilderHandoffTarget(context.Background(), snapshot, target); err == nil {
		t.Fatal("partial copy was inferred complete")
	}
}

func TestBuilderHandoffCandidatePreservesApprovedStagedIndex(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed-%v", committed), func(t *testing.T) {
			r, req := handoffFixture(t)
			file := filepath.Join(req.WorkspacePath, "app/store.cjs")
			if committed {
				writeTestFile(t, file, "approved committed work\n")
				runGit(t, req.WorkspacePath, "add", "app/store.cjs")
				runGit(t, req.WorkspacePath, "-c", "commit.gpgsign=false", "commit", "-qm", "approved old committed work")
			}
			writeTestFile(t, file, "approved staged work\n")
			runGit(t, req.WorkspacePath, "add", "app/store.cjs")
			writeTestFile(t, file, "approved dirty work\n")
			snapshot, err := r.CaptureBuilderHandoff(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			target := handoffTarget(t, r, req)
			if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(target.WorkspacePath, "app/store.cjs"), "new Builder completed task\n")
			freeze := ports.ClearDevMailFreezeRequest{RunID: "handoff-candidate-staged", RepoPath: target.RepoPath, WorkspacePath: target.WorkspacePath, Branch: target.Branch, BaseSHA: req.BaseSHA, ParentSHA: snapshot.HeadSHA, WritePaths: req.WritePaths, ForbiddenPaths: req.ForbiddenPaths, ProjectExecution: req.ProjectExecution}
			if _, err := r.FreezeMailCandidate(context.Background(), freeze); !errors.Is(err, ports.ErrClearDevCandidateInvalid) {
				t.Fatal("ordinary freeze admitted unproved staging", err)
			}
			freeze.BuilderHandoffSnapshot = &snapshot
			frozen, err := r.FreezeMailCandidate(context.Background(), freeze)
			if err != nil {
				t.Fatal("candidate path rejects the precisely retained approved index", err)
			}
			if frozen.BaseSHA != req.BaseSHA || strings.TrimSpace(runGit(t, target.WorkspacePath, "rev-parse", frozen.CandidateSHA+"^")) != snapshot.HeadSHA {
				t.Fatal("freeze changed the original base or approved HEAD parent", frozen)
			}
			if staged := runGit(t, target.WorkspacePath, "show", ":app/store.cjs"); staged != "new Builder completed task\n" {
				t.Fatal("trusted publication did not produce its new index", staged)
			}
			if replayed, err := r.FreezeMailCandidate(context.Background(), freeze); err != nil || !reflect.DeepEqual(replayed, frozen) {
				t.Fatal("exact handoff candidate replay changed", err)
			}
		})
	}
}

func TestBuilderHandoffCandidateProofCannotAdmitNewStagingOrOtherBinding(t *testing.T) {
	for _, mode := range []string{"new-staging", "old-identity", "wrong-parent", "wrong-base", "wrong-branch", "no-project", "wrong-seal", "scope", "scope-index-only", "changed-before-index-publication"} {
		t.Run(mode, func(t *testing.T) {
			r, req := handoffFixture(t)
			writeTestFile(t, filepath.Join(req.WorkspacePath, "app/store.cjs"), "approved staged work\n")
			runGit(t, req.WorkspacePath, "add", "app/store.cjs")
			writeTestFile(t, filepath.Join(req.WorkspacePath, "app/store.cjs"), "approved dirty work\n")
			snapshot, err := r.CaptureBuilderHandoff(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			target := handoffTarget(t, r, req)
			if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(target.WorkspacePath, "app/store.cjs"), "new Builder completed task\n")
			freeze := ports.ClearDevMailFreezeRequest{RunID: "handoff-candidate-refusal", RepoPath: target.RepoPath, WorkspacePath: target.WorkspacePath, Branch: target.Branch, BaseSHA: req.BaseSHA, ParentSHA: snapshot.HeadSHA, WritePaths: req.WritePaths, ForbiddenPaths: req.ForbiddenPaths, ProjectExecution: req.ProjectExecution, BuilderHandoffSnapshot: &snapshot}
			switch mode {
			case "new-staging":
				runGit(t, target.WorkspacePath, "add", "app/store.cjs")
			case "old-identity":
				freeze.WorkspacePath, freeze.Branch = req.WorkspacePath, req.Branch
			case "wrong-parent":
				snapshot.HeadSHA = strings.Repeat("a", 40)
			case "wrong-base":
				snapshot.BaseSHA = strings.Repeat("a", 40)
			case "wrong-branch":
				freeze.Branch = "cleardev-complex-builder-other"
			case "no-project":
				freeze.ProjectExecution = nil
			case "wrong-seal":
				snapshot.IndexSHA256 = strings.Repeat("a", 64)
			case "scope":
				freeze.ForbiddenPaths = append(append([]string(nil), freeze.ForbiddenPaths...), "app/**")
			case "scope-index-only":
				freeze.ForbiddenPaths = append(append([]string(nil), freeze.ForbiddenPaths...), "app/**")
				original, err := os.ReadFile(filepath.Join(req.RepoPath, "app/store.cjs"))
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(target.WorkspacePath, "app/store.cjs"), string(original))
				writeTestFile(t, filepath.Join(target.WorkspacePath, "checks/current.test.cjs"), "allowed working change\n")
			}
			checkpoint := func(stage string) error {
				if mode == "changed-before-index-publication" && stage == "prepared" {
					runGit(t, target.WorkspacePath, "add", "app/store.cjs")
				}
				return nil
			}
			if _, err := r.freezeMailCandidate(context.Background(), freeze, checkpoint); err == nil {
				t.Fatal("handoff proof bypassed ordinary freeze binding", mode)
			}
			if head := strings.TrimSpace(runGit(t, target.WorkspacePath, "rev-parse", "HEAD")); head != snapshot.HeadSHA && mode != "wrong-parent" {
				t.Fatal("rejected proof moved the target HEAD", head)
			}
		})
	}
}

func TestBuilderHandoffCandidateReplaysExactPublicationCrashBoundaries(t *testing.T) {
	for _, boundary := range []string{"prepared", "ref-published", "index-published"} {
		t.Run(boundary, func(t *testing.T) {
			r, req := handoffFixture(t)
			writeTestFile(t, filepath.Join(req.WorkspacePath, "app/store.cjs"), "approved staged work\n")
			runGit(t, req.WorkspacePath, "add", "app/store.cjs")
			writeTestFile(t, filepath.Join(req.WorkspacePath, "app/store.cjs"), "approved dirty work\n")
			snapshot, err := r.CaptureBuilderHandoff(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			target := handoffTarget(t, r, req)
			if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(target.WorkspacePath, "app/store.cjs"), "new Builder completed task\n")
			freeze := ports.ClearDevMailFreezeRequest{RunID: "handoff-candidate-crash", RepoPath: target.RepoPath, WorkspacePath: target.WorkspacePath, Branch: target.Branch, BaseSHA: req.BaseSHA, ParentSHA: snapshot.HeadSHA, WritePaths: req.WritePaths, ForbiddenPaths: req.ForbiddenPaths, ProjectExecution: req.ProjectExecution, BuilderHandoffSnapshot: &snapshot}
			crash := errors.New("simulated publication crash")
			if _, err := r.freezeMailCandidate(context.Background(), freeze, func(stage string) error {
				if stage == boundary {
					return crash
				}
				return nil
			}); !errors.Is(err, crash) {
				t.Fatal("publication boundary not exercised", err)
			}
			frozen, err := NewWithRoot(r.managedRoot).FreezeMailCandidate(context.Background(), freeze)
			if err != nil {
				t.Fatal("exact publication receipt could not be resumed", err)
			}
			if replay, err := NewWithRoot(r.managedRoot).FreezeMailCandidate(context.Background(), freeze); err != nil || !reflect.DeepEqual(replay, frozen) {
				t.Fatal("replay created a different candidate", err)
			}
			if got := runGit(t, target.WorkspacePath, "status", "--porcelain=v1"); got != "" {
				t.Fatal("trusted candidate left a divergent index or working tree", got)
			}
		})
	}
}

func TestBuilderHandoffRejectsBoundedHistoryObjectCount(t *testing.T) {
	r, req := handoffFixture(t)
	var stream strings.Builder
	for n := 0; n < 5500; n++ {
		fmt.Fprintf(&stream, "commit refs/heads/%s\nmark :%d\ncommitter Test <test@example.com> %d +0000\ndata %d\ncommit-%d\n", req.Branch, n+1, 946684800+n, len(fmt.Sprintf("commit-%d", n)), n)
		if n == 0 {
			fmt.Fprintf(&stream, "from %s\n", req.BaseSHA)
		}
		data := fmt.Sprintf("history-%d\n", n)
		fmt.Fprintf(&stream, "M 100644 inline app/store.cjs\ndata %d\n%s\n", len(data), data)
	}
	command := exec.Command("git", "-C", req.RepoPath, "-c", "core.hooksPath=/dev/null", "fast-import", "--quiet")
	command.Stdin = strings.NewReader(stream.String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create bounded local history: %v %s", err, output)
	}
	if _, err := r.CaptureBuilderHandoff(context.Background(), req); !errors.Is(err, ports.ErrBuilderHandoffLimit) {
		t.Fatal("oversized necessary history object count was admitted", err)
	}
	dir, err := builderHandoffDirectory(req.SnapshotKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversized history was published")
	}
}

func TestBuilderHandoffRetainsNecessaryHistoryWithinSeparateObjectByteBound(t *testing.T) {
	r, req := handoffFixture(t)
	writeTestFile(t, filepath.Join(req.WorkspacePath, "app/prior.cjs"), strings.Repeat("x", 17<<20))
	runGit(t, req.WorkspacePath, "add", "app/prior.cjs")
	runGit(t, req.WorkspacePath, "-c", "commit.gpgsign=false", "commit", "-qm", "prior regular Git blob")
	runGit(t, req.WorkspacePath, "rm", "app/prior.cjs")
	runGit(t, req.WorkspacePath, "-c", "commit.gpgsign=false", "commit", "-qm", "remove prior blob from current files")
	snapshot, err := r.CaptureBuilderHandoff(context.Background(), req)
	if err != nil {
		t.Fatal("Git history below its 64MiB bound was subjected to working-file 16MiB bound", err)
	}
	if snapshot.ContentBytes >= mailFreezeMaxBytes || snapshot.ObjectBytes <= mailFreezeMaxBytes || snapshot.ObjectBytes > builderHandoffMaxObjectBytes {
		t.Fatal("separate content/object bounds not retained", snapshot)
	}
	target := handoffTarget(t, r, req)
	if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); err != nil {
		t.Fatal(err)
	}
	if err := r.VerifyBuilderHandoffTarget(context.Background(), snapshot, target); err != nil {
		t.Fatal(err)
	}
}

func TestBuilderHandoffRestoresRegularDirectoryFileChangesWithoutOverwritingIgnoredFiles(t *testing.T) {
	for _, mode := range []string{"directory-to-file", "ignored-collision"} {
		t.Run(mode, func(t *testing.T) {
			r, req := handoffFixture(t)
			if mode == "directory-to-file" {
				if err := os.RemoveAll(filepath.Join(req.WorkspacePath, "app")); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(req.WorkspacePath, "app"), "regular replacement file\n")
				req.WritePaths = append(req.WritePaths, "app")
				contract := *req.ProjectExecution
				contract.Basis.WritePaths = append(append([]string{}, contract.Basis.WritePaths...), "app")
				req.ProjectExecution = &contract
				runGit(t, req.WorkspacePath, "add", "-A", "app")
			} else {
				writeTestFile(t, filepath.Join(req.WorkspacePath, "app/new.cjs"), "approved untracked bytes")
			}
			snapshot, err := r.CaptureBuilderHandoff(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			target := handoffTarget(t, r, req)
			if mode == "ignored-collision" {
				exclude := filepath.Join(t.TempDir(), "exclude")
				if err := os.WriteFile(exclude, []byte("app/new.cjs\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runGit(t, req.RepoPath, "config", "extensions.worktreeConfig", "true")
				runGit(t, target.WorkspacePath, "config", "--worktree", "core.excludesFile", exclude)
				writeTestFile(t, filepath.Join(target.WorkspacePath, "app/new.cjs"), "user ignored progress")
				if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); err == nil {
					t.Fatal("ignored target progress overwritten")
				}
				if data, err := os.ReadFile(filepath.Join(target.WorkspacePath, "app/new.cjs")); err != nil || string(data) != "user ignored progress" {
					t.Fatal("target progress changed", err)
				}
				return
			}
			if err := r.RestoreBuilderHandoff(context.Background(), snapshot, target); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(target.WorkspacePath, "app")); err != nil || string(data) != "regular replacement file\n" {
				t.Fatal("regular directory-to-file bytes not restored", err)
			}
		})
	}
}

func TestBuilderHandoffRejectsScopeAndChangedSource(t *testing.T) {
	for _, mode := range []string{"scope", "shared", "symlink", "conflict", "changed", "text-limit", "content-limit", "cancelled", "tamper"} {
		t.Run(mode, func(t *testing.T) {
			r, req := handoffFixture(t)
			switch mode {
			case "scope":
				writeTestFile(t, filepath.Join(req.WorkspacePath, "outside.txt"), "outside")
			case "shared":
				req.SharedPathsRequireApproval = []string{"app/**"}
				writeTestFile(t, filepath.Join(req.WorkspacePath, "app/store.cjs"), "unapproved")
			case "symlink":
				if err := os.Symlink(req.RepoPath, filepath.Join(req.WorkspacePath, "app/link")); err != nil {
					t.Fatal(err)
				}
			case "conflict":
				oid := strings.TrimSpace(runGit(t, req.WorkspacePath, "rev-parse", "HEAD:app/store.cjs"))
				command := exec.Command("git", "-C", req.WorkspacePath, "update-index", "--index-info")
				command.Stdin = strings.NewReader(fmt.Sprintf("100644 %s 1\tapp/store.cjs\n", oid))
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("make conflicted index: %v %s", err, output)
				}
			case "text-limit":
				req.HandoffText = strings.Repeat("x", builderHandoffMaxTextBytes+1)
			case "content-limit":
				writeTestFile(t, filepath.Join(req.WorkspacePath, "app/large.cjs"), strings.Repeat("x", mailFreezeMaxBytes+1))
			}
			ctx := context.Background()
			if mode == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			snapshot, err := r.CaptureBuilderHandoff(ctx, req)
			if mode == "changed" || mode == "tamper" {
				if err != nil {
					t.Fatal(err)
				}
				if mode == "changed" {
					writeTestFile(t, filepath.Join(req.WorkspacePath, "app/store.cjs"), "changed after approval")
				} else {
					if err := os.Chmod(snapshot.ObjectsPath, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(snapshot.ObjectsPath, []byte("bad"), 0o400); err != nil {
						t.Fatal(err)
					}
				}
				if err := r.VerifyBuilderHandoff(context.Background(), snapshot); err == nil {
					t.Fatal("accepted changed seal/source")
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe request accepted")
			}
			dir, dirErr := builderHandoffDirectory(req.SnapshotKey)
			if dirErr == nil {
				if _, statErr := os.Lstat(dir); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("rejected capture published material")
				}
			}
		})
	}
}

func TestBuilderHandoffRejectsLargeHistoryBeforePublication(t *testing.T) {
	r, req := handoffFixture(t)
	// A tiny final tree must not hide a large earlier allowed blob from the
	// object collection and its bounded seal.
	name := filepath.Join(req.WorkspacePath, "app/history.cjs")
	writeTestFile(t, name, strings.Repeat("z", builderHandoffMaxObjectBytes+1))
	runGit(t, req.WorkspacePath, "add", "app/history.cjs")
	runGit(t, req.WorkspacePath, "-c", "commit.gpgsign=false", "commit", "-qm", "large historical blob")
	runGit(t, req.WorkspacePath, "rm", "app/history.cjs")
	runGit(t, req.WorkspacePath, "-c", "commit.gpgsign=false", "commit", "-qm", "small final tree")
	if _, err := r.CaptureBuilderHandoff(context.Background(), req); !errors.Is(err, ports.ErrBuilderHandoffLimit) {
		t.Fatal("large history was not rejected by object-material bound", err)
	}
	dir, _ := builderHandoffDirectory(req.SnapshotKey)
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("limit rejection published a seal")
	}
}
