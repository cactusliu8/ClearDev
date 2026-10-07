package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type builderReplacementStore interface {
	ReadClearDevBuilderReplacement(context.Context, string, time.Time) (core.BuilderReplacementState, error)
	RequestClearDevBuilderReplacement(context.Context, string, core.WorkflowRecovery, string, core.BuilderReplacementBinding, core.HumanDecisionDisplay, time.Time) error
	ContinueClearDevBuilderReplacement(context.Context, string, core.WorkflowRecovery, time.Time) (core.BuilderReplacementState, error)
	RecordClearDevBuilderReplacementObservation(context.Context, core.BuilderReplacementObservation) error
	BindClearDevBuilderReplacementAlias(context.Context, core.BuilderReplacementHandoff, time.Time) error
	ReadClearDevBuilderReplacementBeforeSend(context.Context, string, string, time.Time) (core.BuilderReplacementState, bool, error)
	GetClearDevBuilderReplacementEffectiveBinding(context.Context, string) (core.BuilderReplacementHandoff, bool, error)
	ClaimClearDevBuilderReplacementOperation(context.Context, string, string, string, time.Time) (bool, error)
}

func (s *Service) builderReplacementState(ctx context.Context, id string) (core.BuilderReplacementState, error) {
	store, ok := s.complexExecution.(builderReplacementStore)
	if !ok {
		return core.BuilderReplacementState{}, apierr.Conflict("BUILDER_REPLACEMENT_UNAVAILABLE", "Worker handoff storage is unavailable", nil)
	}
	return store.ReadClearDevBuilderReplacement(ctx, id, s.now().UTC())
}

func (s *Service) addBuilderReplacementView(ctx context.Context, id string, out *core.WorkflowRecoveryView) error {
	if _, ok := s.complexExecution.(builderReplacementStore); !ok {
		return nil
	}
	state, err := s.builderReplacementState(ctx, id)
	if err != nil {
		return err
	}
	for _, history := range state.History {
		if history.Action != core.RecoveryRequestBuilderReplacement && history.Action != core.RecoveryContinueBuilderReplacement {
			continue
		}
		present := false
		for _, old := range out.History {
			present = present || old.ID == history.ID
		}
		if !present {
			out.History = append(out.History, history)
		}
	}
	appendOption := func() {
		if state.Option.Action == "" {
			return
		}
		if state.Option.Action == core.RecoveryRequestBuilderReplacement && s.desktopRunID == "" {
			state.Option.UnavailableReason = "DESKTOP_UNAVAILABLE"
		}
		_, lossSupported := s.sessions.(ports.BuilderHandoffSessionObserver)
		_, snapshotSupported := s.inspector.(ports.ClearDevBuilderHandoffSnapshotter)
		if (lossSupported && snapshotSupported) || state.RequestID != "" {
			out.Options = append(out.Options, state.Option)
			out.BuilderReplacementSource = &state
		}
	}
	if state.RequestID == "" {
		appendOption()
		return nil
	}
	b := state.Binding
	budgetSummary := fmt.Sprintf("轮次 / steps: %d/%d; 消息账待核对 / messages unknown", state.Budget.UsedTurns, state.Budget.MaxTurns+state.Budget.AuthorizedExtraTurns)
	if s.attempts != nil {
		ledger, err := s.attempts.GetClearDevMessageBudget(ctx, id)
		if err != nil {
			return err
		}
		for _, usage := range ledger.Steps {
			if usage.LogicalStepID == b.LogicalStepID && usage.MaxMessages != nil && usage.ReservedMessages != nil && usage.ConfirmedSentMessages != nil {
				budgetSummary = fmt.Sprintf("轮次 / steps: %d/%d; 消息预占 / reserved: %d/%d; 已发送 / sent: %d", state.Budget.UsedTurns, state.Budget.MaxTurns+state.Budget.AuthorizedExtraTurns, *usage.ReservedMessages, *usage.MaxMessages, *usage.ConfirmedSentMessages)
				break
			}
		}
	}
	v := core.BuilderReplacementView{ID: state.RequestID, TargetID: b.TargetID, ExecutionRunID: b.ExecutionRunID, TaskID: b.TaskID, DispatchID: b.DispatchID, StepID: b.LogicalStepID, DecisionRequestID: state.DecisionRequestID,
		OldRoleBindingID: b.OldRoleBindingID, OldAOSessionID: b.OldAOSessionID, OldWorkspacePath: b.OldWorkspacePath, OldHeadSHA: b.OldHeadSHA, SnapshotSHA256: b.SnapshotSHA256, SnapshotPath: b.SnapshotPath, FileCount: b.FileCount, TotalBytes: b.TotalBytes, NewRoleBindingID: b.NewRoleBindingID,
		State: "PENDING_DECISION", BudgetSummary: budgetSummary}
	if state.Option.Action == core.RecoveryContinueBuilderReplacement {
		v.State = "APPROVED_AWAITING_CONTINUE"
	}
	if s.humanDecisions != nil {
		req, found, err := s.humanDecisions.GetClearDevHumanDecisionRequest(ctx, state.DecisionRequestID)
		if err != nil {
			return err
		}
		if found && req.Decision == core.HumanDecisionReject {
			v.State = "REJECTED"
		}
	}
	if h := state.Handoff; h != nil {
		v.State = "REGISTERED"
		v.ContinueRequestID = h.ID
		v.NewAOSessionID = h.NewAOSessionID
		v.NewWorkspacePath = h.NewWorkspacePath
		for _, o := range state.Observations {
			v.LastStage = o.Stage
			v.LastOutcome = o.Outcome
			v.ReasonCode = o.ReasonCode
			if o.Outcome == "UNKNOWN" {
				v.State = "UNKNOWN"
			} else if o.Outcome == "FAILED" && o.Stage == "CREATE" {
				v.State = "CREATE_FAILED"
			} else if o.Outcome == "FAILED" && o.Stage == "COPY" {
				v.State = "COPY_FAILED"
			} else if o.Stage == "SEND" && o.Outcome == "CONFIRMED" {
				v.State = "HANDED_OFF"
			}
		}
		if h.NewAOSessionID != "" && s.attempts != nil {
			delivery, err := s.attempts.GetClearDevAgentDeliveryState(ctx, id, h.AttemptID, b.ClientMessageID+":attempt:2")
			if err != nil {
				return err
			}
			if delivery.TurnID != "" && delivery.SendStatus != core.AgentAttemptPending && delivery.SendStatus != core.AgentAttemptDeliveryUnknown {
				v.State = "HANDED_OFF"
				v.LastStage = "SEND"
				v.LastOutcome = "CONFIRMED"
			}
		}
	}
	out.BuilderReplacements = append(out.BuilderReplacements, v)
	// Storage keeps the original admission option for exact idempotent replay.
	// Confirmed delivery ends the public continuation action, while retaining
	// the handoff history and its actual delivery state.
	if v.State != "HANDED_OFF" {
		appendOption()
	}
	return nil
}

