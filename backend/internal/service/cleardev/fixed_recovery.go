package cleardev

import (
	"context"
	"errors"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fixedRecoveryStore interface {
	RecordClearDevFixedRecoveryRefusal(context.Context, core.FixedRecoveryRefusal) error
	EnsureClearDevFixedRecoveryRequest(context.Context, core.FixedRecoveryRequest) (core.FixedRecoveryRequest, error)
	ClaimClearDevFixedRecoveryAction(context.Context, core.FixedRecoveryClaim) (bool, error)
	RecordClearDevFixedRecoveryResult(context.Context, core.FixedRecoveryResult) error
}

func fixedRecoveryForFailure(execution core.ComplexExecutionSnapshot, id string) (core.FixedRecoveryEvidence, bool) {
	for _, e := range execution.FixedRecoveries {
		if e.Request.FailureEventID == id {
			return e, true
		}
	}
	return core.FixedRecoveryEvidence{}, false
}

// advanceFixedRecovery admits a reliable exited-step failure before ordinary advancement.
func (s *Service) advanceFixedRecovery(ctx context.Context, e core.ComplexExecutionSnapshot) (handled, progressed, done bool, err error) {
	if phase, _ := core.DeriveComplexExecutionPhase(e); phase == core.ComplexExecutionCompleted {
		return false, false, true, nil
	}
	for _, fixed := range e.FixedRecoveries {
		if fixed.Refusal != nil {
			return true, false, true, errComplexExecutionStopped
		}
		if fixed.Request.CheckRunID != "" {
			continue
		}
		if fixed.Result == nil {
			changed, stop, err := s.advanceComplexExceptionRecovery(ctx, e, fixed.Request.ProjectID)
			return true, changed, stop, err
		}
		if fixed.Result.Outcome != "PASS" {
			return true, false, true, errComplexExecutionStopped
		}
		if fixed.Claim != nil && fixed.Claim.Action == core.ComplexRecoveryActionRebuildReviewer {
			if handled, changed, done, err := s.continueFixedReviewer(ctx, e, fixed); handled {
				return handled, changed, done, err
			}
		}
	}
	views, err := s.attempts.ListLatestClearDevAgentAttemptStates(ctx, e.Run.DevelopmentRequirementID)
	if err != nil {
		return true, false, false, err
	}
	for _, v := range views {
		if core.FixedRecoveryStepAllowed(v.StepCategory, v.StepKind) && v.AttemptNumber == 1 && (v.SendStatus == core.AgentAttemptSent || v.SendStatus == core.AgentAttemptCorrectionSent) {
			record, found, err := s.ao.GetSession(ctx, domain.SessionID(v.AOSessionID))
			if err != nil {
				return true, false, false, err
			}
			if found && !record.IsTerminated && record.Activity.State == domain.ActivityExited {
				snapshot, err := s.chat.Snapshot(ctx, domain.SessionID(v.AOSessionID))
				if err != nil {
					return true, false, false, err
				}
				messageID, promptSHA := v.ClientMessageID, v.PromptSHA256
				if v.SendStatus == core.AgentAttemptCorrectionSent {
					correction, found, err := s.getParseCorrection(ctx, v.LogicalStepID)
					if err != nil {
						return true, false, false, err
					}
					if !found || correction.AttemptNumber != 1 {
						continue
					}
					messageID = correction.ClientMessageID
					promptSHA = coreDigest([]byte(correction.PromptText))
				}
				if snapshot.SessionID == domain.SessionID(v.AOSessionID) {
					for _, turn := range snapshot.Turns {
						if turn.ID != v.TurnID || turn.HandledBySessionID != domain.SessionID(v.AOSessionID) || turn.Failure == nil || (turn.State != domain.TurnStateFailed && turn.State != domain.TurnStateInterrupted) {
							continue
						}
						exact := false
						for _, message := range snapshot.Messages {
							if message.TurnID == turn.ID && message.Role == domain.MessageRoleUser && message.ClientMessageID == messageID && coreDigest([]byte(message.Text)) == promptSHA {
								exact = true
							}
						}
						if !exact {
							continue
						}
						failure := normalizeAgentFailure(*turn.Failure)
						if !failure.Retryable || !core.FixedRecoveryFailureAllowed(failure.Category) {
							continue
						}
						status := core.AgentAttemptFailed
						if turn.State == domain.TurnStateInterrupted {
							status = core.AgentAttemptInterrupted
						}
						err = s.attempts.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: agentEvidenceID(v.ID, "fixed-terminal", turn.ID), AttemptID: v.ID, Status: status, ClientMessageID: v.ClientMessageID, PromptSHA256: v.PromptSHA256, TurnID: turn.ID, TurnState: turn.State, FailureCategory: failure.Category, Retryable: failure.Retryable, RetryAt: failure.RetryAt, ErrorSummary: failure.ErrorSummary, RecordedAt: s.now().UTC()})
						return true, err == nil, false, err
					}
				}
			}
		}
		if !core.FixedRecoveryStepAllowed(v.StepCategory, v.StepKind) || (!canCreateSecondAgentAttempt(v, s.now().UTC()) || !core.FixedRecoveryFailureAllowed(v.FailureCategory)) {
			continue
		}

		states, err := s.attempts.ListClearDevAgentStepAttemptStates(ctx, e.Run.DevelopmentRequirementID, v.LogicalStepID)
		if err != nil {
			return true, false, false, err
		}
		if len(logicalStepAttemptViews(states, v.LogicalStepID)) != 1 {
			continue
		}
		binding, ok := complexExecutionBindingByID(e, v.RoleBindingID)
		if !ok || binding.Status != core.RoleBindingStatusBound {
			continue
		}
		record, found, err := s.ao.GetSession(ctx, domain.SessionID(v.AOSessionID))
		if err != nil {
			return true, false, false, err
		}
		if !found || record.IsTerminated || record.Activity.State != domain.ActivityExited {
			continue
		}
		if fixed, ok := fixedRecoveryForFailure(e, v.LastEventID); ok && fixed.Result != nil {
			if fixed.Claim.Action == core.ComplexRecoveryActionRebuildReviewer {
				return s.continueFixedReviewer(ctx, e, fixed)
			}
			if _, err := s.createSecondAgentAttempt(ctx, e.Run.DevelopmentRequirementID, v); err != nil {
				return true, false, true, err
			}
			return true, true, false, nil
		}
		request := core.FixedRecoveryRequest{ID: v.LogicalStepID + ":fixed-recovery", RequirementID: e.Run.DevelopmentRequirementID, ExecutionRunID: e.Run.ID, LogicalStepID: v.LogicalStepID, FirstAttemptID: v.ID, FailureEventID: v.LastEventID, RoleBindingID: v.RoleBindingID, SessionID: v.AOSessionID, ProviderConversationID: record.Metadata.ProviderConversationID, ControllerGeneration: record.Metadata.ControllerGeneration, ProjectID: string(record.ProjectID), WorkspacePath: record.Metadata.WorkspacePath, Branch: record.Metadata.Branch, Model: record.Metadata.Model, PromptSHA256: v.PromptSHA256, CreatedAt: s.now().UTC()}
		continuationDispatchID := ""
		if e.Exception != nil {
			for _, step := range e.Exception.OnDemandSteps {
				if step.ID == v.LogicalStepID && step.Kind == core.ComplexExceptionAgentStepBuilderContinue {
					continuationDispatchID = step.RequestID
				}
			}
		}
		for _, dispatch := range e.Dispatches {
			if dispatch.AgentStepID == v.LogicalStepID || dispatch.ID == continuationDispatchID {
				request.DispatchID, request.TaskID, request.ExpectedSHA = dispatch.ID, dispatch.ComplexExecutionTaskID, dispatch.BaseCommitSHA
				request.CandidateID, request.CandidateSHA = dispatch.CandidateCommitID, dispatch.CandidateCommitSHA
			}
		}
		for _, review := range e.Reviews {
			if review.AgentStepID == v.LogicalStepID {
				request.DispatchID, request.TaskID = review.DispatchID, review.ComplexExecutionTaskID
				request.ReviewID, request.CandidateID, request.CandidateSHA = review.ID, review.CandidateCommitID, review.CandidateCommitSHA
				request.ReviewPacketJSON, request.ReviewPacketSHA256 = review.ReviewPacketJSON, review.ReviewPacketSHA256
			}
		}
		if request.CandidateSHA != "" {
			request.ExpectedSHA = request.CandidateSHA
		}
		if request.DispatchID == "" {
			continue
		}
		store, ok := s.complexExecution.(fixedRecoveryStore)
		if !ok {
			return true, false, true, errors.New("fixed recovery storage is unavailable")
		}
		if _, err := store.EnsureClearDevFixedRecoveryRequest(ctx, request); err != nil {
			return true, false, true, err
		}
		return true, true, false, nil
	}
	return false, false, false, nil
}

