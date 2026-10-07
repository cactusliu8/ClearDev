package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

const (
	standardCheckMemoryBytes = 512 * 1024 * 1024
	standardCheckPidsLimit   = 64
	standardCheckOutputLimit = 64 * 1024
)

var errStandardStopped = errors.New("ClearDev STANDARD flow reached a durable stop")

type standardStepReasons struct {
	Invalid     core.ReasonCode
	Timeout     core.ReasonCode
	Unavailable core.ReasonCode
	MapInvalid  func(error) core.ReasonCode
	ProjectID   string
	// Internal role binding for isolated product discovery; empty preserves
	// the historical Steward/orchestrator and Planner/worker contracts.
	SessionKind domain.SessionKind
	// OnFailed records the terminal failure on the owning fact after the step
	// is durably failed. It never revives the failed turn.
	OnFailed func(context.Context, core.AgentStep, core.ReasonCode) error
}

type standardMessage struct {
	StepID    string
	TurnID    string
	MessageID string
	Text      string
}

// StartStandardFlow persists only the unique start intent before returning.
// All agent work runs against the daemon lifetime context so cancellation of
// the HTTP request cannot strand an already accepted workflow.
func (s *Service) StartStandardFlow(ctx context.Context, requirementID string) (RequirementView, error) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return RequirementView{}, err
	}

	requirementID = strings.TrimSpace(requirementID)
	if requirementID == "" {
		return RequirementView{}, apierr.Invalid("CLEARDEV_REQUIREMENT_ID_REQUIRED", "ClearDev requirement id is required", nil)
	}
	if err := s.projectExecutionGate(ctx, requirementID); err != nil {
		return RequirementView{}, err
	}
	if s.standard == nil || s.facts == nil || s.ao == nil || s.sessions == nil || s.chat == nil || s.inspector == nil || s.checks == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_STANDARD_UNAVAILABLE", "ClearDev STANDARD flow is not fully configured")
	}
	snapshot, exists, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_REQUIREMENT_READ_FAILED", "Could not read the ClearDev requirement")
	}
	if !exists {
		return RequirementView{}, apierr.NotFound("CLEARDEV_REQUIREMENT_NOT_FOUND", "ClearDev requirement was not found")
	}
	if s.complex != nil {
		if _, complexExists, complexErr := s.complex.GetClearDevComplexPlanning(ctx, requirementID); complexErr != nil {
			return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_PLANNING_READ_FAILED", "Could not read the ClearDev complex planning facts")
		} else if complexExists {
			return RequirementView{}, apierr.Conflict(string(core.ReasonComplexPlanRequired), "Complex requirements cannot start a STANDARD flow", nil)
		}
	}
	if _, confirmed := currentConfirmedVersion(snapshot); !confirmed || snapshot.Requirement.CancelledAt != nil {
		return RequirementView{}, apierr.Conflict(string(core.ReasonPreconditionNotMet), "ClearDev STANDARD flow requires one current confirmed requirement version", nil)
	}
	bindingID := s.newID()
	_, created, err := s.standard.StartClearDevStandardFlow(ctx, core.StartStandardFlowCommand{
		DevelopmentRequirementID:     requirementID,
		StewardRoleBindingID:         bindingID,
		StewardSessionIdempotencyKey: standardSpawnKey(requirementID, core.StandardRoleSteward, bindingID),
		At:                           s.now().UTC(),
	})
	if err != nil {
		return RequirementView{}, mapStoreError(err, "START_STANDARD_FLOW_FAILED")
	}
	if !created {
		return RequirementView{}, apierr.Conflict(string(core.ReasonStandardFlowExists), "A STANDARD flow already exists for this requirement", nil)
	}
	s.scheduleStandardFlow(requirementID)
	return s.GetRequirement(ctx, requirementID)
}

// ResumeStandardFlows schedules every non-terminal durable flow after daemon
// reconciliation. The store list is authoritative; in-memory admission only
// prevents duplicate workers inside one daemon process.
func (s *Service) ResumeStandardFlows(ctx context.Context) error {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return err
	}

	if s.standard == nil {
		return errors.New("ClearDev STANDARD store is not configured")
	}
	ids, err := s.standard.ListClearDevRunnableStandardFlows(ctx)
	if err != nil {
		return fmt.Errorf("list runnable ClearDev STANDARD flows: %w", err)
	}
	for _, id := range ids {
		s.scheduleStandardFlow(id)
	}
	return nil
}

func (s *Service) scheduleStandardFlow(requirementID string) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		s.logger.Error("ClearDev controlled scheduling rejected", "err", err)
		return
	}

	s.standardMu.Lock()
	if s.standardRunning[requirementID] {
		s.standardMu.Unlock()
		return
	}
	s.standardRunning[requirementID] = true
	s.standardMu.Unlock()
	s.runBackground(func() {
		defer func() {
			s.standardMu.Lock()
			delete(s.standardRunning, requirementID)
			s.standardMu.Unlock()
		}()
		if err := s.runStandardFlow(s.backgroundContext, requirementID); err != nil {
			s.logger.Error("ClearDev STANDARD flow stopped with an internal error", "requirementID", requirementID, "error", err)
		}
	})
}

func (s *Service) runStandardFlow(ctx context.Context, requirementID string) error {
	blocked, err := s.recoverRequirementAgentAttempts(ctx, requirementID)
	if err != nil || blocked {
		return err
	}
	for iteration := 0; iteration < 64; iteration++ {
		progressed, done, err := s.advanceStandardFlow(ctx, requirementID)
		if errors.Is(err, errStandardStopped) {
			return nil
		}
		if err != nil || done {
			return err
		}
		if !progressed {
			return nil
		}
	}
	return errors.New("ClearDev STANDARD flow exceeded its bounded advance count")
}

