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

type planningContinuationProof struct {
	state   core.PlanningStepRecoveryState
	step    gen.CleardevComplexAgentStep
	first   gen.CleardevAgentStepAttempt
	failure gen.CleardevAgentAttemptEvent
}

// ReadClearDevPlanningStepRecovery is a read-only projection. Eligibility is
// repeated within ApplyClearDevPlanningStepRecovery and before a new send.
func (s *Store) ReadClearDevPlanningStepRecovery(ctx context.Context, id string, at time.Time) (core.PlanningStepRecoveryState, error) {
	proof, err := planningContinuationEvidence(ctx, s.qr, id, at, false)
	if err != nil {
		return core.PlanningStepRecoveryState{}, err
	}
	rows, err := s.qr.ListClearDevPlanningStepRecoveries(ctx, id)
	if err != nil {
		return core.PlanningStepRecoveryState{}, err
	}
	proof.state.History = []core.WorkflowRecovery{}
	for _, row := range rows {
		item, err := planningContinuationHistory(row)
		if err != nil {
			return core.PlanningStepRecoveryState{}, err
		}
		proof.state.History = append(proof.state.History, item)
	}
	return proof.state, nil
}

func planningContinuationHistory(row gen.CleardevPlanningStepRecovery) (core.WorkflowRecovery, error) {
	var b core.PlanningStepRecoveryBinding
	if err := json.Unmarshal([]byte(row.BindingJson), &b); err != nil {
		return core.WorkflowRecovery{}, err
	}
	target, err := core.PlanningStepRecoveryTarget(b)
	if err != nil {
		return core.WorkflowRecovery{}, err
	}
	return core.WorkflowRecovery{ID: row.ID, Action: core.RecoveryRetryPlanningStep, TargetID: target,
		StepID: row.LogicalStepID, BindingID: b.RoleBindingID, SuccessorID: row.SecondAttemptID,
		ProviderConversationID: b.ProviderConversationID, OriginalStatus: row.OriginalStatus,
		OriginalReason: row.OriginalReason, OriginalSummary: row.OriginalSummary,
		OriginalStoppedAt: row.OriginalStoppedAt, Supplement: row.Supplement, CreatedAt: row.CreatedAt}, nil
}

