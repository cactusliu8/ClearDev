package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func requirementFinalReviewFromGen(row gen.CleardevRequirementFinalReview) (core.RequirementFinalReview, error) {
	checks, err := decodeJSONStringSlice(row.CheckRunIdsJson, "final review check IDs")
	if err != nil {
		return core.RequirementFinalReview{}, err
	}
	r := core.RequirementFinalReview{ID: row.ID, ExecutionRunID: row.ExecutionRunID,
		DevelopmentRequirementID: row.DevelopmentProjectID, RequirementVersionID: row.RequirementVersionID,
		RequirementSHA256: row.RequirementSha256, PlanID: row.PlanID, PlanSHA256: row.PlanSha256,
		CandidateCommitSHA: row.CandidateCommitSha, BaseCommitSHA: row.BaseCommitSha,
		SourceWorkspacePath: row.SourceWorkspacePath, ReviewPacketJSON: row.ReviewPacketJson,
		ReviewPacketSHA256: row.ReviewPacketSha256, PromptSHA256: row.PromptSha256,
		CheckRunIDs: checks, Status: row.Status, AOSessionID: row.AoSessionID.String,
		WorkspacePath: row.WorkspacePath.String, ResultID: row.ResultID.String,
		Verdict: row.Verdict.String, ReasonCode: core.ReasonCode(row.ReasonCode), Summary: row.Summary, CreatedAt: row.CreatedAt}
	var packet core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(row.ReviewPacketJson), &packet); err != nil {
		return core.RequirementFinalReview{}, err
	}
	if packet.PreviousReview != nil {
		r.PreviousReviewID = packet.PreviousReview.ReviewID
		r.AuthorityRequestID = packet.PreviousReview.AuthorityRequestID
	}
	if row.BoundAt.Valid {
		r.BoundAt = &row.BoundAt.Time
	}
	if row.SentAt.Valid {
		r.SentAt = &row.SentAt.Time
	}
	if row.SettledAt.Valid {
		r.SettledAt = &row.SettledAt.Time
	}
	return r, nil
}

// CreateClearDevRequirementFinalReview stores the immutable whole-requirement
// packet only after all tasks and the final integration checks are verified.
func (s *Store) CreateClearDevRequirementFinalReview(ctx context.Context, r core.RequirementFinalReview) error {
	if err := s.validateFinalReviewPacketHistory(ctx, r); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "request requirement final review", func(q *gen.Queries) error {
		run, err := q.GetClearDevComplexExecutionRun(ctx, r.ExecutionRunID)
		if err != nil {
			return err
		}
		if err := validateFinalReviewPrerequisites(ctx, q, run, r); err != nil {
			return err
		}
		if old, err := q.GetClearDevRequirementFinalReview(ctx, run.ID); err == nil {
			if old.ID == r.ID {
				if old.ReviewPacketSha256 != r.ReviewPacketSHA256 || old.PromptSha256 != r.PromptSHA256 || old.CandidateCommitSha != r.CandidateCommitSHA {
					return complexExecutionRule("final review request changed; V1 cannot restart final review")
				}
				return nil
			}
			// A different review id names a replaced candidate: the earlier
			// review keeps its recorded verdict and this one is recorded
			// beside it.
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		checks, err := json.Marshal(r.CheckRunIDs)
		if err != nil {
			return err
		}
		return q.InsertClearDevRequirementFinalReview(ctx, gen.InsertClearDevRequirementFinalReviewParams{
			ID: r.ID, ExecutionRunID: r.ExecutionRunID, DevelopmentProjectID: r.DevelopmentRequirementID,
			RequirementVersionID: r.RequirementVersionID, RequirementSha256: r.RequirementSHA256,
			PlanID: r.PlanID, PlanSha256: r.PlanSHA256, CandidateCommitSha: r.CandidateCommitSHA,
			BaseCommitSha: r.BaseCommitSHA, SourceWorkspacePath: r.SourceWorkspacePath,
			ReviewPacketJson: r.ReviewPacketJSON, ReviewPacketSha256: r.ReviewPacketSHA256,
			PromptSha256: r.PromptSHA256, CheckRunIdsJson: string(checks), CreatedAt: r.CreatedAt})
	})
}

