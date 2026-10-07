package cleardev

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// parallelExecutionMaxRounds bounds the parallel advance loop. Waiting rounds
// sleep one poll interval and do not consume durable progress, so the budget
// only needs to exceed the number of facts one run can persist.
const parallelExecutionMaxRounds = 20000

// runComplexParallelExecution drives one PARALLEL run until a durable stop.
// Waiting for Builder or Reviewer replies happens in poll rounds, so sibling
// dispatches of the same wave keep advancing.
func (s *Service) runComplexParallelExecution(ctx context.Context, requirementID string) error {
	for round := 0; round < parallelExecutionMaxRounds; round++ {
		execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		phase, _ := core.DeriveComplexExecutionPhase(execution)
		if phase == core.ComplexExecutionAwaitingSteward || phase == core.ComplexExecutionPreparingTasks {
			progressed, done, err := s.advanceComplexStandardExecution(ctx, requirementID)
			if err != nil {
				if errors.Is(err, errComplexExecutionStopped) {
					return nil
				}
				return err
			}
			if done {
				return nil
			}
			if progressed {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(s.pollInterval):
			}
			continue
		}
		if phase == core.ComplexExecutionBindingBuilder {
			snapshot, found, lookupErr := s.facts.GetClearDevRequirement(ctx, requirementID)
			if lookupErr != nil || !found {
				return lookupErr
			}
			progressed, done, err := s.ensureComplexExecutionBuilder(ctx, execution, snapshot.Requirement.AOProjectID)
			if err != nil {
				if errors.Is(err, errComplexExecutionStopped) {
					return nil
				}
				return err
			}
			if done {
				return nil
			}
			if progressed {
				continue
			}
			return nil
		}
		progressed, waiting, done, err := s.advanceComplexParallelExecution(ctx, execution, "")
		if err != nil {
			if errors.Is(err, errComplexExecutionStopped) {
				return nil
			}
			return err
		}
		if done {
			return nil
		}
		if progressed {
			continue
		}
		if waiting {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(s.pollInterval):
			}
			continue
		}
		return nil
	}
	return errors.New("ClearDev complex PARALLEL execution exceeded bounded advance count")
}

// advanceComplexParallelExecution advances one PARALLEL run by at least one
// durable fact, or reports that it is waiting on external agents. The empty
// projectID triggers a lazy requirement lookup when a worker check needs it.
func (s *Service) advanceComplexParallelExecution(ctx context.Context, execution core.ComplexExecutionSnapshot, projectID string) (progressed, waiting, done bool, err error) {
	if s.direction != nil {
		stopped, stopErr := s.direction.HasActiveClearDevDirectionStop(ctx, execution.Run.RequirementVersionID)
		if stopErr != nil {
			return false, false, false, stopErr
		}
		if stopped {
			return false, false, true, errComplexExecutionStopped
		}
	}
	if projectID == "" {
		snapshot, found, lookupErr := s.facts.GetClearDevRequirement(ctx, execution.Run.DevelopmentRequirementID)
		if lookupErr != nil || !found {
			return false, false, false, lookupErr
		}
		projectID = snapshot.Requirement.AOProjectID
	}
	if handled, changed, stop, err := s.advanceFixedRecovery(ctx, execution); handled {
		return changed, !changed && !stop, stop, err
	}
	if handled, changed, stop, err := s.advancePlannerRuntime(ctx, execution); handled {
		return changed, !changed && !stop, stop, err
	}
	phase, _ := core.DeriveComplexExecutionPhase(execution)
	switch phase {
	case core.ComplexExecutionCompleted, core.ComplexExecutionNeedsHuman, core.ComplexExecutionBlocked:
		return false, false, true, nil
	case core.ComplexExecutionAwaitingSpecialist:
		task, ok := nextComplexExecutionDispatchTask(execution)
		if !ok {
			return false, false, true, errComplexExecutionStopped
		}
		planTask, ok := complexPlanTaskFromExecution(task)
		if !ok {
			return false, false, true, errComplexExecutionStopped
		}
		changed, done, err := s.advanceComplexExceptionSpecialist(ctx, execution, task, planTask, projectID)
		return changed, false, done, err
	case core.ComplexExecutionAwaitingScope:
		planning, found, lookupErr := s.complex.GetClearDevComplexPlanning(ctx, execution.Run.DevelopmentRequirementID)
		if lookupErr != nil || !found {
			return false, false, false, lookupErr
		}
		snapshot, found, lookupErr := s.facts.GetClearDevRequirement(ctx, execution.Run.DevelopmentRequirementID)
		if lookupErr != nil || !found {
			return false, false, false, lookupErr
		}
		version, ok := currentConfirmedVersion(snapshot)
		plan, _, planOK := approvedComplexExecutionPlan(planning, execution.Run.RequirementVersionID)
		if !ok || !planOK {
			return false, false, true, errComplexExecutionStopped
		}
		parsed, err := parseComplexExecutionPlan(plan)
		if err != nil {
			return false, false, true, errComplexExecutionStopped
		}
		changed, done, err := s.advanceComplexExceptionScope(ctx, execution, version, plan, parsed)
		return changed, false, done, err
	case core.ComplexExecutionRecovering:
		changed, done, err := s.advanceComplexExceptionRecovery(ctx, execution, projectID)
		return changed, false, done, err
	case core.ComplexExecutionComposing:
		return s.advanceComplexParallelComposition(ctx, execution)
	case core.ComplexExecutionIntegrating:
		return s.advanceComplexParallelIntegration(ctx, execution, projectID)
	}
	batch, batchFound := currentComplexParallelBatch(execution)
	if !batchFound {
		return false, false, true, errComplexExecutionStopped
	}
	if batch.Status == core.ComplexExecutionBatchPending {
		activated, err := s.activateComplexParallelBatch(ctx, execution, batch)
		return activated, false, false, err
	}
	if batch.Status != core.ComplexExecutionBatchRunning {
		return false, false, true, errComplexExecutionStopped
	}
	dispatched, err := s.dispatchComplexParallelBatchTasks(ctx, execution, batch)
	if err != nil || dispatched {
		return dispatched, false, false, err
	}
	return s.advanceComplexParallelDispatches(ctx, execution, projectID, false)
}

