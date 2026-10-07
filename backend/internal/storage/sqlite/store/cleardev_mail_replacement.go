package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// CheckClearDevMailReplacementActive rechecks the current requirement before
// any continued external check or message, including after a process restart.
func (s *Store) CheckClearDevMailReplacementActive(ctx context.Context, id string) error {
	request, _, _, err := mailReplacementContext(ctx, s.qr, id)
	if err != nil {
		return err
	}
	run, err := s.qr.GetClearDevComplexExecutionRun(ctx, request.ExecutionRunID)
	if err != nil {
		return err
	}
	if run.Status != "ACCEPTED" {
		return complexExecutionRule("replacement execution is not active")
	}
	version, err := s.qr.GetCurrentClearDevConfirmedRequirementVersion(ctx, run.DevelopmentProjectID)
	if err != nil {
		return err
	}
	if version.ID != run.RequirementVersionID || version.Sha256 != run.RequirementSha256 || version.TaskSetVersion != run.AcceptedTaskSetVersion {
		return complexExecutionRule("replacement requirement is stale")
	}
	return validateMailRequirementCanComplete(ctx, s.qr, run)
}

func replacementReviewerRunSupported(run core.ComplexExecutionRun) (bool, error) {
	if core.BoundedMailAttempts(run) {
		return true, nil
	}
	_, project, err := core.ProjectContractFromRun(run)
	return project, err
}

// All links are read from immutable facts. A replacement is not permission to
// rewrite the failed original review or to accept a verdict from another SHA.
func mailReplacementContext(ctx context.Context, q *gen.Queries, id string) (core.FixedRecoveryRequest, core.FixedRecoveryResult, gen.CleardevComplexExecutionReview, error) {
	var request core.FixedRecoveryRequest
	var result core.FixedRecoveryResult
	var review gen.CleardevComplexExecutionReview
	row, err := q.GetClearDevFixedRecoveryRequest(ctx, id)
	if err != nil {
		return request, result, review, err
	}
	if err := json.Unmarshal([]byte(row.RequestJson), &request); err != nil {
		return request, result, review, err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, row.ExecutionRunID)
	if err != nil {
		return request, result, review, err
	}
	supported, supportErr := replacementReviewerRunSupported(complexExecutionRunFromGen(run))
	if supportErr != nil {
		return request, result, review, supportErr
	}
	if !supported {
		return request, result, review, complexExecutionRule("replacement combination requires a supported immutable execution policy")
	}
	claim, err := q.GetClearDevFixedRecoveryClaim(ctx, id)
	if err != nil {
		return request, result, review, err
	}
	outcome, err := q.GetClearDevFixedRecoveryResult(ctx, id)
	if err != nil {
		return request, result, review, err
	}
	if err := json.Unmarshal([]byte(outcome.ResultJson), &result); err != nil {
		return request, result, review, err
	}
	review, err = q.GetClearDevComplexExecutionReview(ctx, request.ReviewID)
	if err != nil {
		return request, result, review, err
	}
	candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(review.TaskAttemptID))
	if err != nil {
		return request, result, review, err
	}
	binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, result.RoleBindingID)
	if err != nil {
		return request, result, review, err
	}
	if request.ID != id || request.ExecutionRunID != run.ID || request.RequirementID != run.DevelopmentProjectID || claim.Action != core.ComplexRecoveryActionRebuildReviewer || outcome.Outcome != "PASS" || result.Outcome != "PASS" || result.RequestID != id || result.RoleBindingID != claim.OperationID+":reviewer" || result.SessionID == request.SessionID || result.SessionID == "" ||
		request.ReviewID != review.ID || request.LogicalStepID != review.AgentStepID || request.DispatchID != review.TaskAttemptID || request.RoleBindingID != review.ReviewerRoleBindingID || request.CandidateID != candidate.ID || request.CandidateSHA != candidate.CommitSha || request.ReviewPacketSHA256 != review.ReviewPacketSha256 || request.ReviewPacketJSON != review.ReviewPacketJson || complexExecutionRawDigest([]byte(review.ReviewPacketJson)) != review.ReviewPacketSha256 ||
		binding.ExecutionRunID != run.ID || binding.Role != "REVIEWER" || binding.TaskMappingID.String != request.TaskID || binding.ContinuationOfRoleBindingID.String != request.RoleBindingID || binding.CandidateCommitID.String != candidate.ID || binding.BaseCommitSha != candidate.CommitSha || binding.AoSessionID.String != result.SessionID || binding.WorkspacePath != result.WorkspacePath || binding.SessionCreationIdempotencyKey != claim.OperationID+":reviewer-session" {
		return request, result, review, complexExecutionRule("replacement outcome has stale authorization, review, candidate or session")
	}
	return request, result, review, nil
}