// BindClearDevRequirementFinalReview binds exactly one independent session.
func (s *Store) BindClearDevRequirementFinalReview(ctx context.Context, id, session, workspace string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "bind requirement final reviewer", func(q *gen.Queries) error {
		row, err := q.GetClearDevRequirementFinalReviewByID(ctx, id)
		if err != nil {
			return err
		}
		if row.Status != "REQUESTED" {
			if row.AoSessionID.String == session && row.WorkspacePath.String == workspace {
				return nil
			}
			return complexExecutionRule("final reviewer binding changed")
		}
		if session == "" || workspace == "" || workspace == row.SourceWorkspacePath {
			return complexExecutionRule("final reviewer must have an independent workspace")
		}
		r, err := requirementFinalReviewFromGen(row)
		if err != nil {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, row.ExecutionRunID)
		if err != nil {
			return err
		}
		if err := validateFinalReviewPrerequisites(ctx, q, run, r); err != nil {
			return err
		}
		_, err = q.BindClearDevRequirementFinalReview(ctx, gen.BindClearDevRequirementFinalReviewParams{ID: id, AoSessionID: nullableString(session), WorkspacePath: nullableString(workspace), BoundAt: nullableTime(at)})
		return err
	})
}

// MarkClearDevRequirementFinalReviewSent follows the existing reserved message
// ledger. Replays never create another logical review or another reviewer.
func (s *Store) MarkClearDevRequirementFinalReviewSent(ctx context.Context, id string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "send requirement final review", func(q *gen.Queries) error {
		review, err := q.GetClearDevRequirementFinalReviewByID(ctx, id)
		if err != nil {
			return err
		}
		if review.Status == "SENT" || review.Status == "SETTLED" {
			return nil
		}
		if review.Status != "PENDING" {
			return complexExecutionRule("final review is not bound and pending")
		}
		sends, err := q.CountClearDevRequirementFinalReviewSends(ctx, id)
		if err != nil {
			return err
		}
		if sends == 0 {
			return complexExecutionRule("final review has no confirmed reserved provider send")
		}
		_, err = q.SendClearDevRequirementFinalReview(ctx, gen.SendClearDevRequirementFinalReviewParams{ID: id, SentAt: nullableTime(at)})
		return err
	})
}

// SettleClearDevRequirementFinalReview derives the verdict exclusively from
// the saved, parsed result of this review's actual bound provider turn.
func (s *Store) SettleClearDevRequirementFinalReview(ctx context.Context, id, turnID, messageID string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "settle requirement final review", func(q *gen.Queries) error {
		row, err := q.GetClearDevRequirementFinalReviewByID(ctx, id)
		if err != nil {
			return err
		}
		result, err := q.GetClearDevFinalReviewProviderResult(ctx, gen.GetClearDevFinalReviewProviderResultParams{ReviewID: id, TurnID: turnID, FinalMessageID: messageID})
		if err != nil {
			return err
		}
		parsed, err := core.ParseRequirementFinalReviewResult([]byte(result.RawMessageText))
		if err != nil || complexExecutionRawDigest([]byte(result.RawMessageText)) != result.RawMessageSha256 {
			return complexExecutionRule("final review provider result is invalid")
		}
		if row.Status == "SETTLED" {
			if row.ResultID.String == result.ID {
				return nil
			}
			return complexExecutionRule("final review verdict cannot be replaced")
		}
		if row.Status != "SENT" {
			return complexExecutionRule("final review has not been sent")
		}
		r, err := requirementFinalReviewFromGen(row)
		if err != nil {
			return err
		}
		run, err := q.GetClearDevComplexExecutionRun(ctx, row.ExecutionRunID)
		if err != nil {
			return err
		}
		if err := validateFinalReviewPrerequisites(ctx, q, run, r); err != nil {
			return err
		}
		_, err = q.SettleClearDevRequirementFinalReview(ctx, gen.SettleClearDevRequirementFinalReviewParams{
			ID: id, ResultID: nullableString(result.ID), Verdict: nullableString(parsed.Verdict),
			ReasonCode: "REQUIREMENT_FINAL_REVIEW_" + parsed.Verdict, Summary: parsed.Summary, SettledAt: nullableTime(at)})
		return err
	})
}

