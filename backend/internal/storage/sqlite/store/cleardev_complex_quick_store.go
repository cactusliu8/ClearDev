package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func complexQuickRunFromGen(row gen.CleardevComplexQuickRun) cleardev.ComplexQuickRun {
	run := cleardev.ComplexQuickRun{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, RequirementVersionID: row.RequirementVersionID,
		RequirementSHA256: row.RequirementSha256, SourceExecutionRunID: row.SourceExecutionRunID, PlanID: row.PlanID,
		PlanSHA256: row.PlanSha256, SourceTaskKey: row.SourceTaskKey, IntegrationBaseSHA: row.IntegrationBaseSha,
		Mode: cleardev.WorkMode(row.Mode), ModeReason: row.SelectionReasonCode, ExpectedTaskSetVersion: row.ExpectedTaskSetVersion,
		TaskSetVersion: row.AcceptedTaskSetVersion, ExecutionPackageJSON: row.ExecutionPackageJson,
		ExecutionPackageSHA256: row.ExecutionPackageSha256, StewardRoleBindingID: row.StewardRoleBindingID,
		ReasonCode: cleardev.ReasonCode(row.ReasonCode), CreatedAt: row.RequestedAt,
	}
	switch row.Status {
	case "ACCEPTED", "COMPLETED":
		run.Decision = cleardev.ComplexQuickDecisionSubmit
	case "NEEDS_HUMAN":
		run.Decision = string(cleardev.ComplexExecutionDecisionNeedsHuman)
	}
	if row.SettledAt.Valid && row.Status == "COMPLETED" {
		at := row.SettledAt.Time
		run.CompletedAt = &at
	}
	return run
}

func complexQuickRoleBindingFromGen(row gen.CleardevComplexQuickRoleBinding) cleardev.ComplexExecutionRoleBinding {
	binding := cleardev.ComplexExecutionRoleBinding{
		ID: row.ID, ExecutionRunID: row.QuickRunID, SourceComplexRoleBindingID: row.SourceComplexRoleBindingID.String,
		ContinuationOfRoleBindingID: row.ContinuationOfRoleBindingID.String, Role: cleardev.StandardRole(row.Role),
		SessionCreationIdempotencyKey: row.SessionCreationIdempotencyKey, AOSessionID: row.AoSessionID.String,
		WorkspacePath: row.WorkspacePath, BaseCommitSHA: row.BaseCommitSha, Status: cleardev.RoleBindingStatus(row.Status),
		ReasonCode: cleardev.ReasonCode(row.ReasonCode), RequestedAt: row.RequestedAt,
	}
	if row.BoundAt.Valid {
		at := row.BoundAt.Time
		binding.BoundAt = &at
	}
	if row.EndedAt.Valid {
		at := row.EndedAt.Time
		binding.EndedAt = &at
	}
	return binding
}

func complexQuickAgentStepFromGen(row gen.CleardevComplexQuickAgentStep) cleardev.AgentStep {
	sent, completed, failed := complexExecutionAgentStepTimes(row.SentAt, row.CompletedAt, row.FailedAt)
	return cleardev.AgentStep{
		ID: row.ID, RoleBindingID: row.RoleBindingID, Kind: cleardev.AgentStepKind(row.StepKind), RequestID: row.RequestID,
		ClientMessageID: row.ClientMessageID, PromptSHA256: row.PromptSha256, SendStatus: cleardev.AgentStepSendStatus(row.SendStatus),
		TurnID: row.TurnID.String, FinalMessageID: row.FinalMessageID.String, FinalMessageText: row.FinalMessageText.String,
		MessageSHA256: row.MessageSha256.String, RequestedAt: row.RequestedAt, SentAt: sent, CompletedAt: completed, FailedAt: failed,
		ReasonCode: cleardev.ReasonCode(row.ReasonCode),
	}
}

func complexQuickDispatchFromGen(row gen.CleardevComplexQuickTaskAttempt) cleardev.ComplexExecutionDispatch {
	dispatch := cleardev.ComplexExecutionDispatch{
		ID: row.ID, ExecutionRunID: row.QuickRunID, ComplexExecutionTaskID: row.TaskMappingID, Round: int(row.Round),
		BaseCommitSHA: row.BaseCommitSha, AgentStepID: row.AgentStepID, Status: cleardev.ComplexExecutionDispatchStatus(row.Status),
		ReasonCode: cleardev.ReasonCode(row.ReasonCode), BuilderRoleBindingID: row.BuilderRoleBindingID,
	}
	if row.DispatchedAt.Valid {
		dispatch.CreatedAt = row.DispatchedAt.Time
	}
	if row.SettledAt.Valid {
		at := row.SettledAt.Time
		dispatch.SettledAt = &at
	}
	return dispatch
}

func complexQuickCheckRunFromGen(row gen.CleardevComplexQuickCheckRun) cleardev.ComplexExecutionCheckRun { //nolint:dupl // sqlc row type differs from S06
	run := cleardev.ComplexExecutionCheckRun{
		ID: row.ID, DispatchID: row.TaskAttemptID, CandidateCommitID: row.CandidateCommitID, CandidateCommitSHA: row.CandidateCommitSha,
		CheckSpecFactID: row.CheckSpecID, Status: cleardev.ComplexExecutionCheckRunStatus(row.Status),
		Result: cleardev.EvidenceResult(row.Result.String), ReasonCode: cleardev.ReasonCode(row.ReasonCode),
		ContainerImageID: row.ContainerImageID.String, TimedOut: row.TimedOut.Bool, OutputSummary: row.OutputSummary.String,
		OutputSHA256: row.OutputSha256.String, ChangedPathsJSON: row.ChangedPathsJson.String, CreatedAt: row.CreatedAt,
	}
	if row.ExitCode.Valid {
		code := int(row.ExitCode.Int64)
		run.ExitCode = &code
	}
	if row.StartedAt.Valid {
		at := row.StartedAt.Time
		run.StartedAt = &at
	}
	if row.SettledAt.Valid {
		at := row.SettledAt.Time
		run.SettledAt = &at
	}
	return run
}

