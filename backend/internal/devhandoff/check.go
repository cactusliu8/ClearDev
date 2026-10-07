package devhandoff

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkMode(root, mode, stageID string) error {
	roadmapPath := filepath.Join("docs", "cleardev", "development", "roadmap.md")
	roadmapRaw, err := os.ReadFile(filepath.Join(root, roadmapPath))
	if err != nil {
		return fmt.Errorf("无法读取路线图 %s: %w", roadmapPath, err)
	}
	roadmap := parseRoadmap(string(roadmapRaw))
	if stageID == "" {
		stageID = roadmap.Stage
	}
	if stageID == "" {
		return fmt.Errorf("路线图当前阶段缺少阶段编号")
	}
	docs := loadStageDocs(root, stageID)
	plan := parsePlan(docs.Plan)
	execution := parseExecution(docs.Execution)
	acceptance := parseAcceptance(docs.Acceptance)
	git := gitRepo{root: root}
	if mode == ModeAuto || mode == "" {
		mode = resolveAutoMode(git, docs, plan, execution)
	}
	var errs []string
	errs = append(errs, checkShared(git, docs, roadmap, plan, mode)...)
	switch mode {
	case ModePlan:
		errs = append(errs, checkPlanStart(git, docs, roadmap, plan)...)
	case ModeCandidate:
		errs = append(errs, checkCandidate(git, docs, roadmap, plan, execution)...)
	case ModeAccept:
		errs = append(errs, checkAccept(git, docs, roadmap, plan, execution, acceptance, false)...)
	case ModeClose:
		errs = append(errs, checkClose(git, docs, roadmap, plan, execution, acceptance)...)
	default:
		return fmt.Errorf("未知交接模式 %q，应为 plan、candidate、accept、close 或 auto", mode)
	}
	return fmtErrs(errs)
}

func loadStageDocs(root, id string) stageDocs {
	dir := filepath.Join("docs", "cleardev", "development", "stages", id)
	docs := stageDocs{ID: id, Roadmap: filepath.Join("docs", "cleardev", "development", "roadmap.md")}
	planPath := filepath.Join(dir, "plan.md")
	execPath := filepath.Join(dir, "execution.md")
	acceptPath := filepath.Join(dir, "acceptance.md")
	if raw, err := os.ReadFile(filepath.Join(root, planPath)); err == nil {
		docs.Plan, docs.PlanExists = string(raw), true
	}
	if raw, err := os.ReadFile(filepath.Join(root, execPath)); err == nil {
		docs.Execution, docs.ExecExists = string(raw), true
	}
	if raw, err := os.ReadFile(filepath.Join(root, acceptPath)); err == nil {
		docs.Acceptance, docs.AcceptExists = string(raw), true
	}
	return docs
}

func rel(id, name string) string {
	return filepath.ToSlash(filepath.Join("docs", "cleardev", "development", "stages", id, name))
}