// currentComplexParallelBatch returns the first wave that has not composed.
func currentComplexParallelBatch(execution core.ComplexExecutionSnapshot) (core.ComplexExecutionBatch, bool) {
	batches := append([]core.ComplexExecutionBatch(nil), execution.Batches...)
	sort.SliceStable(batches, func(i, j int) bool { return batches[i].Ordinal < batches[j].Ordinal })
	for _, batch := range batches {
		if batch.Status != core.ComplexExecutionBatchComposed {
			return batch, true
		}
	}
	return core.ComplexExecutionBatch{}, false
}

// activateComplexParallelBatch freezes the wave base: the shared spawn base
// for wave 0, or the previous wave's composed commit.
func (s *Service) activateComplexParallelBatch(ctx context.Context, execution core.ComplexExecutionSnapshot, batch core.ComplexExecutionBatch) (bool, error) {
	var base string
	if batch.Ordinal == 0 {
		builder, ok := complexParallelBuilderBySlot(execution, 1)
		if !ok || builder.Status != core.RoleBindingStatusBound || !validComplexExecutionCommitSHA(builder.BaseCommitSHA) {
			return false, errComplexExecutionStopped
		}
		base = builder.BaseCommitSHA
	} else {
		prior, ok := complexParallelBatchByOrdinal(execution, batch.Ordinal-1)
		if !ok || prior.Status != core.ComplexExecutionBatchComposed {
			return false, errComplexExecutionStopped
		}
		for _, composition := range execution.Compositions {
			if composition.BatchID == prior.ID && composition.Status == core.ComplexExecutionCompositionComposed {
				base = composition.OutputCommitSHA
			}
		}
		if !validComplexExecutionCommitSHA(base) {
			return false, errComplexExecutionStopped
		}
	}
	if err := s.complexExecution.ActivateClearDevComplexExecutionBatch(ctx, execution.Run.ID, batch.ID, base, s.now().UTC()); err != nil {
		return false, err
	}
	return true, nil
}

func complexParallelBuilderBySlot(execution core.ComplexExecutionSnapshot, slot int) (core.ComplexExecutionRoleBinding, bool) {
	bindings, ok := currentComplexExecutionBuilderBindings(execution)
	if !ok || slot < 1 || slot > len(bindings) {
		return core.ComplexExecutionRoleBinding{}, false
	}
	return bindings[slot-1], true
}

func complexParallelBatchByOrdinal(execution core.ComplexExecutionSnapshot, ordinal int) (core.ComplexExecutionBatch, bool) {
	for _, batch := range execution.Batches {
		if batch.Ordinal == ordinal {
			return batch, true
		}
	}
	return core.ComplexExecutionBatch{}, false
}

// dispatchComplexParallelBatchTasks assigns the wave's ready tasks to idle
// builders. A builder whose recorded base differs from the wave base is first
// verified clean on its recorded commit and then detached onto the wave base.
// The stable dispatch and message identities are saved before the send.
func (s *Service) dispatchComplexParallelBatchTasks(ctx context.Context, execution core.ComplexExecutionSnapshot, batch core.ComplexExecutionBatch) (bool, error) {
	if blocked, _ := core.PlannerRuntimeBarrier(execution); blocked {
		return false, nil // Drain existing attempts, but do not fill a new slot.
	}
	if batch.Status != core.ComplexExecutionBatchRunning || batch.CommonBaseSHA == "" {
		return false, errComplexExecutionStopped
	}
	progressed := false
	assigned := map[string]bool{}
	for _, task := range execution.Tasks {
		if !parallelComplexTaskInBatch(batch, task.TaskKey) {
			continue
		}
		if task.Status != core.DevelopmentTaskStatusPlanned && task.Status != core.DevelopmentTaskStatusRework {
			continue
		}
		if task.Status == core.DevelopmentTaskStatusRework {
			maxRound := core.ComplexStandardMaxReworkCount
			if core.BoundedMailAttempts(execution.Run) {
				// The durable mail slot/grant gates own the 3 development,
				// 1 review-repair and 1 human-extra attempt budget.
				maxRound = 4
			}
			if task.CurrentRound < 1 || task.CurrentRound > maxRound {
				return false, errComplexExecutionStopped
			}
		}
		builder, ok := complexParallelIdleBuilder(execution, task, assigned)
		if !ok {
			continue
		}
		if builder.BaseCommitSHA != batch.CommonBaseSHA {
			rebased, err := s.rebaseComplexParallelBuilder(ctx, execution, builder, batch.CommonBaseSHA)
			if err != nil || !rebased {
				return progressed, err
			}
			progressed = true
			assigned[builder.ID] = true
			continue
		}
		planTask, ok := complexPlanTaskFromExecution(task)
		if ok {
			if changed, done, err := s.ensureSpecialistBeforeDispatch(ctx, execution, task, planTask, ""); err != nil || done {
				return progressed, err
			} else if changed {
				return true, nil
			}
		}
		created, err := s.createComplexParallelDispatch(ctx, execution, task, builder, batch)
		if err != nil {
			return progressed, err
		}
		if created {
			assigned[builder.ID] = true
		}
		progressed = progressed || created
	}
	return progressed, nil
}

func parallelComplexTaskInBatch(batch core.ComplexExecutionBatch, taskKey string) bool {
	for _, key := range batch.TaskKeys {
		if key == taskKey {
			return true
		}
	}
	return false
}

// complexParallelIdleBuilder picks the builder for one task: the original
// builder for a rework round, otherwise the first free slot. A builder with an
// active attempt is never given a second task.
func complexParallelIdleBuilder(execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, assigned map[string]bool) (core.ComplexExecutionRoleBinding, bool) {
	reworkBindingID := ""
	if task.Status == core.DevelopmentTaskStatusRework {
		for _, dispatch := range execution.Dispatches {
			if dispatch.ComplexExecutionTaskID == task.ID && dispatch.Round == task.CurrentRound-1 {
				reworkBindingID = dispatch.BuilderRoleBindingID
			}
		}
	}
	active := map[string]bool{}
	for id, busy := range assigned {
		if busy {
			active[id] = true
		}
	}
	for _, dispatch := range execution.Dispatches {
		switch dispatch.Status {
		case core.ComplexExecutionDispatchPending, core.ComplexExecutionDispatchRunning, core.ComplexExecutionDispatchObserved, core.ComplexExecutionDispatchReviewing:
			active[dispatch.BuilderRoleBindingID] = true
		}
	}
	for _, binding := range execution.RoleBindings {
		if binding.Role != core.StandardRoleBuilder || binding.Status != core.RoleBindingStatusBound {
			continue
		}
		if reworkBindingID != "" {
			if binding.ID == reworkBindingID {
				return binding, !active[binding.ID]
			}
			continue
		}
		if !active[binding.ID] {
			return binding, true
		}
	}
	return core.ComplexExecutionRoleBinding{}, false
}

