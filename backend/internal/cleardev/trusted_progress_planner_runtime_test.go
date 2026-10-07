package cleardev

import "testing"

func TestPlannerRuntimeProgressHashIncludesExactDecision(t *testing.T) {
	legacy := TrustedProgressSummary{DevelopmentRequirementID: "requirement", Phase: TrustedPhaseCompleted}
	before := trustedProgressFactHash(legacy)
	legacy.PlannerCoordination = []TrustedPlannerCoordination{}
	if trustedProgressFactHash(legacy) != before {
		t.Fatal("empty runtime history changed the legacy fact hash")
	}
	legacy.PlannerCoordination = []TrustedPlannerCoordination{{EventID: "event", Decision: PlannerRuntimeContinue, Summary: "Continue with unchanged contracts"}}
	continued := trustedProgressFactHash(legacy)
	if continued == before {
		t.Fatal("coordination history was omitted from the fact hash")
	}
	legacy.PlannerCoordination[0].Decision = PlannerRuntimeAmend
	legacy.PlannerCoordination[0].AppliedContracts = []TrustedPlannerContractRevision{{ID: "revision", EffectivePackageSHA256: "new-contract"}}
	if trustedProgressFactHash(legacy) == continued {
		t.Fatal("changed effective contract can reuse a stale progress explanation")
	}
}