// FailClearDevRequirementFinalReview records a safe stop without rewriting any
// task review, Builder attempt or previous provider result.
func (s *Store) FailClearDevRequirementFinalReview(ctx context.Context, id string, reason core.ReasonCode, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "stop requirement final review", func(q *gen.Queries) error {
		_, err := q.FailClearDevRequirementFinalReview(ctx, gen.FailClearDevRequirementFinalReviewParams{ID: id, ReasonCode: string(reason), SettledAt: nullableTime(at)})
		return err
	})
}

func validateFinalReviewPrerequisites(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, r core.RequirementFinalReview) error {
	domainRun, err := effectiveProjectRun(ctx, q, run)
	if err != nil {
		return err
	}
	required, err := core.RequirementFinalReviewRequired(domainRun)
	if err != nil || !required || run.Status != "ACCEPTED" {
		return complexExecutionRule("final review requires its frozen accepted execution")
	}
	if r.PreviousReviewID != "" {
		if _, err := q.GetClearDevDirectionIntentByVersion(ctx, r.RequirementVersionID); err == nil {
			return complexExecutionRule("direction intent excludes final evidence recheck")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if err := core.ValidateRequirementFinalReviewBinding(r, domainRun, r.CandidateCommitSHA, r.CheckRunIDs); err != nil {
		return err
	}
	if err := validateFinalAttemptEvidence(ctx, q, r); err != nil {
		return err
	}
	if err := validateFinalReviewExceptionAuthority(ctx, q, run.ID, r); err != nil {
		return err
	}
	requirement, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
	if err != nil {
		return err
	}
	if requirement.CancelledAt.Valid || requirement.State == "PAUSED" {
		return complexExecutionRule("final review requirement is cancelled or paused")
	}
	version, err := q.GetCurrentClearDevConfirmedRequirementVersion(ctx, run.DevelopmentProjectID)
	if err != nil {
		return err
	}
	if version.ID != run.RequirementVersionID || version.Sha256 != run.RequirementSha256 || version.State != "APPROVED" || version.SupersededByID.Valid || version.TaskSetVersion != run.AcceptedTaskSetVersion {
		return complexExecutionRule("final review requirement is stale")
	}
	if stopped, err := activeDirectionStop(ctx, q, run.RequirementVersionID); err != nil {
		return err
	} else if stopped {
		return directionStoppedError()
	}
	tasks, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return err
	}
	verified, err := q.ListClearDevComplexExecutionVerifiedCandidates(ctx, run.ID)
	if err != nil {
		return err
	}
	// A rework round leaves an earlier verification row behind, so count the
	// latest verification per task instead of every recorded row.
	latestByTask := map[string]string{}
	for _, verification := range verified {
		latestByTask[verification.TaskMappingID] = verification.ID
	}
	if len(tasks) == 0 || len(latestByTask) != len(tasks) {
		return complexExecutionRule("final review requires every task verification")
	}
	var packet core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(r.ReviewPacketJSON), &packet); err != nil {
		return err
	}
	if len(packet.Tasks) != len(tasks) || len(packet.Verifications) != len(latestByTask) {
		return complexExecutionRule("final review packet omits tasks or verification evidence")
	}
	finalSHA := ""
	for _, task := range tasks {
		matched := false
		for _, v := range verified {
			if v.TaskMappingID != task.ID || v.ID != latestByTask[task.ID] {
				continue
			}
			if err := validateProjectAttemptRevision(ctx, q, v.TaskAttemptID); err != nil {
				return err
			}
			a, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, v.TaskAttemptID)
			if err != nil {
				return err
			}
			if a.Status != "VERIFIED" || a.ExecutionRunID != run.ID {
				return complexExecutionRule("final review task verification is stale")
			}
			candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(v.TaskAttemptID))
			if err != nil {
				return err
			}
			if task.ID == tasks[len(tasks)-1].ID {
				finalSHA = candidate.CommitSha
			}
			for _, evidence := range packet.Verifications {
				if evidence.ID == v.ID && evidence.DispatchID == v.TaskAttemptID && evidence.LocalReviewID == v.ReviewID && evidence.CandidateCommitSHA == candidate.CommitSha {
					matched = true
				}
			}
		}
		if !matched {
			return complexExecutionRule("final review packet does not contain every exact task verification")
		}
	}
	if run.Mode == "PARALLEL" {
		composition, err := finalParallelComposition(ctx, q, run.ID)
		if err != nil {
			return err
		}
		if composition.Status != "COMPOSED" {
			return complexExecutionRule("final review requires completed composition")
		}
		finalSHA = composition.OutputCommitSha
	}
	if finalSHA != r.CandidateCommitSHA {
		return complexExecutionRule("final review is not on the final candidate")
	}
	specs, err := q.ListClearDevComplexExecutionCheckSpecs(ctx, run.ID)
	if err != nil {
		return err
	}
	seenSpecs := make(map[string]bool)
	for _, id := range r.CheckRunIDs {
		check, err := q.GetClearDevComplexExecutionCheckRun(ctx, id)
		if err != nil {
			return err
		}
		if check.Status != "SETTLED" || check.Result.String != "PASS" || check.CandidateCommitSha != finalSHA {
			return complexExecutionRule("final review requires passing same-SHA checks")
		}
		found := false
		for _, spec := range specs {
			if spec.ID == check.CheckSpecID && spec.CheckKind == "INTEGRATION" && !seenSpecs[spec.ID] {
				found = true
				seenSpecs[spec.ID] = true
			}
		}
		if !found {
			return complexExecutionRule("final review check is foreign, repeated or not integration")
		}
	}
	for _, spec := range specs {
		if spec.CheckKind == "INTEGRATION" && !seenSpecs[spec.ID] {
			return complexExecutionRule("final review is missing a required integration check")
		}
	}
	return nil
}

