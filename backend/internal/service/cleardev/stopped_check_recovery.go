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

type stoppedCheckRecoveryStore interface {
	ReadClearDevStoppedCheckRecovery(context.Context, string) (core.StoppedCheckRecoveryState, error)
	RequestClearDevStoppedCheckRecovery(context.Context, string, string, core.StoppedCheckRecoveryBinding, time.Time) error
	ValidateClearDevStoppedCheckBeforeRun(context.Context, string, string) (core.StoppedCheckRecoveryBinding, bool, error)
}

func (s *Service) addStoppedCheckRecoveryView(ctx context.Context, id string, view *core.WorkflowRecoveryView) error {
	store, ok := s.complexExecution.(stoppedCheckRecoveryStore)
	if !ok {
		return nil
	}
	state, err := store.ReadClearDevStoppedCheckRecovery(ctx, id)
	if err != nil {
		return err
	}
	view.History = append(view.History, state.History...)
	if state.Option.Action != "" {
		if state.Option.UnavailableReason == "" {
			state.Option.UnavailableReason = s.stoppedCheckRecoveryReady(ctx, state.Binding)
		}
		view.Options = append(view.Options, state.Option)
	}
	return nil
}

func (s *Service) requestStoppedCheckRecovery(ctx context.Context, id string, input WorkflowRecoveryInput) (core.WorkflowRecoveryView, error) {
	empty := core.WorkflowRecoveryView{}
	if input.RequestID == "" || strings.TrimSpace(input.RequestID) != input.RequestID || len(input.RequestID) > 100 || strings.TrimSpace(input.Supplement) == "" || len(input.Supplement) > 16000 {
		return empty, apierr.Invalid("RECOVERY_CONTEXT_REQUIRED", "请说明原检查环境已怎样恢复；这只申请准确桌面决定", nil)
	}
	store, ok := s.complexExecution.(stoppedCheckRecoveryStore)
	if !ok {
		return empty, apierr.Internal("RECOVERY_UNAVAILABLE", "Stopped check recovery is unavailable")
	}
	state, err := store.ReadClearDevStoppedCheckRecovery(ctx, id)
	if err != nil {
		return empty, err
	}
	for _, r := range state.History {
		if r.ID != input.RequestID {
			continue
		}
		if r.ExecutionRunID != input.ExecutionRunID || r.Action != input.Action || r.TargetID != input.TargetID || r.Supplement != input.Supplement {
			return empty, apierr.Conflict("RECOVERY_REQUEST_CHANGED", "同一恢复申请不能改变候选、检查或说明", nil)
		}
		return s.GetWorkflowRecovery(ctx, id)
	}
	if state.Option.Action != input.Action || state.Option.TargetID != input.TargetID || state.Binding.ExecutionRunID != input.ExecutionRunID {
		return empty, apierr.Conflict("RECOVERY_NOT_CURRENT", "原 STOP、候选或检查已经变化，请刷新", nil)
	}
	if state.Option.UnavailableReason != "" {
		return empty, apierr.Conflict(state.Option.UnavailableReason, "当前不能申请恢复原候选检查", nil)
	}
	if reason := s.stoppedCheckRecoveryReady(ctx, state.Binding); reason != "" {
		return empty, apierr.Conflict(reason, "原检查尚未确认结束，或原候选和会话不一致", nil)
	}
	if err := store.RequestClearDevStoppedCheckRecovery(ctx, input.RequestID, input.Supplement, state.Binding, s.now().UTC()); err != nil {
		return empty, mapStoreError(err, "STOPPED_CHECK_RECOVERY_REJECTED")
	}
	// Do not wake execution. Only its exact subsequent native human approval can
	// create the bound check successor and allow the normal scheduler to run it.
	return s.GetWorkflowRecovery(ctx, id)
}

// The executor receipt is separate from a SQL infrastructure failure. Require
// its exact terminal fingerprint and released resources before requesting or
// applying authority and again immediately before the authorized check runs.
func (s *Service) stoppedCheckRecoveryReady(ctx context.Context, b core.StoppedCheckRecoveryBinding) string {
	e, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, b.DevelopmentRequirementID)
	if err != nil || !found || e.Run.ID != b.ExecutionRunID || s.workflowRecoveryCurrent(ctx, e) != nil {
		return "EXECUTION_NOT_CURRENT"
	}
	d, found := complexExecutionDispatchByID(e, b.DispatchID)
	if !found || d.CandidateCommitSHA != b.CandidateSHA || d.ComplexExecutionTaskID != b.TaskID || d.BuilderRoleBindingID != b.BuilderRoleBindingID {
		return "EXECUTION_NOT_CURRENT"
	}
	taskFound := false
	for _, task := range e.Tasks {
		if task.ID != b.TaskID {
			continue
		}
		taskFound = task.CurrentDispatchID == b.DispatchID && task.ReworkCount == b.ReworkCount && task.ExecutionPackageSHA256 == b.TaskPacketSHA256
	}
	if !taskFound {
		return "EXECUTION_NOT_CURRENT"
	}
	rec, reason := s.workflowRecoverySession(ctx, e, d, core.RecoveryRetryCheck)
	if reason != "" {
		return reason
	}
	if string(rec.ID) != b.AOSessionID || rec.Metadata.ProviderConversationID != b.ProviderConversationID || rec.Metadata.WorkspacePath != b.WorkspacePath || rec.CreationIdempotencyKey != b.SessionCreationKey || rec.CreationRequestFingerprint != b.CreationFingerprint || string(rec.Harness) != b.Harness || rec.Metadata.Model != b.Model || rec.PermissionMode != domain.PermissionModeAuto {
		return "RECOVERY_NATIVE_IDENTITY_MISSING"
	}
	if rec.Activity.State != domain.ActivityIdle || !s.invalidBuilderSessionSettled(ctx, rec.ID) {
		return "RESULT_NOT_SETTLED"
	}
	inspection, err := s.inspectExecutionCandidate(ctx, e.Run, b.WorkspacePath, d.BaseCommitSHA)
	if err != nil || inspection.CandidateSHA != b.CandidateSHA || inspection.BaseSHA != d.BaseCommitSHA {
		return "RECOVERY_SOURCE_CHANGED"
	}
	settled, err := s.workflowCheckSettled(ctx, e, b.CheckRunID)
	if err != nil || !settled {
		return "RESULT_NOT_SETTLED"
	}
	if e.FinalReview != nil {
		settled, err := s.plannerHistoricalFinalCommandsSettled(ctx, e)
		if err != nil || !settled {
			return "RESULT_NOT_SETTLED"
		}
	}
	return ""
}

func (s *Service) checkStoppedCheckBeforeRun(ctx context.Context, id, checkID string) (string, error) {
	store, ok := s.complexExecution.(stoppedCheckRecoveryStore)
	if !ok {
		return "", nil
	}
	b, registered, err := store.ValidateClearDevStoppedCheckBeforeRun(ctx, id, checkID)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errAgentRecoveryDeferred, err)
	}
	if registered {
		if reason := s.stoppedCheckRecoveryReady(ctx, b); reason != "" {
			return "", fmt.Errorf("%w: %s", errAgentRecoveryDeferred, reason)
		}
	}
	if registered {
		return core.NodeCheckSmallThreadsV1, nil
	}
	return "", nil
}
