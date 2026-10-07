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
)

var errComplexExecutionStopped = errors.New("ClearDev complex execution reached a durable stop")

// StartComplexStandardExecution persists only a Steward request. The store
// rechecks every S05/S04 gate in its transaction; this method never accepts a
// caller-selected task, mode, path, command, session, or candidate.
func (s *Service) StartComplexStandardExecution(ctx context.Context, requirementID string) (RequirementView, error) {
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
	if s.facts == nil || s.complex == nil || s.complexExecution == nil || s.direction == nil || s.ao == nil || s.sessions == nil || s.chat == nil || s.inspector == nil || s.checks == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_EXECUTION_UNAVAILABLE", "ClearDev complex execution is not fully configured")
	}
	snapshot, found, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_REQUIREMENT_READ_FAILED", "Could not read the ClearDev requirement")
	}
	if !found {
		return RequirementView{}, apierr.NotFound("CLEARDEV_REQUIREMENT_NOT_FOUND", "ClearDev requirement was not found")
	}
	benchmarkBinding, benchmarkBound, benchmarkErr := s.benchmarkBindingForRequirement(ctx, snapshot.Requirement)
	if benchmarkErr != nil {
		return RequirementView{}, benchmarkErr
	}
	planning, found, err := s.complex.GetClearDevComplexPlanning(ctx, requirementID)
	if err != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_PLANNING_READ_FAILED", "Could not read the ClearDev complex planning facts")
	}
	if !found {
		return RequirementView{}, apierr.Conflict(string(core.ReasonComplexPlanRequired), "Complex execution requires a reviewed complex plan", nil)
	}
	version, ok := currentConfirmedVersion(snapshot)
	if !ok || snapshot.Requirement.CancelledAt != nil {
		return RequirementView{}, apierr.Conflict(string(core.ReasonPreconditionNotMet), "Complex execution requires the current confirmed requirement", nil)
	}
	plan, review, ok := approvedComplexExecutionPlan(planning, version.ID)
	if !ok {
		return RequirementView{}, apierr.Conflict(string(core.ReasonComplexExecutionPlanNotReady), "Complex execution requires an APPROVED current plan", nil)
	}
	parsedPlan, parsePlanErr := parseComplexExecutionPlan(plan)
	contractPlan := parsedPlan.SchemaVersion == core.PlannerTaskContractVersion
	if parsePlanErr != nil {
		return RequirementView{}, apierr.Conflict("COMPLEX_EXECUTION_PLAN_INVALID", "The approved complex plan cannot be executed", nil)
	}
	existing, exists, existingErr := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if existingErr != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_EXECUTION_READ_FAILED", "Could not read the existing complex execution")
	}
	quick, quickExists, quickErr := s.complexExecution.GetClearDevComplexQuickExecution(ctx, requirementID)
	if quickErr != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_QUICK_READ_FAILED", "Could not read the existing quick execution")
	}
	if quickExists {
		_ = quick
		if s.autoAdvanceComplexPlans && !benchmarkBound {
			return s.GetRequirement(ctx, requirementID)
		}
		s.scheduleComplexStandardExecution(requirementID)
		return s.GetRequirement(ctx, requirementID)
	}
	if exists {
		if existing.Run.CompletedAt == nil && sameComplexExecutionRequest(existing, version, plan, review) {
			s.scheduleComplexStandardExecution(requirementID)
			return s.GetRequirement(ctx, requirementID)
		}
		if existing.Run.CompletedAt != nil && version.TaskSetVersion == existing.Run.TaskSetVersion && existing.Integration != nil && validComplexExecutionCommitSHA(existing.Integration.CandidateCommitSHA) && sameComplexExecutionRequest(existing, version, plan, review) {
			if s.autoAdvanceComplexPlans && !benchmarkBound {
				return s.GetRequirement(ctx, requirementID)
			}
			if benchmarkBound && benchmarkBinding.Policy == core.BenchmarkModeStandardOnly {
				return RequirementView{}, apierr.Conflict("BENCHMARK_QUICK_FORBIDDEN", "G3 benchmark bindings cannot start QUICK", nil)
			}
			return s.startComplexQuickExecution(ctx, requirementID, planning, version, plan, review, existing)
		}
		if sameComplexExecutionRequest(existing, version, plan, review) {
			s.scheduleComplexStandardExecution(requirementID)
			return s.GetRequirement(ctx, requirementID)
		}
		if existing.Run.RequirementVersionID == version.ID {
			return RequirementView{}, apierr.Conflict(string(core.ReasonComplexExecutionTaskSetChange), "The existing complex execution no longer matches the current approved plan", nil)
		}
		// A leftover run from a superseded version must not block the current
		// confirmed plan. The store keys runs by (version, plan).
	}
	if version.TaskSetVersion != 0 {
		return RequirementView{}, apierr.Conflict(string(core.ReasonPreconditionNotMet), "Complex execution requires an unused current task set", nil)
	}
	steward, ok := core.ComplexRoleBindingByRole(planning, core.StandardRoleSteward)
	if !ok {
		return RequirementView{}, apierr.Conflict("STEWARD_BINDING_MISSING", "The approved complex plan has no Project Steward binding", nil)
	}
	now := s.now().UTC()
	runID := s.newID()
	selection, selectionErr := s.selectComplexModeForRequirement(ctx, snapshot.Requirement, parsedPlan.Tasks, parsedPlan.ParallelSuggestion.RecommendedBuilderCount)
	runPackage, runPackageSHA, packageErr := core.BuildComplexExecutionRunPackage(selection.Mode, runID, version.ID, version.SHA256, plan.ID, plan.PlanSHA256)
	if parsePlanErr != nil || selectionErr != nil {
		return RequirementView{}, apierr.Conflict("COMPLEX_EXECUTION_PLAN_INVALID", "The approved complex plan cannot be executed", nil)
	}
	if packageErr != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_EXECUTION_PACKAGE_INVALID", "Could not bind the immutable complex execution package")
	}
	mailBase, mailPolicy, policyErr := s.mailRequirementBaseline(ctx, snapshot.Requirement)
	if policyErr != nil {
		return RequirementView{}, policyErr
	}
	if s.autoAdvanceComplexPlans && !benchmarkBound && !mailPolicy && !contractPlan && (len(parsedPlan.Tasks) != 1 || parsedPlan.ParallelSuggestion.RecommendedBuilderCount != 1) {
		return RequirementView{}, apierr.Conflict("COMPLEX_AUTO_SINGLE_TASK_REQUIRED", "Automatic non-mail execution requires one reviewed task and one Builder", nil)
	}
	if mailPolicy {
		policy := core.MailDeliveryPolicyV1
		bindPolicy := core.BindMailDeliveryPolicy
		if s.boundedMailAttempts {
			policy, bindPolicy = core.MailDeliveryPolicyV2, core.BindMailDeliveryPolicyV2
		}
		// A legacy installation without the bounded attempt ledger retains
		// V1's single-task contract; it must not create a partial V2 run.
		if err := core.ValidateMailPlanForPolicy(policy, parsedPlan); err != nil {
			return RequirementView{}, err
		}
		runPackage, runPackageSHA, packageErr = bindPolicy(runPackage, mailBase)
		if packageErr == nil && s.boundedMailAttempts {
			runPackage, runPackageSHA, packageErr = core.BindMailAttemptPolicy(runPackage)
			if _, supported := s.inspector.(ports.ClearDevMailCandidateFreezer); supported && packageErr == nil {
				runPackage, runPackageSHA, packageErr = core.BindMailFreezePolicy(runPackage)
			}
		}
		if packageErr != nil {
			return RequirementView{}, packageErr
		}
	}
	if s.finalReviews != nil && !benchmarkBound {
		if len(parsedPlan.Tasks) < 1 || len(parsedPlan.Tasks) > 3 || selection.BuilderCount < 1 || selection.BuilderCount > 2 {
			return RequirementView{}, apierr.Conflict("REQUIREMENT_FINAL_REVIEW_BOUNDED_EXECUTION_REQUIRED", "Requirement final review admits 1-3 tasks and at most two Builders", nil)
		}
		runPackage, runPackageSHA, packageErr = core.BindRequirementFinalReviewPolicy(runPackage)
		if packageErr != nil {
			return RequirementView{}, packageErr
		}
	}
	if contractPlan {
		runPackage, runPackageSHA, packageErr = core.BindPlannerTaskContractPolicy(runPackage)
		if packageErr != nil {
			return RequirementView{}, packageErr
		}
		if s.plannerRuntimeCoordination && !benchmarkBound {
			if _, supported := s.complexExecution.(PlannerRuntimeFactStore); !supported {
				return RequirementView{}, apierr.Internal("PLANNER_RUNTIME_STORE_UNAVAILABLE", "Planner runtime coordination requires its transactional fact store")
			}
			runPackage, runPackageSHA, packageErr = core.BindPlannerRuntimePolicy(runPackage)
			if packageErr != nil {
				return RequirementView{}, packageErr
			}
		}
	}
	run := core.ComplexExecutionRun{ID: runID, DevelopmentRequirementID: requirementID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, PlanID: plan.ID, PlanReviewID: review.ID, PlanSHA256: plan.PlanSHA256, Mode: selection.Mode, ModeReason: string(selection.ReasonCode), FixedBuilderCount: selection.BuilderCount, ExpectedTaskSetVersion: version.TaskSetVersion, TaskSetVersion: core.ComplexStandardTaskSetVersion, ExecutionPackageJSON: string(runPackage), ExecutionPackageSHA256: runPackageSHA, StewardRoleBindingID: steward.ID, CreatedAt: now}
	if contractPlan {
		// The original Steward remains provenance, not an execution approver.
		// Do not create a fictitious review, dispatch step, or Agent message.
		_, _, err = s.complexExecution.StartClearDevComplexExecution(ctx, core.StartComplexExecutionCommand{Run: run})
		if err != nil {
			return RequirementView{}, mapStoreError(err, "START_COMPLEX_EXECUTION_FAILED")
		}
		s.scheduleComplexStandardExecution(requirementID)
		return s.GetRequirement(ctx, requirementID)
	}
	role := core.ComplexExecutionRoleBinding{ID: s.newID(), ExecutionRunID: runID, SourceComplexRoleBindingID: steward.ID, Role: core.StandardRoleSteward, AOSessionID: steward.AOSessionID, SessionCreationIdempotencyKey: "cleardev-complex-execution:steward:" + runID, Status: core.RoleBindingStatusBound, RequestedAt: now}
	if strings.TrimSpace(steward.AOSessionID) == "" || steward.Status != core.RoleBindingStatusBound {
		return RequirementView{}, apierr.Conflict("STEWARD_SESSION_UNBOUND", "The original Project Steward session is not bound", nil)
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
		prompt := complexExecutionRequestPrompt(runID, version, plan, review)
		step = core.AgentStep{ID: stepID, RoleBindingID: role.ID, Kind: core.AgentStepDispatchRequest, RequestID: runID, ClientMessageID: "cleardev-complex-execution-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
	}
	_, _, err = s.complexExecution.StartClearDevComplexExecution(ctx, core.StartComplexExecutionCommand{Run: run, StewardRoleBinding: role, AgentStep: step})
	if err != nil {
		return RequirementView{}, mapStoreError(err, "START_COMPLEX_EXECUTION_FAILED")
	}
	s.scheduleComplexStandardExecution(requirementID)
	return s.GetRequirement(ctx, requirementID)
}

func sameComplexExecutionRequest(execution core.ComplexExecutionSnapshot, version core.RequirementVersion, plan core.ComplexEngineeringPlan, review core.ComplexPlanReview) bool {
	run := execution.Run
	if run.DevelopmentRequirementID != version.DevelopmentRequirementID || run.RequirementVersionID != version.ID || run.RequirementSHA256 != version.SHA256 ||
		run.PlanID != plan.ID || run.PlanReviewID != review.ID || run.PlanSHA256 != plan.PlanSHA256 ||
		(run.Mode != core.WorkModeStandard && run.Mode != core.WorkModeParallel) || run.FixedBuilderCount < 1 || run.FixedBuilderCount > core.ComplexParallelMaxBuilders ||
		run.ExpectedTaskSetVersion != 0 || run.TaskSetVersion != core.ComplexStandardTaskSetVersion ||
		run.ExecutionPackageSHA256 == "" || coreDigest([]byte(run.ExecutionPackageJSON)) != run.ExecutionPackageSHA256 {
		return false
	}
	var packageFact core.ComplexExecutionRunPackage
	if err := json.Unmarshal([]byte(run.ExecutionPackageJSON), &packageFact); err != nil || packageFact.SchemaVersion != core.ComplexExecutionProtocolVersion || packageFact.Mode != string(run.Mode) || packageFact.TaskSetVersion != core.ComplexStandardTaskSetVersion || packageFact.ExecutionRunID != run.ID || packageFact.RequirementVersionID != version.ID || packageFact.RequirementSHA256 != version.SHA256 || packageFact.PlanID != plan.ID || packageFact.PlanSHA256 != plan.PlanSHA256 {
		return false
	}
	expectedTaskSet := int64(0)
	if len(execution.Tasks) != 0 {
		expectedTaskSet = core.ComplexStandardTaskSetVersion
	}
	return version.TaskSetVersion == expectedTaskSet
}

// ResumeComplexStandardExecutions schedules every durable complex execution
// that still has runnable work after service startup.
func (s *Service) ResumeComplexStandardExecutions(ctx context.Context) error {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return err
	}

	if s.complexExecution == nil {
		return nil
	}
	ids, err := s.complexExecution.ListClearDevRunnableComplexExecutions(ctx)
	if err != nil {
		return fmt.Errorf("list runnable ClearDev complex executions: %w", err)
	}
	for _, id := range ids {
		s.scheduleComplexStandardExecution(id)
	}
	quickIDs, err := s.complexExecution.ListClearDevRunnableComplexQuickExecutions(ctx)
	if err != nil {
		return fmt.Errorf("list runnable ClearDev complex quick executions: %w", err)
	}
	for _, id := range quickIDs {
		s.scheduleComplexStandardExecution(id)
	}
	return nil
}

