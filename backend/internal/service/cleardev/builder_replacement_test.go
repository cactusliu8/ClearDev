package cleardev

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/cleardevlocal"
	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Provider replies, process launch/loss and checks are explicit doubles.
// SQLite, original Git workspace, sealed objects and target copy are real.
type builderReplacementHarness struct {
	*projectExecutionFlowHarness
	git                                    *cleardevlocal.Runner
	repo, root, oldSessionID, oldWorkspace string
	native                                 ports.NativeSessionAvailability
	stopped, noOpen, idle                  bool
	spawnCalls, replacementSpawns          int
	lossCalls, captureCalls, restoreCalls  int
}

func replacementGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path, "-c", "user.name=ClearDev Fixture", "-c", "user.email=fixture@localhost", "-c", "core.hooksPath=/dev/null"}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git %v: %s: %v", args, data, err)
	}
	return strings.TrimSpace(string(data))
}

func (h *builderReplacementHarness) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	h.spawnCalls++
	session, a, b, err := h.projectExecutionFlowHarness.Spawn(ctx, cfg)
	if err != nil {
		return session, a, b, err
	}
	path := filepath.Join(h.root, string(session.ID))
	if _, err := os.Stat(path); os.IsNotExist(err) {
		args := []string{"-C", h.repo, "-c", "core.hooksPath=/dev/null", "worktree", "add", path, cfg.Branch}
		if err := exec.CommandContext(ctx, "git", "-C", h.repo, "show-ref", "--verify", "--quiet", "refs/heads/"+cfg.Branch).Run(); err != nil {
			args = []string{"-C", h.repo, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-b", cfg.Branch, path, cfg.WorkspaceBaseCommitSHA}
		}
		command := exec.CommandContext(ctx, "git", args...)
		if raw, err := command.CombinedOutput(); err != nil {
			return session, a, b, fmt.Errorf("fixture worktree: %s: %w", raw, err)
		}
	}
	session.Metadata.WorkspacePath = path
	session.Metadata.WorkspaceRepoPath = h.repo
	session.Activity.State = domain.ActivityIdle
	if cfg.BuilderHandoffContext != nil {
		h.replacementSpawns++
		if err := h.store.SaveClearDevBuilderHandoffContext(ctx, session.ID, *cfg.BuilderHandoffContext, "Explicit fake provider system context: "+cfg.BuilderHandoffContext.ReferencePath, session.CreationRequestFingerprint); err != nil {
			return session, a, b, err
		}
		if cfg.Prompt != "" || cfg.IssueContext != "" || len(cfg.Attachments) != 0 {
			return session, a, b, fmt.Errorf("handoff acquired unmetered initial prompt")
		}
	}
	if err := h.store.UpdateSession(ctx, session.SessionRecord); err != nil {
		return session, a, b, err
	}
	return session, a, b, nil
}

func (h *builderReplacementHarness) ObserveBuilderHandoffSession(ctx context.Context, id domain.SessionID) (ports.BuilderHandoffSessionObservation, error) {
	h.lossCalls++
	r, found, err := h.store.GetSession(ctx, id)
	if err != nil || !found {
		return ports.BuilderHandoffSessionObservation{}, fmt.Errorf("fixture original record unavailable: %w", err)
	}
	return ports.BuilderHandoffSessionObservation{Session: r, NativeRef: ports.NativeSessionRef{NativeSessionID: r.Metadata.ProviderConversationID, ConfigDir: "explicit-fake-native-config"}, NativeAvailability: h.native, RuntimeStopped: h.stopped, NoOpenTurn: h.noOpen, OperationIdle: h.idle, ObservedAt: time.Now().UTC()}, nil
}

