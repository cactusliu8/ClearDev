package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (s *Service) startComplexQuickExecution(ctx context.Context, requirementID string, planning core.ComplexPlanningSnapshot, version core.RequirementVersion, plan core.ComplexEngineeringPlan, review core.ComplexPlanReview, source core.ComplexExecutionSnapshot) (RequirementView, error) {
	acceptedTaskSetVersion, taskSetErr := core.NextComplexQuickTaskSetVersion(source.Run.TaskSetVersion)
	if taskSetErr != nil || !validCompletedComplexQuickSource(version, plan, source) {
		return RequirementView{}, apierr.Conflict(string(core.ReasonPreconditionNotMet), "Quick execution requires a completed current integration", nil)
	}
	if s.direction != nil {
		stopped, stopErr := s.direction.HasActiveClearDevDirectionStop(ctx, version.ID)
		if stopErr != nil {
			return RequirementView{}, apierr.Internal("CLEARDEV_DIRECTION_READ_FAILED", "Could not read the ClearDev direction-change facts")
		}
		if stopped {
			return RequirementView{}, apierr.Conflict(string(core.ReasonComplexExecutionStopped), "Quick execution is blocked by an active direction stop", nil)
		}
	}
	steward, ok := core.ComplexRoleBindingByRole(planning, core.StandardRoleSteward)
	if !ok || strings.TrimSpace(steward.AOSessionID) == "" || steward.Status != core.RoleBindingStatusBound {
		return RequirementView{}, apierr.Conflict("STEWARD_BINDING_MISSING", "The approved complex plan has no Project Steward binding", nil)
	}
	now := s.now().UTC()
	runID := s.newID()
	runPackage, runPackageSHA, packageErr := core.BuildComplexQuickRunPackage(runID, version.ID, version.SHA256, plan.ID, plan.PlanSHA256, "", source.Integration.CandidateCommitSHA, acceptedTaskSetVersion)
	if packageErr != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_QUICK_PACKAGE_INVALID", "Could not bind the immutable quick execution package")
	}
	run := core.ComplexQuickRun{
		ID: runID, DevelopmentRequirementID: requirementID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256,
		SourceExecutionRunID: source.Run.ID, PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, IntegrationBaseSHA: source.Integration.CandidateCommitSHA,
		Mode: core.WorkModeQuick, ExpectedTaskSetVersion: source.Run.TaskSetVersion, TaskSetVersion: acceptedTaskSetVersion,
		ExecutionPackageJSON: string(runPackage), ExecutionPackageSHA256: runPackageSHA, StewardRoleBindingID: steward.ID, CreatedAt: now,
	}
	role := core.ComplexExecutionRoleBinding{
		ID: s.newID(), ExecutionRunID: runID, SourceComplexRoleBindingID: steward.ID, Role: core.StandardRoleSteward,
		AOSessionID: steward.AOSessionID, SessionCreationIdempotencyKey: "cleardev-complex-quick:steward:" + runID,
		Status: core.RoleBindingStatusBound, RequestedAt: now,
	}
	record, recordFound, recordErr := s.ao.GetSession(ctx, domain.SessionID(steward.AOSessionID))
	if recordErr != nil {
		return RequirementView{}, apierr.Internal("STEWARD_SESSION_READ_FAILED", "Could not read the original Project Steward session")
	}
	if !recordFound {
		return RequirementView{}, apierr.Conflict("STEWARD_SESSION_MISSING", "The original Project Steward session is unavailable", nil)
	}
	var step core.AgentStep
	if record.IsTerminated || record.Activity.State == domain.ActivityExited {
		role.Status, role.AOSessionID = core.RoleBindingStatusRequested, ""
	} else {
		boundAt := now
		role.BoundAt = &boundAt
		stepID := s.newID()
		prompt := complexQuickTaskRequestPrompt(runID, version, plan, review, source, source.Integration.CandidateCommitSHA)
		step = core.AgentStep{ID: stepID, RoleBindingID: role.ID, Kind: core.AgentStepDispatchRequest, RequestID: runID, ClientMessageID: "cleardev-complex-quick-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
	}
	_, _, err := s.complexExecution.StartClearDevComplexQuickExecution(ctx, core.StartComplexQuickExecutionCommand{Run: run, StewardRoleBinding: role, AgentStep: step})
	if err != nil {
		return RequirementView{}, mapStoreError(err, "START_COMPLEX_QUICK_FAILED")
	}
	s.scheduleComplexStandardExecution(requirementID)
	return s.GetRequirement(ctx, requirementID)
}

func (s *Service) runComplexQuickExecution(ctx context.Context, requirementID string) error {
	for i := 0; i < 64; i++ {
		progressed, done, err := s.advanceComplexQuickExecution(ctx, requirementID)
		if errors.Is(err, errComplexExecutionStopped) || done {
			return nil
		}
		if err != nil {
			return err
		}
		if !progressed {
			return nil
		}
	}
	return errors.New("ClearDev quick execution exceeded bounded advance count")
}

