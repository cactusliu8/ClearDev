package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProductPlanBindingIsExact(t *testing.T) {
	b := ProductPlanBinding{ProductID: "product", DiscussionID: "discussion", ResultSHA256: strings.Repeat("a", 64), SelectionSHA256: strings.Repeat("b", 64)}
	raw, _ := json.Marshal(b)
	got, e := ParseProductPlanBinding(raw)
	if e != nil || got != b {
		t.Fatal(e)
	}
	for _, s := range []string{`{}`, string(raw[:len(raw)-1]) + `,"unknown":true}`, strings.Replace(string(raw), strings.Repeat("a", 64), "fake", 1)} {
		if _, e = ParseProductPlanBinding([]byte(s)); e == nil {
			t.Fatal("invalid grant binding accepted")
		}
	}
}
func TestProductPlanScopeRejectsChangedAcceptance(t *testing.T) {
	stage := ProductStageDefinition{Goal: "保存笔记", AcceptanceCriteria: []string{"保存后可读取"}, NonGoals: []string{"不联网"}, ExecutionBasis: &ProjectExecutionBasis{}}
	doc := NormalizedRequirementDocument{AcceptanceScenarios: []NormalizedAcceptance{{ID: "a", Text: "保存后可读取"}}, Requirements: []NormalizedRequirement{{ID: "r", Text: "保存笔记", Priority: "MUST", AcceptanceIDs: []string{"a"}}}, NonGoals: []string{"不联网"}, Constraints: []string{ProjectStageBasisConstraint(stage)}}
	if !ProductPlanCoversDocument(stage, doc) {
		t.Fatal("original scope rejected")
	}
	for _, change := range []func(*NormalizedRequirementDocument){
		func(d *NormalizedRequirementDocument) {
			d.AcceptanceScenarios = append(d.AcceptanceScenarios, NormalizedAcceptance{ID: "b", Text: "额外功能"})
		},
		func(d *NormalizedRequirementDocument) { d.NonGoals = nil },
		func(d *NormalizedRequirementDocument) {
			d.Requirements = []NormalizedRequirement{{ID: "r", Text: "增加账号登录", Priority: "MUST", AcceptanceIDs: []string{"a"}}}
		},
		func(d *NormalizedRequirementDocument) { d.Constraints = nil },
		func(d *NormalizedRequirementDocument) {
			d.Requirements = []NormalizedRequirement{{ID: "r", Text: "保存笔记", Priority: "MUST", AcceptanceIDs: []string{"outside"}}}
		},
	} {
		modified := doc
		change(&modified)
		if ProductPlanCoversDocument(stage, modified) {
			t.Fatal("scope change accepted automatically")
		}
	}
}
