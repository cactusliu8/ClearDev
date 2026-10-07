package cleardev

import (
	"context"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type builderSessionCheckStore interface {
	ListClearDevBuilderSessionChecks(context.Context, string) ([]core.BuilderSessionCheck, error)
	RecordClearDevBuilderSessionCheck(context.Context, core.BuilderSessionCheck) (bool, error)
	ReadClearDevBuilderStepUnsent(context.Context, string, string) (bool, error)
	ClearDevBuilderSessionHasOpenTurn(context.Context, domain.SessionID) (bool, error)
}

func (s *Service) builderSessionStepUnsent(ctx context.Context, e core.ComplexExecutionSnapshot, stepID string) bool {
	store, ok := s.complexExecution.(builderSessionCheckStore)
	if !ok {
		return false
	}
	unsent, err := store.ReadClearDevBuilderStepUnsent(ctx, e.Run.ID, stepID)
	return err == nil && unsent
}

func builderSessionRetryForDispatch(e core.ComplexExecutionSnapshot, d core.ComplexExecutionDispatch) *core.WorkflowRecovery {
	for i := len(e.WorkflowRecoveries) - 1; i >= 0; i-- {
		r := &e.WorkflowRecoveries[i]
		if r.Action == core.RecoveryRetryBuilderSession && r.DispatchID == d.ID && r.StepID == d.AgentStepID && r.BindingID == d.BuilderRoleBindingID && r.TaskID == d.ComplexExecutionTaskID {
			return r
		}
	}
	return nil
}

// Only replay of the existing request is offered while an unsent recheck is
// registered. A different request ID still fails the normal write admission.
func (s *Service) addBuilderSessionRecheckRegistration(ctx context.Context, e core.ComplexExecutionSnapshot, out *core.WorkflowRecoveryView) {
	for _, task := range e.Tasks {
		if task.Status != core.DevelopmentTaskStatusRunning {
			continue
		}
		for _, dispatch := range e.Dispatches {
			if dispatch.ID != task.CurrentDispatchID {
				continue
			}
			r := builderSessionRetryForDispatch(e, dispatch)
			if r == nil || !s.builderSessionStepUnsent(ctx, e, dispatch.AgentStepID) {
				continue
			}
			failed := false
			for _, check := range out.BuilderSessionChecks {
				failed = failed || check.RecoveryID == r.ID && check.Outcome == "FAILED"
			}
			if !failed {
				out.Options = append(out.Options, core.WorkflowRecoveryOption{Action: core.RecoveryRetryBuilderSession, TargetID: r.TargetID, TaskID: r.TaskID, Role: "BUILDER", Reason: r.OriginalReason, UnavailableReason: "BUILDER_RECHECK_REGISTERED"})
			}
		}
	}
}

// recheckWorkflowBuilder runs only for an explicit original-step retry. It
// never sends a task itself. A persistent start bounds native restoration to
// this request; replay observes the original session instead of restoring twice.
func (s *Service) recheckWorkflowBuilder(ctx context.Context, e core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, d core.ComplexExecutionDispatch, binding core.ComplexExecutionRoleBinding, projectID string) (handled, ready bool, record domain.SessionRecord, err error) {
	r := builderSessionRetryForDispatch(e, d)
	if r == nil {
		return false, false, record, nil
	}
	store, ok := s.complexExecution.(builderSessionCheckStore)
	if !ok {
		return true, false, record, errors.New("original Builder recheck storage is unavailable")
	}
	unsent, err := store.ReadClearDevBuilderStepUnsent(ctx, e.Run.ID, d.AgentStepID)
	if err != nil {
		return true, false, record, err
	}
	if !unsent {
		// The old sender owns any already-reserved delivery, including a crash
		// between provider acceptance and MarkStepSent. Do not restore/resend.
		return false, false, record, nil
	}
	checks, err := store.ListClearDevBuilderSessionChecks(ctx, e.Run.ID)
	if err != nil {
		return true, false, record, err
	}
	var started, finished *core.BuilderSessionCheck
	for i := range checks {
		check := &checks[i]
		if check.RecoveryID != r.ID {
			continue
		}
		if check.Outcome == "FAILED" {
			return true, false, record, nil
		}
		if check.Checkpoint == "STARTED" {
			started = check
		}
		if check.Checkpoint == "FINISHED" {
			finished = check
		}
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		return true, false, record, err
	}
	freshStart := started == nil
	if freshStart {
		check := core.BuilderSessionCheck{RecoveryID: r.ID, Checkpoint: "STARTED", Stage: "RECHECK", Outcome: "PENDING", BindingSHA256: core.BuilderSessionBindingDigest(record), CheckedAt: s.now().UTC()}
		freshStart, err = store.RecordClearDevBuilderSessionCheck(ctx, check)
		if err != nil {
			return true, false, record, err
		}
		started = &check
	}
	checkpoint := "FINISHED"
	if finished != nil {
		checkpoint = "BEFORE_SEND"
	}
	fail := func(stage, reason, preflightID string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, saveErr := store.RecordClearDevBuilderSessionCheck(ctx, core.BuilderSessionCheck{RecoveryID: r.ID, Checkpoint: checkpoint, Stage: stage, Outcome: "FAILED", ReasonCode: reason, PreflightID: preflightID, BindingSHA256: started.BindingSHA256, CheckedAt: s.now().UTC()})
		return saveErr
	}
	if err := s.workflowRecoveryCurrent(ctx, e); err != nil {
		return true, false, record, fail("SOURCE", "RECOVERY_NOT_CURRENT", "")
	}
	if !found || record.IsTerminated || !s.validComplexExecutionWorker(ctx, record, binding, projectID) || record.Metadata.WorkspacePath != binding.WorkspacePath {
		return true, false, record, fail("SESSION", "RECOVERY_SESSION_UNAVAILABLE", "")
	}
	if r.ProviderConversationID == "" || record.Metadata.ProviderConversationID != r.ProviderConversationID || core.BuilderSessionBindingDigest(record) != started.BindingSHA256 {
		return true, false, record, fail("IDENTITY", "RECOVERY_NATIVE_IDENTITY_MISSING", "")
	}
	if err := s.validateComplexExecutionDispatchWorkspace(ctx, e, task, d, record.Metadata.WorkspacePath); err != nil {
		return true, false, record, fail("WORKSPACE", string(gitInspectionReason(err)), "")
	}
	if reason := s.builderSessionRecheckIdle(ctx, store, record, record.Activity.State == domain.ActivityExited); reason != "" {
		return true, false, record, fail("SESSION", reason, "")
	}
	preflightID := ""
	if finished == nil {
		blocked, model, preflightErr := s.runControlledModelPreflight(ctx, e.Run.DevelopmentRequirementID, binding.ID, record.ProjectID, record.Metadata.Model)
		reason := ""
		if preflightErr != nil {
			reason = builderSessionRestoreReason(preflightErr)
		} else if blocked || model != record.Metadata.Model {
			reason = "MODEL_NOT_AVAILABLE"
		}
		if s.preflights != nil {
			preflight, exists, readErr := s.preflights.GetLatestClearDevControlledPreflightForBinding(ctx, binding.ID)
			if readErr != nil {
				return true, false, record, fail("PREFLIGHT", "BUILDER_RECHECK_EVIDENCE_UNAVAILABLE", "")
			}
			if exists {
				preflightID = preflight.ID
				if preflight.Outcome == core.ControlledPreflightFailed {
					reason = string(preflight.ReasonCode)
				}
			}
		}
		if reason != "" {
			return true, false, record, fail("PREFLIGHT", reason, preflightID)
		}
	}
	if record.Activity.State == domain.ActivityExited {
		if !freshStart || finished != nil {
			return true, false, record, fail("RESTORE", "BUILDER_RESTORE_UNCONFIRMED", preflightID)
		}
		if s.restoreOriginalAgentSession == nil {
			return true, false, record, fail("RESTORE", "RECOVERY_NATIVE_IDENTITY_MISSING", preflightID)
		}
		mode, restoreErr := s.restoreOriginalAgentSession(ctx, record.ID)
		if restoreErr != nil {
			return true, false, record, fail("RESTORE", builderSessionRestoreReason(restoreErr), preflightID)
		}
		if mode != "native" {
			return true, false, record, fail("RESTORE", "BUILDER_RESTORE_NOT_NATIVE", preflightID)
		}
	}
	// Restoration may have taken time. Re-sample source, identity, original
	// unfinished work and both durable and native turn state before readiness.
	record, found, err = s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		return true, false, record, fail("IDENTITY", "BUILDER_RECHECK_EVIDENCE_UNAVAILABLE", preflightID)
	}
	if !found || record.IsTerminated || core.BuilderSessionBindingDigest(record) != started.BindingSHA256 || !s.validComplexExecutionWorker(ctx, record, binding, projectID) {
		return true, false, record, fail("IDENTITY", "BUILDER_RESTORE_IDENTITY_CHANGED", preflightID)
	}
	if reason := s.builderSessionRecheckIdle(ctx, store, record, false); reason != "" {
		return true, false, record, fail("SESSION", reason, preflightID)
	}
	if err := s.workflowRecoveryCurrent(ctx, e); err != nil {
		return true, false, record, fail("SOURCE", "RECOVERY_NOT_CURRENT", preflightID)
	}
	if err := s.validateComplexExecutionDispatchWorkspace(ctx, e, task, d, record.Metadata.WorkspacePath); err != nil {
		return true, false, record, fail("WORKSPACE", string(gitInspectionReason(err)), preflightID)
	}
	if finished == nil {
		_, err = store.RecordClearDevBuilderSessionCheck(ctx, core.BuilderSessionCheck{RecoveryID: r.ID, Checkpoint: "FINISHED", Stage: "READY", Outcome: "READY", PreflightID: preflightID, BindingSHA256: started.BindingSHA256, CheckedAt: s.now().UTC()})
	}
	return true, err == nil, record, err
}

