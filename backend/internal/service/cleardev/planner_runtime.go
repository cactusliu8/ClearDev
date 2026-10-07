package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func parseRuntimeExecutionBuilderResult(run core.ComplexExecutionRun, raw []byte, dispatchID, taskID string, round int) (core.BuilderResult, error) {
	enabled, err := core.PlannerRuntimeRun(run)
	if err != nil {
		return core.BuilderResult{}, err
	}
	if !enabled {
		return core.ParseComplexExecutionBuilderResult(raw, dispatchID, taskID, round)
	}
	result, _, err := core.ParsePlannerRuntimeBuilderResult(raw, dispatchID, taskID, round)
	return result, err
}

func validateRuntimeBuilderOrScopeRequest(raw []byte, execution core.ComplexExecutionSnapshot, dispatch core.ComplexExecutionDispatch, task core.ComplexExecutionTask) error {
	enabled, err := core.PlannerRuntimeRun(execution.Run)
	if err != nil {
		return err
	}
	var envelope struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if !enabled || envelope.Kind != "BUILDER_RESULT" {
		return validateBuilderOrScopeRequest(raw, dispatch.ID, task.DevelopmentTaskID, dispatch.Round,
			execution.Run.RequirementVersionID, execution.Run.RequirementSHA256, execution.Run.PlanID, execution.Run.PlanSHA256)
	}
	_, report, err := core.ParsePlannerRuntimeBuilderResult(raw, dispatch.ID, task.DevelopmentTaskID, dispatch.Round)
	if err != nil || report == nil {
		return err
	}
	for _, key := range report.AffectedTaskKeys {
		if _, ok := complexExecutionTaskByKey(execution.Tasks, key); !ok {
			return fmt.Errorf("coordination references task %q outside the current immutable execution", key)
		}
	}
	return nil
}