func checkShared(git gitRepo, docs stageDocs, roadmap parsedRoadmap, plan parsedPlan, mode string) []string {
	var errs []string
	if porcelain, err := git.porcelain(); err != nil {
		errs = append(errs, "无法读取 Git 状态: "+err.Error())
	} else if porcelain != "" {
		errs = append(errs, "工作区还有未提交修改或未跟踪文件。阶段交接检查要求相关文档已提交且工作区干净。")
	}
	if !git.tracked(docs.Roadmap) {
		errs = append(errs, "路线图未被 Git 跟踪: "+docs.Roadmap)
	}
	if !docs.PlanExists {
		errs = append(errs, fmt.Sprintf("缺少当前阶段计划 %s", rel(docs.ID, "plan.md")))
	} else if !git.tracked(rel(docs.ID, "plan.md")) {
		errs = append(errs, "当前阶段计划未被 Git 跟踪: "+rel(docs.ID, "plan.md"))
	}
	if mode != ModeClose && roadmap.Stage != docs.ID {
		errs = append(errs, fmt.Sprintf("路线图阶段是 %s，目录是 %s", roadmap.Stage, docs.ID))
	}
	if docs.PlanExists && plan.Status == "" {
		errs = append(errs, "计划缺少当前状态")
	}
	if docs.PlanExists && plan.Version == "" {
		errs = append(errs, "计划缺少计划版本")
	}
	if docs.PlanExists && plan.StartSHA == "" {
		errs = append(errs, "计划缺少起始 Git 提交")
	}
	if roadmap.Status == "" {
		errs = append(errs, "路线图当前阶段缺少状态")
	}
	if roadmap.Version == "" {
		errs = append(errs, "路线图当前阶段缺少计划版本")
	}
	if roadmap.PlanLink == "" {
		errs = append(errs, "路线图当前阶段缺少计划链接")
	}
	if mode != ModeClose && roadmap.PlanLink != "" && !strings.Contains(roadmap.PlanLink, docs.ID+"/plan.md") {
		errs = append(errs, fmt.Sprintf("路线图计划链接是 %s，与当前阶段 %s 不一致", roadmap.PlanLink, docs.ID))
	}
	if mode != ModeClose && roadmap.Version != "" && plan.Version != "" && roadmap.Version != plan.Version {
		errs = append(errs, fmt.Sprintf("路线图版本是 %s，计划版本是 %s", roadmap.Version, plan.Version))
	}
	return errs
}

func checkPlanStart(git gitRepo, docs stageDocs, roadmap parsedRoadmap, plan parsedPlan) []string {
	var errs []string
	if plan.Status != "可以开发" {
		errs = append(errs, fmt.Sprintf("计划开始检查要求计划状态为可以开发，当前是 %s", plan.Status))
	}
	if stageStatus := statusToken(roadmap.Status); stageStatus != "" && stageStatus != "可以开发" {
		errs = append(errs, fmt.Sprintf("计划开始检查要求路线图状态为可以开发，当前是 %s", roadmap.Status))
	}
	if plan.StartSHA != "" && !git.commitExists(plan.StartSHA) {
		errs = append(errs, "计划起始提交不存在: "+plan.StartSHA)
	} else if plan.StartSHA != "" {
		head, err := git.head()
		if err != nil {
			errs = append(errs, "无法读取 HEAD: "+err.Error())
		} else if !git.isAncestor(plan.StartSHA, head) {
			errs = append(errs, "计划起始提交不是当前 HEAD 的祖先")
		}
	}
	if docs.ExecExists {
		execution := parseExecution(docs.Execution)
		if execution.Status == "待验收" && isCandidateDelivery(git, docs, execution) {
			errs = append(errs, "计划开始时不应已有待验收的开发记录")
		}
	}
	return errs
}

func checkCandidate(git gitRepo, docs stageDocs, roadmap parsedRoadmap, plan parsedPlan, execution parsedExecution) []string {
	var errs []string
	if !docs.ExecExists {
		errs = append(errs, "候选交付检查缺少 execution.md")
	} else if !git.tracked(rel(docs.ID, "execution.md")) {
		errs = append(errs, "开发记录未被 Git 跟踪: "+rel(docs.ID, "execution.md"))
	}
	errs = append(errs, checkExecutionFields(plan, execution)...)
	if plan.Status != "可以开发" {
		errs = append(errs, fmt.Sprintf("候选交付时计划应保持可以开发，当前是 %s", plan.Status))
	}
	errs = append(errs, checkPlanCommit(git, docs, execution)...)
	if execution.CandidateSHA == "" {
		errs = append(errs, "开发记录缺少候选 Git 提交")
	} else if !git.commitExists(execution.CandidateSHA) {
		errs = append(errs, "候选提交不存在: "+execution.CandidateSHA)
	} else {
		head, err := git.head()
		if err != nil {
			errs = append(errs, "无法读取 HEAD: "+err.Error())
		} else if execution.CandidateSHA != head {
			if !git.isAncestor(execution.CandidateSHA, head) {
				errs = append(errs, "候选提交不是当前 HEAD 的祖先")
			} else if files, err := git.changedFiles(execution.CandidateSHA, head); err != nil {
				errs = append(errs, "无法比较候选提交与 HEAD: "+err.Error())
			} else {
				for _, file := range files {
					if file != rel(docs.ID, "execution.md") {
						errs = append(errs, "候选之后只允许提交开发记录，还改了 "+file)
					}
				}
			}
		}
		if plan.StartSHA != "" && git.commitExists(plan.StartSHA) && !git.isAncestor(plan.StartSHA, execution.CandidateSHA) {
			errs = append(errs, "计划起始提交不是候选提交的祖先")
		}
	}
	if stageStatus := statusToken(roadmap.Status); stageStatus != "" && stageStatus != "可以开发" && stageStatus != "待验收" {
		errs = append(errs, fmt.Sprintf("路线图状态与候选交付不一致: %s", roadmap.Status))
	}
	return errs
}

