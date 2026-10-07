package cleardev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/cleardevlocal"
	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

const directionMessage = "只接受 example.com 域名，其他域名计入拒绝数量。"

func TestDirectionChangeHappyPathReplansV2WithoutFormalTasks(t *testing.T) {
	store, _, _, clock, service, view := seedDirectionReady(t)
	planned, running := seedDirectionTasks(t, service, view.Requirement.ID)
	view = proposeDirection(t, service, view.Requirement.ID, "intent-happy")
	if view.DirectionChange == nil || view.DirectionChange.Phase != core.DirectionChangeAwaitingDecision ||
		view.DirectionChange.Gate == nil || view.DirectionChange.Gate.Status != core.DirectionStopGateActive {
		t.Fatalf("after propose = %#v", view.DirectionChange)
	}
	if len(view.DirectionChange.Snapshots) != 2 {
		t.Fatalf("snapshot size = %d", len(view.DirectionChange.Snapshots))
	}
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionApprove)
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.DirectionChange == nil || view.DirectionChange.Phase != core.DirectionChangeAwaitingClarification {
		t.Fatalf("after approve processing = %#v", view.DirectionChange)
	}
	if len(view.DirectionChange.Checkpoints) != 2 {
		t.Fatalf("checkpoints = %d", len(view.DirectionChange.Checkpoints))
	}
	view, err := service.SubmitComplexClarifications(context.Background(), view.Requirement.ID, directionClarificationInput(view))
	if err != nil {
		t.Fatal(err)
	}
	if view.DirectionChange == nil || view.DirectionChange.Phase != core.DirectionChangeAwaitingConfirmation {
		t.Fatalf("after v2 answers = %#v", view.DirectionChange)
	}
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.DirectionChange == nil || view.DirectionChange.Phase != core.DirectionChangeApproved {
		t.Fatalf("v2 approved phase = %#v", view.DirectionChange)
	}
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved {
		t.Fatalf("complex phase = %#v", view.ComplexPlanning)
	}
	var confirmed, superseded int
	for _, version := range view.RequirementVersions {
		switch version.Status {
		case core.RequirementVersionStatusConfirmed:
			confirmed++
			if version.TaskSetVersion != 0 {
				t.Fatalf("v2 task-set version = %d", version.TaskSetVersion)
			}
		case core.RequirementVersionStatusSuperseded:
			superseded++
		}
	}
	if confirmed != 1 || superseded != 1 {
		t.Fatalf("versions confirmed=%d superseded=%d %#v", confirmed, superseded, view.RequirementVersions)
	}
	v2Tasks := 0
	for _, task := range view.DevelopmentTasks {
		if task.DevelopmentTask.ID == planned.ID || task.DevelopmentTask.ID == running.ID {
			if task.DevelopmentTask.Status != core.DevelopmentTaskStatusCancelled {
				t.Fatalf("v1 task %s status = %s", task.DevelopmentTask.ID, task.DevelopmentTask.Status)
			}
			continue
		}
		v2Tasks++
	}
	if v2Tasks != 0 {
		t.Fatalf("v2 formal tasks = %d", v2Tasks)
	}
	var v1SHA, v2SHA, v2ID string
	for _, version := range view.RequirementVersions {
		switch version.Status {
		case core.RequirementVersionStatusConfirmed:
			v2SHA, v2ID = version.SHA256, version.ID
		case core.RequirementVersionStatusSuperseded:
			v1SHA = version.SHA256
		}
	}
	if v1SHA == "" || v2SHA == "" || v1SHA == v2SHA || v2ID == "" {
		t.Fatalf("version SHAs v1=%s v2=%s id=%s", v1SHA, v2SHA, v2ID)
	}
	foundV2Plan := false
	for _, plan := range view.ComplexPlanning.Plans {
		if plan.RequirementVersionID != v2ID {
			continue
		}
		foundV2Plan = true
		if plan.CompilationSHA256 != v2SHA || plan.RequirementSHA256 != v2SHA {
			t.Fatalf("v2 plan bound compilation=%s requirement=%s, want confirmed %s", plan.CompilationSHA256, plan.RequirementSHA256, v2SHA)
		}
		if plan.CompilationSHA256 == v1SHA {
			t.Fatal("v2 plan reused the superseded v1 compilation")
		}
	}
	if !foundV2Plan {
		t.Fatal("missing engineering plan bound to confirmed v2")
	}
}