func (s *Service) scheduleComplexStandardExecution(requirementID string) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		s.logger.Error("ClearDev controlled scheduling rejected", "err", err)
		return
	}

	if s.complexExecution == nil || requirementID == "" {
		return
	}
	s.complexExecutionMu.Lock()
	if s.complexExecutionRunning[requirementID] {
		s.complexExecutionWake[requirementID] = true
		s.complexExecutionMu.Unlock()
		return
	}
	s.complexExecutionRunning[requirementID] = true
	s.complexExecutionMu.Unlock()
	s.runBackground(func() {
		for {
			if err := s.runComplexStandardExecution(s.backgroundContext, requirementID); err != nil && !errors.Is(err, errComplexExecutionStopped) {
				s.logger.Error("ClearDev complex execution stopped", "requirementID", requirementID, "error", err)
				s.recordWorkflowOperationFailure(s.backgroundContext, requirementID, "execution")
			}
			observe, observationErr := s.shouldContinueProjectObservation(s.backgroundContext, requirementID)
			if observationErr != nil && s.backgroundContext.Err() == nil {
				s.logger.Error("ClearDev project observation lookup failed", "requirementID", requirementID, "error", observationErr)
			}
			if observe {
				// Keep the same registered runner and original message. A window
				// expiring is never permission to dispatch another Builder turn.
				observe = waitForProjectObservation(s.backgroundContext)
			}
			s.complexExecutionMu.Lock()
			if s.backgroundContext.Err() == nil && (s.complexExecutionWake[requirementID] || observe) {
				delete(s.complexExecutionWake, requirementID)
				s.complexExecutionMu.Unlock()
				continue
			}
			delete(s.complexExecutionRunning, requirementID)
			s.complexExecutionMu.Unlock()
			return
		}
	})
}

