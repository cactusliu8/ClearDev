package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ListClearDevRequirementIDsByAOProject returns development requirement ids for one AO project.
func (s *Store) ListClearDevRequirementIDsByAOProject(ctx context.Context, aoProjectID string) ([]string, error) {
	rows, err := s.qr.ListClearDevRequirementsByAOProject(ctx, aoProjectID)
	if err != nil {
		return nil, fmt.Errorf("list ClearDev requirements for AO project: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

// OccupyClearDevProgressExplanation creates one note request for a fact snapshot.
func (s *Store) OccupyClearDevProgressExplanation(ctx context.Context, command cleardev.OccupyProgressExplanationCommand) (cleardev.ProgressExplanationRequest, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var created bool
	var stored cleardev.ProgressExplanationRequest
	err := s.inTx(ctx, "occupy ClearDev progress explanation", func(q *gen.Queries) error {
		req := command.Request
		insertErr := q.InsertClearDevProgressExplanationRequest(ctx, gen.InsertClearDevProgressExplanationRequestParams{
			ID: req.ID, DevelopmentProjectID: req.DevelopmentRequirementID, FactSummarySha256: req.FactSummarySHA256,
			MaxEventSequence: req.MaxEventSequence, SourceStewardSessionID: req.SourceStewardSessionID,
			ContinuationOfSessionID: nullableString(req.ContinuationOfSessionID), AoSessionID: nullableString(req.AOSessionID),
			SessionCreationIdempotencyKey: req.SessionCreationIdempotencyKey, ClientMessageID: req.ClientMessageID,
			PromptText: req.PromptText, PromptSha256: req.PromptSHA256, CreatedAt: req.CreatedAt,
		})
		if insertErr != nil {
			if !isUniqueConstraint(insertErr) {
				return insertErr
			}
			row, getErr := q.GetClearDevProgressExplanationBySnapshot(ctx, gen.GetClearDevProgressExplanationBySnapshotParams{
				DevelopmentProjectID: req.DevelopmentRequirementID, FactSummarySha256: req.FactSummarySHA256,
			})
			if getErr != nil {
				return getErr
			}
			stored = progressExplanationFromGen(row)
			created = false
			return nil
		}
		row, getErr := q.GetClearDevProgressExplanation(ctx, req.ID)
		if getErr != nil {
			return getErr
		}
		stored = progressExplanationFromGen(row)
		created = true
		return nil
	})
	return stored, created, err
}

// GetClearDevProgressExplanation reads one explanation request by id.
func (s *Store) GetClearDevProgressExplanation(ctx context.Context, id string) (cleardev.ProgressExplanationRequest, bool, error) {
	row, err := s.qr.GetClearDevProgressExplanation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.ProgressExplanationRequest{}, false, nil
	}
	if err != nil {
		return cleardev.ProgressExplanationRequest{}, false, err
	}
	return progressExplanationFromGen(row), true, nil
}

// GetClearDevProgressExplanationBySnapshot reads the unique request for one fact hash.
func (s *Store) GetClearDevProgressExplanationBySnapshot(ctx context.Context, requirementID, factHash string) (cleardev.ProgressExplanationRequest, bool, error) {
	row, err := s.qr.GetClearDevProgressExplanationBySnapshot(ctx, gen.GetClearDevProgressExplanationBySnapshotParams{
		DevelopmentProjectID: requirementID, FactSummarySha256: factHash,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.ProgressExplanationRequest{}, false, nil
	}
	if err != nil {
		return cleardev.ProgressExplanationRequest{}, false, err
	}
	return progressExplanationFromGen(row), true, nil
}

// ListClearDevProgressExplanations returns every note request for one requirement.
func (s *Store) ListClearDevProgressExplanations(ctx context.Context, requirementID string) ([]cleardev.ProgressExplanationRequest, error) {
	rows, err := s.qr.ListClearDevProgressExplanations(ctx, requirementID)
	if err != nil {
		return nil, err
	}
	out := make([]cleardev.ProgressExplanationRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, progressExplanationFromGen(row))
	}
	return out, nil
}

// ListClearDevRunnableProgressExplanations returns unfinished note requests for boot resume.
func (s *Store) ListClearDevRunnableProgressExplanations(ctx context.Context) ([]cleardev.ProgressExplanationRequest, error) {
	rows, err := s.qr.ListClearDevRunnableProgressExplanations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cleardev.ProgressExplanationRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, progressExplanationFromGen(row))
	}
	return out, nil
}

// BindClearDevProgressExplanationSession attaches the continuation session once.
func (s *Store) BindClearDevProgressExplanationSession(ctx context.Context, id, aoSessionID, continuationOf string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.BindClearDevProgressExplanationSessionCAS(ctx, gen.BindClearDevProgressExplanationSessionCASParams{
		AoSessionID: nullableString(aoSessionID), ContinuationOfSessionID: nullableString(continuationOf), ID: id,
	})
	return n == 1, err
}

// MarkClearDevProgressExplanationSent records that the fact packet was sent.
func (s *Store) MarkClearDevProgressExplanationSent(ctx context.Context, id string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.MarkClearDevProgressExplanationSentCAS(ctx, gen.MarkClearDevProgressExplanationSentCASParams{SentAt: nullableTime(at), ID: id})
	return n == 1, err
}

// SettleClearDevProgressExplanation stores a valid steward note for the snapshot.
func (s *Store) SettleClearDevProgressExplanation(ctx context.Context, id, resultJSON, resultSHA string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.SettleClearDevProgressExplanationCAS(ctx, gen.SettleClearDevProgressExplanationCASParams{
		ResultJson: nullableString(resultJSON), ResultSha256: nullableString(resultSHA), SettledAt: nullableTime(at), ID: id,
	})
	return n == 1, err
}

// FailClearDevProgressExplanation records a durable explanation failure reason.
func (s *Store) FailClearDevProgressExplanation(ctx context.Context, id string, reason cleardev.ReasonCode, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.FailClearDevProgressExplanationCAS(ctx, gen.FailClearDevProgressExplanationCASParams{
		ReasonCode: string(reason), SettledAt: nullableTime(at), ID: id,
	})
	return n == 1, err
}

func progressExplanationFromGen(row gen.CleardevProgressExplanationRequest) cleardev.ProgressExplanationRequest {
	req := cleardev.ProgressExplanationRequest{
		ID: row.ID, DevelopmentRequirementID: row.DevelopmentProjectID, FactSummarySHA256: row.FactSummarySha256,
		MaxEventSequence: row.MaxEventSequence, SourceStewardSessionID: row.SourceStewardSessionID,
		ContinuationOfSessionID: row.ContinuationOfSessionID.String, AOSessionID: row.AoSessionID.String,
		SessionCreationIdempotencyKey: row.SessionCreationIdempotencyKey, ClientMessageID: row.ClientMessageID,
		PromptText: row.PromptText, PromptSHA256: row.PromptSha256, Status: row.Status, ResultJSON: row.ResultJson.String,
		ResultSHA256: row.ResultSha256.String, ReasonCode: cleardev.ReasonCode(row.ReasonCode), CreatedAt: row.CreatedAt,
	}
	if row.SentAt.Valid {
		at := row.SentAt.Time
		req.SentAt = &at
	}
	if row.SettledAt.Valid {
		at := row.SettledAt.Time
		req.SettledAt = &at
	}
	return req
}
