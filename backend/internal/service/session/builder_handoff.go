package session

import (
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ObserveBuilderHandoffSession is private daemon command evidence. Session GETs
// never call it, create a conversation, wake a controller, or change lifecycle.
func (s *Service) ObserveBuilderHandoffSession(ctx context.Context, id domain.SessionID) (ports.BuilderHandoffSessionObservation, error) {
	observer, ok := s.manager.(ports.BuilderHandoffSessionObserver)
	if !ok {
		return ports.BuilderHandoffSessionObservation{}, errors.New("builder loss observation is unavailable")
	}
	return observer.ObserveBuilderHandoffSession(ctx, id)
}
