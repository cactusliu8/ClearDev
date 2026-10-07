package cleardev

import "testing"

func TestWorkflowBlockerCategoriesAreUnambiguousAndConservative(t *testing.T) {
	seen := map[string]string{}
	for category, reasons := range workflowBlockerCategories {
		if category == "" || len(reasons) == 0 {
			t.Fatal("empty blocker guide family")
		}
		for _, reason := range reasons {
			if previous := seen[reason]; previous != "" {
				t.Fatalf("%s maps to both %s and %s", reason, previous, category)
			}
			seen[reason] = category
			if got := WorkflowBlockerCategory(reason); got != category {
				t.Fatalf("%s: got %s, want %s", reason, got, category)
			}
		}
	}
	for _, reason := range []string{"", "FUTURE_CHECK_FAILURE", "PROVIDER_EACCES_WITH_NO_PROOF", "UNRECOGNIZED_TIMEOUT"} {
		if got := WorkflowBlockerCategory(reason); got != "UNKNOWN" {
			t.Fatalf("unrecognized %q was guessed to be %s", reason, got)
		}
	}
	for reason, want := range map[string]string{
		"CHECK_FAILED": "CHECK_FAILURE", "CHECKER_UNAVAILABLE": "CHECK_ENVIRONMENT",
		"DELIVERY_UNKNOWN": "UNKNOWN_DELIVERY", "OBSERVATION_TIMEOUT": "OBSERVING",
		"BUILDER_SPAWN_FAILED": "SESSION", "MESSAGE_BUDGET_EXHAUSTED": "BUDGET",
		"AUTHENTICATION_REQUIRED": "AUTH", "PRODUCT_DISCUSSION_LIMIT_REACHED": "BUDGET",
		"AWAITING_CLARIFICATION": "INPUT", "PENDING_DECISION": "HUMAN",
		"EXECUTION_NOT_CURRENT": "SOURCE", "RESULT_DATA_BASELINE_CHANGED": "DATA",
	} {
		if got := WorkflowBlockerCategory(reason); got != want {
			t.Fatalf("unsafe failure-family conflation for %s: %s", reason, got)
		}
	}
}
