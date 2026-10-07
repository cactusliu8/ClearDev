package cleardev

import (
	"errors"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func mailReviewerRoot(e core.ComplexExecutionSnapshot, b core.ComplexExecutionRoleBinding) core.ComplexExecutionRoleBinding {
	for i := 0; i < 5 && strings.HasPrefix(b.SessionCreationIdempotencyKey, mailReviewerReuseKeyPrefix); i++ {
		prior, ok := complexExecutionBindingByID(e, b.ContinuationOfRoleBindingID)
		if !ok || prior.TaskMappingID != b.TaskMappingID || prior.ExecutionRunID != b.ExecutionRunID || prior.AOSessionID != b.AOSessionID {
			return core.ComplexExecutionRoleBinding{}
		}
		b = prior
	}
	return b
}

func boundedMailReviewerSource(e core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, d core.ComplexExecutionDispatch) (core.ComplexExecutionRoleBinding, core.ComplexExecutionReview, bool, error) {
	invalid := errors.New("bounded mail review has no safe exact predecessor")
	policy, _, required, err := core.MailDeliveryPolicyFromRun(e.Run)
	if err != nil || !required || len(e.Tasks) < 1 || len(e.Tasks) > 3 || d.ComplexExecutionTaskID != task.ID || d.ExecutionRunID != e.Run.ID || task.ExecutionRunID != e.Run.ID || d.Round < 1 || d.Round > 4 {
		return core.ComplexExecutionRoleBinding{}, core.ComplexExecutionReview{}, true, invalid
	}
	if policy == core.MailDeliveryPolicyV1 && len(e.Tasks) != 1 {
		return core.ComplexExecutionRoleBinding{}, core.ComplexExecutionReview{}, true, invalid
	}
	member := false
	for _, current := range e.Tasks {
		if current.ID == task.ID && current.ExecutionRunID == e.Run.ID && current.DevelopmentTaskID == task.DevelopmentTaskID {
			member = true
		}
	}
	if !member {
		return core.ComplexExecutionRoleBinding{}, core.ComplexExecutionReview{}, true, invalid
	}
	var latest core.ComplexExecutionReview
	latestRound := -1
	for _, r := range e.Reviews {
		if r.ComplexExecutionTaskID != task.ID {
			continue
		}
		for _, a := range e.Dispatches {
			if a.ID == r.DispatchID && a.Round < d.Round && a.Round > latestRound {
				latest, latestRound = r, a.Round
			}
		}
	}
	if latestRound < 0 {
		return core.ComplexExecutionRoleBinding{}, core.ComplexExecutionReview{}, false, nil
	}
	latest = mailReviewOutcome(e, latest)
	prior, ok := complexExecutionBindingByID(e, latest.ReviewerRoleBindingID)
	step, stepOK := effectiveReviewStep(e, latest)
	if !ok || !stepOK || latest.Status != core.LocalReviewStatusSettled || latest.Verdict != core.LocalReviewRework || prior.Role != core.StandardRoleReviewer || prior.Status != core.RoleBindingStatusEnded || prior.ExecutionRunID != e.Run.ID || prior.TaskMappingID != task.ID || prior.CandidateCommitID != latest.CandidateCommitID || prior.BaseCommitSHA != latest.CandidateCommitSHA || prior.BaseCommitSHA == d.CandidateCommitSHA || step.SendStatus != core.AgentStepSendStatusSettled || step.RoleBindingID != prior.ID || step.TurnID == "" || step.FinalMessageID == "" {
		return prior, latest, true, invalid
	}
	return prior, latest, true, nil
}