func (h *builderReplacementHarness) PrepareReviewBranch(ctx context.Context, repo, branch, sha string) error {
	if err := h.git.PrepareReviewBranch(ctx, repo, branch, sha); err != nil {
		return err
	}
	return h.projectExecutionFlowHarness.PrepareReviewBranch(ctx, repo, branch, sha)
}
func (h *builderReplacementHarness) InspectCandidate(ctx context.Context, path, base string) (ports.ClearDevCandidateInspection, error) {
	return h.git.InspectCandidate(ctx, path, base)
}
func (h *builderReplacementHarness) PrepareBaseWorkspace(ctx context.Context, path, head, base string) error {
	return h.git.PrepareBaseWorkspace(ctx, path, head, base)
}
func (h *builderReplacementHarness) ComposeCandidates(ctx context.Context, r ports.ClearDevComposeRequest) (ports.ClearDevComposeResult, error) {
	return h.git.ComposeCandidates(ctx, r)
}
func (h *builderReplacementHarness) CaptureBuilderHandoff(ctx context.Context, r ports.ClearDevBuilderHandoffSnapshotRequest) (ports.ClearDevBuilderHandoffSnapshot, error) {
	h.captureCalls++
	return h.git.CaptureBuilderHandoff(ctx, r)
}
func (h *builderReplacementHarness) VerifyBuilderHandoff(ctx context.Context, s ports.ClearDevBuilderHandoffSnapshot) error {
	return h.git.VerifyBuilderHandoff(ctx, s)
}
func (h *builderReplacementHarness) RestoreBuilderHandoff(ctx context.Context, s ports.ClearDevBuilderHandoffSnapshot, target ports.ClearDevBuilderHandoffTarget) error {
	h.restoreCalls++
	return h.git.RestoreBuilderHandoff(ctx, s, target)
}
func (h *builderReplacementHarness) VerifyBuilderHandoffTarget(ctx context.Context, s ports.ClearDevBuilderHandoffSnapshot, target ports.ClearDevBuilderHandoffTarget) error {
	return h.git.VerifyBuilderHandoffTarget(ctx, s, target)
}
func (h *builderReplacementHarness) FreezeMailCandidate(ctx context.Context, r ports.ClearDevMailFreezeRequest) (ports.ClearDevCandidateInspection, error) {
	candidate, err := h.git.FreezeMailCandidate(ctx, r)
	if err == nil {
		h.frozen[r.RunID] = candidate
	}
	return candidate, err
}

func (h *builderReplacementHarness) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	if strings.Contains(prompt, `"kind":"BUILDER_RESULT"`) && h.turnByClientMessageID[key] == "" {
		r, _, err := h.store.GetSession(ctx, id)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Join(r.Metadata.WorkspacePath, "src"), 0755); err != nil {
			return "", err
		}
		file := filepath.Join(r.Metadata.WorkspacePath, "src", "storage.ts")
		old, _ := os.ReadFile(file)
		if err := os.WriteFile(file, append(old, []byte("\nexport const resumed = true;\n")...), 0644); err != nil {
			return "", err
		}
	}
	return h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, id, prompt, key)
}

// Planning still uses the existing explicit provider/Git doubles, but must
// bind the real selected baseline rather than the older fixture's aaaa SHA.
type builderReplacementPlanningSessions struct{ *projectPlanningAgent }

func (h builderReplacementPlanningSessions) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	session, x, y, err := h.projectPlanningAgent.Spawn(ctx, cfg)
	if err != nil {
		return session, x, y, err
	}
	if cfg.WorkspaceBaseCommitSHA != "" {
		session.Metadata.DiffBaseSHA = cfg.WorkspaceBaseCommitSHA
	}
	if err := h.store.UpdateSession(ctx, session.SessionRecord); err != nil {
		return domain.Session{}, 0, 0, err
	}
	return session, x, y, nil
}

func newBuilderReplacementFixture(t *testing.T) (*projectPlanningFixture, *builderReplacementHarness, string) {
	t.Helper()
	return newBuilderReplacementFixtureWithLoss(t, true)
}

