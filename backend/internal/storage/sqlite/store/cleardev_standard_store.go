package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// GetClearDevStandardFlow returns the raw S02 facts for the requirement version
// which owns the persisted Steward binding. A later confirmed version must not
// hide, replace, or accidentally resume the older immutable flow.
func (s *Store) GetClearDevStandardFlow(ctx context.Context, requirementID string) (cleardev.StandardFlowSnapshot, bool, error) {
	requirementRow, err := s.qr.GetClearDevRequirement(ctx, requirementID)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.StandardFlowSnapshot{}, false, nil
	}
	if err != nil {
		return cleardev.StandardFlowSnapshot{}, false, err
	}
	flowVersion, err := s.qr.GetClearDevStandardFlowRequirementVersion(ctx, requirementID)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.StandardFlowSnapshot{}, false, nil
	}
	if err != nil {
		return cleardev.StandardFlowSnapshot{}, false, err
	}
	flow := cleardev.StandardFlowSnapshot{
		DevelopmentRequirementID: requirementRow.ID,
		RequirementVersionID:     flowVersion.ID,
		RoleBindings:             []cleardev.RoleSessionBinding{},
		AgentSteps:               []cleardev.AgentStep{},
		EngineeringPlans:         []cleardev.EngineeringPlan{},
		PlanReviews:              []cleardev.PlanReview{},
		Dispatches:               []cleardev.Dispatch{},
		CandidateCheckRuns:       []cleardev.CandidateCheckRun{},
		LocalReviews:             []cleardev.LocalReview{},
	}
	bindings, err := s.qr.ListClearDevStandardRoleBindings(ctx, flowVersion.ID)
	if err != nil {
		return cleardev.StandardFlowSnapshot{}, false, err
	}
	for _, row := range bindings {
		binding := clearDevStandardRoleBindingFromGen(row)
		flow.RoleBindings = append(flow.RoleBindings, binding)
		steps, listErr := s.qr.ListClearDevStandardAgentSteps(ctx, binding.ID)
		if listErr != nil {
			return cleardev.StandardFlowSnapshot{}, false, listErr
		}
		for _, step := range steps {
			flow.AgentSteps = append(flow.AgentSteps, clearDevStandardAgentStepFromGen(step))
		}
	}
	plans, err := s.qr.ListClearDevStandardEngineeringPlans(ctx, flowVersion.ID)
	if err != nil {
		return cleardev.StandardFlowSnapshot{}, false, err
	}
	for _, row := range plans {
		flow.EngineeringPlans = append(flow.EngineeringPlans, clearDevStandardEngineeringPlanFromGen(row))
	}
	reviews, err := s.qr.ListClearDevStandardPlanReviews(ctx, flowVersion.ID)
	if err != nil {
		return cleardev.StandardFlowSnapshot{}, false, err
	}
	for _, row := range reviews {
		flow.PlanReviews = append(flow.PlanReviews, clearDevStandardPlanReviewFromGen(row))
	}
	dispatches, err := s.qr.ListClearDevStandardDispatches(ctx, flowVersion.ID)
	if err != nil {
		return cleardev.StandardFlowSnapshot{}, false, err
	}
	for _, row := range dispatches {
		flow.Dispatches = append(flow.Dispatches, clearDevStandardDispatchFromGen(row))
	}
	runs, err := s.qr.ListClearDevCandidateCheckRuns(ctx, flowVersion.ID)
	if err != nil {
		return cleardev.StandardFlowSnapshot{}, false, err
	}
	for _, row := range runs {
		flow.CandidateCheckRuns = append(flow.CandidateCheckRuns, clearDevCandidateCheckRunFromGen(row))
	}
	localReviews, err := s.qr.ListClearDevStandardLocalReviews(ctx, flowVersion.ID)
	if err != nil {
		return cleardev.StandardFlowSnapshot{}, false, err
	}
	for _, row := range localReviews {
		flow.LocalReviews = append(flow.LocalReviews, clearDevStandardLocalReviewFromGen(row))
	}
	return flow, true, nil
}

// ListClearDevRunnableStandardFlows returns every non-cancelled requirement
// with a persisted STANDARD Steward fact. Restart can
// happen in between any two durable writes, so filtering only for an obvious
// pending step would silently strand a settled-but-not-yet-advanced flow. The
// coordinator derives the phase and makes already-terminal flows a no-op.
func (s *Store) ListClearDevRunnableStandardFlows(ctx context.Context) ([]string, error) {
	return s.qr.ListClearDevRunnableStandardFlows(ctx)
}

// StartClearDevStandardFlow atomically creates exactly one REQUESTED steward
// binding for the current confirmed requirement version. A duplicate command
// with the same stable spawn key is an idempotent read; another key is refused.
func (s *Store) StartClearDevStandardFlow(ctx context.Context, command cleardev.StartStandardFlowCommand) (cleardev.RoleSessionBinding, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.RoleSessionBinding
	var created bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "start ClearDev STANDARD flow", func(q *gen.Queries) error {
		requirementRow, err := q.GetClearDevRequirement(ctx, command.DevelopmentRequirementID)
		if errors.Is(err, sql.ErrNoRows) {
			rejected = standardRejected(command.DevelopmentRequirementID, cleardev.ReasonPreconditionNotMet, "development requirement does not exist")
			return nil
		}
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		if _, complexErr := q.GetClearDevComplexRequirement(ctx, command.DevelopmentRequirementID); complexErr == nil {
			rejected = standardRejected(command.DevelopmentRequirementID, cleardev.ReasonComplexPlanRequired, "complex requirements cannot start a STANDARD flow")
			return nil
		} else if !errors.Is(complexErr, sql.ErrNoRows) {
			return complexErr
		}
		if confirmed, confirmedErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, command.DevelopmentRequirementID); confirmedErr == nil {
			if stopped, stopErr := activeDirectionStop(ctx, q, confirmed.ID); stopErr != nil {
				return stopErr
			} else if stopped {
				rejected = directionStoppedError()
				return nil
			}
		} else if !errors.Is(confirmedErr, sql.ErrNoRows) {
			return confirmedErr
		}
		if requirement.CancelledAt != nil || strings.TrimSpace(command.StewardRoleBindingID) == "" || strings.TrimSpace(command.StewardSessionIdempotencyKey) == "" {
			rejected = standardRejected(command.DevelopmentRequirementID, cleardev.ReasonPreconditionNotMet, "invalid STANDARD flow start")
			return nil
		}
		flowVersion, flowErr := q.GetClearDevStandardFlowRequirementVersion(ctx, command.DevelopmentRequirementID)
		if flowErr == nil {
			bindings, listErr := q.ListClearDevStandardRoleBindings(ctx, flowVersion.ID)
			if listErr != nil {
				return listErr
			}
			for _, row := range bindings {
				binding := clearDevStandardRoleBindingFromGen(row)
				if binding.Role != cleardev.StandardRoleSteward {
					continue
				}
				if binding.ID == command.StewardRoleBindingID && binding.SessionCreationIdempotencyKey == command.StewardSessionIdempotencyKey {
					result = binding
					return nil
				}
				rejected = standardRejected(command.DevelopmentRequirementID, cleardev.ReasonStandardFlowExists, "a STANDARD flow already owns this confirmed requirement")
				return nil
			}
			return fmt.Errorf("STANDARD flow version %s has no Steward binding", flowVersion.ID)
		}
		if !errors.Is(flowErr, sql.ErrNoRows) {
			return flowErr
		}
		confirmed, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, command.DevelopmentRequirementID)
		if errors.Is(err, sql.ErrNoRows) {
			rejected = standardRejected(command.DevelopmentRequirementID, cleardev.ReasonPreconditionNotMet, "development requirement has no current confirmed version")
			return nil
		}
		if err != nil {
			return err
		}
		result = cleardev.RoleSessionBinding{
			ID: command.StewardRoleBindingID, DevelopmentRequirementID: requirement.ID,
			RequirementVersionID: confirmed.ID, Role: cleardev.StandardRoleSteward,
			SessionCreationIdempotencyKey: command.StewardSessionIdempotencyKey,
			Status:                        cleardev.RoleBindingStatusRequested, RequestedAt: command.At,
		}
		if err := insertClearDevStandardRoleBinding(ctx, q, result); err != nil {
			return err
		}
		if err := insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
			AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
			SubjectType: cleardev.SubjectStandardRoleBinding, SubjectID: result.ID,
			Action: cleardev.ActionStartStandardFlow, Outcome: cleardev.EventAccepted,
			Source: cleardev.EventSourceControlPlane, CreatedAt: command.At,
		}); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return cleardev.RoleSessionBinding{}, false, err
	}
	if rejected != nil {
		return cleardev.RoleSessionBinding{}, false, rejected
	}
	return result, created, nil
}

// CreateClearDevRoleBinding creates one pending Planner or Reviewer role
// binding. It is idempotent only when every identity field agrees.
func (s *Store) CreateClearDevRoleBinding(ctx context.Context, command cleardev.CreateRoleBindingCommand) (cleardev.RoleSessionBinding, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.RoleSessionBinding
	var created bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev STANDARD role binding", func(q *gen.Queries) error {
		binding := command.Binding
		requirement, confirmed, err := currentStandardRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, confirmed.ID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if requirement.CancelledAt != nil || binding.RequirementVersionID != confirmed.ID ||
			(binding.Role != cleardev.StandardRoleEngineeringPlanner && binding.Role != cleardev.StandardRoleReviewer) ||
			binding.Status != cleardev.RoleBindingStatusRequested || binding.AOSessionID != "" ||
			strings.TrimSpace(binding.ID) == "" || strings.TrimSpace(binding.SessionCreationIdempotencyKey) == "" {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid role binding request")
			return nil
		}
		if binding.Role == cleardev.StandardRoleReviewer {
			if strings.TrimSpace(binding.CandidateCommitID) == "" || strings.TrimSpace(binding.DispatchID) == "" {
				rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "Reviewer binding must name its accepted dispatch and candidate")
				return nil
			}
			dispatchRow, dispatchErr := q.GetClearDevStandardDispatch(ctx, binding.DispatchID)
			if dispatchErr != nil {
				return dispatchErr
			}
			dispatch := clearDevStandardDispatchFromGen(dispatchRow)
			candidateRow, candidateErr := q.GetClearDevStandardCandidateCommit(ctx, binding.CandidateCommitID)
			if candidateErr != nil {
				return candidateErr
			}
			candidate := clearDevStandardCandidateFromGen(candidateRow)
			if dispatch.Status != cleardev.DispatchStatusAccepted || dispatch.RequirementVersionID != confirmed.ID ||
				candidate.DevelopmentTaskID != dispatch.PreallocatedDevelopmentTaskID || candidateRow.DispatchID.String != dispatch.ID {
				rejected = standardRejected(requirement.ID, cleardev.ReasonCandidateMismatch, "Reviewer binding must use the current accepted candidate")
				return nil
			}
		}
		bindings, err := q.ListClearDevStandardRoleBindings(ctx, confirmed.ID)
		if err != nil {
			return err
		}
		for _, row := range bindings {
			existing := clearDevStandardRoleBindingFromGen(row)
			if existing.Role != binding.Role {
				continue
			}
			if binding.Role == cleardev.StandardRoleReviewer && existing.CandidateCommitID != binding.CandidateCommitID {
				continue
			}
			if sameStandardRoleBinding(existing, binding) {
				result = existing
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonRoleBindingMismatch, "role is already bound to another stable spawn request")
			return nil
		}
		result = binding
		if err := insertClearDevStandardRoleBinding(ctx, q, result); err != nil {
			return err
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectStandardRoleBinding, result.ID, cleardev.ActionBindStandardRole, cleardev.EventAccepted, cleardev.ReasonNone, "", result.RequestedAt); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return cleardev.RoleSessionBinding{}, false, err
	}
	if rejected != nil {
		return cleardev.RoleSessionBinding{}, false, rejected
	}
	return result, created, nil
}

// BindClearDevRoleBinding CAS-binds an AO session. Database triggers verify
// the exact project, Codex Chat mode, auto permission, and stable spawn key.
func (s *Store) BindClearDevRoleBinding(ctx context.Context, roleBindingID, aoSessionID string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "bind ClearDev STANDARD role", func(q *gen.Queries) error {
		bindingRow, err := q.GetClearDevStandardRoleBinding(ctx, roleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevStandardRoleBindingFromGen(bindingRow)
		requirement, _, err := currentStandardRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, binding.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if binding.Status == cleardev.RoleBindingStatusBound {
			if binding.AOSessionID == aoSessionID {
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonRoleBindingMismatch, "role binding already has another AO session")
			return nil
		}
		if binding.Status != cleardev.RoleBindingStatusRequested || strings.TrimSpace(aoSessionID) == "" {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "role binding is not pending")
			return nil
		}
		bindings, listErr := q.ListClearDevStandardRoleBindings(ctx, binding.RequirementVersionID)
		if listErr != nil {
			return listErr
		}
		for _, row := range bindings {
			other := clearDevStandardRoleBindingFromGen(row)
			if other.ID != binding.ID && other.AOSessionID == aoSessionID {
				rejected = standardRejected(requirement.ID, cleardev.ReasonSessionMismatch, "AO session already belongs to another STANDARD role")
				return nil
			}
		}
		rows, err := q.BindClearDevStandardRoleBindingCAS(ctx, gen.BindClearDevStandardRoleBindingCASParams{
			AoSessionID: nullableString(aoSessionID), BoundAt: nullableTimePtr(&at), ID: roleBindingID,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "role binding compare-and-swap lost")
			return nil
		}
		changed = true
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectStandardRoleBinding, roleBindingID, cleardev.ActionBindStandardRole, cleardev.EventAccepted, cleardev.ReasonNone, aoSessionID, at)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

// FailClearDevRoleBinding records a stable spawn failure once. It cannot turn
// a bound role into a different session or silently retry with another key.
func (s *Store) FailClearDevRoleBinding(ctx context.Context, command cleardev.FailRoleBindingCommand) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "fail ClearDev STANDARD role", func(q *gen.Queries) error {
		bindingRow, err := q.GetClearDevStandardRoleBinding(ctx, command.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevStandardRoleBindingFromGen(bindingRow)
		requirement, _, err := currentStandardRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, binding.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if binding.Status == cleardev.RoleBindingStatusFailed && binding.ReasonCode == command.ReasonCode {
			return nil
		}
		if binding.Status != cleardev.RoleBindingStatusRequested || command.ReasonCode == cleardev.ReasonNone {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "role binding cannot be failed")
			return nil
		}
		rows, err := q.FailClearDevStandardRoleBindingCAS(ctx, gen.FailClearDevStandardRoleBindingCASParams{ReasonCode: string(command.ReasonCode), EndedAt: nullableTimePtr(&command.At), ID: command.RoleBindingID})
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "role binding compare-and-swap lost")
			return nil
		}
		changed = true
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectStandardRoleBinding, command.RoleBindingID, cleardev.ActionBindStandardRole, cleardev.EventRejected, command.ReasonCode, "", command.At)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

