package devhandoff

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanStartSucceedsWhenFrozenAndClean(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "plan")

	if err := checkMode(repo, ModePlan, ""); err != nil {
		t.Fatal(err)
	}
	if err := checkMode(repo, ModeAuto, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPlanStartRejectsUncommittedPlan(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "roadmap")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")

	err := checkMode(repo, ModePlan, "")
	if err == nil || !strings.Contains(err.Error(), "未被 Git 跟踪") && !strings.Contains(err.Error(), "未提交") {
		t.Fatalf("expected untracked or dirty plan, got %v", err)
	}
}

func TestPlanStartRejectsMissingRoadmapFields(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeFile(t, repo, "docs/cleardev/development/roadmap.md", "# 路线\n\n## 当前阶段\n\n- 阶段：S12A7 标题\n\n## 总体路线\n")
	commitAll(t, repo, "plan without roadmap fields")

	err := checkMode(repo, ModePlan, "")
	if err == nil {
		t.Fatal("expected missing roadmap fields to fail")
	}
	for _, field := range []string{"缺少状态", "缺少计划版本", "缺少计划链接"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("expected %q error, got %v", field, err)
		}
	}
}

func TestPlanStartRejectsMissingStartSHA(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	plan := strings.Replace(planDoc("可以开发", "v1", start, ""), "- 起始 Git（版本控制工具）提交：`"+start+"`\n", "", 1)
	writeStage(t, repo, "S12A7", plan, "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "plan without start")

	err := checkMode(repo, ModePlan, "")
	if err == nil || !strings.Contains(err.Error(), "计划缺少起始 Git 提交") {
		t.Fatalf("expected missing plan start, got %v", err)
	}
}

func TestCandidateRejectsMismatchedSHA(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, "ffffffffffffffffffffffffffffffffffffffff"), "")
	commitAll(t, repo, "execution")
	_ = candidate

	err := checkMode(repo, ModeCandidate, "")
	if err == nil || !strings.Contains(err.Error(), "候选提交不存在") {
		t.Fatalf("expected missing candidate, got %v", err)
	}
}

func TestCandidateSucceedsWhenBound(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	commitAll(t, repo, "execution")

	if err := checkMode(repo, ModeCandidate, ""); err != nil {
		t.Fatal(err)
	}
	if err := checkMode(repo, ModeAuto, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateRejectsWrongPlanCommit(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, candidate, candidate), "")
	commitAll(t, repo, "execution")

	err := checkMode(repo, ModeCandidate, "")
	if err == nil || !strings.Contains(err.Error(), "计划提交") {
		t.Fatalf("expected wrong plan commit, got %v", err)
	}
}

func TestCandidateRejectsMissingExecutionPlanVersion(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate")
	execution := strings.Replace(executionDoc("待验收", "v1", start, planCommit, candidate), "- 使用计划版本：v1\n", "", 1)
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), execution, "")
	commitAll(t, repo, "execution without plan version")

	err := checkMode(repo, ModeCandidate, "")
	if err == nil || !strings.Contains(err.Error(), "缺少使用计划版本") {
		t.Fatalf("expected missing execution plan version, got %v", err)
	}
}

func TestCandidateRejectsFrozenPlanChangedInCandidate(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, "")+"\n额外改动\n", "", "")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A7", "", executionDoc("待验收", "v1", start, planCommit, candidate), "")
	commitAll(t, repo, "execution")

	err := checkMode(repo, ModeCandidate, "")
	if err == nil || !strings.Contains(err.Error(), "冻结计划") {
		t.Fatalf("expected frozen plan change, got %v", err)
	}
}

func TestCandidateRejectsFrozenRoadmapChangedInCandidate(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeRoadmap(t, repo, "S12A7", "待验收（v1）")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate with roadmap change")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	commitAll(t, repo, "execution")

	err := checkMode(repo, ModeCandidate, "")
	if err == nil || !strings.Contains(err.Error(), "冻结路线图") {
		t.Fatalf("expected frozen roadmap change, got %v", err)
	}
}

func TestCandidateRejectsFrozenRoadmapTouchedThenRestored(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeRoadmap(t, repo, "S12A7", "待验收（v1）")
	commitAll(t, repo, "touch roadmap")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate restores roadmap")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	commitAll(t, repo, "execution")

	err := checkMode(repo, ModeCandidate, "")
	if err == nil || !strings.Contains(err.Error(), "冻结路线图") {
		t.Fatalf("expected restored frozen roadmap change, got %v", err)
	}
}