// rebaseComplexParallelBuilder moves one builder worktree onto the wave base.
// The worktree must be clean and parked on the builder's recorded commit; any
// unexpected state stops the run and keeps the directory untouched.
func (s *Service) rebaseComplexParallelBuilder(ctx context.Context, execution core.ComplexExecutionSnapshot, builder core.ComplexExecutionRoleBinding, base string) (bool, error) {
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil {
		return false, err
	}
	if !found || record.IsTerminated || record.Activity.State == domain.ActivityExited || strings.TrimSpace(record.Metadata.WorkspacePath) == "" {
		return false, errComplexExecutionStopped
	}
	expectedHead := builder.BaseCommitSHA
	if candidate, ok := lastVerifiedCandidateForBuilder(execution, builder.ID); ok {
		expectedHead = candidate
	}
	if core.TrustedMailFreeze(execution.Run) {
		err = s.prepareParallelMailBuilderBase(ctx, execution, builder, record, expectedHead, base)
	} else {
		err = s.inspector.PrepareBaseWorkspace(ctx, record.Metadata.WorkspacePath, expectedHead, base)
	}
	if err != nil {
		s.logger.Error("ClearDev complex PARALLEL builder rebase refused", "bindingID", builder.ID, "error", err)
		return false, errComplexExecutionStopped
	}
	if err := s.complexExecution.RebaseClearDevComplexExecutionBuilder(ctx, builder.ID, base); err != nil {
		return false, err
	}
	return true, nil
}

// lastVerifiedCandidateForBuilder returns the current recorded candidate of one
// builder. After a wave, that commit is the worktree HEAD and must match before
// the control plane detaches onto the next composed base.
func lastVerifiedCandidateForBuilder(execution core.ComplexExecutionSnapshot, builderID string) (string, bool) {
	sha := ""
	found := false
	for _, verification := range execution.Verifications {
		for _, dispatch := range execution.Dispatches {
			if dispatch.ID != verification.DispatchID || dispatch.BuilderRoleBindingID != builderID {
				continue
			}
			if validComplexExecutionCommitSHA(verification.CandidateCommitSHA) {
				sha = verification.CandidateCommitSHA
				found = true
			}
		}
	}
	return sha, found
}

