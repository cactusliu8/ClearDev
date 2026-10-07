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

type extraCoordinationStore interface {
	ReadClearDevExtraCoordination(context.Context, string) (core.ExtraCoordinationState, error)
	RequestClearDevExtraCoordination(context.Context, string, string, core.ExtraCoordinationBinding, time.Time) error
	ValidateClearDevExtraCoordinationBeforeSend(context.Context, string, string) (core.ExtraCoordinationBinding, bool, error)
}

func (s *Service) addExtraCoordinationView(ctx context.Context, id string, view *core.WorkflowRecoveryView) error {
	store, ok := s.complexExecution.(extraCoordinationStore)
	if !ok {
		return nil
	}
	state, err := store.ReadClearDevExtraCoordination(ctx, id)
	if err != nil {
		return err
	}
	view.History = append(view.History, state.History...)
	if state.Option.Action == "" {
		return nil
	}
	if state.Option.UnavailableReason == "" {
		state.Option.UnavailableReason = s.extraCoordinationReady(ctx, state.Binding, false)
	}
	view.Options = append(view.Options, state.Option)
	return nil
}

func (s *Service) requestExtraCoordination(ctx context.Context, id string, input WorkflowRecoveryInput) (core.WorkflowRecoveryView, error) {
	empty := core.WorkflowRecoveryView{}
	if input.RequestID == "" || strings.TrimSpace(input.RequestID) != input.RequestID || len(input.RequestID) > 100 || strings.TrimSpace(input.Supplement) == "" || len(input.Supplement) > 16000 {
		return empty, apierr.Invalid("RECOVERY_CONTEXT_REQUIRED", "请说明为何需要一次额外协调；本操作只申请桌面决定", nil)
	}
	store, ok := s.complexExecution.(extraCoordinationStore)
	if !ok {
		return empty, apierr.Internal("RECOVERY_UNAVAILABLE", "Extra coordination storage is unavailable")
	}
	state, err := store.ReadClearDevExtraCoordination(ctx, id)
	if err != nil {
		return empty, err
	}
	for _, r := range state.History {
		if r.ID != input.RequestID {
			continue
		}
		if r.ExecutionRunID != input.ExecutionRunID || r.Action != input.Action || r.TargetID != input.TargetID || r.Supplement != input.Supplement {
			return empty, apierr.Conflict("RECOVERY_REQUEST_CHANGED", "同一请求不能更换绑定或说明", nil)
		}
		return s.GetWorkflowRecovery(ctx, id)
	}
	if state.Option.Action != input.Action || state.Option.TargetID != input.TargetID || state.Binding.ExecutionRunID != input.ExecutionRunID {
		return empty, apierr.Conflict("RECOVERY_NOT_CURRENT", "当前协调或候选已变化，请刷新", nil)
	}
	if reason := state.Option.UnavailableReason; reason != "" {
		return empty, apierr.Conflict(reason, "当前不能申请额外协调", nil)
	}
	if reason := s.extraCoordinationReady(ctx, state.Binding, false); reason != "" {
		return empty, apierr.Conflict(reason, "仍有未确认结果或原会话不匹配", nil)
	}
	if err := store.RequestClearDevExtraCoordination(ctx, input.RequestID, input.Supplement, state.Binding, s.now().UTC()); err != nil {
		return empty, mapStoreError(err, "EXTRA_COORDINATION_REJECTED")
	}
	// The existing native decision dispatcher displays the request. Only a real
	// desktop approval schedules the run; do not start work from this intent.
	return s.GetWorkflowRecovery(ctx, id)
}

func (s *Service) extraCoordinationReady(ctx context.Context, b core.ExtraCoordinationBinding, preflight bool) string {
	e, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, b.DevelopmentRequirementID)
	if err != nil || !found || e.Run.ID != b.ExecutionRunID || e.PlannerRuntime == nil || s.workflowRecoveryCurrent(ctx, e) != nil {
		return "EXECUTION_NOT_CURRENT"
	}
	if e.FinalReview != nil {
		settled, err := s.plannerHistoricalFinalCommandsSettled(ctx, e)
		if err != nil || !settled {
			return "RESULT_NOT_SETTLED"
		}
	}
	for _, event := range e.PlannerRuntime.Events {
		if event.ID != b.EventID {
			continue
		}
		for _, check := range e.CheckRuns {
			if check.DispatchID == event.DispatchID && check.Status == core.ComplexExecutionCheckRunFailed {
				settled, err := s.workflowCheckSettled(ctx, e, check.ID)
				if err != nil || !settled {
					return "RESULT_NOT_SETTLED"
				}
			}
		}
	}
	rec, found, err := s.ao.GetSession(ctx, domain.SessionID(b.AOSessionID))
	if err != nil || !found || rec.CreationRequestFingerprint != b.CreationFingerprint || rec.PermissionMode != domain.PermissionModeAuto {
		return "ORIGINAL_PLANNER_UNAVAILABLE"
	}
	state := core.PlanningStepRecoveryState{Binding: core.PlanningStepRecoveryBinding{RequirementID: b.DevelopmentRequirementID, AOSessionID: b.AOSessionID, ProviderConversationID: b.ProviderConversationID, WorkspacePath: b.WorkspacePath, SessionCreationKey: b.SessionCreationKey, Harness: b.Harness, Model: b.Model}}
	if reason := s.planningRecoveryReady(ctx, state, false); reason != "" {
		return reason
	}
	if preflight {
		blocked, model, err := s.inspectConfiguredPreflight(ctx, b.DevelopmentRequirementID, b.PlannerRoleBindingID, rec.ProjectID, rec.Kind)
		if err != nil || blocked || model != b.Model {
			return "PREFLIGHT_OR_DELIVERY_REQUIRED"
		}
	}
	return ""
}

func (s *Service) checkExtraCoordinationBeforeSend(ctx context.Context, id, stepID string) error {
	store, ok := s.complexExecution.(extraCoordinationStore)
	if !ok {
		return nil
	}
	b, registered, err := store.ValidateClearDevExtraCoordinationBeforeSend(ctx, id, stepID)
	if err != nil {
		return fmt.Errorf("%w: %w", errAgentRecoveryDeferred, err)
	}
	if registered {
		if reason := s.extraCoordinationReady(ctx, b, true); reason != "" {
			return fmt.Errorf("%w: %s", errAgentRecoveryDeferred, reason)
		}
	}
	return nil
}