func planningContinuationEvidence(ctx context.Context, q *gen.Queries, id string, at time.Time, sending bool) (planningContinuationProof, error) {
	var p planningContinuationProof
	step, err := q.GetClearDevPlanningContinuationStep(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.step = step
	if step.SendStatus == "SETTLED" {
		return p, nil
	}
	p.state.Option = core.WorkflowRecoveryOption{Action: core.RecoveryRetryPlanningStep, TargetID: step.ID, Role: "STEWARD", Reason: step.ReasonCode, UnavailableReason: "RESULT_NOT_SETTLED"}
	b, selection, current, err := planningContinuationSource(ctx, q, id, step, true)
	p.state.Binding, p.state.Selection = b, selection
	if err != nil {
		return p, err
	}
	if !current {
		p.state.Option.UnavailableReason = "PLANNING_NOT_CURRENT"
		return p, nil
	}
	budget, err := q.GetClearDevMessageBudgetVersion(ctx, id)
	if err != nil || budget.Version != string(core.MessageBudgetV1) {
		p.state.Option.UnavailableReason = string(core.ReasonMessageBudgetUnknown)
		return p, missingBuilderProof(err)
	}
	attempts, err := q.ListClearDevAgentStepAttemptStates(ctx, gen.ListClearDevAgentStepAttemptStatesParams{DevelopmentProjectID: id, LogicalStepID: step.ID})
	if err != nil {
		return p, err
	}
	if len(attempts) == 0 {
		p.state.Option.UnavailableReason = "PREFLIGHT_OR_DELIVERY_REQUIRED"
		return p, nil
	}
	if !sending && len(attempts) >= 2 {
		p.state.Option.UnavailableReason = string(core.ReasonMessageBudgetExhausted)
		if step.SendStatus == "PENDING" {
			if _, err := q.GetClearDevPlanningStepRecoveryForStep(ctx, step.ID); err == nil {
				if _, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, attempts[1].ID); errors.Is(err, sql.ErrNoRows) {
					p.state.Option.UnavailableReason = "CONTINUATION_REGISTERED"
				} else if err != nil {
					return p, err
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return p, err
			}
		}
		return p, nil
	}
	first := attempts[0]
	p.first = first
	p.state.Binding.FirstAttemptID = first.ID
	if first.AttemptNumber != 1 || first.StepCategory != string(core.AgentStepCategoryComplexPlanning) || first.StepKind != string(core.ComplexAgentStepCompilation) ||
		first.RoleBindingID != b.RoleBindingID || first.AoSessionID != b.AOSessionID || first.ClientMessageID != step.ClientMessageID || first.PromptSha256 != step.PromptSha256 {
		return p, nil
	}
	failure, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, first.ID)
	if err != nil {
		return p, missingBuilderProof(err)
	}
	p.failure, p.state.Binding.FailureEventID = failure, failure.ID
	p.state.Option.Summary = failure.ErrorSummary
	if (failure.Status != "FAILED" && failure.Status != "INTERRUPTED") || (failure.TurnState != "failed" && failure.TurnState != "interrupted") || !planningFailureCanContinue(failure.FailureCategory) {
		return p, nil
	}
	if failure.RetryAt.Valid && at.Before(failure.RetryAt.Time) {
		p.state.Option.UnavailableReason = "RETRY_NOT_DUE"
		return p, nil
	}
	messages := []invalidBuilderMessage{{attempt: first, id: first.ClientMessageID, prompt: first.PromptSha256, source: string(core.AgentMessageOriginal), resultKind: string(core.AgentResultOriginal), result: 1, sendStatus: "SENT"}}
	correction, correctionErr := q.GetClearDevParseCorrection(ctx, step.ID)
	if correctionErr != nil && !errors.Is(correctionErr, sql.ErrNoRows) {
		return p, correctionErr
	}
	if correctionErr == nil && correction.AttemptNumber == 1 {
		if correction.ClientMessageID != step.ClientMessageID+":parse-correction" || correction.PromptSha256 != complexExecutionRawDigest([]byte(correction.PromptText)) {
			return p, nil
		}
		messages = append(messages, invalidBuilderMessage{attempt: first, id: first.ClientMessageID + ":parse-correction", prompt: correction.PromptSha256, source: string(core.AgentMessageParseCorrection), resultKind: string(core.AgentResultCorrection), result: 2, sendStatus: "CORRECTION_SENT"})
	}
	if failure.ClientMessageID != messages[len(messages)-1].id || failure.FailureCategory == "RESULT_INVALID" && len(messages) != 2 {
		return p, nil
	}
	reservations, err := q.ListClearDevStepMessageReservations(ctx, step.ID)
	if err != nil {
		return p, err
	}
	if !sending && len(reservations) >= 3 {
		p.state.Option.UnavailableReason = string(core.ReasonMessageBudgetExhausted)
		return p, nil
	}
	firstCount := 0
	for _, item := range reservations {
		if item.AttemptID == first.ID {
			firstCount++
		}
	}
	if firstCount != len(messages) {
		return p, nil
	}
	for _, message := range messages {
		r, err := q.GetClearDevAgentMessageReservation(ctx, message.id)
		if err != nil {
			return p, missingBuilderProof(err)
		}
		if r.DevelopmentProjectID != id || r.LogicalStepID != step.ID || r.AttemptID != first.ID || r.AoSessionID != first.AoSessionID ||
			r.BudgetVersion != string(core.MessageBudgetV1) || r.BudgetID.Valid || r.PromptSha256 != message.prompt || r.Source != message.source {
			return p, nil
		}
		// This shared per-message proof also protects Builder recovery: a later
		// correction result must never conceal an earlier unknown delivery.
		_, ended, err := invalidBuilderMessageEnded(ctx, q, message)
		if err != nil || !ended {
			return p, err
		}
		confirmation, err := q.GetClearDevAgentMessageConfirmation(ctx, message.id)
		if err != nil {
			return p, missingBuilderProof(err)
		}
		p.state.Messages = append(p.state.Messages, core.PlanningStepRecoveryMessage{ClientMessageID: message.id, PromptSHA256: message.prompt, TurnID: confirmation.TurnID})
	}
	session, err := q.GetSession(ctx, domain.SessionID(b.AOSessionID))
	if err != nil {
		return p, err
	}
	if session.ActivityState != domain.ActivityIdle && session.ActivityState != domain.ActivityExited {
		return p, nil
	}
	active, err := q.CountClearDevUnsettledSessionTurns(ctx, domain.SessionID(b.AOSessionID))
	if err != nil || active != 0 {
		return p, err
	}
	if !sending && step.SendStatus != "SENT" && step.SendStatus != "FAILED" {
		return p, nil
	}
	p.state.Option.TargetID, err = core.PlanningStepRecoveryTarget(p.state.Binding)
	if err != nil {
		return p, err
	}
	p.state.Option.UnavailableReason = ""
	return p, nil
}

