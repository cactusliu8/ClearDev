package cleardev

import (
	"context"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// Existing model/authority/Git doubles prepare real histories. Reopening never
// grants any budget, sends a model message, or rewrites those histories.
func assertDecisionKindReopens(t *testing.T, s *Service, id, kind string) {
	t.Helper()
	ctx := context.Background()
	if err := s.BackfillHumanDecisionRequests(ctx); err != nil {
		t.Fatal(err)
	}
	requests, err := s.humanDecisions.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var request core.HumanDecisionRequest
	for _, r := range requests {
		if r.DevelopmentRequirementID == id && r.DecisionKind == kind {
			request = r
		}
	}
	if request.ID == "" {
		t.Fatalf("missing original %s decision", kind)
	}
	store := s.humanDecisions.(humanDecisionReopenStore)
	at := s.now().UTC()
	s.desktopRunID = "reopen-kinds"
	old, err := s.humanDecisions.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: request.ID, DesktopRunID: s.desktopRunID, Nonce: mustComplexNonce(t), IssuedAt: at, ExpiresAt: at.Add(core.HumanDecisionOfferTTL)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DismissHumanDecisionDispatch(ctx, core.DismissHumanDecisionDispatchCommand{Nonce: old.Nonce, DesktopRunID: s.desktopRunID, Outcome: core.HumanDecisionDispatchDisconnected, At: at}); err != nil {
		t.Fatal(err)
	}
	before, _, err := s.facts.GetClearDevRequirement(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var executionBefore core.ComplexExecutionSnapshot
	if s.complexExecution != nil {
		executionBefore, _, err = s.complexExecution.GetClearDevComplexExecution(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.ListClearDevHumanDecisionDispatchHistory(ctx, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ReopenHumanDecision(ctx, id, ReopenHumanDecisionInput{RequestID: "reopen-" + kind, DecisionRequestID: request.ID, ContentSHA256: request.ContentSHA256, PreviousDispatchID: history[len(history)-1].ID})
	if err != nil {
		t.Fatal("reopen registration", kind, err)
	}
	var registered bool
	for _, i := range result.Items {
		if i.DecisionRequestID == request.ID && len(i.Reopens) == 1 {
			registered = true
		}
	}
	if !registered {
		t.Fatal("reopen receipt missing")
	}
	offer, ok, err := s.IssueHumanDecisionOffer(ctx, s.desktopRunID)
	if err != nil || !ok || offer.RequestID != request.ID {
		t.Fatalf("exact %s offer unavailable: %v", kind, err)
	}
	if offer.ContentSHA256 != old.ContentSHA256 || string(offer.Binding) != string(old.Binding) || offer.Nonce == old.Nonce {
		t.Fatal("original decision changed")
	}
	after, _, err := s.facts.GetClearDevRequirement(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.RequirementVersions, after.RequirementVersions) {
		t.Fatal("display recovery rewrote original versions")
	}
	if s.complexExecution != nil {
		after, _, err := s.complexExecution.GetClearDevComplexExecution(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(executionBefore, after) {
			t.Fatal("display recovery rewrote execution or budgets")
		}
	}
	if _, found, err := s.humanDecisions.GetClearDevHumanDecisionEffect(ctx, request.ID); err != nil || found {
		t.Fatal("reopen applied an authority effect")
	}
}
func TestHumanDecisionReopenPlanningAndFinalKinds(t *testing.T) {
	t.Run("planning", func(t *testing.T) {
		s, _, _, id, _ := interruptedProductReviewFixture(t)
		assertDecisionKindReopens(t, s, id, core.HumanDecisionKindPlanningRecovery)
	})
	t.Run("final", func(t *testing.T) {
		f, _ := legacyFinalRecheckFixture(t)
		assertDecisionKindReopens(t, f.service, f.view.Requirement.ID, core.HumanDecisionKindFinalReviewRecheck)
	})
	t.Run("mail", func(t *testing.T) {
		f, _ := newBoundedMailFixture(t, 3)
		f.confirm(t)
		assertDecisionKindReopens(t, f.service, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt)
	})
	t.Run("direction", func(t *testing.T) {
		_, _, _, _, s, view := seedDirectionReady(t)
		view = proposeDirection(t, s, view.Requirement.ID, "reopen-direction")
		assertDecisionKindReopens(t, s, view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange)
	})
}
func TestHumanDecisionReopenBudgetKindsPreserveOriginalBudgets(t *testing.T) {
	for _, kind := range []string{core.HumanDecisionKindExtraBuilderTurn, core.HumanDecisionKindExtraReviewBudget} {
		t.Run(kind, func(t *testing.T) {
			f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
			h := &workflowBlockedBuilderHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), block: true}
			h.benchmarkSequentialCandidates = true
			f.s.chat = h
			if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
				t.Fatal(err)
			}
			stopped := stoppedWorkflow(t, f, child.Requirement.ID)
			if kind == core.HumanDecisionKindExtraBuilderTurn {
				if _, err := f.s.RequestExtraBuilderTurn(context.Background(), child.Requirement.ID, stopped.Tasks[0].ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.s.RequestExtraReviewBudget(context.Background(), child.Requirement.ID, stopped.Tasks[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			assertDecisionKindReopens(t, f.s, child.Requirement.ID, kind)
		})
	}
}
func TestHumanDecisionReopenFailsClosedWithoutDesktop(t *testing.T) {
	s, _, store, id, _ := interruptedProductReviewFixture(t)
	if err := s.BackfillHumanDecisionRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
	request := pendingHumanRequestByKind(t, store, id, core.HumanDecisionKindPlanningRecovery)
	if _, err := s.ReopenHumanDecision(context.Background(), id, ReopenHumanDecisionInput{RequestID: "no-desktop", DecisionRequestID: request.ID, ContentSHA256: request.ContentSHA256, PreviousDispatchID: "not-issued"}); err == nil {
		t.Fatal("bare daemon accepted a reopen")
	}
}
