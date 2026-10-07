package store

import (
	"context"
	"database/sql"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func builderSessionCheckFromRow(row gen.CleardevBuilderSessionCheck) core.BuilderSessionCheck {
	return core.BuilderSessionCheck{RecoveryID: row.RecoveryID, Checkpoint: row.Checkpoint, Stage: row.Stage, Outcome: row.Outcome,
		ReasonCode: row.ReasonCode, PreflightID: row.PreflightID, BindingSHA256: row.BindingSha256, CheckedAt: row.CheckedAt}
}

// ListClearDevBuilderSessionChecks reads immutable observations for one run.
func (s *Store) ListClearDevBuilderSessionChecks(ctx context.Context, runID string) ([]core.BuilderSessionCheck, error) {
	rows, err := s.qr.ListClearDevBuilderSessionChecks(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]core.BuilderSessionCheck, 0, len(rows))
	for _, row := range rows {
		out = append(out, builderSessionCheckFromRow(row))
	}
	return out, nil
}

// RecordClearDevBuilderSessionCheck returns false on exact replay. Evidence is
// never overwritten and cannot grant a message or change a workflow state.
func (s *Store) RecordClearDevBuilderSessionCheck(ctx context.Context, check core.BuilderSessionCheck) (bool, error) {
	if check.RecoveryID == "" || !validSHA256Digest(check.BindingSHA256) || check.CheckedAt.IsZero() {
		return false, complexExecutionRule("Builder recheck lacks an exact binding")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	created := false
	err := s.inTx(ctx, "record Builder session recheck", func(q *gen.Queries) error {
		old, err := q.GetClearDevBuilderSessionCheck(ctx, gen.GetClearDevBuilderSessionCheckParams{RecoveryID: check.RecoveryID, Checkpoint: check.Checkpoint})
		if err == nil {
			if old.BindingSha256 != check.BindingSHA256 || old.Outcome != check.Outcome || old.Stage != check.Stage || old.ReasonCode != check.ReasonCode || old.PreflightID != check.PreflightID {
				return complexExecutionRule("Builder recheck evidence changed on replay")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if check.Checkpoint == "STARTED" {
			recovery, err := q.GetClearDevWorkflowRecovery(ctx, check.RecoveryID)
			if err != nil {
				return err
			}
			unsent, err := builderSessionStepUnsent(ctx, q, recovery.ExecutionRunID, recovery.StepID)
			if err != nil {
				return err
			}
			if !unsent {
				return complexExecutionRule("Builder recheck cannot restore a delivered or unknown step")
			}
		}
		rows, err := q.InsertClearDevBuilderSessionCheck(ctx, gen.InsertClearDevBuilderSessionCheckParams{RecoveryID: check.RecoveryID, Checkpoint: check.Checkpoint, Stage: check.Stage, Outcome: check.Outcome, ReasonCode: check.ReasonCode, PreflightID: check.PreflightID, BindingSha256: check.BindingSHA256, CheckedAt: check.CheckedAt})
		created = rows == 1
		return err
	})
	return created && err == nil, err
}

// ClearDevBuilderSessionHasOpenTurn checks durable turns without restoring a session.
func (s *Store) ClearDevBuilderSessionHasOpenTurn(ctx context.Context, id domain.SessionID) (bool, error) {
	count, err := s.qr.CountClearDevBuilderSessionOpenTurns(ctx, id)
	return count != 0, err
}

// ReadClearDevBuilderStepUnsent verifies the original unreserved first message.
func (s *Store) ReadClearDevBuilderStepUnsent(ctx context.Context, runID, stepID string) (bool, error) {
	return builderSessionStepUnsent(ctx, s.qr, runID, stepID)
}

// A first attempt allocated before a failed reservation is still the same
// unsent step. Any event or reservation (including unknown delivery) excludes it.
func builderSessionStepUnsent(ctx context.Context, q *gen.Queries, runID, stepID string) (bool, error) {
	run, err := q.GetClearDevComplexExecutionRun(ctx, runID)
	if err != nil {
		return false, err
	}
	step, err := q.GetClearDevComplexExecutionAgentStep(ctx, stepID)
	if err != nil {
		return false, err
	}
	if step.SendStatus != "PENDING" || step.SentAt.Valid || step.TurnID.Valid {
		return false, nil
	}
	binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, step.RoleBindingID)
	if err != nil {
		return false, err
	}
	if binding.ExecutionRunID != runID || binding.Role != "BUILDER" || binding.Status != "BOUND" || step.StepKind != "BUILDER_TASK" {
		return false, nil
	}
	attempts, err := q.ListClearDevAgentStepAttempts(ctx, run.DevelopmentProjectID)
	if err != nil {
		return false, err
	}
	for _, attempt := range attempts {
		if attempt.LogicalStepID != stepID {
			continue
		}
		if attempt.AttemptNumber != 1 || attempt.ID != stepID+":attempt:1" || attempt.StepCategory != "COMPLEX_EXECUTION" || attempt.StepKind != "BUILDER_TASK" || attempt.RoleBindingID != step.RoleBindingID || attempt.ClientMessageID != step.ClientMessageID || attempt.PromptSha256 != step.PromptSha256 || attempt.AoSessionID != binding.AoSessionID.String {
			return false, nil
		}
		if _, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, attempt.ID); err == nil {
			return false, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	messages, err := q.ListClearDevStepMessageReservations(ctx, stepID)
	return len(messages) == 0 && err == nil, err
}

// This transaction-side guard closes the gap between the service's session
// snapshot and the actual message reservation. An old READY receipt is not a
// substitute for a currently idle, unchanged original native session.
func validateBuilderSessionRecheckBeforeSend(ctx context.Context, q *gen.Queries, attempt core.AgentStepAttempt) error {
	if attempt.StepCategory != core.AgentStepCategoryComplexExecution || attempt.StepKind != core.ComplexExecutionAgentStepBuilderTask {
		return nil
	}
	_, err := q.GetLatestClearDevBuilderSessionRecoveryForStep(ctx, attempt.LogicalStepID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // steps without this explicit recovery keep their own admission
	}
	if err != nil {
		return err
	}
	checks, err := q.GetClearDevBuilderSessionCheckForStep(ctx, attempt.LogicalStepID)
	if err != nil {
		return err
	}
	var ready *gen.CleardevBuilderSessionCheck
	for i := range checks {
		if checks[i].Outcome == "FAILED" {
			return messageBudgetError("BUILDER_RECHECK_REQUIRED", "original Builder recheck failed before message admission")
		}
		if checks[i].Checkpoint == "FINISHED" && checks[i].Outcome == "READY" {
			ready = &checks[i]
		}
	}
	if ready == nil {
		return messageBudgetError("BUILDER_RECHECK_REQUIRED", "original Builder recheck has not finished")
	}
	session, err := q.GetSession(ctx, domain.SessionID(attempt.AOSessionID))
	if err != nil {
		return err
	}
	record := rowToRecord(session)
	if record.IsTerminated || record.Activity.State != domain.ActivityIdle || core.BuilderSessionBindingDigest(record) != ready.BindingSha256 {
		return messageBudgetError("BUILDER_RECHECK_REQUIRED", "original Builder changed or is not idle before sending")
	}
	count, err := q.CountClearDevBuilderSessionOpenTurns(ctx, record.ID)
	if err != nil {
		return err
	}
	if count != 0 {
		return messageBudgetError("BUILDER_RECHECK_REQUIRED", "original Builder still has an unfinished turn")
	}
	return nil
}
