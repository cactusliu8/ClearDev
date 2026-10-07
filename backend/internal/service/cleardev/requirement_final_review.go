package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
)

func requirementFinalReviewPrompt(review core.RequirementFinalReview) string {
	return core.RequirementFinalReviewPrompt(review)
}

// advanceRequirementFinalReview returns ready only for an unmarked historical
// execution or the exact final candidate's persisted requirement-level PASS.
// It deliberately never invokes task rework, fixed-reviewer replacement or a
// caller-selected check command.
func (s *Service) advanceRequirementFinalReview(ctx context.Context, execution core.ComplexExecutionSnapshot, projectID, sourceWorkspace, finalSHA string, checkIDs []string) (progressed, stopped, ready bool, err error) {
	required, err := core.RequirementFinalReviewRequired(execution.Run)
	if err != nil {
		return false, true, false, err
	}
	if !required {
		return false, false, true, nil
	}
	if s.finalReviews == nil {
		return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_REVIEW_UNAVAILABLE")
	}
	if (execution.Run.Mode != core.WorkModeStandard && execution.Run.Mode != core.WorkModeParallel) ||
		len(execution.Tasks) < 1 || len(execution.Tasks) > 3 ||
		execution.Run.FixedBuilderCount < 1 || execution.Run.FixedBuilderCount > 2 ||
		execution.Run.Mode == core.WorkModeStandard && execution.Run.FixedBuilderCount != 1 ||
		execution.Run.Mode == core.WorkModeParallel && execution.Run.FixedBuilderCount != 2 {
		return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_REVIEW_BOUNDED_EXECUTION_REQUIRED")
	}
	if !s.finalReviewWorkspaceMatches(ctx, sourceWorkspace, finalSHA, execution.Run) {
		return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_CANDIDATE_CHANGED")
	}
	// A rework round replaces the candidate, so the earlier review keeps its
	// recorded verdict and the new candidate gets its own review.
	if execution.FinalReview == nil || execution.FinalReview.CandidateCommitSHA != finalSHA {
		review, err := s.buildRequirementFinalReview(ctx, execution, sourceWorkspace, finalSHA, checkIDs)
		if err != nil {
			return false, true, false, err
		}
		err = s.finalReviews.CreateClearDevRequirementFinalReview(ctx, review)
		return err == nil, false, false, err
	}
	review := *execution.FinalReview
	if err := core.ValidateRequirementFinalReviewBinding(review, execution.Run, finalSHA, checkIDs); err != nil || review.SourceWorkspacePath != sourceWorkspace || review.PromptSHA256 != coreDigest([]byte(requirementFinalReviewPrompt(review))) {
		return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_REVIEW_BINDING_INVALID")
	}
	if review.Status == "FAILED" || (review.Status == "SETTLED" && review.Verdict != "PASS") {
		return false, true, false, nil
	}
	if review.Status == "SETTLED" {
		// Provider PASS and its independent session binding were validated when
		// the review settled. A controller may legitimately exit before the
		// completion transaction resumes; liveness is not durable review
		// evidence. The frozen reviewer worktree must still be the exact final
		// candidate, so changing that tree continues to invalidate the PASS.
		if !s.finalReviewWorkspaceMatches(ctx, review.WorkspacePath, finalSHA, execution.Run) {
			return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_REVIEW_WORKSPACE_CHANGED")
		}
		return false, false, review.Verdict == "PASS" && review.ResultID != "", nil
	}
	planning, found, err := s.complex.GetClearDevComplexPlanning(ctx, execution.Run.DevelopmentRequirementID)
	if err != nil || !found {
		return false, true, false, err
	}
	if review.Status == "REQUESTED" && review.PreviousReviewID != "" {
		var packet core.RequirementFinalReviewPacket
		if json.Unmarshal([]byte(review.ReviewPacketJSON), &packet) != nil || packet.PreviousReview == nil {
			return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_RECHECK_BINDING_INVALID")
		}
		prior := packet.PreviousReview
		record, found, err := s.ao.GetSession(ctx, domain.SessionID(prior.AOSessionID))
		if err != nil || !found || !s.validRequirementFinalReviewer(ctx, record, review, execution, planning, projectID) || record.Metadata.WorkspacePath != prior.WorkspacePath || !s.finalReviewWorkspaceMatches(ctx, prior.WorkspacePath, finalSHA, execution.Run) {
			return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_REVIEWER_UNAVAILABLE")
		}
		err = s.finalReviews.BindClearDevRequirementFinalReview(ctx, review.ID, prior.AOSessionID, prior.WorkspacePath, s.now().UTC())
		return err == nil, false, false, err
	}
	if review.Status == "REQUESTED" {
		branch := "cleardev-requirement-final-review-" + review.ID
		if err := s.inspector.PrepareReviewBranch(ctx, sourceWorkspace, branch, finalSHA); err != nil {
			return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_REVIEW_BRANCH_UNAVAILABLE")
		}
		session, blocked, err := s.spawnControlledChatSession(ctx, execution.Run.DevelopmentRequirementID, review.ID, ports.SpawnConfig{
			ProjectID: domain.ProjectID(projectID), Kind: domain.KindWorker,
			Branch: branch, Prompt: "", RequestedMode: domain.SessionModeChat,
			AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto},
			DisplayName: "ClearDev Requirement Final Reviewer", CreationIdempotencyKey: finalReviewerSessionKey(review),
		})
		if blocked {
			return false, false, false, nil
		}
		if err != nil || !s.validRequirementFinalReviewer(ctx, session.SessionRecord, review, execution, planning, projectID) || !s.finalReviewWorkspaceMatches(ctx, session.Metadata.WorkspacePath, finalSHA, execution.Run) {
			return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_REVIEWER_UNAVAILABLE")
		}
		err = s.finalReviews.BindClearDevRequirementFinalReview(ctx, review.ID, string(session.ID), session.Metadata.WorkspacePath, s.now().UTC())
		return err == nil, false, false, err
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(review.AOSessionID))
	if err != nil || !found || !s.validRequirementFinalReviewer(ctx, record, review, execution, planning, projectID) || record.Metadata.WorkspacePath != review.WorkspacePath || !s.finalReviewWorkspaceMatches(ctx, review.WorkspacePath, finalSHA, execution.Run) {
		return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_REVIEW_WORKSPACE_CHANGED")
	}
	step := review.Step()
	prompt := requirementFinalReviewPrompt(review)
	if review.Status == "PENDING" {
		if err := s.validateFinalReviewInputRecovery(ctx, review); err != nil {
			return false, true, false, err
		}
		if err := s.prepareFinalReviewEvidence(ctx, review); err != nil {
			return false, true, false, err
		}
		if err := s.relayAgentTurn(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, step, review.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return false, false, false, err
		}
		err = s.finalReviews.MarkClearDevRequirementFinalReviewSent(ctx, review.ID, s.now().UTC())
		return err == nil, false, false, err
	}
	if review.Status != "SENT" {
		return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_REVIEW_STATE_INVALID")
	}
	validate := func(raw []byte) error {
		result, err := core.ParseRequirementFinalReviewResult(raw)
		if err == nil && result.Verdict == "PASS" && finalReviewRequiresTrial(review) && !s.stageReviewTrialReady(ctx, review, execution.Run) {
			return errors.New("STAGE_TRIAL_REQUIRED: start and personally operate the exact final candidate before PASS")
		}
		return err
	}
	fail := func(ctx context.Context, _ core.AgentStep, reason core.ReasonCode) error {
		err := s.finalReviews.FailClearDevRequirementFinalReview(ctx, review.ID, reason, s.now().UTC())
		if err == nil && finalReviewRequiresTrial(review) && s.resultPreview != nil {
			_, _ = s.resultPreview.Stop(ctx, domain.SessionID(review.AOSessionID))
		}
		return err
	}
	message, stopped, err := s.awaitValidAgentJSON(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, review.AOSessionID, step, prompt, validate, standardStepReasons{
		Invalid: "REQUIREMENT_FINAL_REVIEW_RESULT_INVALID", Timeout: "REQUIREMENT_FINAL_REVIEW_TIMEOUT", Unavailable: "REQUIREMENT_FINAL_REVIEWER_UNAVAILABLE",
	}, fail, errComplexExecutionStopped)
	if err != nil || stopped {
		return false, stopped, false, err
	}
	// Model output does not permit accepting a worktree changed during review.
	if !s.finalReviewWorkspaceMatches(ctx, sourceWorkspace, finalSHA, execution.Run) || !s.finalReviewWorkspaceMatches(ctx, review.WorkspacePath, finalSHA, execution.Run) {
		return s.stopRequirementFinalReview(ctx, execution, "REQUIREMENT_FINAL_CANDIDATE_CHANGED")
	}
	result, parseErr := core.ParseRequirementFinalReviewResult([]byte(message.Text))
	if parseErr != nil {
		return false, true, false, parseErr
	}
	if result.Verdict == "PASS" && finalReviewRequiresTrial(review) && !s.stageReviewTrialReady(ctx, review, execution.Run) {
		return s.stopRequirementFinalReview(ctx, execution, "STAGE_TRIAL_REQUIRED")
	}
	err = s.finalReviews.SettleClearDevRequirementFinalReview(ctx, review.ID, message.TurnID, message.MessageID, s.now().UTC())
	if err == nil && finalReviewRequiresTrial(review) && s.resultPreview != nil {
		_, _ = s.resultPreview.Stop(ctx, domain.SessionID(review.AOSessionID))
	}
	return err == nil, false, false, err
}