func (s *Service) validateFixedRecoveryTarget(ctx context.Context, r core.FixedRecoveryRequest, exited bool) (domain.SessionRecord, error) {
	rec, found, err := s.ao.GetSession(ctx, domain.SessionID(r.SessionID))
	if err != nil {
		return rec, err
	}
	if !found || rec.IsTerminated || !s.controlledSessionMatches(ctx, rec) || domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeChat || rec.Kind != domain.KindWorker || string(rec.ProjectID) != r.ProjectID || rec.Metadata.WorkspacePath != r.WorkspacePath || rec.Metadata.Branch != r.Branch || rec.Metadata.Model != r.Model || rec.Metadata.ProviderConversationID != r.ProviderConversationID {
		return rec, errOriginalAgentSessionUnavailable
	}
	if exited && (rec.Activity.State != domain.ActivityExited || rec.Metadata.ControllerGeneration != r.ControllerGeneration) {
		return rec, errOriginalAgentSessionUnavailable
	}
	if !exited && rec.Activity.State != domain.ActivityIdle {
		return rec, errOriginalAgentSessionUnavailable
	}
	inspector, ok := s.inspector.(ports.ClearDevRecoveryWorkspaceInspector)
	if !ok {
		return rec, errors.New("managed recovery worktree inspection unavailable")
	}
	if err := inspector.ValidateRecoveryWorkspace(ctx, r.WorkspacePath, r.Branch, r.ExpectedSHA); err != nil {
		return rec, err
	}
	return rec, nil
}

