package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func mailAttemptSlots(ctx context.Context, q *gen.Queries, runID string) ([]core.MailAttemptSlot, error) {
	rows, err := q.ListClearDevMailAttemptSlots(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]core.MailAttemptSlot, 0, len(rows))
	for _, r := range rows {
		out = append(out, core.MailAttemptSlot{DispatchID: r.DispatchID, ExecutionRunID: r.ExecutionRunID, TaskID: r.TaskID, Round: int(r.Round), Kind: r.AttemptKind, GrantRequestID: r.GrantRequestID.String, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

func mailAttemptSlotsForTask(ctx context.Context, q *gen.Queries, runID, taskID string) ([]core.MailAttemptSlot, error) {
	slots, err := mailAttemptSlots(ctx, q, runID)
	if err != nil {
		return nil, err
	}
	out := make([]core.MailAttemptSlot, 0, len(slots))
	for _, slot := range slots {
		if slot.TaskID == taskID {
			out = append(out, slot)
		}
	}
	return out, nil
}

// ListClearDevMailAttempts exposes immutable counters without interpreting model prose.
func (s *Store) ListClearDevMailAttempts(ctx context.Context, runID string) ([]core.MailAttemptSlot, error) {
	return mailAttemptSlots(ctx, s.qr, runID)
}

func latestMailRework(ctx context.Context, q *gen.Queries, runID, dispatchID string) (bool, error) {
	reviews, err := q.ListClearDevComplexExecutionReviews(ctx, runID)
	if err != nil {
		return false, err
	}
	for _, r := range reviews {
		if r.TaskAttemptID != dispatchID {
			continue
		}
		if r.Status == "SETTLED" && r.Verdict.String == "REWORK" {
			return true, nil
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, runID)
		if err != nil {
			return false, err
		}
		if core.BoundedMailAttempts(complexExecutionRunFromGen(run)) {
			replacement, err := mailReplacementResultForReview(ctx, q, runID, r.ID)
			if err != nil {
				return false, err
			}
			if replacement != nil && replacement.Verdict == core.LocalReviewRework {
				return true, nil
			}
		}
	}
	return false, nil
}

func insertMailAttemptSlot(ctx context.Context, q *gen.Queries, d core.ComplexExecutionDispatch) error {
	row, err := q.GetClearDevComplexExecutionRun(ctx, d.ExecutionRunID)
	if err != nil {
		return err
	}
	if !core.BoundedMailAttempts(complexExecutionRunFromGen(row)) {
		return nil
	}
	slots, err := mailAttemptSlotsForTask(ctx, q, row.ID, d.ComplexExecutionTaskID)
	if err != nil {
		return err
	}
	if d.Round != len(slots) {
		return complexExecutionRule("mail attempt round does not match immutable task counters")
	}
	rework := false
	previous := ""
	if len(slots) > 0 {
		previous = slots[len(slots)-1].DispatchID
		rework, err = latestMailRework(ctx, q, row.ID, previous)
		if err != nil {
			return err
		}
	}
	kind, allowed := core.NextAutomaticMailAttempt(slots, rework)
	grant := ""
	if !allowed {
		request, err := q.GetClearDevHumanDecisionRequest(ctx, mailExtraRequestID(row.ID))
		if err != nil {
			return err
		}
		binding, err := core.ParseExtraMailAttemptBinding([]byte(request.BindingJson))
		if err != nil {
			return err
		}
		if request.Status != "RESOLVED" || request.Decision != "APPROVE" || binding.ExecutionRunID != row.ID || binding.TaskID != d.ComplexExecutionTaskID || binding.DispatchID != previous || binding.NextRound != d.Round {
			return complexExecutionRule("mail attempt has no exact extra authorization")
		}
		kind, grant = core.MailAttemptHumanExtra, request.ID
	}
	return q.InsertClearDevMailAttemptSlot(ctx, gen.InsertClearDevMailAttemptSlotParams{DispatchID: d.ID, ExecutionRunID: row.ID, TaskID: d.ComplexExecutionTaskID, Round: int64(d.Round), AttemptKind: kind, GrantRequestID: nullableString(grant), CreatedAt: d.CreatedAt})
}

// An observed failed fixed check, settled Reviewer REWORK, or a freeze that
// produced no new SHA after an earlier frozen candidate may consume remaining
// automatic mail attempts. Scope/plan/security failures and infra stay stopped.
func boundedMailFailure(ctx context.Context, q *gen.Queries, a gen.CleardevComplexExecutionTaskAttempt, infrastructure bool, reason core.ReasonCode, next string) (string, core.ReasonCode, error) {
	run, err := q.GetClearDevComplexExecutionRun(ctx, a.ExecutionRunID)
	if err != nil {
		return next, reason, err
	}
	if !core.BoundedMailAttempts(complexExecutionRunFromGen(run)) {
		return next, reason, nil
	}
	if infrastructure {
		return "BLOCKED", reason, nil
	}
	rework, err := latestMailRework(ctx, q, run.ID, a.ID)
	if err != nil {
		return next, reason, err
	}
	if reason == "REVIEW_BLOCKED" || reason == "BUILDER_BLOCKED" || reason == "CHECK_TIMEOUT" {
		return "BLOCKED", reason, nil
	}
	if !rework && reason != "CHECK_FAILED" && reason != "CANDIDATE_INVALID" {
		return "NEEDS_HUMAN", reason, nil
	}
	if !rework && reason == "CHECK_FAILED" {
		checks, readErr := q.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
		if readErr != nil {
			return next, reason, readErr
		}
		failed := false
		for _, check := range checks {
			failed = failed || (check.TaskAttemptID == a.ID && check.Status == "SETTLED" && check.Result.String == "FAIL" && check.ReasonCode == "CHECK_FAILED" && check.TimedOut.Valid && !check.TimedOut.Bool)
		}
		if !failed {
			return "NEEDS_HUMAN", reason, nil
		}
	}
	// A freeze with no new SHA can still offer Extra from the last frozen
	// candidate, but this dispatch has no SHA to hand to an automatic REWORK.
	hasCurrent := true
	if _, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(a.ID)); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return next, reason, err
		}
		hasCurrent = false
		if reason != "CANDIDATE_INVALID" {
			return "NEEDS_HUMAN", reason, nil
		}
		if _, lastErr := lastMailCandidateForTask(ctx, q, run.ID, a.TaskMappingID); lastErr != nil {
			if errors.Is(lastErr, sql.ErrNoRows) {
				return "NEEDS_HUMAN", reason, nil
			}
			return next, reason, lastErr
		}
	}
	taskSlots, err := mailAttemptSlotsForTask(ctx, q, run.ID, a.TaskMappingID)
	if err != nil {
		return next, reason, err
	}
	if _, allowed := core.NextAutomaticMailAttempt(taskSlots, rework); allowed {
		if !hasCurrent {
			return "NEEDS_HUMAN", reason, nil
		}
		return "REWORK", reason, nil
	}
	allSlots, err := mailAttemptSlots(ctx, q, run.ID)
	if err != nil {
		return next, reason, err
	}
	for _, slot := range allSlots {
		if slot.Kind == core.MailAttemptHumanExtra {
			return "NEEDS_HUMAN", core.MailAttemptFinalLimitReason, nil
		}
	}
	return "NEEDS_HUMAN", core.MailAttemptLimitReason, nil
}

