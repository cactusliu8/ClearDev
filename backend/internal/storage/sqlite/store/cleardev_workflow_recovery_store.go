package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func workflowRecoveryFromRow(r gen.CleardevWorkflowRecovery) core.WorkflowRecovery {
	return core.WorkflowRecovery{ID: r.ID, ExecutionRunID: r.ExecutionRunID, Action: r.Action, TargetID: r.TargetID, DispatchID: r.DispatchID, TaskID: r.TaskID, StepID: r.StepID, BindingID: r.BindingID, SuccessorID: r.SuccessorID, CandidateSHA: r.CandidateSha, ProviderConversationID: r.ProviderConversationID, WorkingTreeSHA256: r.WorkingTreeSha256, OriginalStoppedAt: r.OriginalStoppedAt, OriginalStatus: r.OriginalStatus, OriginalReason: r.OriginalReason, OriginalSummary: r.OriginalSummary, Supplement: r.Supplement, CreatedAt: r.CreatedAt}
}

// ApplyClearDevWorkflowRecovery records the old stop and its successor in one
// transaction. Original messages, checks and reviews are never rewritten.
func (s *Store) ApplyClearDevWorkflowRecovery(ctx context.Context, r core.WorkflowRecovery, final *core.RequirementFinalReview) error {
	r.Supplement = core.NormalizeWorkflowRecoverySupplement(r.Action, r.Supplement)
	if strings.TrimSpace(r.Supplement) == "" || len(r.Supplement) > 16000 || r.ID == "" || r.SuccessorID == "" {
		return complexExecutionRule("invalid recovery context")
	}
	if r.WorkingTreeSHA256 != "" && (!validSHA256Digest(r.WorkingTreeSHA256) || (r.Action != core.RecoveryContinueBuilder && r.Action != core.RecoveryRetryBuilderSession)) {
		return complexExecutionRule("unfinished context requires a Builder recovery and exact digest")
	}
	if final != nil {
		if err := s.validateFinalReviewPacketHistory(ctx, *final); err != nil {
			return err
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "continue stopped workflow", func(q *gen.Queries) error {
		if prior, err := q.GetClearDevWorkflowRecovery(ctx, r.ID); err == nil {
			if prior.ExecutionRunID != r.ExecutionRunID || prior.Action != r.Action || prior.TargetID != r.TargetID || prior.Supplement != r.Supplement {
				return complexExecutionRule("recovery request was reused with different context")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, r.ExecutionRunID)
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
		if r.Action == core.RecoveryRetryStage {
			return applyWorkflowStageRecovery(ctx, q, run, r, final)
		}
		a, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, r.DispatchID)
		if err != nil {
			return err
		}
		if a.ExecutionRunID != r.ExecutionRunID || a.TaskMappingID != r.TaskID || a.Status != r.OriginalStatus || a.ReasonCode != r.OriginalReason || !a.SettledAt.Valid || !a.SettledAt.Time.Equal(r.OriginalStoppedAt) || (a.Status != "BLOCKED" && a.Status != "NEEDS_HUMAN") {
			return complexExecutionRule("recovery no longer matches the stopped attempt")
		}
		attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, other := range attempts {
			if other.TaskMappingID == a.TaskMappingID && other.Round > a.Round {
				return complexExecutionRule("recovery targets an old attempt")
			}
		}
		item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, a.TaskMappingID)
		if err != nil {
			return err
		}
		if item.State != "BLOCKED" && item.State != "NEEDS_HUMAN" {
			return complexExecutionRule("task is no longer stopped")
		}
		next := "RUNNING"
		rework := item.ReworkCount
		switch r.Action {
		case core.RecoveryRetryBuilderSession:
			if r.TargetID != core.WorkflowBuilderRetryTarget(a.ID, a.SettledAt.Time) || (a.ReasonCode != "BUILDER_SPAWN_FAILED" && a.ReasonCode != "BUILDER_WORKTREE_DIRTY") || r.StepID != a.AgentStepID || r.BindingID != a.BuilderRoleBindingID {
				return complexExecutionRule("session retry does not match its never-sent stop")
			}
			if a.ReasonCode == "BUILDER_WORKTREE_DIRTY" && (r.WorkingTreeSHA256 == "" || r.ProviderConversationID == "") {
				return complexExecutionRule("unfinished workspace retry requires its original native identity and exact saved contents")
			}
			step, err := q.GetClearDevComplexExecutionAgentStep(ctx, a.AgentStepID)
			if err != nil {
				return err
			}
			if step.SendStatus != "PENDING" {
				return complexExecutionRule("session retry cannot replay a sent step")
			}
			unsent, err := builderSessionStepUnsent(ctx, q, run.ID, step.ID)
			if err != nil {
				return err
			}
			if !unsent {
				return complexExecutionRule("session retry has external delivery evidence")
			}
			if _, err = q.GetClearDevComplexExecutionCandidate(ctx, nullableString(a.ID)); !errors.Is(err, sql.ErrNoRows) {
				return complexExecutionRule("session retry already has a candidate")
			}
		case core.RecoveryContinueBuilder:
			if bridge, err := q.GetClearDevFailureCoordinationSource(ctx, a.ID+":planner-coordination"); err == nil {
				decision, err := currentPlannerRuntimeDecision(ctx, q, bridge.EventID)
				if err != nil || decision.Source != "PLANNER" || (decision.Outcome != core.PlannerRuntimeContinue && decision.Outcome != core.PlannerRuntimeAmend) || r.WorkingTreeSHA256 != bridge.WorkingTreeSha256 {
					return complexExecutionRule("failure continuation requires the applied original Planner decision and unchanged working bytes")
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if r.TargetID != a.ID || r.StepID != a.AgentStepID || r.BindingID != a.BuilderRoleBindingID || (a.ReasonCode != "BUILDER_BLOCKED" && a.ReasonCode != "BUILDER_NEEDS_HUMAN" && a.ReasonCode != "HUMAN_DECISION_REQUIRED" && a.ReasonCode != "BUILDER_RESULT_INVALID" && a.ReasonCode != "BUILDER_UNAVAILABLE" && a.ReasonCode != "CANDIDATE_INVALID" && a.ReasonCode != "REVIEW_BLOCKED") {
				return complexExecutionRule("Builder recovery requires its stopped result")
			}
			if a.ReasonCode == "REVIEW_BLOCKED" {
				candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(a.ID))
				if err != nil {
					return err
				}
				reviews, err := q.ListClearDevComplexExecutionReviews(ctx, run.ID)
				if err != nil {
					return err
				}
				verified := false
				for _, review := range reviews {
					if review.TaskAttemptID == a.ID && review.Status == "SETTLED" && review.Verdict.String == "BLOCKED" && review.CandidateCommitID == candidate.ID && review.ReasonCode == a.ReasonCode && review.SettledAt.Valid && review.ReviewPacketSha256 == complexExecutionRawDigest([]byte(review.ReviewPacketJson)) {
						source, err := q.GetClearDevComplexExecutionAgentStep(ctx, review.AgentStepID)
						if err != nil {
							return err
						}
						if peekKindForFailure(source.FinalMessageText.String) == core.ReviewCheckRequestKind {
							source, err = q.GetClearDevComplexExecutionAgentStep(ctx, core.ReviewCheckFollowupID(review.ID))
							if err != nil {
								return err
							}
						}
						verified = source.SendStatus == "SETTLED" && source.FinalMessageID.String == review.FinalMessageID.String && source.TurnID.String == review.TurnID.String && source.MessageSha256.String == complexExecutionRawDigest([]byte(source.FinalMessageText.String))
					}
				}
				if !verified || r.CandidateSHA != candidate.CommitSha {
					return complexExecutionRule("task repair requires the exact independently settled blocked review")
				}
			}
			if a.ReasonCode == "CANDIDATE_INVALID" {
				if _, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(a.ID)); !errors.Is(err, sql.ErrNoRows) {
					return complexExecutionRule("candidate handoff recovery requires no frozen candidate")
				}
			}
			step, err := q.GetClearDevComplexExecutionAgentStep(ctx, a.AgentStepID)
			if err != nil {
				return err
			}
			if a.ReasonCode == "BUILDER_RESULT_INVALID" || a.ReasonCode == "BUILDER_UNAVAILABLE" {
				summary, known, err := stoppedBuilderResultProof(ctx, q, run, a)
				if err != nil {
					return err
				}
				if !known || summary != r.OriginalSummary || r.ProviderConversationID == "" {
					return complexExecutionRule("invalid Builder recovery requires exact completed result evidence")
				}
				current, err := invalidBuilderRecoverySessionCurrent(ctx, q, r)
				if err != nil {
					return err
				}
				if !current {
					return complexExecutionRule("invalid Builder recovery lost its original native session")
				}
			} else if step.SendStatus != "SETTLED" || step.FinalMessageText.String != r.OriginalSummary {
				return complexExecutionRule("Builder result is not reliably settled")
			}
			if err := workflowRecoveryBudget(ctx, q, run.ID, a.TaskMappingID, "BUILDER", a.Round+1); err != nil {
				return err
			}
			next = "REWORK"
			rework = a.Round + 1
		case core.RecoveryRetryReview:
			review, readErr := q.GetClearDevComplexExecutionReview(ctx, r.TargetID)
			if errors.Is(readErr, sql.ErrNoRows) {
				binding, err := q.GetClearDevComplexExecutionRoleBinding(ctx, r.TargetID)
				if err != nil {
					return err
				}
				if binding.ID != r.BindingID || binding.ExecutionRunID != r.ExecutionRunID || binding.TaskMappingID.String != r.TaskID || binding.Status != "FAILED" || r.StepID != "" || (a.ReasonCode != "REVIEWER_UNAVAILABLE" && a.ReasonCode != "REVIEW_BRANCH_UNAVAILABLE") {
					return complexExecutionRule("reviewer retry requires a never-bound failed session")
				}
				reviews, err := q.ListClearDevComplexExecutionReviews(ctx, r.ExecutionRunID)
				if err != nil {
					return err
				}
				for _, prior := range reviews {
					if prior.ReviewerRoleBindingID == binding.ID {
						return complexExecutionRule("reviewer already has a delivered review")
					}
				}
			} else {
				if readErr != nil {
					return readErr
				}
				known := review.Status == "SETTLED" && (review.Verdict.String == "BLOCKED" || review.Verdict.String == "NEEDS_HUMAN")
				if review.Status == "FAILED" && review.ReasonCode == "REVIEWER_UNAVAILABLE" {
					step, err := q.GetClearDevComplexExecutionAgentStep(ctx, review.AgentStepID)
					if err != nil {
						return err
					}
					known = (step.SendStatus == "PENDING" || step.SendStatus == "FAILED") && !step.SentAt.Valid && !step.TurnID.Valid
					attempts, err := q.ListClearDevAgentStepAttempts(ctx, run.DevelopmentProjectID)
					if err != nil {
						return err
					}
					for _, attempt := range attempts {
						if attempt.LogicalStepID == step.ID {
							known = false
						}
					}
				}
				if review.TaskAttemptID != a.ID || !known || review.AgentStepID != r.StepID || review.ReviewerRoleBindingID != r.BindingID || review.Summary.String != r.OriginalSummary {
					return complexExecutionRule("review recovery requires the exact stopped review")
				}
			}
			if err := workflowRecoveryBudget(ctx, q, run.ID, a.TaskMappingID, "REVIEWER", 0); err != nil {
				return err
			}

		case core.RecoveryRetryCheck:
			if err := validateStoppedTechnicalRetry(ctx, q, run.DevelopmentProjectID, r.TargetID); err != nil {
				return err
			}
			check, err := q.GetClearDevComplexExecutionCheckRun(ctx, r.TargetID)
			if err != nil {
				return err
			}
			terminated := check.Status == "SETTLED" && check.ReasonCode == "CHECK_FAILED" && check.Result.String == "FAIL" && check.ExitCode.Valid && check.ExitCode.Int64 == 137 && check.TimedOut.Valid && !check.TimedOut.Bool
			retryable := (check.Status == "FAILED" && check.ReasonCode == "CHECKER_UNAVAILABLE") || terminated
			if check.TaskAttemptID != a.ID || !retryable || check.CandidateCommitSha != r.CandidateSHA || check.OutputSummary.String != r.OriginalSummary {
				return complexExecutionRule("only a known environment failure can retry the original check")
			}
			checks, err := q.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
			if err != nil {
				return err
			}
			for _, newer := range checks {
				if newer.TaskAttemptID == a.ID && newer.CheckSpecID == check.CheckSpecID && newer.RetryOrdinal > check.RetryOrdinal {
					return complexExecutionRule("check already has a successor")
				}
			}
		default:
			return complexExecutionRule("unknown workflow recovery action")
		}
		if err := insertWorkflowRecovery(ctx, q, r); err != nil {
			return err
		}
		switch r.Action {
		case core.RecoveryRetryReview:
			old, err := q.GetClearDevComplexExecutionRoleBinding(ctx, r.BindingID)
			if err != nil {
				return err
			}
			if old.Status != "ENDED" && old.Status != "FAILED" {
				return complexExecutionRule("original reviewer is not conclusively ended")
			}
			binding := core.ComplexExecutionRoleBinding{ID: r.SuccessorID, ExecutionRunID: r.ExecutionRunID, Role: core.StandardRoleReviewer, TaskMappingID: r.TaskID, CandidateCommitID: old.CandidateCommitID.String, ContinuationOfRoleBindingID: old.ID, SessionCreationIdempotencyKey: "cleardev-workflow-review:" + r.ID, Status: core.RoleBindingStatusRequested, RequestedAt: r.CreatedAt}
			if err := q.InsertClearDevComplexExecutionRoleBinding(ctx, complexExecutionRoleBindingParams(binding)); err != nil {
				return err
			}
		case core.RecoveryRetryCheck:
			check, err := q.GetClearDevComplexExecutionCheckRun(ctx, r.TargetID)
			if err != nil {
				return err
			}
			if err := q.InsertClearDevComplexExecutionCheckRun(ctx, gen.InsertClearDevComplexExecutionCheckRunParams{ID: r.SuccessorID, CheckSpecID: check.CheckSpecID, TaskAttemptID: check.TaskAttemptID, CandidateCommitID: check.CandidateCommitID, CandidateCommitSha: check.CandidateCommitSha, Status: "PENDING", CreatedAt: r.CreatedAt, RetryOrdinal: check.RetryOrdinal + 1}); err != nil {
				return err
			}
		}
		if r.Action != core.RecoveryContinueBuilder {
			status := "OBSERVED"
			if r.Action == core.RecoveryRetryBuilderSession {
				status = "RUNNING"
			}
			if _, err = q.AdvanceClearDevComplexExecutionTaskAttemptCAS(ctx, gen.AdvanceClearDevComplexExecutionTaskAttemptCASParams{ID: a.ID, ExpectedStatus: a.Status, Status: status}); err != nil {
				return err
			}
		}
		rows, err := q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount, NextState: next, ReworkCount: rework, UpdatedAt: r.CreatedAt})
		if err != nil {
			return err
		}
		if rows != 1 {
			return complexExecutionRule("recovery lost its stopped task")
		}
		return nil
	})
}

