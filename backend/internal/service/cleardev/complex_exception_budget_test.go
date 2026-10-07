package cleardev

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

func TestOccupyExceptionBudgetFailsClosedWithoutFacts(t *testing.T) {
	service := &Service{now: func() time.Time { return time.Unix(1, 0).UTC() }}
	err := service.occupyExceptionBudget(context.Background(), core.ComplexExecutionSnapshot{}, "task", core.ComplexExceptionBudgetBuilder, "", "step")
	if !errors.Is(err, errComplexExecutionStopped) {
		t.Fatalf("empty exception facts err=%v", err)
	}
	err = service.occupyExceptionBudget(context.Background(), core.ComplexExecutionSnapshot{Exception: &core.ComplexExceptionFacts{}}, "task", core.ComplexExceptionBudgetBuilder, "", "step")
	if !errors.Is(err, errComplexExecutionStopped) {
		t.Fatalf("empty budgets err=%v", err)
	}
}

func TestOccupyExceptionBudgetStopsWhenRoleIsMissing(t *testing.T) {
	store := newExceptionBudgetStore(map[string]int{"budget-builder": 1})
	service := &Service{complexExecution: store, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	execution := core.ComplexExecutionSnapshot{Exception: &core.ComplexExceptionFacts{
		Budgets: []core.ComplexExceptionBudget{{ID: "budget-builder", RoleKind: core.ComplexExceptionBudgetBuilder, ComplexExecutionTaskID: "task"}},
	}}
	if err := service.occupyExceptionBudget(context.Background(), execution, "task", core.ComplexExceptionBudgetReviewer, "", "step"); !errors.Is(err, errComplexExecutionStopped) {
		t.Fatalf("missing role err=%v", err)
	}
	if store.calls != 0 {
		t.Fatalf("missing role occupied the store %d times", store.calls)
	}
}

func TestOccupyExceptionBudgetStopsWhenOccupyFails(t *testing.T) {
	store := newExceptionBudgetStore(map[string]int{"budget-builder": 0})
	service := &Service{complexExecution: store, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	execution := core.ComplexExecutionSnapshot{Exception: &core.ComplexExceptionFacts{
		Budgets: []core.ComplexExceptionBudget{{ID: "budget-builder", RoleKind: core.ComplexExceptionBudgetBuilder, ComplexExecutionTaskID: "task"}},
	}}
	if err := service.occupyExceptionBudget(context.Background(), execution, "task", core.ComplexExceptionBudgetBuilder, "", "step"); !errors.Is(err, errComplexExecutionStopped) {
		t.Fatalf("exhausted budget err=%v", err)
	}
}

func TestOccupyExceptionBudgetSameStepIsIdempotent(t *testing.T) {
	store := newExceptionBudgetStore(map[string]int{"budget-builder": 1})
	service := &Service{complexExecution: store, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	execution := core.ComplexExecutionSnapshot{Exception: &core.ComplexExceptionFacts{
		Budgets: []core.ComplexExceptionBudget{{ID: "budget-builder", RoleKind: core.ComplexExceptionBudgetBuilder, ComplexExecutionTaskID: "task"}},
	}}
	if err := service.occupyExceptionBudget(context.Background(), execution, "task", core.ComplexExceptionBudgetBuilder, "", "step-a"); err != nil {
		t.Fatal(err)
	}
	if err := service.occupyExceptionBudget(context.Background(), execution, "task", core.ComplexExceptionBudgetBuilder, "", "step-a"); err != nil {
		t.Fatal(err)
	}
	if store.remaining["budget-builder"] != 0 {
		t.Fatalf("used remaining = %d", store.remaining["budget-builder"])
	}
}

func TestOccupyExceptionBudgetConcurrentTurnsDoNotOverspend(t *testing.T) {
	store := newExceptionBudgetStore(map[string]int{"budget-builder": 2})
	service := &Service{complexExecution: store, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	execution := core.ComplexExecutionSnapshot{Exception: &core.ComplexExceptionFacts{
		Budgets: []core.ComplexExceptionBudget{{ID: "budget-builder", RoleKind: core.ComplexExceptionBudgetBuilder, ComplexExecutionTaskID: "task"}},
	}}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			errs[index] = service.occupyExceptionBudget(context.Background(), execution, "task", core.ComplexExceptionBudgetBuilder, "", "step-"+string(rune('a'+index)))
		}(i)
	}
	wg.Wait()
	stopped, ok := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, errComplexExecutionStopped):
			stopped++
		default:
			t.Fatalf("unexpected err %v", err)
		}
	}
	if ok != 2 || stopped != 2 {
		t.Fatalf("ok=%d stopped=%d remaining=%d", ok, stopped, store.remaining["budget-builder"])
	}
}

