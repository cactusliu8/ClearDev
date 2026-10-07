package cleardev_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

var errMailParallelBoundary = errors.New("explicit test interruption at a durable parallel boundary")

// Only reads are interrupted. Every mutation, including completion, uses the
// production SQLite store. Reopening removes the injected interruption.
type mailParallelBoundaryStore struct {
	*sqlite.Store
	stopWhen func(core.ComplexExecutionSnapshot) bool
	reached  bool
}

func (s *mailParallelBoundaryStore) GetClearDevComplexExecution(ctx context.Context, id string) (core.ComplexExecutionSnapshot, bool, error) {
	snapshot, found, err := s.Store.GetClearDevComplexExecution(ctx, id)
	if err == nil && found && s.stopWhen != nil && s.stopWhen(snapshot) {
		s.reached = true
		return snapshot, found, errMailParallelBoundary
	}
	return snapshot, found, err
}

type mailParallelTurnInterval struct {
	session domain.SessionID
	turn    string
	start   int
	end     int
}

// This explicit provider double exposes real RUNNING snapshots until released.
// Logical event indices prove overlap without sleeps or fabricated timestamps;
// they are not evidence of real Terra provider concurrency.
type gatedMailParallelChat struct {
	*parallelAgentHarness
	mu        sync.Mutex
	hold      bool
	sequence  int
	intervals []mailParallelTurnInterval
}

func (h *gatedMailParallelChat) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, message string) (string, error) {
	turn, err := h.parallelAgentHarness.RelayChatTurnWithID(ctx, session, prompt, message)
	if err != nil || !strings.Contains(prompt, `"kind":"BUILDER_RESULT"`) {
		return turn, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, interval := range h.intervals {
		if interval.turn == turn {
			return turn, nil
		}
		if interval.session == session && interval.end == 0 {
			return "", fmt.Errorf("Builder %s received a second task while active", session)
		}
	}
	h.sequence++
	interval := mailParallelTurnInterval{session: session, turn: turn, start: h.sequence}
	if !h.hold {
		h.sequence++
		interval.end = h.sequence
	}
	h.intervals = append(h.intervals, interval)
	return turn, nil
}

func (h *gatedMailParallelChat) Snapshot(ctx context.Context, session domain.SessionID) (chatsvc.Snapshot, error) {
	snapshot, err := h.parallelAgentHarness.Snapshot(ctx, session)
	if err != nil {
		return snapshot, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, interval := range h.intervals {
		if interval.session != session || interval.end != 0 {
			continue
		}
		for i := range snapshot.Turns {
			if snapshot.Turns[i].ID == interval.turn {
				snapshot.Turns[i].State = domain.TurnStateRunning
				snapshot.Turns[i].CompletedAt = nil
			}
		}
		messages := snapshot.Messages[:0]
		for _, message := range snapshot.Messages {
			if message.TurnID != interval.turn || message.Role != domain.MessageRoleAssistant {
				messages = append(messages, message)
			}
		}
		snapshot.Messages = messages
	}
	return snapshot, nil
}

func (h *gatedMailParallelChat) release(t *testing.T, index int) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if index >= len(h.intervals) || h.intervals[index].end != 0 {
		t.Fatalf("missing active Builder interval %d: %+v", index, h.intervals)
	}
	h.sequence++
	h.intervals[index].end = h.sequence
}

type mailParallelBoundaryFixture struct {
	dataDir string
	store   *sqlite.Store
	harness *parallelAgentHarness
	chat    *gatedMailParallelChat
	prep    cleardevtest.ComplexParallelPrep
	ids     parallelTestIDs
	tick    int
}

func newMailParallelBoundaryFixture(t *testing.T) *mailParallelBoundaryFixture {
	t.Helper()
	return newMailParallelBoundaryFixtureForPaths(t, false)
}

func newMailParallelBoundaryFixtureForPaths(t *testing.T, overlapping bool) *mailParallelBoundaryFixture {
	t.Helper()
	seed := cleardevtest.SeedMailParallelV2
	if overlapping {
		seed = cleardevtest.SeedMailOverlappingV2
	}
	return newMailParallelBoundaryFixtureWithSeed(t, seed)
}

func newMailParallelBoundaryFixtureWithSeed(t *testing.T, seed func(testing.TB, *sqlite.Store, string, string, string) cleardevtest.ComplexParallelPrep) *mailParallelBoundaryFixture {
	t.Helper()
	f := &mailParallelBoundaryFixture{dataDir: t.TempDir()}
	f.store = sqlitetest.MustOpenAt(t, f.dataDir)
	repo := initParallelPrepRepo(t)
	f.prep = seed(t, f.store, "ao-mail-v2-boundaries", repo, f.dataDir)
	f.harness = newParallelAgentHarness(f.store, false)
	f.harness.mail, f.harness.repoPath = true, repo
	f.chat = &gatedMailParallelChat{parallelAgentHarness: f.harness}
	return f
}

func (f *mailParallelBoundaryFixture) clock() time.Time {
	f.tick++
	return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC).Add(time.Duration(f.tick) * time.Second)
}

