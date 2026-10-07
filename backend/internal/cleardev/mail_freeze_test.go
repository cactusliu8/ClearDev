package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTrustedMailFreezePolicyIsVersionedAndLegacySafe(t *testing.T) {
	versionSHA, planSHA := strings.Repeat("a", 64), strings.Repeat("b", 64)
	raw, _, err := BuildComplexExecutionRunPackage(WorkModeStandard, "run", "version", versionSHA, "plan", planSHA)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := BindMailFreezePolicy(raw); err == nil {
		t.Fatal("legacy run acquired freeze authority")
	}
	raw, _, err = BindMailDeliveryPolicy(raw, strings.Repeat("c", 40))
	if err != nil {
		t.Fatal(err)
	}
	raw, digest, err := BindMailAttemptPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	run := ComplexExecutionRun{ID: "run", Mode: WorkModeStandard, FixedBuilderCount: 1, RequirementVersionID: "version", RequirementSHA256: versionSHA, PlanID: "plan", PlanSHA256: planSHA, TaskSetVersion: 1, ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest}
	if TrustedMailFreeze(run) {
		t.Fatal("old bounded run implicitly upgraded")
	}
	raw, digest, err = BindMailFreezePolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	run.ExecutionPackageJSON, run.ExecutionPackageSHA256 = string(raw), digest
	if !TrustedMailFreeze(run) {
		t.Fatal("new trusted policy not recognized")
	}
	for _, mutation := range []string{"version", "attempt", "digest", "missing-delivery"} {
		copyRun := run
		var pkg ComplexExecutionRunPackage
		if err := json.Unmarshal(raw, &pkg); err != nil {
			t.Fatal(err)
		}
		switch mutation {
		case "version":
			pkg.FreezePolicy = "AGENT_SELECTED"
		case "attempt":
			pkg.AttemptPolicy = ""
		case "digest":
			pkg.PlanID = "another-plan"
		case "missing-delivery":
			pkg.DeliveryPolicy, pkg.DeliveryBaseSHA = "", ""
		}
		changed, err := marshalCanonicalJSON(pkg)
		if err != nil {
			t.Fatal(err)
		}
		copyRun.ExecutionPackageJSON, copyRun.ExecutionPackageSHA256 = string(changed), sha256Hex(changed)
		if _, _, err := MailPolicyFromRun(copyRun); err == nil || TrustedMailFreeze(copyRun) {
			t.Fatal("invalid freeze policy admitted", mutation)
		}
	}
}