func TestDirectionChangeApprovesWhenNoTasksWereFrozen(t *testing.T) {
	store, _, _, clock, service, view := seedDirectionReady(t)
	view = proposeDirection(t, service, view.Requirement.ID, "intent-empty-tasks")
	if view.DirectionChange == nil || view.DirectionChange.Phase != core.DirectionChangeAwaitingDecision {
		t.Fatalf("after propose = %#v", view.DirectionChange)
	}
	if len(view.DirectionChange.Snapshots) != 0 {
		t.Fatalf("frozen tasks = %d", len(view.DirectionChange.Snapshots))
	}
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionApprove)
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.DirectionChange == nil || view.DirectionChange.Phase == core.DirectionChangeAwaitingDecision {
		t.Fatalf("empty occupancy stayed awaiting decision: %#v", view.DirectionChange)
	}
	if view.DirectionChange.Revision == nil {
		t.Fatalf("empty occupancy did not record a v2 revision: phase=%s", view.DirectionChange.Phase)
	}
	if view.DirectionChange.Phase == core.DirectionChangeProcessing {
		t.Fatalf("empty occupancy stuck in processing without compiling: %#v", view.DirectionChange)
	}
}

func TestDirectionChangeIntentIdempotentAndRejectsSecondProposal(t *testing.T) {
	_, _, _, _, service, view := seedDirectionReady(t)
	first := proposeDirection(t, service, view.Requirement.ID, "intent-same")
	second, err := service.ProposeDirectionIntent(context.Background(), view.Requirement.ID, ProposeDirectionIntentInput{
		RequestID: "intent-same", DevelopmentRequirementID: view.Requirement.ID, Message: directionMessage,
	})
	if err != nil {
		t.Fatalf("idempotent retry rejected: %v", err)
	}
	if second.DirectionChange == nil || second.DirectionChange.Intent == nil ||
		second.DirectionChange.Intent.RequestID != first.DirectionChange.Intent.RequestID ||
		second.DirectionChange.Intent.AgentStepID != first.DirectionChange.Intent.AgentStepID {
		t.Fatalf("idempotent retry changed facts: first=%#v second=%#v", first.DirectionChange, second.DirectionChange)
	}
	_, err = service.ProposeDirectionIntent(context.Background(), view.Requirement.ID, ProposeDirectionIntentInput{
		RequestID: "intent-same", DevelopmentRequirementID: view.Requirement.ID, Message: directionMessage + " extra",
	})
	assertAPICode(t, err, string(core.ReasonPreconditionNotMet))
	_, err = service.ProposeDirectionIntent(context.Background(), view.Requirement.ID, ProposeDirectionIntentInput{
		RequestID: "intent-other", DevelopmentRequirementID: view.Requirement.ID, Message: directionMessage,
	})
	assertAPICode(t, err, string(core.ReasonPreconditionNotMet))
}

func TestDirectionChangeConcurrentSecondProposal(t *testing.T) {
	_, _, _, _, service, view := seedDirectionReady(t)
	var okCount, errCount atomic.Int32
	var wg sync.WaitGroup
	for _, id := range []string{"intent-a", "intent-b"} {
		wg.Add(1)
		go func(requestID string) {
			defer wg.Done()
			_, err := service.ProposeDirectionIntent(context.Background(), view.Requirement.ID, ProposeDirectionIntentInput{
				RequestID: requestID, DevelopmentRequirementID: view.Requirement.ID, Message: directionMessage,
			})
			if err == nil {
				okCount.Add(1)
				return
			}
			var api *apierr.Error
			if errors.As(err, &api) && api.Code == string(core.ReasonPreconditionNotMet) {
				errCount.Add(1)
				return
			}
			t.Errorf("proposal %s unexpected error: %v", requestID, err)
		}(id)
	}
	wg.Wait()
	if okCount.Load() != 1 || errCount.Load() != 1 {
		t.Fatalf("concurrent proposals ok=%d err=%d", okCount.Load(), errCount.Load())
	}
}