func finalReviewerSessionKey(review core.RequirementFinalReview) string {
	return "cleardev-requirement-final-review:" + review.SessionReviewID()
}

func (s *Service) validRequirementFinalReviewer(ctx context.Context, record domain.SessionRecord, review core.RequirementFinalReview, execution core.ComplexExecutionSnapshot, planning core.ComplexPlanningSnapshot, projectID string) bool {
	binding := core.ComplexExecutionRoleBinding{SessionCreationIdempotencyKey: finalReviewerSessionKey(review)}
	if !s.validComplexExecutionWorker(ctx, record, binding, projectID) || record.IsTerminated || record.Activity.State == domain.ActivityExited || record.Metadata.WorkspacePath == review.SourceWorkspacePath || record.Metadata.Branch != "cleardev-requirement-final-review-"+review.SessionReviewID() || complexExecutionSessionAlreadyBound(execution, planning, string(record.ID)) {
		return false
	}
	if review.AOSessionID != "" && string(record.ID) != review.AOSessionID {
		return false
	}
	for _, prior := range execution.RoleBindings {
		if prior.WorkspacePath == record.Metadata.WorkspacePath {
			return false
		}
	}
	return true
}

func (s *Service) finalReviewWorkspaceMatches(ctx context.Context, workspace, sha string, runs ...core.ComplexExecutionRun) bool {
	if workspace == "" || !validComplexExecutionCommitSHA(sha) {
		return false
	}
	var run core.ComplexExecutionRun
	if len(runs) == 1 {
		run = runs[0]
	}
	inspection, err := s.inspectExecutionCandidate(ctx, run, workspace, sha)
	return err == nil && inspection.BaseSHA == sha && inspection.CandidateSHA == sha && len(inspection.Paths) == 0
}

