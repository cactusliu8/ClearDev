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

// Called inside the existing protected completion transaction, before creating
// any result, integration candidate or DONE task. Only durable facts are read.
func validateMailCompletion(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, verified gen.CleardevComplexExecutionVerifiedCandidate, integration core.ComplexExecutionIntegration) error {
	domainRun := complexExecutionRunFromGen(run)
	policy, base, required, err := core.MailDeliveryPolicyFromRun(domainRun)
	if err != nil || !required {
		return err
	}
	if err := validateMailRequirementCanComplete(ctx, q, run); err != nil {
		return err
	}
	if _, err := q.GetOpenClearDevRequirementVersion(ctx, run.DevelopmentProjectID); err == nil {
		return complexExecutionRule("mail completion has an unresolved requirement")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	pending, err := q.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		return err
	}
	for _, decision := range pending {
		if decision.DevelopmentProjectID == run.DevelopmentProjectID {
			return complexExecutionRule("mail completion has an unresolved human decision")
		}
	}
	scopes, err := q.ListClearDevComplexExceptionScopeRequests(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, request := range scopes {
		if request.Status == "PENDING" {
			return complexExecutionRule("mail completion has an unresolved scope request")
		}
	}
	planRow, err := q.GetApprovedClearDevComplexPlanForVersion(ctx, run.RequirementVersionID)
	if err != nil {
		return err
	}
	var plan core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(planRow.PlanJson), &plan); err != nil {
		return err
	}
	if err := core.ValidateMailPlanForPolicy(policy, plan); err != nil {
		return err
	}
	if planRow.ID != run.PlanID || planRow.PlanSha256 != run.PlanSha256 {
		return complexExecutionRule("mail completion plan changed")
	}
	if policy == core.MailDeliveryPolicyV2 {
		return validateMailCompletionV2(ctx, q, run, plan, base, integration)
	}
	attempt, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, verified.TaskAttemptID)
	if err != nil {
		return err
	}
	if attempt.BaseCommitSha != base {
		return complexExecutionRule("mail dispatch changed the frozen initial SHA")
	}
	candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(verified.TaskAttemptID))
	if err != nil {
		return err
	}
	if candidate.ID != verified.CandidateCommitID || candidate.CommitSha != integration.CandidateCommitSHA {
		return complexExecutionRule("mail completion candidate changed")
	}
	review, err := q.GetClearDevComplexExecutionReview(ctx, verified.ReviewID)
	if err != nil {
		return err
	}
	if err := validateReviewerChecks(ctx, q, review, "PASS"); err != nil {
		return err
	}
	if review.CandidateCommitID != candidate.ID || review.TaskAttemptID != verified.TaskAttemptID {
		return complexExecutionRule("mail completion review is stale")
	}
	if review.Status != "SETTLED" || review.Verdict.String != "PASS" {
		// Keep the already-authorized replacement Reviewer path, while checking
		// its exact raw verdict rather than accepting an old review's PASS.
		if !verified.ReplacementRecoveryID.Valid {
			return complexExecutionRule("mail completion requires independent Reviewer PASS")
		}
		replacement, err := q.GetClearDevReplacementReviewResult(ctx, verified.ReplacementRecoveryID.String)
		if err != nil {
			return err
		}
		if replacement.Verdict != "PASS" || replacement.OriginalReviewID != review.ID || replacement.ResultID != verified.ReplacementResultID.String || replacement.AttemptID != verified.ReplacementAttemptID.String {
			return complexExecutionRule("mail replacement review mismatch")
		}
		if core.BoundedMailAttempts(complexExecutionRunFromGen(run)) {
			var result core.ReplacementReviewResult
			if err := json.Unmarshal([]byte(replacement.ResultJson), &result); err != nil {
				return err
			}
			if _, err := validateMailReplacementVerdict(ctx, q, result); err != nil {
				return err
			}
		} else {
			raw, err := q.GetClearDevFixedRecoveryRawResult(ctx, replacement.ResultID)
			if err != nil {
				return err
			}
			var result core.LocalReviewResult
			if err := json.Unmarshal([]byte(raw.RawMessageText), &result); err != nil {
				return err
			}
			if result.CandidateID != candidate.ID || result.CandidateSHA != candidate.CommitSha || result.Verdict != "PASS" || result.ReviewPacketSHA256 != review.ReviewPacketSha256 {
				return complexExecutionRule("mail replacement review is stale")
			}
		}
	}
	scope, err := q.GetClearDevComplexExecutionCheckRun(ctx, verified.ScopeCheckRunID)
	if err != nil {
		return err
	}
	if !mailStoredCheckPass(scope, candidate.ID, candidate.CommitSha) || scope.TaskAttemptID != verified.TaskAttemptID || scope.OutputSha256.String != complexExecutionRawDigest([]byte(scope.OutputSummary.String)) {
		return complexExecutionRule("mail scope proof missing or stale")
	}
	var scopeProof core.MailScopeProof
	if err := json.Unmarshal([]byte(scope.OutputSummary.String), &scopeProof); err != nil {
		return complexExecutionRule("mail scope proof missing")
	}
	if err := core.ValidateMailScopeProof(scopeProof, base, candidate.CommitSha); err != nil {
		return err
	}
	specs, err := q.ListClearDevComplexExecutionCheckSpecs(ctx, run.ID)
	if err != nil {
		return err
	}
	checks, err := q.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
	if err != nil {
		return err
	}
	var requiredIDs []string
	if err := json.Unmarshal([]byte(verified.RequiredCheckRunsJson), &requiredIDs); err != nil {
		return err
	}
	if len(requiredIDs) != len(plan.Tasks[0].RequiredCheckIDs) || len(integration.CheckRunIDs) != 1 {
		return complexExecutionRule("mail completion check set is incomplete")
	}
	finalCheck, err := q.GetClearDevComplexExecutionCheckRun(ctx, integration.CheckRunIDs[0])
	if err != nil {
		return err
	}
	request, extraRequired, err := readReviewCheckRequest(ctx, q, review.ID)
	if err != nil {
		return err
	}
	if extraRequired {
		extra, err := readReviewCheckResults(ctx, q, request)
		if err != nil {
			return err
		}
		for _, receipt := range extra {
			if receipt.Proof.SourceManifestID != scopeProof.SourceManifestID || receipt.Proof.SourceTreeOID != scopeProof.SourceTreeOID || receipt.Proof.ImageID != finalCheck.ContainerImageID.String {
				return complexExecutionRule("requested Reviewer checks used another source or image")
			}
		}
	}
	integrationCount := 0
	for _, spec := range specs {
		if spec.CheckKind == "SCOPE" {
			continue
		}
		ids := requiredIDs
		if spec.CheckKind == "INTEGRATION" {
			integrationCount++
			ids = integration.CheckRunIDs
			if spec.CheckName != "demo-integration" {
				return complexExecutionRule("mail integration entry changed")
			}
		}
		matched := false
		for _, check := range checks {
			if check.CheckSpecID != spec.ID || !slices.Contains(ids, check.ID) {
				continue
			}
			if !mailStoredCheckPass(check, candidate.ID, candidate.CommitSha) || check.TaskAttemptID != verified.TaskAttemptID || !check.ContainerImageID.Valid || check.ContainerImageID.String != finalCheck.ContainerImageID.String {
				return complexExecutionRule("mail required check failed or stale")
			}
			if spec.CheckKind == "INTEGRATION" {
				if len(ids) != 1 || check.OutputSha256.String != complexExecutionRawDigest([]byte(check.OutputSummary.String)) {
					return complexExecutionRule("mail delivery proof missing")
				}
				if err := core.ValidateMailDeliveryProof(check.OutputSummary.String, base, candidate.CommitSha, check.ID, check.ContainerImageID.String); err != nil {
					return err
				}
				var delivery core.MailDeliveryProof
				if err := json.Unmarshal([]byte(check.OutputSummary.String), &delivery); err != nil {
					return err
				}
				left, _ := json.Marshal(scopeProof)
				right, _ := json.Marshal(delivery.Scope)
				if !bytes.Equal(left, right) {
					return complexExecutionRule("mail final scope differs from reviewed scope")
				}
			}
			matched = true
		}
		if !matched {
			return complexExecutionRule("mail completion is missing a required candidate check")
		}
	}
	if integrationCount != 1 {
		return complexExecutionRule("mail completion requires full test and health evidence")
	}
	return nil
}