func TestCandidateAcceptsRoadmapCommittedBeforePlan(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "roadmap before plan")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	planCommit := commitAll(t, repo, "plan only")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	commitAll(t, repo, "execution")

	if err := checkMode(repo, ModeCandidate, ""); err != nil {
		t.Fatalf("roadmap committed before plan should be accepted: %v", err)
	}
}

func TestCandidateRejectsExtraFilesAfterCandidate(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	writeFile(t, repo, "backend/extra.go", "package extra\n")
	commitAll(t, repo, "execution and extra")

	err := checkMode(repo, ModeCandidate, "")
	if err == nil || !strings.Contains(err.Error(), "只允许提交开发记录") {
		t.Fatalf("expected extra-file error, got %v", err)
	}
}

func TestAutoUsesPlanWhenPendingExecutionIsStale(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	commitAll(t, repo, "execution")
	writeFile(t, repo, "backend/rework.go", "package rework\n")
	commitAll(t, repo, "rework candidate")

	if err := checkMode(repo, ModeAuto, ""); err != nil {
		t.Fatal(err)
	}
	err := checkMode(repo, ModeCandidate, "")
	if err == nil || !strings.Contains(err.Error(), "只允许提交开发记录") {
		t.Fatalf("expected explicit candidate mode to reject stale delivery, got %v", err)
	}
}

func TestAutoUsesPlanForCandidateAfterCommittedReworkAcceptance(t *testing.T) {
	repo := newHandoffRepo(t)
	start, planCommit, oldCandidate, execSHA := freezeCandidate(t, repo)
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, oldCandidate), acceptanceDoc("需要返工", "v1", start, oldCandidate, execSHA))
	commitAll(t, repo, "rework acceptance")
	writeFile(t, repo, "backend/rework.go", "package rework\n")
	commitAll(t, repo, "rework candidate")

	docs := loadStageDocs(repo, "S12A7")
	mode := resolveAutoMode(gitRepo{root: repo}, docs, parsePlan(docs.Plan), parseExecution(docs.Execution))
	if mode != ModePlan {
		t.Fatalf("expected auto mode to use plan for an unbound rework candidate, got %s", mode)
	}
	if err := checkMode(repo, ModeAuto, ""); err != nil {
		t.Fatalf("auto plan check should pass: %v", err)
	}
	err := checkMode(repo, ModeAccept, "")
	if err == nil || !strings.Contains(err.Error(), "验收记录早于当前返工改动") {
		t.Fatalf("explicit acceptance should reject stale review: %v", err)
	}
}

func TestAutoUsesCandidateAfterReworkExecutionIsCommitted(t *testing.T) {
	repo := newHandoffRepo(t)
	start, planCommit, oldCandidate, execSHA := freezeCandidate(t, repo)
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, oldCandidate), acceptanceDoc("需要返工", "v1", start, oldCandidate, execSHA))
	commitAll(t, repo, "rework acceptance")
	writeFile(t, repo, "backend/rework.go", "package rework\n")
	candidate := commitAll(t, repo, "rework candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	commitAll(t, repo, "rework execution")

	docs := loadStageDocs(repo, "S12A7")
	mode := resolveAutoMode(gitRepo{root: repo}, docs, parsePlan(docs.Plan), parseExecution(docs.Execution))
	if mode != ModeCandidate {
		t.Fatalf("expected auto mode to use candidate after rework execution, got %s", mode)
	}
	if err := checkMode(repo, ModeAuto, ""); err != nil {
		t.Fatalf("auto candidate check should pass: %v", err)
	}
	err := checkMode(repo, ModeAccept, "")
	if err == nil || !strings.Contains(err.Error(), "验收记录早于当前返工改动") {
		t.Fatalf("explicit acceptance should still reject stale review: %v", err)
	}
}

