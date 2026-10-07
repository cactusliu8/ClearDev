package cleardev

import (
	"context"
	"errors"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// Capture the original records after the replacement is authorized but before
// its verdict/failure is handled. Never manufacture terminal database states.
type originalMailReviewHistory struct {
	review  core.ComplexExecutionReview
	step    core.AgentStep
	binding core.ComplexExecutionRoleBinding
}

type replacementHistoryStore struct {
	*sqlite.Store
	requirementID string
	original      *originalMailReviewHistory
}

func readOriginalMailReviewHistory(ctx context.Context, store *sqlite.Store, requirementID, reviewID string) (originalMailReviewHistory, error) {
	e, found, err := store.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil {
		return originalMailReviewHistory{}, err
	}
	if !found {
		return originalMailReviewHistory{}, errors.New("original execution missing")
	}
	for _, review := range e.Reviews {
		if review.ID != reviewID {
			continue
		}
		step, stepFound := complexExecutionStepByID(e, review.AgentStepID)
		binding, bindingFound := complexExecutionBindingByID(e, review.ReviewerRoleBindingID)
		if !stepFound || !bindingFound {
			return originalMailReviewHistory{}, errors.New("original step or binding missing")
		}
		return originalMailReviewHistory{review: review, step: step, binding: binding}, nil
	}
	return originalMailReviewHistory{}, errors.New("original review missing")
}

func (s *replacementHistoryStore) capture(ctx context.Context, reviewID string) error {
	if s.original != nil {
		return nil
	}
	history, err := readOriginalMailReviewHistory(ctx, s.Store, s.requirementID, reviewID)
	if err != nil {
		return err
	}
	s.original = &history
	return nil
}

func (s *replacementHistoryStore) RequestClearDevReviewerChecks(ctx context.Context, request core.ReviewCheckRequest) error {
	if request.ReplacementRecoveryID != "" {
		if err := s.capture(ctx, request.ReviewID); err != nil {
			return err
		}
	}
	return s.Store.RequestClearDevReviewerChecks(ctx, request)
}

func (s *replacementHistoryStore) RecordClearDevReplacementReviewResult(ctx context.Context, result core.ReplacementReviewResult) error {
	if err := s.capture(ctx, result.OriginalReviewID); err != nil {
		return err
	}
	return s.Store.RecordClearDevReplacementReviewResult(ctx, result)
}

func assertOriginalMailReviewHistory(t *testing.T, f *autoExecutionFixture, before *originalMailReviewHistory) {
	t.Helper()
	if before == nil {
		t.Fatal("original history capture was not reached")
	}
	after, err := readOriginalMailReviewHistory(context.Background(), f.store, f.view.Requirement.ID, before.review.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.review, after.review) {
		t.Errorf("replacement changed original review: status %s -> %s, reason %s -> %s", before.review.Status, after.review.Status, before.review.ReasonCode, after.review.ReasonCode)
	}
	if !reflect.DeepEqual(before.step, after.step) {
		t.Errorf("replacement changed original step: status %s -> %s, reason %s -> %s", before.step.SendStatus, after.step.SendStatus, before.step.ReasonCode, after.step.ReasonCode)
	}
	if !reflect.DeepEqual(before.binding, after.binding) {
		t.Error("replacement changed original role binding")
	}
}

func TestMailReplacementPreservesOriginalHistoryAcrossOutcomes(t *testing.T) {
	for _, mode := range []string{"direct-pass", "checks-pass", "direct-rework", "checks-rework", "checks-fail", "checks-infra"} {
		t.Run(mode, func(t *testing.T) {
			checks := mode != "direct-pass" && mode != "direct-rework"
			repair := mode == "direct-rework" || mode == "checks-rework"
			failure := mode == "checks-fail" || mode == "checks-infra"
			f, h := newReplacementMailFixture(t, checks, repair)
			seam := &replacementHistoryStore{Store: f.store, requirementID: f.view.Requirement.ID}
			f.service.complexExecution = seam
			switch mode {
			case "checks-fail":
				h.mode = "fail"
			case "checks-infra":
				h.mode = "infra"
			}
			f.confirm(t)
			if !failure {
				driveReplacementMail(t, f)
			} else {
				assertMailNotCompleted(t, f)
				view := mustGetComplex(t, f.service, f.view.Requirement.ID)
				if view.TrustedProgress.Phase != core.TrustedPhaseBlocked && view.TrustedProgress.Phase != core.TrustedPhaseNeedsHuman {
					phase, reason := core.DeriveComplexExecutionPhase(boundedExecution(t, f))
					t.Errorf("replacement failure is not visibly stopped: trusted=%s execution=%s reason=%s", view.TrustedProgress.Phase, phase, reason)
				}
			}
			assertOriginalMailReviewHistory(t, f, seam.original)
			counts := f.counts()
			reopenReplacement(t, f, h)
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertOriginalMailReviewHistory(t, f, seam.original)
			if counts != f.counts() {
				t.Fatal("history-preserving restart repeated external work")
			}
		})
	}
}
