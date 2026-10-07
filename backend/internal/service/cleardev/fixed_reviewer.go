package cleardev

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (s *Service) rebuildFixedReviewer(ctx context.Context, e core.ComplexExecutionSnapshot, r core.FixedRecoveryRequest, c core.FixedRecoveryClaim) (core.FixedRecoveryResult, error) {
	result := core.FixedRecoveryResult{RequestID: r.ID, Outcome: "UNKNOWN"}
	if r.ReviewID == "" || r.CandidateSHA == "" {
		return result, errComplexExecutionStopped
	}
	planning, found, err := s.complex.GetClearDevComplexPlanning(ctx, r.RequirementID)
	if err != nil || !found {
		return result, errComplexExecutionStopped
	}
	if _, err = s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, r.RoleBindingID, "REVIEWER_UNAVAILABLE", s.now().UTC()); err != nil {
		return result, err
	}
	binding := core.ComplexExecutionRoleBinding{ID: c.OperationID + ":reviewer", ExecutionRunID: r.ExecutionRunID, Role: core.StandardRoleReviewer, ContinuationOfRoleBindingID: r.RoleBindingID, TaskMappingID: r.TaskID, CandidateCommitID: r.CandidateID, SessionCreationIdempotencyKey: c.OperationID + ":reviewer-session", Status: core.RoleBindingStatusRequested, RequestedAt: s.now().UTC()}
	if _, _, err = s.complexExecution.CreateClearDevComplexExecutionRoleBinding(ctx, binding); err != nil {
		return result, err
	}
	branch := "cleardev-complex-review-recovery-" + coreDigest([]byte(r.ID))[:20]
	if err := s.inspector.PrepareReviewBranch(ctx, r.WorkspacePath, branch, r.CandidateSHA); err != nil {
		return result, err
	}
	blocked, model, err := s.runControlledModelPreflight(ctx, r.RequirementID, binding.ID, domain.ProjectID(r.ProjectID), r.Model)
	if err != nil {
		return result, err
	}
	if blocked || model != r.Model {
		return result, errAgentRecoveryDeferred
	}
	session, err := s.spawnResolvedChatSession(ctx, ports.SpawnConfig{ProjectID: domain.ProjectID(r.ProjectID), Kind: domain.KindWorker, Branch: branch, Prompt: "", RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Model: r.Model, Permissions: domain.PermissionModeAuto}, DisplayName: "ClearDev Replacement Reviewer", CreationIdempotencyKey: binding.SessionCreationIdempotencyKey}, model)
	if err != nil {
		return result, err
	}
	if !s.validComplexExecutionWorker(ctx, session.SessionRecord, binding, r.ProjectID) || session.Metadata.Model != r.Model || complexExecutionSessionAlreadyBound(e, planning, string(session.ID)) || filepath.Clean(session.Metadata.WorkspacePath) == filepath.Clean(r.WorkspacePath) {
		return result, errComplexExecutionStopped
	}
	for _, b := range e.RoleBindings {
		if filepath.Clean(b.WorkspacePath) == filepath.Clean(session.Metadata.WorkspacePath) {
			return result, errComplexExecutionStopped
		}
	}
	if e.Exception != nil {
		for _, b := range e.Exception.OnDemandBindings {
			if b.AOSessionID == string(session.ID) || filepath.Clean(b.WorkspacePath) == filepath.Clean(session.Metadata.WorkspacePath) {
				return result, errComplexExecutionStopped
			}
		}
	}
	inspector, ok := s.inspector.(ports.ClearDevRecoveryWorkspaceInspector)
	if !ok {
		return result, errors.New("managed recovery inspection unavailable")
	}
	if err := inspector.ValidateRecoveryWorkspace(ctx, session.Metadata.WorkspacePath, branch, r.CandidateSHA); err != nil {
		return result, err
	}
	if _, err = s.complexExecution.BindClearDevComplexExecutionRoleBinding(ctx, binding.ID, string(session.ID), session.Metadata.WorkspacePath, r.CandidateSHA, s.now().UTC()); err != nil {
		return result, err
	}
	result.Outcome = "PASS"
	result.SessionID = string(session.ID)
	result.RoleBindingID = binding.ID
	result.ProviderConversationID = session.Metadata.ProviderConversationID
	result.ControllerGeneration = session.Metadata.ControllerGeneration
	result.WorkspacePath = session.Metadata.WorkspacePath
	return result, nil
}