func checkAccept(git gitRepo, docs stageDocs, roadmap parsedRoadmap, plan parsedPlan, execution parsedExecution, acceptance parsedAcceptance, closing bool) []string {
	var errs []string
	if !docs.AcceptExists {
		errs = append(errs, "验收检查缺少 acceptance.md")
	} else if !git.tracked(rel(docs.ID, "acceptance.md")) {
		errs = append(errs, "验收记录未被 Git 跟踪: "+rel(docs.ID, "acceptance.md"))
	}
	if !docs.ExecExists {
		errs = append(errs, "验收检查缺少 execution.md")
	}
	errs = append(errs, checkExecutionFields(plan, execution)...)
	if !allowedConclusion(acceptance.Conclusion) {
		errs = append(errs, fmt.Sprintf("验收结论无效: %q", acceptance.Conclusion))
	}
	if !closing {
		if isStaleAcceptance(git, docs) {
			errs = append(errs, "验收记录早于当前返工改动，不能作为当前验收")
		}
		if plan.Status != "可以开发" {
			errs = append(errs, fmt.Sprintf("验收时计划应保持可以开发，当前是 %s", plan.Status))
		}
		stageStatus := statusToken(roadmap.Status)
		if stageStatus != "可以开发" && stageStatus != "待验收" {
			errs = append(errs, fmt.Sprintf("路线图状态与验收不一致: %s", roadmap.Status))
		}
	}
	if acceptance.PlanVersion == "" {
		errs = append(errs, "验收记录缺少计划版本")
	} else if plan.Version != "" && acceptance.PlanVersion != plan.Version {
		errs = append(errs, "验收记录的计划版本与计划不一致")
	}
	if acceptance.StartSHA == "" {
		errs = append(errs, "验收记录缺少计划起始提交")
	} else if plan.StartSHA != "" && acceptance.StartSHA != plan.StartSHA {
		errs = append(errs, "验收记录的计划起始提交与计划不一致")
	}
	if !closing {
		errs = append(errs, checkPlanCommit(git, docs, execution)...)
	}
	if acceptance.CandidateSHA == "" || execution.CandidateSHA == "" {
		errs = append(errs, "验收必须绑定准确候选提交")
	} else if acceptance.CandidateSHA != execution.CandidateSHA {
		errs = append(errs, "验收记录的候选提交与开发记录不一致")
	} else if !git.commitExists(acceptance.CandidateSHA) {
		errs = append(errs, "验收绑定的候选提交不存在: "+acceptance.CandidateSHA)
	}
	if acceptance.ExecutionSHA == "" {
		errs = append(errs, "验收必须绑定开发记录提交")
	} else if !git.commitExists(acceptance.ExecutionSHA) {
		errs = append(errs, "开发记录提交不存在: "+acceptance.ExecutionSHA)
	} else {
		errs = append(errs, checkExecutionCommit(git, docs, execution, acceptance.ExecutionSHA)...)
	}
	return errs
}

