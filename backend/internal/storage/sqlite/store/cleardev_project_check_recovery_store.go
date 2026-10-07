package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// RecoverClearDevProjectDependencyCheck commits recovery and its unique retry together. SQLite
// checks every source/gate and records the original blocked-attempt time before
// reopening that same observed candidate. It creates no Builder work or budget.
func (s *Store) RecoverClearDevProjectDependencyCheck(ctx context.Context, originalID string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	changed := false
	err := s.inTx(ctx, "recover ClearDev project dependency check", func(q *gen.Queries) error {
		if _, err := q.GetClearDevProjectCheckRecovery(ctx, originalID); err == nil {
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		original, err := q.GetClearDevComplexExecutionCheckRun(ctx, originalID)
		if err != nil {
			return err
		}
		attempt, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, original.TaskAttemptID)
		if err != nil {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, attempt.ExecutionRunID)
		if err != nil {
			return err
		}
		plan, err := q.GetClearDevProjectExecutionSourcePlan(ctx, run.PlanID)
		if err != nil {
			return err
		}
		if err := validateProjectExecutionRunAdmission(ctx, q, complexExecutionRunFromGen(run), plan); err != nil {
			return err
		}
		if !attempt.SettledAt.Valid {
			return complexExecutionRule("dependency recovery needs the original settled attempt")
		}
		retryID := originalID + ":project-dependency-retry"
		if err := q.InsertClearDevComplexExecutionCheckRun(ctx, gen.InsertClearDevComplexExecutionCheckRunParams{
			ID: retryID, CheckSpecID: original.CheckSpecID, TaskAttemptID: original.TaskAttemptID,
			CandidateCommitID: original.CandidateCommitID, CandidateCommitSha: original.CandidateCommitSha,
			Status: "PENDING", CreatedAt: at, RetryOrdinal: 1,
		}); err != nil {
			return err
		}
		if err := q.InsertClearDevProjectCheckRecovery(ctx, gen.InsertClearDevProjectCheckRecoveryParams{
			OriginalCheckRunID: originalID, RetryCheckRunID: retryID, OriginalAttemptSettledAt: attempt.SettledAt.Time, CreatedAt: at,
		}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// ContinueClearDevProjectDependencyCheck resumes the same never-started retry,
// preserving the false exited-Builder barrier as an immutable continuation fact.
func (s *Store) ContinueClearDevProjectDependencyCheck(ctx context.Context, retryID string, at time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	changed := false
	err := s.inTx(ctx, "continue ClearDev project dependency check", func(q *gen.Queries) error {
		if _, err := q.GetClearDevProjectCheckContinuation(ctx, retryID); err == nil {
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		retry, err := q.GetClearDevComplexExecutionCheckRun(ctx, retryID)
		if err != nil {
			return err
		}
		attempt, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, retry.TaskAttemptID)
		if err != nil {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, attempt.ExecutionRunID)
		if err != nil {
			return err
		}
		plan, err := q.GetClearDevProjectExecutionSourcePlan(ctx, run.PlanID)
		if err != nil {
			return err
		}
		if err := validateProjectExecutionRunAdmission(ctx, q, complexExecutionRunFromGen(run), plan); err != nil {
			return err
		}
		if !attempt.SettledAt.Valid {
			return complexExecutionRule("continuation needs the original blocked attempt")
		}
		if err := q.InsertClearDevProjectCheckContinuation(ctx, gen.InsertClearDevProjectCheckContinuationParams{RetryCheckRunID: retryID, BlockedAttemptSettledAt: attempt.SettledAt.Time, CreatedAt: at}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}
