package chat

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type controlledProjectReader interface {
	GetProject(context.Context, string) (domain.ProjectRecord, bool, error)
}

// controlledExecutionChoice identifies explicitly bound project roles before
// a user request can wake or mutate the native provider. Ordinary AO sessions
// and historical implicit Codex configurations retain their existing behaviour.
func (s *Service) controlledExecutionChoice(ctx context.Context, record domain.SessionRecord) (*domain.ClearDevExecutionConfig, error) {
	if record.CreationIdempotencyKey == "" {
		return nil, nil
	}
	reader, ok := s.sessions.(controlledProjectReader)
	if !ok {
		return nil, fmt.Errorf("%w: controlled project configuration is unavailable", domain.ErrClearDevExecutionFrozen)
	}
	project, found, err := reader.GetProject(ctx, string(record.ProjectID))
	if err != nil || !found {
		return nil, fmt.Errorf("%w: controlled project configuration could not be read", domain.ErrClearDevExecutionFrozen)
	}
	choice := project.Config.ClearDev
	if choice == nil {
		return nil, nil
	}
	if choice.Validate() != nil || choice.Harness != record.Harness || choice.Model != record.Metadata.Model {
		return nil, domain.ErrClearDevExecutionFrozen
	}
	return choice, nil
}

func (s *Service) validateControlledTurnSettings(ctx context.Context, record domain.SessionRecord, settings domain.ConversationSettings) error {
	choice, err := s.controlledExecutionChoice(ctx, record)
	if err != nil || choice == nil {
		return err
	}
	// ClearDev itself may initialize the already-fixed values. Clearing a model
	// is also a switch: it would select the provider's mutable default.
	if settings.Model != choice.Model || settings.ReasoningEffort != choice.Effort ||
		settings.ApprovalMode != domain.PermissionModeAuto || record.PermissionMode != domain.PermissionModeAuto {
		return domain.ErrClearDevExecutionFrozen
	}
	return nil
}