func (s *Service) createComplexParallelDispatch(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, builder core.ComplexExecutionRoleBinding, batch core.ComplexExecutionBatch) (bool, error) {
	deps, ok := executionDependencyIDs(execution, task)
	if !ok {
		return false, errComplexExecutionStopped
	}
	if task.ExecutionPackageJSON == "" || coreDigest([]byte(task.ExecutionPackageJSON)) != task.ExecutionPackageSHA256 {
		return false, errComplexExecutionStopped
	}
	pkg, packageErr := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
	if packageErr != nil || pkg.ExecutionRunID != execution.Run.ID || pkg.TaskID != task.DevelopmentTaskID || pkg.Mode != string(execution.Run.Mode) || strings.Join(pkg.DependencyTaskIDs, "\x00") != strings.Join(deps, "\x00") {
		return false, errComplexExecutionStopped
	}
	now := s.now().UTC()
	dispatchID, stepID := s.newID(), s.newID()
	feedback := complexExecutionReworkFeedback(execution, task)
	prompt := executionBuilderPromptForRun(execution.Run, []byte(task.ExecutionPackageJSON), dispatchID, task.DevelopmentTaskID, task.CurrentRound, feedback)
	step := core.AgentStep{ID: stepID, RoleBindingID: builder.ID, Kind: core.ComplexExecutionAgentStepBuilderTask, RequestID: dispatchID, ClientMessageID: "cleardev-complex-execution-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
	dispatch := core.ComplexExecutionDispatch{ID: dispatchID, ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, DevelopmentTaskID: task.DevelopmentTaskID, Round: task.CurrentRound, BaseCommitSHA: batch.CommonBaseSHA, AgentStepID: stepID, ExecutionPackageSHA256: task.ExecutionPackageSHA256, ClientMessageID: step.ClientMessageID, BatchID: batch.ID, BuilderRoleBindingID: builder.ID, Status: core.ComplexExecutionDispatchPending, CreatedAt: now}
	_, created, err := s.complexExecution.CreateClearDevComplexExecutionDispatch(ctx, core.CreateComplexExecutionDispatchCommand{Dispatch: dispatch, AgentStep: step, BatchID: batch.ID})
	return err == nil && created, err
}

// advanceComplexParallelDispatches advances every active dispatch of the
// current wave by at most one durable fact. Waiting on one Builder never
// blocks a sibling; the Reviewer lane stays serialized.
func (s *Service) advanceComplexParallelDispatches(ctx context.Context, execution core.ComplexExecutionSnapshot, projectID string, waiting bool) (bool, bool, bool, error) {
	batch, ok := currentComplexParallelBatch(execution)
	if !ok {
		return false, false, true, errComplexExecutionStopped
	}
	planTasks := complexParallelPlanTasks(execution)
	reviewAdvanced := false
	progressed := false
	for _, task := range execution.Tasks {
		if !parallelComplexTaskInBatch(batch, task.TaskKey) || task.CurrentDispatchID == "" {
			continue
		}
		if task.Status != core.DevelopmentTaskStatusRunning && task.Status != core.DevelopmentTaskStatusReview {
			continue
		}
		if core.ComplexExecutionTaskVerified(execution, task) {
			continue
		}
		dispatch, found := core.ComplexExecutionDispatch{}, false
		for _, candidate := range execution.Dispatches {
			if candidate.ID == task.CurrentDispatchID && candidate.ComplexExecutionTaskID == task.ID {
				dispatch, found = candidate, true
				break
			}
		}
		if !found {
			return false, false, true, errComplexExecutionStopped
		}
		changed, laneWaiting, laneUsed, err := s.advanceComplexParallelDispatchStep(ctx, execution, projectID, task, dispatch, planTasks, !reviewAdvanced)
		if err != nil {
			return progressed, false, false, err
		}
		if laneUsed {
			reviewAdvanced = true
		}
		progressed = progressed || changed
		waiting = waiting || laneWaiting
	}
	return progressed, waiting, false, nil
}

// complexParallelPlanTasks reconstructs the plan-task view from the persisted
// execution packages, so dispatch-time checks never trust a re-read plan row.
func complexParallelPlanTasks(execution core.ComplexExecutionSnapshot) core.ComplexEngineeringPlanResult {
	var plan core.ComplexEngineeringPlanResult
	for _, task := range execution.Tasks {
		pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
		if err != nil {
			continue
		}
		plan.Tasks = append(plan.Tasks, core.ComplexPlanTask{
			Key: pkg.TaskKey, Title: pkg.Title, Objective: pkg.Objective,
			RequirementIDs: pkg.RequirementIDs, AcceptanceIDs: pkg.AcceptanceIDs,
			WritePaths: pkg.WritePaths, GeneratedPaths: pkg.GeneratedPaths,
			SharedPathsRequireApproval: pkg.SharedPathsRequireApproval,
			ForbiddenPaths:             pkg.ForbiddenPaths,
			DependencyKeys:             pkg.DependencyTaskKeys,
		})
	}
	return plan
}

// advanceComplexParallelDispatchStep advances one dispatch through its send,
// candidate, checks, and — when the serialized Reviewer lane is free — its
// review. waiting reports an unsettled agent message.
func (s *Service) advanceComplexParallelDispatchStep(ctx context.Context, execution core.ComplexExecutionSnapshot, projectID string, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, plan core.ComplexEngineeringPlanResult, reviewLaneFree bool) (progressed, waiting, reviewUsed bool, err error) {
	builder, ok := complexExecutionBindingByID(execution, dispatch.BuilderRoleBindingID)
	if !ok || builder.Status != core.RoleBindingStatusBound {
		return false, false, false, errComplexExecutionStopped
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil {
		return false, false, false, err
	}
	if !found || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexExecutionWorker(ctx, record, builder, projectID) {
		changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonBuilderSpawnFailed)
		return changed, false, false, failErr
	}
	step, ok := complexExecutionStepByID(execution, dispatch.AgentStepID)
	if !ok || step.RoleBindingID != builder.ID || step.Kind != core.ComplexExecutionAgentStepBuilderTask || step.RequestID != dispatch.ID {
		return false, false, false, errComplexExecutionStopped
	}
	feedback := complexExecutionReworkFeedback(execution, task)
	historicalFeedback := complexExecutionReworkFeedbackLegacy(execution, task)
	prompt := executionBuilderPromptForExistingStep(execution.Run, []byte(task.ExecutionPackageJSON), dispatch.ID, task.DevelopmentTaskID, dispatch.Round, feedback, step.PromptSHA256, historicalFeedback)
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return false, false, false, errComplexExecutionStopped
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if preflightErr := s.validateComplexExecutionDispatchWorkspace(ctx, execution, task, dispatch, record.Metadata.WorkspacePath); preflightErr != nil {
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, errors.Is(preflightErr, ports.ErrClearDevGitUnavailable), gitInspectionReason(preflightErr))
			return changed, false, false, failErr
		}
		if err := s.occupyExceptionBudget(ctx, execution, task.ID, core.ComplexExceptionBudgetBuilder, dispatch.ID, step.ID); err != nil {
			if errors.Is(err, errComplexExecutionStopped) {
				changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, "BUILDER_BUDGET_EXHAUSTED")
				return changed, false, false, failErr
			}
			return false, false, false, err
		}
		if err := s.relayAgentTurn(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, step, builder.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return false, false, false, err
		}
		_, err = s.complexExecution.MarkClearDevComplexExecutionAgentStepSent(ctx, step.ID, s.now().UTC())
		return err == nil, false, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		var lastRaw []byte
		validate := func(raw []byte) error {
			lastRaw = raw
			return validateRuntimeBuilderOrScopeRequest(raw, execution, dispatch, task)
		}
		polled, pollErr := s.pollValidAgentJSON(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, builder.AOSessionID, step, prompt, validate, core.ReasonCode("BUILDER_TIMEOUT"), core.ReasonCode("BUILDER_UNAVAILABLE"), core.ReasonCode("BUILDER_RESULT_INVALID"))
		if pollErr != nil {
			return false, false, false, pollErr
		}
		if polled.stopped {
			if polled.deliveryUnknown {
				return false, false, true, nil
			}
			reason := polled.failCode
			if reason == core.ReasonCode("BUILDER_RESULT_INVALID") || reason == core.ReasonScopeRequestInvalid {
				reason = builderOrScopeInvalidReason(lastRaw)
			}
			s.logger.Error("ClearDev complex PARALLEL builder step failed", "dispatchID", dispatch.ID, "reason", reason)
			infra := reason == core.ReasonCode("BUILDER_TIMEOUT") || reason == core.ReasonCode("BUILDER_UNAVAILABLE")
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, infra, reason)
			return changed, false, false, failErr
		}
		if !polled.ready {
			return false, true, false, nil
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, polled.message.TurnID, polled.message.MessageID, polled.message.Text, coreDigest([]byte(polled.message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexExecutionAgentStep(ctx, step)
		return err == nil, false, false, err
	}
	if step.SendStatus != core.AgentStepSendStatusSettled {
		changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("BUILDER_STEP_UNAVAILABLE"))
		return changed, false, false, failErr
	}
	planTask, ok := complexPlanTaskByKey(plan, task.TaskKey)
	if !ok {
		return false, false, false, errComplexExecutionStopped
	}
	if !exceptionBuilderContinueReady(execution, dispatch.ID) {
		handled, changed, done, handleErr := s.handleComplexExceptionAfterBuilderSettled(ctx, execution, task, dispatch, planTask, builder, step)
		if handleErr != nil || handled {
			return changed, false, done, handleErr
		}
		result, resultErr := parseRuntimeExecutionBuilderResult(execution.Run, []byte(step.FinalMessageText), dispatch.ID, task.DevelopmentTaskID, dispatch.Round)
		if resultErr != nil {
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonCode("BUILDER_RESULT_INVALID"))
			return changed, false, false, failErr
		}
		if result.Outcome == "BLOCKED" {
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonCode("BUILDER_BLOCKED"))
			return changed, false, false, failErr
		}
		if result.Outcome == "NEEDS_HUMAN" {
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonHumanDecisionRequired)
			return changed, false, false, failErr
		}
	}
	var inspection ports.ClearDevCandidateInspection
	if dispatch.CandidateCommitID == "" {
		inspection, err = s.inspectOrFreezeMailBuilder(ctx, execution, task, dispatch, record, step)
		if err != nil || inspection.BaseSHA != dispatch.BaseCommitSHA || !validComplexExecutionCommitSHA(inspection.CandidateSHA) {
			if err == nil {
				err = ports.ErrClearDevCandidateInvalid
			}
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, errors.Is(err, ports.ErrClearDevGitUnavailable), gitInspectionReason(err))
			return changed, false, false, failErr
		}
		_, err = s.complexExecution.AppendClearDevComplexExecutionCandidate(ctx, core.AppendComplexExecutionCandidateCommand{ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, Round: dispatch.Round, BaseCommitSHA: dispatch.BaseCommitSHA, Candidate: core.CandidateObservation{ID: s.newID(), DevelopmentTaskID: task.DevelopmentTaskID, AOSessionID: builder.AOSessionID, CommitSHA: inspection.CandidateSHA, ObservedAt: s.now().UTC()}})
		return err == nil, false, false, err
	}
	inspection, err = s.inspector.InspectCandidate(ctx, record.Metadata.WorkspacePath, dispatch.BaseCommitSHA)
	if err != nil || inspection.BaseSHA != dispatch.BaseCommitSHA || inspection.CandidateSHA != dispatch.CandidateCommitSHA {
		if err == nil {
			err = ports.ErrClearDevCandidateInvalid
		}
		changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, errors.Is(err, ports.ErrClearDevGitUnavailable), gitInspectionReason(err))
		return changed, false, false, failErr
	}
	if changed, err := s.ensureComplexGeneratedProof(ctx, execution, task, dispatch, record.Metadata.WorkspacePath); err != nil || changed {
		return changed, false, false, err
	}
	if changed, err := s.ensureComplexScopeCheck(ctx, execution, task, dispatch, inspection, planTask); err != nil || changed {
		return changed, false, false, err
	}
	for _, spec := range complexRequiredCheckSpecs(execution, task.ID) {
		if changed, err := s.ensureComplexRequiredCheck(ctx, execution, task, dispatch, record.Metadata.WorkspacePath, spec); err != nil || changed {
			return changed, false, false, err
		}
	}
	if !reviewLaneFree {
		return false, true, false, nil
	}
	changed, waiting, reviewErr := s.advanceComplexParallelReview(ctx, execution, task, dispatch, record, inspection)
	_, reviewerFound := complexExecutionReviewerForCandidate(execution, task.ID, dispatch.CandidateCommitID)
	// A sibling that is only queued behind the serialized Reviewer must not
	// occupy the lane. Otherwise an earlier task that became ready later
	// hides an in-flight review and that review is never polled.
	return changed, waiting, reviewerFound || changed, reviewErr
}

