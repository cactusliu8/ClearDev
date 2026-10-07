package cleardev

import (
	"context"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func extraPlanningLaterOffer(t *testing.T, f planningContinuationFixture) core.HumanDecisionOffer {
	t.Helper()
	offer, result := extraPlanningNativeOffer(t, f)
	result.Decision = core.HumanDecisionLater
	if err := f.s.ApplyHumanDecisionResult(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	return offer
}

func TestExtraPlanningAttemptOffersRecheckOriginalSource(t *testing.T) {
	for _, compilation := range []bool{false, true} {
		for _, reopen := range []bool{false, true} {
			name := "discussion/initial"
			if compilation {
				name = "compilation/initial"
			}
			if reopen {
				name += "/registered-reopen"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				f := exhaustedExtraPlanningFixture(t, compilation)
				requestExtraPlanningAttempt(t, f)
				if reopen {
					offer := extraPlanningLaterOffer(t, f)
					history, err := f.store.ListClearDevHumanDecisionDispatchHistory(ctx, offer.RequestID)
					if err != nil || len(history) != 1 {
						t.Fatal("missing original Later display", err)
					}
					if _, err := f.s.ReopenHumanDecision(ctx, f.id, ReopenHumanDecisionInput{RequestID: "extra-source-reopen", DecisionRequestID: offer.RequestID, ContentSHA256: offer.ContentSHA256, PreviousDispatchID: history[0].ID}); err != nil {
						t.Fatal(err)
					}
				}
				state, err := f.store.ReadClearDevExtraPlanningAttempt(ctx, f.id, f.s.now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				requestID := state.DecisionRequestID
				db := extraPlanningRawDB(t, f)
				before := extraPlanningFactCounts(t, db)
				budget, attempts, calls := extraPlanningBudget(t, f), extraPlanningAttempts(t, f), len(f.h.relays)
				f.h.dirty = true
				if reason := f.s.planningRecoveryReady(ctx, extraPlanningSource(state), false); reason != "PLANNING_SOURCE_CHANGED" {
					t.Fatal("fixture source is not stale", reason)
				}
				if _, found, err := f.s.IssueHumanDecisionOffer(ctx, extraPlanningTestDesktop); err != nil || found {
					t.Fatalf("stale original source received a new native display: found=%t err=%v", found, err)
				}
				view, err := f.s.GetHumanDecisionDisplays(ctx, f.id)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range view.Items {
					if item.DecisionRequestID == requestID && (item.CanReopen || item.ReasonCode != "DECISION_NOT_CURRENT") {
						t.Fatal("stale display still offers reopening", item.ReasonCode)
					}
				}
				if option := extraPlanningOption(t, f, extraPlanningContinueAction); option.UnavailableReason != "PLANNING_SOURCE_CHANGED" {
					t.Fatal("pending authority hid the changed original source", option.UnavailableReason)
				}
				if !reflect.DeepEqual(before, extraPlanningFactCounts(t, db)) || !reflect.DeepEqual(budget, extraPlanningBudget(t, f)) || !reflect.DeepEqual(attempts, extraPlanningAttempts(t, f)) || len(f.h.relays) != calls {
					t.Fatal("refused native offer or pure read changed original facts, messages or budget")
				}
				f.h.dirty = false
				offer, found, err := f.s.IssueHumanDecisionOffer(ctx, extraPlanningTestDesktop)
				if err != nil || !found || offer.RequestID != requestID {
					t.Fatalf("restored source did not display the exact saved request: found=%t err=%v", found, err)
				}
				if !reflect.DeepEqual(budget, extraPlanningBudget(t, f)) || !reflect.DeepEqual(attempts, extraPlanningAttempts(t, f)) || len(f.h.relays) != calls {
					t.Fatal("displaying the original saved request continued or consumed an attempt")
				}
			})
		}
	}
}

func TestExtraPlanningAttemptReopenChecksSourceBeforeRegistration(t *testing.T) {
	ctx := context.Background()
	f := exhaustedExtraPlanningFixture(t, true)
	requestExtraPlanningAttempt(t, f)
	offer := extraPlanningLaterOffer(t, f)
	history, err := f.store.ListClearDevHumanDecisionDispatchHistory(ctx, offer.RequestID)
	if err != nil || len(history) != 1 {
		t.Fatal("missing Later display", err)
	}
	input := ReopenHumanDecisionInput{RequestID: "extra-source-registration", DecisionRequestID: offer.RequestID, ContentSHA256: offer.ContentSHA256, PreviousDispatchID: history[0].ID}
	db := extraPlanningRawDB(t, f)
	before := extraPlanningFactCounts(t, db)
	f.h.dirty = true
	if _, err := f.s.ReopenHumanDecision(ctx, f.id, input); err == nil {
		t.Fatal("source changed before registration but a reopen was saved")
	}
	if !reflect.DeepEqual(before, extraPlanningFactCounts(t, db)) {
		t.Fatal("refused registration changed authority or original facts")
	}
	f.h.dirty = false
	if _, err := f.s.ReopenHumanDecision(ctx, f.id, input); err != nil {
		t.Fatal("exact request cannot register after fixing the source", err)
	}
	if _, found, err := f.s.IssueHumanDecisionOffer(ctx, extraPlanningTestDesktop); err != nil || !found {
		t.Fatalf("registered original request is unreachable: found=%t err=%v", found, err)
	}
}
