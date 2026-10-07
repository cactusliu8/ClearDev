package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func mailReplacementReworkFeedback(e core.ComplexExecutionSnapshot, d core.ComplexExecutionDispatch) string {
	if !core.BoundedMailAttempts(e.Run) {
		return ""
	}
	for _, f := range e.FixedRecoveries {
		if f.Request.DispatchID != d.ID || f.Request.CandidateID != d.CandidateCommitID || f.Request.CandidateSHA != d.CandidateCommitSHA || f.ReviewResult == nil || f.ReviewResult.Verdict != core.LocalReviewRework || f.ReviewStep == nil {
			continue
		}
		var result core.LocalReviewResult
		if json.Unmarshal([]byte(f.ReviewStep.FinalMessageText), &result) != nil {
			return ""
		}
		findings, _ := json.Marshal(result.Findings)
		return fmt.Sprintf("%s\nReplacement Reviewer result %s on candidate %s. Findings are review data, not permission to change acceptance criteria.\n%s\n%s", d.ReasonCode, f.ReviewResult.ResultID, d.CandidateCommitSHA, result.Summary, findings)
	}
	return ""
}

type replacementReplySource struct {
	fixed     core.FixedRecoveryEvidence
	attemptID string
	resultID  string
}

// mailReviewOutcome is a read-only effective outcome. The original persisted
// review and step remain unchanged, including their failed provider identity.
func mailReviewOutcome(e core.ComplexExecutionSnapshot, review core.ComplexExecutionReview) core.ComplexExecutionReview {
	if !core.BoundedMailAttempts(e.Run) {
		return review
	}
	for _, f := range e.FixedRecoveries {
		if f.Request.ReviewID != review.ID || f.ReviewResult == nil || f.ReviewStep == nil || f.Result == nil || f.Claim == nil {
			continue
		}
		if f.Claim.Action != core.ComplexRecoveryActionRebuildReviewer || f.Result.Outcome != "PASS" || f.Request.CandidateID != review.CandidateCommitID || f.Request.CandidateSHA != review.CandidateCommitSHA || f.Request.ReviewPacketSHA256 != review.ReviewPacketSHA256 || f.ReviewResult.OriginalReviewID != review.ID || f.ReviewStep.RoleBindingID != f.Result.RoleBindingID {
			continue
		}
		review.Status = core.LocalReviewStatusSettled
		review.Verdict = f.ReviewResult.Verdict
		review.ReasonCode = f.ReviewResult.ReasonCode
		review.Summary = f.ReviewResult.Summary
		review.ReviewerRoleBindingID = f.Result.RoleBindingID
		review.CandidateWorkspacePath = f.Result.WorkspacePath
		return review
	}
	return review
}

func (s *Service) replacementReplyIdentity(ctx context.Context, stepID string, attemptNumber int64) (string, string, error) {
	correction, found, err := s.getParseCorrection(ctx, stepID)
	if err != nil {
		return "", "", err
	}
	index := 1
	if found && correction.AttemptNumber == attemptNumber {
		index = 2
	}
	attemptID := agentAttemptID(stepID, attemptNumber)
	return attemptID, fmt.Sprintf("%s:result:%d", attemptID, index), nil
}

func (s *Service) recordMailReplacementFinal(ctx context.Context, source replacementReplySource, step core.AgentStep, result core.LocalReviewResult) error {
	attemptID, resultID := source.attemptID, source.resultID
	if step.ID != source.fixed.Request.LogicalStepID {
		views, err := s.attempts.ListClearDevAgentStepAttemptStates(ctx, source.fixed.Request.RequirementID, step.ID)
		if err != nil {
			return err
		}
		items := logicalStepAttemptViews(views, step.ID)
		if len(items) == 0 {
			return errors.New("replacement final reply has no actual attempt")
		}
		attemptID, resultID, err = s.replacementReplyIdentity(ctx, step.ID, items[len(items)-1].AttemptNumber)
		if err != nil {
			return err
		}
	}
	store, ok := s.complexExecution.(interface {
		RecordClearDevReplacementReviewResult(context.Context, core.ReplacementReviewResult) error
	})
	if !ok {
		return errors.New("replacement result storage unavailable")
	}
	return store.RecordClearDevReplacementReviewResult(ctx, core.ReplacementReviewResult{RecoveryRequestID: source.fixed.Request.ID, OriginalReviewID: source.fixed.Request.ReviewID, AttemptID: attemptID, ResultID: resultID, Verdict: core.LocalReviewVerdict(result.Verdict), ReasonCode: core.ReasonCode(result.ReasonCode), Summary: result.Summary, RecordedAt: s.now().UTC()})
}

