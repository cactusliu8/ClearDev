package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestQuickExecutionStoreFlow(t *testing.T) {
	t.Run("materializes one Builder task and moves the task set from 1 to 2", func(t *testing.T) {
		store, state, integrationSHA := seedCompletedParallelForQuick(t)
		ctx := context.Background()
		started := startQuickRun(t, store, state, integrationSHA, "s08-flow-run")
		command := quickMaterializeCommand(t, store, state, started, integrationSHA)
		if err := store.MaterializeClearDevComplexQuickExecution(ctx, command); err != nil {
			t.Fatalf("materialize QUICK: %v", err)
		}
		requirement, ok, err := store.GetClearDevRequirement(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		for _, version := range requirement.RequirementVersions {
			if version.ID == state.prep.V2ID && version.TaskSetVersion != core.ComplexStandardTaskSetVersion+1 {
				t.Fatalf("task-set version = %d", version.TaskSetVersion)
			}
		}
		snapshot, ok, err := store.GetClearDevComplexQuickExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok || snapshot.Task == nil || snapshot.Task.TaskKey != "deduplicate-email" {
			t.Fatalf("materialized QUICK = ok:%t err:%v task:%#v", ok, err, snapshot.Task)
		}
		builders, reviewers := 0, 0
		for _, binding := range snapshot.RoleBindings {
			if binding.Role == core.StandardRoleBuilder {
				builders++
			}
			if binding.Role == core.StandardRoleReviewer {
				reviewers++
			}
		}
		if builders != 1 || reviewers != 0 {
			t.Fatalf("QUICK roles builders=%d reviewers=%d", builders, reviewers)
		}
	})

	t.Run("invalid materialization rolls back the task set", func(t *testing.T) {
		store, state, integrationSHA := seedCompletedParallelForQuick(t)
		ctx := context.Background()
		started := startQuickRun(t, store, state, integrationSHA, "s08-rollback-run")
		command := quickMaterializeCommand(t, store, state, started, integrationSHA)
		command.SourceTaskKey = "build-summary"
		command.Task.TaskKey = "build-summary"
		if err := store.MaterializeClearDevComplexQuickExecution(ctx, command); err == nil {
			t.Fatal("mismatched source task was materialized")
		}
		requirement, ok, err := store.GetClearDevRequirement(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		for _, version := range requirement.RequirementVersions {
			if version.ID == state.prep.V2ID && version.TaskSetVersion != core.ComplexStandardTaskSetVersion {
				t.Fatalf("rolled-back QUICK changed task-set version to %d", version.TaskSetVersion)
			}
		}
		snapshot, ok, err := store.GetClearDevComplexQuickExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok || snapshot.Task != nil {
			t.Fatalf("rolled-back QUICK left a task: ok:%t err:%v task:%#v", ok, err, snapshot.Task)
		}
	})

	t.Run("generic SQL cannot forge DONE or a Reviewer", func(t *testing.T) {
		store, state, integrationSHA := seedCompletedParallelForQuick(t)
		ctx := context.Background()
		started := startQuickRun(t, store, state, integrationSHA, "s08-guard-run")
		command := quickMaterializeCommand(t, store, state, started, integrationSHA)
		if err := store.MaterializeClearDevComplexQuickExecution(ctx, command); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", "file:"+filepath.Join(state.dataDir, "ao.db")+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`UPDATE cleardev_work_items SET state = 'DONE' WHERE complex_quick_task_id IS NOT NULL`); err == nil {
			t.Fatal("generic SQL forged QUICK DONE")
		}
		if _, err := db.Exec(`INSERT INTO cleardev_complex_quick_role_bindings (
id, quick_run_id, role, session_creation_idempotency_key, workspace_path, status, requested_at
) VALUES ('reviewer', ?, 'REVIEWER', 's08-reviewer', '/tmp', 'REQUESTED', ?)`, started.ID, state.now.Add(4*time.Second).Format(time.RFC3339)); err == nil {
			t.Fatal("QUICK accepted a Reviewer binding")
		}
	})

	t.Run("atomic complete writes the successor integration", func(t *testing.T) {
		store, state, integrationSHA := seedCompletedParallelForQuick(t)
		ctx := context.Background()
		started := startQuickRun(t, store, state, integrationSHA, "s08-complete-run")
		command := quickMaterializeCommand(t, store, state, started, integrationSHA)
		if err := store.MaterializeClearDevComplexQuickExecution(ctx, command); err != nil {
			t.Fatal(err)
		}
		candidateSHA := strings.Repeat("9", 40)
		checkID := settleQuickBuilderAttempt(t, store, state, started, command, integrationSHA, candidateSHA)
		at := state.now.Add(80 * time.Second)
		if err := store.CompleteClearDevComplexQuickExecution(ctx, core.CompleteComplexQuickExecutionCommand{
			RunID: started.ID,
			Integration: core.ComplexExecutionIntegration{
				ID: "s08-result", ExecutionRunID: started.ID, IntegrationCandidateID: "s08-integration",
				CandidateCommitSHA: candidateSHA, CheckRunIDs: []string{checkID}, CompletedAt: at,
			},
			At: at,
		}); err != nil {
			t.Fatalf("complete QUICK: %v", err)
		}
		snapshot, ok, err := store.GetClearDevComplexQuickExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok || snapshot.Run.CompletedAt == nil || snapshot.Integration == nil || snapshot.Task == nil || snapshot.Task.Status != core.DevelopmentTaskStatusDone {
			t.Fatalf("completed QUICK = ok:%t err:%v snapshot:%#v", ok, err, snapshot)
		}
		requirement, ok, err := store.GetClearDevRequirement(ctx, state.prep.RequirementID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		var current core.RequirementVersion
		for _, version := range requirement.RequirementVersions {
			if version.ID == state.prep.V2ID {
				current = version
			}
		}
		if current.TaskSetVersion != started.TaskSetVersion {
			t.Fatalf("completed task set = %d", current.TaskSetVersion)
		}
		found := false
		for _, candidate := range requirement.IntegrationCandidates {
			if candidate.ID == "s08-integration" && candidate.TaskSetVersion != nil && *candidate.TaskSetVersion == started.TaskSetVersion && candidate.CommitSHA == candidateSHA {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing successor integration: %#v", requirement.IntegrationCandidates)
		}
	})
}

func TestQuickExecutionRejectsTamperedInheritedChecks(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*core.ComplexExecutionCheckSpecFact)
	}{
		{name: "name", mutate: func(spec *core.ComplexExecutionCheckSpecFact) { spec.CheckID = "renamed-check" }},
		{name: "argv", mutate: func(spec *core.ComplexExecutionCheckSpecFact) { spec.Argv = []string{"node", "--version"} }},
		{name: "timeout", mutate: func(spec *core.ComplexExecutionCheckSpecFact) { spec.TimeoutSeconds++ }},
		{name: "digest", mutate: func(spec *core.ComplexExecutionCheckSpecFact) { spec.CheckSpecSHA256 = strings.Repeat("7", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, state, integrationSHA := seedCompletedParallelForQuick(t)
			started := startQuickRun(t, store, state, integrationSHA, "s12a2-check-"+test.name)
			command := quickMaterializeCommand(t, store, state, started, integrationSHA)
			found := false
			for index := range command.CheckSpecs {
				if command.CheckSpecs[index].Kind == core.CandidateCheckRequired {
					test.mutate(&command.CheckSpecs[index])
					found = true
					break
				}
			}
			if !found {
				t.Fatal("missing inherited required check")
			}
			if err := store.MaterializeClearDevComplexQuickExecution(context.Background(), command); err == nil {
				t.Fatalf("tampered inherited check %s was accepted", test.name)
			}
			assertQuickMaterializationRolledBack(t, store, state, started.ExpectedTaskSetVersion)
		})
	}
}

func TestQuickExecutionRejectsTamperedSourceBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*core.StartComplexQuickExecutionCommand)
	}{
		{name: "source-commit", mutate: func(command *core.StartComplexQuickExecutionCommand) {
			command.Run.IntegrationBaseSHA = strings.Repeat("8", 40)
		}},
		{name: "requirement-binding", mutate: func(command *core.StartComplexQuickExecutionCommand) {
			command.Run.RequirementSHA256 = strings.Repeat("7", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, state, integrationSHA := seedCompletedParallelForQuick(t)
			command := quickStartCommand(t, store, state, integrationSHA, "s12a2-binding-"+test.name)
			test.mutate(&command)
			rebuildQuickStartPackage(t, &command)
			if _, created, err := store.StartClearDevComplexQuickExecution(context.Background(), command); err == nil || created {
				t.Fatalf("tampered %s was accepted: created=%t err=%v", test.name, created, err)
			}
			if _, ok, err := store.GetClearDevComplexQuickExecution(context.Background(), state.prep.RequirementID); err != nil || ok {
				t.Fatalf("tampered %s left a QUICK run: ok=%t err=%v", test.name, ok, err)
			}
		})
	}
}