func insertWorkflowRecovery(ctx context.Context, q *gen.Queries, r core.WorkflowRecovery) error {
	return q.InsertClearDevWorkflowRecovery(ctx, gen.InsertClearDevWorkflowRecoveryParams{ID: r.ID, ExecutionRunID: r.ExecutionRunID, Action: r.Action, TargetID: r.TargetID, DispatchID: r.DispatchID, TaskID: r.TaskID, StepID: r.StepID, BindingID: r.BindingID, SuccessorID: r.SuccessorID, CandidateSha: r.CandidateSHA, ProviderConversationID: r.ProviderConversationID, WorkingTreeSha256: r.WorkingTreeSHA256, OriginalStoppedAt: r.OriginalStoppedAt, OriginalStatus: r.OriginalStatus, OriginalReason: r.OriginalReason, OriginalSummary: r.OriginalSummary, Supplement: r.Supplement, CreatedAt: r.CreatedAt})
}

func workflowRecoveryBudget(ctx context.Context, q *gen.Queries, run, task, role string, nextRound int64) error {
	budgets, err := q.ListClearDevComplexExceptionBudgets(ctx, run)
	if err != nil {
		return err
	}
	for _, b := range budgets {
		if b.ComplexExecutionTaskID.String == task && b.RoleKind == role && b.UsedTurns < b.MaxTurns+b.AuthorizedExtraTurns && (role != "BUILDER" || nextRound <= b.MaxReworkCount+b.AuthorizedExtraTurns) {
			return nil
		}
	}
	return complexExecutionRule("recovery needs an available role budget; it cannot grant extra turns")
}

