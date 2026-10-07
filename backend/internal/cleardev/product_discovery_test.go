package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func productDiscoveryFixture() ProductDiscoveryResult {
	return ProductDiscoveryResult{SchemaVersion: 1, Kind: "PRODUCT_DISCOVERY", Outcome: "READY", Message: "先交付可查看的本地功能，再讨论账号接入。", FeasibilitySummary: "当前项目提供本地联系人页面；外部账号接入超出当前执行权限。", Questions: []ProductQuestion{},
		Features: []ProductFeature{{Key: "search", Title: "搜索联系人", Description: "用户能按邮箱查找联系人"}, {Key: "accounts", Title: "连接真实邮箱", Description: "用户连接自己的邮箱账号"}},
		Stages: []ProductStageDefinition{
			{Key: "local", Title: "本地搜索", Goal: "用户能在已保存的联系人中搜索", FeatureKeys: []string{"search"}, AcceptanceCriteria: []string{"输入邮箱子串时展示匹配联系人。", "清空查询时恢复全部联系人。"}, NonGoals: []string{"本阶段不连接外部邮箱。"}, Feasibility: "SUPPORTED", FeasibilityReason: "已有 frontend/app.js 与本地联系人接口"},
			{Key: "remote", Title: "账号连接", Goal: "连接真实邮箱", FeatureKeys: []string{"accounts"}, AcceptanceCriteria: []string{"用户能连接自己的邮箱账号。"}, NonGoals: []string{}, Feasibility: "NEEDS_CAPABILITY", FeasibilityReason: "当前不允许新增认证或外部服务"},
		}}
}

func TestProductDiscoveryProtocolKeepsProductNotEngineeringChoices(t *testing.T) {
	original := productDiscoveryFixture()
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseProductDiscoveryResult(raw)
	if err != nil || len(parsed.Stages) != 2 || parsed.Stages[1].Feasibility != "NEEDS_CAPABILITY" {
		t.Fatalf("valid proposal: %+v, %v", parsed, err)
	}
	for name, mutate := range map[string]func(*ProductDiscoveryResult){
		"orphan-feature":  func(r *ProductDiscoveryResult) { r.Stages = r.Stages[:1] },
		"shared-feature":  func(r *ProductDiscoveryResult) { r.Stages[1].FeatureKeys = []string{"search"} },
		"missing-feature": func(r *ProductDiscoveryResult) { r.Stages[1].FeatureKeys = []string{"invented"} },
		"duplicate-stage": func(r *ProductDiscoveryResult) { r.Stages[1].Key = r.Stages[0].Key },
		"ready-with-question": func(r *ProductDiscoveryResult) {
			r.Questions = []ProductQuestion{{Key: "q", Text: "Who?", Reason: "Scope"}}
		},
		"no-functional-acceptance": func(r *ProductDiscoveryResult) { r.Stages[0].AcceptanceCriteria = []string{} },
		"unsafe-feasibility":       func(r *ProductDiscoveryResult) { r.Stages[0].Feasibility = "APPROVED" },
		"self-completion":          func(r *ProductDiscoveryResult) { r.Outcome = "COMPLETED" },
		"missing-list":             func(r *ProductDiscoveryResult) { r.Stages[0].NonGoals = nil },
	} {
		t.Run(name, func(t *testing.T) {
			r := productDiscoveryFixture()
			mutate(&r)
			data, _ := json.Marshal(r)
			if _, err := ParseProductDiscoveryResult(data); err == nil {
				t.Fatal("invalid proposal was accepted")
			}
		})
	}
	for _, extra := range []string{`,"builderCount":2`, `,"approve":true`, `,"tasks":[]`, `,"outcome":"READY"`} {
		if _, err := ParseProductDiscoveryResult([]byte(strings.TrimSuffix(string(raw), "}") + extra + "}")); err == nil {
			t.Fatalf("accepted unknown or duplicate field %s", extra)
		}
	}
}

func TestProductDiscoveryDiscussionIsNotForcedByOrdinal(t *testing.T) {
	r := productDiscoveryFixture()
	r.Outcome, r.Features, r.Stages = "DISCUSS", []ProductFeature{}, []ProductStageDefinition{}
	r.Questions = []ProductQuestion{{Key: "audience", Text: "个人使用还是团队协作？", Reason: "影响权限与第一阶段范围"}}
	raw, _ := json.Marshal(r)
	if _, err := ParseProductDiscoveryResult(raw); err != nil {
		t.Fatal(err)
	}
	r.Questions = []ProductQuestion{}
	raw, _ = json.Marshal(r)
	if _, err := ParseProductDiscoveryResult(raw); err == nil {
		t.Fatal("DISCUSS without a genuine question was accepted")
	}
}

func TestProductStageCompilationPreservesFunctionalAcceptanceAndLegacyProtocol(t *testing.T) {
	stage := productDiscoveryFixture().Stages[0]
	r := RequirementCompilationResult{SchemaVersion: 1, Kind: "REQUIREMENT_COMPILATION", Outcome: "READY", Summary: stage.Goal,
		Requirements:        []CompilationRequirement{{Key: "search", Priority: "MUST", Text: stage.Goal, AcceptanceKeys: []string{"filter", "clear"}}},
		AcceptanceScenarios: []CompilationAcceptance{{Key: "filter", Text: stage.AcceptanceCriteria[0]}, {Key: "clear", Text: stage.AcceptanceCriteria[1]}},
		NonGoals:            stage.NonGoals, Constraints: []string{}, Terms: []CompilationTerm{}, Assumptions: []string{}, Conflicts: []CompilationConflict{}, BlockingQuestions: []CompilationBlockingQuestion{}}
	raw, err := MarshalAgentChosenResult(r)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	if _, err := ParseProductStageCompilationResult(raw, "request", "version", hash, 0, stage); err != nil {
		t.Fatalf("prepared stage should permit immediate READY: %v", err)
	}
	if _, err := ParseRequirementCompilationResult(raw, "request", "version", hash, 0, false); err == nil {
		t.Fatal("legacy first-round clarification contract was silently changed")
	}
	r.AcceptanceScenarios[0].Text = "Run tests."
	raw, _ = MarshalAgentChosenResult(r)
	if _, err := ParseProductStageCompilationResult(raw, "request", "version", hash, 0, stage); err == nil {
		t.Fatal("technical tests replaced functional acceptance")
	}
	r.AcceptanceScenarios[0].Text = stage.AcceptanceCriteria[0]
	r.NonGoals = []string{}
	raw, _ = MarshalAgentChosenResult(r)
	if _, err := ParseProductStageCompilationResult(raw, "request", "version", hash, 0, stage); err == nil {
		t.Fatal("stage non-goals were dropped")
	}
}
