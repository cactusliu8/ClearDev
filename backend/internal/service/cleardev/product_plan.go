package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type productPlanStore interface {
	RequestClearDevProductPlan(context.Context, core.ProductPlanBinding, time.Time) error
	GetClearDevProductPlanAuthorization(context.Context, string, string) (*core.ProductPlanAuthorization, error)
	ListClearDevAutomaticProducts(context.Context) ([]string, error)
	BindClearDevAutomaticStageSource(context.Context, string, string, core.ProductSelection, time.Time) error
	ConfirmClearDevAutomaticStage(context.Context, string, string, time.Time) error
	RecordClearDevProductPlanAdvance(context.Context, string, string, string, time.Time) error
}

func (s *Service) requestProductPlan(ctx context.Context, product core.ProductSnapshot) (ProductGoalView, error) {
	store, ok := s.complex.(productPlanStore)
	if !ok {
		return ProductGoalView{}, apierr.Internal("PRODUCT_AUTOMATION_UNAVAILABLE", "Product plan authorization is unavailable")
	}
	if len(product.Discussions) == 0 {
		return ProductGoalView{}, apierr.Conflict("PRODUCT_PLAN_REQUIRED", "A ready product plan is required", nil)
	}
	d := product.Discussions[len(product.Discussions)-1]
	if d.Result == nil || d.Result.Outcome != "READY" || d.Selection == nil {
		return ProductGoalView{}, apierr.Conflict("PRODUCT_PLAN_REQUIRED", "Confirm a ready product plan and its project selection", nil)
	}
	raw, err := json.Marshal(d.Selection)
	if err != nil {
		return ProductGoalView{}, err
	}
	b := core.ProductPlanBinding{ProductID: product.Goal.ID, DiscussionID: d.ID, ResultSHA256: d.ResultSHA256, SelectionSHA256: coreDigest(raw)}
	if err := store.RequestClearDevProductPlan(ctx, b, s.now().UTC()); err != nil {
		return ProductGoalView{}, err
	}
	if a, e := s.automaticProductAuthorization(ctx, product); e != nil {
		return ProductGoalView{}, e
	} else if a != nil {
		s.scheduleComplexFlow(product.Goal.ID)
	}
	return s.GetProductGoal(ctx, product.Goal.ID)
}

func (s *Service) automaticProductAuthorization(ctx context.Context, product core.ProductSnapshot) (*core.ProductPlanAuthorization, error) {
	store, ok := s.complex.(productPlanStore)
	if !ok || len(product.Discussions) == 0 {
		return nil, nil
	}
	a, err := store.GetClearDevProductPlanAuthorization(ctx, product.Goal.ID, product.Discussions[len(product.Discussions)-1].ID)
	if err != nil || a == nil || a.Status != "RESOLVED" || a.Decision != "APPROVE" {
		return nil, err
	}
	return a, nil
}