// CreateClearDevStandardAgentStep writes the stable request and message IDs
// before any Chat turn is sent. Repeating the same ID is a harmless read.
func (s *Store) CreateClearDevStandardAgentStep(ctx context.Context, step cleardev.AgentStep) (cleardev.AgentStep, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.AgentStep
	var created bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev STANDARD agent step", func(q *gen.Queries) error {
		bindingRow, err := q.GetClearDevStandardRoleBinding(ctx, step.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevStandardRoleBindingFromGen(bindingRow)
		requirement, _, err := currentStandardRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, binding.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if existingRow, getErr := q.GetClearDevStandardAgentStep(ctx, step.ID); getErr == nil {
			existing := clearDevStandardAgentStepFromGen(existingRow)
			if sameStandardAgentStepRequest(existing, step) {
				result = existing
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "agent step ID already belongs to another request")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		if binding.Status != cleardev.RoleBindingStatusBound || step.SendStatus != cleardev.AgentStepSendStatusPending ||
			!validStandardAgentStepKind(step.Kind) || strings.TrimSpace(step.ID) == "" ||
			strings.TrimSpace(step.RequestID) == "" || strings.TrimSpace(step.ClientMessageID) == "" ||
			!validSHA256Digest(step.PromptSHA256) || step.TurnID != "" || step.FinalMessageID != "" || step.FinalMessageText != "" || step.MessageSHA256 != "" ||
			step.SentAt != nil || step.CompletedAt != nil || step.FailedAt != nil || step.ReasonCode != cleardev.ReasonNone {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid pending agent step")
			return nil
		}
		result = step
		if err := insertClearDevStandardAgentStep(ctx, q, result); err != nil {
			return err
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectStandardAgentStep, step.ID, cleardev.ActionCreateStandardAgentStep, cleardev.EventAccepted, cleardev.ReasonNone, binding.AOSessionID, step.RequestedAt); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return cleardev.AgentStep{}, false, err
	}
	if rejected != nil {
		return cleardev.AgentStep{}, false, rejected
	}
	return result, created, nil
}

// MarkClearDevStandardAgentStepSent moves one saved request to SENT exactly
// once. This is the durable point after which a retry must reuse its message.
func (s *Store) MarkClearDevStandardAgentStepSent(ctx context.Context, stepID string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "mark ClearDev STANDARD agent step sent", func(q *gen.Queries) error {
		row, err := q.GetClearDevStandardAgentStep(ctx, stepID)
		if err != nil {
			return err
		}
		step := clearDevStandardAgentStepFromGen(row)
		bindingRow, err := q.GetClearDevStandardRoleBinding(ctx, step.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevStandardRoleBindingFromGen(bindingRow)
		requirement, _, err := currentStandardRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, binding.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if step.SendStatus == cleardev.AgentStepSendStatusSent {
			return nil
		}
		if step.SendStatus != cleardev.AgentStepSendStatusPending {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "agent step is no longer pending")
			return nil
		}
		rows, err := q.MarkClearDevStandardAgentStepSentCAS(ctx, gen.MarkClearDevStandardAgentStepSentCASParams{SentAt: nullableTimePtr(&at), ID: stepID})
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "agent-step compare-and-swap lost")
			return nil
		}
		changed = true
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectStandardAgentStep, stepID, cleardev.ActionSendStandardAgentStep, cleardev.EventAccepted, cleardev.ReasonNone, binding.AOSessionID, at)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

// SettleClearDevStandardAgentStep records only a final exact turn/message, or
// a stable failed state. It cannot overwrite a prior terminal observation.
func (s *Store) SettleClearDevStandardAgentStep(ctx context.Context, step cleardev.AgentStep) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "settle ClearDev STANDARD agent step", func(q *gen.Queries) error {
		row, err := q.GetClearDevStandardAgentStep(ctx, step.ID)
		if err != nil {
			return err
		}
		existing := clearDevStandardAgentStepFromGen(row)
		bindingRow, err := q.GetClearDevStandardRoleBinding(ctx, existing.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevStandardRoleBindingFromGen(bindingRow)
		requirement, _, err := currentStandardRequirement(ctx, q, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if existing.SendStatus == step.SendStatus && sameStandardAgentStepTerminal(existing, step) {
			return nil
		}
		var rows int64
		switch step.SendStatus {
		case cleardev.AgentStepSendStatusSettled:
			if existing.SendStatus != cleardev.AgentStepSendStatusSent {
				rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "agent step is not awaiting a sent result")
				return nil
			}
			if strings.TrimSpace(step.TurnID) == "" || strings.TrimSpace(step.FinalMessageID) == "" || strings.TrimSpace(step.FinalMessageText) == "" ||
				!requirementDigestMatches(step.FinalMessageText, step.MessageSHA256) || step.CompletedAt == nil || step.FailedAt != nil || step.ReasonCode != cleardev.ReasonNone {
				rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid settled agent step")
				return nil
			}
			rows, err = q.SettleClearDevStandardAgentStepCAS(ctx, gen.SettleClearDevStandardAgentStepCASParams{
				TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID), FinalMessageText: nullableString(step.FinalMessageText),
				MessageSha256: nullableString(step.MessageSHA256), CompletedAt: nullableTimePtr(step.CompletedAt), ID: step.ID,
			})
		case cleardev.AgentStepSendStatusFailed:
			if existing.SendStatus != cleardev.AgentStepSendStatusPending && existing.SendStatus != cleardev.AgentStepSendStatusSent {
				rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "agent step cannot fail from its current state")
				return nil
			}
			if step.FailedAt == nil || step.ReasonCode == cleardev.ReasonNone || step.TurnID != "" || step.FinalMessageID != "" ||
				step.FinalMessageText != "" || step.MessageSHA256 != "" || step.CompletedAt != nil {
				rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid failed agent step")
				return nil
			}
			rows, err = q.FailClearDevStandardAgentStepCAS(ctx, gen.FailClearDevStandardAgentStepCASParams{FailedAt: nullableTimePtr(step.FailedAt), ReasonCode: string(step.ReasonCode), ID: step.ID})
		default:
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "agent step must settle or fail")
			return nil
		}
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "agent-step compare-and-swap lost")
			return nil
		}
		changed = true
		outcome := cleardev.EventAccepted
		if step.SendStatus == cleardev.AgentStepSendStatusFailed {
			outcome = cleardev.EventRejected
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectStandardAgentStep, step.ID, cleardev.ActionSettleStandardAgentStep, outcome, step.ReasonCode, binding.AOSessionID, terminalAgentStepTime(step))
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

// CreateClearDevEngineeringPlan appends exactly one immutable normalized plan
// version after the matching Planner step has settled.
func (s *Store) CreateClearDevEngineeringPlan(ctx context.Context, plan cleardev.EngineeringPlan) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev engineering plan", func(q *gen.Queries) error {
		requirement, confirmed, err := standardRequirementForVersion(ctx, q, plan.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, plan.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if requirement.CancelledAt != nil || confirmed.ID != plan.RequirementVersionID ||
			strings.TrimSpace(plan.ID) == "" || !validSHA256Digest(plan.RequirementSHA256) ||
			plan.RequirementSHA256 != confirmed.Sha256 || !requirementDigestMatches(plan.PlanJSON, plan.PlanSHA256) ||
			!validJSONObject(plan.PlanJSON) || strings.TrimSpace(plan.PlannerRoleBindingID) == "" ||
			strings.TrimSpace(plan.AgentStepID) == "" || strings.TrimSpace(plan.TurnID) == "" || strings.TrimSpace(plan.FinalMessageID) == "" {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid engineering plan")
			return nil
		}
		next, err := q.NextClearDevStandardEngineeringPlanVersion(ctx, plan.RequirementVersionID)
		if err != nil {
			return err
		}
		if plan.Version != next {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "engineering plan version is not next")
			return nil
		}
		if err := q.InsertClearDevStandardEngineeringPlan(ctx, standardEngineeringPlanInsertParams(plan)); err != nil {
			return err
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectEngineeringPlan, plan.ID, cleardev.ActionCreateEngineeringPlan, cleardev.EventAccepted, cleardev.ReasonNone, "", plan.CreatedAt)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// RecordClearDevPlanReview appends the steward's settled verdict for one exact
// plan. The database keeps one review per plan and never lets it be rewritten.
func (s *Store) RecordClearDevPlanReview(ctx context.Context, review cleardev.PlanReview) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "record ClearDev plan review", func(q *gen.Queries) error {
		planRow, err := q.GetClearDevStandardEngineeringPlan(ctx, review.EngineeringPlanID)
		if err != nil {
			return err
		}
		plan := clearDevStandardEngineeringPlanFromGen(planRow)
		requirement, _, err := standardRequirementForVersion(ctx, q, plan.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, plan.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if requirement.CancelledAt != nil || strings.TrimSpace(review.ID) == "" || !validPlanReviewVerdict(review.Verdict) ||
			strings.TrimSpace(string(review.ReasonCode)) == "" || strings.TrimSpace(review.Summary) == "" ||
			strings.TrimSpace(review.StewardRoleBindingID) == "" || strings.TrimSpace(review.AgentStepID) == "" ||
			strings.TrimSpace(review.TurnID) == "" || strings.TrimSpace(review.FinalMessageID) == "" {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid plan review")
			return nil
		}
		if err := q.InsertClearDevStandardPlanReview(ctx, standardPlanReviewInsertParams(review)); err != nil {
			return err
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectPlanReview, review.ID, cleardev.ActionRecordPlanReview, cleardev.EventAccepted, review.ReasonCode, "", review.CreatedAt)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// CreateClearDevPendingDispatch persists the steward's exact dispatch request
// and the Builder's stable spawn key, but creates no development task yet.
func (s *Store) CreateClearDevPendingDispatch(ctx context.Context, command cleardev.CreatePendingDispatchCommand) (cleardev.Dispatch, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.Dispatch
	var created bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev pending dispatch", func(q *gen.Queries) error {
		dispatch := command.Dispatch
		requirement, confirmed, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if existingRow, getErr := q.GetClearDevStandardDispatch(ctx, dispatch.ID); getErr == nil {
			existing := clearDevStandardDispatchFromGen(existingRow)
			if sameStandardDispatchRequest(existing, dispatch) {
				result = existing
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "dispatch ID already belongs to another request")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		builder := command.BuilderBinding
		if requirement.CancelledAt != nil || confirmed.ID != dispatch.RequirementVersionID ||
			dispatch.Mode != cleardev.WorkModeStandard || dispatch.Status != cleardev.DispatchStatusPending ||
			strings.TrimSpace(dispatch.ID) == "" || strings.TrimSpace(dispatch.EngineeringPlanID) == "" ||
			strings.TrimSpace(dispatch.PlanReviewID) == "" || strings.TrimSpace(dispatch.StewardRoleBindingID) == "" ||
			strings.TrimSpace(dispatch.AgentStepID) == "" || strings.TrimSpace(dispatch.PreallocatedDevelopmentTaskID) == "" ||
			!validJSONObject(dispatch.ExecutionPackageJSON) || !requirementDigestMatches(dispatch.ExecutionPackageJSON, dispatch.ExecutionPackageSHA256) ||
			strings.TrimSpace(dispatch.BuilderSessionIdempotencyKey) == "" || strings.TrimSpace(dispatch.BuilderRoleBindingID) == "" ||
			dispatch.BaseCommitSHA != "" || dispatch.ReasonCode != cleardev.ReasonNone || dispatch.DecidedAt != nil ||
			builder.ID != dispatch.BuilderRoleBindingID || builder.DevelopmentRequirementID != requirement.ID ||
			builder.RequirementVersionID != confirmed.ID || builder.Role != cleardev.StandardRoleBuilder ||
			builder.DispatchID != dispatch.ID || builder.Status != cleardev.RoleBindingStatusRequested || builder.AOSessionID != "" ||
			builder.SessionCreationIdempotencyKey != dispatch.BuilderSessionIdempotencyKey {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid pending dispatch")
			return nil
		}
		if _, valid, validateErr := validatePersistedStandardExecutionPackage(ctx, q, dispatch); validateErr != nil {
			return validateErr
		} else if !valid {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "execution package does not match the approved engineering plan")
			return nil
		}
		if dispatch.ExpectedTaskSetVersion != confirmed.TaskSetVersion {
			rejected = standardRejected(requirement.ID, cleardev.ReasonTaskSetChanged, "task set changed before dispatch was saved")
			return nil
		}
		if err := insertClearDevStandardRoleBinding(ctx, q, builder); err != nil {
			return err
		}
		if err := q.InsertClearDevStandardDispatch(ctx, standardDispatchInsertParams(dispatch)); err != nil {
			return err
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDispatch, dispatch.ID, cleardev.ActionRequestDispatch, cleardev.EventAccepted, cleardev.ReasonNone, "", dispatch.RequestedAt); err != nil {
			return err
		}
		result, created = dispatch, true
		return nil
	})
	if err != nil {
		return cleardev.Dispatch{}, false, err
	}
	if rejected != nil {
		return cleardev.Dispatch{}, false, rejected
	}
	return result, created, nil
}