func applyWorkflowStageRecovery(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, r core.WorkflowRecovery, final *core.RequirementFinalReview) error {
	old, err := q.GetClearDevRequirementFinalReview(ctx, run.ID)
	if err != nil {
		return err
	}
	retryable := old.Status == "SETTLED" && (old.Verdict.String == "BLOCKED" || old.Verdict.String == "NEEDS_HUMAN") || old.Status == "FAILED" && (old.ReasonCode == "REQUIREMENT_FINAL_REVIEWER_UNAVAILABLE" || old.ReasonCode == "REQUIREMENT_FINAL_REVIEW_BRANCH_UNAVAILABLE" || old.ReasonCode == "STAGE_TRIAL_REQUIRED" || old.ReasonCode == "REQUIREMENT_FINAL_REVIEW_RESULT_INVALID")
	if final == nil || old.ID != r.TargetID || final.ID != r.SuccessorID || old.CandidateCommitSha != final.CandidateCommitSHA || old.CandidateCommitSha != r.CandidateSHA || old.Status != r.OriginalStatus || !old.SettledAt.Time.Equal(r.OriginalStoppedAt) || old.ReasonCode != r.OriginalReason || old.Summary != r.OriginalSummary || !retryable {
		return complexExecutionRule("stage recovery no longer matches the stopped review")
	}
	rejected, err := finalReviewInputRejected(ctx, q, old.ID)
	if err != nil {
		return err
	}
	if rejected && r.StepID != old.ID+":step" || !rejected && r.StepID != "" {
		return complexExecutionRule("stage input recovery source changed")
	}
	if old.Status == "FAILED" && old.ReasonCode != "STAGE_TRIAL_REQUIRED" && !rejected {
		attempts, err := q.ListClearDevAgentStepAttempts(ctx, run.DevelopmentProjectID)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			if attempt.LogicalStepID == old.ID+":step" {
				return complexExecutionRule("old final review has an unresolved external delivery")
			}
		}
		if old.SentAt.Valid {
			return complexExecutionRule("old final review was sent")
		}
	}
	if err := validateFinalReviewPrerequisites(ctx, q, run, *final); err != nil {
		return err
	}
	var packet core.RequirementFinalReviewPacket
	if json.Unmarshal([]byte(final.ReviewPacketJSON), &packet) != nil || packet.Recovery == nil || *packet.Recovery != r {
		return complexExecutionRule("stage recovery packet lost its supplementary context")
	}
	if err := insertWorkflowRecovery(ctx, q, r); err != nil {
		return err
	}
	checks, err := json.Marshal(final.CheckRunIDs)
	if err != nil {
		return err
	}
	if string(checks) != old.CheckRunIdsJson {
		return complexExecutionRule("stage recovery changed the original checks")
	}
	return q.InsertClearDevRequirementFinalReview(ctx, gen.InsertClearDevRequirementFinalReviewParams{ID: final.ID, ExecutionRunID: final.ExecutionRunID, DevelopmentProjectID: final.DevelopmentRequirementID, RequirementVersionID: final.RequirementVersionID, RequirementSha256: final.RequirementSHA256, PlanID: final.PlanID, PlanSha256: final.PlanSHA256, CandidateCommitSha: final.CandidateCommitSHA, BaseCommitSha: final.BaseCommitSHA, SourceWorkspacePath: final.SourceWorkspacePath, ReviewPacketJson: final.ReviewPacketJSON, ReviewPacketSha256: final.ReviewPacketSHA256, PromptSha256: final.PromptSHA256, CheckRunIdsJson: string(checks), CreatedAt: final.CreatedAt})
}

