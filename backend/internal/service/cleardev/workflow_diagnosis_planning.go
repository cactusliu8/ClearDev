package cleardev

import (
	"context"
	"strconv"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// A preflight can stop a role before any logical step exists. Read the precise
// binding's saved check, not another role's newer requirement-wide preflight.
func (s *Service) addWorkflowDiagnosisPreflights(ctx context.Context, diagnosis *core.WorkflowDiagnosis) {
	if s.preflights == nil {
		return
	}
	for i := range diagnosis.Issues {
		issue := &diagnosis.Issues[i]
		if issue.Relationship != "CURRENT" {
			continue
		}
		bindingID, preflightID := "", ""
		for _, fact := range issue.Evidence {
			if fact.Kind == "ROLE_BINDING" {
				bindingID = fact.FactID
			}
			if fact.Kind == "PREFLIGHT_ID" {
				preflightID = fact.FactID
			}
		}
		if bindingID == "" || preflightID == "" {
			continue
		}
		check, found, err := s.preflights.GetLatestClearDevControlledPreflightForBinding(ctx, bindingID)
		if err != nil || !found || check.ID != preflightID || check.RoleBindingID != bindingID {
			diagnosis.Current = false
			diagnosis.ReadError = "CURRENT_BINDINGS_CHANGED"
			diagnosis.Issues = []core.WorkflowDiagnosisIssue{}
			return
		}
		addDiagnosisEvidence(issue, "PREFLIGHT_STATUS", check.ID, check.Outcome, &check.CheckedAt)
		addDiagnosisEvidence(issue, "PREFLIGHT_SUMMARY", check.ID, check.ErrorSummary, &check.CheckedAt)
		addDiagnosisEvidence(issue, "PROVIDER_CODE", check.ID, check.ProviderErrorCode, &check.CheckedAt)
		if check.RetryAt != nil {
			addDiagnosisEvidence(issue, "RETRY_AT", check.ID, check.RetryAt.Format(time.RFC3339), &check.CheckedAt)
		}
	}
}

func workflowPlanningDiagnosis(view RequirementView) []core.WorkflowDiagnosisIssue {
	issues := []core.WorkflowDiagnosisIssue{}
	p := view.ComplexPlanning
	if p == nil || view.ComplexExecution != nil {
		return issues
	}
	if clarification := p.ProductClarification; clarification != nil {
		issue := core.WorkflowDiagnosisIssue{ReasonCode: "PRODUCT_CLARIFICATION_REQUIRED", Relationship: "CURRENT", SubjectType: "PLANNING_REQUEST", SubjectID: clarification.PlanningRequestID, Role: "PLANNER", Summary: diagnosticText(clarification.Summary, 2000)}
		for _, question := range clarification.Questions {
			addDiagnosisEvidence(&issue, "QUESTION", clarification.PlanningRequestID, question, nil)
		}
		issues = append(issues, issue)
	}
	if p.Phase == core.ComplexPlanningAwaitingClarification && len(p.CompilationRequests) > 0 {
		request := p.CompilationRequests[len(p.CompilationRequests)-1]
		issue := core.WorkflowDiagnosisIssue{ReasonCode: "AWAITING_CLARIFICATION", Relationship: "CURRENT", SubjectType: "COMPILATION_REQUEST", SubjectID: request.ID, Role: "STEWARD"}
		for _, question := range p.Questions {
			if question.CompilationRequestID != request.ID {
				continue
			}
			answered := false
			for _, answer := range p.Answers {
				answered = answered || answer.CompilationRequestID == request.ID && answer.QuestionKey == question.QuestionKey
			}
			if !answered {
				addDiagnosisEvidence(&issue, "QUESTION", request.ID+":"+question.QuestionKey, question.Text, nil)
				addDiagnosisEvidence(&issue, "QUESTION_REASON", request.ID+":"+question.QuestionKey, question.Reason, nil)
			}
		}
		issues = append(issues, issue)
	}
	if p.Phase != core.ComplexPlanningNeedsHuman && p.Phase != core.ComplexPlanningRejected {
		return issues
	}
	if len(p.CompilationRequests) > 0 {
		request := p.CompilationRequests[len(p.CompilationRequests)-1]
		for _, compilation := range p.Compilations {
			if compilation.CompilationRequestID == request.ID && compilation.Outcome == "NEEDS_HUMAN" {
				issue := core.WorkflowDiagnosisIssue{ReasonCode: "COMPILATION_NEEDS_HUMAN", Relationship: "CURRENT", SubjectType: "COMPILATION", SubjectID: compilation.ID, Role: "STEWARD", Summary: diagnosticText(compilation.Summary, 2000)}
				addDiagnosisEvidence(&issue, "FAILURE_SUMMARY", compilation.ID, compilation.Summary, &compilation.CreatedAt)
				issues = append(issues, issue)
			}
		}
	}
	if len(p.Plans) > 0 {
		plan := p.Plans[len(p.Plans)-1]
		for _, review := range p.Reviews {
			if review.PlanID != plan.ID || review.PlanSHA256 != plan.PlanSHA256 || review.Verdict == core.PlanReviewApproved {
				continue
			}
			issue := core.WorkflowDiagnosisIssue{ReasonCode: string(review.ReasonCode), Relationship: "CURRENT", SubjectType: "PLAN_REVIEW", SubjectID: review.ID, Role: "STEWARD", Summary: diagnosticText(review.Summary, 2000)}
			if issue.ReasonCode == "" {
				issue.ReasonCode = "PLAN_REVIEW_BLOCKED"
			}
			addDiagnosisEvidence(&issue, "REVIEW_SUMMARY", review.ID, review.Summary, &review.CreatedAt)
			addDiagnosisEvidence(&issue, "REVIEW_FINDINGS", review.ID, review.FindingsJSON, &review.CreatedAt)
			issues = append(issues, issue)
		}
	}
	return issues
}

// Product containers have a separate discussion projection. Reuse it, rather
// than claiming an executable requirement's phase covers product round limits.
func (s *Service) addProductWorkflowDiagnosis(ctx context.Context, requirementID string, diagnosis *core.WorkflowDiagnosis) {
	store, ok := s.complex.(ProductFactStore)
	if !ok {
		return
	}
	_, found, err := store.GetClearDevProduct(ctx, requirementID)
	if err != nil {
		diagnosis.Current, diagnosis.ReadError = false, "DIAGNOSIS_UNAVAILABLE"
		return
	}
	if !found {
		return
	}
	product, err := s.GetProductGoal(ctx, requirementID)
	if err != nil || product.ControlProgress.FactSummarySHA256 != diagnosis.FactSummarySHA256 {
		diagnosis.Current, diagnosis.ReadError = false, "CURRENT_BINDINGS_CHANGED"
		return
	}
	diagnosis.Phase = product.Phase
	if product.Reason == "" || len(product.Discussions) == 0 {
		return
	}
	last := product.Discussions[len(product.Discussions)-1]
	for _, existing := range diagnosis.Issues {
		if existing.ReasonCode == product.Reason && existing.Relationship == "CURRENT" {
			return
		}
	}
	issue := core.WorkflowDiagnosisIssue{ID: "product:" + last.ID, ReasonCode: product.Reason, Category: core.WorkflowBlockerCategory(product.Reason), Relationship: "CURRENT", SubjectType: "PRODUCT_DISCUSSION", SubjectID: last.ID, AOSessionID: product.StewardSessionID, Role: "STEWARD", Evidence: []core.WorkflowDiagnosisEvidence{}}
	addDiagnosisEvidence(&issue, "DISCUSSION_ORDINAL", last.ID, strconv.Itoa(last.Ordinal), last.SettledAt)
	addDiagnosisEvidence(&issue, "REMAINING_DISCUSSIONS", last.ID, strconv.Itoa(product.RemainingDiscussions), nil)
	if last.Result != nil {
		issue.Summary = diagnosticText(last.Result.Message, 2000)
		for _, question := range last.Result.Questions {
			addDiagnosisEvidence(&issue, "QUESTION", last.ID+":"+question.Key, question.Text, nil)
		}
	}
	diagnosis.Issues = append(diagnosis.Issues, issue)
}
