package cleardev

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type replacementMailSeam struct {
	*sqlite.Store
	mode     string
	hit      bool
	rejected error
}

func (s *replacementMailSeam) RequestClearDevReviewerChecks(ctx context.Context, r core.ReviewCheckRequest) error {
	if r.ReplacementRecoveryID == "" {
		return s.Store.RequestClearDevReviewerChecks(ctx, r)
	}
	switch s.mode {
	case "wrong-recovery":
		r.ReplacementRecoveryID = "foreign-recovery"
	case "wrong-attempt":
		r.RequestAttemptID = "foreign-attempt"
	case "wrong-result":
		r.RequestResultID = "foreign-result"
	case "wrong-candidate":
		r.CandidateSHA = forty("f")
	case "missing-source":
		r.ReplacementRecoveryID = ""
	}
	err := s.Store.RequestClearDevReviewerChecks(ctx, r)
	if strings.HasPrefix(s.mode, "wrong-") || s.mode == "missing-source" {
		s.hit = true
		s.rejected = err
	}
	if s.mode == "after-request" && err == nil && !s.hit {
		s.hit = true
		return errors.New("explicit crash after replacement request")
	}
	return err
}
func (s *replacementMailSeam) RecordClearDevReviewerCheck(ctx context.Context, r core.ReviewCheckEvidence) error {
	if s.mode == "receipt-sha" {
		r.Proof.CandidateSHA = forty("f")
	}
	err := s.Store.RecordClearDevReviewerCheck(ctx, r)
	if s.mode == "receipt-sha" {
		s.hit = true
		s.rejected = err
	}
	if s.mode == "after-receipt" && err == nil && !s.hit {
		s.hit = true
		return errors.New("explicit crash after trusted receipt")
	}
	return err
}
func (s *replacementMailSeam) CreateClearDevComplexExecutionAgentStep(ctx context.Context, step core.AgentStep) (core.AgentStep, bool, error) {
	out, created, err := s.Store.CreateClearDevComplexExecutionAgentStep(ctx, step)
	if strings.HasSuffix(step.ID, ":check-results") && s.mode == "after-followup" && err == nil && !s.hit {
		s.hit = true
		return out, created, errors.New("explicit crash after followup")
	}
	return out, created, err
}
func (s *replacementMailSeam) MarkClearDevComplexExecutionAgentStepSent(ctx context.Context, id string, at time.Time) (bool, error) {
	changed, err := s.Store.MarkClearDevComplexExecutionAgentStepSent(ctx, id, at)
	if strings.HasSuffix(id, ":check-results") && s.mode == "after-send" && err == nil && !s.hit {
		s.hit = true
		return changed, errors.New("explicit crash after followup send")
	}
	return changed, err
}
func (s *replacementMailSeam) ConfirmClearDevAgentMessage(ctx context.Context, event core.AgentAttemptEvent) error {
	if strings.HasPrefix(event.ClientMessageID, "cleardev-review-check-results-") && s.mode == "before-receipt" && !s.hit {
		s.hit = true
		return errors.New("explicit lost provider receipt")
	}
	return s.Store.ConfirmClearDevAgentMessage(ctx, event)
}
func (s *replacementMailSeam) RecordClearDevReplacementReviewResult(ctx context.Context, r core.ReplacementReviewResult) error {
	switch s.mode {
	case "verdict-original-request":
		request, _, _, err := s.GetClearDevReviewerChecks(ctx, r.OriginalReviewID)
		if err != nil {
			return err
		}
		r.AttemptID, r.ResultID = request.RequestAttemptID, request.RequestResultID
	case "verdict-other-review":
		r.OriginalReviewID = "foreign-review"
	case "verdict-other-recovery":
		r.RecoveryRequestID = "foreign-recovery"
	case "verdict-old-attempt":
		r.AttemptID = "foreign-attempt"
	case "verdict-waive-rework":
		r.Verdict = core.LocalReviewPass
		r.ReasonCode = "REVIEW_PASSED"
	}
	err := s.Store.RecordClearDevReplacementReviewResult(ctx, r)
	if strings.HasPrefix(s.mode, "verdict-") {
		s.hit = true
		s.rejected = err
	}
	if s.mode == "after-verdict" && err == nil && !s.hit {
		s.hit = true
		return errors.New("explicit crash after immutable replacement verdict")
	}
	return err
}
func (s *replacementMailSeam) CreateClearDevComplexExecutionDispatch(ctx context.Context, c core.CreateComplexExecutionDispatchCommand) (core.ComplexExecutionDispatch, bool, error) {
	out, created, err := s.Store.CreateClearDevComplexExecutionDispatch(ctx, c)
	if c.Dispatch.Round > 0 && s.mode == "after-repair-dispatch" && err == nil && !s.hit {
		s.hit = true
		return out, created, errors.New("explicit crash after repair dispatch")
	}
	return out, created, err
}

