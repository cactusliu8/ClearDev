package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func replacementDigest(v any) string {
	raw, _ := core.CanonicalJSONBytes(v)
	return complexExecutionRawDigest(raw)
}
func replacementJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
func replacementUnavailable(s *core.BuilderReplacementState, reason string) {
	s.Option.UnavailableReason = reason
}
func builderReplacementHarnessAllowed(h domain.AgentHarness) bool {
	return h == domain.HarnessCodex || h == domain.HarnessOpenCode
}

// builderReplacementTransitions projects only durable authority and retirement
// facts. A role reason alone cannot suppress an ended Builder's stop.
func builderReplacementTransitions(ctx context.Context, q *gen.Queries, run core.ComplexExecutionRun) ([]core.BuilderReplacementTransition, error) {
	_, project, err := core.ProjectContractFromRun(run)
	if err != nil || !project || run.Mode != core.WorkModeStandard {
		return nil, err
	}
	requests, err := q.ListClearDevBuilderReplacementRequests(ctx, run.DevelopmentRequirementID)
	if err != nil {
		return nil, err
	}
	var out []core.BuilderReplacementTransition
	for _, r := range requests {
		if r.ExecutionRunID != run.ID {
			continue
		}
		h, err := q.GetClearDevBuilderReplacementHandoffForStep(ctx, r.LogicalStepID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := validateBuilderReplacementGrant(ctx, q, r); err != nil {
			return nil, err
		}
		bound, err := builderHandoffFromRows(ctx, q, h, r)
		if err != nil {
			return nil, err
		}
		b := bound.Binding
		if b.ExecutionRunID != run.ID || b.DevelopmentRequirementID != run.DevelopmentRequirementID || b.LogicalStepID != r.LogicalStepID || h.RequestID != r.ID || h.DecisionRequestID != r.DecisionRequestID || h.NewRoleBindingID != b.NewRoleBindingID || h.SecondAttemptID != b.LogicalStepID+":attempt:2" || h.RetirementEventID != b.LogicalStepID+":replacement-retired" {
			return nil, productConflict("Builder transition changed its exact handoff")
		}
		first, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{LogicalStepID: b.LogicalStepID, AttemptNumber: 1})
		if err != nil {
			return nil, err
		}
		retired, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, first.ID)
		if err != nil {
			return nil, err
		}
		fence, err := q.GetClearDevBuilderSessionFence(ctx, b.OldAOSessionID)
		if err != nil {
			return nil, err
		}
		oldAttempt := agentStepAttemptToDomain(first)
		retirement := agentAttemptEventToDomain(retired)
		if !core.ValidAgentAttemptCreation(oldAttempt) || first.ID != b.LogicalStepID+":attempt:1" || first.DevelopmentProjectID != r.RequirementID || first.RoleBindingID != b.OldRoleBindingID || first.AoSessionID != b.OldAOSessionID || first.StepCategory != "COMPLEX_EXECUTION" || first.StepKind != "BUILDER_TASK" || first.ClientMessageID != b.ClientMessageID || first.PromptSha256 != b.PromptSHA256 || first.TriggerFailureEventID.Valid || retirement.ID != h.RetirementEventID || retirement.Status != core.AgentAttemptRetiredBeforeSend || retirement.ClientMessageID != b.ClientMessageID || retirement.PromptSHA256 != b.PromptSHA256 || retirement.TurnID != "" || retirement.TurnState != "" || retirement.FailureCategory != "" || retirement.Retryable || retirement.RetryAt != nil || retirement.ProviderErrorCode != "" || fence.HandoffID != h.ID || fence.OldRoleBindingID != b.OldRoleBindingID {
			return nil, productConflict("Builder transition lost its precise unsent retirement and fence")
		}
		t := core.BuilderReplacementTransition{HandoffID: h.ID, DecisionRequestID: r.DecisionRequestID, LogicalStepID: b.LogicalStepID, DispatchID: b.DispatchID, OldRoleBindingID: b.OldRoleBindingID, NewRoleBindingID: b.NewRoleBindingID, RetirementEventID: h.RetirementEventID, AttemptID: h.SecondAttemptID}
		if bound.NewAOSessionID != "" {
			second, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{LogicalStepID: b.LogicalStepID, AttemptNumber: 2})
			if err != nil {
				return nil, err
			}
			if !core.ValidBuilderReplacementAttempt(oldAttempt, retirement, agentStepAttemptToDomain(second), bound) {
				return nil, productConflict("Builder transition lost its precise bound second attempt")
			}
			t.AliasBound = true
			t.NewAOSessionID = bound.NewAOSessionID
		}
		out = append(out, t)
	}
	return out, nil
}