func (s *Service) runComplexStandardExecution(ctx context.Context, requirementID string) error {
	blocked, err := s.recoverRequirementAgentAttempts(ctx, requirementID)
	if err != nil || blocked {
		return err
	}
	quick, found, err := s.complexExecution.GetClearDevComplexQuickExecution(ctx, requirementID)
	if err != nil {
		return err
	}
	if found {
		_ = quick
		return s.runComplexQuickExecution(ctx, requirementID)
	}
	execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil || !found {
		return err
	}
	if execution.Run.Mode == core.WorkModeParallel {
		return s.runComplexParallelExecution(ctx, requirementID)
	}
	for i := 0; i < 256; i++ {
		progressed, done, err := s.advanceComplexStandardExecution(ctx, requirementID)
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
	return errors.New("ClearDev complex execution exceeded bounded advance count")
}

// advanceComplexStandardExecution owns only S06 facts. It fails closed before
// every external action and intentionally leaves candidate/check/review work
// pending until their exact durable attempt facts exist.
func (s *Service) advanceComplexStandardExecution(ctx context.Context, requirementID string) (bool, bool, error) {
	if s.direction != nil {
		// The exact v2 stop-gate check is repeated by storage on every mutation.
		execution, found, lookupErr := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
		if lookupErr != nil {
			return false, false, lookupErr
		}
		if found {
			stopped, stopErr := s.direction.HasActiveClearDevDirectionStop(ctx, execution.Run.RequirementVersionID)
			if stopErr != nil {
				return false, false, stopErr
			}
			if stopped {
				return false, true, errComplexExecutionStopped
			}
		}
	}
	execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil || !found {
		return false, !found, err
	}
	repairCurrentRequired := core.BuilderFirstFailureEnabled(execution.Run)
	for _, task := range execution.Tasks {
		repairCurrentRequired = repairCurrentRequired || core.FinalReviewTaskReturned(execution, task)
	}
	if repairCurrentRequired {
		store, ok := s.finalReviews.(builderFirstFinalFailureStore)
		if !ok {
			return false, true, nil
		}
		current, currentErr := store.BuilderFirstFailureCurrent(ctx, execution.Run.ID)
		if currentErr != nil || !current {
			return false, true, currentErr
		}
	}
	if changed, err := s.advanceUnifiedFailureRecovery(ctx, requirementID, &execution); err != nil || changed {
		return changed, false, err
	}
	if handled, changed, stop, err := s.advanceFixedRecovery(ctx, execution); handled {
		return changed, stop, err
	}
	if handled, changed, stop, err := s.advancePlannerRuntime(ctx, execution); handled {
		return changed, stop, err
	}
	if handled, changed, err := s.advanceProjectDependencyCheckRecovery(ctx, execution); handled {
		return changed, !changed, err
	}
	if handled, changed, err := s.advanceBuilderFirstRequiredFailure(ctx, execution); handled {
		return changed, !changed, err
	}
	if handled, changed, err := s.advanceBuilderFirstIntegrationFailure(ctx, execution); handled {
		return changed, !changed, err
	}
	if handled, changed, err := s.advanceBuilderFirstFinalFailure(ctx, execution); handled {
		return changed, !changed, err
	}
	phase, _ := core.DeriveComplexExecutionPhase(execution)
	if phase == core.ComplexExecutionCompleted {
		if err := s.releaseCandidateCheckEnvironment(ctx, execution.Run.ID+":check-preflight"); err != nil {
			return false, false, err
		}
		return false, true, nil
	}
	if phase == core.ComplexExecutionNeedsHuman || phase == core.ComplexExecutionBlocked {
		return false, true, nil
	}
	snapshot, found, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil || !found {
		return false, false, err
	}
	if snapshot.Requirement.CancelledAt != nil {
		if err := s.releaseCandidateCheckEnvironment(ctx, execution.Run.ID+":check-preflight"); err != nil {
			return false, false, err
		}
		return false, true, nil
	}
	planning, found, err := s.complex.GetClearDevComplexPlanning(ctx, requirementID)
	if err != nil || !found {
		return false, false, err
	}
	version, ok := currentConfirmedVersion(snapshot)
	plan, review, parsed, parseErr := s.executionPlanForRun(ctx, planning, execution.Run)
	expectedVersionTaskSet := complexExecutionExpectedRequirementTaskSet(phase)
	if !ok || parseErr != nil || version.ID != execution.Run.RequirementVersionID || version.SHA256 != execution.Run.RequirementSHA256 || version.TaskSetVersion != expectedVersionTaskSet || execution.Run.ExpectedTaskSetVersion != 0 || execution.Run.TaskSetVersion != core.ComplexStandardTaskSetVersion {
		return false, true, errComplexExecutionStopped
	}
	if phase == core.ComplexExecutionPreparingTasks {
		command, err := s.materializeComplexExecution(ctx, snapshot.Requirement, execution.Run, parsed, version)
		if err != nil {
			return false, true, err
		}
		if err := s.complexExecution.MaterializeClearDevComplexExecution(ctx, command); err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	if phase == core.ComplexExecutionAwaitingSteward {
		return s.advanceComplexExecutionSteward(ctx, execution, planning, snapshot, version, plan, review)
	}
	if phase == core.ComplexExecutionBindingBuilder {
		return s.ensureComplexExecutionBuilder(ctx, execution, snapshot.Requirement.AOProjectID)
	}
	if phase == core.ComplexExecutionAwaitingSpecialist {
		task, ok := nextComplexExecutionDispatchTask(execution)
		if !ok {
			return false, true, errComplexExecutionStopped
		}
		planTask, ok := complexPlanTaskByKey(parsed, task.TaskKey)
		if !ok {
			return false, true, errComplexExecutionStopped
		}
		return s.advanceComplexExceptionSpecialist(ctx, execution, task, planTask, snapshot.Requirement.AOProjectID)
	}
	if phase == core.ComplexExecutionAwaitingScope {
		return s.advanceComplexExceptionScope(ctx, execution, version, plan, parsed)
	}
	if phase == core.ComplexExecutionRecovering {
		return s.advanceComplexExceptionRecovery(ctx, execution, snapshot.Requirement.AOProjectID)
	}
	if phase == core.ComplexExecutionReadyToDispatch || phase == core.ComplexExecutionReworking {
		task, ok := nextComplexExecutionDispatchTask(execution)
		if !ok {
			return false, false, nil
		}
		planTask, ok := complexPlanTaskByKey(parsed, task.TaskKey)
		if !ok {
			return false, true, errComplexExecutionStopped
		}
		if changed, done, err := s.ensureSpecialistBeforeDispatch(ctx, execution, task, planTask, snapshot.Requirement.AOProjectID); err != nil || changed || done {
			return changed, done, err
		}
		deps, ok := executionDependencyIDs(execution, task)
		if !ok {
			return false, true, errComplexExecutionStopped
		}
		if task.ExecutionPackageJSON == "" || task.ExecutionPackageSHA256 == "" || coreDigest([]byte(task.ExecutionPackageJSON)) != task.ExecutionPackageSHA256 {
			return false, true, errComplexExecutionStopped
		}
		pkg, packageErr := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
		if packageErr != nil || core.ProjectTaskMatchesRun(pkg, execution.Run) != nil || pkg.ExecutionRunID != execution.Run.ID || pkg.TaskID != task.DevelopmentTaskID || pkg.PlanID != plan.ID || pkg.PlanSHA256 != plan.PlanSHA256 || strings.Join(pkg.DependencyTaskIDs, "\x00") != strings.Join(deps, "\x00") || !sameComplexPlanTask(pkg, planTask) {
			return false, true, errComplexExecutionStopped
		}
		now := s.now().UTC()
		dispatchID, stepID := s.newID(), s.newID()
		// A generic rework round spends its budget before a dispatch row grows
		// the snapshot round, so the new round is the larger of the two; mail
		// rounds already advance the snapshot round and see the same number.
		round := task.CurrentRound
		if task.ReworkCount > round {
			round = task.ReworkCount
		}
		base := execution.Run.InitialBaseCommitSHA
		if revisedBase, revised, err := plannerProjectDispatchBase(execution, task, round); err != nil {
			return false, true, err
		} else if revised {
			base = revisedBase
		} else if round > 0 {
			base, ok = previousDispatchRoundBase(execution, task, round-1)
			if !ok {
				return false, true, errComplexExecutionStopped
			}
		} else if task.Ordinal > 0 {
			base, ok = previousVerifiedCandidate(execution, task.Ordinal)
			if !ok {
				return false, true, errComplexExecutionStopped
			}
		}
		// Freeze feedback for the round being created. Generic manual rework
		// increments its counter before storage advances CurrentRound; using
		// the old round here would change recovery context before delivery.
		promptTask := task
		promptTask.CurrentRound = round
		feedback := complexExecutionReworkFeedback(execution, promptTask)
		prompt := executionBuilderPromptForRun(execution.Run, []byte(task.ExecutionPackageJSON), dispatchID, task.DevelopmentTaskID, round, feedback)
		step := core.AgentStep{ID: stepID, RoleBindingID: execution.Run.BuilderRoleBindingID, Kind: core.ComplexExecutionAgentStepBuilderTask, RequestID: dispatchID, ClientMessageID: "cleardev-complex-execution-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
		dispatch := core.ComplexExecutionDispatch{ID: dispatchID, ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, DevelopmentTaskID: task.DevelopmentTaskID, Round: round, BaseCommitSHA: base, AgentStepID: stepID, ExecutionPackageSHA256: task.ExecutionPackageSHA256, ClientMessageID: step.ClientMessageID, Status: core.ComplexExecutionDispatchPending, CreatedAt: now}
		_, _, err = s.complexExecution.CreateClearDevComplexExecutionDispatch(ctx, core.CreateComplexExecutionDispatchCommand{Dispatch: dispatch, AgentStep: step})
		return err == nil, false, err
	}
	if phase == core.ComplexExecutionBuilding || phase == core.ComplexExecutionReviewing {
		return s.advanceComplexExecutionDispatch(ctx, execution, planning, snapshot.Requirement.AOProjectID, parsed)
	}
	if phase == core.ComplexExecutionIntegrating {
		return s.advanceComplexExecutionIntegration(ctx, execution, snapshot.Requirement.AOProjectID)
	}
	return false, false, nil
}

func (s *Service) advanceComplexExecutionSteward(ctx context.Context, execution core.ComplexExecutionSnapshot, planning core.ComplexPlanningSnapshot, snapshot core.RequirementSnapshot, version core.RequirementVersion, plan core.ComplexEngineeringPlan, review core.ComplexPlanReview) (bool, bool, error) {
	steward := activeComplexExecutionSteward(execution)
	if steward.ID == "" {
		return false, true, errComplexExecutionStopped
	}
	if steward.Status == core.RoleBindingStatusRequested {
		return s.ensureComplexExecutionSteward(ctx, execution, snapshot.Requirement.AOProjectID)
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
		source, sourceFound := complexPlanningRoleBindingByID(planning, steward.SourceComplexRoleBindingID)
		if !sourceFound || source.AOSessionID == "" || steward.AOSessionID != source.AOSessionID {
			changed, endErr := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, steward.ID, core.ReasonCode("STEWARD_CONTINUATION_ENDED"), s.now().UTC())
			return changed, true, endErr
		}
		now := s.now().UTC()
		bindingID := s.newID()
		binding := core.ComplexExecutionRoleBinding{ID: bindingID, ExecutionRunID: execution.Run.ID, SourceComplexRoleBindingID: steward.SourceComplexRoleBindingID, ContinuationOfRoleBindingID: steward.ID, Role: core.StandardRoleSteward, SessionCreationIdempotencyKey: "cleardev-complex-execution:steward-continuation:" + execution.Run.ID, Status: core.RoleBindingStatusRequested, RequestedAt: now}
		changed, continueErr := s.complexExecution.ContinueClearDevComplexExecutionSteward(ctx, steward.ID, binding, core.AgentStep{}, now)
		return changed, false, continueErr
	}
	if !s.validComplexExecutionSteward(ctx, record, snapshot.Requirement.AOProjectID) {
		changed, endErr := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, steward.ID, core.ReasonCode("STEWARD_SESSION_INVALID"), s.now().UTC())
		return changed, true, endErr
	}
	var step core.AgentStep
	for _, item := range execution.AgentSteps {
		if item.RoleBindingID == steward.ID && item.Kind == core.AgentStepDispatchRequest && item.RequestID == execution.Run.ID {
			step = item
			break
		}
	}
	if step.ID == "" {
		now := s.now().UTC()
		stepID := s.newID()
		prompt := complexExecutionRequestPrompt(execution.Run.ID, version, plan, review)
		created := core.AgentStep{ID: stepID, RoleBindingID: steward.ID, Kind: core.AgentStepDispatchRequest, RequestID: execution.Run.ID, ClientMessageID: "cleardev-complex-execution-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
		_, _, err := s.complexExecution.CreateClearDevComplexExecutionAgentStep(ctx, created)
		return err == nil, false, err
	}
	prompt := complexExecutionRequestPrompt(execution.Run.ID, version, plan, review)
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return false, true, errComplexExecutionStopped
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.relayAgentTurn(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, step, steward.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return false, false, err
		}
		_, err := s.complexExecution.MarkClearDevComplexExecutionAgentStepSent(ctx, step.ID, s.now().UTC())
		return err == nil, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		validate := func(raw []byte) error {
			_, err := core.ParseComplexExecutionRequestResult(raw, execution.Run.ID, version.ID, plan.ID, plan.PlanSHA256)
			return err
		}
		fail := func(ctx context.Context, item core.AgentStep, reason core.ReasonCode) error {
			_, err := s.complexExecution.FailClearDevComplexExecutionAgentStep(ctx, item.ID, reason, s.now().UTC())
			return err
		}
		message, stopped, err := s.awaitValidAgentJSON(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, steward.AOSessionID, step, prompt, validate, standardStepReasons{
			Invalid: "STEWARD_RESULT_INVALID", Timeout: "STEWARD_TIMEOUT", Unavailable: "STEWARD_UNAVAILABLE",
		}, fail, errComplexExecutionStopped)
		if err != nil || stopped {
			return false, stopped, err
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, message.TurnID, message.MessageID, message.Text, coreDigest([]byte(message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexExecutionAgentStep(ctx, step)
		return err == nil, false, err
	}
	if step.SendStatus != core.AgentStepSendStatusSettled {
		return false, true, errComplexExecutionStopped
	}
	result, err := core.ParseComplexExecutionRequestResult([]byte(step.FinalMessageText), execution.Run.ID, version.ID, plan.ID, plan.PlanSHA256)
	if err != nil {
		changed, endErr := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, steward.ID, core.ReasonCode("STEWARD_RESULT_INVALID"), s.now().UTC())
		if endErr != nil {
			return false, false, endErr
		}
		return changed, true, errComplexExecutionStopped
	}
	if result.Decision == string(core.ComplexExecutionDecisionNeedsHuman) {
		if err := s.complexExecution.SettleClearDevComplexExecutionRequest(ctx, execution.Run.ID, core.ComplexExecutionDecisionNeedsHuman, core.ReasonHumanDecisionRequired, s.now().UTC()); err != nil {
			return false, false, err
		}
		return true, true, nil
	}
	parsed, err := parseComplexExecutionPlan(plan)
	if err != nil {
		return false, true, err
	}
	command, err := s.materializeComplexExecution(ctx, snapshot.Requirement, execution.Run, parsed, version)
	if err != nil {
		return false, true, err
	}
	if err := s.complexExecution.MaterializeClearDevComplexExecution(ctx, command); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (s *Service) materializeComplexExecution(ctx context.Context, requirement core.DevelopmentRequirement, run core.ComplexExecutionRun, plan core.ComplexEngineeringPlanResult, version core.RequirementVersion) (core.MaterializeComplexExecutionCommand, error) {
	command := core.MaterializeComplexExecutionCommand{ExecutionRunID: run.ID, Tasks: make([]core.ComplexExecutionTask, 0, len(plan.Tasks)), CheckSpecs: []core.ComplexExecutionCheckSpecFact{}, At: s.now().UTC()}
	catalog, catalogErr := s.complexCheckCatalogForRequirement(ctx, requirement)
	if catalogErr != nil {
		return command, catalogErr
	}
	selection, selectionErr := s.selectComplexModeForRequirement(ctx, requirement, plan.Tasks, plan.ParallelSuggestion.RecommendedBuilderCount)
	contract, project, projectErr := core.ProjectContractFromRun(run)
	if projectErr != nil {
		return command, projectErr
	}
	var projectContract *core.ProjectExecutionContract
	if project {
		if err := core.ValidateExecutableProjectPlan(plan, contract.Basis); err != nil {
			return command, err
		}
		projectContract = &contract
		catalog = contract.Basis.CheckCatalog()
		selection, selectionErr = core.ProjectExecutionMode(plan), nil
		plan = core.ProjectExecutionPlan(plan, contract.Basis)
	}
	if selectionErr != nil || selection.Mode != run.Mode || selection.BuilderCount != run.FixedBuilderCount || string(selection.ReasonCode) != run.ModeReason {
		return command, errors.New("saved complex plan no longer selects this execution policy")
	}
	for ordinal, task := range plan.Tasks {
		item := core.ComplexExecutionTask{ID: s.newID(), ExecutionRunID: run.ID, TaskKey: task.Key, DevelopmentTaskID: s.newID(), Ordinal: ordinal, DependencyTaskKeys: append([]string(nil), task.DependencyKeys...), Status: core.DevelopmentTaskStatusPlanned, CurrentRound: 0}
		command.Tasks = append(command.Tasks, item)
	}
	for index := range command.Tasks {
		item := &command.Tasks[index]
		planTask, ok := complexPlanTaskByKey(plan, item.TaskKey)
		if !ok {
			return command, errors.New("saved complex plan lost a materialized task")
		}
		dependencies, ok := executionDependencyIDs(core.ComplexExecutionSnapshot{Tasks: command.Tasks}, *item)
		if !ok {
			return command, errors.New("saved complex plan has an unresolved dependency")
		}
		_, encoded, digest, err := core.BuildComplexStandardExecutionPackageWithCatalog(run.Mode, core.ComplexStandardExecutionPackageInput{ExecutionRunID: run.ID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256, RequirementText: version.RequirementText, PlanID: run.PlanID, PlanSHA256: run.PlanSHA256, TaskSetVersion: core.ComplexStandardTaskSetVersion, TaskID: item.DevelopmentTaskID, DependencyTaskIDs: dependencies, Task: planTask, PlanSchemaVersion: plan.SchemaVersion, InterfaceContracts: plan.InterfaceContracts, ProjectExecution: projectContract}, catalog)
		if err != nil {
			return command, err
		}
		item.ExecutionPackageJSON, item.ExecutionPackageSHA256 = string(encoded), digest
	}
	for _, task := range plan.Tasks {
		item, ok := complexExecutionTaskByKey(command.Tasks, task.Key)
		if !ok {
			return command, errors.New("saved complex plan lost task checks")
		}
		for _, checkID := range task.RequiredCheckIDs {
			check, ok := core.ComplexCheckByID(catalog, checkID)
			if !ok {
				return command, errors.New("saved complex plan names an unknown check")
			}
			argvJSON, _ := json.Marshal(check.Argv)
			command.CheckSpecs = append(command.CheckSpecs, core.ComplexExecutionCheckSpecFact{ID: s.newID(), ExecutionRunID: run.ID, ComplexExecutionTaskID: item.ID, CheckID: check.ID, Kind: core.CandidateCheckRequired, CheckSpecSHA256: coreDigest(argvJSON), Argv: append([]string(nil), check.Argv...), TimeoutSeconds: check.TimeoutSeconds, CreatedAt: command.At})
		}
		command.CheckSpecs = append(command.CheckSpecs, core.ComplexExecutionCheckSpecFact{ID: s.newID(), ExecutionRunID: run.ID, ComplexExecutionTaskID: item.ID, CheckID: "SCOPE", Kind: core.CandidateCheckScope, CheckSpecSHA256: complexScopeSpecDigest(task), Argv: []string{}, TimeoutSeconds: 0, CreatedAt: command.At})
	}
	for _, checkID := range plan.IntegrationCheckIDs {
		check, ok := core.ComplexCheckByID(catalog, checkID)
		if !ok {
			return command, errors.New("saved complex plan names an unknown integration check")
		}
		argvJSON, _ := json.Marshal(check.Argv)
		command.CheckSpecs = append(command.CheckSpecs, core.ComplexExecutionCheckSpecFact{ID: s.newID(), ExecutionRunID: run.ID, CheckID: check.ID, Kind: core.CandidateCheckIntegration, CheckSpecSHA256: coreDigest(argvJSON), Argv: append([]string(nil), check.Argv...), TimeoutSeconds: check.TimeoutSeconds, CreatedAt: command.At})
	}
	builderCount := selection.BuilderCount
	if run.Mode == core.WorkModeParallel {
		for ordinal, keys := range selection.Batches {
			batch := core.ComplexExecutionBatch{ID: s.newID(), ExecutionRunID: run.ID, Ordinal: ordinal, TaskKeys: append([]string(nil), keys...), Status: core.ComplexExecutionBatchPending, CreatedAt: command.At}
			command.Batches = append(command.Batches, batch)
		}
	}
	command.BuilderBindings = make([]core.ComplexExecutionRoleBinding, 0, builderCount)
	for slot := 1; slot <= builderCount; slot++ {
		keySuffix := ""
		if slot > 1 {
			keySuffix = fmt.Sprintf("-%d", slot)
		}
		command.BuilderBindings = append(command.BuilderBindings, core.ComplexExecutionRoleBinding{ID: s.newID(), ExecutionRunID: run.ID, Role: core.StandardRoleBuilder, BuilderSlot: slot, SessionCreationIdempotencyKey: "cleardev-complex-execution:builder:" + run.ID + keySuffix, Status: core.RoleBindingStatusRequested, RequestedAt: command.At})
	}
	return command, nil
}

// ensureComplexExecutionBuilder binds every planned builder slot. Each slot
// gets its own branch and worktree; slot 1 keeps the S06 branch name.
func (s *Service) ensureComplexExecutionBuilder(ctx context.Context, execution core.ComplexExecutionSnapshot, projectID string) (bool, bool, error) {
	contract, project, err := core.ProjectContractFromRun(execution.Run)
	if err != nil {
		return false, true, err
	}
	if project {
		return s.ensureProjectExecutionBuilder(ctx, execution, projectID, contract)
	}
	bindings, ok := currentComplexExecutionBuilderBindings(execution)
	if !ok {
		return false, true, errComplexExecutionStopped
	}
	progressed := false
	for _, binding := range bindings {
		if binding.Status == core.RoleBindingStatusFailed || binding.Status == core.RoleBindingStatusEnded {
			if canContinuePreDispatchParallelBuilder(execution, binding) {
				continuation := core.ComplexExecutionRoleBinding{
					ID:                            s.newID(),
					ExecutionRunID:                execution.Run.ID,
					ContinuationOfRoleBindingID:   binding.ID,
					Role:                          core.StandardRoleBuilder,
					BuilderSlot:                   binding.BuilderSlot,
					SessionCreationIdempotencyKey: "cleardev-complex-execution:builder-infra-continuation:" + binding.ID,
					Status:                        core.RoleBindingStatusRequested,
					RequestedAt:                   s.now().UTC(),
				}
				_, created, err := s.complexExecution.CreateClearDevComplexExecutionRoleBinding(ctx, continuation)
				return created, false, err
			}
			return false, true, errComplexExecutionStopped
		}
		baseline, baselineWorkspace, baselineErr := s.checkBuilderBaseline(ctx, binding.ID+":baseline", projectID)
		if baselineErr != nil {
			return s.stopComplexBaseline(ctx, binding, baselineErr)
		}
		if err := s.verifyMailRunBaseline(ctx, execution.Run, baseline, baselineWorkspace); err != nil {
			return s.stopComplexBaseline(ctx, binding, err)
		}
		if binding.Status == core.RoleBindingStatusBound {
			record, found, err := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
			if err != nil {
				return false, false, err
			}
			// A bound builder stays on its spawn base, or on a composed commit
			// of its own run after an approved parallel rebase.
			baseValid := binding.BaseCommitSHA == record.Metadata.DiffBaseSHA || complexExecutionCompositionOutput(execution, binding.BaseCommitSHA)
			if !found || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexExecutionWorker(ctx, record, binding, projectID) || !validComplexExecutionCommitSHA(record.Metadata.DiffBaseSHA) || !baseValid {
				changed, endErr := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, binding.ID, core.ReasonBuilderSpawnFailed, s.now().UTC())
				return changed, true, endErr
			}
			if baselineErr := s.verifySpawnedBuilderBaseline(ctx, baseline, record); baselineErr != nil {
				return s.stopComplexBaseline(ctx, binding, baselineErr)
			}
			if preflightErr := s.prepareCandidateCheckEnvironment(ctx, execution.Run.ID+":check-preflight", record.Metadata.WorkspacePath, binding.BaseCommitSHA, core.StandardCandidateCheckImage, complexCheckArgv(execution.CheckSpecs)); preflightErr != nil {
				s.logger.Error("ClearDev complex check preflight failed", "executionRunID", execution.Run.ID, "error", preflightErr)
				changed, endErr := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, binding.ID, core.ReasonCode("CHECKER_UNAVAILABLE"), s.now().UTC())
				return changed, true, endErr
			}
			continue
		}
		if binding.Status != core.RoleBindingStatusRequested {
			return false, true, errComplexExecutionStopped
		}
		branch := complexExecutionBuilderBranch(execution.Run.ID, binding)
		if baselineErr := s.pinBaselineBuilderBranch(ctx, baseline, baselineWorkspace, branch); baselineErr != nil {
			return s.stopComplexBaseline(ctx, binding, baselineErr)
		}
		session, blocked, err := s.spawnControlledChatSession(ctx, execution.Run.DevelopmentRequirementID, binding.ID, ports.SpawnConfig{ProjectID: domain.ProjectID(projectID), Kind: domain.KindWorker, Branch: branch, Prompt: "", RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto}, DisplayName: "ClearDev Complex Builder", CreationIdempotencyKey: binding.SessionCreationIdempotencyKey})
		if blocked {
			return false, false, nil
		}
		if err != nil || !s.validComplexExecutionWorker(ctx, session.SessionRecord, binding, projectID) {
			_, _ = s.complexExecution.FailClearDevComplexExecutionRoleBinding(ctx, binding.ID, core.ReasonBuilderSpawnFailed, s.now().UTC())
			return true, true, errComplexExecutionStopped
		}
		if baselineErr := s.verifySpawnedBuilderBaseline(ctx, baseline, session.SessionRecord); baselineErr != nil {
			return s.stopComplexBaseline(ctx, binding, baselineErr)
		}
		if !validComplexExecutionCommitSHA(session.Metadata.DiffBaseSHA) {
			_, _ = s.complexExecution.FailClearDevComplexExecutionRoleBinding(ctx, binding.ID, core.ReasonBuilderSpawnFailed, s.now().UTC())
			return true, true, errComplexExecutionStopped
		}
		if preflightErr := s.prepareCandidateCheckEnvironment(ctx, execution.Run.ID+":check-preflight", session.Metadata.WorkspacePath, session.Metadata.DiffBaseSHA, core.StandardCandidateCheckImage, complexCheckArgv(execution.CheckSpecs)); preflightErr != nil {
			s.logger.Error("ClearDev complex check preflight failed", "executionRunID", execution.Run.ID, "error", preflightErr)
			_, _ = s.complexExecution.FailClearDevComplexExecutionRoleBinding(ctx, binding.ID, core.ReasonCode("CHECKER_UNAVAILABLE"), s.now().UTC())
			return true, true, errComplexExecutionStopped
		}
		if _, err = s.complexExecution.BindClearDevComplexExecutionRoleBinding(ctx, binding.ID, string(session.ID), session.Metadata.WorkspacePath, session.Metadata.DiffBaseSHA, s.now().UTC()); err != nil {
			return false, false, err
		}
		progressed = true
	}
	return progressed, false, nil
}

func complexExecutionBuilderBranch(runID string, binding core.ComplexExecutionRoleBinding) string {
	if binding.ContinuationOfRoleBindingID != "" && strings.HasSuffix(binding.ID, ":replacement-builder") && binding.SessionCreationIdempotencyKey == strings.TrimSuffix(binding.ID, ":replacement-builder")+":replacement-create" {
		return replacementBuilderBranch(runID, binding.ID)
	}
	branch := "cleardev-complex-builder-" + runID
	if binding.BuilderSlot > 1 {
		branch = fmt.Sprintf("cleardev-complex-builder-%s-%d", runID, binding.BuilderSlot)
	}
	if binding.ContinuationOfRoleBindingID != "" {
		branch = fmt.Sprintf("%s-cont-%s", branch, binding.ID)
	}
	return branch
}

func currentComplexExecutionBuilderBindings(execution core.ComplexExecutionSnapshot) ([]core.ComplexExecutionRoleBinding, bool) {
	bySlot := make(map[int]core.ComplexExecutionRoleBinding, execution.Run.FixedBuilderCount)
	for _, binding := range execution.RoleBindings {
		if binding.Role != core.StandardRoleBuilder || binding.BuilderSlot < 1 || binding.BuilderSlot > execution.Run.FixedBuilderCount {
			continue
		}
		current, exists := bySlot[binding.BuilderSlot]
		if !exists || binding.RequestedAt.After(current.RequestedAt) {
			bySlot[binding.BuilderSlot] = binding
		}
	}
	if len(bySlot) != execution.Run.FixedBuilderCount {
		return nil, false
	}
	out := make([]core.ComplexExecutionRoleBinding, 0, execution.Run.FixedBuilderCount)
	for slot := 1; slot <= execution.Run.FixedBuilderCount; slot++ {
		binding, ok := bySlot[slot]
		if !ok {
			return nil, false
		}
		out = append(out, binding)
	}
	return out, true
}

func canContinuePreDispatchParallelBuilder(execution core.ComplexExecutionSnapshot, binding core.ComplexExecutionRoleBinding) bool {
	if execution.Run.Mode != core.WorkModeParallel || len(execution.Dispatches) != 0 || binding.ContinuationOfRoleBindingID != "" {
		return false
	}
	if binding.Status != core.RoleBindingStatusFailed && binding.Status != core.RoleBindingStatusEnded {
		return false
	}
	return binding.ReasonCode == core.ReasonBuilderSpawnFailed || binding.ReasonCode == core.ReasonCode("CHECKER_UNAVAILABLE")
}

// A continuation is permitted only when the original S04 Steward session is
// conclusively terminated. Transient read failures and missing records never
// silently replace human authority.
func (s *Service) ensureComplexExecutionSteward(ctx context.Context, execution core.ComplexExecutionSnapshot, projectID string) (bool, bool, error) {
	binding := activeComplexExecutionSteward(execution)
	if binding.ID == "" || binding.Status != core.RoleBindingStatusRequested || binding.SourceComplexRoleBindingID == "" {
		return false, true, errComplexExecutionStopped
	}
	session, blocked, err := s.spawnControlledChatSession(ctx, execution.Run.DevelopmentRequirementID, binding.ID, ports.SpawnConfig{ProjectID: domain.ProjectID(projectID), Kind: domain.KindOrchestrator, Prompt: "", RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto}, DisplayName: "ClearDev Complex Steward Continuation", CreationIdempotencyKey: binding.SessionCreationIdempotencyKey})
	if blocked {
		return false, false, nil
	}
	if err != nil || !s.validComplexExecutionSteward(ctx, session.SessionRecord, projectID) {
		_, _ = s.complexExecution.FailClearDevComplexExecutionRoleBinding(ctx, binding.ID, core.ReasonCode("STEWARD_UNAVAILABLE"), s.now().UTC())
		return true, true, errComplexExecutionStopped
	}
	if _, err = s.complexExecution.BindClearDevComplexExecutionRoleBinding(ctx, binding.ID, string(session.ID), session.Metadata.WorkspacePath, "", s.now().UTC()); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func activeComplexExecutionSteward(execution core.ComplexExecutionSnapshot) core.ComplexExecutionRoleBinding {
	for index := len(execution.RoleBindings) - 1; index >= 0; index-- {
		binding := execution.RoleBindings[index]
		if binding.Role == core.StandardRoleSteward && (binding.Status == core.RoleBindingStatusRequested || binding.Status == core.RoleBindingStatusBound) {
			return binding
		}
	}
	return core.ComplexExecutionRoleBinding{}
}

func complexPlanningRoleBindingByID(planning core.ComplexPlanningSnapshot, id string) (core.ComplexRoleBinding, bool) {
	for _, binding := range planning.RoleBindings {
		if binding.ID == id {
			return binding, true
		}
	}
	return core.ComplexRoleBinding{}, false
}

func validComplexExecutionSteward(record domain.SessionRecord, projectID string) bool {
	return record.ID != "" && string(record.ProjectID) == projectID && record.Kind == domain.KindOrchestrator && supportedControlledHarness(record.Harness) && domain.NormalizeSessionMode(record.Mode) == domain.SessionModeChat && record.PermissionMode == domain.PermissionModeAuto && strings.TrimSpace(record.Metadata.WorkspacePath) != ""
}

func validComplexExecutionWorker(record domain.SessionRecord, binding core.ComplexExecutionRoleBinding, projectID string) bool {
	return record.ID != "" && string(record.ProjectID) == projectID && record.Kind == domain.KindWorker && supportedControlledHarness(record.Harness) && domain.NormalizeSessionMode(record.Mode) == domain.SessionModeChat && record.PermissionMode == domain.PermissionModeAuto && strings.TrimSpace(record.Metadata.WorkspacePath) != "" && record.CreationIdempotencyKey == binding.SessionCreationIdempotencyKey
}

func validComplexExecutionCommitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func complexExecutionTaskByKey(tasks []core.ComplexExecutionTask, key string) (core.ComplexExecutionTask, bool) {
	for _, task := range tasks {
		if task.TaskKey == key {
			return task, true
		}
	}
	return core.ComplexExecutionTask{}, false
}

func sameComplexPlanTask(pkg core.ComplexStandardExecutionPackage, task core.ComplexPlanTask) bool {
	return pkg.TaskKey == task.Key && pkg.Title == task.Title && pkg.Objective == task.Objective && strings.Join(pkg.RequirementIDs, "\x00") == strings.Join(task.RequirementIDs, "\x00") && strings.Join(pkg.AcceptanceIDs, "\x00") == strings.Join(task.AcceptanceIDs, "\x00") && strings.Join(pkg.DependencyTaskKeys, "\x00") == strings.Join(task.DependencyKeys, "\x00")
}

// advanceComplexExecutionDispatch advances one durable Builder/check/Reviewer
// round. Stable message and check identities are saved before their external
// actions; a recovered STARTED check reuses its durable runner receipt.
func (s *Service) advanceComplexExecutionDispatch(ctx context.Context, execution core.ComplexExecutionSnapshot, planning core.ComplexPlanningSnapshot, projectID string, plan core.ComplexEngineeringPlanResult) (bool, bool, error) {
	task, dispatch, ok := activeComplexExecutionDispatch(execution)
	if !ok {
		return false, true, errComplexExecutionStopped
	}
	builder, ok := complexExecutionBindingByID(execution, execution.Run.BuilderRoleBindingID)
	if !ok || builder.Status != core.RoleBindingStatusBound {
		return false, true, errComplexExecutionStopped
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil {
		return false, false, err
	}
	step, ok := complexExecutionStepByID(execution, dispatch.AgentStepID)
	if ok {
		var replacementErr error
		step, replacementErr = s.replacementEffectiveStep(ctx, step)
		if replacementErr != nil {
			return false, true, replacementErr
		}
	}
	_, project, projectErr := core.ProjectContractFromRun(execution.Run)
	settledProjectCandidate := projectErr == nil && project && dispatch.CandidateCommitID != "" && validComplexExecutionCommitSHA(dispatch.CandidateCommitSHA) && ok && step.SendStatus == core.AgentStepSendStatusSettled
	if projectErr == nil && project && ok && step.SendStatus == core.AgentStepSendStatusPending && builderSessionRetryForDispatch(execution, dispatch) != nil {
		// A saved delivery must be reconciled before considering restoration,
		// even when the session exited after accepting the original message.
		prompt := complexExecutionExistingBuilderPrompt(execution, task, dispatch, step)
		if step.PromptSHA256 != coreDigest([]byte(prompt)) || step.RoleBindingID != builder.ID || step.RequestID != dispatch.ID {
			return false, true, errComplexExecutionStopped
		}
		if resumed, resumeErr := s.resumePendingComplexBuilderDelivery(ctx, execution.Run.DevelopmentRequirementID, step, builder.AOSessionID, prompt); resumed || resumeErr != nil {
			return resumed && resumeErr == nil, false, resumeErr
		}
		handled, ready, checked, checkErr := s.recheckWorkflowBuilder(ctx, execution, task, dispatch, builder, projectID)
		if checkErr != nil {
			return false, false, checkErr
		}
		if handled && !ready {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonBuilderSpawnFailed)
		}
		if handled {
			record, found = checked, true
		}
	}
	if found && !record.IsTerminated && project && ok && step.SendStatus == core.AgentStepSendStatusPending && record.Activity.State == domain.ActivityExited && s.validComplexExecutionWorker(ctx, record, builder, projectID) {
		if s.restoreWorkflowBuilder(ctx, execution, task, dispatch, builder, record) {
			return true, false, nil
		}
	}
	if !found || record.IsTerminated || (record.Activity.State == domain.ActivityExited && !settledProjectCandidate) || !s.validComplexExecutionWorker(ctx, record, builder, projectID) {
		return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonBuilderSpawnFailed)
	}
	if !ok || step.RoleBindingID != builder.ID || step.Kind != core.ComplexExecutionAgentStepBuilderTask || step.RequestID != dispatch.ID {
		return false, true, errComplexExecutionStopped
	}
	prompt := complexExecutionExistingBuilderPrompt(execution, task, dispatch, step)
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return false, true, errComplexExecutionStopped
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		// A crash may leave the logical step pending after the provider has
		// received it. Reconcile its durable delivery before requiring an
		// untouched spawn base; never resend or reset an ambiguous delivery.
		if resumed, resumeErr := s.resumePendingComplexBuilderDelivery(ctx, execution.Run.DevelopmentRequirementID, step, builder.AOSessionID, prompt); resumed || resumeErr != nil {
			return resumed && resumeErr == nil, false, resumeErr
		}
		if preflightErr := s.validateComplexExecutionDispatchWorkspace(ctx, execution, task, dispatch, record.Metadata.WorkspacePath); preflightErr != nil {
			if builderSessionRetryForDispatch(execution, dispatch) != nil {
				if err := s.recordBuilderRecheckSendStop(ctx, execution, dispatch, "WORKSPACE", string(gitInspectionReason(preflightErr))); err != nil {
					return false, false, err
				}
				return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonBuilderSpawnFailed)
			}
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, errors.Is(preflightErr, ports.ErrClearDevGitUnavailable), gitInspectionReason(preflightErr))
		}
		if err := s.occupyExceptionBudget(ctx, execution, task.ID, core.ComplexExceptionBudgetBuilder, dispatch.ID, step.ID); err != nil {
			if errors.Is(err, errComplexExecutionStopped) {
				return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, "BUILDER_BUDGET_EXHAUSTED")
			}
			return false, true, err
		}
		if err := s.relayAgentTurn(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, step, builder.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			var rule *core.RuleError
			if errors.As(err, &rule) && rule.Code == "BUILDER_RECHECK_REQUIRED" && builderSessionRetryForDispatch(execution, dispatch) != nil {
				if saveErr := s.recordBuilderRecheckSendStop(ctx, execution, dispatch, "DELIVERY", "BUILDER_RECHECK_REQUIRED"); saveErr != nil {
					return false, false, saveErr
				}
				return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonBuilderSpawnFailed)
			}
			return false, false, err
		}
		_, err = s.complexExecution.MarkClearDevComplexExecutionAgentStepSent(ctx, step.ID, s.now().UTC())
		return err == nil, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		var lastRaw []byte
		validate := func(raw []byte) error {
			lastRaw = raw
			return validateRuntimeBuilderOrScopeRequest(raw, execution, dispatch, task)
		}
		fail := func(ctx context.Context, item core.AgentStep, reason core.ReasonCode) error {
			if reason == core.ReasonCode("BUILDER_RESULT_INVALID") || reason == core.ReasonScopeRequestInvalid {
				reason = builderOrScopeInvalidReason(lastRaw)
			}
			infra := reason == core.ReasonCode("BUILDER_TIMEOUT") || reason == core.ReasonCode("BUILDER_UNAVAILABLE")
			_, _, err := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, infra, reason)
			return err
		}
		message, stopped, err := s.awaitValidAgentJSON(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, builder.AOSessionID, step, prompt, validate, standardStepReasons{
			Invalid: "BUILDER_RESULT_INVALID", Timeout: "BUILDER_TIMEOUT", Unavailable: "BUILDER_UNAVAILABLE",
		}, fail, errComplexExecutionStopped)
		if err != nil || stopped {
			return false, stopped, err
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, message.TurnID, message.MessageID, message.Text, coreDigest([]byte(message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexExecutionAgentStep(ctx, step)
		return err == nil, false, err
	}
	if step.SendStatus != core.AgentStepSendStatusSettled {
		return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("BUILDER_STEP_UNAVAILABLE"))
	}
	planTask, ok := complexPlanTaskByKey(plan, task.TaskKey)
	if !ok {
		return false, true, errComplexExecutionStopped
	}
	if !exceptionBuilderContinueReady(execution, dispatch.ID) {
		handled, changed, done, handleErr := s.handleComplexExceptionAfterBuilderSettled(ctx, execution, task, dispatch, planTask, builder, step)
		if handleErr != nil || handled {
			return changed, done, handleErr
		}
		result, resultErr := parseRuntimeExecutionBuilderResult(execution.Run, []byte(step.FinalMessageText), dispatch.ID, task.DevelopmentTaskID, dispatch.Round)
		if resultErr != nil {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonCode("BUILDER_RESULT_INVALID"))
		}
		if result.Outcome == "BLOCKED" {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonCode("BUILDER_BLOCKED"))
		}
		if result.Outcome == "NEEDS_HUMAN" {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonHumanDecisionRequired)
		}
	}
	if dispatch.CandidateCommitID == "" {
		inspection, inspectErr := s.inspectOrFreezeMailBuilder(ctx, execution, task, dispatch, record, step)
		if ctx.Err() != nil {
			return false, false, ctx.Err()
		}
		if errors.Is(inspectErr, context.Canceled) {
			return false, false, inspectErr
		}
		if inspectErr != nil || inspection.BaseSHA != dispatch.BaseCommitSHA || !validComplexExecutionCommitSHA(inspection.CandidateSHA) {
			if inspectErr == nil {
				inspectErr = ports.ErrClearDevCandidateInvalid
			}
			s.logger.Error("ClearDev candidate handoff stopped", "dispatchID", dispatch.ID, "error", inspectErr)
			if receiptErr := s.recordHandoffFailure(ctx, execution, task, dispatch, step, inspectErr); receiptErr != nil {
				return false, false, receiptErr
			}
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, errors.Is(inspectErr, ports.ErrClearDevGitUnavailable), gitInspectionReason(inspectErr))
		}
		_, err = s.complexExecution.AppendClearDevComplexExecutionCandidate(ctx, core.AppendComplexExecutionCandidateCommand{ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, Round: dispatch.Round, BaseCommitSHA: dispatch.BaseCommitSHA, Candidate: core.CandidateObservation{ID: s.newID(), DevelopmentTaskID: task.DevelopmentTaskID, AOSessionID: builder.AOSessionID, CommitSHA: inspection.CandidateSHA, ObservedAt: s.now().UTC()}})
		return err == nil, false, err
	}
	inspection, inspectErr := s.inspectExecutionCandidate(ctx, execution.Run, record.Metadata.WorkspacePath, dispatch.BaseCommitSHA)
	if inspectErr != nil || inspection.BaseSHA != dispatch.BaseCommitSHA || inspection.CandidateSHA != dispatch.CandidateCommitSHA {
		if inspectErr == nil {
			inspectErr = ports.ErrClearDevCandidateInvalid
		}
		return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, errors.Is(inspectErr, ports.ErrClearDevGitUnavailable), gitInspectionReason(inspectErr))
	}
	if changed, err := s.ensureComplexGeneratedProof(ctx, execution, task, dispatch, record.Metadata.WorkspacePath); err != nil || changed {
		return changed, false, err
	}
	if changed, err := s.ensureComplexScopeCheck(ctx, execution, task, dispatch, inspection, planTask); err != nil || changed {
		return changed, false, err
	}
	for _, spec := range complexRequiredCheckSpecs(execution, task.ID) {
		if changed, err := s.ensureComplexRequiredCheck(ctx, execution, task, dispatch, record.Metadata.WorkspacePath, spec); err != nil || changed {
			return changed, false, err
		}
	}
	return s.advanceComplexExecutionReview(ctx, execution, planning, task, dispatch, record, inspection)
}

