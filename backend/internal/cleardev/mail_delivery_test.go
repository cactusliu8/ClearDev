package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMailFixedPathsCannotBeWidenedByPlan(t *testing.T) {
	for _, required := range []string{`integrationCheckIds must be exactly ["demo-integration"]`, "generatedPaths, sharedPathsRequireApproval, and dependencyKeys must be empty", "bare directory names such as frontend or test", "never plan an orphan test file that full npm test cannot execute"} {
		if !strings.Contains(MailDeliveryInstructions, required) {
			t.Fatalf("mail planner instructions do not expose enforced rule %q", required)
		}
	}
	for _, name := range []string{"backend/src/server.ts", "frontend/app.js", "test/new.test.js"} {
		if !MailWritePathAllowed(name) {
			t.Errorf("rejected %s", name)
		}
	}
	for _, name := range []string{"package.json", "package-lock.json", "frontend/package.json", "frontend/pnpm-lock.yaml", "migrations/new.sql", "backend/src/db.ts", "backend/src/schema.sql", "frontend/.npmrc", "frontend/node_modules/a.js", "test/../package.json", "../frontend/a.js", "frontend\\app.js", ".github/workflows/a.yml"} {
		if MailWritePathAllowed(name) {
			t.Errorf("accepted %s", name)
		}
	}
	plan := ComplexEngineeringPlanResult{Tasks: []ComplexPlanTask{{Key: "task-1", WritePaths: []string{"backend/src/**", "frontend/**", "test/**"}, RequiredCheckIDs: []string{"demo-integration"}}}, IntegrationCheckIDs: []string{"demo-integration"}, ParallelSuggestion: ComplexParallelSuggestion{RecommendedBuilderCount: 1}}
	if err := ValidateMailPlan(plan); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"**", "backend/**", "frontend", "frontend/", "test", "test/", "frontend/package.json", "migrations/**", "test/../backend/src/**"} {
		plan.Tasks[0].WritePaths = []string{path}
		plan.Tasks[0].ForbiddenPaths = []string{} // Agent can't weaken the outer policy.
		if ValidateMailPlan(plan) == nil {
			t.Errorf("plan widened to %s", path)
		}
	}
	plan.Tasks[0].WritePaths = []string{"frontend/**"}
	plan.IntegrationCheckIDs = []string{"demo-frontend", "demo-integration"}
	if ValidateMailPlan(plan) == nil {
		t.Fatal("mail plan accepted extra integration check instead of exact demo-integration")
	}
}

func TestMailV2PlanBoundsTasksBuildersAndDependencies(t *testing.T) {
	plan := ComplexEngineeringPlanResult{
		Tasks: []ComplexPlanTask{
			{Key: "frontend", WritePaths: []string{"frontend/app.js"}, RequiredCheckIDs: []string{"demo-frontend", "demo-integration"}},
			{Key: "tests", WritePaths: []string{"test/frontend.test.js"}, RequiredCheckIDs: []string{"demo-integration"}},
			{Key: "followup", WritePaths: []string{"frontend/index.html"}, RequiredCheckIDs: []string{"demo-integration"}, DependencyKeys: []string{"frontend", "tests"}},
		},
		IntegrationCheckIDs: []string{"demo-integration"}, ParallelSuggestion: ComplexParallelSuggestion{RecommendedBuilderCount: 2},
	}
	if err := ValidateMailPlan(plan); err != nil {
		t.Fatal(err)
	}
	v1 := plan
	v1.Tasks = v1.Tasks[:1]
	v1.ParallelSuggestion.RecommendedBuilderCount = 1
	if err := ValidateMailPlanForPolicy(MailDeliveryPolicyV1, v1); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMailPlanForPolicy(MailDeliveryPolicyV1, plan); err == nil {
		t.Fatal("historical V1 accepted a multi-task plan")
	}
	tooMany := plan
	tooMany.Tasks = append(append([]ComplexPlanTask{}, plan.Tasks...), ComplexPlanTask{Key: "four", WritePaths: []string{"test/four.test.js"}, RequiredCheckIDs: []string{"demo-integration"}})
	if ValidateMailPlan(tooMany) == nil {
		t.Fatal("V2 accepted more than three tasks")
	}
	threeBuilders := plan
	threeBuilders.ParallelSuggestion.RecommendedBuilderCount = 3
	if ValidateMailPlan(threeBuilders) == nil {
		t.Fatal("V2 accepted more than two Builders")
	}
	reversed := plan
	reversed.Tasks = []ComplexPlanTask{plan.Tasks[2], plan.Tasks[0], plan.Tasks[1]}
	if ValidateMailPlan(reversed) == nil {
		t.Fatal("V2 approved a consumer before the dependencies required by persistent task ordinals")
	}
	cycle := plan
	cycle.Tasks = append([]ComplexPlanTask(nil), plan.Tasks...)
	cycle.Tasks[0].DependencyKeys = []string{"followup"}
	if ValidateMailPlan(cycle) == nil {
		t.Fatal("V2 accepted a cyclic task DAG")
	}
}