// AcceptClearDevStandardDispatch performs the only task-creation path for S02.
// It rechecks the confirmed version, task set, approved review, and bound
// Builder in one transaction; then creates task/permissions/checks, starts the
// task, increments the task set, and settles PENDING to ACCEPTED once.
func (s *Store) AcceptClearDevStandardDispatch(ctx context.Context, command cleardev.DispatchAcceptance) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "accept ClearDev STANDARD dispatch", func(q *gen.Queries) error {
		dispatchRow, err := q.GetClearDevStandardDispatch(ctx, command.DispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(dispatchRow)
		requirement, confirmed, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if dispatch.Status == cleardev.DispatchStatusAccepted {
			if dispatch.BaseCommitSHA == command.BaseCommitSHA && dispatch.PreallocatedDevelopmentTaskID == command.InitialTask.Task.ID {
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonDispatchAlreadySet, "dispatch already accepted with different facts")
			return nil
		}
		if dispatch.Status != cleardev.DispatchStatusPending {
			rejected = standardRejected(requirement.ID, cleardev.ReasonDispatchAlreadySet, "dispatch is already terminal")
			return nil
		}
		reason := cleardev.ReasonNone
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			reason = cleardev.ReasonDirectionChangeStopped
		} else if requirement.CancelledAt != nil || confirmed.ID != dispatch.RequirementVersionID {
			reason = cleardev.ReasonStalePlan
		} else if confirmed.TaskSetVersion != dispatch.ExpectedTaskSetVersion {
			reason = cleardev.ReasonTaskSetChanged
		} else if !validS02SHA1(command.BaseCommitSHA) || command.BuilderSessionID == "" || !validDispatchInitialTask(dispatch, command) {
			reason = cleardev.ReasonPreconditionNotMet
		}
		pack, packValid, packErr := validatePersistedStandardExecutionPackage(ctx, q, dispatch)
		if packErr != nil {
			return packErr
		}
		if reason == cleardev.ReasonNone && (!packValid || !dispatchAcceptanceMatchesPackage(command, pack)) {
			reason = cleardev.ReasonStalePlan
		}
		builderRow, builderErr := q.GetClearDevStandardRoleBinding(ctx, dispatch.BuilderRoleBindingID)
		if builderErr != nil {
			return builderErr
		}
		builder := clearDevStandardRoleBindingFromGen(builderRow)
		if reason == cleardev.ReasonNone && (builder.Status != cleardev.RoleBindingStatusBound || builder.AOSessionID != command.BuilderSessionID ||
			builder.Role != cleardev.StandardRoleBuilder || builder.RequirementVersionID != confirmed.ID) {
			reason = cleardev.ReasonBuilderSpawnFailed
		}
		planReview, reviewErr := q.GetClearDevStandardPlanReview(ctx, dispatch.PlanReviewID)
		if reviewErr != nil {
			return reviewErr
		}
		if reason == cleardev.ReasonNone && planReview.Verdict != string(cleardev.PlanReviewApproved) {
			reason = cleardev.ReasonStalePlan
		}
		if reason != cleardev.ReasonNone {
			rows, updateErr := q.RejectClearDevStandardDispatchCAS(ctx, gen.RejectClearDevStandardDispatchCASParams{Status: string(cleardev.DispatchStatusRejected), ReasonCode: string(reason), DecidedAt: nullableTimePtr(&command.At), ID: dispatch.ID})
			if updateErr != nil {
				return updateErr
			}
			if rows != 1 {
				return fmt.Errorf("reject stale dispatch %s: CAS affected %d rows", dispatch.ID, rows)
			}
			if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDispatch, dispatch.ID, cleardev.ActionRejectDispatch, cleardev.EventRejected, reason, builder.AOSessionID, command.At); err != nil {
				return err
			}
			rejected = standardRejected(requirement.ID, reason, "dispatch acceptance prerequisites changed")
			return nil
		}

		task := command.InitialTask.Task
		task.DevelopmentRequirementID = requirement.ID
		task.RequirementVersionID = confirmed.ID
		if err := q.InsertClearDevStandardDevelopmentTask(ctx, gen.InsertClearDevStandardDevelopmentTaskParams{
			ID: task.ID, DevelopmentProjectID: task.DevelopmentRequirementID, ContractVersionID: task.RequirementVersionID,
			Title: task.Title, Mode: string(cleardev.WorkModeStandard), State: string(cleardev.DevelopmentTaskStatusPlanned),
			PausedFromState: sql.NullString{}, MaxReworkCount: int64(task.MaxReworkCount), ReworkCount: 0,
			AcceptedDispatchID: nullableString(dispatch.ID), DispatchBaseCommitSha: nullableString(command.BaseCommitSHA),
			CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		}); err != nil {
			return err
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID, cleardev.ActionCreateDevelopmentTask, cleardev.EventAccepted, cleardev.ReasonNone, builder.AOSessionID, task.CreatedAt); err != nil {
			return err
		}
		if err := insertClearDevPermission(ctx, q, command.InitialTask.Permission); err != nil {
			return err
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectPermission, command.InitialTask.Permission.ID, cleardev.ActionCreatePermissionVersion, cleardev.EventAccepted, cleardev.ReasonNone, builder.AOSessionID, command.InitialTask.Permission.CreatedAt); err != nil {
			return err
		}
		// The integration command is immutable in the accepted dispatch execution
		// package, and its run is recorded as INTEGRATION evidence.  It is not a
		// task-level required check: putting it in cleardev_required_checks would
		// make the shared task read model demand REQUIRED_CHECK:node-all evidence.
		for _, check := range command.InitialTask.Checks {
			if err := q.InsertClearDevRequiredCheck(ctx, gen.InsertClearDevRequiredCheckParams{ID: check.ID, WorkItemID: task.ID, Name: check.Name, CheckKind: check.Kind, CreatedAt: check.CreatedAt}); err != nil {
				return err
			}
			if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectRequiredCheck, check.ID, cleardev.ActionCreateRequiredCheck, cleardev.EventAccepted, cleardev.ReasonNone, builder.AOSessionID, check.CreatedAt); err != nil {
				return err
			}
		}
		rows, err := q.IncrementClearDevTaskSetVersionCAS(ctx, gen.IncrementClearDevTaskSetVersionCASParams{ID: confirmed.ID, ExpectedTaskSetVersion: confirmed.TaskSetVersion})
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("increment S02 task set: CAS affected %d rows", rows)
		}
		rows, err = q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
			NextState: string(cleardev.DevelopmentTaskStatusRunning), PausedFromState: sql.NullString{}, ReworkCount: 0,
			UpdatedAt: command.At, ID: task.ID, ExpectedState: string(cleardev.DevelopmentTaskStatusPlanned), ExpectedReworkCount: 0,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("start S02 task: CAS affected %d rows", rows)
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID, cleardev.ActionStartDevelopmentTask, cleardev.EventAccepted, cleardev.ReasonNone, builder.AOSessionID, command.At); err != nil {
			return err
		}
		rows, err = q.AcceptClearDevStandardDispatchCAS(ctx, gen.AcceptClearDevStandardDispatchCASParams{BaseCommitSha: nullableString(command.BaseCommitSHA), DecidedAt: nullableTimePtr(&command.At), ID: dispatch.ID})
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("accept dispatch %s: CAS affected %d rows", dispatch.ID, rows)
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDispatch, dispatch.ID, cleardev.ActionAcceptDispatch, cleardev.EventAccepted, cleardev.ReasonNone, builder.AOSessionID, command.At)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// SettleClearDevStandardDispatchFailure closes a still-PENDING dispatch when
// Builder spawning or a recheck cannot continue. It cannot alter any accepted
// request or create a task as a side effect.
func (s *Store) SettleClearDevStandardDispatchFailure(ctx context.Context, dispatchID string, status cleardev.DispatchStatus, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "settle ClearDev STANDARD dispatch failure", func(q *gen.Queries) error {
		row, err := q.GetClearDevStandardDispatch(ctx, dispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(row)
		requirement, _, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if status != cleardev.DispatchStatusRejected && status != cleardev.DispatchStatusFailed || reason == cleardev.ReasonNone {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "dispatch failure must be REJECTED/FAILED with a stable reason")
			return nil
		}
		if dispatch.Status == status && dispatch.ReasonCode == reason {
			return nil
		}
		if dispatch.Status != cleardev.DispatchStatusPending {
			rejected = standardRejected(requirement.ID, cleardev.ReasonDispatchAlreadySet, "dispatch is already terminal")
			return nil
		}
		rows, err := q.RejectClearDevStandardDispatchCAS(ctx, gen.RejectClearDevStandardDispatchCASParams{Status: string(status), ReasonCode: string(reason), DecidedAt: nullableTimePtr(&at), ID: dispatchID})
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "dispatch compare-and-swap lost")
			return nil
		}
		changed = true
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDispatch, dispatchID, cleardev.ActionRejectDispatch, cleardev.EventRejected, reason, "", at)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

// AppendClearDevStandardCandidate records only a trusted worktree observation
// for the accepted Builder. It then moves that one task into REVIEW using the
// existing typed task transition helper and emits the normal event stream.
func (s *Store) AppendClearDevStandardCandidate(ctx context.Context, observation cleardev.StandardCandidateObservation) (cleardev.CandidateCommit, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var candidate cleardev.CandidateCommit
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "append ClearDev STANDARD candidate", func(q *gen.Queries) error {
		dispatchRow, err := q.GetClearDevStandardDispatch(ctx, observation.DispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(dispatchRow)
		requirement, confirmed, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if dispatch.Status != cleardev.DispatchStatusAccepted || requirement.CancelledAt != nil || confirmed.ID != dispatch.RequirementVersionID ||
			strings.TrimSpace(observation.CandidateCommitID) == "" || !validS02SHA1(observation.CommitSHA) {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "candidate does not belong to an accepted current dispatch")
			return nil
		}
		if existingRow, getErr := q.GetClearDevStandardCandidateCommit(ctx, observation.CandidateCommitID); getErr == nil {
			existing := clearDevStandardCandidateFromGen(existingRow)
			if existing.DevelopmentTaskID == dispatch.PreallocatedDevelopmentTaskID && existing.CommitSHA == observation.CommitSHA {
				candidate = existing
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonCandidateMismatch, "candidate ID already belongs to another commit")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		taskRow, err := q.GetClearDevDevelopmentTask(ctx, dispatch.PreallocatedDevelopmentTaskID)
		if err != nil {
			return err
		}
		task := clearDevDevelopmentTaskFromGetGen(taskRow)
		builderRow, err := q.GetClearDevStandardRoleBinding(ctx, dispatch.BuilderRoleBindingID)
		if err != nil {
			return err
		}
		builder := clearDevStandardRoleBindingFromGen(builderRow)
		if task.Status != cleardev.DevelopmentTaskStatusRunning || builder.Status != cleardev.RoleBindingStatusBound || builder.AOSessionID == "" {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "task is not running for a bound Builder")
			return nil
		}
		permission, err := q.GetLatestClearDevPermissionVersion(ctx, task.ID)
		if err != nil {
			return err
		}
		next, err := q.NextClearDevCandidateSequence(ctx, task.ID)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevStandardCandidateCommit(ctx, gen.InsertClearDevStandardCandidateCommitParams{
			ID: observation.CandidateCommitID, WorkItemID: task.ID, Sequence: next, AoSessionID: builder.AOSessionID,
			PermissionVersionID: permission.ID, DispatchID: nullableString(dispatch.ID), BaseCommitSha: nullableString(dispatch.BaseCommitSHA),
			CommitSha: observation.CommitSHA, CreatedAt: observation.ObservedAt,
		}); err != nil {
			return err
		}
		candidate = cleardev.CandidateCommit{ID: observation.CandidateCommitID, DevelopmentTaskID: task.ID, Sequence: next,
			AOSessionID: builder.AOSessionID, PermissionVersionID: permission.ID, CommitSHA: observation.CommitSHA, CreatedAt: observation.ObservedAt}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectCandidate, candidate.ID, cleardev.ActionRegisterCandidate, cleardev.EventAccepted, cleardev.ReasonNone, builder.AOSessionID, observation.ObservedAt); err != nil {
			return err
		}
		_, err = applyClearDevDevelopmentTaskAction(ctx, q, cleardev.ActionRequest{Action: cleardev.ActionSubmitDevelopmentTaskReview, SubjectID: task.ID, SourceAOSessionID: builder.AOSessionID, At: observation.ObservedAt})
		return err
	})
	if err != nil {
		return cleardev.CandidateCommit{}, err
	}
	if rejected != nil {
		return cleardev.CandidateCommit{}, rejected
	}
	return candidate, nil
}

