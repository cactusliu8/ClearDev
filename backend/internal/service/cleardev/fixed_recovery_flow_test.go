package cleardev_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/cleardevlocal"
	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type fixedRecoveryHarness struct {
	originalReviewerModel string
	afterReplacementSpawn func()
	infraRetryFail        bool
	*parallelAgentHarness
	t                                              *testing.T
	repo, root, failKind, action                   string
	runner                                         *cleardevlocal.Runner
	failed                                         bool
	failedSession                                  domain.SessionID
	beforeAction                                   func()
	malformedFirst, malformedSecond, secondFailure bool
	firstMalformedSent                             bool
	failureCategory                                domain.AgentFailureCategory
	emptyProvider                                  bool
	proposed                                       bool
	restores, actualCreates, sends                 int
}

func (h *fixedRecoveryHarness) git(path string, args ...string) string {
	h.t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, args...)...)
	out, err := command.CombinedOutput()
	if err != nil {
		h.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func (h *fixedRecoveryHarness) ObserveWorkspace(_ context.Context, w ports.WorkspaceInfo) (ports.WorkspaceObservation, error) {
	return ports.WorkspaceObservation{Path: w.Path, HeadSHA: h.git(w.Path, "rev-parse", "HEAD"), Branch: h.git(w.Path, "symbolic-ref", "--short", "HEAD"), Dirty: h.git(w.Path, "status", "--porcelain") != ""}, nil
}
func (h *fixedRecoveryHarness) Spawn(ctx context.Context, c ports.SpawnConfig) (domain.Session, int, int, error) {
	workspace := filepath.Join(h.root, parallelDigest([]byte(c.CreationIdempotencyKey))[:20])
	if _, err := os.Stat(workspace); os.IsNotExist(err) {
		if h.git(h.repo, "branch", "--list", c.Branch) == "" {
			h.git(h.repo, "branch", c.Branch, "main")
		}
		h.git(h.repo, "worktree", "add", workspace, c.Branch)
		h.actualCreates++
	}
	providerID := "provider-" + filepath.Base(workspace)
	if h.emptyProvider && c.DisplayName == "ClearDev Complex Reviewer" {
		providerID = ""
	}
	model := c.AgentConfig.Model
	if c.DisplayName == "ClearDev Complex Reviewer" && h.originalReviewerModel != "" {
		model = h.originalReviewerModel
	}
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	record, _, err := h.store.CreateSessionIdempotent(ctx, domain.SessionRecord{ProjectID: c.ProjectID, Kind: c.Kind, Harness: c.Harness, Mode: c.RequestedMode, PermissionMode: c.AgentConfig.Permissions, CreationIdempotencyKey: c.CreationIdempotencyKey, CreationRequestFingerprint: c.CreationIdempotencyKey, DisplayName: c.DisplayName, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}, Metadata: domain.SessionMetadata{Branch: c.Branch, WorkspacePath: workspace, WorkspaceRepoPath: h.repo, DiffBaseRef: "refs/heads/main", DiffBaseSHA: h.git(workspace, "rev-parse", "HEAD"), ProviderConversationID: providerID, Model: model}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return domain.Session{}, 0, 0, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spawnConfigs = append(h.spawnConfigs, c)
	h.sessionWorkspace[record.ID] = workspace
	h.workspaces[workspace] = &parallelWorkspace{headSHA: record.Metadata.DiffBaseSHA, originSHA: record.Metadata.DiffBaseSHA, reviewer: strings.Contains(c.DisplayName, "Reviewer")}
	if c.DisplayName == "ClearDev Replacement Reviewer" && h.afterReplacementSpawn != nil {
		h.afterReplacementSpawn()
	}
	return domain.Session{SessionRecord: record}, 0, 0, nil
}
func (h *fixedRecoveryHarness) PrepareReviewBranch(ctx context.Context, path, branch, sha string) error {
	return h.runner.PrepareReviewBranch(ctx, path, branch, sha)
}
func (h *fixedRecoveryHarness) PrepareBaseWorkspace(ctx context.Context, path, expected, next string) error {
	return h.runner.PrepareBaseWorkspace(ctx, path, expected, next)
}
func (h *fixedRecoveryHarness) InspectCandidate(ctx context.Context, path, base string) (ports.ClearDevCandidateInspection, error) {
	return h.runner.InspectCandidate(ctx, path, base)
}
func (h *fixedRecoveryHarness) ComposeCandidates(ctx context.Context, r ports.ClearDevComposeRequest) (ports.ClearDevComposeResult, error) {
	return h.runner.ComposeCandidates(ctx, r)
}
func (h *fixedRecoveryHarness) ValidateRecoveryWorkspace(ctx context.Context, path, branch, sha string) error {
	return h.runner.ValidateRecoveryWorkspace(ctx, path, branch, sha)
}
func (h *fixedRecoveryHarness) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, clientID string) (string, error) {
	h.sends++
	turn, err := h.parallelAgentHarness.RelayChatTurnWithID(ctx, id, prompt, clientID)
	if err != nil {
		return turn, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := h.snapshots[id]
	last := len(snapshot.Turns) - 1
	if strings.Contains(prompt, `"kind":"RECOVERY_RESULT"`) {
		h.proposed = true
	}
	if strings.Contains(prompt, `"kind":"RECOVERY_RESULT"`) && h.beforeAction != nil {
		h.beforeAction()
	}
	target := strings.Contains(prompt, `"kind":"`+h.failKind+`"`) || (h.malformedFirst && strings.Contains(clientID, ":parse-correction"))
	if target && h.malformedFirst && !h.firstMalformedSent && !strings.Contains(clientID, ":parse-correction") {
		h.firstMalformedSent = true
		snapshot.Messages[len(snapshot.Messages)-1].Text = "{invalid"
		h.snapshots[id] = snapshot
		return turn, nil
	}
	if target && h.malformedSecond && strings.Contains(clientID, ":attempt:2") && !strings.Contains(clientID, ":parse-correction") {
		snapshot.Messages[len(snapshot.Messages)-1].Text = "{invalid"
		h.snapshots[id] = snapshot
		return turn, nil
	}

	if target && (!h.failed || (h.secondFailure && strings.Contains(clientID, ":attempt:2"))) && !strings.Contains(prompt, "RECOVERY_RESULT") {
		h.failed = true
		h.failedSession = id
		snapshot.Turns[last].State = domain.TurnStateFailed
		snapshot.Turns[last].Failure = &domain.ConversationFailure{Category: domain.AgentFailureProviderUnavailable, Retryable: true, ErrorSummary: "fixture provider exited"}
		if h.failureCategory != "" {
			snapshot.Turns[last].Failure.Category = h.failureCategory
		}
		snapshot.Messages = snapshot.Messages[:len(snapshot.Messages)-1]
		h.snapshots[id] = snapshot
		record, found, err := h.store.GetSession(ctx, id)
		if err != nil || !found {
			return turn, fmt.Errorf("read failed session: %w", err)
		}
		record.Activity.State = domain.ActivityExited
		if err = h.store.UpdateSession(ctx, record); err != nil {
			return turn, err
		}
	} else if strings.Contains(prompt, `"kind":"BUILDER_RESULT"`) && !strings.Contains(prompt, "RECOVERY_RESULT") {
		path := h.sessionWorkspace[id]
		for _, name := range writePathsFromExecutionPackage(prompt) {
			if err = os.MkdirAll(filepath.Dir(filepath.Join(path, name)), 0o750); err != nil {
				return turn, err
			}
			if err = os.WriteFile(filepath.Join(path, name), []byte(clientID+"\n"), 0o600); err != nil {
				return turn, err
			}
		}
		h.git(path, "add", ".")
		h.git(path, "commit", "-m", "fixture candidate")
	}
	return turn, nil
}
func (h *fixedRecoveryHarness) restore(ctx context.Context, id domain.SessionID) (string, error) {
	h.restores++
	record, found, err := h.store.GetSession(ctx, id)
	if err != nil || !found {
		return "", fmt.Errorf("missing original session")
	}
	if record.Activity.State != domain.ActivityExited || record.Metadata.ProviderConversationID == "" {
		return "", fmt.Errorf("not native resumable")
	}
	record.Activity.State = domain.ActivityIdle
	err = h.store.UpdateSession(ctx, record)
	return "native", err
}

func TestClearDevFixedRecoveryMainPaths(t *testing.T) {
	for _, mode := range []string{"STANDARD", "PARALLEL"} {
		for _, action := range []string{core.ComplexRecoveryActionRestoreSession, core.ComplexRecoveryActionRebuildReviewer} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				store, h, deps, requirementID, data := fixedRecoveryFlowDeps(t, mode, action)
				service := cleardevsvc.New(deps)
				for wake := 0; wake < 6; wake++ {
					if _, err := service.StartComplexStandardExecution(context.Background(), requirementID); err != nil {
						t.Fatal(err)
					}
					e, _, err := store.GetClearDevComplexExecution(context.Background(), requirementID)
					if err != nil {
						t.Fatal(err)
					}
					phase, _ := core.DeriveComplexExecutionPhase(e)
					if phase == core.ComplexExecutionCompleted {
						break
					}
				}
				e, _, err := store.GetClearDevComplexExecution(context.Background(), requirementID)
				if err != nil {
					t.Fatal(err)
				}
				phase, reason := core.DeriveComplexExecutionPhase(e)
				if phase != core.ComplexExecutionCompleted {
					for _, f := range e.FixedRecoveries {
						t.Logf("result=%+v", f.Result)
					}
					t.Fatalf("phase=%s reason=%s failed=%v restores=%d fixed=%+v dispatch=%+v", phase, reason, h.failed, h.restores, e.FixedRecoveries, e.Dispatches)
				}
				if len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].Result == nil || e.FixedRecoveries[0].Result.Outcome != "PASS" {
					t.Fatalf("missing actual recovery: %+v", e.FixedRecoveries)
				}
				for _, task := range e.Tasks {
					if task.Status != core.DevelopmentTaskStatusDone {
						t.Fatalf("persisted task %s status=%s, want DONE", task.ID, task.Status)
					}
				}
				fixed := e.FixedRecoveries[0]
				attempts, err := store.ListClearDevAgentStepAttemptStates(context.Background(), requirementID, fixed.Request.LogicalStepID)
				if err != nil {
					t.Fatal(err)
				}
				if len(attempts) != 2 || attempts[0].AOSessionID != fixed.Request.SessionID || attempts[1].AOSessionID != fixed.Result.SessionID {
					t.Fatalf("attempt identities: %+v", attempts)
				}
				secondSends := 0
				for _, relay := range h.relays {
					if relay.clientMessageID == attempts[1].ClientMessageID {
						secondSends++
						if relay.sessionID != domain.SessionID(fixed.Result.SessionID) {
							t.Fatal("second request sent to old session")
						}
					}
				}
				if secondSends != 1 {
					t.Fatalf("second sends=%d", secondSends)
				}
				if action == core.ComplexRecoveryActionRebuildReviewer {
					if fixed.ReviewResult == nil || fixed.ReviewResult.Verdict != core.LocalReviewPass {
						t.Fatal("missing separate replacement result")
					}
					for _, review := range e.Reviews {
						if review.ID == fixed.Request.ReviewID && (review.Verdict != "" || review.ReviewerRoleBindingID != fixed.Request.RoleBindingID) {
							t.Fatal("old review overwritten")
						}
					}
					bound := false
					for _, v := range e.Verifications {
						if v.LocalReviewID == fixed.Request.ReviewID && v.ReplacementRecoveryID == fixed.Request.ID && v.ReplacementAttemptID == fixed.ReviewResult.AttemptID && v.ReplacementResultID == fixed.ReviewResult.ResultID {
							bound = true
						}
					}
					if !bound {
						t.Fatal("verification lacks explicit replacement references")
					}
				}
				sends, creates, restores := h.sends, h.actualCreates, h.restores
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := sqlite.Open(data)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = reopened.Close() }()
				rebindFixedRecoveryDeps(&deps, reopened, h)
				if err := cleardevsvc.New(deps).ResumeComplexStandardExecutions(context.Background()); err != nil {
					t.Fatal(err)
				}
				if h.sends != sends || h.actualCreates != creates || h.restores != restores {
					t.Fatal("reopen repeated a side effect")
				}
				t.Logf("database DONE; mode=%s action=%s restores=%d actual worktrees/sessions=%d sends=%d", mode, action, h.restores, h.actualCreates, h.sends)
			})
		}
	}
}

