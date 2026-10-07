package cleardev

import (
	"strings"
	"testing"
	"time"
)

func TestDeriveComplexPlanningPhaseFromDurableFacts(t *testing.T) {
	snapshot := ComplexPlanningSnapshot{
		Requirement: ComplexRequirement{DevelopmentRequirementID: "req"},
	}
	phase, _ := DeriveComplexPlanningPhase(snapshot, nil)
	if phase != ComplexPlanningCompiling {
		t.Fatalf("empty facts phase = %s", phase)
	}

	snapshot.CompilationRequests = []ComplexCompilationRequest{{ID: "c0"}}
	snapshot.Questions = []ComplexClarificationQuestion{{CompilationRequestID: "c0", QuestionKey: "q"}}
	phase, _ = DeriveComplexPlanningPhase(snapshot, nil)
	if phase != ComplexPlanningAwaitingClarification {
		t.Fatalf("unanswered phase = %s", phase)
	}

	snapshot.Answers = []ComplexClarificationAnswer{{CompilationRequestID: "c0", QuestionKey: "q"}}
	pending := []RequirementVersion{{ID: "v1", Status: RequirementVersionStatusPendingConfirmation, Version: 1}}
	phase, reason := DeriveComplexPlanningPhase(snapshot, pending)
	if phase != ComplexPlanningAwaitingConfirmation || reason != ReasonHumanDecisionRequired {
		t.Fatalf("pending confirmation phase = %s reason=%s", phase, reason)
	}

	confirmed := []RequirementVersion{{ID: "v1", Status: RequirementVersionStatusConfirmed, Version: 1}}
	phase, _ = DeriveComplexPlanningPhase(snapshot, confirmed)
	if phase != ComplexPlanningPlanning {
		t.Fatalf("confirmed without plan phase = %s", phase)
	}

	snapshot.Plans = []ComplexEngineeringPlan{{ID: "plan-1", Version: 1, RequirementVersionID: "v1"}}
	phase, _ = DeriveComplexPlanningPhase(snapshot, confirmed)
	if phase != ComplexPlanningAwaitingPlanReview {
		t.Fatalf("unreviewed plan phase = %s", phase)
	}

	snapshot.Reviews = []ComplexPlanReview{{
		ID: "review-1", PlanID: "plan-1", Verdict: PlanReviewReplan, ReasonCode: "REPLAN_REQUIRED",
		CreatedAt: time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
	}}
	phase, _ = DeriveComplexPlanningPhase(snapshot, confirmed)
	if phase != ComplexPlanningPlanning {
		t.Fatalf("REPLAN phase = %s, want PLANNING", phase)
	}
	if ComplexPlanIsDispatchable(snapshot, "plan-1") {
		t.Fatal("REPLAN plan was dispatchable")
	}

	snapshot.Plans = append(snapshot.Plans, ComplexEngineeringPlan{ID: "plan-2", Version: 2, RequirementVersionID: "v1"})
	snapshot.Reviews = append(snapshot.Reviews, ComplexPlanReview{
		ID: "review-2", PlanID: "plan-2", Verdict: PlanReviewApproved, ReasonCode: "PLAN_ACCEPTABLE",
		CreatedAt: time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC),
	})
	phase, _ = DeriveComplexPlanningPhase(snapshot, confirmed)
	if phase != ComplexPlanningApproved {
		t.Fatalf("approved phase = %s", phase)
	}
	if ComplexPlanIsDispatchable(snapshot, "plan-1") || !ComplexPlanIsDispatchable(snapshot, "plan-2") {
		t.Fatal("dispatchability was not derived from the matching review")
	}
}

