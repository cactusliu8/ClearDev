package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func decodeWorkflowFailure(row gen.CleardevWorkflowFailureReceipt) (core.WorkflowFailureReceipt, error) {
	var receipt core.WorkflowFailureReceipt
	if row.ReceiptSha256 != complexExecutionRawDigest([]byte(row.ReceiptJson)) || json.Unmarshal([]byte(row.ReceiptJson), &receipt) != nil ||
		receipt.ID != row.ID || receipt.RequirementID != row.RequirementID || receipt.SourceID != row.SourceID || receipt.SourceKind != row.SourceKind || receipt.ExecutionRunID != row.ExecutionRunID || receipt.Validate() != nil {
		return receipt, complexExecutionRule("workflow failure receipt is invalid")
	}
	return receipt, nil
}

// RecordClearDevWorkflowFailure adds missing diagnostics, not a recovery grant.
// The actual continuation must still pass its existing source/identity/budget
// transaction and the sender's live workspace and native-delivery checks.
func (s *Store) RecordClearDevWorkflowFailure(ctx context.Context, receipt core.WorkflowFailureReceipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record workflow failure evidence", func(q *gen.Queries) error {
		if row, err := q.GetClearDevWorkflowFailureReceipt(ctx, receipt.ID); err == nil {
			prior, err := decodeWorkflowFailure(row)
			if err != nil {
				return err
			}
			// Re-observation time never changes the identity of a failure.
			receipt.ObservedAt = prior.ObservedAt
			if prior != receipt {
				return complexExecutionRule("failure source was reused with changed evidence")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if receipt.SourceKind == core.FailureSourceHandoff {
			a, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, receipt.SourceID)
			if err != nil {
				return err
			}
			run, err := q.GetClearDevComplexExecutionRun(ctx, a.ExecutionRunID)
			if err != nil {
				return err
			}
			step, err := q.GetClearDevComplexExecutionAgentStep(ctx, a.AgentStepID)
			if err != nil {
				return err
			}
			task := core.ComplexExecutionTask{ID: a.TaskMappingID}
			if receipt.RequirementID != run.DevelopmentProjectID || receipt.ExecutionRunID != run.ID ||
				receipt.BindingSHA256 != core.HandoffFailureBinding(complexExecutionRunFromGen(run), task, complexExecutionDispatchFromGen(a), complexExecutionAgentStepFromGen(step)) {
				return complexExecutionRule("failure receipt lost its exact original Builder source")
			}
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		return q.InsertClearDevWorkflowFailureReceipt(ctx, gen.InsertClearDevWorkflowFailureReceiptParams{
			ID: receipt.ID, RequirementID: receipt.RequirementID, ExecutionRunID: receipt.ExecutionRunID,
			SourceKind: receipt.SourceKind, SourceID: receipt.SourceID, ReceiptJson: string(raw),
			ReceiptSha256: complexExecutionRawDigest(raw), CreatedAt: receipt.ObservedAt,
		})
	})
}

// ListClearDevWorkflowFailures returns validated source receipts without changing state.
func (s *Store) ListClearDevWorkflowFailures(ctx context.Context, requirementID string) ([]core.WorkflowFailureReceipt, error) {
	rows, err := s.qr.ListClearDevWorkflowFailureReceipts(ctx, requirementID)
	if err != nil {
		return nil, err
	}
	result := make([]core.WorkflowFailureReceipt, 0, len(rows))
	for _, row := range rows {
		receipt, err := decodeWorkflowFailure(row)
		if err != nil {
			return nil, err
		}
		result = append(result, receipt)
	}
	return result, nil
}
