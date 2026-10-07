package cleardev

import (
	"context"
	"errors"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type planningStepRecoveryStore interface {
	ReadClearDevPlanningStepRecovery(context.Context, string, time.Time) (core.PlanningStepRecoveryState, error)
	ApplyClearDevPlanningStepRecovery(context.Context, string, string, core.PlanningStepRecoveryBinding, time.Time) error
	ReadClearDevPlanningRecoveryBeforeSend(context.Context, string, string, time.Time) (core.PlanningStepRecoveryState, bool, error)
}

func (s *Service) getPlanningStepRecovery(ctx context.Context, id string) (core.WorkflowRecoveryView, error) {
	out := core.WorkflowRecoveryView{Options: []core.WorkflowRecoveryOption{}, History: []core.WorkflowRecovery{}}
	store, ok := s.complex.(planningStepRecoveryStore)
	if !ok {
		return out, nil
	}
	state, err := store.ReadClearDevPlanningStepRecovery(ctx, id, s.now().UTC())
	if err != nil {
		return out, err
	}
	out.History = state.History
	if state.Option.Action != "" {
		if state.Option.UnavailableReason == "" {
			state.Option.UnavailableReason = s.planningRecoveryReady(ctx, state, false)
		}
		out.Options = append(out.Options, state.Option)
	}
	extra, err := s.getExtraPlanningAttempt(ctx, id)
	if err != nil {
		return out, err
	}
	out.Options = append(out.Options, extra.Options...)
	out.History = append(out.History, extra.History...)
	return out, nil
}

func (s *Service) requestPlanningStepRecovery(ctx context.Context, id string, input WorkflowRecoveryInput) (core.WorkflowRecoveryView, error) {
	empty := core.WorkflowRecoveryView{}
	input.Supplement = strings.TrimSpace(input.Supplement)
	if input.ExecutionRunID != "" || input.RequestID == "" || len(input.RequestID) > 100 || strings.TrimSpace(input.RequestID) != input.RequestID || input.TargetID == "" || len(input.Supplement) > 16000 {
		return empty, apierr.Invalid("RECOVERY_REQUEST_INVALID", "请使用原讨论或编译步骤的准确恢复请求", nil)
	}
	store, ok := s.complex.(planningStepRecoveryStore)
	if !ok {
		return empty, apierr.Internal("RECOVERY_UNAVAILABLE", "Planning recovery is unavailable")
	}
	state, err := store.ReadClearDevPlanningStepRecovery(ctx, id, s.now().UTC())
	if err != nil {
		return empty, err
	}
	for _, prior := range state.History {
		if prior.ID == input.RequestID {
			if prior.TargetID != input.TargetID || prior.Supplement != input.Supplement {
				return empty, apierr.Conflict("RECOVERY_REQUEST_CHANGED", "同一请求不能更换步骤或补充内容", nil)
			}
			s.scheduleComplexFlow(id)
			return s.getPlanningStepRecovery(ctx, id)
		}
	}
	if state.Option.Action != core.RecoveryRetryPlanningStep || state.Option.TargetID != input.TargetID {
		return empty, apierr.Conflict("RECOVERY_NOT_CURRENT", "原讨论或编译已经变化，请刷新进度", nil)
	}
	if reason := state.Option.UnavailableReason; reason != "" {
		return empty, apierr.Conflict(reason, "原步骤暂不能安全续行，请查看恢复原因", nil)
	}
	if reason := s.planningRecoveryReady(ctx, state, true); reason != "" {
		return empty, apierr.Conflict(reason, "原会话、来源或运行配置尚不能安全继续", nil)
	}
	if err := store.ApplyClearDevPlanningStepRecovery(ctx, input.RequestID, input.Supplement, state.Binding, s.now().UTC()); err != nil {
		return empty, mapStoreError(err, "PLANNING_RECOVERY_REJECTED")
	}
	s.scheduleComplexFlow(id)
	return s.getPlanningStepRecovery(ctx, id)
}

// Eligibility reads never restore a session or perform preflight. An explicit
// request (and each as-yet-unreserved send) may use the original native recovery
// machinery, but it must return with the same provider conversation identity.
func (s *Service) planningRecoveryReady(ctx context.Context, state core.PlanningStepRecoveryState, prepare bool) string {
	b := state.Binding
	if state.Selection != nil && !s.selectedProjectCurrent(ctx, state.Selection) {
		return "PLANNING_SOURCE_CHANGED"
	}
	if prepare {
		view, err := s.attempts.GetClearDevAgentAttemptState(ctx, b.RequirementID, b.FirstAttemptID)
		if err != nil {
			return "RESULT_NOT_SETTLED"
		}
		if err := s.recoverOriginalAgentSession(ctx, b.RequirementID, view, true); err != nil {
			return "RECOVERY_SESSION_UNAVAILABLE"
		}
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(b.AOSessionID))
	if err != nil || !found || record.IsTerminated || !s.controlledSessionMatches(ctx, record) ||
		record.Metadata.ProviderConversationID != b.ProviderConversationID || b.ProviderConversationID == "" ||
		record.Metadata.WorkspacePath != b.WorkspacePath || record.CreationIdempotencyKey != b.SessionCreationKey ||
		string(record.Harness) != b.Harness || record.Metadata.Model != b.Model {
		return "RECOVERY_NATIVE_IDENTITY_MISSING"
	}
	if state.Selection != nil && !s.selectedProjectCurrent(ctx, state.Selection) {
		return "PLANNING_SOURCE_CHANGED"
	}
	if !s.productWorkspaceUnchanged(ctx, record) {
		return "PLANNING_SOURCE_CHANGED"
	}
	if !prepare && record.Activity.State == domain.ActivityExited && s.recoverAgentSession != nil {
		return ""
	}
	if record.Activity.State != domain.ActivityIdle {
		return "RESULT_NOT_SETTLED"
	}
	snapshot, err := s.chat.Snapshot(ctx, domain.SessionID(b.AOSessionID))
	if err != nil || string(snapshot.SessionID) != b.AOSessionID {
		return "RECOVERY_SESSION_UNAVAILABLE"
	}
	for _, turn := range snapshot.Turns {
		if turn.State != domain.TurnStateCompleted && turn.State != domain.TurnStateFailed && turn.State != domain.TurnStateInterrupted {
			return "RESULT_NOT_SETTLED"
		}
	}
	for _, expected := range state.Messages {
		matched := 0
		for _, message := range snapshot.Messages {
			if message.ClientMessageID != expected.ClientMessageID {
				continue
			}
			if message.Role != domain.MessageRoleUser || message.Origin != domain.MessageOriginAutomation ||
				message.TurnID != expected.TurnID || coreDigest([]byte(message.Text)) != expected.PromptSHA256 {
				return "RECOVERY_NATIVE_IDENTITY_MISSING"
			}
			matched++
		}
		if matched != 1 {
			return "RECOVERY_NATIVE_IDENTITY_MISSING"
		}
		turns := 0
		for _, turn := range snapshot.Turns {
			if turn.ID == expected.TurnID && (turn.HandledBySessionID == "" || string(turn.HandledBySessionID) == b.AOSessionID) {
				turns++
			}
		}
		if turns != 1 {
			return "RECOVERY_NATIVE_IDENTITY_MISSING"
		}
	}
	return ""
}

// A registered but wholly unattempted retry belongs to the explicit sender.
// Let that sender restore/preflight the original session without allowing the
// generic recovery sweep to turn a temporary restore failure into SESSION_LOST.
// Once any delivery boundary exists, the original observation rules take over.
func (s *Service) registeredUnsentPlanningRecovery(ctx context.Context, id string, view core.AgentStepAttemptView) (bool, error) {
	if view.StepCategory != core.AgentStepCategoryComplexPlanning || view.StepKind != core.ComplexAgentStepCompilation ||
		view.AttemptNumber != agentSecondAttemptNumber || view.SendStatus != core.AgentAttemptPending || view.LastEventID != "" {
		return false, nil
	}
	store, ok := s.complex.(planningStepRecoveryStore)
	if !ok {
		return false, nil
	}
	state, err := store.ReadClearDevPlanningStepRecovery(ctx, id, s.now().UTC())
	if err != nil {
		return false, err
	}
	for _, recovery := range state.History {
		if recovery.Action == core.RecoveryRetryPlanningStep && recovery.StepID == view.LogicalStepID && recovery.SuccessorID == view.ID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) checkPlanningRecoveryBeforeSend(ctx context.Context, id, stepID string) error {
	store, ok := s.complex.(planningStepRecoveryStore)
	if !ok {
		return nil
	}
	state, found, err := store.ReadClearDevPlanningRecoveryBeforeSend(ctx, id, stepID, s.now().UTC())
	if err != nil {
		return errors.Join(errAgentRecoveryDeferred, err)
	}
	if !found {
		return nil
	}
	if reason := s.planningRecoveryReady(ctx, state, true); reason != "" {
		return errors.Join(errAgentRecoveryDeferred, errors.New("planning recovery cannot send: "+reason))
	}
	return nil
}