func TestDispatchGateTaskFirstThenStopRejectsNewWrites(t *testing.T) {
	store, _, _, clock, service, view := seedDirectionReady(t)
	_, running := seedDirectionTasks(t, service, view.Requirement.ID)
	view = proposeDirection(t, service, view.Requirement.ID, "intent-task-first")
	if len(view.DirectionChange.Snapshots) != 2 {
		t.Fatalf("task-first snapshot = %d", len(view.DirectionChange.Snapshots))
	}
	_, err := service.CreateDevelopmentTask(context.Background(), view.Requirement.ID, directionTaskInput("after-gate"))
	assertAPICode(t, err, string(core.ReasonDirectionChangeStopped))
	if err := service.StartDevelopmentTask(context.Background(), running.ID); err == nil {
		t.Fatal("starting a snapshot task after the gate was accepted")
	} else {
		assertAPICode(t, err, string(core.ReasonDirectionChangeStopped))
	}
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionLater)
	later := mustGetComplex(t, service, view.Requirement.ID)
	if later.DirectionChange.Gate.Status != core.DirectionStopGateActive {
		t.Fatalf("LATER changed gate = %#v", later.DirectionChange.Gate)
	}
	_, err = service.CreateDevelopmentTask(context.Background(), view.Requirement.ID, directionTaskInput("after-later"))
	assertAPICode(t, err, string(core.ReasonDirectionChangeStopped))
}

func TestDispatchGateGateFirstRejectsNewTask(t *testing.T) {
	_, _, _, _, service, view := seedDirectionReady(t)
	view = proposeDirection(t, service, view.Requirement.ID, "intent-gate-first")
	if view.DirectionChange.Gate == nil || view.DirectionChange.Gate.Status != core.DirectionStopGateActive {
		t.Fatalf("gate-first propose = %#v", view.DirectionChange)
	}
	_, err := service.CreateDevelopmentTask(context.Background(), view.Requirement.ID, directionTaskInput("after-gate"))
	assertAPICode(t, err, string(core.ReasonDirectionChangeStopped))
}

func TestDirectionChangeRejectClosesGateAndLaterLeavesFacts(t *testing.T) {
	store, _, _, clock, service, view := seedDirectionReady(t)
	seedDirectionTasks(t, service, view.Requirement.ID)
	view = proposeDirection(t, service, view.Requirement.ID, "intent-reject")
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionReject)
	rejected := mustGetComplex(t, service, view.Requirement.ID)
	if rejected.DirectionChange == nil || rejected.DirectionChange.Phase != core.DirectionChangeRejected ||
		rejected.DirectionChange.Gate == nil || rejected.DirectionChange.Gate.Status != core.DirectionStopGateClosed {
		t.Fatalf("reject = %#v", rejected.DirectionChange)
	}
	if _, err := service.CreateDevelopmentTask(context.Background(), view.Requirement.ID, directionTaskInput("after-reject")); err != nil {
		t.Fatalf("create after reject: %v", err)
	}

	store2, _, _, clock2, service2, view2 := seedDirectionReady(t)
	view2 = proposeDirection(t, service2, view2.Requirement.ID, "intent-later")
	gateID := view2.DirectionChange.Gate.ID
	applyFakeDesktopDecision(t, store2, service2, clock2, view2.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionLater)
	later := mustGetComplex(t, service2, view2.Requirement.ID)
	if later.DirectionChange.Gate.ID != gateID || later.DirectionChange.Gate.Status != core.DirectionStopGateActive ||
		later.DirectionChange.Phase != core.DirectionChangeAwaitingDecision {
		t.Fatalf("LATER facts = %#v", later.DirectionChange)
	}
}