// recordBuilderRecheckSendStop retains a failed final admission without changing
// the already-saved READY observation. The same unsent attempt can be explicitly
// rechecked later, but its original reservation identity is never reset.
func (s *Service) recordBuilderRecheckSendStop(ctx context.Context, e core.ComplexExecutionSnapshot, d core.ComplexExecutionDispatch, stage, reason string) error {
	r := builderSessionRetryForDispatch(e, d)
	store, ok := s.complexExecution.(builderSessionCheckStore)
	if r == nil || !ok {
		return nil
	}
	checks, err := store.ListClearDevBuilderSessionChecks(ctx, e.Run.ID)
	if err != nil {
		return err
	}
	for _, check := range checks {
		if check.RecoveryID == r.ID && check.Checkpoint == "BEFORE_SEND" {
			return nil
		}
	}
	for _, check := range checks {
		if check.RecoveryID == r.ID && check.Checkpoint == "FINISHED" && check.Outcome == "READY" {
			_, err := store.RecordClearDevBuilderSessionCheck(ctx, core.BuilderSessionCheck{RecoveryID: r.ID, Checkpoint: "BEFORE_SEND", Stage: stage, Outcome: "FAILED", ReasonCode: reason, BindingSHA256: check.BindingSHA256, CheckedAt: s.now().UTC()})
			return err
		}
	}
	return nil
}

