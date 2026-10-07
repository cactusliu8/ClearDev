package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func complexExecutionBatchFromGen(row gen.CleardevComplexExecutionBatch) (cleardev.ComplexExecutionBatch, error) {
	batch := cleardev.ComplexExecutionBatch{ID: row.ID, ExecutionRunID: row.ExecutionRunID, Ordinal: int(row.Ordinal), CommonBaseSHA: row.CommonBaseSha, Status: cleardev.ComplexExecutionBatchStatus(row.Status), CreatedAt: row.CreatedAt}
	keys, err := decodeJSONStringSlice(row.TaskKeysJson, "execution batch task keys")
	if err != nil {
		return cleardev.ComplexExecutionBatch{}, err
	}
	batch.TaskKeys = keys
	if row.ComposedAt.Valid {
		v := row.ComposedAt.Time
		batch.ComposedAt = &v
	}
	return batch, nil
}

func complexExecutionCompositionFromGen(row gen.CleardevComplexExecutionComposition) (cleardev.ComplexExecutionComposition, error) {
	composition := cleardev.ComplexExecutionComposition{
		ID: row.ID, ExecutionRunID: row.ExecutionRunID, BatchID: row.BatchID, RequestID: row.RequestID,
		InputBaseSHA: row.InputBaseSha, WorkspacePath: row.WorkspacePath, OutputCommitSHA: row.OutputCommitSha,
		Status: cleardev.ComplexExecutionCompositionStatus(row.Status), ReasonCode: cleardev.ReasonCode(row.ReasonCode),
		CreatedAt: row.CreatedAt,
	}
	var err error
	if composition.InputCandidateIDs, err = decodeJSONStringSlice(row.InputCandidateIdsJson, "composition candidate ids"); err != nil {
		return cleardev.ComplexExecutionComposition{}, err
	}
	if composition.InputCandidateSHAs, err = decodeJSONStringSlice(row.InputCandidateShasJson, "composition candidate shas"); err != nil {
		return cleardev.ComplexExecutionComposition{}, err
	}
	if composition.ConflictPaths, err = decodeJSONStringSlice(row.ConflictPathsJson, "composition conflict paths"); err != nil {
		return cleardev.ComplexExecutionComposition{}, err
	}
	if row.SettledAt.Valid {
		v := row.SettledAt.Time
		composition.SettledAt = &v
	}
	return composition, nil
}

// complexExecutionBoundBuilderForRun resolves the builder that owns the final
// integration facts: the slot-1 builder for PARALLEL runs, the single builder
// otherwise.
func complexExecutionBoundBuilderForRun(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun) (cleardev.ComplexExecutionRoleBinding, error) {
	rows, err := q.ListClearDevComplexExecutionRoleBindings(ctx, run.ID)
	if err != nil {
		return cleardev.ComplexExecutionRoleBinding{}, err
	}
	var fallback *cleardev.ComplexExecutionRoleBinding
	for _, row := range rows {
		binding := complexExecutionRoleBindingFromGen(row)
		if binding.Role != cleardev.StandardRoleBuilder || binding.Status != cleardev.RoleBindingStatusBound {
			continue
		}
		if run.Mode != "PARALLEL" {
			return binding, nil
		}
		if binding.BuilderSlot == 1 {
			return binding, nil
		}
		fallback = &binding
	}
	if fallback != nil {
		return *fallback, nil
	}
	return cleardev.ComplexExecutionRoleBinding{}, complexExecutionRule("bound Builder missing")
}

// finalParallelComposition returns the composition of the last batch.
func finalParallelComposition(ctx context.Context, q *gen.Queries, runID string) (gen.CleardevComplexExecutionComposition, error) {
	batches, err := q.ListClearDevComplexExecutionBatches(ctx, runID)
	if err != nil {
		return gen.CleardevComplexExecutionComposition{}, err
	}
	if len(batches) == 0 {
		return gen.CleardevComplexExecutionComposition{}, complexExecutionRule("parallel execution lacks batches")
	}
	return q.GetClearDevComplexExecutionCompositionByBatch(ctx, batches[len(batches)-1].ID)
}

