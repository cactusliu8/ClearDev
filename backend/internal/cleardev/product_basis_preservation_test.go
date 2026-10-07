package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

// The trial contract puts acceptance criteria verbatim into the execution
// basis, so the required constraint text can contain "<" and ">". Go renders
// those as \u003c/\u003e while a reviewer's copy renders them literally; the
// same basis must still count as preserved, and any content change must fail.
func TestProductBasisConstraintPreservedAcceptsEscapingOnlyDifferences(t *testing.T) {
	basis := ProjectStageBasisConstraint(ProductStageDefinition{
		Key:                "align",
		AcceptanceCriteria: []string{"输出恰好三行，依次为 total: <n>、done: <n>、overdue: <n>"},
		ExecutionBasis: &ProjectExecutionBasis{
			WritePaths: []string{"src/**"},
			Checks:     []ProjectCheckSpec{{ID: "project-tests", Argv: []string{"npm", "test"}, TimeoutSeconds: 120, MainPaths: []string{"src/**"}}},
			Trial: &ProjectTrial{SchemaVersion: 1, Steps: []ProjectTrialStep{{
				ID: "three-lines", Kind: "COMMAND", Argv: []string{"node", "src/taskstat.mjs"}, TimeoutSeconds: 30,
				AcceptanceCriteria: []string{"输出恰好三行，依次为 total: <n>、done: <n>、overdue: <n>"},
				Observe:            "核对三行输出与默认样例",
			}}},
		},
	})
	if !strings.Contains(basis, `\u003cn\u003e`) {
		t.Fatalf("期望约束串包含 Go 的 HTML 转义，实际: %q", basis)
	}
	unescaped := strings.ReplaceAll(strings.ReplaceAll(basis, `\u003c`, "<"), `\u003e`, ">")
	if unescaped == basis {
		t.Fatal("测试前提不成立：替换未产生差异")
	}
	if !ProductBasisConstraintPreserved(unescaped, basis) {
		t.Fatal("仅转义不同的同一依据被误判为改变")
	}
	if !ProductBasisConstraintPreserved(basis, basis) {
		t.Fatal("逐字相同的依据应通过")
	}
	// 内容变化必须仍然被拒绝：删字段、改值、加字段。
	var decoded map[string]any
	_, payload, _ := strings.Cut(basis, "：")
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	withoutTrial := map[string]any{}
	for k, v := range decoded {
		if k != "trial" {
			withoutTrial[k] = v
		}
	}
	dropped, _ := json.Marshal(withoutTrial)
	if ProductBasisConstraintPreserved("项目执行依据（仅规划，不授权运行命令）："+string(dropped), basis) {
		t.Fatal("丢失 trial 的依据被误判为保留")
	}
	decoded["dependencyNeeds"] = []string{"npm:left-pad"}
	added, _ := json.Marshal(decoded)
	if ProductBasisConstraintPreserved("项目执行依据（仅规划，不授权运行命令）："+string(added), basis) {
		t.Fatal("新增依赖的依据被误判为保留")
	}
	if ProductBasisConstraintPreserved("项目执行依据（仅规划，不授权运行命令）：not json", basis) {
		t.Fatal("非法 JSON 不应通过")
	}
	if ProductBasisConstraintPreserved("完全不同的说明文字", basis) {
		t.Fatal("无关文本不应通过")
	}
}