func attachReplacementSeam(f *autoExecutionFixture, seam *replacementMailSeam) {
	f.service.complexExecution = seam
	f.service.attempts = seam
}
func reopenReplacement(t *testing.T, f *autoExecutionFixture, h *replacementMailHarness) {
	t.Helper()
	f.reopen(t)
	f.service.chat, f.service.checks, f.service.inspector, f.service.sessions = h, h, h, h
}

func TestMailReplacementRestartBoundariesPreserveExactEffects(t *testing.T) {
	for _, mode := range []string{"after-request", "after-receipt", "after-followup", "after-send", "before-receipt", "after-verdict", "after-repair-dispatch"} {
		t.Run(mode, func(t *testing.T) {
			f, h := newReplacementMailFixture(t, true, true)
			seam := &replacementMailSeam{Store: f.store, mode: mode}
			attachReplacementSeam(f, seam)
			f.confirm(t)
			if !seam.hit {
				t.Fatal("crash boundary not reached")
			}
			assertMailNotCompleted(t, f)
			reopenReplacement(t, f, h)
			e := driveReplacementMail(t, f)
			if len(e.Reviews) != 2 || len(e.Dispatches) != 2 || len(h.checks) != 4 || len(h.reports) != 2 || f.counts().spawns != 6 {
				t.Fatal("restart duplicated or skipped work")
			}
			for key, n := range h.relayCalls {
				if n != 1 {
					t.Fatalf("restart repeated relay %s: %d", key, n)
				}
			}
			if e.Reviews[0].Verdict != "" || e.FixedRecoveries[0].ReviewResult.Verdict != core.LocalReviewRework || e.Reviews[1].Verdict != core.LocalReviewPass {
				t.Fatal("old and new review outcomes confused")
			}
		})
	}
}

func TestMailReplacementStorageRejectsForeignOrMissingSources(t *testing.T) {
	for _, mode := range []string{"wrong-recovery", "wrong-attempt", "wrong-result", "wrong-candidate", "missing-source", "receipt-sha", "verdict-original-request", "verdict-other-review", "verdict-other-recovery", "verdict-old-attempt", "verdict-waive-rework"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newReplacementMailFixture(t, true, true)
			seam := &replacementMailSeam{Store: f.store, mode: mode}
			attachReplacementSeam(f, seam)
			f.confirm(t)
			if !seam.hit || seam.rejected == nil {
				t.Fatalf("storage did not reject %s", mode)
			}
			assertMailNotCompleted(t, f)
			f.reopen(t)
			assertMailNotCompleted(t, f)
		})
	}
}

func TestMailReplacementRequestedCheckFailuresCannotBecomePass(t *testing.T) {
	for _, mode := range []string{"fail", "infra", "stale", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			f, h := newReplacementMailFixture(t, true, false)
			h.mode = mode
			f.confirm(t)
			assertMailNotCompleted(t, f)
			if len(h.reports) != 1 {
				t.Fatal("failed check report was not returned to the replacement")
			}
			e := boundedExecution(t, f)
			if e.FixedRecoveries[0].ReviewResult != nil {
				t.Fatal("failed requested checks produced a replacement PASS")
			}
			counts := f.counts()
			reopenReplacement(t, f, h)
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			if f.counts() != counts {
				t.Fatal("check failure triggered automatic repeated work")
			}
		})
	}
}