func (s *Service) advanceComplexExecutionReview(ctx context.Context, execution core.ComplexExecutionSnapshot, planning core.ComplexPlanningSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, builder domain.SessionRecord, inspection ports.ClearDevCandidateInspection) (bool, bool, error) {
	review, reviewFound := complexExecutionReviewForDispatch(execution, dispatch.ID)
	reviewer, reviewerFound := complexExecutionReviewerForCandidate(execution, task.ID, dispatch.CandidateCommitID)
	if !reviewerFound {
		reviewer = core.ComplexExecutionRoleBinding{
			ID: s.newID(), ExecutionRunID: execution.Run.ID, Role: core.StandardRoleReviewer,
			TaskMappingID: task.ID, CandidateCommitID: dispatch.CandidateCommitID,
			SessionCreationIdempotencyKey: "cleardev-complex-execution:review:" + dispatch.CandidateCommitID,
			Status:                        core.RoleBindingStatusRequested, RequestedAt: s.now().UTC(),
		}
		prior, _, reuse, reuseErr := mailReviewerReuseSource(execution, task, dispatch)
		if reuseErr != nil {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, "MAIL_REVIEWER_REUSE_UNAVAILABLE")
		}
		if reuse {
			reviewer.ContinuationOfRoleBindingID = prior.ID
			reviewer.SessionCreationIdempotencyKey = mailReviewerReuseKeyPrefix + dispatch.CandidateCommitID
		}
		_, _, err := s.complexExecution.CreateClearDevComplexExecutionRoleBinding(ctx, reviewer)
		return err == nil, false, err
	}
	if reviewer.ExecutionRunID != execution.Run.ID || reviewer.Role != core.StandardRoleReviewer || reviewer.TaskMappingID != task.ID || reviewer.CandidateCommitID != dispatch.CandidateCommitID {
		return false, true, errComplexExecutionStopped
	}
	branch := "cleardev-complex-review-" + dispatch.CandidateCommitID
	if reviewer.ContinuationOfRoleBindingID != "" && strings.HasPrefix(reviewer.SessionCreationIdempotencyKey, "cleardev-workflow-review:") {
		branch = "cleardev-review-retry-" + reviewer.ID
	}
	if reviewer.Status == core.RoleBindingStatusRequested {
		if reviewFound {
			return false, true, errComplexExecutionStopped
		}
		_, _, reuse, reuseErr := mailReviewerReuseSource(execution, task, dispatch)
		if reuseErr != nil {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, "MAIL_REVIEWER_REUSE_UNAVAILABLE")
		}
		if reuse {
			return s.bindMailReviewerReuse(ctx, execution, task, dispatch, reviewer, builder)
		}
		if err := s.inspector.PrepareReviewBranch(ctx, builder.Metadata.WorkspacePath, branch, dispatch.CandidateCommitSHA); err != nil {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEW_BRANCH_UNAVAILABLE"))
		}
		session, blocked, err := s.spawnControlledChatSession(ctx, execution.Run.DevelopmentRequirementID, reviewer.ID, ports.SpawnConfig{ProjectID: builder.ProjectID, Kind: domain.KindWorker, Branch: branch, Prompt: "", RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto}, DisplayName: "ClearDev Complex Reviewer", CreationIdempotencyKey: reviewer.SessionCreationIdempotencyKey})
		if blocked {
			return false, false, nil
		}
		var reviewerErr error
		switch {
		case err != nil:
			reviewerErr = err
		case !s.validComplexExecutionWorker(ctx, session.SessionRecord, reviewer, string(builder.ProjectID)):
			reviewerErr = errors.New("reviewer session does not satisfy its role binding")
		case complexExecutionSessionAlreadyBound(execution, planning, string(session.ID)):
			reviewerErr = errors.New("reviewer session is already bound to another role")
		}
		if reviewerErr != nil {
			s.logger.Error("ClearDev complex Reviewer session unavailable", "bindingID", reviewer.ID, "sessionID", session.ID, "error", reviewerErr)
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEWER_UNAVAILABLE"))
		}
		if _, err = s.complexExecution.BindClearDevComplexExecutionRoleBinding(ctx, reviewer.ID, string(session.ID), session.Metadata.WorkspacePath, dispatch.CandidateCommitSHA, s.now().UTC()); err != nil {
			s.logger.Error("ClearDev complex Reviewer binding failed", "bindingID", reviewer.ID, "sessionID", session.ID, "error", err)
			failed, done, failureErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEWER_UNAVAILABLE"))
			if failureErr != nil {
				return false, false, errors.Join(err, failureErr)
			}
			return failed, done, nil
		}
		return true, false, nil
	}
	if reviewer.Status != core.RoleBindingStatusBound && reviewer.Status != core.RoleBindingStatusEnded {
		return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, reviewer.ReasonCode)
	}
	if !reviewFound {
		if reviewer.Status != core.RoleBindingStatusBound {
			return false, true, errComplexExecutionStopped
		}
		record, recordFound, err := s.ao.GetSession(ctx, domain.SessionID(reviewer.AOSessionID))
		if err != nil || !recordFound || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexReviewWorker(ctx, execution, task, dispatch, record, reviewer, string(builder.ProjectID)) || record.Metadata.WorkspacePath != reviewer.WorkspacePath || record.Metadata.WorkspacePath == builder.Metadata.WorkspacePath {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEWER_UNAVAILABLE"))
		}
		reviewerInspection, inspectErr := s.inspectExecutionCandidate(ctx, execution.Run, record.Metadata.WorkspacePath, dispatch.CandidateCommitSHA)
		if inspectErr != nil || reviewerInspection.BaseSHA != dispatch.CandidateCommitSHA || reviewerInspection.CandidateSHA != dispatch.CandidateCommitSHA || len(reviewerInspection.Paths) != 0 {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEW_WORKTREE_INVALID"))
		}
		reviewID := s.newID()
		packet, packetSHA, _, packetErr := buildComplexExecutionReviewPacket(reviewID, task, dispatch, execution, inspection.Paths)
		if packetErr != nil {
			return false, false, packetErr
		}
		prompt := requestedChecksReviewPrompt(execution.Run, reviewerPrompt(packet, reviewID, dispatch.CandidateCommitID, dispatch.CandidateCommitSHA, packetSHA))
		stepID := s.newID()
		step := core.AgentStep{ID: stepID, RoleBindingID: reviewer.ID, Kind: core.AgentStepLocalReview, RequestID: reviewID, ClientMessageID: "cleardev-complex-execution-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: s.now().UTC()}
		review = core.ComplexExecutionReview{ID: reviewID, ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: dispatch.CandidateCommitID, CandidateCommitSHA: dispatch.CandidateCommitSHA, BaseCommitSHA: dispatch.BaseCommitSHA, ReviewPacketJSON: string(packet), ReviewPacketSHA256: packetSHA, CandidateWorkspacePath: reviewer.WorkspacePath, ReviewerRoleBindingID: reviewer.ID, AgentStepID: step.ID, Status: core.LocalReviewStatusPending, CreatedAt: step.RequestedAt}
		_, _, err = s.complexExecution.CreateClearDevComplexExecutionReview(ctx, core.CreateComplexExecutionReviewCommand{Review: review, ReviewerBinding: reviewer, AgentStep: step})
		return err == nil, false, err
	}
	if review.ReviewerRoleBindingID != reviewer.ID || review.CandidateWorkspacePath != reviewer.WorkspacePath || review.CandidateCommitID != dispatch.CandidateCommitID || review.CandidateCommitSHA != dispatch.CandidateCommitSHA || review.BaseCommitSHA != dispatch.BaseCommitSHA || coreDigest([]byte(review.ReviewPacketJSON)) != review.ReviewPacketSHA256 {
		return false, true, errComplexExecutionStopped
	}
	if review.Status != core.LocalReviewStatusPending {
		if reviewer.Status == core.RoleBindingStatusBound {
			_, err := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, reviewer.ID, core.ReasonNone, s.now().UTC())
			return err == nil, false, err
		}
		if review.Verdict != core.LocalReviewPass {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, review.ReasonCode)
		}
		return s.verifyComplexExecutionCandidate(ctx, execution, task, dispatch, review)
	}
	if reviewer.Status != core.RoleBindingStatusBound {
		return false, true, errComplexExecutionStopped
	}
	record, recordFound, err := s.ao.GetSession(ctx, domain.SessionID(reviewer.AOSessionID))
	if err != nil || !recordFound || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexReviewWorker(ctx, execution, task, dispatch, record, reviewer, string(builder.ProjectID)) || record.Metadata.WorkspacePath != review.CandidateWorkspacePath || record.Metadata.WorkspacePath == builder.Metadata.WorkspacePath {
		return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEWER_UNAVAILABLE"))
	}
	inspection, inspectErr := s.inspectExecutionCandidate(ctx, execution.Run, record.Metadata.WorkspacePath, dispatch.CandidateCommitSHA)
	if inspectErr != nil || inspection.BaseSHA != dispatch.CandidateCommitSHA || inspection.CandidateSHA != dispatch.CandidateCommitSHA || len(inspection.Paths) != 0 {
		return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEW_WORKTREE_INVALID"))
	}
	step, ok := complexExecutionStepByID(execution, review.AgentStepID)
	if !ok || step.RoleBindingID != reviewer.ID || step.Kind != core.AgentStepLocalReview || step.RequestID != review.ID {
		return false, true, errComplexExecutionStopped
	}
	basePrompt := reviewerPrompt([]byte(review.ReviewPacketJSON), review.ID, dispatch.CandidateCommitID, dispatch.CandidateCommitSHA, review.ReviewPacketSHA256)
	prompt := requestedChecksReviewPrompt(execution.Run, basePrompt)
	allowChecks := core.ReviewCheckRequestsAvailable(execution.Run)
	// Preserve pending reviews created before the optional-check protocol.
	if step.PromptSHA256 == coreDigest([]byte(mailRolePrompt(execution.Run, basePrompt))) {
		prompt, allowChecks = mailRolePrompt(execution.Run, basePrompt), false
	}
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return false, true, errComplexExecutionStopped
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.occupyExceptionBudget(ctx, execution, task.ID, core.ComplexExceptionBudgetReviewer, review.ID, step.ID); err != nil {
			if errors.Is(err, errComplexExecutionStopped) {
				return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, "REVIEWER_BUDGET_EXHAUSTED")
			}
			return false, true, err
		}
		if err := s.relayAgentTurn(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, step, reviewer.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return false, false, err
		}
		_, err = s.complexExecution.MarkClearDevComplexExecutionAgentStepSent(ctx, step.ID, s.now().UTC())
		return err == nil, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		validate := func(raw []byte) error {
			return validateInitialReviewReply(raw, allowChecks, execution.Run, review, dispatch)
		}
		fail := func(ctx context.Context, item core.AgentStep, reason core.ReasonCode) error {
			infra := reason == core.ReasonCode("REVIEW_TIMEOUT") || reason == core.ReasonCode("REVIEWER_UNAVAILABLE")
			_, _, err := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, infra, reason)
			return err
		}
		message, stopped, err := s.awaitValidAgentJSON(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, reviewer.AOSessionID, step, prompt, validate, standardStepReasons{
			Invalid: "REVIEW_RESULT_INVALID", Timeout: "REVIEW_TIMEOUT", Unavailable: "REVIEWER_UNAVAILABLE",
		}, fail, errComplexExecutionStopped)
		if err != nil || stopped {
			return false, stopped, err
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, message.TurnID, message.MessageID, message.Text, coreDigest([]byte(message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexExecutionAgentStep(ctx, step)
		return err == nil, false, err
	}
	if step.SendStatus != core.AgentStepSendStatusSettled {
		return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEWER_STEP_UNAVAILABLE"))
	}
	if allowChecks && peekAgentResultKind([]byte(step.FinalMessageText)) == core.ReviewCheckRequestKind {
		return s.advanceRequestedReviewChecks(ctx, execution, task, dispatch, review, reviewer, step)
	}
	diffPaths := complexReviewDiffPaths(review.ReviewPacketJSON)
	result, parseErr := core.ParseComplexExecutionLocalReviewResult([]byte(step.FinalMessageText), review.ID, dispatch.CandidateCommitID, dispatch.CandidateCommitSHA, review.ReviewPacketSHA256, diffPaths)
	if parseErr != nil {
		return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonCode("REVIEW_RESULT_INVALID"))
	}
	_, err = s.complexExecution.SettleClearDevComplexExecutionReview(ctx, core.SettleComplexExecutionReviewCommand{ReviewID: review.ID, TurnID: step.TurnID, FinalMessageID: step.FinalMessageID, Verdict: core.LocalReviewVerdict(result.Verdict), ReasonCode: core.ReasonCode(result.ReasonCode), Summary: result.Summary, At: s.now().UTC()})
	return err == nil, false, err
}