func checkExecutionFields(plan parsedPlan, execution parsedExecution) []string {
	var errs []string
	if execution.Status == "" {
		errs = append(errs, "开发记录缺少当前状态")
	} else if execution.Status != "待验收" {
		errs = append(errs, fmt.Sprintf("开发记录状态应为待验收，当前是 %s", execution.Status))
	}
	if execution.PlanVersion == "" {
		errs = append(errs, "开发记录缺少使用计划版本")
	} else if plan.Version != "" && execution.PlanVersion != plan.Version {
		errs = append(errs, fmt.Sprintf("开发记录计划版本是 %s，计划是 %s", execution.PlanVersion, plan.Version))
	}
	if execution.StartSHA == "" {
		errs = append(errs, "开发记录缺少计划起始提交")
	} else if plan.StartSHA != "" && execution.StartSHA != plan.StartSHA {
		errs = append(errs, "开发记录的计划起始提交与计划不一致")
	}
	return errs
}

func checkPlanCommit(git gitRepo, docs stageDocs, execution parsedExecution) []string {
	var errs []string
	planPath := rel(docs.ID, "plan.md")
	roadmapPath := filepath.ToSlash(docs.Roadmap)
	if execution.PlanCommitSHA == "" {
		return []string{"开发记录缺少计划提交"}
	}
	if !git.commitExists(execution.PlanCommitSHA) {
		return []string{"计划提交不存在: " + execution.PlanCommitSHA}
	}
	if execution.CandidateSHA != "" && git.commitExists(execution.CandidateSHA) && !git.isAncestor(execution.PlanCommitSHA, execution.CandidateSHA) {
		errs = append(errs, "计划提交不是候选提交的祖先")
	}
	changed, err := git.commitFiles(execution.PlanCommitSHA)
	if err != nil {
		errs = append(errs, "无法读取计划提交文件: "+err.Error())
	} else if !containsFile(changed, planPath) {
		errs = append(errs, "计划提交没有改当前阶段计划")
	}
	frozen, err := git.fileAt(execution.PlanCommitSHA, planPath)
	if err != nil {
		errs = append(errs, "计划提交中没有当前阶段计划")
	} else if frozen != docs.Plan {
		errs = append(errs, "计划提交没有包含当前冻结计划")
	}
	frozenRoadmap, err := git.fileAt(execution.PlanCommitSHA, roadmapPath)
	if err != nil {
		errs = append(errs, "计划提交中没有路线图")
	} else if currentRoadmap, readErr := os.ReadFile(filepath.Join(git.root, docs.Roadmap)); readErr != nil {
		errs = append(errs, "无法读取当前路线图: "+readErr.Error())
	} else if frozenRoadmap != string(currentRoadmap) {
		errs = append(errs, "计划提交没有包含当前冻结路线图")
	}
	if execution.CandidateSHA != "" && git.commitExists(execution.CandidateSHA) {
		files, err := git.touchedFiles(execution.PlanCommitSHA, execution.CandidateSHA)
		if err != nil {
			errs = append(errs, "无法比较计划提交与候选提交: "+err.Error())
		} else {
			if containsFile(files, planPath) {
				errs = append(errs, "候选修改了冻结计划")
			}
			if containsFile(files, roadmapPath) {
				errs = append(errs, "候选修改了冻结路线图")
			}
		}
	}
	return errs
}