func TestAutoIgnoresOldRecordsAfterPlanUpgrade(t *testing.T) {
	repo := newHandoffRepo(t)
	oldStart, oldPlanCommit, oldCandidate, oldExecSHA := freezeCandidate(t, repo)
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", oldStart, ""), executionDoc("待验收", "v1", oldStart, oldPlanCommit, oldCandidate), acceptanceDoc("需要返工", "v1", oldStart, oldCandidate, oldExecSHA))
	commitAll(t, repo, "v1 rework acceptance")

	v3Start := git(t, repo, "rev-parse", "HEAD")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v3", v3Start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v3）")
	v3PlanCommit := commitAll(t, repo, "v3 plan")

	docs := loadStageDocs(repo, "S12A7")
	mode := resolveAutoMode(gitRepo{root: repo}, docs, parsePlan(docs.Plan), parseExecution(docs.Execution))
	if mode != ModePlan {
		t.Fatalf("expected auto mode to ignore old records after a plan upgrade, got %s", mode)
	}
	if err := checkMode(repo, ModeAuto, ""); err != nil {
		t.Fatalf("auto plan check should pass after a plan upgrade: %v", err)
	}
	if err := checkMode(repo, ModeAccept, ""); err == nil || !strings.Contains(err.Error(), "验收记录早于当前返工改动") {
		t.Fatalf("explicit acceptance should reject the old review: %v", err)
	}

	writeFile(t, repo, "backend/v3.go", "package v3\n")
	v3Candidate := commitAll(t, repo, "v3 candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v3", v3Start, ""), executionDoc("待验收", "v3", v3Start, v3PlanCommit, v3Candidate), "")
	commitAll(t, repo, "v3 execution")

	docs = loadStageDocs(repo, "S12A7")
	mode = resolveAutoMode(gitRepo{root: repo}, docs, parsePlan(docs.Plan), parseExecution(docs.Execution))
	if mode != ModeCandidate {
		t.Fatalf("expected auto mode to use the v3 candidate, got %s", mode)
	}
	if err := checkMode(repo, ModeAuto, ""); err != nil {
		t.Fatalf("auto candidate check should pass: %v", err)
	}
}

func TestAcceptRejectsUntrackedAcceptance(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))

	err := checkMode(repo, ModeAccept, "")
	if err == nil || !strings.Contains(err.Error(), "验收记录未被 Git 跟踪") && !strings.Contains(err.Error(), "未提交") {
		t.Fatalf("expected untracked acceptance, got %v", err)
	}
}

func TestAcceptSucceedsWhenBound(t *testing.T) {
	repo := newHandoffRepo(t)
	start, planCommit, candidate, execSHA := freezeCandidate(t, repo)
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))
	commitAll(t, repo, "acceptance")

	if err := checkMode(repo, ModeAccept, ""); err != nil {
		t.Fatal(err)
	}
	if err := checkMode(repo, ModeAuto, ""); err != nil {
		t.Fatalf("auto acceptance check should pass: %v", err)
	}
}

func TestLocalFlowSucceedsWithoutRemotes(t *testing.T) {
	repo := newHandoffRepo(t)
	if remotes := git(t, repo, "remote"); remotes != "" {
		t.Fatalf("temporary repository unexpectedly has remotes: %s", remotes)
	}

	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v3", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v3）")
	planCommit := commitAll(t, repo, "plan")
	if err := checkMode(repo, ModePlan, ""); err != nil {
		t.Fatalf("local plan check failed: %v", err)
	}

	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v3", start, ""), executionDoc("待验收", "v3", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	if err := checkMode(repo, ModeCandidate, ""); err != nil {
		t.Fatalf("local candidate check failed: %v", err)
	}

	writeStage(t, repo, "S12A7", planDoc("可以开发", "v3", start, ""), executionDoc("待验收", "v3", start, planCommit, candidate), acceptanceDoc("通过", "v3", start, candidate, execSHA))
	commitAll(t, repo, "acceptance")
	if err := checkMode(repo, ModeAccept, ""); err != nil {
		t.Fatalf("local acceptance check failed: %v", err)
	}
	if remotes := git(t, repo, "remote"); remotes != "" {
		t.Fatalf("handoff checks changed remotes: %s", remotes)
	}
}

func TestAcceptRejectsWrongExecutionCommit(t *testing.T) {
	repo := newHandoffRepo(t)
	start, planCommit, candidate, execSHA := freezeCandidate(t, repo)
	_ = execSHA
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, candidate))
	commitAll(t, repo, "acceptance")

	err := checkMode(repo, ModeAccept, "")
	if err == nil || !strings.Contains(err.Error(), "开发记录提交") {
		t.Fatalf("expected wrong execution commit, got %v", err)
	}
}