func TestDirectionChangeRestartAfterOccupyDoesNotDuplicateCancel(t *testing.T) {
	store, harness, ids, clock, service, view := seedDirectionReady(t)
	seedDirectionTasks(t, service, view.Requirement.ID)
	view = proposeDirection(t, service, view.Requirement.ID, "intent-restart-occupy")
	var hit atomic.Bool
	service.afterOccupy = func() {
		hit.Store(true)
		store.FailNextDirectionTaskFinalizeForTest(errors.New("injected finalize failure"))
	}
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionApprove)
	if !hit.Load() {
		t.Fatal("afterOccupy barrier was not hit")
	}
	paused := mustGetComplex(t, service, view.Requirement.ID)
	final, occupied := 0, 0
	for _, processing := range paused.DirectionChange.Processings {
		switch processing.Occupancy {
		case core.DirectionOccupancyFinal:
			final++
		case core.DirectionOccupancyOccupied:
			occupied++
		}
	}
	if occupied == 0 {
		t.Fatalf("injected failure left occupancy final=%d occupied=%d %#v", final, occupied, paused.DirectionChange.Processings)
	}
	recovered := complexTestService(store, harness, ids, clock, context.Background())
	if err := recovered.runDirectionChange(context.Background(), view.Requirement.ID); err != nil {
		t.Fatalf("resume after occupy: %v", err)
	}
	resumed := mustGetComplex(t, recovered, view.Requirement.ID)
	cancelled := 0
	for _, task := range resumed.DevelopmentTasks {
		if task.DevelopmentTask.Status == core.DevelopmentTaskStatusCancelled {
			cancelled++
		}
	}
	if cancelled != 2 {
		t.Fatalf("cancelled tasks = %d, want 2", cancelled)
	}
	for _, processing := range resumed.DirectionChange.Processings {
		if processing.Occupancy != core.DirectionOccupancyFinal {
			t.Fatalf("resumed processing = %#v", processing)
		}
	}
}

func TestDirectionChangeRestartBetweenTasks(t *testing.T) {
	store, harness, ids, clock, service, view := seedDirectionReady(t)
	seedDirectionTasks(t, service, view.Requirement.ID)
	view = proposeDirection(t, service, view.Requirement.ID, "intent-between")
	var hit atomic.Bool
	service.betweenTasks = func() {
		if hit.CompareAndSwap(false, true) {
			store.FailNextDirectionTaskFinalizeForTest(errors.New("injected between-tasks failure"))
		}
	}
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionApprove)
	if !hit.Load() {
		t.Fatal("betweenTasks barrier was not hit")
	}
	paused := mustGetComplex(t, service, view.Requirement.ID)
	final := 0
	for _, processing := range paused.DirectionChange.Processings {
		if processing.Occupancy == core.DirectionOccupancyFinal {
			final++
		}
	}
	if final != 1 {
		t.Fatalf("between-tasks occupancy finals = %d %#v", final, paused.DirectionChange.Processings)
	}
	recovered := complexTestService(store, harness, ids, clock, context.Background())
	if err := recovered.runDirectionChange(context.Background(), view.Requirement.ID); err != nil {
		t.Fatalf("resume between tasks: %v", err)
	}
	resumed := mustGetComplex(t, recovered, view.Requirement.ID)
	for _, processing := range resumed.DirectionChange.Processings {
		if processing.Occupancy != core.DirectionOccupancyFinal {
			t.Fatalf("resumed processing = %#v", processing)
		}
	}
}

