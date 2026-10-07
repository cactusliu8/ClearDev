package cleardev

import (
	"encoding/json"
	"time"
)

// DeriveComplexPlanningPhase computes the S04 display phase from durable facts.
func DeriveComplexPlanningPhase(snapshot ComplexPlanningSnapshot, versions []RequirementVersion) (ComplexPlanningPhase, ReasonCode) {
	confirmed := currentConfirmedRequirementVersion(versions)
	if latestReview, ok := latestComplexReviewForCurrent(snapshot, confirmed); ok {
		switch latestReview.Verdict {
		case PlanReviewApproved:
			return ComplexPlanningApproved, ReasonNone
		case PlanReviewNeedsHuman:
			return ComplexPlanningNeedsHuman, latestReview.ReasonCode
		}
	}
	if latestCompilation, ok := latestComplexCompilation(snapshot); ok && latestCompilation.Outcome == "NEEDS_HUMAN" {
		return ComplexPlanningNeedsHuman, ReasonHumanDecisionRequired
	}
	if failed, reason := latestFailedComplexStep(snapshot); failed {
		return ComplexPlanningNeedsHuman, reason
	}
	if version := latestRequirementVersion(versions); version != nil && version.Status == RequirementVersionStatusRejected {
		return ComplexPlanningRejected, ReasonRejectedByDesktopHuman
	}
	if unanswered := unansweredComplexRequest(snapshot); unanswered != nil {
		return ComplexPlanningAwaitingClarification, ReasonNone
	}
	if pending := pendingRequirementVersion(versions); pending != nil {
		return ComplexPlanningAwaitingConfirmation, ReasonHumanDecisionRequired
	}
	if confirmed != nil {
		if latestPlan, ok := latestComplexPlanForVersion(snapshot, confirmed.ID); ok {
			if _, clarification := PlannerClarificationForPlan(latestPlan); clarification {
				if _, answered := PlannerAnswerForPlan(snapshot, latestPlan); answered {
					return ComplexPlanningPlanning, ReasonNone
				}
				return ComplexPlanningNeedsHuman, ReasonProductClarificationRequired
			}
			var result ComplexEngineeringPlanResult
			if json.Unmarshal([]byte(latestPlan.PlanJSON), &result) == nil && result.SchemaVersion == ProjectPlanningVersion {
				return ComplexPlanningProjectPlanned, ReasonProjectPlanningOnly
			}
			if result.SchemaVersion == PlannerTaskContractVersion {
				if _, valid := PlanValidationForPlan(snapshot, latestPlan); valid {
					return ComplexPlanningValidated, ReasonNone
				}
				return ComplexPlanningNeedsHuman, ReasonCode("PLANNER_VALIDATION_REQUIRED")
			}
			if _, reviewed := reviewForComplexPlan(snapshot, latestPlan.ID); !reviewed {
				return ComplexPlanningAwaitingPlanReview, ReasonNone
			}
		}
		if ComplexReplanLimitReached(snapshot, confirmed.ID) {
			return ComplexPlanningNeedsHuman, ReasonComplexReplanLimitReached
		}
		return ComplexPlanningPlanning, ReasonNone
	}
	return ComplexPlanningCompiling, ReasonNone
}

func latestComplexCompilation(snapshot ComplexPlanningSnapshot) (ComplexCompilation, bool) {
	if len(snapshot.Compilations) == 0 {
		return ComplexCompilation{}, false
	}
	latest := snapshot.Compilations[0]
	for _, item := range snapshot.Compilations[1:] {
		if item.CreatedAt.After(latest.CreatedAt) || (item.CreatedAt.Equal(latest.CreatedAt) && item.ID > latest.ID) {
			latest = item
		}
	}
	return latest, true
}

func latestComplexPlan(snapshot ComplexPlanningSnapshot) (ComplexEngineeringPlan, bool) {
	return latestComplexPlanForVersion(snapshot, "")
}

func latestComplexPlanForVersion(snapshot ComplexPlanningSnapshot, versionID string) (ComplexEngineeringPlan, bool) {
	found := false
	var latest ComplexEngineeringPlan
	for _, item := range snapshot.Plans {
		if versionID != "" && item.RequirementVersionID != versionID {
			continue
		}
		if !found || item.Version > latest.Version {
			latest = item
			found = true
		}
	}
	return latest, found
}

