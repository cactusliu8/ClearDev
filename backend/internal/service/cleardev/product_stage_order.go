package cleardev

import (
	"context"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Later stages may use only the exact integrated delivery of their predecessor.
// Historical proposals keep their original baseline; selecting a delivered
// baseline creates a new discussion rather than rewriting an old Stage.
func (s *Service) requirePreviousProductStage(ctx context.Context, product core.ProductSnapshot, stage core.ProductStage) error {
	if stage.Definition.ExecutionBasis == nil || stage.Ordinal == 0 {
		return nil
	}
	for _, previous := range product.Stages {
		if previous.DiscussionID != stage.DiscussionID || previous.Ordinal != stage.Ordinal-1 || previous.DevelopmentRequirementID == "" {
			continue
		}
		if s.complexExecution == nil {
			break
		}
		execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, previous.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if !found || execution.Integration == nil {
			break
		}
		binding, _, err := s.completedProjectBaseline(ctx, product.Goal.ID, previous.DevelopmentRequirementID, execution.Integration.CandidateCommitSHA)
		if err != nil {
			break
		}
		if stage.Selection == nil || stage.Selection.BaseCommitSHA != binding.CandidateSHA {
			return apierr.Conflict("PRODUCT_PREVIOUS_STAGE_BASELINE_REQUIRED", "Select the previous stage's exact completed delivery for the next stage", nil)
		}
		return nil
	}
	return apierr.Conflict("PRODUCT_PREVIOUS_STAGE_REQUIRED", "The previous stage must pass final acceptance and complete delivery before planning this stage", nil)
}