// advancePlannerRuntime is a sparse intervention, not a scheduling round.
// Routine completion/dependency/idle/check/review advancement never calls a
// Planner. Pending observations first let existing attempts settle normally.
func (s *Service) advancePlannerRuntime(ctx context.Context, execution core.ComplexExecutionSnapshot) (handled, changed, done bool, err error) {
	if core.BuilderFirstFailureEnabled(execution.Run) {
		if capture, ok := s.complexExecution.(interface {
			CaptureClearDevProjectPlannerEvent(context.Context, string) (bool, error)
		}); ok {
			changed, err := capture.CaptureClearDevProjectPlannerEvent(ctx, execution.Run.ID)
			if err != nil || changed {
				return true, changed, false, err
			}
		}
	}
	blocked, reason := core.PlannerRuntimeBarrier(execution)
	if !blocked {
		return false, false, false, nil
	}
	if reason != core.ReasonPlannerRuntimePending {
		return true, false, true, nil
	}
	if execution.FinalReview != nil && core.BuilderFirstFailureEnabled(execution.Run) {
		settled, err := s.plannerHistoricalFinalCommandsSettled(ctx, execution)
		if err != nil || !settled {
			return true, false, true, err
		}
	}
	if !core.PlannerRuntimeQuiescent(execution) {
		return false, false, false, nil
	}
	store, supported := s.complexExecution.(PlannerRuntimeFactStore)
	if !supported || execution.PlannerRuntime == nil {
		return true, false, true, errors.New("runtime coordination has no readable transactional fact store")
	}
	var event core.PlannerCoordinationEvent
	for _, candidate := range execution.PlannerRuntime.Events {
		_, resolved := core.PlannerRuntimeEffectiveDecision(execution.PlannerRuntime, candidate.ID)
		if !resolved {
			event = candidate
			break
		}
	}
	if event.ID == "" {
		return true, false, true, errors.New("runtime coordination history has an incomplete application")
	}
	if sourceErr := s.failureCoordinationStillCurrent(ctx, execution, event.ID); sourceErr != nil {
		stopped, stopErr := store.StopClearDevPlannerRuntime(ctx, event.ID, core.ReasonPlannerRuntimeStale, "Original failure source or native completion changed; human inspection is required before further coordination.", s.now().UTC())
		return true, stopped, true, stopErr
	}
	request, prepared, err := store.PrepareClearDevPlannerRuntime(ctx, event.ID, s.now().UTC())
	if err != nil {
		return true, prepared, false, err
	}
	if request.EventID == "" {
		return true, prepared, !prepared, nil // A durable stop or quota decision was recorded.
	}
	planning, found, err := s.complex.GetClearDevComplexPlanning(ctx, execution.Run.DevelopmentRequirementID)
	if err != nil || !found {
		return true, prepared, false, err
	}
	var planner core.ComplexRoleBinding
	for _, binding := range planning.RoleBindings {
		if binding.ID == request.PlannerRoleBindingID && binding.Role == core.StandardRoleEngineeringPlanner &&
			binding.AOSessionID == request.AOSessionID && binding.Status == core.RoleBindingStatusBound {
			planner = binding
			break
		}
	}
	if planner.ID == "" {
		stopped, err := store.StopClearDevPlannerRuntime(ctx, event.ID, core.ReasonPlannerRuntimeUnavailable,
			"The original Stage Planner binding no longer matches the frozen runtime request; no replacement session was allocated.", s.now().UTC())
		return true, prepared || stopped, true, err
	}
	requirement, found, err := s.facts.GetClearDevRequirement(ctx, execution.Run.DevelopmentRequirementID)
	if err != nil || !found {
		return true, prepared, false, err
	}
	validate := func(raw []byte) error {
		_, _, _, err := core.ParsePlannerCoordinationResult(raw, event, request.ContextSHA256)
		return err
	}
	message, progressed, stepErr := s.runComplexAgentStep(ctx, planning, planner, event.ID, core.ComplexAgentStepEngineeringPlan, request.Prompt,
		standardStepReasons{Invalid: "PLANNER_COORDINATION_RESULT_INVALID", Timeout: "PLANNER_COORDINATION_TIMEOUT",
			Unavailable: core.ReasonPlannerRuntimeUnavailable, ProjectID: requirement.Requirement.AOProjectID, SessionKind: domain.KindWorker}, validate)
	if stepErr != nil && !errors.Is(stepErr, errComplexStopped) {
		return true, prepared || progressed, false, stepErr
	}
	if message.StepID == "" {
		// Preserve the existing finite retry/delivery-unknown rules. Only a
		// logically FAILED step is terminal; a pending recovery is not a new
		// request, a refund or permission to send directly.
		latest, found, readErr := s.complex.GetClearDevComplexPlanning(ctx, execution.Run.DevelopmentRequirementID)
		if readErr != nil || !found {
			return true, prepared || progressed, false, readErr
		}
		step, exists := core.ComplexAgentStepByRequest(latest, core.ComplexAgentStepEngineeringPlan, event.ID)
		if exists && step.SendStatus == core.AgentStepSendStatusFailed {
			stopped, stopErr := store.StopClearDevPlannerRuntime(ctx, event.ID, core.ReasonPlannerRuntimeUnavailable,
				"The bounded Planner controlled step failed ("+string(step.ReasonCode)+"); original message and attempt reservations remain consumed.", s.now().UTC())
			return true, prepared || progressed || stopped, true, stopErr
		}
		return true, prepared || progressed, true, errComplexExecutionStopped
	}
	if sourceErr := s.failureCoordinationStillCurrent(ctx, execution, event.ID); sourceErr != nil {
		stopped, stopErr := store.StopClearDevPlannerRuntime(ctx, event.ID, core.ReasonPlannerRuntimeStale, "Original failure source changed while the Planner was responding; no amendment or continuation was applied.", s.now().UTC())
		return true, stopped, true, stopErr
	}
	applied, err := store.ApplyClearDevPlannerRuntime(ctx, event.ID, s.now().UTC())
	return true, prepared || progressed || applied, false, err
}

// Coordination can follow a contract revision while the last final review still
// belongs to the previous candidate. Read its receipts with its frozen contract,
// after checking that the packet belongs to this same immutable execution.
func (s *Service) plannerHistoricalFinalCommandsSettled(ctx context.Context, execution core.ComplexExecutionSnapshot) (bool, error) {
	review := execution.FinalReview
	var packet core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		return false, err
	}
	execution.Run.RuntimeProjectExecution = packet.Run.RuntimeProjectExecution
	if err := core.ValidateRequirementFinalReviewBinding(*review, execution.Run, review.CandidateCommitSHA, packet.CheckRunIDs); err != nil {
		return false, err
	}
	return s.builderFirstFinalCommandsSettled(ctx, execution)
}