// CreateClearDevCandidateCheckRun persists a pending checker request. Its
// command/candidate/base binding is immutable; only its result may settle once.
func (s *Store) CreateClearDevCandidateCheckRun(ctx context.Context, run cleardev.CandidateCheckRun) (cleardev.CandidateCheckRun, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.CandidateCheckRun
	var created bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev candidate check run", func(q *gen.Queries) error {
		dispatchRow, err := q.GetClearDevStandardDispatch(ctx, run.DispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(dispatchRow)
		requirement, _, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if existingRow, getErr := q.GetClearDevCandidateCheckRun(ctx, run.ID); getErr == nil {
			existing := clearDevCandidateCheckRunFromGen(existingRow)
			if sameCandidateCheckRunRequest(existing, run) {
				result = existing
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "check-run ID already belongs to another request")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		if dispatch.Status != cleardev.DispatchStatusAccepted || run.Status != cleardev.CandidateCheckRunStatusPending ||
			strings.TrimSpace(run.ID) == "" || run.DevelopmentTaskID != dispatch.PreallocatedDevelopmentTaskID ||
			run.BaseCommitSHA != dispatch.BaseCommitSHA || !validS02SHA1(run.BaseCommitSHA) ||
			!validCandidateCheckKind(run.Kind) || !validSHA256Digest(run.CheckSpecSHA256) || !validJSONArray(run.ArgvJSON) ||
			run.ContainerImageID != "" || run.ExitCode != nil || run.OutputSummary != "" ||
			run.OutputSHA256 != "" || run.ChangedPathsJSON != "" || run.Result != "" || run.SettledAt != nil || run.ReasonCode != cleardev.ReasonNone ||
			(run.CandidateCommitID == "" && run.CandidateCommitSHA != "") || (run.CandidateCommitID != "" && !validS02SHA1(run.CandidateCommitSHA)) {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid pending candidate check run")
			return nil
		}
		if valid, validateErr := validateStandardCheckRunRequest(ctx, q, dispatch, run); validateErr != nil {
			return validateErr
		} else if !valid {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "candidate check is not authorized by the accepted execution package")
			return nil
		}
		result = run
		if err := q.InsertClearDevCandidateCheckRun(ctx, standardCandidateCheckRunInsertParams(run)); err != nil {
			return err
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectCandidateCheckRun, run.ID, cleardev.ActionRecordCandidateCheck, cleardev.EventAccepted, cleardev.ReasonNone, "", run.CreatedAt); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return cleardev.CandidateCheckRun{}, false, err
	}
	if rejected != nil {
		return cleardev.CandidateCheckRun{}, false, rejected
	}
	return result, created, nil
}

