package cleardev

import (
	"context"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// EvaluateScope classifies trusted checker paths against the permission
// version frozen into the current candidate. S01 has no approval or generated
// proof source, so shared/generated matches append FAIL rather than PASS.
func (s *Service) EvaluateScope(ctx context.Context, taskID, candidateID, commitSHA string, paths []string, expiresAt *time.Time) (core.EvidenceRecord, error) {
	task, requirement, _, candidate, permission, err := s.currentCandidateContext(ctx, taskID, candidateID, commitSHA)
	if err != nil {
		return core.EvidenceRecord{}, err
	}
	result := core.EvidenceResultPass
	for _, path := range paths {
		classification, classifyErr := permission.Rules.ClassifyPath(path)
		if classifyErr != nil {
			return core.EvidenceRecord{}, apierr.Invalid("REPOSITORY_PATH_INVALID", "scope path is not a normalized repository-relative path", map[string]any{"path": path})
		}
		if classification != core.PathAllowed {
			result = core.EvidenceResultFail
		}
	}
	return s.appendTaskEvidence(ctx, task, requirement, candidate, core.EvidenceKindScope, "", result, core.EvidenceSourceControlPlaneChecker, "", expiresAt)
}

// RecordRequiredCheckResult is a trusted checker entrypoint. It is deliberately
// not exposed by HTTP in S01.
func (s *Service) RecordRequiredCheckResult(ctx context.Context, taskID, candidateID, commitSHA, checkName string, result core.EvidenceResult, expiresAt *time.Time) (core.EvidenceRecord, error) {
	task, requirement, snapshot, candidate, _, err := s.currentCandidateContext(ctx, taskID, candidateID, commitSHA)
	if err != nil {
		return core.EvidenceRecord{}, err
	}
	checkName = strings.TrimSpace(checkName)
	found := false
	for _, check := range snapshot.RequiredChecks {
		if check.DevelopmentTaskID == task.ID && check.Name == checkName {
			found = true
			break
		}
	}
	if !found {
		return core.EvidenceRecord{}, apierr.Invalid("REQUIRED_CHECK_NOT_FOUND", "required check is not configured for this development task", nil)
	}
	if err := validateEvidenceResult(result); err != nil {
		return core.EvidenceRecord{}, err
	}
	return s.appendTaskEvidence(ctx, task, requirement, candidate, core.EvidenceKindRequiredCheck, checkName, result, core.EvidenceSourceControlPlaneChecker, "", expiresAt)
}

// RecordDevelopmentTaskIntegrationResult records the candidate-bound
// integration check used by both QUICK and STANDARD task modes.
func (s *Service) RecordDevelopmentTaskIntegrationResult(ctx context.Context, taskID, candidateID, commitSHA string, result core.EvidenceResult, expiresAt *time.Time) (core.EvidenceRecord, error) {
	task, requirement, _, candidate, _, err := s.currentCandidateContext(ctx, taskID, candidateID, commitSHA)
	if err != nil {
		return core.EvidenceRecord{}, err
	}
	if err := validateEvidenceResult(result); err != nil {
		return core.EvidenceRecord{}, err
	}
	return s.appendTaskEvidence(ctx, task, requirement, candidate, core.EvidenceKindIntegration, "", result, core.EvidenceSourceControlPlaneChecker, "", expiresAt)
}

// RecordReviewResult accepts only a REVIEW_ADAPTER result linked to a distinct
// AO session in the same AO project. Storage repeats this identity check and
// records rejected attempts.
func (s *Service) RecordReviewResult(ctx context.Context, taskID, candidateID, commitSHA, reviewerSessionID string, result core.EvidenceResult, expiresAt *time.Time) (core.EvidenceRecord, error) {
	if s.ao == nil {
		return core.EvidenceRecord{}, apierr.Internal("CLEARDEV_AO_UNAVAILABLE", "AO facts are not configured")
	}
	task, requirement, _, candidate, _, err := s.currentCandidateContext(ctx, taskID, candidateID, commitSHA)
	if err != nil {
		return core.EvidenceRecord{}, err
	}
	if err := validateEvidenceResult(result); err != nil {
		return core.EvidenceRecord{}, err
	}
	reviewerSessionID = strings.TrimSpace(reviewerSessionID)
	_, _, err = s.ao.GetSession(ctx, domain.SessionID(reviewerSessionID))
	if err != nil {
		return core.EvidenceRecord{}, apierr.Internal("AO_SESSION_READ_FAILED", "Could not read the AO reviewer session")
	}
	return s.appendTaskEvidence(ctx, task, requirement, candidate, core.EvidenceKindReview, "", result, core.EvidenceSourceReviewAdapter, reviewerSessionID, expiresAt)
}

// RecordRequirementIntegrationResult binds final integration evidence to the
// current candidate, confirmed version and task-set version.
func (s *Service) RecordRequirementIntegrationResult(ctx context.Context, requirementID, integrationCandidateID, commitSHA string, result core.EvidenceResult, expiresAt *time.Time) (core.EvidenceRecord, error) {
	snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, strings.TrimSpace(requirementID))
	if err != nil {
		return core.EvidenceRecord{}, apierr.Internal("CLEARDEV_REQUIREMENT_READ_FAILED", "Could not read the ClearDev requirement")
	}
	if !ok {
		return core.EvidenceRecord{}, apierr.NotFound("CLEARDEV_REQUIREMENT_NOT_FOUND", "ClearDev requirement was not found")
	}
	if err := validateEvidenceResult(result); err != nil {
		return core.EvidenceRecord{}, err
	}
	var current *core.IntegrationCandidate
	for index := range snapshot.IntegrationCandidates {
		candidate := &snapshot.IntegrationCandidates[index]
		if current == nil || candidate.Sequence > current.Sequence {
			current = candidate
		}
	}
	sha, shaErr := core.NormalizeCommitSHA(commitSHA)
	if current == nil || current.ID != strings.TrimSpace(integrationCandidateID) || shaErr != nil || current.CommitSHA != sha {
		return core.EvidenceRecord{}, apierr.Conflict(string(core.ReasonCandidateMismatch), "integration evidence does not match the current candidate", nil)
	}
	record := core.EvidenceRecord{
		ID: currentEvidenceID(s), DevelopmentRequirementID: snapshot.Requirement.ID,
		SubjectType: core.SubjectDevelopmentRequirement, SubjectID: snapshot.Requirement.ID,
		Kind: core.EvidenceKindIntegration, Result: result,
		IntegrationCandidateID: current.ID, CommitSHA: current.CommitSHA,
		Source: core.EvidenceSourceControlPlaneChecker, CreatedAt: s.now().UTC(), ExpiresAt: expiresAt,
	}
	if err := s.facts.AppendClearDevEvidence(ctx, record); err != nil {
		return core.EvidenceRecord{}, mapStoreError(err, "RECORD_REQUIREMENT_INTEGRATION_FAILED")
	}
	return record, nil
}