// Reads never advance work. This entry is called only by the durable scheduler.
func (s *Service) advanceAutomaticProduct(ctx context.Context, product core.ProductSnapshot) (changed, stopped bool, resultErr error) {
	a, err := s.automaticProductAuthorization(ctx, product)
	if err != nil || a == nil {
		return false, true, err
	}
	store, ok := s.complex.(productPlanStore)
	if !ok {
		return false, true, errors.New("automatic product store unavailable")
	}
	defer func() {
		if resultErr != nil || changed {
			s.recordProductPlanAdvance(ctx, a.RequestID, resultErr)
		}
	}()
	if s.complexExecution == nil || s.ao == nil {
		return false, true, apierr.Conflict("PRODUCT_AUTOMATION_UNAVAILABLE", "The execution engine is not configured for automatic stages", nil)
	}
	var previous *core.ProductStage
	for i := range product.Stages {
		stage := product.Stages[i]
		if stage.DiscussionID != a.Binding.DiscussionID {
			continue
		}
		if stage.DevelopmentRequirementID != "" {
			facts, found, e := s.facts.GetClearDevRequirement(ctx, stage.DevelopmentRequirementID)
			if e != nil {
				return false, false, e
			}
			if !found || facts.Requirement.CancelledAt != nil {
				return false, true, apierr.Conflict("PRODUCT_STAGE_STOPPED", "The current stage was cancelled or is unavailable", nil)
			}
			execution, exists, e := s.complexExecution.GetClearDevComplexExecution(ctx, stage.DevelopmentRequirementID)
			if e != nil {
				return false, false, e
			}
			if exists && execution.Run.CompletedAt != nil && execution.Integration != nil {
				if _, _, e = s.completedProjectBaseline(ctx, product.Goal.ID, stage.DevelopmentRequirementID, execution.Integration.CandidateCommitSHA); e != nil {
					return false, true, e
				}
				previous = &product.Stages[i]
				continue
			}
			s.scheduleComplexFlow(stage.DevelopmentRequirementID)
			if exists && execution.Run.CompletedAt == nil {
				s.scheduleComplexStandardExecution(stage.DevelopmentRequirementID)
			}
			return false, true, nil
		}
		if stage.Ordinal > 0 {
			if previous == nil || previous.Ordinal != stage.Ordinal-1 {
				return false, true, errors.New("automatic stage predecessor is missing")
			}
			execution, found, e := s.complexExecution.GetClearDevComplexExecution(ctx, previous.DevelopmentRequirementID)
			if e != nil {
				return false, false, e
			}
			if !found || execution.Integration == nil {
				return false, true, nil
			}
			if stage.Selection == nil {
				return false, true, errors.New("automatic stage has no project selection")
			}
			project, found, e := s.ao.GetProject(ctx, stage.Selection.AOProjectID)
			if e != nil {
				return false, false, e
			}
			if !found {
				return false, true, errors.New("project disappeared")
			}
			choice := ProductChoiceInput{AOProjectID: stage.Selection.AOProjectID, DeliveryRequirementID: previous.DevelopmentRequirementID, ExpectedBaseCommitSHA: execution.Integration.CandidateCommitSHA, Reason: "Continue within the approved product plan"}
			selection, e := s.observeDeliveredProductChoice(ctx, product.Goal.ID, stage.DiscussionID, choice, stage.Selection.Option, project)
			var conflict *apierr.Error
			if errors.As(e, &conflict) && conflict.Code == "RESULT_DATA_PREPARATION_REQUIRED" {
				if e := s.prepareAutomaticDeliveryData(ctx, previous.DevelopmentRequirementID); e != nil {
					return false, true, e
				}
				selection, e = s.observeDeliveredProductChoice(ctx, product.Goal.ID, stage.DiscussionID, choice, stage.Selection.Option, project)
			}
			if e != nil {
				return false, true, e
			}
			selection.PlanAuthorizationID = a.RequestID
			if e := store.BindClearDevAutomaticStageSource(ctx, stage.ID, a.RequestID, *selection, s.now().UTC()); e != nil {
				return false, true, e
			}
		}
		// Preparation re-reads the frozen source and keeps normal compilation.
		_, e := s.PrepareProductStage(ctx, product.Goal.ID, stage.ID, PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256})
		return e == nil, true, e
	}
	s.recordProductPlanAdvance(ctx, a.RequestID, nil)
	return false, true, nil
}