func validateMailVerifiedReview(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, verified gen.CleardevComplexExecutionVerifiedCandidate, candidate gen.CleardevCandidateCommit) (gen.CleardevComplexExecutionReview, error) {
	review, err := q.GetClearDevComplexExecutionReview(ctx, verified.ReviewID)
	if err != nil {
		return review, err
	}
	if err := validateReviewerChecks(ctx, q, review, "PASS"); err != nil {
		return review, err
	}
	if review.CandidateCommitID != candidate.ID || review.TaskAttemptID != verified.TaskAttemptID {
		return review, complexExecutionRule("mail completion review is stale")
	}
	if review.Status == "SETTLED" && review.Verdict.String == "PASS" {
		return review, nil
	}
	if !verified.ReplacementRecoveryID.Valid {
		return review, complexExecutionRule("mail completion requires independent Reviewer PASS")
	}
	replacement, err := q.GetClearDevReplacementReviewResult(ctx, verified.ReplacementRecoveryID.String)
	if err != nil {
		return review, err
	}
	if replacement.Verdict != "PASS" || replacement.OriginalReviewID != review.ID || replacement.ResultID != verified.ReplacementResultID.String || replacement.AttemptID != verified.ReplacementAttemptID.String {
		return review, complexExecutionRule("mail replacement review mismatch")
	}
	domainRun := complexExecutionRunFromGen(run)
	supported, supportErr := replacementReviewerRunSupported(domainRun)
	if supportErr != nil {
		return review, supportErr
	}
	if supported {
		var result core.ReplacementReviewResult
		if err := json.Unmarshal([]byte(replacement.ResultJson), &result); err != nil {
			return review, err
		}
		if _, err := validateMailReplacementVerdict(ctx, q, result); err != nil {
			return review, err
		}
		return review, nil
	}
	raw, err := q.GetClearDevFixedRecoveryRawResult(ctx, replacement.ResultID)
	if err != nil {
		return review, err
	}
	var result core.LocalReviewResult
	if err := json.Unmarshal([]byte(raw.RawMessageText), &result); err != nil {
		return review, err
	}
	if result.CandidateID != candidate.ID || result.CandidateSHA != candidate.CommitSha || result.Verdict != "PASS" || result.ReviewPacketSHA256 != review.ReviewPacketSha256 {
		return review, complexExecutionRule("mail replacement review is stale")
	}
	return review, nil
}