func (s *Service) advanceComplexQuickExecution(ctx context.Context, requirementID string) (bool, bool, error) {
	snapshot, found, err := s.complexExecution.GetClearDevComplexQuickExecution(ctx, requirementID)
	if err != nil || !found {
		return false, !found, err
	}
	if s.direction != nil {
		stopped, stopErr := s.direction.HasActiveClearDevDirectionStop(ctx, snapshot.Run.RequirementVersionID)
		if stopErr != nil {
			return false, false, stopErr
		}
		if stopped {
			return false, true, errComplexExecutionStopped
		}
	}
	phase, _ := core.DeriveComplexQuickPhase(snapshot)
	if phase == core.ComplexExecutionCompleted {
		if err := s.releaseCandidateCheckEnvironment(ctx, snapshot.Run.ID+":check-preflight"); err != nil {
			return false, false, err
		}
		return false, true, nil
	}
	if phase == core.ComplexExecutionNeedsHuman || phase == core.ComplexExecutionBlocked {
		return false, true, nil
	}
	requirement, found, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil || !found {
		return false, false, err
	}
	if requirement.Requirement.CancelledAt != nil {
		if err := s.releaseCandidateCheckEnvironment(ctx, snapshot.Run.ID+":check-preflight"); err != nil {
			return false, false, err
		}
		return false, true, nil
	}
	planning, found, err := s.complex.GetClearDevComplexPlanning(ctx, requirementID)
	if err != nil || !found {
		return false, false, err
	}
	source, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil || !found {
		return false, false, err
	}
	version, ok := currentConfirmedVersion(requirement)
	plan, review, planOK := approvedComplexExecutionPlan(planning, snapshot.Run.RequirementVersionID)
	expectedTaskSet := snapshot.Run.ExpectedTaskSetVersion
	if phase != core.ComplexExecutionAwaitingSteward {
		expectedTaskSet = snapshot.Run.TaskSetVersion
	}
	nextTaskSet, taskSetErr := core.NextComplexQuickTaskSetVersion(snapshot.Run.ExpectedTaskSetVersion)
	if !ok || !planOK || taskSetErr != nil || nextTaskSet != snapshot.Run.TaskSetVersion ||
		version.ID != snapshot.Run.RequirementVersionID || version.SHA256 != snapshot.Run.RequirementSHA256 || version.TaskSetVersion != expectedTaskSet ||
		snapshot.Run.SourceExecutionRunID != source.Run.ID || source.Run.TaskSetVersion != snapshot.Run.ExpectedTaskSetVersion ||
		source.Integration == nil || source.Integration.CandidateCommitSHA != snapshot.Run.IntegrationBaseSHA ||
		!validCompletedComplexQuickSource(version, plan, source) {
		return false, true, errComplexExecutionStopped
	}
	parsed, parseErr := parseComplexExecutionPlan(plan)
	if parseErr != nil {
		return false, true, errComplexExecutionStopped
	}
	if phase == core.ComplexExecutionAwaitingSteward {
		return s.advanceComplexQuickSteward(ctx, snapshot, planning, requirement, version, plan, review, source, parsed)
	}
	if phase == core.ComplexExecutionBindingBuilder {
		return s.ensureComplexQuickBuilder(ctx, snapshot, requirement.Requirement.AOProjectID)
	}
	if phase == core.ComplexExecutionReadyToDispatch || phase == core.ComplexExecutionReworking {
		return s.dispatchComplexQuickBuilder(ctx, snapshot)
	}
	if phase == core.ComplexExecutionBuilding {
		return s.advanceComplexQuickDispatch(ctx, snapshot, requirement.Requirement.AOProjectID)
	}
	if phase == core.ComplexExecutionIntegrating {
		return s.completeComplexQuickIfReady(ctx, snapshot, requirement.Requirement.AOProjectID)
	}
	return false, false, nil
}

