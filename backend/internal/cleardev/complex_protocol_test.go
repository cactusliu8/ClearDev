package cleardev

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestParseRequirementCompilationRoundZeroReadyRejected(t *testing.T) {
	raw := compilationJSON(t, map[string]any{
		"outcome": "READY", "blockingQuestions": []any{},
	})
	if _, err := ParseRequirementCompilationResult(raw, "req-1", "ver-1", strings.Repeat("a", 64), 0, false); err == nil {
		t.Fatal("round 0 READY was accepted")
	}
}

func TestParseRequirementCompilationRejectsUnknownNullAndDuplicateFields(t *testing.T) {
	valid := compilationJSON(t, nil)
	if _, err := ParseRequirementCompilationResult(valid, "req-1", "ver-1", strings.Repeat("a", 64), 0, false); err != nil {
		t.Fatalf("valid round 0 rejected: %v", err)
	}
	withUnknown := compilationJSON(t, map[string]any{"extra": true})
	if _, err := ParseRequirementCompilationResult(withUnknown, "req-1", "ver-1", strings.Repeat("a", 64), 0, false); err == nil {
		t.Fatal("unknown field was accepted")
	}
	if _, err := ParseRequirementCompilationResult([]byte(`{"schemaVersion":1,"kind":null}`), "req-1", "ver-1", strings.Repeat("a", 64), 0, false); err == nil {
		t.Fatal("null field was accepted")
	}
	duplicate := []byte(`{"schemaVersion":1,"schemaVersion":1,"kind":"REQUIREMENT_COMPILATION","outcome":"CLARIFICATION_REQUIRED","summary":"s","requirements":[{"key":"normalize-email","priority":"MUST","text":"normalize","acceptanceKeys":["normalize-scenario"]}],"acceptanceScenarios":[{"key":"normalize-scenario","text":"scenario"}],"constraints":[],"nonGoals":[],"terms":[],"assumptions":[],"conflicts":[],"blockingQuestions":[{"key":"invalid-address","text":"how?","reason":"stats","requirementKeys":["normalize-email"]}]}`)
	if _, err := ParseRequirementCompilationResult(duplicate, "req-1", "ver-1", strings.Repeat("a", 64), 0, false); err == nil {
		t.Fatal("duplicate JSON key was accepted")
	}
}

func TestParseRequirementCompilationRejectsDanglingRefsAndThirdRound(t *testing.T) {
	dangling := compilationJSON(t, map[string]any{
		"blockingQuestions": []any{map[string]any{
			"key": "invalid-address", "text": "how?", "reason": "stats", "requirementKeys": []string{"missing"},
		}},
	})
	if _, err := ParseRequirementCompilationResult(dangling, "req-1", "ver-1", strings.Repeat("a", 64), 0, false); err == nil {
		t.Fatal("dangling requirement key was accepted")
	}
	third := compilationJSON(t, map[string]any{"outcome": "READY", "blockingQuestions": []any{}})
	if _, err := ParseRequirementCompilationResult(third, "req-1", "ver-1", strings.Repeat("a", 64), 3, false); err == nil {
		t.Fatal("third round was accepted")
	}
	previous := compilationJSON(t, map[string]any{
		"requirements": []any{map[string]any{
			"key": "normalize-email", "priority": "MUST", "text": "normalize",
			"acceptanceKeys": []string{"normalize-scenario"}, "previousRequirementId": "REQ-001",
		}},
	})
	if _, err := ParseRequirementCompilationResult(previous, "req-1", "ver-1", strings.Repeat("a", 64), 0, false); err == nil {
		t.Fatal("initial compilation accepted previousRequirementId")
	}
}