func TestReadyCompilationForSHAPrefersDirectionV2OverS04V1(t *testing.T) {
	v1 := ComplexCompilation{
		ID: "c-v1", Outcome: "READY", CompilationSHA256: strings.Repeat("a", 64),
		NormalizedRequirementJSON: `{"v":1}`, CreatedAt: time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
	}
	v2 := ComplexCompilation{
		ID: "c-v2", Outcome: "READY", CompilationSHA256: strings.Repeat("b", 64),
		NormalizedRequirementJSON: `{"v":2}`, CreatedAt: time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC),
	}
	planning := ComplexPlanningSnapshot{Compilations: []ComplexCompilation{v1}}
	direction := DirectionChangeSnapshot{Compilations: []ComplexCompilation{v2}}

	got, ok := ReadyCompilationForSHA(planning, direction, v2.CompilationSHA256)
	if !ok || got.ID != "c-v2" {
		t.Fatalf("v2 SHA selected %#v ok=%v", got, ok)
	}
	got, ok = ReadyCompilationForSHA(planning, direction, v1.CompilationSHA256)
	if !ok || got.ID != "c-v1" {
		t.Fatalf("v1 SHA selected %#v ok=%v", got, ok)
	}
	if _, ok := ReadyCompilationForSHA(planning, DirectionChangeSnapshot{}, v2.CompilationSHA256); ok {
		t.Fatal("v2 compilation was found without 0111 facts")
	}
	if _, ok := ReadyCompilationForSHA(planning, direction, ""); ok {
		t.Fatal("empty SHA selected a compilation")
	}
}

func replanFeedbackSnapshot() ComplexPlanningSnapshot {
	plan := func(id string, version int64, sha string) ComplexEngineeringPlan {
		return ComplexEngineeringPlan{
			ID: id, Version: version, RequirementVersionID: "v1", RequirementSHA256: strings.Repeat("c", 64),
			CompilationSHA256: strings.Repeat("d", 64), PlanJSON: `{"kind":"COMPLEX_ENGINEERING_PLAN","planId":"` + id + `"}`, PlanSHA256: sha,
		}
	}
	review := func(id, planID, sha, verdict string, stepID, requestID string) ComplexPlanReview {
		return ComplexPlanReview{ID: id, PlanID: planID, PlanSHA256: sha, Verdict: PlanReviewVerdict(verdict), ReasonCode: "REPLAN_REQUIRED", Summary: "需要修订。", FindingsJSON: `[{"code":"SPLIT_TASKS","message":"边界需要调整。","requirementIds":[],"taskKeys":[]}]`, StewardRoleBindingID: "steward", AgentStepID: stepID, ReviewRequestID: requestID}
	}
	step := func(id, requestID string, status AgentStepSendStatus) AgentStep {
		return AgentStep{ID: id, RoleBindingID: "steward", Kind: ComplexAgentStepPlanReview, RequestID: requestID, SendStatus: status}
	}
	return ComplexPlanningSnapshot{
		RoleBindings: []ComplexRoleBinding{{ID: "steward", Role: StandardRoleSteward, Status: RoleBindingStatusBound}},
		Plans:        []ComplexEngineeringPlan{plan("plan-1", 1, strings.Repeat("a", 64))},
		Reviews:      []ComplexPlanReview{review("review-1", "plan-1", strings.Repeat("a", 64), "REPLAN", "step-1", "review-request-1")},
		AgentSteps:   []AgentStep{step("step-1", "review-request-1", AgentStepSendStatusSettled)},
	}
}

