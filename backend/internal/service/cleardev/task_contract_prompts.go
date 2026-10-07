package cleardev

import (
	"encoding/json"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

const taskContractBuilderInstructions = `Planner Task Contract / Builder autonomy:
你一次只负责下面执行包指定的一个 Task。objective、requirementIds、acceptanceIds、工程边界、dependencyTaskKeys/Ids、requiredChecks、reviewCriteria 以及相关 interfaceContracts 共同组成准确合同，不是实现步骤。
你应自行调查具体代码、选择实现方案、制定内部步骤、编码、调试和测试，可以自行分内部阶段。不要把 Planner 的结果要求误当作逐条编辑 recipe，也不要修改其他 Task 的计划或扩大写范围。
逐项满足 reviewCriteria。对 interfaceContracts 中自己提供的接口，保留约定的行为、边界条件和兼容语义；对自己消费的接口，按准确的已验证依赖实现消费，不私自更改约定。接口合同随当前 plan/task/package 哈希冻结，反馈不授权改变合同。证据不足或合同矛盾应如实报告，不能静默重定义接口。`

const taskContractReviewerInstructions = `Planner Task Contract / Task Reviewer acceptance:
本次审核的问题是：这个固定 Candidate 是否正确完成了 Planner 为当前 Task 定义的准确 Task Contract？不是泛泛查看 diff 和检查是否绿色，也不是代替最终 Stage 功能验收。
必须审核 executionPackage 中的 objective、映射的 requirementIds/acceptanceIds、写入与禁止边界、依赖、requiredChecks、每项 reviewCriteria，以及当前 Task 提供或消费的每项 interfaceContracts。核对 planId/planSha256、taskId、executionPackageSha256 和 Candidate SHA，不能沿用另一计划、合同或 Candidate 的旧判断。
根据实际候选代码与测试证据判断对外行为、边界条件、兼容性和保留语义。提供者需要兑现约定接口；消费者需要正确使用已验证依赖的接口。检查通过并不自动证明所有合同条款成立，按已有机制请求必要的批准目录检查，不编造运行证据。
在 summary 中说明合同完成判断及证据缺口；任何不满足的合同结果用现有 findings 和非 PASS verdict 如实报告。不要要求 Builder 遵循某种内部实现步骤，也不要新增产品范围或擅自改变合同。Task PASS 不替代最终 Stage/Requirement Final Reviewer。`

// V1 prompt bytes remain unchanged. Only a frozen V2 execution package opts
// into the additional role-specific obligations.
func executionBuilderPromptForRun(run core.ComplexExecutionRun, pkg []byte, dispatchID, taskID string, round int, feedback string) string {
	prompt := executionBuilderPromptBeforeLockfileRepair(run, pkg, dispatchID, taskID, round, feedback)
	return strings.Replace(prompt, "Planner协调仍受现有范围约束，不能承诺可以自动扩大已批准路径。", "Planner协调仍受范围约束。唯一补全例外：已允许package.json且声明Node/npm依赖的旧约定，可由原Planner追加package-lock.json及原检查覆盖；Builder先报告准确矛盾并等待新执行包，不自行扩大范围。依据修订影响全部任务时，affectedTaskKeys须列出全部准确任务key。", 1)
}

func executionBuilderPromptBeforeLockfileRepair(run core.ComplexExecutionRun, pkg []byte, dispatchID, taskID string, round int, feedback string) string {
	prompt := executionBuilderPromptBeforeEnvironmentGuidance(run, pkg, dispatchID, taskID, round, feedback)
	if _, project, err := core.ProjectContractFromRun(run); err == nil && project {
		return strings.Replace(prompt, projectBuilderReliableDependencies, projectBuilderEnvironmentDependencies(), 1) + projectEnvironmentGuidance
	}
	return prompt
}

func executionBuilderPromptBeforeEnvironmentGuidance(run core.ComplexExecutionRun, pkg []byte, dispatchID, taskID string, round int, feedback string) string {
	prompt := executionBuilderPromptForRunLegacy(run, pkg, dispatchID, taskID, round, feedback)
	if _, project, err := core.ProjectContractFromRun(run); err == nil && project {
		prompt = strings.Replace(prompt, projectBuilderOfflineDependencies, projectBuilderReliableDependencies, 1)
		prompt += core.ProjectPlannerRuntimeBuilderPromptSuffix()
	}
	if core.BuilderFirstFailureRun(run) {
		prompt = builderFirstFailureInstructions + "\n\n" + prompt
	}
	return prompt
}

// Preserve the bytes of already-issued work. Updating dependency instructions
// must never invalidate or resend an existing Builder request.
func executionBuilderPromptForExistingStep(run core.ComplexExecutionRun, pkg []byte, dispatchID, taskID string, round int, feedback, expectedSHA string, historicalFeedback ...string) string {
	prompt := executionBuilderPromptForRun(run, pkg, dispatchID, taskID, round, feedback)
	if coreDigest([]byte(prompt)) == expectedSHA {
		return prompt
	}
	prompt = executionBuilderPromptBeforeLockfileRepair(run, pkg, dispatchID, taskID, round, feedback)
	if coreDigest([]byte(prompt)) == expectedSHA {
		return prompt
	}
	prompt = executionBuilderPromptBeforeEnvironmentGuidance(run, pkg, dispatchID, taskID, round, feedback)
	if coreDigest([]byte(prompt)) == expectedSHA {
		return prompt
	}
	priorProject := strings.TrimSuffix(prompt, core.ProjectPlannerRuntimeBuilderPromptSuffix())
	if coreDigest([]byte(priorProject)) == expectedSHA {
		return priorProject
	}
	withoutFailure := strings.TrimPrefix(priorProject, builderFirstFailureInstructions+"\n\n")
	for _, prior := range []string{withoutFailure, strings.Replace(withoutFailure, projectBuilderReliableDependencies, projectBuilderWritableDependencies, 1), strings.Replace(withoutFailure, projectBuilderReliableDependencies, projectBuilderInstallDependencies, 1)} {
		if coreDigest([]byte(prior)) == expectedSHA {
			return prior
		}
	}
	writable := strings.Replace(strings.TrimSuffix(prompt, core.ProjectPlannerRuntimeBuilderPromptSuffix()), projectBuilderReliableDependencies, projectBuilderWritableDependencies, 1)
	if coreDigest([]byte(writable)) == expectedSHA {
		return writable
	}
	installed := strings.Replace(strings.TrimSuffix(prompt, core.ProjectPlannerRuntimeBuilderPromptSuffix()), projectBuilderReliableDependencies, projectBuilderInstallDependencies, 1)
	if coreDigest([]byte(installed)) == expectedSHA {
		return installed
	}
	legacy := executionBuilderPromptForRunLegacy(run, pkg, dispatchID, taskID, round, feedback)
	if coreDigest([]byte(legacy)) == expectedSHA {
		return legacy
	}
	for _, old := range historicalFeedback {
		if old == feedback {
			continue
		}
		for _, candidate := range []string{
			executionBuilderPromptForRun(run, pkg, dispatchID, taskID, round, old),
			executionBuilderPromptBeforeLockfileRepair(run, pkg, dispatchID, taskID, round, old),
			executionBuilderPromptBeforeEnvironmentGuidance(run, pkg, dispatchID, taskID, round, old),
			strings.Replace(executionBuilderPromptBeforeEnvironmentGuidance(run, pkg, dispatchID, taskID, round, old), projectBuilderReliableDependencies, projectBuilderInstallDependencies, 1),
			strings.Replace(executionBuilderPromptBeforeEnvironmentGuidance(run, pkg, dispatchID, taskID, round, old), projectBuilderReliableDependencies, projectBuilderWritableDependencies, 1),
			executionBuilderPromptForRunLegacy(run, pkg, dispatchID, taskID, round, old),
		} {
			for _, version := range []string{candidate, strings.TrimSuffix(candidate, core.ProjectPlannerRuntimeBuilderPromptSuffix())} {
				if coreDigest([]byte(version)) == expectedSHA {
					return version
				}
			}
		}
	}
	return prompt
}

func executionBuilderPromptForRunLegacy(run core.ComplexExecutionRun, pkg []byte, dispatchID, taskID string, round int, feedback string) string {
	prompt := legacyExecutionBuilderPromptForRun(run, pkg, dispatchID, taskID, round, feedback)
	if isTaskContractPackage(pkg) {
		prompt = taskContractBuilderInstructions + "\n\n" + prompt
	}
	_, project, projectErr := core.ProjectAdmissionContractFromRun(run)
	if enabled, err := core.PlannerRuntimeRun(run); err == nil && enabled && projectErr == nil && !project {
		prompt += core.PlannerRuntimeBuilderPromptSuffix()
	}
	return prompt
}

func reviewerPrompt(packet []byte, assignmentID, candidateID, candidateSHA, packetSHA string) string {
	prompt := legacyReviewerPrompt(packet, assignmentID, candidateID, candidateSHA, packetSHA)
	var envelope struct {
		ExecutionPackage json.RawMessage `json:"executionPackage"`
	}
	if json.Unmarshal(packet, &envelope) == nil && isTaskContractPackage(envelope.ExecutionPackage) {
		if pkg, err := core.ParseComplexStandardExecutionPackage(envelope.ExecutionPackage); err == nil && pkg.ProjectExecution != nil {
			prompt = projectExecutionReviewerInstructions + "\n\n" + prompt
		}
		return taskContractReviewerInstructions + "\n\n" + prompt
	}
	return prompt
}

func isTaskContractPackage(raw []byte) bool {
	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	return json.Unmarshal(raw, &envelope) == nil && (envelope.SchemaVersion == core.PlannerTaskContractVersion || envelope.SchemaVersion == core.ProjectExecutionProtocolVersion)
}

const builderFirstFailureInstructions = `控制程序失败先由Builder诊断：
收到上一轮检查环境错误或最终试用失败时，先核对准确候选、原合同、失败输出和未满足条款，不直接假定Control Plane已修好或用户需要处理。自己的代码、依赖声明、安装方式、测试准备或功能实现有问题时，在原授权任务路径内修复，再报告CANDIDATE_READY，由Control Plane冻结新候选并重新检查和独立验收。
若证据说明是Control Plane缺能力、环境不可用、合同没有可执行的试用方案或必须改变原授权，返回BLOCKED，summary写明调查证据、已排除什么及需要哪个具体能力/决定。不能只因错误类别是INFRA_ERROR就推断自己的代码无问题。真实人类决定用NEEDS_HUMAN。不要声称自己能授予权限，不修改已冻结计划/启动合同，不重复执行已知未结清或执行未知的命令。准备或诊断成功不代表功能通过。`