func TestParseRequirementCompilationRoundOneReadyAndExtraRound(t *testing.T) {
	ready := compilationJSON(t, map[string]any{
		"outcome": "READY", "blockingQuestions": []any{},
	})
	got, err := ParseRequirementCompilationResult(ready, "req-1", "ver-1", strings.Repeat("a", 64), 1, false)
	if err != nil {
		t.Fatalf("round 1 READY rejected: %v", err)
	}
	if got.Outcome != "READY" {
		t.Fatalf("outcome = %s", got.Outcome)
	}
	extra := compilationJSON(t, map[string]any{
		"outcome":               "CLARIFICATION_REQUIRED",
		"additionalRoundReason": "Invalid-address counting still blocks the statistics MUST.",
	})
	if _, err := ParseRequirementCompilationResult(extra, "req-1", "ver-1", strings.Repeat("a", 64), 1, false); err != nil {
		t.Fatalf("round 1 extra questions rejected: %v", err)
	}
	missingReason := compilationJSON(t, map[string]any{
		"outcome": "CLARIFICATION_REQUIRED",
	})
	if _, err := ParseRequirementCompilationResult(missingReason, "req-1", "ver-1", strings.Repeat("a", 64), 1, false); err == nil {
		t.Fatal("round 1 extra questions without a reason were accepted")
	}
}