func (s *Service) buildRequirementFinalReview(ctx context.Context, execution core.ComplexExecutionSnapshot, workspace, sha string, checkIDs []string) (core.RequirementFinalReview, error) {
	snapshot, found, err := s.facts.GetClearDevRequirement(ctx, execution.Run.DevelopmentRequirementID)
	if err != nil {
		return core.RequirementFinalReview{}, err
	}
	if !found {
		return core.RequirementFinalReview{}, errors.New("requirement final review has no requirement")
	}
	version, confirmed := currentConfirmedVersion(snapshot)
	planning, found, err := s.complex.GetClearDevComplexPlanning(ctx, execution.Run.DevelopmentRequirementID)
	if err != nil {
		return core.RequirementFinalReview{}, err
	}
	plan, _, _, planErr := s.executionPlanForRun(ctx, planning, execution.Run)
	if !confirmed || !found || planErr != nil || version.ID != execution.Run.RequirementVersionID || version.SHA256 != execution.Run.RequirementSHA256 || plan.ID != execution.Run.PlanID || plan.PlanSHA256 != execution.Run.PlanSHA256 {
		return core.RequirementFinalReview{}, errors.New("requirement final review no longer matches confirmed requirement and approved plan")
	}
	reviewerChecks, err := s.requirementFinalReviewerChecks(ctx, execution.Reviews)
	if err != nil {
		return core.RequirementFinalReview{}, err
	}
	review := core.RequirementFinalReview{
		ID: s.newID(), ExecutionRunID: execution.Run.ID, DevelopmentRequirementID: execution.Run.DevelopmentRequirementID,
		RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, PlanID: plan.ID, PlanSHA256: plan.PlanSHA256,
		CandidateCommitSHA: sha, BaseCommitSHA: execution.Run.InitialBaseCommitSHA, SourceWorkspacePath: workspace,
		CheckRunIDs: append([]string(nil), checkIDs...), Status: "REQUESTED", CreatedAt: s.now().UTC(),
	}
	// A rework round leaves an earlier verification row behind; the packet
	// carries the latest verification per task, in task order, so the stored
	// history stays intact without double-counting a task.
	latest := map[string]core.ComplexExecutionVerification{}
	for _, verification := range execution.Verifications {
		latest[verification.ComplexExecutionTaskID] = verification
	}
	verifications := make([]core.ComplexExecutionVerification, 0, len(latest))
	for _, task := range execution.Tasks {
		if verification, ok := latest[task.ID]; ok {
			verifications = append(verifications, verification)
		}
	}
	packet := core.RequirementFinalReviewPacket{
		SchemaVersion: 1, ReviewID: review.ID, Requirement: version, Plan: plan, Run: execution.Run,
		CandidateCommitSHA: sha, BaseCommitSHA: review.BaseCommitSHA, CheckRunIDs: review.CheckRunIDs,
		Tasks: execution.Tasks, Dispatches: execution.Dispatches, Verifications: verifications,
		TaskReviews: execution.Reviews, ReviewerChecks: reviewerChecks, CheckSpecs: execution.CheckSpecs, CheckRuns: execution.CheckRuns,
		Compositions: execution.Compositions, Exception: execution.Exception, FixedRecoveries: execution.FixedRecoveries,
		PlannerRuntime: execution.PlannerRuntime,
	}
	_, project, err := core.ProjectContractFromRun(execution.Run)
	if err != nil {
		return core.RequirementFinalReview{}, err
	}
	packet.FunctionalTrial = project
	if project {
		// Login shells can replace the session PATH with an older installed CLI.
		// Freeze this daemon's executable into the new trial instructions instead.
		packet.TrialCLIExecutable, err = os.Executable()
		if err != nil {
			return core.RequirementFinalReview{}, err
		}
		// Host process/network access is needed by the controlled CLI, not by
		// source review. Save the hint only in new packets to preserve old prompts.
		packet.TrialHostExecution = true
	}
	if core.BoundedMailAttempts(execution.Run) {
		attempts, ok := s.complexExecution.(interface {
			ListClearDevMailAttempts(context.Context, string) ([]core.MailAttemptSlot, error)
		})
		if !ok {
			return core.RequirementFinalReview{}, errors.New("mail attempt evidence store unavailable")
		}
		slots, err := attempts.ListClearDevMailAttempts(ctx, execution.Run.ID)
		if err != nil {
			return core.RequirementFinalReview{}, err
		}
		packet.AttemptEvidence = core.NewFinalReviewAttemptEvidence(slots)
	}
	if evidence, ok := s.inspector.(ports.ClearDevFinalReviewEvidence); ok {
		packet.WorkflowRecoveries = execution.WorkflowRecoveries
		packet.EvidenceFile, err = evidence.FinalReviewEvidencePath(ctx, review.ID)
		if err != nil {
			return core.RequirementFinalReview{}, err
		}
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		return core.RequirementFinalReview{}, err
	}
	review.ReviewPacketJSON, review.ReviewPacketSHA256 = string(raw), coreDigest(raw)
	review.PromptSHA256 = coreDigest([]byte(requirementFinalReviewPrompt(review)))
	if err := core.ValidateRequirementFinalReviewBinding(review, execution.Run, sha, checkIDs); err != nil {
		return core.RequirementFinalReview{}, err
	}
	return review, nil
}