func TestAcceptRejectsMissingAcceptancePlanFields(t *testing.T) {
	repo := newHandoffRepo(t)
	start, planCommit, candidate, execSHA := freezeCandidate(t, repo)
	acceptance := acceptanceDoc("通过", "v1", start, candidate, execSHA)
	acceptance = strings.Replace(acceptance, "- 计划版本：v1\n", "", 1)
	acceptance = strings.Replace(acceptance, "- 计划起始提交：`"+start+"`\n", "", 1)
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), acceptance)
	commitAll(t, repo, "acceptance without plan fields")

	err := checkMode(repo, ModeAccept, "")
	if err == nil {
		t.Fatal("expected missing acceptance plan fields to fail")
	}
	for _, field := range []string{"验收记录缺少计划版本", "验收记录缺少计划起始提交"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("expected %q error, got %v", field, err)
		}
	}
}

func TestAcceptRejectsMissingExecutionFields(t *testing.T) {
	repo := newHandoffRepo(t)
	start, planCommit, candidate, execSHA := freezeCandidate(t, repo)
	execution := executionDoc("待验收", "v1", start, planCommit, candidate)
	execution = strings.Replace(execution, "- 当前状态：待验收\n", "", 1)
	execution = strings.Replace(execution, "- 使用计划版本：v1\n", "", 1)
	execution = strings.Replace(execution, "- 计划起始 Git 提交：`"+start+"`\n", "", 1)
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), execution, acceptanceDoc("通过", "v1", start, candidate, execSHA))
	commitAll(t, repo, "acceptance with incomplete execution")

	err := checkMode(repo, ModeAccept, "")
	if err == nil {
		t.Fatal("expected missing execution fields to fail acceptance")
	}
	for _, field := range []string{"开发记录缺少当前状态", "开发记录缺少使用计划版本", "开发记录缺少计划起始提交"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("expected %q error, got %v", field, err)
		}
	}
}

func TestAcceptRejectsWrongPlanOrRoadmapStatus(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		planStatus string
		roadStatus string
		want       string
	}{
		{name: "plan", planStatus: "已完成", roadStatus: "可以开发（v1）", want: "验收时计划应保持可以开发"},
		{name: "roadmap", planStatus: "可以开发", roadStatus: "已完成（v1）", want: "路线图状态与验收不一致"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newHandoffRepo(t)
			start, planCommit, candidate, execSHA := freezeCandidate(t, repo)
			writeStage(t, repo, "S12A7", planDoc(testCase.planStatus, "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))
			writeRoadmap(t, repo, "S12A7", testCase.roadStatus)
			commitAll(t, repo, "acceptance with wrong status")

			err := checkMode(repo, ModeAccept, "")
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("expected %q error, got %v", testCase.want, err)
			}
		})
	}
}

func TestAcceptRejectsOffBranchExecutionCommit(t *testing.T) {
	repo := newHandoffRepo(t)
	start, planCommit, candidate, execSHA := freezeCandidate(t, repo)
	body := executionDoc("待验收", "v1", start, planCommit, candidate)
	git(t, repo, "checkout", "-B", "other", candidate)
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), body, "")
	otherExec := commitAll(t, repo, "other-execution")
	git(t, repo, "checkout", "main")
	if otherExec == execSHA {
		t.Fatal("expected a different execution commit on the other branch")
	}
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), body, acceptanceDoc("通过", "v1", start, candidate, otherExec))
	commitAll(t, repo, "acceptance")

	err := checkMode(repo, ModeAccept, "")
	if err == nil || !strings.Contains(err.Error(), "当前 HEAD 的祖先") {
		t.Fatalf("expected off-branch execution commit, got %v", err)
	}
}

func TestRoadmapWrongStatusIsRejected(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "已完成")
	commitAll(t, repo, "mismatch")

	err := checkMode(repo, ModePlan, "")
	if err == nil || !strings.Contains(err.Error(), "路线图状态") {
		t.Fatalf("expected roadmap status error, got %v", err)
	}
}