func replacementWorkflowPrompt(state core.BuilderReplacementState) string {
	feedback := complexExecutionReworkFeedback(state.Execution, state.Task)
	legacy := complexExecutionReworkFeedbackLegacy(state.Execution, state.Task)
	return executionBuilderPromptForExistingStep(state.Execution.Run, []byte(state.Task.ExecutionPackageJSON), state.Dispatch.ID, state.Task.DevelopmentTaskID, state.Dispatch.Round, feedback, state.Step.PromptSHA256, legacy)
}

func replacementBuilderBranch(runID, bindingID string) string {
	return "cleardev-complex-builder-handoff-" + coreDigest([]byte(runID + ":" + bindingID))[:24]
}

func (s *Service) replacementEffectiveStep(ctx context.Context, step core.AgentStep) (core.AgentStep, error) {
	store, ok := s.complexExecution.(builderReplacementStore)
	if !ok {
		return step, nil
	}
	h, found, err := store.GetClearDevBuilderReplacementEffectiveBinding(ctx, step.ID)
	if err != nil || !found {
		return step, err
	}
	if h.Binding.LogicalStepID != step.ID || h.Binding.PromptSHA256 != step.PromptSHA256 || h.Binding.ClientMessageID != step.ClientMessageID || h.Binding.DispatchID != step.RequestID ||
		(step.RoleBindingID != h.Binding.OldRoleBindingID && step.RoleBindingID != h.Binding.NewRoleBindingID) || h.NewAOSessionID == "" {
		return step, errors.New("builder handoff no longer matches the original step")
	}
	step.RoleBindingID = h.Binding.NewRoleBindingID
	return step, nil
}

func (s *Service) replacementSourceCurrent(ctx context.Context, state core.BuilderReplacementState) error {
	if err := s.workflowRecoveryCurrent(ctx, state.Execution); err != nil {
		return err
	}
	if replacementWorkflowPrompt(state) == "" || coreDigest([]byte(replacementWorkflowPrompt(state))) != state.Step.PromptSHA256 {
		return errors.New("original Builder prompt changed")
	}
	if state.Option.UnavailableReason != "" && state.Option.UnavailableReason != "BUILDER_REPLACEMENT_DECISION_PENDING" && state.Option.UnavailableReason != "CONTINUATION_REGISTERED" {
		return errors.New(state.Option.UnavailableReason)
	}
	return nil
}