func (s *Service) executeFixedRecovery(ctx context.Context, e core.ComplexExecutionSnapshot, fixed core.FixedRecoveryEvidence, proposal core.ComplexRecoveryAction) (bool, bool, error) {
	store, ok := s.complexExecution.(fixedRecoveryStore)
	if !ok {
		return false, true, errors.New("fixed recovery storage unavailable")
	}
	r := fixed.Request
	if fixed.Result != nil {
		return false, fixed.Result.Outcome != "PASS", nil
	}
	if fixed.Claim != nil {
		return s.reconcileFixedRecovery(ctx, e, fixed, store)
	}
	refuse := func(reason string) (bool, bool, error) {
		err := store.RecordClearDevFixedRecoveryRefusal(ctx, core.FixedRecoveryRefusal{RequestID: r.ID, ReasonCode: reason, RecordedAt: s.now().UTC()})
		return err == nil, true, err
	}
	rec, err := s.validateFixedRecoveryTarget(ctx, r, true)
	if err != nil {
		reason := "FIXED_RECOVERY_TARGET_UNAVAILABLE"
		if errors.Is(err, ports.ErrClearDevWorkspaceDirty) {
			reason = "WORKSPACE_DIRTY"
		}
		if errors.Is(err, ports.ErrClearDevCandidateInvalid) {
			reason = "WORKSPACE_BINDING_CHANGED"
		}
		return refuse(reason)
	}
	if proposal.Action == core.ComplexRecoveryActionRestoreSession && (strings.TrimSpace(r.ProviderConversationID) == "" || s.restoreOriginalAgentSession == nil) {
		return refuse("NATIVE_RECOVERY_IDENTITY_OR_CAPABILITY_MISSING")
	}
	blocked, resolvedModel, err := s.runControlledModelPreflight(ctx, r.RequirementID, r.RoleBindingID, rec.ProjectID, r.Model)
	if err != nil || blocked {
		return false, true, err
	}
	if resolvedModel != r.Model {
		return refuse("FIXED_RECOVERY_MODEL_CHANGED")
	}
	claim := core.FixedRecoveryClaim{RequestID: r.ID, ProposalID: proposal.ID, Action: proposal.Action, OperationID: r.ID + ":operation", ClaimedAt: s.now().UTC()}
	created, err := store.ClaimClearDevFixedRecoveryAction(ctx, claim)
	if err != nil {
		var rule *core.RuleError
		if errors.As(err, &rule) {
			return refuse(string(rule.Code))
		}
		return false, true, err
	}
	if !created {
		return false, true, nil
	}
	result := core.FixedRecoveryResult{RequestID: r.ID, Outcome: "FAILED"}
	if proposal.Action == core.ComplexRecoveryActionRestoreSession {
		mode, restoreErr := s.restoreOriginalAgentSession(ctx, domain.SessionID(r.SessionID))
		after, inspectErr := s.validateFixedRecoveryTarget(ctx, r, false)
		if restoreErr == nil && inspectErr == nil && mode == "native" {
			result.Outcome = "PASS"
			result.SessionID = r.SessionID
			result.ProviderConversationID = r.ProviderConversationID
			result.ControllerGeneration = after.Metadata.ControllerGeneration
			result.WorkspacePath = r.WorkspacePath
			result.RoleBindingID = r.RoleBindingID
		} else {
			result.ReasonCode = "ORIGINAL_SESSION_RESTORE_UNCONFIRMED"
		}
	} else {
		result, err = s.rebuildFixedReviewer(ctx, e, r, claim)
		if err != nil {
			s.logger.Error("fixed Reviewer creation unconfirmed", "error", err)
			result = core.FixedRecoveryResult{RequestID: r.ID, Outcome: "UNKNOWN", ReasonCode: "REVIEWER_CREATION_UNCONFIRMED"}
		}
	}
	result.RecordedAt = s.now().UTC()
	if err := store.RecordClearDevFixedRecoveryResult(ctx, result); err != nil {
		return false, true, err
	}
	return true, result.Outcome != "PASS", nil
}