// advanceComplexParallelReview is the serialized Reviewer lane. Every stable
// fact is saved before its external action and a sent message is only polled,
// never awaited.
func (s *Service) advanceComplexParallelReview(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, builder domain.SessionRecord, inspection ports.ClearDevCandidateInspection) (progressed, waiting bool, err error) {
	review, reviewFound := complexExecutionReviewForDispatch(execution, dispatch.ID)
	reviewer, reviewerFound := complexExecutionReviewerForCandidate(execution, task.ID, dispatch.CandidateCommitID)
	if !reviewerFound {
		reviewer = core.ComplexExecutionRoleBinding{
			ID: s.newID(), ExecutionRunID: execution.Run.ID, Role: core.StandardRoleReviewer,
			TaskMappingID: task.ID, CandidateCommitID: dispatch.CandidateCommitID,
			SessionCreationIdempotencyKey: "cleardev-complex-execution:review:" + dispatch.CandidateCommitID,
			Status:                        core.RoleBindingStatusRequested, RequestedAt: s.now().UTC(),
		}
		if activeReviewerBinding(execution) != "" {
			return false, true, nil
		}
		_, _, err := s.complexExecution.CreateClearDevComplexExecutionRoleBinding(ctx, reviewer)
		return err == nil, false, err
	}
	if reviewer.ExecutionRunID != execution.Run.ID || reviewer.Role != core.StandardRoleReviewer || reviewer.TaskMappingID != task.ID || reviewer.CandidateCommitID != dispatch.CandidateCommitID {
		return false, false, errComplexExecutionStopped
	}
	branch := "cleardev-complex-review-" + dispatch.CandidateCommitID
	if reviewer.Status == core.RoleBindingStatusRequested {
		if reviewFound {
			return false, false, errComplexExecutionStopped
		}
		if err := s.inspector.PrepareReviewBranch(ctx, builder.Metadata.WorkspacePath, branch, dispatch.CandidateCommitSHA); err != nil {
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEW_BRANCH_UNAVAILABLE"))
			return changed, false, failErr
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
		}
		if reviewerErr != nil {
			s.logger.Error("ClearDev complex Reviewer session unavailable", "bindingID", reviewer.ID, "sessionID", session.ID, "error", reviewerErr)
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEWER_UNAVAILABLE"))
			return changed, false, failErr
		}
		if _, err = s.complexExecution.BindClearDevComplexExecutionRoleBinding(ctx, reviewer.ID, string(session.ID), session.Metadata.WorkspacePath, dispatch.CandidateCommitSHA, s.now().UTC()); err != nil {
			s.logger.Error("ClearDev complex Reviewer binding failed", "bindingID", reviewer.ID, "sessionID", session.ID, "error", err)
			return false, false, err
		}
		return true, false, nil
	}
	if reviewer.Status != core.RoleBindingStatusBound && reviewer.Status != core.RoleBindingStatusEnded {
		changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, reviewer.ReasonCode)
		return changed, false, failErr
	}
	if !reviewFound {
		if reviewer.Status != core.RoleBindingStatusBound {
			return false, false, errComplexExecutionStopped
		}
		record, recordFound, err := s.ao.GetSession(ctx, domain.SessionID(reviewer.AOSessionID))
		if err != nil || !recordFound || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexExecutionWorker(ctx, record, reviewer, string(builder.ProjectID)) || record.Metadata.WorkspacePath != reviewer.WorkspacePath || record.Metadata.WorkspacePath == builder.Metadata.WorkspacePath {
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEWER_UNAVAILABLE"))
			return changed, false, failErr
		}
		reviewerInspection, inspectErr := s.inspector.InspectCandidate(ctx, record.Metadata.WorkspacePath, dispatch.CandidateCommitSHA)
		if inspectErr != nil || reviewerInspection.BaseSHA != dispatch.CandidateCommitSHA || reviewerInspection.CandidateSHA != dispatch.CandidateCommitSHA || len(reviewerInspection.Paths) != 0 {
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEW_WORKTREE_INVALID"))
			return changed, false, failErr
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
		return false, false, errComplexExecutionStopped
	}
	if review.Status != core.LocalReviewStatusPending {
		if reviewer.Status == core.RoleBindingStatusBound {
			_, err := s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, reviewer.ID, core.ReasonNone, s.now().UTC())
			return err == nil, false, err
		}
		if review.Verdict != core.LocalReviewPass {
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, review.ReasonCode)
			return changed, false, failErr
		}
		changed, _, verifyErr := s.verifyComplexExecutionCandidate(ctx, execution, task, dispatch, review)
		return changed, false, verifyErr
	}
	if reviewer.Status != core.RoleBindingStatusBound {
		return false, false, errComplexExecutionStopped
	}
	record, recordFound, err := s.ao.GetSession(ctx, domain.SessionID(reviewer.AOSessionID))
	if err != nil || !recordFound || record.IsTerminated || record.Activity.State == domain.ActivityExited || !s.validComplexExecutionWorker(ctx, record, reviewer, string(builder.ProjectID)) || record.Metadata.WorkspacePath != review.CandidateWorkspacePath || record.Metadata.WorkspacePath == builder.Metadata.WorkspacePath {
		changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEWER_UNAVAILABLE"))
		return changed, false, failErr
	}
	reviewerInspection, inspectErr := s.inspector.InspectCandidate(ctx, record.Metadata.WorkspacePath, dispatch.CandidateCommitSHA)
	if inspectErr != nil || reviewerInspection.BaseSHA != dispatch.CandidateCommitSHA || reviewerInspection.CandidateSHA != dispatch.CandidateCommitSHA || len(reviewerInspection.Paths) != 0 {
		changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEW_WORKTREE_INVALID"))
		return changed, false, failErr
	}
	step, ok := complexExecutionStepByID(execution, review.AgentStepID)
	if !ok || step.RoleBindingID != reviewer.ID || step.Kind != core.AgentStepLocalReview || step.RequestID != review.ID {
		return false, false, errComplexExecutionStopped
	}
	prompt := requestedChecksReviewPrompt(execution.Run, reviewerPrompt([]byte(review.ReviewPacketJSON), review.ID, dispatch.CandidateCommitID, dispatch.CandidateCommitSHA, review.ReviewPacketSHA256))
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return false, false, errComplexExecutionStopped
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.occupyExceptionBudget(ctx, execution, task.ID, core.ComplexExceptionBudgetReviewer, review.ID, step.ID); err != nil {
			return false, false, err
		}
		if err := s.relayAgentTurn(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, step, reviewer.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return false, false, err
		}
		_, err = s.complexExecution.MarkClearDevComplexExecutionAgentStepSent(ctx, step.ID, s.now().UTC())
		return err == nil, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		_, mail, _ := core.MailPolicyFromRun(execution.Run)
		validate := func(raw []byte) error {
			return validateInitialReviewReply(raw, mail, execution.Run, review, dispatch)
		}
		polled, pollErr := s.pollValidAgentJSON(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryComplexExecution, reviewer.AOSessionID, step, prompt, validate, core.ReasonCode("REVIEW_TIMEOUT"), core.ReasonCode("REVIEWER_UNAVAILABLE"), core.ReasonCode("REVIEW_RESULT_INVALID"))
		if pollErr != nil {
			return false, false, pollErr
		}
		if polled.stopped {
			if polled.deliveryUnknown {
				return false, true, nil
			}
			s.logger.Error("ClearDev complex PARALLEL reviewer step failed", "reviewID", review.ID, "reason", polled.failCode)
			infra := polled.failCode == core.ReasonCode("REVIEW_TIMEOUT") || polled.failCode == core.ReasonCode("REVIEWER_UNAVAILABLE")
			changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, infra, polled.failCode)
			return changed, false, failErr
		}
		if !polled.ready {
			return false, true, nil
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, polled.message.TurnID, polled.message.MessageID, polled.message.Text, coreDigest([]byte(polled.message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexExecutionAgentStep(ctx, step)
		return err == nil, false, err
	}
	if step.SendStatus != core.AgentStepSendStatusSettled {
		changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, true, core.ReasonCode("REVIEWER_STEP_UNAVAILABLE"))
		return changed, false, failErr
	}
	if peekAgentResultKind([]byte(step.FinalMessageText)) == core.ReviewCheckRequestKind {
		changed, done, checkErr := s.advanceRequestedReviewChecks(ctx, execution, task, dispatch, review, reviewer, step)
		return changed, !changed && !done, checkErr
	}
	diffPaths := complexReviewDiffPaths(review.ReviewPacketJSON)
	result, parseErr := core.ParseComplexExecutionLocalReviewResult([]byte(step.FinalMessageText), review.ID, dispatch.CandidateCommitID, dispatch.CandidateCommitSHA, review.ReviewPacketSHA256, diffPaths)
	if parseErr != nil {
		changed, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonCode("REVIEW_RESULT_INVALID"))
		return changed, false, failErr
	}
	_, err = s.complexExecution.SettleClearDevComplexExecutionReview(ctx, core.SettleComplexExecutionReviewCommand{ReviewID: review.ID, TurnID: step.TurnID, FinalMessageID: step.FinalMessageID, Verdict: core.LocalReviewVerdict(result.Verdict), ReasonCode: core.ReasonCode(result.ReasonCode), Summary: result.Summary, At: s.now().UTC()})
	return err == nil, false, err
}

