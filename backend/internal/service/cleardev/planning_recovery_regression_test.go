package cleardev

import (
	"context"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPlanningRecoveryRejectsLaterAppendedEvidenceRegardlessOfClock(t *testing.T) {
	for _, mode := range []string{"clock-backwards", "same-timestamp"} {
		t.Run(mode, func(t *testing.T) {
			s, h, store, child, step := interruptedProductReviewFixture(t)
			ctx := context.Background()
			result := planningRecoveryOffer(t, s, store, child)
			first, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 1))
			if err != nil {
				t.Fatal(err)
			}
			at := *first.LastObservedAt
			status, category := core.AgentAttemptInterrupted, domain.AgentFailureTurnInterrupted
			if mode == "clock-backwards" {
				at = at.Add(-time.Hour)
				status, category = core.AgentAttemptDeliveryUnknown, domain.AgentFailureDeliveryUnknown
			}
			event := core.AgentAttemptEvent{ID: "000-newer-appended-event", AttemptID: first.ID, Status: status, ClientMessageID: first.ClientMessageID, PromptSHA256: first.PromptSHA256, TurnID: first.TurnID, TurnState: first.TurnState, FailureCategory: category, RecordedAt: at}
			if err := store.RecordClearDevAgentAttemptEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
			latest, err := store.GetClearDevAgentAttemptState(ctx, child, first.ID)
			if err != nil || latest.LastEventID != event.ID {
				t.Fatalf("store latest event=%+v %v", latest, err)
			}
			calls := len(h.relays)
			if err := s.ApplyHumanDecisionResult(ctx, result); err == nil {
				t.Fatal("stale recovery authorization accepted after a newer appended event")
			}
			if len(h.relays) != calls {
				t.Fatal("stale grant sent a message")
			}
			if _, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 2)); err == nil {
				t.Fatal("stale grant created attempt2")
			}
		})
	}
}

func TestPlanningRecoveryPreflightBeforeSendSurvivesNewService(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "new-service"}[restart], func(t *testing.T) {
			s, h, store, child, step := interruptedProductReviewFixture(t)
			ctx := context.Background()
			h.interrupt = false
			result := planningRecoveryOffer(t, s, store, child)
			checker := &scriptedControlledPreflight{fn: func(string) (ports.ChatControlledPreflight, error) {
				return livePreflightCatalog([]string{"gpt-5.6-terra"}, "gpt-5.6-terra"), ports.ErrChatQuotaExhausted
			}}
			var queued []func()
			enqueue := func(run func()) { queued = append(queued, run) }
			s.preflightChecker = checker
			s.runBackground = enqueue
			if err := s.ApplyHumanDecisionResult(ctx, result); err != nil {
				t.Fatal(err)
			}
			if restart {
				// The old worker never ran: a fresh service must discover the committed grant.
				queued = nil
				s = New(Deps{Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store, ParseCorrections: store, AgentAttempts: store, ControlledPreflights: store, ControlledPreflightChecker: checker, ProgressExplanations: store, Workspace: gitWorkspaceObserver{}, RecoverAgentSession: s.recoverAgentSession, AO: store, Sessions: h, Chat: h, Inspector: h, Checks: h, Human: allowStandardHuman{}, StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: s.newID, Clock: s.now, BackgroundContext: ctx, RunBackground: enqueue})
				if err := s.ResumeComplexFlows(ctx); err != nil {
					t.Fatal(err)
				}
			}
			calls := len(h.relays)
			jobs := queued
			queued = nil
			for _, job := range jobs {
				job()
			}
			if checker.ncalls() == 0 || len(h.relays) != calls {
				t.Fatalf("preflight calls=%d relays=%d -> %d", checker.ncalls(), calls, len(h.relays))
			}
			pending, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 2))
			if err != nil || pending.SendStatus != core.AgentAttemptPending || pending.LastEventID != "" {
				t.Fatalf("preflight consumed retry/message reservation: %+v %v", pending, err)
			}
			checker.fn = func(string) (ports.ChatControlledPreflight, error) {
				return livePreflightCatalog([]string{"gpt-5.6-terra"}, "gpt-5.6-terra"), nil
			}
			if err := s.ResumeComplexFlows(ctx); err != nil {
				t.Fatal(err)
			}
			jobs = queued
			queued = nil
			for _, job := range jobs {
				job()
			}
			view := mustGetComplex(t, s, child)
			if len(view.ComplexPlanning.Reviews) != 1 || len(h.relays) != calls+1 {
				t.Fatalf("cleared preflight did not reuse pending attempt: phase=%s reviews=%d relays=%d", view.ComplexPlanning.Phase, len(view.ComplexPlanning.Reviews), len(h.relays))
			}
		})
	}
}
