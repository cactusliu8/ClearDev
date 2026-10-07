package cleardev

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CreateRequirementVersion atomically allocates the next version number in
// storage. A caller cannot race a stale read-model version calculation.
func (s *Service) CreateRequirementVersion(ctx context.Context, requirementID, text string) (core.RequirementVersion, error) {
	if s.facts == nil {
		return core.RequirementVersion{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev service is not configured")
	}
	requirementID = strings.TrimSpace(requirementID)
	if requirementID == "" {
		return core.RequirementVersion{}, apierr.Invalid("CLEARDEV_REQUIREMENT_ID_REQUIRED", "ClearDev requirement id is required", nil)
	}
	if strings.TrimSpace(text) == "" {
		return core.RequirementVersion{}, apierr.Invalid("REQUIREMENT_TEXT_REQUIRED", "requirement text is required", nil)
	}
	digest := sha256.Sum256([]byte(text))
	version := core.RequirementVersion{
		ID: s.newID(), DevelopmentRequirementID: requirementID,
		RequirementText: text, SHA256: fmt.Sprintf("%x", digest),
		Status: core.RequirementVersionStatusDraft, CreatedAt: s.now().UTC(),
	}
	if err := s.facts.CreateClearDevRequirementVersion(ctx, version); err != nil {
		return core.RequirementVersion{}, mapStoreError(err, "CREATE_REQUIREMENT_VERSION_FAILED")
	}
	snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil || !ok {
		return core.RequirementVersion{}, apierr.Internal("CLEARDEV_REQUIREMENT_READ_FAILED", "Could not read the created requirement version")
	}
	for _, created := range snapshot.RequirementVersions {
		if created.ID == version.ID {
			return created, nil
		}
	}
	return core.RequirementVersion{}, apierr.Internal("CREATE_REQUIREMENT_VERSION_FAILED", "Created requirement version was not found")
}

// CreateDevelopmentTask builds the inseparable task, permission and check
// facts. Storage binds them to the current confirmed requirement version and
// increments that version's task-set version in the same transaction.
func (s *Service) CreateDevelopmentTask(ctx context.Context, requirementID string, input CreateDevelopmentTaskInput) (core.DevelopmentTask, error) {
	if s.facts == nil {
		return core.DevelopmentTask{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev service is not configured")
	}
	requirementID = strings.TrimSpace(requirementID)
	if requirementID == "" {
		return core.DevelopmentTask{}, apierr.Invalid("CLEARDEV_REQUIREMENT_ID_REQUIRED", "ClearDev requirement id is required", nil)
	}
	if err := s.projectExecutionGate(ctx, requirementID); err != nil {
		return core.DevelopmentTask{}, err
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" {
		return core.DevelopmentTask{}, apierr.Invalid("DEVELOPMENT_TASK_TITLE_REQUIRED", "development task title is required", nil)
	}
	if input.Mode != core.WorkModeQuick && input.Mode != core.WorkModeStandard {
		return core.DevelopmentTask{}, apierr.Invalid("DEVELOPMENT_TASK_MODE_INVALID", "development task mode must be QUICK or STANDARD", nil)
	}
	if input.MaxReworkCount < 0 {
		return core.DevelopmentTask{}, apierr.Invalid("MAX_REWORK_COUNT_INVALID", "maxReworkCount must be non-negative", nil)
	}
	rules := core.PathRules{
		WritePaths:                 cloneStrings(input.Permissions.WritePaths),
		GeneratedPaths:             cloneStrings(input.Permissions.GeneratedPaths),
		SharedPathsRequireApproval: cloneStrings(input.Permissions.SharedPathsRequireApproval),
		ForbiddenPaths:             cloneStrings(input.Permissions.ForbiddenPaths),
	}
	if err := rules.Validate(); err != nil {
		return core.DevelopmentTask{}, apierr.Invalid("PATH_RULE_INVALID", "development task has an invalid path rule", map[string]any{"cause": err.Error()})
	}
	if len(input.RequiredChecks) == 0 {
		return core.DevelopmentTask{}, apierr.Invalid("REQUIRED_CHECKS_REQUIRED", "development task needs at least one required check", nil)
	}
	now := s.now().UTC()
	taskID := s.newID()
	initial := core.InitialDevelopmentTask{
		Task: core.DevelopmentTask{
			ID: taskID, DevelopmentRequirementID: requirementID, Title: input.Title,
			Mode: input.Mode, Status: core.DevelopmentTaskStatusPlanned,
			MaxReworkCount: input.MaxReworkCount, CreatedAt: now, UpdatedAt: now,
		},
		Permission: core.PermissionVersion{
			ID: s.newID(), DevelopmentTaskID: taskID, Version: 1, Rules: rules, CreatedAt: now,
		},
	}
	seen := make(map[string]struct{}, len(input.RequiredChecks))
	for index, raw := range input.RequiredChecks {
		name := strings.TrimSpace(raw.Name)
		kind := strings.TrimSpace(raw.Kind)
		if name == "" || kind == "" {
			return core.DevelopmentTask{}, apierr.Invalid("REQUIRED_CHECK_INVALID", fmt.Sprintf("requiredChecks[%d] needs name and kind", index), nil)
		}
		if _, exists := seen[name]; exists {
			return core.DevelopmentTask{}, apierr.Invalid("REQUIRED_CHECK_DUPLICATE", fmt.Sprintf("development task repeats required check %q", name), nil)
		}
		seen[name] = struct{}{}
		initial.Checks = append(initial.Checks, core.RequiredCheck{
			ID: s.newID(), DevelopmentTaskID: taskID, Name: name, Kind: kind, CreatedAt: now,
		})
	}
	if err := s.facts.CreateClearDevTask(ctx, initial); err != nil {
		return core.DevelopmentTask{}, mapStoreError(err, "CREATE_DEVELOPMENT_TASK_FAILED")
	}
	task, _, ok, err := s.facts.GetClearDevTaskContext(ctx, taskID)
	if err != nil || !ok {
		return core.DevelopmentTask{}, apierr.Internal("CLEARDEV_DEVELOPMENT_TASK_READ_FAILED", "Could not read the created development task")
	}
	return task, nil
}

// CreatePermissionVersion appends validated rules. Store checks that the task
// still belongs to the current confirmed version and is still mutable.
func (s *Service) CreatePermissionVersion(ctx context.Context, taskID string, rules core.PathRules) (core.PermissionVersion, error) {
	if err := rules.Validate(); err != nil {
		return core.PermissionVersion{}, apierr.Invalid("PATH_RULE_INVALID", "path rules are invalid", map[string]any{"cause": err.Error()})
	}
	task, requirement, ok, err := s.facts.GetClearDevTaskContext(ctx, strings.TrimSpace(taskID))
	if err != nil {
		return core.PermissionVersion{}, apierr.Internal("CLEARDEV_DEVELOPMENT_TASK_READ_FAILED", "Could not read the development task")
	}
	if !ok {
		return core.PermissionVersion{}, apierr.NotFound("CLEARDEV_DEVELOPMENT_TASK_NOT_FOUND", "ClearDev development task was not found")
	}
	snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, requirement.ID)
	if err != nil || !ok {
		return core.PermissionVersion{}, apierr.Internal("CLEARDEV_REQUIREMENT_READ_FAILED", "Could not read the ClearDev requirement")
	}
	next := int64(1)
	for _, existing := range snapshot.PermissionVersions {
		if existing.DevelopmentTaskID == task.ID && existing.Version >= next {
			next = existing.Version + 1
		}
	}
	permission := core.PermissionVersion{
		ID: s.newID(), DevelopmentTaskID: task.ID, Version: next, Rules: rules, CreatedAt: s.now().UTC(),
	}
	if err := s.facts.AppendClearDevPermissionVersion(ctx, permission); err != nil {
		return core.PermissionVersion{}, mapStoreError(err, "CREATE_PERMISSION_VERSION_FAILED")
	}
	return permission, nil
}

func (s *Service) applyAction(ctx context.Context, action core.Action, subjectID, reason string, trustedHuman bool, replacementID string) error {
	if s.facts == nil {
		return apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev service is not configured")
	}
	err := s.facts.ApplyClearDevAction(ctx, core.ActionRequest{
		Action: action, SubjectID: strings.TrimSpace(subjectID),
		ReplacementRequirementVersionID: strings.TrimSpace(replacementID), ReasonText: strings.TrimSpace(reason),
		TrustedHumanDecision: trustedHuman, Source: core.EventSourceControlPlane, At: s.now().UTC(),
	})
	if err != nil {
		return mapStoreError(err, "CLEARDEV_ACTION_FAILED")
	}
	return nil
}

func (s *Service) requirementVersionDecision(ctx context.Context, action core.Action, versionID, reason string) error {
	decision := HumanDecision{Action: action, RequirementVersionID: strings.TrimSpace(versionID), Reason: strings.TrimSpace(reason)}
	trusted := s.human != nil && s.human.Authorize(ctx, decision) == nil
	return s.applyAction(ctx, action, versionID, reason, trusted, "")
}

func (s *Service) requirementDecision(ctx context.Context, action core.Action, requirementID, reason string) error {
	decision := HumanDecision{Action: action, DevelopmentRequirementID: strings.TrimSpace(requirementID), Reason: strings.TrimSpace(reason)}
	trusted := s.human != nil && s.human.Authorize(ctx, decision) == nil
	return s.applyAction(ctx, action, requirementID, reason, trusted, "")
}

func (s *Service) SubmitRequirementForConfirmation(ctx context.Context, id string) error {
	return s.applyAction(ctx, core.ActionSubmitRequirementConfirmation, id, "", false, "")
}

func (s *Service) ConfirmRequirementVersion(ctx context.Context, id string) error {
	if err := s.requirementVersionDecision(ctx, core.ActionConfirmRequirementVersion, id, ""); err != nil {
		return err
	}
	s.scheduleComplexAfterVersionConfirm(ctx, id)
	return nil
}

func (s *Service) RejectRequirementVersion(ctx context.Context, id, reason string) error {
	return s.requirementVersionDecision(ctx, core.ActionRejectRequirementVersion, id, reason)
}

func (s *Service) CancelRequirement(ctx context.Context, id, reason string) error {
	return s.requirementDecision(ctx, core.ActionCancelRequirement, id, reason)
}

func (s *Service) StartDevelopmentTask(ctx context.Context, id string) error {
	return s.applyAction(ctx, core.ActionStartDevelopmentTask, id, "", false, "")
}

func (s *Service) SubmitDevelopmentTaskReview(ctx context.Context, id string) error {
	return s.applyAction(ctx, core.ActionSubmitDevelopmentTaskReview, id, "", false, "")
}

func (s *Service) CompleteDevelopmentTask(ctx context.Context, id string) error {
	return s.applyAction(ctx, core.ActionCompleteDevelopmentTask, id, "", false, "")
}

// RequestDevelopmentTaskRework sends a reviewed task back to its Builder
// within the task's rework budget: it records the REWORK decision and starts
// the rework round (which spends exactly one budget), then wakes the durable
// execution loop so the Builder is actually dispatched.
func (s *Service) RequestDevelopmentTaskRework(ctx context.Context, requirementID, id string) error {
	if s.complexExecution != nil {
		execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
		if err != nil {
			return mapStoreError(err, "READ_EXECUTION_FAILED")
		}
		if !found {
			return apierr.NotFound("EXECUTION_NOT_FOUND", "No project execution is running for this requirement")
		}
		belongs := false
		status := core.DevelopmentTaskStatus("")
		for _, task := range execution.Tasks {
			if task.ID == id || task.DevelopmentTaskID == id {
				belongs, status = true, task.Status
			}
		}
		if !belongs {
			return apierr.NotFound("TASK_NOT_FOUND", "The development task is not part of this execution")
		}
		if review := execution.FinalReview; review != nil && review.Verdict == "BLOCKED" {
			if err := s.manualBlockedFinalReturnCurrent(ctx, execution, strings.TrimSpace(id)); err != nil {
				return err
			}
		}
		// Only a task whose review failed can be sent back; a planned task has
		// no reviewed candidate and keeps the original rejection.
		if status != core.DevelopmentTaskStatusReview && status != core.DevelopmentTaskStatusRework {
			return apierr.Conflict("TASK_NOT_IN_REVIEW", "Only a task in review or rework can be sent back for rework", nil)
		}
	}
	err := s.facts.ApplyClearDevAction(ctx, core.ActionRequest{
		Action: core.ActionRequestDevelopmentTaskRework, SubjectID: strings.TrimSpace(id),
		Source: core.EventSourceControlPlane, At: s.now().UTC(),
	})
	if err != nil {
		// Repeating the decision on a task already in REWORK continues its
		// round; the Restart action's own state machine rejects anything
		// else, so no real rejection is swallowed here.
		var rule *core.RuleError
		if !errors.As(err, &rule) || rule.Code != core.ReasonInvalidTransition {
			return mapStoreError(err, "CLEARDEV_ACTION_FAILED")
		}
	}
	restartErr := s.facts.ApplyClearDevAction(ctx, core.ActionRequest{
		Action: core.ActionRestartDevelopmentTask, SubjectID: strings.TrimSpace(id),
		Source: core.EventSourceControlPlane, At: s.now().UTC(),
	})
	if restartErr != nil {
		// A task already running its rework round only needs the execution
		// loop woken up; every other rejection stands.
		var rule *core.RuleError
		if !errors.As(restartErr, &rule) || rule.Code != core.ReasonInvalidTransition {
			return mapStoreError(restartErr, "CLEARDEV_ACTION_FAILED")
		}
	}
	// A task rework belongs to the execution runner. Project planning is
	// already settled and cannot wake a newly registered Builder round.
	if s.complexExecution != nil {
		s.scheduleComplexStandardExecution(requirementID)
	} else {
		s.scheduleComplexFlow(requirementID)
	}
	return nil
}

func (s *Service) manualBlockedFinalReturnCurrent(ctx context.Context, execution core.ComplexExecutionSnapshot, taskID string) error {
	review := execution.FinalReview
	if review == nil || review.Status != "SETTLED" || core.ValidateRequirementFinalReviewBinding(*review, execution.Run, review.CandidateCommitSHA, review.CheckRunIDs) != nil {
		return apierr.Conflict("FINAL_REVIEW_NOT_SETTLED", "The exact final review must be settled before returning it", nil)
	}
	if err := s.workflowRecoveryCurrent(ctx, execution); err != nil {
		return err
	}
	task, dispatch, verification, found := finalComplexExecutionCandidate(execution)
	if !found || (task.ID != taskID && task.DevelopmentTaskID != taskID) || verification.CandidateCommitSHA != review.CandidateCommitSHA {
		return apierr.Conflict("FINAL_REVIEW_RETURN_CHANGED", "Only the current final candidate's task may be returned", nil)
	}
	binding, bound := complexExecutionBindingByID(execution, dispatch.BuilderRoleBindingID)
	if !bound || binding.Status != core.RoleBindingStatusBound || s.ao == nil {
		return apierr.Conflict("BUILDER_UNAVAILABLE", "The original Builder is unavailable", nil)
	}
	record, exists, err := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		return err
	}
	requirement, known, err := s.facts.GetClearDevRequirement(ctx, execution.Run.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	version, confirmed := currentConfirmedVersion(requirement)
	if !known || !confirmed || version.TaskSetVersion != execution.Run.TaskSetVersion {
		return apierr.Conflict("BUILDER_RETURN_NOT_CURRENT", "The current task set changed", nil)
	}
	if !exists || !known || record.IsTerminated || record.Activity.State != domain.ActivityIdle ||
		!s.validComplexExecutionWorker(ctx, record, binding, requirement.Requirement.AOProjectID) ||
		!s.finalReviewWorkspaceMatches(ctx, binding.WorkspacePath, review.CandidateCommitSHA, execution.Run) ||
		!s.finalReviewWorkspaceMatches(ctx, review.WorkspacePath, review.CandidateCommitSHA, execution.Run) {
		return apierr.Conflict("BUILDER_RETURN_NOT_READY", "The original Builder must be idle and both worktrees must still match the reviewed candidate", nil)
	}
	return nil
}

func (s *Service) EscalateDevelopmentTask(ctx context.Context, id string) error {
	return s.applyAction(ctx, core.ActionEscalateDevelopmentTask, id, string(core.ReasonReworkLimitReached), false, "")
}

func (s *Service) RestartDevelopmentTask(ctx context.Context, id string) error {
	return s.applyAction(ctx, core.ActionRestartDevelopmentTask, id, "", false, "")
}

func (s *Service) PauseDevelopmentTaskForHuman(ctx context.Context, id, reason string) error {
	return s.applyAction(ctx, core.ActionPauseDevelopmentTaskNeedsHuman, id, reason, false, "")
}

func (s *Service) BlockDevelopmentTask(ctx context.Context, id, reason string) error {
	return s.applyAction(ctx, core.ActionBlockDevelopmentTask, id, reason, false, "")
}

func (s *Service) ResumeDevelopmentTask(ctx context.Context, id string) error {
	return s.applyAction(ctx, core.ActionResumeDevelopmentTask, id, "", false, "")
}

func (s *Service) CancelDevelopmentTask(ctx context.Context, id string) error {
	return s.applyAction(ctx, core.ActionCancelDevelopmentTask, id, "", false, "")
}

// RegisterCandidate observes the daemon-managed AO worktree. The caller never
// supplies a commit SHA or cleanliness claim.
func (s *Service) RegisterCandidate(ctx context.Context, taskID, aoSessionID string) (core.CandidateCommit, error) {
	task, requirement, ok, err := s.facts.GetClearDevTaskContext(ctx, strings.TrimSpace(taskID))
	if err != nil {
		return core.CandidateCommit{}, apierr.Internal("CLEARDEV_DEVELOPMENT_TASK_READ_FAILED", "Could not read the development task")
	}
	if !ok {
		return core.CandidateCommit{}, apierr.NotFound("CLEARDEV_DEVELOPMENT_TASK_NOT_FOUND", "ClearDev development task was not found")
	}
	if s.ao == nil {
		return core.CandidateCommit{}, apierr.Internal("CLEARDEV_AO_UNAVAILABLE", "AO facts are not configured")
	}
	aoProject, projectOK, err := s.ao.GetProject(ctx, requirement.AOProjectID)
	if err != nil {
		return core.CandidateCommit{}, apierr.Internal("AO_PROJECT_READ_FAILED", "Could not read the AO project")
	}
	if !projectOK || aoProject.Kind.WithDefault() != domain.ProjectKindSingleRepo {
		return s.rejectCandidate(ctx, task.ID)
	}
	session, sessionOK, err := s.ao.GetSession(ctx, domain.SessionID(strings.TrimSpace(aoSessionID)))
	if err != nil {
		return core.CandidateCommit{}, apierr.Internal("AO_SESSION_READ_FAILED", "Could not read the AO session")
	}
	if !sessionOK || session.IsTerminated || string(session.ProjectID) != requirement.AOProjectID || s.workspace == nil || strings.TrimSpace(session.Metadata.WorkspacePath) == "" {
		return s.rejectCandidate(ctx, task.ID)
	}
	info := ports.WorkspaceInfo{
		Path: session.Metadata.WorkspacePath, Branch: session.Metadata.Branch,
		BaseRef: session.Metadata.DiffBaseRef, RepoPath: session.Metadata.WorkspaceRepoPath,
		SessionID: session.ID, ProjectID: session.ProjectID,
	}
	observation, err := s.workspace.ObserveWorkspace(ctx, info)
	if err != nil || strings.TrimSpace(observation.Path) == "" || observation.Dirty || observation.Staged || observation.Untracked || len(observation.Changes) != 0 {
		return s.rejectCandidate(ctx, task.ID)
	}
	sha, err := core.NormalizeCommitSHA(observation.HeadSHA)
	if err != nil {
		return s.rejectCandidate(ctx, task.ID)
	}
	candidate, err := s.facts.AppendClearDevCandidate(ctx, core.CandidateObservation{
		ID: s.newID(), DevelopmentTaskID: task.ID, AOSessionID: string(session.ID),
		CommitSHA: sha, ObservedAt: s.now().UTC(),
	})
	if err != nil {
		return core.CandidateCommit{}, mapStoreError(err, "REGISTER_CANDIDATE_FAILED")
	}
	return candidate, nil
}

func (s *Service) rejectCandidate(ctx context.Context, taskID string) (core.CandidateCommit, error) {
	_, err := s.facts.AppendClearDevCandidate(ctx, core.CandidateObservation{
		ID: s.newID(), DevelopmentTaskID: taskID, ObservedAt: s.now().UTC(),
	})
	if err == nil {
		return core.CandidateCommit{}, apierr.Internal("REGISTER_CANDIDATE_FAILED", "Invalid candidate observation was unexpectedly accepted")
	}
	return core.CandidateCommit{}, mapStoreError(err, "REGISTER_CANDIDATE_FAILED")
}

// RegisterIntegrationCandidate uses the same managed-worktree authority for a
// requirement-level candidate. Store binds the current confirmed version and
// task-set version; the caller cannot choose either value.
func (s *Service) RegisterIntegrationCandidate(ctx context.Context, requirementID, aoSessionID string) (core.IntegrationCandidate, error) {
	if s.ao == nil {
		return core.IntegrationCandidate{}, apierr.Internal("CLEARDEV_AO_UNAVAILABLE", "AO facts are not configured")
	}
	snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, strings.TrimSpace(requirementID))
	if err != nil {
		return core.IntegrationCandidate{}, apierr.Internal("CLEARDEV_REQUIREMENT_READ_FAILED", "Could not read the ClearDev requirement")
	}
	if !ok {
		return core.IntegrationCandidate{}, apierr.NotFound("CLEARDEV_REQUIREMENT_NOT_FOUND", "ClearDev requirement was not found")
	}
	aoProject, projectOK, err := s.ao.GetProject(ctx, snapshot.Requirement.AOProjectID)
	if err != nil {
		return core.IntegrationCandidate{}, apierr.Internal("AO_PROJECT_READ_FAILED", "Could not read the AO project")
	}
	if !projectOK || aoProject.Kind.WithDefault() != domain.ProjectKindSingleRepo {
		return s.rejectIntegrationCandidate(ctx, snapshot.Requirement.ID)
	}
	session, sessionOK, err := s.ao.GetSession(ctx, domain.SessionID(strings.TrimSpace(aoSessionID)))
	if err != nil {
		return core.IntegrationCandidate{}, apierr.Internal("AO_SESSION_READ_FAILED", "Could not read the AO session")
	}
	if !sessionOK || session.IsTerminated || string(session.ProjectID) != snapshot.Requirement.AOProjectID || s.workspace == nil || strings.TrimSpace(session.Metadata.WorkspacePath) == "" {
		return s.rejectIntegrationCandidate(ctx, snapshot.Requirement.ID)
	}
	observation, err := s.workspace.ObserveWorkspace(ctx, ports.WorkspaceInfo{
		Path: session.Metadata.WorkspacePath, Branch: session.Metadata.Branch,
		BaseRef: session.Metadata.DiffBaseRef, RepoPath: session.Metadata.WorkspaceRepoPath,
		SessionID: session.ID, ProjectID: session.ProjectID,
	})
	if err != nil || strings.TrimSpace(observation.Path) == "" || observation.Dirty || observation.Staged || observation.Untracked || len(observation.Changes) != 0 {
		return s.rejectIntegrationCandidate(ctx, snapshot.Requirement.ID)
	}
	sha, err := core.NormalizeCommitSHA(observation.HeadSHA)
	if err != nil {
		return s.rejectIntegrationCandidate(ctx, snapshot.Requirement.ID)
	}
	candidate, err := s.facts.AppendClearDevIntegrationCandidate(ctx, core.IntegrationCandidateObservation{
		ID: s.newID(), DevelopmentRequirementID: snapshot.Requirement.ID, AOSessionID: string(session.ID),
		CommitSHA: sha, ObservedAt: s.now().UTC(),
	})
	if err != nil {
		return core.IntegrationCandidate{}, mapStoreError(err, "REGISTER_INTEGRATION_CANDIDATE_FAILED")
	}
	return candidate, nil
}

func (s *Service) rejectIntegrationCandidate(ctx context.Context, requirementID string) (core.IntegrationCandidate, error) {
	_, err := s.facts.AppendClearDevIntegrationCandidate(ctx, core.IntegrationCandidateObservation{
		ID: s.newID(), DevelopmentRequirementID: requirementID, ObservedAt: s.now().UTC(),
	})
	if err == nil {
		return core.IntegrationCandidate{}, apierr.Internal("REGISTER_INTEGRATION_CANDIDATE_FAILED", "Invalid integration candidate observation was unexpectedly accepted")
	}
	return core.IntegrationCandidate{}, mapStoreError(err, "REGISTER_INTEGRATION_CANDIDATE_FAILED")
}