func TestCreateExceptionAgentStepDoesNotInsertWhenBudgetStops(t *testing.T) {
	store := &exceptionStepBudgetStore{}
	service := &Service{complexExecution: store, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	execution := core.ComplexExecutionSnapshot{Exception: &core.ComplexExceptionFacts{
		Budgets: []core.ComplexExceptionBudget{{ID: "budget-specialist", RoleKind: core.ComplexExceptionBudgetSpecialist, ComplexExecutionTaskID: "task"}},
	}}
	store.occupy = func() (bool, error) { return false, nil }
	if err := service.createExceptionAgentStep(context.Background(), execution, "task", core.ComplexExceptionBudgetSpecialist, "run", "", "bind", core.AgentStep{ID: "step"}); !errors.Is(err, errComplexExecutionStopped) {
		t.Fatalf("err=%v", err)
	}
	if store.created != 0 || store.occupied != 1 {
		t.Fatalf("created=%d occupied=%d", store.created, store.occupied)
	}
	store.occupy = func() (bool, error) { return true, nil }
	if err := service.createExceptionAgentStep(context.Background(), execution, "task", core.ComplexExceptionBudgetSpecialist, "run", "", "bind", core.AgentStep{ID: "step-ok"}); err != nil {
		t.Fatal(err)
	}
	if store.created != 1 {
		t.Fatalf("created=%d", store.created)
	}
}

func TestSpecialistRequestedDoesNotContactAgentWithoutBudget(t *testing.T) {
	probe := &agentContactProbe{}
	service := newExceptionAgentGateService(probe, nil)
	execution, task := specialistRequestedExecution(nil)
	_, stopped, err := service.advanceComplexExceptionSpecialist(context.Background(), execution, task, core.ComplexPlanTask{}, "project")
	assertNoAgentContact(t, probe, stopped, err)
}

func TestSpecialistRequestedDoesNotContactAgentWhenOccupyFails(t *testing.T) {
	probe := &agentContactProbe{}
	store := &exceptionStepBudgetStore{occupy: func() (bool, error) { return false, nil }}
	service := newExceptionAgentGateService(probe, store)
	execution, task := specialistRequestedExecution([]core.ComplexExceptionBudget{{
		ID: "budget-specialist", RoleKind: core.ComplexExceptionBudgetSpecialist, ComplexExecutionTaskID: "task",
	}})
	_, stopped, err := service.advanceComplexExceptionSpecialist(context.Background(), execution, task, core.ComplexPlanTask{}, "project")
	assertNoAgentContact(t, probe, stopped, err)
	if store.created != 0 || store.occupied != 1 {
		t.Fatalf("created=%d occupied=%d", store.created, store.occupied)
	}
}

func TestSpecialistDoesNotCreateBindingWithoutBudget(t *testing.T) {
	probe := &agentContactProbe{}
	store := &exceptionStepBudgetStore{}
	service := newExceptionAgentGateService(probe, store)
	execution := core.ComplexExecutionSnapshot{
		Run:       core.ComplexExecutionRun{ID: "run"},
		Exception: &core.ComplexExceptionFacts{},
	}
	_, stopped, err := service.advanceComplexExceptionSpecialist(context.Background(), execution, core.ComplexExecutionTask{ID: "task"}, core.ComplexPlanTask{}, "project")
	assertNoAgentContact(t, probe, stopped, err)
	if store.occupied != 0 || store.created != 0 {
		t.Fatalf("created=%d occupied=%d", store.created, store.occupied)
	}
}

func TestSpecialistCheckKeepsInfrastructureFailureSeparateFromCandidateFailure(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		result     ports.ClearDevCheckResult
		runErr     error
		wantReason core.ReasonCode
	}{
		{
			name: "infrastructure", result: ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1},
			runErr: errors.New("prepared dependency environment is unavailable"), wantReason: core.ReasonCode("CHECKER_UNAVAILABLE"),
		},
		{
			name: "candidate", result: ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckFail, ExitCode: 1},
			wantReason: core.ReasonSpecialistInvalid,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := &specialistCheckSettleStore{}
			service := &Service{
				complexExecution: store,
				checks:           &specialistCheckResultRunner{result: testCase.result, err: testCase.runErr},
				now:              func() time.Time { return time.Unix(1, 0).UTC() },
			}
			execution := core.ComplexExecutionSnapshot{Exception: &core.ComplexExceptionFacts{
				SpecialistChecks: []core.ComplexSpecialistCheck{{
					ID: "check", ComplexExecutionTaskID: "task", CheckID: "package-json-specialist", Status: "STARTED",
				}},
			}}
			changed, err := service.advanceSpecialistChecks(context.Background(), execution, core.ComplexExecutionTask{ID: "task"}, core.ComplexOnDemandBinding{
				WorkspacePath: "/tmp/specialist", BaseCommitSHA: strings.Repeat("a", 40),
			})
			if err != nil || !changed {
				t.Fatalf("changed=%t err=%v", changed, err)
			}
			if store.settled.Status != "SETTLED" || store.settled.Result != core.EvidenceResultFail || store.settled.ReasonCode != testCase.wantReason {
				t.Fatalf("settled specialist check = %#v", store.settled)
			}
		})
	}
}

