package chat

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type controlledRuntimeSessionReader interface {
	IsClearDevControlledSession(context.Context, string) (bool, error)
}

// unattendedControlledRuntime separates the persisted ClearDev Auto identity
// from the provider's tool approval posture. All Chat start/restore/switch paths
// converge here. Ordinary AO Auto and explicit restricted modes are unchanged.
// No name, prompt, environment variable or creation-key prefix grants this policy.
func (s *Service) unattendedControlledRuntime(ctx context.Context, cfg StartConfig) (bool, error) {
	if cfg.Permissions != domain.PermissionModeAuto ||
		(cfg.Harness != domain.HarnessCodex && cfg.Harness != domain.HarnessOpenCode) {
		return false, nil
	}
	reader, ok := s.store.(controlledRuntimeSessionReader)
	if !ok {
		return false, nil
	}
	controlled, err := reader.IsClearDevControlledSession(ctx, string(cfg.SessionID))
	if err != nil {
		return false, fmt.Errorf("read controlled runtime policy: %w", err)
	}
	if !controlled {
		return false, nil
	}
	if s.sessions == nil {
		return false, fmt.Errorf("controlled runtime policy requires a session reader")
	}
	record, found, err := s.sessions.GetSession(ctx, cfg.SessionID)
	if err != nil {
		return false, fmt.Errorf("read controlled runtime identity: %w", err)
	}
	if !found || record.ID != cfg.SessionID || record.ProjectID != cfg.ProjectID || record.Kind != cfg.Kind ||
		(record.Kind != domain.KindWorker && record.Kind != domain.KindOrchestrator) ||
		record.IsTerminated || record.Mode != domain.SessionModeChat || record.PermissionMode != domain.PermissionModeAuto ||
		(cfg.CreationIdempotencyKey != "" && cfg.CreationIdempotencyKey != record.CreationIdempotencyKey) {
		return false, fmt.Errorf("controlled runtime policy does not match the durable session")
	}
	// A switch has not yet committed its target harness when Chat starts it.
	// The lifecycle coordinator owns that authorization; do not rewrite or
	// re-interpret the source session, its fingerprint, or its role binding here.
	return true, nil
}
