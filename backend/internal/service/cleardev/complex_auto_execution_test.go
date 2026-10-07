package cleardev

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// Models, Git and checks are explicit doubles; every control/attempt fact uses
// real SQLite. The fake remote Chat survives service restarts, like an external
// provider, while the database is actually closed and reopened from disk.
type autoExecutionFixture struct {
	dir     string
	store   *sqlite.Store
	harness *standardAgentHarness
	ids     *standardTestIDs
	clock   func() time.Time
	service *Service
	view    RequirementView
}

func newAutoExecutionFixture(t *testing.T) *autoExecutionFixture {
	t.Helper()
	f := &autoExecutionFixture{dir: t.TempDir(), ids: &standardTestIDs{}}
	var err error
	f.store, err = sqlitetest.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	var tick atomic.Int64
	f.clock = func() time.Time {
		return time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC).Add(time.Duration(tick.Add(1)) * time.Second)
	}
	if err := f.store.UpsertProject(context.Background(), domain.ProjectRecord{ID: "s04-project", Path: "/tmp/s04-project", Kind: domain.ProjectKindSingleRepo, RegisteredAt: f.clock()}); err != nil {
		t.Fatal(err)
	}
	f.harness = newStandardAgentHarness(f.store, false)
	f.harness.benchmarkSequentialCandidates = true // fake Git starts clean, then advances with each Builder result
	f.harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
		task := plan.Tasks[0]
		task.DependencyKeys = []string{}
		task.WritePaths = []string{"src/email.js", "test/email.test.js"}
		plan.Tasks = []core.ComplexPlanTask{task}
		plan.ParallelSuggestion.RecommendedBuilderCount = 1
	}
	f.service = f.newService()
	f.view = createComplexUntilClarification(t, f.service)
	f.view = answerComplexQuestions(t, f.service, f.view)
	assertNoDevelopmentWork(t, f.store, f.harness, f.view.Requirement.ID)
	return f
}

func (f *autoExecutionFixture) newService() *Service {
	return New(Deps{
		Facts: f.store, StandardFacts: f.store, ComplexFacts: f.store, ComplexExecutionFacts: f.store, DirectionFacts: f.store, HumanDecisions: f.store,
		ParseCorrections: f.store, AgentAttempts: f.store, ControlledPreflights: f.store, ControlledPreflightChecker: alwaysPassControlledPreflight{},
		ProgressExplanations: f.store, Workspace: gitWorkspaceObserver{}, AO: f.store,
		Sessions: f.harness, Chat: f.harness, Inspector: f.harness, Checks: f.harness,
		Human: allowStandardHuman{}, RunBackground: func(run func()) { run() }, AutoAdvanceComplexPlans: true,
		RecoverAgentSession: func(context.Context, domain.SessionID) error {
			return errors.New("unexpected fake provider session recovery")
		},
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: f.ids.New, Clock: f.clock,
	})
}

func (f *autoExecutionFixture) confirm(t *testing.T) {
	t.Helper()
	// Explicit human-authority test double; no production HTTP approval route.
	if err := f.service.ConfirmRequirementVersion(context.Background(), f.view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
}

func (f *autoExecutionFixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.harness.store = f.store
	f.service = f.newService()
}

func (f *autoExecutionFixture) completed(t *testing.T) core.ComplexExecutionSnapshot {
	t.Helper()
	execution, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil || !found {
		t.Fatalf("approved plan did not create execution: found=%v err=%v", found, err)
	}
	phase, reason := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionCompleted || len(execution.Tasks) != 1 || len(execution.Dispatches) != 1 || len(execution.Reviews) != 1 || execution.Integration == nil {
		t.Fatalf("execution phase=%s reason=%s tasks=%d dispatches=%+v reviews=%d", phase, reason, len(execution.Tasks), execution.Dispatches, len(execution.Reviews))
	}
	f.noQuick(t)
	view, err := f.service.GetRequirement(context.Background(), f.view.Requirement.ID)
	if err != nil || view.TrustedProgress.Phase != core.TrustedPhaseCompleted {
		t.Fatalf("trusted progress=%s err=%v", view.TrustedProgress.Phase, err)
	}
	requests := 0
	for _, event := range view.Events {
		if event.Action == core.ActionRequestComplexExecution && event.Outcome == core.EventAccepted {
			requests++
		}
	}
	if requests != 1 {
		t.Fatalf("execution creation events=%d, want exactly one", requests)
	}
	return execution
}

func (f *autoExecutionFixture) noQuick(t *testing.T) {
	t.Helper()
	if _, found, err := f.store.GetClearDevComplexQuickExecution(context.Background(), f.view.Requirement.ID); err != nil || found {
		t.Fatalf("unexpected QUICK: found=%v err=%v", found, err)
	}
}

func (f *autoExecutionFixture) noExecution(t *testing.T) {
	t.Helper()
	if _, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID); err != nil || found {
		t.Fatalf("unexpected execution: found=%v err=%v", found, err)
	}
	assertNoDevelopmentWork(t, f.store, f.harness, f.view.Requirement.ID)
	f.noQuick(t)
}