func TestQuickExecutionRechecksCurrentProjectBeforeAcceptance(t *testing.T) {
	store, state, integrationSHA := seedCompletedParallelForQuick(t)
	started := startQuickRun(t, store, state, integrationSHA, "s12a2-current-project")
	command := quickMaterializeCommand(t, store, state, started, integrationSHA)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(state.dataDir, "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`UPDATE cleardev_development_projects SET cancelled_at = ? WHERE id = ?`, state.now.Add(72*time.Second), state.prep.RequirementID); err != nil {
		t.Fatalf("cancel project between QUICK request and acceptance: %v", err)
	}
	if err := store.MaterializeClearDevComplexQuickExecution(context.Background(), command); err == nil {
		t.Fatal("QUICK was accepted after its project was cancelled")
	}
	assertQuickMaterializationRolledBack(t, store, state, started.ExpectedTaskSetVersion)
}

func assertQuickMaterializationRolledBack(t *testing.T, store *sqlite.Store, state complexParallelFlowState, expectedTaskSetVersion int64) {
	t.Helper()
	requirement, ok, err := store.GetClearDevRequirement(context.Background(), state.prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("read requirement after rejected QUICK materialization: ok=%t err=%v", ok, err)
	}
	for _, version := range requirement.RequirementVersions {
		if version.ID == state.prep.V2ID && version.TaskSetVersion != expectedTaskSetVersion {
			t.Fatalf("rejected QUICK changed task-set version to %d", version.TaskSetVersion)
		}
	}
	snapshot, ok, err := store.GetClearDevComplexQuickExecution(context.Background(), state.prep.RequirementID)
	if err != nil || !ok || snapshot.Task != nil {
		t.Fatalf("rejected QUICK materialization left a task: ok=%t err=%v task=%#v", ok, err, snapshot.Task)
	}
}