type finalReviewExceptionAuthority struct {
	ScopeRequests      []core.ComplexScopeExpansionRequest  `json:"scopeRequests"`
	ScopeDecisions     []core.ComplexScopeExpansionDecision `json:"scopeDecisions"`
	PermissionVersions []core.PermissionVersion             `json:"permissionVersions"`
	GeneratedCommands  []core.ComplexGeneratedCommandFact   `json:"generatedCommands"`
	GeneratedProofs    []core.ComplexGeneratedProof         `json:"generatedProofs"`
	PathLeases         []core.ComplexExceptionPathLease     `json:"pathLeases"`
}

func finalReviewExceptionAuthorityFromFacts(facts *core.ComplexExceptionFacts) finalReviewExceptionAuthority {
	if facts == nil {
		return finalReviewExceptionAuthority{}
	}
	return finalReviewExceptionAuthority{
		ScopeRequests: facts.ScopeRequests, ScopeDecisions: facts.ScopeDecisions,
		PermissionVersions: facts.PermissionVersions, GeneratedCommands: facts.GeneratedCommands,
		GeneratedProofs: facts.GeneratedProofs, PathLeases: facts.PathLeases,
	}
}

func validateFinalReviewExceptionAuthority(ctx context.Context, q *gen.Queries, runID string, review core.RequirementFinalReview) error {
	var packet core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		return err
	}
	if packet.Exception == nil {
		return complexExecutionRule("final review packet omits exception authority")
	}
	current, err := loadComplexExceptionFacts(ctx, q, runID)
	if err != nil {
		return err
	}
	stored, err := json.Marshal(finalReviewExceptionAuthorityFromFacts(current))
	if err != nil {
		return err
	}
	supplied, err := json.Marshal(finalReviewExceptionAuthorityFromFacts(packet.Exception))
	if err != nil {
		return err
	}
	if !bytes.Equal(stored, supplied) {
		return complexExecutionRule("final review packet exception authority is stale")
	}
	return nil
}