func (s *Service) advanceStandardFlow(ctx context.Context, requirementID string) (bool, bool, error) {
	snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, false, errors.New("ClearDev requirement disappeared after STANDARD start")
	}
	flow, ok, err := s.standard.GetClearDevStandardFlow(ctx, requirementID)
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, false, errors.New("ClearDev STANDARD flow lost its confirmed requirement")
	}
	if s.direction != nil {
		if stopped, stopErr := s.direction.HasActiveClearDevDirectionStop(ctx, flow.RequirementVersionID); stopErr != nil {
			return false, false, stopErr
		} else if stopped {
			return false, false, nil
		}
	}
	version, ok := confirmedVersion(snapshot, flow.RequirementVersionID)
	if !ok || snapshot.Requirement.CancelledAt != nil {
		if snapshot.Requirement.CancelledAt != nil {
			for _, dispatch := range flow.Dispatches {
				if releaseErr := s.releaseCandidateCheckEnvironment(ctx, dispatch.ID+":check-preflight"); releaseErr != nil {
					return false, false, releaseErr
				}
			}
		}
		return false, true, nil
	}

	steward, ok := roleBinding(flow, core.StandardRoleSteward, "")
	if !ok {
		return false, false, errors.New("ClearDev STANDARD start has no steward binding")
	}
	stewardRecord, progressed, err := s.ensureRoleSession(ctx, flow, steward, snapshot.Requirement.AOProjectID, domain.KindOrchestrator, "", core.ReasonCode("STEWARD_UNAVAILABLE"))
	if err != nil {
		return progressed, false, err
	}
	if progressed {
		return true, false, nil
	}
	if controlledPreflightIdle(steward.Status, stewardRecord.ID) {
		return false, false, nil
	}

	planningRequestID := ""
	if step, found := agentStep(flow, steward.ID, core.AgentStepRequestPlanning, ""); found {
		planningRequestID = step.RequestID
	} else {
		planningRequestID = s.newID()
	}
	planningPromptText := planningPrompt(planningRequestID, version.ID, version.SHA256, version.RequirementText)
	_, planningCreated, err := s.runAgentStep(ctx, flow, steward, planningRequestID, core.AgentStepRequestPlanning, planningPromptText,
		standardStepReasons{Invalid: core.ReasonCode("STEWARD_RESULT_INVALID"), Timeout: core.ReasonCode("STEWARD_TIMEOUT"), Unavailable: core.ReasonCode("STEWARD_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
		func(raw []byte) error {
			_, parseErr := core.ParsePlanningResult(raw, planningRequestID, version.ID, version.SHA256)
			return parseErr
		})
	if err != nil {
		return planningCreated, false, err
	}
	if planningCreated {
		return true, false, nil
	}

	planner, exists := roleBinding(flow, core.StandardRoleEngineeringPlanner, "")
	if !exists {
		planner = core.RoleSessionBinding{
			ID: s.newID(), DevelopmentRequirementID: requirementID, RequirementVersionID: version.ID,
			Role: core.StandardRoleEngineeringPlanner, Status: core.RoleBindingStatusRequested,
			RequestedAt: s.now().UTC(),
		}
		planner.SessionCreationIdempotencyKey = standardSpawnKey(version.ID, planner.Role, planner.ID)
		if _, _, createErr := s.standard.CreateClearDevRoleBinding(ctx, core.CreateRoleBindingCommand{Binding: planner}); createErr != nil {
			return false, false, createErr
		}
		return true, false, nil
	}
	plannerBranch := standardBranch("planner", version.ID)
	plannerRecord, progressed, err := s.ensureRoleSession(ctx, flow, planner, snapshot.Requirement.AOProjectID, domain.KindWorker, plannerBranch, core.ReasonCode("PLANNER_UNAVAILABLE"))
	if err != nil {
		return progressed, false, err
	}
	if progressed {
		return true, false, nil
	}
	if controlledPreflightIdle(planner.Status, plannerRecord.ID) {
		return false, false, nil
	}

	plan, exists := engineeringPlan(flow)
	if !exists {
		prompt := engineeringPlanPrompt(planningRequestID, version.ID, version.SHA256, version.RequirementText, version.TaskSetVersion)
		var parsed core.EngineeringPlanResult
		var normalized []byte
		var planSHA string
		message, changed, stepErr := s.runAgentStep(ctx, flow, planner, planningRequestID, core.AgentStepEngineeringPlan, prompt,
			standardStepReasons{
				Invalid: core.ReasonCode("PLANNER_RESULT_INVALID"), Timeout: core.ReasonCode("PLANNER_TIMEOUT"), Unavailable: core.ReasonCode("PLANNER_UNAVAILABLE"),
				ProjectID:  snapshot.Requirement.AOProjectID,
				MapInvalid: standardPlannerInvalidReason,
			},
			func(raw []byte) error {
				var parseErr error
				parsed, normalized, planSHA, parseErr = core.ParseEngineeringPlanResult(raw, planningRequestID, version.ID, version.SHA256, version.TaskSetVersion)
				return parseErr
			})
		if stepErr != nil {
			return changed, false, stepErr
		}
		if changed {
			return true, false, nil
		}
		_ = parsed
		plan = core.EngineeringPlan{
			ID: s.newID(), RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, Version: 1,
			PlannerRoleBindingID: planner.ID, AgentStepID: message.StepID,
			TurnID: message.TurnID, FinalMessageID: message.MessageID, PlanJSON: string(normalized), PlanSHA256: planSHA,
			CreatedAt: s.now().UTC(),
		}
		if createErr := s.standard.CreateClearDevEngineeringPlan(ctx, plan); createErr != nil {
			return false, false, createErr
		}
		return true, false, nil
	}

	review, exists := planReview(flow, plan.ID)
	if !exists {
		prompt := planReviewPrompt(plan.ID, plan.PlanSHA256, []byte(plan.PlanJSON))
		var parsed core.PlanReviewResult
		message, changed, stepErr := s.runAgentStep(ctx, flow, steward, plan.ID, core.AgentStepPlanReview, prompt,
			standardStepReasons{Invalid: core.ReasonCode("STEWARD_RESULT_INVALID"), Timeout: core.ReasonCode("STEWARD_TIMEOUT"), Unavailable: core.ReasonCode("STEWARD_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
			func(raw []byte) error {
				var parseErr error
				parsed, parseErr = core.ParsePlanReviewResult(raw, plan.ID, plan.PlanSHA256)
				return parseErr
			})
		if stepErr != nil {
			return changed, false, stepErr
		}
		if changed {
			return true, false, nil
		}
		review = core.PlanReview{
			ID: s.newID(), EngineeringPlanID: plan.ID, StewardRoleBindingID: steward.ID,
			AgentStepID: message.StepID,
			TurnID:      message.TurnID, FinalMessageID: message.MessageID,
			Verdict: core.PlanReviewVerdict(parsed.Verdict), ReasonCode: core.ReasonCode(parsed.ReasonCode), Summary: parsed.Summary,
			CreatedAt: s.now().UTC(),
		}
		if recordErr := s.standard.RecordClearDevPlanReview(ctx, review); recordErr != nil {
			return false, false, recordErr
		}
		return true, false, nil
	}
	if review.Verdict != core.PlanReviewApproved {
		return false, true, nil
	}

	dispatch, exists := standardDispatch(flow, plan.ID)
	if !exists {
		prompt := dispatchRequestPrompt(plan.ID, plan.PlanSHA256)
		message, changed, stepErr := s.runAgentStep(ctx, flow, steward, plan.ID, core.AgentStepDispatchRequest, prompt,
			standardStepReasons{Invalid: core.ReasonCode("STEWARD_RESULT_INVALID"), Timeout: core.ReasonCode("STEWARD_TIMEOUT"), Unavailable: core.ReasonCode("STEWARD_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
			func(raw []byte) error {
				_, parseErr := core.ParseDispatchRequestResult(raw, plan.ID, plan.PlanSHA256)
				return parseErr
			})
		if stepErr != nil {
			return changed, false, stepErr
		}
		if changed {
			return true, false, nil
		}
		var planResult core.EngineeringPlanResult
		if unmarshalErr := json.Unmarshal([]byte(plan.PlanJSON), &planResult); unmarshalErr != nil {
			return false, false, unmarshalErr
		}
		dispatchID, taskID, builderBindingID := s.newID(), s.newID(), s.newID()
		packBytes, packSHA, encodeErr := encodeStandardExecutionPackage(standardExecutionPackage{
			SchemaVersion: 1, Mode: string(core.WorkModeStandard), DispatchID: dispatchID, TaskID: taskID,
			RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, RequirementText: version.RequirementText,
			PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, Task: planResult.Task,
		})
		if encodeErr != nil {
			return false, false, encodeErr
		}
		builderKey := standardSpawnKey(dispatchID, core.StandardRoleBuilder, builderBindingID)
		now := s.now().UTC()
		dispatch = core.Dispatch{
			ID: dispatchID, RequirementVersionID: version.ID, EngineeringPlanID: plan.ID, PlanReviewID: review.ID,
			StewardRoleBindingID: steward.ID, AgentStepID: message.StepID,
			Mode: core.WorkModeStandard, PreallocatedDevelopmentTaskID: taskID, ExpectedTaskSetVersion: version.TaskSetVersion,
			ExecutionPackageJSON: string(packBytes), ExecutionPackageSHA256: packSHA,
			BuilderSessionIdempotencyKey: builderKey, BuilderRoleBindingID: builderBindingID,
			Status: core.DispatchStatusPending, RequestedAt: now,
		}
		builder := core.RoleSessionBinding{
			ID: builderBindingID, DevelopmentRequirementID: requirementID, RequirementVersionID: version.ID,
			Role: core.StandardRoleBuilder, DispatchID: dispatchID,
			SessionCreationIdempotencyKey: builderKey, Status: core.RoleBindingStatusRequested, RequestedAt: now,
		}
		_ = message
		if _, _, createErr := s.standard.CreateClearDevPendingDispatch(ctx, core.CreatePendingDispatchCommand{Dispatch: dispatch, BuilderBinding: builder}); createErr != nil {
			return false, false, createErr
		}
		return true, false, nil
	}
	if dispatch.Status != core.DispatchStatusPending && dispatch.Status != core.DispatchStatusAccepted {
		return false, true, nil
	}

	if dispatch.Status == core.DispatchStatusPending {
		builder, found := roleBindingByID(flow, dispatch.BuilderRoleBindingID)
		if !found {
			return false, false, errors.New("pending STANDARD dispatch lost its Builder binding")
		}
		builderBranch := standardBranch("builder", dispatch.PreallocatedDevelopmentTaskID)
		baseline, baselineWorkspace, baselineErr := s.checkBuilderBaseline(ctx, dispatch.ID+":baseline", snapshot.Requirement.AOProjectID)
		if baselineErr == nil && builder.Status == core.RoleBindingStatusRequested {
			baselineErr = s.pinBaselineBuilderBranch(ctx, baseline, baselineWorkspace, builderBranch)
		}
		if baselineErr != nil {
			return s.stopStandardBaseline(ctx, dispatch.ID, baselineErr)
		}
		builderSession, changed, bindErr := s.ensureRoleSession(ctx, flow, builder, snapshot.Requirement.AOProjectID, domain.KindWorker, builderBranch, core.ReasonBuilderSpawnFailed)
		if bindErr != nil {
			if errors.Is(bindErr, errStandardStopped) {
				_, _ = s.standard.SettleClearDevStandardDispatchFailure(ctx, dispatch.ID, core.DispatchStatusFailed, core.ReasonBuilderSpawnFailed, s.now().UTC())
			}
			return changed, false, bindErr
		}
		if builderSession.ID != "" {
			if baselineErr := s.verifySpawnedBuilderBaseline(ctx, baseline, builderSession); baselineErr != nil {
				return s.stopStandardBaseline(ctx, dispatch.ID, baselineErr)
			}
		}
		if changed {
			return true, false, nil
		}
		if controlledPreflightIdle(builder.Status, builderSession.ID) {
			return false, false, nil
		}
		if builderSession.Metadata.DiffBaseSHA == "" || builderSession.Metadata.WorkspacePath == "" {
			_, _ = s.standard.SettleClearDevStandardDispatchFailure(ctx, dispatch.ID, core.DispatchStatusFailed, core.ReasonBuilderSpawnFailed, s.now().UTC())
			return true, false, errStandardStopped
		}
		planResult, planErr := savedPlanResult(plan)
		if planErr != nil {
			return false, false, planErr
		}
		if preflightErr := s.prepareCandidateCheckEnvironment(ctx, dispatch.ID+":check-preflight", builderSession.Metadata.WorkspacePath, builderSession.Metadata.DiffBaseSHA, core.StandardCandidateCheckImage, standardPlanCheckArgv(planResult)); preflightErr != nil {
			s.logger.Error("ClearDev STANDARD check preflight failed", "dispatchID", dispatch.ID, "error", preflightErr)
			_, _ = s.standard.SettleClearDevStandardDispatchFailure(ctx, dispatch.ID, core.DispatchStatusFailed, core.ReasonCode("CHECKER_UNAVAILABLE"), s.now().UTC())
			return true, false, errStandardStopped
		}
		acceptance, acceptErr := standardDispatchAcceptance(dispatch, builderSession, version, s.now().UTC(), s.newID)
		if acceptErr != nil {
			return false, false, acceptErr
		}
		if acceptErr = s.standard.AcceptClearDevStandardDispatch(ctx, acceptance); acceptErr != nil {
			return false, false, acceptErr
		}
		return true, false, nil
	}

	return s.advanceAcceptedDispatch(ctx, snapshot, flow, version, stewardRecord, plan, review, dispatch)
}

func standardPlannerInvalidReason(err error) core.ReasonCode {
	if err != nil && err.Error() == "PLAN_OUT_OF_SCOPE" {
		return core.ReasonCode("PLAN_OUT_OF_SCOPE")
	}
	return core.ReasonCode("PLANNER_RESULT_INVALID")
}

func (s *Service) ensureRoleSession(
	ctx context.Context,
	flow core.StandardFlowSnapshot,
	binding core.RoleSessionBinding,
	projectID string,
	kind domain.SessionKind,
	branch string,
	unavailableReason core.ReasonCode,
) (domain.SessionRecord, bool, error) {
	if binding.Status == core.RoleBindingStatusFailed || binding.Status == core.RoleBindingStatusEnded {
		return domain.SessionRecord{}, false, errStandardStopped
	}
	if binding.Status == core.RoleBindingStatusBound {
		record, found, err := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
		if err != nil {
			return domain.SessionRecord{}, false, err
		}
		if !found || record.IsTerminated || record.Activity.State == domain.ActivityExited || s.validateStandardSession(ctx, record, binding, projectID, kind, flow) != nil {
			// The exact next Agent step owns the durable *_UNAVAILABLE result.
			// Returning an empty record lets that step be saved before the flow
			// stops, while pending dispatch and Git paths still fail closed on
			// the missing workspace/baseline below.
			return domain.SessionRecord{}, false, nil
		}
		return record, false, nil
	}
	if binding.Status != core.RoleBindingStatusRequested {
		return domain.SessionRecord{}, false, errStandardStopped
	}
	session, blocked, err := s.spawnControlledChatSession(ctx, binding.DevelopmentRequirementID, binding.ID, ports.SpawnConfig{
		ProjectID: domain.ProjectID(projectID), Kind: kind,
		Branch: branch, Prompt: "", RequestedMode: domain.SessionModeChat,
		AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		DisplayName:            standardRoleDisplayName(binding.Role),
		CreationIdempotencyKey: binding.SessionCreationIdempotencyKey,
	})
	if blocked {
		return domain.SessionRecord{}, false, nil
	}
	if err != nil {
		s.logger.Error("ClearDev STANDARD role session spawn failed", "role", binding.Role, "bindingID", binding.ID, "error", err)
		_, _ = s.standard.FailClearDevRoleBinding(ctx, core.FailRoleBindingCommand{RoleBindingID: binding.ID, ReasonCode: unavailableReason, At: s.now().UTC()})
		return domain.SessionRecord{}, true, errStandardStopped
	}
	if session.IsTerminated || session.Activity.State == domain.ActivityExited {
		s.logger.Error("ClearDev STANDARD role session exited during spawn", "role", binding.Role, "bindingID", binding.ID, "sessionID", session.ID)
		_, _ = s.standard.FailClearDevRoleBinding(ctx, core.FailRoleBindingCommand{RoleBindingID: binding.ID, ReasonCode: unavailableReason, At: s.now().UTC()})
		return domain.SessionRecord{}, true, errStandardStopped
	}
	if validateErr := s.validateStandardSession(ctx, session.SessionRecord, binding, projectID, kind, flow); validateErr != nil {
		s.logger.Error("ClearDev STANDARD role session failed validation", "role", binding.Role, "bindingID", binding.ID, "sessionID", session.ID, "error", validateErr)
		_, _ = s.standard.FailClearDevRoleBinding(ctx, core.FailRoleBindingCommand{RoleBindingID: binding.ID, ReasonCode: unavailableReason, At: s.now().UTC()})
		return domain.SessionRecord{}, true, errStandardStopped
	}
	if _, err = s.standard.BindClearDevRoleBinding(ctx, binding.ID, string(session.ID), s.now().UTC()); err != nil {
		return domain.SessionRecord{}, false, err
	}
	return session.SessionRecord, true, nil
}

func validateStandardSession(record domain.SessionRecord, binding core.RoleSessionBinding, projectID string, kind domain.SessionKind, flow core.StandardFlowSnapshot) error {
	if record.ID == "" || string(record.ProjectID) != projectID || record.Kind != kind || !supportedControlledHarness(record.Harness) ||
		domain.NormalizeSessionMode(record.Mode) != domain.SessionModeChat || record.PermissionMode != domain.PermissionModeAuto ||
		strings.TrimSpace(record.Metadata.WorkspacePath) == "" {
		return errors.New("session does not satisfy the bound controlled Chat role")
	}
	if binding.Role != core.StandardRoleSteward && record.CreationIdempotencyKey != binding.SessionCreationIdempotencyKey {
		return errors.New("worker session creation key does not match its role binding")
	}
	for _, other := range flow.RoleBindings {
		if other.ID != binding.ID && other.Status == core.RoleBindingStatusBound && other.AOSessionID == string(record.ID) {
			return errors.New("one AO session cannot hold two ClearDev STANDARD roles")
		}
	}
	return nil
}

func (s *Service) runAgentStep(
	ctx context.Context,
	flow core.StandardFlowSnapshot,
	binding core.RoleSessionBinding,
	requestID string,
	kind core.AgentStepKind,
	prompt string,
	reasons standardStepReasons,
	validate func([]byte) error,
) (standardMessage, bool, error) {
	step, found := agentStep(flow, binding.ID, kind, requestID)
	changed := false
	if !found {
		stepID := s.newID()
		step = core.AgentStep{
			ID: stepID, RoleBindingID: binding.ID, Kind: kind, RequestID: requestID,
			ClientMessageID: "cleardev-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)),
			SendStatus: core.AgentStepSendStatusPending, RequestedAt: s.now().UTC(),
		}
		var err error
		step, _, err = s.standard.CreateClearDevStandardAgentStep(ctx, step)
		if err != nil {
			return standardMessage{}, changed, err
		}
		changed = true
	}
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return standardMessage{}, changed, errors.New("saved ClearDev Agent step prompt does not match immutable facts")
	}
	if step.SendStatus == core.AgentStepSendStatusPending && reasons.ProjectID != "" {
		kind := domain.KindWorker
		if binding.Role == core.StandardRoleSteward {
			kind = domain.KindOrchestrator
		}
		record, available, readErr := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
		if readErr != nil {
			// A read/probe error is unknown, not proof that a durable session is
			// dead. Leave the pending request resumable instead of mislabeling it.
			return standardMessage{}, changed, readErr
		}
		if !available || record.IsTerminated || record.Activity.State == domain.ActivityExited ||
			s.validateStandardSession(ctx, record, binding, reasons.ProjectID, kind, flow) != nil {
			if err := s.failAgentStep(ctx, step, reasons.Unavailable); err != nil {
				return standardMessage{}, changed, err
			}
			return standardMessage{}, true, errStandardStopped
		}
	}
	if step.SendStatus == core.AgentStepSendStatusFailed {
		return standardMessage{}, changed, errStandardStopped
	}
	if step.SendStatus == core.AgentStepSendStatusSettled {
		message, err := s.readSettledStep(ctx, binding, step, prompt)
		if err != nil {
			return standardMessage{}, changed, errStandardStopped
		}
		if err := validate([]byte(message.Text)); err != nil {
			return standardMessage{}, changed, errors.New("settled Agent step no longer passes its frozen protocol")
		}
		message.StepID = step.ID
		return message, changed, nil
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.relayAgentTurn(ctx, flow.DevelopmentRequirementID, core.AgentStepCategoryStandard, step, binding.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			if isMessageBudgetError(err) {
				return standardMessage{}, changed, err
			}
			if ctx.Err() != nil {
				return standardMessage{}, changed, ctx.Err()
			}
			if isAgentRecoveryDeferred(err) {
				return standardMessage{}, true, errStandardStopped
			}
			if failure, ok := ports.ChatFailureFromError(err); ok && (failure.Category == domain.AgentFailureDeliveryUnknown || failure.Retryable) {
				return standardMessage{}, true, errStandardStopped
			}
			_ = s.failAgentStep(ctx, step, reasons.Unavailable)
			return standardMessage{}, true, errStandardStopped
		}
		sentAt := s.now().UTC()
		if _, err := s.standard.MarkClearDevStandardAgentStepSent(ctx, step.ID, sentAt); err != nil {
			return standardMessage{}, changed, err
		}
		step.SendStatus = core.AgentStepSendStatusSent
		step.SentAt = &sentAt
		changed = true
	}
	message, stopped, err := s.awaitValidAgentJSON(ctx, flow.DevelopmentRequirementID, core.AgentStepCategoryStandard, binding.AOSessionID, step, prompt, validate, reasons, s.failAgentStep, errStandardStopped)
	if err != nil || stopped {
		return standardMessage{}, changed || stopped, err
	}
	now := s.now().UTC()
	step.SendStatus = core.AgentStepSendStatusSettled
	step.TurnID, step.FinalMessageID, step.FinalMessageText = message.TurnID, message.MessageID, message.Text
	step.MessageSHA256 = coreDigest([]byte(message.Text))
	step.CompletedAt = &now
	if _, err := s.standard.SettleClearDevStandardAgentStep(ctx, step); err != nil {
		return standardMessage{}, changed, err
	}
	message.StepID = step.ID
	return message, true, nil
}

func (s *Service) failAgentStep(ctx context.Context, step core.AgentStep, reason core.ReasonCode) error {
	now := s.now().UTC()
	step.SendStatus, step.FailedAt, step.ReasonCode = core.AgentStepSendStatusFailed, &now, reason
	_, err := s.standard.SettleClearDevStandardAgentStep(ctx, step)
	return err
}

func (s *Service) awaitAgentMessage(ctx context.Context, binding core.RoleSessionBinding, step core.AgentStep, prompt string) (standardMessage, error) {
	timeout := s.stepTimeout
	if timeout <= 0 {
		return standardMessage{}, context.DeadlineExceeded
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		snapshot, err := s.chat.Snapshot(waitCtx, domain.SessionID(binding.AOSessionID))
		if err == nil {
			message, stateErr := exactAgentMessage(snapshot, binding.AOSessionID, step.ClientMessageID, prompt)
			switch {
			case stateErr == nil:
				return message, nil
			case !errors.Is(stateErr, errAgentMessagePending):
				return standardMessage{}, stateErr
			}
		}
		record, found, readErr := s.ao.GetSession(waitCtx, domain.SessionID(binding.AOSessionID))
		if readErr == nil && (!found || record.IsTerminated || record.Activity.State == domain.ActivityExited) {
			return standardMessage{}, errAgentSessionLost
		}
		select {
		case <-waitCtx.Done():
			return standardMessage{}, waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) readSettledStep(ctx context.Context, binding core.RoleSessionBinding, step core.AgentStep, prompt string) (standardMessage, error) {
	_ = ctx
	_ = binding
	_ = prompt
	if strings.TrimSpace(step.TurnID) == "" || strings.TrimSpace(step.FinalMessageID) == "" || strings.TrimSpace(step.FinalMessageText) == "" ||
		coreDigest([]byte(step.FinalMessageText)) != step.MessageSHA256 {
		return standardMessage{}, errors.New("saved Agent result text does not match its immutable completed-turn hash")
	}
	return standardMessage{TurnID: step.TurnID, MessageID: step.FinalMessageID, Text: step.FinalMessageText}, nil
}

var errAgentMessagePending = errors.New("Agent message is not settled yet")

func exactAgentMessage(snapshot chatsvc.Snapshot, sessionID, clientMessageID, prompt string) (standardMessage, error) {
	if strings.TrimSpace(sessionID) == "" || snapshot.SessionID == "" || string(snapshot.SessionID) != sessionID {
		return standardMessage{}, errors.New("Chat snapshot belongs to another AO session")
	}
	var user *domain.ConversationMessage
	for index := range snapshot.Messages {
		message := &snapshot.Messages[index]
		if message.ClientMessageID != clientMessageID {
			continue
		}
		if user != nil || message.Role != domain.MessageRoleUser || message.Origin != domain.MessageOriginAutomation || message.Text != prompt || message.TurnID == "" {
			return standardMessage{}, errors.New("stable ClientMessageID does not identify exactly one saved automation prompt")
		}
		user = message
	}
	if user == nil {
		return standardMessage{}, errAgentMessagePending
	}
	var turn *domain.ConversationTurn
	for index := range snapshot.Turns {
		candidate := &snapshot.Turns[index]
		if candidate.ID == user.TurnID {
			if turn != nil {
				return standardMessage{}, errors.New("saved user prompt names an ambiguous turn")
			}
			turn = candidate
		}
	}
	if turn == nil || turn.State == domain.TurnStateQueued || turn.State == domain.TurnStateRunning {
		return standardMessage{}, errAgentMessagePending
	}
	if turn.State != domain.TurnStateCompleted || turn.CompletedAt == nil || turn.RolledBackAt != nil || string(turn.HandledBySessionID) != sessionID {
		return standardMessage{}, &agentTerminalError{turn: *turn}
	}
	var final *domain.ConversationMessage
	for index := range snapshot.Messages {
		message := &snapshot.Messages[index]
		if message.TurnID != turn.ID || message.Role != domain.MessageRoleAssistant {
			continue
		}
		if final == nil || message.Sequence > final.Sequence {
			final = message
		}
	}
	if final == nil {
		return standardMessage{}, errors.New("completed Agent turn has no assistant result")
	}
	if final.Streaming || final.Origin != domain.MessageOriginProvider || strings.TrimSpace(final.ID) == "" {
		return standardMessage{}, errors.New("the final assistant message is streaming or is not provider output")
	}
	return standardMessage{TurnID: turn.ID, MessageID: final.ID, Text: final.Text}, nil
}

func standardDispatchAcceptance(dispatch core.Dispatch, session domain.SessionRecord, version core.RequirementVersion, now time.Time, newID func() string) (core.DispatchAcceptance, error) {
	if coreDigest([]byte(dispatch.ExecutionPackageJSON)) != dispatch.ExecutionPackageSHA256 {
		return core.DispatchAcceptance{}, errors.New("saved execution package hash does not match its immutable JSON")
	}
	pack, err := core.ParseStandardExecutionPackage([]byte(dispatch.ExecutionPackageJSON))
	if err != nil {
		return core.DispatchAcceptance{}, fmt.Errorf("decode saved execution package: %w", err)
	}
	if pack.DispatchID != dispatch.ID || pack.TaskID != dispatch.PreallocatedDevelopmentTaskID ||
		pack.RequirementVersionID != version.ID || pack.PlanID != dispatch.EngineeringPlanID {
		return core.DispatchAcceptance{}, errors.New("saved execution package does not match its dispatch")
	}
	task := core.DevelopmentTask{
		ID: dispatch.PreallocatedDevelopmentTaskID, Title: pack.Task.Title, Mode: core.WorkModeStandard,
		Status: core.DevelopmentTaskStatusPlanned, MaxReworkCount: pack.Task.MaxReworkCount,
		CreatedAt: now, UpdatedAt: now,
	}
	permission := core.PermissionVersion{
		ID: newID(), DevelopmentTaskID: task.ID, Version: 1,
		Rules: core.PathRules{
			WritePaths: append([]string(nil), pack.Task.WritePaths...), GeneratedPaths: append([]string(nil), pack.Task.GeneratedPaths...),
			SharedPathsRequireApproval: append([]string(nil), pack.Task.SharedPathsRequireApproval...), ForbiddenPaths: append([]string(nil), pack.Task.ForbiddenPaths...),
		},
		CreatedAt: now,
	}
	checks := make([]core.RequiredCheck, 0, len(pack.Task.RequiredChecks))
	for _, check := range pack.Task.RequiredChecks {
		checks = append(checks, core.RequiredCheck{ID: newID(), DevelopmentTaskID: task.ID, Name: check.Name, Kind: "REQUIRED_CHECK", CreatedAt: now})
	}
	return core.DispatchAcceptance{
		DispatchID: dispatch.ID, BuilderSessionID: string(session.ID), BaseCommitSHA: session.Metadata.DiffBaseSHA,
		InitialTask:      core.InitialDevelopmentTask{Task: task, Permission: permission, Checks: checks},
		IntegrationCheck: core.RequiredCheck{ID: newID(), DevelopmentTaskID: task.ID, Name: pack.Task.IntegrationCheck.Name, Kind: "INTEGRATION", CreatedAt: now},
		At:               now,
	}, nil
}

func confirmedVersion(snapshot core.RequirementSnapshot, id string) (core.RequirementVersion, bool) {
	var found core.RequirementVersion
	count := 0
	for _, version := range snapshot.RequirementVersions {
		if version.Status == core.RequirementVersionStatusConfirmed {
			count++
			found = version
		}
	}
	return found, count == 1 && found.ID == id
}

func currentConfirmedVersion(snapshot core.RequirementSnapshot) (core.RequirementVersion, bool) {
	var found core.RequirementVersion
	count := 0
	for _, version := range snapshot.RequirementVersions {
		if version.Status == core.RequirementVersionStatusConfirmed {
			found = version
			count++
		}
	}
	return found, count == 1
}

func deriveStandardFlowStatus(snapshot core.RequirementSnapshot, flow core.StandardFlowSnapshot, overall core.OverallProgress) StandardFlowStatus {
	if snapshot.Requirement.CancelledAt != nil {
		return StandardFlowStatus{Phase: core.OverallPhaseCancelled, Attention: StandardFlowAttentionNone}
	}

	var flowTask *core.DevelopmentTask
	for _, dispatch := range flow.Dispatches {
		if dispatch.Status != core.DispatchStatusAccepted {
			continue
		}
		if task, found := taskByID(snapshot, dispatch.PreallocatedDevelopmentTaskID); found {
			copy := task
			flowTask = &copy
		}
	}
	if flowTask != nil && flowTask.Status == core.DevelopmentTaskStatusDone {
		return StandardFlowStatus{Phase: core.OverallPhaseCompleted, Attention: StandardFlowAttentionNone}
	}
	current, currentOK := currentConfirmedVersion(snapshot)
	if !currentOK || current.ID != flow.RequirementVersionID {
		return StandardFlowStatus{Phase: core.OverallPhasePlanningTasks, Attention: StandardFlowAttentionReplan, ReasonCode: core.ReasonStalePlan}
	}
	if flowTask != nil {
		status := StandardFlowStatus{Phase: overall.Phase, Attention: standardAttentionFromOverall(overall.Attention)}
		if status.Attention != StandardFlowAttentionNone {
			status.ReasonCode = latestStandardTaskReason(snapshot.Events, flowTask.ID)
		}
		return status
	}

	status := StandardFlowStatus{Phase: core.OverallPhasePlanningTasks, Attention: StandardFlowAttentionNone}
	if len(flow.Dispatches) != 0 {
		dispatch := flow.Dispatches[len(flow.Dispatches)-1]
		switch dispatch.Status {
		case core.DispatchStatusRejected:
			status.Attention, status.ReasonCode = StandardFlowAttentionReplan, dispatch.ReasonCode
			return status
		case core.DispatchStatusFailed:
			status.Attention, status.ReasonCode = StandardFlowAttentionBlocked, dispatch.ReasonCode
			return status
		}
	}
	if len(flow.PlanReviews) != 0 {
		review := flow.PlanReviews[len(flow.PlanReviews)-1]
		switch review.Verdict {
		case core.PlanReviewReplan:
			status.Attention, status.ReasonCode = StandardFlowAttentionReplan, review.ReasonCode
			return status
		case core.PlanReviewNeedsHuman:
			status.Attention, status.ReasonCode = StandardFlowAttentionNeedsHuman, review.ReasonCode
			return status
		}
	}
	roles := make(map[string]core.StandardRole, len(flow.RoleBindings))
	for _, binding := range flow.RoleBindings {
		roles[binding.ID] = binding.Role
	}
	var latestFailedStep *core.AgentStep
	for index := range flow.AgentSteps {
		step := &flow.AgentSteps[index]
		role := roles[step.RoleBindingID]
		if step.SendStatus != core.AgentStepSendStatusFailed || step.Kind == core.AgentStepStatusReport ||
			(role != core.StandardRoleSteward && role != core.StandardRoleEngineeringPlanner && role != core.StandardRoleBuilder) {
			continue
		}
		if latestFailedStep == nil || terminalAgentStepAt(*step).After(terminalAgentStepAt(*latestFailedStep)) {
			latestFailedStep = step
		}
	}
	if latestFailedStep != nil {
		status.Attention, status.ReasonCode = StandardFlowAttentionNeedsHuman, latestFailedStep.ReasonCode
		if roles[latestFailedStep.RoleBindingID] == core.StandardRoleBuilder {
			status.Attention = StandardFlowAttentionBlocked
		}
		return status
	}
	var latestFailedBinding *core.RoleSessionBinding
	for index := range flow.RoleBindings {
		binding := &flow.RoleBindings[index]
		if binding.Status != core.RoleBindingStatusFailed {
			continue
		}
		if latestFailedBinding == nil || roleBindingTerminalAt(*binding).After(roleBindingTerminalAt(*latestFailedBinding)) {
			latestFailedBinding = binding
		}
	}
	if latestFailedBinding != nil {
		status.Attention, status.ReasonCode = StandardFlowAttentionNeedsHuman, latestFailedBinding.ReasonCode
		if latestFailedBinding.Role == core.StandardRoleBuilder || latestFailedBinding.Role == core.StandardRoleReviewer {
			status.Attention = StandardFlowAttentionBlocked
		}
	}
	return status
}

func standardAttentionFromOverall(attention core.OverallAttention) StandardFlowAttention {
	switch attention {
	case core.OverallAttentionNeedsHuman:
		return StandardFlowAttentionNeedsHuman
	case core.OverallAttentionBlocked:
		return StandardFlowAttentionBlocked
	case core.OverallAttentionRework:
		return StandardFlowAttentionRework
	case core.OverallAttentionIntegrationFailed:
		return StandardFlowAttentionIntegrationFailed
	default:
		return StandardFlowAttentionNone
	}
}

func latestStandardTaskReason(events []core.RequirementEvent, taskID string) core.ReasonCode {
	var reason core.ReasonCode
	var sequence int64
	for _, event := range events {
		if event.SubjectID == taskID && event.Outcome == core.EventAccepted && event.Reason != core.ReasonNone && event.Sequence >= sequence {
			reason, sequence = event.Reason, event.Sequence
		}
	}
	return reason
}

func terminalAgentStepAt(step core.AgentStep) time.Time {
	if step.FailedAt != nil {
		return *step.FailedAt
	}
	if step.CompletedAt != nil {
		return *step.CompletedAt
	}
	return step.RequestedAt
}

func roleBindingTerminalAt(binding core.RoleSessionBinding) time.Time {
	if binding.EndedAt != nil {
		return *binding.EndedAt
	}
	return binding.RequestedAt
}

func roleBinding(flow core.StandardFlowSnapshot, role core.StandardRole, candidateID string) (core.RoleSessionBinding, bool) {
	for _, binding := range flow.RoleBindings {
		if binding.Role == role && (candidateID == "" || binding.CandidateCommitID == candidateID) {
			return binding, true
		}
	}
	return core.RoleSessionBinding{}, false
}

func roleBindingByID(flow core.StandardFlowSnapshot, id string) (core.RoleSessionBinding, bool) {
	for _, binding := range flow.RoleBindings {
		if binding.ID == id {
			return binding, true
		}
	}
	return core.RoleSessionBinding{}, false
}

func agentStep(flow core.StandardFlowSnapshot, bindingID string, kind core.AgentStepKind, requestID string) (core.AgentStep, bool) {
	for _, step := range flow.AgentSteps {
		if step.RoleBindingID == bindingID && step.Kind == kind && (requestID == "" || step.RequestID == requestID) {
			return step, true
		}
	}
	return core.AgentStep{}, false
}

func engineeringPlan(flow core.StandardFlowSnapshot) (core.EngineeringPlan, bool) {
	if len(flow.EngineeringPlans) == 0 {
		return core.EngineeringPlan{}, false
	}
	return flow.EngineeringPlans[len(flow.EngineeringPlans)-1], true
}

func planReview(flow core.StandardFlowSnapshot, planID string) (core.PlanReview, bool) {
	for _, review := range flow.PlanReviews {
		if review.EngineeringPlanID == planID {
			return review, true
		}
	}
	return core.PlanReview{}, false
}

func standardDispatch(flow core.StandardFlowSnapshot, planID string) (core.Dispatch, bool) {
	for _, dispatch := range flow.Dispatches {
		if dispatch.EngineeringPlanID == planID {
			return dispatch, true
		}
	}
	return core.Dispatch{}, false
}

func standardSpawnKey(scope string, role core.StandardRole, id string) string {
	return "cleardev-standard:" + strings.ToLower(string(role)) + ":" + scope + ":" + id
}

func standardBranch(role, id string) string {
	clean := strings.ToLower(id)
	clean = strings.Map(func(value rune) rune {
		if (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') || value == '-' {
			return value
		}
		return '-'
	}, clean)
	if len(clean) > 24 {
		clean = clean[:24]
	}
	return "codex/cleardev-" + role + "-" + strings.Trim(clean, "-")
}

func standardRoleDisplayName(role core.StandardRole) string {
	switch role {
	case core.StandardRoleSteward:
		return "ClearDev Project Steward"
	case core.StandardRoleEngineeringPlanner:
		return "ClearDev Engineering Planner"
	case core.StandardRoleBuilder:
		return "ClearDev Builder"
	case core.StandardRoleReviewer:
		return "ClearDev Reviewer"
	default:
		return "ClearDev STANDARD"
	}
}

func (s *Service) advanceAcceptedDispatch(
	ctx context.Context,
	snapshot core.RequirementSnapshot,
	flow core.StandardFlowSnapshot,
	version core.RequirementVersion,
	steward domain.SessionRecord,
	plan core.EngineeringPlan,
	_ core.PlanReview,
	dispatch core.Dispatch,
) (bool, bool, error) {
	task, found := taskByID(snapshot, dispatch.PreallocatedDevelopmentTaskID)
	if !found {
		return false, false, errors.New("accepted STANDARD dispatch has no development task")
	}
	if task.Status == core.DevelopmentTaskStatusDone {
		if err := s.releaseCandidateCheckEnvironment(ctx, dispatch.ID+":check-preflight"); err != nil {
			return false, false, err
		}
		return s.ensureStandardStatusReport(ctx, snapshot, flow, steward, snapshot.Requirement.ID, plan, dispatch)
	}
	if task.Status == core.DevelopmentTaskStatusCancelled {
		if err := s.releaseCandidateCheckEnvironment(ctx, dispatch.ID+":check-preflight"); err != nil {
			return false, false, err
		}
		return false, true, nil
	}
	if task.Status == core.DevelopmentTaskStatusBlocked || task.Status == core.DevelopmentTaskStatusNeedsHuman {
		return false, true, nil
	}
	builder, found := roleBindingByID(flow, dispatch.BuilderRoleBindingID)
	if !found || builder.Status != core.RoleBindingStatusBound {
		return false, false, errors.New("accepted STANDARD dispatch has no bound Builder")
	}
	builderRecord, exists, err := s.ao.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil {
		return false, false, err
	}
	if !exists || builderRecord.IsTerminated || builderRecord.Activity.State == domain.ActivityExited ||
		s.validateStandardSession(ctx, builderRecord, builder, snapshot.Requirement.AOProjectID, domain.KindWorker, flow) != nil {
		builderRecord = domain.SessionRecord{}
	}

	candidate, hasCandidate := currentRoundCandidate(snapshot, task)
	if task.Status == core.DevelopmentTaskStatusRunning && !hasCandidate {
		requestID := fmt.Sprintf("%s:round:%d", dispatch.ID, task.ReworkCount)
		feedback := ""
		if task.ReworkCount > 0 {
			feedback, err = standardReworkFeedback(flow, dispatch.ID, task.ID)
			if err != nil {
				return false, false, err
			}
		}
		var builderResult core.BuilderResult
		message, changed, stepErr := s.runAgentStep(ctx, flow, builder, requestID, core.AgentStepBuilderResult,
			builderPrompt([]byte(dispatch.ExecutionPackageJSON), task.ReworkCount, feedback),
			standardStepReasons{Invalid: core.ReasonCode("BUILDER_RESULT_INVALID"), Timeout: core.ReasonCode("BUILDER_TIMEOUT"), Unavailable: core.ReasonCode("BUILDER_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
			func(raw []byte) error {
				var parseErr error
				builderResult, parseErr = core.ParseBuilderResult(raw, dispatch.ID, task.ID, task.ReworkCount)
				return parseErr
			})
		if stepErr != nil {
			if errors.Is(stepErr, errStandardStopped) {
				latest, _, loadErr := s.standard.GetClearDevStandardFlow(ctx, snapshot.Requirement.ID)
				failedStep, ok := agentStep(latest, builder.ID, core.AgentStepBuilderResult, requestID)
				if loadErr == nil && ok && failedStep.SendStatus == core.AgentStepSendStatusFailed {
					if outcomeErr := s.standard.ApplyClearDevStandardBuilderOutcome(ctx, core.StandardBuilderOutcomeCommand{
						DispatchID: dispatch.ID, AgentStepID: failedStep.ID, Outcome: core.DevelopmentTaskStatusNeedsHuman,
						ReasonCode: failedStep.ReasonCode, At: s.now().UTC(),
					}); outcomeErr != nil {
						return false, false, outcomeErr
					}
				}
			}
			return changed, false, stepErr
		}
		if changed {
			return true, false, nil
		}
		if builderResult.Outcome == "BLOCKED" || builderResult.Outcome == "NEEDS_HUMAN" {
			outcome := core.DevelopmentTaskStatusBlocked
			reason := core.ReasonCode("BUILDER_BLOCKED")
			if builderResult.Outcome == "NEEDS_HUMAN" {
				outcome, reason = core.DevelopmentTaskStatusNeedsHuman, core.ReasonCode("BUILDER_NEEDS_HUMAN")
			}
			if outcomeErr := s.standard.ApplyClearDevStandardBuilderOutcome(ctx, core.StandardBuilderOutcomeCommand{
				DispatchID: dispatch.ID, AgentStepID: message.StepID, Outcome: outcome,
				ReasonCode: reason, ReasonText: builderResult.Summary, At: s.now().UTC(),
			}); outcomeErr != nil {
				return false, false, outcomeErr
			}
			return true, false, nil
		}
		if builderRecord.Metadata.WorkspacePath == "" {
			return s.recordGitInspectionFailure(ctx, dispatch, task, ports.ErrClearDevGitUnavailable)
		}
		inspection, inspectErr := s.inspector.InspectCandidate(ctx, builderRecord.Metadata.WorkspacePath, dispatch.BaseCommitSHA)
		if inspectErr != nil || inspection.BaseSHA != dispatch.BaseCommitSHA {
			if inspectErr == nil {
				inspectErr = ports.ErrClearDevCandidateInvalid
			}
			return s.recordGitInspectionFailure(ctx, dispatch, task, inspectErr)
		}
		candidate, err = s.standard.AppendClearDevStandardCandidate(ctx, core.StandardCandidateObservation{
			CandidateCommitID: s.newID(), DispatchID: dispatch.ID, CommitSHA: inspection.CandidateSHA, ObservedAt: s.now().UTC(),
		})
		if err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	if !hasCandidate {
		return false, false, errors.New("STANDARD task review state has no current candidate")
	}
	if builderRecord.Metadata.WorkspacePath == "" {
		return s.recordGitInspectionFailure(ctx, dispatch, task, ports.ErrClearDevGitUnavailable)
	}

	inspection, inspectErr := s.inspector.InspectCandidate(ctx, builderRecord.Metadata.WorkspacePath, dispatch.BaseCommitSHA)
	if inspectErr != nil || inspection.BaseSHA != dispatch.BaseCommitSHA {
		if inspectErr == nil {
			inspectErr = ports.ErrClearDevCandidateInvalid
		}
		return s.recordGitInspectionFailure(ctx, dispatch, task, inspectErr)
	}
	if inspection.CandidateSHA != candidate.CommitSHA {
		return s.recordGitInspectionFailure(ctx, dispatch, task, ports.ErrClearDevCandidateInvalid)
	}
	planResult, parseErr := savedPlanResult(plan)
	if parseErr != nil {
		return false, false, parseErr
	}

	scopeRun, changed, err := s.ensureScopeCheck(ctx, snapshot, flow, dispatch, task, candidate, planResult.Task, inspection)
	if err != nil || changed {
		return changed, false, err
	}
	if scopeRun.Result != core.EvidenceResultPass {
		return false, false, errors.New("settled scope failure did not transition the current task")
	}
	for _, spec := range planResult.Task.RequiredChecks {
		run, runChanged, runErr := s.ensureContainerCheck(ctx, snapshot, flow, dispatch, task, candidate, builderRecord.Metadata.WorkspacePath, core.CandidateCheckRequired, spec)
		if runErr != nil || runChanged {
			return runChanged, false, runErr
		}
		if run.Result != core.EvidenceResultPass {
			return false, false, errors.New("settled required-check failure did not transition the current task")
		}
	}
	integrationRun, changed, err := s.ensureContainerCheck(ctx, snapshot, flow, dispatch, task, candidate, builderRecord.Metadata.WorkspacePath, core.CandidateCheckIntegration, planResult.Task.IntegrationCheck)
	if err != nil || changed {
		return changed, false, err
	}
	if integrationRun.Result != core.EvidenceResultPass {
		return false, false, errors.New("settled integration failure did not transition the current task")
	}

	localReview, changed, err := s.ensureLocalReview(ctx, snapshot, flow, version, plan, dispatch, task, candidate, inspection, builderRecord)
	if err != nil || changed {
		return changed, false, err
	}
	if localReview.Verdict != core.LocalReviewPass {
		return false, false, errors.New("settled local-review failure did not transition the current task")
	}
	if err = s.standard.CompleteClearDevStandardTask(ctx, core.CompleteStandardTaskCommand{
		DispatchID: dispatch.ID, IntegrationCandidateID: "s02-integration-" + dispatch.ID,
		IntegrationEvidenceID: "s02-integration-evidence-" + dispatch.ID, At: s.now().UTC(),
	}); err != nil {
		return false, false, err
	}
	if err := s.releaseCandidateCheckEnvironment(ctx, dispatch.ID+":check-preflight"); err != nil {
		return false, false, err
	}
	return true, false, nil
}

type standardReworkFailureFact struct {
	Source            string          `json:"source"`
	Kind              string          `json:"kind"`
	Name              string          `json:"name"`
	ReasonCode        core.ReasonCode `json:"reasonCode"`
	Result            string          `json:"result"`
	Summary           string          `json:"summary"`
	CandidateCommitID string          `json:"candidateCommitId"`
}

// standardReworkFeedback selects the latest durable Control Plane or Reviewer
// failure for the accepted dispatch. It never uses the Builder's summary or
// reported test claims, and its struct encoding gives retries identical text.
func standardReworkFeedback(flow core.StandardFlowSnapshot, dispatchID, taskID string) (string, error) {
	var selected standardReworkFailureFact
	var selectedAt time.Time
	selectedID := ""
	selectFact := func(id string, at time.Time, fact standardReworkFailureFact) {
		if selectedID == "" || at.After(selectedAt) || (at.Equal(selectedAt) && id > selectedID) {
			selected, selectedAt, selectedID = fact, at, id
		}
	}
	for _, run := range flow.CandidateCheckRuns {
		if run.DispatchID != dispatchID || run.DevelopmentTaskID != taskID || run.SettledAt == nil {
			continue
		}
		result := ""
		switch {
		case run.Status == core.CandidateCheckRunStatusFailed:
			result = "FAILED"
		case run.Status == core.CandidateCheckRunStatusSettled && run.Result == core.EvidenceResultFail:
			result = string(core.EvidenceResultFail)
		default:
			continue
		}
		selectFact(run.ID, *run.SettledAt, standardReworkFailureFact{
			Source:            "CANDIDATE_CHECK",
			Kind:              string(run.Kind),
			Name:              run.Name,
			ReasonCode:        run.ReasonCode,
			Result:            result,
			Summary:           run.OutputSummary,
			CandidateCommitID: run.CandidateCommitID,
		})
	}
	for _, review := range flow.LocalReviews {
		if review.DispatchID != dispatchID || review.SettledAt == nil {
			continue
		}
		result := ""
		switch {
		case review.Status == core.LocalReviewStatusFailed:
			result = "FAILED"
		case review.Status == core.LocalReviewStatusSettled && review.Verdict != core.LocalReviewPass:
			result = string(review.Verdict)
		default:
			continue
		}
		selectFact(review.ID, *review.SettledAt, standardReworkFailureFact{
			Source:            "LOCAL_REVIEW",
			Kind:              "LOCAL_REVIEW",
			Name:              "independent-review",
			ReasonCode:        review.ReasonCode,
			Result:            result,
			Summary:           review.Summary,
			CandidateCommitID: review.CandidateCommitID,
		})
	}
	if selectedID == "" || selected.ReasonCode == core.ReasonNone {
		return "", errors.New("STANDARD rework has no durable failed checker or review fact")
	}
	encoded, err := json.Marshal(selected)
	if err != nil {
		return "", fmt.Errorf("encode STANDARD rework failure fact: %w", err)
	}
	return "持久失败事实：" + string(encoded) + "。请按原执行包修复，不得扩大范围。", nil
}

func taskByID(snapshot core.RequirementSnapshot, id string) (core.DevelopmentTask, bool) {
	for _, task := range snapshot.DevelopmentTasks {
		if task.ID == id {
			return task, true
		}
	}
	return core.DevelopmentTask{}, false
}

func currentRoundCandidate(snapshot core.RequirementSnapshot, task core.DevelopmentTask) (core.CandidateCommit, bool) {
	var current core.CandidateCommit
	found := false
	for _, candidate := range snapshot.Candidates {
		if candidate.DevelopmentTaskID != task.ID || !candidateIsCurrentRound(snapshot.Events, task.ID, candidate.ID) {
			continue
		}
		if !found || candidate.Sequence > current.Sequence {
			current, found = candidate, true
		}
	}
	return current, found
}

func savedPlanResult(plan core.EngineeringPlan) (core.EngineeringPlanResult, error) {
	var result core.EngineeringPlanResult
	if err := json.Unmarshal([]byte(plan.PlanJSON), &result); err != nil {
		return result, err
	}
	if coreDigest([]byte(plan.PlanJSON)) != plan.PlanSHA256 {
		return result, errors.New("saved normalized engineering plan hash does not match")
	}
	return result, nil
}

func (s *Service) recordGitInspectionFailure(ctx context.Context, dispatch core.Dispatch, task core.DevelopmentTask, inspectionErr error) (bool, bool, error) {
	flow, _, err := s.standard.GetClearDevStandardFlow(ctx, task.DevelopmentRequirementID)
	if err != nil {
		return false, false, err
	}
	name := fmt.Sprintf("scope-round-%d", task.ReworkCount)
	run, found := candidateCheckRun(flow, "", core.CandidateCheckScope, name)
	if !found {
		specJSON, _ := json.Marshal(struct {
			Kind       string   `json:"kind"`
			WritePaths []string `json:"writePaths"`
		}{Kind: "git-name-status", WritePaths: core.FrozenStandardTaskPlan().WritePaths})
		run = core.CandidateCheckRun{
			ID: s.newID(), DevelopmentTaskID: task.ID, DispatchID: dispatch.ID, BaseCommitSHA: dispatch.BaseCommitSHA,
			Kind: core.CandidateCheckScope, Name: name, CheckSpecSHA256: coreDigest(specJSON), ArgvJSON: "[]",
			Status: core.CandidateCheckRunStatusPending, CreatedAt: s.now().UTC(),
		}
		if _, _, err = s.standard.CreateClearDevCandidateCheckRun(ctx, run); err != nil {
			return false, false, err
		}
	}
	if run.Status == core.CandidateCheckRunStatusPending {
		now := s.now().UTC()
		reason := gitInspectionReason(inspectionErr)
		if errors.Is(inspectionErr, ports.ErrClearDevGitUnavailable) {
			_, err = s.standard.SettleClearDevCandidateCheckRun(ctx, core.SettleCandidateCheckRunCommand{
				CandidateCheckRunID: run.ID, At: now, ReasonCode: reason,
			})
		} else {
			exitCode := 1
			summary := "Git candidate inspection rejected the current Builder worktree"
			_, err = s.standard.SettleClearDevCandidateCheckRun(ctx, core.SettleCandidateCheckRunCommand{
				CandidateCheckRunID: run.ID, ExitCode: &exitCode, OutputSummary: summary,
				OutputSHA256: coreDigest([]byte(summary)), ChangedPathsJSON: "[]", Result: core.EvidenceResultFail, At: now, ReasonCode: reason,
			})
		}
		if err != nil {
			return false, false, err
		}
	}
	infrastructureFailure := errors.Is(inspectionErr, ports.ErrClearDevGitUnavailable)
	reason := gitInspectionReason(inspectionErr)
	if run.Status != core.CandidateCheckRunStatusPending {
		infrastructureFailure = run.Status == core.CandidateCheckRunStatusFailed
		reason = run.ReasonCode
	}
	if err = s.standard.ApplyClearDevStandardFailure(ctx, core.StandardFailureCommand{
		DispatchID: dispatch.ID, CandidateCheckRunID: run.ID,
		InfrastructureFailure: infrastructureFailure,
		ReasonCode:            reason, At: s.now().UTC(),
	}); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func gitInspectionReason(err error) core.ReasonCode {
	switch {
	case errors.Is(err, ports.ErrClearDevGitUnavailable):
		return core.ReasonCode("GIT_CHECKER_UNAVAILABLE")
	case errors.Is(err, ports.ErrClearDevWorkspaceDirty):
		return core.ReasonCode("BUILDER_WORKTREE_DIRTY")
	case errors.Is(err, ports.ErrClearDevCandidateNotDescendant):
		return core.ReasonCode("CANDIDATE_NOT_DESCENDANT")
	default:
		return core.ReasonCode("CANDIDATE_INVALID")
	}
}

func (s *Service) ensureScopeCheck(
	ctx context.Context,
	snapshot core.RequirementSnapshot,
	flow core.StandardFlowSnapshot,
	dispatch core.Dispatch,
	task core.DevelopmentTask,
	candidate core.CandidateCommit,
	plan core.StandardTaskPlan,
	inspection ports.ClearDevCandidateInspection,
) (core.CandidateCheckRun, bool, error) {
	name := "scope"
	run, found := candidateCheckRun(flow, candidate.ID, core.CandidateCheckScope, name)
	paths := make([]standardReviewPath, 0, len(inspection.Paths))
	for _, path := range inspection.Paths {
		paths = append(paths, standardReviewPath{Status: path.Status, Path: path.Path, OldPath: path.OldPath})
	}
	pathsJSON, _ := json.Marshal(paths)
	specJSON, _ := json.Marshal(struct {
		WritePaths                 []string `json:"writePaths"`
		GeneratedPaths             []string `json:"generatedPaths"`
		SharedPathsRequireApproval []string `json:"sharedPathsRequireApproval"`
		ForbiddenPaths             []string `json:"forbiddenPaths"`
	}{plan.WritePaths, plan.GeneratedPaths, plan.SharedPathsRequireApproval, plan.ForbiddenPaths})
	if !found {
		run = core.CandidateCheckRun{
			ID: s.newID(), DevelopmentTaskID: task.ID, CandidateCommitID: candidate.ID, DispatchID: dispatch.ID,
			BaseCommitSHA: dispatch.BaseCommitSHA, CandidateCommitSHA: candidate.CommitSHA,
			Kind: core.CandidateCheckScope, Name: name, CheckSpecSHA256: coreDigest(specJSON), ArgvJSON: "[]",
			Status: core.CandidateCheckRunStatusPending, CreatedAt: s.now().UTC(),
		}
		var err error
		run, _, err = s.standard.CreateClearDevCandidateCheckRun(ctx, run)
		if err != nil {
			return run, false, err
		}
		return run, true, nil
	}
	if run.Status == core.CandidateCheckRunStatusPending {
		passed, summary := scopePathsPass(plan, inspection.Paths)
		result, exitCode := core.EvidenceResultPass, 0
		if !passed {
			result, exitCode = core.EvidenceResultFail, 1
		}
		reason := core.ReasonNone
		if result == core.EvidenceResultFail {
			reason = core.ReasonCode("SCOPE_CHECK_FAILED")
		}
		_, err := s.standard.SettleClearDevCandidateCheckRun(ctx, core.SettleCandidateCheckRunCommand{
			CandidateCheckRunID: run.ID, ExitCode: &exitCode, OutputSummary: summary,
			OutputSHA256: coreDigest([]byte(summary)), ChangedPathsJSON: string(pathsJSON), Result: result, At: s.now().UTC(), ReasonCode: reason,
		})
		if err != nil {
			return run, false, err
		}
		return run, true, nil
	}
	changed, err := s.ensureCheckEvidence(ctx, snapshot, task, candidate, run)
	if err != nil || changed {
		return run, changed, err
	}
	if run.Status == core.CandidateCheckRunStatusFailed || run.Result == core.EvidenceResultFail {
		err = s.standard.ApplyClearDevStandardFailure(ctx, core.StandardFailureCommand{
			DispatchID: dispatch.ID, CandidateCheckRunID: run.ID,
			InfrastructureFailure: run.Status == core.CandidateCheckRunStatusFailed,
			ReasonCode:            run.ReasonCode, At: s.now().UTC(),
		})
		return run, err == nil, err
	}
	return run, false, nil
}

func scopePathsPass(plan core.StandardTaskPlan, paths []ports.ClearDevDiffPath) (bool, string) {
	rules := core.PathRules{
		WritePaths: plan.WritePaths, GeneratedPaths: plan.GeneratedPaths,
		SharedPathsRequireApproval: plan.SharedPathsRequireApproval, ForbiddenPaths: plan.ForbiddenPaths,
	}
	for _, change := range paths {
		for _, path := range []string{change.OldPath, change.Path} {
			if path == "" {
				continue
			}
			classification, err := rules.ClassifyPath(path)
			if err != nil || classification != core.PathAllowed {
				return false, "Git diff contains a path outside the approved write paths"
			}
		}
	}
	return true, "Git diff paths are within the approved write paths"
}

func (s *Service) ensureContainerCheck(
	ctx context.Context,
	snapshot core.RequirementSnapshot,
	flow core.StandardFlowSnapshot,
	dispatch core.Dispatch,
	task core.DevelopmentTask,
	candidate core.CandidateCommit,
	workspacePath string,
	kind core.CandidateCheckKind,
	spec core.StandardCheckSpec,
) (core.CandidateCheckRun, bool, error) {
	run, found := candidateCheckRun(flow, candidate.ID, kind, spec.Name)
	argvJSON, _ := json.Marshal(spec.Argv)
	specJSON, _ := json.Marshal(spec)
	if !found {
		run = core.CandidateCheckRun{
			ID: s.newID(), DevelopmentTaskID: task.ID, CandidateCommitID: candidate.ID, DispatchID: dispatch.ID,
			BaseCommitSHA: dispatch.BaseCommitSHA, CandidateCommitSHA: candidate.CommitSHA,
			Kind: kind, Name: spec.Name, CheckSpecSHA256: coreDigest(specJSON), ArgvJSON: string(argvJSON),
			Status: core.CandidateCheckRunStatusPending, CreatedAt: s.now().UTC(),
		}
		var err error
		run, _, err = s.standard.CreateClearDevCandidateCheckRun(ctx, run)
		if err != nil {
			return run, false, err
		}
		return run, true, nil
	}
	if run.Status == core.CandidateCheckRunStatusPending {
		result, runErr := s.checks.RunCandidateCheck(ctx, ports.ClearDevCheckRequest{
			RunID: run.ID, WorkspacePath: workspacePath, CandidateSHA: candidate.CommitSHA, Image: core.StandardCandidateCheckImage,
			Argv: append([]string(nil), spec.Argv...), Timeout: time.Duration(spec.TimeoutSeconds) * time.Second,
			MemoryBytes: standardCheckMemoryBytes, PidsLimit: standardCheckPidsLimit, OutputLimit: standardCheckOutputLimit,
		})
		command := core.SettleCandidateCheckRunCommand{CandidateCheckRunID: run.ID, ContainerImageID: result.ImageID, At: s.now().UTC()}
		if runErr != nil || result.Outcome == ports.ClearDevCheckInfraError {
			command.ReasonCode = core.ReasonCode("CHECKER_UNAVAILABLE")
		} else {
			exitCode := result.ExitCode
			command.ExitCode, command.TimedOut = &exitCode, result.TimedOut
			command.OutputSummary, command.OutputSHA256, command.ChangedPathsJSON = result.OutputSummary, result.OutputSHA256, "[]"
			command.Result = core.EvidenceResultFail
			if result.Outcome == ports.ClearDevCheckPass {
				command.Result = core.EvidenceResultPass
			} else if result.TimedOut {
				command.ReasonCode = core.ReasonCode("CHECK_TIMEOUT")
			} else {
				command.ReasonCode = core.ReasonCode("CHECK_FAILED")
			}
		}
		if _, err := s.standard.SettleClearDevCandidateCheckRun(ctx, command); err != nil {
			return run, false, err
		}
		return run, true, nil
	}
	changed, err := s.ensureCheckEvidence(ctx, snapshot, task, candidate, run)
	if err != nil || changed {
		return run, changed, err
	}
	if run.Status == core.CandidateCheckRunStatusFailed || run.Result == core.EvidenceResultFail {
		err = s.standard.ApplyClearDevStandardFailure(ctx, core.StandardFailureCommand{
			DispatchID: dispatch.ID, CandidateCheckRunID: run.ID,
			InfrastructureFailure: run.Status == core.CandidateCheckRunStatusFailed,
			ReasonCode:            run.ReasonCode, At: s.now().UTC(),
		})
		return run, err == nil, err
	}
	return run, false, nil
}

func (s *Service) ensureCheckEvidence(ctx context.Context, snapshot core.RequirementSnapshot, task core.DevelopmentTask, candidate core.CandidateCommit, run core.CandidateCheckRun) (bool, error) {
	if run.Status == core.CandidateCheckRunStatusFailed || run.Result == "" || candidate.ID == "" {
		return false, nil
	}
	evidenceID := "s02-check-evidence-" + run.ID
	if evidenceByID(snapshot, evidenceID) {
		return false, nil
	}
	kind, key := core.EvidenceKindScope, ""
	switch run.Kind {
	case core.CandidateCheckRequired:
		kind, key = core.EvidenceKindRequiredCheck, run.Name
	case core.CandidateCheckIntegration:
		kind = core.EvidenceKindIntegration
	}
	err := s.standard.AppendClearDevStandardEvidence(ctx, core.StandardEvidenceRecord{
		Evidence: core.EvidenceRecord{
			ID: evidenceID, DevelopmentRequirementID: task.DevelopmentRequirementID,
			SubjectType: core.SubjectDevelopmentTask, SubjectID: task.ID, Kind: kind, Key: key, Result: run.Result,
			CandidateCommitID: candidate.ID, CommitSHA: candidate.CommitSHA, Source: core.EvidenceSourceControlPlaneChecker,
			CreatedAt: s.now().UTC(),
		},
		CandidateCheckRunID: run.ID,
	})
	return err == nil, err
}

func evidenceByID(snapshot core.RequirementSnapshot, id string) bool {
	for _, evidence := range snapshot.Evidence {
		if evidence.ID == id {
			return true
		}
	}
	return false
}

func candidateCheckRun(flow core.StandardFlowSnapshot, candidateID string, kind core.CandidateCheckKind, name string) (core.CandidateCheckRun, bool) {
	for _, run := range flow.CandidateCheckRuns {
		if run.CandidateCommitID == candidateID && run.Kind == kind && run.Name == name {
			return run, true
		}
	}
	return core.CandidateCheckRun{}, false
}

func (s *Service) ensureLocalReview(
	ctx context.Context,
	snapshot core.RequirementSnapshot,
	flow core.StandardFlowSnapshot,
	version core.RequirementVersion,
	plan core.EngineeringPlan,
	dispatch core.Dispatch,
	task core.DevelopmentTask,
	candidate core.CandidateCommit,
	inspection ports.ClearDevCandidateInspection,
	builder domain.SessionRecord,
) (core.LocalReview, bool, error) {
	if review, found := localReviewForCandidate(flow, candidate.ID); found {
		return s.advanceLocalReview(ctx, snapshot, flow, dispatch, task, candidate, review)
	}
	reviewer, found := roleBinding(flow, core.StandardRoleReviewer, candidate.ID)
	branch := standardBranch("review", candidate.ID)
	if !found {
		if err := s.inspector.PrepareReviewBranch(ctx, builder.Metadata.WorkspacePath, branch, candidate.CommitSHA); err != nil {
			// A Reviewer worktree is not yet available, so persist this trusted Git
			// infrastructure failure through a checker fact on the current candidate.
			progressed, _, recordErr := s.recordReviewerPreparationFailure(ctx, dispatch, task, candidate, err)
			return core.LocalReview{}, progressed, recordErr
		}
		reviewer = core.RoleSessionBinding{
			ID: s.newID(), DevelopmentRequirementID: task.DevelopmentRequirementID, RequirementVersionID: version.ID,
			Role: core.StandardRoleReviewer, DispatchID: dispatch.ID, DevelopmentTaskID: task.ID, CandidateCommitID: candidate.ID,
			Status: core.RoleBindingStatusRequested, RequestedAt: s.now().UTC(),
		}
		reviewer.SessionCreationIdempotencyKey = standardSpawnKey(candidate.ID, reviewer.Role, reviewer.ID)
		if _, _, err := s.standard.CreateClearDevRoleBinding(ctx, core.CreateRoleBindingCommand{Binding: reviewer}); err != nil {
			return core.LocalReview{}, false, err
		}
		return core.LocalReview{}, true, nil
	}
	reviewerRecord, changed, err := s.ensureRoleSession(ctx, flow, reviewer, snapshot.Requirement.AOProjectID, domain.KindWorker, branch, core.ReasonCode("REVIEWER_UNAVAILABLE"))
	if err != nil {
		if errors.Is(err, errStandardStopped) {
			latest, _, loadErr := s.standard.GetClearDevStandardFlow(ctx, task.DevelopmentRequirementID)
			if loadErr != nil {
				return core.LocalReview{}, changed, loadErr
			}
			if failedBinding, found := roleBindingByID(latest, reviewer.ID); found && failedBinding.Status == core.RoleBindingStatusFailed {
				if applyErr := s.standard.ApplyClearDevStandardReviewerBindingFailure(ctx, core.StandardReviewerBindingFailureCommand{
					RoleBindingID: failedBinding.ID, ReasonCode: failedBinding.ReasonCode, At: s.now().UTC(),
				}); applyErr != nil {
					return core.LocalReview{}, changed, applyErr
				}
			}
		}
		return core.LocalReview{}, changed, err
	}
	if changed {
		return core.LocalReview{}, true, nil
	}
	if controlledPreflightIdle(reviewer.Status, reviewerRecord.ID) {
		return core.LocalReview{}, false, nil
	}
	if reviewerRecord.ID != "" {
		if reviewerRecord.Metadata.WorkspacePath == "" || reviewerRecord.Metadata.WorkspacePath == builder.Metadata.WorkspacePath {
			progressed, _, recordErr := s.recordReviewerPreparationFailure(ctx, dispatch, task, candidate, errors.New("Reviewer worktree is not fixed to the candidate"))
			return core.LocalReview{}, progressed, recordErr
		}
		reviewerInspection, inspectErr := s.inspector.InspectCandidate(ctx, reviewerRecord.Metadata.WorkspacePath, candidate.CommitSHA)
		if inspectErr != nil || reviewerInspection.BaseSHA != candidate.CommitSHA || reviewerInspection.CandidateSHA != candidate.CommitSHA || len(reviewerInspection.Paths) != 0 {
			if inspectErr == nil {
				inspectErr = ports.ErrClearDevCandidateInvalid
			}
			progressed, _, recordErr := s.recordReviewerPreparationFailure(ctx, dispatch, task, candidate, inspectErr)
			return core.LocalReview{}, progressed, recordErr
		}
	}

	reviewID := ""
	if step, ok := agentStep(flow, reviewer.ID, core.AgentStepLocalReview, ""); ok {
		reviewID = step.RequestID
	} else {
		reviewID = s.newID()
	}
	packet, packetSHA, diffPaths, err := buildStandardReviewPacket(reviewID, version.RequirementText, plan, dispatch, candidate, inspection, flow.CandidateCheckRuns)
	if err != nil {
		return core.LocalReview{}, false, err
	}
	prompt := reviewerPrompt(packet, reviewID, candidate.ID, candidate.CommitSHA, packetSHA)
	step, stepFound := agentStep(flow, reviewer.ID, core.AgentStepLocalReview, reviewID)
	if !stepFound {
		stepID := s.newID()
		step = core.AgentStep{
			ID: stepID, RoleBindingID: reviewer.ID, Kind: core.AgentStepLocalReview, RequestID: reviewID,
			ClientMessageID: "cleardev-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)),
			SendStatus: core.AgentStepSendStatusPending, RequestedAt: s.now().UTC(),
		}
		if _, _, err = s.standard.CreateClearDevStandardAgentStep(ctx, step); err != nil {
			return core.LocalReview{}, false, err
		}
		return core.LocalReview{}, true, nil
	}
	review := core.LocalReview{
		ID: reviewID, CandidateCommitID: candidate.ID, DispatchID: dispatch.ID,
		ReviewPacketJSON: string(packet), ReviewPacketSHA256: packetSHA,
		ReviewerRoleBindingID: reviewer.ID, AgentStepID: step.ID,
		Status: core.LocalReviewStatusPending, CreatedAt: step.RequestedAt,
	}
	if _, _, err = s.standard.CreateClearDevLocalReview(ctx, review); err != nil {
		return core.LocalReview{}, false, err
	}
	_ = diffPaths
	return review, true, nil
}

func (s *Service) advanceLocalReview(
	ctx context.Context,
	snapshot core.RequirementSnapshot,
	flow core.StandardFlowSnapshot,
	dispatch core.Dispatch,
	task core.DevelopmentTask,
	candidate core.CandidateCommit,
	review core.LocalReview,
) (core.LocalReview, bool, error) {
	reviewer, found := roleBindingByID(flow, review.ReviewerRoleBindingID)
	if !found {
		return review, false, errors.New("local review lost its Reviewer binding")
	}
	var packet standardReviewPacket
	if coreDigest([]byte(review.ReviewPacketJSON)) != review.ReviewPacketSHA256 {
		return review, false, errors.New("saved review packet hash does not match its immutable JSON")
	}
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		return review, false, err
	}
	diffPaths := make([]string, 0, len(packet.Diff)*2)
	for _, path := range packet.Diff {
		if path.OldPath != "" {
			diffPaths = append(diffPaths, path.OldPath)
		}
		diffPaths = append(diffPaths, path.Path)
	}
	prompt := reviewerPrompt([]byte(review.ReviewPacketJSON), review.ID, candidate.ID, candidate.CommitSHA, review.ReviewPacketSHA256)
	if review.Status == core.LocalReviewStatusPending {
		var result core.LocalReviewResult
		message, changed, stepErr := s.runAgentStep(ctx, flow, reviewer, review.ID, core.AgentStepLocalReview, prompt,
			standardStepReasons{Invalid: core.ReasonCode("REVIEW_RESULT_INVALID"), Timeout: core.ReasonCode("REVIEW_TIMEOUT"), Unavailable: core.ReasonCode("REVIEWER_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
			func(raw []byte) error {
				var parseErr error
				result, parseErr = core.ParseLocalReviewResult(raw, review.ID, candidate.ID, candidate.CommitSHA, review.ReviewPacketSHA256, diffPaths)
				return parseErr
			})
		if stepErr != nil {
			if errors.Is(stepErr, errStandardStopped) {
				latest, _, loadErr := s.standard.GetClearDevStandardFlow(ctx, task.DevelopmentRequirementID)
				failed, ok := agentStep(latest, reviewer.ID, core.AgentStepLocalReview, review.ID)
				if loadErr == nil && ok && failed.SendStatus == core.AgentStepSendStatusFailed {
					settled, settleErr := s.standard.SettleClearDevLocalReview(ctx, core.SettleLocalReviewCommand{
						LocalReviewID: review.ID, ReasonCode: failed.ReasonCode, At: s.now().UTC(),
					})
					if settleErr != nil {
						return review, changed, settleErr
					}
					if settled {
						applyErr := s.standard.ApplyClearDevStandardFailure(ctx, core.StandardFailureCommand{
							DispatchID: dispatch.ID, LocalReviewID: review.ID, InfrastructureFailure: true,
							ReasonCode: failed.ReasonCode, At: s.now().UTC(),
						})
						if applyErr != nil {
							return review, true, applyErr
						}
						return review, true, errStandardStopped
					}
				}
			}
			return review, changed, stepErr
		}
		if changed {
			return review, true, nil
		}
		if _, err := s.standard.SettleClearDevLocalReview(ctx, core.SettleLocalReviewCommand{
			LocalReviewID: review.ID, TurnID: message.TurnID, FinalMessageID: message.MessageID,
			Verdict: core.LocalReviewVerdict(result.Verdict), ReasonCode: core.ReasonCode(result.ReasonCode), Summary: result.Summary, At: s.now().UTC(),
		}); err != nil {
			return review, false, err
		}
		return review, true, nil
	}
	changed, err := s.ensureReviewEvidence(ctx, snapshot, task, candidate, reviewer, review)
	if err != nil || changed {
		return review, changed, err
	}
	if review.Status == core.LocalReviewStatusFailed || review.Verdict != core.LocalReviewPass {
		err = s.standard.ApplyClearDevStandardFailure(ctx, core.StandardFailureCommand{
			DispatchID: dispatch.ID, LocalReviewID: review.ID,
			InfrastructureFailure: review.Status == core.LocalReviewStatusFailed || review.Verdict == core.LocalReviewBlocked,
			ReasonCode:            review.ReasonCode, At: s.now().UTC(),
		})
		return review, err == nil, err
	}
	return review, false, nil
}

func (s *Service) ensureReviewEvidence(ctx context.Context, snapshot core.RequirementSnapshot, task core.DevelopmentTask, candidate core.CandidateCommit, reviewer core.RoleSessionBinding, review core.LocalReview) (bool, error) {
	if review.Status != core.LocalReviewStatusSettled {
		return false, nil
	}
	id := "s02-review-evidence-" + review.ID
	if evidenceByID(snapshot, id) {
		return false, nil
	}
	result := core.EvidenceResultFail
	if review.Verdict == core.LocalReviewPass {
		result = core.EvidenceResultPass
	}
	err := s.standard.AppendClearDevStandardEvidence(ctx, core.StandardEvidenceRecord{
		Evidence: core.EvidenceRecord{
			ID: id, DevelopmentRequirementID: task.DevelopmentRequirementID,
			SubjectType: core.SubjectDevelopmentTask, SubjectID: task.ID, Kind: core.EvidenceKindReview, Result: result,
			CandidateCommitID: candidate.ID, CommitSHA: candidate.CommitSHA, Source: core.EvidenceSourceReviewAdapter,
			SourceAOSessionID: reviewer.AOSessionID, CreatedAt: s.now().UTC(),
		},
		LocalReviewID: review.ID,
	})
	return err == nil, err
}

func buildStandardReviewPacket(
	assignmentID, requirement string,
	plan core.EngineeringPlan,
	dispatch core.Dispatch,
	candidate core.CandidateCommit,
	inspection ports.ClearDevCandidateInspection,
	runs []core.CandidateCheckRun,
) ([]byte, string, []string, error) {
	packet := standardReviewPacket{
		SchemaVersion: 1, AssignmentID: assignmentID, Requirement: requirement,
		PlanJSON: json.RawMessage(plan.PlanJSON), ExecutionPack: json.RawMessage(dispatch.ExecutionPackageJSON),
		BaseSHA: dispatch.BaseCommitSHA, CandidateID: candidate.ID, CandidateSHA: candidate.CommitSHA,
	}
	diffPaths := make([]string, 0, len(inspection.Paths)*2)
	for _, path := range inspection.Paths {
		packet.Diff = append(packet.Diff, standardReviewPath{Status: path.Status, Path: path.Path, OldPath: path.OldPath})
		if path.OldPath != "" {
			diffPaths = append(diffPaths, path.OldPath)
		}
		diffPaths = append(diffPaths, path.Path)
	}
	for _, run := range runs {
		if run.CandidateCommitID != candidate.ID || run.Status != core.CandidateCheckRunStatusSettled {
			continue
		}
		packet.Checks = append(packet.Checks, standardReviewCheck{
			Kind: string(run.Kind), Name: run.Name, Result: string(run.Result), OutputSHA256: run.OutputSHA256,
		})
	}
	sort.Slice(packet.Checks, func(i, j int) bool {
		if packet.Checks[i].Kind != packet.Checks[j].Kind {
			return packet.Checks[i].Kind < packet.Checks[j].Kind
		}
		return packet.Checks[i].Name < packet.Checks[j].Name
	})
	encoded, err := json.Marshal(packet)
	if err != nil {
		return nil, "", nil, err
	}
	return encoded, coreDigest(encoded), diffPaths, nil
}

func localReviewForCandidate(flow core.StandardFlowSnapshot, candidateID string) (core.LocalReview, bool) {
	for _, review := range flow.LocalReviews {
		if review.CandidateCommitID == candidateID {
			return review, true
		}
	}
	return core.LocalReview{}, false
}

func (s *Service) recordReviewerPreparationFailure(ctx context.Context, dispatch core.Dispatch, task core.DevelopmentTask, candidate core.CandidateCommit, cause error) (bool, bool, error) {
	flow, _, err := s.standard.GetClearDevStandardFlow(ctx, task.DevelopmentRequirementID)
	if err != nil {
		return false, false, err
	}
	name := "review-worktree"
	run, found := candidateCheckRun(flow, candidate.ID, core.CandidateCheckScope, name)
	if !found {
		spec := []byte(`{"kind":"review-worktree"}`)
		run = core.CandidateCheckRun{
			ID: s.newID(), DevelopmentTaskID: task.ID, CandidateCommitID: candidate.ID, DispatchID: dispatch.ID,
			BaseCommitSHA: dispatch.BaseCommitSHA, CandidateCommitSHA: candidate.CommitSHA,
			Kind: core.CandidateCheckScope, Name: name, CheckSpecSHA256: coreDigest(spec), ArgvJSON: "[]",
			Status: core.CandidateCheckRunStatusPending, CreatedAt: s.now().UTC(),
		}
		if _, _, err = s.standard.CreateClearDevCandidateCheckRun(ctx, run); err != nil {
			return false, false, err
		}
	}
	if run.Status == core.CandidateCheckRunStatusPending {
		_, err = s.standard.SettleClearDevCandidateCheckRun(ctx, core.SettleCandidateCheckRunCommand{
			CandidateCheckRunID: run.ID, ReasonCode: core.ReasonCode("GIT_CHECKER_UNAVAILABLE"), At: s.now().UTC(),
		})
		if err != nil {
			return false, false, err
		}
	}
	err = s.standard.ApplyClearDevStandardFailure(ctx, core.StandardFailureCommand{
		DispatchID: dispatch.ID, CandidateCheckRunID: run.ID, InfrastructureFailure: true,
		ReasonCode: core.ReasonCode("GIT_CHECKER_UNAVAILABLE"), ReasonText: cause.Error(), At: s.now().UTC(),
	})
	return err == nil, false, err
}

func (s *Service) ensureStandardStatusReport(
	ctx context.Context,
	snapshot core.RequirementSnapshot,
	flow core.StandardFlowSnapshot,
	steward domain.SessionRecord,
	requirementID string,
	_ core.EngineeringPlan,
	dispatch core.Dispatch,
) (bool, bool, error) {
	binding, found := roleBinding(flow, core.StandardRoleSteward, "")
	if !found || binding.AOSessionID != string(steward.ID) {
		return false, true, nil
	}
	progress := core.DeriveOverallProgress(snapshot, s.now().UTC())
	requestID := dispatch.ID + ":status:" + string(progress.Phase) + ":" + string(progress.Attention)
	prompt := statusReportPrompt(requirementID, string(progress.Phase), string(progress.Attention))
	_, changed, err := s.runAgentStep(ctx, flow, binding, requestID, core.AgentStepStatusReport, prompt,
		standardStepReasons{Invalid: core.ReasonCode("STEWARD_RESULT_INVALID"), Timeout: core.ReasonCode("STEWARD_TIMEOUT"), Unavailable: core.ReasonCode("STEWARD_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
		func(raw []byte) error {
			_, parseErr := core.ParseStatusReportResult(raw, requirementID, string(progress.Phase), string(progress.Attention))
			return parseErr
		})
	if err != nil {
		// Status reporting is display-only. The completed task and evidence remain
		// authoritative even when the Steward cannot produce a valid summary.
		if errors.Is(err, errStandardStopped) {
			return true, true, nil
		}
		return changed, false, err
	}
	return changed, !changed, nil
}
