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

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type reviewerReceiptFault struct {
	*sqlite.Store
	mode          string
	requirementID string
	rejected      error
	hit           bool
}

func (s *reviewerReceiptFault) RecordClearDevReviewerCheck(ctx context.Context, receipt core.ReviewCheckEvidence) error {
	switch s.mode {
	case "sha":
		receipt.Proof.CandidateSHA = forty("f")
	case "argv":
		receipt.Proof.Argv = []string{"sh", "-c", "true"}
	case "unrequested":
		receipt.CheckID = "demo-integration"
	case "false-pass":
		receipt.Proof.ExitCode = 1
	case "foreign-review":
		receipt.ReviewID = "another-review"
	case "source":
		receipt.Proof.SourceTreeOID = forty("f")
	case "image":
		receipt.Proof.ImageID = "sha256:other"
	}
	err := s.Store.RecordClearDevReviewerCheck(ctx, receipt)
	if err != nil {
		s.rejected = err
	}
	return err
}
func (s *reviewerReceiptFault) RequestClearDevReviewerChecks(ctx context.Context, request core.ReviewCheckRequest) error {
	if err := s.Store.RequestClearDevReviewerChecks(ctx, request); err != nil {
		return err
	}
	if s.mode == "premature-pass" {
		e, _, err := s.GetClearDevComplexExecution(ctx, s.requirementID)
		if err != nil {
			return err
		}
		step, _ := complexExecutionStepByID(e, request.RequestStepID)
		_, s.rejected = s.SettleClearDevComplexExecutionReview(ctx, core.SettleComplexExecutionReviewCommand{ReviewID: request.ReviewID, TurnID: step.TurnID, FinalMessageID: step.FinalMessageID, Verdict: core.LocalReviewPass, ReasonCode: "REVIEW_PASSED", Summary: "not a real final review", At: request.RequestedAt})
		return errors.New("stop after premature verdict probe")
	}
	return nil
}
func (s *reviewerReceiptFault) ConfirmClearDevAgentMessage(ctx context.Context, event core.AgentAttemptEvent) error {
	if s.mode == "before-receipt" && strings.HasPrefix(event.ClientMessageID, "cleardev-review-check-results-") && !s.hit {
		s.hit = true
		return errors.New("crash before reply receipt")
	}
	return s.Store.ConfirmClearDevAgentMessage(ctx, event)
}
func (s *reviewerReceiptFault) CompleteClearDevComplexExecution(ctx context.Context, c core.CompleteComplexExecutionCommand) error {
	err := s.Store.CompleteClearDevComplexExecution(ctx, c)
	if err != nil {
		s.rejected = err
	}
	return err
}

func TestReviewerRequestedChecksStorageRejectsForgedAndMissingEvidence(t *testing.T) {
	for _, mode := range []string{"sha", "argv", "unrequested", "false-pass", "foreign-review", "source", "image", "premature-pass"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newRequestingReviewer(t)
			fault := &reviewerReceiptFault{Store: f.store, mode: mode, requirementID: f.view.Requirement.ID}
			f.service.complexExecution = fault
			f.confirm(t)
			if fault.rejected == nil {
				t.Fatal("storage did not reject corrupt evidence")
			}
			assertMailNotCompleted(t, f)
			f.reopen(t)
			assertMailNotCompleted(t, f)
		})
	}
}

func TestReviewerRequestedChecksReceiptLossDoesNotResend(t *testing.T) {
	f, h := newRequestingReviewer(t)
	fault := &reviewerReceiptFault{Store: f.store, mode: "before-receipt"}
	f.service.attempts = fault
	f.confirm(t)
	if !fault.hit {
		t.Fatal("receipt boundary not hit")
	}
	assertMailNotCompleted(t, f)
	f.reopen(t)
	f.service.chat, f.service.checks, f.service.inspector = h, h, h.mailFlowChecks
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.completed(t)
	if len(h.sessions) != 2 || len(h.checks) != 2 {
		t.Fatal("receipt recovery duplicated external action")
	}
}