func validateReplacementInput(input WorkflowRecoveryInput) error {
	if input.RequestID == "" || len(input.RequestID) > 100 || strings.TrimSpace(input.RequestID) != input.RequestID || input.ExecutionRunID == "" || input.TargetID == "" || len(input.TargetID) > 600 || len(input.Supplement) > 16000 {
		return apierr.Invalid("RECOVERY_REQUEST_INVALID", "An exact original Builder recovery request is required", nil)
	}
	return nil
}

func sameReplacementRecovery(prior core.WorkflowRecovery, input WorkflowRecoveryInput) bool {
	return prior.ID == input.RequestID && prior.Action == input.Action && prior.TargetID == input.TargetID && prior.Supplement == input.Supplement
}

func replacementDisplay(b core.BuilderReplacementBinding, task core.ComplexStandardExecutionPackage) core.HumanDecisionDisplay {
	details, _ := json.Marshal(map[string]any{"originalTaskId": b.TaskID, "oldAOSessionId": b.OldAOSessionID, "oldWorkspacePath": b.OldWorkspacePath, "oldHeadSha": b.OldHeadSHA, "snapshotSha256": b.SnapshotSHA256, "snapshotPath": b.SnapshotPath, "newRoleBindingId": b.NewRoleBindingID, "fileCount": b.FileCount, "totalBytes": b.TotalBytes, "messageBudget": "Original three-message ledger; no additional development turn or correction", "action": "Only approval is recorded. Use Continue separately to create the worker and send the original task.", "说明": "仅登记准确授权；还须单独继续。保留原代码、历史与预算，不增加开发轮次或纠正次数。"})
	return core.HumanDecisionDisplay{Title: "Replace worker", Summary: "Replace the lost Builder for the original task without changing its code, history or budgets.", FullContent: "Approval permits one replacement for the original task. It does not create a worker or send a message; choose Continue separately. The original code, failed checks and message/step budgets remain.\n批准只允许这项原任务更换一次工作者，不创建会话或发送消息；还须单独点击继续。原代码、失败检查和消息/轮次预算保留。\nTask / 原任务: " + task.Title + "\n\n" + string(details), ChangeSummary: "One new worker identity for the same wholly unsent task; no new development round. / 原未发送任务更换一个工作者，不增加开发轮次。"}
}

func (s *Service) observeReplacementLoss(ctx context.Context, state core.BuilderReplacementState) (ports.BuilderHandoffSessionObservation, error) {
	observer, ok := s.sessions.(ports.BuilderHandoffSessionObserver)
	if !ok {
		return ports.BuilderHandoffSessionObservation{}, errors.New("BUILDER_LOSS_OBSERVER_UNAVAILABLE")
	}
	obs, err := observer.ObserveBuilderHandoffSession(ctx, domain.SessionID(state.OldBinding.AOSessionID))
	if err != nil {
		return obs, err
	}
	r := obs.Session
	if string(r.ID) != state.OldBinding.AOSessionID || r.Metadata.WorkspacePath != state.OldBinding.WorkspacePath || r.Metadata.DiffBaseSHA != state.OldBinding.BaseCommitSHA ||
		!obs.RuntimeStopped || !obs.NoOpenTurn || !obs.OperationIdle || (!r.IsTerminated && obs.NativeAvailability != ports.NativeSessionAvailabilityUnavailable) {
		return obs, errors.New("BUILDER_LOSS_NOT_CONFIRMED")
	}
	if store, ok := s.complexExecution.(builderSessionCheckStore); !ok {
		return obs, errors.New("builder open-turn storage unavailable")
	} else if open, e := store.ClearDevBuilderSessionHasOpenTurn(ctx, r.ID); e != nil || open {
		return obs, errors.New("BUILDER_TURN_NOT_SETTLED")
	}
	return obs, nil
}

func replacementLossDigest(obs ports.BuilderHandoffSessionObservation) string {
	raw, _ := json.Marshal(struct {
		Identity, Native, Config, Availability string
		Stopped, NoOpen, Idle, Terminated      bool
	}{core.BuilderSessionBindingDigest(obs.Session), obs.NativeRef.NativeSessionID, obs.NativeRef.ConfigDir, string(obs.NativeAvailability), obs.RuntimeStopped, obs.NoOpenTurn, obs.OperationIdle, obs.Session.IsTerminated})
	return coreDigest(raw)
}