func planningFailureCanContinue(category string) bool {
	switch category {
	case "PROVIDER_UNAVAILABLE", "PROVIDER_FAILURE", "AUTHENTICATION_REQUIRED", "QUOTA_EXHAUSTED", "RATE_LIMITED", "MODEL_UNAVAILABLE", "RESULT_INVALID":
		return true
	default:
		// An interrupted or lost session alone cannot prove a provider failure
		// rather than a user's cancellation or an unobserved external action.
		return false
	}
}

func planningContinuationSource(ctx context.Context, q *gen.Queries, id string, step gen.CleardevComplexAgentStep, allowInvalid ...bool) (core.PlanningStepRecoveryBinding, *core.ProductSelection, bool, error) {
	b := core.PlanningStepRecoveryBinding{RequirementID: id, LogicalStepID: step.ID, StepRequestID: step.RequestID, RoleBindingID: step.RoleBindingID, PromptSHA256: step.PromptSha256, ClientMessageID: step.ClientMessageID}
	requirement, err := q.GetClearDevRequirement(ctx, id)
	if err != nil {
		return b, nil, false, err
	}
	planning, err := q.GetClearDevComplexRequirement(ctx, id)
	if err != nil {
		return b, nil, false, err
	}
	b.TargetVersionID = planning.TargetRequirementVersionID
	if requirement.CancelledAt.Valid || requirement.PausedFromState.Valid {
		return b, nil, false, nil
	}
	conflicts, err := q.CountClearDevPlanningContinuationConflicts(ctx, id)
	if err != nil || conflicts != 0 {
		return b, nil, false, err
	}
	stage, stageErr := q.GetClearDevProductStageByRequirement(ctx, nullableString(id))
	if stageErr == nil {
		b.ProductID, b.DiscussionID, b.StageID, b.StageDefinitionSHA256 = stage.ProductID, stage.DiscussionID, stage.ID, stage.DefinitionSha256
		if stage.DefinitionSha256 != complexExecutionRawDigest([]byte(stage.DefinitionJson)) {
			return b, nil, false, nil
		}
		stageValue, err := productStageFromRow(stage)
		if err != nil {
			return b, nil, false, err
		}
		if err := requireProductPredecessor(ctx, q, stageValue, stageValue.BaseCommitSHA); err != nil {
			return b, nil, false, err
		}
		if _, err := q.GetClearDevComplexCompilationRequest(ctx, step.RequestID); err == nil {
			return b, nil, false, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return b, nil, false, err
		}
	} else if errors.Is(stageErr, sql.ErrNoRows) {
		if _, err := q.GetClearDevProductGoal(ctx, id); err != nil {
			return b, nil, false, missingBuilderProof(err)
		}
		b.ProductID, b.DiscussionID = id, step.RequestID
	} else {
		return b, nil, false, stageErr
	}
	parent, err := q.GetClearDevRequirement(ctx, b.ProductID)
	if err != nil || parent.CancelledAt.Valid || parent.PausedFromState.Valid {
		return b, nil, false, err
	}
	rounds, err := q.ListClearDevProductDiscussions(ctx, b.ProductID)
	if err != nil || len(rounds) == 0 || rounds[len(rounds)-1].ID != b.DiscussionID {
		return b, nil, false, err
	}
	discussion := rounds[len(rounds)-1]
	if b.StageID == "" && (discussion.SettledAt.Valid || discussion.ResultJson.Valid || discussion.FailureReason.Valid) {
		// Only the existing first-to-second continuation may inspect this
		// terminal failure. Extra-attempt callers retain their old boundary.
		invalidContinuation := len(allowInvalid) == 1 && allowInvalid[0] && discussion.FailureReason.String == "PRODUCT_DISCOVERY_INVALID" && discussion.SettledAt.Valid && !discussion.ResultJson.Valid && step.SendStatus == "FAILED" && step.ReasonCode == "PRODUCT_DISCOVERY_INVALID"
		if !invalidContinuation {
			return b, nil, false, nil
		}
	}
	active, err := q.HasClearDevProductExecution(ctx, b.ProductID)
	if err != nil || active {
		return b, nil, false, err
	}
	_, selection, err := loadProductContext(ctx, q, discussion.ID)
	if err != nil {
		return b, nil, false, err
	}
	if selection != nil {
		raw, err := core.CanonicalJSONBytes(*selection)
		if err != nil {
			return b, nil, false, err
		}
		b.SelectionSHA256 = complexExecutionRawDigest(raw)
		if err := validateProjectDeliverySelection(ctx, q, b.ProductID, *selection); err != nil {
			return b, selection, false, err
		}
	}
	role, err := q.GetClearDevComplexRoleBinding(ctx, step.RoleBindingID)
	if err != nil {
		return b, selection, false, err
	}
	if role.DevelopmentProjectID != id || role.Role != "STEWARD" || role.Status != "BOUND" || role.AoSessionID.String == "" {
		return b, selection, false, nil
	}
	session, err := q.GetSession(ctx, domain.SessionID(role.AoSessionID.String))
	if err != nil {
		return b, selection, false, missingBuilderProof(err)
	}
	b.AOSessionID, b.ProviderConversationID, b.WorkspacePath, b.SessionCreationKey = role.AoSessionID.String, session.ProviderConversationID, session.WorkspacePath, role.SessionCreationIdempotencyKey
	b.Harness, b.Model = string(session.Harness), session.Model
	if session.IsTerminated || session.ProviderConversationID == "" || string(session.ProjectID) != requirement.AoProjectID ||
		session.CreationIdempotencyKey != role.SessionCreationIdempotencyKey || session.WorkspacePath == "" || session.SessionMode != domain.SessionModeChat {
		return b, selection, false, nil
	}
	matchingTool, err := q.IsClearDevPlanningContinuationSession(ctx, b.AOSessionID)
	return b, selection, matchingTool, err
}