func seedCompletedParallelForQuick(t *testing.T) (*sqlite.Store, complexParallelFlowState, string) {
	t.Helper()
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
		Integration:    core.ComplexExecutionIntegration{ID: "s07-complete", ExecutionRunID: state.run.ID, IntegrationCandidateID: "s07-integration", CandidateCommitSHA: composed1, CheckRunIDs: []string{checkID}, CompletedAt: at},
		At:             at,
	}); err != nil {
		t.Fatalf("complete S07 for QUICK: %v", err)
	}
	return store, state, composed1
}

func startQuickRun(t *testing.T, store *sqlite.Store, state complexParallelFlowState, integrationSHA, runID string) core.ComplexQuickRun {
	t.Helper()
	command := quickStartCommand(t, store, state, integrationSHA, runID)
	if _, created, err := store.StartClearDevComplexQuickExecution(context.Background(), command); err != nil || !created {
		t.Fatalf("start QUICK: created=%t err=%v", created, err)
	}
	return command.Run
}

func quickStartCommand(t *testing.T, store *sqlite.Store, state complexParallelFlowState, integrationSHA, runID string) core.StartComplexQuickExecutionCommand {
	t.Helper()
	ctx := context.Background()
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
	planning, ok, err := store.GetClearDevComplexPlanning(ctx, state.prep.RequirementID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	var plan core.ComplexEngineeringPlan
	var steward core.ComplexRoleBinding
	for _, item := range planning.Plans {
		if item.ID == state.prep.V2PlanID {
			plan = item
		}
	}
	for _, item := range planning.RoleBindings {
		if item.Role == core.StandardRoleSteward && item.Status == core.RoleBindingStatusBound {
			steward = item
		}
	}
	nextTaskSetVersion, err := core.NextComplexQuickTaskSetVersion(version.TaskSetVersion)
	if err != nil {
		t.Fatal(err)
	}
	raw, digest, err := core.BuildComplexQuickRunPackage(runID, version.ID, version.SHA256, plan.ID, plan.PlanSHA256, "", integrationSHA, nextTaskSetVersion)
	if err != nil {
		t.Fatal(err)
	}
	now := state.now.Add(71 * time.Second)
	run := core.ComplexQuickRun{
		ID: runID, DevelopmentRequirementID: state.prep.RequirementID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256,
		SourceExecutionRunID: state.run.ID, PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, IntegrationBaseSHA: integrationSHA,
		Mode: core.WorkModeQuick, ExpectedTaskSetVersion: version.TaskSetVersion, TaskSetVersion: nextTaskSetVersion, ExecutionPackageJSON: string(raw),
		ExecutionPackageSHA256: digest, StewardRoleBindingID: steward.ID, CreatedAt: now,
	}
	binding := core.ComplexExecutionRoleBinding{
		ID: runID + "-steward", ExecutionRunID: runID, SourceComplexRoleBindingID: steward.ID, Role: core.StandardRoleSteward,
		SessionCreationIdempotencyKey: runID + "-steward", AOSessionID: state.prep.StewardSessionID, Status: core.RoleBindingStatusBound,
		RequestedAt: now, BoundAt: &now,
	}
	step := core.AgentStep{
		ID: runID + "-steward-step", RoleBindingID: binding.ID, Kind: core.AgentStepDispatchRequest, RequestID: runID,
		ClientMessageID: runID + "-steward-client", PromptSHA256: strings.Repeat("a", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now,
	}
	return core.StartComplexQuickExecutionCommand{Run: run, StewardRoleBinding: binding, AgentStep: step}
}

func rebuildQuickStartPackage(t *testing.T, command *core.StartComplexQuickExecutionCommand) {
	t.Helper()
	run := &command.Run
	raw, digest, err := core.BuildComplexQuickRunPackage(
		run.ID,
		run.RequirementVersionID,
		run.RequirementSHA256,
		run.PlanID,
		run.PlanSHA256,
		run.SourceTaskKey,
		run.IntegrationBaseSHA,
		run.TaskSetVersion,
	)
	if err != nil {
		t.Fatal(err)
	}
	run.ExecutionPackageJSON = string(raw)
	run.ExecutionPackageSHA256 = digest
}

func quickMaterializeCommand(t *testing.T, store *sqlite.Store, state complexParallelFlowState, run core.ComplexQuickRun, integrationSHA string) core.MaterializeComplexQuickExecutionCommand {
	t.Helper()
	ctx := context.Background()
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
	source := core.ComplexPlanTask{
		Key: "deduplicate-email", Title: "去重邮箱", Objective: "收紧无效邮箱过滤。",
		RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"},
		WritePaths: []string{"src/deduplicate.js", "test/deduplicate.test.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{},
		ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"deduplicate-unit"},
	}
	task := core.ComplexExecutionTask{ID: run.ID + "-task", ExecutionRunID: run.ID, TaskKey: source.Key, DevelopmentTaskID: run.ID + "-work", Ordinal: 0, Status: core.DevelopmentTaskStatusPlanned}
	required, _ := core.FrozenComplexCheckByID("deduplicate-unit")
	integration, _ := core.FrozenComplexCheckByID("all-tests")
	_, encoded, digest, err := core.BuildComplexQuickExecutionPackage(core.ComplexStandardExecutionPackageInput{
		ExecutionRunID: run.ID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, RequirementText: version.RequirementText,
		PlanID: run.PlanID, PlanSHA256: run.PlanSHA256, TaskSetVersion: run.TaskSetVersion, TaskID: task.DevelopmentTaskID, Task: source,
	}, source.WritePaths, []core.ComplexExecutionCheckSpec{{ID: required.ID, Argv: required.Argv, TimeoutSeconds: required.TimeoutSeconds}})
	if err != nil {
		t.Fatal(err)
	}
	task.ExecutionPackageJSON, task.ExecutionPackageSHA256 = string(encoded), digest
	now := state.now.Add(72 * time.Second)
	command := core.MaterializeComplexQuickExecutionCommand{
		RunID: run.ID, SourceTaskKey: source.Key, Task: task, At: now,
		WritePaths: source.WritePaths, GeneratedPaths: []string{}, SharedPaths: []string{}, ForbiddenPaths: source.ForbiddenPaths,
		BuilderBinding: core.ComplexExecutionRoleBinding{ID: run.ID + "-builder", ExecutionRunID: run.ID, Role: core.StandardRoleBuilder, SessionCreationIdempotencyKey: "cleardev-complex-execution:builder-quick:" + run.ID, Status: core.RoleBindingStatusRequested, RequestedAt: now},
	}
	command.CheckSpecs = []core.ComplexExecutionCheckSpecFact{
		{ID: run.ID + "-required", ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, CheckID: required.ID, Kind: core.CandidateCheckRequired, CheckSpecSHA256: complexExecutionTestDigest(required.Argv), Argv: required.Argv, TimeoutSeconds: required.TimeoutSeconds, CreatedAt: now},
		{ID: run.ID + "-scope", ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, CheckID: "SCOPE", Kind: core.CandidateCheckScope, CheckSpecSHA256: complexExecutionTestScopeDigest(core.ComplexStandardExecutionPackage{WritePaths: source.WritePaths, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: source.ForbiddenPaths}), Argv: []string{}, TimeoutSeconds: 0, CreatedAt: now},
		{ID: run.ID + "-integration", ExecutionRunID: run.ID, CheckID: integration.ID, Kind: core.CandidateCheckIntegration, CheckSpecSHA256: complexExecutionTestDigest(integration.Argv), Argv: integration.Argv, TimeoutSeconds: integration.TimeoutSeconds, CreatedAt: now},
	}
	_ = integrationSHA
	return command
}

func settleQuickBuilderAttempt(t *testing.T, store *sqlite.Store, state complexParallelFlowState, run core.ComplexQuickRun, command core.MaterializeComplexQuickExecutionCommand, baseSHA, candidateSHA string) string {
	t.Helper()
	ctx := context.Background()
	now := state.now.Add(73 * time.Second)
	workspace := "/worktrees/s08-builder"
	session := createComplexExecutionSession(t, store, state.projectID, "s08-builder-session", command.BuilderBinding.SessionCreationIdempotencyKey, workspace, baseSHA, now)
	if changed, err := store.BindClearDevComplexQuickRoleBinding(ctx, command.BuilderBinding.ID, string(session.ID), workspace, baseSHA, now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("bind QUICK Builder: changed=%t err=%v", changed, err)
	}
	dispatchID := run.ID + "-dispatch"
	step := core.AgentStep{ID: run.ID + "-builder-step", RoleBindingID: command.BuilderBinding.ID, Kind: core.ComplexExecutionAgentStepBuilderTask, RequestID: dispatchID, ClientMessageID: run.ID + "-builder-client", PromptSHA256: strings.Repeat("b", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now.Add(2 * time.Second)}
	dispatch := core.ComplexExecutionDispatch{ID: dispatchID, ExecutionRunID: run.ID, ComplexExecutionTaskID: command.Task.ID, DevelopmentTaskID: command.Task.DevelopmentTaskID, Round: 0, BaseCommitSHA: baseSHA, AgentStepID: step.ID, ExecutionPackageSHA256: command.Task.ExecutionPackageSHA256, ClientMessageID: step.ClientMessageID, Status: core.ComplexExecutionDispatchPending, CreatedAt: now.Add(2 * time.Second)}
	if _, created, err := store.CreateClearDevComplexQuickDispatch(ctx, core.CreateComplexExecutionDispatchCommand{Dispatch: dispatch, AgentStep: step}); err != nil || !created {
		t.Fatalf("dispatch QUICK: created=%t err=%v", created, err)
	}
	if _, err := store.AppendClearDevComplexQuickCandidate(ctx, core.AppendComplexExecutionCandidateCommand{
		ExecutionRunID: run.ID, ComplexExecutionTaskID: command.Task.ID, DispatchID: dispatchID, Round: 0, BaseCommitSHA: baseSHA,
		Candidate: core.CandidateObservation{ID: run.ID + "-candidate", DevelopmentTaskID: command.Task.DevelopmentTaskID, AOSessionID: string(session.ID), CommitSHA: candidateSHA, ObservedAt: now.Add(3 * time.Second)},
	}); err != nil {
		t.Fatalf("append QUICK candidate: %v", err)
	}
	var integrationID string
	exit := 0
	for _, spec := range command.CheckSpecs {
		checkID := spec.ID + "-run"
		check := core.ComplexExecutionCheckRun{ID: checkID, ExecutionRunID: run.ID, ComplexExecutionTaskID: command.Task.ID, DispatchID: dispatchID, CandidateCommitID: run.ID + "-candidate", CandidateCommitSHA: candidateSHA, CheckSpecFactID: spec.ID, Kind: spec.Kind, Argv: spec.Argv, Status: core.ComplexExecutionCheckRunPending, CreatedAt: now.Add(4 * time.Second)}
		if _, created, err := store.CreateClearDevComplexQuickCheckRun(ctx, check); err != nil || !created {
			t.Fatalf("create QUICK check %s: created=%t err=%v", spec.Kind, created, err)
		}
		if changed, err := store.StartClearDevComplexQuickCheckRun(ctx, checkID, now.Add(5*time.Second)); err != nil || !changed {
			t.Fatalf("start QUICK check %s: %v", spec.Kind, err)
		}
		if changed, err := store.SettleClearDevComplexQuickCheckRun(ctx, core.SettleComplexExecutionCheckCommand{CheckRunID: checkID, Result: core.EvidenceResultPass, ContainerImageID: "cleardev-go-validation", ExitCode: &exit, OutputSummary: "pass", OutputSHA256: strings.Repeat("d", 64), ChangedPathsJSON: "[]", At: now.Add(6 * time.Second)}); err != nil || !changed {
			t.Fatalf("settle QUICK check %s: %v", spec.Kind, err)
		}
		if spec.Kind == core.CandidateCheckIntegration {
			integrationID = checkID
		}
	}
	if integrationID == "" {
		t.Fatal("missing QUICK integration check")
	}
	return integrationID
}