func (s *Service) continueFixedReviewer(ctx context.Context, e core.ComplexExecutionSnapshot, fixed core.FixedRecoveryEvidence) (bool, bool, bool, error) {
	r := fixed.Request
	var task core.ComplexExecutionTask
	var dispatch core.ComplexExecutionDispatch
	var review core.ComplexExecutionReview
	for _, t := range e.Tasks {
		if t.ID == r.TaskID {
			task = t
		}
	}
	for _, d := range e.Dispatches {
		if d.ID == r.DispatchID {
			dispatch = d
		}
	}
	for _, v := range e.Reviews {
		if v.ID == r.ReviewID {
			review = v
		}
	}
	if dispatch.Status == core.ComplexExecutionDispatchVerified || core.ComplexExecutionTaskVerified(e, task) {
		return false, false, false, nil
	}
	replacementFlow := core.BoundedMailAttempts(e.Run)
	if !replacementFlow {
		_, project, contractErr := core.ProjectContractFromRun(e.Run)
		if contractErr != nil {
			return true, false, true, contractErr
		}
		replacementFlow = project
	}
	if replacementFlow && task.ID != "" && dispatch.ID == r.DispatchID && dispatch.CandidateCommitID == r.CandidateID && dispatch.CandidateCommitSHA == r.CandidateSHA && review.ReviewPacketSHA256 == r.ReviewPacketSHA256 && review.ReviewPacketJSON == r.ReviewPacketJSON && (task.CurrentDispatchID == r.DispatchID || fixed.ReviewResult != nil) {
		return s.continueMailReplacementReview(ctx, e, fixed, task, dispatch, review)
	}
	if task.ID == "" || task.CurrentDispatchID != r.DispatchID || dispatch.CandidateCommitID != r.CandidateID || dispatch.CandidateCommitSHA != r.CandidateSHA || review.ReviewPacketSHA256 != r.ReviewPacketSHA256 || review.ReviewPacketJSON != r.ReviewPacketJSON {
		return true, false, true, errComplexExecutionStopped
	}
	if fixed.ReviewResult != nil {
		if fixed.ReviewResult.Verdict != core.LocalReviewPass {
			return true, false, true, errComplexExecutionStopped
		}
		binding, ok := complexExecutionBindingByID(e, fixed.Result.RoleBindingID)
		if ok && binding.Status == core.RoleBindingStatusBound {
			changed, err := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, binding.ID, core.ReasonNone, s.now().UTC())
			return true, changed, false, err
		}
		if recovery, ok := activeOnDemandBinding(e, core.ComplexOnDemandModeRecovery); ok {
			changed, err := s.complexExecution.EndClearDevComplexExceptionOnDemand(ctx, recovery.ID, core.ReasonNone, s.now().UTC())
			return true, changed, false, err
		}
		changed, done, err := s.verifyComplexExecutionCandidate(ctx, e, task, dispatch, review)
		return true, changed, done, err
	}
	step, ok := complexExecutionStepByID(e, r.LogicalStepID)
	if !ok {
		return true, false, true, errComplexExecutionStopped
	}
	effective := step
	effective.RoleBindingID = fixed.Result.RoleBindingID
	if _, err := s.ensureAgentAttempt(ctx, r.RequirementID, core.AgentStepCategoryComplexExecution, effective, fixed.Result.SessionID, 2, r.FailureEventID); err != nil {
		return true, false, true, err
	}
	basePrompt := reviewerPrompt([]byte(r.ReviewPacketJSON), r.ReviewID, r.CandidateID, r.CandidateSHA, r.ReviewPacketSHA256)
	prompt := mailRolePrompt(e.Run, basePrompt)
	if current := requestedChecksReviewPrompt(e.Run, basePrompt); coreDigest([]byte(current)) == r.PromptSHA256 {
		prompt = current
	}
	if coreDigest([]byte(prompt)) != r.PromptSHA256 {
		return true, false, true, errComplexExecutionStopped
	}
	validate := func(raw []byte) error {
		_, err := core.ParseComplexExecutionLocalReviewResult(raw, r.ReviewID, r.CandidateID, r.CandidateSHA, r.ReviewPacketSHA256, complexReviewDiffPaths(r.ReviewPacketJSON))
		return err
	}
	polled, err := s.pollValidAgentJSON(ctx, r.RequirementID, core.AgentStepCategoryComplexExecution, r.SessionID, step, prompt, validate, "REVIEW_TIMEOUT", "REVIEWER_UNAVAILABLE", "REVIEW_RESULT_INVALID")
	if err != nil {
		return true, false, true, err
	}
	if polled.stopped {
		return true, false, true, errComplexExecutionStopped
	}
	if !polled.ready {
		return true, false, false, nil
	}
	parsed, err := core.ParseComplexExecutionLocalReviewResult([]byte(polled.message.Text), r.ReviewID, r.CandidateID, r.CandidateSHA, r.ReviewPacketSHA256, complexReviewDiffPaths(r.ReviewPacketJSON))
	if err != nil {
		return true, false, true, err
	}
	attemptID := agentAttemptID(r.LogicalStepID, 2)
	index := 1
	correction, found, err := s.getParseCorrection(ctx, r.LogicalStepID)
	if err != nil {
		return true, false, true, err
	}
	if found && correction.AttemptNumber == 2 {
		index = 2
	}
	resultID := fmt.Sprintf("%s:result:%d", attemptID, index)

	store, ok := s.complexExecution.(interface {
		RecordClearDevReplacementReviewResult(context.Context, core.ReplacementReviewResult) error
	})
	if !ok {
		return true, false, true, errors.New("replacement review storage unavailable")
	}
	err = store.RecordClearDevReplacementReviewResult(ctx, core.ReplacementReviewResult{RecoveryRequestID: r.ID, OriginalReviewID: r.ReviewID, AttemptID: attemptID, ResultID: resultID, Verdict: core.LocalReviewVerdict(parsed.Verdict), ReasonCode: core.ReasonCode(parsed.ReasonCode), Summary: parsed.Summary, RecordedAt: s.now().UTC()})
	return true, err == nil, false, err
}

