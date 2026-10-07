package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// WorkflowRecoveryInput supplies context for one exact stopped action.
type WorkflowRecoveryInput struct {
	RequestID      string `json:"requestId"`
	ExecutionRunID string `json:"executionRunId"`
	Action         string `json:"action"`
	TargetID       string `json:"targetId"`
	Supplement     string `json:"supplement"`
}

type workflowRecoveryStore interface {
	ApplyClearDevWorkflowRecovery(context.Context, core.WorkflowRecovery, *core.RequirementFinalReview) error
}

// getWorkflowRecovery derives only authoritative actions and their history.
// The public read wrapper adds diagnosis without changing these admission rules.
func (s *Service) getWorkflowRecovery(ctx context.Context, requirementID string) (core.WorkflowRecoveryView, error) {
	out := core.WorkflowRecoveryView{Options: []core.WorkflowRecoveryOption{}, History: []core.WorkflowRecovery{}}
	if s.complexExecution == nil {
		return out, apierr.Internal("CLEARDEV_UNAVAILABLE", "Recovery is unavailable")
	}
	execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil {
		return out, err
	}
	if !found {
		return s.getPlanningStepRecovery(ctx, requirementID)
	}
	out.ExecutionRunID = execution.Run.ID
	planning, err := s.getPlanningStepRecovery(ctx, requirementID)
	if err != nil {
		return out, err
	}
	// Starting execution must not hide the earlier compilation recovery.
	// Only execution actions remain available; planning history stays read-only.
	out.History = append(out.History, planning.History...)
	out.History = append(out.History, execution.WorkflowRecoveries...)
	out.Options = s.workflowRecoveryOptions(ctx, execution)
	if checks, ok := s.complexExecution.(builderSessionCheckStore); ok {
		out.BuilderSessionChecks, err = checks.ListClearDevBuilderSessionChecks(ctx, execution.Run.ID)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Diagnosis = &core.WorkflowDiagnosis{ObservedAt: s.now().UTC(), ReadError: "DIAGNOSIS_UNAVAILABLE", Issues: []core.WorkflowDiagnosisIssue{}}
			return out, nil
		}
		s.addBuilderSessionRecheckRegistration(ctx, execution, &out)
	}
	if err := s.workflowRecoveryCurrent(ctx, execution); err != nil {
		for i := range out.Options {
			out.Options[i].UnavailableReason = "EXECUTION_NOT_CURRENT"
		}
	}
	for i := range out.Options {
		option := &out.Options[i]
		if option.UnavailableReason != "" {
			continue
		}
		if option.Action == core.RecoveryRequestBuilderReplacement || option.Action == core.RecoveryContinueBuilderReplacement {
			continue // The new handoff observes its own exact source at explicit writes.
		}
		if option.Action != core.RecoveryRetryStage {
			for _, task := range execution.Tasks {
				if task.ID == option.TaskID {
					for _, d := range execution.Dispatches {
						if d.ID == task.CurrentDispatchID {
							if _, reason := s.workflowRecoverySession(ctx, execution, d, option.Action); reason != "" {
								option.UnavailableReason = reason
							}
						}
					}
				}
			}
		}
		if option.UnavailableReason != "" {
			continue
		}
		if option.Action == core.RecoveryRetryReview {
			for _, r := range execution.Reviews {
				if r.ID == option.TargetID && r.Status == core.LocalReviewStatusFailed && !s.workflowStepNeverAttempted(ctx, execution, r.AgentStepID) {
					option.UnavailableReason = "RESULT_NOT_SETTLED"
				}
			}
		}
		if option.Action == core.RecoveryRetryBuilderSession {
			for _, d := range execution.Dispatches {
				if d.SettledAt != nil && core.WorkflowBuilderRetryTarget(d.ID, *d.SettledAt) == option.TargetID && !s.builderSessionStepUnsent(ctx, execution, d.AgentStepID) {
					option.UnavailableReason = "RESULT_NOT_SETTLED"
				}
			}
		}
		if option.Action == core.RecoveryRetryCheck {
			if allowed, err := s.workflowCheckSettled(ctx, execution, option.TargetID); err != nil || !allowed {
				option.UnavailableReason = "RESULT_NOT_SETTLED"
			}
		}
		if option.Action == core.RecoveryRetryStage {
			if allowed, err := s.workflowStageRetryKnown(ctx, execution); err != nil || !allowed {
				option.UnavailableReason = "RESULT_NOT_SETTLED"
			}
		}
	}
	if err := s.addBuilderReplacementView(ctx, requirementID, &out); err != nil {
		return out, err
	}
	if err := s.addStoppedCheckRecoveryView(ctx, requirementID, &out); err != nil {
		return out, err
	}
	if err := s.addExtraCoordinationView(ctx, requirementID, &out); err != nil {
		return out, err
	}
	if err := s.addPlannerCoordinationRecoveryView(ctx, requirementID, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Service) workflowRecoveryCurrent(ctx context.Context, e core.ComplexExecutionSnapshot) error {
	contract, project, err := core.ProjectContractFromRun(e.Run)
	if err != nil || !project || e.Run.Mode != core.WorkModeStandard || e.Run.CompletedAt != nil || !s.selectedProjectCurrent(ctx, &contract.Selection) {
		return apierr.Conflict("RECOVERY_NOT_CURRENT", "当前来源或执行条件已改变，不能恢复旧步骤", nil)
	}
	snapshot, found, err := s.facts.GetClearDevRequirement(ctx, e.Run.DevelopmentRequirementID)
	if err != nil {
		return err
	}
	version, confirmed := currentConfirmedVersion(snapshot)
	if !found || snapshot.Requirement.CancelledAt != nil || !confirmed || version.ID != e.Run.RequirementVersionID || version.SHA256 != e.Run.RequirementSHA256 {
		return apierr.Conflict("RECOVERY_NOT_CURRENT", "规格已改变或已停止，不能恢复旧步骤", nil)
	}
	if s.direction != nil {
		stopped, err := s.direction.HasActiveClearDevDirectionStop(ctx, e.Run.RequirementVersionID)
		if err != nil {
			return err
		}
		if stopped {
			return apierr.Conflict("RECOVERY_DIRECTION_STOP", "请先处理当前方向变更", nil)
		}
	}
	return nil
}