func TestNormalizedRequirementDocumentUsesStableIDsAndFixedOrder(t *testing.T) {
	raw := compilationJSON(t, map[string]any{
		"outcome": "READY", "blockingQuestions": []any{},
	})
	result, err := ParseRequirementCompilationResult(raw, "req-1", "ver-1", strings.Repeat("a", 64), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	reqMaps, accMaps := AssignStableCompilationIDs("comp-1", result)
	doc, encoded, digest, err := BuildNormalizedRequirementDocument(result, strings.Repeat("b", 64), reqMaps, accMaps)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Requirements[0].ID != "REQ-001" || doc.AcceptanceScenarios[0].ID != "ACC-001" {
		t.Fatalf("stable ids = %#v %#v", doc.Requirements, doc.AcceptanceScenarios)
	}
	if len(digest) != 64 || !json.Valid(encoded) {
		t.Fatalf("normalized document hash/bytes invalid: %s %s", digest, encoded)
	}
	if strings.Contains(string(encoded), `"key"`) {
		t.Fatal("normalized document still contains temporary keys")
	}
}

func TestParseComplexEngineeringPlanRejectsCyclesUnknownChecksAndTraversal(t *testing.T) {
	coverage := ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002", "REQ-003"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST", "REQ-003": "MUST", "REQ-004": "SHOULD"},
		AcceptanceIDs:  map[string]string{"ACC-001": "a", "ACC-002": "b", "ACC-003": "c"},
	}
	valid := planJSON(t, nil)
	if _, _, _, err := ParseComplexEngineeringPlanResult(valid, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	cycle := planJSON(t, map[string]any{
		"tasks": []any{
			planTask("normalize-and-deduplicate", []string{"REQ-001", "REQ-002"}, []string{"ACC-001", "ACC-002"}, []string{"src/email.js"}, []string{"email-unit"}, []string{"build-summary"}),
			planTask("build-summary", []string{"REQ-003"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"summary-unit"}, []string{"normalize-and-deduplicate"}),
		},
	})
	if _, _, _, err := ParseComplexEngineeringPlanResult(cycle, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err == nil {
		t.Fatal("cyclic plan was accepted")
	}
	unknown := planJSON(t, map[string]any{
		"tasks": []any{
			planTask("normalize-and-deduplicate", []string{"REQ-001", "REQ-002"}, []string{"ACC-001", "ACC-002"}, []string{"src/email.js"}, []string{"made-up"}, []string{}),
			planTask("build-summary", []string{"REQ-003"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"summary-unit"}, []string{"normalize-and-deduplicate"}),
		},
	})
	if _, _, _, err := ParseComplexEngineeringPlanResult(unknown, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err == nil {
		t.Fatal("unknown check was accepted")
	}
	traversal := planJSON(t, map[string]any{
		"tasks": []any{
			planTask("normalize-and-deduplicate", []string{"REQ-001", "REQ-002"}, []string{"ACC-001", "ACC-002"}, []string{"../secret"}, []string{"email-unit"}, []string{}),
			planTask("build-summary", []string{"REQ-003"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"summary-unit"}, []string{"normalize-and-deduplicate"}),
		},
	})
	if _, _, _, err := ParseComplexEngineeringPlanResult(traversal, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err == nil {
		t.Fatal("traversing path was accepted")
	}
	uncovered := planJSON(t, map[string]any{
		"tasks": []any{
			planTask("normalize-and-deduplicate", []string{"REQ-001"}, []string{"ACC-001"}, []string{"src/email.js"}, []string{"email-unit"}, []string{}),
			planTask("build-summary", []string{"REQ-004"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"summary-unit"}, []string{"normalize-and-deduplicate"}),
		},
	})
	if _, _, _, err := ParseComplexEngineeringPlanResult(uncovered, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err == nil {
		t.Fatal("plan missing MUST coverage was accepted")
	}
}

func TestComplexEngineeringPlanProtocolModeAndReviewCompatibility(t *testing.T) {
	coverage := ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002", "REQ-003"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST", "REQ-003": "MUST"},
		AcceptanceIDs:  map[string]string{"ACC-001": "a", "ACC-002": "b", "ACC-003": "c"},
	}
	parallelTasks := []any{
		planTask("normalize-email", []string{"REQ-001"}, []string{"ACC-001"}, []string{"src/email.js"}, []string{"email-unit"}, []string{}),
		planTask("deduplicate-email", []string{"REQ-002"}, []string{"ACC-002"}, []string{"src/deduplicate.js"}, []string{"deduplicate-unit"}, []string{}),
		planTask("build-summary", []string{"REQ-003"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"summary-unit"}, []string{"normalize-email", "deduplicate-email"}),
	}
	cases := []struct {
		name         string
		raw          []byte
		wantMode     WorkMode
		wantBuilders int
	}{
		{name: "standard", raw: planJSON(t, nil), wantMode: WorkModeStandard, wantBuilders: 1},
		{
			name: "parallel",
			raw: planJSON(t, map[string]any{
				"tasks": parallelTasks,
				"parallelSuggestion": map[string]any{
					"recommendedBuilderCount": 2,
					"reason":                  "Two ready tasks have disjoint narrow paths and enough work to justify coordination.",
				},
			}),
			wantMode:     WorkModeParallel,
			wantBuilders: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, _, planSHA, err := ParseComplexEngineeringPlanResult(tc.raw, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage)
			if err != nil {
				t.Fatalf("valid %s plan rejected: %v", tc.name, err)
			}
			selection, err := SelectComplexExecutionMode(parsed.Tasks, parsed.ParallelSuggestion.RecommendedBuilderCount)
			if err != nil || selection.Mode != tc.wantMode || selection.BuilderCount != tc.wantBuilders {
				t.Fatalf("%s selection = %#v err=%v", tc.name, selection, err)
			}
			taskKeys := make([]string, 0, len(parsed.Tasks))
			for _, task := range parsed.Tasks {
				taskKeys = append(taskKeys, task.Key)
			}
			approved := reviewJSON(t, nil)
			if _, _, err := ParseComplexPlanReviewResult(approved, "rev-1", "plan-1", planSHA, coverage, taskKeys); err != nil {
				t.Fatalf("valid %s plan review rejected: %v", tc.name, err)
			}
		})
	}
}

func TestParseComplexEngineeringPlanTaskKeyAndDependencyGrammar(t *testing.T) {
	coverage := ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002", "REQ-003"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST", "REQ-003": "MUST"},
		AcceptanceIDs:  map[string]string{"ACC-001": "a", "ACC-002": "b", "ACC-003": "c"},
	}
	planWithKeys := func(firstKey, dependencyKey string) []byte {
		return planJSON(t, map[string]any{
			"tasks": []any{
				planTask(firstKey, []string{"REQ-001", "REQ-002"}, []string{"ACC-001", "ACC-002"}, []string{"src/email.js"}, []string{"email-unit"}, []string{}),
				planTask("build-summary", []string{"REQ-003"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"summary-unit"}, []string{dependencyKey}),
			},
		})
	}
	parse := func(raw []byte) error {
		_, _, _, err := ParseComplexEngineeringPlanResult(raw, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage)
		return err
	}

	fortyCharacterKey := "a" + strings.Repeat("b", 39)
	if err := parse(planWithKeys(fortyCharacterKey, fortyCharacterKey)); err != nil {
		t.Fatalf("40-character task key was rejected: %v", err)
	}

	cases := []struct {
		name          string
		firstKey      string
		dependencyKey string
	}{
		{name: "underscore", firstKey: "implement_core", dependencyKey: "implement_core"},
		{name: "space", firstKey: "implement core", dependencyKey: "implement core"},
		{name: "uppercase", firstKey: "Implement-core", dependencyKey: "Implement-core"},
		{name: "too-long", firstKey: "a" + strings.Repeat("b", 40), dependencyKey: "a" + strings.Repeat("b", 40)},
		{name: "unknown-dependency", firstKey: "implement-core", dependencyKey: "missing-task"},
		{name: "inconsistent-dependency", firstKey: "implement-core-v2", dependencyKey: "implement-core"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := parse(planWithKeys(tc.firstKey, tc.dependencyKey)); err == nil {
				t.Fatalf("invalid task key or dependency was accepted: key=%q dependency=%q", tc.firstKey, tc.dependencyKey)
			}
		})
	}
}