func fixedRecoveryFlowDeps(t *testing.T, mode, action string) (*sqlite.Store, *fixedRecoveryHarness, cleardevsvc.Deps, string, string) {
	t.Helper()
	data := t.TempDir()
	store := sqlitetest.MustOpenAt(t, data)
	repo := initParallelPrepRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	var requirementID string
	if mode == "STANDARD" {
		prep := cleardevtest.SeedComplexStandardV2(t, store, "fixed-standard", repo, data)
		requirementID = prep.RequirementID
	} else {
		prep := cleardevtest.SeedComplexParallelV2(t, store, "fixed-parallel", repo, data)
		requirementID = prep.RequirementID
	}
	h := &fixedRecoveryHarness{parallelAgentHarness: newParallelAgentHarness(store, false), t: t, repo: repo, root: root, runner: cleardevlocal.NewWithRoot(root), action: action, failKind: "BUILDER_RESULT"}
	if action == core.ComplexRecoveryActionRebuildReviewer {
		h.failKind = "LOCAL_REVIEW"
	}
	h.quickOverride = func(raw string) string {
		return strings.ReplaceAll(raw, core.ComplexRecoveryActionRetryInfraCheck, action)
	}
	ids := &parallelTestIDs{}
	tick := 0
	clock := func() time.Time {
		tick++
		return time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC).Add(time.Duration(tick) * time.Second)
	}
	deps := cleardevsvc.Deps{Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store, ProgressExplanations: store, ParseCorrections: store, AgentAttempts: store, ControlledPreflights: store, ControlledPreflightChecker: passingProgressPreflight{}, AO: store, Workspace: h, Sessions: h, RecoverAgentSession: func(context.Context, domain.SessionID) error { return fmt.Errorf("unexpected automatic restore") }, RestoreOriginalAgentSession: h.restore, Chat: h, Inspector: h, Checks: h, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)), Human: allowParallelHuman{}, RunBackground: func(run func()) { run() }, BackgroundContext: context.Background(), StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock}
	return store, h, deps, requirementID, data
}

func rebindFixedRecoveryDeps(deps *cleardevsvc.Deps, store *sqlite.Store, h *fixedRecoveryHarness) {
	deps.Facts = store
	deps.StandardFacts = store
	deps.ComplexFacts = store
	deps.ComplexExecutionFacts = store
	deps.DirectionFacts = store
	deps.HumanDecisions = store
	deps.ProgressExplanations = store
	deps.ParseCorrections = store
	deps.AgentAttempts = store
	deps.ControlledPreflights = store
	deps.AO = store
	h.store = store
}

func (h *fixedRecoveryHarness) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	if h.infraRetryFail && request.AllowInfraInject && h.integrationChecks == 1 {
		h.integrationChecks++
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckFail, ExitCode: 1, OutputSummary: "retry failed", OutputSHA256: parallelDigest([]byte("retry failed"))}, nil
	}
	return h.parallelAgentHarness.RunCandidateCheck(ctx, request)
}