// RequestWorkflowRecovery records supplementary context without granting approval.
func (s *Service) RequestWorkflowRecovery(ctx context.Context, requirementID string, input WorkflowRecoveryInput) (core.WorkflowRecoveryView, error) {
	if input.Action == core.RecoveryRequestStoppedCheck {
		return s.requestStoppedCheckRecovery(ctx, requirementID, input)
	}
	if input.Action == core.RecoveryRequestExtraCoordination {
		return s.requestExtraCoordination(ctx, requirementID, input)
	}
	if input.Action == core.RecoveryRetryPlannerCoordination {
		return s.requestPlannerCoordinationRecovery(ctx, requirementID, input)
	}
	if input.Action == core.RecoveryRequestBuilderReplacement || input.Action == core.RecoveryContinueBuilderReplacement {
		return s.requestBuilderReplacement(ctx, requirementID, input)
	}
	if input.Action == core.RecoveryRequestExtraPlanningAttempt || input.Action == core.RecoveryContinueExtraPlanningAttempt {
		return s.requestExtraPlanningAttempt(ctx, requirementID, input)
	}
	if input.Action == core.RecoveryRetryPlanningStep {
		return s.requestPlanningStepRecovery(ctx, requirementID, input)
	}
	empty := core.WorkflowRecoveryView{}
	input.Supplement = core.NormalizeWorkflowRecoverySupplement(input.Action, input.Supplement)
	if input.RequestID == "" || len(input.RequestID) > 100 || strings.TrimSpace(input.RequestID) != input.RequestID || input.Supplement == "" || len(input.Supplement) > 16000 {
		return empty, apierr.Invalid("RECOVERY_CONTEXT_REQUIRED", "请填写补充说明或已修复的环境问题", nil)
	}
	store, ok := s.complexExecution.(workflowRecoveryStore)
	if !ok {
		return empty, apierr.Internal("RECOVERY_UNAVAILABLE", "Recovery storage is unavailable")
	}
	e, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil {
		return empty, err
	}
	if !found || e.Run.ID != input.ExecutionRunID {
		return empty, apierr.Conflict("RECOVERY_NOT_CURRENT", "恢复请求已过期，请刷新进度", nil)
	}
	for _, prior := range e.WorkflowRecoveries {
		if prior.ID == input.RequestID {
			if prior.TargetID != input.TargetID || prior.Action != input.Action || prior.Supplement != input.Supplement {
				return empty, apierr.Conflict("RECOVERY_REQUEST_CHANGED", "同一请求不能更改补充内容", nil)
			}
			s.scheduleComplexStandardExecution(requirementID)
			return s.GetWorkflowRecovery(ctx, requirementID)
		}
	}
	if err := s.workflowRecoveryCurrent(ctx, e); err != nil {
		return empty, err
	}
	var selected *core.WorkflowRecoveryOption
	for _, option := range s.workflowRecoveryOptions(ctx, e) {
		if option.Action == input.Action && option.TargetID == input.TargetID {
			selectedOption := option
			selected = &selectedOption
			break
		}
	}
	if selected == nil || selected.UnavailableReason != "" {
		return empty, apierr.Conflict("RECOVERY_UNAVAILABLE", "此步骤已变化、仍在执行或缺少可用预算，请刷新查看下一步", nil)
	}
	r := core.WorkflowRecovery{ID: input.RequestID, ExecutionRunID: e.Run.ID, Action: input.Action, TargetID: input.TargetID, TaskID: selected.TaskID, SuccessorID: s.newID(), OriginalSummary: selected.Summary, Supplement: input.Supplement, CreatedAt: s.now().UTC()}
	var final *core.RequirementFinalReview
	if input.Action == core.RecoveryRetryStage {
		old := e.FinalReview
		if allowed, readErr := s.workflowStageRetryKnown(ctx, e); readErr != nil || !allowed {
			return empty, apierr.Conflict("RECOVERY_RESULT_UNKNOWN", "原审核的外部执行尚未可靠结算，不能重复发送", nil)
		}
		if old == nil || !s.finalReviewWorkspaceMatches(ctx, old.SourceWorkspacePath, old.CandidateCommitSHA, e.Run) {
			return empty, apierr.Conflict("RECOVERY_CANDIDATE_CHANGED", "待验收版本已改变，不能复用旧审核", nil)
		}
		if rejected, rejectErr := s.finalReviewInputRejected(ctx, old); rejectErr != nil {
			return empty, rejectErr
		} else if rejected {
			r.StepID = old.Step().ID
		}
		r.OriginalStoppedAt = *old.SettledAt
		r.OriginalStatus = old.Status
		r.OriginalReason = string(old.ReasonCode)
		r.CandidateSHA = old.CandidateCommitSHA
		review, err := s.buildRequirementFinalReview(ctx, e, old.SourceWorkspacePath, old.CandidateCommitSHA, old.CheckRunIDs)
		if err != nil {
			return empty, err
		}
		r.SuccessorID = review.ID
		var packet core.RequirementFinalReviewPacket
		if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
			return empty, err
		}
		packet.Recovery = &r
		raw, err := json.Marshal(packet)
		if err != nil {
			return empty, err
		}
		review.ReviewPacketJSON = string(raw)
		review.ReviewPacketSHA256 = coreDigest(raw)
		review.PromptSHA256 = coreDigest([]byte(requirementFinalReviewPrompt(review)))
		final = &review
	} else {
		var dispatch core.ComplexExecutionDispatch
		for _, task := range e.Tasks {
			if task.ID == r.TaskID {
				for _, d := range e.Dispatches {
					if d.ID == task.CurrentDispatchID {
						dispatch = d
					}
				}
			}
		}
		r.OriginalStoppedAt = *dispatch.SettledAt
		r.DispatchID = dispatch.ID
		r.OriginalStatus = string(dispatch.Status)
		r.OriginalReason = string(dispatch.ReasonCode)
		r.CandidateSHA = dispatch.CandidateCommitSHA
		r.StepID = dispatch.AgentStepID
		r.BindingID = dispatch.BuilderRoleBindingID
		if r.Action == core.RecoveryRetryReview {
			for _, binding := range e.RoleBindings {
				if binding.ID == r.TargetID && binding.Role == core.StandardRoleReviewer {
					r.StepID = ""
					r.BindingID = binding.ID
				}
			}
			for _, review := range e.Reviews {
				if review.ID == r.TargetID {
					r.StepID = review.AgentStepID
					r.BindingID = review.ReviewerRoleBindingID
				}
			}
		}
		session, reason := s.workflowRecoverySession(ctx, e, dispatch, r.Action)
		if reason != "" {
			return empty, apierr.Conflict(reason, "原开发会话身份或恢复能力不可用，请先处理会话连接", nil)
		}
		if r.Action == core.RecoveryContinueBuilder || r.Action == core.RecoveryRetryBuilderSession {
			r.ProviderConversationID = session.Metadata.ProviderConversationID
			if r.Action == core.RecoveryRetryBuilderSession && !s.builderSessionStepUnsent(ctx, e, dispatch.AgentStepID) {
				return empty, apierr.Conflict("RECOVERY_RESULT_UNKNOWN", "原步骤已有投递尝试，不能重复发送", nil)
			}
		}
		inspection, err := s.inspectExecutionCandidate(ctx, e.Run, session.Metadata.WorkspacePath, dispatch.BaseCommitSHA)
		expected := dispatch.CandidateCommitSHA
		if expected == "" {
			expected = dispatch.BaseCommitSHA
			for _, old := range e.Dispatches {
				if old.ComplexExecutionTaskID == dispatch.ComplexExecutionTaskID && old.Round < dispatch.Round && old.CandidateCommitSHA != "" {
					expected = old.CandidateCommitSHA
				}
			}
		}
		r.CandidateSHA = expected
		if errors.Is(err, ports.ErrClearDevWorkspaceDirty) && (r.Action == core.RecoveryContinueBuilder || r.Action == core.RecoveryRetryBuilderSession) {
			r.WorkingTreeSHA256, err = s.workflowWorkingTreeDigest(ctx, e, dispatch, expected, session.Metadata.WorkspacePath)
			if err == nil {
				inspection = ports.ClearDevCandidateInspection{BaseSHA: dispatch.BaseCommitSHA, CandidateSHA: expected}
			}
		}
		if err != nil || inspection.BaseSHA != dispatch.BaseCommitSHA || inspection.CandidateSHA != expected {
			return empty, apierr.Conflict("RECOVERY_CANDIDATE_CHANGED", "工作目录版本已改变，不能恢复旧步骤", nil)
		}

		// A Planner diagnosed these exact unfinished bytes. Even an empty or
		// clean baseline must receive the same source seal; "clean" is not a
		// substitute for the bridge's bound worktree digest.
		if r.Action == core.RecoveryContinueBuilder {
			if bridgeStore, ok := s.complexExecution.(interface {
				GetClearDevFailureCoordinationSource(context.Context, string) (core.FailureCoordinationSource, bool, error)
			}); ok {
				source, found, readErr := bridgeStore.GetClearDevFailureCoordinationSource(ctx, dispatch.ID+":planner-coordination")
				if readErr != nil {
					return empty, readErr
				}
				if found {
					digest, readErr := s.workflowWorkingTreeDigest(ctx, e, dispatch, expected, session.Metadata.WorkspacePath)
					if readErr != nil || source.ExecutionRunID != e.Run.ID || source.DispatchID != dispatch.ID || digest != source.WorkingTreeSHA256 {
						return empty, apierr.Conflict("RECOVERY_CANDIDATE_CHANGED", "原工作目录与工程协调时保存的内容不一致，需要人工核对", nil)
					}
					r.WorkingTreeSHA256 = digest
				}
			}
		}

		if r.Action == core.RecoveryContinueBuilder && dispatch.ReasonCode == "REVIEW_BLOCKED" {
			if allowed, readErr := s.reviewFailureRepairReady(ctx, e, dispatch); readErr != nil || !allowed {
				return empty, apierr.Conflict("RECOVERY_RESULT_UNKNOWN", "原审核、开发会话或审核检查尚未可靠结算，不能启动返工", nil)
			}
		}
		if r.Action == core.RecoveryRetryCheck {
			if allowed, readErr := s.workflowCheckSettled(ctx, e, r.TargetID); readErr != nil || !allowed {
				return empty, apierr.Conflict("RECOVERY_RESULT_UNKNOWN", "原检查的外部执行尚未可靠结算，不能启动第二次检查", nil)
			}
		}

	}
	if err := store.ApplyClearDevWorkflowRecovery(ctx, r, final); err != nil {
		s.logger.Error("ClearDev workflow recovery rejected", "targetID", r.TargetID, "error", err)
		return empty, mapStoreError(err, "RECOVERY_REJECTED")
	}
	s.scheduleComplexStandardExecution(requirementID)
	return s.GetWorkflowRecovery(ctx, requirementID)
}

