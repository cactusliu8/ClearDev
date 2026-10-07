package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func fixedRecoveryCurrent(ctx context.Context, q *gen.Queries, r core.FixedRecoveryRequest, at time.Time) error {
	run, err := q.GetClearDevComplexExecutionRun(ctx, r.ExecutionRunID)
	if err != nil {
		return err
	}
	if run.Status != "ACCEPTED" || run.DevelopmentProjectID != r.RequirementID || (run.Mode != "STANDARD" && run.Mode != "PARALLEL") {
		return complexExecutionRule("fixed recovery requires current complex execution")
	}
	req, err := q.GetClearDevRequirement(ctx, r.RequirementID)
	if err != nil {
		return err
	}
	if req.CancelledAt.Valid || req.State == "PAUSED" || req.AoProjectID != r.ProjectID {
		return complexExecutionRule("fixed recovery requirement stopped or changed")
	}
	version, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, r.RequirementID)
	if err != nil {
		return err
	}
	if version.ID != run.RequirementVersionID || version.State != "APPROVED" || version.SupersededByID.Valid || version.TaskSetVersion != run.AcceptedTaskSetVersion {
		return complexExecutionRule("fixed recovery version changed")
	}
	if stopped, err := activeDirectionStop(ctx, q, run.RequirementVersionID); err != nil {
		return err
	} else if stopped {
		return directionStoppedError()
	}
	if r.CheckRunID != "" {
		original, err := q.GetClearDevComplexExecutionCheckRun(ctx, r.CheckRunID)
		if err != nil {
			return err
		}
		retry, err := q.GetClearDevComplexExecutionCheckRun(ctx, r.RetryCheckRunID)
		if err != nil {
			return err
		}
		specs, err := q.ListClearDevComplexExecutionCheckSpecs(ctx, r.ExecutionRunID)
		if err != nil {
			return err
		}
		var spec gen.CleardevComplexExecutionCheckSpec
		for _, item := range specs {
			if item.ID == original.CheckSpecID {
				spec = item
			}
		}
		if spec.ExecutionRunID != r.ExecutionRunID || spec.CheckKind != "INTEGRATION" || original.RetryOrdinal != 0 || original.ReasonCode != "CHECKER_UNAVAILABLE" || !original.SettledAt.Valid || original.Result.String == "PASS" || retry.RetryOrdinal != 1 || retry.CheckSpecID != original.CheckSpecID || retry.TaskAttemptID != original.TaskAttemptID || retry.CandidateCommitID != original.CandidateCommitID || retry.CandidateCommitSha != r.ExpectedSHA || original.CandidateCommitSha != r.ExpectedSHA {
			return complexExecutionRule("fixed infrastructure retry target changed")
		}
		return nil
	}
	if r.LogicalStepID == "" {
		return complexExecutionRule("fixed recovery requires an exact logical step")
	}
	first, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{LogicalStepID: r.LogicalStepID, AttemptNumber: 1})
	if err != nil {
		return err
	}
	if first.ID != r.FirstAttemptID || first.DevelopmentProjectID != r.RequirementID || first.RoleBindingID != r.RoleBindingID || first.AoSessionID != r.SessionID || first.PromptSha256 != r.PromptSHA256 || !core.FixedRecoveryStepAllowed(core.AgentStepCategory(first.StepCategory), core.AgentStepKind(first.StepKind)) {
		return complexExecutionRule("fixed recovery original attempt changed")
	}
	if _, err := q.GetClearDevAgentStepAttempt(ctx, gen.GetClearDevAgentStepAttemptParams{LogicalStepID: r.LogicalStepID, AttemptNumber: 2}); err == nil {
		return complexExecutionRule("second attempt already owns recovery")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	event, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, first.ID)
	if err != nil {
		return err
	}
	second := agentStepAttemptToDomain(first)
	second.ID += "-recovery"
	second.AttemptNumber = 2
	second.TriggerFailureEventID = r.FailureEventID
	second.ClientMessageID += ":attempt:2"
	second.RequestedAt = at
	second.CreatedAt = &at
	second.RequestedAtSemantics = core.AttemptTimeActualCreation
	if event.ID != r.FailureEventID || event.TurnID == "" || !core.FixedRecoveryFailureAllowed(agentAttemptEventToDomain(event).FailureCategory) || !core.ValidSecondAgentAttempt(agentStepAttemptToDomain(first), agentAttemptEventToDomain(event), agentAttemptEventToDomain(event), second) {
		return complexExecutionRule("fixed recovery requires latest retryable terminal evidence")
	}
	bindings, err := q.GetClearDevMessageExecutionBinding(ctx, r.LogicalStepID)
	if err != nil {
		return err
	}
	if len(bindings) != 1 || bindings[0].ExecutionRunID != r.ExecutionRunID || bindings[0].TaskID != r.TaskID || (bindings[0].Role != "BUILDER" && bindings[0].Role != "REVIEWER") {
		return complexExecutionRule("fixed recovery task or role mismatch")
	}
	binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, r.RoleBindingID)
	if err != nil {
		return err
	}
	if binding.Status != "BOUND" || binding.AoSessionID.String != r.SessionID {
		return complexExecutionRule("fixed recovery original role ended or changed")
	}
	if first.StepCategory == string(core.AgentStepCategoryComplexExecution) {
		step, err := q.GetClearDevComplexExecutionAgentStep(ctx, r.LogicalStepID)
		if err != nil {
			return err
		}
		if step.SendStatus != "PENDING" && step.SendStatus != "SENT" {
			return complexExecutionRule("fixed recovery logical step already settled")
		}
	} else {
		step, err := q.GetClearDevComplexExceptionAgentStep(ctx, r.LogicalStepID)
		if err != nil {
			return err
		}
		if step.SendStatus != "PENDING" && step.SendStatus != "SENT" {
			return complexExecutionRule("fixed recovery continuation already settled")
		}
	}
	dispatch, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, r.DispatchID)
	if err != nil {
		return err
	}
	candidate, candidateErr := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(r.DispatchID))
	if r.CandidateID == "" {
		if !errors.Is(candidateErr, sql.ErrNoRows) || r.ExpectedSHA != dispatch.BaseCommitSha {
			return complexExecutionRule("fixed recovery baseline changed or candidate already exists")
		}
	} else if candidateErr != nil || candidate.ID != r.CandidateID || candidate.CommitSha != r.CandidateSHA || r.ExpectedSHA != r.CandidateSHA {
		return complexExecutionRule("fixed recovery candidate changed")
	}
	if dispatch.ExecutionRunID != r.ExecutionRunID || dispatch.TaskMappingID != r.TaskID || (dispatch.Status != "RUNNING" && dispatch.Status != "REVIEWING") {
		return complexExecutionRule("fixed recovery dispatch is not active")
	}
	versionBudget, err := q.GetClearDevMessageBudgetVersion(ctx, r.RequirementID)
	if err != nil {
		return err
	}
	if versionBudget.Version != string(core.MessageBudgetV1) {
		return messageBudgetError(core.ReasonMessageBudgetUnknown, "fixed recovery cannot grant historical usage new messages")
	}
	budget, err := resolveMessageRoleBudget(ctx, q, agentStepAttemptToDomain(first))
	if err != nil {
		return err
	}
	count, err := q.CountClearDevRoleMessages(ctx, nullableString(budget.ID))
	if err != nil {
		return err
	}
	if budget.ID == "" || count >= (budget.MaxTurns+budget.AuthorizedExtraTurns)*5 {
		return messageBudgetError(core.ReasonMessageBudgetExhausted, "fixed recovery target message budget exhausted")
	}
	if r.ReviewID != "" {
		review, err := q.GetClearDevComplexExecutionReview(ctx, r.ReviewID)
		if err != nil {
			return err
		}
		if review.AgentStepID != r.LogicalStepID || review.TaskAttemptID != r.DispatchID || review.ReviewerRoleBindingID != r.RoleBindingID || review.CandidateCommitID != r.CandidateID || review.ReviewPacketJson != r.ReviewPacketJSON || review.ReviewPacketSha256 != r.ReviewPacketSHA256 || review.Verdict.Valid {
			return complexExecutionRule("fixed recovery original review changed or has a verdict")
		}
	}
	return nil
}

