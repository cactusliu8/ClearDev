package cleardev

import (
	"context"
	"errors"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type executionChoiceWriter interface {
	SetClearDevProjectExecution(context.Context, string, domain.ClearDevExecutionConfig) error
}

// SetProjectExecution changes only the project's controlled execution selection.
// It never starts a role, changes native/global config, or grants authority.
func (s *Service) SetProjectExecution(ctx context.Context, projectID string, choice domain.ClearDevExecutionConfig) (*ExecutionChoiceView, error) {
	projectID = strings.TrimSpace(projectID)
	if err := choice.Validate(); err != nil {
		return nil, apierr.Invalid("EXECUTION_CHOICE_INVALID", "请选择 Codex 或 OpenCode 并填写有效模型；OpenCode 模型必须包含服务商和模型名", nil)
	}
	writer, ok := s.complex.(executionChoiceWriter)
	if !ok {
		return nil, apierr.Internal("CLEARDEV_UNAVAILABLE", "当前后台不能保存项目执行选择")
	}
	if _, err := s.controlledProject(ctx, projectID); err != nil {
		return nil, apierr.NotFound("PROJECT_NOT_FOUND", "无法读取项目，请刷新项目列表后重试")
	}
	if err := writer.SetClearDevProjectExecution(ctx, projectID, choice); err != nil {
		if errors.Is(err, domain.ErrClearDevExecutionFrozen) {
			return nil, apierr.Conflict("EXECUTION_CHOICE_FROZEN", "项目已固定执行工具；模型在首次准入后也不可修改。请使用原配置，或新建项目", nil)
		}
		return nil, mapStoreError(err, "SET_EXECUTION_CHOICE_FAILED")
	}
	return s.executionChoiceView(ctx, projectID)
}

// RetryPreflightInput binds an explicit retry to the failure the user observed.
// A passed, replaced, or not-yet-retryable observation never authorizes a wake.
type RetryPreflightInput struct {
	PreflightID string `json:"preflightId"`
}

// RetryControlledPreflight wakes the same durable workflow. All role, result,
// message, candidate, recovery and Human Authority checks remain in that flow.
// It does not create a replacement session or count unknown delivery as failed.
func (s *Service) RetryControlledPreflight(ctx context.Context, requirementID string, input RetryPreflightInput) (RequirementView, error) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return RequirementView{}, err
	}
	if s.preflights == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "无法读取预检查记录")
	}
	if strings.TrimSpace(input.PreflightID) == "" {
		return RequirementView{}, apierr.Invalid("PREFLIGHT_ID_REQUIRED", "请刷新后选择当前失败的预检查", nil)
	}
	record, found, err := s.preflights.GetLatestClearDevControlledPreflight(ctx, requirementID)
	if err != nil {
		return RequirementView{}, apierr.Internal("PREFLIGHT_READ_FAILED", "无法读取预检查记录，请刷新后重试")
	}
	if !found || record.ID != input.PreflightID || record.Outcome != core.ControlledPreflightFailed || !record.Retryable {
		return RequirementView{}, apierr.Conflict("PREFLIGHT_RETRY_STALE", "预检查状态已经变化，请刷新后继续原流程", nil)
	}
	if record.RetryAt != nil && s.now().Before(*record.RetryAt) {
		return RequirementView{}, apierr.Conflict("PREFLIGHT_RETRY_NOT_READY", "服务商给出的重试时间尚未到达，请稍后重试", nil)
	}
	view, err := s.GetRequirement(ctx, requirementID)
	if err != nil {
		return RequirementView{}, err
	}
	var target *core.ControlledWork
	for i := range view.TrustedProgress.ControlledWork {
		work := &view.TrustedProgress.ControlledWork[i]
		if work.PreflightID == record.ID && work.RoleBindingID == record.RoleBindingID {
			target = work
			break
		}
	}
	if target == nil || view.Requirement.CancelledAt != nil || view.TrustedProgress.Phase == core.TrustedPhaseCompleted {
		return RequirementView{}, apierr.Conflict("PREFLIGHT_RETRY_UNAVAILABLE", "这条预检查不再属于当前可继续的步骤，请刷新后查看原流程", nil)
	}
	switch {
	case target.StepCategory == core.AgentStepCategoryDirection:
		s.scheduleDirectionChange(requirementID)
	case view.ComplexExecution != nil || view.QuickExecution != nil:
		s.scheduleComplexStandardExecution(requirementID)
	case view.ComplexPlanning != nil:
		s.scheduleComplexFlow(requirementID)
	case view.StandardFlow != nil:
		s.scheduleStandardFlow(requirementID)
	default:
		return RequirementView{}, apierr.Conflict("PREFLIGHT_RETRY_UNAVAILABLE", "没有可继续的原流程，请刷新并查看阻塞说明", nil)
	}
	return s.GetRequirement(ctx, requirementID)
}
