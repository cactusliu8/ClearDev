package cleardev

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// Each injected error represents a process loss AFTER the named durable write,
// except receipt loss, which occurs after the provider accepted the message but
// before its local sent marker. The external provider double survives reopening
// the real on-disk SQLite database; no production state is edited by these tests.
type runtimeCrashStore struct {
	ComplexFactStore
	ComplexExecutionFactStore
	PlannerRuntimeFactStore
	boundary string
	fired    bool
}

func (s *runtimeCrashStore) crash(boundary string, changed bool, err error) error {
	if err == nil && changed && !s.fired && s.boundary == boundary {
		s.fired = true
		return errAutoInjectedCrash
	}
	return err
}

func (s *runtimeCrashStore) SettleClearDevComplexExecutionAgentStep(ctx context.Context, step core.AgentStep) (bool, error) {
	changed, err := s.ComplexExecutionFactStore.SettleClearDevComplexExecutionAgentStep(ctx, step)
	if strings.Contains(step.FinalMessageText, `"coordination"`) {
		err = s.crash("event", changed, err)
	}
	return changed, err
}

func (s *runtimeCrashStore) PrepareClearDevPlannerRuntime(ctx context.Context, eventID string, at time.Time) (core.PlannerCoordinationRequest, bool, error) {
	request, changed, err := s.PlannerRuntimeFactStore.PrepareClearDevPlannerRuntime(ctx, eventID, at)
	return request, changed, s.crash("request", changed, err)
}

func (s *runtimeCrashStore) MarkClearDevComplexAgentStepSent(ctx context.Context, id string, at time.Time) (bool, error) {
	if strings.HasSuffix(id, ":planner") {
		if err := s.crash("receipt", true, nil); err != nil {
			return false, err
		}
	}
	return s.ComplexFactStore.MarkClearDevComplexAgentStepSent(ctx, id, at)
}

func (s *runtimeCrashStore) SettleClearDevComplexAgentStep(ctx context.Context, step core.AgentStep) (bool, error) {
	changed, err := s.ComplexFactStore.SettleClearDevComplexAgentStep(ctx, step)
	if strings.HasSuffix(step.ID, ":planner") {
		err = s.crash("settlement", changed, err)
	}
	return changed, err
}

func (s *runtimeCrashStore) ApplyClearDevPlannerRuntime(ctx context.Context, eventID string, at time.Time) (bool, error) {
	changed, err := s.PlannerRuntimeFactStore.ApplyClearDevPlannerRuntime(ctx, eventID, at)
	return changed, s.crash("application", changed, err)
}

func TestPlannerRuntimeRestartAcrossDurableBoundaries(t *testing.T) {
	for _, boundary := range []string{"event", "request", "receipt", "settlement", "application"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			f, h := newRuntimeFixture(t, "dependency")
			crash := &runtimeCrashStore{ComplexFactStore: f.store, ComplexExecutionFactStore: f.store,
				PlannerRuntimeFactStore: f.store, boundary: boundary}
			f.service.complex, f.service.complexExecution = crash, crash
			f.confirm(t)
			if !crash.fired {
				t.Fatal("requested crash boundary was not reached")
			}
			before, found, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
			if err != nil || !found || before.PlannerRuntime == nil || len(before.PlannerRuntime.Events) != 1 {
				t.Fatalf("event was not durable before crash: found=%v err=%v", found, err)
			}
			f.reopen(t)
			attachRuntimeHarness(f, h)
			// Upgrading/restarting configuration cannot disable the frozen policy.
			f.service.plannerRuntimeCoordination = false
			if err := f.service.ResumeComplexFlows(ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.service.ResumeComplexStandardExecutions(ctx); err != nil {
				t.Fatal(err)
			}
			after, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
			if err != nil {
				t.Fatal(err)
			}
			phase, reason := core.DeriveComplexExecutionPhase(after)
			if phase != core.ComplexExecutionCompleted || after.Run.ID != before.Run.ID || len(after.Dispatches) != 2 ||
				len(after.Reviews) != 2 || after.FinalReview == nil || after.FinalReview.Verdict != "PASS" ||
				len(after.PlannerRuntime.Requests) != 1 || len(after.PlannerRuntime.Decisions) != 1 ||
				len(after.PlannerRuntime.Amendments) != 1 || h.runtimeTurns != 1 {
				t.Fatalf("restart failed or duplicated work: phase=%s reason=%s turns=%d history=%+v", phase, reason, h.runtimeTurns, after.PlannerRuntime)
			}
			if !reflect.DeepEqual(before.PlannerRuntime.Events, after.PlannerRuntime.Events) {
				t.Fatal("restart replaced the original source event")
			}
			if len(before.PlannerRuntime.Requests) != 0 && !reflect.DeepEqual(before.PlannerRuntime.Requests, after.PlannerRuntime.Requests) {
				t.Fatal("restart replaced the request identity, context or consumed round")
			}
			counts := f.counts()
			if err := f.service.ResumeComplexFlows(ctx); err != nil {
				t.Fatal(err)
			}
			if f.counts() != counts {
				t.Fatal("completed replay sent additional provider work")
			}
		})
	}
}

func TestPlannerRuntimeConcurrentRequestAndDecisionReplay(t *testing.T) {
	ctx := context.Background()
	f, h := newRuntimeFixture(t, "dependency")
	h.beforeRuntime = func(ctx context.Context, _ string) error {
		execution, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
		if err != nil {
			return err
		}
		want := execution.PlannerRuntime.Requests[0]
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, changed, err := f.store.PrepareClearDevPlannerRuntime(ctx, want.EventID, f.clock())
				if err != nil || changed || !reflect.DeepEqual(got, want) {
					t.Errorf("concurrent request changed reservation: changed=%v err=%v", changed, err)
				}
			}()
		}
		wg.Wait()
		return nil
	}
	f.confirm(t)
	execution, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if phase, _ := core.DeriveComplexExecutionPhase(execution); phase != core.ComplexExecutionCompleted {
		t.Fatalf("concurrent prepare did not converge: phase=%s", phase)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := f.store.ApplyClearDevPlannerRuntime(ctx, execution.PlannerRuntime.Events[0].ID, f.clock())
			if err != nil || changed {
				t.Errorf("concurrent decision replay applied twice: changed=%v err=%v", changed, err)
			}
		}()
	}
	wg.Wait()
	after, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
	if err != nil || !reflect.DeepEqual(execution, after) || h.runtimeTurns != 1 {
		t.Fatalf("concurrent replay changed durable facts: %v", err)
	}
}

func TestPlannerRuntimeParallelDrainsBeforeCoordination(t *testing.T) {
	f, h := newRuntimeFixture(t, "parallel")
	h.decision = core.PlannerRuntimeContinue
	h.beforeRuntime = func(ctx context.Context, _ string) error {
		execution, _, err := f.store.GetClearDevComplexExecution(ctx, f.view.Requirement.ID)
		if err != nil {
			return err
		}
		if !core.PlannerRuntimeQuiescent(execution) || len(execution.Verifications) != 2 || len(execution.Reviews) != 2 {
			t.Fatalf("parallel Planner called before existing attempts settled: %+v", execution.Dispatches)
		}
		return nil
	}
	f.confirm(t)
	execution, _, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	phase, reason := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionCompleted || execution.Run.FixedBuilderCount != 2 || h.runtimeTurns != 1 || len(execution.PlannerRuntime.Decisions) != 1 {
		t.Fatalf("parallel coordination failed: phase=%s reason=%s history=%+v", phase, reason, execution.PlannerRuntime)
	}
}