func TestRecoveryRequestedDoesNotContactAgentWithoutBudget(t *testing.T) {
	probe := &agentContactProbe{}
	service := newExceptionAgentGateService(probe, nil)
	execution := recoveryRequestedExecution(nil)
	_, stopped, err := service.advanceComplexExceptionRecovery(context.Background(), execution, "project")
	assertNoAgentContact(t, probe, stopped, err)
}

func TestRecoveryRequestedDoesNotContactAgentWhenOccupyFails(t *testing.T) {
	probe := &agentContactProbe{}
	store := &exceptionStepBudgetStore{occupy: func() (bool, error) { return false, nil }}
	service := newExceptionAgentGateService(probe, store)
	execution := recoveryRequestedExecution([]core.ComplexExceptionBudget{{
		ID: "budget-recovery", RoleKind: core.ComplexExceptionBudgetRecovery,
	}})
	_, stopped, err := service.advanceComplexExceptionRecovery(context.Background(), execution, "project")
	assertNoAgentContact(t, probe, stopped, err)
	if store.created != 0 || store.occupied != 1 {
		t.Fatalf("created=%d occupied=%d", store.created, store.occupied)
	}
}

func TestSpecialistRequestedDoesNotCreateStepWhenModelMissing(t *testing.T) {
	execution, _ := specialistRequestedExecution([]core.ComplexExceptionBudget{{
		ID: "budget-specialist", RoleKind: core.ComplexExceptionBudgetSpecialist, ComplexExecutionTaskID: "task",
	}})
	assertOnDemandPreflightBlocksStep(t, execution, func(service *Service, snapshot core.ComplexExecutionSnapshot) (bool, bool, error) {
		return service.advanceComplexExceptionSpecialist(context.Background(), snapshot, core.ComplexExecutionTask{ID: "task"}, core.ComplexPlanTask{WritePaths: []string{"package.json"}}, "project")
	})
}

func TestRecoveryRequestedDoesNotCreateStepWhenModelMissing(t *testing.T) {
	execution := recoveryRequestedExecution([]core.ComplexExceptionBudget{{
		ID: "budget-recovery", RoleKind: core.ComplexExceptionBudgetRecovery,
	}})
	assertOnDemandPreflightBlocksStep(t, execution, func(service *Service, snapshot core.ComplexExecutionSnapshot) (bool, bool, error) {
		return service.advanceComplexExceptionRecovery(context.Background(), snapshot, "project")
	})
}

