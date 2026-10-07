package cleardev

import (
	"context"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestClearDevFixedRecoveryExitedComplexAttemptWaitsForFixedAction(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	service, attempts, ao, _, _, step, id := recoveryFixture(now)
	step.Kind = core.ComplexExecutionAgentStepBuilderTask
	first, err := service.ensureAgentAttempt(ctx, id, core.AgentStepCategoryComplexExecution, step, "session-1", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := attempts.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: "failed", AttemptID: first.ID, Status: core.AgentAttemptFailed, ClientMessageID: first.ClientMessageID, TurnID: "turn", TurnState: domain.TurnStateFailed, FailureCategory: domain.AgentFailureProviderUnavailable, Retryable: true, RecordedAt: now}); err != nil {
		t.Fatal(err)
	}
	ao.setActivity(domain.ActivityExited)
	calls := 0
	service.recoverAgentSession = func(context.Context, domain.SessionID) error {
		calls++
		ao.setActivity(domain.ActivityIdle)
		return nil
	}
	_, err = service.recoverRequirementAgentAttempts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	views, err := attempts.ListClearDevAgentStepAttemptStates(ctx, id, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(views) != 1 {
		t.Fatalf("automatic path consumed fixed recovery: restore calls=%d attempts=%d", calls, len(views))
	}
}