func checkExecutionCommit(git gitRepo, docs stageDocs, execution parsedExecution, executionSHA string) []string {
	var errs []string
	execPath := rel(docs.ID, "execution.md")
	got, err := git.fileAt(executionSHA, execPath)
	if err != nil {
		errs = append(errs, "开发记录提交没有当前开发记录")
	} else if got != docs.Execution {
		errs = append(errs, "开发记录提交不是当前 execution.md 的准确提交")
	}
	if execution.CandidateSHA == "" || !git.commitExists(execution.CandidateSHA) {
		return errs
	}
	if executionSHA == execution.CandidateSHA {
		errs = append(errs, "开发记录提交不能与候选提交相同")
		return errs
	}
	if !git.isAncestor(execution.CandidateSHA, executionSHA) {
		errs = append(errs, "候选提交不是开发记录提交的祖先")
		return errs
	}
	head, err := git.head()
	if err != nil {
		errs = append(errs, "无法读取 HEAD: "+err.Error())
		return errs
	}
	if !git.isAncestor(executionSHA, head) {
		errs = append(errs, "开发记录提交不是当前 HEAD 的祖先")
		return errs
	}
	files, err := git.changedFiles(execution.CandidateSHA, executionSHA)
	if err != nil {
		errs = append(errs, "无法比较候选提交与开发记录提交: "+err.Error())
		return errs
	}
	if !containsFile(files, execPath) {
		errs = append(errs, "开发记录提交没有改 execution.md")
	}
	for _, file := range files {
		if file != execPath {
			errs = append(errs, "开发记录提交相对候选还改了 "+file)
		}
	}
	return errs
}

func checkClose(git gitRepo, docs stageDocs, roadmap parsedRoadmap, plan parsedPlan, execution parsedExecution, acceptance parsedAcceptance) []string {
	errs := checkAccept(git, docs, roadmap, plan, execution, acceptance, true)
	if plan.Status != "已完成" {
		errs = append(errs, fmt.Sprintf("阶段关闭要求计划状态为已完成，当前是 %s", plan.Status))
	}
	if acceptance.Conclusion != "通过" {
		errs = append(errs, "阶段关闭要求验收结论为通过")
	}
	if plan.CandidateSHA == "" {
		errs = append(errs, "阶段关闭要求计划写明候选提交")
	} else {
		if !git.commitExists(plan.CandidateSHA) {
			errs = append(errs, "计划中的候选提交不存在: "+plan.CandidateSHA)
		}
		if execution.CandidateSHA != "" && plan.CandidateSHA != execution.CandidateSHA {
			errs = append(errs, "计划中的候选提交与开发记录不一致")
		}
		if acceptance.CandidateSHA != "" && plan.CandidateSHA != acceptance.CandidateSHA {
			errs = append(errs, "计划中的候选提交与验收记录不一致")
		}
	}
	nextID := roadmap.Stage
	if nextID == "" || nextID == docs.ID {
		errs = append(errs, "阶段关闭后路线图当前阶段应进入下一阶段，不能停在 "+docs.ID)
	}
	if stageStatus := statusToken(roadmap.Status); stageStatus != "可以开发" {
		errs = append(errs, fmt.Sprintf("阶段关闭后路线图当前阶段应进入可以开发，当前是 %s", roadmap.Status))
	}
	nextPlanPath := ""
	if nextID != "" && nextID != docs.ID {
		nextDocs := loadStageDocs(git.root, nextID)
		nextPlan := parsePlan(nextDocs.Plan)
		nextPlanPath = rel(nextID, "plan.md")
		if !nextDocs.PlanExists {
			errs = append(errs, "缺少下一阶段计划 "+nextPlanPath)
		} else if !git.tracked(nextPlanPath) {
			errs = append(errs, "下一阶段计划未被 Git 跟踪: "+nextPlanPath)
		} else if nextPlan.Status != "可以开发" {
			errs = append(errs, fmt.Sprintf("下一阶段计划状态应为可以开发，当前是 %s", nextPlan.Status))
		}
		if roadmap.PlanLink == "" || !strings.Contains(roadmap.PlanLink, nextID+"/plan.md") {
			errs = append(errs, fmt.Sprintf("路线图计划链接是 %s，与下一阶段 %s 不一致", roadmap.PlanLink, nextID))
		}
		if roadmap.Version != "" && nextPlan.Version != "" && roadmap.Version != nextPlan.Version {
			errs = append(errs, fmt.Sprintf("路线图版本是 %s，下一阶段计划版本是 %s", roadmap.Version, nextPlan.Version))
		}
	}
	head, err := git.head()
	if err != nil {
		errs = append(errs, "无法读取 HEAD: "+err.Error())
		return errs
	}
	files, err := git.commitFiles(head)
	if err != nil {
		errs = append(errs, "无法读取关闭提交文件: "+err.Error())
		return errs
	}
	acceptPath := rel(docs.ID, "acceptance.md")
	if containsFile(files, acceptPath) {
		errs = append(errs, "关闭提交不能新建或修改 acceptance.md；验收记录必须先单独提交")
	}
	acceptCommit, err := git.lastCommitForFile(acceptPath)
	if err != nil || acceptCommit == "" {
		errs = append(errs, "无法读取验收记录提交")
	} else if acceptCommit == head || !git.isAncestor(acceptCommit, head) {
		errs = append(errs, "验收记录提交必须是关闭提交的严格祖先")
	}
	required := []string{rel(docs.ID, "plan.md"), filepath.ToSlash(docs.Roadmap)}
	if nextPlanPath != "" {
		required = append(required, nextPlanPath)
	}
	for _, path := range required {
		if !containsFile(files, path) {
			errs = append(errs, "关闭提交缺少 "+path)
		}
	}
	return errs
}