func TestCloseSucceedsAfterHandoffCommit(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	oldCandidate := commitAll(t, repo, "old-candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, oldCandidate), "")
	execSHA := commitAll(t, repo, "old-execution")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, oldCandidate), acceptanceDoc("通过", "v1", start, oldCandidate, execSHA))
	acceptanceSHA := commitAll(t, repo, "acceptance")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, oldCandidate), executionDoc("待验收", "v1", start, planCommit, oldCandidate), acceptanceDoc("通过", "v1", start, oldCandidate, execSHA))
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", acceptanceSHA, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "close")

	if err := checkMode(repo, ModeClose, "S12A6"); err != nil {
		t.Fatal(err)
	}
}

func TestCloseRejectsAcceptanceFirstAddedByCloseCommit(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, candidate), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", execSHA, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "close adds acceptance")

	err := checkMode(repo, ModeClose, "S12A6")
	if err == nil || !strings.Contains(err.Error(), "关闭提交不能新建或修改 acceptance.md") {
		t.Fatalf("expected close-added acceptance to fail, got %v", err)
	}
}

func TestCloseRejectsAcceptanceRewrittenByCloseCommit(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	acceptance := acceptanceDoc("通过", "v1", start, candidate, execSHA)
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), acceptance)
	acceptanceSHA := commitAll(t, repo, "acceptance")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, candidate), executionDoc("待验收", "v1", start, planCommit, candidate), acceptance+"\n关闭时改写。\n")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", acceptanceSHA, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "close rewrites acceptance")

	err := checkMode(repo, ModeClose, "S12A6")
	if err == nil || !strings.Contains(err.Error(), "关闭提交不能新建或修改 acceptance.md") {
		t.Fatalf("expected rewritten acceptance to fail, got %v", err)
	}
}

func TestCloseSucceedsWithUnchangedAncestorAcceptance(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))
	acceptanceSHA := commitAll(t, repo, "acceptance")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, candidate), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", acceptanceSHA, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "close without rewriting acceptance")

	if err := checkMode(repo, ModeClose, "S12A6"); err != nil {
		t.Fatal(err)
	}
	head, err := gitRepo{root: repo}.head()
	if err != nil {
		t.Fatal(err)
	}
	files, err := gitRepo{root: repo}.commitFiles(head)
	if err != nil {
		t.Fatal(err)
	}
	if containsFile(files, "docs/cleardev/development/stages/S12A6/acceptance.md") {
		t.Fatalf("close commit unexpectedly rewrote acceptance: %#v", files)
	}
}

func TestCloseRejectsAncestorAcceptanceForOldCandidate(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	oldCandidate := commitAll(t, repo, "old candidate")
	writeFile(t, repo, "backend/old.go", "package old\n\nvar current = true\n")
	candidate := commitAll(t, repo, "current candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, oldCandidate, execSHA))
	acceptanceSHA := commitAll(t, repo, "old candidate acceptance")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, candidate), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, oldCandidate, execSHA))
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", acceptanceSHA, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "close with old candidate acceptance")

	err := checkMode(repo, ModeClose, "S12A6")
	if err == nil || !strings.Contains(err.Error(), "验收记录的候选提交与开发记录不一致") {
		t.Fatalf("expected old candidate acceptance to fail, got %v", err)
	}
}

func TestCloseRejectsUncommittedAcceptance(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, candidate), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", execSHA, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "close without acceptance")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, candidate), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))

	err := checkMode(repo, ModeClose, "S12A6")
	if err == nil || (!strings.Contains(err.Error(), "未被 Git 跟踪") && !strings.Contains(err.Error(), "未提交修改")) {
		t.Fatalf("expected uncommitted acceptance to fail, got %v", err)
	}
}

func TestCloseRejectsStaleCompletedRoadmap(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, candidate), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))
	writeRoadmap(t, repo, "S12A6", "已完成")
	commitAll(t, repo, "close without next stage")

	err := checkMode(repo, ModeClose, "S12A6")
	if err == nil || !strings.Contains(err.Error(), "下一阶段") {
		t.Fatalf("expected close to reject stale roadmap, got %v", err)
	}
}

func TestCloseRejectsWrongNextPlanLink(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, candidate), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", execSHA, ""), "", "")
	writeFile(t, repo, "docs/cleardev/development/roadmap.md", "# 路线\n\n## 当前阶段\n\n- 阶段：S12A7 标题\n- 状态：可以开发（v1）\n- 阶段计划：[S12A6 计划](stages/S12A6/plan.md)\n\n## 总体路线\n")
	commitAll(t, repo, "close with stale plan link")

	err := checkMode(repo, ModeClose, "S12A6")
	if err == nil || !strings.Contains(err.Error(), "链接") {
		t.Fatalf("expected close to reject wrong next plan link, got %v", err)
	}
}

