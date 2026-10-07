package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

type plannerCoordinationRecoveryProof struct {
	state core.PlannerCoordinationRecoveryState
	step  gen.CleardevComplexAgentStep
	first gen.CleardevAgentStepAttempt
	stop  gen.CleardevPlannerRuntimeDecision
}

func plannerCoordinationHistory(row gen.CleardevPlannerRuntimeRecovery) (core.PlannerCoordinationRecovery, error) {
	var b core.PlannerCoordinationRecoveryBinding
	if err := json.Unmarshal([]byte(row.BindingJson), &b); err != nil {
		return core.PlannerCoordinationRecovery{}, err
	}
	target, err := core.PlannerCoordinationRecoveryTarget(b)
	if err != nil {
		return core.PlannerCoordinationRecovery{}, err
	}
	return core.PlannerCoordinationRecovery{EventID: row.EventID, Recovery: core.WorkflowRecovery{
		ID: row.ID, ExecutionRunID: row.ExecutionRunID, Action: core.RecoveryRetryPlannerCoordination, TargetID: target,
		StepID: row.LogicalStepID, BindingID: b.RoleBindingID, SuccessorID: row.SecondAttemptID,
		OriginalStatus: "STOP", OriginalReason: string(core.ReasonPlannerRuntimeUnavailable), OriginalSummary: row.OriginalSummary,
		OriginalStoppedAt: row.OriginalStoppedAt, Supplement: row.Supplement, CreatedAt: row.CreatedAt,
		ProviderConversationID: b.ProviderConversationID,
	}}, nil
}

// ReadClearDevPlannerCoordinationRecovery has no provider or write effects.
func (s *Store) ReadClearDevPlannerCoordinationRecovery(ctx context.Context, id string, at time.Time) (core.PlannerCoordinationRecoveryState, error) {
	runs, err := s.qr.ListClearDevComplexExecutionRuns(ctx, id)
	if err != nil || len(runs) == 0 {
		return core.PlannerCoordinationRecoveryState{}, err
	}
	run := runs[len(runs)-1]
	requests, err := s.qr.ListClearDevPlannerRuntimeRequests(ctx, run.ID)
	if err != nil || len(requests) == 0 {
		return core.PlannerCoordinationRecoveryState{}, err
	}
	p, err := plannerCoordinationRecoveryEvidence(ctx, s.qr, id, requests[len(requests)-1].EventID, at, false)
	if err != nil {
		return p.state, err
	}
	rows, err := s.qr.ListClearDevPlannerCoordinationRecoveries(ctx, run.ID)
	if err != nil {
		return p.state, err
	}
	for _, row := range rows {
		h, err := plannerCoordinationHistory(row)
		if err != nil {
			return p.state, err
		}
		p.state.History = append(p.state.History, h.Recovery)
	}
	return p.state, nil
}