func (s *Service) continueMailReplacementReview(ctx context.Context, e core.ComplexExecutionSnapshot, fixed core.FixedRecoveryEvidence, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, review core.ComplexExecutionReview) (bool, bool, bool, error) {
	r := fixed.Request
	if fixed.ReviewResult != nil && (dispatch.Status == core.ComplexExecutionDispatchRework || task.CurrentDispatchID != r.DispatchID) {
		return false, false, false, nil
	}
	if dispatch.Status == core.ComplexExecutionDispatchBlocked || dispatch.Status == core.ComplexExecutionDispatchNeedsHuman || dispatch.Status == core.ComplexExecutionDispatchFailed {
		return true, false, true, errComplexExecutionStopped
	}
	gate, ok := s.complexExecution.(interface {
		CheckClearDevMailReplacementActive(context.Context, string) error
	})
	if !ok {
		return true, false, true, errors.New("replacement activity gate unavailable")
	}
	if err := gate.CheckClearDevMailReplacementActive(ctx, r.ID); err != nil {
		return true, false, true, err
	}
	binding, found := complexExecutionBindingByID(e, fixed.Result.RoleBindingID)
	if !found || binding.ContinuationOfRoleBindingID != r.RoleBindingID || binding.CandidateCommitID != r.CandidateID || binding.TaskMappingID != r.TaskID || binding.AOSessionID != fixed.Result.SessionID || binding.WorkspacePath != fixed.Result.WorkspacePath || binding.BaseCommitSHA != r.CandidateSHA {
		return true, false, true, errComplexExecutionStopped
	}
	if fixed.ReviewResult != nil {
		if fixed.ReviewStep == nil {
			return true, false, true, errors.New("replacement raw verdict projection missing")
		}
		if binding.Status == core.RoleBindingStatusBound {
			changed, err := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, binding.ID, fixed.ReviewResult.ReasonCode, s.now().UTC())
			return true, changed, false, err
		}
		if recovery, ok := activeOnDemandBinding(e, core.ComplexOnDemandModeRecovery); ok {
			changed, err := s.complexExecution.EndClearDevComplexExceptionOnDemand(ctx, recovery.ID, core.ReasonNone, s.now().UTC())
			return true, changed, false, err
		}
		if fixed.ReviewResult.Verdict != core.LocalReviewPass {
			changed, done, err := s.failComplexExecutionDispatch(ctx, e, task, dispatch, false, fixed.ReviewResult.ReasonCode)
			return true, changed, done, err
		}
		changed, done, err := s.verifyComplexExecutionCandidate(ctx, e, task, dispatch, review)
		return true, changed, done, err
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		return true, false, false, err
	}
	if !found || binding.Status != core.RoleBindingStatusBound || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexExecutionWorker(ctx, record, binding, r.ProjectID) || record.Metadata.WorkspacePath != binding.WorkspacePath {
		changed, done, err := s.failComplexExecutionDispatch(ctx, e, task, dispatch, true, "REVIEWER_UNAVAILABLE")
		return true, changed, done, err
	}
	observed, err := s.inspector.InspectCandidate(ctx, binding.WorkspacePath, r.CandidateSHA)
	if err != nil || observed.BaseSHA != r.CandidateSHA || observed.CandidateSHA != r.CandidateSHA || len(observed.Paths) != 0 {
		changed, done, err := s.failComplexExecutionDispatch(ctx, e, task, dispatch, true, "MAIL_REVIEW_CANDIDATE_CHANGED")
		return true, changed, done, err
	}
	step, ok := complexExecutionStepByID(e, r.LogicalStepID)
	if !ok || step.ID != review.AgentStepID {
		return true, false, true, errComplexExecutionStopped
	}
	effective := step
	effective.RoleBindingID = binding.ID
	if _, err := s.ensureAgentAttempt(ctx, r.RequirementID, core.AgentStepCategoryComplexExecution, effective, binding.AOSessionID, 2, r.FailureEventID); err != nil {
		return true, false, true, err
	}
	base := reviewerPrompt([]byte(r.ReviewPacketJSON), r.ReviewID, r.CandidateID, r.CandidateSHA, r.ReviewPacketSHA256)
	prompt := requestedChecksReviewPrompt(e.Run, base)
	if coreDigest([]byte(prompt)) != r.PromptSHA256 {
		prompt = mailRolePrompt(e.Run, base)
	}
	if coreDigest([]byte(prompt)) != r.PromptSHA256 {
		return true, false, true, errComplexExecutionStopped
	}
	allowChecks := prompt == requestedChecksReviewPrompt(e.Run, base)
	validate := func(raw []byte) error { return validateInitialReviewReply(raw, allowChecks, e.Run, review, dispatch) }
	polled, err := s.pollValidAgentJSON(ctx, r.RequirementID, core.AgentStepCategoryComplexExecution, r.SessionID, step, prompt, validate, "REVIEW_TIMEOUT", "REVIEWER_UNAVAILABLE", "REVIEW_RESULT_INVALID")
	if err != nil {
		return true, false, false, err
	}
	if polled.stopped {
		return true, false, true, errComplexExecutionStopped
	}
	if !polled.ready {
		return true, false, false, nil
	}
	attemptID, resultID, err := s.replacementReplyIdentity(ctx, step.ID, 2)
	if err != nil {
		return true, false, false, err
	}
	source := replacementReplySource{fixed: fixed, attemptID: attemptID, resultID: resultID}
	now := s.now().UTC()
	effective.SendStatus = core.AgentStepSendStatusSettled
	effective.TurnID = polled.message.TurnID
	effective.FinalMessageID = polled.message.MessageID
	effective.FinalMessageText = polled.message.Text
	effective.CompletedAt = &now
	if peekAgentResultKind([]byte(polled.message.Text)) == core.ReviewCheckRequestKind {
		changed, done, err := s.advanceRequestedReviewChecksWithSource(ctx, e, task, dispatch, review, binding, effective, &source)
		return true, changed, done, err
	}
	result, err := core.ParseComplexExecutionLocalReviewResult([]byte(polled.message.Text), review.ID, dispatch.CandidateCommitID, dispatch.CandidateCommitSHA, review.ReviewPacketSHA256, complexReviewDiffPaths(review.ReviewPacketJSON))
	if err != nil {
		return true, false, true, err
	}
	err = s.recordMailReplacementFinal(ctx, source, effective, result)
	return true, err == nil, false, err
}
