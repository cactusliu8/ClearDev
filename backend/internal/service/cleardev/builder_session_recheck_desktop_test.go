package cleardev

import (
	"context"
	"errors"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Native UI exercises a failed explicit recheck and a second explicit request.
// The real Service/SQLite keeps one original dispatch; only native/provider/Git
// operations are doubles. This is not a repair of the user's daily task.
func builderSessionRecheckDesktopCase(t *testing.T) *recoveryDesktopCase {
	return newBuilderSessionRecheckDesktopCase(t, false)
}

func newBuilderSessionRecheckDesktopCase(t *testing.T, registered bool) *recoveryDesktopCase {
	t.Helper()
	ctx := context.Background()
	f, h, id, before, _ := newBuilderSessionRecheckFixture(t)
	name, expectedRequests := "recheck", 2
	if registered {
		name, expectedRequests = "registeredRecheck", 1
		f.s.runBackground = func(func()) {}
		input := builderSessionRecheckInput(t, f, id, "native-registered-original-recheck")
		if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
			t.Fatal(err)
		}
		reopenBuilderRecheck(t, f, h)
	}
	restores := 0
	f.s.restoreOriginalAgentSession = func(ctx context.Context, sid domain.SessionID) (string, error) {
		restores++
		if !registered && restores == 1 {
			return "", ports.ErrChatProviderUnavailable
		}
		return restoreTestRecheckBuilder(f)(ctx, sid)
	}
	item := &recoveryDesktopCase{Name: name, ProjectID: "notes-project", RequirementID: id, service: f.s,
		ExpectedHistory: len(before.WorkflowRecoveries) + expectedRequests, calls: func() int { return len(h.relays) }, beforeCalls: len(h.relays)}
	item.advance = func() error {
		for range 120 {
			state, _, err := f.store.GetClearDevComplexExecution(ctx, id)
			if err != nil {
				return err
			}
			phase, _ := core.DeriveComplexExecutionPhase(state)
			if state.Run.CompletedAt != nil || phase == core.ComplexExecutionBlocked || phase == core.ComplexExecutionNeedsHuman {
				return nil
			}
			_, _, err = f.s.advanceComplexStandardExecution(ctx, id)
			if err != nil && !errors.Is(err, errComplexExecutionStopped) {
				return err
			}
		}
		return errors.New("native recheck fixture did not reach a settled observation")
	}
	item.verify = func() error {
		after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
		if err != nil {
			return err
		}
		if restores != expectedRequests || len(after.Dispatches) != len(before.Dispatches) || !reflect.DeepEqual(after.Dispatches[0], before.Dispatches[0]) || after.Dispatches[1].AgentStepID != before.Dispatches[1].AgentStepID || after.Run.CompletedAt == nil || len(item.requests) != expectedRequests {
			return errors.New("explicit recheck lost original work, repeated a restore or failed to continue")
		}
		checks, err := f.store.ListClearDevBuilderSessionChecks(ctx, before.Run.ID)
		if err != nil {
			return err
		}
		failed, ready := registered, false
		for _, check := range checks {
			failed = failed || check.RecoveryID == item.requests[0].RequestID && check.Outcome == "FAILED" && check.Stage == "RESTORE" && check.ReasonCode == "PROVIDER_UNAVAILABLE"
			ready = ready || check.RecoveryID == item.requests[expectedRequests-1].RequestID && check.Outcome == "READY"
		}
		if len(checks) != expectedRequests*2 || !failed || !ready || item.calls()-item.beforeCalls != 3 {
			return errors.New("recheck did not preserve failure and readiness with one Builder/Reviewer/final send")
		}
		for _, b := range after.Exception.Budgets {
			if b.RoleKind == core.ComplexExceptionBudgetBuilder && b.UsedTurns != 2 {
				return errors.New("failed environment recheck changed the original Builder budget")
			}
		}
		return nil
	}
	return item
}
