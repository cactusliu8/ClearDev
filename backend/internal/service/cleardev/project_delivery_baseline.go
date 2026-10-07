package cleardev

import (
	"context"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// completedProjectBaseline reads the actual completed integration and exact
// independent final review. A task verification, local Builder commit or
// successful preview is not eligible as a next-stage source.
func (s *Service) completedProjectBaseline(ctx context.Context, productID, requirementID, candidateSHA string) (core.ProductDeliveryBaseline, core.ProjectExecutionContract, error) {
	var binding core.ProductDeliveryBaseline
	var contract core.ProjectExecutionContract
	if s.complexExecution == nil || s.facts == nil || !validComplexExecutionCommitSHA(candidateSHA) {
		return binding, contract, errors.New("the completed project execution store is unavailable")
	}
	execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil {
		return binding, contract, err
	}
	if !found || execution.Run.CompletedAt == nil || execution.Integration == nil || execution.FinalReview == nil ||
		execution.Integration.CandidateCommitSHA != candidateSHA || execution.Integration.ExecutionRunID != execution.Run.ID ||
		execution.FinalReview.Status != "SETTLED" || execution.FinalReview.Verdict != "PASS" ||
		core.ValidateRequirementFinalReviewBinding(*execution.FinalReview, execution.Run, candidateSHA, execution.Integration.CheckRunIDs) != nil {
		return binding, contract, errors.New("only the exact independently finalized integrated delivery can become a new baseline")
	}
	contract, project, err := core.ProjectContractFromRun(execution.Run)
	if err != nil || !project || contract.ProductID != productID || execution.Run.DevelopmentRequirementID != requirementID {
		return binding, contract, errors.New("the selected delivery belongs to another product or protocol")
	}
	snapshot, exists, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil || !exists {
		return binding, contract, errors.New("the completed requirement facts are unavailable")
	}
	candidateFound := false
	for _, candidate := range snapshot.IntegrationCandidates {
		candidateFound = candidateFound || candidate.ID == execution.Integration.IntegrationCandidateID && candidate.CommitSHA == candidateSHA &&
			candidate.RequirementVersionID == execution.Run.RequirementVersionID && candidate.DevelopmentRequirementID == requirementID &&
			candidate.TaskSetVersion != nil && *candidate.TaskSetVersion == execution.Run.TaskSetVersion
	}
	if !candidateFound {
		return binding, contract, errors.New("the final integration candidate is missing or untrusted")
	}
	binding = core.ProductDeliveryBaseline{
		RequirementID: requirementID, ExecutionRunID: execution.Run.ID, ResultID: execution.Integration.ID,
		IntegrationCandidateID: execution.Integration.IntegrationCandidateID, CandidateSHA: candidateSHA,
	}
	return binding, contract, nil
}

func (s *Service) observeDeliveredProductChoice(ctx context.Context, productID, proposalID string, choice ProductChoiceInput, option core.ProductOption, project domain.ProjectRecord) (*core.ProductSelection, error) {
	if choice.ExpectedBaseCommitSHA == "" {
		return nil, apierr.Invalid("PRODUCT_DELIVERY_BINDING_REQUIRED", "Choose the displayed exact completed candidate", nil)
	}
	binding, prior, err := s.completedProjectBaseline(ctx, productID, choice.DeliveryRequirementID, choice.ExpectedBaseCommitSHA)
	if err != nil || prior.Selection.AOProjectID != choice.AOProjectID || prior.Selection.RepositoryPath != project.Path || !core.ProductOptionsEqual(prior.Selection.Option, option) {
		return nil, apierr.Conflict("PRODUCT_DELIVERY_NOT_COMPLETED", "The selected candidate is not this project's exact final delivery", nil)
	}
	// A later delivery must finish migrating its inherited data before it can
	// become another Stage's baseline. Otherwise A -> B -> C could skip opening
	// B and ask C to migrate data that is still at A's schema.
	if prior.Selection.Delivery != nil {
		manager, supported := s.resultPreview.(ProjectResultPreviewManager)
		if !supported {
			return nil, apierr.Conflict("RESULT_DATA_PREPARATION_REQUIRED", "The project data runtime is unavailable", nil)
		}
		ready, err := manager.ProjectDataReady(ctx, prior, binding.CandidateSHA)
		if err != nil || !ready {
			return nil, apierr.Conflict("RESULT_DATA_PREPARATION_REQUIRED", "Open this completed delivery and finish its data migration before selecting it for another Stage", nil)
		}
	}
	inspector, supported := s.inspector.(ports.ClearDevDeliveredProjectSourceInspector)
	if !supported {
		return nil, apierr.Conflict("PRODUCT_SOURCE_INSPECTION_UNAVAILABLE", "The current adapter cannot verify a delivered project baseline", nil)
	}
	observed, err := inspector.InspectDeliveredProjectSource(ctx, project.Path, project.Config.DefaultBranch, binding.CandidateSHA)
	if err != nil || observed.BaseCommitSHA != binding.CandidateSHA || repositoryIdentity(observed.RepositoryURL) != prior.Selection.RepositoryURL {
		return nil, apierr.Conflict("PRODUCT_DELIVERY_SOURCE_CHANGED", "The completed Git candidate or registered repository is no longer available", nil)
	}
	return &core.ProductSelection{
		SourceDiscussionID: proposalID, Option: option, Reason: choice.Reason, AOProjectID: choice.AOProjectID,
		RepositoryPath: project.Path, RepositoryURL: prior.Selection.RepositoryURL, BaseCommitSHA: binding.CandidateSHA,
		CreatedAt: s.now().UTC(), Delivery: &binding,
	}, nil
}

func (s *Service) selectedDeliveryCurrent(ctx context.Context, selection *core.ProductSelection, project domain.ProjectRecord) bool {
	if selection == nil || selection.Delivery == nil || core.ValidateProductDeliveryBaseline(*selection) != nil {
		return false
	}
	// Product identity comes from the previously admitted immutable run, not
	// from a newly supplied option or a free-form repository SHA.
	execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, selection.Delivery.RequirementID)
	if err != nil || !found {
		return false
	}
	prior, admitted, err := core.ProjectContractFromRun(execution.Run)
	if err != nil || !admitted || prior.Selection.AOProjectID != selection.AOProjectID || prior.Selection.RepositoryPath != selection.RepositoryPath {
		return false
	}
	binding, _, err := s.completedProjectBaseline(ctx, prior.ProductID, selection.Delivery.RequirementID, selection.BaseCommitSHA)
	if err != nil || binding != *selection.Delivery {
		return false
	}
	manager, supported := s.resultPreview.(ProjectResultPreviewManager)
	if !supported {
		return false
	}
	compatible, err := manager.ProjectDataBaselineCurrent(ctx, prior, binding.CandidateSHA)
	if err != nil || !compatible {
		return false
	}
	inspector, supported := s.inspector.(ports.ClearDevDeliveredProjectSourceInspector)
	if !supported {
		return false
	}
	observed, err := inspector.InspectDeliveredProjectSource(ctx, project.Path, project.Config.DefaultBranch, selection.BaseCommitSHA)
	return err == nil && observed.BaseCommitSHA == selection.BaseCommitSHA && repositoryIdentity(observed.RepositoryURL) == selection.RepositoryURL
}