func (s *Service) ensureComplexScopeCheck(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, inspection ports.ClearDevCandidateInspection, plan core.ComplexPlanTask) (bool, error) {
	spec, ok := complexCheckSpec(execution, task.ID, core.CandidateCheckScope, "SCOPE")
	if !ok {
		return false, errComplexExecutionStopped
	}
	run, found := complexCheckRun(execution, dispatch.ID, dispatch.CandidateCommitID, spec.ID)
	if !found {
		run = core.ComplexExecutionCheckRun{ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: dispatch.CandidateCommitID, BaseCommitSHA: dispatch.BaseCommitSHA, CandidateCommitSHA: dispatch.CandidateCommitSHA, CheckSpecFactID: spec.ID, Kind: core.CandidateCheckScope, Argv: []string{}, Status: core.ComplexExecutionCheckRunPending, CreatedAt: s.now().UTC()}
		_, _, err := s.complexExecution.CreateClearDevComplexExecutionCheckRun(ctx, run)
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunPending {
		_, err := s.complexExecution.StartClearDevComplexExecutionCheckRun(ctx, run.ID, s.now().UTC())
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunStarted {
		passed, summary := complexExceptionScopePathsPass(currentExceptionPathRules(execution, task, plan), inspection.Paths, execution, dispatch)
		if passed {
			builder, exists := complexExecutionBindingByID(execution, dispatch.BuilderRoleBindingID)
			if !exists || builder.Role != core.StandardRoleBuilder || builder.WorkspacePath == "" {
				return false, errComplexExecutionStopped
			}
			mailSummary, required, err := s.mailScopeSummary(ctx, execution, dispatch, builder.WorkspacePath)
			if err != nil {
				if !errors.Is(err, ports.ErrClearDevMailScopeViolation) {
					_, settleErr := s.complexExecution.SettleClearDevComplexExecutionCheckRun(ctx, core.SettleComplexExecutionCheckCommand{CheckRunID: run.ID, ReasonCode: "MAIL_SCOPE_CHECKER_UNAVAILABLE", At: s.now().UTC()})
					return settleErr == nil, settleErr
				}
				passed, summary = false, err.Error()
			} else if required {
				summary = mailSummary
			}
		}
		result, code, reason := core.EvidenceResultPass, 0, core.ReasonNone
		if !passed {
			result, code, reason = core.EvidenceResultFail, 1, core.ReasonCode("SCOPE_CHECK_FAILED")
		}
		paths, _ := json.Marshal(inspection.Paths)
		_, err := s.complexExecution.SettleClearDevComplexExecutionCheckRun(ctx, core.SettleComplexExecutionCheckCommand{CheckRunID: run.ID, Result: result, ReasonCode: reason, ExitCode: &code, OutputSummary: summary, OutputSHA256: coreDigest([]byte(summary)), ChangedPathsJSON: string(paths), At: s.now().UTC()})
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunFailed || run.Result == core.EvidenceResultFail {
		changed, _, err := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, run.Status == core.ComplexExecutionCheckRunFailed, run.ReasonCode)
		return changed, err
	}
	return false, nil
}

func (s *Service) ensureComplexRequiredCheck(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, workspace string, spec core.ComplexExecutionCheckSpecFact) (bool, error) {
	run, found := complexCheckRun(execution, dispatch.ID, dispatch.CandidateCommitID, spec.ID)
	if !found {
		run = core.ComplexExecutionCheckRun{ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: dispatch.CandidateCommitID, BaseCommitSHA: dispatch.BaseCommitSHA, CandidateCommitSHA: dispatch.CandidateCommitSHA, CheckSpecFactID: spec.ID, Kind: core.CandidateCheckRequired, Argv: append([]string(nil), spec.Argv...), Status: core.ComplexExecutionCheckRunPending, CreatedAt: s.now().UTC()}
		_, _, err := s.complexExecution.CreateClearDevComplexExecutionCheckRun(ctx, run)
		return err == nil, err
	}
	profile := ""
	if run.Status == core.ComplexExecutionCheckRunPending || run.Status == core.ComplexExecutionCheckRunStarted {
		var err error
		if profile, err = s.checkStoppedCheckBeforeRun(ctx, execution.Run.DevelopmentRequirementID, run.ID); err != nil {
			return false, err
		}
	}
	if run.Status == core.ComplexExecutionCheckRunPending {
		_, err := s.complexExecution.StartClearDevComplexExecutionCheckRun(ctx, run.ID, s.now().UTC())
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunStarted {
		request := complexCandidateCheckRequest(run.ID, workspace, dispatch.CandidateCommitSHA, spec)
		request.ExecutionProfile = profile
		result, runErr := s.runExecutionCandidateCheck(ctx, execution.Run, request, spec.CheckID)
		_, mailPolicy, policyErr := core.MailPolicyFromRun(execution.Run)
		if policyErr != nil {
			runErr = policyErr
		} else if mailPolicy && !validMailRunnerResult(result, dispatch.CandidateCommitSHA) {
			runErr = errors.New("MAIL_REQUIRED_CHECK_BINDING_INVALID")
		}
		_, err := s.complexExecution.SettleClearDevComplexExecutionCheckRun(ctx, complexSettleCheckCommand(run.ID, result, runErr, s.now().UTC()))
		return err == nil, err
	}
	if run.Status == core.ComplexExecutionCheckRunFailed || run.Result == core.EvidenceResultFail {
		infrastructure := run.Status == core.ComplexExecutionCheckRunFailed
		if infrastructure && core.BuilderFirstFailureEnabled(execution.Run) {
			settled, receiptErr := s.workflowCheckSettled(ctx, execution, run.ID)
			// Missing, unreadable or unsettled native evidence keeps the original
			// blocked failure. The historical return path rechecks the same receipt.
			infrastructure = receiptErr != nil || !settled
		}
		changed, _, err := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, infrastructure, run.ReasonCode)
		return changed, err
	}
	return false, nil
}

func (s *Service) failComplexExecutionDispatch(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, infrastructure bool, reason core.ReasonCode) (bool, bool, error) {
	if reason == core.ReasonNone {
		reason = core.ReasonCode("COMPLEX_EXECUTION_FAILED")
	}
	err := s.complexExecution.ApplyClearDevComplexExecutionFailure(ctx, execution.Run.ID, task.ID, dispatch.ID, infrastructure, reason, s.now().UTC())
	return err == nil, false, err
}

func activeComplexExecutionDispatch(snapshot core.ComplexExecutionSnapshot) (core.ComplexExecutionTask, core.ComplexExecutionDispatch, bool) {
	for _, task := range snapshot.Tasks {
		if task.CurrentDispatchID == "" || (task.Status != core.DevelopmentTaskStatusRunning && task.Status != core.DevelopmentTaskStatusReview) {
			continue
		}
		if core.ComplexExecutionTaskVerified(snapshot, task) {
			continue
		}
		for _, dispatch := range snapshot.Dispatches {
			if dispatch.ID == task.CurrentDispatchID && dispatch.ComplexExecutionTaskID == task.ID && dispatch.Round == task.CurrentRound {
				return task, dispatch, true
			}
		}
	}
	return core.ComplexExecutionTask{}, core.ComplexExecutionDispatch{}, false
}

func nextComplexExecutionDispatchTask(snapshot core.ComplexExecutionSnapshot) (core.ComplexExecutionTask, bool) {
	if task, ok := core.NextEligibleComplexStandardTask(snapshot); ok {
		return task, true
	}
	boundedMail := core.BoundedMailAttempts(snapshot.Run)
	for _, task := range snapshot.Tasks {
		// A rework round re-dispatches from the failed attempt: the round to
		// run is the recorded CurrentRound, capped by this task's frozen
		// rework budget plus any human-authorized recovery turns. Bounded mail
		// keeps its own attempt-slot bounds.
		budgetMax := 0
		if snapshot.Exception != nil {
			for _, budget := range snapshot.Exception.Budgets {
				if budget.ComplexExecutionTaskID == task.ID && budget.RoleKind == core.ComplexExceptionBudgetBuilder {
					budgetMax = budget.MaxReworkCount + budget.AuthorizedExtraTurns
				}
			}
		}
		if task.Status == core.DevelopmentTaskStatusRework && ((boundedMail && task.CurrentRound > 0 && task.CurrentRound <= 4) || (!boundedMail && task.CurrentRound >= 1 && task.CurrentRound <= budgetMax)) {
			return task, true
		}
		// Generic executions spend a rework budget before their dispatch row
		// exists, so the started-but-undispatched round shows up as a gap
		// between the two counters.
		if !boundedMail && !core.StoppedCheckRecoveryRetainsReworkCount(snapshot, task) && task.ReworkCount > task.CurrentRound &&
			task.ReworkCount <= budgetMax &&
			(task.Status == core.DevelopmentTaskStatusRework || task.Status == core.DevelopmentTaskStatusReview || task.Status == core.DevelopmentTaskStatusRunning) {
			return task, true
		}
	}
	return core.ComplexExecutionTask{}, false
}

func complexExecutionExistingBuilderPrompt(snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, step core.AgentStep) string {
	historical := []string{complexExecutionReworkFeedbackLegacy(snapshot, task), complexExecutionReworkFeedbackBeforeUnified(snapshot, task)}
	legacyFinal := snapshot
	if !core.BuilderFirstFailureRun(snapshot.Run) && snapshot.FinalReview != nil {
		// The prior historical reader omitted the saved JSON. Preserve already
		// frozen message bytes while new feedback includes the full result.
		final := *snapshot.FinalReview
		final.FailureResultJSON = ""
		legacyFinal.FinalReview = &final
		historical = append(historical, complexExecutionReworkFeedbackBeforeUnified(legacyFinal, task))
	}
	if dispatch.Round > 0 && core.FinalReviewTaskReturned(snapshot, task) {
		// Older manual-return requests froze feedback before CurrentRound
		// advanced. Rebuild those bytes only when their saved hash matches;
		// never rewrite the durable message or authorize another delivery.
		prior := task
		prior.CurrentRound = dispatch.Round - 1
		historical = append(historical, complexExecutionReworkFeedback(snapshot, prior), complexExecutionReworkFeedbackBeforeUnified(snapshot, prior), complexExecutionReworkFeedbackBeforeUnified(legacyFinal, prior))
	}
	return executionBuilderPromptForExistingStep(snapshot.Run, []byte(task.ExecutionPackageJSON), dispatch.ID, task.DevelopmentTaskID, dispatch.Round, complexExecutionReworkFeedback(snapshot, task), step.PromptSHA256, historical...)
}

func complexExecutionReworkFeedback(snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask) string {
	if feedback := builderFirstCheckFeedback(snapshot, task); feedback != "" {
		return mailRolePrompt(snapshot.Run, feedback) + workflowBuilderSupplement(snapshot, task) + builderFirstFinalFeedback(snapshot, task)
	}
	feedback := complexExecutionReworkFeedbackBeforeUnified(snapshot, task)
	if core.BuilderFirstFailureEnabled(snapshot.Run) {
		feedback = strings.Replace(feedback, "用户通过原任务返工入口要求处理下述最终验收缺项。", "控制程序按默认诊断规则交回下述最终验收缺项。", 1)
	}
	return feedback
}

func complexExecutionReworkFeedbackBeforeUnified(snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask) string {
	clip := clipMailCheckFailureOutput
	if core.BuilderFirstFailureRun(snapshot.Run) {
		clip = clipBuilderFailureReceiptOutput
	}
	return mailRolePrompt(snapshot.Run, complexExecutionSavedReworkFeedbackWithClip(snapshot, task, clip)) + workflowBuilderSupplement(snapshot, task) + builderFirstFinalFeedback(snapshot, task)
}

func complexExecutionReworkFeedbackLegacy(snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask) string {
	return mailRolePrompt(snapshot.Run, complexExecutionSavedReworkFeedbackWithClip(snapshot, task, clipMailCheckFailureOutputLegacy))
}

func complexExecutionSavedReworkFeedback(snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask) string {
	return complexExecutionSavedReworkFeedbackWithClip(snapshot, task, clipMailCheckFailureOutput)
}

func complexExecutionSavedReworkFeedbackWithClip(snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, clip func(string) string) string {
	if task.CurrentRound <= 0 {
		return ""
	}
	preferred, fallback := "", ""
	preferredRound, fallbackRound := -1, -1
	for _, dispatch := range snapshot.Dispatches {
		if dispatch.ComplexExecutionTaskID != task.ID || dispatch.Round >= task.CurrentRound {
			continue
		}
		if feedback := mailReplacementReworkFeedback(snapshot, dispatch); feedback != "" && dispatch.Round >= preferredRound {
			preferred, preferredRound = feedback, dispatch.Round
		}
		reason := strings.TrimSpace(string(dispatch.ReasonCode))
		if (reason == "CHECK_FAILED" || (reason == "CHECKER_UNAVAILABLE" && core.BuilderFirstFailureRun(snapshot.Run))) && dispatch.Round >= preferredRound {
			preferred, preferredRound = mailCheckFailureFeedback(snapshot, dispatch, clip), dispatch.Round
		}
		if reason != "" && dispatch.Round >= fallbackRound {
			fallback, fallbackRound = reason, dispatch.Round
		}
	}
	if preferred != "" {
		return preferred
	}
	return fallback
}

func mailCheckFailureFeedback(snapshot core.ComplexExecutionSnapshot, dispatch core.ComplexExecutionDispatch, clip func(string) string) string {
	detail := ""
	for _, run := range snapshot.CheckRuns {
		if run.DispatchID != dispatch.ID || (run.Status != core.ComplexExecutionCheckRunSettled && run.Status != core.ComplexExecutionCheckRunFailed) || (run.Result != core.EvidenceResultFail && run.Status != core.ComplexExecutionCheckRunFailed) || (strings.TrimSpace(string(run.ReasonCode)) != "CHECK_FAILED" && strings.TrimSpace(string(run.ReasonCode)) != "CHECKER_UNAVAILABLE") {
			continue
		}
		if run.Kind == core.CandidateCheckScope {
			continue
		}
		detail = clip(run.OutputSummary)
		if core.BuilderFirstFailureRun(snapshot.Run) {
			detail = "失败检查=" + run.ID + " 候选=" + run.CandidateCommitSHA + " 命令=" + strings.Join(run.Argv, " ") + "\n" + detail
		}
	}
	if detail == "" {
		return "CHECK_FAILED"
	}
	return string(dispatch.ReasonCode) + "\n" + detail
}

func clipMailCheckFailureOutput(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	// Project checks store a receipt envelope, while Builder feedback needs
	// the command output inside it. Clipping the envelope first can hide the
	// actual failure behind its hashes and environment metadata.
	var receipt struct {
		SchemaVersion int     `json:"schemaVersion"`
		Policy        string  `json:"policy"`
		OutputSummary *string `json:"outputSummary"`
	}
	if json.Unmarshal([]byte(summary), &receipt) == nil && receipt.SchemaVersion == 1 && (receipt.Policy == core.ProjectCheckPolicyV1 || receipt.Policy == core.ProjectCheckPolicyV2) && receipt.OutputSummary != nil {
		summary = strings.TrimSpace(*receipt.OutputSummary)
		if summary == "" {
			return ""
		}
	}
	return clipMailCheckFailureOutputLegacy(summary)
}

// The original clipping rule is retained for replaying steps whose prompt
// hash was saved before project receipt output was unpacked.
func clipMailCheckFailureOutputLegacy(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	const limit = 1500
	start := strings.Index(summary, "not ok")
	if start >= 0 {
		summary = summary[start:]
	}
	runes := []rune(summary)
	if len(runes) > limit {
		return string(runes[:limit]) + "\n..."
	}
	return summary
}

func (s *Service) validateComplexExecutionDispatchWorkspace(ctx context.Context, snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, workspace string) error {
	if store, ok := s.complexExecution.(builderReplacementStore); ok {
		h, found, err := store.GetClearDevBuilderReplacementEffectiveBinding(ctx, dispatch.AgentStepID)
		if err != nil {
			return err
		}
		if found {
			if h.Binding.DispatchID != dispatch.ID || h.Binding.TaskID != task.ID || h.NewWorkspacePath != workspace {
				return ports.ErrClearDevCandidateInvalid
			}
			return s.checkBuilderReplacementBeforeSend(ctx, snapshot.Run.DevelopmentRequirementID, dispatch.AgentStepID)
		}
	}
	if err := s.validateInvalidBuilderContinuation(ctx, snapshot, dispatch); err != nil {
		return err
	}
	expectedHead, err := complexExecutionDispatchParent(snapshot, task, dispatch)
	if err != nil {
		return err
	}
	if expectedDigest := workflowPendingWorkingTree(snapshot, dispatch); expectedDigest != "" {
		digest, err := s.workflowWorkingTreeDigest(ctx, snapshot, dispatch, expectedHead, workspace)
		if err != nil {
			return err
		}
		if digest != expectedDigest {
			return ports.ErrClearDevWorkspaceDirty
		}
		return nil
	}
	inspection, err := s.inspectExecutionCandidate(ctx, snapshot.Run, workspace, expectedHead)
	if err != nil {
		return err
	}
	if inspection.BaseSHA != expectedHead || inspection.CandidateSHA != expectedHead || len(inspection.Paths) != 0 {
		return ports.ErrClearDevCandidateInvalid
	}
	return nil
}

func previousDispatchBase(snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask) (string, bool) {
	return previousDispatchRoundBase(snapshot, task, task.CurrentRound-1)
}

func previousDispatchRoundBase(snapshot core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, round int) (string, bool) {
	for _, dispatch := range snapshot.Dispatches {
		if dispatch.ComplexExecutionTaskID == task.ID && dispatch.Round == round && validComplexExecutionCommitSHA(dispatch.BaseCommitSHA) {
			return dispatch.BaseCommitSHA, true
		}
	}
	return "", false
}

func lastFrozenDispatchCandidate(snapshot core.ComplexExecutionSnapshot, taskID string, beforeRound int) (string, bool) {
	bestRound := -1
	sha := ""
	for _, dispatch := range snapshot.Dispatches {
		if dispatch.ComplexExecutionTaskID != taskID || dispatch.Round >= beforeRound || !validComplexExecutionCommitSHA(dispatch.CandidateCommitSHA) {
			continue
		}
		if dispatch.Round >= bestRound {
			bestRound = dispatch.Round
			sha = dispatch.CandidateCommitSHA
		}
	}
	if sha == "" {
		for _, r := range snapshot.WorkflowRecoveries {
			if r.Action != core.RecoveryContinueBuilder || r.TaskID != taskID {
				continue
			}
			for _, d := range snapshot.Dispatches {
				if d.ID == r.DispatchID && d.Round+1 == beforeRound && validComplexExecutionCommitSHA(r.CandidateSHA) {
					sha = r.CandidateSHA
				}
			}
		}
	}
	return sha, sha != ""
}

func complexExecutionBindingByID(snapshot core.ComplexExecutionSnapshot, id string) (core.ComplexExecutionRoleBinding, bool) {
	for _, binding := range snapshot.RoleBindings {
		if binding.ID == id {
			return binding, true
		}
	}
	return core.ComplexExecutionRoleBinding{}, false
}

func complexExecutionStepByID(snapshot core.ComplexExecutionSnapshot, id string) (core.AgentStep, bool) {
	for _, step := range snapshot.AgentSteps {
		if step.ID == id {
			return step, true
		}
	}
	return core.AgentStep{}, false
}

func complexCandidateCheckRequest(runID, workspace, candidateSHA string, spec core.ComplexExecutionCheckSpecFact) ports.ClearDevCheckRequest {
	return ports.ClearDevCheckRequest{
		RunID: runID, WorkspacePath: workspace, CandidateSHA: candidateSHA, Image: core.StandardCandidateCheckImage,
		Argv: append([]string(nil), spec.Argv...), Timeout: time.Duration(spec.TimeoutSeconds) * time.Second,
		MemoryBytes: standardCheckMemoryBytes, PidsLimit: standardCheckPidsLimit, OutputLimit: standardCheckOutputLimit,
	}
}

func complexSettleCheckCommand(runID string, result ports.ClearDevCheckResult, runErr error, at time.Time) core.SettleComplexExecutionCheckCommand {
	command := core.SettleComplexExecutionCheckCommand{CheckRunID: runID, ContainerImageID: result.ImageID, At: at, ChangedPathsJSON: "[]"}
	if runErr != nil || result.Outcome == ports.ClearDevCheckInfraError {
		if runErr != nil {
			result.OutputSummary += "\n" + runErr.Error()
			result.OutputSHA256 = coreDigest([]byte(result.OutputSummary))
		}
		command.ReasonCode = core.ReasonCode("CHECKER_UNAVAILABLE")
		command.OutputSummary, command.OutputSHA256, command.OutputTruncated = result.OutputSummary, result.OutputSHA256, result.OutputTruncated
		return command
	}
	code := result.ExitCode
	command.ExitCode, command.TimedOut, command.OutputSummary, command.OutputSHA256, command.OutputTruncated = &code, result.TimedOut, result.OutputSummary, result.OutputSHA256, result.OutputTruncated
	switch {
	case result.Outcome == ports.ClearDevCheckPass && result.ExitCode == 0 && !result.TimedOut && !result.OutputTruncated:
		command.Result = core.EvidenceResultPass
	case result.TimedOut:
		command.Result = core.EvidenceResultFail
		command.ReasonCode = core.ReasonCode("CHECK_TIMEOUT")
	default:
		command.Result = core.EvidenceResultFail
		command.ReasonCode = core.ReasonCode("CHECK_FAILED")
	}
	return command
}

func complexCheckSpec(snapshot core.ComplexExecutionSnapshot, taskID string, kind core.CandidateCheckKind, checkID string) (core.ComplexExecutionCheckSpecFact, bool) {
	for _, spec := range snapshot.CheckSpecs {
		if spec.ComplexExecutionTaskID == taskID && spec.Kind == kind && spec.CheckID == checkID {
			return spec, true
		}
	}
	return core.ComplexExecutionCheckSpecFact{}, false
}

func complexRequiredCheckSpecs(snapshot core.ComplexExecutionSnapshot, taskID string) []core.ComplexExecutionCheckSpecFact {
	result := []core.ComplexExecutionCheckSpecFact{}
	for _, spec := range snapshot.CheckSpecs {
		if spec.ComplexExecutionTaskID == taskID && spec.Kind == core.CandidateCheckRequired {
			result = append(result, spec)
		}
	}
	return result
}

func complexCheckRun(snapshot core.ComplexExecutionSnapshot, dispatchID, candidateID, specID string) (core.ComplexExecutionCheckRun, bool) {
	found := false
	best := core.ComplexExecutionCheckRun{}
	for _, run := range snapshot.CheckRuns {
		if run.DispatchID == dispatchID && run.CandidateCommitID == candidateID && run.CheckSpecFactID == specID {
			if !found || run.RetryOrdinal >= best.RetryOrdinal {
				best, found = run, true
			}
		}
	}
	return best, found
}

func complexScopePathsPass(plan core.ComplexPlanTask, paths []ports.ClearDevDiffPath) bool {
	rules := core.PathRules{WritePaths: plan.WritePaths, GeneratedPaths: plan.GeneratedPaths, SharedPathsRequireApproval: plan.SharedPathsRequireApproval, ForbiddenPaths: plan.ForbiddenPaths}
	for _, change := range paths {
		for _, path := range []string{change.OldPath, change.Path} {
			if path == "" {
				continue
			}
			classification, err := rules.ClassifyPath(path)
			if err != nil || classification != core.PathAllowed {
				return false
			}
		}
	}
	return true
}

func complexScopeSpecDigest(task core.ComplexPlanTask) string {
	raw, _ := json.Marshal(struct {
		WritePaths                 []string `json:"writePaths"`
		GeneratedPaths             []string `json:"generatedPaths"`
		SharedPathsRequireApproval []string `json:"sharedPathsRequireApproval"`
		ForbiddenPaths             []string `json:"forbiddenPaths"`
	}{task.WritePaths, task.GeneratedPaths, task.SharedPathsRequireApproval, task.ForbiddenPaths})
	return coreDigest(raw)
}

type complexExecutionReviewPacket struct {
	Recovery               *core.WorkflowRecovery        `json:"recovery,omitempty"`
	SchemaVersion          int                           `json:"schemaVersion"`
	ReviewID               string                        `json:"reviewAssignmentId"`
	ExecutionRunID         string                        `json:"executionRunId"`
	RequirementVersionID   string                        `json:"requirementVersionId"`
	RequirementSHA256      string                        `json:"requirementSha256"`
	PlanID                 string                        `json:"planId"`
	PlanSHA256             string                        `json:"planSha256"`
	TaskMappingID          string                        `json:"taskMappingId"`
	TaskID                 string                        `json:"taskId"`
	ExecutionPackage       json.RawMessage               `json:"executionPackage"`
	ExecutionPackageSHA256 string                        `json:"executionPackageSha256"`
	CandidateID            string                        `json:"candidateId"`
	CandidateSHA           string                        `json:"candidateSha"`
	BaseSHA                string                        `json:"baseSha"`
	Diff                   []ports.ClearDevDiffPath      `json:"diff"`
	Checks                 []complexExecutionReviewCheck `json:"checks"`
}

type complexExecutionReviewCheck struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	// OutputSummary carries the trusted executor's complete check log so the
	// Reviewer reads recorded evidence instead of re-running the check. It is
	// bound by OutputSHA256 exactly like every other receipt.
	Result        string `json:"result"`
	OutputSummary string `json:"outputSummary,omitempty"`
	OutputSHA256  string `json:"outputSha256"`
}

func buildComplexExecutionReviewPacket(reviewID string, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, execution core.ComplexExecutionSnapshot, diff []ports.ClearDevDiffPath) ([]byte, string, []string, error) {
	checks := []complexExecutionReviewCheck{}
	for _, run := range execution.CheckRuns {
		if run.DispatchID == dispatch.ID && run.CandidateCommitID == dispatch.CandidateCommitID && run.Status == core.ComplexExecutionCheckRunSettled && run.Result == core.EvidenceResultPass {
			name := "SCOPE"
			if spec, ok := complexCheckSpecByID(execution, run.CheckSpecFactID); ok {
				name = spec.CheckID
			}
			if (run.OutputSummary != "" || run.OutputSHA256 != "") && coreDigest([]byte(run.OutputSummary)) != run.OutputSHA256 {
				return nil, "", nil, fmt.Errorf("check %s output does not match its receipt", run.ID)
			}
			checks = append(checks, complexExecutionReviewCheck{ID: run.ID, Kind: string(run.Kind), Name: name, Result: string(run.Result), OutputSummary: run.OutputSummary, OutputSHA256: run.OutputSHA256})
		}
	}
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Kind != checks[j].Kind {
			return checks[i].Kind < checks[j].Kind
		}
		return checks[i].Name < checks[j].Name
	})
	packet := complexExecutionReviewPacket{
		SchemaVersion: core.ComplexExecutionProtocolVersion, ReviewID: reviewID,
		ExecutionRunID: execution.Run.ID, RequirementVersionID: execution.Run.RequirementVersionID,
		RequirementSHA256: execution.Run.RequirementSHA256, PlanID: execution.Run.PlanID, PlanSHA256: execution.Run.PlanSHA256,
		TaskMappingID: task.ID, TaskID: task.DevelopmentTaskID, ExecutionPackage: json.RawMessage(task.ExecutionPackageJSON),
		ExecutionPackageSHA256: task.ExecutionPackageSHA256, CandidateID: dispatch.CandidateCommitID,
		CandidateSHA: dispatch.CandidateCommitSHA, BaseSHA: dispatch.BaseCommitSHA,
		Diff: append([]ports.ClearDevDiffPath(nil), diff...), Checks: checks,
	}
	for i := range execution.WorkflowRecoveries {
		r := &execution.WorkflowRecoveries[i]
		if r.Action == core.RecoveryRetryReview && r.DispatchID == dispatch.ID {
			binding, found := complexExecutionReviewerForCandidate(execution, task.ID, dispatch.CandidateCommitID)
			if found && binding.ID == r.SuccessorID {
				packet.Recovery = r
			}
		}
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		return nil, "", nil, err
	}
	paths := make([]string, 0, len(diff)*2)
	for _, item := range diff {
		if item.OldPath != "" {
			paths = append(paths, item.OldPath)
		}
		if item.Path != "" {
			paths = append(paths, item.Path)
		}
	}
	return raw, coreDigest(raw), paths, nil
}