func activeReviewerBinding(execution core.ComplexExecutionSnapshot) string {
	for _, binding := range execution.RoleBindings {
		if binding.Role == core.StandardRoleReviewer && (binding.Status == core.RoleBindingStatusRequested || binding.Status == core.RoleBindingStatusBound) {
			return binding.ID
		}
	}
	return ""
}

// pollComplexAgentMessage checks once, without waiting, whether the exact
// automation message has a completed turn. It fails when the step timed out or
// the bound session died; otherwise settled reports the completed message.
func (s *Service) pollComplexAgentMessage(ctx context.Context, sessionID string, step core.AgentStep, prompt string) (standardMessage, bool, error) {
	snapshot, err := s.chat.Snapshot(ctx, domain.SessionID(sessionID))
	if err != nil {
		return standardMessage{}, false, err
	}
	message, stateErr := exactAgentMessage(snapshot, sessionID, step.ClientMessageID, prompt)
	if stateErr == nil {
		return message, true, nil
	}
	if !errors.Is(stateErr, errAgentMessagePending) {
		return standardMessage{}, false, stateErr
	}
	if step.SentAt != nil && s.now().Sub(*step.SentAt) > s.stepTimeout {
		return standardMessage{}, false, errComplexAgentStepTimeout
	}
	record, found, readErr := s.ao.GetSession(ctx, domain.SessionID(sessionID))
	if readErr == nil && (!found || record.IsTerminated || record.Activity.State == domain.ActivityExited) {
		return standardMessage{}, false, errAgentSessionLost
	}
	return standardMessage{}, false, nil
}