func validateMailCompletionV2(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, plan core.ComplexEngineeringPlanResult, base string, integration core.ComplexExecutionIntegration) error {
	if len(integration.CheckRunIDs) != 1 {
		return complexExecutionRule("mail V2 completion requires exactly one final integration check")
	}
	tasks, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return err
	}
	verifiedRows, err := q.ListClearDevComplexExecutionVerifiedCandidates(ctx, run.ID)
	if err != nil {
		return err
	}
	specs, err := q.ListClearDevComplexExecutionCheckSpecs(ctx, run.ID)
	if err != nil {
		return err
	}
	checks, err := q.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
	if err != nil {
		return err
	}
	if len(tasks) < 1 || len(tasks) > 3 || len(tasks) != len(plan.Tasks) || len(verifiedRows) != len(tasks) {
		return complexExecutionRule("mail V2 completion task evidence is incomplete")
	}

	finalSHA := integration.CandidateCommitSHA
	if run.Mode == string(core.WorkModeParallel) {
		composition, err := finalParallelComposition(ctx, q, run.ID)
		if err != nil {
			return err
		}
		if composition.Status != string(core.ComplexExecutionCompositionComposed) || composition.OutputCommitSha == "" || composition.OutputCommitSha != finalSHA {
			return complexExecutionRule("mail V2 completion requires the final composed candidate")
		}
	} else if run.Mode != string(core.WorkModeStandard) {
		return complexExecutionRule("mail V2 completion has an unsupported execution mode")
	}

	finalCheck, err := q.GetClearDevComplexExecutionCheckRun(ctx, integration.CheckRunIDs[0])
	if err != nil {
		return err
	}
	integrationSpecs := 0
	for _, spec := range specs {
		if spec.CheckKind != string(core.CandidateCheckIntegration) {
			continue
		}
		integrationSpecs++
		if spec.TaskMappingID.Valid || spec.CheckName != "demo-integration" || finalCheck.CheckSpecID != spec.ID {
			return complexExecutionRule("mail V2 final integration specification changed")
		}
	}
	if integrationSpecs != 1 || !mailStoredFinalCheckPass(finalCheck, finalSHA) || !finalCheck.ContainerImageID.Valid {
		return complexExecutionRule("mail V2 final integration evidence is missing or stale")
	}
	if err := core.ValidateMailDeliveryProofForPolicy(finalCheck.OutputSummary.String, core.MailDeliveryPolicyV2, base, finalSHA, finalCheck.ID, finalCheck.ContainerImageID.String); err != nil {
		return err
	}

	var delivery core.MailDeliveryProof
	if err := json.Unmarshal([]byte(finalCheck.OutputSummary.String), &delivery); err != nil {
		return err
	}
	var finalTaskCandidate gen.CleardevCandidateCommit
	for index, task := range tasks {
		planTask := plan.Tasks[index]
		if task.Ordinal != int64(index) || task.PlanTaskKey != planTask.Key || verifiedRows[index].TaskMappingID != task.ID {
			return complexExecutionRule("mail V2 task ordering or plan binding changed")
		}
		verified := verifiedRows[index]
		attempt, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, verified.TaskAttemptID)
		if err != nil {
			return err
		}
		if attempt.ExecutionRunID != run.ID || attempt.TaskMappingID != task.ID || attempt.Status != "VERIFIED" {
			return complexExecutionRule("mail V2 task attempt is not the verified current attempt")
		}
		candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(verified.TaskAttemptID))
		if err != nil {
			return err
		}
		if candidate.ID != verified.CandidateCommitID || candidate.CommitSha == "" {
			return complexExecutionRule("mail V2 verified candidate changed")
		}
		review, err := validateMailVerifiedReview(ctx, q, run, verified, candidate)
		if err != nil {
			return err
		}
		scopeRun, err := q.GetClearDevComplexExecutionCheckRun(ctx, verified.ScopeCheckRunID)
		if err != nil {
			return err
		}
		if !mailStoredCheckPass(scopeRun, candidate.ID, candidate.CommitSha) || scopeRun.TaskAttemptID != attempt.ID || scopeRun.OutputSha256.String != complexExecutionRawDigest([]byte(scopeRun.OutputSummary.String)) {
			return complexExecutionRule("mail V2 task scope proof is missing or stale")
		}
		var scopeProof core.MailScopeProof
		if err := json.Unmarshal([]byte(scopeRun.OutputSummary.String), &scopeProof); err != nil {
			return complexExecutionRule("mail V2 task scope proof is invalid")
		}
		if err := core.ValidateMailScopeProofForPolicy(scopeProof, core.MailDeliveryPolicyV2, attempt.BaseCommitSha, candidate.CommitSha); err != nil {
			return err
		}
		if candidate.CommitSha == finalSHA {
			if scopeProof.SourceManifestID != delivery.Scope.SourceManifestID || scopeProof.SourceTreeOID != delivery.Scope.SourceTreeOID {
				return complexExecutionRule("mail V2 final candidate source differs from reviewed source")
			}
			if attempt.BaseCommitSha == base {
				left, _ := json.Marshal(scopeProof)
				right, _ := json.Marshal(delivery.Scope)
				if !bytes.Equal(left, right) {
					return complexExecutionRule("mail V2 final scope differs from reviewed scope on the same base")
				}
			}
		}

		var requiredRunIDs []string
		if err := json.Unmarshal([]byte(verified.RequiredCheckRunsJson), &requiredRunIDs); err != nil {
			return err
		}
		if len(requiredRunIDs) != len(planTask.RequiredCheckIDs) {
			return complexExecutionRule("mail V2 task required check set is incomplete")
		}
		requiredSet := make(map[string]bool, len(requiredRunIDs))
		for _, id := range requiredRunIDs {
			if id == "" || requiredSet[id] {
				return complexExecutionRule("mail V2 task required check set is ambiguous")
			}
			requiredSet[id] = true
		}
		matchedNames := make(map[string]bool, len(planTask.RequiredCheckIDs))
		for _, spec := range specs {
			if !spec.TaskMappingID.Valid || spec.TaskMappingID.String != task.ID || spec.CheckKind != string(core.CandidateCheckRequired) {
				continue
			}
			if !slices.Contains(planTask.RequiredCheckIDs, spec.CheckName) || matchedNames[spec.CheckName] {
				return complexExecutionRule("mail V2 task required check specification changed")
			}
			matched := false
			for _, check := range checks {
				if check.CheckSpecID != spec.ID || !requiredSet[check.ID] {
					continue
				}
				if matched || !mailStoredCheckPass(check, candidate.ID, candidate.CommitSha) || check.TaskAttemptID != attempt.ID || !check.ContainerImageID.Valid || check.ContainerImageID.String != finalCheck.ContainerImageID.String {
					return complexExecutionRule("mail V2 task required check failed or is stale")
				}
				matched = true
				delete(requiredSet, check.ID)
			}
			if !matched {
				return complexExecutionRule("mail V2 task required check is missing")
			}
			matchedNames[spec.CheckName] = true
		}
		if len(matchedNames) != len(planTask.RequiredCheckIDs) || len(requiredSet) != 0 {
			return complexExecutionRule("mail V2 task required check evidence does not match the plan")
		}

		request, extraRequired, err := readReviewCheckRequest(ctx, q, review.ID)
		if err != nil {
			return err
		}
		if extraRequired {
			extra, err := readReviewCheckResults(ctx, q, request)
			if err != nil {
				return err
			}
			for _, receipt := range extra {
				if receipt.Proof.SourceManifestID != scopeProof.SourceManifestID || receipt.Proof.SourceTreeOID != scopeProof.SourceTreeOID || receipt.Proof.ImageID != finalCheck.ContainerImageID.String {
					return complexExecutionRule("mail V2 requested Reviewer checks used another task source or image")
				}
			}
		}
		finalTaskCandidate = candidate
	}
	if run.Mode == string(core.WorkModeStandard) && finalTaskCandidate.CommitSha != finalSHA {
		return complexExecutionRule("mail V2 STANDARD completion candidate changed")
	}
	if finalCheck.TaskAttemptID != verifiedRows[len(verifiedRows)-1].TaskAttemptID || finalCheck.CandidateCommitID != verifiedRows[len(verifiedRows)-1].CandidateCommitID {
		return complexExecutionRule("mail V2 final integration owner changed")
	}
	return nil
}