func workflowRecoveredTarget(s core.ComplexExecutionSnapshot, id string) bool {
	for _, r := range s.WorkflowRecoveries {
		if r.TargetID == id {
			return true
		}
	}
	return false
}

func workflowBuilderSupplement(s core.ComplexExecutionSnapshot, task core.ComplexExecutionTask) string {
	var selected *core.WorkflowRecovery
	for i := range s.WorkflowRecoveries {
		r := &s.WorkflowRecoveries[i]
		if r.Action == core.RecoveryContinueBuilder && r.TaskID == task.ID {
			for _, d := range s.Dispatches {
				if d.ID == r.DispatchID && d.Round+1 == task.CurrentRound {
					selected = r
				}
			}
		}
	}
	if selected == nil {
		return ""
	}
	raw, _ := json.Marshal(selected)
	return "\nRecovery context (supplementary information only; it does not change approved scope, permissions, checks or acceptance):\n" + string(raw)
}

func (s *Service) workflowCheckSettled(ctx context.Context, e core.ComplexExecutionSnapshot, target string) (bool, error) {
	runner, supported := s.checks.(interface {
		CanRetryWorkflowCheck(context.Context, ports.ClearDevCheckRequest) (bool, error)
	})
	if !supported {
		return false, nil
	}
	contract, project, err := core.ProjectContractFromRun(e.Run)
	if err != nil || !project {
		return false, err
	}
	for _, check := range e.CheckRuns {
		if check.ID != target {
			continue
		}
		spec, found := complexCheckSpecByID(e, check.CheckSpecFactID)
		if !found {
			return false, nil
		}
		for _, dispatch := range e.Dispatches {
			if dispatch.ID != check.DispatchID {
				continue
			}
			binding, found := complexExecutionBindingByID(e, dispatch.BuilderRoleBindingID)
			if !found {
				return false, nil
			}
			request := complexCandidateCheckRequest(check.ID, binding.WorkspacePath, check.CandidateCommitSHA, spec)
			request.ProjectExecution = &contract
			return runner.CanRetryWorkflowCheck(ctx, request)
		}
	}
	return false, nil
}

