package store

import (
	"context"
	"database/sql"
	"errors"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// GetClearDevParseCorrection loads the one mechanical JSON correction for a step.
func (s *Store) GetClearDevParseCorrection(ctx context.Context, stepID string) (cleardev.ParseCorrection, bool, error) {
	row, err := s.qr.GetClearDevParseCorrection(ctx, stepID)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.ParseCorrection{}, false, nil
	}
	if err != nil {
		return cleardev.ParseCorrection{}, false, err
	}
	return cleardev.ParseCorrection{
		StepID: row.StepID, AttemptNumber: row.AttemptNumber, ClientMessageID: row.ClientMessageID,
		PromptText: row.PromptText, PromptSHA256: row.PromptSha256, SentAt: row.SentAt,
	}, true, nil
}

// RecordClearDevParseCorrection occupies the one correction turn for a step.
func (s *Store) RecordClearDevParseCorrection(ctx context.Context, correction cleardev.ParseCorrection) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if correction.AttemptNumber == 0 {
		correction.AttemptNumber = 1
	}
	return s.inTx(ctx, "record ClearDev parse correction", func(q *gen.Queries) error {
		return q.InsertClearDevParseCorrection(ctx, gen.InsertClearDevParseCorrectionParams{
			StepID: correction.StepID, AttemptNumber: correction.AttemptNumber, ClientMessageID: correction.ClientMessageID,
			PromptText: correction.PromptText, PromptSha256: correction.PromptSHA256, SentAt: correction.SentAt,
		})
	})
}
