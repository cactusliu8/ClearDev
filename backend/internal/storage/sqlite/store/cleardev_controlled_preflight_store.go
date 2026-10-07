package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// RecordClearDevControlledPreflight appends one inspection. Rows are immutable.
func (s *Store) RecordClearDevControlledPreflight(ctx context.Context, record cleardev.ControlledPreflight) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	changed := false
	err := s.inTx(ctx, "record ClearDev controlled preflight", func(q *gen.Queries) error {
		var bindErr error
		record, changed, bindErr = bindPreflightChoiceTx(ctx, q, record)
		if bindErr != nil {
			return bindErr
		}
		evidence, err := json.Marshal(record.Evidence)
		if err != nil {
			return err
		}
		return q.InsertClearDevControlledPreflight(ctx, gen.InsertClearDevControlledPreflightParams{
			ID:                   record.ID,
			DevelopmentProjectID: record.DevelopmentRequirementID,
			RoleBindingID:        record.RoleBindingID,
			AoProjectID:          record.AOProjectID,
			RequestedModel:       record.RequestedModel,
			ResolvedModel:        record.ResolvedModel,
			Provider:             record.Provider,
			CatalogJson:          record.CatalogJSON,
			CatalogSha256:        record.CatalogSHA256,
			Outcome:              record.Outcome,
			ReasonCode:           string(record.ReasonCode),
			Retryable:            record.Retryable,
			RetryAt:              nullableTimePtr(record.RetryAt),
			ProviderErrorCode:    record.ProviderErrorCode,
			ErrorSummary:         record.ErrorSummary,
			CapabilityEvidence:   string(evidence),
			CheckedAt:            record.CheckedAt,
		})
	})
	if err == nil && changed {
		return cleardev.ErrControlledExecutionChoiceChanged
	}
	return err
}

// GetLatestClearDevControlledPreflight returns the newest inspection for a requirement.
func (s *Store) GetLatestClearDevControlledPreflight(ctx context.Context, requirementID string) (cleardev.ControlledPreflight, bool, error) {
	row, err := s.qr.GetLatestClearDevControlledPreflight(ctx, requirementID)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.ControlledPreflight{}, false, nil
	}
	if err != nil {
		return cleardev.ControlledPreflight{}, false, err
	}
	record, decodeErr := controlledPreflightFromRow(row)
	return record, decodeErr == nil, decodeErr
}

// GetLatestClearDevControlledPreflightForBinding returns the newest inspection for one role binding.
func (s *Store) GetLatestClearDevControlledPreflightForBinding(ctx context.Context, roleBindingID string) (cleardev.ControlledPreflight, bool, error) {
	row, err := s.qr.GetLatestClearDevControlledPreflightForBinding(ctx, roleBindingID)
	if errors.Is(err, sql.ErrNoRows) {
		return cleardev.ControlledPreflight{}, false, nil
	}
	if err != nil {
		return cleardev.ControlledPreflight{}, false, err
	}
	record, decodeErr := controlledPreflightFromRow(row)
	return record, decodeErr == nil, decodeErr
}

func controlledPreflightFromRow(row gen.CleardevControlledPreflight) (cleardev.ControlledPreflight, error) {
	record := cleardev.ControlledPreflight{
		ID:                       row.ID,
		DevelopmentRequirementID: row.DevelopmentProjectID,
		RoleBindingID:            row.RoleBindingID,
		AOProjectID:              row.AoProjectID,
		RequestedModel:           row.RequestedModel,
		ResolvedModel:            row.ResolvedModel,
		Provider:                 row.Provider,
		CatalogJSON:              row.CatalogJson,
		CatalogSHA256:            row.CatalogSha256,
		Outcome:                  row.Outcome,
		ReasonCode:               cleardev.ReasonCode(row.ReasonCode),
		Retryable:                row.Retryable,
		ProviderErrorCode:        row.ProviderErrorCode,
		ErrorSummary:             row.ErrorSummary,
		CheckedAt:                row.CheckedAt,
	}
	if row.RetryAt.Valid {
		retryAt := row.RetryAt.Time
		record.RetryAt = &retryAt
	}
	if err := json.Unmarshal([]byte(row.CapabilityEvidence), &record.Evidence); err != nil {
		return record, fmt.Errorf("decode controlled preflight evidence: %w", err)
	}
	return record, nil
}
