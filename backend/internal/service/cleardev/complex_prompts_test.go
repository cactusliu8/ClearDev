package cleardev

import (
	"encoding/json"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestComplexCompilationPromptDoesNotSearchForExamples(t *testing.T) {
	snapshot := core.ComplexPlanningSnapshot{
		Requirement: core.ComplexRequirement{
			OriginalPRDText:            "prd",
			OriginalPRDSHA256:          strings.Repeat("b", 64),
			TargetRequirementVersionID: "ver-1",
		},
	}
	prompt := complexCompilationPrompt(snapshot, "req-1", strings.Repeat("a", 64), 0)
	for _, required := range []string{
		"Do not search the repository for compilation examples, sample JSON, or previous compilation results.",
		"If the original PRD names contract files, you may read those files.",
		"Do not explore unrelated files looking for examples.",
		"After you finish reading any contract files named by the original PRD, the next assistant message must be the complete JSON object. Do not write any more commentary.",
		"Every requirement key, acceptance scenario key, and blocking question key must match ^[a-z][a-z0-9-]{0,39}$ exactly, must be at most 40 characters, and must be unique within its list. Do not use underscores.",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("compilation prompt is missing %q:\n%s", required, prompt)
		}
	}
}

func TestComplexCompilationPromptExamplesParseAndExtract(t *testing.T) {
	snapshot := core.ComplexPlanningSnapshot{
		Requirement: core.ComplexRequirement{
			OriginalPRDText:            "prd",
			OriginalPRDSHA256:          strings.Repeat("b", 64),
			TargetRequirementVersionID: "ver-1",
		},
	}
	contextSHA := strings.Repeat("a", 64)
	prompt := complexCompilationPrompt(snapshot, "req-1", contextSHA, 0)
	extracted, err := promptJSONObject(prompt, "REQUIREMENT_COMPILATION")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.ParseRequirementCompilationResult([]byte(extracted), "req-1", "ver-1", contextSHA, 0, false); err != nil {
		t.Fatalf("round 0 example: %v\n%s", err, extracted)
	}

	round1 := complexCompilationExample(snapshot, "req-1", contextSHA, 1)
	parsed, err := core.ParseRequirementCompilationResult([]byte(round1), "req-1", "ver-1", contextSHA, 1, false)
	if err != nil {
		t.Fatalf("round 1 example: %v\n%s", err, round1)
	}
	if parsed.Outcome != "READY" || !json.Valid([]byte(extracted)) || !json.Valid([]byte(round1)) {
		t.Fatal("compilation prompt examples were not valid JSON results")
	}
}

func TestComplexPlanningPromptsAreCaseNeutral(t *testing.T) {
	fullStackPRD, err := core.FrozenDemoPRD()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		confirmed   string
		originalPRD string
	}{
		{
			name:        "small-email-list",
			confirmed:   "规范化本地邮件名单，按规范化邮箱去重，并输出接受和拒绝数量。",
			originalPRD: "处理一个小型本地邮件名单；不访问外部网络。",
		},
		{
			name:        "s11-full-stack",
			confirmed:   "把规范化邮件名单保存在 SQLite，通过本机 HTTP 接口和浏览器页面显示导入统计。",
			originalPRD: fullStackPRD,
		},
		{
			name:        "campaign-representative",
			confirmed:   "在现有联系人应用中增加活动草稿、受众预览、一次性冻结受众和历史查看。",
			originalPRD: "活动必须可以编辑内容、预览排除统计、冻结不可变受众并在重启后查看历史；不发送邮件。",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := promptTestRequirementDocument()
			version := &core.RequirementVersion{
				ID:              "ver-1",
				RequirementText: tc.confirmed,
				SHA256:          strings.Repeat("c", 64),
			}
			compilation := core.ComplexCompilation{CompilationSHA256: strings.Repeat("d", 64)}
			planPrompt := complexEngineeringPlanPrompt("plan-req", version, compilation, doc, tc.originalPRD)
			assertNeutralEngineeringPlanPrompt(t, planPrompt)

			plan := core.ComplexEngineeringPlan{
				ID:         "plan-1",
				PlanJSON:   `{"technicalApproach":"candidate supplied for review","tasks":[]}`,
				PlanSHA256: strings.Repeat("e", 64),
			}
			reviewPrompt := complexPlanReviewPrompt("review-req", plan, doc)
			if strings.Count(reviewPrompt, plan.PlanJSON) != 1 {
				t.Fatal("review prompt did not include the exact candidate plan once")
			}
			if strings.Count(reviewPrompt, mustComplexPromptJSON(doc)) != 1 {
				t.Fatal("review prompt did not include the complete normalized requirement once")
			}
			assertNeutralPlanReviewPrompt(t, strings.Replace(reviewPrompt, plan.PlanJSON, "", 1))
		})
	}
}