func (f *mailParallelBoundaryFixture) snapshot(t *testing.T) core.ComplexExecutionSnapshot {
	t.Helper()
	execution, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.prep.RequirementID)
	if err != nil || !found {
		t.Fatalf("parallel execution read: found=%v err=%v", found, err)
	}
	return execution
}

func (f *mailParallelBoundaryFixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = sqlite.Open(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.harness.store = f.store
}

func (f *mailParallelBoundaryFixture) advance(t *testing.T, first bool, stopWhen func(core.ComplexExecutionSnapshot) bool) core.ComplexExecutionSnapshot {
	t.Helper()
	// One advance may execute all three tasks and their SQLite transactions.
	// Keep a finite liveness bound without imposing a five-second performance
	// requirement on race-instrumented runs. Event order still proves overlap.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	boundary := &mailParallelBoundaryStore{Store: f.store, stopWhen: stopWhen}
	service := parallelTestServiceWithOptions(f.store, f.harness, func(deps *cleardevsvc.Deps) {
		deps.ComplexExecutionFacts, deps.Chat = boundary, f.chat
		deps.NewID, deps.Clock, deps.BackgroundContext = f.ids.New, f.clock, ctx
		deps.AutoAdvanceComplexPlans = true
	})
	if first {
		// Exercise the production approval/restart handoff. A direct execution
		// start would hide failures in automatic admission of multi-task plans.
		err := service.ResumeComplexFlows(ctx)
		if err != nil && !boundary.reached {
			t.Fatal(err)
		}
	}
	for i := 0; i < 64 && !boundary.reached; i++ {
		execution := f.snapshot(t)
		phase, _ := core.DeriveComplexExecutionPhase(execution)
		if phase == core.ComplexExecutionCompleted || phase == core.ComplexExecutionBlocked || phase == core.ComplexExecutionNeedsHuman {
			break
		}
		if err := service.ResumeComplexStandardExecutions(ctx); err != nil {
			t.Fatal(err)
		}
		if ctx.Err() != nil {
			t.Fatal("parallel execution did not reach its bounded test boundary")
		}
	}
	execution := f.snapshot(t)
	if stopWhen != nil && !boundary.reached {
		phase, reason := core.DeriveComplexExecutionPhase(execution)
		t.Fatalf("boundary not reached: phase=%s reason=%s dispatches=%+v reviews=%+v", phase, reason, execution.Dispatches, execution.Reviews)
	}
	return execution
}

func mailParallelCompletionCommand(execution core.ComplexExecutionSnapshot, at time.Time) core.CompleteComplexExecutionCommand {
	composition := execution.Compositions[len(execution.Compositions)-1]
	var checkIDs []string
	for _, check := range execution.CheckRuns {
		if check.Kind == core.CandidateCheckIntegration && check.CandidateCommitSHA == composition.OutputCommitSHA {
			checkIDs = append(checkIDs, check.ID)
		}
	}
	return core.CompleteComplexExecutionCommand{
		ExecutionRunID: execution.Run.ID, At: at,
		Integration: core.ComplexExecutionIntegration{
			ID: "test-final-integration", ExecutionRunID: execution.Run.ID,
			IntegrationCandidateID: "test-final-candidate", CandidateCommitSHA: composition.OutputCommitSHA,
			CheckRunIDs: checkIDs, CompletedAt: at,
		},
	}
}

func finalParallelCheckSettled(execution core.ComplexExecutionSnapshot) bool {
	for _, check := range execution.CheckRuns {
		if check.Kind == core.CandidateCheckIntegration && check.Status == core.ComplexExecutionCheckRunSettled {
			return true
		}
	}
	return false
}

func TestMailV2ParallelOverlapDependencyAndSQLiteRestart(t *testing.T) {
	f := newMailParallelBoundaryFixture(t)
	f.chat.hold = true
	execution := f.advance(t, true, func(s core.ComplexExecutionSnapshot) bool {
		return len(f.chat.intervals) == 2 && len(s.Dispatches) == 2 && s.Dispatches[0].Status == core.ComplexExecutionDispatchRunning && s.Dispatches[1].Status == core.ComplexExecutionDispatchRunning
	})
	if len(f.chat.intervals) != 2 || f.chat.intervals[0].end != 0 || f.chat.intervals[1].end != 0 ||
		f.chat.intervals[0].session == f.chat.intervals[1].session || len(execution.Compositions) != 0 || f.harness.freezeCalls != 0 {
		t.Fatal("the first wave did not have two distinct simultaneously active Builders")
	}
	f.reopen(t)
	f.chat.release(t, 0)
	execution = f.advance(t, false, func(s core.ComplexExecutionSnapshot) bool { return len(s.Verifications) == 1 })
	if len(execution.Dispatches) != 2 || len(execution.Compositions) != 0 || len(f.chat.intervals) != 2 || f.chat.intervals[1].end != 0 {
		t.Fatal("a completed sibling released dependent work while the other Builder was still active")
	}
	f.reopen(t)
	f.chat.release(t, 1)
	left, right := f.chat.intervals[0], f.chat.intervals[1]
	if max(left.start, right.start) >= min(left.end, right.end) {
		t.Fatalf("Builder intervals did not overlap: %+v %+v", left, right)
	}
	execution = f.advance(t, false, func(s core.ComplexExecutionSnapshot) bool {
		return len(s.Compositions) == 1 && s.Compositions[0].Status == core.ComplexExecutionCompositionPending
	})
	if len(execution.Dispatches) != 2 || f.harness.composeCalls != 0 {
		t.Fatal("composition ran before its durable request or dependent task started early")
	}
	f.reopen(t)
	execution = f.advance(t, false, func(s core.ComplexExecutionSnapshot) bool {
		return len(s.Compositions) == 1 && s.Compositions[0].Status == core.ComplexExecutionCompositionComposed
	})
	firstComposition := execution.Compositions[0]
	if len(execution.Dispatches) != 2 || firstComposition.SettledAt == nil || f.harness.composeCalls != 1 {
		t.Fatal("first composition was not settled exactly once before dependent dispatch")
	}
	f.reopen(t)
	execution = f.advance(t, false, func(s core.ComplexExecutionSnapshot) bool {
		return len(f.chat.intervals) == 3 && len(s.Dispatches) == 3 && s.Dispatches[2].Status == core.ComplexExecutionDispatchRunning
	})
	followup := execution.Dispatches[2]
	if followup.BaseCommitSHA != firstComposition.OutputCommitSHA || !followup.CreatedAt.After(*firstComposition.SettledAt) || len(f.chat.intervals) != 3 {
		t.Fatal("dependent Builder did not wait for the precise trusted composition")
	}
	f.reopen(t)
	f.chat.release(t, 2)
	execution = f.advance(t, false, finalParallelCheckSettled)
	if len(execution.Verifications) != 3 || execution.FinalReview != nil || execution.Integration != nil || f.harness.mailDeliveryCalls != 1 {
		t.Fatal("final check boundary has missing task facts or premature completion")
	}
	if err := f.store.CompleteClearDevComplexExecution(context.Background(), mailParallelCompletionCommand(execution, f.clock())); err == nil {
		t.Fatal("task PASS and final checks completed the requirement without final Reviewer PASS")
	}
	var reviewID, packetSHA, resultID string
	for _, status := range []string{"REQUESTED", "PENDING", "SENT", "SETTLED"} {
		f.reopen(t)
		execution = f.advance(t, false, func(s core.ComplexExecutionSnapshot) bool {
			return s.FinalReview != nil && s.FinalReview.Status == status
		})
		review := execution.FinalReview
		if execution.Integration != nil || (reviewID != "" && (review.ID != reviewID || review.ReviewPacketSHA256 != packetSHA)) {
			t.Fatal("restart changed final review identity or prematurely completed")
		}
		reviewID, packetSHA, resultID = review.ID, review.ReviewPacketSHA256, review.ResultID
		if status != "SETTLED" {
			if err := f.store.CompleteClearDevComplexExecution(context.Background(), mailParallelCompletionCommand(execution, f.clock())); err == nil {
				t.Fatalf("completion accepted a %s final review", status)
			}
		}
	}
	f.reopen(t)
	execution = f.advance(t, false, nil)
	phase, _ := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionCompleted || execution.Integration == nil || execution.FinalReview.ResultID != resultID || execution.FinalReview.Verdict != "PASS" {
		t.Fatalf("restart lost settled final PASS: phase=%s final=%+v", phase, execution.FinalReview)
	}
	if f.harness.freezeCalls != 3 || f.harness.composeCalls != 2 || f.harness.mailDeliveryCalls != 1 || len(f.chat.intervals) != 3 {
		t.Fatalf("restart repeated effects: freezes=%d compose=%d delivery=%d turns=%d", f.harness.freezeCalls, f.harness.composeCalls, f.harness.mailDeliveryCalls, len(f.chat.intervals))
	}
	builders, reviewers, finalReviewers := 0, 0, 0
	for _, cfg := range f.harness.spawnConfigs {
		switch {
		case strings.Contains(cfg.Branch, "cleardev-complex-builder-"):
			builders++
		case strings.Contains(cfg.Branch, "cleardev-complex-review-"):
			reviewers++
		case strings.Contains(cfg.Branch, "cleardev-requirement-final-review-"):
			finalReviewers++
		}
	}
	if builders != 2 || reviewers != 3 || finalReviewers != 1 {
		t.Fatalf("restart duplicated roles: builders=%d reviewers=%d final=%d", builders, reviewers, finalReviewers)
	}
}

func TestMailV2AutomaticHandoffRequiresBoundedMailIdentity(t *testing.T) {
	for _, mode := range []string{"legacy-budget-disabled", "non-mail"} {
		t.Run(mode, func(t *testing.T) {
			f := newMailParallelBoundaryFixture(t)
			if mode == "non-mail" {
				f.harness.mail = false
			}
			service := parallelTestServiceWithOptions(f.store, f.harness, func(deps *cleardevsvc.Deps) {
				deps.AutoAdvanceComplexPlans = true
				deps.BoundedMailAttempts = mode != "legacy-budget-disabled"
			})
			if err := service.ResumeComplexFlows(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.prep.RequirementID)
			if err != nil || found || len(f.harness.spawnConfigs) != 0 {
				t.Fatalf("automatic multi-task admission escaped the bounded mail contract: found=%v spawns=%d err=%v", found, len(f.harness.spawnConfigs), err)
			}
		})
	}
}

func TestMailV2AutomaticHandoffCompletedWakeIsReadOnly(t *testing.T) {
	f := newMailParallelBoundaryFixture(t)
	execution := f.advance(t, true, nil)
	spawns := len(f.harness.spawnConfigs)
	f.reopen(t)
	service := parallelTestServiceWithOptions(f.store, f.harness, func(deps *cleardevsvc.Deps) {
		deps.AutoAdvanceComplexPlans = true
		deps.NewID, deps.Clock = f.ids.New, f.clock
	})
	for i := 0; i < 3; i++ {
		if err := service.ResumeComplexFlows(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	after := f.snapshot(t)
	if after.Run.ID != execution.Run.ID || after.Run.Mode != core.WorkModeParallel || after.Integration == nil || after.Integration.ID != execution.Integration.ID || len(f.harness.spawnConfigs) != spawns || f.harness.freezeCalls != 3 || f.harness.composeCalls != 2 || f.harness.mailDeliveryCalls != 1 {
		t.Fatal("completed automatic wake created another execution or repeated an external effect")
	}
}

func TestMailV2ThreeReadyTasksUseTwoComposedWaves(t *testing.T) {
	f := newMailParallelBoundaryFixtureWithSeed(t, cleardevtest.SeedMailIndependentV2)
	execution := f.advance(t, true, nil)
	phase, reason := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionCompleted || execution.Run.FixedBuilderCount != 2 || len(execution.Batches) != 2 || len(execution.Batches[0].TaskKeys) != 2 || len(execution.Batches[1].TaskKeys) != 1 || len(execution.Dispatches) != 3 || len(execution.Compositions) != 2 {
		t.Fatalf("three ready tasks did not complete in bounded waves: phase=%s reason=%s batches=%+v", phase, reason, execution.Batches)
	}
	for _, task := range execution.Tasks {
		if len(task.DependencyTaskKeys) != 0 {
			t.Fatal("fixture accidentally serialized the third task through a declared dependency")
		}
	}
	first := execution.Compositions[0]
	lastDispatch := execution.Dispatches[2]
	if lastDispatch.BaseCommitSHA != first.OutputCommitSHA || first.SettledAt == nil || !lastDispatch.CreatedAt.After(*first.SettledAt) || f.harness.prepareCalls != 1 || f.harness.freezeCalls != 3 {
		t.Fatal("reused slot did not wait for the prior trusted composition")
	}
}

func TestMailV2OverlappingPathsExecuteSeriallyThroughFinalReview(t *testing.T) {
	f := newMailParallelBoundaryFixtureForPaths(t, true)
	execution := f.advance(t, true, nil)
	phase, reason := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionCompleted || execution.Run.Mode != core.WorkModeStandard || execution.Run.FixedBuilderCount != 1 || len(execution.Dispatches) != 3 || len(execution.Compositions) != 0 || execution.FinalReview == nil || execution.FinalReview.Verdict != "PASS" {
		t.Fatalf("overlap did not complete safely in STANDARD: phase=%s reason=%s run=%+v", phase, reason, execution.Run)
	}
	for i := 1; i < len(execution.Dispatches); i++ {
		prior, current := execution.Dispatches[i-1], execution.Dispatches[i]
		if current.BaseCommitSHA != prior.CandidateCommitSHA || f.chat.intervals[i-1].end >= f.chat.intervals[i].start {
			t.Fatal("overlapping tasks ran concurrently or did not inherit the verified predecessor")
		}
	}
	builders := 0
	for _, cfg := range f.harness.spawnConfigs {
		if strings.Contains(cfg.Branch, "cleardev-complex-builder-") {
			builders++
		}
	}
	if builders != 1 || len(f.chat.intervals) != 3 || f.harness.freezeCalls != 3 || f.harness.mailDeliveryCalls != 1 {
		t.Fatalf("overlap fanout/effects: builders=%d turns=%d freezes=%d finalChecks=%d", builders, len(f.chat.intervals), f.harness.freezeCalls, f.harness.mailDeliveryCalls)
	}
}

func TestMailV2ParallelReviewerRequestedChecksUseTaskCandidate(t *testing.T) {
	f := newMailParallelBoundaryFixture(t)
	requested := map[domain.SessionID]bool{}
	f.harness.responseOverride = func(session domain.SessionID, kind, response string) string {
		if kind == "LOCAL_REVIEW" && !requested[session] {
			requested[session] = true
			return `{"schemaVersion":1,"kind":"REVIEW_CHECK_REQUEST","checkIds":["demo-api","demo-frontend"],"summary":"Verify the current task candidate before integration."}`
		}
		return response
	}
	execution := f.advance(t, true, nil)
	phase, reason := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionCompleted || len(requested) != 3 || execution.FinalReview == nil || execution.FinalReview.Verdict != "PASS" {
		t.Fatalf("V2 supplemental checks failed: phase=%s reason=%s requests=%d", phase, reason, len(requested))
	}
	for _, review := range execution.Reviews {
		request, receipts, found, err := f.store.GetClearDevReviewerChecks(context.Background(), review.ID)
		if err != nil || !found || len(receipts) != 2 || request.CandidateSHA != review.CandidateCommitSHA {
			t.Fatalf("task requested checks lost their candidate: request=%+v receipts=%d err=%v", request, len(receipts), err)
		}
		for _, receipt := range receipts {
			if receipt.Proof.CandidateSHA != review.CandidateCommitSHA || !strings.Contains(execution.FinalReview.ReviewPacketJSON, receipt.Proof.RunID) {
				t.Fatal("Final Reviewer packet omitted a task-bound supplemental receipt")
			}
		}
	}
}

func TestMailV2ParallelReviewerRepairBudgetStops(t *testing.T) {
	f := newMailParallelBoundaryFixture(t)
	reworks := 0
	f.harness.responseOverride = func(session domain.SessionID, kind, response string) string {
		if kind != "LOCAL_REVIEW" || reworks == 2 {
			return response
		}
		reviewer := f.harness.workspaces[f.harness.sessionWorkspace[session]]
		for _, workspace := range f.harness.workspaces {
			if !workspace.reviewer && workspace.headSHA == reviewer.headSHA && len(workspace.paths) != 0 && workspace.paths[0].Path == "frontend/app.js" {
				reworks++
				response = strings.Replace(response, `"verdict":"PASS"`, `"verdict":"REWORK"`, 1)
				response = strings.Replace(response, `"reasonCode":"REVIEW_PASSED"`, `"reasonCode":"REVIEW_CHANGES_REQUIRED"`, 1)
				return strings.Replace(response, `"findings":[]`, `"findings":[{"severity":"BLOCKING","path":"frontend/app.js","message":"Exercise the existing bounded task rework policy."}]`, 1)
			}
		}
		return response
	}
	execution := f.advance(t, true, nil)
	phase, reason := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionNeedsHuman || reworks != 2 || len(execution.Dispatches) != 3 || execution.Integration != nil || execution.FinalReview != nil {
		t.Fatalf("mail repair budget did not stop: phase=%s reason=%s reworks=%d dispatches=%+v", phase, reason, reworks, execution.Dispatches)
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), execution.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	byTask := map[string]int{}
	for _, slot := range slots {
		byTask[slot.TaskID]++
	}
	for _, task := range execution.Tasks {
		want := 1
		switch task.TaskKey {
		case "frontend-state":
			want = 2
		case "frontend-followup":
			want = 0
		}
		if byTask[task.ID] != want {
			t.Fatalf("task %s consumed another task's budget: slots=%d want=%d", task.TaskKey, byTask[task.ID], want)
		}
	}
}

func TestMailV2ParallelUsesTaskLocalDevelopmentBudget(t *testing.T) {
	f := newMailParallelBoundaryFixture(t)
	failedCandidates := map[string]bool{}
	f.harness.checkFailureFor = func(request ports.ClearDevCheckRequest) bool {
		workspace := f.harness.workspaces[request.WorkspacePath]
		if workspace == nil || len(workspace.paths) == 0 || workspace.paths[0].Path != "frontend/app.js" {
			return false
		}
		if failedCandidates[request.CandidateSHA] {
			return true
		}
		if len(failedCandidates) < 2 {
			failedCandidates[request.CandidateSHA] = true
			return true
		}
		return false
	}
	execution := f.advance(t, true, nil)
	phase, reason := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionCompleted || len(failedCandidates) != 2 || len(execution.Dispatches) != 5 || len(execution.Verifications) != 3 {
		t.Fatalf("task-local development budget failed: phase=%s reason=%s dispatches=%+v", phase, reason, execution.Dispatches)
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), execution.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	byTask := map[string]int{}
	for _, slot := range slots {
		if slot.Kind != core.MailAttemptDevelopment {
			t.Fatalf("check failure consumed a non-development budget: %+v", slot)
		}
		byTask[slot.TaskID]++
	}
	for _, task := range execution.Tasks {
		want := 1
		if task.TaskKey == "frontend-state" {
			want = 3
		}
		if byTask[task.ID] != want {
			t.Fatalf("task %s consumed another task's budget: slots=%d want=%d", task.TaskKey, byTask[task.ID], want)
		}
	}
}

func TestMailV2ParallelFinalNonPassCannotComplete(t *testing.T) {
	for _, verdict := range []string{"REWORK", "BLOCKED", "NEEDS_HUMAN"} {
		t.Run(verdict, func(t *testing.T) {
			f := newMailParallelBoundaryFixture(t)
			f.harness.responseOverride = func(_ domain.SessionID, kind, response string) string {
				if kind == "REQUIREMENT_FINAL_REVIEW_RESULT" {
					return strings.Replace(response, `"verdict":"PASS"`, `"verdict":"`+verdict+`"`, 1)
				}
				return response
			}
			execution := f.advance(t, true, nil)
			phase, _ := core.DeriveComplexExecutionPhase(execution)
			if execution.FinalReview == nil || execution.FinalReview.Verdict != verdict || execution.Integration != nil || phase == core.ComplexExecutionCompleted {
				t.Fatalf("non-PASS final review was lost or completed: phase=%s review=%+v", phase, execution.FinalReview)
			}
			if err := f.store.CompleteClearDevComplexExecution(context.Background(), mailParallelCompletionCommand(execution, f.clock())); err == nil {
				t.Fatal("non-PASS final review passed the completion transaction")
			}
		})
	}
}

func TestMailV2ParallelChangedFinalCandidateCannotReusePass(t *testing.T) {
	f := newMailParallelBoundaryFixture(t)
	execution := f.advance(t, true, func(s core.ComplexExecutionSnapshot) bool {
		return s.FinalReview != nil && s.FinalReview.Status == "SETTLED"
	})
	before := *execution.FinalReview
	command := mailParallelCompletionCommand(execution, f.clock())
	command.Integration.CandidateCommitSHA = forty("f")
	if err := f.store.CompleteClearDevComplexExecution(context.Background(), command); err == nil {
		t.Fatal("old final PASS accepted another integration SHA")
	}
	composition := execution.Compositions[len(execution.Compositions)-1]
	f.harness.workspaces[composition.WorkspacePath].headSHA = forty("f")
	f.reopen(t)
	execution = f.advance(t, false, nil)
	phase, _ := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionBlocked || execution.Integration != nil || execution.FinalReview.ResultID != before.ResultID || execution.FinalReview.Verdict != "PASS" {
		t.Fatalf("source drift failed to preserve and invalidate old PASS: phase=%s final=%+v", phase, execution.FinalReview)
	}
	f.harness.workspaces[composition.WorkspacePath].headSHA = composition.OutputCommitSHA
	f.reopen(t)
	execution = f.advance(t, false, nil)
	if execution.Integration != nil {
		t.Fatal("restoring the old tree silently reused invalidated final evidence")
	}
	if err := f.store.CompleteClearDevComplexExecution(context.Background(), mailParallelCompletionCommand(execution, f.clock())); err == nil {
		t.Fatal("restoring the old tree bypassed durable final evidence invalidation")
	}
}
