package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func readReviewCheckRequest(ctx context.Context, q *gen.Queries, id string) (core.ReviewCheckRequest, bool, error) {
	row, err := q.GetClearDevReviewCheckRequest(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ReviewCheckRequest{}, false, nil
	}
	if err != nil {
		return core.ReviewCheckRequest{}, false, err
	}
	var request core.ReviewCheckRequest
	if row.RequestSha256 != complexExecutionRawDigest([]byte(row.RequestJson)) {
		return request, false, errors.New("damaged Reviewer check request")
	}
	if err := json.Unmarshal([]byte(row.RequestJson), &request); err != nil {
		return request, false, err
	}
	review, err := q.GetClearDevComplexExecutionReview(ctx, id)
	if err != nil {
		return request, false, err
	}
	run, err := reviewCheckExecutionRun(ctx, q, review)
	if err != nil {
		return request, false, err
	}
	if request.ReviewID != id {
		return request, false, errors.New("reviewer check request belongs to a different review")
	}
	return request, true, core.ValidateReviewCheckRequestForRun(request, run)
}

func reviewCheckExecutionRun(ctx context.Context, q *gen.Queries, review gen.CleardevComplexExecutionReview) (core.ComplexExecutionRun, error) {
	attempt, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, review.TaskAttemptID)
	if err != nil {
		return core.ComplexExecutionRun{}, err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, attempt.ExecutionRunID)
	if err != nil {
		return core.ComplexExecutionRun{}, err
	}
	return complexExecutionRunFromGen(run), nil
}

// GetClearDevReviewerChecks reads immutable request and execution receipts.
func (s *Store) GetClearDevReviewerChecks(ctx context.Context, id string) (core.ReviewCheckRequest, []core.ReviewCheckEvidence, bool, error) {
	request, found, err := readReviewCheckRequest(ctx, s.qr, id)
	if err != nil || !found {
		return request, nil, found, err
	}
	results, err := readReviewCheckResults(ctx, s.qr, request)
	return request, results, true, err
}

func readReviewCheckResults(ctx context.Context, q *gen.Queries, request core.ReviewCheckRequest) ([]core.ReviewCheckEvidence, error) {
	rows, err := q.ListClearDevReviewCheckResults(ctx, request.ReviewID)
	if err != nil {
		return nil, err
	}
	out := make([]core.ReviewCheckEvidence, 0, len(rows))
	for _, row := range rows {
		var result core.ReviewCheckEvidence
		if row.ResultSha256 != complexExecutionRawDigest([]byte(row.ResultJson)) {
			return nil, errors.New("damaged Reviewer check result")
		}
		if err := json.Unmarshal([]byte(row.ResultJson), &result); err != nil {
			return nil, err
		}
		if err := core.ValidateReviewCheckEvidence(request, result); err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	return out, nil
}

// RequestClearDevReviewerChecks binds the exact original model request once.
func (s *Store) RequestClearDevReviewerChecks(ctx context.Context, request core.ReviewCheckRequest) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "request approved Reviewer checks", func(q *gen.Queries) error {
		review, err := q.GetClearDevComplexExecutionReview(ctx, request.ReviewID)
		if err != nil {
			return err
		}
		step, err := q.GetClearDevComplexExecutionAgentStep(ctx, review.AgentStepID)
		if err != nil {
			return err
		}
		sourceText := step.FinalMessageText.String
		if request.ReplacementRecoveryID != "" {
			raw, _, sourceErr := mailReplacementCheckSource(ctx, q, request)
			if sourceErr != nil {
				return sourceErr
			}
			sourceText = raw.RawMessageText
		} else if request.RequestAttemptID != "" || request.RequestResultID != "" {
			return complexExecutionRule("replacement request source is incomplete")
		}
		run, err := reviewCheckExecutionRun(ctx, q, review)
		if err != nil {
			return err
		}
		if err := core.ValidateReviewCheckRequestForRun(request, run); err != nil {
			return err
		}
		parsed, err := core.ParseExecutionReviewCheckRequest([]byte(sourceText), run)
		if err != nil || !slices.Equal(parsed.CheckIDs, request.CheckIDs) || request.RequestStepID != step.ID || request.RequestedAt.IsZero() {
			return complexExecutionRule("Reviewer request differs from original completed result")
		}
		// The canonical encoder keeps this request comparable, as exact text, with
		// the admission mirror it binds to (no HTML escaping of "<", ">", "&").
		raw, err := core.CanonicalJSONBytes(request)
		if err != nil {
			return err
		}
		old, err := q.GetClearDevReviewCheckRequest(ctx, request.ReviewID)
		if err == nil {
			// Requests written before the canonical encoder spell "<", ">" and
			// "&" as \u003c, \u003e and \u0026; that same request is still
			// the same request, so accept its legacy byte form too.
			legacy, legacyErr := json.Marshal(request)
			if old.RequestJson != string(raw) && (legacyErr != nil || old.RequestJson != string(legacy)) {
				return complexExecutionRule("Reviewer check request is immutable")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return q.InsertClearDevReviewCheckRequest(ctx, gen.InsertClearDevReviewCheckRequestParams{ReviewID: request.ReviewID, RequestJson: string(raw), RequestSha256: complexExecutionRawDigest(raw), CreatedAt: request.RequestedAt})
	})
}

// RecordClearDevReviewerCheck appends a validated trusted executor receipt.
func (s *Store) RecordClearDevReviewerCheck(ctx context.Context, result core.ReviewCheckEvidence) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record trusted Reviewer check", func(q *gen.Queries) error {
		request, found, err := readReviewCheckRequest(ctx, q, result.ReviewID)
		if err != nil {
			return err
		}
		if !found {
			return complexExecutionRule("Reviewer check has no approved request")
		}
		if err := core.ValidateReviewCheckEvidence(request, result); err != nil {
			return err
		}
		previous, err := readReviewCheckResults(ctx, q, request)
		if err != nil {
			return err
		}
		for _, old := range previous {
			if old.CheckID != result.CheckID {
				continue
			}
			result.RecordedAt = old.RecordedAt
			left, _ := json.Marshal(old)
			right, _ := json.Marshal(result)
			if !bytes.Equal(left, right) {
				return complexExecutionRule("Reviewer check receipt changed")
			}
			return nil
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return q.InsertClearDevReviewCheckResult(ctx, gen.InsertClearDevReviewCheckResultParams{ReviewID: result.ReviewID, CheckID: result.CheckID, RunID: result.Proof.RunID, ResultJson: string(raw), ResultSha256: complexExecutionRawDigest(raw), CreatedAt: result.RecordedAt})
	})
}