func latestComplexReview(snapshot ComplexPlanningSnapshot) (ComplexPlanReview, bool) {
	return latestComplexReviewForCurrent(snapshot, nil)
}

func latestComplexReviewForCurrent(snapshot ComplexPlanningSnapshot, confirmed *RequirementVersion) (ComplexPlanReview, bool) {
	if confirmed != nil {
		plan, ok := latestComplexPlanForVersion(snapshot, confirmed.ID)
		if !ok {
			return ComplexPlanReview{}, false
		}
		return reviewForComplexPlan(snapshot, plan.ID)
	}
	if len(snapshot.Reviews) == 0 {
		return ComplexPlanReview{}, false
	}
	latest := snapshot.Reviews[0]
	for _, item := range snapshot.Reviews[1:] {
		if item.CreatedAt.After(latest.CreatedAt) || (item.CreatedAt.Equal(latest.CreatedAt) && item.ID > latest.ID) {
			latest = item
		}
	}
	return latest, true
}

func reviewForComplexPlan(snapshot ComplexPlanningSnapshot, planID string) (ComplexPlanReview, bool) {
	for _, review := range snapshot.Reviews {
		if review.PlanID == planID {
			return review, true
		}
	}
	return ComplexPlanReview{}, false
}

func unansweredComplexRequest(snapshot ComplexPlanningSnapshot) *ComplexCompilationRequest {
	for i := len(snapshot.CompilationRequests) - 1; i >= 0; i-- {
		request := snapshot.CompilationRequests[i]
		questions := 0
		answers := 0
		for _, question := range snapshot.Questions {
			if question.CompilationRequestID == request.ID {
				questions++
			}
		}
		for _, answer := range snapshot.Answers {
			if answer.CompilationRequestID == request.ID {
				answers++
			}
		}
		if questions > 0 && answers < questions {
			copy := request
			return &copy
		}
	}
	return nil
}

func latestFailedComplexStep(snapshot ComplexPlanningSnapshot) (bool, ReasonCode) {
	var latest *AgentStep
	for i := range snapshot.AgentSteps {
		step := &snapshot.AgentSteps[i]
		if step.SendStatus != AgentStepSendStatusFailed {
			continue
		}
		if latest == nil || step.RequestedAt.After(latest.RequestedAt) {
			latest = step
		}
	}
	if latest == nil {
		return false, ReasonNone
	}
	return true, latest.ReasonCode
}

func latestRequirementVersion(versions []RequirementVersion) *RequirementVersion {
	var latest *RequirementVersion
	for i := range versions {
		version := &versions[i]
		if latest == nil || version.Version > latest.Version {
			latest = version
		}
	}
	return latest
}

func pendingRequirementVersion(versions []RequirementVersion) *RequirementVersion {
	for i := range versions {
		if versions[i].Status == RequirementVersionStatusPendingConfirmation {
			return &versions[i]
		}
	}
	return nil
}

func currentConfirmedRequirementVersion(versions []RequirementVersion) *RequirementVersion {
	for i := range versions {
		if versions[i].Status == RequirementVersionStatusConfirmed {
			return &versions[i]
		}
	}
	return nil
}

func ComplexRoleBindingByRole(snapshot ComplexPlanningSnapshot, role StandardRole) (ComplexRoleBinding, bool) {
	for _, binding := range snapshot.RoleBindings {
		if binding.Role == role {
			return binding, true
		}
	}
	return ComplexRoleBinding{}, false
}

func ComplexAgentStepByRequest(snapshot ComplexPlanningSnapshot, kind AgentStepKind, requestID string) (AgentStep, bool) {
	for _, step := range snapshot.AgentSteps {
		if step.Kind == kind && step.RequestID == requestID {
			return step, true
		}
	}
	return AgentStep{}, false
}