func (s *Service) confirmAutomaticStage(ctx context.Context, snapshot core.RequirementSnapshot) (changed bool, resultErr error) {
	stage, found, err := s.productStageSource(ctx, snapshot.Requirement.ID)
	if err != nil || !found {
		return false, err
	}
	store, ok := s.complex.(productPlanStore)
	if !ok {
		return false, nil
	}
	a, err := store.GetClearDevProductPlanAuthorization(ctx, stage.ProductID, stage.DiscussionID)
	if err != nil || a == nil || a.Status != "RESOLVED" || a.Decision != "APPROVE" {
		return false, err
	}
	defer func() {
		if resultErr != nil || changed {
			s.recordProductPlanAdvance(ctx, a.RequestID, resultErr)
		}
	}()
	for _, v := range snapshot.RequirementVersions {
		if v.Status == core.RequirementVersionStatusPendingConfirmation {
			var doc core.NormalizedRequirementDocument
			if json.Unmarshal([]byte(v.RequirementText), &doc) != nil || !core.ProductPlanCoversDocument(stage.Definition, doc) {
				return false, apierr.Conflict("PRODUCT_PLAN_SCOPE_CHANGED", "规格改变了整体计划的验收范围，需要与用户讨论", nil)
			}
			if err := store.ConfirmClearDevAutomaticStage(ctx, snapshot.Requirement.ID, v.ID, s.now().UTC()); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}
func (s *Service) startAutomaticStage(ctx context.Context, snapshot core.RequirementSnapshot, planning core.ComplexPlanningSnapshot) (changed bool, resultErr error) {
	stage, found, err := s.productStageSource(ctx, snapshot.Requirement.ID)
	if err != nil || !found {
		return false, err
	}
	store, ok := s.complex.(productPlanStore)
	if !ok {
		return false, nil
	}
	a, err := store.GetClearDevProductPlanAuthorization(ctx, stage.ProductID, stage.DiscussionID)
	if err != nil || a == nil || a.Status != "RESOLVED" || a.Decision != "APPROVE" {
		return false, err
	}
	defer func() {
		if resultErr != nil || changed {
			s.recordProductPlanAdvance(ctx, a.RequestID, resultErr)
		}
	}()
	v := confirmedComplexRequirementVersion(snapshot.RequirementVersions)
	if v == nil || len(planning.Plans) == 0 {
		return false, nil
	}
	var document core.NormalizedRequirementDocument
	if json.Unmarshal([]byte(v.RequirementText), &document) != nil || !core.ProductPlanCoversDocument(stage.Definition, document) {
		return false, apierr.Conflict("PRODUCT_PLAN_SCOPE_CHANGED", "已确认规格超出原整体计划，须先调整产品计划", nil)
	}
	plan := planning.Plans[len(planning.Plans)-1]
	_, err = s.StartProjectExecution(ctx, snapshot.Requirement.ID, core.ProjectExecutionAdmission{RequestID: "product-plan:" + stage.ID, PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, RequirementSHA256: v.SHA256, BaseCommitSHA: stage.BaseCommitSHA})
	return err == nil, err
}

// Data preparation reuses the result manager's migration and owned process
// lifecycle. It cannot stop an interactive preview opened by the user.
func (s *Service) prepareAutomaticDeliveryData(ctx context.Context, requirementID string) error {
	target, err := s.resultPreviewTarget(ctx, requirementID)
	if err != nil {
		return err
	}
	if err := s.checkResultPreviewSource(ctx, requirementID, target); err != nil {
		return err
	}
	manager, ok := s.resultPreview.(interface {
		PrepareProjectProgression(context.Context, domain.SessionID, string, string, core.ProjectExecutionContract, ports.ClearDevProjectResultPrepare) error
	})
	preparer, supported := s.checks.(ports.ClearDevProjectResultPreparer)
	if !ok || !supported || target.project == nil {
		return apierr.Conflict("RESULT_DATA_PREPARATION_REQUIRED", "The completed delivery data preparation runtime is unavailable", nil)
	}
	return manager.PrepareProjectProgression(ctx, target.session.ID, target.workspace, target.sha, *target.project, func(ctx context.Context) (ports.ClearDevProjectResultSource, error) {
		return preparer.PrepareProjectResult(ctx, target.workspace, target.sha, *target.project)
	})
}

func (s *Service) recordProductPlanAdvance(ctx context.Context, requestID string, advanceErr error) {
	code, message := "", ""
	if advanceErr != nil {
		code = "PRODUCT_PLAN_ADVANCE_FAILED"
		message = advanceErr.Error()
		var api *apierr.Error
		if errors.As(advanceErr, &api) {
			code = api.Code
			message = api.Message
		}
	}
	store, ok := s.complex.(productPlanStore)
	if !ok {
		return
	}
	if err := store.RecordClearDevProductPlanAdvance(ctx, requestID, code, message, s.now().UTC()); err != nil {
		s.logger.Error("record automatic product progression", "err", err)
	}
}