func mailStoredFinalCheckPass(check gen.CleardevComplexExecutionCheckRun, sha string) bool {
	return check.Status == "SETTLED" && check.Result.String == "PASS" && check.CandidateCommitSha == sha &&
		check.ExitCode.Valid && check.ExitCode.Int64 == 0 && check.TimedOut.Valid && !check.TimedOut.Bool &&
		check.OutputSha256.Valid && check.OutputSha256.String == complexExecutionRawDigest([]byte(check.OutputSummary.String)) &&
		check.ReasonCode == ""
}

// Re-read user actions in the same transaction as completion. A service-level
// check before the final external probe cannot close this last race window.
func validateMailRequirementCanComplete(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun) error {
	requirement, err := q.GetClearDevRequirement(ctx, run.DevelopmentProjectID)
	if err != nil {
		return err
	}
	if requirement.CancelledAt.Valid || requirement.State == "CANCELLED" || requirement.State == "PAUSED" {
		return complexExecutionRule("mail completion is cancelled or paused")
	}
	intent, err := q.GetClearDevDirectionIntentByVersion(ctx, run.RequirementVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	decision, err := q.GetClearDevHumanDecisionRequestByDirectionRequestID(ctx, intent.DirectionRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return complexExecutionRule("mail completion has an unresolved direction intent")
	}
	if err != nil {
		return err
	}
	// A rejected change leaves the original requirement valid. A pending or
	// approved change must not be silently discarded by completing old work.
	if decision.Status != "RESOLVED" || decision.Decision != string(core.HumanDecisionReject) {
		return complexExecutionRule("mail completion has an unresolved direction decision")
	}
	return nil
}

func validateMailCompletionReplay(ctx context.Context, q *gen.Queries, run gen.CleardevComplexExecutionRun, integration core.ComplexExecutionIntegration) error {
	_, required, err := core.MailPolicyFromRun(complexExecutionRunFromGen(run))
	if err != nil || !required {
		return err
	}
	result, err := q.GetClearDevComplexExecutionResult(ctx, run.ID)
	if err != nil {
		return err
	}
	candidate, err := q.GetClearDevIntegrationCandidate(ctx, result.IntegrationCandidateID)
	if err != nil {
		return err
	}
	if result.CompletionStatus != "COMPLETED" || candidate.CommitSha != integration.CandidateCommitSHA {
		return complexExecutionRule("completed mail delivery cannot acknowledge another SHA")
	}
	return nil
}

func mailStoredCheckPass(check gen.CleardevComplexExecutionCheckRun, candidateID, sha string) bool {
	return check.Status == "SETTLED" && check.Result.String == "PASS" && check.CandidateCommitID == candidateID && check.CandidateCommitSha == sha && check.ExitCode.Valid && check.ExitCode.Int64 == 0 && check.TimedOut.Valid && !check.TimedOut.Bool && check.OutputSha256.Valid && check.ReasonCode == ""
}
