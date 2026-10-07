package cleardev

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/cleardevlocal"
	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Only Git operations and SQLite/control-plane facts are real in this fixture.
// Provider text, native authority, checks and reviewer verdicts remain explicit
// deterministic doubles. Passing it does NOT prove live-model or Docker health.
type runtimeGitHarness struct {
	*runtimeAgentHarness
	t          *testing.T
	repo, root string
	runner     *cleardevlocal.Runner
	committed  map[string]bool
	clock      func() time.Time
}

func (h *runtimeGitHarness) git(path string, args ...string) string {
	h.t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, args...)...)
	out, err := command.CombinedOutput()
	if err != nil {
		h.t.Fatalf("Git fixture %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRuntimeGitHarness(t *testing.T, f *autoExecutionFixture, h *runtimeAgentHarness) *runtimeGitHarness {
	t.Helper()
	root := t.TempDir()
	g := &runtimeGitHarness{runtimeAgentHarness: h, t: t, repo: filepath.Join(root, "repo"),
		root: root, runner: cleardevlocal.NewWithRoot(root), committed: map[string]bool{}, clock: f.clock}
	if err := os.MkdirAll(g.repo, 0o750); err != nil {
		t.Fatal(err)
	}
	g.git(g.repo, "init", "-b", "main")
	g.git(g.repo, "config", "user.name", "ClearDev deterministic Git test")
	g.git(g.repo, "config", "user.email", "cleardev@example.invalid")
	if err := os.WriteFile(filepath.Join(g.repo, "README.md"), []byte("Runtime coordination Git fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.git(g.repo, "add", "README.md")
	g.git(g.repo, "commit", "-m", "baseline")
	if err := f.store.UpsertProject(context.Background(), domain.ProjectRecord{ID: "s04-project", Path: g.repo,
		Kind: domain.ProjectKindSingleRepo, RegisteredAt: f.clock()}); err != nil {
		t.Fatal(err)
	}
	g.attach(f)
	return g
}

func (h *runtimeGitHarness) attach(f *autoExecutionFixture) {
	attachRuntimeHarness(f, h.runtimeAgentHarness)
	f.service.sessions, f.service.chat, f.service.inspector = h, h, h
}

func (h *runtimeGitHarness) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	workspace := filepath.Join(h.root, coreDigest([]byte(cfg.CreationIdempotencyKey))[:20])
	if _, err := os.Stat(workspace); os.IsNotExist(err) {
		if h.git(h.repo, "branch", "--list", cfg.Branch) == "" {
			h.git(h.repo, "branch", cfg.Branch, "main")
		}
		h.git(h.repo, "worktree", "add", workspace, cfg.Branch)
	}
	head := h.git(workspace, "rev-parse", "HEAD")
	now := h.clock()
	record, _, err := h.store.CreateSessionIdempotent(ctx, domain.SessionRecord{
		ProjectID: cfg.ProjectID, Kind: cfg.Kind, Harness: cfg.Harness, Mode: cfg.RequestedMode,
		PermissionMode: cfg.AgentConfig.Permissions, CreationIdempotencyKey: cfg.CreationIdempotencyKey,
		CreationRequestFingerprint: cfg.CreationIdempotencyKey, DisplayName: cfg.DisplayName,
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		Metadata: domain.SessionMetadata{Branch: cfg.Branch, WorkspacePath: workspace, WorkspaceRepoPath: h.repo,
			DiffBaseRef: "refs/heads/main", DiffBaseSHA: head, Model: cfg.AgentConfig.Model}, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return domain.Session{}, 0, 0, err
	}
	h.gitMu.Lock()
	h.workspaces[workspace] = &contractFakeWorkspace{base: head, head: head}
	h.gitMu.Unlock()
	h.mu.Lock()
	h.spawnConfigs = append(h.spawnConfigs, cfg)
	if strings.Contains(cfg.DisplayName, "Reviewer") {
		h.reviewerWorkspaces[workspace] = head
	} else if strings.Contains(cfg.DisplayName, "Builder") {
		h.builderWorkspace = workspace
	}
	if strings.HasPrefix(cfg.CreationIdempotencyKey, "cleardev-requirement-final-review:") {
		h.final.finalSessions[record.ID] = true
	}
	h.mu.Unlock()
	return domain.Session{SessionRecord: record}, 0, 0, nil
}

func (h *runtimeGitHarness) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, clientID string) (string, error) {
	turn, err := h.runtimeAgentHarness.RelayChatTurnWithID(ctx, id, prompt, clientID)
	if err != nil || !strings.Contains(prompt, taskContractBuilderInstructions) || h.committed[clientID] {
		return turn, err
	}
	_, tail, _ := strings.Cut(prompt, "执行包：\n")
	var pkg core.ComplexStandardExecutionPackage
	if err := json.NewDecoder(strings.NewReader(tail)).Decode(&pkg); err != nil {
		return "", err
	}
	record, found, err := h.store.GetSession(ctx, id)
	if err != nil || !found {
		return "", err
	}
	workspace := record.Metadata.WorkspacePath
	files := map[string]string{}
	if pkg.TaskKey == "implement-core" {
		files["src/email.js"] = "exports.normalize = values => [...new Set(values.map(v => v.trim().toLowerCase()))];\n"
		files["test/email.test.js"] = "const {test}=require('node:test'); const assert=require('node:assert/strict'); const {normalize}=require('../src/email'); test('stable normalization',()=>assert.deepEqual(normalize([' B@X ','a@x','b@x']),['b@x','a@x']));\n"
	} else {
		files["src/summary.js"] = "const {normalize}=require('./email'); exports.summary=values=>({addresses:normalize(values),count:normalize(values).length});\n"
		files["test/summary.test.js"] = "const {test}=require('node:test'); const assert=require('node:assert/strict'); const {summary}=require('../src/summary'); test('preserve provider order',()=>assert.deepEqual(summary([' B@X ','a@x','b@x']).addresses,['b@x','a@x']));\n"
	}
	for path, body := range files {
		full := filepath.Join(workspace, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			return "", err
		}
	}
	h.git(workspace, "add", "src", "test")
	h.git(workspace, "commit", "-m", "deterministic Builder: "+pkg.TaskKey)
	h.committed[clientID] = true
	return turn, nil
}

func (h *runtimeGitHarness) InspectCandidate(ctx context.Context, path, base string) (ports.ClearDevCandidateInspection, error) {
	return h.runner.InspectCandidate(ctx, path, base)
}

func (h *runtimeGitHarness) PrepareReviewBranch(ctx context.Context, path, branch, candidate string) error {
	if err := h.runner.PrepareReviewBranch(ctx, path, branch, candidate); err != nil {
		return err
	}
	h.mu.Lock()
	h.reviewBranchCandidates[branch] = candidate
	h.mu.Unlock()
	return nil
}

func (h *runtimeGitHarness) PrepareBaseWorkspace(ctx context.Context, path, expected, next string) error {
	return h.runner.PrepareBaseWorkspace(ctx, path, expected, next)
}

func TestPlannerRuntimeRealGitContractsAndRestart(t *testing.T) {
	ctx := context.Background()
	f, h := newRuntimeFixture(t, "dependency")
	g := newRuntimeGitHarness(t, f, h)
	h.beforeRuntime = func(ctx context.Context, _ string) error {
		execution, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
		if err != nil {
			return err
		}
		candidate := execution.Dispatches[0].CandidateCommitSHA
		if g.git(g.repo, "cat-file", "-t", candidate) != "commit" || !strings.Contains(execution.PlannerRuntime.Requests[0].ContextJSON, candidate) {
			t.Fatal("Planner context does not bind the real frozen Git commit")
		}
		return nil
	}
	crash := &runtimeCrashStore{ComplexFactStore: f.store, ComplexExecutionFactStore: f.store,
		PlannerRuntimeFactStore: f.store, boundary: "application"}
	f.service.complex, f.service.complexExecution = crash, crash
	f.confirm(t)
	if !crash.fired {
		t.Fatal("real Git execution did not reach the persisted amendment boundary")
	}
	before, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	g.attach(f)
	if err := f.service.ResumeComplexStandardExecutions(ctx); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	phase, reason := core.DeriveComplexExecutionPhase(after)
	if phase != core.ComplexExecutionCompleted || len(after.Dispatches) != 2 || len(after.Reviews) != 2 ||
		after.FinalReview == nil || after.FinalReview.Verdict != "PASS" || after.Integration == nil || h.runtimeTurns != 1 {
		t.Fatalf("real Git/SQLite execution did not complete: phase=%s reason=%s dispatches=%d reviews=%d", phase, reason, len(after.Dispatches), len(after.Reviews))
	}
	if before.Dispatches[0].CandidateCommitSHA != after.Dispatches[0].CandidateCommitSHA ||
		before.Tasks[0].ExecutionPackageSHA256 != after.Tasks[0].ExecutionPackageSHA256 ||
		before.Tasks[1].ExecutionPackageSHA256 != after.Tasks[1].ExecutionPackageSHA256 ||
		after.Dispatches[1].BaseCommitSHA != after.Dispatches[0].CandidateCommitSHA {
		t.Fatal("restart changed the reviewed source, effective contract or dependency Git base")
	}
	for _, dispatch := range after.Dispatches {
		if g.git(g.repo, "cat-file", "-t", dispatch.CandidateCommitSHA) != "commit" {
			t.Fatal("candidate is not a real Git commit")
		}
	}
	if g.git(g.builderWorkspace, "rev-parse", "HEAD") != after.Integration.CandidateCommitSHA || g.git(g.builderWorkspace, "status", "--porcelain") != "" {
		t.Fatal("final candidate does not match the clean real Builder worktree")
	}
}

// A durable amendment does not authorize using a changed Builder branch. Move
// a real temporary Git worktree after the application crash window, then reopen
// SQLite; the existing workspace guard must stop before any downstream send.
func TestPlannerRuntimeRealGitCandidateDriftStopsAfterAmendment(t *testing.T) {
	ctx := context.Background()
	f, h := newRuntimeFixture(t, "dependency")
	g := newRuntimeGitHarness(t, f, h)
	crash := &runtimeCrashStore{ComplexFactStore: f.store, ComplexExecutionFactStore: f.store,
		PlannerRuntimeFactStore: f.store, boundary: "application"}
	f.service.complex, f.service.complexExecution = crash, crash
	f.confirm(t)
	before, found, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil || !found || !crash.fired || before.PlannerRuntime == nil || len(before.PlannerRuntime.Amendments) != 1 || len(before.Dispatches) != 1 || len(before.Reviews) != 1 || before.Reviews[0].Verdict != "PASS" {
		t.Fatalf("did not freeze a reviewed candidate and remaining amendment: found=%v crash=%v err=%v", found, crash.fired, err)
	}
	counts := f.counts()
	g.git(g.builderWorkspace, "commit", "--allow-empty", "-m", "deterministic external branch drift after amendment")
	drifted := g.git(g.builderWorkspace, "rev-parse", "HEAD")
	if drifted == before.Dispatches[0].CandidateCommitSHA {
		t.Fatal("fixture did not change the actual Git candidate")
	}
	f.reopen(t)
	g.attach(f)
	// A guarded resume may return a stop error; durable facts and external send
	// counts, rather than that error's wording, determine the safety outcome.
	_ = f.service.ResumeComplexStandardExecutions(ctx)
	after, found, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil || !found {
		t.Fatalf("cannot read resumed drifted execution: found=%v err=%v", found, err)
	}
	if after.Run.CompletedAt != nil || after.FinalReview != nil || after.Integration != nil || f.counts().builderSends != counts.builderSends {
		t.Fatal("amendment allowed downstream work or completion on a changed Git candidate")
	}
	if h.runtimeTurns != 1 || len(after.PlannerRuntime.Requests) != 1 || len(after.PlannerRuntime.Decisions) != 1 || len(after.PlannerRuntime.Amendments) != 1 || after.PlannerRuntime.Decisions[0].Outcome != core.PlannerRuntimeAmend {
		t.Fatal("candidate drift rewrote or refunded the already applied coordination")
	}
	if after.Dispatches[0].CandidateCommitSHA != before.Dispatches[0].CandidateCommitSHA || after.Reviews[0].ReviewPacketSHA256 != before.Reviews[0].ReviewPacketSHA256 || after.Tasks[1].ExecutionPackageSHA256 != before.Tasks[1].ExecutionPackageSHA256 {
		t.Fatal("candidate drift replaced a frozen candidate, review or effective contract")
	}
	if g.git(g.builderWorkspace, "rev-parse", "HEAD") != drifted {
		t.Fatal("resume silently reset the changed Builder branch")
	}
	counts = f.counts()
	f.reopen(t)
	g.attach(f)
	_ = f.service.ResumeComplexStandardExecutions(ctx)
	if f.counts().builderSends != counts.builderSends || h.runtimeTurns != 1 || g.git(g.builderWorkspace, "rev-parse", "HEAD") != drifted {
		t.Fatal("a second restart retried work or reset the changed branch")
	}
}
