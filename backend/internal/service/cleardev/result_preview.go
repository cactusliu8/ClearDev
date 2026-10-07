package cleardev

import (
	"context"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
)

// ResultPreviewManager is the existing session-owned preview lifecycle, not a
// checker or a second ClearDev execution engine. A preview never records PASS.
type ResultPreviewManager interface {
	StartMail(context.Context, domain.SessionID, string, string, string) (previewserver.Status, error)
	Stop(context.Context, domain.SessionID) (previewserver.Status, error)
	MailStatus(domain.SessionID, string) previewserver.Status
}

// ProjectResultPreviewManager extends the same preview lifecycle for an exact
// admitted project contract. Old preview managers cannot silently ignore it.
type ProjectResultPreviewManager interface {
	StartProject(context.Context, domain.SessionID, string, string, core.ProjectExecutionContract, ports.ClearDevProjectResultPrepare) (previewserver.Status, error)
	ProjectStatus(domain.SessionID, string, string) previewserver.Status
	ProjectDataReady(context.Context, core.ProjectExecutionContract, string) (bool, error)
	ProjectDataBaselineCurrent(context.Context, core.ProjectExecutionContract, string) (bool, error)
}

// ResultPreviewView binds an interactive application to an already completed
// delivery. The persisted contact database belongs to the project, not a turn.
type ResultPreviewView struct {
	CandidateSHA string               `json:"candidateSha"`
	SourceBranch string               `json:"sourceBranch"`
	Preview      previewserver.Status `json:"preview"`
	DataReady    bool                 `json:"dataReady"`
}

type resultPreviewTarget struct {
	session   domain.SessionRecord
	sha       string
	workspace string
	branch    string
	project   *core.ProjectExecutionContract
}

func (s *Service) resultPreviewTarget(ctx context.Context, id string) (resultPreviewTarget, error) {
	var target resultPreviewTarget
	if s.resultPreview == nil || s.inspector == nil || s.ao == nil {
		return target, apierr.Internal("RESULT_PREVIEW_UNAVAILABLE", "Completed result preview is not configured")
	}
	view, err := s.GetRequirement(ctx, id)
	if err != nil {
		return target, err
	}
	execution := view.ComplexExecution
	if execution == nil || execution.Run.CompletedAt == nil || execution.Integration == nil || len(execution.Tasks) < 1 || len(execution.Tasks) > 3 {
		return target, apierr.Conflict("RESULT_NOT_COMPLETED", "Only a completed, independently reviewed bounded delivery can be opened", nil)
	}
	// Completion remains the authority: this endpoint cannot bless an arbitrary
	// session, branch, candidate, command, database path or unfinished result.
	contract, project, projectErr := core.ProjectContractFromRun(execution.Run)
	if projectErr != nil {
		return target, apierr.Conflict("RESULT_BINDING_CHANGED", "The completed project contract is invalid", nil)
	}
	if project {
		if execution.FinalReview == nil || execution.FinalReview.Status != "SETTLED" || execution.FinalReview.Verdict != "PASS" ||
			core.ValidateRequirementFinalReviewBinding(*execution.FinalReview, execution.Run, execution.Integration.CandidateCommitSHA, execution.Integration.CheckRunIDs) != nil {
			return target, apierr.Conflict("RESULT_NOT_COMPLETED", "The exact project delivery has no independent final PASS", nil)
		}
		target.project = &contract
	} else if _, mail, policyErr := core.MailPolicyFromRun(execution.Run); policyErr != nil || !mail {
		return target, apierr.Conflict("RESULT_NOT_COMPLETED", "This delivery has no supported versioned runtime contract", nil)
	}
	for _, candidate := range view.TrustedProgress.CurrentCandidates {
		if candidate.Kind == "INTEGRATION" && candidate.Current {
			target.sha = candidate.CommitSHA
		}
	}
	if !validComplexExecutionCommitSHA(target.sha) || target.sha != execution.Integration.CandidateCommitSHA {
		return target, apierr.Conflict("RESULT_NOT_COMPLETED", "The completed delivery SHA is missing", nil)
	}
	bound := false
	for _, candidate := range view.IntegrationCandidates {
		bound = bound || candidate.CommitSHA == target.sha && candidate.AOSessionID == execution.Run.BuilderAOSessionID && candidate.RequirementVersionID == execution.Run.RequirementVersionID
	}
	if !bound {
		return target, apierr.Conflict("RESULT_BINDING_CHANGED", "The delivery no longer matches its original Builder", nil)
	}
	session, found, err := s.ao.GetSession(ctx, domain.SessionID(execution.Run.BuilderAOSessionID))
	if err != nil || !found || string(session.ProjectID) != view.Requirement.AOProjectID || session.Kind != domain.KindWorker || session.Metadata.WorkspacePath == "" {
		return target, apierr.Conflict("RESULT_WORKSPACE_UNAVAILABLE", "The delivered application workspace is unavailable", nil)
	}
	target.session, target.workspace, target.branch = session, session.Metadata.WorkspacePath, session.Metadata.Branch
	if execution.Run.Mode == core.WorkModeParallel {
		composition, ok := core.ComplexExecutionFinalComposition(*execution)
		if !ok || composition.OutputCommitSHA != target.sha || composition.WorkspacePath == "" || execution.FinalReview == nil || execution.FinalReview.Status != "SETTLED" || execution.FinalReview.Verdict != "PASS" || execution.FinalReview.CandidateCommitSHA != target.sha {
			return target, apierr.Conflict("RESULT_BINDING_CHANGED", "The completed integration source is unavailable", nil)
		}
		// Keep the real session only as preview lifecycle owner. The immutable
		// composition, not a Builder's partial workspace, supplies the code.
		target.workspace = composition.WorkspacePath
		target.branch = core.ComplexCompositionBranch(composition.RequestID)
	}
	return target, nil
}