// advanceComplexParallelComposition stores the stable combine request, runs
// the controlled Git combine, and settles the outcome. A conflict blocks the
// whole run and keeps every candidate and workspace.
func (s *Service) advanceComplexParallelComposition(ctx context.Context, execution core.ComplexExecutionSnapshot) (bool, bool, bool, error) {
	batch, ok := currentComplexParallelBatch(execution)
	if !ok {
		return false, false, true, errComplexExecutionStopped
	}
	var composition *core.ComplexExecutionComposition
	for index := range execution.Compositions {
		if execution.Compositions[index].BatchID == batch.ID {
			composition = &execution.Compositions[index]
		}
	}
	if composition == nil {
		if batch.Status != core.ComplexExecutionBatchRunning {
			return false, false, true, errComplexExecutionStopped
		}
		request, buildErr := buildComplexParallelCompositionRequest(execution, batch, s.newID(), s.now().UTC())
		if buildErr != nil {
			return false, false, true, buildErr
		}
		if err := s.complexExecution.RecordClearDevComplexExecutionComposition(ctx, request); err != nil {
			return false, false, false, err
		}
		return true, false, false, nil
	}
	if batch.Status != core.ComplexExecutionBatchComposing {
		return false, false, true, errComplexExecutionStopped
	}
	switch composition.Status {
	case core.ComplexExecutionCompositionPending:
		if err := s.complexExecution.StartClearDevComplexExecutionComposition(ctx, composition.ID); err != nil {
			return false, false, false, err
		}
		composition.Status = core.ComplexExecutionCompositionRunning
		return s.runComplexParallelComposition(ctx, execution, batch, *composition)
	case core.ComplexExecutionCompositionRunning:
		return s.runComplexParallelComposition(ctx, execution, batch, *composition)
	default:
		return false, false, true, errComplexExecutionStopped
	}
}

func buildComplexParallelCompositionRequest(execution core.ComplexExecutionSnapshot, batch core.ComplexExecutionBatch, compositionID string, at time.Time) (core.ComplexExecutionComposition, error) {
	request := core.ComplexExecutionComposition{
		ID: compositionID, ExecutionRunID: execution.Run.ID, BatchID: batch.ID,
		RequestID:          "cleardev-complex-composition-" + compositionID,
		InputBaseSHA:       batch.CommonBaseSHA,
		InputCandidateIDs:  []string{},
		InputCandidateSHAs: []string{},
		Status:             core.ComplexExecutionCompositionPending,
		CreatedAt:          at,
	}
	for _, key := range batch.TaskKeys {
		task, taskOK := complexExecutionTaskByKey(execution.Tasks, key)
		if !taskOK || !core.ComplexExecutionTaskVerified(execution, task) {
			return request, errComplexExecutionStopped
		}
		matched := false
		for _, verification := range execution.Verifications {
			if verification.ComplexExecutionTaskID == task.ID && verification.DispatchID == task.CurrentDispatchID && verification.Round == task.CurrentRound {
				request.InputCandidateIDs = append(request.InputCandidateIDs, verification.CandidateCommitID)
				request.InputCandidateSHAs = append(request.InputCandidateSHAs, verification.CandidateCommitSHA)
				matched = true
			}
		}
		if !matched {
			return request, errComplexExecutionStopped
		}
	}
	if len(request.InputCandidateIDs) != len(batch.TaskKeys) {
		return request, errComplexExecutionStopped
	}
	return request, nil
}