func TestCloseRejectsWrongNextPlanVersion(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, candidate), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", execSHA, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v2）")
	commitAll(t, repo, "close with version mismatch")

	err := checkMode(repo, ModeClose, "S12A6")
	if err == nil || !strings.Contains(err.Error(), "版本") {
		t.Fatalf("expected close to reject next plan version mismatch, got %v", err)
	}
}

func TestCloseRejectsMissingPlanCandidate(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", execSHA, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "close without plan candidate")

	err := checkMode(repo, ModeClose, "S12A6")
	if err == nil || !strings.Contains(err.Error(), "计划写明候选提交") {
		t.Fatalf("expected missing plan candidate to fail, got %v", err)
	}
}

func TestCloseRejectsMismatchedPlanCandidate(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A6", "可以开发（v1）")
	planCommit := commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/old.go", "package old\n")
	candidate := commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A6", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA := commitAll(t, repo, "execution")
	writeStage(t, repo, "S12A6", planDoc("已完成", "v1", start, start), executionDoc("待验收", "v1", start, planCommit, candidate), acceptanceDoc("通过", "v1", start, candidate, execSHA))
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", execSHA, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "close with mismatched plan candidate")

	err := checkMode(repo, ModeClose, "S12A6")
	if err == nil || !strings.Contains(err.Error(), "计划中的候选提交与开发记录不一致") {
		t.Fatalf("expected mismatched plan candidate to fail, got %v", err)
	}
}

func TestMainPrintsHandoffOK(t *testing.T) {
	repo := newHandoffRepo(t)
	start := commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	commitAll(t, repo, "plan")

	var stdout, stderr bytes.Buffer
	if err := Main([]string{"-mode=plan", "-root", repo}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestMainToolchainRejectsWrongGo(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "docs/cleardev/development/toolchain.json", `{
  "go": "1.25.7",
  "node": "22.23.1",
  "npm": "10.9.8",
  "golangciLint": "2.12.2",
  "goImage": "cleardev-go-validation:1.25.7"
}
`)
	bin := t.TempDir()
	writeFile(t, bin, "go", "#!/bin/sh\necho 'go version go1.27.0 linux/amd64'\n")
	if err := os.Chmod(filepath.Join(bin, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Main([]string{"-mode=toolchain", "-require-go", "-root", repo}, ioDiscard(), ioDiscard())
	if err == nil || !strings.Contains(err.Error(), "1.27.0") || !strings.Contains(err.Error(), "1.25.7") {
		t.Fatalf("expected go version failure, got %v", err)
	}
}

func ioDiscard() *bytes.Buffer { return &bytes.Buffer{} }

func newHandoffRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "handoff-test")
	git(t, root, "config", "commit.gpgsign", "false")
	git(t, root, "config", "core.hooksPath", "/dev/null")
	writeFile(t, root, "README.md", "test\n")
	return root
}

func freezeCandidate(t *testing.T, repo string) (start, planCommit, candidate, execSHA string) {
	t.Helper()
	start = commitAll(t, repo, "start")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), "", "")
	writeRoadmap(t, repo, "S12A7", "可以开发（v1）")
	planCommit = commitAll(t, repo, "plan")
	writeFile(t, repo, "backend/feature.go", "package feature\n")
	candidate = commitAll(t, repo, "candidate")
	writeStage(t, repo, "S12A7", planDoc("可以开发", "v1", start, ""), executionDoc("待验收", "v1", start, planCommit, candidate), "")
	execSHA = commitAll(t, repo, "execution")
	return start, planCommit, candidate, execSHA
}

func writeRoadmap(t *testing.T, root, stage, status string) {
	t.Helper()
	writeFile(t, root, "docs/cleardev/development/roadmap.md", "# 路线\n\n## 当前阶段\n\n- 阶段："+stage+" 标题\n- 状态："+status+"\n- 阶段计划：["+stage+" 计划](stages/"+stage+"/plan.md)\n\n## 总体路线\n")
}