func TestComplexPlanFeedbackRequiresExactReplanHistory(t *testing.T) {
	snapshot := replanFeedbackSnapshot()
	feedback, err := ComplexPlanFeedbackForNextPlanning(snapshot, "v1", strings.Repeat("c", 64), strings.Repeat("d", 64))
	if err != nil || feedback == nil {
		t.Fatalf("valid REPLAN feedback = %#v err=%v", feedback, err)
	}
	if feedback.PlanID != "plan-1" || feedback.ReviewID != "review-1" || feedback.Verdict != "REPLAN" ||
		feedback.FindingsJSON == "" || feedback.PlanJSON == "" {
		t.Fatalf("feedback = %#v", feedback)
	}
	if count := ComplexReplanCount(snapshot, "v1"); count != 1 {
		t.Fatalf("replan count = %d", count)
	}
	if ComplexReplanLimitReached(snapshot, "v1") {
		t.Fatal("one replan reached the limit")
	}

	firstPlan := ComplexPlanningSnapshot{}
	if feedback, err := ComplexPlanFeedbackForNextPlanning(firstPlan, "v1", strings.Repeat("c", 64), strings.Repeat("d", 64)); err != nil || feedback != nil {
		t.Fatalf("first plan feedback = %#v err=%v", feedback, err)
	}

	wrongHash := snapshot
	wrongHash.Reviews = append([]ComplexPlanReview(nil), snapshot.Reviews...)
	wrongHash.Reviews[0].PlanSHA256 = strings.Repeat("e", 64)
	if _, err := ComplexPlanFeedbackForNextPlanning(wrongHash, "v1", strings.Repeat("c", 64), strings.Repeat("d", 64)); err == nil {
		t.Fatal("mismatched review plan hash was accepted")
	}

	wrongVersion := snapshot
	if feedback, err := ComplexPlanFeedbackForNextPlanning(wrongVersion, "v2", strings.Repeat("c", 64), strings.Repeat("d", 64)); err != nil || feedback != nil {
		t.Fatalf("a version without plans must have no feedback: %#v err=%v", feedback, err)
	}

	wrongRequirement := snapshot
	if _, err := ComplexPlanFeedbackForNextPlanning(wrongRequirement, "v1", strings.Repeat("e", 64), strings.Repeat("d", 64)); err == nil {
		t.Fatal("feedback from another requirement hash was accepted")
	}

	wrongCompilation := snapshot
	if _, err := ComplexPlanFeedbackForNextPlanning(wrongCompilation, "v1", strings.Repeat("c", 64), strings.Repeat("e", 64)); err == nil {
		t.Fatal("feedback from another compilation was accepted")
	}

	unsettled := snapshot
	unsettled.AgentSteps = append([]AgentStep(nil), snapshot.AgentSteps...)
	unsettled.AgentSteps[0].SendStatus = AgentStepSendStatusSent
	if _, err := ComplexPlanFeedbackForNextPlanning(unsettled, "v1", strings.Repeat("c", 64), strings.Repeat("d", 64)); err == nil {
		t.Fatal("unsettled review step was accepted as feedback")
	}
}

func TestDeriveComplexPlanningPhaseStopsAfterThreeAutomaticReplans(t *testing.T) {
	snapshot := replanFeedbackSnapshot()
	confirmed := []RequirementVersion{{ID: "v1", Status: RequirementVersionStatusConfirmed, Version: 1}}
	for round := 2; round <= MaxComplexAutomaticReplans+1; round++ {
		sha := strings.Repeat(string(rune('a'+round)), 64)
		snapshot.Plans = append(snapshot.Plans, ComplexEngineeringPlan{
			ID: "plan-" + string(rune('0'+round)), Version: int64(round), RequirementVersionID: "v1",
			RequirementSHA256: strings.Repeat("c", 64), CompilationSHA256: strings.Repeat("d", 64),
			PlanJSON: `{}`, PlanSHA256: sha,
		})
		snapshot.Reviews = append(snapshot.Reviews, ComplexPlanReview{
			ID: "review-" + string(rune('0'+round)), PlanID: "plan-" + string(rune('0'+round)), PlanSHA256: sha,
			Verdict: PlanReviewReplan, ReasonCode: "REPLAN_REQUIRED", Summary: "继续修订。", FindingsJSON: `[]`,
			StewardRoleBindingID: "steward", AgentStepID: "step-" + string(rune('0'+round)), ReviewRequestID: "review-request-" + string(rune('0'+round)),
		})
		snapshot.AgentSteps = append(snapshot.AgentSteps, AgentStep{
			ID: "step-" + string(rune('0'+round)), RoleBindingID: "steward", Kind: ComplexAgentStepPlanReview,
			RequestID: "review-request-" + string(rune('0'+round)), SendStatus: AgentStepSendStatusSettled,
		})
	}
	if count := ComplexReplanCount(snapshot, "v1"); count != MaxComplexAutomaticReplans+1 {
		t.Fatalf("replan count = %d", count)
	}
	if !ComplexReplanLimitReached(snapshot, "v1") {
		t.Fatal("limit was not reached after the fourth valid REPLAN")
	}
	phase, reason := DeriveComplexPlanningPhase(snapshot, confirmed)
	if phase != ComplexPlanningNeedsHuman || reason != ReasonComplexReplanLimitReached {
		t.Fatalf("limit phase = %s/%s", phase, reason)
	}
}