func validateRequirementFinalReviewCompletion(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, integration core.ComplexExecutionIntegration) error {
	required, err := core.RequirementFinalReviewRequired(complexExecutionRunFromGen(run))
	if err != nil {
		return err
	}
	if !required {
		return nil
	}
	row, err := q.GetClearDevRequirementFinalReview(ctx, run.ID)
	if err != nil {
		return complexExecutionRule("completion requires requirement final reviewer PASS")
	}
	r, err := requirementFinalReviewFromGen(row)
	if err != nil {
		return err
	}
	if r.Status != "SETTLED" || r.Verdict != "PASS" || r.ResultID == "" || r.AOSessionID == "" {
		return complexExecutionRule("completion requires settled requirement final reviewer PASS")
	}
	effective, err := effectiveProjectRun(ctx, q, run)
	if err != nil {
		return err
	}
	if err := core.ValidateRequirementFinalReviewBinding(r, effective, integration.CandidateCommitSHA, integration.CheckRunIDs); err != nil {
		return err
	}
	if err := validateFinalAttemptEvidence(ctx, q, r); err != nil {
		return err
	}
	if err := validateFinalReviewExceptionAuthority(ctx, q, run.ID, r); err != nil {
		return err
	}
	result, err := q.GetClearDevFinalReviewSavedResult(ctx, r.ID)
	if err != nil {
		return err
	}
	parsed, err := core.ParseRequirementFinalReviewResult([]byte(result.RawMessageText))
	if err != nil || parsed.Verdict != "PASS" || complexExecutionRawDigest([]byte(result.RawMessageText)) != result.RawMessageSha256 {
		return complexExecutionRule("completion final review provider evidence is invalid")
	}
	return nil
}

// Compare the packet with independently loaded durable history before opening
// the write transaction. The transaction then rechecks the current requirement,
// verified candidates and every selected check. An injected packet cannot hide
// failed checks, change scope/acceptance evidence, or invent a task review.
func (s *Store) validateFinalReviewPacketHistory(ctx context.Context, review core.RequirementFinalReview) error {
	snapshot, found, err := s.GetClearDevComplexExecution(ctx, review.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	if !found || snapshot.Run.ID != review.ExecutionRunID || snapshot.Run.InitialBaseCommitSHA != review.BaseCommitSHA {
		return complexExecutionRule("final review packet does not belong to the current execution baseline")
	}
	sourceFound := false
	if snapshot.Run.Mode == core.WorkModeParallel {
		composition, ok := core.ComplexExecutionFinalComposition(snapshot)
		sourceFound = ok && composition.Status == core.ComplexExecutionCompositionComposed &&
			composition.WorkspacePath == review.SourceWorkspacePath &&
			composition.OutputCommitSHA == review.CandidateCommitSHA
	} else {
		for _, binding := range snapshot.RoleBindings {
			if binding.ID == snapshot.Run.BuilderRoleBindingID && binding.Status == core.RoleBindingStatusBound && binding.WorkspacePath == review.SourceWorkspacePath {
				sourceFound = true
			}
		}
	}
	if !sourceFound {
		return complexExecutionRule("final review packet source is not the current final candidate workspace")
	}
	var packet core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		return err
	}
	if packet.EvidenceFile != "" {
		expected, _ := json.Marshal(append([]core.WorkflowRecovery(nil), snapshot.WorkflowRecoveries...))
		provided, _ := json.Marshal(append([]core.WorkflowRecovery(nil), packet.WorkflowRecoveries...))
		if !bytes.Equal(expected, provided) {
			return complexExecutionRule("final review packet changed workflow recovery history")
		}
	}
	reviewerChecks, err := finalReviewReviewerChecks(ctx, s.qr, snapshot.Reviews)
	if err != nil {
		return err
	}
	for _, pair := range []struct {
		name   string
		stored any
		packet any
	}{
		{"run", snapshot.Run, packet.Run},
		{"tasks", snapshot.Tasks, packet.Tasks},
		{"dispatches", snapshot.Dispatches, packet.Dispatches},
		{"verifications", latestVerificationsPerTask(snapshot), packet.Verifications},
		{"task reviews", snapshot.Reviews, packet.TaskReviews},
		{"Reviewer requested checks", reviewerChecks, packet.ReviewerChecks},
		{"check specifications", snapshot.CheckSpecs, packet.CheckSpecs},
		{"check results", snapshot.CheckRuns, packet.CheckRuns},
		{"compositions", snapshot.Compositions, packet.Compositions},
		{"exception authority", snapshot.Exception, packet.Exception},
		{"recovery evidence", snapshot.FixedRecoveries, packet.FixedRecoveries},
		{"Planner runtime coordination", snapshot.PlannerRuntime, packet.PlannerRuntime},
	} {
		stored, err := json.Marshal(pair.stored)
		if err != nil {
			return err
		}
		supplied, err := json.Marshal(pair.packet)
		if err != nil {
			return err
		}
		if !bytes.Equal(stored, supplied) {
			return complexExecutionRule("final review packet changed durable " + pair.name)
		}
	}
	return nil
}