func assertOnDemandPreflightBlocksStep(t *testing.T, execution core.ComplexExecutionSnapshot, advance func(*Service, core.ComplexExecutionSnapshot) (bool, bool, error)) {
	t.Helper()
	execution.Run.DevelopmentRequirementID = "req"
	probe := &agentContactProbe{}
	store := &exceptionStepBudgetStore{}
	checker := &scriptedControlledPreflight{fn: func(requested string) (ports.ChatControlledPreflight, error) {
		result := livePreflightCatalog([]string{"gpt-5.4"}, "")
		result.RequestedModel = requested
		return result, ports.ErrChatModelNotAvailable
	}}
	service := &Service{
		ao:       &onDemandAccountAO{project: domain.ProjectRecord{ID: "project", Config: domain.ProjectConfig{AgentConfig: domain.AgentConfig{Model: "gpt-5.6-sol"}}}},
		sessions: probe, chat: probe, inspector: probe, complexExecution: store,
		preflightChecker: checker, preflights: newMemoryControlledPreflights(),
		logger: slog.New(slog.DiscardHandler),
		now:    func() time.Time { return time.Unix(1, 0).UTC() },
		newID:  func() string { return "preflight-1" },
	}
	progressed, stopped, err := advance(service, execution)
	if err != nil || progressed || stopped {
		t.Fatalf("progressed=%t stopped=%t err=%v", progressed, stopped, err)
	}
	if probe.spawns != 0 || probe.relays != 0 || probe.prepares != 0 {
		t.Fatalf("agent contact spawns=%d relays=%d prepares=%d", probe.spawns, probe.relays, probe.prepares)
	}
	if store.created != 0 || store.occupied != 0 {
		t.Fatalf("created=%d occupied=%d", store.created, store.occupied)
	}
	record, found, recErr := service.preflights.GetLatestClearDevControlledPreflightForBinding(context.Background(), execution.Exception.OnDemandBindings[0].ID)
	if recErr != nil || !found || record.ReasonCode != core.ReasonModelNotAvailable {
		t.Fatalf("preflight record found=%t err=%v record=%#v", found, recErr, record)
	}
}

type onDemandAccountAO struct {
	project domain.ProjectRecord
}

func (a *onDemandAccountAO) GetProject(_ context.Context, id string) (domain.ProjectRecord, bool, error) {
	if a.project.ID == "" {
		a.project.ID = id
	}
	return a.project, true, nil
}

func (a *onDemandAccountAO) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	return domain.SessionRecord{}, false, errors.New("session must not be read before preflight")
}

func newExceptionAgentGateService(probe *agentContactProbe, store ComplexExecutionFactStore) *Service {
	return &Service{
		sessions: probe, chat: probe, inspector: probe, complexExecution: store,
		ao: &onDemandAccountAO{}, preflightChecker: alwaysPassControlledPreflight{}, preflights: newMemoryControlledPreflights(),
		now:   func() time.Time { return time.Unix(1, 0).UTC() },
		newID: func() string { return "step-1" },
	}
}

func assertNoAgentContact(t *testing.T, probe *agentContactProbe, stopped bool, err error) {
	t.Helper()
	if !stopped || !errors.Is(err, errComplexExecutionStopped) {
		t.Fatalf("stopped=%t err=%v", stopped, err)
	}
	if probe.spawns != 0 || probe.relays != 0 || probe.prepares != 0 {
		t.Fatalf("agent contact spawns=%d relays=%d prepares=%d", probe.spawns, probe.relays, probe.prepares)
	}
}

func specialistRequestedExecution(budgets []core.ComplexExceptionBudget) (core.ComplexExecutionSnapshot, core.ComplexExecutionTask) {
	task := core.ComplexExecutionTask{ID: "task"}
	return core.ComplexExecutionSnapshot{
		Run: core.ComplexExecutionRun{ID: "run", BuilderRoleBindingID: "builder"},
		RoleBindings: []core.ComplexExecutionRoleBinding{{
			ID: "builder", Status: core.RoleBindingStatusBound, AOSessionID: "sess",
			SessionCreationIdempotencyKey: "builder-key",
		}},
		Exception: &core.ComplexExceptionFacts{
			Budgets: budgets,
			OnDemandBindings: []core.ComplexOnDemandBinding{{
				ID: "specialist-bind", ExecutionRunID: "run", ComplexExecutionTaskID: "task",
				Mode: core.ComplexOnDemandModeSpecialist, Status: core.RoleBindingStatusRequested,
				SessionCreationIdempotencyKey: "cleardev-complex-exception:specialist:task",
			}},
		},
	}, task
}

func recoveryRequestedExecution(budgets []core.ComplexExceptionBudget) core.ComplexExecutionSnapshot {
	failedID := "check-fail"
	return core.ComplexExecutionSnapshot{
		Run: core.ComplexExecutionRun{ID: "run", BuilderRoleBindingID: "builder"},
		RoleBindings: []core.ComplexExecutionRoleBinding{{
			ID: "builder", Status: core.RoleBindingStatusBound, AOSessionID: "sess",
			SessionCreationIdempotencyKey: "builder-key",
		}},
		CheckRuns: []core.ComplexExecutionCheckRun{{
			ID: failedID, ReasonCode: core.ReasonCode("CHECKER_UNAVAILABLE"),
			Status: core.ComplexExecutionCheckRunFailed, RetryOrdinal: 0,
			CandidateCommitSHA: strings.Repeat("a", 40),
			CheckSpecFactID:    "spec", DispatchID: "disp", CandidateCommitID: "cand",
		}},
		Exception: &core.ComplexExceptionFacts{
			Budgets: budgets,
			OnDemandBindings: []core.ComplexOnDemandBinding{{
				ID: "recovery-bind", ExecutionRunID: "run", Mode: core.ComplexOnDemandModeRecovery,
				Status:                        core.RoleBindingStatusRequested,
				TriggerReason:                 core.ReasonCode("CHECKER_UNAVAILABLE"),
				SessionCreationIdempotencyKey: "cleardev-complex-exception:recovery:" + failedID,
			}},
		},
	}
}