func (s *Service) replacementExternalCurrent(ctx context.Context, state core.BuilderReplacementState) error {
	if err := s.replacementSourceCurrent(ctx, state); err != nil {
		return err
	}
	obs, err := s.observeReplacementLoss(ctx, state)
	if err != nil {
		return err
	}
	if core.BuilderSessionBindingDigest(obs.Session) != state.Binding.SessionIdentitySHA256 || replacementLossDigest(obs) != state.Binding.LossEvidenceSHA256 {
		return errors.New("BUILDER_LOSS_BINDING_CHANGED")
	}
	contract, project, err := core.ProjectContractFromRun(state.Execution.Run)
	if err != nil || !project {
		return errors.New("builder handoff contract changed")
	}
	p, err := s.controlledProject(ctx, contract.Selection.AOProjectID)
	if err != nil {
		return err
	}
	configured, hash := replacementConfiguredLaunch(p)
	if hash != state.Binding.AgentConfigSHA256 || p.Config.ClearDevHarness() != obs.Session.Harness {
		return errors.New("builder launch configuration changed")
	}
	var expected ports.AgentConfig
	if err := json.Unmarshal([]byte(state.Binding.AgentConfigJSON), &expected); err != nil {
		return err
	}
	if s.preflightChecker == nil {
		return errors.New("builder model observer unavailable")
	}
	preflight, err := s.preflightChecker.CheckControlledPreflight(ctx, p.Config.ClearDevHarness(), configured.Model)
	if err != nil || preflight.ResolvedModel != expected.Model {
		return errors.New("builder resolved model changed or is unavailable")
	}
	var snapshot ports.ClearDevBuilderHandoffSnapshot
	if err := json.Unmarshal([]byte(state.Binding.SnapshotJSON), &snapshot); err != nil {
		return err
	}
	snapshotter, ok := s.inspector.(ports.ClearDevBuilderHandoffSnapshotter)
	if !ok {
		return errors.New("BUILDER_SNAPSHOT_UNAVAILABLE")
	}
	if snapshot.SnapshotSHA256 != state.Binding.SnapshotSHA256 || snapshot.ManifestPath != state.Binding.SnapshotPath {
		return errors.New("BUILDER_SNAPSHOT_BINDING_CHANGED")
	}
	return snapshotter.VerifyBuilderHandoff(ctx, snapshot)
}