func TestMailV2OverlappingTasksCannotRunTwoBuilders(t *testing.T) {
	plan := ComplexEngineeringPlanResult{
		Tasks: []ComplexPlanTask{
			{Key: "left", WritePaths: []string{"frontend/**"}, RequiredCheckIDs: []string{"demo-integration"}},
			{Key: "right", WritePaths: []string{"frontend/app.js"}, RequiredCheckIDs: []string{"demo-integration"}},
			{Key: "followup", WritePaths: []string{"test/frontend.test.js"}, RequiredCheckIDs: []string{"demo-integration"}, DependencyKeys: []string{"left", "right"}},
		},
		IntegrationCheckIDs: []string{"demo-integration"},
		ParallelSuggestion:  ComplexParallelSuggestion{RecommendedBuilderCount: 2},
	}
	if err := ValidateMailPlanForPolicy(MailDeliveryPolicyV2, plan); err != nil {
		t.Fatal(err)
	}
	selection, err := SelectComplexExecutionMode(plan.Tasks, plan.ParallelSuggestion.RecommendedBuilderCount)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != WorkModeStandard || selection.BuilderCount != 1 || selection.ReasonCode != ReasonParallelUnsafeDegrade {
		t.Fatalf("overlapping mail V2 tasks were scheduled concurrently: %#v", selection)
	}
}

func mailProofFixture() MailDeliveryProof {
	scope := MailScopeProof{Policy: MailDeliveryPolicyV1, BaseSHA: strings.Repeat("a", 40), CandidateSHA: strings.Repeat("b", 40), SourceManifestID: strings.Repeat("c", 64), SourceTreeOID: strings.Repeat("d", 40), OldTestCount: 8, ExtraTestPaths: []string{"test/added.test.js"}}
	check := func(suffix string, argv []string) MailCheckProof {
		return MailCheckProof{RunID: "check" + suffix, CandidateSHA: scope.CandidateSHA, SourceManifestID: scope.SourceManifestID, SourceTreeOID: scope.SourceTreeOID, ImageID: "sha256:image", EnvironmentID: strings.Repeat("e", 64), OutputSHA256: strings.Repeat("f", 64), Argv: argv, Passed: true}
	}
	extra := check(":extra-tests", MailExtraTestArgv(scope.ExtraTestPaths))
	return MailDeliveryProof{Scope: scope, Tests: check(":npm-test", []string{"npm", "test"}), ExtraTests: &extra, Health: check(":health", []string{"trusted-mail-health-v1"}), HealthProbeSHA256: MailHealthProbeSHA256}
}

func TestMailDeliveryProofRejectsMissingFailedAndStaleFacts(t *testing.T) {
	valid := mailProofFixture()
	raw, _ := json.Marshal(valid)
	validate := func(raw []byte) error {
		return ValidateMailDeliveryProof(string(raw), valid.Scope.BaseSHA, valid.Scope.CandidateSHA, "check", "sha256:image")
	}
	if err := validate(raw); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*MailDeliveryProof){
		"missing-health":        func(p *MailDeliveryProof) { p.Health = MailCheckProof{} },
		"stale-health":          func(p *MailDeliveryProof) { p.Health.CandidateSHA = p.Scope.BaseSHA },
		"different-tree":        func(p *MailDeliveryProof) { p.Tests.SourceTreeOID = p.Scope.BaseSHA },
		"no-full-npm-test":      func(p *MailDeliveryProof) { p.Tests.Argv = []string{"node", "--test"} },
		"test-failed":           func(p *MailDeliveryProof) { p.Tests.Passed = false },
		"false-pass-nonzero":    func(p *MailDeliveryProof) { p.Tests.ExitCode = 1 },
		"timed-out":             func(p *MailDeliveryProof) { p.Health.TimedOut = true },
		"truncated":             func(p *MailDeliveryProof) { p.Tests.Truncated = true },
		"new-tests-not-run":     func(p *MailDeliveryProof) { p.ExtraTests = nil },
		"new-tests-other-entry": func(p *MailDeliveryProof) { p.ExtraTests.Argv = []string{"npm", "test"} },
		"no-old-tests":          func(p *MailDeliveryProof) { p.Scope.OldTestCount = 0 },
		"wrong-policy":          func(p *MailDeliveryProof) { p.Scope.Policy = "AGENT_POLICY" },
		"wrong-health-program":  func(p *MailDeliveryProof) { p.HealthProbeSHA256 = strings.Repeat("a", 64) },
		"wrong-run":             func(p *MailDeliveryProof) { p.Health.RunID = "old:health" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := mailProofFixture()
			mutate(&p)
			data, _ := json.Marshal(p)
			if validate(data) == nil {
				t.Fatal("invalid proof accepted")
			}
		})
	}
	if validate([]byte(`{"passed":true}`)) == nil {
		t.Fatal("Agent PASS accepted")
	}
}
