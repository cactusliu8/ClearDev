package cleardev

import (
	"context"
	"fmt"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func (s *Service) productStageSource(ctx context.Context, requirementID string) (core.ProductStage, bool, error) {
	if store, ok := s.complex.(ProductFactStore); ok {
		return store.GetClearDevProductStageByRequirement(ctx, requirementID)
	}
	return core.ProductStage{}, false, nil
}

func productStageCompilationPrompt(stage core.ProductStage, planning core.ComplexPlanningSnapshot, round int) string {
	definition := stage.Definition
	criteria := make([]core.CompilationAcceptance, 0, len(definition.AcceptanceCriteria))
	keys := make([]string, 0, len(definition.AcceptanceCriteria))
	for i, text := range definition.AcceptanceCriteria {
		key := fmt.Sprintf("stage-outcome-%d", i+1)
		criteria = append(criteria, core.CompilationAcceptance{Key: key, Text: text})
		keys = append(keys, key)
	}
	example := core.RequirementCompilationResult{
		SchemaVersion: 1, Kind: "REQUIREMENT_COMPILATION", Outcome: "READY", Summary: definition.Goal,
		Requirements:        []core.CompilationRequirement{{Key: "stage-goal", Priority: "MUST", Text: definition.Goal, AcceptanceKeys: keys}},
		AcceptanceScenarios: criteria, NonGoals: definition.NonGoals, Constraints: []string{}, Terms: []core.CompilationTerm{},
		Assumptions: []string{}, Conflicts: []core.CompilationConflict{}, BlockingQuestions: []core.CompilationBlockingQuestion{},
	}
	if basis := core.ProjectStageBasisConstraint(definition); basis != "" {
		example.Constraints = []string{basis}
	}
	prompt := fmt.Sprintf(`你是 ClearDev Steward。这个阶段已在产品探索中讨论过，现在把准确阶段定义转成待真人确认的执行规格。不要重新探索整个产品，不要重复已回答的问题，不要输出任务拆分或技术实现步骤。

不可改写的阶段定义：
%s

阶段来源 SHA-256：%s
所选开发基线：%s
阶段已有问答：
%s

当前规格编译回合：%d
不强制首轮问问题；没有真实阻塞疑问时直接 READY。有必要的产品问题时返回 CLARIFICATION_REQUIRED；第1回合再次提问需要 additionalRoundReason；最多第2回合，仍未明确就 NEEDS_HUMAN。不要把从代码能查到的事实变成用户作业。
阶段的每条 acceptanceCriteria 必须原文保留在 acceptanceScenarios.text，并被至少一个 MUST 引用。每条 nonGoals 必须原文保留。不得把后续阶段、整个产品功能或技术计划加进当前阶段。
此输出不是批准；现有 Electron Human Authority 的真人确认仍是唯一开工授权。
只输出严格 JSON，无 Markdown、无绑定 ID/SHA、无未知字段。所有文本字段非空且不超过 10000 字；超出上限会被控制程序判为无效结果。下面是基于真实阶段的最小 READY 结构；可以细化 MUST、解释约束或提出确实需要的澄清，但不能改变功能验收和非目标：
%s`, mustComplexPromptJSON(definition), stage.DefinitionSHA256, stage.BaseCommitSHA,
		mustComplexPromptJSON(core.CompilationContextFromSnapshot(planning, planning.Requirement.TargetRequirementVersionID)), round, mustAgentChosenPromptJSON(example))
	if definition.ExecutionBasis != nil {
		prompt += "\n这是通用项目阶段，不是邮箱模板。示例中 constraints 的项目执行依据必须原文保留；依赖、检查和启动方式只是经确认后供 Planner 使用的依据，当前不得执行命令或派发 Builder。控制程序绑定的用户选择：\n" + mustComplexPromptJSON(stage.Selection)
	}
	return prompt
}