func TestComplexPlanReviewPromptIncludesOmittedAcceptanceContext(t *testing.T) {
	doc := promptTestRequirementDocument()
	plan := core.ComplexEngineeringPlan{
		ID: "plan-1",
		PlanJSON: `{"technicalApproach":"candidate omits the third acceptance","tasks":[` +
			`{"key":"first","requirementIds":["REQ-001"],"acceptanceIds":["ACC-001"]},` +
			`{"key":"second","requirementIds":["REQ-002"],"acceptanceIds":["ACC-002"]}]}`,
		PlanSHA256: strings.Repeat("e", 64),
	}

	prompt := complexPlanReviewPrompt("review-req", plan, doc)
	withoutPlan := strings.Replace(prompt, plan.PlanJSON, "", 1)
	for _, required := range []string{"REQ-003", "third requirement", "ACC-003", "third acceptance"} {
		if !strings.Contains(withoutPlan, required) {
			t.Fatalf("review prompt cannot identify omitted requirement context %q:\n%s", required, prompt)
		}
	}
}

func TestComplexCompilationExampleKeepsFullStackMUST(t *testing.T) {
	prd, err := core.FrozenDemoPRD()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := core.ComplexPlanningSnapshot{
		Requirement: core.ComplexRequirement{
			OriginalPRDText:            prd,
			OriginalPRDSHA256:          strings.Repeat("b", 64),
			TargetRequirementVersionID: "ver-1",
		},
	}
	contextSHA := strings.Repeat("a", 64)
	round0 := complexCompilationExample(snapshot, "req-1", contextSHA, 0)
	if _, err := core.ParseRequirementCompilationResult([]byte(round0), "req-1", "ver-1", contextSHA, 0, false); err != nil {
		t.Fatalf("round 0: %v\n%s", err, round0)
	}
	if !strings.Contains(round0, `"store-sqlite"`) || !strings.Contains(round0, `"local-api"`) || !strings.Contains(round0, `"browser-page"`) {
		t.Fatalf("full-stack compilation example dropped MUST fields:\n%s", round0)
	}
	round1 := complexCompilationExample(snapshot, "req-1", contextSHA, 1)
	parsed, err := core.ParseRequirementCompilationResult([]byte(round1), "req-1", "ver-1", contextSHA, 1, false)
	if err != nil {
		t.Fatalf("round 1: %v\n%s", err, round1)
	}
	keys := map[string]struct{}{}
	for _, req := range parsed.Requirements {
		keys[req.Key] = struct{}{}
	}
	for _, want := range []string{"store-sqlite", "normalize-email", "reject-invalid", "deduplicate-email", "local-api", "browser-page"} {
		if _, ok := keys[want]; !ok {
			t.Fatalf("missing MUST %s in %#v", want, keys)
		}
	}
}

func promptTestRequirementDocument() core.NormalizedRequirementDocument {
	return core.NormalizedRequirementDocument{
		Requirements: []core.NormalizedRequirement{
			{ID: "REQ-001", Priority: "MUST", Text: "first requirement", AcceptanceIDs: []string{"ACC-001"}},
			{ID: "REQ-002", Priority: "MUST", Text: "second requirement", AcceptanceIDs: []string{"ACC-002"}},
			{ID: "REQ-003", Priority: "MUST", Text: "third requirement", AcceptanceIDs: []string{"ACC-003"}},
		},
		AcceptanceScenarios: []core.NormalizedAcceptance{
			{ID: "ACC-001", Text: "first acceptance"},
			{ID: "ACC-002", Text: "second acceptance"},
			{ID: "ACC-003", Text: "third acceptance"},
		},
	}
}

