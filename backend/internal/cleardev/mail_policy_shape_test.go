package cleardev

import (
	"strings"
	"testing"
)

func TestMailV2PolicyRequiresExactModeBuilderShape(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    WorkMode
		count   int
		allowed bool
	}{
		{"standard-one", WorkModeStandard, 1, true},
		{"parallel-two", WorkModeParallel, 2, true},
		{"standard-two", WorkModeStandard, 2, false},
		{"parallel-one", WorkModeParallel, 1, false},
		{"parallel-three", WorkModeParallel, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			versionSHA, planSHA := strings.Repeat("a", 64), strings.Repeat("b", 64)
			raw, _, err := BuildComplexExecutionRunPackage(tc.mode, "run", "version", versionSHA, "plan", planSHA)
			if err != nil {
				t.Fatal(err)
			}
			raw, digest, err := BindMailDeliveryPolicyV2(raw, strings.Repeat("c", 40))
			if err != nil {
				t.Fatal(err)
			}
			run := ComplexExecutionRun{
				ID: "run", Mode: tc.mode, FixedBuilderCount: tc.count,
				RequirementVersionID: "version", RequirementSHA256: versionSHA, PlanID: "plan", PlanSHA256: planSHA,
				TaskSetVersion: 1, ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest,
			}
			policy, _, required, err := MailDeliveryPolicyFromRun(run)
			if tc.allowed {
				if err != nil || !required || policy != MailDeliveryPolicyV2 {
					t.Fatalf("valid bounded mail shape rejected: policy=%s required=%v err=%v", policy, required, err)
				}
			} else if err == nil {
				t.Fatal("mail policy admitted an inconsistent mode/Builder count")
			}
		})
	}
}
