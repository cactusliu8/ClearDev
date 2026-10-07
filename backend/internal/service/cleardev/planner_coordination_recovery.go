package cleardev

import (
	"context"
	"fmt"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type plannerCoordinationRecoveryStore interface {
	ReadClearDevPlannerCoordinationRecovery(context.Context, string, time.Time) (core.PlannerCoordinationRecoveryState, error)
	ApplyClearDevPlannerCoordinationRecovery(context.Context, string, string, core.PlannerCoordinationRecoveryBinding, time.Time) error
	ReadClearDevPlannerCoordinationRecoveryBeforeSend(context.Context, string, string, time.Time) (core.PlannerCoordinationRecoveryState, bool, error)
}

func (s *Service) addPlannerCoordinationRecoveryView(ctx context.Context, id string, view *core.WorkflowRecoveryView) error {
	store, ok := s.complexExecution.(plannerCoordinationRecoveryStore)
	if !ok {
		return nil
	}
	state, err := store.ReadClearDevPlannerCoordinationRecovery(ctx, id, s.now().UTC())
	if err != nil {
		return err
	}
	view.History = append(view.History, state.History...)
	if state.Option.Action == "" {
		return nil
	}
	if state.Option.UnavailableReason == "" {
		state.Option.UnavailableReason = s.plannerCoordinationRecoveryReady(ctx, state, false)
	}
	view.Options = append(view.Options, state.Option)
	return nil
}

func (s *Service) requestPlannerCoordinationRecovery(ctx context.Context, id string, input WorkflowRecoveryInput) (core.WorkflowRecoveryView, error) {
	empty := core.WorkflowRecoveryView{}
	if input.RequestID == "" || len(input.RequestID) > 100 || strings.TrimSpace(input.RequestID) != input.RequestID || strings.TrimSpace(input.Supplement) == "" || len(input.Supplement) > 16000 {
		return empty, apierr.Invalid("RECOVERY_CONTEXT_REQUIRED", "请说明已修复的 Planner 执行环境；此操作不增加协调或消息额度", nil)
	}
	store, ok := s.complexExecution.(plannerCoordinationRecoveryStore)
	if !ok {
		return empty, apierr.Internal("RECOVERY_UNAVAILABLE", "Planner recovery storage is unavailable")
	}
	state, err := store.ReadClearDevPlannerCoordinationRecovery(ctx, id, s.now().UTC())
	if err != nil {
		return empty, err
	}
	for _, r := range state.History {
		if r.ID != input.RequestID {
			continue
		}
		if r.ExecutionRunID != input.ExecutionRunID || r.Action != input.Action || r.TargetID != input.TargetID || r.Supplement != input.Supplement {
			return empty, apierr.Conflict("RECOVERY_REQUEST_CHANGED", "同一恢复请求不能更换来源或补充说明", nil)
		}
		s.scheduleComplexStandardExecution(id)
		return s.GetWorkflowRecovery(ctx, id)
	}
	if state.Option.Action != input.Action || state.Option.TargetID != input.TargetID || state.Binding.ExecutionRunID != input.ExecutionRunID {
		return empty, apierr.Conflict("RECOVERY_NOT_CURRENT", "协调或原会话已变化，请刷新进度", nil)
	}
	if state.Option.UnavailableReason != "" {
		return empty, apierr.Conflict(state.Option.UnavailableReason, "原 Planner 暂不能安全续行", nil)
	}
	if reason := s.plannerCoordinationRecoveryReady(ctx, state, true); reason != "" {
		return empty, apierr.Conflict(reason, "原生会话、冻结来源或模型预检尚未满足", nil)
	}
	if err := store.ApplyClearDevPlannerCoordinationRecovery(ctx, input.RequestID, input.Supplement, state.Binding, s.now().UTC()); err != nil {
		return empty, mapStoreError(err, "COORDINATION_RECOVERY_REJECTED")
	}
	s.scheduleComplexStandardExecution(id)
	return s.GetWorkflowRecovery(ctx, id)
}

func (s *Service) plannerCoordinationRecoveryReady(ctx context.Context, state core.PlannerCoordinationRecoveryState, prepare bool) string {
	b := state.Binding
	e, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, b.RequirementID)
	if err != nil || !found || e.Run.ID != b.ExecutionRunID || s.workflowRecoveryCurrent(ctx, e) != nil {
		return "EXECUTION_NOT_CURRENT"
	}
	if e.FinalReview != nil {
		settled, err := s.plannerHistoricalFinalCommandsSettled(ctx, e)
		if err != nil || !settled {
			return "RESULT_NOT_SETTLED"
		}
	}
	rec, found, err := s.ao.GetSession(ctx, domain.SessionID(b.AOSessionID))
	if err != nil || !found || rec.PermissionMode != domain.PermissionModeAuto || rec.CreationRequestFingerprint != b.CreationFingerprint {
		return "RECOVERY_NATIVE_IDENTITY_MISSING"
	}
	// Reuse the exact native-history/preflight checks, not the compilation-only
	// admission rules. This continuation's source remains the executing project.
	compatible := core.PlanningStepRecoveryState{Binding: core.PlanningStepRecoveryBinding{
		RequirementID: b.RequirementID, FirstAttemptID: b.FirstAttemptID, AOSessionID: b.AOSessionID,
		ProviderConversationID: b.ProviderConversationID, WorkspacePath: b.WorkspacePath, SessionCreationKey: b.SessionCreationKey, Harness: b.Harness, Model: b.Model,
	}, Messages: state.Messages}
	return s.planningRecoveryReady(ctx, compatible, prepare)
}

func (s *Service) checkPlannerCoordinationRecoveryBeforeSend(ctx context.Context, id, stepID string) error {
	store, ok := s.complexExecution.(plannerCoordinationRecoveryStore)
	if !ok {
		return nil
	}
	state, registered, err := store.ReadClearDevPlannerCoordinationRecoveryBeforeSend(ctx, id, stepID, s.now().UTC())
	if err != nil {
		return fmt.Errorf("%w: %w", errAgentRecoveryDeferred, err)
	}
	if registered {
		if reason := s.plannerCoordinationRecoveryReady(ctx, state, true); reason != "" {
			return fmt.Errorf("%w: %s", errAgentRecoveryDeferred, reason)
		}
	}
	return nil
}

func (s *Service) registeredUnsentCoordinationRecovery(ctx context.Context, id string, view core.AgentStepAttemptView) (bool, error) {
	if view.StepCategory != core.AgentStepCategoryComplexPlanning || view.StepKind != core.ComplexAgentStepEngineeringPlan || view.AttemptNumber != 2 || view.SendStatus != core.AgentAttemptPending || view.LastEventID != "" {
		return false, nil
	}
	store, ok := s.complexExecution.(plannerCoordinationRecoveryStore)
	if !ok {
		return false, nil
	}
	state, err := store.ReadClearDevPlannerCoordinationRecovery(ctx, id, s.now().UTC())
	if err != nil {
		return false, err
	}
	for _, r := range state.History {
		if r.StepID == view.LogicalStepID && r.SuccessorID == view.ID {
			return true, nil
		}
	}
	return false, nil
}