type autoObservedCounts struct{ spawns, sends, checks, builderSends int }

func (f *autoExecutionFixture) counts() autoObservedCounts {
	f.harness.mu.Lock()
	defer f.harness.mu.Unlock()
	out := autoObservedCounts{spawns: len(f.harness.spawnConfigs), sends: len(f.harness.relays), checks: len(f.harness.checkRequests)}
	for _, relay := range f.harness.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) {
			out.builderSends++
		}
	}
	return out
}

func TestApprovedSinglePlanAutomaticallyExecutesWithoutExternalStart(t *testing.T) {
	f := newAutoExecutionFixture(t)
	f.confirm(t)
	before := f.completed(t)
	counts := f.counts()
	if counts.spawns != 4 || counts.builderSends != 1 {
		t.Fatalf("expected Steward/Planner/Builder/Reviewer and one actual Builder send, got %+v", counts)
	}
	f.reopen(t)
	for i := 0; i < 3; i++ {
		if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.StartComplexStandardExecution(context.Background(), f.view.Requirement.ID); err != nil {
			t.Fatalf("completed duplicate start: %v", err)
		}
	}
	if after := f.completed(t); !reflect.DeepEqual(before, after) || f.counts() != counts {
		t.Fatalf("completed replay changed durable facts or external actions: before=%+v after=%+v", counts, f.counts())
	}
}

var errAutoInjectedCrash = errors.New("test double: process lost at durable boundary")

type autoCrashStore struct {
	ComplexFactStore
	ComplexExecutionFactStore
	AgentAttemptStore
	boundary, requirementID string
	fired                   bool
}

func (s *autoCrashStore) RecordClearDevComplexPlanReview(ctx context.Context, command core.RecordComplexPlanReviewCommand) error {
	if err := s.ComplexFactStore.RecordClearDevComplexPlanReview(ctx, command); err != nil {
		return err
	}
	if s.boundary == "after-approval" && !s.fired {
		s.fired = true
		return errAutoInjectedCrash
	}
	return nil
}

func (s *autoCrashStore) StartClearDevComplexExecution(ctx context.Context, command core.StartComplexExecutionCommand) (core.ComplexExecutionRun, bool, error) {
	run, created, err := s.ComplexExecutionFactStore.StartClearDevComplexExecution(ctx, command)
	if err == nil && created && s.boundary == "after-run-created" && !s.fired {
		s.fired = true
		return run, created, errAutoInjectedCrash
	}
	return run, created, err
}

func (s *autoCrashStore) MarkClearDevComplexExecutionAgentStepSent(ctx context.Context, id string, at time.Time) (bool, error) {
	if s.boundary == "after-builder-received" && !s.fired {
		execution, _, err := s.GetClearDevComplexExecution(ctx, s.requirementID)
		if err != nil {
			return false, err
		}
		for _, step := range execution.AgentSteps {
			if step.ID == id && step.Kind == core.ComplexExecutionAgentStepBuilderTask {
				s.fired = true
				return false, errAutoInjectedCrash
			}
		}
	}
	return s.ComplexExecutionFactStore.MarkClearDevComplexExecutionAgentStepSent(ctx, id, at)
}

func (s *autoCrashStore) ConfirmClearDevAgentMessage(ctx context.Context, event core.AgentAttemptEvent) error {
	view, err := s.GetClearDevAgentAttemptState(ctx, s.requirementID, event.AttemptID)
	if err != nil {
		return err
	}
	if s.boundary == "before-provider-receipt" && view.StepKind == core.ComplexExecutionAgentStepBuilderTask && !s.fired {
		s.fired = true
		return errAutoInjectedCrash
	}
	return s.AgentAttemptStore.ConfirmClearDevAgentMessage(ctx, event)
}

