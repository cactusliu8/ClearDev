package store_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// TestComplexExecutionStoreTwoTaskFlow uses the public S04/S06 store APIs to
// prove the whole STANDARD hand-off.  In particular, the second task cannot
// start until the first verification exists and it must inherit that verified
// candidate as its immutable baseline.
func TestComplexExecutionStoreTwoTaskFlow(t *testing.T) {
	t.Run("terminated original Steward gets one atomic continuation", func(t *testing.T) {
		store, state := seedComplexExecutionFlow(t)
		ctx := context.Background()
		db, err := sql.Open("sqlite", "file:"+filepath.Join(state.dataDir, "ao.db")+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE sessions SET is_terminated = TRUE WHERE id = ?`, state.prep.StewardSessionID); err != nil {
			_ = db.Close()
			t.Fatalf("terminate original Steward session: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatalf("read execution before continuation: ok=%t err=%v", ok, err)
		}
		var prior core.ComplexExecutionRoleBinding
		for _, binding := range snapshot.RoleBindings {
			if binding.Role == core.StandardRoleSteward && binding.Status == core.RoleBindingStatusBound {
				prior = binding
			}
		}
		if prior.ID == "" {
			t.Fatal("bound original Steward is missing")
		}
		at := state.now.Add(70 * time.Second)
		continuation := core.ComplexExecutionRoleBinding{ID: "s06-flow-steward-continuation", ExecutionRunID: state.run.ID, SourceComplexRoleBindingID: prior.SourceComplexRoleBindingID, ContinuationOfRoleBindingID: prior.ID, Role: core.StandardRoleSteward, SessionCreationIdempotencyKey: "s06-flow-steward-continuation-key", Status: core.RoleBindingStatusRequested, RequestedAt: at}
		step := core.AgentStep{ID: "s06-flow-steward-continuation-step", RoleBindingID: continuation.ID, Kind: core.AgentStepDispatchRequest, RequestID: state.run.ID, ClientMessageID: "s06-flow-steward-continuation-client", PromptSHA256: strings.Repeat("9", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: at}
		if changed, err := store.ContinueClearDevComplexExecutionSteward(ctx, prior.ID, continuation, step, at); err != nil || !changed {
			t.Fatalf("continue terminated Steward: changed=%t err=%v", changed, err)
		}
		snapshot, ok, err = store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatalf("read Steward continuation: ok=%t err=%v", ok, err)
		}
		active, ended := 0, false
		for _, binding := range snapshot.RoleBindings {
			if binding.Role != core.StandardRoleSteward {
				continue
			}
			if binding.ID == prior.ID && binding.Status == core.RoleBindingStatusEnded {
				ended = true
			}
			if binding.Status == core.RoleBindingStatusRequested || binding.Status == core.RoleBindingStatusBound {
				active++
			}
		}
		if !ended || active != 1 {
			t.Fatalf("Steward continuation roles = %#v", snapshot.RoleBindings)
		}
	})

	t.Run("Reviewer infrastructure failure blocks task and role atomically", func(t *testing.T) {
		store, state := seedComplexExecutionFlow(t)
		ctx := context.Background()
		task := state.tasks[0]
		dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "dispatch-reviewer-failure")
		if err != nil {
			t.Fatal(err)
		}
		const candidateID = "candidate-reviewer-failure"
		candidateSHA := strings.Repeat("b", 40)
		if _, err := store.AppendClearDevComplexExecutionCandidate(ctx, core.AppendComplexExecutionCandidateCommand{ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, Round: 0, BaseCommitSHA: state.initialBase, Candidate: core.CandidateObservation{ID: candidateID, DevelopmentTaskID: task.DevelopmentTaskID, AOSessionID: state.builder.AOSessionID, CommitSHA: candidateSHA, ObservedAt: state.now.Add(10 * time.Second)}}); err != nil {
			t.Fatal(err)
		}
		reviewer := core.ComplexExecutionRoleBinding{ID: "reviewer-failure-binding", ExecutionRunID: state.run.ID, Role: core.StandardRoleReviewer, TaskMappingID: task.ID, CandidateCommitID: candidateID, SessionCreationIdempotencyKey: "reviewer-failure-key", Status: core.RoleBindingStatusRequested, RequestedAt: state.now.Add(11 * time.Second)}
		if _, created, err := store.CreateClearDevComplexExecutionRoleBinding(ctx, reviewer); err != nil || !created {
			t.Fatalf("create Reviewer failure binding: created=%t err=%v", created, err)
		}
		at := state.now.Add(12 * time.Second)
		if err := store.ApplyClearDevComplexExecutionFailure(ctx, state.run.ID, task.ID, dispatch.ID, true, core.ReasonCode("REVIEWER_UNAVAILABLE"), at); err != nil {
			t.Fatalf("apply Reviewer infrastructure failure: %v", err)
		}
		snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatalf("read blocked execution: ok=%t err=%v", ok, err)
		}
		var blockedTask core.ComplexExecutionTask
		for _, item := range snapshot.Tasks {
			if item.ID == task.ID {
				blockedTask = item
			}
		}
		var failedReviewer core.ComplexExecutionRoleBinding
		for _, binding := range snapshot.RoleBindings {
			if binding.ID == reviewer.ID {
				failedReviewer = binding
			}
		}
		if blockedTask.Status != core.DevelopmentTaskStatusBlocked || failedReviewer.Status != core.RoleBindingStatusFailed || failedReviewer.ReasonCode != core.ReasonCode("REVIEWER_UNAVAILABLE") {
			t.Fatalf("atomic Reviewer failure facts: task=%#v reviewer=%#v", blockedTask, failedReviewer)
		}
		requirement, ok, err := store.GetClearDevRequirement(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatalf("read blocked work item: ok=%t err=%v", ok, err)
		}
		for _, item := range requirement.DevelopmentTasks {
			if item.ID == task.DevelopmentTaskID {
				if item.Status != core.DevelopmentTaskStatusBlocked || item.PausedFromStatus != core.DevelopmentTaskStatusRunning {
					t.Fatalf("blocked work item did not retain pause source: %#v", item)
				}
				return
			}
		}
		t.Fatalf("blocked work item %s missing", task.DevelopmentTaskID)
	})

	t.Run("invalid Builder result without candidate stops safely", func(t *testing.T) {
		store, state := seedComplexExecutionFlow(t)
		ctx := context.Background()
		task := state.tasks[0]
		dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "dispatch-invalid-builder-result")
		if err != nil {
			t.Fatal(err)
		}
		at := state.now.Add(10 * time.Second)
		if err := store.ApplyClearDevComplexExecutionFailure(ctx, state.run.ID, task.ID, dispatch.ID, false, core.ReasonCode("BUILDER_RESULT_INVALID"), at); err != nil {
			t.Fatalf("apply invalid Builder result: %v", err)
		}
		snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatalf("read invalid Builder stop: ok=%t err=%v", ok, err)
		}
		for _, item := range snapshot.Tasks {
			if item.ID == task.ID && item.Status != core.DevelopmentTaskStatusNeedsHuman {
				t.Fatalf("invalid Builder result task = %#v", item)
			}
		}
		for _, item := range snapshot.Dispatches {
			if item.ID == dispatch.ID && (item.Status != core.ComplexExecutionDispatchNeedsHuman || item.ReasonCode != core.ReasonCode("BUILDER_RESULT_INVALID")) {
				t.Fatalf("invalid Builder result dispatch = %#v", item)
			}
		}
		requirement, ok, err := store.GetClearDevRequirement(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatalf("read invalid Builder work item: ok=%t err=%v", ok, err)
		}
		for _, item := range requirement.DevelopmentTasks {
			if item.ID == task.DevelopmentTaskID && (item.Status != core.DevelopmentTaskStatusNeedsHuman || item.PausedFromStatus != core.DevelopmentTaskStatusRunning) {
				t.Fatalf("invalid Builder work item = %#v", item)
			}
		}
	})

	t.Run("pending Reviewer turn failure settles every fact atomically", func(t *testing.T) {
		store, state := seedComplexExecutionFlow(t)
		ctx := context.Background()
		task := state.tasks[0]
		dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "dispatch-reviewer-turn-failure")
		if err != nil {
			t.Fatal(err)
		}
		const candidateID = "candidate-reviewer-turn-failure"
		candidateSHA := strings.Repeat("b", 40)
		if _, err := store.AppendClearDevComplexExecutionCandidate(ctx, core.AppendComplexExecutionCandidateCommand{ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, Round: 0, BaseCommitSHA: state.initialBase, Candidate: core.CandidateObservation{ID: candidateID, DevelopmentTaskID: task.DevelopmentTaskID, AOSessionID: state.builder.AOSessionID, CommitSHA: candidateSHA, ObservedAt: state.now.Add(10 * time.Second)}}); err != nil {
			t.Fatal(err)
		}
		reviewerSession := createComplexExecutionSession(t, store, state.projectID, "reviewer-turn-failure-session", "reviewer-turn-failure-key", "/worktrees/reviewer-turn-failure", state.initialBase, state.now.Add(11*time.Second))
		reviewer := core.ComplexExecutionRoleBinding{ID: "reviewer-turn-failure-binding", ExecutionRunID: state.run.ID, Role: core.StandardRoleReviewer, TaskMappingID: task.ID, CandidateCommitID: candidateID, SessionCreationIdempotencyKey: "reviewer-turn-failure-key", Status: core.RoleBindingStatusRequested, RequestedAt: state.now.Add(11 * time.Second)}
		if _, created, err := store.CreateClearDevComplexExecutionRoleBinding(ctx, reviewer); err != nil || !created {
			t.Fatalf("create Reviewer turn failure binding: created=%t err=%v", created, err)
		}
		if changed, err := store.BindClearDevComplexExecutionRoleBinding(ctx, reviewer.ID, string(reviewerSession.ID), "/worktrees/reviewer-turn-failure", candidateSHA, state.now.Add(12*time.Second)); err != nil || !changed {
			t.Fatalf("bind Reviewer turn failure: changed=%t err=%v", changed, err)
		}
		reviewer.AOSessionID, reviewer.WorkspacePath, reviewer.BaseCommitSHA = string(reviewerSession.ID), "/worktrees/reviewer-turn-failure", candidateSHA
		reviewer.Status = core.RoleBindingStatusBound
		step := core.AgentStep{ID: "reviewer-turn-failure-step", RoleBindingID: reviewer.ID, Kind: core.AgentStepLocalReview, RequestID: "reviewer-turn-failure-review", ClientMessageID: "reviewer-turn-failure-client", PromptSHA256: strings.Repeat("f", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: state.now.Add(13 * time.Second)}
		review := core.ComplexExecutionReview{ID: step.RequestID, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: candidateID, CandidateCommitSHA: candidateSHA, BaseCommitSHA: state.initialBase, ReviewPacketJSON: "{}", ReviewPacketSHA256: strings.Repeat("e", 64), CandidateWorkspacePath: reviewer.WorkspacePath, ReviewerRoleBindingID: reviewer.ID, AgentStepID: step.ID, Status: core.LocalReviewStatusPending, CreatedAt: step.RequestedAt}
		if _, created, err := store.CreateClearDevComplexExecutionReview(ctx, core.CreateComplexExecutionReviewCommand{Review: review, ReviewerBinding: reviewer, AgentStep: step}); err != nil || !created {
			t.Fatalf("create pending Reviewer turn: created=%t err=%v", created, err)
		}
		if changed, err := store.MarkClearDevComplexExecutionAgentStepSent(ctx, step.ID, state.now.Add(14*time.Second)); err != nil || !changed {
			t.Fatalf("mark Reviewer turn sent: changed=%t err=%v", changed, err)
		}
		at := state.now.Add(15 * time.Second)
		if err := store.ApplyClearDevComplexExecutionFailure(ctx, state.run.ID, task.ID, dispatch.ID, true, core.ReasonCode("REVIEWER_UNAVAILABLE"), at); err != nil {
			t.Fatalf("apply pending Reviewer turn failure: %v", err)
		}
		snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatalf("read pending Reviewer turn failure: ok=%t err=%v", ok, err)
		}
		for _, persisted := range snapshot.AgentSteps {
			if persisted.ID == step.ID && (persisted.SendStatus != core.AgentStepSendStatusFailed || persisted.ReasonCode != core.ReasonCode("REVIEWER_UNAVAILABLE")) {
				t.Fatalf("Reviewer Agent step was not failed atomically: %#v", persisted)
			}
		}
		for _, persisted := range snapshot.Reviews {
			if persisted.ID == review.ID && (persisted.Status != core.LocalReviewStatusFailed || persisted.ReasonCode != core.ReasonCode("REVIEWER_UNAVAILABLE")) {
				t.Fatalf("Reviewer review was not failed atomically: %#v", persisted)
			}
		}
		for _, persisted := range snapshot.RoleBindings {
			if persisted.ID == reviewer.ID && (persisted.Status != core.RoleBindingStatusEnded || persisted.ReasonCode != core.ReasonCode("REVIEWER_UNAVAILABLE")) {
				t.Fatalf("Reviewer role was not ended atomically: %#v", persisted)
			}
		}
		for _, persisted := range snapshot.Tasks {
			if persisted.ID == task.ID && persisted.Status != core.DevelopmentTaskStatusBlocked {
				t.Fatalf("Reviewer turn failure task = %#v", persisted)
			}
		}
	})

	t.Run("two tasks reach atomic DONE", func(t *testing.T) {
		store, state := seedComplexExecutionFlow(t)
		ctx := context.Background()

		first := state.tasks[0]
		second := state.tasks[1]
		if _, _, err := dispatchComplexExecutionTask(ctx, store, state, second, 0, state.initialBase, "dispatch-two-before-one"); err == nil {
			t.Fatal("dependent task dispatched before predecessor verification")
		}

		firstCandidate := runVerifiedComplexExecutionTask(t, store, state, first, 0, state.initialBase, strings.Repeat("b", 40))
		secondCandidate := runVerifiedComplexExecutionTask(t, store, state, second, 0, firstCandidate, strings.Repeat("c", 40))
		integrationCheckID := settleIntegrationCheck(t, store, state, secondCandidate)

		at := state.now.Add(50 * time.Second)
		err := store.CompleteClearDevComplexExecution(ctx, core.CompleteComplexExecutionCommand{
			ExecutionRunID: state.run.ID,
			Integration: core.ComplexExecutionIntegration{
				ID: "s06-flow-integration", ExecutionRunID: state.run.ID,
				IntegrationCandidateID: "s06-flow-integration-candidate", CandidateCommitSHA: secondCandidate,
				CheckRunIDs: []string{integrationCheckID}, CompletedAt: at,
			},
			At: at,
		})
		if err != nil {
			t.Fatalf("complete verified S06 flow: %v", err)
		}

		snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatalf("read completed S06 flow: ok=%t err=%v", ok, err)
		}
		if snapshot.Integration == nil || snapshot.Integration.CandidateCommitSHA != secondCandidate {
			t.Fatalf("integration fact = %#v, want final candidate %s", snapshot.Integration, secondCandidate)
		}
		if got, reason := core.DeriveComplexExecutionPhase(snapshot); got != core.ComplexExecutionCompleted || reason != core.ReasonNone {
			t.Fatalf("derived completed phase = %s/%s", got, reason)
		}
		if missing := core.DeriveComplexExecutionMissingEvidence(snapshot); len(missing) != 0 {
			t.Fatalf("completed flow missing evidence = %v", missing)
		}
		for _, task := range snapshot.Tasks {
			if task.Status != core.DevelopmentTaskStatusDone {
				t.Fatalf("task %s state = %s, want DONE", task.TaskKey, task.Status)
			}
		}
		expectedCandidates := map[string]string{
			"dispatch-" + first.ID:  firstCandidate,
			"dispatch-" + second.ID: secondCandidate,
		}
		seenCandidates := map[string]bool{}
		for _, dispatch := range snapshot.Dispatches {
			if expectedSHA, relevant := expectedCandidates[dispatch.ID]; relevant && (dispatch.CandidateCommitID != "candidate-"+dispatch.ComplexExecutionTaskID || dispatch.CandidateCommitSHA != expectedSHA) {
				t.Fatalf("dispatch %s candidate readback = id:%q sha:%q, want id:%q sha:%q", dispatch.ID, dispatch.CandidateCommitID, dispatch.CandidateCommitSHA, "candidate-"+dispatch.ComplexExecutionTaskID, expectedSHA)
			} else if relevant {
				seenCandidates[dispatch.ID] = true
			}
		}
		if len(seenCandidates) != len(expectedCandidates) {
			t.Fatalf("dispatch candidate readback missing: seen=%v expected=%v", seenCandidates, expectedCandidates)
		}
		if len(snapshot.Verifications) != 2 || snapshot.Dispatches[1].BaseCommitSHA != firstCandidate {
			t.Fatalf("verification/base chain = verifications:%#v dispatches:%#v", snapshot.Verifications, snapshot.Dispatches)
		}
		requirement, ok, err := store.GetClearDevRequirement(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatalf("read integration evidence: ok=%t err=%v", ok, err)
		}
		foundIntegrationEvidence := false
		for _, evidence := range requirement.Evidence {
			if evidence.Kind == core.EvidenceKindIntegration && evidence.Result == core.EvidenceResultPass && evidence.CommitSHA == secondCandidate && evidence.IntegrationCandidateID == snapshot.Integration.IntegrationCandidateID {
				foundIntegrationEvidence = true
			}
		}
		if !foundIntegrationEvidence {
			t.Fatalf("integration evidence does not bind the final candidate: %#v", requirement.Evidence)
		}
	})

	t.Run("task-set change rolls completion back", func(t *testing.T) {
		store, state := seedComplexExecutionFlow(t)
		ctx := context.Background()
		firstCandidate := runVerifiedComplexExecutionTask(t, store, state, state.tasks[0], 0, state.initialBase, strings.Repeat("b", 40))
		secondCandidate := runVerifiedComplexExecutionTask(t, store, state, state.tasks[1], 0, firstCandidate, strings.Repeat("c", 40))
		integrationCheckID := settleIntegrationCheck(t, store, state, secondCandidate)

		if err := store.CreateClearDevTask(ctx, core.InitialDevelopmentTask{
			Task:       core.DevelopmentTask{ID: "s06-flow-unrelated-task", DevelopmentRequirementID: state.prep.RequirementID, Title: "late task", Mode: core.WorkModeQuick, Status: core.DevelopmentTaskStatusPlanned, MaxReworkCount: 0, CreatedAt: state.now.Add(60 * time.Second), UpdatedAt: state.now.Add(60 * time.Second)},
			Permission: core.PermissionVersion{ID: "s06-flow-unrelated-permission", DevelopmentTaskID: "s06-flow-unrelated-task", Version: 1, Rules: core.PathRules{WritePaths: []string{"src/**"}, ForbiddenPaths: []string{".git/**"}}, CreatedAt: state.now.Add(60 * time.Second)},
			Checks:     []core.RequiredCheck{{ID: "s06-flow-unrelated-check", DevelopmentTaskID: "s06-flow-unrelated-task", Name: "all-tests", Kind: "command", CreatedAt: state.now.Add(60 * time.Second)}},
		}); err != nil {
			t.Fatalf("change v2 task set through public API: %v", err)
		}
		err := store.CompleteClearDevComplexExecution(ctx, core.CompleteComplexExecutionCommand{
			ExecutionRunID: state.run.ID,
			Integration:    core.ComplexExecutionIntegration{ID: "s06-flow-rollback-integration", ExecutionRunID: state.run.ID, IntegrationCandidateID: "s06-flow-rollback-candidate", CandidateCommitSHA: secondCandidate, CheckRunIDs: []string{integrationCheckID}},
			At:             state.now.Add(61 * time.Second),
		})
		if err == nil {
			t.Fatal("completion succeeded after the frozen v2 task set changed")
		}
		snapshot, ok, readErr := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if readErr != nil || !ok {
			t.Fatalf("read rollback result: ok=%t err=%v", ok, readErr)
		}
		if snapshot.Integration != nil || snapshot.Run.CompletedAt != nil {
			t.Fatalf("failed completion persisted integration/run completion: %#v", snapshot)
		}
		for _, task := range snapshot.Tasks {
			if task.Status != core.DevelopmentTaskStatusReview {
				t.Fatalf("failed completion changed task %s to %s", task.TaskKey, task.Status)
			}
		}
	})

	t.Run("finalizer failure rolls every DONE transition back", func(t *testing.T) {
		store, state := seedComplexExecutionFlow(t)
		ctx := context.Background()
		firstCandidate := runVerifiedComplexExecutionTask(t, store, state, state.tasks[0], 0, state.initialBase, strings.Repeat("b", 40))
		secondCandidate := runVerifiedComplexExecutionTask(t, store, state, state.tasks[1], 0, firstCandidate, strings.Repeat("c", 40))
		integrationCheckID := settleIntegrationCheck(t, store, state, secondCandidate)

		injectFinalizerAbort(t, filepath.Join(state.dataDir, "ao.db"), state.tasks[1].DevelopmentTaskID)
		err := store.CompleteClearDevComplexExecution(ctx, core.CompleteComplexExecutionCommand{
			ExecutionRunID: state.run.ID,
			Integration:    core.ComplexExecutionIntegration{ID: "s06-flow-injected-failure", ExecutionRunID: state.run.ID, IntegrationCandidateID: "s06-flow-injected-candidate", CandidateCommitSHA: secondCandidate, CheckRunIDs: []string{integrationCheckID}},
			At:             state.now.Add(70 * time.Second),
		})
		if err == nil {
			t.Fatal("injected finalizer failure completed S06 execution")
		}
		snapshot, ok, readErr := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if readErr != nil || !ok {
			t.Fatalf("read injected rollback: ok=%t err=%v", ok, readErr)
		}
		if snapshot.Integration != nil || snapshot.Run.CompletedAt != nil {
			t.Fatalf("injected failure persisted a completion fact: %#v", snapshot)
		}
		for _, task := range snapshot.Tasks {
			if task.Status == core.DevelopmentTaskStatusDone {
				t.Fatalf("injected failure left task %s DONE", task.TaskKey)
			}
		}
	})
}

type complexExecutionFlowState struct {
	prep        cleardevtest.ComplexStandardPrep
	projectID   string
	dataDir     string
	run         core.ComplexExecutionRun
	tasks       []core.ComplexExecutionTask
	checkSpecs  []core.ComplexExecutionCheckSpecFact
	builder     core.ComplexExecutionRoleBinding
	initialBase string
	now         time.Time
}

func seedComplexExecutionFlow(t *testing.T) (*sqlite.Store, complexExecutionFlowState) {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initComplexExecutionTestRepo(t)
	const projectID = "ao-s06-store-flow"
	prep := cleardevtest.SeedComplexStandardV2(t, store, projectID, repo, dataDir)
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

	requirement, ok, err := store.GetClearDevRequirement(ctx, prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("read S04 fixture requirement: ok=%t err=%v", ok, err)
	}
	var version core.RequirementVersion
	for _, item := range requirement.RequirementVersions {
		if item.ID == prep.V2ID {
			version = item
		}
	}
	planning, ok, err := store.GetClearDevComplexPlanning(ctx, prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("read S04 fixture planning: ok=%t err=%v", ok, err)
	}
	var plan core.ComplexEngineeringPlan
	var steward core.ComplexRoleBinding
	for _, item := range planning.Plans {
		if item.ID == prep.V2PlanID {
			plan = item
		}
	}
	for _, item := range planning.RoleBindings {
		if item.Role == core.StandardRoleSteward && item.Status == core.RoleBindingStatusBound {
			steward = item
		}
	}
	if plan.ID == "" || steward.ID == "" {
		t.Fatalf("fixture missing approved plan or bound Steward: %#v %#v", plan, steward)
	}
	var parsed core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(plan.PlanJSON), &parsed); err != nil {
		t.Fatalf("decode approved plan: %v", err)
	}

	initialBase := strings.Repeat("a", 40)
	raw, digest, err := core.BuildComplexExecutionRunPackage(core.WorkModeStandard, "s06-flow-run", version.ID, version.SHA256, plan.ID, plan.PlanSHA256)
	if err != nil {
		t.Fatalf("build run package: %v", err)
	}
	run := core.ComplexExecutionRun{ID: "s06-flow-run", DevelopmentRequirementID: prep.RequirementID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, PlanID: plan.ID, PlanReviewID: prep.V2ReviewID, PlanSHA256: plan.PlanSHA256, Mode: core.WorkModeStandard, ModeReason: "ONE_BUILDER_REQUIRED", FixedBuilderCount: 1, ExpectedTaskSetVersion: 0, TaskSetVersion: core.ComplexStandardTaskSetVersion, ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest, StewardRoleBindingID: steward.ID, CreatedAt: now}
	// The fixture's source Steward is live, so the initial S06 role preserves
	// that exact S04 session.  A REQUESTED continuation is only valid after it
	// has exited or been terminated.
	stewardBinding := core.ComplexExecutionRoleBinding{ID: "s06-flow-steward", ExecutionRunID: run.ID, SourceComplexRoleBindingID: steward.ID, Role: core.StandardRoleSteward, SessionCreationIdempotencyKey: "s06-flow-steward-continuation", AOSessionID: prep.StewardSessionID, Status: core.RoleBindingStatusBound, RequestedAt: now, BoundAt: &now}
	step := core.AgentStep{ID: "s06-flow-steward-step", RoleBindingID: stewardBinding.ID, Kind: core.AgentStepDispatchRequest, RequestID: run.ID, ClientMessageID: "s06-flow-steward-client", PromptSHA256: strings.Repeat("1", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
	if _, created, err := store.StartClearDevComplexExecution(ctx, core.StartComplexExecutionCommand{Run: run, StewardRoleBinding: stewardBinding, AgentStep: step}); err != nil || !created {
		t.Fatalf("start S06 execution: created=%t err=%v", created, err)
	}
	if runnable, err := store.ListClearDevRunnableComplexExecutions(ctx); err != nil || len(runnable) != 1 || runnable[0] != prep.RequirementID {
		t.Fatalf("restart scan must retain pending Steward request: runnable=%v err=%v", runnable, err)
	}

	builderSession := createComplexExecutionSession(t, store, projectID, "s06-flow-builder-session", "s06-flow-builder-key", "/worktrees/s06-flow-builder", initialBase, now)
	builder := core.ComplexExecutionRoleBinding{ID: "s06-flow-builder", ExecutionRunID: run.ID, Role: core.StandardRoleBuilder, BuilderSlot: 1, SessionCreationIdempotencyKey: "s06-flow-builder-key", Status: core.RoleBindingStatusRequested, RequestedAt: now}
	tasks, specs := complexExecutionMaterialization(t, run, version, parsed, now)
	if err := store.MaterializeClearDevComplexExecution(ctx, core.MaterializeComplexExecutionCommand{ExecutionRunID: run.ID, Tasks: tasks, CheckSpecs: specs, BuilderBindings: []core.ComplexExecutionRoleBinding{builder}, At: now.Add(time.Second)}); err != nil {
		t.Fatalf("materialize two S06 tasks: %v", err)
	}
	if changed, err := store.BindClearDevComplexExecutionRoleBinding(ctx, builder.ID, string(builderSession.ID), "/worktrees/s06-flow-builder", initialBase, now.Add(2*time.Second)); err != nil || !changed {
		t.Fatalf("bind S06 Builder: changed=%t err=%v", changed, err)
	}
	builder.AOSessionID, builder.WorkspacePath, builder.BaseCommitSHA = string(builderSession.ID), "/worktrees/s06-flow-builder", initialBase
	builder.Status = core.RoleBindingStatusBound
	return store, complexExecutionFlowState{prep: prep, projectID: projectID, dataDir: dataDir, run: run, tasks: tasks, checkSpecs: specs, builder: builder, initialBase: initialBase, now: now}
}

func complexExecutionMaterialization(t *testing.T, run core.ComplexExecutionRun, version core.RequirementVersion, plan core.ComplexEngineeringPlanResult, now time.Time) ([]core.ComplexExecutionTask, []core.ComplexExecutionCheckSpecFact) {
	t.Helper()
	tasks := make([]core.ComplexExecutionTask, 0, len(plan.Tasks))
	byKey := make(map[string]string, len(plan.Tasks))
	workItemByKey := make(map[string]string, len(plan.Tasks))
	for index, task := range plan.Tasks {
		byKey[task.Key] = "s06-flow-task-" + string(rune('1'+index))
		workItemByKey[task.Key] = "s06-flow-work-" + string(rune('1'+index))
	}
	for index, task := range plan.Tasks {
		deps := make([]string, 0, len(task.DependencyKeys))
		for _, key := range task.DependencyKeys {
			deps = append(deps, workItemByKey[key])
		}
		_, raw, digest, err := core.BuildComplexStandardExecutionPackage(core.WorkModeStandard, core.ComplexStandardExecutionPackageInput{ExecutionRunID: run.ID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, RequirementText: version.RequirementText, PlanID: run.PlanID, PlanSHA256: run.PlanSHA256, TaskSetVersion: core.ComplexStandardTaskSetVersion, TaskID: workItemByKey[task.Key], DependencyTaskIDs: deps, Task: task})
		if err != nil {
			t.Fatalf("build package for %s: %v", task.Key, err)
		}
		tasks = append(tasks, core.ComplexExecutionTask{ID: byKey[task.Key], ExecutionRunID: run.ID, TaskKey: task.Key, DevelopmentTaskID: workItemByKey[task.Key], Ordinal: index, DependencyTaskKeys: append([]string(nil), task.DependencyKeys...), ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest, Status: core.DevelopmentTaskStatusPlanned, CurrentRound: 0})
	}
	specs := []core.ComplexExecutionCheckSpecFact{}
	for _, task := range tasks {
		var packet core.ComplexStandardExecutionPackage
		if err := json.Unmarshal([]byte(task.ExecutionPackageJSON), &packet); err != nil {
			t.Fatalf("decode package for specs: %v", err)
		}
		specs = append(specs, core.ComplexExecutionCheckSpecFact{ID: task.ID + "-scope", ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, CheckID: "SCOPE", CheckSpecSHA256: complexExecutionTestScopeDigest(packet), Kind: core.CandidateCheckScope, Argv: []string{}, TimeoutSeconds: 0, CreatedAt: now})
		for _, check := range packet.RequiredChecks {
			specs = append(specs, core.ComplexExecutionCheckSpecFact{ID: task.ID + "-" + check.ID, ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, CheckID: check.ID, CheckSpecSHA256: complexExecutionTestDigest(check.Argv), Kind: core.CandidateCheckRequired, Argv: check.Argv, TimeoutSeconds: check.TimeoutSeconds, CreatedAt: now})
		}
	}
	integration, _ := core.FrozenComplexCheckByID("all-tests")
	specs = append(specs, core.ComplexExecutionCheckSpecFact{ID: "s06-flow-integration-check", ExecutionRunID: run.ID, CheckID: integration.ID, CheckSpecSHA256: complexExecutionTestDigest(integration.Argv), Kind: core.CandidateCheckIntegration, Argv: integration.Argv, TimeoutSeconds: integration.TimeoutSeconds, CreatedAt: now})
	return tasks, specs
}

func runVerifiedComplexExecutionTask(t *testing.T, store *sqlite.Store, state complexExecutionFlowState, task core.ComplexExecutionTask, round int, base, candidateSHA string) string {
	t.Helper()
	ctx := context.Background()
	dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, round, base, "dispatch-"+task.ID)
	if err != nil {
		t.Fatalf("dispatch %s: %v", task.TaskKey, err)
	}
	candidateID := "candidate-" + task.ID
	if _, err := store.AppendClearDevComplexExecutionCandidate(ctx, core.AppendComplexExecutionCandidateCommand{ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, Round: round, BaseCommitSHA: base, Candidate: core.CandidateObservation{ID: candidateID, DevelopmentTaskID: task.DevelopmentTaskID, AOSessionID: state.builder.AOSessionID, CommitSHA: candidateSHA, ObservedAt: state.now.Add(10 * time.Second)}}); err != nil {
		t.Fatalf("append candidate for %s: %v", task.TaskKey, err)
	}
	checkIDs := []string{}
	for _, spec := range state.checkSpecs {
		if spec.Kind == core.CandidateCheckIntegration || spec.ComplexExecutionTaskID != task.ID {
			continue
		}
		checkID := "check-" + spec.ID
		check := core.ComplexExecutionCheckRun{ID: checkID, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: candidateID, CandidateCommitSHA: candidateSHA, CheckSpecFactID: spec.ID, Kind: spec.Kind, Argv: spec.Argv, Status: core.ComplexExecutionCheckRunPending, CreatedAt: state.now.Add(11 * time.Second)}
		if _, created, err := store.CreateClearDevComplexExecutionCheckRun(ctx, check); err != nil || !created {
			t.Fatalf("create %s check: created=%t err=%v", spec.Kind, created, err)
		}
		if changed, err := store.StartClearDevComplexExecutionCheckRun(ctx, checkID, state.now.Add(12*time.Second)); err != nil || !changed {
			t.Fatalf("start %s check: changed=%t err=%v", spec.Kind, changed, err)
		}
		exit := 0
		if changed, err := store.SettleClearDevComplexExecutionCheckRun(ctx, core.SettleComplexExecutionCheckCommand{CheckRunID: checkID, Result: core.EvidenceResultPass, ContainerImageID: "cleardev-go-validation", ExitCode: &exit, OutputSummary: "pass", OutputSHA256: strings.Repeat("d", 64), ChangedPathsJSON: "[]", At: state.now.Add(13 * time.Second)}); err != nil || !changed {
			t.Fatalf("settle %s check: changed=%t err=%v", spec.Kind, changed, err)
		}
		if spec.Kind == core.CandidateCheckRequired {
			checkIDs = append(checkIDs, checkID)
		}
	}

	// AO's session diff base remains the project base; the S06 Reviewer binding
	// independently fixes the exact candidate SHA and the Git inspector proves
	// that its worktree HEAD is that clean candidate.
	reviewerSession := createComplexExecutionSession(t, store, state.projectID, "reviewer-session-"+task.ID, "reviewer-key-"+task.ID, "/worktrees/reviewer-"+task.ID, state.initialBase, state.now.Add(14*time.Second))
	reviewer := core.ComplexExecutionRoleBinding{ID: "reviewer-" + task.ID, ExecutionRunID: state.run.ID, Role: core.StandardRoleReviewer, TaskMappingID: task.ID, CandidateCommitID: candidateID, SessionCreationIdempotencyKey: "reviewer-key-" + task.ID, Status: core.RoleBindingStatusRequested, RequestedAt: state.now.Add(14 * time.Second)}
	if _, created, err := store.CreateClearDevComplexExecutionRoleBinding(ctx, reviewer); err != nil || !created {
		t.Fatalf("create Reviewer binding for %s: created=%t err=%v", task.TaskKey, created, err)
	}
	if changed, err := store.BindClearDevComplexExecutionRoleBinding(ctx, reviewer.ID, string(reviewerSession.ID), "/worktrees/reviewer-"+task.ID, candidateSHA, state.now.Add(14*time.Second)); err != nil || !changed {
		t.Fatalf("bind Reviewer for %s: changed=%t err=%v", task.TaskKey, changed, err)
	}
	reviewer.AOSessionID, reviewer.WorkspacePath, reviewer.BaseCommitSHA = string(reviewerSession.ID), "/worktrees/reviewer-"+task.ID, candidateSHA
	reviewer.Status, reviewer.BoundAt = core.RoleBindingStatusBound, ptrTime(state.now.Add(14*time.Second))
	review := core.ComplexExecutionReview{ID: "review-" + task.ID, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: candidateID, CandidateCommitSHA: candidateSHA, BaseCommitSHA: base, ReviewPacketJSON: "{}", ReviewPacketSHA256: strings.Repeat("e", 64), CandidateWorkspacePath: reviewer.WorkspacePath, ReviewerRoleBindingID: reviewer.ID, AgentStepID: "review-step-" + task.ID, Status: core.LocalReviewStatusPending, CreatedAt: state.now.Add(15 * time.Second)}
	reviewStep := core.AgentStep{ID: review.AgentStepID, RoleBindingID: reviewer.ID, Kind: core.AgentStepLocalReview, RequestID: review.ID, ClientMessageID: "review-client-" + task.ID, PromptSHA256: strings.Repeat("f", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: review.CreatedAt}
	if _, created, err := store.CreateClearDevComplexExecutionReview(ctx, core.CreateComplexExecutionReviewCommand{Review: review, ReviewerBinding: reviewer, AgentStep: reviewStep}); err != nil || !created {
		t.Fatalf("create review for %s: created=%t err=%v", task.TaskKey, created, err)
	}
	if changed, err := store.MarkClearDevComplexExecutionAgentStepSent(ctx, reviewStep.ID, state.now.Add(15*time.Second)); err != nil || !changed {
		t.Fatalf("send review step for %s: changed=%t err=%v", task.TaskKey, changed, err)
	}
	settledAt := state.now.Add(16 * time.Second)
	reviewStep.SendStatus, reviewStep.TurnID, reviewStep.FinalMessageID, reviewStep.FinalMessageText = core.AgentStepSendStatusSettled, "review-turn-"+task.ID, "review-message-"+task.ID, "approved"
	reviewStep.MessageSHA256, reviewStep.CompletedAt = strings.Repeat("3", 64), &settledAt
	if changed, err := store.SettleClearDevComplexExecutionAgentStep(ctx, reviewStep); err != nil || !changed {
		t.Fatalf("settle review step for %s: changed=%t err=%v", task.TaskKey, changed, err)
	}
	if changed, err := store.SettleClearDevComplexExecutionReview(ctx, core.SettleComplexExecutionReviewCommand{ReviewID: review.ID, TurnID: "review-turn-" + task.ID, FinalMessageID: "review-message-" + task.ID, Verdict: core.LocalReviewPass, ReasonCode: core.ReasonCode("REVIEW_PASSED"), Summary: "approved", At: settledAt}); err != nil || !changed {
		t.Fatalf("settle review for %s: changed=%t err=%v", task.TaskKey, changed, err)
	}
	scopeID := "check-" + task.ID + "-scope"
	if err := store.VerifyClearDevComplexExecutionCandidate(ctx, core.VerifyComplexExecutionCandidateCommand{Verification: core.ComplexExecutionVerification{ID: "verify-" + task.ID, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: candidateID, CandidateCommitSHA: candidateSHA, Round: round, ScopeEvidenceID: scopeID, RequiredCheckRunIDs: checkIDs, LocalReviewID: review.ID, VerifiedAt: state.now.Add(17 * time.Second)}}); err != nil {
		t.Fatalf("verify %s: %v", task.TaskKey, err)
	}
	snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("read verified %s: ok=%t err=%v", task.TaskKey, ok, err)
	}
	for _, persisted := range snapshot.Tasks {
		if persisted.ID == task.ID && persisted.Status != core.DevelopmentTaskStatusReview {
			t.Fatalf("verified non-final task %s state = %s, want REVIEW", task.TaskKey, persisted.Status)
		}
	}
	for _, persisted := range snapshot.Dispatches {
		if persisted.ID == dispatch.ID && (persisted.DevelopmentTaskID != task.DevelopmentTaskID || persisted.ExecutionPackageSHA256 != task.ExecutionPackageSHA256 || persisted.ClientMessageID != dispatch.ClientMessageID) {
			t.Fatalf("dispatch readback lost its immutable request fields: %#v", persisted)
		}
	}
	for _, persisted := range snapshot.Reviews {
		if persisted.ID == review.ID && persisted.CandidateWorkspacePath != reviewer.WorkspacePath {
			t.Fatalf("review readback workspace = %q, want %q", persisted.CandidateWorkspacePath, reviewer.WorkspacePath)
		}
	}
	return candidateSHA
}

func dispatchComplexExecutionTask(ctx context.Context, store *sqlite.Store, state complexExecutionFlowState, task core.ComplexExecutionTask, round int, base, id string) (core.ComplexExecutionDispatch, bool, error) {
	at := state.now.Add(3 * time.Second)
	dispatch := core.ComplexExecutionDispatch{ID: id, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DevelopmentTaskID: task.DevelopmentTaskID, Round: round, BaseCommitSHA: base, AgentStepID: "step-" + id, ExecutionPackageSHA256: task.ExecutionPackageSHA256, ClientMessageID: "client-" + id, Status: core.ComplexExecutionDispatchPending, CreatedAt: at}
	step := core.AgentStep{ID: dispatch.AgentStepID, RoleBindingID: state.builder.ID, Kind: core.ComplexExecutionAgentStepBuilderTask, RequestID: id, ClientMessageID: dispatch.ClientMessageID, PromptSHA256: strings.Repeat("2", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: at}
	return store.CreateClearDevComplexExecutionDispatch(ctx, core.CreateComplexExecutionDispatchCommand{Dispatch: dispatch, AgentStep: step})
}

func settleIntegrationCheck(t *testing.T, store *sqlite.Store, state complexExecutionFlowState, candidateSHA string) string {
	t.Helper()
	ctx := context.Background()
	const id = "s06-flow-integration-run"
	check := core.ComplexExecutionCheckRun{ID: id, ExecutionRunID: state.run.ID, DispatchID: "dispatch-" + state.tasks[1].ID, CandidateCommitID: "candidate-" + state.tasks[1].ID, CandidateCommitSHA: candidateSHA, CheckSpecFactID: "s06-flow-integration-check", Kind: core.CandidateCheckIntegration, Status: core.ComplexExecutionCheckRunPending, CreatedAt: state.now.Add(40 * time.Second)}
	if _, created, err := store.CreateClearDevComplexExecutionCheckRun(ctx, check); err != nil || !created {
		t.Fatalf("create integration check: created=%t err=%v", created, err)
	}
	if changed, err := store.StartClearDevComplexExecutionCheckRun(ctx, id, state.now.Add(41*time.Second)); err != nil || !changed {
		t.Fatalf("start integration check: changed=%t err=%v", changed, err)
	}
	exit := 0
	if changed, err := store.SettleClearDevComplexExecutionCheckRun(ctx, core.SettleComplexExecutionCheckCommand{CheckRunID: id, Result: core.EvidenceResultPass, ContainerImageID: "cleardev-go-validation", ExitCode: &exit, OutputSummary: "pass", OutputSHA256: strings.Repeat("d", 64), ChangedPathsJSON: "[]", At: state.now.Add(42 * time.Second)}); err != nil || !changed {
		t.Fatalf("settle integration check: changed=%t err=%v", changed, err)
	}
	return id
}

func createComplexExecutionSession(t *testing.T, store *sqlite.Store, projectID, id, key, workspace, diffBase string, now time.Time) domain.SessionRecord {
	t.Helper()
	record, err := store.CreateSession(context.Background(), domain.SessionRecord{ID: domain.SessionID(id), ProjectID: domain.ProjectID(projectID), Kind: domain.KindWorker, Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto, CreationIdempotencyKey: key, CreationRequestFingerprint: key, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}, Metadata: domain.SessionMetadata{Branch: "main", WorkspacePath: workspace, WorkspaceRepoPath: workspace, DiffBaseSHA: diffBase, DiffBaseRef: "refs/heads/main"}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("create S06 session %s: %v", id, err)
	}
	return record
}

func initComplexExecutionTestRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "ClearDev test"}} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	return repo
}

func ptrTime(value time.Time) *time.Time { return &value }

func injectFinalizerAbort(t *testing.T, path, taskID string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open test fault-injection connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER s06_flow_injected_finalizer_abort
BEFORE UPDATE OF state ON cleardev_work_items
WHEN NEW.id = '` + taskID + `' AND NEW.state = 'DONE'
BEGIN SELECT RAISE(ABORT, 's06 injected finalizer failure'); END;`); err != nil {
		t.Fatalf("install finalizer failure injection: %v", err)
	}
}

func complexExecutionTestDigest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func complexExecutionTestScopeDigest(task core.ComplexStandardExecutionPackage) string {
	return complexExecutionTestDigest(struct {
		WritePaths                 []string `json:"writePaths"`
		GeneratedPaths             []string `json:"generatedPaths"`
		SharedPathsRequireApproval []string `json:"sharedPathsRequireApproval"`
		ForbiddenPaths             []string `json:"forbiddenPaths"`
	}{task.WritePaths, task.GeneratedPaths, task.SharedPathsRequireApproval, task.ForbiddenPaths})
}