// SettleClearDevCandidateCheckRun records checker facts once. FAILED denotes
// unavailable infrastructure; a normal failed command is SETTLED with FAIL.
func (s *Store) SettleClearDevCandidateCheckRun(ctx context.Context, command cleardev.SettleCandidateCheckRunCommand) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "settle ClearDev candidate check run", func(q *gen.Queries) error {
		row, err := q.GetClearDevCandidateCheckRun(ctx, command.CandidateCheckRunID)
		if err != nil {
			return err
		}
		run := clearDevCandidateCheckRunFromGen(row)
		dispatchRow, err := q.GetClearDevStandardDispatch(ctx, run.DispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(dispatchRow)
		requirement, _, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if run.Status != cleardev.CandidateCheckRunStatusPending {
			if run.Status == cleardev.CandidateCheckRunStatusSettled && run.Result == command.Result {
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "check run is already terminal")
			return nil
		}
		status := cleardev.CandidateCheckRunStatusSettled
		if isStandardCheckInfrastructureReason(run.Kind, command.ReasonCode) {
			status = cleardev.CandidateCheckRunStatusFailed
		}
		if status == cleardev.CandidateCheckRunStatusSettled && (!validSHA256Digest(command.OutputSHA256) || !validJSONArray(command.ChangedPathsJSON) ||
			(command.Result != cleardev.EvidenceResultPass && command.Result != cleardev.EvidenceResultFail) ||
			(run.Kind != cleardev.CandidateCheckScope && strings.TrimSpace(command.ContainerImageID) == "") ||
			(run.Kind == cleardev.CandidateCheckScope && command.ContainerImageID != "") ||
			!validStandardCheckTerminalReason(run, command.Result, command.TimedOut, command.ReasonCode)) {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid terminal check result")
			return nil
		}
		if status == cleardev.CandidateCheckRunStatusFailed && (!isStandardCheckInfrastructureReason(run.Kind, command.ReasonCode) || command.Result != "") {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid checker infrastructure failure")
			return nil
		}
		rows, err := q.SettleClearDevCandidateCheckRunCAS(ctx, gen.SettleClearDevCandidateCheckRunCASParams{
			Status: string(status), ContainerImageID: nullableString(command.ContainerImageID), ExitCode: nullableInt64Ptr(intPtrToInt64(command.ExitCode)), TimedOut: nullableBool(command.TimedOut),
			OutputSummary: nullableString(command.OutputSummary), OutputSha256: nullableString(command.OutputSHA256),
			ChangedPathsJson: nullableString(command.ChangedPathsJSON), Result: nullableString(string(command.Result)),
			SettledAt: nullableTimePtr(&command.At), ReasonCode: string(command.ReasonCode), ID: command.CandidateCheckRunID,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "check-run compare-and-swap lost")
			return nil
		}
		changed = true
		outcome := cleardev.EventAccepted
		if status == cleardev.CandidateCheckRunStatusFailed {
			outcome = cleardev.EventRejected
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectCandidateCheckRun, command.CandidateCheckRunID, cleardev.ActionRecordCandidateCheck, outcome, command.ReasonCode, "", command.At)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

// CreateClearDevLocalReview saves a PENDING review assignment before the
// Reviewer request is sent. The assigned step must still be PENDING, which
// prevents a reboot from sending an unrecorded review packet.
func (s *Store) CreateClearDevLocalReview(ctx context.Context, review cleardev.LocalReview) (cleardev.LocalReview, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result cleardev.LocalReview
	var created bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "create ClearDev local review", func(q *gen.Queries) error {
		dispatchRow, err := q.GetClearDevStandardDispatch(ctx, review.DispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(dispatchRow)
		requirement, _, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if existingRow, getErr := q.GetClearDevStandardLocalReview(ctx, review.ID); getErr == nil {
			existing := clearDevStandardLocalReviewFromGen(existingRow)
			if sameLocalReviewRequest(existing, review) {
				result = existing
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "local-review ID already belongs to another request")
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		stepRow, err := q.GetClearDevStandardAgentStep(ctx, review.AgentStepID)
		if err != nil {
			return err
		}
		step := clearDevStandardAgentStepFromGen(stepRow)
		if dispatch.Status != cleardev.DispatchStatusAccepted || review.Status != cleardev.LocalReviewStatusPending ||
			strings.TrimSpace(review.ID) == "" || strings.TrimSpace(review.CandidateCommitID) == "" ||
			!validJSONObject(review.ReviewPacketJSON) || !requirementDigestMatches(review.ReviewPacketJSON, review.ReviewPacketSHA256) ||
			strings.TrimSpace(review.ReviewerRoleBindingID) == "" || step.RoleBindingID != review.ReviewerRoleBindingID ||
			step.Kind != cleardev.AgentStepLocalReview || step.SendStatus != cleardev.AgentStepSendStatusPending ||
			review.TurnID != "" || review.FinalMessageID != "" || review.Verdict != "" || review.ReasonCode != cleardev.ReasonNone ||
			review.Summary != "" || review.SettledAt != nil {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid pending local review")
			return nil
		}
		result = review
		if err := q.InsertClearDevStandardLocalReview(ctx, standardLocalReviewInsertParams(review)); err != nil {
			return err
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectLocalReview, review.ID, cleardev.ActionRecordLocalReview, cleardev.EventAccepted, cleardev.ReasonNone, "", review.CreatedAt); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return cleardev.LocalReview{}, false, err
	}
	if rejected != nil {
		return cleardev.LocalReview{}, false, rejected
	}
	return result, created, nil
}

// SettleClearDevLocalReview ties the parsed terminal verdict to the exact
// settled Reviewer Chat turn. The DB trigger also enforces a different session
// from the Builder bound to the candidate's accepted dispatch.
func (s *Store) SettleClearDevLocalReview(ctx context.Context, command cleardev.SettleLocalReviewCommand) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var changed bool
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "settle ClearDev local review", func(q *gen.Queries) error {
		row, err := q.GetClearDevStandardLocalReview(ctx, command.LocalReviewID)
		if err != nil {
			return err
		}
		review := clearDevStandardLocalReviewFromGen(row)
		dispatchRow, err := q.GetClearDevStandardDispatch(ctx, review.DispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(dispatchRow)
		requirement, _, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if review.Status != cleardev.LocalReviewStatusPending {
			if review.Status == cleardev.LocalReviewStatusSettled && review.Verdict == command.Verdict && review.ReasonCode == command.ReasonCode {
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "local review is already terminal")
			return nil
		}
		status := cleardev.LocalReviewStatusSettled
		if command.Verdict == "" {
			status = cleardev.LocalReviewStatusFailed
		}
		if status == cleardev.LocalReviewStatusSettled && (!validLocalReviewVerdict(command.Verdict) || strings.TrimSpace(command.TurnID) == "" ||
			strings.TrimSpace(command.FinalMessageID) == "" || !validLocalReviewReason(command.Verdict, command.ReasonCode) || strings.TrimSpace(command.Summary) == "") {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid terminal local review")
			return nil
		}
		if status == cleardev.LocalReviewStatusFailed && !validFailedLocalReviewReason(command.ReasonCode) {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "failed local review needs a stable reason")
			return nil
		}
		rows, err := q.SettleClearDevStandardLocalReviewCAS(ctx, gen.SettleClearDevStandardLocalReviewCASParams{
			Status: string(status), TurnID: nullableString(command.TurnID), FinalMessageID: nullableString(command.FinalMessageID),
			Verdict: nullableString(string(command.Verdict)), ReasonCode: string(command.ReasonCode), Summary: nullableString(command.Summary),
			SettledAt: nullableTimePtr(&command.At), ID: command.LocalReviewID,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "local-review compare-and-swap lost")
			return nil
		}
		ended, err := q.EndClearDevStandardRoleBindingCAS(ctx, gen.EndClearDevStandardRoleBindingCASParams{
			ReasonCode: string(command.ReasonCode), EndedAt: nullableTimePtr(&command.At), ID: review.ReviewerRoleBindingID,
		})
		if err != nil {
			return err
		}
		if ended != 1 {
			return fmt.Errorf("end Reviewer binding %s: CAS affected %d rows", review.ReviewerRoleBindingID, ended)
		}
		changed = true
		outcome := cleardev.EventAccepted
		if status == cleardev.LocalReviewStatusFailed {
			outcome = cleardev.EventRejected
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectLocalReview, review.ID, cleardev.ActionRecordLocalReview, outcome, command.ReasonCode, "", command.At); err != nil {
			return err
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectStandardRoleBinding, review.ReviewerRoleBindingID, cleardev.ActionBindStandardRole, cleardev.EventAccepted, command.ReasonCode, "", command.At)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil {
		return false, rejected
	}
	return changed, nil
}

// AppendClearDevStandardEvidence is the narrow S02-only evidence writer. The
// migration requires a matching run/review whenever its candidate is S02.
func (s *Store) AppendClearDevStandardEvidence(ctx context.Context, record cleardev.StandardEvidenceRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "append ClearDev STANDARD evidence", func(q *gen.Queries) error {
		evidence := record.Evidence
		requirementRow, err := q.GetClearDevRequirement(ctx, evidence.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		requirement := clearDevRequirementFromGen(requirementRow)
		if confirmed, confErr := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirement.ID); confErr == nil {
			if stopped, stopErr := activeDirectionStop(ctx, q, confirmed.ID); stopErr != nil {
				return stopErr
			} else if stopped {
				rejected = directionStoppedError()
				return nil
			}
		} else if !errors.Is(confErr, sql.ErrNoRows) {
			return confErr
		}
		if requirement.CancelledAt != nil || strings.TrimSpace(evidence.ID) == "" ||
			(evidence.Result != cleardev.EvidenceResultPass && evidence.Result != cleardev.EvidenceResultFail) ||
			((record.CandidateCheckRunID == "") == (record.LocalReviewID == "")) {
			rejected = standardRejected(requirement.ID, cleardev.ReasonEvidenceSourceInvalid, "S02 evidence needs exactly one durable source")
			return nil
		}
		if err := q.InsertClearDevStandardEvidence(ctx, gen.InsertClearDevStandardEvidenceParams{
			ID: evidence.ID, DevelopmentProjectID: evidence.DevelopmentRequirementID, SubjectType: physicalEvidenceSubject(evidence.SubjectType), SubjectID: evidence.SubjectID,
			EvidenceKind: string(evidence.Kind), EvidenceKey: evidence.Key, Result: string(evidence.Result),
			CandidateCommitID: nullableString(evidence.CandidateCommitID), IntegrationCandidateID: nullableString(evidence.IntegrationCandidateID),
			CommitSha: evidence.CommitSHA, SourceType: string(evidence.Source), SourceAoSessionID: nullableString(evidence.SourceAOSessionID),
			CandidateCheckRunID: nullableString(record.CandidateCheckRunID), LocalReviewID: nullableString(record.LocalReviewID),
			CreatedAt: evidence.CreatedAt, ExpiresAt: nullableTimePtr(evidence.ExpiresAt),
		}); err != nil {
			return err
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectEvidence, evidence.ID, cleardev.ActionRecordEvidence, cleardev.EventAccepted, cleardev.ReasonNone, evidence.SourceAOSessionID, evidence.CreatedAt)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// ApplyClearDevStandardFailure applies the frozen failure mapping only from a
// persisted failed run/review in the current dispatch round. Normal failures
// consume the one rework allowance; unavailable checker infrastructure blocks
// instead; an exhausted allowance becomes NEEDS_HUMAN.
func (s *Store) ApplyClearDevStandardFailure(ctx context.Context, command cleardev.StandardFailureCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "apply ClearDev STANDARD failure", func(q *gen.Queries) error {
		dispatchRow, err := q.GetClearDevStandardDispatch(ctx, command.DispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(dispatchRow)
		requirement, confirmed, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if dispatch.Status != cleardev.DispatchStatusAccepted || requirement.CancelledAt != nil || confirmed.ID != dispatch.RequirementVersionID ||
			(command.CandidateCheckRunID == "") == (command.LocalReviewID == "") || command.ReasonCode == cleardev.ReasonNone {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid STANDARD failure command")
			return nil
		}
		taskRow, err := q.GetClearDevDevelopmentTask(ctx, dispatch.PreallocatedDevelopmentTaskID)
		if err != nil {
			return err
		}
		task := clearDevDevelopmentTaskFromGetGen(taskRow)
		if task.Status != cleardev.DevelopmentTaskStatusRunning && task.Status != cleardev.DevelopmentTaskStatusReview {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "failed result does not belong to a running/review task")
			return nil
		}
		infrastructureFailure := false
		forceNeedsHuman := false
		reason := cleardev.ReasonNone
		if command.CandidateCheckRunID != "" {
			runRow, getErr := q.GetClearDevCandidateCheckRun(ctx, command.CandidateCheckRunID)
			if getErr != nil {
				return getErr
			}
			run := clearDevCandidateCheckRunFromGen(runRow)
			if run.DispatchID != dispatch.ID || run.DevelopmentTaskID != task.ID ||
				(run.Status != cleardev.CandidateCheckRunStatusFailed && !(run.Status == cleardev.CandidateCheckRunStatusSettled && run.Result == cleardev.EvidenceResultFail)) {
				rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "check run is not a terminal failed fact for this dispatch")
				return nil
			}
			if run.Status == cleardev.CandidateCheckRunStatusFailed {
				infrastructureFailure = true
			}
			if run.Status == cleardev.CandidateCheckRunStatusFailed {
				if !isStandardCheckInfrastructureReason(run.Kind, run.ReasonCode) {
					rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "failed check run has no recognized infrastructure reason")
					return nil
				}
			} else if !validStandardCheckTerminalReason(run, run.Result, run.TimedOut, run.ReasonCode) {
				rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "failed check run reason does not match its persisted result")
				return nil
			}
			reason = run.ReasonCode
			if run.CandidateCommitID != "" && !isCurrentStandardCandidate(ctx, q, task.ID, run.CandidateCommitID) {
				rejected = standardRejected(requirement.ID, cleardev.ReasonCandidateMismatch, "check run belongs to an old candidate round")
				return nil
			}
		} else {
			reviewRow, getErr := q.GetClearDevStandardLocalReview(ctx, command.LocalReviewID)
			if getErr != nil {
				return getErr
			}
			review := clearDevStandardLocalReviewFromGen(reviewRow)
			if review.DispatchID != dispatch.ID ||
				(review.Status != cleardev.LocalReviewStatusFailed && !(review.Status == cleardev.LocalReviewStatusSettled && review.Verdict != cleardev.LocalReviewPass)) ||
				!isCurrentStandardCandidate(ctx, q, task.ID, review.CandidateCommitID) {
				rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "local review is not a current failed fact for this dispatch")
				return nil
			}
			if review.Status == cleardev.LocalReviewStatusFailed {
				infrastructureFailure = true
				if !validFailedLocalReviewReason(review.ReasonCode) {
					rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "failed local review has no recognized reason")
					return nil
				}
			}
			if review.Status == cleardev.LocalReviewStatusSettled && review.Verdict == cleardev.LocalReviewBlocked {
				infrastructureFailure = true
			}
			if review.Status == cleardev.LocalReviewStatusSettled && review.Verdict == cleardev.LocalReviewNeedsHuman {
				forceNeedsHuman = true
			}
			if review.Status == cleardev.LocalReviewStatusSettled && !validLocalReviewReason(review.Verdict, review.ReasonCode) {
				rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "local review reason does not match its persisted verdict")
				return nil
			}
			reason = review.ReasonCode
		}
		if command.InfrastructureFailure != infrastructureFailure || command.ReasonCode != reason {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "failure mapping must match its persisted run or review")
			return nil
		}
		if forceNeedsHuman {
			rows, updateErr := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
				NextState: string(cleardev.DevelopmentTaskStatusNeedsHuman), PausedFromState: nullableString(taskRow.State),
				ReworkCount: taskRow.ReworkCount, UpdatedAt: command.At, ID: task.ID,
				ExpectedState: taskRow.State, ExpectedReworkCount: taskRow.ReworkCount,
			})
			if updateErr != nil {
				return updateErr
			}
			if rows != 1 {
				return fmt.Errorf("escalate Reviewer NEEDS_HUMAN task %s: CAS affected %d rows", task.ID, rows)
			}
			return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID, cleardev.ActionEscalateDevelopmentTask, cleardev.EventAccepted, reason, "", command.At)
		}
		if infrastructureFailure {
			rows, updateErr := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
				NextState: string(cleardev.DevelopmentTaskStatusBlocked), PausedFromState: nullableString(taskRow.State),
				ReworkCount: taskRow.ReworkCount, UpdatedAt: command.At, ID: task.ID,
				ExpectedState: taskRow.State, ExpectedReworkCount: taskRow.ReworkCount,
			})
			if updateErr != nil {
				return updateErr
			}
			if rows != 1 {
				return fmt.Errorf("block S02 task %s: CAS affected %d rows", task.ID, rows)
			}
			return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID, cleardev.ActionBlockDevelopmentTask, cleardev.EventAccepted, reason, "", command.At)
		}
		if task.ReworkCount >= task.MaxReworkCount {
			rows, updateErr := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
				NextState: string(cleardev.DevelopmentTaskStatusNeedsHuman), PausedFromState: nullableString(taskRow.State),
				ReworkCount: taskRow.ReworkCount, UpdatedAt: command.At, ID: task.ID,
				ExpectedState: taskRow.State, ExpectedReworkCount: taskRow.ReworkCount,
			})
			if updateErr != nil {
				return updateErr
			}
			if rows != 1 {
				return fmt.Errorf("escalate exhausted S02 task %s: CAS affected %d rows", task.ID, rows)
			}
			return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID, cleardev.ActionEscalateDevelopmentTask, cleardev.EventAccepted, cleardev.ReasonReworkLimitReached, "", command.At)
		}
		rows, err := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
			NextState: string(cleardev.DevelopmentTaskStatusRework), PausedFromState: sql.NullString{},
			ReworkCount: taskRow.ReworkCount, UpdatedAt: command.At, ID: task.ID,
			ExpectedState: taskRow.State, ExpectedReworkCount: taskRow.ReworkCount,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("request S02 rework %s: CAS affected %d rows", task.ID, rows)
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID, cleardev.ActionRequestDevelopmentTaskRework, cleardev.EventAccepted, reason, "", command.At); err != nil {
			return err
		}
		rows, err = q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
			NextState: string(cleardev.DevelopmentTaskStatusRunning), PausedFromState: sql.NullString{},
			ReworkCount: taskRow.ReworkCount + 1, UpdatedAt: command.At, ID: task.ID,
			ExpectedState: string(cleardev.DevelopmentTaskStatusRework), ExpectedReworkCount: taskRow.ReworkCount,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("restart S02 rework %s: CAS affected %d rows", task.ID, rows)
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID, cleardev.ActionRestartDevelopmentTask, cleardev.EventAccepted, reason, "", command.At)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// ApplyClearDevStandardBuilderOutcome maps either a frozen settled Builder
// BLOCKED/NEEDS_HUMAN result or one persisted Builder step failure. Candidate
// readiness remains a trusted worktree observation, never an Agent claim.
func (s *Store) ApplyClearDevStandardBuilderOutcome(ctx context.Context, command cleardev.StandardBuilderOutcomeCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "apply ClearDev STANDARD Builder outcome", func(q *gen.Queries) error {
		dispatchRow, err := q.GetClearDevStandardDispatch(ctx, command.DispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(dispatchRow)
		requirement, confirmed, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		stepRow, err := q.GetClearDevStandardAgentStep(ctx, command.AgentStepID)
		if err != nil {
			return err
		}
		step := clearDevStandardAgentStepFromGen(stepRow)
		validFailedStep := step.SendStatus == cleardev.AgentStepSendStatusFailed &&
			command.Outcome == cleardev.DevelopmentTaskStatusNeedsHuman &&
			command.ReasonCode == step.ReasonCode &&
			(command.ReasonCode == cleardev.ReasonCode("BUILDER_RESULT_INVALID") ||
				command.ReasonCode == cleardev.ReasonCode("BUILDER_TIMEOUT") ||
				command.ReasonCode == cleardev.ReasonCode("BUILDER_UNAVAILABLE"))
		validSettledStep := step.SendStatus == cleardev.AgentStepSendStatusSettled &&
			((command.Outcome == cleardev.DevelopmentTaskStatusBlocked && command.ReasonCode == cleardev.ReasonCode("BUILDER_BLOCKED")) ||
				(command.Outcome == cleardev.DevelopmentTaskStatusNeedsHuman && command.ReasonCode == cleardev.ReasonCode("BUILDER_NEEDS_HUMAN")))
		if dispatch.Status != cleardev.DispatchStatusAccepted || requirement.CancelledAt != nil || confirmed.ID != dispatch.RequirementVersionID ||
			step.Kind != cleardev.AgentStepBuilderResult || (!validFailedStep && !validSettledStep) {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid Builder outcome")
			return nil
		}
		bindingRow, err := q.GetClearDevStandardRoleBinding(ctx, step.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevStandardRoleBindingFromGen(bindingRow)
		if binding.ID != dispatch.BuilderRoleBindingID || binding.Role != cleardev.StandardRoleBuilder || binding.Status != cleardev.RoleBindingStatusBound {
			rejected = standardRejected(requirement.ID, cleardev.ReasonRoleBindingMismatch, "Builder outcome came from another session")
			return nil
		}
		taskRow, err := q.GetClearDevDevelopmentTask(ctx, dispatch.PreallocatedDevelopmentTaskID)
		if err != nil {
			return err
		}
		task := clearDevDevelopmentTaskFromGetGen(taskRow)
		if task.Status != cleardev.DevelopmentTaskStatusRunning {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "Builder outcome requires a running task")
			return nil
		}
		rows, err := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
			NextState: string(command.Outcome), PausedFromState: nullableString(taskRow.State), ReworkCount: taskRow.ReworkCount,
			UpdatedAt: command.At, ID: task.ID, ExpectedState: taskRow.State, ExpectedReworkCount: taskRow.ReworkCount,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("apply Builder outcome for %s: CAS affected %d rows", task.ID, rows)
		}
		action := cleardev.ActionBlockDevelopmentTask
		if command.Outcome == cleardev.DevelopmentTaskStatusNeedsHuman {
			action = cleardev.ActionEscalateDevelopmentTask
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID, action, cleardev.EventAccepted, command.ReasonCode, binding.AOSessionID, command.At)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// ApplyClearDevStandardReviewerBindingFailure blocks the current review task
// from a persisted failed Reviewer binding. It covers spawn failure before an
// assignment step/local-review row could be safely created.
func (s *Store) ApplyClearDevStandardReviewerBindingFailure(ctx context.Context, command cleardev.StandardReviewerBindingFailureCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "apply ClearDev STANDARD Reviewer binding failure", func(q *gen.Queries) error {
		bindingRow, err := q.GetClearDevStandardRoleBinding(ctx, command.RoleBindingID)
		if err != nil {
			return err
		}
		binding := clearDevStandardRoleBindingFromGen(bindingRow)
		requirement, confirmed, err := standardRequirementForVersion(ctx, q, binding.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, binding.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if binding.Role != cleardev.StandardRoleReviewer || binding.Status != cleardev.RoleBindingStatusFailed ||
			binding.ReasonCode != command.ReasonCode || binding.DispatchID == "" || binding.CandidateCommitID == "" ||
			command.ReasonCode == cleardev.ReasonNone || requirement.CancelledAt != nil {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "not a persisted failed Reviewer binding")
			return nil
		}
		dispatchRow, err := q.GetClearDevStandardDispatch(ctx, binding.DispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(dispatchRow)
		if dispatch.Status != cleardev.DispatchStatusAccepted || dispatch.RequirementVersionID != confirmed.ID ||
			!isCurrentStandardCandidate(ctx, q, dispatch.PreallocatedDevelopmentTaskID, binding.CandidateCommitID) {
			rejected = standardRejected(requirement.ID, cleardev.ReasonCandidateMismatch, "failed Reviewer binding is not for the current accepted candidate")
			return nil
		}
		taskRow, err := q.GetClearDevDevelopmentTask(ctx, dispatch.PreallocatedDevelopmentTaskID)
		if err != nil {
			return err
		}
		task := clearDevDevelopmentTaskFromGetGen(taskRow)
		if task.Status != cleardev.DevelopmentTaskStatusReview {
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "Reviewer failure requires a review task")
			return nil
		}
		rows, err := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
			NextState: string(cleardev.DevelopmentTaskStatusBlocked), PausedFromState: nullableString(taskRow.State),
			ReworkCount: taskRow.ReworkCount, UpdatedAt: command.At, ID: task.ID,
			ExpectedState: taskRow.State, ExpectedReworkCount: taskRow.ReworkCount,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("block task for Reviewer failure %s: CAS affected %d rows", task.ID, rows)
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID, cleardev.ActionBlockDevelopmentTask, cleardev.EventAccepted, command.ReasonCode, "", command.At)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

// CompleteClearDevStandardTask is the S02-only success gate. It ignores the
// generic S01 evidence table while deciding success and derives the one
// requirement integration evidence record from the exact passing precheck run.
func (s *Store) CompleteClearDevStandardTask(ctx context.Context, command cleardev.CompleteStandardTaskCommand) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var rejected *cleardev.RuleError
	err := s.inTx(ctx, "complete ClearDev STANDARD task", func(q *gen.Queries) error {
		dispatchRow, err := q.GetClearDevStandardDispatch(ctx, command.DispatchID)
		if err != nil {
			return err
		}
		dispatch := clearDevStandardDispatchFromGen(dispatchRow)
		requirement, confirmed, err := standardRequirementForVersion(ctx, q, dispatch.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped, stopErr := activeDirectionStop(ctx, q, dispatch.RequirementVersionID); stopErr != nil {
			return stopErr
		} else if stopped {
			rejected = directionStoppedError()
			return nil
		}
		if dispatch.Status != cleardev.DispatchStatusAccepted || requirement.CancelledAt != nil || confirmed.ID != dispatch.RequirementVersionID ||
			strings.TrimSpace(command.IntegrationCandidateID) == "" || strings.TrimSpace(command.IntegrationEvidenceID) == "" {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "invalid STANDARD completion command")
			return nil
		}
		taskRow, err := q.GetClearDevDevelopmentTask(ctx, dispatch.PreallocatedDevelopmentTaskID)
		if err != nil {
			return err
		}
		task := clearDevDevelopmentTaskFromGetGen(taskRow)
		if task.Status == cleardev.DevelopmentTaskStatusDone {
			if _, getErr := q.GetClearDevStandardIntegrationCandidate(ctx, command.IntegrationCandidateID); getErr == nil {
				return nil
			}
			rejected = standardRejected(requirement.ID, cleardev.ReasonInvalidTransition, "completed task has no requested integration candidate")
			return nil
		}
		if task.Status != cleardev.DevelopmentTaskStatusReview || confirmed.TaskSetVersion != dispatch.ExpectedTaskSetVersion+1 {
			rejected = standardRejected(requirement.ID, cleardev.ReasonPreconditionNotMet, "task is not the current review round")
			return nil
		}
		candidateRow, err := q.GetCurrentClearDevCandidateCommit(ctx, task.ID)
		if err != nil {
			return err
		}
		candidate := clearDevCandidateFromCurrentGen(candidateRow)
		standardCandidateRow, err := q.GetClearDevStandardCandidateCommit(ctx, candidate.ID)
		if err != nil {
			return err
		}
		standardCandidate := clearDevStandardCandidateFromGen(standardCandidateRow)
		if standardCandidate.DevelopmentTaskID != task.ID || standardCandidate.CommitSHA != candidate.CommitSHA || !isCurrentStandardCandidate(ctx, q, task.ID, candidate.ID) {
			rejected = standardRejected(requirement.ID, cleardev.ReasonCandidateMismatch, "current candidate is not the accepted S02 round")
			return nil
		}
		runs, err := q.ListClearDevCandidateCheckRuns(ctx, confirmed.ID)
		if err != nil {
			return err
		}
		checks, err := q.ListClearDevRequiredChecks(ctx, task.ID)
		if err != nil {
			return err
		}
		integrationRunID, complete := standardCompletionRuns(runs, candidate.ID, checks)
		if !complete {
			rejected = standardRejected(requirement.ID, cleardev.ReasonEvidenceIncomplete, "current candidate is missing passing S02 check runs")
			return nil
		}
		reviews, err := q.ListClearDevStandardLocalReviews(ctx, confirmed.ID)
		if err != nil {
			return err
		}
		if !hasPassingStandardLocalReview(reviews, candidate.ID) {
			rejected = standardRejected(requirement.ID, cleardev.ReasonEvidenceIncomplete, "current candidate is missing an independent Reviewer PASS")
			return nil
		}
		// The database refuses a dispatch-bound DONE transition unless this
		// candidate's integration candidate and source-bound evidence already
		// exist. Keep those immutable facts before the task CAS; the enclosing
		// transaction still makes completion atomic.
		next, err := q.NextClearDevIntegrationCandidateSequence(ctx, requirement.ID)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevStandardIntegrationCandidate(ctx, gen.InsertClearDevStandardIntegrationCandidateParams{
			ID: command.IntegrationCandidateID, DevelopmentProjectID: requirement.ID,
			RequirementVersionID: nullableString(confirmed.ID), TaskSetVersion: nullableInt64(confirmed.TaskSetVersion),
			Sequence: next, AoSessionID: standardCandidate.AOSessionID, CommitSha: candidate.CommitSHA,
			DispatchID: nullableString(dispatch.ID), SourceCandidateCommitID: nullableString(candidate.ID), CreatedAt: command.At,
		}); err != nil {
			return err
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectIntegrationCandidate, command.IntegrationCandidateID, cleardev.ActionRegisterIntegrationCandidate, cleardev.EventAccepted, cleardev.ReasonNone, standardCandidate.AOSessionID, command.At); err != nil {
			return err
		}
		if err := q.InsertClearDevStandardEvidence(ctx, gen.InsertClearDevStandardEvidenceParams{
			ID: command.IntegrationEvidenceID, DevelopmentProjectID: requirement.ID,
			SubjectType: physicalEvidenceSubject(cleardev.SubjectDevelopmentRequirement), SubjectID: requirement.ID,
			EvidenceKind: string(cleardev.EvidenceKindIntegration), EvidenceKey: "", Result: string(cleardev.EvidenceResultPass),
			CandidateCommitID: sql.NullString{}, IntegrationCandidateID: nullableString(command.IntegrationCandidateID),
			CommitSha: candidate.CommitSHA, SourceType: string(cleardev.EvidenceSourceControlPlaneChecker), SourceAoSessionID: sql.NullString{},
			CandidateCheckRunID: nullableString(integrationRunID), LocalReviewID: sql.NullString{}, CreatedAt: command.At, ExpiresAt: sql.NullTime{},
		}); err != nil {
			return err
		}
		if err := insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectEvidence, command.IntegrationEvidenceID, cleardev.ActionRecordEvidence, cleardev.EventAccepted, cleardev.ReasonNone, "", command.At); err != nil {
			return err
		}
		rows, err := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{
			NextState: string(cleardev.DevelopmentTaskStatusDone), PausedFromState: sql.NullString{}, ReworkCount: taskRow.ReworkCount,
			UpdatedAt: command.At, ID: task.ID, ExpectedState: taskRow.State, ExpectedReworkCount: taskRow.ReworkCount,
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("complete S02 task %s: CAS affected %d rows", task.ID, rows)
		}
		return insertStandardFactEvent(ctx, q, requirement, cleardev.SubjectDevelopmentTask, task.ID, cleardev.ActionCompleteDevelopmentTask, cleardev.EventAccepted, cleardev.ReasonNone, standardCandidate.AOSessionID, command.At)
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

func currentStandardRequirement(ctx context.Context, q *gen.Queries, requirementID string) (cleardev.DevelopmentRequirement, gen.CleardevContractVersion, error) {
	row, err := q.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return cleardev.DevelopmentRequirement{}, gen.CleardevContractVersion{}, err
	}
	confirmed, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, requirementID)
	if err != nil {
		return cleardev.DevelopmentRequirement{}, gen.CleardevContractVersion{}, err
	}
	return clearDevRequirementFromGen(row), confirmed, nil
}

func standardRequirementForVersion(ctx context.Context, q *gen.Queries, requirementVersionID string) (cleardev.DevelopmentRequirement, gen.CleardevContractVersion, error) {
	version, err := q.GetClearDevRequirementVersion(ctx, requirementVersionID)
	if err != nil {
		return cleardev.DevelopmentRequirement{}, gen.CleardevContractVersion{}, err
	}
	return currentStandardRequirement(ctx, q, version.DevelopmentProjectID)
}

func insertStandardFactEvent(ctx context.Context, q *gen.Queries, requirement cleardev.DevelopmentRequirement, subject cleardev.SubjectType, subjectID string, action cleardev.Action, outcome cleardev.EventOutcome, reason cleardev.ReasonCode, sourceSessionID string, at time.Time) error {
	return insertClearDevEvent(ctx, q, cleardev.RequirementEvent{
		AOProjectID: requirement.AOProjectID, DevelopmentRequirementID: requirement.ID,
		SubjectType: subject, SubjectID: subjectID, Action: action, Outcome: outcome,
		Reason: reason, Source: cleardev.EventSourceControlPlane,
		SourceAOSessionID: sourceSessionID, CreatedAt: at,
	})
}

func standardRejected(requirementID string, reason cleardev.ReasonCode, message string) *cleardev.RuleError {
	return &cleardev.RuleError{Code: reason, Message: fmt.Sprintf("ClearDev STANDARD flow %s: %s", requirementID, message)}
}

func insertClearDevStandardRoleBinding(ctx context.Context, q *gen.Queries, binding cleardev.RoleSessionBinding) error {
	return q.InsertClearDevStandardRoleBinding(ctx, gen.InsertClearDevStandardRoleBindingParams{
		ID: binding.ID, DevelopmentProjectID: binding.DevelopmentRequirementID, RequirementVersionID: binding.RequirementVersionID,
		Role: string(binding.Role), EngineeringPlanID: nullableString(binding.EngineeringPlanID), DispatchID: nullableString(binding.DispatchID),
		WorkItemID: nullableString(binding.DevelopmentTaskID), CandidateCommitID: nullableString(binding.CandidateCommitID),
		SessionCreationIdempotencyKey: binding.SessionCreationIdempotencyKey, AoSessionID: nullableString(binding.AOSessionID),
		Status: string(binding.Status), ReasonCode: string(binding.ReasonCode), RequestedAt: binding.RequestedAt,
		BoundAt: nullableTimePtr(binding.BoundAt), EndedAt: nullableTimePtr(binding.EndedAt),
	})
}

func insertClearDevStandardAgentStep(ctx context.Context, q *gen.Queries, step cleardev.AgentStep) error {
	return q.InsertClearDevStandardAgentStep(ctx, gen.InsertClearDevStandardAgentStepParams{
		ID: step.ID, RoleBindingID: step.RoleBindingID, StepKind: string(step.Kind), RequestID: step.RequestID,
		ClientMessageID: step.ClientMessageID, PromptSha256: step.PromptSHA256, SendStatus: string(step.SendStatus),
		TurnID: nullableString(step.TurnID), FinalMessageID: nullableString(step.FinalMessageID), FinalMessageText: nullableString(step.FinalMessageText), MessageSha256: nullableString(step.MessageSHA256),
		RequestedAt: step.RequestedAt, SentAt: nullableTimePtr(step.SentAt), CompletedAt: nullableTimePtr(step.CompletedAt),
		FailedAt: nullableTimePtr(step.FailedAt), ReasonCode: string(step.ReasonCode),
	})
}

func standardEngineeringPlanInsertParams(plan cleardev.EngineeringPlan) gen.InsertClearDevStandardEngineeringPlanParams {
	return gen.InsertClearDevStandardEngineeringPlanParams{
		ID: plan.ID, RequirementVersionID: plan.RequirementVersionID, RequirementSha256: plan.RequirementSHA256, Version: plan.Version,
		PlannerRoleBindingID: plan.PlannerRoleBindingID, AgentStepID: plan.AgentStepID, TurnID: plan.TurnID,
		FinalMessageID: plan.FinalMessageID, PlanJson: plan.PlanJSON, PlanSha256: plan.PlanSHA256, CreatedAt: plan.CreatedAt,
	}
}

func standardPlanReviewInsertParams(review cleardev.PlanReview) gen.InsertClearDevStandardPlanReviewParams {
	return gen.InsertClearDevStandardPlanReviewParams{
		ID: review.ID, EngineeringPlanID: review.EngineeringPlanID, StewardRoleBindingID: review.StewardRoleBindingID,
		AgentStepID: review.AgentStepID, TurnID: review.TurnID, FinalMessageID: review.FinalMessageID,
		Verdict: string(review.Verdict), ReasonCode: string(review.ReasonCode), Summary: review.Summary, CreatedAt: review.CreatedAt,
	}
}

func standardDispatchInsertParams(dispatch cleardev.Dispatch) gen.InsertClearDevStandardDispatchParams {
	return gen.InsertClearDevStandardDispatchParams{
		ID: dispatch.ID, RequirementVersionID: dispatch.RequirementVersionID, EngineeringPlanID: dispatch.EngineeringPlanID,
		PlanReviewID: dispatch.PlanReviewID, StewardRoleBindingID: dispatch.StewardRoleBindingID, AgentStepID: dispatch.AgentStepID,
		Mode: string(dispatch.Mode), PreallocatedWorkItemID: dispatch.PreallocatedDevelopmentTaskID,
		ExpectedTaskSetVersion: dispatch.ExpectedTaskSetVersion, ExecutionPackageJson: dispatch.ExecutionPackageJSON,
		ExecutionPackageSha256: dispatch.ExecutionPackageSHA256, BuilderSessionIdempotencyKey: dispatch.BuilderSessionIdempotencyKey,
		BuilderRoleBindingID: dispatch.BuilderRoleBindingID, BaseCommitSha: nullableString(dispatch.BaseCommitSHA),
		Status: string(dispatch.Status), ReasonCode: string(dispatch.ReasonCode), RequestedAt: dispatch.RequestedAt,
		DecidedAt: nullableTimePtr(dispatch.DecidedAt),
	}
}

func standardCandidateCheckRunInsertParams(run cleardev.CandidateCheckRun) gen.InsertClearDevCandidateCheckRunParams {
	return gen.InsertClearDevCandidateCheckRunParams{
		ID: run.ID, WorkItemID: run.DevelopmentTaskID, CandidateCommitID: nullableString(run.CandidateCommitID), DispatchID: run.DispatchID,
		BaseCommitSha: run.BaseCommitSHA, CandidateCommitSha: run.CandidateCommitSHA, CheckKind: string(run.Kind), CheckName: run.Name,
		CheckSpecSha256: run.CheckSpecSHA256, ArgvJson: run.ArgvJSON, ContainerImageID: nullableString(run.ContainerImageID),
		ExitCode: nullableInt64Ptr(intPtrToInt64(run.ExitCode)), Status: string(run.Status), TimedOut: sql.NullBool{},
		OutputSummary: sql.NullString{}, OutputSha256: sql.NullString{}, ChangedPathsJson: sql.NullString{}, Result: sql.NullString{},
		CreatedAt: run.CreatedAt, SettledAt: sql.NullTime{}, ReasonCode: "",
	}
}

func standardLocalReviewInsertParams(review cleardev.LocalReview) gen.InsertClearDevStandardLocalReviewParams {
	return gen.InsertClearDevStandardLocalReviewParams{
		ID: review.ID, CandidateCommitID: review.CandidateCommitID, DispatchID: review.DispatchID,
		ReviewPacketJson: review.ReviewPacketJSON, ReviewPacketSha256: review.ReviewPacketSHA256,
		ReviewerRoleBindingID: review.ReviewerRoleBindingID, AgentStepID: review.AgentStepID, Status: string(review.Status),
		TurnID: nullableString(review.TurnID), FinalMessageID: nullableString(review.FinalMessageID), Verdict: nullableString(string(review.Verdict)),
		ReasonCode: string(review.ReasonCode), Summary: nullableString(review.Summary), CreatedAt: review.CreatedAt,
		SettledAt: nullableTimePtr(review.SettledAt),
	}
}

func clearDevStandardRoleBindingFromGen(row gen.CleardevStandardRoleBinding) cleardev.RoleSessionBinding {
	binding := cleardev.RoleSessionBinding{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, RequirementVersionID: row.RequirementVersionID,
		Role: cleardev.StandardRole(row.Role), EngineeringPlanID: row.EngineeringPlanID.String, DispatchID: row.DispatchID.String,
		DevelopmentTaskID: row.WorkItemID.String, CandidateCommitID: row.CandidateCommitID.String,
		SessionCreationIdempotencyKey: row.SessionCreationIdempotencyKey, AOSessionID: row.AoSessionID.String,
		Status: cleardev.RoleBindingStatus(row.Status), ReasonCode: cleardev.ReasonCode(row.ReasonCode), RequestedAt: row.RequestedAt,
	}
	if row.BoundAt.Valid {
		value := row.BoundAt.Time
		binding.BoundAt = &value
	}
	if row.EndedAt.Valid {
		value := row.EndedAt.Time
		binding.EndedAt = &value
	}
	return binding
}

func clearDevStandardAgentStepFromGen(row gen.CleardevStandardAgentStep) cleardev.AgentStep {
	step := cleardev.AgentStep{
		ID: row.ID, RoleBindingID: row.RoleBindingID, Kind: cleardev.AgentStepKind(row.StepKind), RequestID: row.RequestID,
		ClientMessageID: row.ClientMessageID, PromptSHA256: row.PromptSha256, SendStatus: cleardev.AgentStepSendStatus(row.SendStatus),
		TurnID: row.TurnID.String, FinalMessageID: row.FinalMessageID.String, FinalMessageText: row.FinalMessageText.String, MessageSHA256: row.MessageSha256.String,
		RequestedAt: row.RequestedAt, ReasonCode: cleardev.ReasonCode(row.ReasonCode),
	}
	if row.SentAt.Valid {
		value := row.SentAt.Time
		step.SentAt = &value
	}
	if row.CompletedAt.Valid {
		value := row.CompletedAt.Time
		step.CompletedAt = &value
	}
	if row.FailedAt.Valid {
		value := row.FailedAt.Time
		step.FailedAt = &value
	}
	return step
}

func clearDevStandardEngineeringPlanFromGen(row gen.CleardevStandardEngineeringPlan) cleardev.EngineeringPlan {
	return cleardev.EngineeringPlan{
		ID: row.ID, RequirementVersionID: row.RequirementVersionID, RequirementSHA256: row.RequirementSha256, Version: row.Version,
		PlannerRoleBindingID: row.PlannerRoleBindingID, AgentStepID: row.AgentStepID, TurnID: row.TurnID,
		FinalMessageID: row.FinalMessageID, PlanJSON: row.PlanJson, PlanSHA256: row.PlanSha256, CreatedAt: row.CreatedAt,
	}
}

func clearDevStandardPlanReviewFromGen(row gen.CleardevStandardPlanReview) cleardev.PlanReview {
	return cleardev.PlanReview{
		ID: row.ID, EngineeringPlanID: row.EngineeringPlanID, StewardRoleBindingID: row.StewardRoleBindingID,
		AgentStepID: row.AgentStepID, TurnID: row.TurnID, FinalMessageID: row.FinalMessageID,
		Verdict: cleardev.PlanReviewVerdict(row.Verdict), ReasonCode: cleardev.ReasonCode(row.ReasonCode), Summary: row.Summary, CreatedAt: row.CreatedAt,
	}
}

func clearDevStandardDispatchFromGen(row gen.CleardevStandardDispatch) cleardev.Dispatch {
	dispatch := cleardev.Dispatch{
		ID: row.ID, RequirementVersionID: row.RequirementVersionID, EngineeringPlanID: row.EngineeringPlanID,
		PlanReviewID: row.PlanReviewID, StewardRoleBindingID: row.StewardRoleBindingID, AgentStepID: row.AgentStepID,
		Mode: cleardev.WorkMode(row.Mode), PreallocatedDevelopmentTaskID: row.PreallocatedWorkItemID,
		ExpectedTaskSetVersion: row.ExpectedTaskSetVersion, ExecutionPackageJSON: row.ExecutionPackageJson,
		ExecutionPackageSHA256: row.ExecutionPackageSha256, BuilderSessionIdempotencyKey: row.BuilderSessionIdempotencyKey,
		BuilderRoleBindingID: row.BuilderRoleBindingID, BaseCommitSHA: row.BaseCommitSha.String,
		Status: cleardev.DispatchStatus(row.Status), ReasonCode: cleardev.ReasonCode(row.ReasonCode), RequestedAt: row.RequestedAt,
	}
	if row.DecidedAt.Valid {
		value := row.DecidedAt.Time
		dispatch.DecidedAt = &value
	}
	return dispatch
}

func clearDevStandardCandidateFromGen(row gen.CleardevCandidateCommit) cleardev.CandidateCommit {
	return cleardev.CandidateCommit{
		ID: row.ID, DevelopmentTaskID: row.WorkItemID, Sequence: row.Sequence, AOSessionID: row.AoSessionID,
		PermissionVersionID: row.PermissionVersionID, CommitSHA: row.CommitSha, CreatedAt: row.CreatedAt,
	}
}

func clearDevDevelopmentTaskFromGetGen(row gen.GetClearDevDevelopmentTaskRow) cleardev.DevelopmentTask {
	return cleardev.DevelopmentTask{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, RequirementVersionID: row.ContractVersionID,
		Title: row.Title, Mode: cleardev.WorkMode(row.Mode), Status: publicDevelopmentTaskStatus(row.State),
		PausedFromStatus: publicDevelopmentTaskStatus(row.PausedFromState.String),
		MaxReworkCount:   int(row.MaxReworkCount), ReworkCount: int(row.ReworkCount), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func clearDevCandidateFromCurrentGen(row gen.GetCurrentClearDevCandidateCommitRow) cleardev.CandidateCommit {
	return cleardev.CandidateCommit{
		ID: row.ID, DevelopmentTaskID: row.WorkItemID, Sequence: row.Sequence, AOSessionID: row.AoSessionID,
		PermissionVersionID: row.PermissionVersionID, CommitSHA: row.CommitSha, CreatedAt: row.CreatedAt,
	}
}

func clearDevCandidateCheckRunFromGen(row gen.CleardevCandidateCheckRun) cleardev.CandidateCheckRun {
	run := cleardev.CandidateCheckRun{
		ID: row.ID, DevelopmentTaskID: row.WorkItemID, CandidateCommitID: row.CandidateCommitID.String, DispatchID: row.DispatchID,
		BaseCommitSHA: row.BaseCommitSha, CandidateCommitSHA: row.CandidateCommitSha, Kind: cleardev.CandidateCheckKind(row.CheckKind),
		Name: row.CheckName, CheckSpecSHA256: row.CheckSpecSha256, ArgvJSON: row.ArgvJson, ContainerImageID: row.ContainerImageID.String,
		Status: cleardev.CandidateCheckRunStatus(row.Status), TimedOut: row.TimedOut.Bool, OutputSummary: row.OutputSummary.String,
		OutputSHA256: row.OutputSha256.String, ChangedPathsJSON: row.ChangedPathsJson.String, Result: cleardev.EvidenceResult(row.Result.String),
		CreatedAt: row.CreatedAt, ReasonCode: cleardev.ReasonCode(row.ReasonCode),
	}
	if row.ExitCode.Valid {
		value := int(row.ExitCode.Int64)
		run.ExitCode = &value
	}
	if row.SettledAt.Valid {
		value := row.SettledAt.Time
		run.SettledAt = &value
	}
	return run
}

func clearDevStandardLocalReviewFromGen(row gen.CleardevStandardLocalReview) cleardev.LocalReview {
	review := cleardev.LocalReview{
		ID: row.ID, CandidateCommitID: row.CandidateCommitID, DispatchID: row.DispatchID,
		ReviewPacketJSON: row.ReviewPacketJson, ReviewPacketSHA256: row.ReviewPacketSha256,
		ReviewerRoleBindingID: row.ReviewerRoleBindingID, AgentStepID: row.AgentStepID, Status: cleardev.LocalReviewStatus(row.Status),
		TurnID: row.TurnID.String, FinalMessageID: row.FinalMessageID.String, Verdict: cleardev.LocalReviewVerdict(row.Verdict.String),
		ReasonCode: cleardev.ReasonCode(row.ReasonCode), Summary: row.Summary.String, CreatedAt: row.CreatedAt,
	}
	if row.SettledAt.Valid {
		value := row.SettledAt.Time
		review.SettledAt = &value
	}
	return review
}

func sameStandardRoleBinding(left, right cleardev.RoleSessionBinding) bool {
	return left.ID == right.ID && left.DevelopmentRequirementID == right.DevelopmentRequirementID &&
		left.RequirementVersionID == right.RequirementVersionID && left.Role == right.Role &&
		left.EngineeringPlanID == right.EngineeringPlanID && left.DispatchID == right.DispatchID &&
		left.DevelopmentTaskID == right.DevelopmentTaskID && left.CandidateCommitID == right.CandidateCommitID &&
		left.SessionCreationIdempotencyKey == right.SessionCreationIdempotencyKey
}

func sameStandardAgentStepRequest(left, right cleardev.AgentStep) bool {
	return left.ID == right.ID && left.RoleBindingID == right.RoleBindingID && left.Kind == right.Kind &&
		left.RequestID == right.RequestID && left.ClientMessageID == right.ClientMessageID && left.PromptSHA256 == right.PromptSHA256
}

func sameStandardAgentStepTerminal(left, right cleardev.AgentStep) bool {
	return left.ID == right.ID && left.TurnID == right.TurnID && left.FinalMessageID == right.FinalMessageID && left.FinalMessageText == right.FinalMessageText &&
		left.MessageSHA256 == right.MessageSHA256 && left.ReasonCode == right.ReasonCode
}

func sameStandardDispatchRequest(left, right cleardev.Dispatch) bool {
	return left.ID == right.ID && left.RequirementVersionID == right.RequirementVersionID &&
		left.EngineeringPlanID == right.EngineeringPlanID && left.PlanReviewID == right.PlanReviewID &&
		left.StewardRoleBindingID == right.StewardRoleBindingID && left.AgentStepID == right.AgentStepID &&
		left.Mode == right.Mode && left.PreallocatedDevelopmentTaskID == right.PreallocatedDevelopmentTaskID &&
		left.ExpectedTaskSetVersion == right.ExpectedTaskSetVersion && left.ExecutionPackageJSON == right.ExecutionPackageJSON &&
		left.ExecutionPackageSHA256 == right.ExecutionPackageSHA256 &&
		left.BuilderSessionIdempotencyKey == right.BuilderSessionIdempotencyKey && left.BuilderRoleBindingID == right.BuilderRoleBindingID
}

func sameCandidateCheckRunRequest(left, right cleardev.CandidateCheckRun) bool {
	return left.ID == right.ID && left.DevelopmentTaskID == right.DevelopmentTaskID &&
		left.CandidateCommitID == right.CandidateCommitID && left.DispatchID == right.DispatchID &&
		left.BaseCommitSHA == right.BaseCommitSHA && left.CandidateCommitSHA == right.CandidateCommitSHA &&
		left.Kind == right.Kind && left.Name == right.Name && left.CheckSpecSHA256 == right.CheckSpecSHA256 &&
		left.ArgvJSON == right.ArgvJSON
}

func sameLocalReviewRequest(left, right cleardev.LocalReview) bool {
	return left.ID == right.ID && left.CandidateCommitID == right.CandidateCommitID && left.DispatchID == right.DispatchID &&
		left.ReviewPacketJSON == right.ReviewPacketJSON && left.ReviewPacketSHA256 == right.ReviewPacketSHA256 &&
		left.ReviewerRoleBindingID == right.ReviewerRoleBindingID && left.AgentStepID == right.AgentStepID
}

func validStandardAgentStepKind(kind cleardev.AgentStepKind) bool {
	switch kind {
	case cleardev.AgentStepRequestPlanning, cleardev.AgentStepEngineeringPlan, cleardev.AgentStepPlanReview,
		cleardev.AgentStepDispatchRequest, cleardev.AgentStepBuilderResult, cleardev.AgentStepLocalReview,
		cleardev.AgentStepStatusReport:
		return true
	default:
		return false
	}
}

func validPlanReviewVerdict(verdict cleardev.PlanReviewVerdict) bool {
	return verdict == cleardev.PlanReviewApproved || verdict == cleardev.PlanReviewReplan || verdict == cleardev.PlanReviewNeedsHuman
}

func validCandidateCheckKind(kind cleardev.CandidateCheckKind) bool {
	return kind == cleardev.CandidateCheckScope || kind == cleardev.CandidateCheckRequired || kind == cleardev.CandidateCheckIntegration
}

func validLocalReviewVerdict(verdict cleardev.LocalReviewVerdict) bool {
	return verdict == cleardev.LocalReviewPass || verdict == cleardev.LocalReviewRework || verdict == cleardev.LocalReviewBlocked || verdict == cleardev.LocalReviewNeedsHuman
}

func validLocalReviewReason(verdict cleardev.LocalReviewVerdict, reason cleardev.ReasonCode) bool {
	expected := map[cleardev.LocalReviewVerdict]cleardev.ReasonCode{
		cleardev.LocalReviewPass:       "REVIEW_PASSED",
		cleardev.LocalReviewRework:     "REVIEW_CHANGES_REQUIRED",
		cleardev.LocalReviewBlocked:    "REVIEW_BLOCKED",
		cleardev.LocalReviewNeedsHuman: "REVIEW_NEEDS_HUMAN",
	}
	return expected[verdict] == reason
}

func validFailedLocalReviewReason(reason cleardev.ReasonCode) bool {
	return reason == "REVIEW_RESULT_INVALID" || reason == "REVIEW_TIMEOUT" || reason == "REVIEWER_UNAVAILABLE"
}

func isStandardCheckInfrastructureReason(kind cleardev.CandidateCheckKind, reason cleardev.ReasonCode) bool {
	if kind == cleardev.CandidateCheckScope {
		return reason == "GIT_CHECKER_UNAVAILABLE"
	}
	return (kind == cleardev.CandidateCheckRequired || kind == cleardev.CandidateCheckIntegration) && reason == "CHECKER_UNAVAILABLE"
}

func validStandardCheckTerminalReason(run cleardev.CandidateCheckRun, result cleardev.EvidenceResult, timedOut bool, reason cleardev.ReasonCode) bool {
	if result == cleardev.EvidenceResultPass {
		return !timedOut && reason == cleardev.ReasonNone
	}
	if result != cleardev.EvidenceResultFail {
		return false
	}
	switch run.Kind {
	case cleardev.CandidateCheckRequired, cleardev.CandidateCheckIntegration:
		if timedOut {
			return reason == "CHECK_TIMEOUT"
		}
		return reason == "CHECK_FAILED"
	case cleardev.CandidateCheckScope:
		if timedOut {
			return false
		}
		if run.Name == "scope" {
			return reason == "SCOPE_CHECK_FAILED"
		}
		if strings.HasPrefix(run.Name, "scope-round-") {
			return reason == "BUILDER_WORKTREE_DIRTY" || reason == "CANDIDATE_NOT_DESCENDANT" || reason == "CANDIDATE_INVALID"
		}
	}
	return false
}

func validS02SHA1(value string) bool {
	return len(value) == 40 && cleardev.IsFullCommitSHA(value) && value == strings.ToLower(value)
}

func validJSONObject(value string) bool {
	var decoded any
	return json.Unmarshal([]byte(value), &decoded) == nil && isJSONObject(decoded)
}

func validJSONArray(value string) bool {
	var decoded any
	return json.Unmarshal([]byte(value), &decoded) == nil && isJSONArray(decoded)
}

func validatePersistedStandardExecutionPackage(ctx context.Context, q *gen.Queries, dispatch cleardev.Dispatch) (cleardev.StandardExecutionPackage, bool, error) {
	var empty cleardev.StandardExecutionPackage
	if !requirementDigestMatches(dispatch.ExecutionPackageJSON, dispatch.ExecutionPackageSHA256) {
		return empty, false, nil
	}
	pack, err := cleardev.ParseStandardExecutionPackage([]byte(dispatch.ExecutionPackageJSON))
	if err != nil {
		return empty, false, nil
	}
	version, err := q.GetClearDevRequirementVersion(ctx, dispatch.RequirementVersionID)
	if err != nil {
		return empty, false, err
	}
	planRow, err := q.GetClearDevStandardEngineeringPlan(ctx, dispatch.EngineeringPlanID)
	if err != nil {
		return empty, false, err
	}
	plan := clearDevStandardEngineeringPlanFromGen(planRow)
	if !requirementDigestMatches(plan.PlanJSON, plan.PlanSHA256) {
		return empty, false, nil
	}
	var planResult cleardev.EngineeringPlanResult
	if err := json.Unmarshal([]byte(plan.PlanJSON), &planResult); err != nil {
		return empty, false, nil
	}
	valid := pack.DispatchID == dispatch.ID && pack.TaskID == dispatch.PreallocatedDevelopmentTaskID &&
		pack.RequirementVersionID == dispatch.RequirementVersionID && pack.RequirementSHA256 == version.Sha256 &&
		pack.RequirementText == version.ContractText && pack.PlanID == plan.ID && pack.PlanSHA256 == plan.PlanSHA256 &&
		plan.RequirementVersionID == version.ID && plan.RequirementSHA256 == version.Sha256 &&
		planResult.RequirementVersionID == version.ID && planResult.RequirementSHA256 == version.Sha256 &&
		planResult.Mode == string(cleardev.WorkModeStandard) && cleardev.EqualStandardTaskPlan(pack.Task, planResult.Task)
	return pack, valid, nil
}

func dispatchAcceptanceMatchesPackage(command cleardev.DispatchAcceptance, pack cleardev.StandardExecutionPackage) bool {
	task := command.InitialTask.Task
	permission := command.InitialTask.Permission.Rules
	if task.Title != pack.Task.Title || task.MaxReworkCount != pack.Task.MaxReworkCount ||
		!slices.Equal(permission.WritePaths, pack.Task.WritePaths) ||
		!slices.Equal(permission.GeneratedPaths, pack.Task.GeneratedPaths) ||
		!slices.Equal(permission.SharedPathsRequireApproval, pack.Task.SharedPathsRequireApproval) ||
		!slices.Equal(permission.ForbiddenPaths, pack.Task.ForbiddenPaths) ||
		len(command.InitialTask.Checks) != len(pack.Task.RequiredChecks) ||
		command.IntegrationCheck.Name != pack.Task.IntegrationCheck.Name {
		return false
	}
	for index, spec := range pack.Task.RequiredChecks {
		check := command.InitialTask.Checks[index]
		if check.Name != spec.Name || check.Kind != "REQUIRED_CHECK" {
			return false
		}
	}
	return command.IntegrationCheck.Kind == "INTEGRATION"
}

func validateStandardCheckRunRequest(ctx context.Context, q *gen.Queries, dispatch cleardev.Dispatch, run cleardev.CandidateCheckRun) (bool, error) {
	pack, valid, err := validatePersistedStandardExecutionPackage(ctx, q, dispatch)
	if err != nil || !valid {
		return false, err
	}
	var argv []string
	if err := json.Unmarshal([]byte(run.ArgvJSON), &argv); err != nil {
		return false, nil
	}
	matchSpec := func(spec cleardev.StandardCheckSpec) bool {
		return run.Name == spec.Name && slices.Equal(argv, spec.Argv) && run.CheckSpecSHA256 == standardJSONDigest(spec)
	}
	switch run.Kind {
	case cleardev.CandidateCheckRequired:
		if run.CandidateCommitID == "" {
			return false, nil
		}
		for _, spec := range pack.Task.RequiredChecks {
			if matchSpec(spec) {
				return true, nil
			}
		}
		return false, nil
	case cleardev.CandidateCheckIntegration:
		return run.CandidateCommitID != "" && matchSpec(pack.Task.IntegrationCheck), nil
	case cleardev.CandidateCheckScope:
		if len(argv) != 0 {
			return false, nil
		}
		switch {
		case run.Name == "scope" && run.CandidateCommitID != "":
			spec := struct {
				WritePaths                 []string `json:"writePaths"`
				GeneratedPaths             []string `json:"generatedPaths"`
				SharedPathsRequireApproval []string `json:"sharedPathsRequireApproval"`
				ForbiddenPaths             []string `json:"forbiddenPaths"`
			}{pack.Task.WritePaths, pack.Task.GeneratedPaths, pack.Task.SharedPathsRequireApproval, pack.Task.ForbiddenPaths}
			return run.CheckSpecSHA256 == standardJSONDigest(spec), nil
		case run.Name == "review-worktree" && run.CandidateCommitID != "":
			return run.CheckSpecSHA256 == standardJSONDigest(struct {
				Kind string `json:"kind"`
			}{Kind: "review-worktree"}), nil
		case strings.HasPrefix(run.Name, "scope-round-") && run.CandidateCommitID == "" && run.CandidateCommitSHA == "":
			round, parseErr := strconv.Atoi(strings.TrimPrefix(run.Name, "scope-round-"))
			if parseErr != nil || round < 0 {
				return false, nil
			}
			task, taskErr := q.GetClearDevDevelopmentTask(ctx, dispatch.PreallocatedDevelopmentTaskID)
			if taskErr != nil {
				return false, taskErr
			}
			spec := struct {
				Kind       string   `json:"kind"`
				WritePaths []string `json:"writePaths"`
			}{Kind: "git-name-status", WritePaths: pack.Task.WritePaths}
			return int(task.ReworkCount) == round && run.CheckSpecSHA256 == standardJSONDigest(spec), nil
		default:
			return false, nil
		}
	default:
		return false, nil
	}
}

func standardJSONDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func isJSONObject(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

func isJSONArray(value any) bool {
	_, ok := value.([]any)
	return ok
}

func terminalAgentStepTime(step cleardev.AgentStep) time.Time {
	if step.CompletedAt != nil {
		return *step.CompletedAt
	}
	if step.FailedAt != nil {
		return *step.FailedAt
	}
	return step.RequestedAt
}

func nullableBool(value bool) sql.NullBool {
	return sql.NullBool{Bool: value, Valid: true}
}

func intPtrToInt64(value *int) *int64 {
	if value == nil {
		return nil
	}
	converted := int64(*value)
	return &converted
}

func validDispatchInitialTask(dispatch cleardev.Dispatch, command cleardev.DispatchAcceptance) bool {
	task := command.InitialTask.Task
	permission := command.InitialTask.Permission
	if task.ID != dispatch.PreallocatedDevelopmentTaskID || strings.TrimSpace(task.Title) == "" ||
		task.Mode != cleardev.WorkModeStandard || task.Status != cleardev.DevelopmentTaskStatusPlanned ||
		task.PausedFromStatus != "" || task.MaxReworkCount != 1 || task.ReworkCount != 0 ||
		permission.DevelopmentTaskID != task.ID || permission.Version != 1 || strings.TrimSpace(permission.ID) == "" {
		return false
	}
	if err := permission.Rules.Validate(); err != nil {
		return false
	}
	if command.IntegrationCheck.DevelopmentTaskID != task.ID || strings.TrimSpace(command.IntegrationCheck.ID) == "" ||
		strings.TrimSpace(command.IntegrationCheck.Name) == "" || command.IntegrationCheck.Kind != "INTEGRATION" {
		return false
	}
	seen := map[string]bool{command.IntegrationCheck.Name: true}
	if len(command.InitialTask.Checks) == 0 {
		return false
	}
	for _, check := range command.InitialTask.Checks {
		if check.DevelopmentTaskID != task.ID || strings.TrimSpace(check.ID) == "" || strings.TrimSpace(check.Name) == "" ||
			check.Kind != "REQUIRED_CHECK" || seen[check.Name] {
			return false
		}
		seen[check.Name] = true
	}
	return true
}

func isCurrentStandardCandidate(ctx context.Context, q *gen.Queries, taskID, candidateID string) bool {
	candidate, err := q.GetCurrentClearDevCandidateCommit(ctx, taskID)
	if err != nil || candidate.ID != candidateID {
		return false
	}
	current, err := q.IsClearDevCandidateCurrentRound(ctx, gen.IsClearDevCandidateCurrentRoundParams{CandidateID: candidateID, DevelopmentTaskID: taskID})
	return err == nil && current != 0
}

func standardCompletionRuns(rows []gen.CleardevCandidateCheckRun, candidateID string, checks []gen.ListClearDevRequiredChecksRow) (string, bool) {
	scopePass := false
	integrationRunID := ""
	required := make(map[string]bool)
	for _, check := range checks {
		if check.CheckKind != "INTEGRATION" {
			required[check.Name] = false
		}
	}
	for _, row := range rows {
		run := clearDevCandidateCheckRunFromGen(row)
		if run.CandidateCommitID != candidateID || run.Status != cleardev.CandidateCheckRunStatusSettled || run.Result != cleardev.EvidenceResultPass {
			continue
		}
		switch run.Kind {
		case cleardev.CandidateCheckScope:
			if run.Name == "scope" {
				scopePass = true
			}
		case cleardev.CandidateCheckRequired:
			if _, ok := required[run.Name]; ok {
				required[run.Name] = true
			}
		case cleardev.CandidateCheckIntegration:
			integrationRunID = run.ID
		}
	}
	if !scopePass || integrationRunID == "" {
		return "", false
	}
	for _, passed := range required {
		if !passed {
			return "", false
		}
	}
	return integrationRunID, true
}

func hasPassingStandardLocalReview(rows []gen.CleardevStandardLocalReview, candidateID string) bool {
	for _, row := range rows {
		review := clearDevStandardLocalReviewFromGen(row)
		if review.CandidateCommitID == candidateID && review.Status == cleardev.LocalReviewStatusSettled && review.Verdict == cleardev.LocalReviewPass {
			return true
		}
	}
	return false
}