func assertNeutralEngineeringPlanPrompt(t *testing.T, prompt string) {
	t.Helper()
	lower := strings.ToLower(prompt)
	for _, required := range []string{
		"inspect the repository's current modules",
		"dependencies, risks, path conflicts, and coordination cost",
		"narrowest repository-relative paths",
		"do not split work only to reach a task or builder count",
		"do not omit dependencies merely to make tasks appear parallel",
		"suggest 1-3 builders",
		"separating frontend and backend layers alone is not evidence",
		"the control plane makes the final mode decision",
	} {
		if !strings.Contains(lower, required) {
			t.Fatalf("engineering prompt is missing neutral rule %q:\n%s", required, prompt)
		}
	}
	assertNoFixedPlanningAnswer(t, prompt)
	if strings.Count(prompt, "recommendedBuilderCount") != 1 {
		t.Fatalf("recommendedBuilderCount must appear once in the result field contract:\n%s", prompt)
	}
	for _, required := range []string{
		"kind=COMPLEX_ENGINEERING_PLAN", "planningRequestId=plan-req", "requirementVersionId=ver-1",
		"schemaVersion (integer 1)", "technicalApproach", "tasks, integrationCheckIds, parallelSuggestion, risks",
		"key, title, objective, requirementIds, acceptanceIds, writePaths, generatedPaths",
		"sharedPathsRequireApproval, forbiddenPaths, requiredCheckIds, dependencyKeys",
		"Each task key must match ^[a-z][a-z0-9-]{0,39}$ exactly, must be at most 40 characters, and must be unique within the result.",
		"Every JSON string-array value must be non-empty, and values must be unique within that array.",
		"requirementIds, acceptanceIds, writePaths, forbiddenPaths, and requiredCheckIds must each contain 1-20 items.",
		"generatedPaths, sharedPathsRequireApproval, and dependencyKeys must each contain 0-20 items.",
		"Every dependencyKeys entry must exactly equal a task key in the same result.",
		"Do not normalize, rewrite, or substitute characters in task keys or dependencyKeys.",
		"Every path in writePaths, generatedPaths, sharedPathsRequireApproval, and forbiddenPaths must stay inside those frozen roots.",
		"Do not name compiler configuration files or any other file outside those roots, even as a forbidden path.",
		"integrationCheckIds must contain 1-10 frozen check ids. risks must contain 0-20 items.",
		"technicalApproach, every task title and objective, parallelSuggestion.reason, and every risks item must each be non-empty and at most 10000 Unicode characters.",
		"recommendedBuilderCount (an integer chosen from 1, 2, or 3 using the evidence above)",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("engineering plan result contract is missing %q:\n%s", required, prompt)
		}
	}
	if strings.Contains(prompt, `"kind":"COMPLEX_ENGINEERING_PLAN"`) {
		t.Fatalf("engineering prompt contains a copyable prefilled result object:\n%s", prompt)
	}
}

func assertNeutralPlanReviewPrompt(t *testing.T, promptWithoutPlan string) {
	t.Helper()
	lower := strings.ToLower(promptWithoutPlan)
	for _, required := range []string{
		"every must requirement and acceptance scenario",
		"entire code layer without necessity",
		"broad paths manufacture apparent non-conflicts",
		"every real ordering dependency is present",
		"simultaneously ready tasks",
		"risk, coordination cost",
		"frontend/backend separation alone is not sufficient evidence",
		"split only to reach a task or builder count",
		"do not return a replacement task graph or edited plan",
	} {
		if !strings.Contains(lower, required) {
			t.Fatalf("plan review prompt is missing rule %q:\n%s", required, promptWithoutPlan)
		}
	}
	assertNoFixedPlanningAnswer(t, promptWithoutPlan)
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(promptWithoutPlan)
	if strings.Contains(compact, `"verdict":"APPROVED"`) {
		t.Fatalf("plan review prompt prefilled an approval verdict:\n%s", promptWithoutPlan)
	}
	for _, required := range []string{
		"kind=COMPLEX_PLAN_REVIEW", "reviewRequestId=review-req", "planId=plan-1",
		"schemaVersion (integer 1)", "verdict, reasonCode, summary, findings",
		"code, message, requirementIds, taskKeys",
	} {
		if !strings.Contains(promptWithoutPlan, required) {
			t.Fatalf("plan review result contract is missing %q:\n%s", required, promptWithoutPlan)
		}
	}
	if strings.Contains(promptWithoutPlan, `"kind":"COMPLEX_PLAN_REVIEW"`) || strings.Contains(compact, `"findings":[{`) {
		t.Fatalf("plan review prompt contains a copyable prefilled result object:\n%s", promptWithoutPlan)
	}
}

func assertNoFixedPlanningAnswer(t *testing.T, prompt string) {
	t.Helper()
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(prompt)
	for _, fixed := range []string{
		`"migrations/**"`, `"backend/**"`, `"frontend/**"`,
		`"key":"schema"`, `"key":"api"`, `"key":"page"`,
		`"key":"normalize-and-deduplicate"`, `"key":"build-summary"`,
		`"dependencyKeys":["schema"]`, `"dependencyKeys":["normalize-and-deduplicate"]`,
		`"requiredCheckIds":["demo-database"]`, `"requiredCheckIds":["demo-backend","demo-api"]`,
		`"requiredCheckIds":["demo-frontend"]`, `"requiredCheckIds":["email-unit","deduplicate-unit"]`,
		`"requiredCheckIds":["summary-unit"]`, `"integrationCheckIds":["demo-integration"]`,
		`"integrationCheckIds":["all-tests"]`, `"recommendedBuilderCount":1`,
		`"recommendedBuilderCount":2`, `"recommendedBuilderCount":3`,
		`"mode":"STANDARD"`, `"mode":"PARALLEL"`,
	} {
		if strings.Contains(compact, fixed) {
			t.Fatalf("prompt leaked fixed planning answer %q:\n%s", fixed, prompt)
		}
	}
	for _, fixedProse := range []string{"keep the two-task", "recommendedbuildercount 1", "recommendedbuildercount 2", "recommendedbuildercount 3"} {
		if strings.Contains(strings.ToLower(prompt), fixedProse) {
			t.Fatalf("prompt leaked fixed planning prose %q:\n%s", fixedProse, prompt)
		}
	}
}