func (s *Service) requestBuilderReplacement(ctx context.Context, id string, input WorkflowRecoveryInput) (core.WorkflowRecoveryView, error) {
	if err := validateReplacementInput(input); err != nil {
		return core.WorkflowRecoveryView{}, err
	}
	input.Supplement = strings.TrimSpace(input.Supplement)
	state, err := s.builderReplacementState(ctx, id)
	if err != nil {
		return core.WorkflowRecoveryView{}, err
	}
	if state.Execution.Run.ID != input.ExecutionRunID {
		return core.WorkflowRecoveryView{}, apierr.Conflict("RECOVERY_NOT_CURRENT", "Execution changed", nil)
	}
	replay := false
	for _, prior := range state.History {
		if prior.ID == input.RequestID {
			if !sameReplacementRecovery(prior, input) {
				return core.WorkflowRecoveryView{}, apierr.Conflict("RECOVERY_REQUEST_CHANGED", "Request contents changed", nil)
			}
			replay = true
		}
	}
	if replay && input.Action == core.RecoveryRequestBuilderReplacement {
		return s.GetWorkflowRecovery(ctx, id)
	}
	if replay && input.Action == core.RecoveryContinueBuilderReplacement && state.Handoff != nil && state.Handoff.NewAOSessionID != "" {
		delivery, err := s.attempts.GetClearDevAgentDeliveryState(ctx, id, state.Handoff.AttemptID, state.Binding.ClientMessageID+":attempt:2")
		if err != nil {
			return core.WorkflowRecoveryView{}, err
		}
		if delivery.LastEventID != "" {
			s.scheduleComplexFlow(id)
			return s.GetWorkflowRecovery(ctx, id)
		}
	}
	if state.Option.Action != input.Action || state.Option.TargetID != input.TargetID {
		return core.WorkflowRecoveryView{}, apierr.Conflict("RECOVERY_NOT_CURRENT", "Worker replacement source or approval changed", nil)
	}
	if err := s.replacementSourceCurrent(ctx, state); err != nil {
		return core.WorkflowRecoveryView{}, apierr.Conflict("BUILDER_REPLACEMENT_NOT_READY", "Original worker, source or sealed code could not be verified", nil)
	}
	store, ok := s.complexExecution.(builderReplacementStore)
	if !ok {
		return core.WorkflowRecoveryView{}, errors.New("builder handoff storage is unavailable")
	}
	recovery := core.WorkflowRecovery{ID: input.RequestID, Action: input.Action, TargetID: input.TargetID, Supplement: input.Supplement}
	if input.Action == core.RecoveryContinueBuilderReplacement {
		if state.Handoff == nil {
			if err := s.replacementExternalCurrent(ctx, state); err != nil {
				return core.WorkflowRecoveryView{}, apierr.Conflict("BUILDER_REPLACEMENT_NOT_READY", "Original worker, source or sealed code could not be verified", nil)
			}
		}
		state, err = store.ContinueClearDevBuilderReplacement(ctx, id, recovery, s.now().UTC())
		if err != nil {
			s.logger.Error("Builder handoff storage admission failed", "error", err)
			return core.WorkflowRecoveryView{}, mapStoreError(err, "BUILDER_REPLACEMENT_REJECTED")
		}
		if err := s.executeBuilderReplacement(ctx, state); err != nil {
			s.logger.Error("Builder handoff external stage is unconfirmed", "error", err)
			return core.WorkflowRecoveryView{}, apierr.Conflict("BUILDER_HANDOFF_NOT_CONFIRMED", "The worker handoff is registered but its external result is not confirmed; inspect the saved state before continuing", nil)
		}
		s.scheduleComplexFlow(id)
		return s.GetWorkflowRecovery(ctx, id)
	}
	if s.desktopRunID == "" {
		return core.WorkflowRecoveryView{}, apierr.Conflict("DESKTOP_UNAVAILABLE", "Connect the trusted desktop before requesting worker replacement", nil)
	}
	obs, err := s.observeReplacementLoss(ctx, state)
	if err != nil {
		return core.WorkflowRecoveryView{}, apierr.Conflict("BUILDER_LOSS_NOT_CONFIRMED", "Original worker loss could not be verified", nil)
	}
	contract, project, err := core.ProjectContractFromRun(state.Execution.Run)
	if err != nil || !project {
		return core.WorkflowRecoveryView{}, errors.New("project handoff contract unavailable")
	}
	pkg, err := core.ParseComplexStandardExecutionPackage([]byte(state.Task.ExecutionPackageJSON))
	if err != nil {
		return core.WorkflowRecoveryView{}, err
	}
	rules := currentExceptionPathRules(state.Execution, state.Task, core.ComplexPlanTask{WritePaths: pkg.WritePaths, GeneratedPaths: pkg.GeneratedPaths, SharedPathsRequireApproval: pkg.SharedPathsRequireApproval, ForbiddenPaths: pkg.ForbiddenPaths})
	projectRecord, err := s.controlledProject(ctx, contract.Selection.AOProjectID)
	if err != nil {
		return core.WorkflowRecoveryView{}, err
	}
	cfg, configuredHash := replacementConfiguredLaunch(projectRecord)
	if projectRecord.Config.ClearDevHarness() != obs.Session.Harness || s.preflightChecker == nil {
		return core.WorkflowRecoveryView{}, errors.New("builder harness or model observation unavailable")
	}
	preflight, err := s.preflightChecker.CheckControlledPreflight(ctx, projectRecord.Config.ClearDevHarness(), cfg.Model)
	if err != nil || preflight.ResolvedModel == "" {
		return core.WorkflowRecoveryView{}, apierr.Conflict("BUILDER_MODEL_NOT_VERIFIED", "The original controlled role's concrete model could not be verified", nil)
	}
	cfg.Model = preflight.ResolvedModel
	configRaw, _ := json.Marshal(cfg)
	info, _ := json.Marshal(map[string]any{"schemaVersion": 1, "originalTaskId": state.Task.ID, "dispatchId": state.Dispatch.ID, "logicalStepId": state.Step.ID, "promptSha256": state.Step.PromptSHA256, "oldAOSessionId": state.OldBinding.AOSessionID, "originalBudget": state.Budget, "oldCandidates": state.Execution.Dispatches, "oldChecks": state.Execution.CheckRuns, "notice": "These are persisted observations. The lost worker did not produce a handoff report. Task scope, checks and budgets remain unchanged."})
	snapshotter, ok := s.inspector.(ports.ClearDevBuilderHandoffSnapshotter)
	if !ok {
		return core.WorkflowRecoveryView{}, errors.New("BUILDER_SNAPSHOT_UNAVAILABLE")
	}
	snapshot, err := snapshotter.CaptureBuilderHandoff(ctx, ports.ClearDevBuilderHandoffSnapshotRequest{SnapshotKey: state.Option.TargetID, RepoPath: contract.Selection.RepositoryPath, WorkspacePath: obs.Session.Metadata.WorkspacePath, Branch: obs.Session.Metadata.Branch, BaseSHA: state.Dispatch.BaseCommitSHA, WritePaths: rules.WritePaths, GeneratedPaths: rules.GeneratedPaths, SharedPathsRequireApproval: rules.SharedPathsRequireApproval, ForbiddenPaths: rules.ForbiddenPaths, ProjectExecution: &contract, ExecutionPackageSHA256: coreDigest([]byte(state.Task.ExecutionPackageJSON)), HandoffText: string(info)})
	if err != nil {
		return core.WorkflowRecoveryView{}, apierr.Conflict("BUILDER_SNAPSHOT_REJECTED", "Original code could not be sealed within the approved scope and limits", nil)
	}
	snapshotRaw, _ := json.Marshal(snapshot)
	b := core.BuilderReplacementBinding{DevelopmentRequirementID: id, ExecutionRunID: state.Execution.Run.ID, RequirementVersionID: state.Execution.Run.RequirementVersionID, RequirementSHA256: state.Execution.Run.RequirementSHA256, TaskID: state.Task.ID, DispatchID: state.Dispatch.ID, LogicalStepID: state.Step.ID, Round: int64(state.Dispatch.Round), TargetID: input.TargetID, SourceSHA256: state.SourceSHA256,
		OldRoleBindingID: state.OldBinding.ID, OldAOSessionID: state.OldBinding.AOSessionID, OldNativeSessionID: obs.NativeRef.NativeSessionID, OldWorkspacePath: obs.Session.Metadata.WorkspacePath, OldBranch: obs.Session.Metadata.Branch, OldHeadSHA: snapshot.HeadSHA, BaseCommitSHA: state.Dispatch.BaseCommitSHA, SessionIdentitySHA256: core.BuilderSessionBindingDigest(obs.Session), LossEvidenceSHA256: replacementLossDigest(obs), PromptSHA256: state.Step.PromptSHA256, ClientMessageID: state.Step.ClientMessageID, BudgetID: state.Budget.ID, OccupancyID: state.OccupancyID,
		BudgetSHA256: coreDigest(mustJSON(state.Budget)), SnapshotJSON: string(snapshotRaw), SnapshotSHA256: snapshot.SnapshotSHA256, SnapshotPath: snapshot.ManifestPath, HandoffPath: snapshot.HandoffPath, HandoffSHA256: snapshot.HandoffSHA256, NewRoleBindingID: state.Step.ID + ":replacement-builder", SessionCreationKey: state.Step.ID + ":replacement-create", AgentConfigJSON: string(configRaw), AgentConfigSHA256: configuredHash, FileCount: int64(snapshot.FileCount), TotalBytes: snapshot.TotalBytes}
	if _, err := core.ParseBuilderReplacementBinding(mustJSON(b)); err != nil {
		return core.WorkflowRecoveryView{}, err
	}
	if err := s.replacementExternalCurrent(ctx, core.BuilderReplacementState{Execution: state.Execution, Task: state.Task, Dispatch: state.Dispatch, Step: state.Step, OldBinding: state.OldBinding, Binding: b, Option: state.Option}); err != nil {
		return core.WorkflowRecoveryView{}, err
	}
	if err := store.RequestClearDevBuilderReplacement(ctx, id, recovery, s.newID(), b, replacementDisplay(b, pkg), s.now().UTC()); err != nil {
		s.logger.Error("Builder handoff storage admission failed", "error", err)
		return core.WorkflowRecoveryView{}, mapStoreError(err, "BUILDER_REPLACEMENT_REJECTED")
	}
	return s.GetWorkflowRecovery(ctx, id)
}

