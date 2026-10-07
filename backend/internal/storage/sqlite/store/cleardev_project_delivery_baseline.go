package store

import (
	"context"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// validateProjectDeliverySelection repeats the exact final-delivery lookup
// inside discussion insertion. It is also used for inherited continuation
// contexts; no ordinary UI/CLI can manufacture provenance by supplying a SHA.
func validateProjectDeliverySelection(ctx context.Context, q *gen.Queries, productID string, selection core.ProductSelection) error {
	if err := core.ValidateProductDeliveryBaseline(selection); err != nil {
		return productConflict(err.Error())
	}
	if selection.Delivery == nil {
		return nil
	}
	binding := selection.Delivery
	run, err := q.GetClearDevComplexExecutionRun(ctx, binding.ExecutionRunID)
	if err != nil {
		return err
	}
	effective, err := effectiveProjectRun(ctx, q, run)
	if err != nil {
		return err
	}
	contract, admitted, err := core.ProjectContractFromRun(effective)
	if err != nil || !admitted || run.Status != "COMPLETED" || !run.SettledAt.Valid ||
		run.DevelopmentProjectID != binding.RequirementID || contract.ProductID != productID ||
		contract.Selection.AOProjectID != selection.AOProjectID || contract.Selection.RepositoryPath != selection.RepositoryPath ||
		contract.Selection.RepositoryURL != selection.RepositoryURL || !core.ProductOptionsEqual(contract.Selection.Option, selection.Option) {
		return productConflict("a delivery choice must belong to the same product and registered repository")
	}
	result, err := q.GetClearDevComplexExecutionResult(ctx, run.ID)
	if err != nil {
		return err
	}
	candidate, err := q.GetClearDevIntegrationCandidate(ctx, result.IntegrationCandidateID)
	if err != nil {
		return err
	}
	if result.ID != binding.ResultID || result.CompletionStatus != "COMPLETED" || result.IntegrationCandidateID != binding.IntegrationCandidateID ||
		candidate.CommitSha != binding.CandidateSHA || candidate.CommitSha != selection.BaseCommitSHA || candidate.RequirementVersionID.String != run.RequirementVersionID ||
		candidate.ComplexExecutionResultID.String != result.ID || !result.CompletedAt.Valid {
		return productConflict("a task-local or unreviewed candidate cannot become the next Stage baseline")
	}
	checks, err := q.ListClearDevComplexExecutionResultChecks(ctx, result.ID)
	if err != nil {
		return err
	}
	integration := core.ComplexExecutionIntegration{
		ID: result.ID, ExecutionRunID: run.ID, IntegrationCandidateID: result.IntegrationCandidateID,
		CandidateCommitSHA: candidate.CommitSha,
	}
	for _, check := range checks {
		integration.CheckRunIDs = append(integration.CheckRunIDs, check.CheckRunID)
	}
	if err := validateProjectCheckSet(ctx, q, contract, "", candidate.CommitSha, integration.CheckRunIDs); err != nil {
		return err
	}
	return validateRequirementFinalReviewCompletion(ctx, q, run, integration)
}