func TestAutoExecutionRestartsAcrossDurableBoundariesWithoutDuplicateDispatch(t *testing.T) {
	for _, boundary := range []string{"after-approval", "after-run-created", "after-builder-received", "before-provider-receipt"} {
		t.Run(boundary, func(t *testing.T) {
			f := newAutoExecutionFixture(t)
			crash := &autoCrashStore{ComplexFactStore: f.store, ComplexExecutionFactStore: f.store, boundary: boundary, requirementID: f.view.Requirement.ID}
			crash.AgentAttemptStore = f.store
			f.service.complex, f.service.complexExecution, f.service.attempts = crash, crash, crash
			f.confirm(t)
			if !crash.fired {
				t.Fatal("crash injection never reached its intended boundary")
			}
			before, exists, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
			if err != nil || exists != (boundary != "after-approval") {
				t.Fatalf("pre-restart boundary: exists=%v err=%v", exists, err)
			}
			if (boundary == "after-builder-received" || boundary == "before-provider-receipt") && f.counts().builderSends != 1 {
				t.Fatal("fake provider did not actually receive Builder message before crash")
			}
			f.reopen(t)
			// These are the existing daemon recovery entry points; no execution-runs call.
			if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			after := f.completed(t)
			if exists && before.Run.ID != after.Run.ID {
				t.Fatal("restart replaced the persisted execution run")
			}
			if (boundary == "after-builder-received" || boundary == "before-provider-receipt") && before.Dispatches[0].ClientMessageID != after.Dispatches[0].ClientMessageID {
				t.Fatal("restart replaced the Builder message identity")
			}
			if counts := f.counts(); counts.spawns != 4 || counts.builderSends != 1 {
				t.Fatalf("restart duplicated external work: %+v", counts)
			}
		})
	}
}

func TestAutoExecutionConcurrentApprovalRecoveryAndStartConverge(t *testing.T) {
	f := newAutoExecutionFixture(t)
	crash := &autoCrashStore{ComplexFactStore: f.store, ComplexExecutionFactStore: f.store, boundary: "after-approval", requirementID: f.view.Requirement.ID}
	f.service.complex = crash
	f.confirm(t)
	f.noExecution(t)
	f.reopen(t)
	var mu sync.Mutex
	queue := []func(){}
	f.service.runBackground = func(run func()) { mu.Lock(); queue = append(queue, run); mu.Unlock() }
	var wg sync.WaitGroup
	errs := make([]error, 12)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				errs[i] = f.service.ResumeComplexFlows(context.Background())
			} else {
				_, errs[i] = f.service.StartComplexStandardExecution(context.Background(), f.view.Requirement.ID)
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; ; i++ {
		mu.Lock()
		if len(queue) == 0 {
			mu.Unlock()
			break
		}
		run := queue[0]
		queue = queue[1:]
		mu.Unlock()
		if i >= 6 {
			t.Fatal("duplicate triggers kept scheduling new work")
		}
		run()
	}
	f.completed(t)
	if counts := f.counts(); counts.spawns != 4 || counts.builderSends != 1 {
		t.Fatalf("concurrent triggers duplicated external work: %+v", counts)
	}
}

type autoWakeChat struct {
	StandardChatService
	once sync.Once
	wake func()
}

func (c *autoWakeChat) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, key string) (string, error) {
	if strings.Contains(prompt, `"kind":"BUILDER_RESULT"`) {
		c.once.Do(c.wake)
	}
	return c.StandardChatService.RelayChatTurnWithID(ctx, session, prompt, key)
}

func TestAutoExecutionTrailingWakeCannotStartQuick(t *testing.T) {
	f := newAutoExecutionFixture(t)
	woken := false
	f.service.chat = &autoWakeChat{StandardChatService: f.harness, wake: func() {
		woken = true
		f.service.scheduleComplexFlow(f.view.Requirement.ID)
		f.service.scheduleComplexStandardExecution(f.view.Requirement.ID)
	}}
	f.confirm(t)
	if !woken {
		t.Fatal("wake was not injected while the Builder was active")
	}
	f.completed(t)
	if f.counts().builderSends != 1 {
		t.Fatal("trailing wake resent Builder task")
	}
}

type autoStoppedDirection struct{ DirectionFactStore }

func (autoStoppedDirection) HasActiveClearDevDirectionStop(context.Context, string) (bool, error) {
	return true, nil // explicit stop-gate observation double
}

