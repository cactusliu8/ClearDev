package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtraCoordinationHasNativeDecisionKind(t *testing.T) {
	spec, found := ProductionDecisionRegistry().Lookup("AUTHORIZE_EXTRA_PLANNER_COORDINATION")
	if !found || spec.BindingSchemaVersion != 1 || !spec.Allowed[HumanDecisionApprove] || !spec.Allowed[HumanDecisionReject] || !spec.Allowed[HumanDecisionLater] {
		t.Fatal("coordination limit has no bounded native human decision")
	}
}

func TestExtraCoordinationStrictBindingAndSeparateOutcome(t *testing.T) {
	b := ExtraCoordinationBinding{DevelopmentRequirementID: "req", ExecutionRunID: "run", EventID: "event", StopSHA256: strings.Repeat("a", 64), ContextSHA256: strings.Repeat("b", 64), PlannerRoleBindingID: "planner", AOSessionID: "session", ProviderConversationID: "native", WorkspacePath: "/worktree", SessionCreationKey: "key", CreationFingerprint: "fingerprint", Harness: "opencode", Model: "provider/model", Ordinal: 3}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := ParseExtraCoordinationBinding(raw); err != nil || parsed != b {
		t.Fatal(parsed, err)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"ordinal":3`, `"ordinal":4`, 1), strings.Replace(string(raw), `"eventId":"event"`, `"eventId":""`, 1), string(raw[:len(raw)-1]) + `,"approve":true}`} {
		if _, err := ParseExtraCoordinationBinding([]byte(bad)); err == nil {
			t.Fatal("invalid grant binding accepted", bad)
		}
	}
	history := &PlannerRuntimeSnapshot{Decisions: []PlannerCoordinationDecision{{EventID: "event", Source: "CONTROL_PLANE", Outcome: "LIMIT_REACHED", ReasonCode: ReasonPlannerRuntimeBudget}}}
	if d, known := PlannerRuntimeEffectiveDecision(history, "event"); !known || d.Outcome != "LIMIT_REACHED" {
		t.Fatal("original limit missing")
	}
	history.ExtraCoordinationGrants = []ExtraCoordinationGrant{{EventID: "event", DecisionRequestID: "native-request", Ordinal: 3}}
	if _, known := PlannerRuntimeEffectiveDecision(history, "event"); known {
		t.Fatal("native grant was treated as Planner success")
	}
	history.ExtraCoordinationDecisions = []PlannerCoordinationDecision{{EventID: "event", Source: "PLANNER", Outcome: PlannerRuntimeContinue}}
	if d, known := PlannerRuntimeEffectiveDecision(history, "event"); !known || d.Outcome != PlannerRuntimeContinue {
		t.Fatal("exact later result not projected")
	}
	if history.Decisions[0].Outcome != "LIMIT_REACHED" || PlannerRuntimeMaxRounds != 2 || PlannerRuntimeMaxRevisions != 2 {
		t.Fatal("grant rewrote original limits or history")
	}
}

func TestExtraCoordinationProgressShowsGrantWithoutErasingLimit(t *testing.T) {
	raw, _, err := BuildComplexExecutionRunPackage(WorkModeStandard, "run", "version", strings.Repeat("a", 64), "plan", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = BindPlannerTaskContractPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = BindRequirementFinalReviewPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, digest, err := BindPlannerRuntimePolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	run := ComplexExecutionRun{ID: "run", RequirementVersionID: "version", RequirementSHA256: strings.Repeat("a", 64), PlanID: "plan", PlanSHA256: strings.Repeat("b", 64), Mode: WorkModeStandard, FixedBuilderCount: 1, TaskSetVersion: 1, ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest}
	e := ComplexExecutionSnapshot{Run: run, PlannerRuntime: &PlannerRuntimeSnapshot{
		Events:                  []PlannerCoordinationEvent{{ID: "event", Report: PlannerCoordinationReport{Category: "ENGINEERING"}}},
		Requests:                []PlannerCoordinationRequest{{EventID: "event", Ordinal: 3}},
		Decisions:               []PlannerCoordinationDecision{{EventID: "event", Source: "CONTROL_PLANE", Outcome: "LIMIT_REACHED", ReasonCode: ReasonPlannerRuntimeBudget}},
		ExtraCoordinationGrants: []ExtraCoordinationGrant{{EventID: "event", DecisionRequestID: "native-request", Ordinal: 3}},
	}}
	summary := TrustedProgressSummary{}
	applyTrustedPlannerRuntime(&summary, TrustedProgressFacts{ComplexExecution: &e})
	if len(summary.PlannerCoordination) != 1 {
		t.Fatal("missing extra-coordination history")
	}
	item := summary.PlannerCoordination[0]
	if item.Decision != "LIMIT_REACHED" || item.MaxCoordinationRounds != 3 || item.ExtraCoordinationGrant == nil || item.ExtraCoordinationDecision != nil || len(summary.CurrentWork) != 1 {
		t.Fatal("grant hid original stop or pretended completion", item)
	}
	before := trustedProgressFactHash(summary)
	e.PlannerRuntime.ExtraCoordinationDecisions = []PlannerCoordinationDecision{{EventID: "event", Source: "PLANNER", Outcome: PlannerRuntimeContinue}}
	after := TrustedProgressSummary{}
	applyTrustedPlannerRuntime(&after, TrustedProgressFacts{ComplexExecution: &e})
	if len(after.CurrentWork) != 0 || after.PlannerCoordination[0].Decision != "LIMIT_REACHED" || after.PlannerCoordination[0].ExtraCoordinationDecision == nil || trustedProgressFactHash(after) == before {
		t.Fatal("separate result lost from progress facts")
	}
}
