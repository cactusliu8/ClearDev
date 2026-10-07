package cleardev

import (
	"context"
	"errors"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
)

// StageReviewTrialManager extends the existing preview lifecycle; arbitrary
// launch-file previews cannot enable this service-owned mode.
type StageReviewTrialManager interface {
	StartStageTrial(context.Context, domain.SessionID, string, string, core.ProjectExecutionContract, ports.ClearDevProjectResultPrepare) (previewserver.Status, error)
	StageTrialStatus(domain.SessionID, string, string) previewserver.Status
}

// StageReviewTrialView reports runtime readiness, never functional acceptance.
type StageReviewTrialView struct {
	CandidateSHA string                   `json:"candidateSha"`
	Preview      previewserver.Status     `json:"preview"`
	Trial        *core.ProjectTrial       `json:"trial,omitempty"`
	Observation  *TrialCommandObservation `json:"observation,omitempty"`
}

// StageReviewTrial derives every runtime binding from the current saved review.
// The worker presents its existing unguessable session capability, not a role
// name, command, source directory or self-reported candidate.
func (s *Service) StageReviewTrial(ctx context.Context, id, sessionID, capability, action string) (StageReviewTrialView, error) {
	var out StageReviewTrialView
	if action != "start" && action != "status" && action != "stop" && !strings.HasPrefix(action, "run:") {
		return out, apierr.Invalid("STAGE_TRIAL_ACTION_INVALID", "Use start, status, stop or run:<frozen-step-id>", nil)
	}
	if s.ao == nil || s.inspector == nil || s.complexExecution == nil || s.complex == nil {
		return out, apierr.Internal("STAGE_TRIAL_UNAVAILABLE", "Stage trial is not configured")
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(sessionID))
	if err != nil {
		return out, err
	}
	if !found || record.IsTerminated || !browser.NewAuthority().Valid(record.ID, capability, record.Metadata.BrowserCapabilityVerifier) {
		return out, apierr.Forbidden("STAGE_TRIAL_OWNER_REQUIRED", "Only the owning Stage Reviewer may request its trial")
	}
	execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, id)
	if err != nil {
		return out, err
	}
	if !found || execution.Run.CompletedAt != nil || execution.FinalReview == nil {
		return out, apierr.Conflict("STAGE_TRIAL_REVIEW_REQUIRED", "No current unfinished Stage review", nil)
	}
	review := *execution.FinalReview
	if review.Status != "SENT" || review.AOSessionID != sessionID || !finalReviewRequiresTrial(review) ||
		core.ValidateRequirementFinalReviewBinding(review, execution.Run, review.CandidateCommitSHA, review.CheckRunIDs) != nil {
		return out, apierr.Conflict("STAGE_TRIAL_BINDING_INVALID", "The current review does not authorize this trial", nil)
	}
	requirement, found, err := s.facts.GetClearDevRequirement(ctx, id)
	if err != nil {
		return out, err
	}
	version, confirmed := currentConfirmedVersion(requirement)
	if !found || !confirmed || version.ID != review.RequirementVersionID || version.SHA256 != review.RequirementSHA256 {
		return out, apierr.Conflict("STAGE_TRIAL_BINDING_INVALID", "The confirmed Stage specification changed", nil)
	}
	planning, found, err := s.complex.GetClearDevComplexPlanning(ctx, id)
	if err != nil || !found {
		return out, apierr.Conflict("STAGE_TRIAL_BINDING_INVALID", "The current planning source is unavailable", nil)
	}
	if _, _, _, err := s.executionPlanForRun(ctx, planning, execution.Run); err != nil {
		return out, apierr.Conflict("STAGE_TRIAL_BINDING_INVALID", "The approved plan changed", nil)
	}
	contract, project, err := core.ProjectContractFromRun(execution.Run)
	if err != nil || !project || !s.validRequirementFinalReviewer(ctx, record, review, execution, planning, contract.Selection.AOProjectID) || record.Metadata.WorkspacePath != review.WorkspacePath {
		return out, apierr.Conflict("STAGE_TRIAL_BINDING_INVALID", "The reviewer or project runtime changed", nil)
	}
	manager, ok := s.resultPreview.(StageReviewTrialManager)
	if !ok && core.ProjectTrialNeedsService(contract.Basis) {
		return out, apierr.Internal("STAGE_TRIAL_UNAVAILABLE", "The controlled stage runtime is unavailable")
	}
	digest, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		return out, err
	}
	out.CandidateSHA = review.CandidateCommitSHA
	out.Trial = contract.Basis.Trial
	out.Preview = previewserver.Status{SessionID: record.ID, State: previewserver.StateStopped, Logs: []string{}}
	// Stop remains available even if a source became dirty during trial.
	if action == "stop" {
		if !core.ProjectTrialNeedsService(contract.Basis) {
			return out, nil
		}
		out.Preview, err = s.resultPreview.Stop(ctx, record.ID)
		return out, err
	}
	if !s.finalReviewWorkspaceMatches(ctx, review.SourceWorkspacePath, review.CandidateCommitSHA, execution.Run) || !s.finalReviewWorkspaceMatches(ctx, review.WorkspacePath, review.CandidateCommitSHA, execution.Run) {
		return out, apierr.Conflict("STAGE_TRIAL_CANDIDATE_CHANGED", "The exact final source or reviewer worktree changed", nil)
	}
	if strings.HasPrefix(action, "run:") {
		observation, runErr := s.runStageTrialCommand(ctx, review, contract, strings.TrimPrefix(action, "run:"))
		out.Observation = &observation
		if runErr != nil {
			return out, runErr
		}
		current, exists, readErr := s.complexExecution.GetClearDevComplexExecution(ctx, id)
		if readErr != nil || !exists || current.FinalReview == nil || current.FinalReview.ID != review.ID || current.FinalReview.Status != "SENT" || (!s.finalReviewWorkspaceMatches(ctx, review.WorkspacePath, review.CandidateCommitSHA, execution.Run) || !s.finalReviewWorkspaceMatches(ctx, review.SourceWorkspacePath, review.CandidateCommitSHA, execution.Run)) {
			return out, apierr.Conflict("STAGE_TRIAL_BINDING_CHANGED", "The review changed during the operation", nil)
		}
		if _, err := s.StageReviewTrial(ctx, id, sessionID, capability, "status"); err != nil {
			return out, err
		}
		return out, nil
	}
	if !core.ProjectTrialNeedsService(contract.Basis) {
		out.Preview = previewserver.Status{SessionID: record.ID, State: previewserver.StateStopped, Logs: []string{}}
		return out, nil
	}
	if action == "status" {
		out.Preview = manager.StageTrialStatus(record.ID, review.CandidateCommitSHA, digest)
		return out, nil
	}
	preparer, ok := s.checks.(ports.ClearDevProjectResultPreparer)
	if !ok {
		return out, apierr.Internal("STAGE_TRIAL_UNAVAILABLE", "Candidate preparation is unavailable")
	}
	out.Preview, err = manager.StartStageTrial(ctx, record.ID, review.SourceWorkspacePath, review.CandidateCommitSHA, contract,
		func(ctx context.Context) (ports.ClearDevProjectResultSource, error) {
			return preparer.PrepareProjectResult(ctx, review.SourceWorkspacePath, review.CandidateCommitSHA, contract)
		})
	if err != nil {
		return out, err
	}
	// Reject a changed review or candidate while the controlled runtime prepared.
	current, found, readErr := s.complexExecution.GetClearDevComplexExecution(ctx, id)
	if readErr != nil || !found || current.FinalReview == nil || current.FinalReview.ID != review.ID || current.FinalReview.Status != "SENT" ||
		!s.finalReviewWorkspaceMatches(ctx, review.SourceWorkspacePath, review.CandidateCommitSHA, execution.Run) || !s.finalReviewWorkspaceMatches(ctx, review.WorkspacePath, review.CandidateCommitSHA, execution.Run) {
		_, stopErr := s.resultPreview.Stop(ctx, record.ID)
		return out, errors.Join(apierr.Conflict("STAGE_TRIAL_CANDIDATE_CHANGED", "The review changed during runtime preparation", nil), readErr, stopErr)
	}
	return out, nil
}