// reconcileFixedRecovery only inspects the exact claimed operation; it never invokes a capability.
func (s *Service) reconcileFixedRecovery(ctx context.Context, e core.ComplexExecutionSnapshot, fixed core.FixedRecoveryEvidence, store fixedRecoveryStore) (bool, bool, error) {
	r, c := fixed.Request, *fixed.Claim
	if c.Action == core.ComplexRecoveryActionRestoreSession {
		// Current availability cannot prove which resume mode completed before the failed save.
		// Inspect the bound session and directory, but retain unknown without a native outcome receipt.
		if _, err := s.validateFixedRecoveryTarget(ctx, r, false); err != nil {
			return false, true, err
		}
		return false, true, fmt.Errorf("%w: native recovery outcome receipt is missing", errComplexExecutionStopped)
	}
	if c.Action != core.ComplexRecoveryActionRebuildReviewer {
		return false, true, errComplexExecutionStopped
	}
	if _, err := s.validateFixedRecoveryTarget(ctx, r, true); err != nil {
		return false, true, err
	}
	binding, found := complexExecutionBindingByID(e, c.OperationID+":reviewer")
	if !found || binding.Status != core.RoleBindingStatusBound || binding.ContinuationOfRoleBindingID != r.RoleBindingID || binding.TaskMappingID != r.TaskID || binding.CandidateCommitID != r.CandidateID || binding.BaseCommitSHA != r.CandidateSHA || binding.SessionCreationIdempotencyKey != c.OperationID+":reviewer-session" {
		return false, true, errComplexExecutionStopped
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		return false, true, err
	}
	branch := "cleardev-complex-review-recovery-" + coreDigest([]byte(r.ID))[:20]
	if !found || !s.validComplexExecutionWorker(ctx, record, binding, r.ProjectID) || record.IsTerminated || record.Activity.State != domain.ActivityIdle || record.Metadata.Model != r.Model || record.Metadata.Branch != branch || record.Metadata.WorkspacePath != binding.WorkspacePath || record.ID == domain.SessionID(r.SessionID) {
		return false, true, errComplexExecutionStopped
	}
	for _, other := range e.RoleBindings {
		if other.ID != binding.ID && (other.AOSessionID == binding.AOSessionID || filepath.Clean(other.WorkspacePath) == filepath.Clean(binding.WorkspacePath)) {
			return false, true, errComplexExecutionStopped
		}
	}
	inspector, ok := s.inspector.(ports.ClearDevRecoveryWorkspaceInspector)
	if !ok {
		return false, true, errComplexExecutionStopped
	}
	if err := inspector.ValidateRecoveryWorkspace(ctx, binding.WorkspacePath, branch, r.CandidateSHA); err != nil {
		return false, true, err
	}
	result := core.FixedRecoveryResult{RequestID: r.ID, Outcome: "PASS", SessionID: binding.AOSessionID, RoleBindingID: binding.ID, ProviderConversationID: record.Metadata.ProviderConversationID, ControllerGeneration: record.Metadata.ControllerGeneration, WorkspacePath: binding.WorkspacePath, RecordedAt: s.now().UTC()}
	if err := store.RecordClearDevFixedRecoveryResult(ctx, result); err != nil {
		return false, true, err
	}
	return true, false, nil
}