func (s *Service) workflowStageRetryKnown(ctx context.Context, e core.ComplexExecutionSnapshot) (bool, error) {
	if e.FinalReview == nil {
		return false, nil
	}
	if e.FinalReview.Status != "FAILED" || e.FinalReview.ReasonCode == "STAGE_TRIAL_REQUIRED" {
		return true, nil
	}
	if rejected, err := s.finalReviewInputRejected(ctx, e.FinalReview); err != nil || rejected {
		return rejected, err
	}
	if s.attempts == nil {
		return false, nil
	}
	attempts, err := s.attempts.ListLatestClearDevAgentAttemptStates(ctx, e.Run.DevelopmentRequirementID)
	if err != nil {
		return false, err
	}
	for _, attempt := range attempts {
		if attempt.LogicalStepID == e.FinalReview.Step().ID {
			return false, nil
		}
	}
	return e.FinalReview.SentAt == nil, nil
}

func (s *Service) workflowStepNeverAttempted(ctx context.Context, e core.ComplexExecutionSnapshot, stepID string) bool {
	if s.attempts == nil {
		return false
	}
	attempts, err := s.attempts.ListLatestClearDevAgentAttemptStates(ctx, e.Run.DevelopmentRequirementID)
	if err != nil {
		return false
	}
	for _, a := range attempts {
		if a.LogicalStepID == stepID {
			return false
		}
	}
	return true
}