func finalReviewReviewerChecks(ctx context.Context, q *gen.Queries, reviews []core.ComplexExecutionReview) ([]core.RequirementFinalReviewReviewerChecks, error) {
	bundles := make([]core.RequirementFinalReviewReviewerChecks, 0)
	for _, review := range reviews {
		request, found, err := readReviewCheckRequest(ctx, q, review.ID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		results, err := readReviewCheckResults(ctx, q, request)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, core.RequirementFinalReviewReviewerChecks{
			ReviewID: review.ID,
			Request:  request,
			Results:  results,
		})
	}
	return bundles, nil
}

func finalReviewMessageBudget(ctx context.Context, q *gen.Queries, a core.AgentStepAttempt) error {
	row, err := q.GetClearDevRequirementFinalReviewForStep(ctx, a.LogicalStepID)
	if err != nil {
		return messageBudgetError(core.ReasonMessageBudgetBinding, "final review has no durable request")
	}
	r, err := requirementFinalReviewFromGen(row)
	if err != nil {
		return err
	}
	step := r.Step()
	if (r.Status != "PENDING" && r.Status != "SENT") || a.StepCategory != core.AgentStepCategoryComplexExecution || a.RoleBindingID != r.ID || a.AOSessionID != r.AOSessionID || a.DevelopmentRequirementID != r.DevelopmentRequirementID || a.PromptSHA256 != r.PromptSHA256 {
		return messageBudgetError(core.ReasonMessageBudgetBinding, "final review message identity changed or review stopped")
	}
	message := step.ClientMessageID
	if a.AttemptNumber == 2 {
		message += ":attempt:2"
	}
	if a.ClientMessageID != message {
		return messageBudgetError(core.ReasonMessageBudgetBinding, "final review message key changed")
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, r.ExecutionRunID)
	if err != nil {
		return err
	}
	return validateFinalReviewPrerequisites(ctx, q, run, r)
}

// latestVerificationsPerTask keeps only the newest verification per task in
// task order, the same rule the review packet uses; the stored history rows
// stay untouched.
func latestVerificationsPerTask(snapshot core.ComplexExecutionSnapshot) []core.ComplexExecutionVerification {
	latest := map[string]core.ComplexExecutionVerification{}
	for _, verification := range snapshot.Verifications {
		current, ok := latest[verification.ComplexExecutionTaskID]
		if !ok || verification.VerifiedAt.After(current.VerifiedAt) || current.VerifiedAt.IsZero() {
			latest[verification.ComplexExecutionTaskID] = verification
		}
	}
	out := make([]core.ComplexExecutionVerification, 0, len(latest))
	for _, task := range snapshot.Tasks {
		if verification, ok := latest[task.ID]; ok {
			out = append(out, verification)
		}
	}
	return out
}