func TestAutoExecutionPreservesApprovalCancellationAndDirectionGates(t *testing.T) {
	for _, mode := range []string{"pending-confirmation", "cancelled", "needs-human", "replan", "direction-stop", "multiple-tasks"} {
		t.Run(mode, func(t *testing.T) {
			f := newAutoExecutionFixture(t)
			switch mode {
			case "cancelled":
				if err := f.service.CancelRequirement(context.Background(), f.view.Requirement.ID, "test cancellation"); err != nil {
					t.Fatal(err)
				}
			case "needs-human":
				f.harness.planReviewVerdict = "NEEDS_HUMAN"
				f.confirm(t)
			case "replan":
				f.harness.planReviewVerdict = "REPLAN"
				f.confirm(t)
			case "direction-stop":
				// Freeze approval before the artificial stop observation appears.
				f.service.autoAdvanceComplexPlans = false
				f.confirm(t)
				f.service.autoAdvanceComplexPlans = true
				f.service.direction = autoStoppedDirection{f.store}
			case "multiple-tasks":
				f.harness.complexPlanMutator = nil
				f.confirm(t)
			}
			if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.noExecution(t)
		})
	}
}

type autoUnhealthyBaseline struct{ ports.ClearDevCheckRunner }

func (autoUnhealthyBaseline) CheckDevelopmentBaseline(context.Context, ports.ClearDevBaselineRequest) (ports.ClearDevBaselineResult, error) {
	return ports.ClearDevBaselineResult{Required: true, ReasonCode: "BASELINE_TEST_FAILED"}, errors.New("test double: unhealthy starting baseline")
}

func TestAutoExecutionCannotBypassBaselineFailure(t *testing.T) {
	f := newAutoExecutionFixture(t)
	f.service.checks = autoUnhealthyBaseline{f.harness}
	f.confirm(t)
	view := mustGetComplex(t, f.service, f.view.Requirement.ID)
	if view.TrustedProgress.Phase != core.TrustedPhaseBlocked || view.TrustedProgress.ReasonCode != "BASELINE_TEST_FAILED" {
		t.Fatalf("baseline failure not visible: phase=%s reason=%s", view.TrustedProgress.Phase, view.TrustedProgress.ReasonCode)
	}
	counts := f.counts()
	if counts.spawns != 2 || counts.builderSends != 0 {
		t.Fatalf("unhealthy baseline created or sent to Builder: %+v", counts)
	}
	f.reopen(t)
	f.service.checks = autoUnhealthyBaseline{f.harness}
	if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.counts() != counts {
		t.Fatal("restart reset the durable baseline failure")
	}
	f.noQuick(t)
}

type autoUnknownDeliveryChat struct {
	StandardChatService
	calls int
}

func (c *autoUnknownDeliveryChat) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, key string) (string, error) {
	if strings.Contains(prompt, `"kind":"BUILDER_RESULT"`) {
		c.calls++
		return "", errors.New("test double: connection lost without provider receipt")
	}
	return c.StandardChatService.RelayChatTurnWithID(ctx, session, prompt, key)
}

func TestAutoExecutionUnknownDeliveryRemainsBlockedWithoutResend(t *testing.T) {
	f := newAutoExecutionFixture(t)
	unknown := &autoUnknownDeliveryChat{StandardChatService: f.harness}
	f.service.chat = unknown
	f.confirm(t)
	if unknown.calls != 1 || f.counts().builderSends != 0 {
		t.Fatal("unknown-delivery injection missed its first send boundary")
	}
	before, _, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil || before.Run.CompletedAt != nil {
		t.Fatal("unknown delivery was completed")
	}
	budget, err := f.store.GetClearDevMessageBudget(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	for i := 0; i < 2; i++ {
		if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil || after.Run.ID != before.Run.ID || after.Run.CompletedAt != nil || len(after.Dispatches) != 1 || f.counts().builderSends != 0 {
		t.Fatal("unknown delivery was resent, replaced or completed after restart")
	}
	afterBudget, err := f.store.GetClearDevMessageBudget(context.Background(), f.view.Requirement.ID)
	if err != nil || !reflect.DeepEqual(budget, afterBudget) {
		t.Fatal("unknown-delivery restart changed the message budget")
	}
	f.noQuick(t)
}

func TestAutoExecutionDoesNotStartBoundBenchmark(t *testing.T) {
	f := newAutoExecutionFixture(t)
	f.service.autoAdvanceComplexPlans = false
	f.confirm(t)
	manifest := benchmarkManifestFixture(t, core.BenchmarkGroupG3, core.BenchmarkModeStandardOnly)
	binding, err := core.NewBenchmarkBinding(manifest, f.view.Requirement.ID, "s04-project", f.clock())
	if err != nil {
		t.Fatal(err)
	}
	f.service.benchmarkManifest = &manifest
	f.service.benchmarkFacts = benchmarkFactsFixture{binding: binding, found: true}
	f.service.autoAdvanceComplexPlans = true
	if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.noExecution(t)
}