// restoreWorkflowBuilder restores no provider turn: the normal durable relay
// still owns delivery of the exact pending step after identity is rechecked.
func (s *Service) restoreWorkflowBuilder(ctx context.Context, e core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, d core.ComplexExecutionDispatch, b core.ComplexExecutionRoleBinding, rec domain.SessionRecord) bool {
	if s.restoreOriginalAgentSession == nil || rec.Metadata.ProviderConversationID == "" || !s.workflowStepNeverAttempted(ctx, e, d.AgentStepID) {
		return false
	}
	authorized := false
	for _, r := range e.WorkflowRecoveries {
		if r.TaskID != task.ID || r.ProviderConversationID != rec.Metadata.ProviderConversationID {
			continue
		}
		if r.Action == core.RecoveryRetryBuilderSession && r.DispatchID == d.ID {
			authorized = true
		}
		if r.Action == core.RecoveryContinueBuilder {
			for _, old := range e.Dispatches {
				if old.ID == r.DispatchID && old.Round+1 == d.Round {
					authorized = true
				}
			}
		}
	}
	if !authorized || s.validateComplexExecutionDispatchWorkspace(ctx, e, task, d, rec.Metadata.WorkspacePath) != nil {
		return false
	}
	blocked, model, err := s.runControlledModelPreflight(ctx, e.Run.DevelopmentRequirementID, b.ID, rec.ProjectID, rec.Metadata.Model)
	if err != nil || blocked || model != rec.Metadata.Model {
		return false
	}
	mode, err := s.restoreOriginalAgentSession(ctx, rec.ID)
	if err != nil || mode != "native" {
		return false
	}
	after, found, err := s.ao.GetSession(ctx, rec.ID)
	return err == nil && found && !after.IsTerminated && after.Activity.State != domain.ActivityExited && after.Metadata.ProviderConversationID == rec.Metadata.ProviderConversationID && after.Metadata.WorkspacePath == rec.Metadata.WorkspacePath && after.Metadata.Model == rec.Metadata.Model && s.validComplexExecutionWorker(ctx, after, b, string(rec.ProjectID)) && s.validateComplexExecutionDispatchWorkspace(ctx, e, task, d, after.Metadata.WorkspacePath) == nil
}

