package cleardev

import (
	"context"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ExecutionChoiceView is the current project selection, separate from the
// immutable original product request and each role's recorded preflight.
type ExecutionChoiceView struct {
	Agent       domain.AgentHarness `json:"agent"`
	Model       string              `json:"model"`
	Effort      string              `json:"effort,omitempty"`
	Legacy      bool                `json:"legacy"`
	Known       bool                `json:"known"`
	ToolLocked  bool                `json:"toolLocked"`
	ModelLocked bool                `json:"modelLocked"`
}

type executionChoiceLockReader interface {
	GetClearDevExecutionChoiceLocks(context.Context, string) (bool, bool, error)
}

func (s *Service) executionChoiceView(ctx context.Context, projectID string) (*ExecutionChoiceView, error) {
	project, err := s.controlledProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	view := &ExecutionChoiceView{
		Agent: project.Config.ClearDevHarness(), Legacy: project.Config.ClearDev == nil,
		Model:      requestedChatModel(project, domain.KindOrchestrator),
		ToolLocked: true, ModelLocked: true, // Missing lock facts are not permission.
	}
	if project.Config.ClearDev != nil {
		view.Effort = project.Config.ClearDev.Effort
	}
	reader, ok := s.complex.(executionChoiceLockReader)
	if !ok {
		return view, nil
	}
	view.ToolLocked, view.ModelLocked, err = reader.GetClearDevExecutionChoiceLocks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	view.Known = true
	return view, nil
}

func (s *Service) controlledProject(ctx context.Context, projectID string) (domain.ProjectRecord, error) {
	if s.ao == nil {
		return domain.ProjectRecord{}, errors.New("controlled project configuration is unavailable")
	}
	project, found, err := s.ao.GetProject(ctx, projectID)
	if err != nil {
		return domain.ProjectRecord{}, err
	}
	if !found {
		return domain.ProjectRecord{}, errors.New("controlled project was not found")
	}
	if project.Config.ClearDev != nil {
		if err := project.Config.ClearDev.Validate(); err != nil {
			return domain.ProjectRecord{}, err
		}
	}
	return project, nil
}

func supportedControlledHarness(harness domain.AgentHarness) bool {
	return harness == domain.HarnessCodex || harness == domain.HarnessOpenCode
}

// controlledSessionMatches binds validation and recovery to the project's
// durable tool/model, not merely to either of the two supported harness names.
func (s *Service) controlledSessionMatches(ctx context.Context, record domain.SessionRecord) bool {
	project, err := s.controlledProject(ctx, string(record.ProjectID))
	if err != nil || record.Harness != project.Config.ClearDevHarness() {
		return false
	}
	choice := project.Config.ClearDev
	return choice == nil || record.Metadata.Model == choice.Model
}

func (s *Service) validComplexExecutionWorker(ctx context.Context, record domain.SessionRecord, binding core.ComplexExecutionRoleBinding, projectID string) bool {
	return validComplexExecutionWorker(record, binding, projectID) && s.controlledSessionMatches(ctx, record)
}

func (s *Service) validComplexExecutionSteward(ctx context.Context, record domain.SessionRecord, projectID string) bool {
	return validComplexExecutionSteward(record, projectID) && s.controlledSessionMatches(ctx, record)
}

func (s *Service) validateStandardSession(ctx context.Context, record domain.SessionRecord, binding core.RoleSessionBinding, projectID string, kind domain.SessionKind, flow core.StandardFlowSnapshot) error {
	if !s.controlledSessionMatches(ctx, record) {
		return errors.New("session does not match the project's fixed execution tool and model")
	}
	return validateStandardSession(record, binding, projectID, kind, flow)
}

func (s *Service) validateComplexSession(ctx context.Context, record domain.SessionRecord, binding core.ComplexRoleBinding, projectID string, kind domain.SessionKind, planning core.ComplexPlanningSnapshot) error {
	if !s.controlledSessionMatches(ctx, record) {
		return errors.New("session does not match the project's fixed execution tool and model")
	}
	return validateComplexSession(record, binding, projectID, kind, planning)
}