func finalReviewRequiresTrial(review core.RequirementFinalReview) bool {
	var packet core.RequirementFinalReviewPacket
	return json.Unmarshal([]byte(review.ReviewPacketJSON), &packet) == nil && packet.FunctionalTrial
}

func (s *Service) stageReviewTrialReady(ctx context.Context, review core.RequirementFinalReview, run core.ComplexExecutionRun) bool {
	manager, ok := s.resultPreview.(StageReviewTrialManager)
	contract, project, err := core.ProjectContractFromRun(run)
	if err != nil || !project || !s.stageTrialCommandsComplete(ctx, review, contract) {
		return false
	}
	if !core.ProjectTrialNeedsService(contract.Basis) {
		return true
	}
	if !ok {
		return false
	}
	digest, err := core.ProjectExecutionContractDigest(contract)
	return err == nil && manager.StageTrialStatus(domain.SessionID(review.AOSessionID), review.CandidateCommitSHA, digest).State == previewserver.StateReady
}

func (s *Service) requirementFinalReviewerChecks(ctx context.Context, reviews []core.ComplexExecutionReview) ([]core.RequirementFinalReviewReviewerChecks, error) {
	store, ok := s.complexExecution.(ReviewerCheckStore)
	if !ok {
		return nil, errors.New("requirement final review cannot read task Reviewer supplemental checks")
	}
	bundles := make([]core.RequirementFinalReviewReviewerChecks, 0)
	for _, review := range reviews {
		request, results, found, err := store.GetClearDevReviewerChecks(ctx, review.ID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		bundles = append(bundles, core.RequirementFinalReviewReviewerChecks{
			ReviewID: review.ID,
			Request:  request,
			Results:  append([]core.ReviewCheckEvidence(nil), results...),
		})
	}
	return bundles, nil
}

func (s *Service) stopRequirementFinalReview(ctx context.Context, execution core.ComplexExecutionSnapshot, reason core.ReasonCode) (bool, bool, bool, error) {
	if review := execution.FinalReview; review != nil && review.Status != "SETTLED" && s.finalReviews != nil {
		err := s.finalReviews.FailClearDevRequirementFinalReview(ctx, review.ID, reason, s.now().UTC())
		if err == nil && finalReviewRequiresTrial(*review) && s.resultPreview != nil && review.AOSessionID != "" {
			_, _ = s.resultPreview.Stop(ctx, domain.SessionID(review.AOSessionID))
		}
		return err == nil, true, false, err
	}
	// Never rewrite an already settled PASS. Persist the new stop on the
	// existing Builder binding so restoring the old tree cannot reuse that PASS.
	changed, err := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, execution.Run.BuilderRoleBindingID, reason, s.now().UTC())
	return changed, true, false, err
}

func (s *Service) prepareFinalReviewEvidence(ctx context.Context, review core.RequirementFinalReview) error {
	var packet core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		return err
	}
	if packet.EvidenceFile == "" {
		return nil
	}
	evidence, ok := s.inspector.(ports.ClearDevFinalReviewEvidence)
	if !ok {
		return errors.New("final review evidence writer unavailable")
	}
	return evidence.WriteFinalReviewEvidence(ctx, review.ID, packet.EvidenceFile, review.ReviewPacketJSON, review.ReviewPacketSHA256)
}
