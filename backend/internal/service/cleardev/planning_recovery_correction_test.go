package cleardev

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Produce one malformed original response, then a valid parse correction.
type planningRecoveryCorrectionAgent struct{ *interruptedProductReviewAgent }

func (h *planningRecoveryCorrectionAgent) RelayChatTurnWithID(ctx context.Context, sid domain.SessionID, prompt, client string) (string, error) {
	if strings.HasSuffix(client, ":parse-correction") {
		return h.standardAgentHarness.RelayChatTurnWithID(ctx, sid, prompt, client)
	}
	turn, err := h.interruptedProductReviewAgent.RelayChatTurnWithID(ctx, sid, prompt, client)
	if err == nil && strings.HasSuffix(client, ":attempt:2") {
		h.mu.Lock()
		snapshot := h.snapshots[sid]
		for i := range snapshot.Messages {
			if snapshot.Messages[i].TurnID == turn && snapshot.Messages[i].Role == domain.MessageRoleAssistant {
				snapshot.Messages[i].Text = "{malformed original review"
			}
		}
		h.snapshots[sid] = snapshot
		h.mu.Unlock()
	}
	return turn, err
}

func TestPlanningRecoveryPreflightDoesNotConvertParseCorrectionToTerminalFailure(t *testing.T) {
	s, h, store, child, step := interruptedProductReviewFixture(t)
	ctx := context.Background()
	h.interrupt = false
	s.chat = &planningRecoveryCorrectionAgent{h}
	checks := 0
	checker := &scriptedControlledPreflight{fn: func(string) (ports.ChatControlledPreflight, error) {
		checks++
		result := livePreflightCatalog([]string{"gpt-5.6-terra"}, "gpt-5.6-terra")
		if checks > 1 {
			return result, ports.ErrChatQuotaExhausted
		}
		return result, nil
	}}
	s.preflightChecker = checker
	var queued []func()
	s.runBackground = func(run func()) { queued = append(queued, run) }
	result := planningRecoveryOffer(t, s, store, child)
	before := len(h.relays)
	if err := s.ApplyHumanDecisionResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	jobs := queued
	queued = nil
	for _, job := range jobs {
		job()
	}
	attempt, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 2))
	if err != nil {
		t.Fatal(err)
	}
	view := mustGetComplex(t, s, child)
	if checker.ncalls() != 1 || len(view.ComplexPlanning.Reviews) != 1 || attempt.FailureCategory != "" {
		t.Fatalf("parse correction changed recovery preflight semantics: checks=%d reviews=%d failure=%s state=%s", checker.ncalls(), len(view.ComplexPlanning.Reviews), attempt.FailureCategory, attempt.SendStatus)
	}
	if len(h.relays) != before+2 || h.relays[len(h.relays)-1].clientMessageID != attempt.ClientMessageID+":parse-correction" {
		t.Fatal("expected one original and one existing parse correction")
	}
	if _, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 3)); err == nil {
		t.Fatal("correction created another attempt")
	}
}