// ApplyClearDevPlanningStepRecovery records the exact old compatibility row,
// claims attempt 2 and reopens only this compilation step in one transaction.
func (s *Store) ApplyClearDevPlanningStepRecovery(ctx context.Context, requestID, supplement string, expected core.PlanningStepRecoveryBinding, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "continue original product planning step", func(q *gen.Queries) error {
		if prior, err := q.GetClearDevPlanningStepRecovery(ctx, requestID); err == nil {
			var b core.PlanningStepRecoveryBinding
			if json.Unmarshal([]byte(prior.BindingJson), &b) != nil || b != expected || prior.Supplement != supplement {
				return productConflict("planning recovery request is bound to different facts")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		proof, err := planningContinuationEvidence(ctx, q, expected.RequirementID, at, false)
		if err != nil {
			return err
		}
		if proof.state.Option.Action == "" || proof.state.Option.UnavailableReason != "" || proof.state.Binding != expected {
			return productConflict("planning recovery evidence or source changed")
		}
		binding, err := core.CanonicalJSONBytes(expected)
		if err != nil {
			return err
		}
		old, err := json.Marshal(proof.step)
		if err != nil {
			return err
		}
		var oldDiscussion sql.NullString
		if expected.StageID == "" && proof.step.ReasonCode == "PRODUCT_DISCOVERY_INVALID" {
			snapshot, err := q.GetClearDevFailedDiscussionSnapshot(ctx, expected.DiscussionID)
			if err != nil {
				return err
			}
			oldDiscussion = nullableString(snapshot)
		}
		second := proof.step.ID + ":attempt:2"
		if err := q.InsertClearDevPlanningStepRecovery(ctx, gen.InsertClearDevPlanningStepRecoveryParams{ID: requestID, RequirementID: expected.RequirementID,
			LogicalStepID: proof.step.ID, FirstAttemptID: proof.first.ID, FailureEventID: proof.failure.ID, SecondAttemptID: second,
			BindingJson: string(binding), OldStepJson: string(old), OldDiscussionJson: oldDiscussion, OriginalStatus: proof.step.SendStatus, OriginalReason: proof.step.ReasonCode,
			OriginalSummary: proof.state.Option.Summary, OriginalStoppedAt: proof.failure.RecordedAt, Supplement: supplement, CreatedAt: at}); err != nil {
			return err
		}
		_, err = q.InsertClearDevAgentStepAttempt(ctx, gen.InsertClearDevAgentStepAttemptParams{ID: second, DevelopmentProjectID: expected.RequirementID,
			LogicalStepID: proof.step.ID, StepCategory: proof.first.StepCategory, StepKind: proof.first.StepKind, AttemptNumber: 2,
			RoleBindingID: expected.RoleBindingID, AoSessionID: expected.AOSessionID, ClientMessageID: expected.ClientMessageID + ":attempt:2", PromptSha256: expected.PromptSHA256,
			TriggerFailureEventID: nullableString(expected.FailureEventID), RequestedAt: at, CreatedAt: nullableTime(at), RequestedAtSemantics: string(core.AttemptTimeActualCreation)})
		if err != nil {
			return err
		}
		rows, err := q.ReopenClearDevStoppedProductStep(ctx, gen.ReopenClearDevStoppedProductStepParams{ID: proof.step.ID, OriginalStatus: proof.step.SendStatus, OriginalReason: proof.step.ReasonCode})
		if err != nil {
			return err
		}
		if rows != 1 {
			return productConflict("planning step changed before continuation")
		}
		if oldDiscussion.Valid {
			rows, err := q.ReopenClearDevFailedDiscussion(ctx, expected.DiscussionID)
			if err != nil {
				return err
			}
			if rows != 1 {
				return productConflict("discussion changed before its bounded continuation")
			}
		}
		return nil
	})
}

// ReadClearDevPlanningRecoveryBeforeSend validates an existing explicit retry.
// Absence preserves the original automatic-retry contract for other steps.
func (s *Store) ReadClearDevPlanningRecoveryBeforeSend(ctx context.Context, id, stepID string, at time.Time) (core.PlanningStepRecoveryState, bool, error) {
	return planningRecoveryBeforeSend(ctx, s.qr, id, stepID, at)
}

func planningRecoveryBeforeSend(ctx context.Context, q *gen.Queries, id, stepID string, at time.Time) (core.PlanningStepRecoveryState, bool, error) {
	row, err := q.GetClearDevPlanningStepRecoveryForStep(ctx, stepID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.PlanningStepRecoveryState{}, false, nil
	}
	if err != nil {
		return core.PlanningStepRecoveryState{}, false, err
	}
	var expected core.PlanningStepRecoveryBinding
	if err := json.Unmarshal([]byte(row.BindingJson), &expected); err != nil || row.RequirementID != id {
		return core.PlanningStepRecoveryState{}, true, productConflict("planning retry binding is invalid")
	}
	proof, err := planningContinuationEvidence(ctx, q, id, at, true)
	if err != nil {
		return proof.state, true, err
	}
	if proof.state.Option.Action == "" || proof.state.Option.UnavailableReason != "" || proof.state.Binding != expected {
		return proof.state, true, productConflict("planning retry is no longer current or fully settled")
	}
	return proof.state, true, nil
}
