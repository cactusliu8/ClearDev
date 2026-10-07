package cleardev_test

import (
	"context"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type controlledAttemptReads struct {
	*sqlite.Store
	full, light  int
	afterEvent   func(core.AgentAttemptEvent)
	beforeEvent  func(*core.AgentAttemptEvent)
	afterConfirm func(core.AgentAttemptEvent)
}

func (s *controlledAttemptReads) ListClearDevAgentStepAttempts(ctx context.Context, id string) ([]core.AgentStepAttemptView, error) {
	s.full++
	return s.Store.ListClearDevAgentStepAttempts(ctx, id)
}
func (s *controlledAttemptReads) ListClearDevAgentStepAttemptStates(ctx context.Context, id, step string) ([]core.AgentStepAttemptView, error) {
	s.light++
	return s.Store.ListClearDevAgentStepAttemptStates(ctx, id, step)
}
func (s *controlledAttemptReads) EnsureClearDevAgentAttemptEvent(ctx context.Context, e core.AgentAttemptEvent) (bool, error) {
	if s.beforeEvent != nil {
		s.beforeEvent(&e)
	}
	ok, err := s.Store.EnsureClearDevAgentAttemptEvent(ctx, e)
	if err == nil && ok && s.afterEvent != nil {
		s.afterEvent(e)
	}
	return ok, err
}
func (s *controlledAttemptReads) RecordClearDevAgentAttemptEvent(ctx context.Context, e core.AgentAttemptEvent) error {
	if s.beforeEvent != nil {
		s.beforeEvent(&e)
	}
	err := s.Store.RecordClearDevAgentAttemptEvent(ctx, e)
	if err == nil && s.afterEvent != nil {
		s.afterEvent(e)
	}
	return err
}

func TestClearDevControlledProgressWaitThenActualSecondSend(t *testing.T) {
	store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	deps.Clock = func() time.Time { return now }
	reads := &controlledAttemptReads{Store: store}
	deps.AgentAttempts = reads
	svc := cleardevsvc.New(deps)
	seen := false
	reads.beforeEvent = func(e *core.AgentAttemptEvent) {
		if !seen && e.Status == core.AgentAttemptFailed {
			at := now.Add(time.Hour)
			e.RetryAt = &at
		}
	}
	reads.afterEvent = func(e core.AgentAttemptEvent) {
		if seen || e.Status != core.AgentAttemptFailed {
			return
		}
		seen = true
		view, err := svc.GetRequirement(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, w := range view.TrustedProgress.ControlledWork {
			if w.EvidenceID == e.ID && w.State == "WAITING_RETRY" {
				found = true
			}
		}
		if !found {
			t.Fatalf("failure has no wait: %+v", view.TrustedProgress)
		}
		before := reads.full
		project, err := svc.ListProjectProgress(context.Background(), view.Requirement.AOProjectID)
		if err != nil {
			t.Fatal(err)
		}
		if reads.full != before || reads.light == 0 {
			t.Fatal("project used full history or omitted targeted states")
		}
		if project.Requirements[0].FactSummarySHA256 != view.TrustedProgress.FactSummarySHA256 {
			t.Fatal("hash mismatch")
		}
		now = now.Add(time.Minute)
		again, err := svc.GetRequirement(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if again.TrustedProgress.FactSummarySHA256 != view.TrustedProgress.FactSummarySHA256 {
			t.Fatal("waiting countdown staled explanation")
		}
		sends := h.sends
		now = *e.RetryAt
		eligible, err := svc.GetRequirement(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		found = false
		for _, w := range eligible.TrustedProgress.ControlledWork {
			if w.EvidenceID == e.ID && w.State == "RETRY_ELIGIBLE" && w.AttemptNumber != nil && *w.AttemptNumber == 1 {
				found = true
			}
		}
		if !found || h.sends != sends || eligible.TrustedProgress.FactSummarySHA256 == view.TrustedProgress.FactSummarySHA256 {
			t.Fatal("eligibility changed attempt or failed to change hash")
		}
		t.Logf("reliable first failure: waiting and eligible with unchanged actual sends=%d", sends)
	}
	for wake := 0; wake < 6; wake++ {
		if _, err := svc.StartComplexStandardExecution(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		e, _, _ := store.GetClearDevComplexExecution(context.Background(), id)
		if e.Run.CompletedAt != nil {
			break
		}
	}
	if !seen || h.restores != 1 || h.sends != 7 {
		t.Fatalf("wait/main path missing seen=%v restores=%d sends=%d", seen, h.restores, h.sends)
	}
	view, err := svc.GetRequirement(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if view.TrustedProgress.Phase != core.TrustedPhaseCompleted {
		t.Fatalf("not complete: %+v", view.TrustedProgress)
	}
}

type controlledMovingFacts struct {
	*sqlite.Store
	calls int
}

func (s *controlledMovingFacts) GetClearDevRequirement(ctx context.Context, id string) (core.RequirementSnapshot, bool, error) {
	v, ok, err := s.Store.GetClearDevRequirement(ctx, id)
	s.calls++
	if s.calls == 2 {
		for i := range v.RequirementVersions {
			if v.RequirementVersions[i].Status == core.RequirementVersionStatusConfirmed {
				v.RequirementVersions[i].TaskSetVersion++
			}
		}
	}
	return v, ok, err
}
func TestClearDevControlledProgressRejectsReadTimeBindingChange(t *testing.T) {
	store, _, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
	deps.Facts = &controlledMovingFacts{Store: store}
	v, err := cleardevsvc.New(deps).GetRequirement(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if v.TrustedProgress.Phase != core.TrustedPhaseUnknown || v.TrustedProgress.ReasonCode != "CURRENT_BINDINGS_CHANGED" {
		t.Fatal("mixed binding returned an actionable result")
	}
}

func (s *controlledAttemptReads) ConfirmClearDevAgentMessage(ctx context.Context, e core.AgentAttemptEvent) error {
	err := s.Store.ConfirmClearDevAgentMessage(ctx, e)
	if err == nil && s.afterConfirm != nil {
		s.afterConfirm(e)
	}
	return err
}