func mustJSON(value any) []byte { raw, _ := json.Marshal(value); return raw }

func replacementConfiguredLaunch(p domain.ProjectRecord) (ports.AgentConfig, string) {
	cfg := p.Config.Worker.AgentConfig
	cfg.Model = requestedChatModel(p, domain.KindWorker)
	cfg.Permissions = domain.PermissionModeAuto
	if cfg.Mode == "" {
		cfg.Mode = p.Config.AgentConfig.Mode
	}
	return cfg, coreDigest(mustJSON(struct {
		Harness domain.AgentHarness
		Config  ports.AgentConfig
	}{p.Config.ClearDevHarness(), cfg}))
}

func (s *Service) replacementObservation(ctx context.Context, h core.BuilderReplacementHandoff, stage, outcome, reason string, r domain.SessionRecord) error {
	operationKey := h.ID + ":" + stage
	if stage == "CREATE" {
		operationKey = h.Binding.SessionCreationKey
	}
	o := core.BuilderReplacementObservation{ID: h.ID + ":" + stage + ":" + outcome, HandoffID: h.ID, Stage: stage, OperationKey: operationKey, Outcome: outcome, ReasonCode: reason, AOSessionID: string(r.ID), WorkspacePath: r.Metadata.WorkspacePath, LaunchSHA256: r.CreationRequestFingerprint, SnapshotSHA256: h.Binding.SnapshotSHA256, ObservedAt: s.now().UTC()}
	store, ok := s.complexExecution.(builderReplacementStore)
	if !ok {
		return errors.New("builder handoff storage is unavailable")
	}
	return store.RecordClearDevBuilderReplacementObservation(ctx, o)
}