func TestParseComplexEngineeringPlanForbiddenPathCountBoundary(t *testing.T) {
	coverage := ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002", "REQ-003"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST", "REQ-003": "MUST"},
		AcceptanceIDs:  map[string]string{"ACC-001": "a", "ACC-002": "b", "ACC-003": "c"},
	}
	forbiddenPaths := func(count int) []string {
		paths := make([]string, 0, count)
		for index := 0; index < count; index++ {
			paths = append(paths, fmt.Sprintf(".git/protected-%02d", index))
		}
		return paths
	}
	planWithForbiddenPaths := func(count int) []byte {
		first := planTask("normalize-and-deduplicate", []string{"REQ-001", "REQ-002"}, []string{"ACC-001", "ACC-002"}, []string{"src/email.js"}, []string{"email-unit"}, []string{})
		first["forbiddenPaths"] = forbiddenPaths(count)
		return planJSON(t, map[string]any{
			"tasks": []any{
				first,
				planTask("build-summary", []string{"REQ-003"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"summary-unit"}, []string{"normalize-and-deduplicate"}),
			},
		})
	}
	parse := func(raw []byte) error {
		_, _, _, err := ParseComplexEngineeringPlanResult(raw, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage)
		return err
	}

	if err := parse(planWithForbiddenPaths(20)); err != nil {
		t.Fatalf("20 forbiddenPaths were rejected: %v", err)
	}
	if err := parse(planWithForbiddenPaths(21)); err == nil {
		t.Fatal("21 forbiddenPaths were accepted")
	}
}

