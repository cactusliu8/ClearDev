package cleardev

import "time"

// CurrentExecutionProgress projects current work without modifying scheduler facts.
// Historical failed steps and ended bindings remain in the public evidence arrays.
func CurrentExecutionProgress(e ComplexExecutionSnapshot, controlled []ControlledProgressFacts, now time.Time) (ComplexExecutionPhase, ReasonCode, []string) {
	if phase, reason, stopped := requirementFinalReviewStop(e); stopped {
		return phase, reason, DeriveComplexExecutionMissingEvidence(e)
	}
	if e.Run.CompletedAt != nil {
		if e.Integration == nil && e.Run.ReasonCode != ReasonNone {
			phase, reason := DeriveComplexExecutionPhase(e)
			return phase, reason, DeriveComplexExecutionMissingEvidence(e)
		}
		if !trustedExecutionComplete(e) {
			return ComplexExecutionBlocked, "CURRENT_COMPLETION_EVIDENCE_MISSING", []string{"CURRENT_COMPLETION_EVIDENCE"}
		}
		return ComplexExecutionCompleted, ReasonNone, []string{}
	}
	steps := make([]AgentStep, 0, len(e.AgentSteps))
	for _, step := range e.AgentSteps {
		if step.SendStatus != AgentStepSendStatusFailed {
			steps = append(steps, step)
		}
	}
	e.AgentSteps = steps
	bindings := make([]ComplexExecutionRoleBinding, 0, len(e.RoleBindings))
	for _, b := range e.RoleBindings {
		if b.Status == RoleBindingStatusEnded {
			continue
		}
		if b.Status == RoleBindingStatusFailed {
			selected := false
			for _, f := range controlled {
				if f.Target.RoleBindingID == b.ID {
					selected = true
				}
			}
			if !selected {
				continue
			}
			b.Status = RoleBindingStatusRequested
		}
		bindings = append(bindings, b)
	}
	e.RoleBindings = bindings
	e.Reviews = append([]ComplexExecutionReview(nil), e.Reviews...)
	for i, review := range e.Reviews {
		for _, recovery := range e.FixedRecoveries {
			if trustedReplacementReviewPassed(review, recovery) {
				e.Reviews[i].Status, e.Reviews[i].Verdict, e.Reviews[i].ReasonCode = LocalReviewStatusSettled, LocalReviewPass, ReasonNone
			}
		}
	}
	phase, reason := DeriveComplexExecutionPhase(e)
	missing := DeriveComplexExecutionMissingEvidence(e)
	var active *ControlledWork
	for _, f := range controlled {
		if w, ok := deriveControlledWork(f, now); ok && (active == nil || controlledRank(w) < controlledRank(*active)) {
			active = &w
		}
	}
	if active != nil && phase != ComplexExecutionNeedsHuman {
		if controlledRank(*active) == 0 {
			phase, reason, missing = ComplexExecutionBlocked, active.ReasonCode, []string{active.State}
		} else if active.PreflightID == "" && (active.State == "RECOVERY_PENDING" || active.State == "RECOVERY_UNCONFIRMED" || active.State == "WAITING_RETRY" || active.State == "RETRY_ELIGIBLE") {
			phase, reason, missing = ComplexExecutionRecovering, active.ReasonCode, []string{active.State}
		}
	}
	return phase, reason, missing
}