func writeStage(t *testing.T, root, id, plan, execution, acceptance string) {
	t.Helper()
	if plan != "" {
		writeFile(t, root, "docs/cleardev/development/stages/"+id+"/plan.md", plan)
	}
	if execution != "" {
		writeFile(t, root, "docs/cleardev/development/stages/"+id+"/execution.md", execution)
	}
	if acceptance != "" {
		writeFile(t, root, "docs/cleardev/development/stages/"+id+"/acceptance.md", acceptance)
	}
}

func planDoc(status, version, start, candidate string) string {
	out := "# 计划\n\n- 计划版本：" + version + "\n- 当前状态：" + status + "\n- 起始 Git（版本控制工具）提交：`" + start + "`\n"
	if candidate != "" {
		out += "- 候选提交：`" + candidate + "`\n"
	}
	return out
}

func executionDoc(status, version, start, planCommit, candidate string) string {
	out := "# 开发记录\n\n- 当前状态：" + status + "\n- 使用计划版本：" + version + "\n"
	if planCommit != "" {
		out += "- 计划提交：`" + planCommit + "`\n"
	}
	out += "- 计划起始 Git 提交：`" + start + "`\n- 候选 Git 提交：`" + candidate + "`\n"
	return out
}

func acceptanceDoc(conclusion, version, start, candidate, execution string) string {
	return "# 验收结论：" + conclusion + "\n\n- 计划版本：" + version + "\n- 计划起始提交：`" + start + "`\n- 本轮候选：`" + candidate + "`\n- 开发记录提交：`" + execution + "`\n"
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, root, message string) string {
	t.Helper()
	git(t, root, "add", "-A")
	git(t, root, "commit", "--quiet", "-m", message)
	return git(t, root, "rev-parse", "HEAD")
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=handoff-test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=handoff-test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s", strings.Join(args, " "), out)
	}
	return strings.TrimSpace(string(out))
}

func TestAutoResumedEmptyCandidate(t *testing.T) {
	repo := newHandoffRepo(t)
	start, planCommit, oldCandidate, oldExec := freezeCandidate(t, repo)
	writeStage(t, repo, "S12A7", "", "", acceptanceDoc("阻塞", "v1", start, oldCandidate, oldExec))
	commitAll(t, repo, "blocked acceptance")
	g := gitRepo{root: repo}
	if _, err := g.run("commit", "--allow-empty", "-m", "empty resumed candidate"); err != nil {
		t.Fatal(err)
	}
	candidate, err := g.head()
	if err != nil {
		t.Fatal(err)
	}
	writeStage(t, repo, "S12A7", "", executionDoc("待验收", "v1", start, planCommit, candidate), "")
	currentExec := commitAll(t, repo, "resumed execution")
	for _, mode := range []string{ModeCandidate, ModeAuto} {
		if err := checkMode(repo, mode, ""); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	if err := checkMode(repo, ModeAccept, ""); err == nil {
		t.Fatal("old acceptance accepted")
	}
	writeStage(t, repo, "S12A7", "", "", acceptanceDoc("通过", "v1", start, candidate, currentExec))
	commitAll(t, repo, "current acceptance")
	for _, mode := range []string{ModeAuto, ModeAccept} {
		if err := checkMode(repo, mode, ""); err != nil {
			t.Fatalf("new acceptance %s: %v", mode, err)
		}
	}
}

func TestAutoOldDeliveryRecordIsNotNewCandidate(t *testing.T) {
	repo := newHandoffRepo(t)
	start, planCommit, candidate, oldExec := freezeCandidate(t, repo)
	writeStage(t, repo, "S12A7", "", "", acceptanceDoc("阻塞", "v1", start, candidate, oldExec))
	commitAll(t, repo, "blocked acceptance")
	writeStage(t, repo, "S12A7", "", executionDoc("待验收", "v1", start, planCommit, candidate)+"\n补充同一旧交付。\n", "")
	commitAll(t, repo, "old record supplement")
	docs := loadStageDocs(repo, "S12A7")
	if mode := resolveAutoMode(gitRepo{root: repo}, docs, parsePlan(docs.Plan), parseExecution(docs.Execution)); mode != ModeAccept {
		t.Fatalf("old candidate promoted: %s", mode)
	}
	if err := checkMode(repo, ModeAuto, ""); err == nil {
		t.Fatal("stale execution binding accepted")
	}
}
