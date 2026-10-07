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

func loadComplexExceptionFacts(ctx context.Context, q *gen.Queries, runID string) (*cleardev.ComplexExceptionFacts, error) {
	facts := &cleardev.ComplexExceptionFacts{
		Budgets: []cleardev.ComplexExceptionBudget{}, ScopeRequests: []cleardev.ComplexScopeExpansionRequest{},
		ScopeDecisions: []cleardev.ComplexScopeExpansionDecision{}, PermissionVersions: []cleardev.PermissionVersion{},
		GeneratedCommands: []cleardev.ComplexGeneratedCommandFact{}, GeneratedProofs: []cleardev.ComplexGeneratedProof{},
		OnDemandBindings: []cleardev.ComplexOnDemandBinding{}, OnDemandSteps: []cleardev.AgentStep{},
		SpecialistResults: []cleardev.ComplexSpecialistResult{}, SpecialistChecks: []cleardev.ComplexSpecialistCheck{},
		RecoveryActions: []cleardev.ComplexRecoveryAction{}, PathLeases: []cleardev.ComplexExceptionPathLease{},
	}
	budgets, err := q.ListClearDevComplexExceptionBudgets(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range budgets {
		types, err := decodeJSONStringSlice(row.AllowedAgentTypesJson, "exception budget agent types")
		if err != nil {
			return nil, err
		}
		facts.Budgets = append(facts.Budgets, cleardev.ComplexExceptionBudget{
			ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.ComplexExecutionTaskID.String,
			RoleKind: row.RoleKind, AllowedAgentTypes: types, ModelSelection: row.ModelSelection,
			MaxTurns: int(row.MaxTurns), UsedTurns: int(row.UsedTurns), MaxReworkCount: int(row.MaxReworkCount), AuthorizedExtraTurns: int(row.AuthorizedExtraTurns), CreatedAt: row.CreatedAt,
		})
	}
	requests, err := q.ListClearDevComplexExceptionScopeRequests(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range requests {
		paths, err := decodeJSONStringSlice(row.RequestedPathsJson, "exception scope request paths")
		if err != nil {
			return nil, err
		}
		item := cleardev.ComplexScopeExpansionRequest{
			ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.ComplexExecutionTaskID,
			DispatchID: row.DispatchID, Round: int(row.Round), RequirementVersionID: row.RequirementVersionID,
			RequirementSHA256: row.RequirementSha256, PlanID: row.PlanID, PlanSHA256: row.PlanSha256,
			RequestedPaths: paths, AgentStepID: row.AgentStepID, Status: row.Status, ReasonCode: cleardev.ReasonCode(row.ReasonCode), CreatedAt: row.CreatedAt,
		}
		if row.SettledAt.Valid {
			item.SettledAt = &row.SettledAt.Time
		}
		facts.ScopeRequests = append(facts.ScopeRequests, item)
	}
	decisions, err := q.ListClearDevComplexExceptionScopeDecisions(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range decisions {
		paths, err := decodeJSONStringSlice(row.PathsJson, "exception scope decision paths")
		if err != nil {
			return nil, err
		}
		facts.ScopeDecisions = append(facts.ScopeDecisions, cleardev.ComplexScopeExpansionDecision{
			ID: row.ID, RequestID: row.RequestID, ExecutionRunID: row.ExecutionRunID, RequirementVersionID: row.RequirementVersionID,
			RequirementSHA256: row.RequirementSha256, PlanID: row.PlanID, PlanSHA256: row.PlanSha256, Paths: paths,
			Decision: row.Decision, ReasonCode: cleardev.ReasonCode(row.ReasonCode), Summary: row.Summary,
			StewardRoleBindingID: row.StewardRoleBindingID, AgentStepID: row.AgentStepID, PermissionVersionID: row.PermissionVersionID.String,
			ControlAccepted: row.ControlAccepted, CreatedAt: row.CreatedAt,
		})
	}
	perms, err := q.ListClearDevComplexExecutionPermissionVersions(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range perms {
		permission, err := permissionVersionFromGen(row)
		if err != nil {
			return nil, err
		}
		facts.PermissionVersions = append(facts.PermissionVersions, permission)
	}
	commands, err := q.ListClearDevComplexExceptionGeneratedCommands(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range commands {
		argv, err := decodeJSONStringSlice(row.ArgvJson, "exception generated command argv")
		if err != nil {
			return nil, err
		}
		outputs, err := decodeJSONStringSlice(row.OutputPathsJson, "exception generated command outputs")
		if err != nil {
			return nil, err
		}
		facts.GeneratedCommands = append(facts.GeneratedCommands, cleardev.ComplexGeneratedCommandFact{
			ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.ComplexExecutionTaskID,
			CommandID: row.CommandID, Argv: argv, TimeoutSeconds: int(row.TimeoutSeconds), OutputPaths: outputs,
			Image: row.Image, CommandSpecSHA256: row.CommandSpecSha256, CreatedAt: row.CreatedAt,
		})
	}
	proofs, err := q.ListClearDevComplexExceptionGeneratedProofs(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range proofs {
		item := cleardev.ComplexGeneratedProof{
			ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.ComplexExecutionTaskID,
			DispatchID: row.DispatchID, CandidateCommitID: row.CandidateCommitID, CandidateCommitSHA: row.CandidateCommitSha,
			CommandFactID: row.CommandFactID, ContainerImageID: row.ContainerImageID.String, OutputSHA256JSON: row.OutputSha256Json,
			Status: row.Status, Result: cleardev.EvidenceResult(row.Result.String), ReasonCode: cleardev.ReasonCode(row.ReasonCode), CreatedAt: row.CreatedAt,
		}
		if row.SettledAt.Valid {
			item.SettledAt = &row.SettledAt.Time
		}
		facts.GeneratedProofs = append(facts.GeneratedProofs, item)
	}
	bindings, err := q.ListClearDevComplexExceptionOnDemandBindings(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range bindings {
		item := cleardev.ComplexOnDemandBinding{
			ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.ComplexExecutionTaskID.String,
			Mode: row.Mode, TriggerReason: cleardev.ReasonCode(row.TriggerReason), SessionCreationIdempotencyKey: row.SessionCreationIdempotencyKey,
			AOSessionID: row.AoSessionID.String, WorkspacePath: row.WorkspacePath, BaseCommitSHA: row.BaseCommitSha,
			Status: cleardev.RoleBindingStatus(row.Status), ReasonCode: cleardev.ReasonCode(row.ReasonCode),
			BindingFingerprint: row.BindingFingerprint, RequestedAt: row.RequestedAt,
		}
		if row.BoundAt.Valid {
			item.BoundAt = &row.BoundAt.Time
		}
		if row.EndedAt.Valid {
			item.EndedAt = &row.EndedAt.Time
		}
		facts.OnDemandBindings = append(facts.OnDemandBindings, item)
	}
	steps, err := q.ListClearDevComplexExceptionAgentSteps(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range steps {
		facts.OnDemandSteps = append(facts.OnDemandSteps, complexExceptionAgentStepFromGen(row))
	}
	results, err := q.ListClearDevComplexExceptionSpecialistResults(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range results {
		facts.SpecialistResults = append(facts.SpecialistResults, cleardev.ComplexSpecialistResult{
			ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.ComplexExecutionTaskID,
			OnDemandBindingID: row.OndemandBindingID, AgentStepID: row.AgentStepID, BindingFingerprint: row.BindingFingerprint,
			Outcome: row.Outcome, ConstraintsJSON: row.ConstraintsJson, ReasonCode: cleardev.ReasonCode(row.ReasonCode),
			Summary: row.Summary, CreatedAt: row.CreatedAt,
		})
	}
	checks, err := q.ListClearDevComplexExceptionSpecialistChecks(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range checks {
		argv, err := decodeJSONStringSlice(row.ArgvJson, "exception specialist check argv")
		if err != nil {
			return nil, err
		}
		item := cleardev.ComplexSpecialistCheck{
			ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.ComplexExecutionTaskID,
			SpecialistResultID: row.SpecialistResultID, CheckID: row.CheckID, Argv: argv, ContainerImageID: row.ContainerImageID.String,
			Status: row.Status, Result: cleardev.EvidenceResult(row.Result.String), ReasonCode: cleardev.ReasonCode(row.ReasonCode), CreatedAt: row.CreatedAt,
		}
		if row.SettledAt.Valid {
			item.SettledAt = &row.SettledAt.Time
		}
		facts.SpecialistChecks = append(facts.SpecialistChecks, item)
	}
	actions, err := q.ListClearDevComplexExceptionRecoveryActions(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range actions {
		facts.RecoveryActions = append(facts.RecoveryActions, cleardev.ComplexRecoveryAction{
			ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.ComplexExecutionTaskID.String,
			OnDemandBindingID: row.OndemandBindingID, AgentStepID: row.AgentStepID, TriggerReason: cleardev.ReasonCode(row.TriggerReason),
			TriggerFactID: row.TriggerFactID, Action: row.Action, Outcome: row.Outcome, RetryCheckRunID: row.RetryCheckRunID.String,
			ReasonCode: cleardev.ReasonCode(row.ReasonCode), Summary: row.Summary, CreatedAt: row.CreatedAt,
		})
	}
	leases, err := q.ListClearDevComplexExceptionPathLeases(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range leases {
		item := cleardev.ComplexExceptionPathLease{
			ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.ComplexExecutionTaskID,
			Path: row.Path, Status: row.Status, CreatedAt: row.CreatedAt,
		}
		if row.ReleasedAt.Valid {
			item.ReleasedAt = &row.ReleasedAt.Time
		}
		facts.PathLeases = append(facts.PathLeases, item)
	}
	return facts, nil
}

func permissionVersionFromGen(row gen.CleardevPathPermissionVersion) (cleardev.PermissionVersion, error) {
	rules, err := pathRulesFromColumns(row.WritePaths, row.GeneratedPaths, row.SharedPathsRequireApproval, row.ForbiddenPaths)
	if err != nil {
		return cleardev.PermissionVersion{}, err
	}
	return cleardev.PermissionVersion{
		ID:                row.ID,
		DevelopmentTaskID: row.WorkItemID,
		Version:           row.Version,
		Rules:             rules,
		CreatedAt:         row.CreatedAt,
	}, nil
}

func complexExceptionAgentStepFromGen(row gen.CleardevComplexExceptionAgentStep) cleardev.AgentStep {
	step := cleardev.AgentStep{
		ID: row.ID, RoleBindingID: row.RoleBindingID.String, Kind: cleardev.AgentStepKind(row.StepKind),
		RequestID: row.RequestID, ClientMessageID: row.ClientMessageID, PromptSHA256: row.PromptSha256,
		SendStatus: cleardev.AgentStepSendStatus(row.SendStatus), TurnID: row.TurnID.String, FinalMessageID: row.FinalMessageID.String,
		FinalMessageText: row.FinalMessageText.String, MessageSHA256: row.MessageSha256.String, RequestedAt: row.RequestedAt,
		ReasonCode: cleardev.ReasonCode(row.ReasonCode),
	}
	if row.OndemandBindingID.Valid && step.RoleBindingID == "" {
		step.RoleBindingID = row.OndemandBindingID.String
	}
	if row.SentAt.Valid {
		step.SentAt = &row.SentAt.Time
	}
	if row.CompletedAt.Valid {
		step.CompletedAt = &row.CompletedAt.Time
	}
	if row.FailedAt.Valid {
		step.FailedAt = &row.FailedAt.Time
	}
	return step
}

func insertComplexExceptionMaterialization(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, tasks []cleardev.ComplexExecutionTask, plan cleardev.ComplexEngineeringPlanResult, at time.Time) error {
	_, project, err := cleardev.ProjectContractFromRun(complexExecutionRunFromGen(run))
	if err != nil {
		return err
	}
	// Generic projects may send a task back three times; the mail flow keeps
	// its historical single rework round and spends attempts through slots.
	maxReworkCount := cleardev.ComplexStandardMaxReworkCount
	if project {
		maxReworkCount = cleardev.ComplexProjectMaxReworkCount
	}
	budgets := cleardev.FreezeComplexExceptionBudgets(run.ID, tasks, plan.Tasks, maxReworkCount, at)
	for _, budget := range budgets {
		if cleardev.BoundedMailAttempts(complexExecutionRunFromGen(run)) {
			switch budget.RoleKind {
			case cleardev.ComplexExceptionBudgetBuilder:
				budget.MaxTurns = 5
			case cleardev.ComplexExceptionBudgetReviewer:
				budget.MaxTurns = 6
			}
		}
		types, err := json.Marshal(budget.AllowedAgentTypes)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevComplexExceptionBudget(ctx, gen.InsertClearDevComplexExceptionBudgetParams{
			ID: budget.ID, ExecutionRunID: budget.ExecutionRunID, ComplexExecutionTaskID: nullableString(budget.ComplexExecutionTaskID),
			RoleKind: budget.RoleKind, AllowedAgentTypesJson: string(types), ModelSelection: budget.ModelSelection,
			MaxTurns: int64(budget.MaxTurns), UsedTurns: 0, MaxReworkCount: int64(budget.MaxReworkCount), CreatedAt: at,
		}); err != nil {
			return err
		}
	}
	planByKey := map[string]cleardev.ComplexPlanTask{}
	for _, task := range plan.Tasks {
		planByKey[task.Key] = task
	}
	for _, task := range tasks {
		planTask := planByKey[task.TaskKey]
		// Admitted project generated paths are checked writes. In particular, a
		// dependency lock cannot be rebuilt by the historical mail-only template.
		if project {
			continue
		}
		if command, ok := cleardev.FrozenGeneratedCommandForPaths(planTask.GeneratedPaths); ok {
			argv, _ := json.Marshal(command.Argv)
			outputs, _ := json.Marshal(command.OutputPaths)
			if err := q.InsertClearDevComplexExceptionGeneratedCommand(ctx, gen.InsertClearDevComplexExceptionGeneratedCommandParams{
				ID: task.ID + ":generated:" + command.ID, ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID,
				CommandID: command.ID, ArgvJson: string(argv), TimeoutSeconds: int64(command.TimeoutSeconds),
				OutputPathsJson: string(outputs), Image: command.Image, CommandSpecSha256: complexExecutionDigest(command.Argv), CreatedAt: at,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func acquireComplexExceptionPathLeases(ctx context.Context, q *gen.Queries, runID, taskID, dispatchID string, paths []string, at time.Time) error {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if err := q.InsertClearDevComplexExceptionPathLease(ctx, gen.InsertClearDevComplexExceptionPathLeaseParams{
			ID: runID + ":lease:" + dispatchID + ":" + path, ExecutionRunID: runID, ComplexExecutionTaskID: taskID,
			Path: path, Status: "HELD", CreatedAt: at,
		}); err != nil {
			return complexExecutionRule("shared or generated path is already leased to another running task")
		}
	}
	return nil
}

func releaseComplexExceptionPathLeases(ctx context.Context, q *gen.Queries, runID, taskID string, at time.Time) error {
	leases, err := q.ListClearDevComplexExceptionPathLeases(ctx, runID)
	if err != nil {
		return err
	}
	for _, lease := range leases {
		if lease.ComplexExecutionTaskID != taskID || lease.Status != "HELD" {
			continue
		}
		if _, err := q.ReleaseClearDevComplexExceptionPathLeaseCAS(ctx, gen.ReleaseClearDevComplexExceptionPathLeaseCASParams{ID: lease.ID, ReleasedAt: nullableTime(at)}); err != nil {
			return err
		}
	}
	return nil
}

// OccupyClearDevComplexExceptionBudget records one budget turn before send.
// A round key makes the turn per-round: the same round never recounts, no
// matter how many messages it sends, while an empty key keeps the per-step
// behaviour for roles that have no multi-message round.
func (s *Store) OccupyClearDevComplexExceptionBudget(ctx context.Context, budgetID, roundKey, agentStepID string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if strings.TrimSpace(budgetID) == "" || strings.TrimSpace(agentStepID) == "" {
		return false, complexExecutionRule("budget occupancy requires a budget and agent step")
	}
	if strings.TrimSpace(roundKey) == "" {
		roundKey = agentStepID
	}
	var occupied bool
	err := s.inTx(ctx, "occupy ClearDev complex exception budget", func(q *gen.Queries) error {
		if old, e := q.GetClearDevComplexExceptionBudgetOccupancyByStep(ctx, agentStepID); e == nil {
			if old.BudgetID != budgetID {
				return complexExecutionRule("agent step already occupies a different budget")
			}
			occupied = true
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		// A round's first message runs the counted CAS update; later messages
		// of the same round record their own step row without one, because the
		// message-binding guard resolves every send through its own occupancy
		// row while the turn still belongs to the round.
		startRound := false
		if old, e := q.GetClearDevComplexExceptionBudgetOccupancyByRoundKey(ctx, sql.NullString{String: roundKey, Valid: true}); e == nil {
			if old.BudgetID != budgetID {
				return complexExecutionRule("agent round already occupies a different budget")
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		} else {
			startRound = true
		}
		if startRound {
			rows, e := q.OccupyClearDevComplexExceptionBudgetCAS(ctx, budgetID)
			if e != nil {
				return e
			}
			if rows != 1 {
				return complexExecutionRule("exception budget is exhausted")
			}
		}
		if e := q.InsertClearDevComplexExceptionBudgetOccupancy(ctx, gen.InsertClearDevComplexExceptionBudgetOccupancyParams{
			ID: budgetID + ":occ:" + agentStepID, BudgetID: budgetID, AgentStepID: agentStepID,
			RoundKey: sql.NullString{String: roundKey, Valid: true}, OccupiedAt: at,
		}); e != nil {
			return e
		}
		occupied = true
		return nil
	})
	return occupied, err
}

// RecordClearDevComplexExceptionScopeRequest stores a pending Builder shared-path request.
func (s *Store) RecordClearDevComplexExceptionScopeRequest(ctx context.Context, request cleardev.ComplexScopeExpansionRequest) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record ClearDev complex scope request", func(q *gen.Queries) error {
		current, err := currentComplexExceptionScope(ctx, q, request.ExecutionRunID, request.ComplexExecutionTaskID, request.DispatchID, request.Round, request.RequirementVersionID, request.RequirementSHA256, request.PlanID, request.PlanSHA256)
		if err != nil {
			return err
		}
		if _, err := cleardev.ApprovedSharedPaths(planTaskFromPackage(current.pkg), request.RequestedPaths); err != nil {
			return scopePathRuleError(err)
		}
		paths, err := json.Marshal(request.RequestedPaths)
		if err != nil {
			return err
		}
		return q.InsertClearDevComplexExceptionScopeRequest(ctx, gen.InsertClearDevComplexExceptionScopeRequestParams{
			ID: request.ID, ExecutionRunID: request.ExecutionRunID, ComplexExecutionTaskID: request.ComplexExecutionTaskID,
			DispatchID: request.DispatchID, Round: int64(request.Round), RequirementVersionID: request.RequirementVersionID,
			RequirementSha256: request.RequirementSHA256, PlanID: request.PlanID, PlanSha256: request.PlanSHA256,
			RequestedPathsJson: string(paths), AgentStepID: request.AgentStepID, Status: "PENDING", CreatedAt: request.CreatedAt,
		})
	})
}

// AcceptClearDevComplexExceptionScope appends a permission version and settles the request as approved.
func (s *Store) AcceptClearDevComplexExceptionScope(ctx context.Context, decision cleardev.ComplexScopeExpansionDecision, permission cleardev.PermissionVersion, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "accept ClearDev complex scope expansion", func(q *gen.Queries) error {
		request, err := q.GetClearDevComplexExceptionScopeRequest(ctx, decision.RequestID)
		if err != nil {
			return err
		}
		if request.Status != "PENDING" || request.ExecutionRunID != decision.ExecutionRunID {
			return complexExceptionScopeStale("scope decision does not match the current request")
		}
		current, err := currentComplexExceptionScope(ctx, q, request.ExecutionRunID, request.ComplexExecutionTaskID, request.DispatchID, int(request.Round), decision.RequirementVersionID, decision.RequirementSHA256, decision.PlanID, decision.PlanSHA256)
		if err != nil {
			return err
		}
		if request.RequirementVersionID != decision.RequirementVersionID || request.RequirementSha256 != decision.RequirementSHA256 ||
			request.PlanID != decision.PlanID || request.PlanSha256 != decision.PlanSHA256 {
			return complexExceptionScopeStale("scope decision does not match the current request")
		}
		var requested []string
		requested, err = decodeJSONStringSlice(request.RequestedPathsJson, "exception scope request paths")
		if err != nil {
			return err
		}
		if !sameStringSet(decision.Paths, requested) {
			return complexExceptionScopeStale("scope decision paths do not match the original request")
		}
		approved, err := cleardev.ApprovedSharedPaths(planTaskFromPackage(current.pkg), requested)
		if err != nil {
			return scopePathRuleError(err)
		}
		expected := cleardev.PermissionRulesAfterScopeApproval(current.permission, approved)
		if permission.DevelopmentTaskID != current.task.WorkItemID || !samePathRules(permission.Rules, expected) {
			return complexExceptionScopeStale("new permission is not derived from the original request")
		}
		next, err := q.NextClearDevPermissionVersion(ctx, current.task.WorkItemID)
		if err != nil {
			return err
		}
		write := mustJSONStringArray(expected.WritePaths)
		generated := mustJSONStringArray(expected.GeneratedPaths)
		shared := mustJSONStringArray(expected.SharedPathsRequireApproval)
		forbidden := mustJSONStringArray(expected.ForbiddenPaths)
		if err := q.InsertClearDevComplexExecutionPermission(ctx, gen.InsertClearDevComplexExecutionPermissionParams{
			ID: permission.ID, WorkItemID: current.task.WorkItemID, Version: next, WritePaths: write, GeneratedPaths: generated,
			SharedPathsRequireApproval: shared, ForbiddenPaths: forbidden, ComplexExecutionTaskID: nullableString(current.task.ID), CreatedAt: at,
		}); err != nil {
			return err
		}
		paths, _ := json.Marshal(requested)
		if err := q.InsertClearDevComplexExceptionScopeDecision(ctx, gen.InsertClearDevComplexExceptionScopeDecisionParams{
			ID: decision.ID, RequestID: decision.RequestID, ExecutionRunID: request.ExecutionRunID, RequirementVersionID: decision.RequirementVersionID,
			RequirementSha256: decision.RequirementSHA256, PlanID: decision.PlanID, PlanSha256: decision.PlanSHA256, PathsJson: string(paths),
			Decision: cleardev.ComplexScopeDecisionApprove, ReasonCode: "SCOPE_APPROVED", Summary: decision.Summary,
			StewardRoleBindingID: decision.StewardRoleBindingID, AgentStepID: decision.AgentStepID, PermissionVersionID: nullableString(permission.ID),
			ControlAccepted: true, CreatedAt: at,
		}); err != nil {
			return err
		}
		return settleComplexExceptionScopeRequest(ctx, q, request.ID, "APPROVED", "SCOPE_APPROVED", at)
	})
}

// CreateClearDevComplexExceptionAgentStep inserts a specialist, recovery, scope, or continue step.
func (s *Store) CreateClearDevComplexExceptionAgentStep(ctx context.Context, runID, roleBindingID, ondemandID string, step cleardev.AgentStep) (cleardev.AgentStep, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.AgentStep
	var created bool
	err := s.inTx(ctx, "create ClearDev complex exception agent step", func(q *gen.Queries) error {
		if old, e := q.GetClearDevComplexExceptionAgentStep(ctx, step.ID); e == nil {
			out = complexExceptionAgentStepFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e := q.InsertClearDevComplexExceptionAgentStep(ctx, gen.InsertClearDevComplexExceptionAgentStepParams{
			ID: step.ID, ExecutionRunID: runID, RoleBindingID: nullableString(roleBindingID), OndemandBindingID: nullableString(ondemandID),
			StepKind: string(step.Kind), RequestID: step.RequestID, ClientMessageID: step.ClientMessageID, PromptSha256: step.PromptSHA256,
			SendStatus: string(cleardev.AgentStepSendStatusPending), RequestedAt: step.RequestedAt,
		}); e != nil {
			return e
		}
		out, created = step, true
		out.SendStatus = cleardev.AgentStepSendStatusPending
		return nil
	})
	return out, created, err
}

// MarkClearDevComplexExceptionAgentStepSent records that the Chat turn left the control plane.
func (s *Store) MarkClearDevComplexExceptionAgentStepSent(ctx context.Context, id string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "mark ClearDev complex exception agent step sent", func(q *gen.Queries) error {
		rows, e := q.MarkClearDevComplexExceptionAgentStepSentCAS(ctx, gen.MarkClearDevComplexExceptionAgentStepSentCASParams{ID: id, SentAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// SettleClearDevComplexExceptionAgentStep stores the exact final message text and hash.
func (s *Store) SettleClearDevComplexExceptionAgentStep(ctx context.Context, step cleardev.AgentStep) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "settle ClearDev complex exception agent step", func(q *gen.Queries) error {
		completed := time.Time{}
		if step.CompletedAt != nil {
			completed = *step.CompletedAt
		}
		rows, e := q.SettleClearDevComplexExceptionAgentStepCAS(ctx, gen.SettleClearDevComplexExceptionAgentStepCASParams{
			ID: step.ID, TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID),
			FinalMessageText: nullableString(step.FinalMessageText), MessageSha256: nullableString(step.MessageSHA256), CompletedAt: nullableTime(completed),
		})
		changed = rows == 1
		return e
	})
	return changed, err
}

// CreateClearDevComplexExceptionOnDemandBinding inserts one Specialist or Recovery binding.
func (s *Store) CreateClearDevComplexExceptionOnDemandBinding(ctx context.Context, binding cleardev.ComplexOnDemandBinding) (cleardev.ComplexOnDemandBinding, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out cleardev.ComplexOnDemandBinding
	var created bool
	err := s.inTx(ctx, "create ClearDev complex on-demand binding", func(q *gen.Queries) error {
		if old, e := q.GetClearDevComplexExceptionOnDemandBinding(ctx, binding.ID); e == nil {
			out = complexOnDemandFromGen(old)
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e := q.InsertClearDevComplexExceptionOnDemandBinding(ctx, gen.InsertClearDevComplexExceptionOnDemandBindingParams{
			ID: binding.ID, ExecutionRunID: binding.ExecutionRunID, ComplexExecutionTaskID: nullableString(binding.ComplexExecutionTaskID),
			Mode: binding.Mode, TriggerReason: string(binding.TriggerReason), SessionCreationIdempotencyKey: binding.SessionCreationIdempotencyKey,
			Status: string(cleardev.RoleBindingStatusRequested), BindingFingerprint: binding.BindingFingerprint, RequestedAt: binding.RequestedAt,
		}); e != nil {
			return e
		}
		out, created = binding, true
		out.Status = cleardev.RoleBindingStatusRequested
		return nil
	})
	return out, created, err
}

// BindClearDevComplexExceptionOnDemand records the observed session, workspace, and base SHA.
func (s *Store) BindClearDevComplexExceptionOnDemand(ctx context.Context, id, sessionID, workspace, base string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "bind ClearDev complex on-demand role", func(q *gen.Queries) error {
		rows, e := q.BindClearDevComplexExceptionOnDemandCAS(ctx, gen.BindClearDevComplexExceptionOnDemandCASParams{
			ID: id, AoSessionID: nullableString(sessionID), WorkspacePath: workspace, BaseCommitSha: base, BoundAt: nullableTime(at),
		})
		changed = rows == 1
		return e
	})
	return changed, err
}

// EndClearDevComplexExceptionOnDemand marks a bound on-demand role as ENDED.
func (s *Store) EndClearDevComplexExceptionOnDemand(ctx context.Context, id string, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "end ClearDev complex on-demand role", func(q *gen.Queries) error {
		rows, e := q.EndClearDevComplexExceptionOnDemandCAS(ctx, gen.EndClearDevComplexExceptionOnDemandCASParams{ID: id, ReasonCode: string(reason), EndedAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// RecordClearDevComplexExceptionSpecialist stores a specialist result and pending specialist checks.
func (s *Store) RecordClearDevComplexExceptionSpecialist(ctx context.Context, result cleardev.ComplexSpecialistResult, checks []cleardev.ComplexSpecialistCheck) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record ClearDev complex specialist result", func(q *gen.Queries) error {
		if err := q.InsertClearDevComplexExceptionSpecialistResult(ctx, gen.InsertClearDevComplexExceptionSpecialistResultParams{
			ID: result.ID, ExecutionRunID: result.ExecutionRunID, ComplexExecutionTaskID: result.ComplexExecutionTaskID,
			OndemandBindingID: result.OnDemandBindingID, AgentStepID: result.AgentStepID, BindingFingerprint: result.BindingFingerprint,
			Outcome: result.Outcome, ConstraintsJson: result.ConstraintsJSON, ReasonCode: string(result.ReasonCode), Summary: result.Summary, CreatedAt: result.CreatedAt,
		}); err != nil {
			return err
		}
		for _, check := range checks {
			argv, _ := json.Marshal(check.Argv)
			if err := q.InsertClearDevComplexExceptionSpecialistCheck(ctx, gen.InsertClearDevComplexExceptionSpecialistCheckParams{
				ID: check.ID, ExecutionRunID: check.ExecutionRunID, ComplexExecutionTaskID: check.ComplexExecutionTaskID,
				SpecialistResultID: result.ID, CheckID: check.CheckID, ArgvJson: string(argv), Status: "PENDING", CreatedAt: check.CreatedAt,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// StartClearDevComplexExceptionSpecialistCheck marks a specialist check started.
func (s *Store) StartClearDevComplexExceptionSpecialistCheck(ctx context.Context, id string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "start ClearDev specialist check", func(q *gen.Queries) error {
		rows, e := q.MarkClearDevComplexExceptionSpecialistCheckStartedCAS(ctx, id)
		changed = rows == 1
		return e
	})
	return changed, err
}

// SettleClearDevComplexExceptionSpecialistCheck records a specialist check result.
func (s *Store) SettleClearDevComplexExceptionSpecialistCheck(ctx context.Context, check cleardev.ComplexSpecialistCheck, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "settle ClearDev specialist check", func(q *gen.Queries) error {
		rows, e := q.SettleClearDevComplexExceptionSpecialistCheckCAS(ctx, gen.SettleClearDevComplexExceptionSpecialistCheckCASParams{
			ID: check.ID, ContainerImageID: nullableString(check.ContainerImageID), Status: "SETTLED",
			Result: nullableString(string(check.Result)), ReasonCode: string(check.ReasonCode), SettledAt: nullableTime(at),
		})
		changed = rows == 1
		return e
	})
	return changed, err
}

// RecordClearDevComplexExceptionGeneratedProof inserts a pending generated-file proof.
func (s *Store) RecordClearDevComplexExceptionGeneratedProof(ctx context.Context, proof cleardev.ComplexGeneratedProof) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record ClearDev generated proof", func(q *gen.Queries) error {
		if old, e := q.GetClearDevComplexExceptionGeneratedProof(ctx, proof.ID); e == nil {
			_ = old
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		return q.InsertClearDevComplexExceptionGeneratedProof(ctx, gen.InsertClearDevComplexExceptionGeneratedProofParams{
			ID: proof.ID, ExecutionRunID: proof.ExecutionRunID, ComplexExecutionTaskID: proof.ComplexExecutionTaskID,
			DispatchID: proof.DispatchID, CandidateCommitID: proof.CandidateCommitID, CandidateCommitSha: proof.CandidateCommitSHA,
			CommandFactID: proof.CommandFactID, OutputSha256Json: "{}", Status: "PENDING", CreatedAt: proof.CreatedAt,
		})
	})
}

// StartClearDevComplexExceptionGeneratedProof marks a generated-file proof started.
func (s *Store) StartClearDevComplexExceptionGeneratedProof(ctx context.Context, id string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "start ClearDev generated proof", func(q *gen.Queries) error {
		rows, e := q.MarkClearDevComplexExceptionGeneratedProofStartedCAS(ctx, id)
		changed = rows == 1
		return e
	})
	return changed, err
}

// SettleClearDevComplexExceptionGeneratedProof records the rebuild comparison.
func (s *Store) SettleClearDevComplexExceptionGeneratedProof(ctx context.Context, proof cleardev.ComplexGeneratedProof, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "settle ClearDev generated proof", func(q *gen.Queries) error {
		status := "SETTLED"
		if proof.Status == "FAILED" {
			status = "FAILED"
		}
		rows, e := q.SettleClearDevComplexExceptionGeneratedProofCAS(ctx, gen.SettleClearDevComplexExceptionGeneratedProofCASParams{
			ID: proof.ID, ContainerImageID: nullableString(proof.ContainerImageID), OutputSha256Json: proof.OutputSHA256JSON,
			Status: status, Result: nullableString(string(proof.Result)), ReasonCode: string(proof.ReasonCode), SettledAt: nullableTime(at),
		})
		changed = rows == 1
		return e
	})
	return changed, err
}

// RecordClearDevComplexExceptionRecovery stores the allowed recovery action for one failure.
func (s *Store) RecordClearDevComplexExceptionRecovery(ctx context.Context, action cleardev.ComplexRecoveryAction) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record ClearDev recovery action", func(q *gen.Queries) error {
		return q.InsertClearDevComplexExceptionRecoveryAction(ctx, gen.InsertClearDevComplexExceptionRecoveryActionParams{
			ID: action.ID, ExecutionRunID: action.ExecutionRunID, ComplexExecutionTaskID: nullableString(action.ComplexExecutionTaskID),
			OndemandBindingID: action.OnDemandBindingID, AgentStepID: action.AgentStepID, TriggerReason: string(action.TriggerReason),
			TriggerFactID: action.TriggerFactID, Action: action.Action, Outcome: action.Outcome, RetryCheckRunID: nullableString(action.RetryCheckRunID),
			ReasonCode: string(action.ReasonCode), Summary: action.Summary, CreatedAt: action.CreatedAt,
		})
	})
}

func complexOnDemandFromGen(row gen.CleardevComplexExceptionOndemandBinding) cleardev.ComplexOnDemandBinding {
	item := cleardev.ComplexOnDemandBinding{
		ID: row.ID, ExecutionRunID: row.ExecutionRunID, ComplexExecutionTaskID: row.ComplexExecutionTaskID.String,
		Mode: row.Mode, TriggerReason: cleardev.ReasonCode(row.TriggerReason), SessionCreationIdempotencyKey: row.SessionCreationIdempotencyKey,
		AOSessionID: row.AoSessionID.String, WorkspacePath: row.WorkspacePath, BaseCommitSHA: row.BaseCommitSha,
		Status: cleardev.RoleBindingStatus(row.Status), ReasonCode: cleardev.ReasonCode(row.ReasonCode),
		BindingFingerprint: row.BindingFingerprint, RequestedAt: row.RequestedAt,
	}
	if row.BoundAt.Valid {
		item.BoundAt = &row.BoundAt.Time
	}
	if row.EndedAt.Valid {
		item.EndedAt = &row.EndedAt.Time
	}
	return item
}

// FailClearDevComplexExceptionAgentStep records an unusable exception Agent step.
func (s *Store) FailClearDevComplexExceptionAgentStep(ctx context.Context, id string, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "fail ClearDev complex exception agent step", func(q *gen.Queries) error {
		rows, e := q.FailClearDevComplexExceptionAgentStepCAS(ctx, gen.FailClearDevComplexExceptionAgentStepCASParams{ID: id, ReasonCode: string(reason), FailedAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// FailClearDevComplexExceptionOnDemand marks a requested on-demand role as FAILED.
func (s *Store) FailClearDevComplexExceptionOnDemand(ctx context.Context, id string, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	err := s.inTx(ctx, "fail ClearDev complex on-demand role", func(q *gen.Queries) error {
		rows, e := q.FailClearDevComplexExceptionOnDemandCAS(ctx, gen.FailClearDevComplexExceptionOnDemandCASParams{ID: id, ReasonCode: string(reason), EndedAt: nullableTime(at)})
		changed = rows == 1
		return e
	})
	return changed, err
}

// RejectClearDevComplexExceptionScope settles a pending request without a new permission version.
func (s *Store) RejectClearDevComplexExceptionScope(ctx context.Context, decision cleardev.ComplexScopeExpansionDecision, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "reject ClearDev complex scope expansion", func(q *gen.Queries) error {
		request, err := q.GetClearDevComplexExceptionScopeRequest(ctx, decision.RequestID)
		if err != nil {
			return err
		}
		if request.Status != "PENDING" || request.ExecutionRunID != decision.ExecutionRunID {
			return complexExceptionScopeStale("scope decision does not match the current request")
		}
		if _, err := currentComplexExceptionScope(ctx, q, request.ExecutionRunID, request.ComplexExecutionTaskID, request.DispatchID, int(request.Round), decision.RequirementVersionID, decision.RequirementSHA256, decision.PlanID, decision.PlanSHA256); err != nil {
			return err
		}
		if request.RequirementVersionID != decision.RequirementVersionID || request.RequirementSha256 != decision.RequirementSHA256 ||
			request.PlanID != decision.PlanID || request.PlanSha256 != decision.PlanSHA256 {
			return complexExceptionScopeStale("scope decision does not match the current request")
		}
		paths, _ := json.Marshal(decision.Paths)
		if err := q.InsertClearDevComplexExceptionScopeDecision(ctx, gen.InsertClearDevComplexExceptionScopeDecisionParams{
			ID: decision.ID, RequestID: decision.RequestID, ExecutionRunID: request.ExecutionRunID, RequirementVersionID: decision.RequirementVersionID,
			RequirementSha256: decision.RequirementSHA256, PlanID: decision.PlanID, PlanSha256: decision.PlanSHA256, PathsJson: string(paths),
			Decision: decision.Decision, ReasonCode: string(decision.ReasonCode), Summary: decision.Summary,
			StewardRoleBindingID: decision.StewardRoleBindingID, AgentStepID: decision.AgentStepID, ControlAccepted: false, CreatedAt: at,
		}); err != nil {
			return err
		}
		return settleComplexExceptionScopeRequest(ctx, q, request.ID, "REJECTED", string(decision.ReasonCode), at)
	})
}

func settleComplexExceptionScopeRequest(ctx context.Context, q *gen.Queries, id, status, reason string, at time.Time) error {
	rows, err := q.SettleClearDevComplexExceptionScopeRequestCAS(ctx, gen.SettleClearDevComplexExceptionScopeRequestCASParams{
		ID: id, Status: status, ReasonCode: reason, SettledAt: nullableTime(at),
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return complexExceptionScopeStale("scope request was not pending")
	}
	return nil
}

func latestPermissionID(ctx context.Context, q *gen.Queries, workItemID string) (string, error) {
	row, err := q.GetLatestClearDevPermissionVersion(ctx, workItemID)
	if err != nil {
		return "", err
	}
	return row.ID, nil
}

type complexExceptionScopeCurrent struct {
	run        gen.CleardevComplexExecutionRun
	task       gen.CleardevComplexExecutionTaskMapping
	attempt    gen.CleardevComplexExecutionTaskAttempt
	pkg        cleardev.ComplexStandardExecutionPackage
	permission cleardev.PathRules
}

func currentComplexExceptionScope(ctx context.Context, q *gen.Queries, executionRunID, taskID, dispatchID string, round int, requirementVersionID, requirementSHA256, planID, planSHA256 string) (complexExceptionScopeCurrent, error) {
	var current complexExceptionScopeCurrent
	run, err := q.GetClearDevComplexExecutionRun(ctx, executionRunID)
	if err != nil {
		return current, err
	}
	if run.Status != "ACCEPTED" || run.RequirementVersionID != requirementVersionID || run.RequirementSha256 != requirementSHA256 ||
		run.PlanID != planID || run.PlanSha256 != planSHA256 {
		return current, complexExceptionScopeStale("scope request does not match the current accepted run")
	}
	version, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, run.DevelopmentProjectID)
	if err != nil {
		return current, err
	}
	if version.ID != run.RequirementVersionID || version.State != "APPROVED" || version.SupersededByID.Valid ||
		version.Sha256 != run.RequirementSha256 || version.TaskSetVersion != run.AcceptedTaskSetVersion {
		return current, complexExceptionScopeStale("scope request no longer owns the current approved task set")
	}
	if stopped, err := activeDirectionStop(ctx, q, run.RequirementVersionID); err != nil {
		return current, err
	} else if stopped {
		return current, directionStoppedError()
	}
	task, err := q.GetClearDevComplexExecutionTaskMapping(ctx, taskID)
	if err != nil {
		return current, err
	}
	if task.ExecutionRunID != run.ID {
		return current, complexExceptionScopeStale("scope request does not match the current task")
	}
	attempt, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, dispatchID)
	if err != nil {
		return current, err
	}
	if attempt.ExecutionRunID != run.ID || attempt.TaskMappingID != task.ID || attempt.Round != int64(round) || attempt.Status != "RUNNING" {
		return current, complexExceptionScopeStale("scope request does not match the current running round")
	}
	pkg, err := cleardev.ParseComplexStandardExecutionPackage([]byte(task.TaskPacketJson))
	if err != nil {
		return current, err
	}
	if pkg.RequirementVersionID != run.RequirementVersionID || pkg.RequirementSHA256 != run.RequirementSha256 ||
		pkg.PlanID != run.PlanID || pkg.PlanSHA256 != run.PlanSha256 || pkg.TaskSetVersion != run.AcceptedTaskSetVersion {
		return current, complexExceptionScopeStale("scope request does not match the current task package")
	}
	row, err := q.GetLatestClearDevPermissionVersion(ctx, task.WorkItemID)
	if err != nil {
		return current, err
	}
	current.run, current.task, current.attempt, current.pkg = run, task, attempt, pkg
	rules, err := pathRulesFromColumns(row.WritePaths, row.GeneratedPaths, row.SharedPathsRequireApproval, row.ForbiddenPaths)
	if err != nil {
		return current, err
	}
	current.permission = rules
	return current, nil
}

func planTaskFromPackage(pkg cleardev.ComplexStandardExecutionPackage) cleardev.ComplexPlanTask {
	return cleardev.ComplexPlanTask{
		WritePaths: pkg.WritePaths, GeneratedPaths: pkg.GeneratedPaths,
		SharedPathsRequireApproval: pkg.SharedPathsRequireApproval, ForbiddenPaths: pkg.ForbiddenPaths,
	}
}

func pathRulesFromColumns(write, generated, shared, forbidden string) (cleardev.PathRules, error) {
	rules := cleardev.PathRules{}
	var err error
	if rules.WritePaths, err = decodeJSONStringSlice(write, "permission write paths"); err != nil {
		return cleardev.PathRules{}, err
	}
	if rules.GeneratedPaths, err = decodeJSONStringSlice(generated, "permission generated paths"); err != nil {
		return cleardev.PathRules{}, err
	}
	if rules.SharedPathsRequireApproval, err = decodeJSONStringSlice(shared, "permission shared paths"); err != nil {
		return cleardev.PathRules{}, err
	}
	if rules.ForbiddenPaths, err = decodeJSONStringSlice(forbidden, "permission forbidden paths"); err != nil {
		return cleardev.PathRules{}, err
	}
	return rules, nil
}

func samePathRules(left, right cleardev.PathRules) bool {
	return sameStringSet(left.WritePaths, right.WritePaths) &&
		sameStringSet(left.GeneratedPaths, right.GeneratedPaths) &&
		sameStringSet(left.SharedPathsRequireApproval, right.SharedPathsRequireApproval) &&
		sameStringSet(left.ForbiddenPaths, right.ForbiddenPaths)
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := map[string]int{}
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func complexExceptionScopeStale(message string) error {
	return &cleardev.RuleError{Code: cleardev.ReasonScopeStaleBinding, Message: message}
}

func scopePathRuleError(err error) error {
	code := cleardev.ReasonScopePathNotListed
	message := err.Error()
	switch {
	case strings.Contains(message, string(cleardev.ReasonScopePathForbidden)):
		code = cleardev.ReasonScopePathForbidden
	case strings.Contains(message, string(cleardev.ReasonScopeDuplicateRequest)):
		code = cleardev.ReasonScopeDuplicateRequest
	}
	return &cleardev.RuleError{Code: code, Message: message}
}

func mustJSONStringArray(values []string) string {
	if values == nil {
		return "[]"
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func exclusiveTaskPathsFromPackage(raw string) []string {
	pkg, err := cleardev.ParseComplexStandardExecutionPackage([]byte(raw))
	if err != nil {
		return nil
	}
	return append(append([]string{}, pkg.SharedPathsRequireApproval...), pkg.GeneratedPaths...)
}