func (s *Service) builderSessionRecheckIdle(ctx context.Context, store builderSessionCheckStore, record domain.SessionRecord, allowExited bool) string {
	busy, err := store.ClearDevBuilderSessionHasOpenTurn(ctx, record.ID)
	if err != nil {
		return "BUILDER_RECHECK_EVIDENCE_UNAVAILABLE"
	}
	if busy {
		return "BUILDER_SESSION_BUSY"
	}
	if allowExited && record.Activity.State == domain.ActivityExited {
		return ""
	}
	if record.Activity.State != domain.ActivityIdle {
		return "BUILDER_SESSION_BUSY"
	}
	if s.chat == nil {
		return "BUILDER_RECHECK_EVIDENCE_UNAVAILABLE"
	}
	snapshot, err := s.chat.Snapshot(ctx, record.ID)
	if err != nil || snapshot.SessionID != record.ID {
		return "BUILDER_RECHECK_EVIDENCE_UNAVAILABLE"
	}
	for _, turn := range snapshot.Turns {
		if turn.State != domain.TurnStateCompleted && turn.State != domain.TurnStateFailed && turn.State != domain.TurnStateInterrupted {
			return "BUILDER_SESSION_BUSY"
		}
	}
	return ""
}

// Error strings can contain credentials and provider request bodies. Preserve
// a typed, actionable cause; never echo arbitrary native error text into JSON.
func builderSessionRestoreReason(err error) string {
	for _, pair := range []struct {
		err    error
		reason string
	}{
		{ports.ErrChatAuthRequired, "LOGIN_REQUIRED"},
		{ports.ErrChatModelNotAvailable, "MODEL_NOT_AVAILABLE"},
		{ports.ErrChatQuotaExhausted, "QUOTA_EXHAUSTED"},
		{ports.ErrChatRateLimited, "RATE_LIMITED"},
		{ports.ErrChatProviderUnavailable, "PROVIDER_UNAVAILABLE"},
		{ports.ErrChatToolNotInstalled, "EXECUTION_TOOL_NOT_INSTALLED"},
		{ports.ErrChatConfigurationInvalid, "EXECUTION_TOOL_CONFIG_INVALID"},
		{ports.ErrChatDriverIncompatible, "DRIVER_INCOMPATIBLE"},
		{ports.ErrChatResumeFailed, "NATIVE_SESSION_RESUME_FAILED"},
		{ports.ErrChatHistoryUnavailable, "NATIVE_SESSION_HISTORY_UNAVAILABLE"},
	} {
		if errors.Is(err, pair.err) {
			return pair.reason
		}
	}
	return "BUILDER_RESTORE_FAILED"
}