func complexCheckSpecByID(snapshot core.ComplexExecutionSnapshot, id string) (core.ComplexExecutionCheckSpecFact, bool) {
	for _, spec := range snapshot.CheckSpecs {
		if spec.ID == id {
			return spec, true
		}
	}
	return core.ComplexExecutionCheckSpecFact{}, false
}

func complexReviewDiffPaths(raw string) []string {
	var packet complexExecutionReviewPacket
	if json.Unmarshal([]byte(raw), &packet) != nil {
		return nil
	}
	paths := make([]string, 0, len(packet.Diff)*2)
	for _, diff := range packet.Diff {
		if diff.OldPath != "" {
			paths = append(paths, diff.OldPath)
		}
		if diff.Path != "" {
			paths = append(paths, diff.Path)
		}
	}
	return paths
}

func (s *Service) verifyComplexExecutionCandidate(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, review core.ComplexExecutionReview) (bool, bool, error) {
	if core.ComplexExecutionTaskVerified(execution, task) {
		return false, false, nil
	}
	scopeID := ""
	required := []string{}
	for _, run := range execution.CheckRuns {
		if run.DispatchID != dispatch.ID || run.CandidateCommitID != dispatch.CandidateCommitID || run.Status != core.ComplexExecutionCheckRunSettled || run.Result != core.EvidenceResultPass {
			continue
		}
		switch run.Kind {
		case core.CandidateCheckScope:
			scopeID = run.ID
		case core.CandidateCheckRequired:
			required = append(required, run.ID)
		}
	}
	if scopeID == "" || len(required) == 0 {
		return false, true, errComplexExecutionStopped
	}
	recoveryID, attemptID, resultID := "", "", ""
	for _, fixed := range execution.FixedRecoveries {
		if fixed.Request.ReviewID == review.ID && fixed.ReviewResult != nil && fixed.ReviewResult.Verdict == core.LocalReviewPass {
			recoveryID = fixed.Request.ID
			attemptID = fixed.ReviewResult.AttemptID
			resultID = fixed.ReviewResult.ResultID
		}
	}
	if _, mailPolicy, policyErr := core.MailPolicyFromRun(execution.Run); policyErr != nil {
		return false, false, policyErr
	} else if mailPolicy {
		workspace := review.CandidateWorkspacePath
		if recoveryID != "" {
			for _, fixed := range execution.FixedRecoveries {
				if fixed.Request.ID == recoveryID && fixed.Result != nil {
					reviewer, found, readErr := s.ao.GetSession(ctx, domain.SessionID(fixed.Result.SessionID))
					if readErr != nil || !found {
						return false, false, errors.New("MAIL_REVIEW_WORKSPACE_UNAVAILABLE")
					}
					workspace = reviewer.Metadata.WorkspacePath
				}
			}
		}
		observed, inspectErr := s.inspectExecutionCandidate(ctx, execution.Run, workspace, dispatch.CandidateCommitSHA)
		if inspectErr != nil || observed.CandidateSHA != dispatch.CandidateCommitSHA || observed.BaseSHA != dispatch.CandidateCommitSHA || len(observed.Paths) != 0 {
			return s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, "MAIL_REVIEW_CANDIDATE_CHANGED")
		}
	}
	err := s.complexExecution.VerifyClearDevComplexExecutionCandidate(ctx, core.VerifyComplexExecutionCandidateCommand{Verification: core.ComplexExecutionVerification{ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: dispatch.CandidateCommitID, CandidateCommitSHA: dispatch.CandidateCommitSHA, Round: dispatch.Round, ScopeEvidenceID: scopeID, RequiredCheckRunIDs: required, LocalReviewID: review.ID, ReplacementRecoveryID: recoveryID, ReplacementAttemptID: attemptID, ReplacementResultID: resultID, VerifiedAt: s.now().UTC()}})
	return err == nil, false, err
}