func newBuilderReplacementFixtureWithLoss(t *testing.T, lost bool) (*projectPlanningFixture, *builderReplacementHarness, string) {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	replacementGit(t, repo, "init", "-q")
	replacementGit(t, repo, "commit", "--allow-empty", "-qm", "admitted empty baseline")
	base := replacementGit(t, repo, "rev-parse", "HEAD")
	f := newProjectPlanningFixture(t, "EMPTY")
	t.Setenv("AO_DATA_DIR", f.dir)
	f.s.logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	p, _, err := f.store.GetProject(ctx, "notes-project")
	if err != nil {
		t.Fatal(err)
	}
	p.Path = repo
	p.Config.AgentConfig.Model = "explicit-handoff-fixture-model"
	if err := f.store.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	f.h.source.BaseCommitSHA = base
	f.h.base = base
	f.s.sessions = builderReplacementPlanningSessions{f.h}
	initial := f.create(t)
	f.h.replies = append(f.h.replies, genericProjectReply("empty"))
	choice := projectChoice(initial, "empty")
	choice.Choice.ExpectedBaseCommitSHA = base
	selected, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, choice)
	if err != nil || selected.Selection == nil || selected.Selection.BaseCommitSHA != base {
		t.Fatal("actual Git source was not selected", err)
	}
	_, child := f.prepare(t, selected)
	f.h.replies = append(f.h.replies, genericEngineeringReply(t, child.RequirementVersions[0]))
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	if err := f.s.runComplexFlow(ctx, child.Requirement.ID); err != nil {
		t.Fatalf("real-Git fixture planning run: %v", err)
	}
	child = mustGetComplex(t, f.s, child.Requirement.ID)
	if child.ComplexPlanning == nil || len(child.ComplexPlanning.Plans) != 1 {
		t.Fatalf("real-Git fixture plan failed: planning=%+v progress=%+v", child.ComplexPlanning, child.TrustedProgress)
	}
	plan := child.ComplexPlanning.Plans[0]
	preparer := &projectExecutionPreparer{projectPlanningAgent: f.h}
	flow := attachProjectFlow(f, preparer)
	root := t.TempDir()
	h := &builderReplacementHarness{projectExecutionFlowHarness: flow, git: cleardevlocal.NewWithRoot(root), repo: repo, root: root, native: ports.NativeSessionAvailabilityUnavailable, stopped: true, noOpen: true, idle: true}
	f.s.sessions, f.s.chat, f.s.inspector = h, h, h
	f.s.checks = flow
	f.s.finalReviews = f.store
	f.s.desktopRunID = "explicit-fake-handoff-desktop"
	f.s.runBackground = func(func()) {}
	id := child.Requirement.ID
	_, err = f.s.StartProjectExecution(ctx, id, core.ProjectExecutionAdmission{RequestID: "start-handoff-project", PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, RequirementSHA256: child.RequirementVersions[0].SHA256, BaseCommitSHA: base})
	if err != nil {
		t.Fatal(err)
	}
	var run core.ComplexExecutionSnapshot
	for i := 0; i < 12; i++ {
		run, _, err = f.store.GetClearDevComplexExecution(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(run.Dispatches) == 1 {
			break
		}
		if _, _, err = f.s.advanceComplexStandardExecution(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(run.Dispatches) != 1 {
		t.Fatal("original unsent dispatch was not created")
	}
	binding, _ := complexExecutionBindingByID(run, run.Run.BuilderRoleBindingID)
	r, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	h.oldSessionID, h.oldWorkspace = string(r.ID), r.Metadata.WorkspacePath
	if err := os.MkdirAll(filepath.Join(h.oldWorkspace, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.oldWorkspace, "src", "storage.ts"), []byte("export const preserved = 'original';\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r.Metadata.ProviderConversationID = "explicit-lost-original-native"
	if lost {
		r.Activity.State = domain.ActivityExited
	}
	if err := f.store.UpdateSession(ctx, r); err != nil {
		t.Fatal(err)
	}
	if lost {
		if _, _, err := f.s.advanceComplexExecutionDispatch(ctx, run, core.ComplexPlanningSnapshot{}, string(r.ProjectID), core.ComplexEngineeringPlanResult{}); err != nil && !errors.Is(err, errComplexExecutionStopped) {
			t.Fatal(err)
		}
	}
	return f, h, id
}

func builderReplacementInput(t *testing.T, f *projectPlanningFixture, id, request, action string) WorkflowRecoveryInput {
	t.Helper()
	v, err := f.s.GetWorkflowRecovery(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range v.Options {
		if o.Action == action && (o.UnavailableReason == "" || o.UnavailableReason == "CONTINUATION_REGISTERED") {
			return WorkflowRecoveryInput{RequestID: request, ExecutionRunID: v.ExecutionRunID, Action: action, TargetID: o.TargetID}
		}
	}
	t.Fatalf("replacement action unavailable: %+v", v.Options)
	return WorkflowRecoveryInput{}
}

func TestBuilderReplacementRequiresNativeApprovalAndExplicitContinue(t *testing.T) {
	f, h, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	beforeSpawns, beforeSends := h.spawnCalls, len(h.relays)
	input := builderReplacementInput(t, f, id, "request-worker-handoff", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	if h.spawnCalls != beforeSpawns || len(h.relays) != beforeSends {
		t.Fatal("request created or sent")
	}
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, id, core.HumanDecisionKindBuilderReplacement, core.HumanDecisionApprove)
	if h.spawnCalls != beforeSpawns || len(h.relays) != beforeSends {
		t.Fatal("approval created or sent")
	}
	cont := builderReplacementInput(t, f, id, "continue-worker-handoff", core.RecoveryContinueBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil {
		t.Fatal(err)
	}
	if h.replacementSpawns != 1 || len(h.relays) != beforeSends {
		t.Fatal("handoff registration did not create exactly one empty worker")
	}
	if _, _, err := f.s.advanceComplexStandardExecution(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(h.relays) != beforeSends+1 {
		run, _, _ := f.store.GetClearDevComplexExecution(ctx, id)
		phase, reason := core.DeriveComplexExecutionPhase(run)
		t.Fatalf("original successor message did not send exactly once: phase=%s reason=%s runBuilder=%s dispatches=%+v roles=%+v", phase, reason, run.Run.BuilderRoleBindingID, run.Dispatches, run.RoleBindings)
	}
	old, err := os.ReadFile(filepath.Join(h.oldWorkspace, "src", "storage.ts"))
	if err != nil || string(old) != "export const preserved = 'original';\n" {
		t.Fatal("old code changed", err)
	}
	state, err := f.s.builderReplacementState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Handoff == nil || state.Handoff.NewAOSessionID == h.oldSessionID {
		t.Fatal("no distinct new identity")
	}
	newCode, err := os.ReadFile(filepath.Join(state.Handoff.NewWorkspacePath, "src", "storage.ts"))
	if err != nil || !strings.Contains(string(newCode), "preserved = 'original'") {
		t.Fatal("original code was discarded", err)
	}
	completed := advanceReplacementToCandidateReview(t, f, id)
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.BuilderReplacements) != 1 || view.BuilderReplacements[0].State != "HANDED_OFF" || view.Diagnosis == nil || !view.Diagnosis.Current {
		t.Fatalf("handed-off task lost its current public state: %+v, %v", view, err)
	}
	for _, option := range view.Options {
		if option.Action == core.RecoveryContinueBuilderReplacement {
			t.Fatal("delivered handoff still offers an unsent continuation")
		}
	}
	for _, issue := range view.Diagnosis.Issues {
		if issue.Relationship == "CURRENT" && issue.ReasonCode == "CONTINUATION_REGISTERED" {
			t.Fatal("delivered handoff is described as not yet sent")
		}
	}
	if completed.Dispatches[0].BuilderRoleBindingID != state.Binding.OldRoleBindingID || completed.Dispatches[0].AgentStepID != state.Binding.LogicalStepID || completed.Dispatches[0].CandidateCommitSHA != replacementGit(t, state.Handoff.NewWorkspacePath, "rev-parse", "HEAD") {
		t.Fatal("candidate rewrote original dispatch or used another workspace")
	}
	var author string
	if err := builderReplacementStorageDB(t, f).QueryRow("SELECT ao_session_id FROM cleardev_candidate_commits WHERE id = ?", completed.Dispatches[0].CandidateCommitID).Scan(&author); err != nil || author != state.Handoff.NewAOSessionID {
		t.Fatal("actual candidate author is not the granted replacement", author, err)
	}
	sendsAfterReview := len(h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil {
		t.Fatal(err)
	}
	if h.replacementSpawns != 1 || len(h.relays) != sendsAfterReview {
		t.Fatal("replay repeated creation or delivery")
	}
}

func TestBuilderReplacementPreservesCommittedStagedAndWorkingCodeThroughCandidate(t *testing.T) {
	f, h, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	replacementGit(t, h.oldWorkspace, "add", "src/storage.ts")
	replacementGit(t, h.oldWorkspace, "commit", "-qm", "original worker committed code")
	sealedHead := replacementGit(t, h.oldWorkspace, "rev-parse", "HEAD")
	file := filepath.Join(h.oldWorkspace, "src", "storage.ts")
	staged := "export const preserved = 'staged';\n"
	working := staged + "export const pending = 'working';\n"
	if err := os.WriteFile(file, []byte(staged), 0644); err != nil {
		t.Fatal(err)
	}
	replacementGit(t, h.oldWorkspace, "add", "src/storage.ts")
	if err := os.WriteFile(file, []byte(working), 0644); err != nil {
		t.Fatal(err)
	}
	input := builderReplacementInput(t, f, id, "request-staged-handoff", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, id, core.HumanDecisionKindBuilderReplacement, core.HumanDecisionApprove)
	cont := builderReplacementInput(t, f, id, "continue-staged-handoff", core.RecoveryContinueBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil {
		t.Fatal(err)
	}
	state, err := f.s.builderReplacementState(ctx, id)
	if err != nil || state.Handoff == nil {
		t.Fatal("missing bound replacement", err)
	}
	target := state.Handoff.NewWorkspacePath
	if replacementGit(t, target, "rev-parse", "HEAD") != sealedHead || replacementGit(t, target, "show", ":src/storage.ts") != strings.TrimSpace(staged) {
		t.Fatal("approved HEAD or staged code was not preserved")
	}
	before, err := os.ReadFile(filepath.Join(target, "src", "storage.ts"))
	if err != nil || string(before) != working {
		t.Fatal("working code was not preserved", err)
	}
	completed := advanceReplacementToCandidateReview(t, f, id)
	candidate := completed.Dispatches[0].CandidateCommitSHA
	if replacementGit(t, target, "rev-parse", candidate+"^") != sealedHead || !strings.Contains(replacementGit(t, target, "show", candidate+":src/storage.ts"), "pending = 'working'") {
		t.Fatal("normal candidate lost approved ancestry or working code")
	}
	if replacementGit(t, h.oldWorkspace, "rev-parse", "HEAD") != sealedHead || replacementGit(t, h.oldWorkspace, "show", ":src/storage.ts") != strings.TrimSpace(staged) {
		t.Fatal("replacement changed original HEAD or index")
	}
	old, err := os.ReadFile(file)
	if err != nil || string(old) != working {
		t.Fatal("replacement changed original working code", err)
	}
}

func TestBuilderReplacementLaterRoundKeepsFailedCandidateAndBudget(t *testing.T) {
	f, h, id := newBuilderReplacementFixtureWithLoss(t, false)
	ctx := context.Background()
	if err := os.Remove(filepath.Join(h.oldWorkspace, "src", "storage.ts")); err != nil {
		t.Fatal(err)
	}
	h.checkFailure = true
	var run core.ComplexExecutionSnapshot
	for i := 0; i < 40; i++ {
		var err error
		run, _, err = f.store.GetClearDevComplexExecution(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(run.Dispatches) == 2 {
			break
		}
		changed, stop, err := f.s.advanceComplexStandardExecution(ctx, id)
		if err != nil || stop && !changed {
			t.Fatalf("first failed check did not reach its normal rework dispatch: run=%+v err=%v", run.Dispatches, err)
		}
	}
	if len(run.Dispatches) != 2 || run.Dispatches[0].CandidateCommitID == "" || run.Dispatches[0].ReasonCode != "CHECK_FAILED" || run.Dispatches[1].Round != 1 {
		t.Fatal("fixture did not retain a genuine failed candidate", run.Dispatches)
	}
	failed := run.Dispatches[0]
	originalSecondBase := run.Dispatches[1].BaseCommitSHA
	r, _, err := f.store.GetSession(ctx, domain.SessionID(h.oldSessionID))
	if err != nil {
		t.Fatal(err)
	}
	r.Activity.State = domain.ActivityExited
	if err := f.store.UpdateSession(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.advanceComplexExecutionDispatch(ctx, run, core.ComplexPlanningSnapshot{}, string(r.ProjectID), core.ComplexEngineeringPlanResult{}); err != nil && !errors.Is(err, errComplexExecutionStopped) {
		t.Fatal(err)
	}
	input := builderReplacementInput(t, f, id, "request-later-round", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, id, core.HumanDecisionKindBuilderReplacement, core.HumanDecisionApprove)
	cont := builderReplacementInput(t, f, id, "continue-later-round", core.RecoveryContinueBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil {
		t.Fatal(err)
	}
	h.checkFailure = false
	current, _, err := f.store.GetClearDevComplexExecution(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	bound, _ := complexExecutionBindingByID(current, current.Run.BuilderRoleBindingID)
	if err := f.s.validateComplexExecutionDispatchWorkspace(ctx, current, current.Tasks[0], current.Dispatches[1], bound.WorkspacePath); err != nil {
		t.Fatalf("later-round replacement workspace gate: %v bound=%+v", err, bound)
	}
	completed := advanceReplacementToCandidateReview(t, f, id)
	if !reflect.DeepEqual(completed.Dispatches[0], failed) || completed.Dispatches[1].Round != 1 || completed.Dispatches[1].BaseCommitSHA != originalSecondBase {
		t.Fatal("replacement rewrote failed candidate or spent another round", completed.Dispatches)
	}
	state, err := f.s.builderReplacementState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if replacementGit(t, state.Handoff.NewWorkspacePath, "rev-parse", completed.Dispatches[1].CandidateCommitSHA+"^") != failed.CandidateCommitSHA {
		t.Fatal("replacement candidate abandoned the prior failed code")
	}
	oldRecord, _, err := f.store.GetSession(ctx, domain.SessionID(h.oldSessionID))
	if err != nil || oldRecord.Metadata.DiffBaseSHA != run.Run.InitialBaseCommitSHA || state.Budget.UsedTurns != 2 {
		t.Fatal("original admission base or original two-round occupancy changed", state.Budget, err)
	}
}

func TestBuilderReplacementLossProofAndReadOnlyGET(t *testing.T) {
	cases := []struct {
		name   string
		change func(*builderReplacementHarness)
	}{
		{"native-available", func(h *builderReplacementHarness) { h.native = ports.NativeSessionAvailabilityAvailable }},
		{"native-unknown", func(h *builderReplacementHarness) { h.native = ports.NativeSessionAvailabilityUnknown }},
		{"runtime-not-stopped", func(h *builderReplacementHarness) { h.stopped = false }},
		{"provider-turn-unsettled", func(h *builderReplacementHarness) { h.noOpen = false }},
		{"operation-not-idle", func(h *builderReplacementHarness) { h.idle = false }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, h, id := newBuilderReplacementFixture(t)
			ctx := context.Background()
			input := builderReplacementInput(t, f, id, "invalid-loss-proof", core.RecoveryRequestBuilderReplacement)
			db := builderReplacementStorageDB(t, f)
			before := builderReplacementStorageRows(t, db)
			for i := 0; i < 3; i++ {
				if _, err := f.s.GetWorkflowRecovery(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			if h.lossCalls != 0 || h.captureCalls != 0 || h.restoreCalls != 0 || !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
				t.Fatal("GET probed or changed persisted facts")
			}
			c.change(h)
			spawns, sends := h.spawnCalls, len(h.relays)
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err == nil {
				t.Fatal("ambiguous or recoverable original was replaced")
			}
			if h.spawnCalls != spawns || len(h.relays) != sends || h.captureCalls != 0 || !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
				t.Fatal("rejected loss proof created side effects")
			}
		})
	}
}

func TestBuilderReplacementExistingUnreservedAttemptAndTerminatedWorker(t *testing.T) {
	f, h, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	state, err := f.s.builderReplacementState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.s.ensureAgentAttempt(ctx, id, core.AgentStepCategoryComplexExecution, state.Step, h.oldSessionID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := f.store.GetSession(ctx, domain.SessionID(h.oldSessionID))
	if err != nil {
		t.Fatal(err)
	}
	record.IsTerminated = true
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	h.native = ports.NativeSessionAvailabilityUnknown
	input := builderReplacementInput(t, f, id, "request-terminated-handoff", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	contInput := WorkflowRecoveryInput{RequestID: "continue-before-approval", ExecutionRunID: input.ExecutionRunID, Action: core.RecoveryContinueBuilderReplacement, TargetID: input.TargetID}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, contInput); err == nil {
		t.Fatal("termination supplied replacement authority")
	}
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, id, core.HumanDecisionKindBuilderReplacement, core.HumanDecisionApprove)
	cont := builderReplacementInput(t, f, id, "continue-terminated-handoff", core.RecoveryContinueBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil {
		t.Fatal(err)
	}
	original, err := f.store.GetClearDevAgentAttemptState(ctx, id, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if original.AOSessionID != h.oldSessionID || original.SendStatus != core.AgentAttemptRetiredBeforeSend || original.TurnID != "" || original.FailureCategory != "" || original.Retryable {
		t.Fatal("old attempt acquired fabricated delivery/failure")
	}
	after, _, err := f.store.GetSession(ctx, domain.SessionID(h.oldSessionID))
	if err != nil || !after.IsTerminated {
		t.Fatal("old termination was reset", err)
	}
}

func advanceReplacementToCandidateReview(t *testing.T, f *projectPlanningFixture, id string) core.ComplexExecutionSnapshot {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		execution, _, err := f.store.GetClearDevComplexExecution(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(execution.Dispatches) > 0 {
			dispatch := execution.Dispatches[len(execution.Dispatches)-1]
			checked, reviewing := false, false
			for _, check := range execution.CheckRuns {
				checked = checked || check.DispatchID == dispatch.ID && check.Status == core.ComplexExecutionCheckRunSettled && check.Result == core.EvidenceResultPass
			}
			for _, review := range execution.Reviews {
				reviewing = reviewing || review.DispatchID == dispatch.ID && review.CandidateCommitSHA == dispatch.CandidateCommitSHA
			}
			if dispatch.CandidateCommitID != "" && checked && reviewing {
				return execution
			}
		}
		changed, stop, err := f.s.advanceComplexStandardExecution(ctx, id)
		if err != nil {
			t.Fatalf("normal candidate/check/review: %v", err)
		}
		if stop && !changed {
			phase, reason := core.DeriveComplexExecutionPhase(execution)
			t.Fatalf("normal candidate/check/review stopped: phase=%s reason=%s dispatches=%+v steps=%+v", phase, reason, execution.Dispatches, execution.AgentSteps)
		}
	}
	t.Fatal("normal candidate/check/review did not finish within its bounded transitions")
	return core.ComplexExecutionSnapshot{}
}