func mailWorkItemAttemptLimit(run gen.CleardevComplexExecutionRun, legacy int) int64 {
	if core.BoundedMailAttempts(complexExecutionRunFromGen(run)) {
		return 4
	}
	return int64(legacy)
}

func mailExtraRequestID(runID string) string { return "cleardev-mail-extra-attempt:" + runID }

func ensureMailExtraRequest(ctx context.Context, q *gen.Queries, runID, taskID, dispatchID string, at time.Time) error {
	run, err := q.GetClearDevComplexExecutionRun(ctx, runID)
	if err != nil {
		return err
	}
	if !core.BoundedMailAttempts(complexExecutionRunFromGen(run)) {
		return nil
	}
	taskSlots, err := mailAttemptSlotsForTask(ctx, q, runID, taskID)
	if err != nil {
		return err
	}
	if len(taskSlots) >= 5 {
		return nil
	}
	allSlots, err := mailAttemptSlots(ctx, q, runID)
	if err != nil {
		return err
	}
	for _, s := range allSlots {
		if s.Kind == core.MailAttemptHumanExtra {
			return nil
		}
	}
	requestID := mailExtraRequestID(runID)
	if _, err := q.GetClearDevHumanDecisionRequest(ctx, requestID); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	candidate, err := mailCandidateForExtra(ctx, q, runID, taskID, dispatchID)
	if err != nil {
		return err
	}
	binding := core.ExtraMailAttemptBinding{DevelopmentRequirementID: run.DevelopmentProjectID, RequirementVersionID: run.RequirementVersionID, RequirementVersionSHA256: run.RequirementSha256, ExecutionRunID: runID, PlanSHA256: run.PlanSha256, TaskID: taskID, DispatchID: dispatchID, CandidateSHA: candidate.CommitSha, NextRound: len(taskSlots)}
	raw, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	display := core.ExtraMailAttemptDisplay(binding, string(core.MailAttemptLimitReason))
	displayJSON, err := json.Marshal(display)
	if err != nil {
		return err
	}
	digest, err := core.HumanDecisionContentSHA256(core.HumanDecisionKindExtraMailAttempt, raw, display)
	if err != nil {
		return err
	}
	return q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{ID: requestID, DevelopmentProjectID: run.DevelopmentProjectID, DecisionKind: core.HumanDecisionKindExtraMailAttempt, BindingSchemaVersion: 1, BindingJson: string(raw), DisplayJson: string(displayJSON), ContentSha256: digest, CreatedAt: at})
}