func TestMailReplacementConcurrentRecoveryDoesNotRepeatRequests(t *testing.T) {
	f, h := newReplacementMailFixture(t, true, true)
	seam := &replacementMailSeam{Store: f.store, mode: "after-request"}
	attachReplacementSeam(f, seam)
	f.confirm(t)
	if !seam.hit {
		t.Fatal("request not reached")
	}
	reopenReplacement(t, f, h)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = f.service.ResumeComplexStandardExecutions(context.Background())
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	e := driveReplacementMail(t, f)
	if len(e.Reviews) != 2 || len(h.checks) != 4 || f.counts().spawns != 6 {
		t.Fatal("concurrency duplicated work")
	}
	for key, n := range h.relayCalls {
		if n != 1 {
			t.Fatalf("duplicate relay %s: %d", key, n)
		}
	}
}

func TestMailReplacementUnknownDeliveryAndCancellationStayStopped(t *testing.T) {
	for _, mode := range []string{"unknown", "cancelled", "direction"} {
		t.Run(mode, func(t *testing.T) {
			f, h := newReplacementMailFixture(t, true, false)
			seam := &replacementMailSeam{Store: f.store, mode: "before-receipt"}
			attachReplacementSeam(f, seam)
			f.confirm(t)
			if !seam.hit {
				t.Fatal("reply boundary not reached")
			}
			reopenReplacement(t, f, h)
			switch mode {
			case "unknown":
				h.mode = "unknown-receipt"
			case "cancelled":
				if err := f.service.CancelRequirement(context.Background(), f.view.Requirement.ID, "explicit test cancellation"); err != nil {
					t.Fatal(err)
				}
			case "direction":
				f.service.runBackground = func(func()) {}
				if _, err := f.service.ProposeDirectionIntent(context.Background(), f.view.Requirement.ID, ProposeDirectionIntentInput{RequestID: "replacement-stop", DevelopmentRequirementID: f.view.Requirement.ID, Message: directionMessage}); err != nil {
					t.Fatal(err)
				}
				f.service.runBackground = func(run func()) { run() }
			}
			before := f.counts()
			budget, err := f.store.GetClearDevMessageBudget(context.Background(), f.view.Requirement.ID)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			assertMailNotCompleted(t, f)
			afterBudget, err := f.store.GetClearDevMessageBudget(context.Background(), f.view.Requirement.ID)
			if err != nil {
				t.Fatal(err)
			}
			if f.counts() != before || !reflect.DeepEqual(budget, afterBudget) {
				t.Fatal("stopped replacement resent or changed budget")
			}
		})
	}
}

func TestMailReplacementWhitespaceSummaryKeepsExactSource(t *testing.T) {
	f, h := newReplacementMailFixture(t, true, true)
	h.whitespaceSummary = true
	f.confirm(t)
	driveReplacementMail(t, f)
}

func TestMailReplacementRepairAndGrantRetainFiniteBudget(t *testing.T) {
	f, h := newReplacementMailFixture(t, true, true)
	h.repairLimit = 2
	f.confirm(t)
	before := boundedExecution(t, f)
	assertMailNotCompleted(t, f)
	if len(before.Dispatches) != 2 || before.Tasks[0].Status != core.DevelopmentTaskStatusNeedsHuman {
		t.Fatal("replacement second REWORK did not exhaust automatic repair")
	}
	applyFakeDesktopDecision(t, f.store, f.service, f.clock, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt, core.HumanDecisionApprove)
	e := driveReplacementMail(t, f)
	if len(e.Dispatches) != 3 || len(e.Reviews) != 3 || len(h.checks) != 6 || len(h.reports) != 3 || f.counts().spawns != 6 {
		t.Fatal("replacement grant combination lost finite counts")
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), e.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 3 || slots[1].Kind != core.MailAttemptReviewRepair || slots[2].Kind != core.MailAttemptHumanExtra {
		t.Fatal("replacement rework misclassified as ordinary development")
	}
	for _, id := range h.sessions {
		if id != h.replacementSession {
			t.Fatal("grant lost replacement reviewer context")
		}
	}
}