func (s *Service) advanceComplexQuickSteward(ctx context.Context, snapshot core.ComplexQuickSnapshot, planning core.ComplexPlanningSnapshot, requirement core.RequirementSnapshot, version core.RequirementVersion, plan core.ComplexEngineeringPlan, review core.ComplexPlanReview, source core.ComplexExecutionSnapshot, parsed core.ComplexEngineeringPlanResult) (bool, bool, error) {
	execution := quickSnapshotAsExecution(snapshot)
	steward := activeComplexExecutionSteward(execution)
	if steward.ID == "" {
		return false, true, errComplexExecutionStopped
	}
	if steward.Status == core.RoleBindingStatusRequested {
		return s.ensureComplexQuickSteward(ctx, snapshot, requirement.Requirement.AOProjectID)
	}
	if steward.Status != core.RoleBindingStatusBound || strings.TrimSpace(steward.AOSessionID) == "" {
		return false, true, errComplexExecutionStopped
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(steward.AOSessionID))
	if err != nil {
		return false, false, err
	}
	if !found {
		return false, false, errors.New("ClearDev Steward session state is unavailable")
	}
	if record.IsTerminated || record.Activity.State == domain.ActivityExited {
		sourceRole, sourceFound := complexPlanningRoleBindingByID(planning, steward.SourceComplexRoleBindingID)
		if !sourceFound || sourceRole.AOSessionID == "" || steward.AOSessionID != sourceRole.AOSessionID {
			changed, endErr := s.complexExecution.EndClearDevComplexQuickRoleBinding(ctx, steward.ID, core.ReasonCode("STEWARD_CONTINUATION_ENDED"), s.now().UTC())
			return changed, true, endErr
		}
		now := s.now().UTC()
		bindingID := s.newID()
		binding := core.ComplexExecutionRoleBinding{ID: bindingID, ExecutionRunID: snapshot.Run.ID, SourceComplexRoleBindingID: steward.SourceComplexRoleBindingID, ContinuationOfRoleBindingID: steward.ID, Role: core.StandardRoleSteward, SessionCreationIdempotencyKey: "cleardev-complex-quick:steward-continuation:" + snapshot.Run.ID, Status: core.RoleBindingStatusRequested, RequestedAt: now}
		changed, continueErr := s.complexExecution.ContinueClearDevComplexQuickSteward(ctx, steward.ID, binding, core.AgentStep{}, now)
		return changed, false, continueErr
	}
	if !s.validComplexExecutionSteward(ctx, record, requirement.Requirement.AOProjectID) {
		changed, endErr := s.complexExecution.EndClearDevComplexQuickRoleBinding(ctx, steward.ID, core.ReasonCode("STEWARD_SESSION_INVALID"), s.now().UTC())
		return changed, true, endErr
	}
	var step core.AgentStep
	for _, item := range snapshot.AgentSteps {
		if item.RoleBindingID == steward.ID && item.Kind == core.AgentStepDispatchRequest && item.RequestID == snapshot.Run.ID {
			step = item
			break
		}
	}
	if step.ID == "" {
		now := s.now().UTC()
		stepID := s.newID()
		prompt := complexQuickTaskRequestPrompt(snapshot.Run.ID, version, plan, review, source, snapshot.Run.IntegrationBaseSHA)
		created := core.AgentStep{ID: stepID, RoleBindingID: steward.ID, Kind: core.AgentStepDispatchRequest, RequestID: snapshot.Run.ID, ClientMessageID: "cleardev-complex-quick-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
		_, _, err := s.complexExecution.CreateClearDevComplexQuickAgentStep(ctx, created)
		return err == nil, false, err
	}
	prompt := complexQuickTaskRequestPrompt(snapshot.Run.ID, version, plan, review, source, snapshot.Run.IntegrationBaseSHA)
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return false, true, errComplexExecutionStopped
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.relayAgentTurn(ctx, snapshot.Run.DevelopmentRequirementID, core.AgentStepCategoryQuickExecution, step, steward.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return false, false, err
		}
		_, err := s.complexExecution.MarkClearDevComplexQuickAgentStepSent(ctx, step.ID, s.now().UTC())
		return err == nil, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		validate := func(raw []byte) error {
			_, err := parseQuickStewardRequest(raw, snapshot.Run.ID, version, snapshot.Run.IntegrationBaseSHA, snapshot.Run.ExpectedTaskSetVersion)
			return err
		}
		fail := func(ctx context.Context, item core.AgentStep, reason core.ReasonCode) error {
			if reason != core.ReasonCode("STEWARD_RESULT_INVALID") {
				return errors.New("quick steward observation failed: " + string(reason))
			}
			_, err := s.complexExecution.FailClearDevComplexQuickAgentStep(ctx, item.ID, reason, s.now().UTC())
			return err
		}
		message, stopped, err := s.awaitValidAgentJSON(ctx, snapshot.Run.DevelopmentRequirementID, core.AgentStepCategoryQuickExecution, steward.AOSessionID, step, prompt, validate, standardStepReasons{
			Invalid: "STEWARD_RESULT_INVALID", Timeout: "STEWARD_TIMEOUT", Unavailable: "STEWARD_UNAVAILABLE",
		}, fail, errComplexExecutionStopped)
		if err != nil || stopped {
			return false, stopped, err
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, message.TurnID, message.MessageID, message.Text, coreDigest([]byte(message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexQuickAgentStep(ctx, step)
		return err == nil, false, err
	}
	if step.SendStatus != core.AgentStepSendStatusSettled {
		return false, true, errComplexExecutionStopped
	}
	result, err := parseQuickStewardRequest([]byte(step.FinalMessageText), snapshot.Run.ID, version, snapshot.Run.IntegrationBaseSHA, snapshot.Run.ExpectedTaskSetVersion)
	if err != nil {
		changed, endErr := s.complexExecution.EndClearDevComplexQuickRoleBinding(ctx, steward.ID, core.ReasonCode("STEWARD_RESULT_INVALID"), s.now().UTC())
		if endErr != nil {
			return false, false, endErr
		}
		return changed, true, errComplexExecutionStopped
	}
	if result.Decision == string(core.ComplexExecutionDecisionNeedsHuman) {
		if err := s.complexExecution.SettleClearDevComplexQuickRequest(ctx, snapshot.Run.ID, core.ReasonQuickNeedsHuman, s.now().UTC()); err != nil {
			return false, false, err
		}
		return true, true, nil
	}
	planTask, ok := complexPlanTaskByKey(parsed, result.SourceTaskKey)
	done := ok && complexQuickSourceTaskDone(source, result.SourceTaskKey)
	selection, selectionErr := core.SelectComplexQuickExecution(result, planTask, parsed, version, snapshot.Run.IntegrationBaseSHA, done)
	if selectionErr != nil || selection.ReasonCode != core.ReasonQuickFollowUpApproved {
		reason := selection.ReasonCode
		if reason == "" {
			reason = core.ReasonQuickUnsafe
		}
		if err := s.complexExecution.SettleClearDevComplexQuickRequest(ctx, snapshot.Run.ID, reason, s.now().UTC()); err != nil {
			return false, false, err
		}
		return true, true, nil
	}
	command, err := s.materializeComplexQuickExecution(snapshot.Run, result, planTask, parsed, version, source)
	if err != nil {
		return false, true, err
	}
	if err := s.complexExecution.MaterializeClearDevComplexQuickExecution(ctx, command); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (s *Service) materializeComplexQuickExecution(run core.ComplexQuickRun, request core.ComplexQuickTaskRequestResult, source core.ComplexPlanTask, plan core.ComplexEngineeringPlanResult, version core.RequirementVersion, sourceExecution core.ComplexExecutionSnapshot) (core.MaterializeComplexQuickExecutionCommand, error) {
	now := s.now().UTC()
	task := core.ComplexExecutionTask{ID: s.newID(), ExecutionRunID: run.ID, TaskKey: source.Key, DevelopmentTaskID: s.newID(), Ordinal: 0, Status: core.DevelopmentTaskStatusPlanned, CurrentRound: 0}
	writePaths := uniqueQuickFollowUpPaths(append(append([]string{}, request.WritePaths...), request.TestPaths...))
	followUp := source
	followUp.WritePaths = writePaths
	followUp.GeneratedPaths = []string{}
	followUp.SharedPathsRequireApproval = []string{}
	followUp.Objective = request.Objective
	if strings.TrimSpace(followUp.Objective) == "" {
		followUp.Objective = request.Summary
	}
	required, integration, inheritErr := inheritedComplexQuickCheckSpecs(sourceExecution, source, plan, request, run.ID, task.ID, now, s.newID)
	if inheritErr != nil {
		return core.MaterializeComplexQuickExecutionCommand{}, inheritErr
	}
	packageChecks := make([]core.ComplexExecutionCheckSpec, 0, len(required))
	for _, spec := range required {
		packageChecks = append(packageChecks, core.ComplexExecutionCheckSpec{ID: spec.CheckID, Argv: append([]string(nil), spec.Argv...), TimeoutSeconds: spec.TimeoutSeconds})
	}
	_, encoded, digest, err := core.BuildComplexQuickExecutionPackage(core.ComplexStandardExecutionPackageInput{
		ExecutionRunID: run.ID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, RequirementText: version.RequirementText,
		PlanID: run.PlanID, PlanSHA256: run.PlanSHA256, TaskSetVersion: run.TaskSetVersion, TaskID: task.DevelopmentTaskID, Task: followUp,
	}, writePaths, packageChecks)
	if err != nil {
		return core.MaterializeComplexQuickExecutionCommand{}, err
	}
	task.ExecutionPackageJSON, task.ExecutionPackageSHA256 = string(encoded), digest
	command := core.MaterializeComplexQuickExecutionCommand{
		RunID: run.ID, SourceTaskKey: source.Key, Task: task, At: now,
		WritePaths: writePaths, GeneratedPaths: []string{}, SharedPaths: []string{}, ForbiddenPaths: append([]string(nil), source.ForbiddenPaths...),
		BuilderBinding: core.ComplexExecutionRoleBinding{ID: s.newID(), ExecutionRunID: run.ID, Role: core.StandardRoleBuilder, SessionCreationIdempotencyKey: "cleardev-complex-execution:builder-quick:" + run.ID, Status: core.RoleBindingStatusRequested, RequestedAt: now},
	}
	command.CheckSpecs = append(command.CheckSpecs, required...)
	command.CheckSpecs = append(command.CheckSpecs, core.ComplexExecutionCheckSpecFact{ID: s.newID(), ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, CheckID: "SCOPE", Kind: core.CandidateCheckScope, CheckSpecSHA256: complexScopeSpecDigest(followUp), Argv: []string{}, TimeoutSeconds: 0, CreatedAt: now})
	command.CheckSpecs = append(command.CheckSpecs, integration...)
	return command, nil
}

func validCompletedComplexQuickSource(version core.RequirementVersion, plan core.ComplexEngineeringPlan, source core.ComplexExecutionSnapshot) bool {
	return source.Run.CompletedAt != nil && source.Integration != nil &&
		source.Run.RequirementVersionID == version.ID && source.Run.RequirementSHA256 == version.SHA256 &&
		source.Run.PlanID == plan.ID && source.Run.PlanSHA256 == plan.PlanSHA256 && source.Run.TaskSetVersion > 0 &&
		source.Integration.ExecutionRunID == source.Run.ID && validComplexExecutionCommitSHA(source.Integration.CandidateCommitSHA)
}

func inheritedComplexQuickCheckSpecs(sourceExecution core.ComplexExecutionSnapshot, sourceTask core.ComplexPlanTask, plan core.ComplexEngineeringPlanResult, request core.ComplexQuickTaskRequestResult, runID, taskID string, at time.Time, newID func() string) ([]core.ComplexExecutionCheckSpecFact, []core.ComplexExecutionCheckSpecFact, error) {
	if !sameQuickCheckNames(request.RequiredCheckIDs, sourceTask.RequiredCheckIDs) || !sameQuickCheckNames(request.IntegrationCheckIDs, plan.IntegrationCheckIDs) {
		return nil, nil, errors.New("quick execution check names do not match the completed source plan")
	}
	var completedTask core.ComplexExecutionTask
	for _, task := range sourceExecution.Tasks {
		if task.TaskKey != sourceTask.Key {
			continue
		}
		if completedTask.ID != "" || task.Status != core.DevelopmentTaskStatusDone {
			return nil, nil, errors.New("quick execution source task is not one completed task")
		}
		completedTask = task
	}
	if completedTask.ID == "" || sourceExecution.Integration == nil || sourceExecution.Integration.ExecutionRunID != sourceExecution.Run.ID {
		return nil, nil, errors.New("quick execution source completion is unavailable")
	}
	var verification core.ComplexExecutionVerification
	for _, item := range sourceExecution.Verifications {
		if item.ComplexExecutionTaskID == completedTask.ID {
			verification = item
		}
	}
	if verification.ID == "" {
		return nil, nil, errors.New("quick execution source task has no verified candidate")
	}
	runs := make(map[string]core.ComplexExecutionCheckRun, len(sourceExecution.CheckRuns))
	for _, run := range sourceExecution.CheckRuns {
		runs[run.ID] = run
	}
	requiredEvidence := make(map[string]struct{}, len(verification.RequiredCheckRunIDs))
	for _, id := range verification.RequiredCheckRunIDs {
		requiredEvidence[id] = struct{}{}
	}
	integrationEvidence := make(map[string]struct{}, len(sourceExecution.Integration.CheckRunIDs))
	for _, id := range sourceExecution.Integration.CheckRunIDs {
		integrationEvidence[id] = struct{}{}
	}
	requiredByName := make(map[string]core.ComplexExecutionCheckSpecFact)
	integrationByName := make(map[string]core.ComplexExecutionCheckSpecFact)
	for _, spec := range sourceExecution.CheckSpecs {
		if spec.ExecutionRunID != sourceExecution.Run.ID {
			return nil, nil, errors.New("quick execution source check has a mismatched run")
		}
		switch {
		case spec.Kind == core.CandidateCheckRequired && spec.ComplexExecutionTaskID == completedTask.ID:
			if _, duplicate := requiredByName[spec.CheckID]; duplicate || !trustedComplexQuickSourceSpec(spec, runs, requiredEvidence, verification.CandidateCommitID, verification.CandidateCommitSHA) {
				return nil, nil, errors.New("quick execution source required check is not trusted")
			}
			requiredByName[spec.CheckID] = spec
		case spec.Kind == core.CandidateCheckIntegration:
			if spec.ComplexExecutionTaskID != "" {
				return nil, nil, errors.New("quick execution source integration check has a task binding")
			}
			if _, duplicate := integrationByName[spec.CheckID]; duplicate || !trustedComplexQuickSourceSpec(spec, runs, integrationEvidence, "", sourceExecution.Integration.CandidateCommitSHA) {
				return nil, nil, errors.New("quick execution source integration check is not trusted")
			}
			integrationByName[spec.CheckID] = spec
		}
	}
	required, err := cloneComplexQuickSourceSpecs(sourceTask.RequiredCheckIDs, requiredByName, runID, taskID, at, newID)
	if err != nil {
		return nil, nil, err
	}
	integration, err := cloneComplexQuickSourceSpecs(plan.IntegrationCheckIDs, integrationByName, runID, "", at, newID)
	if err != nil {
		return nil, nil, err
	}
	return required, integration, nil
}

func trustedComplexQuickSourceSpec(spec core.ComplexExecutionCheckSpecFact, runs map[string]core.ComplexExecutionCheckRun, evidence map[string]struct{}, candidateID, candidateSHA string) bool {
	if strings.TrimSpace(spec.CheckID) == "" || len(spec.Argv) == 0 || spec.TimeoutSeconds <= 0 {
		return false
	}
	argv, err := json.Marshal(spec.Argv)
	if err != nil || coreDigest(argv) != spec.CheckSpecSHA256 {
		return false
	}
	for id := range evidence {
		run, ok := runs[id]
		if !ok || run.CheckSpecFactID != spec.ID || run.Status != core.ComplexExecutionCheckRunSettled || run.Result != core.EvidenceResultPass || run.CandidateCommitSHA != candidateSHA {
			continue
		}
		if candidateID == "" || run.CandidateCommitID == candidateID {
			return true
		}
	}
	return false
}

func cloneComplexQuickSourceSpecs(names []string, source map[string]core.ComplexExecutionCheckSpecFact, runID, taskID string, at time.Time, newID func() string) ([]core.ComplexExecutionCheckSpecFact, error) {
	out := make([]core.ComplexExecutionCheckSpecFact, 0, len(names))
	for _, name := range names {
		spec, ok := source[name]
		if !ok {
			return nil, errors.New("quick execution source check set is incomplete")
		}
		out = append(out, core.ComplexExecutionCheckSpecFact{
			ID: newID(), ExecutionRunID: runID, ComplexExecutionTaskID: taskID,
			CheckID: spec.CheckID, Kind: spec.Kind, CheckSpecSHA256: spec.CheckSpecSHA256,
			Argv: append([]string(nil), spec.Argv...), TimeoutSeconds: spec.TimeoutSeconds, CreatedAt: at,
		})
	}
	if len(source) != len(names) {
		return nil, errors.New("quick execution source check set has unexpected entries")
	}
	return out, nil
}

func sameQuickCheckNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func (s *Service) ensureComplexQuickSteward(ctx context.Context, snapshot core.ComplexQuickSnapshot, projectID string) (bool, bool, error) {
	binding := activeComplexExecutionSteward(quickSnapshotAsExecution(snapshot))
	if binding.ID == "" || binding.Status != core.RoleBindingStatusRequested || binding.SourceComplexRoleBindingID == "" {
		return false, true, errComplexExecutionStopped
	}
	session, blocked, err := s.spawnControlledChatSession(ctx, snapshot.Run.DevelopmentRequirementID, binding.ID, ports.SpawnConfig{ProjectID: domain.ProjectID(projectID), Kind: domain.KindOrchestrator, Prompt: "", RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto}, DisplayName: "ClearDev Complex Quick Steward Continuation", CreationIdempotencyKey: binding.SessionCreationIdempotencyKey})
	if blocked {
		return false, false, nil
	}
	if err != nil || !s.validComplexExecutionSteward(ctx, session.SessionRecord, projectID) {
		_, _ = s.complexExecution.FailClearDevComplexQuickRoleBinding(ctx, binding.ID, core.ReasonCode("STEWARD_UNAVAILABLE"), s.now().UTC())
		return true, true, errComplexExecutionStopped
	}
	if _, err = s.complexExecution.BindClearDevComplexQuickRoleBinding(ctx, binding.ID, string(session.ID), session.Metadata.WorkspacePath, "", s.now().UTC()); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (s *Service) ensureComplexQuickBuilder(ctx context.Context, snapshot core.ComplexQuickSnapshot, projectID string) (bool, bool, error) {
	var binding core.ComplexExecutionRoleBinding
	for _, item := range snapshot.RoleBindings {
		if item.Role == core.StandardRoleBuilder {
			binding = item
		}
	}
	if binding.ID == "" {
		return false, true, errComplexExecutionStopped
	}
	if binding.Status == core.RoleBindingStatusFailed || binding.Status == core.RoleBindingStatusEnded {
		return false, true, errComplexExecutionStopped
	}
	if binding.Status == core.RoleBindingStatusBound {
		record, found, err := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
		if err != nil {
			return false, false, err
		}
		baseValid := binding.BaseCommitSHA == record.Metadata.DiffBaseSHA || binding.BaseCommitSHA == snapshot.Run.IntegrationBaseSHA
		if !found || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexExecutionWorker(ctx, record, binding, projectID) || !validComplexExecutionCommitSHA(record.Metadata.DiffBaseSHA) || !baseValid {
			changed, endErr := s.complexExecution.EndClearDevComplexQuickRoleBinding(ctx, binding.ID, core.ReasonBuilderSpawnFailed, s.now().UTC())
			return changed, true, endErr
		}
		if preflightErr := s.prepareCandidateCheckEnvironment(ctx, snapshot.Run.ID+":check-preflight", record.Metadata.WorkspacePath, snapshot.Run.IntegrationBaseSHA, core.StandardCandidateCheckImage, complexCheckArgv(snapshot.CheckSpecs)); preflightErr != nil {
			s.logger.Error("ClearDev QUICK check preflight failed", "executionRunID", snapshot.Run.ID, "error", preflightErr)
			changed, endErr := s.complexExecution.EndClearDevComplexQuickRoleBinding(ctx, binding.ID, core.ReasonCode("CHECKER_UNAVAILABLE"), s.now().UTC())
			return changed, true, endErr
		}
		return false, false, nil
	}
	if binding.Status != core.RoleBindingStatusRequested {
		return false, true, errComplexExecutionStopped
	}
	branch := "cleardev-complex-builder-quick-" + snapshot.Run.ID
	session, blocked, err := s.spawnControlledChatSession(ctx, snapshot.Run.DevelopmentRequirementID, binding.ID, ports.SpawnConfig{ProjectID: domain.ProjectID(projectID), Kind: domain.KindWorker, Branch: branch, Prompt: "", RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto}, DisplayName: "ClearDev Complex Quick Builder", CreationIdempotencyKey: binding.SessionCreationIdempotencyKey})
	if blocked {
		return false, false, nil
	}
	if err != nil || !s.validComplexExecutionWorker(ctx, session.SessionRecord, binding, projectID) || strings.Contains(branch, "cleardev-review-") {
		_, _ = s.complexExecution.FailClearDevComplexQuickRoleBinding(ctx, binding.ID, core.ReasonBuilderSpawnFailed, s.now().UTC())
		return true, true, errComplexExecutionStopped
	}
	if !validComplexExecutionCommitSHA(session.Metadata.DiffBaseSHA) {
		_, _ = s.complexExecution.FailClearDevComplexQuickRoleBinding(ctx, binding.ID, core.ReasonBuilderSpawnFailed, s.now().UTC())
		return true, true, errComplexExecutionStopped
	}
	base := snapshot.Run.IntegrationBaseSHA
	if session.Metadata.DiffBaseSHA != base {
		if err := s.inspector.PrepareBaseWorkspace(ctx, session.Metadata.WorkspacePath, session.Metadata.DiffBaseSHA, base); err != nil {
			_, _ = s.complexExecution.FailClearDevComplexQuickRoleBinding(ctx, binding.ID, core.ReasonBuilderSpawnFailed, s.now().UTC())
			return true, true, errComplexExecutionStopped
		}
	}
	if preflightErr := s.prepareCandidateCheckEnvironment(ctx, snapshot.Run.ID+":check-preflight", session.Metadata.WorkspacePath, base, core.StandardCandidateCheckImage, complexCheckArgv(snapshot.CheckSpecs)); preflightErr != nil {
		s.logger.Error("ClearDev QUICK check preflight failed", "executionRunID", snapshot.Run.ID, "error", preflightErr)
		_, _ = s.complexExecution.FailClearDevComplexQuickRoleBinding(ctx, binding.ID, core.ReasonCode("CHECKER_UNAVAILABLE"), s.now().UTC())
		return true, true, errComplexExecutionStopped
	}
	if _, err = s.complexExecution.BindClearDevComplexQuickRoleBinding(ctx, binding.ID, string(session.ID), session.Metadata.WorkspacePath, base, s.now().UTC()); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (s *Service) dispatchComplexQuickBuilder(ctx context.Context, snapshot core.ComplexQuickSnapshot) (bool, bool, error) {
	if snapshot.Task == nil {
		return false, true, errComplexExecutionStopped
	}
	task := *snapshot.Task
	if task.ExecutionPackageJSON == "" || task.ExecutionPackageSHA256 == "" || coreDigest([]byte(task.ExecutionPackageJSON)) != task.ExecutionPackageSHA256 {
		return false, true, errComplexExecutionStopped
	}
	if _, err := core.ParseComplexQuickExecutionPackage([]byte(task.ExecutionPackageJSON)); err != nil {
		return false, true, errComplexExecutionStopped
	}
	if task.Status != core.DevelopmentTaskStatusPlanned && task.Status != core.DevelopmentTaskStatusRework {
		return false, false, nil
	}
	if task.ReworkCount > core.ComplexStandardMaxReworkCount {
		return false, true, errComplexExecutionStopped
	}
	now := s.now().UTC()
	dispatchID, stepID := s.newID(), s.newID()
	base := snapshot.Run.IntegrationBaseSHA
	feedback := complexExecutionReworkFeedback(quickSnapshotAsExecution(snapshot), task)
	prompt := complexExecutionBuilderPrompt([]byte(task.ExecutionPackageJSON), dispatchID, task.DevelopmentTaskID, task.CurrentRound, feedback)
	builderID := snapshot.Run.BuilderRoleBindingID
	step := core.AgentStep{ID: stepID, RoleBindingID: builderID, Kind: core.ComplexExecutionAgentStepBuilderTask, RequestID: dispatchID, ClientMessageID: "cleardev-complex-quick-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
	dispatch := core.ComplexExecutionDispatch{ID: dispatchID, ExecutionRunID: snapshot.Run.ID, ComplexExecutionTaskID: task.ID, DevelopmentTaskID: task.DevelopmentTaskID, Round: task.CurrentRound, BaseCommitSHA: base, AgentStepID: stepID, ExecutionPackageSHA256: task.ExecutionPackageSHA256, ClientMessageID: step.ClientMessageID, Status: core.ComplexExecutionDispatchPending, CreatedAt: now}
	_, _, err := s.complexExecution.CreateClearDevComplexQuickDispatch(ctx, core.CreateComplexExecutionDispatchCommand{Dispatch: dispatch, AgentStep: step})
	return err == nil, false, err
}

func (s *Service) advanceComplexQuickDispatch(ctx context.Context, snapshot core.ComplexQuickSnapshot, projectID string) (bool, bool, error) {
	execution := quickSnapshotAsExecution(snapshot)
	task, dispatch, ok := activeComplexQuickDispatch(snapshot)
	if !ok {
		return false, true, errComplexExecutionStopped
	}
	builder, ok := complexExecutionBindingByID(execution, snapshot.Run.BuilderRoleBindingID)
	if !ok || builder.Status != core.RoleBindingStatusBound {
		return false, true, errComplexExecutionStopped
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil {
		return false, false, err
	}
	if !found || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexExecutionWorker(ctx, record, builder, projectID) {
		return s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, true, core.ReasonBuilderSpawnFailed)
	}
	step, ok := complexExecutionStepByID(execution, dispatch.AgentStepID)
	if !ok || step.RoleBindingID != builder.ID || step.Kind != core.ComplexExecutionAgentStepBuilderTask || step.RequestID != dispatch.ID {
		return false, true, errComplexExecutionStopped
	}
	feedback := complexExecutionReworkFeedback(execution, task)
	prompt := complexExecutionBuilderPrompt([]byte(task.ExecutionPackageJSON), dispatch.ID, task.DevelopmentTaskID, dispatch.Round, feedback)
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return false, true, errComplexExecutionStopped
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if preflightErr := s.validateComplexQuickDispatchWorkspace(ctx, snapshot, task, dispatch, record.Metadata.WorkspacePath); preflightErr != nil {
			return s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, errors.Is(preflightErr, ports.ErrClearDevGitUnavailable), gitInspectionReason(preflightErr))
		}
		if err := s.relayAgentTurn(ctx, snapshot.Run.DevelopmentRequirementID, core.AgentStepCategoryQuickExecution, step, builder.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return false, false, err
		}
		_, err = s.complexExecution.MarkClearDevComplexQuickAgentStepSent(ctx, step.ID, s.now().UTC())
		return err == nil, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		validate := func(raw []byte) error {
			_, err := core.ParseComplexExecutionBuilderResult(raw, dispatch.ID, task.DevelopmentTaskID, dispatch.Round)
			return err
		}
		fail := func(ctx context.Context, item core.AgentStep, reason core.ReasonCode) error {
			if reason != core.ReasonCode("BUILDER_RESULT_INVALID") {
				return errors.New("quick builder observation failed: " + string(reason))
			}
			_, _, err := s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, false, reason)
			return err
		}
		message, stopped, err := s.awaitValidAgentJSON(ctx, snapshot.Run.DevelopmentRequirementID, core.AgentStepCategoryQuickExecution, builder.AOSessionID, step, prompt, validate, standardStepReasons{
			Invalid: "BUILDER_RESULT_INVALID", Timeout: "BUILDER_TIMEOUT", Unavailable: "BUILDER_UNAVAILABLE",
		}, fail, errComplexExecutionStopped)
		if err != nil || stopped {
			return false, stopped, err
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, message.TurnID, message.MessageID, message.Text, coreDigest([]byte(message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexQuickAgentStep(ctx, step)
		return err == nil, false, err
	}
	if step.SendStatus != core.AgentStepSendStatusSettled {
		return s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, true, core.ReasonCode("BUILDER_STEP_UNAVAILABLE"))
	}
	result, resultErr := core.ParseComplexExecutionBuilderResult([]byte(step.FinalMessageText), dispatch.ID, task.DevelopmentTaskID, dispatch.Round)
	if resultErr != nil {
		return s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, false, core.ReasonCode("BUILDER_RESULT_INVALID"))
	}
	if result.Outcome == "BLOCKED" {
		return s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, false, core.ReasonCode("BUILDER_BLOCKED"))
	}
	if result.Outcome == "NEEDS_HUMAN" {
		return s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, false, core.ReasonHumanDecisionRequired)
	}
	if dispatch.CandidateCommitID == "" {
		inspection, inspectErr := s.inspector.InspectCandidate(ctx, record.Metadata.WorkspacePath, dispatch.BaseCommitSHA)
		if inspectErr != nil || inspection.BaseSHA != dispatch.BaseCommitSHA || !validComplexExecutionCommitSHA(inspection.CandidateSHA) {
			if inspectErr == nil {
				inspectErr = ports.ErrClearDevCandidateInvalid
			}
			return s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, errors.Is(inspectErr, ports.ErrClearDevGitUnavailable), gitInspectionReason(inspectErr))
		}
		_, err = s.complexExecution.AppendClearDevComplexQuickCandidate(ctx, core.AppendComplexExecutionCandidateCommand{ExecutionRunID: snapshot.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, Round: dispatch.Round, BaseCommitSHA: dispatch.BaseCommitSHA, Candidate: core.CandidateObservation{ID: s.newID(), DevelopmentTaskID: task.DevelopmentTaskID, AOSessionID: builder.AOSessionID, CommitSHA: inspection.CandidateSHA, ObservedAt: s.now().UTC()}})
		return err == nil, false, err
	}
	inspection, inspectErr := s.inspector.InspectCandidate(ctx, record.Metadata.WorkspacePath, dispatch.BaseCommitSHA)
	if inspectErr != nil || inspection.BaseSHA != dispatch.BaseCommitSHA || inspection.CandidateSHA != dispatch.CandidateCommitSHA {
		if inspectErr == nil {
			inspectErr = ports.ErrClearDevCandidateInvalid
		}
		return s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, errors.Is(inspectErr, ports.ErrClearDevGitUnavailable), gitInspectionReason(inspectErr))
	}
	pkg, packageErr := core.ParseComplexQuickExecutionPackage([]byte(task.ExecutionPackageJSON))
	if packageErr != nil {
		return false, true, errComplexExecutionStopped
	}
	if changed, err := s.ensureComplexQuickScopeCheck(ctx, snapshot, task, dispatch, inspection, pkg); err != nil || changed {
		return changed, false, err
	}
	for _, spec := range complexRequiredCheckSpecs(execution, task.ID) {
		if changed, err := s.ensureComplexQuickRequiredCheck(ctx, snapshot, task, dispatch, record.Metadata.WorkspacePath, spec); err != nil || changed {
			return changed, false, err
		}
	}
	for _, spec := range snapshot.CheckSpecs {
		if spec.Kind != core.CandidateCheckIntegration {
			continue
		}
		if changed, err := s.ensureComplexQuickRequiredCheck(ctx, snapshot, task, dispatch, record.Metadata.WorkspacePath, spec); err != nil || changed {
			return changed, false, err
		}
	}
	return s.completeComplexQuickIfReady(ctx, snapshot, projectID)
}