func (s *Service) checkResultPreviewSource(ctx context.Context, id string, target resultPreviewTarget) error {
	view, err := s.GetRequirement(ctx, id)
	if err != nil {
		return err
	}
	if view.TrustedProgress.Phase != "COMPLETED" || len(view.TrustedProgress.Blockers) != 0 || len(view.TrustedProgress.PendingDecisions) != 0 || len(view.TrustedProgress.MissingEvidence) != 0 || target.session.IsTerminated || target.session.Activity.State != domain.ActivityIdle {
		return apierr.Conflict("RESULT_NOT_READY", "The delivery has pending work or its session is not idle", nil)
	}
	if err := s.checkResultPreviewBranch(ctx, target); err != nil {
		return err
	}
	var run core.ComplexExecutionRun
	if view.ComplexExecution != nil {
		run = view.ComplexExecution.Run
	}
	inspection, err := s.inspectExecutionCandidate(ctx, run, target.workspace, target.sha)
	if err != nil || inspection.BaseSHA != target.sha || inspection.CandidateSHA != target.sha || len(inspection.Paths) != 0 {
		return apierr.Conflict("RESULT_SOURCE_CHANGED", "The delivered workspace is dirty or no longer at the reviewed SHA; no application was accepted", nil)
	}
	return nil
}

func (s *Service) checkResultPreviewBranch(ctx context.Context, target resultPreviewTarget) error {
	inspector, ok := s.inspector.(ports.ClearDevDeliveryBranchInspector)
	if !ok || inspector.InspectDeliveryBranch(ctx, target.workspace, target.branch, target.sha) != nil {
		return apierr.Conflict("RESULT_SOURCE_CHANGED", "The delivery branch no longer identifies the completed SHA", nil)
	}
	return nil
}