func isCandidateDelivery(git gitRepo, docs stageDocs, execution parsedExecution) bool {
	if execution.Status != "待验收" || execution.CandidateSHA == "" || !git.commitExists(execution.CandidateSHA) {
		return false
	}
	head, err := git.head()
	if err != nil {
		return false
	}
	if execution.CandidateSHA == head {
		return true
	}
	if !git.isAncestor(execution.CandidateSHA, head) {
		return false
	}
	files, err := git.changedFiles(execution.CandidateSHA, head)
	if err != nil {
		return false
	}
	for _, file := range files {
		if file != rel(docs.ID, "execution.md") {
			return false
		}
	}
	return true
}

func resolveAutoMode(git gitRepo, docs stageDocs, plan parsedPlan, execution parsedExecution) string {
	acceptExists := docs.AcceptExists && !isStaleAcceptance(git, docs)
	mode := detectMode(plan, docs.ExecExists, execution, acceptExists)
	if mode == ModeCandidate && !isCandidateDelivery(git, docs, execution) {
		return ModePlan
	}
	return mode
}

func isStaleAcceptance(git gitRepo, docs stageDocs) bool {
	if !docs.AcceptExists {
		return false
	}
	acceptCommit, err := git.lastCommitForFile(rel(docs.ID, "acceptance.md"))
	if err != nil || acceptCommit == "" {
		return false
	}
	head, err := git.head()
	if err != nil || acceptCommit == head || !git.isAncestor(acceptCommit, head) {
		return false
	}
	// An empty candidate is a new delivery only when its exact binding follows
	// the previous acceptance and a separate current execution commit follows it.
	execution := parseExecution(docs.Execution)
	acceptance := parseAcceptance(docs.Acceptance)
	execCommit, execErr := git.lastCommitForFile(rel(docs.ID, "execution.md"))
	if execErr == nil && execution.CandidateSHA != acceptance.CandidateSHA &&
		execution.CandidateSHA != acceptCommit && execution.CandidateSHA != execCommit &&
		git.isAncestor(acceptCommit, execution.CandidateSHA) &&
		git.isAncestor(execution.CandidateSHA, execCommit) &&
		git.isAncestor(execCommit, head) && isCandidateDelivery(git, docs, execution) {
		return true
	}
	files, err := git.touchedFiles(acceptCommit, head)
	if err != nil {
		return false
	}
	for _, file := range files {
		if file != rel(docs.ID, "execution.md") {
			return true
		}
	}
	return false
}

func containsFile(files []string, name string) bool {
	for _, file := range files {
		if file == name {
			return true
		}
	}
	return false
}