func settleExtraMailAttempt(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*core.RuleError, error) {
	b, err := core.ParseExtraMailAttemptBinding(result.Binding)
	if err != nil {
		return nil, err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, b.ExecutionRunID)
	if err != nil {
		return nil, err
	}
	if !core.BoundedMailAttempts(complexExecutionRunFromGen(run)) || run.Status != "ACCEPTED" || run.DevelopmentProjectID != b.DevelopmentRequirementID || run.RequirementVersionID != b.RequirementVersionID || run.RequirementSha256 != b.RequirementVersionSHA256 || run.PlanSha256 != b.PlanSHA256 || request.ID != mailExtraRequestID(run.ID) {
		return nil, complexExecutionRule("extra attempt policy or plan is stale")
	}
	project, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
	if err != nil {
		return nil, err
	}
	version, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, run.DevelopmentProjectID)
	if err != nil {
		return nil, err
	}
	if project.CancelledAt.Valid || project.State == "PAUSED" || version.ID != run.RequirementVersionID || version.Sha256 != run.RequirementSha256 || version.TaskSetVersion != run.AcceptedTaskSetVersion {
		return nil, complexExecutionRule("extra attempt requirement changed")
	}
	if stopped, err := activeDirectionStop(ctx, q, run.RequirementVersionID); err != nil {
		return nil, err
	} else if stopped {
		return nil, directionStoppedError()
	}
	if _, err := q.GetOpenClearDevRequirementVersion(ctx, run.DevelopmentProjectID); err == nil {
		return nil, complexExecutionRule("extra attempt has an unresolved requirement version")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if intent, err := q.GetClearDevDirectionIntentByVersion(ctx, run.RequirementVersionID); err == nil {
		decision, err := q.GetClearDevHumanDecisionRequestByDirectionRequestID(ctx, intent.DirectionRequestID)
		if err != nil || decision.Status != "RESOLVED" || decision.Decision != "REJECT" {
			return nil, complexExecutionRule("extra attempt is blocked by an unresolved direction intent")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	taskSlots, err := mailAttemptSlotsForTask(ctx, q, run.ID, b.TaskID)
	if err != nil {
		return nil, err
	}
	if len(taskSlots) != b.NextRound || len(taskSlots) == 0 || taskSlots[len(taskSlots)-1].DispatchID != b.DispatchID {
		return nil, complexExecutionRule("extra attempt is no longer the next task round")
	}
	allSlots, err := mailAttemptSlots(ctx, q, run.ID)
	if err != nil {
		return nil, err
	}
	for _, s := range allSlots {
		if s.Kind == core.MailAttemptHumanExtra {
			return nil, complexExecutionRule("extra attempt already consumed")
		}
	}
	failed, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, b.DispatchID)
	if err != nil {
		return nil, err
	}
	candidate, err := mailCandidateForExtra(ctx, q, run.ID, b.TaskID, failed.ID)
	if err != nil {
		return nil, err
	}
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, b.TaskID)
	if err != nil {
		return nil, err
	}
	if failed.ExecutionRunID != run.ID || failed.TaskMappingID != b.TaskID || failed.Status != "NEEDS_HUMAN" || failed.ReasonCode != string(core.MailAttemptLimitReason) || candidate.CommitSha != b.CandidateSHA || item.State != "NEEDS_HUMAN" {
		return nil, complexExecutionRule("extra attempt failure or candidate changed")
	}
	rows, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{ID: request.ID, Decision: string(result.Decision), ResolvedAt: nullableTime(at)})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, complexExecutionRule("extra attempt decision already settled")
	}
	outcome := core.HumanDecisionDispatchRejected
	if result.Decision == core.HumanDecisionApprove {
		outcome = core.HumanDecisionDispatchApproved
		rows, err = q.UpdateClearDevDevelopmentTaskStateCAS(ctx, gen.UpdateClearDevDevelopmentTaskStateCASParams{ID: item.ID, ExpectedState: item.State, ExpectedReworkCount: item.ReworkCount, NextState: "REWORK", ReworkCount: item.ReworkCount + 1, UpdatedAt: at})
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			return nil, complexExecutionRule("extra attempt task changed")
		}
	}
	if err := consumeDispatchCAS(ctx, q, result.Nonce, result.DesktopRunID, outcome, at); err != nil {
		return nil, err
	}
	event := core.RequirementEvent{AOProjectID: project.AoProjectID, DevelopmentRequirementID: project.ID, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: request.ID, Action: core.ActionSettleHumanDecision, PreviousState: "PENDING", TargetState: "RESOLVED", Outcome: core.EventAccepted, Source: core.EventSourceHumanDecision, CreatedAt: at}
	if err := insertClearDevEvent(ctx, q, event); err != nil {
		return nil, err
	}
	sequence, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: request.ID, Action: string(core.ActionSettleHumanDecision)})
	if err != nil {
		return nil, err
	}
	return nil, q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: request.ID, Decision: string(result.Decision), EventSequence: sequence, CreatedAt: at})
}