// GetClearDevComplexQuickExecution rebuilds the S08 read model from independent facts.
func (s *Store) GetClearDevComplexQuickExecution(ctx context.Context, requirementID string) (cleardev.ComplexQuickSnapshot, bool, error) {
	row, err := s.qr.GetClearDevComplexQuickRunForRequirement(ctx, requirementID)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.ComplexQuickSnapshot{}, false, nil
	}
	if err != nil {
		return cleardev.ComplexQuickSnapshot{}, false, err
	}
	snapshot := cleardev.ComplexQuickSnapshot{Run: complexQuickRunFromGen(row), RoleBindings: []cleardev.ComplexExecutionRoleBinding{}, AgentSteps: []cleardev.AgentStep{}, Dispatches: []cleardev.ComplexExecutionDispatch{}, CheckSpecs: []cleardev.ComplexExecutionCheckSpecFact{}, CheckRuns: []cleardev.ComplexExecutionCheckRun{}}
	bindings, err := s.qr.ListClearDevComplexQuickRoleBindings(ctx, row.ID)
	if err != nil {
		return snapshot, false, err
	}
	for _, binding := range bindings {
		item := complexQuickRoleBindingFromGen(binding)
		snapshot.RoleBindings = append(snapshot.RoleBindings, item)
		if item.Role == cleardev.StandardRoleBuilder && snapshot.Run.BuilderRoleBindingID == "" {
			snapshot.Run.BuilderRoleBindingID = item.ID
		}
		if item.Role == cleardev.StandardRoleBuilder && item.Status == cleardev.RoleBindingStatusBound {
			snapshot.Run.BuilderRoleBindingID = item.ID
		}
	}
	steps, err := s.qr.ListClearDevComplexQuickAgentSteps(ctx, row.ID)
	if err != nil {
		return snapshot, false, err
	}
	for _, step := range steps {
		snapshot.AgentSteps = append(snapshot.AgentSteps, complexQuickAgentStepFromGen(step))
	}
	taskRow, taskErr := s.qr.GetClearDevComplexQuickTaskMappingForRun(ctx, row.ID)
	if taskErr != nil && !errors.Is(taskErr, sql.ErrNoRows) {
		return snapshot, false, taskErr
	}
	if taskErr == nil {
		item, itemErr := s.qr.GetClearDevComplexQuickTaskByWorkItem(ctx, taskRow.ID)
		if itemErr != nil {
			return snapshot, false, itemErr
		}
		task := cleardev.ComplexExecutionTask{
			ID: taskRow.ID, ExecutionRunID: taskRow.QuickRunID, TaskKey: taskRow.PlanTaskKey, DevelopmentTaskID: taskRow.WorkItemID,
			Ordinal: int(taskRow.Ordinal), ExecutionPackageJSON: taskRow.TaskPacketJson, ExecutionPackageSHA256: taskRow.TaskPacketSha256,
			Status: cleardev.DevelopmentTaskStatus(item.State), ReworkCount: int(item.ReworkCount),
		}
		snapshot.Run.DevelopmentTaskID = task.DevelopmentTaskID
		attempts, attemptErr := s.qr.ListClearDevComplexQuickTaskAttempts(ctx, row.ID)
		if attemptErr != nil {
			return snapshot, false, attemptErr
		}
		for _, attempt := range attempts {
			dispatch := complexQuickDispatchFromGen(attempt)
			dispatch.DevelopmentTaskID = task.DevelopmentTaskID
			dispatch.ExecutionPackageSHA256 = task.ExecutionPackageSHA256
			for _, step := range snapshot.AgentSteps {
				if step.ID == attempt.AgentStepID {
					dispatch.ClientMessageID = step.ClientMessageID
					break
				}
			}
			candidate, candidateErr := s.qr.GetClearDevComplexQuickCandidate(ctx, nullableString(attempt.ID))
			if candidateErr == nil {
				dispatch.CandidateCommitID = candidate.ID
				dispatch.CandidateCommitSHA = candidate.CommitSha
			} else if !errors.Is(candidateErr, sql.ErrNoRows) {
				return snapshot, false, candidateErr
			}
			snapshot.Dispatches = append(snapshot.Dispatches, dispatch)
			task.CurrentRound = int(attempt.Round)
			task.CurrentDispatchID = attempt.ID
			if task.Status == cleardev.DevelopmentTaskStatusDone {
				continue
			}
			switch attempt.Status {
			case "PENDING", "RUNNING", "OBSERVED":
				task.Status = cleardev.DevelopmentTaskStatusRunning
			case "REWORK":
				task.Status = cleardev.DevelopmentTaskStatusRework
				task.CurrentRound = int(attempt.Round) + 1
			case "BLOCKED", "FAILED":
				task.Status = cleardev.DevelopmentTaskStatusBlocked
			case "NEEDS_HUMAN":
				task.Status = cleardev.DevelopmentTaskStatusNeedsHuman
			}
		}
		snapshot.Task = &task
	}
	specs, err := s.qr.ListClearDevComplexQuickCheckSpecs(ctx, row.ID)
	if err != nil {
		return snapshot, false, err
	}
	specByID := map[string]gen.CleardevComplexQuickCheckSpec{}
	specArgv := map[string][]string{}
	for _, spec := range specs {
		argv, e := decodeJSONStringSlice(spec.ArgvJson, "quick check spec argv")
		if e != nil {
			return snapshot, false, e
		}
		specByID[spec.ID] = spec
		specArgv[spec.ID] = argv
		checkID := spec.CheckName
		if spec.CheckKind == string(cleardev.CandidateCheckScope) {
			checkID = "SCOPE"
		}
		snapshot.CheckSpecs = append(snapshot.CheckSpecs, cleardev.ComplexExecutionCheckSpecFact{
			ID: spec.ID, ExecutionRunID: spec.QuickRunID, ComplexExecutionTaskID: spec.TaskMappingID.String, CheckID: checkID,
			Kind: cleardev.CandidateCheckKind(spec.CheckKind), CheckSpecSHA256: spec.CheckSpecSha256, Argv: argv, TimeoutSeconds: int(spec.TimeoutSeconds), CreatedAt: spec.CreatedAt,
		})
	}
	runs, err := s.qr.ListClearDevComplexQuickCheckRuns(ctx, row.ID)
	if err != nil {
		return snapshot, false, err
	}
	for _, checkRun := range runs {
		item := complexQuickCheckRunFromGen(checkRun)
		if spec, ok := specByID[checkRun.CheckSpecID]; ok {
			item.Kind = cleardev.CandidateCheckKind(spec.CheckKind)
			item.ExecutionRunID = spec.QuickRunID
			item.ComplexExecutionTaskID = spec.TaskMappingID.String
			item.Argv = append([]string(nil), specArgv[checkRun.CheckSpecID]...)
		}
		snapshot.CheckRuns = append(snapshot.CheckRuns, item)
	}
	result, resultErr := s.qr.GetClearDevComplexQuickResult(ctx, row.ID)
	if resultErr == nil {
		checks, checkErr := s.qr.ListClearDevComplexQuickResultChecks(ctx, result.ID)
		if checkErr != nil {
			return snapshot, false, checkErr
		}
		ids := make([]string, 0, len(checks))
		for _, check := range checks {
			ids = append(ids, check.CheckRunID)
		}
		sha := ""
		for _, dispatch := range snapshot.Dispatches {
			if dispatch.CandidateCommitSHA != "" {
				sha = dispatch.CandidateCommitSHA
			}
		}
		integration := cleardev.ComplexExecutionIntegration{ID: result.ID, ExecutionRunID: row.ID, IntegrationCandidateID: result.IntegrationCandidateID, CandidateCommitSHA: sha, CheckRunIDs: ids}
		if result.CompletedAt.Valid {
			at := result.CompletedAt.Time
			integration.CompletedAt = at
		}
		snapshot.Integration = &integration
	} else if !errors.Is(resultErr, sql.ErrNoRows) {
		return snapshot, false, resultErr
	}
	snapshot.Phase, snapshot.PhaseReason = cleardev.DeriveComplexQuickPhase(snapshot)
	snapshot.MissingEvidence = cleardev.DeriveComplexQuickMissingEvidence(snapshot)
	return snapshot, true, nil
}

// ListClearDevRunnableComplexQuickExecutions returns requirement IDs with unfinished QUICK runs.
func (s *Store) ListClearDevRunnableComplexQuickExecutions(ctx context.Context) ([]string, error) {
	return s.qr.ListClearDevRunnableComplexQuickExecutions(ctx)
}

