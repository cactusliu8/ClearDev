package cleardev

import "testing"

func TestUnifiedFailureRoutesByAuthorityAndSourceNotDisplayCategory(t *testing.T) {
	for _, tc := range []struct {
		name          string
		input         WorkflowFailureInput
		owner, action string
	}{
		{"unpublished implementation", WorkflowFailureInput{Role: TrustedOwnerBuilder, ReasonCode: "CANDIDATE_INVALID", ProblemCode: "NO_IMPLEMENTATION_CHANGE", RecoveryAction: RecoveryContinueBuilder, Current: true, Eligible: true, BeforePublish: true}, TrustedOwnerBuilder, FailureRepair},
		{"untyped candidate is not proof", WorkflowFailureInput{Role: TrustedOwnerBuilder, ReasonCode: "CANDIDATE_INVALID", RecoveryAction: RecoveryContinueBuilder, Current: true, Eligible: true}, TrustedOwnerHuman, FailureHuman},
		{"late publication cannot be repaired blindly", WorkflowFailureInput{Role: TrustedOwnerBuilder, ReasonCode: "CANDIDATE_INVALID", ProblemCode: "NO_IMPLEMENTATION_CHANGE", RecoveryAction: RecoveryContinueBuilder, Current: true, Eligible: true}, TrustedOwnerHuman, FailureHuman},
		{"repeat escalates", WorkflowFailureInput{Role: TrustedOwnerBuilder, ReasonCode: "CANDIDATE_INVALID", ProblemCode: "HANDOFF_CONTENT", RecoveryAction: RecoveryContinueBuilder, Current: true, Eligible: true, BeforePublish: true, Repeated: true}, TrustedOwnerPlanner, FailureEscalate},
		{"Planner answers within same authority", WorkflowFailureInput{Role: TrustedOwnerBuilder, ReasonCode: "CANDIDATE_INVALID", ProblemCode: "HANDOFF_CONTENT", RecoveryAction: RecoveryContinueBuilder, Current: true, Eligible: true, BeforePublish: true, Repeated: true, PlannerResolved: true}, TrustedOwnerBuilder, FailureRepair},
		{"Builder unable", WorkflowFailureInput{Role: TrustedOwnerBuilder, ReasonCode: "BUILDER_BLOCKED", RecoveryAction: RecoveryContinueBuilder, Current: true, Eligible: true}, TrustedOwnerPlanner, FailureEscalate},
		{"Builder explicit human", WorkflowFailureInput{Role: TrustedOwnerBuilder, ReasonCode: "HUMAN_DECISION_REQUIRED", RecoveryAction: RecoveryContinueBuilder, Current: true, Eligible: true}, TrustedOwnerHuman, FailureHuman},
		{"Steward protocol", WorkflowFailureInput{Role: TrustedOwnerSteward, ReasonCode: "PRODUCT_DISCOVERY_INVALID", RecoveryAction: RecoveryRetryPlanningStep, Current: true, Eligible: true}, TrustedOwnerSteward, FailureRetry},
		{"Reviewer technical", WorkflowFailureInput{Role: TrustedOwnerReviewer, ReasonCode: "REVIEWER_UNAVAILABLE", ProblemCode: "SETTLED_TECHNICAL_FAILURE", RecoveryAction: RecoveryRetryReview, Current: true, Eligible: true}, TrustedOwnerReviewer, FailureRetry},
		{"Reviewer verdict cannot be retried for PASS", WorkflowFailureInput{Role: TrustedOwnerReviewer, ReasonCode: "REVIEW_BLOCKED", RecoveryAction: RecoveryRetryReview, Current: true, Eligible: true}, TrustedOwnerHuman, FailureHuman},
		{"unknown delivery", WorkflowFailureInput{Role: TrustedOwnerBuilder, ReasonCode: "DELIVERY_UNKNOWN", ProblemCode: "SETTLED_TECHNICAL_FAILURE", RecoveryAction: RecoveryContinueBuilder, Current: true, Eligible: true}, TrustedOwnerControlPlane, FailureObserve},
		{"stale selection", WorkflowFailureInput{Role: TrustedOwnerBuilder, ReasonCode: "CANDIDATE_INVALID", ProblemCode: "HANDOFF_CONTENT", RecoveryAction: RecoveryContinueBuilder, Eligible: true, BeforePublish: true}, TrustedOwnerHuman, FailureStopped},
		{"budget", WorkflowFailureInput{Role: TrustedOwnerBuilder, ReasonCode: "BUILDER_BLOCKED", Current: true, UnavailableReason: "BUILDER_BUDGET_EXHAUSTED"}, TrustedOwnerHuman, FailureHuman},
		{"unknown new failure", WorkflowFailureInput{Role: TrustedOwnerPlanner, ReasonCode: "FUTURE_UNCLASSIFIED_ERROR", Current: true, Eligible: true}, TrustedOwnerHuman, FailureHuman},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RouteWorkflowFailure(tc.input)
			if got.Owner != tc.owner || got.Action != tc.action {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestUnifiedFailureNeverTreatsEscalationAsPermission(t *testing.T) {
	for _, problem := range []string{"PATH_SCOPE", "UNSAFE_PATH", "AUTHORITY", "SOURCE_CHANGED"} {
		for _, role := range []string{TrustedOwnerBuilder, TrustedOwnerPlanner, TrustedOwnerReviewer, TrustedOwnerSteward} {
			got := RouteWorkflowFailure(WorkflowFailureInput{Role: role, ReasonCode: "CANDIDATE_INVALID", ProblemCode: problem, RecoveryAction: RecoveryContinueBuilder, Current: true, Eligible: true, BeforePublish: true, Repeated: true, PlannerResolved: true})
			if got.Owner != TrustedOwnerHuman || got.Action != FailureHuman {
				t.Fatalf("%s/%s gained authority: %+v", role, problem, got)
			}
		}
	}
	for _, reason := range []string{"PAUSED", "CANCELLED", "REQUIREMENT_CANCELLED", "DIRECTION_CHANGE_STOPPED"} {
		got := RouteWorkflowFailure(WorkflowFailureInput{ReasonCode: reason, Current: true, Eligible: true, PlannerResolved: true})
		if got.Action != FailureStopped {
			t.Fatalf("%s automatically resumed", reason)
		}
	}
}