func TestDirectionChangeCleanAndDirtyGitCheckpoints(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for worktree checkpoint tests")
	}
	store, harness, ids, clock, _ := newComplexFixture(t)
	cleanRepo, base, head := initDirectionGitRepo(t, false)
	dirtyRepo, dirtyBase, dirtyHead := initDirectionGitRepo(t, true)
	cleanSession, err := store.CreateSession(context.Background(), domain.SessionRecord{
		ProjectID: "s04-project", Kind: domain.KindWorker, Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: clock()},
		Metadata:  domain.SessionMetadata{WorkspacePath: cleanRepo, DiffBaseSHA: base},
		CreatedAt: clock(), UpdatedAt: clock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	dirtySession, err := store.CreateSession(context.Background(), domain.SessionRecord{
		ProjectID: "s04-project", Kind: domain.KindWorker, Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: clock()},
		Metadata:  domain.SessionMetadata{WorkspacePath: dirtyRepo, DiffBaseSHA: dirtyBase},
		CreatedAt: clock(), UpdatedAt: clock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	facts := &directionDispatchFacts{Store: store, byTask: map[string]dispatchFact{}}
	service := New(Deps{
		ParseCorrections:           store,
		AgentAttempts:              store,
		ControlledPreflights:       store,
		ControlledPreflightChecker: alwaysPassControlledPreflight{},
		ComplexExecutionFacts:      store,
		ProgressExplanations:       store,
		RecoverAgentSession:        func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },

		Facts: store, StandardFacts: store, ComplexFacts: store, DirectionFacts: facts, HumanDecisions: store,
		AO: store, Sessions: harness, Chat: harness, Inspector: cleardevlocal.New(), Checks: harness,
		Workspace: gitWorkspaceObserver{}, Human: allowStandardHuman{},
		BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	cleanTask, err := service.CreateDevelopmentTask(context.Background(), view.Requirement.ID, directionTaskInput("clean"))
	if err != nil {
		t.Fatal(err)
	}
	dirtyTask, err := service.CreateDevelopmentTask(context.Background(), view.Requirement.ID, directionTaskInput("dirty"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.StartDevelopmentTask(context.Background(), cleanTask.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.StartDevelopmentTask(context.Background(), dirtyTask.ID); err != nil {
		t.Fatal(err)
	}
	facts.byTask[cleanTask.ID] = dispatchFact{dispatch: "dispatch-clean", base: base, session: string(cleanSession.ID)}
	facts.byTask[dirtyTask.ID] = dispatchFact{dispatch: "dispatch-dirty", base: dirtyBase, session: string(dirtySession.ID)}
	var observeHit atomic.Bool
	service.afterObserve = func() {
		if observeHit.CompareAndSwap(false, true) {
			store.FailNextDirectionTaskFinalizeForTest(errors.New("injected observe barrier"))
		}
	}
	view = proposeDirection(t, service, view.Requirement.ID, "intent-git")
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionApprove)
	if !observeHit.Load() {
		t.Fatal("afterObserve barrier was not hit")
	}
	recovered := New(Deps{
		ParseCorrections:           store,
		AgentAttempts:              store,
		ControlledPreflights:       store,
		ControlledPreflightChecker: alwaysPassControlledPreflight{},
		ComplexExecutionFacts:      store,
		ProgressExplanations:       store,
		RecoverAgentSession:        func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },

		Facts: store, StandardFacts: store, ComplexFacts: store, DirectionFacts: facts, HumanDecisions: store,
		AO: store, Sessions: harness, Chat: harness, Inspector: cleardevlocal.New(), Checks: harness,
		Workspace: gitWorkspaceObserver{}, Human: allowStandardHuman{},
		BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})
	if err := recovered.runDirectionChange(context.Background(), view.Requirement.ID); err != nil {
		t.Fatalf("resume git checkpoints: %v", err)
	}
	resumed := mustGetComplex(t, recovered, view.Requirement.ID)
	kinds := map[core.DirectionCheckpointKind]int{}
	for _, checkpoint := range resumed.DirectionChange.Checkpoints {
		kinds[checkpoint.Kind]++
		if checkpoint.TaskID == cleanTask.ID {
			if checkpoint.Kind != core.DirectionCheckpointCleanCandidate || checkpoint.HeadSHA != head || checkpoint.CandidateCommitID == "" {
				t.Fatalf("clean checkpoint = %#v", checkpoint)
			}
		}
		if checkpoint.TaskID == dirtyTask.ID {
			if checkpoint.Kind != core.DirectionCheckpointDirtyPreserved || checkpoint.HeadSHA != dirtyHead ||
				!checkpoint.Dirty || checkpoint.CandidateCommitID != "" {
				t.Fatalf("dirty checkpoint = %#v", checkpoint)
			}
		}
	}
	if kinds[core.DirectionCheckpointCleanCandidate] != 1 || kinds[core.DirectionCheckpointDirtyPreserved] != 1 {
		t.Fatalf("checkpoint kinds = %#v", kinds)
	}
	status := strings.TrimSpace(runGit(t, dirtyRepo, "status", "--porcelain=v1"))
	if status == "" {
		t.Fatal("dirty worktree was cleaned")
	}
	if strings.TrimSpace(runGit(t, dirtyRepo, "rev-parse", "HEAD")) != dirtyHead {
		t.Fatal("dirty HEAD changed")
	}
}

func TestStartComplexStandardExecutionCreatesRunAfterSupersededIncompleteRun(t *testing.T) {
	store, harness, ids, clock, service, view := seedDirectionReady(t)
	starter := complexTestServiceWithBackground(context.Background(), store, harness, ids, clock, func(func()) {})
	started, err := starter.StartComplexStandardExecution(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.ComplexExecution == nil || started.ComplexExecution.Run.ID == "" {
		t.Fatal("expected an incomplete v1 complex execution run")
	}
	staleID := started.ComplexExecution.Run.ID
	staleVersion := started.ComplexExecution.Run.RequirementVersionID

	view = proposeDirection(t, service, view.Requirement.ID, "intent-stale-run")
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionApprove)
	view = mustGetComplex(t, service, view.Requirement.ID)
	view, err = service.SubmitComplexClarifications(context.Background(), view.Requirement.ID, directionClarificationInput(view))
	if err != nil {
		t.Fatal(err)
	}
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved {
		t.Fatalf("v2 planning = %#v", view.ComplexPlanning)
	}
	var confirmedVersion string
	for _, version := range view.RequirementVersions {
		if version.Status == core.RequirementVersionStatusConfirmed {
			confirmedVersion = version.ID
		}
	}
	if confirmedVersion == "" || confirmedVersion == staleVersion {
		t.Fatalf("confirmed version = %s stale=%s", confirmedVersion, staleVersion)
	}

	next, err := starter.StartComplexStandardExecution(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.ComplexExecution == nil || next.ComplexExecution.Run.ID == staleID || next.ComplexExecution.Run.RequirementVersionID != confirmedVersion {
		t.Fatalf("current execution = %#v, want new run on %s not %s", next.ComplexExecution, confirmedVersion, staleID)
	}
}

func TestDirectionChangeLeavesBlockedTaskUncancelled(t *testing.T) {
	store, _, _, clock, service, view := seedDirectionReady(t)
	planned, running := seedDirectionTasks(t, service, view.Requirement.ID)
	if err := service.BlockDevelopmentTask(context.Background(), running.ID, "builder blocked on frozen writePaths"); err != nil {
		t.Fatal(err)
	}
	view = proposeDirection(t, service, view.Requirement.ID, "intent-blocked")
	if view.DirectionChange == nil || view.DirectionChange.Phase != core.DirectionChangeAwaitingDecision {
		t.Fatalf("after propose = %#v", view.DirectionChange)
	}
	applyFakeDesktopDecision(t, store, service, clock, view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionApprove)
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.DirectionChange == nil || view.DirectionChange.Phase != core.DirectionChangeAwaitingClarification {
		t.Fatalf("after approve processing = %#v", view.DirectionChange)
	}
	if len(view.DirectionChange.Checkpoints) != 2 {
		t.Fatalf("checkpoints = %d", len(view.DirectionChange.Checkpoints))
	}
	var blockedKept, plannedCancelled bool
	for _, task := range view.DevelopmentTasks {
		switch task.DevelopmentTask.ID {
		case running.ID:
			if task.DevelopmentTask.Status != core.DevelopmentTaskStatusBlocked {
				t.Fatalf("blocked task status = %s", task.DevelopmentTask.Status)
			}
			blockedKept = true
		case planned.ID:
			if task.DevelopmentTask.Status != core.DevelopmentTaskStatusCancelled {
				t.Fatalf("planned task status = %s", task.DevelopmentTask.Status)
			}
			plannedCancelled = true
		}
	}
	if !blockedKept || !plannedCancelled {
		t.Fatalf("task outcomes blockedKept=%v plannedCancelled=%v %#v", blockedKept, plannedCancelled, view.DevelopmentTasks)
	}
	var terminal, unstarted int
	for _, checkpoint := range view.DirectionChange.Checkpoints {
		switch checkpoint.Kind {
		case core.DirectionCheckpointTerminal:
			terminal++
		case core.DirectionCheckpointUnstarted:
			unstarted++
		default:
			t.Fatalf("unexpected checkpoint kind %s", checkpoint.Kind)
		}
	}
	if terminal != 1 || unstarted != 1 {
		t.Fatalf("checkpoint kinds terminal=%d unstarted=%d", terminal, unstarted)
	}
}

func TestDirectionChangePathMismatchAndEmptyBodyRejected(t *testing.T) {
	_, _, _, _, service, view := seedDirectionReady(t)
	_, err := service.ProposeDirectionIntent(context.Background(), view.Requirement.ID, ProposeDirectionIntentInput{
		RequestID: "intent-mismatch", DevelopmentRequirementID: "other", Message: directionMessage,
	})
	assertAPICode(t, err, "CLEARDEV_REQUIREMENT_ID_MISMATCH")
	_, err = service.ProposeDirectionIntent(context.Background(), view.Requirement.ID, ProposeDirectionIntentInput{
		RequestID: "intent-empty", DevelopmentRequirementID: view.Requirement.ID, Message: "  ",
	})
	assertAPICode(t, err, "DIRECTION_MESSAGE_INVALID")
}

func seedDirectionReady(t *testing.T) (*sqlite.Store, *standardAgentHarness, *standardTestIDs, func() time.Time, *Service, RequirementView) {
	t.Helper()
	store, harness, ids, clock, service := newComplexService(t)
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved {
		t.Fatalf("seed planning = %#v", view.ComplexPlanning)
	}
	return store, harness, ids, clock, service, view
}

func seedDirectionTasks(t *testing.T, service *Service, requirementID string) (planned, running core.DevelopmentTask) {
	t.Helper()
	var err error
	planned, err = service.CreateDevelopmentTask(context.Background(), requirementID, directionTaskInput("planned"))
	if err != nil {
		t.Fatal(err)
	}
	running, err = service.CreateDevelopmentTask(context.Background(), requirementID, directionTaskInput("running"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.StartDevelopmentTask(context.Background(), running.ID); err != nil {
		t.Fatal(err)
	}
	return planned, running
}

func directionTaskInput(title string) CreateDevelopmentTaskInput {
	return CreateDevelopmentTaskInput{
		Title: title, Mode: core.WorkModeQuick, MaxReworkCount: 0,
		Permissions:    CreatePathPermissionsInput{WritePaths: []string{"src/**"}},
		RequiredChecks: []CreateRequiredCheckInput{{Name: "node-all", Kind: "command"}},
	}
}

func directionClarificationInput(view RequirementView) SubmitComplexClarificationsInput {
	if view.DirectionChange == nil || len(view.DirectionChange.CompilationRequests) == 0 {
		return SubmitComplexClarificationsInput{}
	}
	request := view.DirectionChange.CompilationRequests[len(view.DirectionChange.CompilationRequests)-1]
	answers := make([]ComplexClarificationAnswerInput, 0, len(view.DirectionChange.Questions))
	for _, question := range view.DirectionChange.Questions {
		if question.CompilationRequestID != request.ID {
			continue
		}
		text := "计入拒绝数量。"
		if strings.Contains(question.QuestionKey, "duplicate") {
			text = "重复地址忽略不计，只保留第一次出现的记录。"
		}
		answers = append(answers, ComplexClarificationAnswerInput{QuestionKey: question.QuestionKey, Text: text})
	}
	return SubmitComplexClarificationsInput{CompilationRequestID: request.ID, ClarificationRound: request.ClarificationRound, Answers: answers}
}

func proposeDirection(t *testing.T, service *Service, requirementID, requestID string) RequirementView {
	t.Helper()
	view, err := service.ProposeDirectionIntent(context.Background(), requirementID, ProposeDirectionIntentInput{
		RequestID: requestID, DevelopmentRequirementID: requirementID, Message: directionMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.DirectionChange == nil {
		t.Fatal("direction change view is missing")
	}
	return view
}

func applyFakeDesktopDecision(t *testing.T, store *sqlite.Store, service *Service, clock func() time.Time, requirementID, kind string, decision core.HumanDecisionChoice) {
	t.Helper()
	pending := pendingHumanRequestByKind(t, store, requirementID, kind)
	offer, err := store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{
		RequestID: pending.ID, DesktopRunID: "desk-" + kind, Nonce: mustComplexNonce(t),
		IssuedAt: clock(), ExpiresAt: clock().Add(core.HumanDecisionOfferTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyHumanDecisionResult(context.Background(), core.HumanDecisionResult{
		ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
		DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
		BindingSchemaVersion: offer.BindingSchemaVersion, Binding: append(json.RawMessage(nil), offer.Binding...),
		ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: decision,
	}); err != nil {
		t.Fatal(err)
	}
}

func pendingHumanRequestByKind(t *testing.T, store *sqlite.Store, requirementID, kind string) core.HumanDecisionRequest {
	t.Helper()
	pending, err := store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range pending {
		if request.DevelopmentRequirementID == requirementID && request.DecisionKind == kind {
			return request
		}
	}
	t.Fatalf("pending %s request was not found", kind)
	return core.HumanDecisionRequest{}
}

func assertAPICode(t *testing.T, err error, code string) {
	t.Helper()
	var api *apierr.Error
	if err == nil || !errors.As(err, &api) || api.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

type dispatchFact struct {
	dispatch, base, session string
}

type directionDispatchFacts struct {
	*sqlite.Store
	byTask map[string]dispatchFact
}

func (d *directionDispatchFacts) GetClearDevTaskDispatchFacts(ctx context.Context, taskID string) (string, string, string, error) {
	if row, ok := d.byTask[taskID]; ok {
		return row.dispatch, row.base, row.session, nil
	}
	return d.Store.GetClearDevTaskDispatchFacts(ctx, taskID)
}

type gitWorkspaceObserver struct{}

func (gitWorkspaceObserver) ObserveWorkspace(ctx context.Context, info ports.WorkspaceInfo) (ports.WorkspaceObservation, error) {
	path := strings.TrimSpace(info.Path)
	headRaw, err := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		return ports.WorkspaceObservation{}, err
	}
	statusRaw, err := exec.CommandContext(ctx, "git", "-C", path, "status", "--porcelain=v1", "--untracked-files=all").Output()
	if err != nil {
		return ports.WorkspaceObservation{}, err
	}
	observation := ports.WorkspaceObservation{
		Path: path, Branch: info.Branch, HeadSHA: strings.TrimSpace(string(headRaw)),
	}
	for _, line := range bytes.Split(bytes.TrimRight(statusRaw, "\n"), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		status := string(line)
		if len(status) >= 2 {
			if status[0] != ' ' && status[0] != '?' {
				observation.Staged = true
			}
			if status[1] != ' ' {
				observation.Dirty = true
			}
			if strings.HasPrefix(status, "??") {
				observation.Untracked = true
			}
			pathName := strings.TrimSpace(status[2:])
			observation.Changes = append(observation.Changes, ports.WorkspaceChange{Path: pathName, Status: status[:2]})
		}
	}
	return observation, nil
}

func initDirectionGitRepo(t *testing.T, dirty bool) (dir, base, head string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cleardev-s05-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "ClearDev Test")
	if err := os.WriteFile(filepath.Join(dir, "src.js"), []byte("export const n = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "src.js")
	runGit(t, dir, "commit", "-m", "base")
	base = strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(dir, "src.js"), []byte("export const n = 2;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "src.js")
	runGit(t, dir, "commit", "-m", "candidate")
	head = strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, base, head
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