// Only a known ended original message without any assistant output is eligible.
// A missing reply alone is not proof: confirmed delivery and the exact terminal
// turn are required, including a positive archive event for completed/no-reply.
func plannerCoordinationRecoveryEvidence(ctx context.Context, q *gen.Queries, id, eventID string, at time.Time, sending bool) (plannerCoordinationRecoveryProof, error) {
	var p plannerCoordinationRecoveryProof
	stop, err := q.GetClearDevPlannerRuntimeDecision(ctx, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if stop.Source != "CONTROL_PLANE" || stop.Outcome != "STOP" || stop.ReasonCode != string(core.ReasonPlannerRuntimeUnavailable) || stop.ResultJson.Valid || stop.ResultSha256.Valid {
		return p, nil
	}
	p.stop = stop
	p.state.Option = core.WorkflowRecoveryOption{Action: core.RecoveryRetryPlannerCoordination, TargetID: eventID, Role: "ENGINEERING_PLANNER", Reason: stop.ReasonCode, Summary: stop.Summary, UnavailableReason: "RESULT_NOT_SETTLED"}
	run, err := q.GetClearDevComplexExecutionRun(ctx, stop.ExecutionRunID)
	if err != nil {
		return p, err
	}
	if run.DevelopmentProjectID != id || run.Mode != "STANDARD" || !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) {
		p.state.Option = core.WorkflowRecoveryOption{}
		return p, nil
	}
	current, err := plannerRuntimeCurrent(ctx, q, run)
	if err != nil {
		return p, err
	}
	if !current {
		p.state.Option.UnavailableReason = "EXECUTION_NOT_CURRENT"
		return p, nil
	}
	requests, err := q.ListClearDevPlannerRuntimeRequests(ctx, run.ID)
	if err != nil {
		return p, err
	}
	if len(requests) == 0 || requests[len(requests)-1].EventID != eventID {
		p.state.Option.UnavailableReason = "EXECUTION_NOT_CURRENT"
		return p, nil
	}
	request := requests[len(requests)-1]
	step, err := q.GetClearDevComplexAgentStep(ctx, request.AgentStepID)
	if err != nil {
		return p, err
	}
	p.step = step
	registration, registeredErr := q.GetClearDevPlannerCoordinationRecoveryForEvent(ctx, eventID)
	if registeredErr != nil && !errors.Is(registeredErr, sql.ErrNoRows) {
		return p, registeredErr
	}
	if !sending && registeredErr == nil {
		p.state.Option.UnavailableReason = "COORDINATION_RECOVERY_REGISTERED"
		if decision, err := q.GetClearDevPlannerCoordinationRecoveryDecision(ctx, eventID); err == nil {
			if decision.Source == "PLANNER" && (decision.Outcome == core.PlannerRuntimeContinue || decision.Outcome == core.PlannerRuntimeAmend) {
				p.state.Option = core.WorkflowRecoveryOption{}
				return p, nil
			}
			p.state.Option.UnavailableReason = string(core.ReasonMessageBudgetExhausted)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return p, err
		}
		var b core.PlannerCoordinationRecoveryBinding
		if err := json.Unmarshal([]byte(registration.BindingJson), &b); err != nil {
			return p, err
		}
		p.state.Binding = b
		p.state.Option.TargetID, err = core.PlannerCoordinationRecoveryTarget(b)
		return p, err
	}
	if (!sending && step.SendStatus != "FAILED") || (sending && step.SendStatus != "PENDING") || step.StepKind != "COMPLEX_ENGINEERING_PLAN" || step.RequestID != eventID || step.RoleBindingID != request.PlannerRoleBindingID || step.PromptSha256 != complexExecutionRawDigest([]byte(request.Prompt)) {
		return p, nil
	}
	role, err := q.GetClearDevComplexRoleBinding(ctx, request.PlannerRoleBindingID)
	if err != nil {
		return p, err
	}
	session, err := q.GetSession(ctx, domain.SessionID(request.AoSessionID))
	if err != nil {
		return p, missingBuilderProof(err)
	}
	if role.Status != "BOUND" || role.Role != "ENGINEERING_PLANNER" || role.DevelopmentProjectID != id || role.AoSessionID.String != request.AoSessionID || session.IsTerminated || session.Kind != domain.KindWorker || string(session.ProjectID) == "" || session.PermissionMode != "auto" || session.SessionMode != domain.SessionModeChat || session.WorkspacePath == "" || session.ProviderConversationID == "" || session.CreationIdempotencyKey != role.SessionCreationIdempotencyKey || session.CreationRequestFingerprint == "" {
		p.state.Option.UnavailableReason = "ORIGINAL_PLANNER_UNAVAILABLE"
		return p, nil
	}
	requirement, err := q.GetClearDevRequirement(ctx, id)
	if err != nil {
		return p, err
	}
	matches, err := q.IsClearDevPlanningContinuationSession(ctx, request.AoSessionID)
	if err != nil {
		return p, err
	}
	if !matches || string(session.ProjectID) != requirement.AoProjectID {
		p.state.Option.UnavailableReason = "ORIGINAL_PLANNER_UNAVAILABLE"
		return p, nil
	}
	p.state.Binding = core.PlannerCoordinationRecoveryBinding{RequirementID: id, ExecutionRunID: run.ID, EventID: eventID, LogicalStepID: step.ID, RoleBindingID: role.ID, AOSessionID: request.AoSessionID,
		ProviderConversationID: session.ProviderConversationID, WorkspacePath: session.WorkspacePath, SessionCreationKey: session.CreationIdempotencyKey, CreationFingerprint: session.CreationRequestFingerprint,
		Harness: string(session.Harness), Model: session.Model, PromptSHA256: step.PromptSha256, ClientMessageID: step.ClientMessageID, ContextSHA256: request.ContextSha256}
	first, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{LogicalStepID: step.ID, AttemptNumber: 1})
	if err != nil {
		return p, missingBuilderProof(err)
	}
	p.first = first
	p.state.Binding.FirstAttemptID = first.ID
	if first.DevelopmentProjectID != id || first.StepCategory != "COMPLEX_PLANNING" || first.StepKind != step.StepKind || first.RoleBindingID != role.ID || first.AoSessionID != request.AoSessionID || first.PromptSha256 != step.PromptSha256 || first.ClientMessageID != step.ClientMessageID {
		return p, nil
	}
	if !sending {
		if _, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{LogicalStepID: step.ID, AttemptNumber: 2}); err == nil {
			p.state.Option.UnavailableReason = string(core.ReasonMessageBudgetExhausted)
			return p, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return p, err
		}
	}
	failure, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, first.ID)
	if err != nil {
		return p, missingBuilderProof(err)
	}
	p.state.Binding.FailureEventID = failure.ID
	if (failure.Status != "FAILED" && failure.Status != "INTERRUPTED") || (failure.TurnState != "failed" && failure.TurnState != "interrupted") || !planningFailureCanContinue(failure.FailureCategory) || failure.FailureCategory == "RESULT_INVALID" || (failure.ClientMessageID != "" && failure.ClientMessageID != first.ClientMessageID) || (failure.PromptSha256 != "" && failure.PromptSha256 != first.PromptSha256) {
		return p, nil
	}
	if at.Before(failure.RecordedAt) || failure.RetryAt.Valid && at.Before(failure.RetryAt.Time) {
		p.state.Option.UnavailableReason = "RETRY_NOT_DUE"
		return p, nil
	}
	budget, err := q.GetClearDevMessageBudgetVersion(ctx, id)
	if err != nil {
		return p, missingBuilderProof(err)
	}
	if budget.Version != string(core.MessageBudgetV1) {
		p.state.Option.UnavailableReason = string(core.ReasonMessageBudgetUnknown)
		return p, nil
	}
	reservations, err := q.ListClearDevStepMessageReservations(ctx, step.ID)
	if err != nil {
		return p, err
	}
	if len(reservations) != 1 {
		p.state.Option.UnavailableReason = string(core.ReasonMessageBudgetExhausted)
		return p, nil
	}
	r := reservations[0]
	if r.ClientMessageID != first.ClientMessageID || r.AttemptID != first.ID || r.DevelopmentProjectID != id || r.LogicalStepID != step.ID || r.AoSessionID != first.AoSessionID || r.Source != "ORIGINAL" || r.PromptSha256 != first.PromptSha256 || r.BudgetID.Valid || r.BudgetVersion != budget.Version {
		return p, nil
	}
	if _, err := q.GetClearDevParseCorrection(ctx, step.ID); err == nil {
		return p, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	if _, err := q.GetClearDevAgentStepResult(ctx, gen.GetClearDevAgentStepResultParams{AttemptID: first.ID, ResultIndex: 1}); err == nil {
		return p, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	confirmation, err := q.GetClearDevAgentMessageConfirmation(ctx, first.ClientMessageID)
	if err != nil {
		return p, missingBuilderProof(err)
	}
	if confirmation.TurnID == "" || failure.TurnID != "" && failure.TurnID != confirmation.TurnID {
		return p, nil
	}
	if _, err := q.GetClearDevBoundAgentSendEvent(ctx, gen.GetClearDevBoundAgentSendEventParams{AttemptID: first.ID, ClientMessageID: first.ClientMessageID, TurnID: confirmation.TurnID, PromptSha256: first.PromptSha256, Status: "SENT"}); err != nil {
		return p, missingBuilderProof(err)
	}
	native, err := q.GetClearDevPlannerRecoveryNativeMessage(ctx, gen.GetClearDevPlannerRecoveryNativeMessageParams{ClientMessageID: first.ClientMessageID, ID: confirmation.TurnID, HandledBySessionID: session.ID})
	if err != nil {
		return p, missingBuilderProof(err)
	}
	if complexExecutionRawDigest([]byte(native.Text)) != step.PromptSha256 {
		return p, nil
	}
	// A protocol-completed turn without an assistant reply is a known ended
	// request, not a successful Planner result. Require its original positive
	// terminal event from before ClearDev recorded the failure. Replayed
	// history alone and absence of a reply cannot authorize this path.
	if native.NativeState == domain.TurnStateCompleted && (!native.TerminalEventID.Valid || !native.TerminalReceivedAt.Valid || native.TerminalReceivedAt.Time.After(failure.RecordedAt) || native.TerminalReceivedAt.Time.Before(native.RequestedAt)) {
		return p, nil
	}
	p.state.Binding.NativeTurnID = native.TurnID
	p.state.Binding.NativeTurnState = string(native.NativeState)
	p.state.Binding.TerminalEventID = native.TerminalEventID.Int64
	p.state.Messages = []core.PlanningStepRecoveryMessage{{ClientMessageID: first.ClientMessageID, PromptSHA256: first.PromptSha256, TurnID: confirmation.TurnID}}
	active, err := q.CountClearDevUnsettledSessionTurns(ctx, session.ID)
	if err != nil {
		return p, err
	}
	if active != 0 || session.ActivityState != domain.ActivityIdle && session.ActivityState != domain.ActivityExited {
		return p, nil
	}
	eventRow, err := q.GetClearDevPlannerRuntimeEvent(ctx, eventID)
	if err != nil {
		return p, err
	}
	event, err := plannerRuntimeEventFromGen(eventRow)
	if err != nil {
		return p, err
	}
	contextJSON, err := plannerRuntimeContext(ctx, q, run, event)
	if err != nil {
		return p, err
	}
	if string(contextJSON) != request.ContextJson || complexExecutionRawDigest(contextJSON) != request.ContextSha256 {
		p.state.Option.UnavailableReason = "PLANNER_COORDINATION_CONTEXT_CHANGED"
		return p, nil
	}
	activeExecution, err := q.CountClearDevPlannerRuntimeActiveAttempts(ctx, run.ID)
	if err != nil {
		return p, err
	}
	if activeExecution != 0 {
		return p, nil
	}
	p.state.Option.UnavailableReason = ""
	p.state.Option.TargetID, err = core.PlannerCoordinationRecoveryTarget(p.state.Binding)
	return p, err
}

// ApplyClearDevPlannerCoordinationRecovery atomically claims the second attempt
// and reopens the compatibility step, preserving its exact prior row as evidence.
func (s *Store) ApplyClearDevPlannerCoordinationRecovery(ctx context.Context, requestID, supplement string, expected core.PlannerCoordinationRecoveryBinding, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "recover original Planner coordination", func(q *gen.Queries) error {
		if old, err := q.GetClearDevPlannerCoordinationRecovery(ctx, requestID); err == nil {
			var b core.PlannerCoordinationRecoveryBinding
			if json.Unmarshal([]byte(old.BindingJson), &b) != nil || b != expected || old.Supplement != supplement {
				return productConflict("coordination recovery request changed")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p, err := plannerCoordinationRecoveryEvidence(ctx, q, expected.RequirementID, expected.EventID, at, false)
		if err != nil {
			return err
		}
		if p.state.Option.Action == "" || p.state.Option.UnavailableReason != "" || p.state.Binding != expected {
			return productConflict("coordination recovery proof changed")
		}
		raw, err := core.CanonicalJSONBytes(expected)
		if err != nil {
			return err
		}
		old, err := json.Marshal(p.step)
		if err != nil {
			return err
		}
		second := p.step.ID + ":attempt:2"
		if err := q.InsertClearDevPlannerCoordinationRecovery(ctx, gen.InsertClearDevPlannerCoordinationRecoveryParams{ID: requestID, EventID: expected.EventID, RequirementID: expected.RequirementID, ExecutionRunID: expected.ExecutionRunID,
			LogicalStepID: p.step.ID, FirstAttemptID: p.first.ID, FailureEventID: expected.FailureEventID, SecondAttemptID: second, BindingJson: string(raw), OldStepJson: string(old), OriginalStoppedAt: p.stop.CreatedAt, OriginalSummary: p.stop.Summary, Supplement: supplement, CreatedAt: at}); err != nil {
			return err
		}
		n, err := q.InsertClearDevAgentStepAttempt(ctx, gen.InsertClearDevAgentStepAttemptParams{ID: second, DevelopmentProjectID: expected.RequirementID, LogicalStepID: p.step.ID, StepCategory: p.first.StepCategory, StepKind: p.first.StepKind, AttemptNumber: 2,
			RoleBindingID: expected.RoleBindingID, AoSessionID: expected.AOSessionID, ClientMessageID: expected.ClientMessageID + ":attempt:2", PromptSha256: expected.PromptSHA256, TriggerFailureEventID: nullableString(expected.FailureEventID), RequestedAt: at, CreatedAt: nullableTime(at), RequestedAtSemantics: string(core.AttemptTimeActualCreation)})
		if err != nil {
			return err
		}
		if n != 1 {
			return productConflict("coordination second attempt is already claimed")
		}
		n, err = q.ReopenClearDevPlannerCoordinationStep(ctx, gen.ReopenClearDevPlannerCoordinationStepParams{ID: p.step.ID, OriginalStatus: p.step.SendStatus, OriginalReason: p.step.ReasonCode})
		if err != nil {
			return err
		}
		if n != 1 {
			return productConflict("stopped Planner step changed")
		}
		return nil
	})
}

// ReadClearDevPlannerCoordinationRecoveryBeforeSend rechecks an unsent retry
// against its original frozen request and terminal native-message evidence.
func (s *Store) ReadClearDevPlannerCoordinationRecoveryBeforeSend(ctx context.Context, id, stepID string, at time.Time) (core.PlannerCoordinationRecoveryState, bool, error) {
	r, err := s.qr.GetClearDevPlannerCoordinationRecoveryForStep(ctx, stepID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.PlannerCoordinationRecoveryState{}, false, nil
	}
	if err != nil {
		return core.PlannerCoordinationRecoveryState{}, false, err
	}
	var b core.PlannerCoordinationRecoveryBinding
	if json.Unmarshal([]byte(r.BindingJson), &b) != nil || r.RequirementID != id {
		return core.PlannerCoordinationRecoveryState{}, true, productConflict("invalid coordination recovery binding")
	}
	p, err := plannerCoordinationRecoveryEvidence(ctx, s.qr, id, r.EventID, at, true)
	if err != nil {
		return p.state, true, err
	}
	if p.state.Option.Action == "" || p.state.Option.UnavailableReason != "" || p.state.Binding != b {
		return p.state, true, productConflict("coordination recovery is no longer current")
	}
	return p.state, true, nil
}
