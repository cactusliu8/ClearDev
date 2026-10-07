package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestComplexParallelExecutionStoreFlow(t *testing.T) {
	t.Run("materializes three tasks two builders and batches atomically", func(t *testing.T) {
		store, state := seedComplexParallelFlow(t)
		ctx := context.Background()
		snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatalf("read materialized PARALLEL run: ok=%t err=%v", ok, err)
		}
		if snapshot.Run.Mode != core.WorkModeParallel || snapshot.Run.FixedBuilderCount != 2 || snapshot.Run.ModeReason != string(core.ReasonParallelPlanApproved) {
			t.Fatalf("run mode = %#v", snapshot.Run)
		}
		if len(snapshot.Tasks) != 3 || len(state.builders) != 2 || len(snapshot.Batches) != 2 {
			t.Fatalf("tasks=%d builders=%d batches=%d", len(snapshot.Tasks), len(state.builders), len(snapshot.Batches))
		}
		requirement, ok, err := store.GetClearDevRequirement(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		var version core.RequirementVersion
		for _, item := range requirement.RequirementVersions {
			if item.ID == state.prep.V2ID {
				version = item
			}
		}
		if version.TaskSetVersion != core.ComplexStandardTaskSetVersion {
			t.Fatalf("task-set version = %d", version.TaskSetVersion)
		}
	})

	t.Run("mismatched batches roll back the task set", func(t *testing.T) {
		dataDir := t.TempDir()
		store := sqlitetest.MustOpenAt(t, dataDir)
		repo := initComplexExecutionTestRepo(t)
		prep := cleardevtest.SeedComplexParallelV2(t, store, "ao-s07-rollback", repo, dataDir)
		ctx := context.Background()
		state := startParallelRun(t, store, prep, "s07-rollback-run", time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
		state.command.Batches[0].TaskKeys = []string{"not-a-plan-task"}
		if err := store.MaterializeClearDevComplexExecution(ctx, state.command); err == nil {
			t.Fatal("mismatched batches were materialized")
		}
		requirement, ok, err := store.GetClearDevRequirement(ctx, prep.RequirementID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		for _, version := range requirement.RequirementVersions {
			if version.ID == prep.V2ID && version.TaskSetVersion != 0 {
				t.Fatalf("rolled-back materialization changed task-set version to %d", version.TaskSetVersion)
			}
		}
	})

	t.Run("two builders can be RUNNING at once and one builder cannot hold two attempts", func(t *testing.T) {
		store, state := seedComplexParallelFlow(t)
		ctx := context.Background()
		if err := store.ActivateClearDevComplexExecutionBatch(ctx, state.run.ID, state.batches[0].ID, state.initialBase, state.now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := dispatchParallelTask(ctx, store, state, state.tasks[0], state.builders[0], state.batches[0].ID, 0, state.initialBase, "dispatch-left"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := dispatchParallelTask(ctx, store, state, state.tasks[1], state.builders[0], state.batches[0].ID, 0, state.initialBase, "dispatch-same-builder"); err == nil {
			t.Fatal("same Builder accepted a second active attempt")
		}
		if _, _, err := dispatchParallelTask(ctx, store, state, state.tasks[1], state.builders[1], state.batches[0].ID, 0, state.initialBase, "dispatch-right"); err != nil {
			t.Fatal(err)
		}
		snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		running := 0
		for _, task := range snapshot.Tasks {
			if task.Status == core.DevelopmentTaskStatusRunning {
				running++
			}
		}
		if running != 2 {
			t.Fatalf("RUNNING tasks = %d, want 2; %#v", running, snapshot.Tasks)
		}
	})

	t.Run("next wave uses the composed commit and conflict blocks without DONE", func(t *testing.T) {
		store, state := seedComplexParallelFlow(t)
		ctx := context.Background()
		if err := store.ActivateClearDevComplexExecutionBatch(ctx, state.run.ID, state.batches[0].ID, state.initialBase, state.now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		left := strings.Repeat("b", 40)
		right := strings.Repeat("c", 40)
		composed := strings.Repeat("d", 40)
		verifyParallelTask(t, store, state, state.tasks[0], state.builders[0], state.batches[0].ID, 0, state.initialBase, left)
		verifyParallelTask(t, store, state, state.tasks[1], state.builders[1], state.batches[0].ID, 0, state.initialBase, right)

		at := state.now.Add(20 * time.Second)
		request := core.ComplexExecutionComposition{
			ID: "s07-compose-0", ExecutionRunID: state.run.ID, BatchID: state.batches[0].ID,
			RequestID: "cleardev-complex-composition-s07-compose-0", InputBaseSHA: state.initialBase,
			InputCandidateIDs:  []string{"candidate-" + state.tasks[0].ID, "candidate-" + state.tasks[1].ID},
			InputCandidateSHAs: []string{left, right}, Status: core.ComplexExecutionCompositionPending, CreatedAt: at,
		}
		if err := store.RecordClearDevComplexExecutionComposition(ctx, request); err != nil {
			t.Fatalf("record composition: %v", err)
		}
		if err := store.StartClearDevComplexExecutionComposition(ctx, request.ID); err != nil {
			t.Fatalf("start composition: %v", err)
		}
		settled := request
		settled.Status, settled.WorkspacePath, settled.OutputCommitSHA, settled.CreatedAt = core.ComplexExecutionCompositionComposed, "/managed/compose-0", composed, at.Add(time.Second)
		if err := store.SettleClearDevComplexExecutionComposition(ctx, settled); err != nil {
			t.Fatalf("settle composition: %v", err)
		}
		if err := store.ActivateClearDevComplexExecutionBatch(ctx, state.run.ID, state.batches[1].ID, composed, at.Add(2*time.Second)); err != nil {
			t.Fatalf("activate wave 1: %v", err)
		}
		dispatch, _, err := dispatchParallelTask(ctx, store, state, state.tasks[2], state.builders[0], state.batches[1].ID, 0, composed, "dispatch-summary")
		if err != nil {
			t.Fatal(err)
		}
		if dispatch.BaseCommitSHA != composed {
			t.Fatalf("wave-1 base = %s, want composed %s", dispatch.BaseCommitSHA, composed)
		}

		store2, blocked := seedComplexParallelFlow(t)
		if err := store2.ActivateClearDevComplexExecutionBatch(ctx, blocked.run.ID, blocked.batches[0].ID, blocked.initialBase, blocked.now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		verifyParallelTask(t, store2, blocked, blocked.tasks[0], blocked.builders[0], blocked.batches[0].ID, 0, blocked.initialBase, left)
		verifyParallelTask(t, store2, blocked, blocked.tasks[1], blocked.builders[1], blocked.batches[0].ID, 0, blocked.initialBase, right)
		conflict := core.ComplexExecutionComposition{
			ID: "s07-compose-conflict", ExecutionRunID: blocked.run.ID, BatchID: blocked.batches[0].ID,
			RequestID: "cleardev-complex-composition-s07-compose-conflict", InputBaseSHA: blocked.initialBase,
			InputCandidateIDs:  []string{"candidate-" + blocked.tasks[0].ID, "candidate-" + blocked.tasks[1].ID},
			InputCandidateSHAs: []string{left, right}, Status: core.ComplexExecutionCompositionPending, CreatedAt: blocked.now.Add(20 * time.Second),
		}
		if err := store2.RecordClearDevComplexExecutionComposition(ctx, conflict); err != nil {
			t.Fatal(err)
		}
		if err := store2.StartClearDevComplexExecutionComposition(ctx, conflict.ID); err != nil {
			t.Fatal(err)
		}
		conflict.Status, conflict.ConflictPaths, conflict.ReasonCode, conflict.CreatedAt = core.ComplexExecutionCompositionBlocked, []string{"src/email.js"}, core.ReasonCompositionConflict, blocked.now.Add(21*time.Second)
		if err := store2.SettleClearDevComplexExecutionComposition(ctx, conflict); err != nil {
			t.Fatalf("settle conflict: %v", err)
		}
		snapshot, ok, err := store2.GetClearDevComplexExecution(ctx, blocked.prep.RequirementID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		phase, _ := core.DeriveComplexExecutionPhase(snapshot)
		if phase != core.ComplexExecutionBlocked || snapshot.Integration != nil {
			t.Fatalf("conflict snapshot = phase:%s integration:%#v run:%#v", phase, snapshot.Integration, snapshot.Run)
		}
		for _, task := range snapshot.Tasks {
			if task.Status == core.DevelopmentTaskStatusDone {
				t.Fatalf("conflict marked task %s DONE", task.TaskKey)
			}
		}
	})

	t.Run("complete only on the final composed commit", func(t *testing.T) {
		store, state := seedComplexParallelFlow(t)
		ctx := context.Background()
		if err := store.ActivateClearDevComplexExecutionBatch(ctx, state.run.ID, state.batches[0].ID, state.initialBase, state.now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		left, right, composed0 := strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 40)
		verifyParallelTask(t, store, state, state.tasks[0], state.builders[0], state.batches[0].ID, 0, state.initialBase, left)
		verifyParallelTask(t, store, state, state.tasks[1], state.builders[1], state.batches[0].ID, 0, state.initialBase, right)
		settleParallelComposition(t, store, state, state.batches[0], []string{left, right}, composed0, "/managed/compose-0")
		if err := store.ActivateClearDevComplexExecutionBatch(ctx, state.run.ID, state.batches[1].ID, composed0, state.now.Add(30*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := store.RebaseClearDevComplexExecutionBuilder(ctx, state.builders[0].ID, composed0); err != nil {
			t.Fatal(err)
		}
		state.builders[0].BaseCommitSHA = composed0
		summary := strings.Repeat("e", 40)
		verifyParallelTask(t, store, state, state.tasks[2], state.builders[0], state.batches[1].ID, 0, composed0, summary)
		composed1 := strings.Repeat("f", 40)
		settleParallelComposition(t, store, state, state.batches[1], []string{summary}, composed1, "/managed/compose-1")
		checkID := settleParallelIntegrationCheck(t, store, state, summary, composed1)
		at := state.now.Add(70 * time.Second)
		if err := store.CompleteClearDevComplexExecution(ctx, core.CompleteComplexExecutionCommand{
			ExecutionRunID: state.run.ID,
			Integration:    core.ComplexExecutionIntegration{ID: "s07-complete", ExecutionRunID: state.run.ID, IntegrationCandidateID: "s07-integration", CandidateCommitSHA: summary, CheckRunIDs: []string{checkID}, CompletedAt: at},
			At:             at,
		}); err == nil {
			t.Fatal("completion accepted the last task SHA instead of the composed commit")
		}
		if err := store.CompleteClearDevComplexExecution(ctx, core.CompleteComplexExecutionCommand{
			ExecutionRunID: state.run.ID,
			Integration:    core.ComplexExecutionIntegration{ID: "s07-complete", ExecutionRunID: state.run.ID, IntegrationCandidateID: "s07-integration", CandidateCommitSHA: composed1, CheckRunIDs: []string{checkID}, CompletedAt: at},
			At:             at,
		}); err != nil {
			t.Fatalf("complete on composed SHA: %v", err)
		}
		snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok || snapshot.Integration == nil || snapshot.Integration.CandidateCommitSHA != composed1 {
			t.Fatalf("completed snapshot = ok:%t err:%v integration:%#v", ok, err, snapshot.Integration)
		}
	})
}

func TestGetClearDevComplexExecutionRejectsCorruptBatchJSON(t *testing.T) {
	store, state := seedComplexParallelFlow(t)
	execSQL(t, state.dataDir,
		`DROP TRIGGER IF EXISTS cleardev_complex_execution_batch_update_valid`,
		`UPDATE cleardev_complex_execution_batches SET task_keys_json = '[1]'`,
	)
	if _, _, err := store.GetClearDevComplexExecution(context.Background(), state.prep.RequirementID); err == nil {
		t.Fatal("corrupt batch JSON was accepted")
	}
}

func TestComplexParallelExecutionStandardFlowStillWorksAfter0113(t *testing.T) {
	store, state := seedComplexExecutionFlow(t)
	if state.run.Mode != core.WorkModeStandard || state.run.FixedBuilderCount != 1 {
		t.Fatalf("STANDARD run after 0113 = %#v", state.run)
	}
	snapshot, ok, err := store.GetClearDevComplexExecution(context.Background(), state.prep.RequirementID)
	if err != nil || !ok || len(snapshot.Batches) != 0 || len(snapshot.Tasks) != 2 {
		t.Fatalf("STANDARD snapshot after 0113 = ok:%t err:%v tasks:%d batches:%d", ok, err, len(snapshot.Tasks), len(snapshot.Batches))
	}
}

type complexParallelFlowState struct {
	prep        cleardevtest.ComplexParallelPrep
	projectID   string
	run         core.ComplexExecutionRun
	tasks       []core.ComplexExecutionTask
	checkSpecs  []core.ComplexExecutionCheckSpecFact
	builders    []core.ComplexExecutionRoleBinding
	batches     []core.ComplexExecutionBatch
	command     core.MaterializeComplexExecutionCommand
	initialBase string
	now         time.Time
	dataDir     string
}

type parallelStartState struct {
	run     core.ComplexExecutionRun
	command core.MaterializeComplexExecutionCommand
}

func seedComplexParallelFlow(t *testing.T) (*sqlite.Store, complexParallelFlowState) {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initComplexExecutionTestRepo(t)
	const projectID = "ao-s07-store-flow"
	prep := cleardevtest.SeedComplexParallelV2(t, store, projectID, repo, dataDir)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	started := startParallelRun(t, store, prep, "s07-flow-run", now)
	if err := store.MaterializeClearDevComplexExecution(context.Background(), started.command); err != nil {
		t.Fatalf("materialize PARALLEL run: %v", err)
	}
	builders := make([]core.ComplexExecutionRoleBinding, 0, len(started.command.BuilderBindings))
	for index, builder := range started.command.BuilderBindings {
		workspace := "/worktrees/s07-flow-builder-" + string(rune('1'+index))
		session := createComplexExecutionSession(t, store, projectID, "s07-flow-builder-session-"+string(rune('1'+index)), builder.SessionCreationIdempotencyKey, workspace, strings.Repeat("a", 40), now)
		if changed, err := store.BindClearDevComplexExecutionRoleBinding(context.Background(), builder.ID, string(session.ID), workspace, strings.Repeat("a", 40), now.Add(2*time.Second)); err != nil || !changed {
			t.Fatalf("bind PARALLEL builder %d: changed=%t err=%v", index+1, changed, err)
		}
		builder.AOSessionID, builder.WorkspacePath, builder.BaseCommitSHA = string(session.ID), workspace, strings.Repeat("a", 40)
		builder.Status = core.RoleBindingStatusBound
		builders = append(builders, builder)
	}
	return store, complexParallelFlowState{
		prep: prep, projectID: projectID, run: started.run, tasks: started.command.Tasks, checkSpecs: started.command.CheckSpecs,
		builders: builders, batches: started.command.Batches, command: started.command, initialBase: strings.Repeat("a", 40), now: now, dataDir: dataDir,
	}
}

func startParallelRun(t *testing.T, store *sqlite.Store, prep cleardevtest.ComplexParallelPrep, runID string, now time.Time) parallelStartState {
	t.Helper()
	ctx := context.Background()
	requirement, ok, err := store.GetClearDevRequirement(ctx, prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("read PARALLEL requirement: ok=%t err=%v", ok, err)
	}
	var version core.RequirementVersion
	for _, item := range requirement.RequirementVersions {
		if item.ID == prep.V2ID {
			version = item
		}
	}
	planning, ok, err := store.GetClearDevComplexPlanning(ctx, prep.RequirementID)
	if err != nil || !ok {
		t.Fatal(err)
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
	var parsed core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(plan.PlanJSON), &parsed); err != nil {
		t.Fatal(err)
	}
	selection, err := core.SelectComplexExecutionMode(parsed.Tasks, parsed.ParallelSuggestion.RecommendedBuilderCount)
	if err != nil {
		t.Fatal(err)
	}
	raw, digest, err := core.BuildComplexExecutionRunPackage(selection.Mode, runID, version.ID, version.SHA256, plan.ID, plan.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	run := core.ComplexExecutionRun{ID: runID, DevelopmentRequirementID: prep.RequirementID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, PlanID: plan.ID, PlanReviewID: prep.V2ReviewID, PlanSHA256: plan.PlanSHA256, Mode: selection.Mode, ModeReason: string(selection.ReasonCode), FixedBuilderCount: selection.BuilderCount, ExpectedTaskSetVersion: 0, TaskSetVersion: core.ComplexStandardTaskSetVersion, ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest, StewardRoleBindingID: steward.ID, CreatedAt: now}
	stewardBinding := core.ComplexExecutionRoleBinding{ID: runID + "-steward", ExecutionRunID: run.ID, SourceComplexRoleBindingID: steward.ID, Role: core.StandardRoleSteward, SessionCreationIdempotencyKey: runID + "-steward", AOSessionID: prep.StewardSessionID, Status: core.RoleBindingStatusBound, RequestedAt: now, BoundAt: &now}
	step := core.AgentStep{ID: runID + "-steward-step", RoleBindingID: stewardBinding.ID, Kind: core.AgentStepDispatchRequest, RequestID: run.ID, ClientMessageID: runID + "-steward-client", PromptSHA256: strings.Repeat("1", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
	if _, created, err := store.StartClearDevComplexExecution(ctx, core.StartComplexExecutionCommand{Run: run, StewardRoleBinding: stewardBinding, AgentStep: step}); err != nil || !created {
		t.Fatalf("start PARALLEL execution: created=%t err=%v", created, err)
	}
	tasks, specs := parallelExecutionMaterialization(t, run, version, parsed, now)
	command := core.MaterializeComplexExecutionCommand{ExecutionRunID: run.ID, Tasks: tasks, CheckSpecs: specs, At: now.Add(time.Second)}
	for slot := 1; slot <= selection.BuilderCount; slot++ {
		suffix := ""
		if slot > 1 {
			suffix = "-" + string(rune('0'+slot))
		}
		command.BuilderBindings = append(command.BuilderBindings, core.ComplexExecutionRoleBinding{ID: runID + "-builder" + suffix, ExecutionRunID: run.ID, Role: core.StandardRoleBuilder, BuilderSlot: slot, SessionCreationIdempotencyKey: runID + "-builder-key" + suffix, Status: core.RoleBindingStatusRequested, RequestedAt: now})
	}
	for ordinal, keys := range selection.Batches {
		command.Batches = append(command.Batches, core.ComplexExecutionBatch{ID: runID + "-batch-" + string(rune('0'+ordinal)), ExecutionRunID: run.ID, Ordinal: ordinal, TaskKeys: append([]string(nil), keys...), Status: core.ComplexExecutionBatchPending, CreatedAt: now})
	}
	return parallelStartState{run: run, command: command}
}

func parallelExecutionMaterialization(t *testing.T, run core.ComplexExecutionRun, version core.RequirementVersion, plan core.ComplexEngineeringPlanResult, now time.Time) ([]core.ComplexExecutionTask, []core.ComplexExecutionCheckSpecFact) {
	t.Helper()
	tasks := make([]core.ComplexExecutionTask, 0, len(plan.Tasks))
	byKey := map[string]string{}
	workItemByKey := map[string]string{}
	for index, task := range plan.Tasks {
		byKey[task.Key] = run.ID + "-task-" + string(rune('1'+index))
		workItemByKey[task.Key] = run.ID + "-work-" + string(rune('1'+index))
	}
	for index, task := range plan.Tasks {
		deps := make([]string, 0, len(task.DependencyKeys))
		for _, key := range task.DependencyKeys {
			deps = append(deps, workItemByKey[key])
		}
		_, raw, digest, err := core.BuildComplexStandardExecutionPackage(run.Mode, core.ComplexStandardExecutionPackageInput{ExecutionRunID: run.ID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, RequirementText: version.RequirementText, PlanID: run.PlanID, PlanSHA256: run.PlanSHA256, TaskSetVersion: core.ComplexStandardTaskSetVersion, TaskID: workItemByKey[task.Key], DependencyTaskIDs: deps, Task: task})
		if err != nil {
			t.Fatalf("build PARALLEL package for %s: %v", task.Key, err)
		}
		tasks = append(tasks, core.ComplexExecutionTask{ID: byKey[task.Key], ExecutionRunID: run.ID, TaskKey: task.Key, DevelopmentTaskID: workItemByKey[task.Key], Ordinal: index, DependencyTaskKeys: append([]string(nil), task.DependencyKeys...), ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest, Status: core.DevelopmentTaskStatusPlanned, CurrentRound: 0})
	}
	specs := []core.ComplexExecutionCheckSpecFact{}
	for _, task := range tasks {
		var packet core.ComplexStandardExecutionPackage
		if err := json.Unmarshal([]byte(task.ExecutionPackageJSON), &packet); err != nil {
			t.Fatal(err)
		}
		specs = append(specs, core.ComplexExecutionCheckSpecFact{ID: task.ID + "-scope", ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, CheckID: "SCOPE", CheckSpecSHA256: complexExecutionTestScopeDigest(packet), Kind: core.CandidateCheckScope, Argv: []string{}, TimeoutSeconds: 0, CreatedAt: now})
		for _, check := range packet.RequiredChecks {
			specs = append(specs, core.ComplexExecutionCheckSpecFact{ID: task.ID + "-" + check.ID, ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, CheckID: check.ID, CheckSpecSHA256: complexExecutionTestDigest(check.Argv), Kind: core.CandidateCheckRequired, Argv: check.Argv, TimeoutSeconds: check.TimeoutSeconds, CreatedAt: now})
		}
	}
	integration, _ := core.FrozenComplexCheckByID("all-tests")
	specs = append(specs, core.ComplexExecutionCheckSpecFact{ID: run.ID + "-integration-check", ExecutionRunID: run.ID, CheckID: integration.ID, CheckSpecSHA256: complexExecutionTestDigest(integration.Argv), Kind: core.CandidateCheckIntegration, Argv: integration.Argv, TimeoutSeconds: integration.TimeoutSeconds, CreatedAt: now})
	return tasks, specs
}

func dispatchParallelTask(ctx context.Context, store *sqlite.Store, state complexParallelFlowState, task core.ComplexExecutionTask, builder core.ComplexExecutionRoleBinding, batchID string, round int, base, id string) (core.ComplexExecutionDispatch, bool, error) {
	at := state.now.Add(3 * time.Second)
	dispatch := core.ComplexExecutionDispatch{ID: id, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DevelopmentTaskID: task.DevelopmentTaskID, Round: round, BaseCommitSHA: base, AgentStepID: "step-" + id, ExecutionPackageSHA256: task.ExecutionPackageSHA256, ClientMessageID: "client-" + id, BatchID: batchID, BuilderRoleBindingID: builder.ID, Status: core.ComplexExecutionDispatchPending, CreatedAt: at}
	step := core.AgentStep{ID: dispatch.AgentStepID, RoleBindingID: builder.ID, Kind: core.ComplexExecutionAgentStepBuilderTask, RequestID: id, ClientMessageID: dispatch.ClientMessageID, PromptSHA256: strings.Repeat("2", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: at}
	return store.CreateClearDevComplexExecutionDispatch(ctx, core.CreateComplexExecutionDispatchCommand{Dispatch: dispatch, AgentStep: step, BatchID: batchID})
}

func verifyParallelTask(t *testing.T, store *sqlite.Store, state complexParallelFlowState, task core.ComplexExecutionTask, builder core.ComplexExecutionRoleBinding, batchID string, round int, base, candidateSHA string) {
	t.Helper()
	ctx := context.Background()
	dispatchID := "dispatch-" + task.ID
	if _, _, err := dispatchParallelTask(ctx, store, state, task, builder, batchID, round, base, dispatchID); err != nil {
		t.Fatalf("dispatch %s: %v", task.TaskKey, err)
	}
	candidateID := "candidate-" + task.ID
	if _, err := store.AppendClearDevComplexExecutionCandidate(ctx, core.AppendComplexExecutionCandidateCommand{ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatchID, Round: round, BaseCommitSHA: base, Candidate: core.CandidateObservation{ID: candidateID, DevelopmentTaskID: task.DevelopmentTaskID, AOSessionID: builder.AOSessionID, CommitSHA: candidateSHA, ObservedAt: state.now.Add(10 * time.Second)}}); err != nil {
		t.Fatalf("append candidate for %s: %v", task.TaskKey, err)
	}
	checkIDs := []string{}
	for _, spec := range state.checkSpecs {
		if spec.Kind == core.CandidateCheckIntegration || spec.ComplexExecutionTaskID != task.ID {
			continue
		}
		checkID := "check-" + spec.ID
		check := core.ComplexExecutionCheckRun{ID: checkID, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatchID, CandidateCommitID: candidateID, CandidateCommitSHA: candidateSHA, CheckSpecFactID: spec.ID, Kind: spec.Kind, Argv: spec.Argv, Status: core.ComplexExecutionCheckRunPending, CreatedAt: state.now.Add(11 * time.Second)}
		if _, created, err := store.CreateClearDevComplexExecutionCheckRun(ctx, check); err != nil || !created {
			t.Fatalf("create %s check: created=%t err=%v", spec.Kind, created, err)
		}
		if changed, err := store.StartClearDevComplexExecutionCheckRun(ctx, checkID, state.now.Add(12*time.Second)); err != nil || !changed {
			t.Fatalf("start %s check: %v", spec.Kind, err)
		}
		exit := 0
		if changed, err := store.SettleClearDevComplexExecutionCheckRun(ctx, core.SettleComplexExecutionCheckCommand{CheckRunID: checkID, Result: core.EvidenceResultPass, ContainerImageID: "cleardev-go-validation", ExitCode: &exit, OutputSummary: "pass", OutputSHA256: strings.Repeat("d", 64), ChangedPathsJSON: "[]", At: state.now.Add(13 * time.Second)}); err != nil || !changed {
			t.Fatalf("settle %s check: %v", spec.Kind, err)
		}
		if spec.Kind == core.CandidateCheckRequired {
			checkIDs = append(checkIDs, checkID)
		}
	}
	reviewerSession := createComplexExecutionSession(t, store, state.projectID, "reviewer-session-"+task.ID, "reviewer-key-"+task.ID, "/worktrees/reviewer-"+task.ID, state.initialBase, state.now.Add(14*time.Second))
	reviewer := core.ComplexExecutionRoleBinding{ID: "reviewer-" + task.ID, ExecutionRunID: state.run.ID, Role: core.StandardRoleReviewer, TaskMappingID: task.ID, CandidateCommitID: candidateID, SessionCreationIdempotencyKey: "reviewer-key-" + task.ID, Status: core.RoleBindingStatusRequested, RequestedAt: state.now.Add(14 * time.Second)}
	if _, created, err := store.CreateClearDevComplexExecutionRoleBinding(ctx, reviewer); err != nil || !created {
		t.Fatalf("create Reviewer for %s: %v", task.TaskKey, err)
	}
	if changed, err := store.BindClearDevComplexExecutionRoleBinding(ctx, reviewer.ID, string(reviewerSession.ID), "/worktrees/reviewer-"+task.ID, candidateSHA, state.now.Add(14*time.Second)); err != nil || !changed {
		t.Fatalf("bind Reviewer for %s: %v", task.TaskKey, err)
	}
	reviewer.AOSessionID, reviewer.WorkspacePath, reviewer.BaseCommitSHA = string(reviewerSession.ID), "/worktrees/reviewer-"+task.ID, candidateSHA
	reviewer.Status = core.RoleBindingStatusBound
	review := core.ComplexExecutionReview{ID: "review-" + task.ID, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatchID, CandidateCommitID: candidateID, CandidateCommitSHA: candidateSHA, BaseCommitSHA: base, ReviewPacketJSON: "{}", ReviewPacketSHA256: strings.Repeat("e", 64), CandidateWorkspacePath: reviewer.WorkspacePath, ReviewerRoleBindingID: reviewer.ID, AgentStepID: "review-step-" + task.ID, Status: core.LocalReviewStatusPending, CreatedAt: state.now.Add(15 * time.Second)}
	reviewStep := core.AgentStep{ID: review.AgentStepID, RoleBindingID: reviewer.ID, Kind: core.AgentStepLocalReview, RequestID: review.ID, ClientMessageID: "review-client-" + task.ID, PromptSHA256: strings.Repeat("f", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: review.CreatedAt}
	if _, created, err := store.CreateClearDevComplexExecutionReview(ctx, core.CreateComplexExecutionReviewCommand{Review: review, ReviewerBinding: reviewer, AgentStep: reviewStep}); err != nil || !created {
		t.Fatalf("create review for %s: %v", task.TaskKey, err)
	}
	if changed, err := store.MarkClearDevComplexExecutionAgentStepSent(ctx, reviewStep.ID, state.now.Add(15*time.Second)); err != nil || !changed {
		t.Fatalf("send review for %s: %v", task.TaskKey, err)
	}
	settledAt := state.now.Add(16 * time.Second)
	reviewStep.SendStatus, reviewStep.TurnID, reviewStep.FinalMessageID, reviewStep.FinalMessageText = core.AgentStepSendStatusSettled, "review-turn-"+task.ID, "review-message-"+task.ID, "approved"
	reviewStep.MessageSHA256, reviewStep.CompletedAt = strings.Repeat("3", 64), &settledAt
	if changed, err := store.SettleClearDevComplexExecutionAgentStep(ctx, reviewStep); err != nil || !changed {
		t.Fatalf("settle review step for %s: %v", task.TaskKey, err)
	}
	if changed, err := store.SettleClearDevComplexExecutionReview(ctx, core.SettleComplexExecutionReviewCommand{ReviewID: review.ID, TurnID: "review-turn-" + task.ID, FinalMessageID: "review-message-" + task.ID, Verdict: core.LocalReviewPass, ReasonCode: core.ReasonCode("REVIEW_PASSED"), Summary: "approved", At: settledAt}); err != nil || !changed {
		t.Fatalf("settle review for %s: %v", task.TaskKey, err)
	}
	if err := store.VerifyClearDevComplexExecutionCandidate(ctx, core.VerifyComplexExecutionCandidateCommand{Verification: core.ComplexExecutionVerification{ID: "verify-" + task.ID, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatchID, CandidateCommitID: candidateID, CandidateCommitSHA: candidateSHA, Round: round, ScopeEvidenceID: "check-" + task.ID + "-scope", RequiredCheckRunIDs: checkIDs, LocalReviewID: review.ID, VerifiedAt: state.now.Add(17 * time.Second)}}); err != nil {
		t.Fatalf("verify %s: %v", task.TaskKey, err)
	}
}

func settleParallelComposition(t *testing.T, store *sqlite.Store, state complexParallelFlowState, batch core.ComplexExecutionBatch, shas []string, output, workspace string) {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, len(batch.TaskKeys))
	for _, key := range batch.TaskKeys {
		for _, task := range state.tasks {
			if task.TaskKey == key {
				ids = append(ids, "candidate-"+task.ID)
			}
		}
	}
	at := state.now.Add(20 * time.Second)
	snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	base := batch.CommonBaseSHA
	for _, item := range snapshot.Batches {
		if item.ID == batch.ID && item.CommonBaseSHA != "" {
			base = item.CommonBaseSHA
		}
	}
	if base == "" {
		t.Fatalf("composition for batch %s has no common base", batch.ID)
	}
	request := core.ComplexExecutionComposition{ID: "compose-" + batch.ID, ExecutionRunID: state.run.ID, BatchID: batch.ID, RequestID: "cleardev-complex-composition-" + batch.ID, InputBaseSHA: base, InputCandidateIDs: ids, InputCandidateSHAs: shas, Status: core.ComplexExecutionCompositionPending, CreatedAt: at}
	if err := store.RecordClearDevComplexExecutionComposition(ctx, request); err != nil {
		t.Fatalf("record composition %s: %v", batch.ID, err)
	}
	if err := store.StartClearDevComplexExecutionComposition(ctx, request.ID); err != nil {
		t.Fatalf("start composition %s: %v", batch.ID, err)
	}
	request.Status, request.WorkspacePath, request.OutputCommitSHA, request.CreatedAt = core.ComplexExecutionCompositionComposed, workspace, output, at.Add(time.Second)
	if err := store.SettleClearDevComplexExecutionComposition(ctx, request); err != nil {
		t.Fatalf("settle composition %s: %v", batch.ID, err)
	}
}

func settleParallelIntegrationCheck(t *testing.T, store *sqlite.Store, state complexParallelFlowState, lastCandidateSHA, composedSHA string) string {
	t.Helper()
	ctx := context.Background()
	final := state.tasks[len(state.tasks)-1]
	const id = "s07-integration-run"
	check := core.ComplexExecutionCheckRun{ID: id, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: final.ID, DispatchID: "dispatch-" + final.ID, CandidateCommitID: "candidate-" + final.ID, CandidateCommitSHA: composedSHA, CheckSpecFactID: state.run.ID + "-integration-check", Kind: core.CandidateCheckIntegration, Status: core.ComplexExecutionCheckRunPending, CreatedAt: state.now.Add(40 * time.Second)}
	if _, created, err := store.CreateClearDevComplexExecutionCheckRun(ctx, check); err != nil || !created {
		t.Fatalf("create PARALLEL integration check: created=%t err=%v", created, err)
	}
	if changed, err := store.StartClearDevComplexExecutionCheckRun(ctx, id, state.now.Add(41*time.Second)); err != nil || !changed {
		t.Fatalf("start PARALLEL integration check: %v", err)
	}
	exit := 0
	if changed, err := store.SettleClearDevComplexExecutionCheckRun(ctx, core.SettleComplexExecutionCheckCommand{CheckRunID: id, Result: core.EvidenceResultPass, ContainerImageID: "cleardev-go-validation", ExitCode: &exit, OutputSummary: "pass", OutputSHA256: strings.Repeat("d", 64), ChangedPathsJSON: "[]", At: state.now.Add(42 * time.Second)}); err != nil || !changed {
		t.Fatalf("settle PARALLEL integration check: %v", err)
	}
	_ = lastCandidateSHA
	return id
}