func currentEvidenceID(s *Service) string {
	return s.newID()
}

func (s *Service) currentCandidateContext(ctx context.Context, taskID, candidateID, commitSHA string) (core.DevelopmentTask, core.DevelopmentRequirement, core.RequirementSnapshot, core.CandidateCommit, core.PermissionVersion, error) {
	task, requirement, ok, err := s.facts.GetClearDevTaskContext(ctx, strings.TrimSpace(taskID))
	if err != nil {
		return core.DevelopmentTask{}, core.DevelopmentRequirement{}, core.RequirementSnapshot{}, core.CandidateCommit{}, core.PermissionVersion{}, apierr.Internal("CLEARDEV_DEVELOPMENT_TASK_READ_FAILED", "Could not read the development task")
	}
	if !ok {
		return core.DevelopmentTask{}, core.DevelopmentRequirement{}, core.RequirementSnapshot{}, core.CandidateCommit{}, core.PermissionVersion{}, apierr.NotFound("CLEARDEV_DEVELOPMENT_TASK_NOT_FOUND", "ClearDev development task was not found")
	}
	snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, requirement.ID)
	if err != nil || !ok {
		return core.DevelopmentTask{}, core.DevelopmentRequirement{}, core.RequirementSnapshot{}, core.CandidateCommit{}, core.PermissionVersion{}, apierr.Internal("CLEARDEV_REQUIREMENT_READ_FAILED", "Could not read the ClearDev requirement")
	}
	var current *core.CandidateCommit
	for index := range snapshot.Candidates {
		candidate := &snapshot.Candidates[index]
		if candidate.DevelopmentTaskID == task.ID && (current == nil || candidate.Sequence > current.Sequence) {
			current = candidate
		}
	}
	sha, shaErr := core.NormalizeCommitSHA(commitSHA)
	if current == nil || !candidateIsCurrentRound(snapshot.Events, task.ID, current.ID) ||
		current.ID != strings.TrimSpace(candidateID) || shaErr != nil || current.CommitSHA != sha {
		return core.DevelopmentTask{}, core.DevelopmentRequirement{}, core.RequirementSnapshot{}, core.CandidateCommit{}, core.PermissionVersion{}, apierr.Conflict(string(core.ReasonCandidateMismatch), "evidence does not match the current candidate", nil)
	}
	var permission *core.PermissionVersion
	for index := range snapshot.PermissionVersions {
		candidatePermission := &snapshot.PermissionVersions[index]
		if candidatePermission.ID == current.PermissionVersionID {
			permission = candidatePermission
			break
		}
	}
	if permission == nil {
		return core.DevelopmentTask{}, core.DevelopmentRequirement{}, core.RequirementSnapshot{}, core.CandidateCommit{}, core.PermissionVersion{}, apierr.Internal("CLEARDEV_PERMISSION_NOT_FOUND", "Candidate permission version was not found")
	}
	return task, requirement, snapshot, *current, *permission, nil
}

func (s *Service) appendTaskEvidence(ctx context.Context, task core.DevelopmentTask, requirement core.DevelopmentRequirement, candidate core.CandidateCommit, kind core.EvidenceKind, key string, result core.EvidenceResult, source core.EvidenceSource, sourceSessionID string, expiresAt *time.Time) (core.EvidenceRecord, error) {
	record := core.EvidenceRecord{
		ID: s.newID(), DevelopmentRequirementID: requirement.ID, SubjectType: core.SubjectDevelopmentTask,
		SubjectID: task.ID, Kind: kind, Key: key, Result: result,
		CandidateCommitID: candidate.ID, CommitSHA: candidate.CommitSHA,
		Source: source, SourceAOSessionID: sourceSessionID,
		CreatedAt: s.now().UTC(), ExpiresAt: expiresAt,
	}
	if err := s.facts.AppendClearDevEvidence(ctx, record); err != nil {
		return core.EvidenceRecord{}, mapStoreError(err, "RECORD_EVIDENCE_FAILED")
	}
	return record, nil
}

func validateEvidenceResult(result core.EvidenceResult) error {
	if result != core.EvidenceResultPass && result != core.EvidenceResultFail {
		return apierr.Invalid("EVIDENCE_RESULT_INVALID", "evidence result must be PASS or FAIL", nil)
	}
	return nil
}