func (s *Service) workflowRecoverySession(ctx context.Context, e core.ComplexExecutionSnapshot, d core.ComplexExecutionDispatch, action string) (domain.SessionRecord, string) {
	var empty domain.SessionRecord
	binding, found := complexExecutionBindingByID(e, d.BuilderRoleBindingID)
	if !found || binding.Status != core.RoleBindingStatusBound {
		return empty, "RECOVERY_SESSION_UNAVAILABLE"
	}
	requirement, found, err := s.facts.GetClearDevRequirement(ctx, e.Run.DevelopmentRequirementID)
	if err != nil || !found {
		return empty, "RECOVERY_SESSION_UNAVAILABLE"
	}
	rec, found, err := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil || !found || rec.IsTerminated || rec.Metadata.WorkspacePath != binding.WorkspacePath || !s.validComplexExecutionWorker(ctx, rec, binding, requirement.Requirement.AOProjectID) {
		return empty, "RECOVERY_SESSION_UNAVAILABLE"
	}
	if (action == core.RecoveryContinueBuilder || action == core.RecoveryRetryBuilderSession) && rec.Activity.State == domain.ActivityExited && (rec.Metadata.ProviderConversationID == "" || s.restoreOriginalAgentSession == nil) {
		return empty, "RECOVERY_NATIVE_IDENTITY_MISSING"
	}
	return rec, ""
}