type agentContactProbe struct {
	spawns   int
	relays   int
	prepares int
}

func (p *agentContactProbe) Spawn(context.Context, ports.SpawnConfig) (domain.Session, int, int, error) {
	p.spawns++
	return domain.Session{}, 0, 0, errors.New("spawn must not run")
}

func (p *agentContactProbe) RelayChatTurnWithID(context.Context, domain.SessionID, string, string) (string, error) {
	p.relays++
	return "", errors.New("relay must not run")
}

func (p *agentContactProbe) Snapshot(context.Context, domain.SessionID) (chatsvc.Snapshot, error) {
	return chatsvc.Snapshot{}, nil
}

func (p *agentContactProbe) Interrupt(context.Context, domain.SessionID) error { return nil }

func (p *agentContactProbe) PrepareReviewBranch(context.Context, string, string, string) error {
	p.prepares++
	return errors.New("prepare must not run")
}

func (p *agentContactProbe) InspectCandidate(context.Context, string, string) (ports.ClearDevCandidateInspection, error) {
	return ports.ClearDevCandidateInspection{}, nil
}

func (p *agentContactProbe) PrepareBaseWorkspace(context.Context, string, string, string) error {
	return nil
}

func (p *agentContactProbe) ComposeCandidates(context.Context, ports.ClearDevComposeRequest) (ports.ClearDevComposeResult, error) {
	return ports.ClearDevComposeResult{}, nil
}

type exceptionBudgetStore struct {
	ComplexExecutionFactStore
	mu        sync.Mutex
	remaining map[string]int
	steps     map[string]string
	calls     int
}

func newExceptionBudgetStore(remaining map[string]int) *exceptionBudgetStore {
	return &exceptionBudgetStore{remaining: remaining, steps: map[string]string{}}
}

func (s *exceptionBudgetStore) OccupyClearDevComplexExceptionBudget(_ context.Context, budgetID, roundKey, stepID string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if prev, ok := s.steps[stepID]; ok {
		return prev == budgetID, nil
	}
	if s.remaining[budgetID] <= 0 {
		return false, nil
	}
	s.remaining[budgetID]--
	s.steps[stepID] = budgetID
	return true, nil
}

type exceptionStepBudgetStore struct {
	ComplexExecutionFactStore
	occupy   func() (bool, error)
	occupied int
	created  int
}

type specialistCheckSettleStore struct {
	ComplexExecutionFactStore
	settled core.ComplexSpecialistCheck
}

func (s *specialistCheckSettleStore) SettleClearDevComplexExceptionSpecialistCheck(_ context.Context, check core.ComplexSpecialistCheck, _ time.Time) (bool, error) {
	s.settled = check
	return true, nil
}

type specialistCheckResultRunner struct {
	result ports.ClearDevCheckResult
	err    error
}

func (r *specialistCheckResultRunner) PrepareCandidateChecks(_ context.Context, request ports.ClearDevCheckPreflightRequest) (ports.ClearDevCheckEnvironment, error) {
	return ports.ClearDevCheckEnvironment{CandidateSHA: request.CandidateSHA, Image: request.Image}, nil
}

func (r *specialistCheckResultRunner) RunCandidateCheck(context.Context, ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	return r.result, r.err
}

func (s *exceptionStepBudgetStore) OccupyClearDevComplexExceptionBudget(_ context.Context, _, _, _ string, _ time.Time) (bool, error) {
	s.occupied++
	if s.occupy != nil {
		return s.occupy()
	}
	return true, nil
}

func (s *exceptionStepBudgetStore) CreateClearDevComplexExceptionAgentStep(_ context.Context, _, _, _ string, step core.AgentStep) (core.AgentStep, bool, error) {
	s.created++
	return step, true, nil
}