// mailReplacementCheckSource validates the original request made by attempt two.
// Optional source IDs are control-plane fields, never model-selected authority.
func mailReplacementCheckSource(ctx context.Context, q *gen.Queries, request core.ReviewCheckRequest) (gen.CleardevAgentStepResult, string, error) {
	var raw gen.CleardevAgentStepResult
	recovery, result, review, err := mailReplacementContext(ctx, q, request.ReplacementRecoveryID)
	if err != nil {
		return raw, "", err
	}
	a, err := q.GetClearDevAgentStepAttemptByID(ctx, request.RequestAttemptID)
	if err != nil {
		return raw, "", err
	}
	authorized, err := authorizedReplacementAttempt(ctx, q, agentStepAttemptToDomain(a))
	if err != nil {
		return raw, "", err
	}
	raw, err = q.GetClearDevFixedRecoveryRawResult(ctx, request.RequestResultID)
	if err != nil {
		return raw, "", err
	}
	parsed, err := q.GetClearDevAgentStepResultParse(ctx, raw.ID)
	if err != nil {
		return raw, "", err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, recovery.ExecutionRunID)
	if err != nil {
		return raw, "", err
	}
	wire, parseErr := core.ParseExecutionReviewCheckRequest([]byte(raw.RawMessageText), complexExecutionRunFromGen(run))
	if !authorized || a.LogicalStepID != recovery.LogicalStepID || raw.AttemptID != a.ID || parsed.Conclusion != "VALID" || raw.RawMessageSha256 != complexExecutionRawDigest([]byte(raw.RawMessageText)) || parseErr != nil || !slices.Equal(wire.CheckIDs, request.CheckIDs) || request.ReviewID != review.ID || request.CandidateID != recovery.CandidateID || request.CandidateSHA != recovery.CandidateSHA || request.PacketSHA256 != recovery.ReviewPacketSHA256 || request.RequestStepID != review.AgentStepID || request.RequestedAt.IsZero() {
		return raw, "", complexExecutionRule("replacement check request differs from authorized raw result")
	}
	return raw, result.RoleBindingID, nil
}

// validateMailReplacementVerdict is used before writing the replacement result,
// when reconstructing history, and inside the protected completion transaction.
func validateMailReplacementVerdict(ctx context.Context, q *gen.Queries, r core.ReplacementReviewResult) (*core.AgentStep, error) {
	request, result, review, err := mailReplacementContext(ctx, q, r.RecoveryRequestID)
	if err != nil {
		return nil, err
	}
	a, err := q.GetClearDevAgentStepAttemptByID(ctx, r.AttemptID)
	if err != nil {
		return nil, err
	}
	raw, err := q.GetClearDevFixedRecoveryRawResult(ctx, r.ResultID)
	if err != nil {
		return nil, err
	}
	parsed, err := q.GetClearDevAgentStepResultParse(ctx, r.ResultID)
	if err != nil {
		return nil, err
	}
	if raw.AttemptID != a.ID || parsed.Conclusion != "VALID" || raw.RawMessageSha256 != complexExecutionRawDigest([]byte(raw.RawMessageText)) || r.OriginalReviewID != review.ID || a.RoleBindingID != result.RoleBindingID || a.AoSessionID != result.SessionID || a.DevelopmentProjectID != request.RequirementID || a.StepCategory != "COMPLEX_EXECUTION" || a.StepKind != "LOCAL_REVIEW" {
		return nil, complexExecutionRule("replacement verdict has a foreign raw result or attempt")
	}
	checks, found, err := readReviewCheckRequest(ctx, q, review.ID)
	if err != nil {
		return nil, err
	}
	if found {
		_, binding, sourceErr := mailReplacementCheckSource(ctx, q, checks)
		if sourceErr != nil {
			return nil, sourceErr
		}
		final, err := q.GetClearDevComplexExecutionAgentStep(ctx, core.ReviewCheckFollowupID(review.ID))
		if err != nil {
			return nil, err
		}
		if checks.ReplacementRecoveryID != request.ID || binding != a.RoleBindingID || a.LogicalStepID != final.ID || final.RoleBindingID != binding || final.PromptSha256 != a.PromptSha256 || final.SendStatus != "SETTLED" || final.TurnID.String != raw.TurnID || final.FinalMessageID.String != raw.FinalMessageID || final.FinalMessageText.String != raw.RawMessageText {
			return nil, complexExecutionRule("replacement checks require their exact new final response")
		}
	} else {
		authorized, err := authorizedReplacementAttempt(ctx, q, agentStepAttemptToDomain(a))
		if err != nil {
			return nil, err
		}
		if !authorized || a.LogicalStepID != review.AgentStepID {
			return nil, complexExecutionRule("replacement final response has no authorization")
		}
	}
	var packet struct {
		Diff []struct{ Path string } `json:"diff"`
	}
	if err := json.Unmarshal([]byte(review.ReviewPacketJson), &packet); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(packet.Diff))
	for _, p := range packet.Diff {
		paths = append(paths, p.Path)
	}
	verdict, err := core.ParseComplexExecutionLocalReviewResult([]byte(raw.RawMessageText), review.ID, request.CandidateID, request.CandidateSHA, request.ReviewPacketSHA256, paths)
	if err != nil {
		return nil, err
	}
	if verdict.Verdict != string(r.Verdict) || verdict.ReasonCode != string(r.ReasonCode) || verdict.Summary != r.Summary {
		return nil, complexExecutionRule("replacement verdict differs from its exact raw response")
	}
	if err := validateReviewerChecks(ctx, q, review, string(r.Verdict)); err != nil {
		return nil, err
	}
	at := raw.ObservedAt
	return &core.AgentStep{ID: a.LogicalStepID, RoleBindingID: a.RoleBindingID, Kind: core.AgentStepLocalReview, RequestID: review.ID, ClientMessageID: raw.ClientMessageID, PromptSHA256: a.PromptSha256, SendStatus: core.AgentStepSendStatusSettled, TurnID: raw.TurnID, FinalMessageID: raw.FinalMessageID, FinalMessageText: raw.RawMessageText, MessageSHA256: raw.RawMessageSha256, CompletedAt: &at}, nil
}