func (s *Service) executeBuilderReplacement(ctx context.Context, state core.BuilderReplacementState) error {
	if state.Handoff == nil {
		return errors.New("builder handoff is not registered")
	}
	h := *state.Handoff
	store, ok := s.complexExecution.(builderReplacementStore)
	if !ok {
		return errors.New("builder handoff storage is unavailable")
	}
	if h.NewAOSessionID != "" {
		return nil
	}
	if err := s.replacementExternalCurrent(ctx, state); err != nil {
		return err
	}
	contract, project, err := core.ProjectContractFromRun(state.Execution.Run)
	if err != nil || !project {
		return errors.New("builder handoff source is not admitted")
	}
	binding, ok := complexExecutionBindingByID(state.Execution, h.Binding.NewRoleBindingID)
	if !ok {
		return errors.New("builder handoff role is missing")
	}
	var cfg ports.AgentConfig
	if err := json.Unmarshal([]byte(h.Binding.AgentConfigJSON), &cfg); err != nil {
		return err
	}
	projectRecord, err := s.controlledProject(ctx, contract.Selection.AOProjectID)
	if err != nil {
		return err
	}
	_, configHash := replacementConfiguredLaunch(projectRecord)
	if configHash != h.Binding.AgentConfigSHA256 {
		return errors.New("builder launch configuration changed")
	}
	if _, err := store.ClaimClearDevBuilderReplacementOperation(ctx, h.ID, "CREATE", h.Binding.SessionCreationKey, s.now().UTC()); err != nil {
		return err
	}
	// Spawn is itself a durable, exact-fingerprint reconciliation by creation key.
	// It cannot choose another session after an unknown provider side effect.
	blocked, resolved, err := s.runControlledPreflight(ctx, state.Execution.Run.DevelopmentRequirementID, binding.ID, domain.ProjectID(contract.Selection.AOProjectID), domain.KindWorker)
	var session domain.Session
	if !blocked && err == nil {
		if resolved != cfg.Model {
			return errors.New("builder resolved model changed before creation")
		}
		session, err = s.spawnResolvedChatSession(ctx, ports.SpawnConfig{ProjectID: domain.ProjectID(contract.Selection.AOProjectID), Kind: domain.KindWorker, Branch: replacementBuilderBranch(state.Execution.Run.ID, binding.ID), WorkspaceBaseCommitSHA: contract.BaseCommitSHA, RequestedMode: domain.SessionModeChat, AgentConfig: cfg, DisplayName: "ClearDev Replacement Builder", CreationIdempotencyKey: h.Binding.SessionCreationKey, BuilderHandoffContext: &ports.BuilderHandoffContext{ReferencePath: h.Binding.HandoffPath, SHA256: h.Binding.HandoffSHA256, SnapshotSHA256: h.Binding.SnapshotSHA256}}, resolved)
	}
	if blocked || err != nil {
		outcome := "UNKNOWN"
		if blocked {
			outcome = "FAILED"
		}
		if saveErr := s.replacementObservation(ctx, h, "CREATE", outcome, "BUILDER_CREATE_NOT_CONFIRMED", session.SessionRecord); saveErr != nil {
			return saveErr
		}
		if err != nil {
			return err
		}
		return errAgentRecoveryDeferred
	}
	r := session.SessionRecord
	if !s.validComplexExecutionWorker(ctx, r, binding, contract.Selection.AOProjectID) || r.Metadata.DiffBaseSHA != contract.BaseCommitSHA || r.Metadata.WorkspacePath == "" || r.CreationIdempotencyKey != h.Binding.SessionCreationKey || r.CreationRequestFingerprint == "" {
		return errors.New("new Builder did not retain its exact launch binding")
	}
	if err := s.replacementObservation(ctx, h, "CREATE", "CONFIRMED", "", r); err != nil {
		return err
	}
	if err := s.replacementExternalCurrent(ctx, state); err != nil {
		return err
	}
	var snapshot ports.ClearDevBuilderHandoffSnapshot
	if err := json.Unmarshal([]byte(h.Binding.SnapshotJSON), &snapshot); err != nil {
		return err
	}
	snapshotter, ok := s.inspector.(ports.ClearDevBuilderHandoffSnapshotter)
	if !ok {
		return errors.New("builder handoff snapshotter is unavailable")
	}
	target := ports.ClearDevBuilderHandoffTarget{RepoPath: contract.Selection.RepositoryPath, WorkspacePath: r.Metadata.WorkspacePath, Branch: r.Metadata.Branch, BaseSHA: h.Binding.BaseCommitSHA, InitialBaseSHA: contract.BaseCommitSHA}
	claimed, err := store.ClaimClearDevBuilderReplacementOperation(ctx, h.ID, "COPY", h.ID+":COPY", s.now().UTC())
	if err != nil {
		return err
	}
	knownBeforeCopyFailure := false
	for _, o := range state.Observations {
		if o.Stage == "COPY" {
			knownBeforeCopyFailure = o.Outcome == "FAILED" && o.ReasonCode == "BUILDER_COPY_NOT_STARTED"
		}
	}
	if claimed || knownBeforeCopyFailure {
		if err := snapshotter.RestoreBuilderHandoff(ctx, snapshot, target); err != nil {
			outcome, reason := "UNKNOWN", "BUILDER_COPY_UNKNOWN"
			if errors.Is(err, ports.ErrBuilderHandoffBeforeCopy) {
				outcome, reason = "FAILED", "BUILDER_COPY_NOT_STARTED"
			}
			if saveErr := s.replacementObservation(ctx, h, "COPY", outcome, reason, r); saveErr != nil {
				return saveErr
			}
			return err
		}
	}
	if err := snapshotter.VerifyBuilderHandoffTarget(ctx, snapshot, target); err != nil {
		if saveErr := s.replacementObservation(ctx, h, "COPY", "UNKNOWN", "BUILDER_COPY_UNKNOWN", r); saveErr != nil {
			return saveErr
		}
		return err
	}
	if err := s.replacementObservation(ctx, h, "COPY", "CONFIRMED", "", r); err != nil {
		return err
	}
	if err := s.replacementExternalCurrent(ctx, state); err != nil {
		return err
	}
	h.NewAOSessionID = string(r.ID)
	h.NewWorkspacePath = r.Metadata.WorkspacePath
	h.LaunchSHA256 = r.CreationRequestFingerprint
	return store.BindClearDevBuilderReplacementAlias(ctx, h, s.now().UTC())
}