func (s *Service) ensureComplexQuickScopeCheck(ctx context.Context, snapshot core.ComplexQuickSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, inspection ports.ClearDevCandidateInspection, pkg core.ComplexStandardExecutionPackage) (bool, error) {
	execution := quickSnapshotAsExecution(snapshot)
	spec, ok := complexCheckSpec(execution, task.ID, core.CandidateCheckScope, "SCOPE")
	if !ok {
		return false, errComplexExecutionStopped
	}
	run, found := complexCheckRun(execution, dispatch.ID, dispatch.CandidateCommitID, spec.ID)
	if !found {
		run = core.ComplexExecutionCheckRun{ID: s.newID(), ExecutionRunID: snapshot.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: dispatch.CandidateCommitID, BaseCommitSHA: dispatch.BaseCommitSHA, CandidateCommitSHA: dispatch.CandidateCommitSHA, CheckSpecFactID: spec.ID, Kind: core.CandidateCheckScope, Argv: []string{}, Status: core.ComplexExecutionCheckRunPending, CreatedAt: s.now().UTC()}
		_, _, err := s.complexExecution.CreateClearDevComplexQuickCheckRun(ctx, run)
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunPending {
		_, err := s.complexExecution.StartClearDevComplexQuickCheckRun(ctx, run.ID, s.now().UTC())
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunStarted {
		passed, summary := complexQuickScopePathsPass(pkg, inspection.Paths)
		result, code, reason := core.EvidenceResultPass, 0, core.ReasonNone
		if !passed {
			result, code, reason = core.EvidenceResultFail, 1, core.ReasonCode("SCOPE_CHECK_FAILED")
		}
		paths, _ := json.Marshal(inspection.Paths)
		_, err := s.complexExecution.SettleClearDevComplexQuickCheckRun(ctx, core.SettleComplexExecutionCheckCommand{CheckRunID: run.ID, Result: result, ReasonCode: reason, ExitCode: &code, OutputSummary: summary, OutputSHA256: coreDigest([]byte(summary)), ChangedPathsJSON: string(paths), At: s.now().UTC()})
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunFailed || run.Result == core.EvidenceResultFail {
		changed, _, err := s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, run.Status == core.ComplexExecutionCheckRunFailed, run.ReasonCode)
		return changed, err
	}
	return false, nil
}

func (s *Service) ensureComplexQuickRequiredCheck(ctx context.Context, snapshot core.ComplexQuickSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, workspace string, spec core.ComplexExecutionCheckSpecFact) (bool, error) { //nolint:dupl // same checker settlement as STANDARD, independent store
	execution := quickSnapshotAsExecution(snapshot)
	run, found := complexCheckRun(execution, dispatch.ID, dispatch.CandidateCommitID, spec.ID)
	if !found {
		run = core.ComplexExecutionCheckRun{ID: s.newID(), ExecutionRunID: snapshot.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: dispatch.CandidateCommitID, BaseCommitSHA: dispatch.BaseCommitSHA, CandidateCommitSHA: dispatch.CandidateCommitSHA, CheckSpecFactID: spec.ID, Kind: spec.Kind, Argv: append([]string(nil), spec.Argv...), Status: core.ComplexExecutionCheckRunPending, CreatedAt: s.now().UTC()}
		_, _, err := s.complexExecution.CreateClearDevComplexQuickCheckRun(ctx, run)
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunPending {
		_, err := s.complexExecution.StartClearDevComplexQuickCheckRun(ctx, run.ID, s.now().UTC())
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunStarted {
		result, runErr := s.checks.RunCandidateCheck(ctx, complexCandidateCheckRequest(run.ID, workspace, dispatch.CandidateCommitSHA, spec))
		_, err := s.complexExecution.SettleClearDevComplexQuickCheckRun(ctx, complexSettleCheckCommand(run.ID, result, runErr, s.now().UTC()))
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunFailed || run.Result == core.EvidenceResultFail {
		changed, _, err := s.failComplexQuickDispatch(ctx, snapshot, task, dispatch, run.Status == core.ComplexExecutionCheckRunFailed, run.ReasonCode)
		return changed, err
	}
	return false, nil
}

func (s *Service) completeComplexQuickIfReady(ctx context.Context, snapshot core.ComplexQuickSnapshot, projectID string) (bool, bool, error) {
	fresh, found, err := s.complexExecution.GetClearDevComplexQuickExecution(ctx, snapshot.Run.DevelopmentRequirementID)
	if err != nil || !found {
		return false, !found, err
	}
	task, dispatch, ok := activeComplexQuickDispatch(fresh)
	if !ok || dispatch.CandidateCommitID == "" {
		return false, true, errComplexExecutionStopped
	}
	execution := quickSnapshotAsExecution(fresh)
	if _, ok := complexCheckRunPassed(execution, dispatch, core.CandidateCheckScope); !ok {
		return false, false, nil
	}
	for _, spec := range complexRequiredCheckSpecs(execution, task.ID) {
		run, found := complexCheckRun(execution, dispatch.ID, dispatch.CandidateCommitID, spec.ID)
		if !found || run.Status != core.ComplexExecutionCheckRunSettled || run.Result != core.EvidenceResultPass {
			return false, false, nil
		}
	}
	checkIDs := []string{}
	for _, spec := range fresh.CheckSpecs {
		if spec.Kind != core.CandidateCheckIntegration {
			continue
		}
		run, found := complexCheckRun(execution, dispatch.ID, dispatch.CandidateCommitID, spec.ID)
		if !found || run.Status != core.ComplexExecutionCheckRunSettled || run.Result != core.EvidenceResultPass {
			return false, false, nil
		}
		checkIDs = append(checkIDs, run.ID)
	}
	if len(checkIDs) == 0 {
		return false, true, errComplexExecutionStopped
	}
	builder, ok := complexExecutionBindingByID(execution, fresh.Run.BuilderRoleBindingID)
	if !ok {
		return false, true, errComplexExecutionStopped
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil || !found || !s.validComplexExecutionWorker(ctx, record, builder, projectID) {
		return false, false, err
	}
	inspection, inspectErr := s.inspector.InspectCandidate(ctx, record.Metadata.WorkspacePath, dispatch.BaseCommitSHA)
	if inspectErr != nil || inspection.CandidateSHA != dispatch.CandidateCommitSHA {
		return s.failComplexQuickDispatch(ctx, fresh, task, dispatch, inspectErr != nil && errors.Is(inspectErr, ports.ErrClearDevGitUnavailable), gitInspectionReason(inspectErr))
	}
	err = s.complexExecution.CompleteClearDevComplexQuickExecution(ctx, core.CompleteComplexQuickExecutionCommand{
		RunID:       fresh.Run.ID,
		Integration: core.ComplexExecutionIntegration{ID: s.newID(), ExecutionRunID: fresh.Run.ID, IntegrationCandidateID: "s08-integration-" + fresh.Run.ID, CandidateCommitSHA: dispatch.CandidateCommitSHA, CheckRunIDs: checkIDs, CompletedAt: s.now().UTC()},
		At:          s.now().UTC(),
	})
	if err != nil {
		return false, false, err
	}
	if err := s.releaseCandidateCheckEnvironment(ctx, fresh.Run.ID+":check-preflight"); err != nil {
		return false, false, err
	}
	return true, true, nil
}

func (s *Service) failComplexQuickDispatch(ctx context.Context, snapshot core.ComplexQuickSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, infrastructure bool, reason core.ReasonCode) (bool, bool, error) {
	if reason == core.ReasonNone {
		reason = core.ReasonCode("COMPLEX_EXECUTION_FAILED")
	}
	err := s.complexExecution.ApplyClearDevComplexQuickFailure(ctx, snapshot.Run.ID, task.ID, dispatch.ID, infrastructure, reason, s.now().UTC())
	return err == nil, false, err
}

func (s *Service) validateComplexQuickDispatchWorkspace(ctx context.Context, snapshot core.ComplexQuickSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, workspace string) error { //nolint:dupl // same Git preflight as STANDARD
	expectedHead := dispatch.BaseCommitSHA
	if dispatch.Round > 0 {
		found := false
		for _, previous := range snapshot.Dispatches {
			if previous.ComplexExecutionTaskID == task.ID && previous.Round == dispatch.Round-1 && validComplexExecutionCommitSHA(previous.CandidateCommitSHA) {
				expectedHead, found = previous.CandidateCommitSHA, true
				break
			}
		}
		if !found {
			return ports.ErrClearDevCandidateInvalid
		}
	}
	inspection, err := s.inspector.InspectCandidate(ctx, workspace, expectedHead)
	if err != nil {
		return err
	}
	if inspection.BaseSHA != expectedHead || inspection.CandidateSHA != expectedHead || len(inspection.Paths) != 0 {
		return ports.ErrClearDevCandidateInvalid
	}
	return nil
}

func parseQuickStewardRequest(raw []byte, requestID string, version core.RequirementVersion, integrationSHA string, taskSetVersion int64) (core.ComplexQuickTaskRequestResult, error) {
	var preview struct {
		SourceTaskKey string `json:"sourceTaskKey"`
	}
	if err := json.Unmarshal(raw, &preview); err != nil {
		return core.ComplexQuickTaskRequestResult{}, err
	}
	return core.ParseComplexQuickTaskRequestResult(raw, requestID, version.ID, version.SHA256, preview.SourceTaskKey, integrationSHA, taskSetVersion)
}

func complexQuickSourceTaskDone(source core.ComplexExecutionSnapshot, key string) bool {
	for _, task := range source.Tasks {
		if task.TaskKey == key && task.Status == core.DevelopmentTaskStatusDone {
			return true
		}
	}
	return false
}

func quickSnapshotAsExecution(snapshot core.ComplexQuickSnapshot) core.ComplexExecutionSnapshot {
	execution := core.ComplexExecutionSnapshot{
		Run: core.ComplexExecutionRun{
			ID: snapshot.Run.ID, DevelopmentRequirementID: snapshot.Run.DevelopmentRequirementID, RequirementVersionID: snapshot.Run.RequirementVersionID,
			RequirementSHA256: snapshot.Run.RequirementSHA256, PlanID: snapshot.Run.PlanID, PlanSHA256: snapshot.Run.PlanSHA256,
			Mode: snapshot.Run.Mode, BuilderRoleBindingID: snapshot.Run.BuilderRoleBindingID, StewardRoleBindingID: snapshot.Run.StewardRoleBindingID,
		},
		RoleBindings: snapshot.RoleBindings, AgentSteps: snapshot.AgentSteps, Dispatches: snapshot.Dispatches,
		CheckSpecs: snapshot.CheckSpecs, CheckRuns: snapshot.CheckRuns, Integration: snapshot.Integration,
	}
	if snapshot.Task != nil {
		execution.Tasks = []core.ComplexExecutionTask{*snapshot.Task}
	}
	return execution
}

func activeComplexQuickDispatch(snapshot core.ComplexQuickSnapshot) (core.ComplexExecutionTask, core.ComplexExecutionDispatch, bool) {
	if snapshot.Task == nil || snapshot.Task.CurrentDispatchID == "" {
		return core.ComplexExecutionTask{}, core.ComplexExecutionDispatch{}, false
	}
	for _, dispatch := range snapshot.Dispatches {
		if dispatch.ID == snapshot.Task.CurrentDispatchID && dispatch.ComplexExecutionTaskID == snapshot.Task.ID {
			return *snapshot.Task, dispatch, true
		}
	}
	return core.ComplexExecutionTask{}, core.ComplexExecutionDispatch{}, false
}

func complexCheckRunPassed(snapshot core.ComplexExecutionSnapshot, dispatch core.ComplexExecutionDispatch, kind core.CandidateCheckKind) (core.ComplexExecutionCheckRun, bool) {
	for _, run := range snapshot.CheckRuns {
		if run.DispatchID == dispatch.ID && run.CandidateCommitID == dispatch.CandidateCommitID && run.Kind == kind && run.Status == core.ComplexExecutionCheckRunSettled && run.Result == core.EvidenceResultPass {
			return run, true
		}
	}
	return core.ComplexExecutionCheckRun{}, false
}

func complexQuickScopePathsPass(pkg core.ComplexStandardExecutionPackage, paths []ports.ClearDevDiffPath) (bool, string) {
	rules := core.PathRules{WritePaths: pkg.WritePaths, GeneratedPaths: pkg.GeneratedPaths, SharedPathsRequireApproval: pkg.SharedPathsRequireApproval, ForbiddenPaths: pkg.ForbiddenPaths}
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

func uniqueQuickFollowUpPaths(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