// Granting another turn does not answer a Builder's question. Leave that stop
// intact until its separate bound supplementary-context request is received.
func workflowBuilderAwaitingSupplement(ctx context.Context, q *gen.Queries, runID, taskID string, round int) (bool, error) {
	run, err := q.GetClearDevComplexExecutionRun(ctx, runID)
	if err != nil {
		return false, err
	}
	_, project, err := core.ProjectContractFromRun(complexExecutionRunFromGen(run))
	if err != nil || !project {
		return false, err
	}
	attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, runID)
	if err != nil {
		return false, err
	}
	for _, a := range attempts {
		if a.TaskMappingID != taskID || int(a.Round) != round {
			continue
		}
		if (a.ReasonCode == "BUILDER_RESULT_INVALID" || a.ReasonCode == "BUILDER_UNAVAILABLE" || a.ReasonCode == "CANDIDATE_INVALID") && (a.Status == "BLOCKED" || a.Status == "NEEDS_HUMAN") {
			// A budget grant never supplies a replacement result or bypasses
			// the separate current-evidence/worktree recovery request.
			return true, nil
		}
		if (a.Status != "BLOCKED" && a.Status != "NEEDS_HUMAN") || (a.ReasonCode != "BUILDER_BLOCKED" && a.ReasonCode != "BUILDER_NEEDS_HUMAN" && a.ReasonCode != "HUMAN_DECISION_REQUIRED") {
			return false, nil
		}
		step, err := q.GetClearDevComplexExecutionAgentStep(ctx, a.AgentStepID)
		if err != nil {
			return false, err
		}
		return step.SendStatus == "SETTLED", nil
	}
	return false, nil
}

func peekKindForFailure(raw string) string {
	var envelope struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return ""
	}
	return envelope.Kind
}
