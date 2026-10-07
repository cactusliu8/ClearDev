package cleardev

import (
	"context"
	"errors"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const mailReviewerReuseKeyPrefix = "cleardev-mail-review-rework:"

// A reuse is a new candidate binding, not a new provider session and not a
// fixed-recovery authorization. Never mutate the old review or carry its PASS.
func mailReviewerReuseSource(execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch) (core.ComplexExecutionRoleBinding, core.ComplexExecutionReview, bool, error) {
	_, mail, err := core.MailPolicyFromRun(execution.Run)
	if err != nil || !mail || dispatch.Round == 0 {
		return core.ComplexExecutionRoleBinding{}, core.ComplexExecutionReview{}, false, err
	}
	if core.BoundedMailAttempts(execution.Run) {
		return boundedMailReviewerSource(execution, task, dispatch)
	}
	invalid := errors.New("mail re-review has no exact ended REWORK predecessor")
	if dispatch.Round != 1 || len(execution.Tasks) != 1 || task.ID != execution.Tasks[0].ID || dispatch.ComplexExecutionTaskID != task.ID {
		return core.ComplexExecutionRoleBinding{}, core.ComplexExecutionReview{}, true, invalid
	}
	for _, previous := range execution.Dispatches {
		if previous.ComplexExecutionTaskID != task.ID || previous.Round != 0 || previous.BaseCommitSHA != dispatch.BaseCommitSHA || previous.CandidateCommitSHA == dispatch.CandidateCommitSHA {
			continue
		}
		review, found := complexExecutionReviewForDispatch(execution, previous.ID)
		if !found || review.Status != core.LocalReviewStatusSettled || review.Verdict != core.LocalReviewRework || review.CandidateCommitID != previous.CandidateCommitID || review.CandidateCommitSHA != previous.CandidateCommitSHA {
			continue
		}
		prior, found := complexExecutionBindingByID(execution, review.ReviewerRoleBindingID)
		if !found || prior.Role != core.StandardRoleReviewer || prior.Status != core.RoleBindingStatusEnded || prior.ExecutionRunID != execution.Run.ID || prior.TaskMappingID != task.ID || prior.CandidateCommitID != review.CandidateCommitID || prior.BaseCommitSHA != review.CandidateCommitSHA || prior.AOSessionID == "" || prior.WorkspacePath == "" {
			continue
		}
		step, found := effectiveReviewStep(execution, review)
		if !found || step.RoleBindingID != prior.ID || step.SendStatus != core.AgentStepSendStatusSettled || step.TurnID == "" || step.FinalMessageID == "" {
			continue
		}
		return prior, review, true, nil
	}
	return core.ComplexExecutionRoleBinding{}, core.ComplexExecutionReview{}, true, invalid
}

func (s *Service) validComplexReviewWorker(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, record domain.SessionRecord, binding core.ComplexExecutionRoleBinding, projectID string) bool {
	if !strings.HasPrefix(binding.SessionCreationIdempotencyKey, mailReviewerReuseKeyPrefix) {
		return s.validComplexExecutionWorker(ctx, record, binding, projectID)
	}
	prior, _, required, err := mailReviewerReuseSource(execution, task, dispatch)
	return err == nil && required && binding.ContinuationOfRoleBindingID == prior.ID && binding.SessionCreationIdempotencyKey == mailReviewerReuseKeyPrefix+dispatch.CandidateCommitID &&
		binding.AOSessionID == prior.AOSessionID && string(record.ID) == prior.AOSessionID && binding.WorkspacePath == prior.WorkspacePath && s.validComplexExecutionWorker(ctx, record, mailReviewerRoot(execution, prior), projectID)
}

func (s *Service) bindMailReviewerReuse(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, binding core.ComplexExecutionRoleBinding, builder domain.SessionRecord) (bool, bool, error) {
	fail := func() (bool, bool, error) {
		return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, "MAIL_REVIEWER_REUSE_UNAVAILABLE")
	}
	prior, previousReview, required, err := mailReviewerReuseSource(execution, task, dispatch)
	if err != nil || !required || binding.ContinuationOfRoleBindingID != prior.ID || binding.SessionCreationIdempotencyKey != mailReviewerReuseKeyPrefix+dispatch.CandidateCommitID {
		return fail()
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(prior.AOSessionID))
	if err != nil || !found || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexExecutionWorker(ctx, record, mailReviewerRoot(execution, prior), string(builder.ProjectID)) || record.Metadata.WorkspacePath != prior.WorkspacePath || record.Metadata.WorkspacePath == builder.Metadata.WorkspacePath || record.ID == builder.ID {
		return fail()
	}
	// Recheck the original model/login/quota, never the project's new default.
	// Empty is the existing provider-session default, not a request to spawn
	// another session; require the same preflight resolution below.
	preflight, found, err := s.preflights.GetLatestClearDevControlledPreflightForBinding(ctx, prior.ID)
	if err != nil || !found || preflight.Outcome != core.ControlledPreflightPassed {
		return fail()
	}
	blocked, model, err := s.runControlledModelPreflight(ctx, execution.Run.DevelopmentRequirementID, binding.ID, builder.ProjectID, preflight.ResolvedModel)
	if err != nil {
		return false, false, err
	}
	if blocked {
		return false, false, nil
	}
	if model != preflight.ResolvedModel {
		return fail()
	}
	// Do not move a worktree while a provider turn might still be using it.
	conversation, err := s.chat.Snapshot(ctx, record.ID)
	if err != nil || conversation.SessionID != record.ID {
		return fail()
	}
	previousStep, _ := effectiveReviewStep(execution, previousReview)
	completed := false
	for _, turn := range conversation.Turns {
		if !turn.State.Terminal() {
			return fail()
		}
		if turn.ID == previousStep.TurnID && turn.State == domain.TurnStateCompleted {
			completed = true
		}
	}
	if !completed {
		return fail()
	}
	observed, err := s.inspector.InspectCandidate(ctx, prior.WorkspacePath, prior.BaseCommitSHA)
	if err != nil || observed.BaseSHA != prior.BaseCommitSHA {
		return fail()
	}
	// An interruption after checkout but before binding may already be at the
	// exact new SHA. Only those two known clean heads are accepted.
	if observed.CandidateSHA != dispatch.CandidateCommitSHA {
		if observed.CandidateSHA != prior.BaseCommitSHA || len(observed.Paths) != 0 {
			return fail()
		}
		if err := s.inspector.PrepareBaseWorkspace(ctx, prior.WorkspacePath, prior.BaseCommitSHA, dispatch.CandidateCommitSHA); err != nil {
			return fail()
		}
	}
	observed, err = s.inspector.InspectCandidate(ctx, prior.WorkspacePath, dispatch.CandidateCommitSHA)
	if err != nil || observed.CandidateSHA != dispatch.CandidateCommitSHA || observed.BaseSHA != dispatch.CandidateCommitSHA || len(observed.Paths) != 0 {
		return fail()
	}
	_, err = s.complexExecution.BindClearDevComplexExecutionRoleBinding(ctx, binding.ID, prior.AOSessionID, prior.WorkspacePath, dispatch.CandidateCommitSHA, s.now().UTC())
	return err == nil, false, err
}