func (s *Service) workflowWorkingTreeDigest(ctx context.Context, e core.ComplexExecutionSnapshot, d core.ComplexExecutionDispatch, head, workspace string) (string, error) {
	reader, ok := s.inspector.(interface {
		InspectWorkflowWorkingTree(context.Context, ports.ClearDevMailFreezeRequest) (string, error)
	})
	if !ok {
		return "", ports.ErrClearDevWorkspaceDirty
	}
	contract, project, err := core.ProjectContractFromRun(e.Run)
	if err != nil || !project {
		return "", ports.ErrClearDevCandidateInvalid
	}
	binding, ok := complexExecutionBindingByID(e, d.BuilderRoleBindingID)
	if !ok || binding.WorkspacePath != workspace {
		return "", ports.ErrClearDevCandidateInvalid
	}
	var task core.ComplexExecutionTask
	for _, t := range e.Tasks {
		if t.ID == d.ComplexExecutionTaskID {
			task = t
		}
	}
	pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
	if err != nil || core.ProjectTaskMatchesRun(pkg, e.Run) != nil {
		return "", ports.ErrClearDevCandidateInvalid
	}
	rules := currentExceptionPathRules(e, task, core.ComplexPlanTask{WritePaths: pkg.WritePaths, GeneratedPaths: pkg.GeneratedPaths, SharedPathsRequireApproval: pkg.SharedPathsRequireApproval, ForbiddenPaths: pkg.ForbiddenPaths})
	return reader.InspectWorkflowWorkingTree(ctx, ports.ClearDevMailFreezeRequest{RunID: d.ID, RepoPath: contract.Selection.RepositoryPath, WorkspacePath: workspace, Branch: complexExecutionBuilderBranch(e.Run.ID, binding), BaseSHA: head, ParentSHA: head, WritePaths: rules.WritePaths, ForbiddenPaths: append(append([]string(nil), rules.ForbiddenPaths...), rules.SharedPathsRequireApproval...), ProjectExecution: &contract})
}

func workflowPendingWorkingTree(e core.ComplexExecutionSnapshot, d core.ComplexExecutionDispatch) string {
	var selected *core.WorkflowRecovery
	for i := range e.WorkflowRecoveries {
		r := &e.WorkflowRecoveries[i]
		if r.TaskID != d.ComplexExecutionTaskID {
			continue
		}
		matches := r.Action == core.RecoveryRetryBuilderSession && r.DispatchID == d.ID
		if r.Action == core.RecoveryContinueBuilder {
			for _, old := range e.Dispatches {
				if old.ID == r.DispatchID && old.Round+1 == d.Round {
					matches = true
				}
			}
		}
		if matches && (selected == nil || r.CreatedAt.After(selected.CreatedAt)) {
			selected = r
		}
	}
	if selected != nil {
		return selected.WorkingTreeSHA256
	}
	return ""
}