// replacementSource reads only the original durable source. Registration
// restores historical stop fields for identity checks without rewriting rows.
func replacementSource(ctx context.Context, q *gen.Queries, id string) (core.BuilderReplacementState, error) {
	var out core.BuilderReplacementState
	runs, err := q.ListClearDevComplexExecutionRuns(ctx, id)
	if err != nil || len(runs) == 0 {
		return out, err
	}
	run := runs[len(runs)-1]
	out.Execution.Run = complexExecutionRunFromGen(run)
	contract, project, err := core.ProjectContractFromRun(out.Execution.Run)
	if err != nil || !project || run.Mode != "STANDARD" {
		return out, err
	}
	_ = contract
	attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, run.ID)
	if err != nil {
		return out, err
	}
	for _, a := range attempts {
		out.Execution.Dispatches = append(out.Execution.Dispatches, complexExecutionDispatchFromGen(a))
	}
	// The current stopped dispatch is the last round of its task. A registered
	// handoff remains visible after that same row advances normally.
	requests, err := q.ListClearDevBuilderReplacementRequests(ctx, id)
	if err != nil {
		return out, err
	}
	var intent *gen.CleardevBuilderReplacementRequest
	var selected *gen.CleardevComplexExecutionTaskAttempt
	for i := range attempts {
		a := attempts[i]
		if a.Status != "BLOCKED" || a.ReasonCode != "BUILDER_SPAWN_FAILED" || !a.SettledAt.Valid {
			continue
		}
		latest := true
		for _, other := range attempts {
			if other.TaskMappingID == a.TaskMappingID && other.Round > a.Round {
				latest = false
			}
		}
		if latest {
			selected = &a
		}
	}
	// A later stopped logical step has its own single authorization. Historical
	// handoff display must not replace that current step's source.
	if selected != nil {
		for i := range requests {
			r := requests[i]
			if r.ExecutionRunID == run.ID && r.LogicalStepID == selected.AgentStepID {
				intent = &r
			}
		}
	} else {
		for i := range requests {
			r := requests[i]
			if r.ExecutionRunID == run.ID {
				intent = &r
			}
		}
	}
	if intent != nil {
		b, e := core.ParseBuilderReplacementBinding([]byte(intent.BindingJson))
		if e != nil {
			return out, e
		}
		a, e := q.GetClearDevComplexExecutionTaskAttempt(ctx, b.DispatchID)
		if e != nil {
			return out, e
		}
		selected = &a
		out.Binding = b
		out.RequestID = intent.ID
		out.DecisionRequestID = intent.DecisionRequestID
	}
	if selected == nil {
		return out, nil
	}
	a := *selected
	step, err := q.GetClearDevComplexExecutionAgentStep(ctx, a.AgentStepID)
	if err != nil {
		return out, err
	}
	binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, a.BuilderRoleBindingID)
	if err != nil {
		return out, err
	}
	task, err := q.GetClearDevComplexExecutionTaskMapping(ctx, a.TaskMappingID)
	if err != nil {
		return out, err
	}
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, task.ID)
	if err != nil {
		return out, err
	}
	out.Step = complexExecutionAgentStepFromGen(step)
	out.OldBinding = complexExecutionRoleBindingFromGen(binding)
	out.Dispatch = complexExecutionDispatchFromGen(a)
	out.Task = core.ComplexExecutionTask{ID: task.ID, ExecutionRunID: run.ID, TaskKey: task.PlanTaskKey, DevelopmentTaskID: task.WorkItemID, Ordinal: int(task.Ordinal), ExecutionPackageJSON: task.TaskPacketJson, ExecutionPackageSHA256: task.TaskPacketSha256, Status: core.DevelopmentTaskStatus(item.State), ReworkCount: int(item.ReworkCount), CurrentRound: int(a.Round), CurrentDispatchID: a.ID}
	out.Dispatch.DevelopmentTaskID = task.WorkItemID
	out.Dispatch.ExecutionPackageSHA256 = task.TaskPacketSha256
	out.Dispatch.ClientMessageID = step.ClientMessageID
	out.Option = core.WorkflowRecoveryOption{Action: core.RecoveryRequestBuilderReplacement, TargetID: step.ID, TaskID: task.ID, Role: "BUILDER", Reason: a.ReasonCode, UnavailableReason: "RESULT_NOT_SETTLED"}
	if intent != nil {
		out.Option.TargetID = out.Binding.TargetID
		out.Option.UnavailableReason = "BUILDER_REPLACEMENT_SOURCE_CHANGED"
		h, e := q.GetClearDevBuilderReplacementHandoffForStep(ctx, step.ID)
		if e == nil {
			v, e := builderHandoffFromRows(ctx, q, h, *intent)
			if e != nil {
				return out, e
			}
			out.Handoff = &v
			observations, e := q.ListClearDevBuilderReplacementObservations(ctx, h.ID)
			if e != nil {
				return out, e
			}
			for _, o := range observations {
				out.Observations = append(out.Observations, builderObservationFromRow(o))
			}
			out.Option.Action = core.RecoveryContinueBuilderReplacement
			out.Option.UnavailableReason = "BUILDER_REPLACEMENT_SOURCE_CHANGED"
		} else if !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
	}
	requirement, err := q.GetClearDevRequirement(ctx, id)
	if err != nil {
		return out, err
	}
	version, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, id)
	if err != nil {
		return out, missingBuilderProof(err)
	}
	if run.Status != "ACCEPTED" || run.SettledAt.Valid || requirement.CancelledAt.Valid || requirement.PausedFromState.Valid || requirement.State == "PAUSED" || version.ID != run.RequirementVersionID || version.Sha256 != run.RequirementSha256 || version.SupersededByID.Valid {
		replacementUnavailable(&out, "EXECUTION_NOT_CURRENT")
		return out, nil
	}
	if stopped, e := activeDirectionStop(ctx, q, version.ID); e != nil {
		return out, e
	} else if stopped {
		replacementUnavailable(&out, "RECOVERY_DIRECTION_STOP")
		return out, nil
	}
	mb, err := q.GetClearDevMessageBudgetVersion(ctx, id)
	if err != nil || mb.Version != string(core.MessageBudgetV1) {
		replacementUnavailable(&out, string(core.ReasonMessageBudgetUnknown))
		return out, missingBuilderProof(err)
	}
	session, err := q.GetSession(ctx, domain.SessionID(binding.AoSessionID.String))
	if err != nil {
		return out, missingBuilderProof(err)
	}
	if binding.Role != "BUILDER" || binding.ExecutionRunID != run.ID || step.RoleBindingID != binding.ID || step.RequestID != a.ID || step.StepKind != "BUILDER_TASK" || string(session.ProjectID) != requirement.AoProjectID || session.WorkspacePath != binding.WorkspacePath || session.CreationIdempotencyKey != binding.SessionCreationIdempotencyKey || session.SessionMode != "chat" || !builderReplacementHarnessAllowed(session.Harness) || session.PermissionMode != "auto" {
		return out, nil
	}
	budgets, err := q.ListClearDevComplexExceptionBudgets(ctx, run.ID)
	if err != nil {
		return out, err
	}
	var budget gen.CleardevComplexExceptionBudget
	for _, b := range budgets {
		if b.ComplexExecutionTaskID.String == task.ID && b.RoleKind == "BUILDER" {
			budget = b
		}
	}
	if budget.ID == "" {
		replacementUnavailable(&out, string(core.ReasonMessageBudgetUnknown))
		return out, nil
	}
	types, err := decodeJSONStringSlice(budget.AllowedAgentTypesJson, "Builder handoff budget types")
	if err != nil {
		return out, err
	}
	out.Budget = core.ComplexExceptionBudget{ID: budget.ID, ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, RoleKind: budget.RoleKind, AllowedAgentTypes: types, ModelSelection: budget.ModelSelection, MaxTurns: int(budget.MaxTurns), UsedTurns: int(budget.UsedTurns), MaxReworkCount: int(budget.MaxReworkCount), AuthorizedExtraTurns: int(budget.AuthorizedExtraTurns), CreatedAt: budget.CreatedAt}
	occupancy, occErr := q.GetClearDevComplexExceptionBudgetOccupancyByStep(ctx, step.ID)
	if occErr == nil {
		if occupancy.BudgetID != budget.ID || occupancy.RoundKey.String != a.ID {
			return out, nil
		}
		out.OccupancyID = occupancy.ID
	} else if !errors.Is(occErr, sql.ErrNoRows) {
		return out, occErr
	}
	if out.OccupancyID == "" && budget.UsedTurns >= budget.MaxTurns+budget.AuthorizedExtraTurns {
		replacementUnavailable(&out, "BUILDER_BUDGET_EXHAUSTED")
		return out, nil
	}
	if n, e := q.CountClearDevBuilderSessionOpenTurns(ctx, session.ID); e != nil {
		return out, e
	} else if n != 0 {
		replacementUnavailable(&out, "BUILDER_SESSION_BUSY")
		return out, nil
	}
	if n, e := q.CountOpenClearDevBuilderSessionOperations(ctx, string(session.ID)); e != nil {
		return out, e
	} else if n != 0 {
		replacementUnavailable(&out, "BUILDER_OPERATION_PENDING")
		return out, nil
	}
	oldAttempt, attemptErr := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{LogicalStepID: step.ID, AttemptNumber: 1})
	if attemptErr == nil {
		v := agentStepAttemptToDomain(oldAttempt)
		out.OldAttempt = &v
		if oldAttempt.ID != step.ID+":attempt:1" || oldAttempt.RoleBindingID != binding.ID || oldAttempt.AoSessionID != string(session.ID) || oldAttempt.PromptSha256 != step.PromptSha256 || oldAttempt.ClientMessageID != step.ClientMessageID || oldAttempt.TriggerFailureEventID.Valid || !core.ValidAgentAttemptCreation(v) {
			return out, nil
		}
	} else if !errors.Is(attemptErr, sql.ErrNoRows) {
		return out, attemptErr
	}
	source := struct {
		Run       gen.CleardevComplexExecutionRun
		Task      gen.CleardevComplexExecutionTaskMapping
		Dispatch  gen.CleardevComplexExecutionTaskAttempt
		Step      gen.CleardevComplexExecutionAgentStep
		Binding   gen.CleardevComplexExecutionRoleBinding
		Session   string
		Budget    core.ComplexExceptionBudget
		Occupancy any
		Attempt   any
		ItemState string
		Rework    int64
	}{run, task, a, step, binding, core.BuilderSessionBindingDigest(rowToRecord(session)), out.Budget, nil, nil, item.State, item.ReworkCount}
	if occErr == nil {
		source.Occupancy = occupancy
	}
	if attemptErr == nil {
		source.Attempt = oldAttempt
	}
	out.SourceSHA256 = replacementDigest(source)
	if intent == nil {
		if a.Status != "BLOCKED" || a.ReasonCode != "BUILDER_SPAWN_FAILED" || !a.SettledAt.Valid || step.SendStatus != "PENDING" || step.SentAt.Valid || step.TurnID.Valid || binding.Status != "BOUND" {
			return out, nil
		}
		unsent, err := builderSessionStepUnsent(ctx, q, run.ID, step.ID)
		if err != nil {
			return out, err
		}
		if !unsent {
			return out, nil
		}
		if _, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(a.ID)); err == nil {
			return out, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		correction, err := q.GetClearDevParseCorrection(ctx, step.ID)
		if err == nil && correction.StepID != "" {
			return out, nil
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		// Runtime and native availability are proved by the Service port.
		// Activity metadata is identity context, not a death observation.
		out.Option.TargetID = step.ID + ":replace:" + out.SourceSHA256
		out.Option.UnavailableReason = ""
		return out, nil
	}
	out.Option.TargetID = out.Binding.TargetID
	if out.Handoff != nil {
		validationErr := validateRegisteredBuilderReplacementSource(ctx, q, out, *intent)
		var rejection *core.RuleError
		if errors.As(validationErr, &rejection) && rejection.Code == core.ReasonPreconditionNotMet {
			replacementUnavailable(&out, "BUILDER_REPLACEMENT_SOURCE_CHANGED")
			return out, nil
		}
		if validationErr != nil {
			return out, validationErr
		}
		var historical core.ComplexExecutionDispatch
		if err := json.Unmarshal([]byte(intent.OldDispatchJson), &historical); err != nil {
			return out, err
		}
		out.Dispatch = historical
		var historicalStep core.AgentStep
		if err := json.Unmarshal([]byte(intent.OldStepJson), &historicalStep); err != nil {
			return out, err
		}
		out.Step = historicalStep
		out.SourceSHA256 = out.Binding.SourceSHA256
		out.Option.UnavailableReason = "CONTINUATION_REGISTERED"
		return out, nil
	}
	if out.SourceSHA256 != out.Binding.SourceSHA256 {
		replacementUnavailable(&out, "BUILDER_REPLACEMENT_SOURCE_CHANGED")
		return out, nil
	}
	decision, err := q.GetClearDevHumanDecisionRequest(ctx, intent.DecisionRequestID)
	if err != nil {
		return out, err
	}
	if decision.Status == "PENDING" {
		replacementUnavailable(&out, "BUILDER_REPLACEMENT_DECISION_PENDING")
		return out, nil
	}
	if decision.Decision != "APPROVE" {
		replacementUnavailable(&out, "BUILDER_REPLACEMENT_DECISION_REJECTED")
		return out, nil
	}
	if err := validateBuilderReplacementGrant(ctx, q, *intent); err != nil {
		return out, err
	}
	out.Option.Action = core.RecoveryContinueBuilderReplacement
	out.Option.UnavailableReason = ""
	return out, nil
}