// StartClearDevComplexQuickExecution persists the follow-up request and Steward step before Chat.
func (s *Store) StartClearDevComplexQuickExecution(ctx context.Context, c cleardev.StartComplexQuickExecutionCommand) (cleardev.ComplexQuickRun, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.ComplexQuickRun
	var created bool
	err := s.inTx(ctx, "start ClearDev complex quick execution", func(q *gen.Queries) error {
		if old, e := q.GetClearDevComplexQuickRunForRequirement(ctx, c.Run.DevelopmentRequirementID); e == nil {
			out = complexQuickRunFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		run := c.Run
		nextTaskSetVersion, taskSetErr := cleardev.NextComplexQuickTaskSetVersion(run.ExpectedTaskSetVersion)
		if run.Mode != cleardev.WorkModeQuick || taskSetErr != nil || run.TaskSetVersion != nextTaskSetVersion ||
			!complexExecutionSHA1(run.IntegrationBaseSHA) || run.ExecutionPackageJSON == "" || len(run.ExecutionPackageSHA256) != 64 {
			return complexExecutionRule("invalid quick execution run")
		}
		if c.StewardRoleBinding.ExecutionRunID != run.ID || c.StewardRoleBinding.Role != cleardev.StandardRoleSteward {
			return complexExecutionRule("invalid initial Steward binding or step")
		}
		if err := requireOptionalDispatchStep(c.StewardRoleBinding, c.AgentStep, run.ID); err != nil {
			return err
		}
		if e := q.InsertClearDevComplexQuickRun(ctx, gen.InsertClearDevComplexQuickRunParams{
			ID: run.ID, DevelopmentProjectID: run.DevelopmentRequirementID, RequirementVersionID: run.RequirementVersionID,
			RequirementSha256: run.RequirementSHA256, SourceExecutionRunID: run.SourceExecutionRunID, PlanID: run.PlanID,
			PlanSha256: run.PlanSHA256, SourceTaskKey: run.SourceTaskKey, IntegrationBaseSha: run.IntegrationBaseSHA,
			StewardRoleBindingID: run.StewardRoleBindingID, Mode: string(cleardev.WorkModeQuick), SelectionReasonCode: "",
			ExpectedTaskSetVersion: run.ExpectedTaskSetVersion, AcceptedTaskSetVersion: run.TaskSetVersion,
			ExecutionPackageJson: run.ExecutionPackageJSON, ExecutionPackageSha256: run.ExecutionPackageSHA256,
			Status: "PENDING", RequestedAt: run.CreatedAt,
		}); e != nil {
			return e
		}
		if e := q.InsertClearDevComplexQuickRoleBinding(ctx, quickRoleBindingParams(c.StewardRoleBinding)); e != nil {
			return e
		}
		if c.AgentStep.ID != "" {
			if e := q.InsertClearDevComplexQuickAgentStep(ctx, quickAgentStepParams(c.AgentStep)); e != nil {
				return e
			}
		}
		requirement, e := q.GetClearDevRequirement(ctx, run.DevelopmentRequirementID)
		if e != nil {
			return e
		}
		if e := insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectComplexQuickRun, run.ID, cleardev.ActionRequestComplexQuick, cleardev.EventAccepted, cleardev.ReasonNone, c.StewardRoleBinding.AOSessionID, run.CreatedAt); e != nil {
			return e
		}
		out, created = run, true
		return nil
	})
	return out, created, err
}

// CreateClearDevComplexQuickRoleBinding inserts a Steward or Builder binding. Reviewer is rejected.
func (s *Store) CreateClearDevComplexQuickRoleBinding(ctx context.Context, b cleardev.ComplexExecutionRoleBinding) (cleardev.ComplexExecutionRoleBinding, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.ComplexExecutionRoleBinding
	var created bool
	err := s.inTx(ctx, "create ClearDev complex quick role", func(q *gen.Queries) error {
		if old, e := q.GetClearDevComplexQuickRoleBinding(ctx, b.ID); e == nil {
			out = complexQuickRoleBindingFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if b.Role == cleardev.StandardRoleReviewer || b.Status != cleardev.RoleBindingStatusRequested {
			return complexExecutionRule("quick execution cannot bind a Reviewer")
		}
		if e := q.InsertClearDevComplexQuickRoleBinding(ctx, quickRoleBindingParams(b)); e != nil {
			return e
		}
		out, created = b, true
		return nil
	})
	return out, created, err
}

// ContinueClearDevComplexQuickSteward ends a dead Steward session and requests a continuation.
func (s *Store) ContinueClearDevComplexQuickSteward(ctx context.Context, priorID string, binding cleardev.ComplexExecutionRoleBinding, step cleardev.AgentStep, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "continue ClearDev complex quick Steward", func(q *gen.Queries) error {
		prior, e := q.GetClearDevComplexQuickRoleBinding(ctx, priorID)
		if e != nil {
			return e
		}
		if prior.Role != string(cleardev.StandardRoleSteward) || prior.Status != string(cleardev.RoleBindingStatusBound) ||
			binding.ContinuationOfRoleBindingID != prior.ID || binding.Role != cleardev.StandardRoleSteward ||
			binding.Status != cleardev.RoleBindingStatusRequested {
			return complexExecutionRule("invalid Steward continuation")
		}
		if err := requireOptionalDispatchStep(binding, step, binding.ExecutionRunID); err != nil {
			return err
		}
		rows, e := q.EndClearDevComplexQuickRoleBindingCAS(ctx, gen.EndClearDevComplexQuickRoleBindingCASParams{ID: prior.ID, ReasonCode: "STEWARD_SESSION_ENDED", EndedAt: nullableTime(at)})
		if e != nil {
			return e
		}
		if rows != 1 {
			return complexExecutionRule("Steward continuation lost its prior binding")
		}
		if e := q.InsertClearDevComplexQuickRoleBinding(ctx, quickRoleBindingParams(binding)); e != nil {
			return e
		}
		if step.ID != "" {
			if e := q.InsertClearDevComplexQuickAgentStep(ctx, quickAgentStepParams(step)); e != nil {
				return e
			}
		}
		changed = true
		return nil
	})
	return changed, err
}