// Independently enforced at review settlement AND again in the protected final
// transaction. A model PASS cannot mask missing, stale or failed receipts.
func validateReviewerChecks(ctx context.Context, q *gen.Queries, review gen.CleardevComplexExecutionReview, verdict string) error {
	request, found, err := readReviewCheckRequest(ctx, q, review.ID)
	if err != nil {
		return err
	}
	if !found {
		original, err := q.GetClearDevComplexExecutionAgentStep(ctx, review.AgentStepID)
		if err != nil {
			return err
		}
		var kind struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal([]byte(original.FinalMessageText.String), &kind)
		if kind.Kind == core.ReviewCheckRequestKind {
			return complexExecutionRule("unresolved Reviewer check request")
		}
		return nil
	}
	candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(review.TaskAttemptID))
	if err != nil {
		return err
	}
	if request.CandidateID != candidate.ID || request.CandidateSHA != candidate.CommitSha || request.PacketSHA256 != review.ReviewPacketSha256 || request.RequestStepID != review.AgentStepID {
		return complexExecutionRule("Reviewer check request has stale bindings")
	}
	results, err := readReviewCheckResults(ctx, q, request)
	if err != nil {
		return err
	}
	_, passed, err := core.ReviewCheckReport(request, results)
	if err != nil {
		return err
	}
	if verdict == "PASS" && !passed {
		return complexExecutionRule("Reviewer cannot pass failed additional checks")
	}
	step, err := q.GetClearDevComplexExecutionAgentStep(ctx, core.ReviewCheckFollowupID(review.ID))
	if err != nil {
		return err
	}
	var result core.LocalReviewResult
	if err := json.Unmarshal([]byte(step.FinalMessageText.String), &result); err != nil {
		return err
	}
	finalBinding := review.ReviewerRoleBindingID
	if request.ReplacementRecoveryID != "" {
		_, binding, err := mailReplacementCheckSource(ctx, q, request)
		if err != nil {
			return err
		}
		finalBinding = binding
	} else if request.RequestAttemptID != "" || request.RequestResultID != "" {
		return complexExecutionRule("replacement request source is incomplete")
	}
	if step.RoleBindingID != finalBinding || step.RequestID != core.ReviewCheckFollowupID(review.ID) || step.SendStatus != "SETTLED" || result.Kind != "LOCAL_REVIEW" || result.Verdict != verdict {
		return complexExecutionRule("Reviewer checks require the exact final response")
	}
	return nil
}