func builderObservationFromRow(o gen.CleardevBuilderReplacementObservation) core.BuilderReplacementObservation {
	return core.BuilderReplacementObservation{ID: o.ID, HandoffID: o.HandoffID, Stage: o.Stage, OperationKey: o.OperationKey, Outcome: o.Outcome, ReasonCode: o.ReasonCode, AOSessionID: o.AoSessionID, WorkspacePath: o.WorkspacePath, LaunchSHA256: o.LaunchSha256, SnapshotSHA256: o.SnapshotSha256, ObservedAt: o.ObservedAt}
}
func builderHandoffFromRows(ctx context.Context, q *gen.Queries, h gen.CleardevBuilderReplacementHandoff, r gen.CleardevBuilderReplacementRequest) (core.BuilderReplacementHandoff, error) {
	b, err := core.ParseBuilderReplacementBinding([]byte(r.BindingJson))
	out := core.BuilderReplacementHandoff{ID: h.ID, RequestID: r.ID, DecisionRequestID: r.DecisionRequestID, Binding: b, RetirementEventID: h.RetirementEventID, AttemptID: h.SecondAttemptID, CreatedAt: h.CreatedAt}
	if err != nil {
		return out, err
	}
	alias, err := q.GetClearDevBuilderReplacementAlias(ctx, h.ID)
	if err == nil {
		out.NewAOSessionID = alias.NewAoSessionID
		out.NewWorkspacePath = alias.WorkspacePath
		out.LaunchSHA256 = alias.LaunchSha256
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	return out, nil
}

// ReadClearDevBuilderReplacement observes the durable source, authority and handoff.
func (s *Store) ReadClearDevBuilderReplacement(ctx context.Context, id string, _ time.Time) (core.BuilderReplacementState, error) {
	out, err := replacementSource(ctx, s.qr, id)
	if err != nil {
		return out, err
	}
	execution, found, err := s.GetClearDevComplexExecution(ctx, id)
	if err != nil {
		return out, err
	}
	if found {
		out.Execution = execution
		for _, t := range execution.Tasks {
			if t.ID == out.Task.ID {
				out.Task = t
				break
			}
		}
	}
	out.History, err = builderReplacementHistory(ctx, s.qr, id)
	return out, err
}
func builderReplacementHistory(ctx context.Context, q *gen.Queries, id string) ([]core.WorkflowRecovery, error) {
	rows, err := q.ListClearDevBuilderReplacementRequests(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []core.WorkflowRecovery{}
	for _, r := range rows {
		b, err := core.ParseBuilderReplacementBinding([]byte(r.BindingJson))
		if err != nil {
			return nil, err
		}
		old := core.WorkflowRecovery{ID: r.ID, ExecutionRunID: r.ExecutionRunID, Action: core.RecoveryRequestBuilderReplacement, TargetID: r.TargetID, TaskID: b.TaskID, DispatchID: b.DispatchID, StepID: b.LogicalStepID, BindingID: b.OldRoleBindingID, SuccessorID: r.DecisionRequestID, OriginalStoppedAt: r.OriginalStoppedAt, OriginalStatus: r.OriginalStatus, OriginalReason: r.OriginalReason, OriginalSummary: r.OriginalSummary, Supplement: r.Supplement, CreatedAt: r.CreatedAt}
		out = append(out, old)
		h, err := q.GetClearDevBuilderReplacementHandoffForStep(ctx, r.LogicalStepID)
		if err == nil {
			old.ID = h.ID
			old.Action = core.RecoveryContinueBuilderReplacement
			old.SuccessorID = h.NewRoleBindingID
			old.Supplement = h.Supplement
			old.CreatedAt = h.CreatedAt
			out = append(out, old)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func replacementRequestCollision(ctx context.Context, q *gen.Queries, id string) error {
	for _, load := range []func() error{func() error { _, e := q.GetClearDevWorkflowRecovery(ctx, id); return e }, func() error { _, e := q.GetClearDevPlanningStepRecovery(ctx, id); return e }, func() error { _, e := q.GetClearDevPlanningExtraRequest(ctx, id); return e }, func() error { _, e := q.GetClearDevPlanningExtraContinuation(ctx, id); return e }} {
		err := load()
		if err == nil {
			return productConflict("recovery request id already identifies another immutable intent")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}
func replacementIntentMatches(r gen.CleardevBuilderReplacementRequest, input core.WorkflowRecovery, id, action string) bool {
	return r.RequirementID == id && input.Action == action && r.TargetID == input.TargetID && r.Supplement == input.Supplement && (input.ExecutionRunID == "" || input.ExecutionRunID == r.ExecutionRunID)
}
func validateBuilderReplacementBinding(s core.BuilderReplacementState, b core.BuilderReplacementBinding) error {
	if b.DevelopmentRequirementID != s.Execution.Run.DevelopmentRequirementID || b.ExecutionRunID != s.Execution.Run.ID || b.RequirementVersionID != s.Execution.Run.RequirementVersionID || b.RequirementSHA256 != s.Execution.Run.RequirementSHA256 || b.TaskID != s.Task.ID || b.DispatchID != s.Dispatch.ID || b.LogicalStepID != s.Step.ID || b.Round != int64(s.Dispatch.Round) || b.OldRoleBindingID != s.OldBinding.ID || b.OldAOSessionID != s.OldBinding.AOSessionID || b.OldWorkspacePath != s.OldBinding.WorkspacePath || b.BaseCommitSHA != s.Dispatch.BaseCommitSHA || b.PromptSHA256 != s.Step.PromptSHA256 || b.ClientMessageID != s.Step.ClientMessageID || b.BudgetID != s.Budget.ID || b.OccupancyID != s.OccupancyID || b.BudgetSHA256 != complexExecutionRawDigest([]byte(replacementJSON(s.Budget))) || b.SourceSHA256 != s.SourceSHA256 || b.TargetID != s.Option.TargetID || b.NewRoleBindingID != s.Step.ID+":replacement-builder" || b.SessionCreationKey != s.Step.ID+":replacement-create" {
		return productConflict("Builder replacement changed the exact persistent source")
	}
	return nil
}

// RequestClearDevBuilderReplacement saves one exact intent and its pending native decision.
func (s *Store) RequestClearDevBuilderReplacement(ctx context.Context, id string, input core.WorkflowRecovery, decisionID string, b core.BuilderReplacementBinding, display core.HumanDecisionDisplay, at time.Time) error {
	if input.ID == "" || strings.TrimSpace(input.ID) != input.ID || len(input.ID) > 100 || len(input.Supplement) > 16000 {
		return productConflict("Builder replacement intent is invalid")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if _, err = core.ParseBuilderReplacementBinding(raw); err != nil {
		return err
	}
	displayRaw, err := json.Marshal(display)
	if err != nil {
		return err
	}
	if _, err := core.ParseHumanDecisionDisplay(displayRaw); err != nil {
		return err
	}
	digest, err := core.HumanDecisionContentSHA256(core.HumanDecisionKindBuilderReplacement, raw, display)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "request exact human Builder handoff", func(q *gen.Queries) error {
		prior, err := q.GetClearDevBuilderReplacementRequest(ctx, input.ID)
		if err == nil {
			if !replacementIntentMatches(prior, input, id, core.RecoveryRequestBuilderReplacement) {
				return productConflict("Builder replacement request changed on replay")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := q.GetClearDevBuilderReplacementHandoff(ctx, input.ID); err == nil {
			return productConflict("request id identifies an existing continuation")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := replacementRequestCollision(ctx, q, input.ID); err != nil {
			return err
		}
		state, err := replacementSource(ctx, q, id)
		if err != nil {
			return err
		}
		if state.Option.UnavailableReason != "" || state.Option.Action != input.Action || state.Option.TargetID != input.TargetID || state.RequestID != "" {
			return productConflict("Builder replacement is not currently eligible")
		}
		if err := validateBuilderReplacementBinding(state, b); err != nil {
			return err
		}
		session, err := q.GetSession(ctx, domain.SessionID(b.OldAOSessionID))
		if err != nil {
			return err
		}
		if b.SessionIdentitySHA256 != core.BuilderSessionBindingDigest(rowToRecord(session)) || b.OldBranch != session.Branch {
			return productConflict("original Builder identity changed")
		}
		if state.Dispatch.SettledAt == nil {
			return productConflict("original Builder stop lacks an observed time")
		}
		if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{ID: decisionID, DevelopmentProjectID: id, DecisionKind: core.HumanDecisionKindBuilderReplacement, BindingSchemaVersion: 1, BindingJson: string(raw), DisplayJson: string(displayRaw), ContentSha256: digest, CreatedAt: at}); err != nil {
			return err
		}
		return q.InsertClearDevBuilderReplacementRequest(ctx, gen.InsertClearDevBuilderReplacementRequestParams{ID: input.ID, RequirementID: id, ExecutionRunID: b.ExecutionRunID, LogicalStepID: b.LogicalStepID, DecisionRequestID: decisionID, TargetID: input.TargetID, BindingJson: string(raw), SourceJson: replacementJSON(state), OldDispatchJson: replacementJSON(state.Dispatch), OldStepJson: replacementJSON(state.Step), OriginalStoppedAt: *state.Dispatch.SettledAt, OriginalStatus: string(state.Dispatch.Status), OriginalReason: string(state.Dispatch.ReasonCode), OriginalSummary: state.Option.Summary, Supplement: input.Supplement, CreatedAt: at})
	})
}
func validateBuilderReplacementGrant(ctx context.Context, q *gen.Queries, r gen.CleardevBuilderReplacementRequest) error {
	grant, err := q.GetClearDevBuilderReplacementGrant(ctx, r.DecisionRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return productConflict("Builder replacement has no exact native grant")
	}
	if err != nil {
		return err
	}
	d, err := q.GetClearDevHumanDecisionRequest(ctx, r.DecisionRequestID)
	if err != nil {
		return err
	}
	effect, err := q.GetClearDevHumanDecisionEffect(ctx, r.DecisionRequestID)
	if err != nil {
		return err
	}
	if grant.RequestID != r.ID || grant.LogicalStepID != r.LogicalStepID || d.Status != "RESOLVED" || d.Decision != "APPROVE" || d.DecisionKind != core.HumanDecisionKindBuilderReplacement || d.BindingJson != r.BindingJson || effect.Decision != "APPROVE" {
		return productConflict("Builder replacement grant changed its native authority")
	}
	return nil
}

// ContinueClearDevBuilderReplacement atomically fences and retires the old unsent identity and requests its successor.
func (s *Store) ContinueClearDevBuilderReplacement(ctx context.Context, id string, input core.WorkflowRecovery, at time.Time) (core.BuilderReplacementState, error) {
	if input.ID == "" || strings.TrimSpace(input.ID) != input.ID || len(input.ID) > 100 || len(input.Supplement) > 16000 {
		return core.BuilderReplacementState{}, productConflict("Builder replacement continuation is invalid")
	}
	s.writeMu.Lock()
	err := s.inTx(ctx, "claim one authorized Builder handoff", func(q *gen.Queries) error {
		prior, err := q.GetClearDevBuilderReplacementHandoff(ctx, input.ID)
		if err == nil {
			r, err := q.GetClearDevBuilderReplacementRequest(ctx, prior.RequestID)
			if err != nil {
				return err
			}
			if input.Action != core.RecoveryContinueBuilderReplacement || input.TargetID != r.TargetID || input.Supplement != prior.Supplement || r.RequirementID != id || (input.ExecutionRunID != "" && input.ExecutionRunID != r.ExecutionRunID) {
				return productConflict("Builder continuation changed on replay")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := q.GetClearDevBuilderReplacementRequest(ctx, input.ID); err == nil {
			return productConflict("continuation id identifies its decision request")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := replacementRequestCollision(ctx, q, input.ID); err != nil {
			return err
		}
		state, err := replacementSource(ctx, q, id)
		if err != nil {
			return err
		}
		if input.Action != core.RecoveryContinueBuilderReplacement || state.Option.Action != input.Action || state.Option.TargetID != input.TargetID || state.Option.UnavailableReason != "" || state.Handoff != nil {
			return productConflict("Builder handoff lacks a current unused grant")
		}
		r, err := q.GetClearDevBuilderReplacementRequest(ctx, state.RequestID)
		if err != nil {
			return err
		}
		if err := validateBuilderReplacementGrant(ctx, q, r); err != nil {
			return err
		}
		b := state.Binding
		retirement := b.LogicalStepID + ":replacement-retired"
		second := b.LogicalStepID + ":attempt:2"
		if err := q.InsertClearDevBuilderReplacementHandoff(ctx, gen.InsertClearDevBuilderReplacementHandoffParams{ID: input.ID, RequestID: r.ID, DecisionRequestID: r.DecisionRequestID, LogicalStepID: b.LogicalStepID, NewRoleBindingID: b.NewRoleBindingID, SessionCreationKey: b.SessionCreationKey, RetirementEventID: retirement, SecondAttemptID: second, Supplement: input.Supplement, CreatedAt: at}); err != nil {
			return err
		}
		if err := q.InsertClearDevBuilderSessionFence(ctx, gen.InsertClearDevBuilderSessionFenceParams{AoSessionID: b.OldAOSessionID, OldRoleBindingID: b.OldRoleBindingID, HandoffID: input.ID, CreatedAt: at}); err != nil {
			return err
		}
		if state.OldAttempt == nil {
			if _, err := q.InsertClearDevAgentStepAttempt(ctx, gen.InsertClearDevAgentStepAttemptParams{ID: b.LogicalStepID + ":attempt:1", DevelopmentProjectID: id, LogicalStepID: b.LogicalStepID, StepCategory: "COMPLEX_EXECUTION", StepKind: "BUILDER_TASK", AttemptNumber: 1, RoleBindingID: b.OldRoleBindingID, AoSessionID: b.OldAOSessionID, ClientMessageID: b.ClientMessageID, PromptSha256: b.PromptSHA256, RequestedAt: at, CreatedAt: nullableTime(at), RequestedAtSemantics: string(core.AttemptTimeActualCreation)}); err != nil {
				return err
			}
		}
		if _, err := q.InsertClearDevAgentAttemptEvent(ctx, gen.InsertClearDevAgentAttemptEventParams{ID: retirement, AttemptID: b.LogicalStepID + ":attempt:1", Status: string(core.AgentAttemptRetiredBeforeSend), ClientMessageID: b.ClientMessageID, PromptSha256: b.PromptSHA256, RecordedAt: at}); err != nil {
			return err
		}
		rows, err := q.EndClearDevComplexExecutionRoleBindingCAS(ctx, gen.EndClearDevComplexExecutionRoleBindingCASParams{ID: b.OldRoleBindingID, ReasonCode: "BUILDER_REPLACED", EndedAt: nullableTime(at)})
		if err != nil {
			return err
		}
		if rows != 1 {
			return productConflict("old Builder binding changed before handoff")
		}
		replacement := core.ComplexExecutionRoleBinding{ID: b.NewRoleBindingID, ExecutionRunID: b.ExecutionRunID, Role: core.StandardRoleBuilder, BuilderSlot: state.OldBinding.BuilderSlot, ContinuationOfRoleBindingID: b.OldRoleBindingID, SessionCreationIdempotencyKey: b.SessionCreationKey, Status: core.RoleBindingStatusRequested, RequestedAt: at}
		if err := q.InsertClearDevComplexExecutionRoleBinding(ctx, complexExecutionRoleBindingParams(replacement)); err != nil {
			return err
		}
		rows, err = q.AdvanceClearDevComplexExecutionTaskAttemptCAS(ctx, gen.AdvanceClearDevComplexExecutionTaskAttemptCASParams{ID: b.DispatchID, ExpectedStatus: string(state.Dispatch.Status), Status: "RUNNING"})
		if err != nil {
			return err
		}
		if rows != 1 {
			return productConflict("original dispatch changed before handoff")
		}
		item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, b.TaskID)
		if err != nil {
			return err
		}
		rows, err = q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount, NextState: "RUNNING", ReworkCount: item.ReworkCount, UpdatedAt: at})
		if err != nil {
			return err
		}
		if rows != 1 {
			return productConflict("original task changed before handoff")
		}
		return nil
	})
	s.writeMu.Unlock()
	if err != nil {
		return core.BuilderReplacementState{}, err
	}
	return s.ReadClearDevBuilderReplacement(ctx, id, at)
}

// GetClearDevBuilderReplacementEffectiveBinding reads a handoff only after its exact alias is bound.
func (s *Store) GetClearDevBuilderReplacementEffectiveBinding(ctx context.Context, step string) (core.BuilderReplacementHandoff, bool, error) {
	return builderEffectiveBinding(ctx, s.qr, step)
}
func builderEffectiveBinding(ctx context.Context, q *gen.Queries, step string) (core.BuilderReplacementHandoff, bool, error) {
	h, err := q.GetClearDevBuilderReplacementHandoffForStep(ctx, step)
	if errors.Is(err, sql.ErrNoRows) {
		return core.BuilderReplacementHandoff{}, false, nil
	}
	if err != nil {
		return core.BuilderReplacementHandoff{}, false, err
	}
	r, err := q.GetClearDevBuilderReplacementRequest(ctx, h.RequestID)
	if err != nil {
		return core.BuilderReplacementHandoff{}, true, err
	}
	out, err := builderHandoffFromRows(ctx, q, h, r)
	return out, out.NewAOSessionID != "", err
}

// BindClearDevBuilderReplacementAlias binds the copied workspace and its unique second attempt.
func (s *Store) BindClearDevBuilderReplacementAlias(ctx context.Context, h core.BuilderReplacementHandoff, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "bind exact Builder replacement and its second attempt", func(q *gen.Queries) error {
		saved, err := q.GetClearDevBuilderReplacementHandoff(ctx, h.ID)
		if err != nil {
			return err
		}
		r, err := q.GetClearDevBuilderReplacementRequest(ctx, saved.RequestID)
		if err != nil {
			return err
		}
		expected, err := builderHandoffFromRows(ctx, q, saved, r)
		if err != nil {
			return err
		}
		if expected.Binding != h.Binding || expected.RetirementEventID != h.RetirementEventID || expected.AttemptID != h.AttemptID || h.NewAOSessionID == "" || h.NewAOSessionID == h.Binding.OldAOSessionID || h.NewWorkspacePath == "" || h.NewWorkspacePath == h.Binding.OldWorkspacePath || !validSHA256Digest(h.LaunchSHA256) {
			return productConflict("Builder alias changed its exact handoff")
		}
		if expected.NewAOSessionID != "" {
			if expected.NewAOSessionID != h.NewAOSessionID || expected.NewWorkspacePath != h.NewWorkspacePath || expected.LaunchSHA256 != h.LaunchSHA256 {
				return productConflict("Builder alias changed on replay")
			}
			return nil
		}
		state, err := replacementSource(ctx, q, r.RequirementID)
		if err != nil {
			return err
		}
		if state.Option.UnavailableReason != "CONTINUATION_REGISTERED" {
			return productConflict("Builder handoff source changed before binding")
		}
		if err := validateBuilderReplacementGrant(ctx, q, r); err != nil {
			return err
		}
		session, err := q.GetSession(ctx, domain.SessionID(h.NewAOSessionID))
		if err != nil {
			return err
		}
		launchContext, err := q.GetClearDevBuilderHandoffContext(ctx, h.NewAOSessionID)
		if err != nil {
			return err
		}
		b := h.Binding
		oldSession, err := q.GetSession(ctx, domain.SessionID(b.OldAOSessionID))
		if err != nil {
			return err
		}
		if session.IsTerminated || session.ActivityState != domain.ActivityIdle || !builderReplacementHarnessAllowed(session.Harness) || session.Harness != oldSession.Harness || session.SessionMode != "chat" || session.PermissionMode != "auto" || session.CreationIdempotencyKey != b.SessionCreationKey || session.CreationRequestFingerprint != h.LaunchSHA256 || session.WorkspacePath != h.NewWorkspacePath || session.DiffBaseSha != state.OldBinding.BaseCommitSHA || launchContext.LaunchFingerprint != h.LaunchSHA256 || launchContext.ReferencePath != b.HandoffPath || launchContext.ContextSha256 != b.HandoffSHA256 || launchContext.SnapshotSha256 != b.SnapshotSHA256 {
			return productConflict("new Builder lost its exact launch or copied snapshot")
		}
		if n, e := q.CountOpenClearDevBuilderSessionOperations(ctx, h.NewAOSessionID); e != nil || n != 0 {
			return productConflict("new Builder launch or lifecycle operation is unresolved")
		}
		if n, e := q.CountClearDevBuilderSessionOpenTurns(ctx, domain.SessionID(h.NewAOSessionID)); e != nil || n != 0 {
			return productConflict("new Builder has an unresolved provider turn before binding")
		}
		rows, err := q.BindClearDevComplexExecutionRoleBindingCAS(ctx, gen.BindClearDevComplexExecutionRoleBindingCASParams{ID: b.NewRoleBindingID, AoSessionID: nullableString(h.NewAOSessionID), WorkspacePath: h.NewWorkspacePath, BaseCommitSha: state.OldBinding.BaseCommitSHA, BoundAt: nullableTime(at)})
		if err != nil {
			return err
		}
		if rows != 1 {
			return productConflict("new Builder binding already changed")
		}
		if err := q.InsertClearDevBuilderReplacementAlias(ctx, gen.InsertClearDevBuilderReplacementAliasParams{HandoffID: h.ID, LogicalStepID: b.LogicalStepID, NewRoleBindingID: b.NewRoleBindingID, NewAoSessionID: h.NewAOSessionID, WorkspacePath: h.NewWorkspacePath, LaunchSha256: h.LaunchSHA256, SnapshotSha256: b.SnapshotSHA256, SecondAttemptID: h.AttemptID, CreatedAt: at}); err != nil {
			return err
		}
		first, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{LogicalStepID: b.LogicalStepID, AttemptNumber: 1})
		if err != nil {
			return err
		}
		retired, err := q.GetClearDevAgentAttemptEvent(ctx, h.RetirementEventID)
		if err != nil {
			return err
		}
		second := core.AgentStepAttempt{ID: h.AttemptID, DevelopmentRequirementID: r.RequirementID, LogicalStepID: b.LogicalStepID, StepCategory: core.AgentStepCategoryComplexExecution, StepKind: core.ComplexExecutionAgentStepBuilderTask, AttemptNumber: 2, RoleBindingID: b.NewRoleBindingID, AOSessionID: h.NewAOSessionID, ClientMessageID: b.ClientMessageID + ":attempt:2", PromptSHA256: b.PromptSHA256, TriggerFailureEventID: h.RetirementEventID, RequestedAt: at, CreatedAt: &at, RequestedAtSemantics: core.AttemptTimeActualCreation}
		if !core.ValidBuilderReplacementAttempt(agentStepAttemptToDomain(first), agentAttemptEventToDomain(retired), second, h) {
			return productConflict("Builder second attempt is not the exact authorized replacement")
		}
		_, err = q.InsertClearDevAgentStepAttempt(ctx, gen.InsertClearDevAgentStepAttemptParams{ID: second.ID, DevelopmentProjectID: r.RequirementID, LogicalStepID: b.LogicalStepID, StepCategory: string(second.StepCategory), StepKind: string(second.StepKind), AttemptNumber: 2, RoleBindingID: b.NewRoleBindingID, AoSessionID: h.NewAOSessionID, ClientMessageID: second.ClientMessageID, PromptSha256: b.PromptSHA256, TriggerFailureEventID: nullableString(h.RetirementEventID), RequestedAt: at, CreatedAt: nullableTime(at), RequestedAtSemantics: string(core.AttemptTimeActualCreation)})
		return err
	})
}

// ReadClearDevBuilderReplacementBeforeSend checks the exact alias and reads the original saved prompt feedback.
func (s *Store) ReadClearDevBuilderReplacementBeforeSend(ctx context.Context, id, step string, at time.Time) (core.BuilderReplacementState, bool, error) {
	state, found, err := builderReplacementBeforeSend(ctx, s.qr, id, step)
	if err != nil || !found {
		return state, found, err
	}
	// The sender must reconstruct the original prompt from the same complete
	// saved check/review/recovery feedback used by the normal execution loop.
	// The transaction helper above only establishes exact durable admission.
	full, err := s.ReadClearDevBuilderReplacement(ctx, id, at)
	if err != nil {
		return full, true, err
	}
	if full.Step.ID != step || full.Handoff == nil || state.Handoff == nil || full.Handoff.ID != state.Handoff.ID || full.Binding != state.Binding {
		return full, true, productConflict("Builder replacement source changed before observing its original prompt")
	}
	return full, true, nil
}
func builderReplacementBeforeSend(ctx context.Context, q *gen.Queries, id, step string) (core.BuilderReplacementState, bool, error) {
	h, found, err := builderEffectiveBinding(ctx, q, step)
	if err != nil || !found {
		return core.BuilderReplacementState{}, found, err
	}
	state, err := replacementSource(ctx, q, id)
	if err != nil {
		return state, true, err
	}
	if state.Handoff == nil || state.Handoff.ID != h.ID || state.Option.UnavailableReason != "CONTINUATION_REGISTERED" {
		return state, true, productConflict("Builder replacement source changed before sending")
	}
	r, err := q.GetClearDevBuilderReplacementRequest(ctx, h.RequestID)
	if err != nil {
		return state, true, err
	}
	if err := validateBuilderReplacementGrant(ctx, q, r); err != nil {
		return state, true, err
	}
	session, err := q.GetSession(ctx, domain.SessionID(h.NewAOSessionID))
	if err != nil {
		return state, true, err
	}
	b := h.Binding
	oldSession, err := q.GetSession(ctx, domain.SessionID(b.OldAOSessionID))
	if err != nil {
		return state, true, err
	}
	if session.IsTerminated || session.ActivityState != domain.ActivityIdle || !builderReplacementHarnessAllowed(session.Harness) || session.Harness != oldSession.Harness || session.SessionMode != "chat" || session.PermissionMode != "auto" || session.CreationIdempotencyKey != b.SessionCreationKey || session.CreationRequestFingerprint != h.LaunchSHA256 || session.WorkspacePath != h.NewWorkspacePath {
		return state, true, productConflict("new Builder is not idle or its launch changed")
	}
	role, e := q.GetClearDevComplexExecutionRoleBinding(ctx, b.NewRoleBindingID)
	if e != nil {
		return state, true, e
	}
	launch, e := q.GetClearDevBuilderHandoffContext(ctx, h.NewAOSessionID)
	if e != nil {
		return state, true, e
	}
	if role.Status != "BOUND" || role.AoSessionID.String != h.NewAOSessionID || role.WorkspacePath != h.NewWorkspacePath || role.ContinuationOfRoleBindingID.String != b.OldRoleBindingID || role.SessionCreationIdempotencyKey != b.SessionCreationKey || launch.LaunchFingerprint != h.LaunchSHA256 || launch.ReferencePath != b.HandoffPath || launch.ContextSha256 != b.HandoffSHA256 || launch.SnapshotSha256 != b.SnapshotSHA256 {
		return state, true, productConflict("new Builder effective binding or private launch context changed")
	}
	if open, e := q.CountOpenClearDevBuilderSessionOperations(ctx, h.NewAOSessionID); e != nil || open != 0 {
		return state, true, productConflict("new Builder has an unresolved lifecycle operation")
	}
	if n, err := q.CountClearDevBuilderSessionOpenTurns(ctx, domain.SessionID(h.NewAOSessionID)); err != nil || n != 0 {
		return state, true, productConflict("new Builder has an unresolved turn")
	}
	return state, true, nil
}

// RecordClearDevBuilderReplacementObservation saves an immutable external outcome or replays the same fact.
func (s *Store) RecordClearDevBuilderReplacementObservation(ctx context.Context, o core.BuilderReplacementObservation) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record immutable Builder handoff observation", func(q *gen.Queries) error {
		old, err := q.GetClearDevBuilderReplacementObservation(ctx, o.ID)
		if err == nil {
			stored := builderObservationFromRow(old)
			stored.ObservedAt = o.ObservedAt
			if !reflect.DeepEqual(stored, o) {
				return productConflict("Builder handoff observation changed on replay")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rows, e := q.ListClearDevBuilderReplacementObservations(ctx, o.HandoffID)
		if e != nil {
			return e
		}
		for _, row := range rows {
			if row.Stage != o.Stage || row.OperationKey != o.OperationKey || row.Outcome != o.Outcome {
				continue
			}
			saved := builderObservationFromRow(row)
			saved.ID = o.ID
			saved.ObservedAt = o.ObservedAt
			if !reflect.DeepEqual(saved, o) {
				return productConflict("Builder stage observation changed on replay")
			}
			return nil
		}
		return q.InsertClearDevBuilderReplacementObservation(ctx, gen.InsertClearDevBuilderReplacementObservationParams{ID: o.ID, HandoffID: o.HandoffID, Stage: o.Stage, OperationKey: o.OperationKey, Outcome: o.Outcome, ReasonCode: o.ReasonCode, AoSessionID: o.AOSessionID, WorkspacePath: o.WorkspacePath, LaunchSha256: o.LaunchSHA256, SnapshotSha256: o.SnapshotSHA256, ObservedAt: o.ObservedAt})
	})
}

// ClaimClearDevBuilderReplacementOperation grants one caller the stage claim; a saved claim never grants another action.
func (s *Store) ClaimClearDevBuilderReplacementOperation(ctx context.Context, handoff, stage, key string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	claimed := false
	err := s.inTx(ctx, "claim one Builder handoff side effect", func(q *gen.Queries) error {
		rows, err := q.ListClearDevBuilderReplacementObservations(ctx, handoff)
		if err != nil {
			return err
		}
		for _, o := range rows {
			if o.Stage == stage {
				if o.OperationKey != key {
					return productConflict("Builder handoff stage changed its operation key")
				}
				return nil
			}
		}
		h, e := q.GetClearDevBuilderReplacementHandoff(ctx, handoff)
		if e != nil {
			return e
		}
		r, e := q.GetClearDevBuilderReplacementRequest(ctx, h.RequestID)
		if e != nil {
			return e
		}
		source, e := replacementSource(ctx, q, r.RequirementID)
		if e != nil {
			return e
		}
		if source.Handoff == nil || source.Handoff.ID != handoff || source.Option.UnavailableReason != "CONTINUATION_REGISTERED" || source.SourceSHA256 != source.Binding.SourceSHA256 {
			return productConflict("Builder handoff source changed before its external stage claim")
		}
		if err := q.InsertClearDevBuilderReplacementObservation(ctx, gen.InsertClearDevBuilderReplacementObservationParams{ID: handoff + ":claim:" + stage + ":" + replacementDigest(key), HandoffID: handoff, Stage: stage, OperationKey: key, Outcome: "STARTED", ObservedAt: at}); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed && err == nil, err
}

func validateReopenBuilderReplacement(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, r gen.CleardevHumanDecisionRequest) error {
	b, err := core.ParseBuilderReplacementBinding(result.Binding)
	if err != nil {
		return err
	}
	intent, err := q.GetClearDevBuilderReplacementRequestForDecision(ctx, r.ID)
	if err != nil {
		return err
	}
	if intent.BindingJson != r.BindingJson || intent.RequirementID != b.DevelopmentRequirementID || intent.LogicalStepID != b.LogicalStepID {
		return productConflict("Builder decision changed its immutable request")
	}
	state, err := replacementSource(ctx, q, b.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	if state.SourceSHA256 != b.SourceSHA256 || state.Option.UnavailableReason != "BUILDER_REPLACEMENT_DECISION_PENDING" || state.Handoff != nil {
		return productConflict("Builder decision source is no longer current")
	}
	return nil
}
func settleBuilderReplacement(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, r gen.CleardevHumanDecisionRequest, at time.Time) (*core.RuleError, error) {
	if err := validateReopenBuilderReplacement(ctx, q, result, r); err != nil {
		return nil, err
	}
	rows, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{ID: r.ID, Decision: string(result.Decision), ResolvedAt: nullableTime(at)})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, productConflict("Builder native decision already settled")
	}
	outcome := core.HumanDecisionDispatchRejected
	if result.Decision == core.HumanDecisionApprove {
		outcome = core.HumanDecisionDispatchApproved
	}
	if err := consumeDispatchCAS(ctx, q, result.Nonce, result.DesktopRunID, outcome, at); err != nil {
		return nil, err
	}
	project, err := q.GetClearDevRequirement(ctx, r.DevelopmentProjectID)
	if err != nil {
		return nil, err
	}
	if err := insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: project.AoProjectID, DevelopmentRequirementID: project.ID, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: r.ID, Action: core.ActionSettleHumanDecision, PreviousState: "PENDING", TargetState: "RESOLVED", Outcome: core.EventAccepted, Source: core.EventSourceHumanDecision, CreatedAt: at}); err != nil {
		return nil, err
	}
	event, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: r.ID, Action: string(core.ActionSettleHumanDecision)})
	if err != nil {
		return nil, err
	}
	if err := q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: r.ID, Decision: string(result.Decision), EventSequence: event, CreatedAt: at}); err != nil {
		return nil, err
	}
	if result.Decision == core.HumanDecisionApprove {
		intent, err := q.GetClearDevBuilderReplacementRequestForDecision(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		if err := q.InsertClearDevBuilderReplacementGrant(ctx, gen.InsertClearDevBuilderReplacementGrantParams{DecisionRequestID: r.ID, RequestID: intent.ID, LogicalStepID: intent.LogicalStepID, CreatedAt: at}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// authorizedBuilderReplacementAttempt is a narrow identity alias. It retains
// the original logical step and ledger rather than allocating a role budget.
func authorizedBuilderReplacementAttempt(ctx context.Context, q *gen.Queries, a core.AgentStepAttempt) (bool, error) {
	if a.AttemptNumber != 2 || a.StepKind != core.ComplexExecutionAgentStepBuilderTask {
		return false, nil
	}
	h, found, err := builderEffectiveBinding(ctx, q, a.LogicalStepID)
	if err != nil || !found {
		return false, err
	}
	first, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{LogicalStepID: a.LogicalStepID, AttemptNumber: 1})
	if err != nil {
		return false, err
	}
	retired, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, first.ID)
	if err != nil {
		return false, err
	}
	if !core.ValidBuilderReplacementAttempt(agentStepAttemptToDomain(first), agentAttemptEventToDomain(retired), a, h) {
		return false, productConflict("Builder attempt changed the authorized identity alias")
	}
	return true, nil
}

// Registered handoffs normalize only the lifecycle changes they themselves
// authorized. Immutable source and budget ceilings remain exact.
func validateRegisteredBuilderReplacementSource(ctx context.Context, q *gen.Queries, current core.BuilderReplacementState, r gen.CleardevBuilderReplacementRequest) error {
	var old core.BuilderReplacementState
	if err := json.Unmarshal([]byte(r.SourceJson), &old); err != nil {
		return err
	}
	b := current.Binding
	h := current.Handoff
	if h == nil {
		return productConflict("handoff missing")
	}
	if !reflect.DeepEqual(current.Execution.Run, old.Execution.Run) || current.Task.ID != old.Task.ID || current.Task.ExecutionPackageJSON != old.Task.ExecutionPackageJSON || current.Task.ExecutionPackageSHA256 != old.Task.ExecutionPackageSHA256 || current.Task.ReworkCount != old.Task.ReworkCount || current.Dispatch.ID != old.Dispatch.ID || current.Dispatch.Round != old.Dispatch.Round || current.Dispatch.BaseCommitSHA != old.Dispatch.BaseCommitSHA || current.Dispatch.AgentStepID != old.Dispatch.AgentStepID || current.Dispatch.BuilderRoleBindingID != old.Dispatch.BuilderRoleBindingID || current.Dispatch.Status == core.ComplexExecutionDispatchBlocked || current.Dispatch.Status == core.ComplexExecutionDispatchNeedsHuman {
		return productConflict("original execution or task changed")
	}
	for _, a := range current.Execution.Dispatches {
		if a.ComplexExecutionTaskID == old.Task.ID && a.Round > old.Dispatch.Round {
			return productConflict("original dispatch is no longer current")
		}
	}
	step := current.Step
	step.SendStatus = old.Step.SendStatus
	step.TurnID = old.Step.TurnID
	step.FinalMessageID = old.Step.FinalMessageID
	step.FinalMessageText = old.Step.FinalMessageText
	step.MessageSHA256 = old.Step.MessageSHA256
	step.SentAt = old.Step.SentAt
	step.CompletedAt = old.Step.CompletedAt
	step.FailedAt = old.Step.FailedAt
	step.ReasonCode = old.Step.ReasonCode
	if !reflect.DeepEqual(step, old.Step) {
		return productConflict("original step identity changed")
	}
	role := current.OldBinding
	if role.Status != core.RoleBindingStatusEnded || role.ReasonCode != "BUILDER_REPLACED" || role.EndedAt == nil || !role.EndedAt.Equal(h.CreatedAt) {
		return productConflict("original Builder retirement changed")
	}
	role.Status = old.OldBinding.Status
	role.ReasonCode = old.OldBinding.ReasonCode
	role.EndedAt = old.OldBinding.EndedAt
	if !reflect.DeepEqual(role, old.OldBinding) {
		return productConflict("original Builder binding changed")
	}
	budget := current.Budget
	expectedUsed := old.Budget.UsedTurns
	if old.OccupancyID == "" && current.OccupancyID != "" {
		expectedUsed++
	}
	if current.Budget.UsedTurns != expectedUsed {
		return productConflict("original Builder budget use changed")
	}
	budget.UsedTurns = old.Budget.UsedTurns
	if !reflect.DeepEqual(budget, old.Budget) {
		return productConflict("original Builder budget authority changed")
	}
	if old.OccupancyID != "" && current.OccupancyID != old.OccupancyID {
		return productConflict("original budget occupancy changed")
	}
	session, err := q.GetSession(ctx, domain.SessionID(b.OldAOSessionID))
	if err != nil {
		return err
	}
	if core.BuilderSessionBindingDigest(rowToRecord(session)) != b.SessionIdentitySHA256 || session.Branch != b.OldBranch {
		return productConflict("original Builder session identity changed")
	}
	fence, err := q.GetClearDevBuilderSessionFence(ctx, b.OldAOSessionID)
	if err != nil {
		return err
	}
	if fence.HandoffID != h.ID || fence.OldRoleBindingID != b.OldRoleBindingID {
		return productConflict("old Builder fence changed")
	}
	first, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{LogicalStepID: b.LogicalStepID, AttemptNumber: 1})
	if err != nil {
		return err
	}
	retired, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, first.ID)
	if err != nil {
		return err
	}
	if retired.ID != h.RetirementEventID || retired.Status != string(core.AgentAttemptRetiredBeforeSend) {
		return productConflict("old attempt retirement changed")
	}
	return validateBuilderReplacementGrant(ctx, q, r)
}
