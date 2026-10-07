package cleardev

import (
	"encoding/json"
	"fmt"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func complexExecutionRequestPrompt(requestID string, version core.RequirementVersion, plan core.ComplexEngineeringPlan, review core.ComplexPlanReview) string {
	payload, _ := json.Marshal(struct {
		SchemaVersion int                         `json:"schemaVersion"`
		Kind          string                      `json:"kind"`
		RequestID     string                      `json:"requestId"`
		Requirement   core.RequirementVersion     `json:"requirement"`
		Plan          core.ComplexEngineeringPlan `json:"plan"`
		Review        core.ComplexPlanReview      `json:"review"`
	}{1, "COMPLEX_EXECUTION_CONTEXT", requestID, version, plan, review})
	return fmt.Sprintf(`你是 ClearDev Project Steward（项目统筹者）。下面是后台从持久事实重建的当前已确认需求版本、工程计划和 APPROVED 审核。你不能修改计划、选择模式或增加任务。

上下文：
%s

只输出一个严格 JSON 对象，不要 Markdown、代码围栏或额外字段。若准确计划现在可以按原样派发，输出：
{"schemaVersion":1,"kind":"COMPLEX_EXECUTION_REQUEST","decision":"DISPATCH","reasonCode":"PLAN_READY","summary":"计划与当前已确认需求版本一致，可以按原样派发。"}

不要在 JSON 中填写 requestId、requirementVersionId、planId 或 planSha256；控制程序会从当前请求绑定这些值。若持久事实仍需要人工决定，decision 使用 NEEDS_HUMAN，reasonCode 必须为 HUMAN_DECISION_REQUIRED。`, payload)
}

func complexQuickTaskRequestPrompt(requestID string, version core.RequirementVersion, plan core.ComplexEngineeringPlan, review core.ComplexPlanReview, source core.ComplexExecutionSnapshot, integrationSHA string) string {
	example, err := core.MarshalAgentChosenResult(complexQuickExampleRequest(requestID, version, source, integrationSHA, plan))
	if err != nil {
		example = []byte("{}")
	}
	payload, _ := json.Marshal(struct {
		SchemaVersion  int                         `json:"schemaVersion"`
		Kind           string                      `json:"kind"`
		RequestID      string                      `json:"requestId"`
		Requirement    core.RequirementVersion     `json:"requirement"`
		Plan           core.ComplexEngineeringPlan `json:"plan"`
		Review         core.ComplexPlanReview      `json:"review"`
		CompletedTasks []core.ComplexExecutionTask `json:"completedTasks"`
		IntegrationSHA string                      `json:"integrationCommitSha"`
	}{1, "COMPLEX_QUICK_TASK_CONTEXT", requestID, version, plan, review, completedComplexExecutionTasks(source), integrationSHA})
	return fmt.Sprintf(`你是 ClearDev Project Steward（项目统筹者）。用户在原会话提出了一个小型后续改动。下面是后台从持久事实重建的当前已确认需求版本、已审核计划、已完成任务和当前集成提交。你不能选择模式、检查命令、候选提交或完成状态，也不能启动工程计划者或 Reviewer。

上下文：
%s

只输出一个严格 JSON 对象，不要 Markdown、代码围栏或额外字段。不要填写 requestId、requirementVersionId、requirementSha256、taskSetVersion 或 integrationCommitSha；控制程序会绑定这些值。sourceTaskKey 必须是一个已完成原任务的 key。若该后续任务只改一个已完成原任务且满足固定低风险规则，输出：
%s

若仍需要人工决定，decision 使用 NEEDS_HUMAN，reasonCode 必须为 HUMAN_DECISION_REQUIRED。`, payload, example)
}

func completedComplexExecutionTasks(source core.ComplexExecutionSnapshot) []core.ComplexExecutionTask {
	out := []core.ComplexExecutionTask{}
	for _, task := range source.Tasks {
		if task.Status == core.DevelopmentTaskStatusDone {
			out = append(out, task)
		}
	}
	return out
}

func complexQuickExampleRequest(requestID string, version core.RequirementVersion, source core.ComplexExecutionSnapshot, integrationSHA string, plan core.ComplexEngineeringPlan) core.ComplexQuickTaskRequestResult {
	parsed, _ := parseComplexExecutionPlan(plan)
	task := core.ComplexPlanTask{}
	for _, item := range parsed.Tasks {
		if item.Key == "deduplicate-email" && complexQuickSourceTaskDone(source, item.Key) {
			task = item
			break
		}
	}
	if task.Key == "" {
		for _, item := range parsed.Tasks {
			if complexQuickSourceTaskDone(source, item.Key) {
				task = item
				break
			}
		}
	}
	if task.Key == "" && len(parsed.Tasks) > 0 {
		task = parsed.Tasks[0]
	}
	testPaths := []string{}
	for _, id := range task.RequiredCheckIDs {
		spec, ok := core.FrozenComplexCheckByID(id)
		if !ok {
			continue
		}
		for _, path := range spec.MainPaths {
			if strings.HasPrefix(path, "test/") && !strings.ContainsAny(path, "*?[") {
				testPaths = append(testPaths, path)
			}
		}
	}
	objective := task.Objective
	if task.Key == "deduplicate-email" {
		objective = "收紧无效邮箱过滤。"
	}
	integrationIDs := append([]string(nil), parsed.IntegrationCheckIDs...)
	if len(integrationIDs) == 0 {
		integrationIDs = []string{"all-tests"}
	}
	return core.ComplexQuickTaskRequestResult{
		SchemaVersion: 1, Kind: core.ComplexQuickTaskRequestKind, RequestID: requestID,
		RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, TaskSetVersion: source.Run.TaskSetVersion,
		IntegrationCommitSHA: integrationSHA, SourceTaskKey: task.Key,
		Decision: core.ComplexQuickDecisionSubmit, ReasonCode: core.ReasonFollowUpReady,
		Summary: objective, Objective: objective,
		RequirementIDs: append([]string(nil), task.RequirementIDs...), AcceptanceIDs: append([]string(nil), task.AcceptanceIDs...),
		WritePaths: append([]string(nil), task.WritePaths...), TestPaths: testPaths,
		GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: append([]string(nil), task.ForbiddenPaths...),
		RequiredCheckIDs: append([]string(nil), task.RequiredCheckIDs...), IntegrationCheckIDs: integrationIDs,
	}
}

func complexExecutionBuilderPrompt(executionPackage []byte, dispatchID, taskID string, round int, feedback string) string {
	feedbackSection := ""
	if feedback != "" {
		feedbackSection = "\n这是唯一允许的返工轮次。先处理下面的持久化失败原因，再重新检查并提交新的候选：\n" + feedback + "\n"
	}
	pkg, err := core.ParseComplexStandardExecutionPackage(executionPackage)
	if err == nil && len(pkg.SharedPathsRequireApproval) > 0 {
		paths, _ := json.Marshal(pkg.SharedPathsRequireApproval)
		return fmt.Sprintf(`你是 ClearDev Builder。只实现下面不可改写的任务执行包。不能扩大路径、改变检查、创建其他 Agent、请求交互式确认，或把文字声明当成测试证据。

执行包：
%s
%s
当前绑定（不要写入 JSON）：
dispatchId=%s
taskId=%s
round=%d

共享路径必须先申请。先不要提交候选。只输出一个严格 JSON 对象，不要 Markdown 或额外字段：
{"schemaVersion":1,"kind":"SCOPE_EXPANSION_REQUEST","requestedPaths":%s,"summary":"需要写入已审核计划中的共享路径。"}

批准后会收到继续消息。候选提交必须包含生成路径的最终文件，且内容必须能由后台固定命令从非生成改动重建。不要发明额外生成文件或命令。`, executionPackage, feedbackSection, dispatchID, taskID, round, paths)
	}
	return fmt.Sprintf(`你是 ClearDev Builder。只实现下面不可改写的任务执行包。不能扩大路径、改变检查、创建其他 Agent、请求交互式确认，或把文字声明当成测试证据。

执行包：
%s
%s
当前绑定（不要写入 JSON）：
dispatchId=%s
taskId=%s
round=%d

在准确受管 worktree 中完成修改并创建一个干净 Git 提交。随后只输出一个严格 JSON 对象，不要 Markdown 或额外字段：
{"schemaVersion":1,"kind":"BUILDER_RESULT","outcome":"CANDIDATE_READY","summary":"已完成任务并提交候选。"}

summary 必须是简要说明，非空且不超过 10000 字；超长会被控制程序判为无效结果。详细过程写在工作与提交里，不要写进 summary。

若无法继续，outcome 只能使用 BLOCKED 或 NEEDS_HUMAN。`, executionPackage, feedbackSection, dispatchID, taskID, round)
}

func complexExceptionScopeDecisionPrompt(request core.ComplexScopeExpansionRequest, version core.RequirementVersion, plan core.ComplexEngineeringPlan) string {
	paths, _ := json.Marshal(request.RequestedPaths)
	return fmt.Sprintf(`你是 ClearDev Project Steward。Builder 请求写入当前已审核计划中的共享路径。你只能批准这些准确路径，不能扩大需求或增加命令。

需求标识：%s
需求哈希：%s
计划标识：%s
计划哈希：%s
请求标识：%s
请求路径：%s

只输出一个严格 JSON 对象。不要填写 requestId、requirementVersionId、requirementSha256、planId 或 planSha256：
{"schemaVersion":1,"kind":"SCOPE_EXPANSION_DECISION","paths":%s,"decision":"APPROVE","reasonCode":"SCOPE_APPROVED","summary":"批准当前计划已列出的共享路径。"}
`, version.ID, version.SHA256, plan.ID, plan.PlanSHA256, request.ID, paths, paths)
}

func complexExceptionBuilderContinuePrompt(executionPackage []byte, dispatchID, taskID string, round int, approved []string) string {
	paths, _ := json.Marshal(approved)
	return fmt.Sprintf(`范围申请已被控制程序接受。现在只实现下面不可改写的任务执行包，并写入已批准路径 %s。候选提交必须包含生成路径的最终文件；package-lock.json 必须能由后台固定命令从当前 package.json 重建：lockfileVersion 为 3，name 和 version 来自 package.json（缺 version 时用 0.0.0），packages[""] 只含 name、version，以及 private（若有）。不要发明额外生成文件或命令。

执行包：
%s

当前绑定（不要写入 JSON）：
dispatchId=%s
taskId=%s
round=%d

在准确受管 worktree 中完成修改并创建一个干净 Git 提交。随后只输出一个严格 JSON 对象：
{"schemaVersion":1,"kind":"BUILDER_RESULT","outcome":"CANDIDATE_READY","summary":"已完成任务并提交候选。"}

summary 必须非空且不超过 10000 字；超长会被判为无效结果。
`, paths, executionPackage, dispatchID, taskID, round)
}

func complexExceptionSpecialistPrompt(run core.ComplexExecutionRun, task core.ComplexExecutionTask, planTask core.ComplexPlanTask) string {
	paths, _ := json.Marshal(append(append(append([]string{}, planTask.WritePaths...), planTask.SharedPathsRequireApproval...), planTask.GeneratedPaths...))
	return fmt.Sprintf(`你是 ClearDev Specialist。在 Builder 改高风险文件前给出约束。不能改需求、计划、路径或命令，也不能修改业务代码。

执行标识：%s
任务标识：%s
需求标识：%s
需求哈希：%s
计划标识：%s
计划哈希：%s
相关路径：%s

只输出一个严格 JSON 对象。不要填写 executionRunId、taskId、requirementVersionId、requirementSha256、planId 或 planSha256：
{"schemaVersion":1,"kind":"SPECIALIST_RESULT","outcome":"PASS","constraints":["不新增依赖。"],"reasonCode":"SPECIALIST_PASS","summary":"可以按当前计划修改列出的依赖清单。"}
`, run.ID, task.ID, run.RequirementVersionID, run.RequirementSHA256, run.PlanID, run.PlanSHA256, paths)
}

func complexExceptionRecoveryPrompt(run core.ComplexExecutionRun, trigger core.ReasonCode, factID string) string {
	return fmt.Sprintf(`你是 ClearDev Recovery。只能根据已结算的失败事实选择固定安全动作。不能改代码、增加返工或设置完成。

执行标识：%s
触发原因：%s
触发事实：%s

只输出一个严格 JSON 对象。不要填写 executionRunId、triggerReason 或 triggerFactId：
{"schemaVersion":1,"kind":"RECOVERY_RESULT","action":"RETRY_SETTLED_INFRA_CHECK","outcome":"PASS","reasonCode":"RECOVERY_SAFE","summary":"重试已结算的基础设施检查。"}
`, run.ID, trigger, factID)
}