func (s *Service) runComplexParallelComposition(ctx context.Context, execution core.ComplexExecutionSnapshot, batch core.ComplexExecutionBatch, composition core.ComplexExecutionComposition) (bool, bool, bool, error) {
	builder, ok := complexParallelBuilderBySlot(execution, 1)
	if !ok || builder.Status != core.RoleBindingStatusBound {
		return false, false, true, errComplexExecutionStopped
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil {
		return false, false, false, err
	}
	if !found || strings.TrimSpace(record.Metadata.WorkspacePath) == "" {
		return false, false, true, errComplexExecutionStopped
	}
	policy, _, _, policyErr := core.MailDeliveryPolicyFromRun(execution.Run)
	if policyErr != nil {
		return false, false, false, policyErr
	}
	result, composeErr := s.inspector.ComposeCandidates(ctx, ports.ClearDevComposeRequest{
		RequestID: composition.RequestID, RepoPath: record.Metadata.WorkspacePath,
		BaseSHA: composition.InputBaseSHA, CandidateSHAs: append([]string(nil), composition.InputCandidateSHAs...),
		CompleteTaskDeltas: policy == core.MailDeliveryPolicyV2,
	})
	settled := core.ComplexExecutionComposition{
		ID: composition.ID, ExecutionRunID: composition.ExecutionRunID, BatchID: composition.BatchID,
		RequestID: composition.RequestID, InputBaseSHA: composition.InputBaseSHA,
		InputCandidateIDs:  append([]string(nil), composition.InputCandidateIDs...),
		InputCandidateSHAs: append([]string(nil), composition.InputCandidateSHAs...),
		CreatedAt:          s.now().UTC(),
	}
	switch {
	case composeErr == nil:
		settled.Status = core.ComplexExecutionCompositionComposed
		settled.WorkspacePath = result.WorkspacePath
		settled.OutputCommitSHA = result.OutputSHA
	case errors.Is(composeErr, ports.ErrClearDevCompositionConflict):
		settled.Status = core.ComplexExecutionCompositionBlocked
		settled.ConflictPaths = result.ConflictPaths
		settled.ReasonCode = core.ReasonCompositionConflict
	default:
		settled.Status = core.ComplexExecutionCompositionFailed
		settled.ReasonCode = core.ReasonCompositionInvalid
		s.logger.Error("ClearDev complex PARALLEL composition failed", "runID", execution.Run.ID, "batchID", batch.ID, "error", composeErr)
	}
	if err := s.complexExecution.SettleClearDevComplexExecutionComposition(ctx, settled); err != nil {
		return false, false, false, err
	}
	return true, false, settled.Status != core.ComplexExecutionCompositionComposed, nil
}

// advanceComplexParallelIntegration runs every frozen integration check on
// the final composed commit inside the managed composition worktree, then
// completes the run in one transaction.
func (s *Service) advanceComplexParallelIntegration(ctx context.Context, execution core.ComplexExecutionSnapshot, projectID string) (bool, bool, bool, error) {
	composition, ok := core.ComplexExecutionFinalComposition(execution)
	if !ok {
		return false, false, true, errComplexExecutionStopped
	}
	finalTask := core.ComplexExecutionTask{}
	for _, task := range execution.Tasks {
		if core.ComplexExecutionTaskVerified(execution, task) && (finalTask.ID == "" || task.Ordinal > finalTask.Ordinal) {
			finalTask = task
		}
	}
	if finalTask.ID == "" {
		return false, false, true, errComplexExecutionStopped
	}
	finalDispatch := core.ComplexExecutionDispatch{}
	finalVerification := core.ComplexExecutionVerification{}
	for _, dispatch := range execution.Dispatches {
		if dispatch.ID == finalTask.CurrentDispatchID {
			finalDispatch = dispatch
		}
	}
	for _, verification := range execution.Verifications {
		if verification.ComplexExecutionTaskID == finalTask.ID && verification.DispatchID == finalTask.CurrentDispatchID {
			finalVerification = verification
		}
	}
	if finalDispatch.ID == "" || finalVerification.CandidateCommitID == "" {
		return false, false, true, errComplexExecutionStopped
	}
	checkIDs := []string{}
	for _, spec := range execution.CheckSpecs {
		if spec.Kind != core.CandidateCheckIntegration {
			continue
		}
		run, found := complexCheckRun(execution, finalDispatch.ID, finalVerification.CandidateCommitID, spec.ID)
		if !found {
			run = core.ComplexExecutionCheckRun{ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: finalTask.ID, DispatchID: finalDispatch.ID, CandidateCommitID: finalVerification.CandidateCommitID, BaseCommitSHA: composition.InputBaseSHA, CandidateCommitSHA: composition.OutputCommitSHA, CheckSpecFactID: spec.ID, Kind: core.CandidateCheckIntegration, Argv: append([]string(nil), spec.Argv...), Status: core.ComplexExecutionCheckRunPending, CreatedAt: s.now().UTC()}
			_, _, err := s.complexExecution.CreateClearDevComplexExecutionCheckRun(ctx, run)
			return err == nil, false, false, err
		}
		if run.RetryOrdinal == 1 {
			actual := false
			for _, fixed := range execution.FixedRecoveries {
				if fixed.Request.RetryCheckRunID == run.ID && fixed.Result != nil && fixed.Result.Outcome == "PASS" {
					actual = true
				}
			}
			if !actual {
				changed, done, err := s.runFixedInfraRetry(ctx, execution, run, composition.WorkspacePath, spec)
				return changed, false, done, err
			}
		}

		if run.Status == core.ComplexExecutionCheckRunPending {
			_, err := s.complexExecution.StartClearDevComplexExecutionCheckRun(ctx, run.ID, s.now().UTC())
			return err == nil, false, false, err
		}
		if run.Status == core.ComplexExecutionCheckRunStarted {
			request := s.integrationCheckRequest(run.ID, composition.WorkspacePath, composition.OutputCommitSHA, spec)
			result, runErr := s.runMailIntegration(ctx, execution, request)
			command := complexSettleCheckCommand(run.ID, result, runErr, s.now().UTC())
			if result.OutputSummary != "" {
				command.OutputSummary = result.OutputSummary
			}
			_, err := s.complexExecution.SettleClearDevComplexExecutionCheckRun(ctx, command)
			return err == nil, false, false, err
		}
		if run.Status == core.ComplexExecutionCheckRunFailed || run.Result != core.EvidenceResultPass {
			if complexExceptionInfraRetryEligible(execution, run) {
				snapshot, found, lookupErr := s.facts.GetClearDevRequirement(ctx, execution.Run.DevelopmentRequirementID)
				if lookupErr != nil || !found {
					return false, false, false, lookupErr
				}
				changed, done, err := s.advanceComplexExceptionRecovery(ctx, execution, snapshot.Requirement.AOProjectID)
				return changed, false, done, err
			}
			return false, false, true, errComplexExecutionStopped
		}
		checkIDs = append(checkIDs, run.ID)
	}
	if len(checkIDs) == 0 {
		return false, false, true, errComplexExecutionStopped
	}
	if progressed, stopped, ready, reviewErr := s.advanceRequirementFinalReview(ctx, execution, projectID, composition.WorkspacePath, composition.OutputCommitSHA, checkIDs); reviewErr != nil || !ready {
		return progressed, !progressed && !stopped, stopped, reviewErr
	}
	err := s.complexExecution.CompleteClearDevComplexExecution(ctx, core.CompleteComplexExecutionCommand{ExecutionRunID: execution.Run.ID, Integration: core.ComplexExecutionIntegration{ID: s.newID(), ExecutionRunID: execution.Run.ID, IntegrationCandidateID: "s07-integration-" + execution.Run.ID, CandidateCommitSHA: composition.OutputCommitSHA, CheckRunIDs: checkIDs, CompletedAt: s.now().UTC()}, At: s.now().UTC()})
	if err != nil {
		return false, false, false, err
	}
	if err := s.releaseCandidateCheckEnvironment(ctx, execution.Run.ID+":check-preflight"); err != nil {
		return false, false, false, err
	}
	return true, false, false, nil
}