// ActivateClearDevComplexExecutionBatch freezes the wave's common base and
// moves it to RUNNING. The base must come from the slot-1 builder (wave 0) or
// the previous wave's composed commit; the batch trigger enforces the source.
func (s *Store) ActivateClearDevComplexExecutionBatch(ctx context.Context, runID, batchID, commonBaseSHA string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "activate ClearDev complex batch", func(q *gen.Queries) error {
		run, err := q.GetClearDevComplexExecutionRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.Mode != "PARALLEL" || run.Status != "ACCEPTED" {
			return complexExecutionRule("batch activation requires an accepted PARALLEL run")
		}
		batch, err := q.GetClearDevComplexExecutionBatch(ctx, batchID)
		if err != nil {
			return err
		}
		if batch.ExecutionRunID != runID || batch.Status != "PENDING" {
			return complexExecutionRule("batch is not pending activation")
		}
		if batch.Ordinal > 0 {
			priorBatches, err := q.ListClearDevComplexExecutionBatches(ctx, runID)
			if err != nil {
				return err
			}
			prior, found := gen.CleardevComplexExecutionBatch{}, false
			for _, row := range priorBatches {
				if int(row.Ordinal) == int(batch.Ordinal)-1 {
					prior, found = row, true
				}
			}
			if !found {
				return complexExecutionRule("previous batch missing")
			}
			priorComposition, err := q.GetClearDevComplexExecutionCompositionByBatch(ctx, prior.ID)
			if err != nil {
				return complexExecutionRule("previous batch is not composed")
			}
			if prior.Status != "COMPOSED" || priorComposition.Status != "COMPOSED" || priorComposition.OutputCommitSha != commonBaseSHA {
				return complexExecutionRule("batch base must equal the previous composed commit")
			}
		}
		rows, err := q.ActivateClearDevComplexExecutionBatchCAS(ctx, gen.ActivateClearDevComplexExecutionBatchCASParams{ID: batchID, CommonBaseSha: commonBaseSHA})
		if err != nil {
			return err
		}
		if rows != 1 {
			return complexExecutionRule("batch activation lost its compare-and-swap")
		}
		requirement, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
		if err != nil {
			return err
		}
		return insertComplexFactEvent(ctx, q, clearDevRequirementFromGen(requirement), cleardev.SubjectComplexExecutionRun, run.ID, cleardev.ActionDispatchComplexTask, cleardev.EventAccepted, cleardev.ReasonNone, "", at)
	})
}

// RecordClearDevComplexExecutionComposition stores the stable combine request
// (batch -> COMPOSING, composition PENDING) before any Git action runs.
func (s *Store) RecordClearDevComplexExecutionComposition(ctx context.Context, c cleardev.ComplexExecutionComposition) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record ClearDev complex composition", func(q *gen.Queries) error {
		run, err := q.GetClearDevComplexExecutionRun(ctx, c.ExecutionRunID)
		if err != nil {
			return err
		}
		if run.Mode != "PARALLEL" || run.Status != "ACCEPTED" {
			return complexExecutionRule("composition requires an accepted PARALLEL run")
		}
		if _, err := q.GetClearDevComplexExecutionCompositionByBatch(ctx, c.BatchID); err == nil {
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		batch, err := q.GetClearDevComplexExecutionBatch(ctx, c.BatchID)
		if err != nil {
			return err
		}
		if batch.ExecutionRunID != c.ExecutionRunID {
			return complexExecutionRule("composition batch belongs to another run")
		}
		rows, err := q.AdvanceClearDevComplexExecutionBatchCAS(ctx, gen.AdvanceClearDevComplexExecutionBatchCASParams{ID: c.BatchID, ExpectedStatus: "RUNNING", Status: "COMPOSING"})
		if err != nil {
			return err
		}
		if rows != 1 {
			return complexExecutionRule("batch is not running")
		}
		ids, err := json.Marshal(c.InputCandidateIDs)
		if err != nil {
			return err
		}
		shas, err := json.Marshal(c.InputCandidateSHAs)
		if err != nil {
			return err
		}
		return q.InsertClearDevComplexExecutionComposition(ctx, gen.InsertClearDevComplexExecutionCompositionParams{
			ID: c.ID, ExecutionRunID: c.ExecutionRunID, BatchID: c.BatchID, RequestID: c.RequestID,
			InputBaseSha: c.InputBaseSHA, InputCandidateIdsJson: string(ids), InputCandidateShasJson: string(shas),
			CreatedAt: c.CreatedAt,
		})
	})
}

