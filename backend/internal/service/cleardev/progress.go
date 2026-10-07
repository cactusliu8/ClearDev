package cleardev

import (
	"context"
	"encoding/json"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// ListProjectProgress returns derived trusted progress for every ClearDev
// requirement in one AO project.
func (s *Service) ListProjectProgress(ctx context.Context, projectID string) (ProjectProgressView, error) {
	if s.facts == nil || s.ao == nil {
		return ProjectProgressView{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev service is not fully configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ProjectProgressView{}, apierr.Invalid("AO_PROJECT_ID_REQUIRED", "aoProjectId is required", nil)
	}
	project, ok, err := s.ao.GetProject(ctx, projectID)
	if err != nil {
		return ProjectProgressView{}, apierr.Internal("AO_PROJECT_READ_FAILED", "Could not read the AO project")
	}
	if !ok || !project.ArchivedAt.IsZero() {
		return ProjectProgressView{}, apierr.NotFound("AO_PROJECT_NOT_FOUND", "AO project was not found")
	}
	if project.Kind.WithDefault() != domain.ProjectKindSingleRepo {
		return ProjectProgressView{}, apierr.Invalid("AO_PROJECT_KIND_UNSUPPORTED", "ClearDev S01 supports only single_repo AO projects", nil)
	}
	ids, err := s.facts.ListClearDevRequirementIDsByAOProject(ctx, projectID)
	if err != nil {
		return ProjectProgressView{}, apierr.Internal("CLEARDEV_PROGRESS_READ_FAILED", "Could not list ClearDev requirements")
	}
	items := make([]core.TrustedProgressSummary, 0, len(ids))
	now := s.now().UTC()
	for _, id := range ids {
		view, getErr := s.getProgressRequirement(ctx, id, now, false)
		if getErr != nil {
			return ProjectProgressView{}, getErr
		}
		items = append(items, view.TrustedProgress)
	}
	core.SortTrustedProgressSummaries(items)
	return ProjectProgressView{AOProjectID: projectID, Requirements: items}, nil
}

func (s *Service) attachTrustedProgress(ctx context.Context, view *RequirementView, facts core.TrustedProgressFacts) ([]core.ProgressExplanationRequest, error) {
	facts.StewardSessionID = progressStewardSessionID(*view)
	facts.HasStewardSession = facts.StewardSessionID != ""
	if s.humanDecisions != nil {
		pending, err := s.humanDecisions.ListPendingClearDevHumanDecisionRequests(ctx)
		if err != nil {
			return nil, apierr.Internal("CLEARDEV_PROGRESS_READ_FAILED", "Could not read pending human decisions")
		}
		for _, request := range pending {
			if request.DevelopmentRequirementID == view.Requirement.ID {
				facts.PendingDecisions = append(facts.PendingDecisions, request)
			}
		}
	}
	projectState, err := s.projectPlanningState(ctx, view.Requirement.ID)
	if err != nil {
		return nil, apierr.Internal("CLEARDEV_PROJECT_PLANNING_READ_FAILED", "Could not read the current project planning source")
	}
	facts.ProjectPlanning = projectState
	summary := core.DeriveTrustedProgress(facts)
	var rows []core.ProgressExplanationRequest
	if s.progress != nil {
		var err error
		rows, err = s.progress.ListClearDevProgressExplanations(ctx, view.Requirement.ID)
		if err != nil {
			return nil, apierr.Internal("CLEARDEV_PROGRESS_READ_FAILED", "Could not read progress explanations")
		}
		attachTrustedExplanation(&summary, rows)
	}
	view.TrustedProgress = summary
	return rows, nil
}

func attachTrustedExplanation(summary *core.TrustedProgressSummary, rows []core.ProgressExplanationRequest) {
	var current, latest *core.ProgressExplanationRequest
	for index := range rows {
		row := &rows[index]
		if latest == nil || row.CreatedAt.After(latest.CreatedAt) || (row.CreatedAt.Equal(latest.CreatedAt) && row.ID > latest.ID) {
			latest = row
		}
		if row.FactSummarySHA256 == summary.FactSummarySHA256 {
			current = row
		}
	}
	chosen := current
	if chosen == nil {
		chosen = latest
	}
	if chosen == nil {
		return
	}
	view := core.TrustedProgressExplanationView{
		RequestID: chosen.ID, Status: chosen.Status,
		Stale: chosen.FactSummarySHA256 != summary.FactSummarySHA256, FactSummarySHA256: chosen.FactSummarySHA256,
		ReasonCode: string(chosen.ReasonCode),
	}
	if chosen.Status == core.ProgressExplanationSettled && chosen.ResultJSON != "" {
		var result core.TrustedProgressExplanationResult
		if json.Unmarshal([]byte(chosen.ResultJSON), &result) == nil {
			view.Summary = result.Summary
			view.CitedFactIDs = result.CitedFactIDs
			view.Phase = result.Phase
			view.Attention = result.Attention
			view.NextOwnerRole = result.NextOwnerRole
			view.PendingDecision = result.PendingDecision
		}
	}
	summary.Explanation = &view
}

func progressStewardSessionID(view RequirementView) string {
	if view.ComplexPlanning != nil {
		if id := firstStewardSession(view.ComplexPlanning.RoleBindings); id != "" {
			return id
		}
	}
	if view.ComplexExecution != nil {
		if id := originalExecutionStewardSession(view.ComplexExecution.RoleBindings); id != "" {
			return id
		}
	}
	if view.QuickExecution != nil {
		if id := originalExecutionStewardSession(view.QuickExecution.RoleBindings); id != "" {
			return id
		}
	}
	if view.StandardFlow != nil {
		for _, binding := range view.StandardFlow.RoleBindings {
			if binding.Role == core.StandardRoleSteward && strings.TrimSpace(binding.AOSessionID) != "" {
				return binding.AOSessionID
			}
		}
	}
	return ""
}

func firstStewardSession(bindings []core.ComplexRoleBinding) string {
	for _, binding := range bindings {
		if binding.Role == core.StandardRoleSteward && strings.TrimSpace(binding.AOSessionID) != "" {
			return binding.AOSessionID
		}
	}
	return ""
}

func originalExecutionStewardSession(bindings []core.ComplexExecutionRoleBinding) string {
	for _, binding := range bindings {
		if binding.Role == core.StandardRoleSteward && binding.ContinuationOfRoleBindingID == "" && strings.TrimSpace(binding.AOSessionID) != "" {
			return binding.AOSessionID
		}
	}
	for _, binding := range bindings {
		if binding.Role == core.StandardRoleSteward && strings.TrimSpace(binding.AOSessionID) != "" {
			return binding.AOSessionID
		}
	}
	return ""
}