func CompilationContextFromSnapshot(snapshot ComplexPlanningSnapshot, targetVersionID string) CompilationContext {
	context := CompilationContext{
		SchemaVersion:              ComplexProtocolVersion,
		DevelopmentRequirementID:   snapshot.Requirement.DevelopmentRequirementID,
		TargetRequirementVersionID: targetVersionID,
		SourcePRDSHA256:            snapshot.Requirement.OriginalPRDSHA256,
		PreviousRounds:             []CompilationContextRound{},
	}
	for _, request := range snapshot.CompilationRequests {
		questions := questionsForRequest(snapshot, request.ID)
		answers := answersForRequest(snapshot, request.ID)
		if len(questions) == 0 || len(answers) < len(questions) {
			continue
		}
		round := CompilationContextRound{
			ClarificationRound:    request.ClarificationRound,
			CompilationRequestID:  request.ID,
			AdditionalRoundReason: request.AdditionalRoundReason,
			Questions:             make([]CompilationContextQuestion, 0, len(questions)),
			Answers:               make([]CompilationContextAnswer, 0, len(answers)),
		}
		for _, question := range questions {
			round.Questions = append(round.Questions, CompilationContextQuestion{
				Key: question.QuestionKey, Text: question.Text, Reason: question.Reason,
				RequirementKeys: append([]string(nil), question.RequirementKeys...),
			})
		}
		for _, answer := range answers {
			round.Answers = append(round.Answers, CompilationContextAnswer{QuestionKey: answer.QuestionKey, Text: answer.Text})
		}
		context.PreviousRounds = append(context.PreviousRounds, round)
	}
	return context
}

func questionsForRequest(snapshot ComplexPlanningSnapshot, requestID string) []ComplexClarificationQuestion {
	out := []ComplexClarificationQuestion{}
	for _, question := range snapshot.Questions {
		if question.CompilationRequestID == requestID {
			out = append(out, question)
		}
	}
	return out
}

func answersForRequest(snapshot ComplexPlanningSnapshot, requestID string) []ComplexClarificationAnswer {
	out := []ComplexClarificationAnswer{}
	for _, answer := range snapshot.Answers {
		if answer.CompilationRequestID == requestID {
			out = append(out, answer)
		}
	}
	return out
}

func NextComplexClarificationRound(snapshot ComplexPlanningSnapshot) int {
	if unanswered := unansweredComplexRequest(snapshot); unanswered != nil {
		return unanswered.ClarificationRound
	}
	if len(snapshot.CompilationRequests) == 0 {
		return 0
	}
	latest := snapshot.CompilationRequests[len(snapshot.CompilationRequests)-1]
	return latest.ClarificationRound + 1
}

func ComplexPlanIsDispatchable(snapshot ComplexPlanningSnapshot, planID string) bool {
	for _, plan := range snapshot.Plans {
		if plan.ID != planID {
			continue
		}
		latest, ok := latestComplexPlanForVersion(snapshot, plan.RequirementVersionID)
		if !ok || latest.ID != plan.ID {
			return false
		}
		var parsed ComplexEngineeringPlanResult
		if json.Unmarshal([]byte(plan.PlanJSON), &parsed) == nil && parsed.SchemaVersion == PlannerTaskContractVersion {
			_, admitted := PlanValidationForPlan(snapshot, plan)
			return parsed.Kind == "COMPLEX_ENGINEERING_PLAN" && admitted
		}
		break
	}
	review, ok := reviewForComplexPlan(snapshot, planID)
	return ok && review.Verdict == PlanReviewApproved
}

func ComplexPlanningNeedsResume(snapshot ComplexPlanningSnapshot, versions []RequirementVersion, cancelledAt *time.Time) bool {
	if cancelledAt != nil {
		return false
	}
	phase, _ := DeriveComplexPlanningPhase(snapshot, versions)
	switch phase {
	case ComplexPlanningApproved, ComplexPlanningNeedsHuman, ComplexPlanningRejected:
		return false
	default:
		return true
	}
}

// ReadyCompilationForSHA returns the latest READY compilation whose SHA-256
// equals compilationSHA. S04 compilations and direction-change 0111
// compilations are searched together so a confirmed v2 version cannot reuse
// the superseded v1 compilation.
func ReadyCompilationForSHA(planning ComplexPlanningSnapshot, direction DirectionChangeSnapshot, compilationSHA string) (ComplexCompilation, bool) {
	if compilationSHA == "" {
		return ComplexCompilation{}, false
	}
	items := make([]ComplexCompilation, 0, len(planning.Compilations)+len(direction.Compilations))
	items = append(items, planning.Compilations...)
	items = append(items, direction.Compilations...)
	var latest ComplexCompilation
	found := false
	for _, item := range items {
		if item.Outcome != "READY" || item.CompilationSHA256 != compilationSHA {
			continue
		}
		if !found || item.CreatedAt.After(latest.CreatedAt) || (item.CreatedAt.Equal(latest.CreatedAt) && item.ID > latest.ID) {
			latest = item
			found = true
		}
	}
	return latest, found
}
