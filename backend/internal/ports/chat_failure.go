package ports

import (
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ChatFailureError carries a stable failure without requiring a caller to parse
// the wrapped provider or transport error text.
type ChatFailureError struct {
	Failure domain.ConversationFailure
	Err     error
}

func (e *ChatFailureError) Error() string {
	if e == nil || e.Err == nil {
		return "chat operation failed"
	}
	return e.Err.Error()
}

func (e *ChatFailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// WithChatFailure attaches stable evidence to an existing error.
func WithChatFailure(err error, failure domain.ConversationFailure) error {
	if err == nil {
		return nil
	}
	return &ChatFailureError{Failure: failure, Err: err}
}

// ChatFailureFromError reads the typed failure carried through wrapped errors.
func ChatFailureFromError(err error) (domain.ConversationFailure, bool) {
	var typed *ChatFailureError
	if !errors.As(err, &typed) || typed == nil || typed.Failure.Category == "" {
		return domain.ConversationFailure{}, false
	}
	return typed.Failure, true
}
