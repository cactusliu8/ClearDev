package cleardev

import (
	"context"
	"errors"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var errFinalReviewTestPause = errors.New("explicit test pause after durable final-review boundary")

// The only substituted completion behavior is a refusal. Reopening restores
// the production store and its original atomic completion transaction.
type holdFinalCompletionStore struct {
	ComplexExecutionFactStore
	ReviewerCheckStore
	held bool
}

func (s *holdFinalCompletionStore) CompleteClearDevComplexExecution(ctx context.Context, command core.CompleteComplexExecutionCommand) error {
	if s.held {
		return errFinalReviewTestPause
	}
	return s.ComplexExecutionFactStore.CompleteClearDevComplexExecution(ctx, command)
}

type pauseFinalReviewStore struct {
	RequirementFinalReviewFactStore
	boundary string
	paused   bool
}

func (s *pauseFinalReviewStore) after(boundary string, err error) error {
	if err == nil && s.boundary == boundary {
		s.paused = true
		return errFinalReviewTestPause
	}
	return err
}

func (s *pauseFinalReviewStore) CreateClearDevRequirementFinalReview(ctx context.Context, review core.RequirementFinalReview) error {
	if s.paused {
		return errFinalReviewTestPause
	}
	return s.after("REQUESTED", s.RequirementFinalReviewFactStore.CreateClearDevRequirementFinalReview(ctx, review))
}

func (s *pauseFinalReviewStore) BindClearDevRequirementFinalReview(ctx context.Context, id, session, workspace string, at time.Time) error {
	if s.paused {
		return errFinalReviewTestPause
	}
	return s.after("PENDING", s.RequirementFinalReviewFactStore.BindClearDevRequirementFinalReview(ctx, id, session, workspace, at))
}

func (s *pauseFinalReviewStore) MarkClearDevRequirementFinalReviewSent(ctx context.Context, id string, at time.Time) error {
	if s.paused {
		return errFinalReviewTestPause
	}
	return s.after("SENT", s.RequirementFinalReviewFactStore.MarkClearDevRequirementFinalReviewSent(ctx, id, at))
}

func (s *pauseFinalReviewStore) SettleClearDevRequirementFinalReview(ctx context.Context, id, turn, message string, at time.Time) error {
	if s.paused {
		return errFinalReviewTestPause
	}
	return s.after("SETTLED", s.RequirementFinalReviewFactStore.SettleClearDevRequirementFinalReview(ctx, id, turn, message, at))
}

func pauseAtFinalReview(t *testing.T, boundary string) (*autoExecutionFixture, *requirementFinalReviewHarness, core.RequirementFinalReview) {
	t.Helper()
	f, h := newRequirementFinalReviewFixture(t)
	f.service.finalReviews = &pauseFinalReviewStore{RequirementFinalReviewFactStore: f.store, boundary: boundary}
	f.service.complexExecution = &holdFinalCompletionStore{ComplexExecutionFactStore: f.store, ReviewerCheckStore: f.store, held: true}
	f.confirm(t)
	execution := finalReviewExecution(t, f)
	if execution.FinalReview == nil || execution.FinalReview.Status != boundary || execution.Integration != nil {
		status := "MISSING"
		if execution.FinalReview != nil {
			status = execution.FinalReview.Status
		}
		t.Fatalf("did not stop at %s: status=%s integration=%v", boundary, status, execution.Integration != nil)
	}
	assertMailNotCompleted(t, f)
	return f, h, *execution.FinalReview
}

func TestRequirementFinalReviewDurableBoundaryRestart(t *testing.T) {
	for _, boundary := range []string{"REQUESTED", "PENDING", "SENT", "SETTLED"} {
		t.Run(boundary, func(t *testing.T) {
			f, h, before := pauseAtFinalReview(t, boundary)
			f.reopen(t)
			attachRequirementFinalReviewFixture(f, h)
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			after := f.completed(t)
			review := after.FinalReview
			if review == nil || review.ID != before.ID || review.ReviewPacketSHA256 != before.ReviewPacketSHA256 || review.PromptSHA256 != before.PromptSHA256 || review.Verdict != "PASS" || review.CandidateCommitSHA != after.Integration.CandidateCommitSHA {
				t.Fatal("restart changed immutable final review identity or candidate")
			}
			if before.AOSessionID != "" && review.AOSessionID != before.AOSessionID {
				t.Fatal("restart silently replaced the final reviewer")
			}
			if len(h.finalSessions) != 1 || h.finalSends != 1 || h.deliveryCalls != 1 {
				t.Fatalf("restart repeated final work: sessions=%d sends=%d integrationChecks=%d", len(h.finalSessions), h.finalSends, h.deliveryCalls)
			}
		})
	}
}

func TestRequestedFinalReviewerSeedIsControlledBeforeBinding(t *testing.T) {
	f, _, review := pauseAtFinalReview(t, "REQUESTED")
	ctx := context.Background()
	if review.AOSessionID != "" {
		t.Fatal("fixture is already bound")
	}
	now := f.clock()
	seed, err := f.store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "s04-project", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto,
		CreationIdempotencyKey: finalReviewerSessionKey(review), CreationRequestFingerprint: "final-review-seed-test",
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		Metadata:  domain.SessionMetadata{Branch: "cleardev-requirement-final-review-" + review.SessionReviewID(), WorkspacePath: f.dir},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	controlled, err := f.store.IsClearDevControlledSession(ctx, string(seed.ID))
	if err != nil || !controlled {
		t.Fatalf("unbound final reviewer seed: controlled=%v err=%v", controlled, err)
	}
}

func TestRequirementFinalReviewRejectsTaskOrPlannerSession(t *testing.T) {
	f, h, review := pauseAtFinalReview(t, "REQUESTED")
	execution := finalReviewExecution(t, f)
	planning, found, err := f.store.GetClearDevComplexPlanning(context.Background(), f.view.Requirement.ID)
	if err != nil || !found {
		t.Fatalf("missing planning facts: found=%v err=%v", found, err)
	}
	ids := make(map[string]bool)
	for _, binding := range execution.RoleBindings {
		ids[binding.AOSessionID] = true
	}
	for _, binding := range planning.RoleBindings {
		ids[binding.AOSessionID] = true
	}
	tested := 0
	for id := range ids {
		if id == "" {
			continue
		}
		record, found, err := f.store.GetSession(context.Background(), domain.SessionID(id))
		if err != nil || !found {
			t.Fatalf("missing saved prior role session: %s found=%v err=%v", id, found, err)
		}
		if err := f.store.BindClearDevRequirementFinalReview(context.Background(), review.ID, id, record.Metadata.WorkspacePath, f.clock()); err == nil {
			t.Fatal("final review accepted a prior task/planning role session")
		}
		tested++
	}
	if tested != 4 || finalReviewExecution(t, f).FinalReview.Status != "REQUESTED" {
		t.Fatalf("did not reject all four prior roles atomically: tested=%d", tested)
	}
	f.reopen(t)
	attachRequirementFinalReviewFixture(f, h)
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.completed(t)
}

func TestRequirementFinalReviewNeedsActualReservedSendAndOwnResult(t *testing.T) {
	f, h, review := pauseAtFinalReview(t, "PENDING")
	if h.finalSends != 0 {
		t.Fatal("test did not stop before final provider send")
	}
	if err := f.store.MarkClearDevRequirementFinalReviewSent(context.Background(), review.ID, f.clock()); err == nil {
		t.Fatal("final review marked SENT without a confirmed reserved send")
	}
	execution := finalReviewExecution(t, f)
	taskStep, found := effectiveReviewStep(execution, execution.Reviews[0])
	if !found || taskStep.TurnID == "" || taskStep.FinalMessageID == "" {
		t.Fatal("task review has no saved provider-turn evidence")
	}
	if err := f.store.SettleClearDevRequirementFinalReview(context.Background(), review.ID, taskStep.TurnID, taskStep.FinalMessageID, f.clock()); err == nil {
		t.Fatal("a task provider result was accepted as the final review")
	}
	if finalReviewExecution(t, f).FinalReview.Status != "PENDING" {
		t.Fatal("rejected send/result changed final review state")
	}
	assertMailNotCompleted(t, f)
	f.reopen(t)
	attachRequirementFinalReviewFixture(f, h)
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.completed(t)
}

func TestRequirementFinalReviewSettledPassSurvivesReviewerExit(t *testing.T) {
	f, h, before := pauseAtFinalReview(t, "SETTLED")
	if before.Verdict != "PASS" || before.ResultID == "" {
		t.Fatal("test requires a persisted final PASS")
	}
	record, found, err := f.store.GetSession(context.Background(), domain.SessionID(before.AOSessionID))
	if err != nil || !found {
		t.Fatalf("missing final Reviewer session: found=%v err=%v", found, err)
	}
	record.IsTerminated = true
	record.Activity.State = domain.ActivityExited
	if err := f.store.UpdateSession(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	attachRequirementFinalReviewFixture(f, h)
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := f.completed(t)
	if after.FinalReview == nil || after.FinalReview.ResultID != before.ResultID || after.FinalReview.Verdict != "PASS" || after.Integration == nil || after.Integration.CandidateCommitSHA != before.CandidateCommitSHA {
		t.Fatalf("reviewer exit invalidated durable final PASS: before=%+v after=%+v", before, after.FinalReview)
	}
	if h.finalSends != 1 {
		t.Fatalf("settled PASS caused another provider send after reviewer exit: %d", h.finalSends)
	}
}

func TestRequirementFinalReviewBindingDriftPersistsFailure(t *testing.T) {
	f, h, before := pauseAtFinalReview(t, "PENDING")
	record, found, err := f.store.GetSession(context.Background(), domain.SessionID(before.AOSessionID))
	if err != nil || !found {
		t.Fatalf("missing final Reviewer session: found=%v err=%v", found, err)
	}
	record.Metadata.WorkspacePath = before.WorkspacePath + "-moved"
	if err := f.store.UpdateSession(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	attachRequirementFinalReviewFixture(f, h)
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := finalReviewExecution(t, f)
	if after.FinalReview == nil || after.FinalReview.Status != "FAILED" || after.FinalReview.ReasonCode != "REQUIREMENT_FINAL_REVIEW_WORKSPACE_CHANGED" {
		t.Fatalf("reviewer binding drift did not persist a safe terminal stop: %+v", after.FinalReview)
	}
	if after.FinalReview.AOSessionID != before.AOSessionID || after.FinalReview.WorkspacePath != before.WorkspacePath || after.FinalReview.BoundAt == nil || before.BoundAt == nil || !after.FinalReview.BoundAt.Equal(*before.BoundAt) {
		t.Fatal("failure persistence rewrote the original final Reviewer binding")
	}
	assertMailNotCompleted(t, f)
}

func TestRequirementFinalReviewChangedCandidateCannotReusePass(t *testing.T) {
	for _, mode := range []string{"source-sha", "reviewer-workspace"} {
		t.Run(mode, func(t *testing.T) {
			f, h, before := pauseAtFinalReview(t, "SETTLED")
			if before.Verdict != "PASS" || before.ResultID == "" {
				t.Fatal("test requires a real persisted final PASS before the changed candidate")
			}
			f.reopen(t)
			attachRequirementFinalReviewFixture(f, h)
			h.mu.Lock()
			if mode == "source-sha" {
				h.sourceSHA = forty("f")
			} else {
				h.dirtyReviewer = true
			}
			h.mu.Unlock()
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertMailNotCompleted(t, f)
			stopped := finalReviewExecution(t, f)
			phase, _ := core.DeriveComplexExecutionPhase(stopped)
			if phase != core.ComplexExecutionBlocked || stopped.FinalReview.ResultID != before.ResultID || stopped.FinalReview.Verdict != "PASS" {
				t.Fatal("candidate change failed to stop safely or rewrote the old PASS")
			}
			view, err := f.service.GetRequirement(context.Background(), f.view.Requirement.ID)
			if err != nil || view.ComplexExecution == nil || view.ComplexExecution.Phase != core.ComplexExecutionBlocked {
				t.Fatalf("current progress hid the candidate-change stop: view=%+v err=%v", view.ComplexExecution, err)
			}
			h.mu.Lock()
			h.sourceSHA, h.dirtyReviewer = "", false
			h.mu.Unlock()
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertMailNotCompleted(t, f)
			if err := f.store.CompleteClearDevComplexExecution(context.Background(), finalReviewCompletionCommand(t, f, stopped)); err == nil {
				t.Fatal("restoring the old tree reused invalidated completion evidence")
			}
			if h.finalSends != 1 {
				t.Fatal("changed candidate silently restarted final review")
			}
		})
	}
}