func TestParseComplexEngineeringPlanRejectsInvalidTaskAndBuilderCounts(t *testing.T) {
	coverage := ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002", "REQ-003"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST", "REQ-003": "MUST"},
		AcceptanceIDs:  map[string]string{"ACC-001": "a", "ACC-002": "b", "ACC-003": "c"},
	}
	for _, count := range []int{0, 4} {
		raw := planJSON(t, map[string]any{
			"parallelSuggestion": map[string]any{"recommendedBuilderCount": count, "reason": "invalid count"},
		})
		if _, _, _, err := ParseComplexEngineeringPlanResult(raw, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err == nil {
			t.Fatalf("recommendedBuilderCount %d was accepted", count)
		}
	}

	oneTask := []any{
		planTask("all-work", []string{"REQ-001", "REQ-002", "REQ-003"}, []string{"ACC-001", "ACC-002", "ACC-003"}, []string{"src/**"}, []string{"all-tests"}, []string{}),
	}
	sevenTasks := make([]any, 0, 7)
	for index := 0; index < 7; index++ {
		sevenTasks = append(sevenTasks, planTask(
			fmt.Sprintf("task-%d", index), []string{"REQ-001", "REQ-002", "REQ-003"},
			[]string{"ACC-001", "ACC-002", "ACC-003"}, []string{"src/**"}, []string{"all-tests"}, []string{},
		))
	}
	if _, _, _, err := ParseComplexEngineeringPlanResult(planJSON(t, map[string]any{"tasks": oneTask}), "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err != nil {
		t.Fatalf("natural one-task plan was rejected: %v", err)
	}
	fakeParallel := planJSON(t, map[string]any{"tasks": oneTask, "parallelSuggestion": map[string]any{"recommendedBuilderCount": 2, "reason": "伪造并行"}})
	if _, _, _, err := ParseComplexEngineeringPlanResult(fakeParallel, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err == nil {
		t.Fatal("one-task plan with two recommended builders was accepted")
	}
	if _, _, _, err := ParseComplexEngineeringPlanResult(planJSON(t, map[string]any{"tasks": sevenTasks}), "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err == nil {
		t.Fatal("seven-task plan was accepted")
	}
}

func TestParseComplexEngineeringPlanRejectsCheckThatDoesNotCoverTaskPaths(t *testing.T) {
	coverage := ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002", "REQ-003"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST", "REQ-003": "MUST", "REQ-004": "SHOULD"},
		AcceptanceIDs:  map[string]string{"ACC-001": "a", "ACC-002": "b", "ACC-003": "c"},
	}
	mismatched := planJSON(t, map[string]any{
		"tasks": []any{
			planTask("normalize-and-deduplicate", []string{"REQ-001", "REQ-002"}, []string{"ACC-001", "ACC-002"}, []string{"src/email.js"}, []string{"summary-unit"}, []string{}),
			planTask("build-summary", []string{"REQ-003"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"summary-unit"}, []string{"normalize-and-deduplicate"}),
		},
	})
	if _, _, _, err := ParseComplexEngineeringPlanResult(mismatched, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err == nil {
		t.Fatal("task writing src/email.js with only summary-unit was accepted")
	}
	partial := planJSON(t, map[string]any{
		"tasks": []any{
			planTask("normalize-and-deduplicate", []string{"REQ-001", "REQ-002"}, []string{"ACC-001", "ACC-002"}, []string{"src/email.js", "src/deduplicate.js"}, []string{"email-unit"}, []string{}),
			planTask("build-summary", []string{"REQ-003"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"summary-unit"}, []string{"normalize-and-deduplicate"}),
		},
	})
	if _, _, _, err := ParseComplexEngineeringPlanResult(partial, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err == nil {
		t.Fatal("task writing src/deduplicate.js without a covering check was accepted")
	}
	glob := planJSON(t, map[string]any{
		"tasks": []any{
			planTask("normalize-and-deduplicate", []string{"REQ-001", "REQ-002"}, []string{"ACC-001", "ACC-002"}, []string{"src/email.js", "src/deduplicate.js"}, []string{"all-tests"}, []string{}),
			planTask("build-summary", []string{"REQ-003"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"all-tests"}, []string{"normalize-and-deduplicate"}),
		},
	})
	if _, _, _, err := ParseComplexEngineeringPlanResult(glob, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage); err != nil {
		t.Fatalf("all-tests glob did not cover src/test paths: %v", err)
	}
}

func TestParseComplexPlanReviewRejectsRewrittenTasksAndMismatchedVerdict(t *testing.T) {
	coverage := ComplexCoverage{RequirementIDs: map[string]string{"REQ-001": "MUST"}}
	approved := reviewJSON(t, map[string]any{"verdict": "APPROVED", "reasonCode": "PLAN_ACCEPTABLE", "findings": []any{}})
	if _, _, err := ParseComplexPlanReviewResult(approved, "rev-1", "plan-1", strings.Repeat("e", 64), coverage, []string{"normalize-and-deduplicate"}); err != nil {
		t.Fatalf("valid APPROVED review rejected: %v", err)
	}
	mismatch := reviewJSON(t, map[string]any{"verdict": "APPROVED", "reasonCode": "REPLAN_REQUIRED", "findings": []any{}})
	if _, _, err := ParseComplexPlanReviewResult(mismatch, "rev-1", "plan-1", strings.Repeat("e", 64), coverage, []string{"normalize-and-deduplicate"}); err == nil {
		t.Fatal("mismatched verdict/reason was accepted")
	}
	unknownTask := reviewJSON(t, map[string]any{
		"verdict": "REPLAN", "reasonCode": "REPLAN_REQUIRED",
		"findings": []any{map[string]any{"code": "SCOPE", "message": "too wide", "requirementIds": []string{}, "taskKeys": []string{"rewritten"}}},
	})
	if _, _, err := ParseComplexPlanReviewResult(unknownTask, "rev-1", "plan-1", strings.Repeat("e", 64), coverage, []string{"normalize-and-deduplicate"}); err == nil {
		t.Fatal("review naming a rewritten task was accepted")
	}
}

func compilationJSON(t *testing.T, overlay map[string]any) []byte {
	t.Helper()
	value := map[string]any{
		"schemaVersion": 1, "kind": "REQUIREMENT_COMPILATION",
		"outcome": "CLARIFICATION_REQUIRED",
		"summary": "需求包含邮件规范化、去重和统计输出。",
		"requirements": []any{map[string]any{
			"key": "normalize-email", "priority": "MUST", "text": "把邮箱地址两端空白去除并转换为小写。",
			"acceptanceKeys": []string{"normalize-scenario"},
		}},
		"acceptanceScenarios": []any{map[string]any{"key": "normalize-scenario", "text": "输入带空白和大写字母的邮箱时，输出规范化地址。"}},
		"constraints":         []any{"不访问外部网络。"},
		"nonGoals":            []any{"不发送邮件。"},
		"terms":               []any{map[string]any{"term": "接受数量", "definition": "通过全部输入规则并进入去重集合的记录数。"}},
		"assumptions":         []any{"输入文件使用 UTF-8 编码。"},
		"conflicts":           []any{},
		"blockingQuestions": []any{map[string]any{
			"key": "invalid-address", "text": "格式无效的地址是否计入拒绝数量？",
			"reason": "这会改变统计验收结果。", "requirementKeys": []string{"normalize-email"},
		}},
	}
	for key, item := range overlay {
		value[key] = item
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func planJSON(t *testing.T, overlay map[string]any) []byte {
	t.Helper()
	value := map[string]any{
		"schemaVersion": 1, "kind": "COMPLEX_ENGINEERING_PLAN",
		"technicalApproach": "使用纯函数完成规范化和去重，再生成统计摘要。",
		"tasks": []any{
			planTask("normalize-and-deduplicate", []string{"REQ-001", "REQ-002"}, []string{"ACC-001", "ACC-002"}, []string{"src/email.js", "src/deduplicate.js"}, []string{"email-unit", "deduplicate-unit"}, []string{}),
			planTask("build-summary", []string{"REQ-003"}, []string{"ACC-003"}, []string{"src/summary.js"}, []string{"summary-unit"}, []string{"normalize-and-deduplicate"}),
		},
		"integrationCheckIds": []string{"all-tests"},
		"parallelSuggestion":  map[string]any{"recommendedBuilderCount": 1, "reason": "两个修改集中在同一文件，不适合并行。"},
		"risks":               []string{"统计定义必须沿用已确认验收场景。"},
	}
	for key, item := range overlay {
		value[key] = item
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func planTask(key string, reqs, accs, writes, checks, deps []string) map[string]any {
	return map[string]any{
		"key": key, "title": key, "objective": "cover the mapped requirements",
		"requirementIds": reqs, "acceptanceIds": accs, "writePaths": writes,
		"generatedPaths": []string{}, "sharedPathsRequireApproval": []string{},
		"forbiddenPaths": []string{".git/**"}, "requiredCheckIds": checks, "dependencyKeys": deps,
	}
}

func reviewJSON(t *testing.T, overlay map[string]any) []byte {
	t.Helper()
	value := map[string]any{
		"schemaVersion": 1, "kind": "COMPLEX_PLAN_REVIEW",
		"verdict": "APPROVED", "reasonCode": "PLAN_ACCEPTABLE",
		"summary": "计划覆盖全部 MUST，任务边界和检查与当前方向一致。", "findings": []any{},
	}
	for key, item := range overlay {
		value[key] = item
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