func TestMailReplacementAfterHumanExtraCannotOpenRepair(t *testing.T) {
	f, h := newReplacementMailFixture(t, true, true)
	h.developmentFailures = 3
	f.confirm(t)
	applyFakeDesktopDecision(t, f.store, f.service, f.clock, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt, core.HumanDecisionApprove)
	e := boundedExecution(t, f)
	assertMailNotCompleted(t, f)
	if len(e.Dispatches) != 4 || e.Dispatches[3].ReasonCode != core.MailAttemptFinalLimitReason || e.FixedRecoveries[0].ReviewResult == nil {
		t.Fatalf("replacement escaped final grant boundary: %+v", e.Dispatches)
	}
	counts := f.counts()
	reopenReplacement(t, f, h)
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.counts() != counts {
		t.Fatal("exhausted replacement restarted business work")
	}
}

func TestMailReplacementProgressSelectsActualFollowup(t *testing.T) {
	f, h := newReplacementMailFixture(t, true, false)
	seam := &replacementMailSeam{Store: f.store, mode: "after-followup"}
	attachReplacementSeam(f, seam)
	f.confirm(t)
	if !seam.hit {
		t.Fatal("followup not reached")
	}
	e := boundedExecution(t, f)
	view, err := f.service.GetRequirement(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, work := range view.TrustedProgress.ControlledWork {
		if work.LogicalStepID == e.Reviews[0].AgentStepID {
			t.Fatal("progress still selected failed original request")
		}
		if work.LogicalStepID == core.ReviewCheckFollowupID(e.Reviews[0].ID) && work.AOSessionID == string(h.replacementSession) {
			found = true
		}
	}
	if !found {
		t.Fatalf("actual replacement result return missing from trusted progress: %+v", view.TrustedProgress.ControlledWork)
	}
}

type replacementStalePass struct {
	*sqlite.Store
	old      core.FixedRecoveryEvidence
	rejected error
}

func (s *replacementStalePass) VerifyClearDevComplexExecutionCandidate(ctx context.Context, c core.VerifyComplexExecutionCandidateCommand) error {
	if c.Verification.ReplacementRecoveryID != "" {
		c.Verification.LocalReviewID = s.old.Request.ReviewID
		c.Verification.ReplacementRecoveryID = s.old.Request.ID
		c.Verification.ReplacementAttemptID = s.old.ReviewResult.AttemptID
		c.Verification.ReplacementResultID = s.old.ReviewResult.ResultID
	}
	err := s.Store.VerifyClearDevComplexExecutionCandidate(ctx, c)
	s.rejected = err
	return err
}
func TestMailReplacementCannotBorrowAnotherStoredPass(t *testing.T) {
	f, h := newReplacementMailFixture(t, true, false)
	f.confirm(t)
	old := driveReplacementMail(t, f)
	if old.FixedRecoveries[0].ReviewResult.Verdict != core.LocalReviewPass {
		t.Fatal("first stored replacement PASS missing")
	}
	h.mu.Lock()
	h.currentCandidate = ""
	h.failed = false
	h.mu.Unlock()
	f.view = createComplexUntilClarification(t, f.service)
	f.view = answerComplexQuestions(t, f.service, f.view)
	seam := &replacementStalePass{Store: f.store, old: old.FixedRecoveries[0]}
	f.service.complexExecution = seam
	f.confirm(t)
	if seam.rejected == nil {
		t.Fatal("actual stored foreign PASS did not reach rejection")
	}
	assertMailNotCompleted(t, f)
}

func TestMailReplacementDowngradePreservesAllEvidence(t *testing.T) {
	f, _ := newReplacementMailFixture(t, true, true)
	f.confirm(t)
	before := driveReplacementMail(t, f)
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(context.Background(), 133); err == nil {
		t.Fatal("downgrade discarded replacement continuation authority")
	}
	if after := boundedExecution(t, f); !reflect.DeepEqual(before, after) {
		t.Fatal("refused downgrade changed facts")
	}
}