func mailCandidateForExtra(ctx context.Context, q *gen.Queries, runID, taskID, dispatchID string) (gen.CleardevCandidateCommit, error) {
	candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(dispatchID))
	if err == nil {
		return candidate, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return gen.CleardevCandidateCommit{}, err
	}
	return lastMailCandidateForTask(ctx, q, runID, taskID)
}

func lastMailCandidateForTask(ctx context.Context, q *gen.Queries, runID, taskID string) (gen.CleardevCandidateCommit, error) {
	attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, runID)
	if err != nil {
		return gen.CleardevCandidateCommit{}, err
	}
	var last gen.CleardevCandidateCommit
	found := false
	for _, attempt := range attempts {
		if attempt.TaskMappingID != taskID {
			continue
		}
		candidate, candidateErr := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(attempt.ID))
		if errors.Is(candidateErr, sql.ErrNoRows) {
			continue
		}
		if candidateErr != nil {
			return gen.CleardevCandidateCommit{}, candidateErr
		}
		last, found = candidate, true
	}
	if !found {
		return gen.CleardevCandidateCommit{}, sql.ErrNoRows
	}
	return last, nil
}

func mailInvalidCandidateCanRetry(ctx context.Context, q *gen.Queries, runID, taskID, reason string) (bool, error) {
	if reason != "CANDIDATE_INVALID" {
		return false, nil
	}
	if _, err := lastMailCandidateForTask(ctx, q, runID, taskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	slots, err := mailAttemptSlotsForTask(ctx, q, runID, taskID)
	if err != nil {
		return false, err
	}
	_, allowed := core.NextAutomaticMailAttempt(slots, false)
	return allowed, nil
}
