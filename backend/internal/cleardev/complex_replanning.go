package cleardev

import (
	"encoding/json"
	"fmt"
)

// ComplexPlanFeedback is the exact production Steward review of one previous
// plan that the next planning request must answer. It is derived only from
// immutable plan, review, role-binding and agent-step facts. It is not hidden
// benchmark feedback: it is the normal Project Steward review of the Planner's
// own previous plan.
type ComplexPlanFeedback struct {
	PlanID               string `json:"planId"`
	PlanSHA256           string `json:"planSha256"`
	PlanJSON             string `json:"planJson"`
	ReviewID             string `json:"reviewId"`
	ReviewRequestID      string `json:"reviewRequestId"`
	StewardRoleBindingID string `json:"stewardRoleBindingId"`
	Verdict              string `json:"verdict"`
	ReasonCode           string `json:"reasonCode"`
	Summary              string `json:"summary"`
	FindingsJSON         string `json:"findingsJson"`
}

// ComplexReplanCount counts the valid completed REPLAN reviews for one
// confirmed requirement version. A review counts only when its plan belongs to
// the same version and its persisted plan hash matches that immutable plan, so
// repeat reads, restarts, recovery or session changes cannot reset it.
func ComplexReplanCount(snapshot ComplexPlanningSnapshot, versionID string) int {
	plans := map[string]ComplexEngineeringPlan{}
	for _, plan := range snapshot.Plans {
		if plan.RequirementVersionID == versionID {
			plans[plan.ID] = plan
		}
	}
	count := 0
	for _, review := range snapshot.Reviews {
		if review.Verdict != PlanReviewReplan {
			continue
		}
		plan, ok := plans[review.PlanID]
		if !ok || review.PlanSHA256 != plan.PlanSHA256 {
			continue
		}
		count++
	}
	return count
}

// ComplexReplanLimitReached reports whether the version already used the first
// plan plus every automatic revision. The next planning request must not be
// created, occupy a message or send anything.
func ComplexReplanLimitReached(snapshot ComplexPlanningSnapshot, versionID string) bool {
	if versionID == "" {
		return false
	}
	return ComplexReplanCount(snapshot, versionID) > MaxComplexAutomaticReplans
}

// ComplexPlanFeedbackForNextPlanning returns the latest valid production
// REPLAN review of the confirmed version's latest plan. The first plan has no
// previous feedback and returns (nil, nil). Missing, mismatched or ambiguous
// history returns an error so the control plane never sends a new planning
// message with empty or stale feedback.
func ComplexPlanFeedbackForNextPlanning(snapshot ComplexPlanningSnapshot, versionID, requirementSHA, compilationSHA string) (*ComplexPlanFeedback, error) {
	if versionID == "" || requirementSHA == "" || compilationSHA == "" {
		return nil, fmt.Errorf("complex plan feedback requires the confirmed version, requirement hash and compilation")
	}
	plan, ok := latestComplexPlanForVersion(snapshot, versionID)
	if !ok {
		return nil, nil
	}
	review, ok := reviewForComplexPlan(snapshot, plan.ID)
	if !ok {
		return nil, fmt.Errorf("previous plan %s has no completed review", plan.ID)
	}
	if review.Verdict != PlanReviewReplan {
		return nil, fmt.Errorf("previous plan %s review verdict is %s, not REPLAN", plan.ID, review.Verdict)
	}
	if review.PlanSHA256 != plan.PlanSHA256 {
		return nil, fmt.Errorf("previous review does not bind the previous plan hash")
	}
	if plan.RequirementVersionID != versionID || plan.RequirementSHA256 != requirementSHA {
		return nil, fmt.Errorf("previous plan does not belong to the confirmed requirement version")
	}
	if plan.CompilationSHA256 != compilationSHA {
		return nil, fmt.Errorf("previous plan does not belong to the current compilation")
	}
	if !complexReviewStepValid(snapshot, review) {
		return nil, fmt.Errorf("previous review is not a completed production Steward review of its plan")
	}
	if !validComplexFindingsJSON(review.FindingsJSON) {
		return nil, fmt.Errorf("previous review findings are not a JSON array")
	}
	return &ComplexPlanFeedback{
		PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, PlanJSON: plan.PlanJSON,
		ReviewID: review.ID, ReviewRequestID: review.ReviewRequestID,
		StewardRoleBindingID: review.StewardRoleBindingID,
		Verdict:              string(review.Verdict), ReasonCode: string(review.ReasonCode),
		Summary: review.Summary, FindingsJSON: review.FindingsJSON,
	}, nil
}

func complexReviewStepValid(snapshot ComplexPlanningSnapshot, review ComplexPlanReview) bool {
	steward := false
	for _, binding := range snapshot.RoleBindings {
		if binding.ID == review.StewardRoleBindingID && binding.Role == StandardRoleSteward {
			steward = true
			break
		}
	}
	if !steward {
		return false
	}
	for _, step := range snapshot.AgentSteps {
		if step.ID == review.AgentStepID && step.Kind == ComplexAgentStepPlanReview &&
			step.RequestID == review.ReviewRequestID && step.RoleBindingID == review.StewardRoleBindingID &&
			step.SendStatus == AgentStepSendStatusSettled {
			return true
		}
	}
	return false
}

func validComplexFindingsJSON(value string) bool {
	var findings []ComplexReviewFinding
	return json.Unmarshal([]byte(value), &findings) == nil
}