func (s *Service) checkBuilderReplacementBeforeSend(ctx context.Context, id, stepID string) error {
	store, ok := s.complexExecution.(builderReplacementStore)
	if !ok {
		return nil
	}
	state, found, err := store.ReadClearDevBuilderReplacementBeforeSend(ctx, id, stepID, s.now().UTC())
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if state.Handoff == nil || state.Handoff.NewAOSessionID == "" {
		return errAgentRecoveryDeferred
	}
	if err := s.replacementExternalCurrent(ctx, state); err != nil {
		return err
	}
	h := *state.Handoff
	r, exists, err := s.ao.GetSession(ctx, domain.SessionID(h.NewAOSessionID))
	if err != nil {
		return err
	}
	if !exists || r.IsTerminated || r.Activity.State != domain.ActivityIdle || r.CreationIdempotencyKey != h.Binding.SessionCreationKey || r.CreationRequestFingerprint != h.LaunchSHA256 || r.Metadata.WorkspacePath != h.NewWorkspacePath {
		return errors.New("new Builder identity or idle state changed")
	}
	var snapshot ports.ClearDevBuilderHandoffSnapshot
	if err := json.Unmarshal([]byte(h.Binding.SnapshotJSON), &snapshot); err != nil {
		return err
	}
	snapshotter, ok := s.inspector.(ports.ClearDevBuilderHandoffSnapshotter)
	if !ok {
		return errors.New("builder handoff snapshotter is unavailable")
	}
	return snapshotter.VerifyBuilderHandoffTarget(ctx, snapshot, ports.ClearDevBuilderHandoffTarget{RepoPath: r.Metadata.WorkspaceRepoPath, WorkspacePath: r.Metadata.WorkspacePath, Branch: r.Metadata.Branch, BaseSHA: h.Binding.BaseCommitSHA})
}

// Display reads persisted currentness only. Issuance, settlement and continuation
// perform fresh loss/snapshot checks; a GET must never launch those probes.
func (s *Service) builderReplacementDisplayCurrent(ctx context.Context, req core.HumanDecisionRequest) (bool, error) {
	binding, err := core.ParseBuilderReplacementBinding([]byte(req.BindingJSON))
	if err != nil {
		return false, err
	}
	state, err := s.builderReplacementState(ctx, req.DevelopmentRequirementID)
	if err != nil {
		return false, err
	}
	if state.DecisionRequestID != req.ID || state.Binding != binding || state.Option.UnavailableReason != "BUILDER_REPLACEMENT_DECISION_PENDING" {
		return false, nil
	}
	contract, project, err := core.ProjectContractFromRun(state.Execution.Run)
	if err != nil || !project {
		return false, err
	}
	record, err := s.controlledProject(ctx, contract.Selection.AOProjectID)
	if err != nil {
		return false, err
	}
	_, configHash := replacementConfiguredLaunch(record)
	return configHash == binding.AgentConfigSHA256, nil
}