func complexExecutionReviewForDispatch(snapshot core.ComplexExecutionSnapshot, dispatchID string) (core.ComplexExecutionReview, bool) {
	for _, review := range snapshot.Reviews {
		if review.DispatchID == dispatchID && !workflowRecoveredTarget(snapshot, review.ID) {
			return review, true
		}
	}
	return core.ComplexExecutionReview{}, false
}

func complexExecutionReviewerForCandidate(snapshot core.ComplexExecutionSnapshot, taskID, candidateID string) (core.ComplexExecutionRoleBinding, bool) {
	for _, binding := range snapshot.RoleBindings {
		if binding.Role == core.StandardRoleReviewer && binding.TaskMappingID == taskID && binding.CandidateCommitID == candidateID && !core.WorkflowRecoveryResolvesBinding(snapshot, binding) {
			return binding, true
		}
	}
	return core.ComplexExecutionRoleBinding{}, false
}

func complexExecutionSessionAlreadyBound(snapshot core.ComplexExecutionSnapshot, planning core.ComplexPlanningSnapshot, sessionID string) bool {
	for _, binding := range snapshot.RoleBindings {
		if binding.AOSessionID == sessionID {
			return true
		}
	}
	for _, binding := range planning.RoleBindings {
		if binding.AOSessionID == sessionID {
			return true
		}
	}
	return false
}

