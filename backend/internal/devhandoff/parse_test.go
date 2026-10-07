package devhandoff

import "testing"

func TestParseStageDocuments(t *testing.T) {
	plan := parsePlan("# S12A7\n\n- 计划版本：v1\n- 当前状态：可以开发\n- 起始 Git（版本控制工具）提交：`aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`\n")
	if plan.Version != "v1" || plan.Status != "可以开发" || plan.StartSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("%#v", plan)
	}
	execution := parseExecution("- 当前状态：待验收\n- 使用计划版本：v1\n- 计划提交：`cccccccccccccccccccccccccccccccccccccccc`\n- 计划起始 Git 提交：`aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`\n- 候选 Git 提交：`bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb`\n")
	if execution.Status != "待验收" || execution.PlanCommitSHA != "cccccccccccccccccccccccccccccccccccccccc" || execution.StartSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || execution.CandidateSHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("%#v", execution)
	}
	acceptance := parseAcceptance("# 验收结论：通过\n\n- 计划版本：v1\n- 计划起始提交：`aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`\n- 本轮候选：`bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb`\n- 开发记录提交：`cccccccccccccccccccccccccccccccccccccccc`\n")
	if acceptance.Conclusion != "通过" || acceptance.CandidateSHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || acceptance.ExecutionSHA != "cccccccccccccccccccccccccccccccccccccccc" {
		t.Fatalf("%#v", acceptance)
	}
	roadmap := parseRoadmap("## 当前阶段\n\n- 阶段：S12A7 开发基线\n- 状态：可以开发（v1）\n- 阶段计划：[S12A7 计划](stages/S12A7/plan.md)\n\n## 总体路线\n")
	if roadmap.Stage != "S12A7" || statusToken(roadmap.Status) != "可以开发" || roadmap.Version != "v1" || roadmap.PlanLink != "stages/S12A7/plan.md" {
		t.Fatalf("%#v", roadmap)
	}
}

func TestDetectMode(t *testing.T) {
	planOpen := parsedPlan{Status: "可以开发"}
	execPending := parsedExecution{Status: "待验收"}
	if got := detectMode(planOpen, false, parsedExecution{}, false); got != ModePlan {
		t.Fatalf("got %s", got)
	}
	if got := detectMode(planOpen, true, execPending, false); got != ModeCandidate {
		t.Fatalf("got %s", got)
	}
	if got := detectMode(planOpen, true, execPending, true); got != ModeAccept {
		t.Fatalf("got %s", got)
	}
	if got := detectMode(parsedPlan{Status: "已完成"}, true, execPending, true); got != ModeClose {
		t.Fatalf("got %s", got)
	}
}