// StartResultPreview starts the exact protected completed delivery for interactive local use.
func (s *Service) StartResultPreview(ctx context.Context, id string) (ResultPreviewView, error) {
	target, err := s.resultPreviewTarget(ctx, id)
	if err != nil {
		return ResultPreviewView{}, err
	}
	if err := s.checkResultPreviewSource(ctx, id, target); err != nil {
		return ResultPreviewView{}, err
	}
	var status previewserver.Status
	if target.project != nil {
		manager, supported := s.resultPreview.(ProjectResultPreviewManager)
		preparer, prepared := s.checks.(ports.ClearDevProjectResultPreparer)
		if !supported || !prepared {
			return ResultPreviewView{}, apierr.Conflict(string(core.ReasonProjectRuntime), "The configured runtime cannot open this exact project delivery", nil)
		}
		status, err = manager.StartProject(ctx, target.session.ID, target.workspace, target.sha, *target.project, func(ctx context.Context) (ports.ClearDevProjectResultSource, error) {
			return preparer.PrepareProjectResult(ctx, target.workspace, target.sha, *target.project)
		})
	} else {
		status, err = s.resultPreview.StartMail(ctx, target.session.ID, target.workspace, string(target.session.ProjectID), target.sha)
	}
	if err != nil {
		return ResultPreviewView{}, apierr.Conflict("RESULT_START_FAILED", err.Error(), nil)
	}
	// Read the actual session again after startup rather than reusing its idle
	// snapshot. A concurrent edit or turn cannot be presented as the old result.
	current, err := s.resultPreviewTarget(ctx, id)
	if err == nil && (current.sha != target.sha || current.workspace != target.workspace || current.branch != target.branch || current.session.ID != target.session.ID) {
		err = errors.New("delivery binding changed while starting")
	}
	if err == nil {
		err = s.checkResultPreviewSource(ctx, id, current)
	}
	if err != nil {
		_, _ = s.resultPreview.Stop(context.WithoutCancel(ctx), target.session.ID)
		return ResultPreviewView{}, apierr.Conflict("RESULT_SOURCE_CHANGED", "The result changed while starting; its preview was stopped", nil)
	}
	return s.previewResult(ctx, target, status), nil
}

// GetResultPreview reports the interactive preview state of the protected completed delivery.
func (s *Service) GetResultPreview(ctx context.Context, id string) (ResultPreviewView, error) {
	target, err := s.resultPreviewTarget(ctx, id)
	if err != nil {
		return ResultPreviewView{}, err
	}
	if err := s.checkResultPreviewBranch(ctx, target); err != nil {
		return ResultPreviewView{}, err
	}
	status := s.resultPreview.MailStatus(target.session.ID, target.sha)
	if target.project != nil {
		manager, supported := s.resultPreview.(ProjectResultPreviewManager)
		if !supported {
			return ResultPreviewView{}, apierr.Conflict(string(core.ReasonProjectRuntime), "The project preview manager is unavailable", nil)
		}
		digest, err := core.ProjectExecutionContractDigest(*target.project)
		if err != nil {
			return ResultPreviewView{}, err
		}
		status = manager.ProjectStatus(target.session.ID, target.sha, digest)
	}
	if status.State == previewserver.StateReady {
		if err := s.checkResultPreviewSource(ctx, id, target); err != nil {
			return ResultPreviewView{}, err
		}
	}
	return s.previewResult(ctx, target, status), nil
}

// StopResultPreview stops the interactive preview without deleting project data or completion facts.
func (s *Service) StopResultPreview(ctx context.Context, id string) (ResultPreviewView, error) {
	target, err := s.resultPreviewTarget(ctx, id)
	if err != nil {
		return ResultPreviewView{}, err
	}
	// Stopping must remain possible even if the workspace has since changed.
	status, err := s.resultPreview.Stop(ctx, target.session.ID)
	return s.previewResult(ctx, target, status), err
}

func (s *Service) previewResult(ctx context.Context, target resultPreviewTarget, status previewserver.Status) ResultPreviewView {
	view := previewResult(target, status)
	if target.project != nil {
		if manager, ok := s.resultPreview.(ProjectResultPreviewManager); ok {
			view.DataReady, _ = manager.ProjectDataReady(ctx, *target.project, target.sha)
		}
	}
	return view
}

func previewResult(target resultPreviewTarget, status previewserver.Status) ResultPreviewView {
	return ResultPreviewView{CandidateSHA: target.sha, SourceBranch: target.branch, Preview: status}
}