// StartClearDevComplexExecutionComposition marks the stored request as running.
func (s *Store) StartClearDevComplexExecutionComposition(ctx context.Context, compositionID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "start ClearDev complex composition", func(q *gen.Queries) error {
		rows, err := q.StartClearDevComplexExecutionCompositionCAS(ctx, compositionID)
		if err != nil {
			return err
		}
		if rows != 1 {
			return complexExecutionRule("composition is not pending")
		}
		return nil
	})
}

// SettleClearDevComplexExecutionComposition records the outcome of one
// combine. A COMPOSED result also settles the batch; a BLOCKED or FAILED
// result stops the whole run without deleting any candidate or workspace.
func (s *Store) SettleClearDevComplexExecutionComposition(ctx context.Context, c cleardev.ComplexExecutionComposition) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "settle ClearDev complex composition", func(q *gen.Queries) error {
		stored, err := q.GetClearDevComplexExecutionComposition(ctx, c.ID)
		if err != nil {
			return err
		}
		if stored.BatchID != c.BatchID || stored.ExecutionRunID != c.ExecutionRunID || stored.Status != "RUNNING" {
			return complexExecutionRule("composition is not running")
		}
		if c.Status != cleardev.ComplexExecutionCompositionComposed &&
			c.Status != cleardev.ComplexExecutionCompositionBlocked &&
			c.Status != cleardev.ComplexExecutionCompositionFailed {
			return complexExecutionRule("composition outcome is invalid")
		}
		conflicts := c.ConflictPaths
		if conflicts == nil {
			conflicts = []string{}
		}
		encoded, err := json.Marshal(conflicts)
		if err != nil {
			return err
		}
		rows, err := q.SettleClearDevComplexExecutionCompositionCAS(ctx, gen.SettleClearDevComplexExecutionCompositionCASParams{
			ID: c.ID, Status: string(c.Status), WorkspacePath: c.WorkspacePath, OutputCommitSha: c.OutputCommitSHA,
			ConflictPathsJson: string(encoded), ReasonCode: string(c.ReasonCode), SettledAt: nullableTime(c.CreatedAt),
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return complexExecutionRule("composition settlement lost its compare-and-swap")
		}
		switch c.Status {
		case cleardev.ComplexExecutionCompositionComposed:
			if _, err := q.AdvanceClearDevComplexExecutionBatchCAS(ctx, gen.AdvanceClearDevComplexExecutionBatchCASParams{ID: c.BatchID, ExpectedStatus: "COMPOSING", Status: "COMPOSED", ComposedAt: nullableTime(c.CreatedAt)}); err != nil {
				return err
			}
		case cleardev.ComplexExecutionCompositionBlocked, cleardev.ComplexExecutionCompositionFailed:
			if _, err := q.AdvanceClearDevComplexExecutionBatchCAS(ctx, gen.AdvanceClearDevComplexExecutionBatchCASParams{ID: c.BatchID, ExpectedStatus: "COMPOSING", Status: "BLOCKED", ComposedAt: nullableTime(c.CreatedAt)}); err != nil {
				return err
			}
			reason := string(cleardev.ReasonCompositionConflict)
			if c.ReasonCode != cleardev.ReasonNone {
				reason = string(c.ReasonCode)
			}
			if _, err := q.SettleClearDevComplexExecutionRunCAS(ctx, gen.SettleClearDevComplexExecutionRunCASParams{ID: c.ExecutionRunID, Status: "BLOCKED", ReasonCode: reason, SettledAt: nullableTime(c.CreatedAt)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// RebaseClearDevComplexExecutionBuilder moves one builder binding onto a
// composed commit of its own run. The workspace cleanliness and head checks
// happen before this call; the database only accepts a composed output.
func (s *Store) RebaseClearDevComplexExecutionBuilder(ctx context.Context, bindingID, baseCommitSHA string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "rebase ClearDev complex builder", func(q *gen.Queries) error {
		rows, err := q.RebaseClearDevComplexExecutionBuilderCAS(ctx, gen.RebaseClearDevComplexExecutionBuilderCASParams{ID: bindingID, BaseCommitSha: baseCommitSHA})
		if err != nil {
			return err
		}
		if rows != 1 {
			return complexExecutionRule("builder rebase lost its compare-and-swap")
		}
		return nil
	})
}