func trustedExecutionComplete(e ComplexExecutionSnapshot) bool {
	if _, _, stopped := requirementFinalReviewStop(e); stopped {
		return false
	}
	if e.Integration == nil || e.Integration.ExecutionRunID != e.Run.ID || len(e.Tasks) == 0 {
		return false
	}
	for _, t := range e.Tasks {
		if t.Status != DevelopmentTaskStatusDone {
			return false
		}
		d, ok := currentComplexExecutionDispatch(e, t)
		if !ok || d.CandidateCommitID == "" {
			return false
		}
		verified := false
		for _, v := range e.Verifications {
			if v.ExecutionRunID != e.Run.ID || v.ComplexExecutionTaskID != t.ID || v.DispatchID != d.ID || v.Round != d.Round || v.CandidateCommitID != d.CandidateCommitID || v.CandidateCommitSHA != d.CandidateCommitSHA || v.ScopeEvidenceID == "" || len(v.RequiredCheckRunIDs) == 0 {
				continue
			}
			review := false
			for _, r := range e.Reviews {
				if r.ID != v.LocalReviewID || r.DispatchID != d.ID || r.CandidateCommitID != d.CandidateCommitID || r.CandidateCommitSHA != d.CandidateCommitSHA {
					continue
				}
				review = r.Verdict == LocalReviewPass && r.Status == LocalReviewStatusSettled
				for _, recovery := range e.FixedRecoveries {
					if recovery.Request.ID == v.ReplacementRecoveryID && recovery.Request.ReviewID == r.ID && recovery.Request.CandidateID == d.CandidateCommitID && recovery.Request.CandidateSHA == d.CandidateCommitSHA && recovery.Result != nil && recovery.Result.Outcome == "PASS" && recovery.ReviewResult != nil && recovery.ReviewResult.AttemptID == v.ReplacementAttemptID && recovery.ReviewResult.ResultID == v.ReplacementResultID && recovery.ReviewResult.Verdict == LocalReviewPass {
						review = true
					}
				}
			}
			checks := true
			checkIDs := append([]string{v.ScopeEvidenceID}, v.RequiredCheckRunIDs...)
			for _, id := range checkIDs {
				found := false
				for _, r := range e.CheckRuns {
					if r.ID == id && r.DispatchID == d.ID && r.CandidateCommitID == d.CandidateCommitID && r.Status == ComplexExecutionCheckRunSettled && r.Result == EvidenceResultPass {
						found = true
					}
				}
				checks = checks && found
			}
			verified = review && checks
			if verified {
				break
			}
		}
		if !verified {
			return false
		}
	}
	if len(e.Integration.CheckRunIDs) == 0 {
		return false
	}
	for _, id := range e.Integration.CheckRunIDs {
		found := false
		for _, r := range e.CheckRuns {
			if r.ID == id && r.Status == ComplexExecutionCheckRunSettled && r.Result == EvidenceResultPass {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// CurrentQuickProgress checks current completion evidence before trusting the old projection.
func CurrentQuickProgress(q ComplexQuickSnapshot) (ComplexExecutionPhase, ReasonCode, []string) {
	if q.Run.CompletedAt != nil {
		valid := q.Task != nil && q.Task.Status == DevelopmentTaskStatusDone && q.Integration != nil && q.Integration.ExecutionRunID == q.Run.ID && len(q.Integration.CheckRunIDs) > 0
		candidate := ""
		if q.Task != nil {
			for _, d := range q.Dispatches {
				if d.ID == q.Task.CurrentDispatchID && d.Round == q.Task.CurrentRound {
					candidate = d.CandidateCommitID
				}
			}
		}
		valid = valid && candidate != ""
		if valid {
			for _, id := range q.Integration.CheckRunIDs {
				found := false
				for _, r := range q.CheckRuns {
					if r.ID == id && r.Status == ComplexExecutionCheckRunSettled && r.Result == EvidenceResultPass && r.CandidateCommitID == candidate {
						found = true
					}
				}
				valid = valid && found
			}
		}
		if !valid {
			return ComplexExecutionBlocked, "CURRENT_COMPLETION_EVIDENCE_MISSING", []string{"CURRENT_COMPLETION_EVIDENCE"}
		}
	}
	phase, reason := DeriveComplexQuickPhase(q)
	return phase, reason, DeriveComplexQuickMissingEvidence(q)
}

func trustedReplacementReviewPassed(review ComplexExecutionReview, recovery FixedRecoveryEvidence) bool {
	r, c, result, replacement := recovery.Request, recovery.Claim, recovery.Result, recovery.ReviewResult
	return c != nil && c.Action == ComplexRecoveryActionRebuildReviewer && result != nil && result.Outcome == "PASS" && replacement != nil && replacement.Verdict == LocalReviewPass && replacement.RecoveryRequestID == r.ID && replacement.OriginalReviewID == review.ID && replacement.AttemptID != "" && replacement.ResultID != "" && r.ReviewID == review.ID && r.LogicalStepID == review.AgentStepID && r.DispatchID == review.DispatchID && r.CandidateID == review.CandidateCommitID && r.CandidateSHA == review.CandidateCommitSHA
}