func (s *Service) advanceComplexExecutionIntegration(ctx context.Context, execution core.ComplexExecutionSnapshot, projectID string) (bool, bool, error) {
	_, projectExecution, contractErr := core.ProjectContractFromRun(execution.Run)
	if contractErr != nil {
		return false, false, contractErr
	}
	_, marked, policyErr := core.MailPolicyFromRun(execution.Run)
	if policyErr != nil {
		return false, false, policyErr
	}
	if !marked && !projectExecution {
		_, isMail, err := s.mailRequirementBaseline(ctx, core.DevelopmentRequirement{ID: execution.Run.DevelopmentRequirementID, AOProjectID: projectID})
		if err != nil {
			return false, false, err
		}
		if isMail {
			changed, err := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, execution.Run.BuilderRoleBindingID, "MAIL_EXECUTION_POLICY_REQUIRED", s.now().UTC())
			return changed, true, err
		}
	}
	finalTask, finalDispatch, finalVerification, ok := finalComplexExecutionCandidate(execution)
	if !ok {
		return false, true, errComplexExecutionStopped
	}
	builder, ok := complexExecutionBindingByID(execution, execution.Run.BuilderRoleBindingID)
	if !ok || builder.Status != core.RoleBindingStatusBound {
		return false, true, errComplexExecutionStopped
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil || !found || !s.validComplexExecutionWorker(ctx, record, builder, projectID) {
		return false, false, err
	}
	checkIDs := []string{}
	for _, spec := range execution.CheckSpecs {
		if spec.Kind != core.CandidateCheckIntegration {
			continue
		}
		run, found := complexCheckRun(execution, finalDispatch.ID, finalVerification.CandidateCommitID, spec.ID)
		if !found {
			run = core.ComplexExecutionCheckRun{ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: finalTask.ID, DispatchID: finalDispatch.ID, CandidateCommitID: finalVerification.CandidateCommitID, BaseCommitSHA: finalDispatch.BaseCommitSHA, CandidateCommitSHA: finalVerification.CandidateCommitSHA, CheckSpecFactID: spec.ID, Kind: core.CandidateCheckIntegration, Argv: append([]string(nil), spec.Argv...), Status: core.ComplexExecutionCheckRunPending, CreatedAt: s.now().UTC()}
			_, _, err = s.complexExecution.CreateClearDevComplexExecutionCheckRun(ctx, run)
			return err == nil, false, err
		}
		if run.RetryOrdinal == 1 {
			actual := false
			for _, fixed := range execution.FixedRecoveries {
				if fixed.Request.RetryCheckRunID == run.ID && fixed.Result != nil && fixed.Result.Outcome == "PASS" {
					actual = true
				}
			}
			if !actual {
				changed, done, err := s.runFixedInfraRetry(ctx, execution, run, record.Metadata.WorkspacePath, spec)
				return changed, done, err
			}
		}

		if run.Status == core.ComplexExecutionCheckRunPending {
			_, err = s.complexExecution.StartClearDevComplexExecutionCheckRun(ctx, run.ID, s.now().UTC())
			return err == nil, false, err
		}
		if run.Status == core.ComplexExecutionCheckRunStarted {
			result, runErr := s.runMailIntegration(ctx, execution, s.integrationCheckRequest(run.ID, record.Metadata.WorkspacePath, finalVerification.CandidateCommitSHA, spec))
			command := complexSettleCheckCommand(run.ID, result, runErr, s.now().UTC())
			_, err = s.complexExecution.SettleClearDevComplexExecutionCheckRun(ctx, command)
			return err == nil, false, err
		}
		if run.Status == core.ComplexExecutionCheckRunFailed || run.Result != core.EvidenceResultPass {
			if complexExceptionInfraRetryEligible(execution, run) {
				return s.advanceComplexExceptionRecovery(ctx, execution, projectID)
			}
			return false, true, errComplexExecutionStopped
		}
		checkIDs = append(checkIDs, run.ID)
	}
	if len(checkIDs) == 0 {
		return false, true, errComplexExecutionStopped
	}
	if _, mailPolicy, policyErr := core.MailPolicyFromRun(execution.Run); policyErr != nil {
		return false, false, policyErr
	} else if mailPolicy || projectExecution {
		inspection, inspectErr := s.inspectExecutionCandidate(ctx, execution.Run, record.Metadata.WorkspacePath, finalVerification.CandidateCommitSHA)
		if inspectErr != nil || inspection.CandidateSHA != finalVerification.CandidateCommitSHA || len(inspection.Paths) != 0 {
			changed, endErr := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, builder.ID, core.ReasonCode("MAIL_FINAL_CANDIDATE_CHANGED"), s.now().UTC())
			return changed, true, endErr
		}
	}
	if progressed, stopped, ready, reviewErr := s.advanceRequirementFinalReview(ctx, execution, projectID, record.Metadata.WorkspacePath, finalVerification.CandidateCommitSHA, checkIDs); reviewErr != nil || !ready {
		return progressed, stopped, reviewErr
	}
	err = s.complexExecution.CompleteClearDevComplexExecution(ctx, core.CompleteComplexExecutionCommand{ExecutionRunID: execution.Run.ID, Integration: core.ComplexExecutionIntegration{ID: s.newID(), ExecutionRunID: execution.Run.ID, IntegrationCandidateID: "s06-integration-" + execution.Run.ID, CandidateCommitSHA: finalVerification.CandidateCommitSHA, CheckRunIDs: checkIDs, CompletedAt: s.now().UTC()}, At: s.now().UTC()})
	if err != nil {
		return false, false, err
	}
	if err := s.releaseCandidateCheckEnvironment(ctx, execution.Run.ID+":check-preflight"); err != nil {
		return false, false, err
	}
	if stage, found, e := s.productStageSource(ctx, execution.Run.DevelopmentRequirementID); e != nil {
		return false, false, e
	} else if found {
		if plans, ok := s.complex.(productPlanStore); ok {
			a, e := plans.GetClearDevProductPlanAuthorization(ctx, stage.ProductID, stage.DiscussionID)
			if e != nil {
				return false, false, e
			}
			if a != nil && a.Status == "RESOLVED" && a.Decision == "APPROVE" {
				s.scheduleComplexFlow(stage.ProductID)
			}
		}
	}
	return true, false, nil
}

func finalComplexExecutionCandidate(snapshot core.ComplexExecutionSnapshot) (core.ComplexExecutionTask, core.ComplexExecutionDispatch, core.ComplexExecutionVerification, bool) {
	var task core.ComplexExecutionTask
	for _, candidate := range snapshot.Tasks {
		if !core.ComplexExecutionTaskVerified(snapshot, candidate) || candidate.Ordinal < task.Ordinal {
			continue
		}
		task = candidate
	}
	if task.ID == "" {
		return core.ComplexExecutionTask{}, core.ComplexExecutionDispatch{}, core.ComplexExecutionVerification{}, false
	}
	for _, verification := range snapshot.Verifications {
		if verification.ComplexExecutionTaskID != task.ID || verification.DispatchID != task.CurrentDispatchID || verification.Round != task.CurrentRound {
			continue
		}
		for _, dispatch := range snapshot.Dispatches {
			if dispatch.ID == verification.DispatchID {
				return task, dispatch, verification, true
			}
		}
	}
	return core.ComplexExecutionTask{}, core.ComplexExecutionDispatch{}, core.ComplexExecutionVerification{}, false
}

func complexExecutionExpectedRequirementTaskSet(phase core.ComplexExecutionPhase) int64 {
	if phase == core.ComplexExecutionAwaitingSteward || phase == core.ComplexExecutionPreparingTasks {
		return 0
	}
	return core.ComplexStandardTaskSetVersion
}

func approvedComplexExecutionPlan(planning core.ComplexPlanningSnapshot, versionID string) (core.ComplexEngineeringPlan, core.ComplexPlanReview, bool) {
	var best core.ComplexEngineeringPlan
	found := false
	for _, plan := range planning.Plans {
		if plan.RequirementVersionID == versionID && (!found || plan.Version > best.Version) {
			best, found = plan, true
		}
	}
	if !found {
		return core.ComplexEngineeringPlan{}, core.ComplexPlanReview{}, false
	}
	var parsed core.ComplexEngineeringPlanResult
	if json.Unmarshal([]byte(best.PlanJSON), &parsed) == nil && parsed.SchemaVersion == core.PlannerTaskContractVersion {
		if parsed.Kind == "COMPLEX_ENGINEERING_PLAN" {
			if _, admitted := core.PlanValidationForPlan(planning, best); admitted {
				return best, core.ComplexPlanReview{}, true
			}
		}
		return core.ComplexEngineeringPlan{}, core.ComplexPlanReview{}, false
	}
	for _, review := range planning.Reviews {
		if review.PlanID == best.ID && review.Verdict == core.PlanReviewApproved && review.PlanSHA256 == best.PlanSHA256 {
			return best, review, true
		}
	}
	return core.ComplexEngineeringPlan{}, core.ComplexPlanReview{}, false
}

func parseComplexExecutionPlan(plan core.ComplexEngineeringPlan) (core.ComplexEngineeringPlanResult, error) {
	var parsed core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(plan.PlanJSON), &parsed); err != nil {
		return parsed, err
	}
	if coreDigest([]byte(plan.PlanJSON)) != plan.PlanSHA256 ||
		parsed.ParallelSuggestion.RecommendedBuilderCount < 1 ||
		parsed.ParallelSuggestion.RecommendedBuilderCount > core.ComplexParallelMaxBuilders {
		return parsed, errors.New("complex execution requires the saved one-to-three-Builder complex plan")
	}
	for _, task := range parsed.Tasks {
		if err := core.ValidateComplexExecutionGeneratedPaths([]core.ComplexPlanTask{task}); err != nil {
			return parsed, err
		}
	}
	return parsed, nil
}

func complexPlanTaskByKey(plan core.ComplexEngineeringPlanResult, key string) (core.ComplexPlanTask, bool) {
	for _, task := range plan.Tasks {
		if task.Key == key {
			return task, true
		}
	}
	return core.ComplexPlanTask{}, false
}
func executionDependencyIDs(execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask) ([]string, bool) {
	out := make([]string, 0, len(task.DependencyTaskKeys))
	for _, key := range task.DependencyTaskKeys {
		found := false
		for _, item := range execution.Tasks {
			if item.TaskKey == key {
				out = append(out, item.DevelopmentTaskID)
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return out, true
}
func previousVerifiedCandidate(execution core.ComplexExecutionSnapshot, ordinal int) (string, bool) {
	var selected core.ComplexExecutionVerification
	found := false
	for _, task := range execution.Tasks {
		if task.Ordinal >= ordinal || !core.ComplexExecutionTaskVerified(execution, task) {
			continue
		}
		for _, verification := range execution.Verifications {
			if verification.ComplexExecutionTaskID == task.ID && verification.DispatchID == task.CurrentDispatchID && verification.Round == task.CurrentRound && (!found || task.Ordinal > selectedOrdinal(execution, selected.ComplexExecutionTaskID)) {
				selected, found = verification, true
			}
		}
	}
	return selected.CandidateCommitSHA, found
}
func selectedOrdinal(execution core.ComplexExecutionSnapshot, taskID string) int {
	for _, task := range execution.Tasks {
		if task.ID == taskID {
			return task.Ordinal
		}
	}
	return -1
}

func complexExecutionCompositionOutput(execution core.ComplexExecutionSnapshot, commitSHA string) bool {
	for _, composition := range execution.Compositions {
		if composition.Status == core.ComplexExecutionCompositionComposed && composition.OutputCommitSHA == commitSHA {
			return true
		}
	}
	return false
}