func TestReviewerRequestedChecksConcurrentResumeIsIdempotent(t *testing.T) {
	f, h := newRequestingReviewer(t)
	fault := &reviewerCheckFaultStore{ComplexExecutionFactStore: f.store, ReviewerCheckStore: f.store, mode: "after-result"}
	f.service.complexExecution = fault
	f.confirm(t)
	if !fault.hit {
		t.Fatal("not paused")
	}
	f.reopen(t)
	f.service.chat, f.service.checks, f.service.inspector = h, h, h.mailFlowChecks
	var mu sync.Mutex
	queue := []func(){}
	f.service.runBackground = func(run func()) { mu.Lock(); queue = append(queue, run); mu.Unlock() }
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
	for turns := 0; ; turns++ {
		mu.Lock()
		if len(queue) == 0 {
			mu.Unlock()
			break
		}
		run := queue[0]
		queue = queue[1:]
		mu.Unlock()
		if turns > 8 {
			t.Fatal("unbounded scheduling")
		}
		run()
	}
	f.completed(t)
	if len(h.sessions) != 2 || len(h.checks) != 2 || f.counts().spawns != 4 {
		t.Fatal("duplicate reviewer actions")
	}
}

func TestReviewerRequestedChecksUnknownDeliveryKeepsBudget(t *testing.T) {
	f, h := newRequestingReviewer(t)
	h.mode = "unknown-receipt"
	fault := &reviewerReceiptFault{Store: f.store, mode: "before-receipt"}
	f.service.attempts = fault
	f.confirm(t)
	if !fault.hit {
		t.Fatal("receipt boundary not hit")
	}
	before, err := f.store.GetClearDevMessageBudget(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		f.reopen(t)
		f.service.chat, f.service.checks, f.service.inspector = h, h, h
		if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertMailNotCompleted(t, f)
	}
	after, err := f.store.GetClearDevMessageBudget(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.ReservedMessages, after.ReservedMessages) || !reflect.DeepEqual(before.ConfirmedSentMessages, after.ConfirmedSentMessages) || len(h.sessions) != 2 || len(h.checks) != 2 {
		t.Fatal("unknown delivery reset budget or resent results")
	}
}

func TestReviewerRequestedChecksDoNotIncreaseRoleBudget(t *testing.T) {
	f, h := newRequestingReviewer(t)
	h.reviewVerdicts = []string{"PASS", "REWORK"}
	f.confirm(t)
	execution, _, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Two full review rounds — each with a requested-check round trip — still
	// fit the two-turn reviewer budget: a round's follow-up shares the round's
	// turn instead of spending the next round's.
	if phase, reason := core.DeriveComplexExecutionPhase(execution); phase != core.ComplexExecutionCompleted {
		t.Fatalf("round turn accounting did not complete both review rounds: phase=%s reason=%s", phase, reason)
	}
	if len(execution.Reviews) != 2 || len(execution.Dispatches) != 2 {
		t.Fatalf("review rounds=%d dispatches=%d", len(execution.Reviews), len(execution.Dispatches))
	}
	budget, err := f.store.GetClearDevMessageBudget(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, role := range budget.Roles {
		if role.RoleKind != "REVIEWER" {
			continue
		}
		found = true
		if role.MaxSteps == nil || *role.MaxSteps != 2 || role.ReservedSteps == nil || *role.ReservedSteps != 2 {
			t.Fatalf("reviewer turn count changed: %+v", role)
		}
		if role.ConfirmedSentMessages == nil || *role.ConfirmedSentMessages <= *role.ReservedSteps {
			t.Fatalf("check-request round trips are not visible as role messages: %+v", role)
		}
	}
	// Four request/report relays: both review rounds now complete their check
	// round trip; the historical count of two stopped after the first round
	// because the follow-up consumed its turn.
	if !found || len(h.sessions) != 4 || f.counts().spawns != 4 {
		t.Fatalf("reviewer sessions=%d spawns=%d found=%v", len(h.sessions), f.counts().spawns, found)
	}
}

func TestReviewerRequestedChecksDowngradePreservesHistory(t *testing.T) {
	f, _ := newRequestingReviewer(t)
	f.confirm(t)
	before := f.completed(t)
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var beforeVersion int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(context.Background(), 130); err == nil {
		t.Fatal("removed request history")
	}
	var version int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != beforeVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if after := f.completed(t); !reflect.DeepEqual(before, after) {
		t.Fatal("failed downgrade changed facts")
	}
}