// Return only the Recovery role that performed this exact replacement. Later
// failures belong to the replacement and dispatch, not the original review.
// A proposal without a confirmed operation does not qualify for this exception.
func mailReplacementRecoveryBinding(ctx context.Context, q *gen.Queries, review gen.CleardevComplexExecutionReview) (string, error) {
	row, err := q.GetClearDevFixedRecoveryForStep(ctx, nullableString(review.AgentStepID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, row.ExecutionRunID)
	if err != nil {
		return "", err
	}
	supported, supportErr := replacementReviewerRunSupported(complexExecutionRunFromGen(run))
	if supportErr != nil {
		return "", supportErr
	}
	if !supported {
		return "", nil
	}
	claim, err := q.GetClearDevFixedRecoveryClaim(ctx, row.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if claim.Action != core.ComplexRecoveryActionRebuildReviewer {
		return "", nil
	}
	outcome, err := q.GetClearDevFixedRecoveryResult(ctx, row.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if outcome.Outcome != "PASS" {
		return "", nil
	}
	request, _, original, err := mailReplacementContext(ctx, q, row.ID)
	if err != nil {
		return "", err
	}
	if original.ID != review.ID {
		return "", complexExecutionRule("replacement belongs to another original review")
	}
	actions, err := q.ListClearDevComplexExceptionRecoveryActions(ctx, run.ID)
	if err != nil {
		return "", err
	}
	for _, action := range actions {
		if action.ID == claim.ProposalID && action.Action == claim.Action && action.Outcome == "PASS" && action.TriggerFactID == fixedRecoveryTriggerID(request) && action.OndemandBindingID != "" {
			return action.OndemandBindingID, nil
		}
	}
	return "", complexExecutionRule("replacement has no exact Recovery role")
}

func mailReplacementResultForReview(ctx context.Context, q *gen.Queries, runID, reviewID string) (*core.ReplacementReviewResult, error) {
	rows, err := q.ListClearDevFixedRecoveryRequests(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		var request core.FixedRecoveryRequest
		if err := json.Unmarshal([]byte(row.RequestJson), &request); err != nil {
			return nil, err
		}
		if request.ReviewID != reviewID {
			continue
		}
		saved, err := q.GetClearDevReplacementReviewResult(ctx, row.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var result core.ReplacementReviewResult
		if err := json.Unmarshal([]byte(saved.ResultJson), &result); err != nil {
			return nil, err
		}
		if _, err := validateMailReplacementVerdict(ctx, q, result); err != nil {
			return nil, err
		}
		return &result, nil
	}
	return nil, nil
}
