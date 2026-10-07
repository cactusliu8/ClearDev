package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func builderSessionFenced(ctx context.Context, q *gen.Queries, id domain.SessionID) (bool, error) {
	_, err := q.GetClearDevBuilderSessionFence(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// IsClearDevBuilderSessionFenced observes the durable retired identity.
func (s *Store) IsClearDevBuilderSessionFenced(ctx context.Context, id domain.SessionID) (bool, error) {
	return builderSessionFenced(ctx, s.qr, id)
}

// HasOpenClearDevBuilderSessionOperation observes any unresolved lifecycle operation.
func (s *Store) HasOpenClearDevBuilderSessionOperation(ctx context.Context, id domain.SessionID) (bool, error) {
	n, err := s.qr.CountOpenClearDevBuilderSessionOperations(ctx, string(id))
	return n != 0, err
}

// HasOpenClearDevBuilderConversationTurn observes queued and running provider turns on every branch.
func (s *Store) HasOpenClearDevBuilderConversationTurn(ctx context.Context, id domain.SessionID) (bool, error) {
	return s.qr.HasOpenClearDevBuilderConversationTurn(ctx, id)
}

// BeginClearDevBuilderSessionOperation atomically excludes a handoff while an
// existing controlled Builder's external lifecycle operation is unresolved.
// False is a saved claim, never permission to repeat its side effect.
func (s *Store) BeginClearDevBuilderSessionOperation(ctx context.Context, id domain.SessionID, operationID, kind string, at time.Time) (bool, error) {
	if strings.TrimSpace(operationID) == "" || strings.TrimSpace(kind) == "" || at.IsZero() {
		return false, complexExecutionRule("Builder operation lacks identity")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	claimed := false
	err := s.inTx(ctx, "claim controlled Builder lifecycle operation", func(q *gen.Queries) error {
		managed, err := q.IsClearDevManagedBuilderSession(ctx, nullableString(string(id)))
		if err != nil {
			return err
		}
		if !managed {
			claimed = true
			return nil
		}
		if fenced, err := builderSessionFenced(ctx, q, id); err != nil {
			return err
		} else if fenced {
			return errors.Join(ports.ErrBuilderSessionFenced, &core.RuleError{Code: "BUILDER_SESSION_REPLACED", Message: "The old Builder identity is fenced by its authorized handoff"})
		}
		old, err := q.GetClearDevBuilderSessionOperation(ctx, operationID)
		if err == nil {
			if old.AoSessionID != string(id) || old.Kind != kind {
				return complexExecutionRule("Builder operation id changed its binding")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		open, err := q.CountOpenClearDevBuilderSessionOperations(ctx, string(id))
		if err != nil {
			return err
		}
		if open != 0 {
			return errors.Join(ports.ErrBuilderSessionOperationBusy, complexExecutionRule("Builder has an unresolved lifecycle operation"))
		}
		if err := q.InsertClearDevBuilderSessionOperation(ctx, gen.InsertClearDevBuilderSessionOperationParams{ID: operationID, AoSessionID: string(id), Kind: kind, CreatedAt: at}); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed && err == nil, err
}

// EndClearDevBuilderSessionOperation releases a confirmed completion or no-action failure.
// Unknown outcomes have no end fact and keep admission closed.
func (s *Store) EndClearDevBuilderSessionOperation(ctx context.Context, operationID, outcome string, at time.Time) error {
	if outcome != "COMPLETED" && outcome != "FAILED_BEFORE_ACTION" {
		return complexExecutionRule("unknown Builder operation cannot be released")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "settle controlled Builder lifecycle observation", func(q *gen.Queries) error {
		if _, err := q.GetClearDevBuilderSessionOperation(ctx, operationID); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		old, err := q.GetClearDevBuilderSessionOperationEnd(ctx, operationID)
		if err == nil {
			if old.Outcome != outcome {
				return complexExecutionRule("Builder operation outcome changed")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return q.InsertClearDevBuilderSessionOperationEnd(ctx, gen.InsertClearDevBuilderSessionOperationEndParams{OperationID: operationID, Outcome: outcome, CreatedAt: at})
	})
}

// SaveClearDevBuilderHandoffContext is a private daemon launch receipt. It is
// persisted before provider launch and can only replay the same exact context.
func (s *Store) SaveClearDevBuilderHandoffContext(ctx context.Context, id domain.SessionID, c ports.BuilderHandoffContext, systemPrompt, launchFingerprint string) error {
	if c.ReferencePath == "" || !validSHA256Digest(c.SHA256) || !validSHA256Digest(c.SnapshotSHA256) || systemPrompt == "" || launchFingerprint == "" {
		return complexExecutionRule("Builder handoff launch context is incomplete")
	}
	raw, err := core.CanonicalJSONBytes(c)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "persist private Builder handoff launch context", func(q *gen.Queries) error {
		old, err := q.GetClearDevBuilderHandoffContext(ctx, string(id))
		if err == nil {
			if old.ContextJson != string(raw) || old.SystemPrompt != systemPrompt || old.LaunchFingerprint != launchFingerprint {
				return complexExecutionRule("Builder launch context changed on replay")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		record, err := q.GetSession(ctx, id)
		if err != nil {
			return err
		}
		if record.CreationRequestFingerprint != launchFingerprint {
			return complexExecutionRule("Builder launch fingerprint does not match the persisted session")
		}
		return q.InsertClearDevBuilderHandoffContext(ctx, gen.InsertClearDevBuilderHandoffContextParams{AoSessionID: string(id), ContextJson: string(raw), ReferencePath: c.ReferencePath, ContextSha256: c.SHA256, SnapshotSha256: c.SnapshotSHA256, SystemPrompt: systemPrompt, LaunchFingerprint: launchFingerprint, CreatedAt: time.Now().UTC()})
	})
}

// GetClearDevBuilderHandoffContext reads the immutable private launch context and fingerprint.
func (s *Store) GetClearDevBuilderHandoffContext(ctx context.Context, id domain.SessionID) (ports.BuilderHandoffContext, string, string, bool, error) {
	var c ports.BuilderHandoffContext
	r, err := s.qr.GetClearDevBuilderHandoffContext(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, "", "", false, nil
	}
	if err != nil {
		return c, "", "", false, err
	}
	if err := json.Unmarshal([]byte(r.ContextJson), &c); err != nil {
		return c, "", "", false, err
	}
	return c, r.SystemPrompt, r.LaunchFingerprint, true, nil
}
