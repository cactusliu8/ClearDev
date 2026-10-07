package cleardev

// MailReplacementCheckFollowup selects the actual replacement's budgeted
// result-return step, not the failed original provider request.
func MailReplacementCheckFollowup(e ComplexExecutionSnapshot, review ComplexExecutionReview, step AgentStep) bool {
	if !BoundedMailAttempts(e.Run) || step.ID != ReviewCheckFollowupID(review.ID) || step.RequestID != step.ID || step.Kind != AgentStepLocalReview {
		return false
	}
	for _, f := range e.FixedRecoveries {
		if f.Request.ReviewID != review.ID || f.Request.DispatchID != review.DispatchID || f.Request.CandidateID != review.CandidateCommitID || f.Request.CandidateSHA != review.CandidateCommitSHA || f.Claim == nil || f.Claim.Action != ComplexRecoveryActionRebuildReviewer || f.Result == nil || f.Result.Outcome != "PASS" || f.Result.RoleBindingID != step.RoleBindingID {
			continue
		}
		for _, binding := range e.RoleBindings {
			if binding.ID == step.RoleBindingID && binding.ExecutionRunID == e.Run.ID && binding.Role == StandardRoleReviewer && binding.CandidateCommitID == review.CandidateCommitID && binding.BaseCommitSHA == review.CandidateCommitSHA && binding.AOSessionID == f.Result.SessionID && binding.WorkspacePath == f.Result.WorkspacePath {
				return true
			}
		}
	}
	return false
}

func mailReplacementHasFollowup(e ComplexExecutionSnapshot, original AgentStep) bool {
	for _, review := range e.Reviews {
		if review.AgentStepID != original.ID {
			continue
		}
		for _, step := range e.AgentSteps {
			if MailReplacementCheckFollowup(e, review, step) {
				return true
			}
		}
	}
	return false
}

// MailReplacementResolvedOriginalStep recognizes only the original failed step
// whose exact authorized replacement verdict is already persisted. It does not
// suppress failures in the replacement or in another candidate's work.
func MailReplacementResolvedOriginalStep(e ComplexExecutionSnapshot, step AgentStep) bool {
	if !BoundedMailAttempts(e.Run) {
		return false
	}
	for _, f := range e.FixedRecoveries {
		if f.Request.LogicalStepID != step.ID || f.Request.RoleBindingID != step.RoleBindingID || f.Claim == nil || f.Claim.Action != ComplexRecoveryActionRebuildReviewer || f.Result == nil || f.Result.Outcome != "PASS" || f.ReviewResult == nil || f.ReviewStep == nil {
			continue
		}
		if f.ReviewResult.RecoveryRequestID != f.Request.ID || f.ReviewResult.OriginalReviewID != f.Request.ReviewID || f.ReviewStep.RoleBindingID != f.Result.RoleBindingID || f.ReviewStep.SendStatus != AgentStepSendStatusSettled || f.ReviewStep.TurnID == "" || f.ReviewStep.FinalMessageID == "" {
			continue
		}
		for _, d := range e.Dispatches {
			if d.ID == f.Request.DispatchID && d.CandidateCommitID == f.Request.CandidateID && d.CandidateCommitSHA == f.Request.CandidateSHA && (d.Status == ComplexExecutionDispatchRework || d.Status == ComplexExecutionDispatchNeedsHuman || d.Status == ComplexExecutionDispatchBlocked || d.Status == ComplexExecutionDispatchVerified) {
				return true
			}
		}
	}
	return false
}
