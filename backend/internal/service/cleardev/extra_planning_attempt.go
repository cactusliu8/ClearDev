package cleardev

import (
	"context"
	"errors"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type extraPlanningAttemptStore interface {
	ReadClearDevExtraPlanningAttempt(context.Context, string, time.Time) (core.ExtraPlanningAttemptState, error)
	RequestClearDevExtraPlanningAttempt(context.Context, string, core.WorkflowRecovery, string, time.Time) error
	ContinueClearDevExtraPlanningAttempt(context.Context, string, core.WorkflowRecovery, time.Time) error
	ReadClearDevExtraPlanningAttemptBeforeSend(context.Context, string, string, time.Time) (core.ExtraPlanningAttemptState, bool, error)
}

func extraPlanningSource(state core.ExtraPlanningAttemptState) core.PlanningStepRecoveryState {
	return core.PlanningStepRecoveryState{Binding: state.Binding.Source, Selection: state.Selection, Messages: state.Messages}
}

func (s *Service) getExtraPlanningAttempt(ctx context.Context, id string) (core.WorkflowRecoveryView, error) {
	out := core.WorkflowRecoveryView{Options: []core.WorkflowRecoveryOption{}, History: []core.WorkflowRecovery{}}
	store, ok := s.complex.(extraPlanningAttemptStore)
	if !ok {
		return out, nil
	}
	state, err := store.ReadClearDevExtraPlanningAttempt(ctx, id, s.now().UTC())
	if err != nil {
		return out, err
	}
	out.History = state.History
	if state.Option.Action != "" {
		if state.Option.UnavailableReason == "" || state.Option.UnavailableReason == "EXTRA_ATTEMPT_DECISION_PENDING" || state.Option.UnavailableReason == "CONTINUATION_REGISTERED" {
			if s.desktopRunID == "" && state.Option.Action == core.RecoveryRequestExtraPlanningAttempt {
				state.Option.UnavailableReason = "DESKTOP_UNAVAILABLE"
			} else if reason := s.planningRecoveryReady(ctx, extraPlanningSource(state), false); reason != "" {
				state.Option.UnavailableReason = reason
			}
		}
		out.Options = append(out.Options, state.Option)
	}
	return out, nil
}

func (s *Service) requestExtraPlanningAttempt(ctx context.Context, id string, input WorkflowRecoveryInput) (core.WorkflowRecoveryView, error) {
	empty := core.WorkflowRecoveryView{}
	input.Supplement = strings.TrimSpace(input.Supplement)
	if input.ExecutionRunID != "" || input.RequestID == "" || len(input.RequestID) > 100 || strings.TrimSpace(input.RequestID) != input.RequestID || input.TargetID == "" || len(input.TargetID) > 600 || len(input.Supplement) > 16000 {
		return empty, apierr.Invalid("RECOVERY_REQUEST_INVALID", "请使用原讨论或编译步骤的准确请求", nil)
	}
	store, ok := s.complex.(extraPlanningAttemptStore)
	if !ok {
		return empty, apierr.Internal("RECOVERY_UNAVAILABLE", "Extra planning recovery is unavailable")
	}
	state, err := store.ReadClearDevExtraPlanningAttempt(ctx, id, s.now().UTC())
	if err != nil {
		return empty, err
	}
	replay := false
	var replaySuccessor string
	for _, prior := range state.History {
		if prior.ID != input.RequestID {
			continue
		}
		if prior.Action != input.Action || prior.TargetID != input.TargetID || prior.Supplement != input.Supplement {
			return empty, apierr.Conflict("RECOVERY_REQUEST_CHANGED", "同一请求不能更换动作、步骤或补充内容", nil)
		}
		replay = true
		replaySuccessor = prior.SuccessorID
	}
	if replay && input.Action == core.RecoveryRequestExtraPlanningAttempt {
		return s.getPlanningStepRecovery(ctx, id)
	}
	if replay && input.Action == core.RecoveryContinueExtraPlanningAttempt {
		view, err := s.attempts.GetClearDevAgentAttemptState(ctx, id, replaySuccessor)
		if err != nil {
			return empty, err
		}
		if view.LastEventID != "" {
			// An established delivery boundary belongs to the original observer.
			// It cannot acquire another reservation, message or continuation.
			s.scheduleComplexFlow(id)
			return s.getPlanningStepRecovery(ctx, id)
		}
	}
	if state.Option.Action != input.Action || state.Option.TargetID != input.TargetID {
		return empty, apierr.Conflict("RECOVERY_NOT_CURRENT", "原步骤或授权已变化，请刷新", nil)
	}
	if reason := state.Option.UnavailableReason; reason != "" && (!replay || reason != "CONTINUATION_REGISTERED") {
		return empty, apierr.Conflict(reason, "原步骤暂不能安全继续，请查看原因", nil)
	}
	prepare := input.Action == core.RecoveryContinueExtraPlanningAttempt
	if !prepare && s.desktopRunID == "" {
		return empty, apierr.Conflict("DESKTOP_UNAVAILABLE", "需要连接桌面原生人工决定通道", nil)
	}
	if reason := s.planningRecoveryReady(ctx, extraPlanningSource(state), prepare); reason != "" {
		return empty, apierr.Conflict(reason, "原会话或来源暂不能安全继续", nil)
	}
	recovery := core.WorkflowRecovery{ID: input.RequestID, Action: input.Action, TargetID: input.TargetID, Supplement: input.Supplement}
	if prepare {
		err = store.ContinueClearDevExtraPlanningAttempt(ctx, id, recovery, s.now().UTC())
	} else {
		err = store.RequestClearDevExtraPlanningAttempt(ctx, id, recovery, s.newID(), s.now().UTC())
	}
	if err != nil {
		return empty, mapStoreError(err, "EXTRA_PLANNING_ATTEMPT_REJECTED")
	}
	if prepare {
		s.scheduleComplexFlow(id)
	}
	return s.getPlanningStepRecovery(ctx, id)
}

func (s *Service) checkExtraPlanningAttemptBeforeSend(ctx context.Context, id, stepID string) error {
	store, ok := s.complex.(extraPlanningAttemptStore)
	if !ok {
		return errors.Join(errAgentRecoveryDeferred, errors.New("extra planning store unavailable"))
	}
	state, found, err := store.ReadClearDevExtraPlanningAttemptBeforeSend(ctx, id, stepID, s.now().UTC())
	if err != nil {
		return errors.Join(errAgentRecoveryDeferred, err)
	}
	if !found {
		return errors.Join(errAgentRecoveryDeferred, errors.New("extra planning continuation missing"))
	}
	if state.Option.UnavailableReason != "" && state.Option.UnavailableReason != "CONTINUATION_REGISTERED" {
		return errors.Join(errAgentRecoveryDeferred, errors.New(state.Option.UnavailableReason))
	}
	if reason := s.planningRecoveryReady(ctx, extraPlanningSource(state), true); reason != "" {
		return errors.Join(errAgentRecoveryDeferred, errors.New(reason))
	}
	return nil
}

func (s *Service) registeredUnsentExtraPlanningAttempt(ctx context.Context, id string, view core.AgentStepAttemptView) (bool, error) {
	if view.AttemptNumber != 3 || view.StepCategory != core.AgentStepCategoryComplexPlanning || view.StepKind != core.ComplexAgentStepCompilation || view.SendStatus != core.AgentAttemptPending || view.LastEventID != "" {
		return false, nil
	}
	store, ok := s.complex.(extraPlanningAttemptStore)
	if !ok {
		return false, nil
	}
	state, err := store.ReadClearDevExtraPlanningAttempt(ctx, id, s.now().UTC())
	if err != nil {
		return false, err
	}
	for _, h := range state.History {
		if h.Action == core.RecoveryContinueExtraPlanningAttempt && h.StepID == view.LogicalStepID && h.SuccessorID == view.ID {
			return true, nil
		}
	}
	return false, nil
}