// EnsureClearDevFixedRecoveryRequest claims the original logical step before any external effect.
func (s *Store) EnsureClearDevFixedRecoveryRequest(ctx context.Context, r core.FixedRecoveryRequest) (core.FixedRecoveryRequest, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err := s.inTx(ctx, "request fixed recovery", func(q *gen.Queries) error {
		old, err := q.GetClearDevFixedRecoveryRequest(ctx, r.ID)
		if err == nil {
			var saved core.FixedRecoveryRequest
			if err := json.Unmarshal([]byte(old.RequestJson), &saved); err != nil {
				return err
			}
			r.CreatedAt = saved.CreatedAt
			if !reflect.DeepEqual(r, saved) {
				return complexExecutionRule("fixed recovery request changed")
			}
			r = saved
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := fixedRecoveryCurrent(ctx, q, r, r.CreatedAt); err != nil {
			return err
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return q.InsertClearDevFixedRecoveryRequest(ctx, gen.InsertClearDevFixedRecoveryRequestParams{ID: r.ID, ExecutionRunID: r.ExecutionRunID, LogicalStepID: nullableString(r.LogicalStepID), CheckRunID: nullableString(r.CheckRunID), FailureEventID: nullableString(r.FailureEventID), RequestJson: string(raw), CreatedAt: r.CreatedAt})
	})
	return r, err
}

// ClaimClearDevFixedRecoveryAction elects one caller; replays never repeat the external operation.
func (s *Store) ClaimClearDevFixedRecoveryAction(ctx context.Context, c core.FixedRecoveryClaim) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	created := false
	err := s.inTx(ctx, "claim fixed recovery", func(q *gen.Queries) error {
		if old, err := q.GetClearDevFixedRecoveryClaim(ctx, c.RequestID); err == nil {
			if old.ProposalID != c.ProposalID || old.Action != c.Action || old.OperationID != c.OperationID {
				return complexExecutionRule("fixed recovery operation changed")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		row, err := q.GetClearDevFixedRecoveryRequest(ctx, c.RequestID)
		if err != nil {
			return err
		}
		var r core.FixedRecoveryRequest
		if err := json.Unmarshal([]byte(row.RequestJson), &r); err != nil {
			return err
		}
		if _, err := q.GetClearDevFixedRecoveryRefusal(ctx, r.ID); err == nil {
			return complexExecutionRule("fixed recovery was refused")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := fixedRecoveryCurrent(ctx, q, r, c.ClaimedAt); err != nil {
			return err
		}
		if c.OperationID != r.ID+":operation" || (r.CheckRunID != "" && c.Action != core.ComplexRecoveryActionRetryInfraCheck) || (r.CheckRunID == "" && c.Action != core.ComplexRecoveryActionRestoreSession && (c.Action != core.ComplexRecoveryActionRebuildReviewer || r.ReviewID == "")) {
			return complexExecutionRule("fixed recovery action is not authorized for target")
		}
		proposals, err := q.ListClearDevComplexExceptionRecoveryActions(ctx, r.ExecutionRunID)
		if err != nil {
			return err
		}
		valid := false
		for _, p := range proposals {
			if p.ID == c.ProposalID && p.TriggerFactID == fixedRecoveryTriggerID(r) && p.Action == c.Action && p.Outcome == "PASS" {
				valid = true
			}
		}
		if !valid {
			return complexExecutionRule("fixed recovery lacks exact passing proposal")
		}
		if err := q.InsertClearDevFixedRecoveryClaim(ctx, gen.InsertClearDevFixedRecoveryClaimParams{RequestID: c.RequestID, ProposalID: c.ProposalID, Action: c.Action, OperationID: c.OperationID, ClaimedAt: c.ClaimedAt}); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created && err == nil, err
}

// RecordClearDevFixedRecoveryResult appends the observed external outcome; it does not complete a task.
func (s *Store) RecordClearDevFixedRecoveryResult(ctx context.Context, r core.FixedRecoveryResult) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "record fixed recovery result", func(q *gen.Queries) error {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if old, err := q.GetClearDevFixedRecoveryResult(ctx, r.RequestID); err == nil {
			if old.ResultJson != string(raw) {
				return complexExecutionRule("fixed recovery result changed")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		requestRow, err := q.GetClearDevFixedRecoveryRequest(ctx, r.RequestID)
		if err != nil {
			return err
		}
		claim, err := q.GetClearDevFixedRecoveryClaim(ctx, r.RequestID)
		if err != nil {
			return err
		}
		var request core.FixedRecoveryRequest
		if err := json.Unmarshal([]byte(requestRow.RequestJson), &request); err != nil {
			return err
		}
		if r.Outcome == "PASS" {
			switch claim.Action {
			case core.ComplexRecoveryActionRestoreSession:
				if r.SessionID != request.SessionID || r.RoleBindingID != request.RoleBindingID || r.ProviderConversationID != request.ProviderConversationID || r.WorkspacePath != request.WorkspacePath {
					return complexExecutionRule("native recovery result changed original identity")
				}
			case core.ComplexRecoveryActionRebuildReviewer:
				binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, r.RoleBindingID)
				if err != nil {
					return err
				}
				if r.RoleBindingID != claim.OperationID+":reviewer" || binding.Status != "BOUND" || binding.AoSessionID.String != r.SessionID || r.SessionID == request.SessionID || binding.WorkspacePath != r.WorkspacePath {
					return complexExecutionRule("replacement recovery result changed new binding")
				}
			case core.ComplexRecoveryActionRetryInfraCheck:
				check, err := q.GetClearDevComplexExecutionCheckRun(ctx, r.CheckRunID)
				if err != nil {
					return err
				}
				if r.CheckRunID != request.RetryCheckRunID || check.Status != "SETTLED" || check.Result.String != "PASS" {
					return complexExecutionRule("infra recovery has no actual passing check")
				}
			}
		}
		return q.InsertClearDevFixedRecoveryResult(ctx, gen.InsertClearDevFixedRecoveryResultParams{RequestID: r.RequestID, Outcome: r.Outcome, ResultJson: string(raw), RecordedAt: r.RecordedAt})
	})
}

func fixedRecoveryEvidence(ctx context.Context, q *gen.Queries, runID string) ([]core.FixedRecoveryEvidence, error) {
	run, err := q.GetClearDevComplexExecutionRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListClearDevFixedRecoveryRequests(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]core.FixedRecoveryEvidence, 0, len(rows))
	for _, row := range rows {
		var e core.FixedRecoveryEvidence
		if err := json.Unmarshal([]byte(row.RequestJson), &e.Request); err != nil {
			return nil, err
		}
		if c, err := q.GetClearDevFixedRecoveryClaim(ctx, row.ID); err == nil {
			e.Claim = &core.FixedRecoveryClaim{RequestID: c.RequestID, ProposalID: c.ProposalID, Action: c.Action, OperationID: c.OperationID, ClaimedAt: c.ClaimedAt}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if r, err := q.GetClearDevFixedRecoveryResult(ctx, row.ID); err == nil {
			e.Result = &core.FixedRecoveryResult{}
			if err := json.Unmarshal([]byte(r.ResultJson), e.Result); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if r, err := q.GetClearDevReplacementReviewResult(ctx, row.ID); err == nil {
			e.ReviewResult = &core.ReplacementReviewResult{}
			if err := json.Unmarshal([]byte(r.ResultJson), e.ReviewResult); err != nil {
				return nil, err
			}
			supported, supportErr := replacementReviewerRunSupported(complexExecutionRunFromGen(run))
			if supportErr != nil {
				return nil, supportErr
			}
			if supported {
				e.ReviewStep, err = validateMailReplacementVerdict(ctx, q, *e.ReviewResult)
				if err != nil {
					return nil, err
				}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if refusal, err := q.GetClearDevFixedRecoveryRefusal(ctx, row.ID); err == nil {
			e.Refusal = &core.FixedRecoveryRefusal{RequestID: refusal.RequestID, ReasonCode: refusal.ReasonCode, RecordedAt: refusal.RecordedAt}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// authorizedReplacementAttempt is the only identity exception: one stored recovery, one reviewer, attempt two.
func authorizedReplacementAttempt(ctx context.Context, q *gen.Queries, a core.AgentStepAttempt) (bool, error) {
	if a.AttemptNumber != 2 {
		return false, nil
	}
	row, err := q.GetClearDevFixedRecoveryForStep(ctx, nullableString(a.LogicalStepID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	claim, err := q.GetClearDevFixedRecoveryClaim(ctx, row.ID)
	if err != nil {
		return false, err
	}
	if claim.Action != core.ComplexRecoveryActionRebuildReviewer {
		return false, nil
	}
	stored, err := q.GetClearDevFixedRecoveryResult(ctx, row.ID)
	if err != nil {
		return false, err
	}
	var request core.FixedRecoveryRequest
	var result core.FixedRecoveryResult
	if err := json.Unmarshal([]byte(row.RequestJson), &request); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(stored.ResultJson), &result); err != nil {
		return false, err
	}
	if result.Outcome != "PASS" || request.ReviewID == "" || a.DevelopmentRequirementID != request.RequirementID || a.StepCategory != core.AgentStepCategoryComplexExecution || a.StepKind != core.AgentStepLocalReview || a.PromptSHA256 != request.PromptSHA256 || a.TriggerFailureEventID != request.FailureEventID || a.RoleBindingID != claim.OperationID+":reviewer" || result.RoleBindingID != a.RoleBindingID || result.SessionID != a.AOSessionID || a.AOSessionID == request.SessionID {
		return false, complexExecutionRule("replacement attempt changed its exact authorization")
	}
	binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, a.RoleBindingID)
	if err != nil {
		return false, err
	}
	if binding.ExecutionRunID != request.ExecutionRunID || binding.Role != "REVIEWER" || binding.ContinuationOfRoleBindingID.String != request.RoleBindingID || binding.CandidateCommitID.String != request.CandidateID || binding.BaseCommitSha != request.CandidateSHA || binding.AoSessionID.String != a.AOSessionID {
		return false, complexExecutionRule("replacement reviewer binding changed")
	}
	return true, nil
}

// RecordClearDevReplacementReviewResult preserves the original request and raw results.
func (s *Store) RecordClearDevReplacementReviewResult(ctx context.Context, r core.ReplacementReviewResult) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "settle replacement review", func(q *gen.Queries) error {
		if old, err := q.GetClearDevReplacementReviewResult(ctx, r.RecoveryRequestID); err == nil {
			var saved core.ReplacementReviewResult
			if err := json.Unmarshal([]byte(old.ResultJson), &saved); err != nil {
				return err
			}
			r.RecordedAt = saved.RecordedAt
			if !reflect.DeepEqual(r, saved) {
				return complexExecutionRule("replacement review result changed")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		recovery, err := q.GetClearDevFixedRecoveryRequest(ctx, r.RecoveryRequestID)
		if err != nil {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, recovery.ExecutionRunID)
		if err != nil {
			return err
		}
		domainRun := complexExecutionRunFromGen(run)
		supported, supportErr := replacementReviewerRunSupported(domainRun)
		if supportErr != nil {
			return supportErr
		}
		if supported {
			if _, err := validateMailReplacementVerdict(ctx, q, r); err != nil {
				return err
			}
			raw, err := json.Marshal(r)
			if err != nil {
				return err
			}
			return q.InsertClearDevReplacementReviewResult(ctx, gen.InsertClearDevReplacementReviewResultParams{RecoveryRequestID: r.RecoveryRequestID, OriginalReviewID: r.OriginalReviewID, AttemptID: r.AttemptID, ResultID: r.ResultID, Verdict: string(r.Verdict), ResultJson: string(raw), RecordedAt: r.RecordedAt})
		}
		a, err := q.GetClearDevAgentStepAttemptByID(ctx, r.AttemptID)
		if err != nil {
			return err
		}
		authorized, err := authorizedReplacementAttempt(ctx, q, agentStepAttemptToDomain(a))
		if err != nil {
			return err
		}
		if !authorized {
			return complexExecutionRule("replacement result lacks authorization")
		}
		requestRow, err := q.GetClearDevFixedRecoveryRequest(ctx, r.RecoveryRequestID)
		if err != nil {
			return err
		}
		var request core.FixedRecoveryRequest
		if err := json.Unmarshal([]byte(requestRow.RequestJson), &request); err != nil {
			return err
		}
		if request.ReviewID != r.OriginalReviewID || request.LogicalStepID != a.LogicalStepID {
			return complexExecutionRule("replacement result refers to another review")
		}
		raw, err := q.GetClearDevFixedRecoveryRawResult(ctx, r.ResultID)
		if err != nil {
			return err
		}
		parsed, err := q.GetClearDevAgentStepResultParse(ctx, r.ResultID)
		if err != nil {
			return err
		}
		if raw.AttemptID != a.ID || parsed.Conclusion != "VALID" {
			return complexExecutionRule("replacement raw result is not valid")
		}
		var verdict core.LocalReviewResult
		if err := json.Unmarshal([]byte(raw.RawMessageText), &verdict); err != nil {
			return err
		}
		if verdict.Verdict != string(r.Verdict) || verdict.Summary != r.Summary || verdict.ReasonCode != string(r.ReasonCode) {
			return complexExecutionRule("replacement result differs from raw verdict")
		}
		jsonResult, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return q.InsertClearDevReplacementReviewResult(ctx, gen.InsertClearDevReplacementReviewResultParams{RecoveryRequestID: r.RecoveryRequestID, OriginalReviewID: r.OriginalReviewID, AttemptID: r.AttemptID, ResultID: r.ResultID, Verdict: string(r.Verdict), ResultJson: string(jsonResult), RecordedAt: r.RecordedAt})
	})
}

func fixedRecoveryTriggerID(r core.FixedRecoveryRequest) string {
	if r.CheckRunID != "" {
		return r.CheckRunID
	}
	return r.FailureEventID
}

// RecordClearDevFixedRecoveryRefusal preserves a reason without pretending an external action ran.
func (s *Store) RecordClearDevFixedRecoveryRefusal(ctx context.Context, r core.FixedRecoveryRefusal) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "refuse fixed recovery", func(q *gen.Queries) error {
		if _, err := q.GetClearDevFixedRecoveryClaim(ctx, r.RequestID); err == nil {
			return complexExecutionRule("claimed operation cannot be rewritten as a refusal")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if old, err := q.GetClearDevFixedRecoveryRefusal(ctx, r.RequestID); err == nil {
			if old.ReasonCode != r.ReasonCode {
				return complexExecutionRule("fixed recovery refusal changed")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return q.InsertClearDevFixedRecoveryRefusal(ctx, gen.InsertClearDevFixedRecoveryRefusalParams{RequestID: r.RequestID, ReasonCode: r.ReasonCode, RecordedAt: r.RecordedAt})
	})
}