// BindClearDevComplexQuickRoleBinding records the observed session, workspace, and base SHA.
func (s *Store) BindClearDevComplexQuickRoleBinding(ctx context.Context, id, sessionID, workspacePath, baseSHA string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "bind ClearDev complex quick role", func(q *gen.Queries) error {
		binding, e := q.GetClearDevComplexQuickRoleBinding(ctx, id)
		if e != nil {
			return e
		}
		if binding.Status == "BOUND" {
			return nil
		}
		rows, e := q.BindClearDevComplexQuickRoleBindingCAS(ctx, gen.BindClearDevComplexQuickRoleBindingCASParams{ID: id, AoSessionID: nullableString(sessionID), WorkspacePath: workspacePath, BaseCommitSha: baseSHA, BoundAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// FailClearDevComplexQuickRoleBinding marks a requested binding as FAILED.
func (s *Store) FailClearDevComplexQuickRoleBinding(ctx context.Context, id string, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "fail ClearDev complex quick role", func(q *gen.Queries) error {
		rows, e := q.FailClearDevComplexQuickRoleBindingCAS(ctx, gen.FailClearDevComplexQuickRoleBindingCASParams{ID: id, ReasonCode: string(reason), EndedAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// EndClearDevComplexQuickRoleBinding marks a bound role as ENDED.
func (s *Store) EndClearDevComplexQuickRoleBinding(ctx context.Context, id string, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "end ClearDev complex quick role", func(q *gen.Queries) error {
		rows, e := q.EndClearDevComplexQuickRoleBindingCAS(ctx, gen.EndClearDevComplexQuickRoleBindingCASParams{ID: id, ReasonCode: string(reason), EndedAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// CreateClearDevComplexQuickAgentStep creates an idempotent quick-execution agent step.
func (s *Store) CreateClearDevComplexQuickAgentStep(ctx context.Context, step cleardev.AgentStep) (cleardev.AgentStep, bool, error) { //nolint:dupl // QUICK steps live in a separate table from STANDARD execution steps
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.AgentStep
	var created bool
	err := s.inTx(ctx, "create ClearDev complex quick agent step", func(q *gen.Queries) error {
		if old, e := q.GetClearDevComplexQuickAgentStep(ctx, step.ID); e == nil {
			out = complexQuickAgentStepFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if step.SendStatus != cleardev.AgentStepSendStatusPending {
			return complexExecutionRule("agent step must start pending")
		}
		if e := q.InsertClearDevComplexQuickAgentStep(ctx, quickAgentStepParams(step)); e != nil {
			return e
		}
		out, created = step, true
		return nil
	})
	return out, created, err
}

// MarkClearDevComplexQuickAgentStepSent records that the Chat turn left the control plane.
func (s *Store) MarkClearDevComplexQuickAgentStepSent(ctx context.Context, id string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "mark ClearDev complex quick agent step sent", func(q *gen.Queries) error {
		rows, e := q.MarkClearDevComplexQuickAgentStepSentCAS(ctx, gen.MarkClearDevComplexQuickAgentStepSentCASParams{ID: id, SentAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// SettleClearDevComplexQuickAgentStep stores the exact final message text and hash.
func (s *Store) SettleClearDevComplexQuickAgentStep(ctx context.Context, step cleardev.AgentStep) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "settle ClearDev complex quick agent step", func(q *gen.Queries) error {
		rows, e := q.SettleClearDevComplexQuickAgentStepCAS(ctx, gen.SettleClearDevComplexQuickAgentStepCASParams{
			ID: step.ID, TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID),
			FinalMessageText: nullableString(step.FinalMessageText), MessageSha256: nullableString(step.MessageSHA256), CompletedAt: nullableTimePtr(step.CompletedAt),
		})
		changed = rows == 1
		return e
	})
	return changed, err
}

// FailClearDevComplexQuickAgentStep records an unusable Agent step.
func (s *Store) FailClearDevComplexQuickAgentStep(ctx context.Context, id string, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "fail ClearDev complex quick agent step", func(q *gen.Queries) error {
		rows, e := q.FailClearDevComplexQuickAgentStepCAS(ctx, gen.FailClearDevComplexQuickAgentStepCASParams{ID: id, ReasonCode: string(reason), FailedAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// SettleClearDevComplexQuickRequest rejects or marks NEEDS_HUMAN without creating a task.
func (s *Store) SettleClearDevComplexQuickRequest(ctx context.Context, runID string, reason cleardev.ReasonCode, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "settle ClearDev complex quick request", func(q *gen.Queries) error {
		run, e := q.GetClearDevComplexQuickRun(ctx, runID)
		if e != nil {
			return e
		}
		status := "REJECTED"
		if reason == cleardev.ReasonQuickNeedsHuman || reason == cleardev.ReasonHumanDecisionRequired {
			status = "NEEDS_HUMAN"
		}
		if run.Status == status && run.ReasonCode == string(reason) {
			return nil
		}
		if run.Status != "PENDING" {
			return complexExecutionRule("invalid quick execution request settlement")
		}
		rows, e := q.SettleClearDevComplexQuickRunCAS(ctx, gen.SettleClearDevComplexQuickRunCASParams{ID: runID, Status: status, ReasonCode: string(reason), SelectionReasonCode: string(reason), SettledAt: nullableTime(at)})
		if e != nil {
			return e
		}
		if rows != 1 {
			return complexExecutionRule("quick execution request settlement lost its compare-and-swap")
		}
		return nil
	})
}

// MaterializeClearDevComplexQuickExecution creates one task and one Builder
// and advances the completed source task set by one.
func (s *Store) MaterializeClearDevComplexQuickExecution(ctx context.Context, c cleardev.MaterializeComplexQuickExecutionCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "materialize ClearDev complex quick execution", func(q *gen.Queries) error {
		run, e := q.GetClearDevComplexQuickRun(ctx, c.RunID)
		if e != nil {
			return e
		}
		pkg, e := cleardev.ParseComplexQuickExecutionPackage([]byte(c.Task.ExecutionPackageJSON))
		if e != nil {
			return e
		}
		if err := validateComplexQuickMaterialization(ctx, q, run, c, pkg); err != nil {
			return err
		}
		if run.Status == "PENDING" {
			if _, e = q.AcceptClearDevComplexQuickRunCAS(ctx, gen.AcceptClearDevComplexQuickRunCASParams{ID: run.ID, AcceptedAt: nullableTime(c.At), SelectionReasonCode: string(cleardev.ReasonQuickFollowUpApproved), SourceTaskKey: c.SourceTaskKey}); e != nil {
				return e
			}
		}
		run, e = q.GetClearDevComplexQuickRun(ctx, c.RunID)
		if e != nil {
			return e
		}
		if run.Status != "ACCEPTED" || run.SourceTaskKey != c.SourceTaskKey || c.Task.TaskKey != run.SourceTaskKey {
			return complexExecutionRule("quick execution is not accepted")
		}
		version, e := q.GetClearDevRequirementVersion(ctx, run.RequirementVersionID)
		if e != nil {
			return e
		}
		if version.Sha256 != run.RequirementSha256 || version.TaskSetVersion != run.ExpectedTaskSetVersion {
			return complexExecutionRule("quick execution no longer owns the current task set")
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, run.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			return directionStoppedError()
		}
		task := c.Task
		paths, e := json.Marshal(pkg.WritePaths)
		if e != nil {
			return e
		}
		generated, _ := json.Marshal(pkg.GeneratedPaths)
		shared, _ := json.Marshal(pkg.SharedPathsRequireApproval)
		forbidden, _ := json.Marshal(pkg.ForbiddenPaths)
		if e := q.InsertClearDevComplexQuickTaskMapping(ctx, gen.InsertClearDevComplexQuickTaskMappingParams{
			ID: task.ID, QuickRunID: run.ID, PlanTaskKey: task.TaskKey, WorkItemID: task.DevelopmentTaskID, Ordinal: 0,
			TaskPacketJson: task.ExecutionPackageJSON, TaskPacketSha256: task.ExecutionPackageSHA256, CreatedAt: c.At,
		}); e != nil {
			return e
		}
		if e := q.InsertClearDevComplexQuickWorkItem(ctx, gen.InsertClearDevComplexQuickWorkItemParams{
			ID: task.DevelopmentTaskID, DevelopmentProjectID: run.DevelopmentProjectID, ContractVersionID: run.RequirementVersionID,
			Title: pkg.Title, Mode: string(cleardev.WorkModeQuick), State: string(cleardev.DevelopmentTaskStatusPlanned),
			MaxReworkCount: int64(pkg.MaxReworkCount), ReworkCount: 0, ComplexQuickTaskID: nullableString(task.ID), CreatedAt: c.At, UpdatedAt: c.At,
		}); e != nil {
			return e
		}
		if e := q.InsertClearDevComplexQuickPermission(ctx, gen.InsertClearDevComplexQuickPermissionParams{
			ID: task.ID + ":permission", WorkItemID: task.DevelopmentTaskID, Version: 1, WritePaths: string(paths),
			ForbiddenPaths: string(forbidden), SharedPathsRequireApproval: string(shared), GeneratedPaths: string(generated),
			ComplexQuickTaskID: nullableString(task.ID), CreatedAt: c.At,
		}); e != nil {
			return e
		}
		for _, spec := range c.CheckSpecs {
			taskID := nullableString(spec.ComplexExecutionTaskID)
			checkName := spec.CheckID
			argv, marshalErr := json.Marshal(spec.Argv)
			if marshalErr != nil {
				return marshalErr
			}
			required := sql.NullString{}
			switch spec.Kind {
			case cleardev.CandidateCheckRequired:
				required = nullableString(spec.ID + ":required")
			case cleardev.CandidateCheckScope:
				checkName = ""
				argv = []byte("[]")
			}
			if e := q.InsertClearDevComplexQuickCheckSpec(ctx, gen.InsertClearDevComplexQuickCheckSpecParams{
				ID: spec.ID, QuickRunID: run.ID, TaskMappingID: taskID, RequiredCheckID: required, CheckKind: string(spec.Kind),
				CheckName: checkName, CheckSpecSha256: spec.CheckSpecSHA256, ArgvJson: string(argv), TimeoutSeconds: int64(spec.TimeoutSeconds), CreatedAt: c.At,
			}); e != nil {
				return e
			}
			if spec.Kind == cleardev.CandidateCheckRequired {
				if e := q.InsertClearDevComplexQuickRequiredCheck(ctx, gen.InsertClearDevComplexQuickRequiredCheckParams{
					ID: required.String, WorkItemID: task.DevelopmentTaskID, Name: spec.CheckID, CheckKind: string(spec.Kind),
					ComplexQuickCheckSpecID: nullableString(spec.ID), CreatedAt: c.At,
				}); e != nil {
					return e
				}
			}
		}
		if e := q.InsertClearDevComplexQuickRoleBinding(ctx, quickRoleBindingParams(c.BuilderBinding)); e != nil {
			return e
		}
		rows, e := q.IncrementClearDevTaskSetVersionCAS(ctx, gen.IncrementClearDevTaskSetVersionCASParams{ID: run.RequirementVersionID, ExpectedTaskSetVersion: run.ExpectedTaskSetVersion})
		if e != nil {
			return e
		}
		if rows != 1 {
			return complexExecutionRule("current task set changed during quick materialization")
		}
		requirement, e := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
		if e != nil {
			return e
		}
		return insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectComplexQuickRun, run.ID, cleardev.ActionAcceptComplexQuick, cleardev.EventAccepted, cleardev.ReasonNone, "", c.At)
	})
}

// CreateClearDevComplexQuickDispatch inserts a Builder attempt and starts it in one transaction.
func (s *Store) CreateClearDevComplexQuickDispatch(ctx context.Context, c cleardev.CreateComplexExecutionDispatchCommand) (cleardev.ComplexExecutionDispatch, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.ComplexExecutionDispatch
	var created bool
	err := s.inTx(ctx, "create ClearDev complex quick dispatch", func(q *gen.Queries) error {
		d := c.Dispatch
		if old, e := q.GetClearDevComplexQuickTaskAttempt(ctx, d.ID); e == nil {
			out = complexQuickDispatchFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if d.Status != cleardev.ComplexExecutionDispatchPending || !complexExecutionSHA1(d.BaseCommitSHA) || c.AgentStep.Kind != cleardev.ComplexExecutionAgentStepBuilderTask {
			return complexExecutionRule("invalid quick dispatch")
		}
		if e := q.InsertClearDevComplexQuickAgentStep(ctx, quickAgentStepParams(c.AgentStep)); e != nil {
			return e
		}
		if e := q.InsertClearDevComplexQuickTaskAttempt(ctx, gen.InsertClearDevComplexQuickTaskAttemptParams{
			ID: d.ID, QuickRunID: d.ExecutionRunID, TaskMappingID: d.ComplexExecutionTaskID, BuilderRoleBindingID: c.AgentStep.RoleBindingID,
			AgentStepID: d.AgentStepID, Round: int64(d.Round), BaseCommitSha: d.BaseCommitSHA, Status: string(d.Status),
		}); e != nil {
			return e
		}
		if _, e := q.StartClearDevComplexQuickTaskAttemptCAS(ctx, gen.StartClearDevComplexQuickTaskAttemptCASParams{ID: d.ID, DispatchedAt: nullableTime(d.CreatedAt)}); e != nil {
			return e
		}
		item, e := q.GetClearDevComplexQuickTaskByWorkItem(ctx, d.ComplexExecutionTaskID)
		if e != nil {
			return e
		}
		if _, e = q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
			ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount,
			NextState: string(cleardev.DevelopmentTaskStatusRunning), ReworkCount: item.ReworkCount, UpdatedAt: d.CreatedAt,
		}); e != nil {
			return e
		}
		d.Status = cleardev.ComplexExecutionDispatchRunning
		run, e := q.GetClearDevComplexQuickRun(ctx, d.ExecutionRunID)
		if e != nil {
			return e
		}
		requirement, e := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
		if e != nil {
			return e
		}
		if e := insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectComplexQuickDispatch, d.ID, cleardev.ActionDispatchComplexTask, cleardev.EventAccepted, cleardev.ReasonNone, "", d.CreatedAt); e != nil {
			return e
		}
		out, created = d, true
		return nil
	})
	return out, created, err
}

// AppendClearDevComplexQuickCandidate records the observed Git candidate on the current attempt.
func (s *Store) AppendClearDevComplexQuickCandidate(ctx context.Context, c cleardev.AppendComplexExecutionCandidateCommand) (cleardev.CandidateCommit, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.CandidateCommit
	err := s.inTx(ctx, "append ClearDev complex quick candidate", func(q *gen.Queries) error {
		a, e := q.GetClearDevComplexQuickTaskAttempt(ctx, c.DispatchID)
		if e != nil {
			return e
		}
		if a.QuickRunID != c.ExecutionRunID || a.TaskMappingID != c.ComplexExecutionTaskID || a.Round != int64(c.Round) || a.BaseCommitSha != c.BaseCommitSHA {
			return complexExecutionRule("candidate does not match dispatch")
		}
		task, e := q.GetClearDevComplexQuickTaskMapping(ctx, a.TaskMappingID)
		if e != nil {
			return e
		}
		if old, e := q.GetClearDevComplexQuickCandidate(ctx, nullableString(a.ID)); e == nil {
			out = clearDevCandidateFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		sequence, e := q.NextClearDevCandidateSequence(ctx, task.WorkItemID)
		if e != nil {
			return e
		}
		if e := q.InsertClearDevComplexQuickCandidate(ctx, gen.InsertClearDevComplexQuickCandidateParams{
			ID: c.Candidate.ID, WorkItemID: task.WorkItemID, Sequence: sequence, AoSessionID: c.Candidate.AOSessionID,
			PermissionVersionID: task.ID + ":permission", ComplexQuickTaskAttemptID: nullableString(a.ID),
			ComplexQuickBaseCommitSha: nullableString(a.BaseCommitSha), CommitSha: c.Candidate.CommitSHA, CreatedAt: c.Candidate.ObservedAt,
		}); e != nil {
			return e
		}
		if _, e = q.AdvanceClearDevComplexQuickTaskAttemptCAS(ctx, gen.AdvanceClearDevComplexQuickTaskAttemptCASParams{ID: a.ID, ExpectedStatus: "RUNNING", Status: "OBSERVED", ReasonCode: ""}); e != nil {
			return e
		}
		out = cleardev.CandidateCommit{ID: c.Candidate.ID, DevelopmentTaskID: task.WorkItemID, Sequence: sequence, AOSessionID: c.Candidate.AOSessionID, PermissionVersionID: task.ID + ":permission", CommitSHA: c.Candidate.CommitSHA, CreatedAt: c.Candidate.ObservedAt}
		return nil
	})
	return out, err
}

// CreateClearDevComplexQuickCheckRun inserts a pending scope, required, or integration check.
func (s *Store) CreateClearDevComplexQuickCheckRun(ctx context.Context, r cleardev.ComplexExecutionCheckRun) (cleardev.ComplexExecutionCheckRun, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.ComplexExecutionCheckRun
	var created bool
	err := s.inTx(ctx, "create ClearDev complex quick check run", func(q *gen.Queries) error {
		if old, e := q.GetClearDevComplexQuickCheckRun(ctx, r.ID); e == nil {
			out = complexQuickCheckRunFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e := q.InsertClearDevComplexQuickCheckRun(ctx, gen.InsertClearDevComplexQuickCheckRunParams{
			ID: r.ID, CheckSpecID: r.CheckSpecFactID, TaskAttemptID: r.DispatchID, CandidateCommitID: r.CandidateCommitID,
			CandidateCommitSha: r.CandidateCommitSHA, Status: string(cleardev.ComplexExecutionCheckRunPending), CreatedAt: r.CreatedAt,
		}); e != nil {
			return e
		}
		out, created = r, true
		return nil
	})
	return out, created, err
}

// StartClearDevComplexQuickCheckRun marks a pending check as STARTED.
func (s *Store) StartClearDevComplexQuickCheckRun(ctx context.Context, id string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "start ClearDev complex quick check run", func(q *gen.Queries) error {
		rows, e := q.MarkClearDevComplexQuickCheckRunStartedCAS(ctx, gen.MarkClearDevComplexQuickCheckRunStartedCASParams{ID: id, StartedAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// SettleClearDevComplexQuickCheckRun records the exact checker result for one candidate.
func (s *Store) SettleClearDevComplexQuickCheckRun(ctx context.Context, c cleardev.SettleComplexExecutionCheckCommand) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "settle ClearDev complex quick check run", func(q *gen.Queries) error {
		status := "SETTLED"
		result := nullableString(string(c.Result))
		if c.Result != "PASS" && c.Result != "FAIL" {
			status = "FAILED"
			result = sql.NullString{}
		}
		rows, e := q.SettleClearDevComplexQuickCheckRunCAS(ctx, gen.SettleClearDevComplexQuickCheckRunCASParams{
			ID: c.CheckRunID, ContainerImageID: nullableString(c.ContainerImageID), ExitCode: nullableComplexExecutionInt(c.ExitCode),
			Status: status, TimedOut: sql.NullBool{Bool: c.TimedOut, Valid: status == "SETTLED"}, OutputSummary: nullableString(c.OutputSummary),
			OutputSha256: nullableString(c.OutputSHA256), ChangedPathsJson: nullableString(c.ChangedPathsJSON), Result: result,
			SettledAt: nullableTime(c.At), ReasonCode: string(c.ReasonCode),
		})
		changed = rows == 1
		return e
	})
	return changed, err
}

// ApplyClearDevComplexQuickFailure applies one rework, block, or needs-human outcome.
func (s *Store) ApplyClearDevComplexQuickFailure(ctx context.Context, runID, taskID, dispatchID string, infrastructure bool, reason cleardev.ReasonCode, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "apply ClearDev complex quick failure", func(q *gen.Queries) error {
		a, e := q.GetClearDevComplexQuickTaskAttempt(ctx, dispatchID)
		if e != nil {
			return e
		}
		if a.QuickRunID != runID || a.TaskMappingID != taskID {
			return complexExecutionRule("failure does not match dispatch")
		}
		next := "REWORK"
		switch reason {
		case cleardev.ReasonHumanDecisionRequired, "BUILDER_NEEDS_HUMAN":
			next = "NEEDS_HUMAN"
		case "BUILDER_BLOCKED":
			next = "BLOCKED"
		}
		if infrastructure {
			next = "BLOCKED"
		} else if next == "REWORK" && a.Round >= 1 {
			next = "NEEDS_HUMAN"
		} else if next == "REWORK" && a.Status == string(cleardev.ComplexExecutionDispatchRunning) {
			next = "NEEDS_HUMAN"
		}
		if _, e = q.AdvanceClearDevComplexQuickTaskAttemptCAS(ctx, gen.AdvanceClearDevComplexQuickTaskAttemptCASParams{ID: a.ID, ExpectedStatus: a.Status, Status: next, ReasonCode: string(reason), SettledAt: nullableTime(at)}); e != nil {
			return e
		}
		item, e := q.GetClearDevComplexQuickTaskByWorkItem(ctx, taskID)
		if e != nil {
			return e
		}
		state := cleardev.DevelopmentTaskStatusRework
		switch next {
		case "BLOCKED":
			state = cleardev.DevelopmentTaskStatusBlocked
		case "NEEDS_HUMAN":
			state = cleardev.DevelopmentTaskStatusNeedsHuman
		}
		reworkCount := item.ReworkCount
		if state == cleardev.DevelopmentTaskStatusRework {
			reworkCount++
		}
		pausedFrom := sql.NullString{}
		if state == cleardev.DevelopmentTaskStatusBlocked || state == cleardev.DevelopmentTaskStatusNeedsHuman {
			pausedFrom = item.PausedFromState
			if !pausedFrom.Valid {
				pausedFrom = nullableString(item.State)
			}
		}
		rows, e := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
			ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount, NextState: string(state),
			PausedFromState: pausedFrom, ReworkCount: reworkCount, UpdatedAt: at,
		})
		if e != nil {
			return e
		}
		if rows != 1 {
			return complexExecutionRule("failure task state changed concurrently")
		}
		return nil
	})
}

// CompleteClearDevComplexQuickExecution writes the accepted successor
// integration and completes atomically.
func (s *Store) CompleteClearDevComplexQuickExecution(ctx context.Context, c cleardev.CompleteComplexQuickExecutionCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "complete ClearDev complex quick execution", func(q *gen.Queries) error {
		run, e := q.GetClearDevComplexQuickRun(ctx, c.RunID)
		if e != nil {
			return e
		}
		if run.Status == "COMPLETED" {
			return nil
		}
		if run.Status != "ACCEPTED" {
			return complexExecutionRule("quick execution is not accepted")
		}
		version, e := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, run.DevelopmentProjectID)
		if e != nil {
			return e
		}
		if version.ID != run.RequirementVersionID || version.State != "APPROVED" || version.SupersededByID.Valid || version.Sha256 != run.RequirementSha256 || version.TaskSetVersion != run.AcceptedTaskSetVersion {
			return complexExecutionRule("quick execution no longer owns the current task set")
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, run.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			return directionStoppedError()
		}
		attempts, e := q.ListClearDevComplexQuickTaskAttempts(ctx, run.ID)
		if e != nil || len(attempts) == 0 {
			return complexExecutionRule("quick execution has no observed candidate")
		}
		last := attempts[len(attempts)-1]
		candidate, e := q.GetClearDevComplexQuickCandidate(ctx, nullableString(last.ID))
		if e != nil {
			return e
		}
		if c.Integration.CandidateCommitSHA != candidate.CommitSha {
			return complexExecutionRule("integration must use the observed candidate")
		}
		builder, e := q.ListClearDevComplexQuickRoleBindings(ctx, run.ID)
		if e != nil {
			return e
		}
		builderSession := ""
		for _, binding := range builder {
			if binding.Role == "BUILDER" && binding.Status == "BOUND" {
				builderSession = binding.AoSessionID.String
			}
		}
		if builderSession == "" {
			return complexExecutionRule("quick execution has no bound Builder")
		}
		if e := q.InsertClearDevComplexQuickResult(ctx, gen.InsertClearDevComplexQuickResultParams{
			ID: c.Integration.ID, QuickRunID: run.ID, IntegrationCandidateID: c.Integration.IntegrationCandidateID, CompletionStatus: "PENDING", CreatedAt: c.At,
		}); e != nil {
			return e
		}
		sequence, e := q.NextClearDevIntegrationCandidateSequence(ctx, run.DevelopmentProjectID)
		if e != nil {
			return e
		}
		if e := q.InsertClearDevComplexQuickIntegrationCandidate(ctx, gen.InsertClearDevComplexQuickIntegrationCandidateParams{
			ID: c.Integration.IntegrationCandidateID, DevelopmentProjectID: run.DevelopmentProjectID, Sequence: sequence,
			AoSessionID: builderSession, CommitSha: c.Integration.CandidateCommitSHA, RequirementVersionID: nullableString(run.RequirementVersionID),
			TaskSetVersion: sql.NullInt64{Int64: run.AcceptedTaskSetVersion, Valid: true}, ComplexQuickResultID: nullableString(c.Integration.ID),
			ComplexQuickSourceCandidateCommitID: nullableString(candidate.ID), CreatedAt: c.At,
		}); e != nil {
			return e
		}
		if len(c.Integration.CheckRunIDs) == 0 {
			return complexExecutionRule("integration requires a passing fixed check")
		}
		for _, id := range c.Integration.CheckRunIDs {
			if e := q.InsertClearDevComplexQuickResultCheck(ctx, gen.InsertClearDevComplexQuickResultCheckParams{ResultID: c.Integration.ID, CheckRunID: id}); e != nil {
				return e
			}
		}
		evidenceID := c.Integration.ID + ":integration-evidence"
		if e := q.InsertClearDevComplexQuickEvidence(ctx, gen.InsertClearDevComplexQuickEvidenceParams{
			ID: evidenceID, DevelopmentProjectID: run.DevelopmentProjectID, SubjectType: physicalEvidenceSubject(cleardev.SubjectDevelopmentRequirement),
			SubjectID: run.DevelopmentProjectID, EvidenceKind: string(cleardev.EvidenceKindIntegration), EvidenceKey: "", Result: string(cleardev.EvidenceResultPass),
			IntegrationCandidateID: nullableString(c.Integration.IntegrationCandidateID), CommitSha: c.Integration.CandidateCommitSHA,
			SourceType: string(cleardev.EvidenceSourceControlPlaneChecker), ComplexQuickCheckRunID: nullableString(c.Integration.CheckRunIDs[0]), CreatedAt: c.At,
		}); e != nil {
			return e
		}
		rows, e := q.StartClearDevComplexQuickCompletionCAS(ctx, gen.StartClearDevComplexQuickCompletionCASParams{ID: c.Integration.ID, CommittingAt: nullableTime(c.At)})
		if e != nil {
			return e
		}
		if rows != 1 {
			return complexExecutionRule("atomic completion lost its compare-and-swap")
		}
		requirement, e := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
		if e != nil {
			return e
		}
		if e := insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectEvidence, evidenceID, cleardev.ActionRecordEvidence, cleardev.EventAccepted, cleardev.ReasonNone, "", c.At); e != nil {
			return e
		}
		return insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectComplexQuickRun, c.Integration.ID, cleardev.ActionCompleteComplexQuick, cleardev.EventAccepted, cleardev.ReasonNone, builderSession, c.At)
	})
}

func validateComplexQuickMaterialization(ctx context.Context, q *gen.Queries, run gen.CleardevComplexQuickRun, c cleardev.MaterializeComplexQuickExecutionCommand, pkg cleardev.ComplexStandardExecutionPackage) error {
	if c.BuilderBinding.Role != cleardev.StandardRoleBuilder || c.BuilderBinding.Status != cleardev.RoleBindingStatusRequested ||
		c.BuilderBinding.ExecutionRunID != run.ID || strings.Contains(c.BuilderBinding.SessionCreationIdempotencyKey, "review") ||
		c.Task.ExecutionRunID != run.ID || c.Task.Ordinal != 0 || c.Task.Status != cleardev.DevelopmentTaskStatusPlanned ||
		c.Task.TaskKey == "" || c.SourceTaskKey != c.Task.TaskKey || pkg.TaskKey != c.Task.TaskKey ||
		pkg.TaskID != c.Task.DevelopmentTaskID || pkg.ExecutionRunID != run.ID ||
		pkg.RequirementVersionID != run.RequirementVersionID || pkg.RequirementSHA256 != run.RequirementSha256 ||
		pkg.PlanID != run.PlanID || pkg.PlanSHA256 != run.PlanSha256 ||
		pkg.Mode != string(cleardev.WorkModeQuick) || pkg.TaskSetVersion != run.AcceptedTaskSetVersion ||
		complexExecutionRawDigest([]byte(c.Task.ExecutionPackageJSON)) != c.Task.ExecutionPackageSHA256 ||
		!quickStringListsEqual(pkg.WritePaths, c.WritePaths) || len(c.GeneratedPaths) != 0 || len(c.SharedPaths) != 0 {
		return complexExecutionRule("invalid quick materialization")
	}
	if run.Status == "ACCEPTED" && run.SourceTaskKey != c.SourceTaskKey {
		return complexExecutionRule("invalid quick materialization")
	}
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.SourceExecutionRunID)
	if err != nil {
		return err
	}
	sourceTaskID := ""
	for _, mapping := range mappings {
		if mapping.PlanTaskKey != c.SourceTaskKey {
			continue
		}
		item, itemErr := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mapping.ID)
		if itemErr != nil {
			return itemErr
		}
		if item.State == string(cleardev.DevelopmentTaskStatusDone) {
			if sourceTaskID != "" {
				return complexExecutionRule("quick execution source task is ambiguous")
			}
			sourceTaskID = mapping.ID
		}
	}
	if sourceTaskID == "" {
		return complexExecutionRule("invalid quick materialization")
	}
	return validateComplexQuickInheritedCheckSpecs(ctx, q, run, c, pkg, sourceTaskID)
}

func validateComplexQuickInheritedCheckSpecs(ctx context.Context, q *gen.Queries, run gen.CleardevComplexQuickRun, c cleardev.MaterializeComplexQuickExecutionCommand, pkg cleardev.ComplexStandardExecutionPackage, sourceTaskID string) error {
	nextTaskSet, err := cleardev.NextComplexQuickTaskSetVersion(run.ExpectedTaskSetVersion)
	if err != nil || nextTaskSet != run.AcceptedTaskSetVersion {
		return complexExecutionRule("quick execution task-set binding is invalid")
	}
	sourceSpecs, err := q.ListClearDevComplexExecutionCheckSpecs(ctx, run.SourceExecutionRunID)
	if err != nil {
		return err
	}
	type checkKey struct {
		kind string
		name string
	}
	expected := make(map[checkKey]gen.CleardevComplexExecutionCheckSpec)
	for _, spec := range sourceSpecs {
		include := (spec.CheckKind == string(cleardev.CandidateCheckRequired) && spec.TaskMappingID.Valid && spec.TaskMappingID.String == sourceTaskID) ||
			(spec.CheckKind == string(cleardev.CandidateCheckIntegration) && !spec.TaskMappingID.Valid)
		if !include {
			continue
		}
		key := checkKey{kind: spec.CheckKind, name: spec.CheckName}
		if _, duplicate := expected[key]; duplicate {
			return complexExecutionRule("quick execution source check set is ambiguous")
		}
		expected[key] = spec
	}
	if len(expected) == 0 {
		return complexExecutionRule("quick execution source check set is empty")
	}
	packageChecks := make(map[string]struct{}, len(pkg.RequiredChecks))
	requiredSourceCount := 0
	for key, source := range expected {
		if key.kind != string(cleardev.CandidateCheckRequired) {
			continue
		}
		requiredSourceCount++
		matched := false
		for _, check := range pkg.RequiredChecks {
			if check.ID != key.name {
				continue
			}
			if _, duplicate := packageChecks[check.ID]; duplicate {
				return complexExecutionRule("quick execution task packet duplicates an inherited check")
			}
			argv, decodeErr := decodeJSONStringSlice(source.ArgvJson, "source quick package check argv")
			if decodeErr != nil {
				return decodeErr
			}
			if check.TimeoutSeconds != int(source.TimeoutSeconds) || !quickStringListsEqual(check.Argv, argv) {
				return complexExecutionRule("quick execution task packet changed an inherited check")
			}
			packageChecks[check.ID] = struct{}{}
			matched = true
			break
		}
		if !matched {
			return complexExecutionRule("quick execution task packet omits an inherited check")
		}
	}
	if len(pkg.RequiredChecks) != requiredSourceCount || len(packageChecks) != requiredSourceCount {
		return complexExecutionRule("quick execution task packet check set does not match its source")
	}
	seen := make(map[checkKey]struct{}, len(expected))
	scopeCount := 0
	for _, actual := range c.CheckSpecs {
		if actual.ExecutionRunID != run.ID {
			return complexExecutionRule("quick execution check spec has a mismatched run")
		}
		if actual.Kind == cleardev.CandidateCheckScope {
			scopeCount++
			if actual.ComplexExecutionTaskID != c.Task.ID || actual.CheckID != "SCOPE" || len(actual.Argv) != 0 || actual.TimeoutSeconds != 0 ||
				actual.CheckSpecSHA256 != complexExecutionScopeDigest(cleardev.ComplexPlanTask{WritePaths: pkg.WritePaths, GeneratedPaths: pkg.GeneratedPaths, SharedPathsRequireApproval: pkg.SharedPathsRequireApproval, ForbiddenPaths: pkg.ForbiddenPaths}) {
				return complexExecutionRule("quick execution scope check does not match its task packet")
			}
			continue
		}
		key := checkKey{kind: string(actual.Kind), name: actual.CheckID}
		source, ok := expected[key]
		if !ok {
			return complexExecutionRule("quick execution check is not inherited from its completed source")
		}
		if _, duplicate := seen[key]; duplicate {
			return complexExecutionRule("quick execution inherited check is duplicated")
		}
		seen[key] = struct{}{}
		if (actual.Kind == cleardev.CandidateCheckRequired && actual.ComplexExecutionTaskID != c.Task.ID) ||
			(actual.Kind == cleardev.CandidateCheckIntegration && actual.ComplexExecutionTaskID != "") {
			return complexExecutionRule("quick execution inherited check has a mismatched task")
		}
		argv, decodeErr := decodeJSONStringSlice(source.ArgvJson, "source quick check argv")
		if decodeErr != nil {
			return decodeErr
		}
		if actual.CheckSpecSHA256 != source.CheckSpecSha256 || actual.TimeoutSeconds != int(source.TimeoutSeconds) || !quickStringListsEqual(actual.Argv, argv) {
			return complexExecutionRule("quick execution inherited check facts were changed")
		}
	}
	if scopeCount != 1 || len(seen) != len(expected) {
		return complexExecutionRule("quick execution check set does not exactly match its completed source")
	}
	return nil
}

func quickStringListsEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func quickRoleBindingParams(b cleardev.ComplexExecutionRoleBinding) gen.InsertClearDevComplexQuickRoleBindingParams {
	return gen.InsertClearDevComplexQuickRoleBindingParams{
		ID: b.ID, QuickRunID: b.ExecutionRunID, Role: string(b.Role), SourceComplexRoleBindingID: nullableString(b.SourceComplexRoleBindingID),
		ContinuationOfRoleBindingID: nullableString(b.ContinuationOfRoleBindingID), SessionCreationIdempotencyKey: b.SessionCreationIdempotencyKey,
		AoSessionID: nullableString(b.AOSessionID), WorkspacePath: b.WorkspacePath, BaseCommitSha: b.BaseCommitSHA, Status: string(b.Status),
		ReasonCode: string(b.ReasonCode), RequestedAt: b.RequestedAt, BoundAt: nullableTimePtr(b.BoundAt), EndedAt: nullableTimePtr(b.EndedAt),
	}
}

func quickAgentStepParams(step cleardev.AgentStep) gen.InsertClearDevComplexQuickAgentStepParams {
	return gen.InsertClearDevComplexQuickAgentStepParams{
		ID: step.ID, RoleBindingID: step.RoleBindingID, StepKind: string(step.Kind), RequestID: step.RequestID, ClientMessageID: step.ClientMessageID,
		PromptSha256: step.PromptSHA256, SendStatus: string(step.SendStatus), TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID),
		FinalMessageText: nullableString(step.FinalMessageText), MessageSha256: nullableString(step.MessageSHA256), RequestedAt: step.RequestedAt,
		SentAt: nullableTimePtr(step.SentAt), CompletedAt: nullableTimePtr(step.CompletedAt), FailedAt: nullableTimePtr(step.FailedAt), ReasonCode: string(step.ReasonCode),
	}
}
