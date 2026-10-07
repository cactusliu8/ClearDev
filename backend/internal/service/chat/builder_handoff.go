package chat

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type builderRuntimeObservation struct {
	providerID string
	generation string
	process    ports.ChatProcessObserver
}

type builderSessionFenceStore interface {
	IsClearDevBuilderSessionFenced(context.Context, domain.SessionID) (bool, error)
	BeginClearDevBuilderSessionOperation(context.Context, domain.SessionID, string, string, time.Time) (bool, error)
	EndClearDevBuilderSessionOperation(context.Context, string, string, time.Time) error
}

func (s *Service) builderFenceStore() builderSessionFenceStore {
	if store, ok := s.store.(builderSessionFenceStore); ok {
		return store
	}
	if store, ok := s.sessions.(builderSessionFenceStore); ok {
		return store
	}
	return nil
}

func (s *Service) checkBuilderSessionFence(ctx context.Context, id domain.SessionID) error {
	if store := s.builderFenceStore(); store != nil {
		fenced, err := store.IsClearDevBuilderSessionFenced(ctx, id)
		if err != nil {
			return err
		}
		if fenced {
			return ports.ErrBuilderSessionFenced
		}
	}
	return nil
}

func (s *Service) beginBuilderChatOperation(ctx context.Context, id domain.SessionID) (context.Context, func(bool), error) {
	if ports.BuilderSessionOperationAdmitted(ctx, id) {
		return ctx, func(bool) {}, nil
	}
	store := s.builderFenceStore()
	if store == nil {
		return ctx, func(bool) {}, nil
	}
	idempotencyID := "builder-chat-operation-" + uuid.NewString()
	claimed, err := store.BeginClearDevBuilderSessionOperation(ctx, id, idempotencyID, "chat-send", s.now())
	if err != nil {
		return ctx, nil, err
	}
	if !claimed {
		return ctx, nil, errors.New("builder Chat operation was already claimed")
	}
	finish := func(completed bool) {
		if !completed {
			return
		}
		endCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.EndClearDevBuilderSessionOperation(endCtx, idempotencyID, "COMPLETED", s.now()); err != nil {
			s.log.Error("Builder Chat operation could not be settled", "operationID", idempotencyID, "error", err)
		}
	}
	return ports.WithBuilderSessionOperation(ctx, id), finish, nil
}

// ObserveBuilderHandoffChatRuntime never wakes or closes a controller. The
// retained observer belongs to the exact launch, including after registry
// cleanup. Old daemon launches and unsupported drivers have no proof.
func (s *Service) ObserveBuilderHandoffChatRuntime(ctx context.Context, id domain.SessionID, providerID, generation string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if providerID == "" || generation == "" {
		return false, nil
	}
	s.mu.RLock()
	observation, found := s.builderRuntimeObservations[id]
	s.mu.RUnlock()
	if !found || observation.providerID != providerID || observation.generation != generation || observation.process == nil {
		return false, nil
	}
	return observation.process.ProcessStopped(), nil
}
