package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func exceptionBudgetID(execution core.ComplexExecutionSnapshot, taskID, roleKind string) string {
	if execution.Exception == nil {
		return ""
	}
	budgetID := ""
	for _, budget := range execution.Exception.Budgets {
		if budget.RoleKind != roleKind {
			continue
		}
		if roleKind == core.ComplexExceptionBudgetRecovery {
			if budget.ComplexExecutionTaskID == "" {
				budgetID = budget.ID
			}
			continue
		}
		if budget.ComplexExecutionTaskID == taskID {
			budgetID = budget.ID
		}
	}
	return budgetID
}

func (s *Service) requireExceptionBudget(execution core.ComplexExecutionSnapshot, taskID, roleKind string) error {
	if exceptionBudgetID(execution, taskID, roleKind) == "" {
		return errComplexExecutionStopped
	}
	return nil
}

func (s *Service) occupyExceptionBudget(ctx context.Context, execution core.ComplexExecutionSnapshot, taskID, roleKind, roundKey, stepID string) error {
	budgetID := exceptionBudgetID(execution, taskID, roleKind)
	if budgetID == "" {
		return errComplexExecutionStopped
	}
	occupied, err := s.complexExecution.OccupyClearDevComplexExceptionBudget(ctx, budgetID, roundKey, stepID, s.now().UTC())
	if err != nil {
		var rule *core.RuleError
		if errors.As(err, &rule) {
			return errComplexExecutionStopped
		}
		return err
	}
	if !occupied {
		return errComplexExecutionStopped
	}
	return nil
}

func (s *Service) createExceptionAgentStep(ctx context.Context, execution core.ComplexExecutionSnapshot, taskID, roleKind, runID, roleBindingID, ondemandID string, step core.AgentStep) error {
	if err := s.occupyExceptionBudget(ctx, execution, taskID, roleKind, "", step.ID); err != nil {
		return err
	}
	_, _, err := s.complexExecution.CreateClearDevComplexExceptionAgentStep(ctx, runID, roleBindingID, ondemandID, step)
	return err
}

func (s *Service) reserveExceptionAgentStep(ctx context.Context, execution core.ComplexExecutionSnapshot, taskID, roleKind string, kind core.AgentStepKind, binding core.ComplexOnDemandBinding, prompt string) error {
	if _, found := exceptionStepByKind(execution, kind, binding.ID); found {
		return nil
	}
	now := s.now().UTC()
	stepID := s.newID()
	step := core.AgentStep{
		ID: stepID, Kind: kind, RequestID: binding.ID,
		ClientMessageID: "cleardev-complex-exception-step-" + stepID,
		PromptSHA256:    coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now,
	}
	return s.createExceptionAgentStep(ctx, execution, taskID, roleKind, execution.Run.ID, "", binding.ID, step)
}

func (s *Service) generatedProofRunner() ports.ClearDevGeneratedProofRunner {
	runner, _ := s.checks.(ports.ClearDevGeneratedProofRunner)
	return runner
}

func currentExceptionPathRules(execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, plan core.ComplexPlanTask) core.PathRules {
	rules := core.PathRules{
		WritePaths: append([]string{}, plan.WritePaths...), GeneratedPaths: append([]string{}, plan.GeneratedPaths...),
		SharedPathsRequireApproval: append([]string{}, plan.SharedPathsRequireApproval...), ForbiddenPaths: append([]string{}, plan.ForbiddenPaths...),
	}
	projectRules := func(value core.PathRules) core.PathRules {
		if _, project, err := core.ProjectContractFromRun(execution.Run); err == nil && project {
			value.WritePaths = append(append([]string(nil), value.WritePaths...), value.GeneratedPaths...)
			value.GeneratedPaths = nil
			// Apply only the companion-file permission from a validated append-only revision.
			pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
			if err == nil && pkg.RuntimeRevision != nil && core.ProjectTaskMatchesRun(pkg, execution.Run) == nil &&
				slices.Contains(pkg.WritePaths, "package-lock.json") && slices.Contains(value.WritePaths, "package.json") && !slices.Contains(value.WritePaths, "package-lock.json") {
				value.WritePaths = append(value.WritePaths, "package-lock.json")
			}
		}
		return value
	}
	if execution.Exception == nil {
		return projectRules(rules)
	}
	version := int64(-1)
	for _, permission := range execution.Exception.PermissionVersions {
		if permission.DevelopmentTaskID == task.DevelopmentTaskID && permission.Version >= version {
			version = permission.Version
			rules = permission.Rules
		}
	}
	return projectRules(rules)
}

func complexExceptionScopePathsPass(rules core.PathRules, paths []ports.ClearDevDiffPath, execution core.ComplexExecutionSnapshot, dispatch core.ComplexExecutionDispatch) (bool, string) {
	for _, change := range paths {
		for _, path := range []string{change.OldPath, change.Path} {
			if path == "" {
				continue
			}
			classification, err := rules.ClassifyPath(path)
			if err != nil {
				return false, "Git diff contains a path outside the approved write paths"
			}
			switch classification {
			case core.PathAllowed:
				continue
			case core.PathRequiresGeneratedProof:
				if !complexGeneratedProofCovers(execution, dispatch, path) {
					return false, "Git diff contains a generated path without a matching proof"
				}
			default:
				return false, "Git diff contains a path outside the approved write paths"
			}
		}
	}
	return true, "Git diff paths are within the approved write paths"
}

func complexGeneratedProofCovers(execution core.ComplexExecutionSnapshot, dispatch core.ComplexExecutionDispatch, path string) bool {
	if execution.Exception == nil {
		return false
	}
	for _, proof := range execution.Exception.GeneratedProofs {
		if proof.DispatchID != dispatch.ID || proof.CandidateCommitID != dispatch.CandidateCommitID || proof.Status != "SETTLED" || proof.Result != core.EvidenceResultPass {
			continue
		}
		for _, command := range execution.Exception.GeneratedCommands {
			if command.ID != proof.CommandFactID {
				continue
			}
			for _, output := range command.OutputPaths {
				if output == path {
					return true
				}
			}
		}
	}
	return false
}

func pendingComplexScopeRequest(execution core.ComplexExecutionSnapshot, dispatchID string) (core.ComplexScopeExpansionRequest, bool) {
	if execution.Exception == nil {
		return core.ComplexScopeExpansionRequest{}, false
	}
	for _, request := range execution.Exception.ScopeRequests {
		if request.DispatchID == dispatchID && request.Status == "PENDING" {
			return request, true
		}
	}
	return core.ComplexScopeExpansionRequest{}, false
}

func approvedComplexScopeRequest(execution core.ComplexExecutionSnapshot, dispatchID string) (core.ComplexScopeExpansionRequest, core.ComplexScopeExpansionDecision, bool) {
	if execution.Exception == nil {
		return core.ComplexScopeExpansionRequest{}, core.ComplexScopeExpansionDecision{}, false
	}
	for _, request := range execution.Exception.ScopeRequests {
		if request.DispatchID != dispatchID || request.Status != "APPROVED" {
			continue
		}
		for _, decision := range execution.Exception.ScopeDecisions {
			if decision.RequestID == request.ID && decision.ControlAccepted {
				return request, decision, true
			}
		}
	}
	return core.ComplexScopeExpansionRequest{}, core.ComplexScopeExpansionDecision{}, false
}

func exceptionStepByKind(execution core.ComplexExecutionSnapshot, kind core.AgentStepKind, requestID string) (core.AgentStep, bool) {
	if execution.Exception == nil {
		return core.AgentStep{}, false
	}
	for _, step := range execution.Exception.OnDemandSteps {
		if step.Kind == kind && step.RequestID == requestID {
			return step, true
		}
	}
	return core.AgentStep{}, false
}

func specialistFingerprintChanged(execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, planTask core.ComplexPlanTask) bool {
	if execution.Exception == nil {
		return false
	}
	fingerprint := core.SpecialistBindingFingerprint(execution.Run, task, planTask)
	for _, result := range execution.Exception.SpecialistResults {
		if result.ComplexExecutionTaskID == task.ID && result.BindingFingerprint != fingerprint {
			return true
		}
	}
	return false
}

func validSpecialistForTask(execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, planTask core.ComplexPlanTask) bool {
	if execution.Exception == nil {
		return false
	}
	fingerprint := core.SpecialistBindingFingerprint(execution.Run, task, planTask)
	for _, result := range execution.Exception.SpecialistResults {
		if result.ComplexExecutionTaskID != task.ID || result.BindingFingerprint != fingerprint || result.Outcome != "PASS" {
			continue
		}
		needed := core.FrozenSpecialistChecksForTask(planTask)
		if len(needed) == 0 {
			return true
		}
		for _, spec := range needed {
			passed := false
			for _, check := range execution.Exception.SpecialistChecks {
				if check.SpecialistResultID == result.ID && check.CheckID == spec.ID && check.Status == "SETTLED" && check.Result == core.EvidenceResultPass {
					passed = true
					break
				}
			}
			if !passed {
				return false
			}
		}
		return true
	}
	return false
}

func activeOnDemandBinding(execution core.ComplexExecutionSnapshot, mode string) (core.ComplexOnDemandBinding, bool) {
	if execution.Exception == nil {
		return core.ComplexOnDemandBinding{}, false
	}
	for _, binding := range execution.Exception.OnDemandBindings {
		if binding.Mode == mode && (binding.Status == core.RoleBindingStatusRequested || binding.Status == core.RoleBindingStatusBound) {
			return binding, true
		}
	}
	return core.ComplexOnDemandBinding{}, false
}

func (s *Service) ensureSpecialistBeforeDispatch(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, planTask core.ComplexPlanTask, projectID string) (bool, bool, error) {
	_, project, err := core.ProjectContractFromRun(execution.Run)
	if err != nil {
		return false, true, err
	}
	if project {
		pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
		if err != nil {
			return false, true, err
		}
		if err := core.ProjectTaskMatchesRun(pkg, execution.Run); err != nil {
			return false, true, err
		}
		// V4 already binds confirmed configuration/dependency permissions and
		// candidate checks. The historical specialist tests a mailbox manifest
		// and forbids new dependencies before implementation, so cannot apply
		// to an EMPTY source. Exact candidate preparation and independent Task
		// and Final Review enforce the admitted project contract instead.
		return false, false, nil
	}
	if !core.TaskRequiresSpecialist(planTask) {
		return false, false, nil
	}
	if specialistFingerprintChanged(execution, task, planTask) {
		return false, true, errComplexExecutionStopped
	}
	if validSpecialistForTask(execution, task, planTask) {
		return false, false, nil
	}
	return s.advanceComplexExceptionSpecialist(ctx, execution, task, planTask, projectID)
}

func (s *Service) advanceComplexExceptionSpecialist(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, planTask core.ComplexPlanTask, projectID string) (bool, bool, error) {
	if specialistFingerprintChanged(execution, task, planTask) {
		return false, true, errComplexExecutionStopped
	}
	binding, found := activeOnDemandBinding(execution, core.ComplexOnDemandModeSpecialist)
	if !found {
		if err := s.requireExceptionBudget(execution, task.ID, core.ComplexExceptionBudgetSpecialist); err != nil {
			return false, true, err
		}
		now := s.now().UTC()
		binding = core.ComplexOnDemandBinding{
			ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, Mode: core.ComplexOnDemandModeSpecialist,
			TriggerReason: core.ReasonSpecialistRequired, SessionCreationIdempotencyKey: "cleardev-complex-exception:specialist:" + task.ID,
			Status: core.RoleBindingStatusRequested, BindingFingerprint: core.SpecialistBindingFingerprint(execution.Run, task, planTask), RequestedAt: now,
		}
		_, _, err := s.complexExecution.CreateClearDevComplexExceptionOnDemandBinding(ctx, binding)
		return err == nil, false, err
	}
	if binding.ComplexExecutionTaskID != task.ID {
		return false, true, errComplexExecutionStopped
	}
	prompt := complexExceptionSpecialistPrompt(execution.Run, task, planTask)
	resolvedModel := ""
	pendingStepID := ""
	if binding.Status == core.RoleBindingStatusRequested {
		blocked, resolved, inspectErr := s.inspectConfiguredPreflight(ctx, execution.Run.DevelopmentRequirementID, binding.ID, domain.ProjectID(projectID), domain.KindWorker)
		if inspectErr != nil {
			return false, false, inspectErr
		}
		if blocked {
			return false, false, nil
		}
		resolvedModel = resolved
		pendingStepID = s.newID()
		if err := s.occupyExceptionBudget(ctx, execution, task.ID, core.ComplexExceptionBudgetSpecialist, "", pendingStepID); err != nil {
			return false, true, err
		}
	}
	builder, builderOK := complexExecutionBindingByID(execution, execution.Run.BuilderRoleBindingID)
	if !builderOK || builder.Status != core.RoleBindingStatusBound {
		return false, true, errComplexExecutionStopped
	}
	builderRecord, found, err := s.ao.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil || !found {
		return false, false, err
	}
	specialistSpecs := core.FrozenSpecialistChecksForTask(planTask)
	if len(specialistSpecs) == 0 {
		return false, true, errComplexExecutionStopped
	}
	specialistImage := specialistSpecs[0].Image
	specialistArgv := make([][]string, 0, len(specialistSpecs))
	for _, spec := range specialistSpecs {
		if spec.Image != specialistImage || len(spec.Argv) == 0 {
			return false, true, errComplexExecutionStopped
		}
		specialistArgv = append(specialistArgv, append([]string(nil), spec.Argv...))
	}
	specialistPreflightID := execution.Run.ID + ":specialist:" + task.ID + ":check-preflight"
	if err := s.prepareCandidateCheckEnvironment(ctx, specialistPreflightID, builderRecord.Metadata.WorkspacePath, execution.Run.InitialBaseCommitSHA, specialistImage, specialistArgv); err != nil {
		s.logger.Error("ClearDev specialist check preflight failed", "executionRunID", execution.Run.ID, "taskID", task.ID, "error", err)
		_, _ = s.complexExecution.FailClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonCode("CHECKER_UNAVAILABLE"), s.now().UTC())
		return false, true, errComplexExecutionStopped
	}
	if binding.Status == core.RoleBindingStatusRequested {
		branch := "cleardev-complex-specialist-" + task.ID
		if err := s.inspector.PrepareReviewBranch(ctx, builderRecord.Metadata.WorkspacePath, branch, execution.Run.InitialBaseCommitSHA); err != nil {
			_, _ = s.complexExecution.FailClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonCode("BUILDER_WORKTREE_DIRTY"), s.now().UTC())
			return false, true, errComplexExecutionStopped
		}
		session, spawnErr := s.spawnResolvedChatSession(ctx, ports.SpawnConfig{
			ProjectID: builderRecord.ProjectID, Kind: domain.KindWorker, Branch: branch, Prompt: "",
			RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto},
			DisplayName: "ClearDev Specialist", CreationIdempotencyKey: binding.SessionCreationIdempotencyKey,
		}, resolvedModel)
		if spawnErr != nil || !s.validComplexExecutionWorker(ctx, session.SessionRecord, core.ComplexExecutionRoleBinding{SessionCreationIdempotencyKey: binding.SessionCreationIdempotencyKey, Status: core.RoleBindingStatusRequested}, projectID) {
			_, _ = s.complexExecution.FailClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonCode("BUILDER_UNAVAILABLE"), s.now().UTC())
			return false, true, errComplexExecutionStopped
		}
		if _, err = s.complexExecution.BindClearDevComplexExceptionOnDemand(ctx, binding.ID, string(session.ID), session.Metadata.WorkspacePath, execution.Run.InitialBaseCommitSHA, s.now().UTC()); err != nil {
			return false, false, err
		}
		now := s.now().UTC()
		step := core.AgentStep{
			ID: pendingStepID, Kind: core.ComplexExceptionAgentStepSpecialist, RequestID: binding.ID,
			ClientMessageID: "cleardev-complex-exception-step-" + pendingStepID,
			PromptSHA256:    coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now,
		}
		_, _, err = s.complexExecution.CreateClearDevComplexExceptionAgentStep(ctx, execution.Run.ID, "", binding.ID, step)
		return err == nil, false, err
	}
	step, stepFound := exceptionStepByKind(execution, core.ComplexExceptionAgentStepSpecialist, binding.ID)
	if !stepFound {
		if err := s.reserveExceptionAgentStep(ctx, execution, task.ID, core.ComplexExceptionBudgetSpecialist, core.ComplexExceptionAgentStepSpecialist, binding, prompt); err != nil {
			return false, true, err
		}
		return true, false, nil
	}
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return false, true, errComplexExecutionStopped
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.occupyExceptionBudget(ctx, execution, task.ID, core.ComplexExceptionBudgetSpecialist, "", step.ID); err != nil {
			return false, true, err
		}
		if err := s.relayAgentTurn(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryException, step, binding.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return false, false, err
		}
		_, err = s.complexExecution.MarkClearDevComplexExceptionAgentStepSent(ctx, step.ID, s.now().UTC())
		return err == nil, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		validate := func(raw []byte) error {
			_, err := core.ParseComplexSpecialistResult(raw, execution.Run.ID, task.ID, execution.Run.RequirementVersionID, execution.Run.RequirementSHA256, execution.Run.PlanID, execution.Run.PlanSHA256)
			return err
		}
		fail := func(ctx context.Context, item core.AgentStep, reason core.ReasonCode) error {
			if reason != core.ReasonSpecialistInvalid {
				return fmt.Errorf("specialist observation failed: %s", reason)
			}
			_, err := s.complexExecution.EndClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonSpecialistInvalid, s.now().UTC())
			return err
		}
		message, stopped, err := s.awaitValidAgentJSON(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryException, binding.AOSessionID, step, prompt, validate, standardStepReasons{
			Invalid: core.ReasonSpecialistInvalid, Timeout: "TIMEOUT", Unavailable: "UNAVAILABLE",
		}, fail, errComplexExecutionStopped)
		if err != nil || stopped {
			return false, stopped, err
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, message.TurnID, message.MessageID, message.Text, coreDigest([]byte(message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexExceptionAgentStep(ctx, step)
		return err == nil, false, err
	}
	if step.SendStatus != core.AgentStepSendStatusSettled {
		return false, true, errComplexExecutionStopped
	}
	parsed, parseErr := core.ParseComplexSpecialistResult([]byte(step.FinalMessageText), execution.Run.ID, task.ID, execution.Run.RequirementVersionID, execution.Run.RequirementSHA256, execution.Run.PlanID, execution.Run.PlanSHA256)
	if parseErr != nil || parsed.Outcome != "PASS" {
		_, _ = s.complexExecution.EndClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonSpecialistInvalid, s.now().UTC())
		return false, true, errComplexExecutionStopped
	}
	constraints, err := core.EncodeSpecialistConstraints(parsed.Constraints)
	if err != nil {
		return false, false, err
	}
	if !specialistResultRecorded(execution, task.ID, step.ID) {
		checks := []core.ComplexSpecialistCheck{}
		for _, spec := range core.FrozenSpecialistChecksForTask(planTask) {
			checks = append(checks, core.ComplexSpecialistCheck{
				ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, CheckID: spec.ID,
				Argv: append([]string(nil), spec.Argv...), Status: "PENDING", CreatedAt: s.now().UTC(),
			})
		}
		result := core.ComplexSpecialistResult{
			ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, OnDemandBindingID: binding.ID,
			AgentStepID: step.ID, BindingFingerprint: binding.BindingFingerprint, Outcome: parsed.Outcome, ConstraintsJSON: constraints,
			ReasonCode: core.ReasonCode(parsed.ReasonCode), Summary: parsed.Summary, CreatedAt: s.now().UTC(),
		}
		if err := s.complexExecution.RecordClearDevComplexExceptionSpecialist(ctx, result, checks); err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	if changed, err := s.advanceSpecialistChecks(ctx, execution, task, binding); err != nil || changed {
		return changed, false, err
	}
	_, err = s.complexExecution.EndClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonNone, s.now().UTC())
	if err != nil {
		return false, false, err
	}
	if err := s.releaseCandidateCheckEnvironment(ctx, specialistPreflightID); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func specialistResultRecorded(execution core.ComplexExecutionSnapshot, taskID, stepID string) bool {
	if execution.Exception == nil {
		return false
	}
	for _, result := range execution.Exception.SpecialistResults {
		if result.ComplexExecutionTaskID == taskID && result.AgentStepID == stepID {
			return true
		}
	}
	return false
}

func (s *Service) advanceSpecialistChecks(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, binding core.ComplexOnDemandBinding) (bool, error) {
	if execution.Exception == nil {
		return false, errComplexExecutionStopped
	}
	for _, check := range execution.Exception.SpecialistChecks {
		if check.ComplexExecutionTaskID != task.ID {
			continue
		}
		if check.Status == "PENDING" {
			_, err := s.complexExecution.StartClearDevComplexExceptionSpecialistCheck(ctx, check.ID)
			return err == nil, err
		}
		if check.Status == "STARTED" {
			spec, ok := core.FrozenSpecialistCheckByID(check.CheckID)
			if !ok {
				return false, errComplexExecutionStopped
			}
			result, runErr := s.checks.RunCandidateCheck(ctx, ports.ClearDevCheckRequest{
				RunID: check.ID, WorkspacePath: binding.WorkspacePath, CandidateSHA: binding.BaseCommitSHA, Image: spec.Image,
				Argv: append([]string(nil), spec.Argv...), Timeout: time.Duration(spec.TimeoutSeconds) * time.Second,
				MemoryBytes: standardCheckMemoryBytes, PidsLimit: standardCheckPidsLimit, OutputLimit: standardCheckOutputLimit,
			})
			settled := check
			switch {
			case runErr != nil || result.Outcome == ports.ClearDevCheckInfraError:
				settled.Result, settled.ReasonCode, settled.ContainerImageID = core.EvidenceResultFail, core.ReasonCode("CHECKER_UNAVAILABLE"), result.ImageID
			case result.Outcome != ports.ClearDevCheckPass:
				settled.Result, settled.ReasonCode, settled.ContainerImageID = core.EvidenceResultFail, core.ReasonSpecialistInvalid, result.ImageID
			default:
				settled.Result, settled.ContainerImageID = core.EvidenceResultPass, result.ImageID
			}
			settled.Status = "SETTLED"
			_, err := s.complexExecution.SettleClearDevComplexExceptionSpecialistCheck(ctx, settled, s.now().UTC())
			return err == nil, err
		}
		if check.Status != "SETTLED" || check.Result != core.EvidenceResultPass {
			return false, errComplexExecutionStopped
		}
	}
	return false, nil
}

func (s *Service) advanceComplexExceptionScope(ctx context.Context, execution core.ComplexExecutionSnapshot, version core.RequirementVersion, plan core.ComplexEngineeringPlan, parsed core.ComplexEngineeringPlanResult) (bool, bool, error) {
	task, dispatch, ok := activeComplexExecutionDispatch(execution)
	if !ok {
		return false, true, errComplexExecutionStopped
	}
	request, found := pendingComplexScopeRequest(execution, dispatch.ID)
	if !found {
		return false, true, errComplexExecutionStopped
	}
	planTask, ok := complexPlanTaskByKey(parsed, task.TaskKey)
	if !ok {
		return false, true, errComplexExecutionStopped
	}
	steward := activeComplexExecutionSteward(execution)
	if steward.ID == "" || steward.Status != core.RoleBindingStatusBound {
		return false, true, errComplexExecutionStopped
	}
	step, stepFound := exceptionStepByKind(execution, core.ComplexExceptionAgentStepScopeDecision, request.ID)
	prompt := complexExceptionScopeDecisionPrompt(request, version, plan)
	if !stepFound {
		now := s.now().UTC()
		stepID := s.newID()
		step = core.AgentStep{ID: stepID, RoleBindingID: steward.ID, Kind: core.ComplexExceptionAgentStepScopeDecision, RequestID: request.ID, ClientMessageID: "cleardev-complex-exception-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
		if err := s.createExceptionAgentStep(ctx, execution, task.ID, core.ComplexExceptionBudgetStewardException, execution.Run.ID, steward.ID, "", step); err != nil {
			return false, true, err
		}
		return true, false, nil
	}
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return false, true, errComplexExecutionStopped
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.occupyExceptionBudget(ctx, execution, task.ID, core.ComplexExceptionBudgetStewardException, "", step.ID); err != nil {
			return false, true, err
		}
		if err := s.relayAgentTurn(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryException, step, steward.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return false, false, err
		}
		_, err := s.complexExecution.MarkClearDevComplexExceptionAgentStepSent(ctx, step.ID, s.now().UTC())
		return err == nil, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		validate := func(raw []byte) error {
			_, err := core.ParseComplexScopeExpansionDecision(raw, request.ID, version.ID, version.SHA256, plan.ID, plan.PlanSHA256, request.RequestedPaths)
			return err
		}
		fail := func(ctx context.Context, item core.AgentStep, reason core.ReasonCode) error {
			if reason != core.ReasonScopeDecisionInvalid {
				return fmt.Errorf("scope decision observation failed: %s", reason)
			}
			if _, failErr := s.complexExecution.FailClearDevComplexExceptionAgentStep(ctx, item.ID, core.ReasonScopeDecisionInvalid, s.now().UTC()); failErr != nil {
				return failErr
			}
			_, _, err := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonScopeDecisionInvalid)
			return err
		}
		message, stopped, err := s.awaitValidAgentJSON(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryException, steward.AOSessionID, step, prompt, validate, standardStepReasons{
			Invalid: core.ReasonScopeDecisionInvalid, Timeout: "TIMEOUT", Unavailable: "UNAVAILABLE",
		}, fail, errComplexExecutionStopped)
		if err != nil || stopped {
			return false, stopped, err
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, message.TurnID, message.MessageID, message.Text, coreDigest([]byte(message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexExceptionAgentStep(ctx, step)
		return err == nil, false, err
	}
	decision, err := core.ParseComplexScopeExpansionDecision([]byte(step.FinalMessageText), request.ID, version.ID, version.SHA256, plan.ID, plan.PlanSHA256, request.RequestedPaths)
	if err != nil {
		if _, failErr := s.complexExecution.FailClearDevComplexExceptionAgentStep(ctx, step.ID, core.ReasonScopeDecisionInvalid, s.now().UTC()); failErr != nil {
			return false, false, failErr
		}
		_, _, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonScopeDecisionInvalid)
		return false, true, failErr
	}
	recorded := core.ComplexScopeExpansionDecision{
		ID: s.newID(), RequestID: request.ID, ExecutionRunID: execution.Run.ID, RequirementVersionID: version.ID,
		RequirementSHA256: version.SHA256, PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, Paths: append([]string{}, decision.Paths...),
		Decision: decision.Decision, ReasonCode: core.ReasonCode(decision.ReasonCode), Summary: decision.Summary,
		StewardRoleBindingID: steward.ID, AgentStepID: step.ID, CreatedAt: s.now().UTC(),
	}
	if decision.Decision != core.ComplexScopeDecisionApprove {
		if err := s.complexExecution.RejectClearDevComplexExceptionScope(ctx, recorded, s.now().UTC()); err != nil {
			return false, false, err
		}
		return true, true, nil
	}
	approved, err := core.ApprovedSharedPaths(planTask, decision.Paths)
	if err != nil {
		recorded.Decision, recorded.ReasonCode = core.ComplexScopeDecisionNeedsHuman, core.ReasonScopePathNotListed
		if settleErr := s.complexExecution.RejectClearDevComplexExceptionScope(ctx, recorded, s.now().UTC()); settleErr != nil {
			return false, false, settleErr
		}
		return true, true, nil
	}
	current := currentExceptionPathRules(execution, task, planTask)
	permission := core.PermissionVersion{ID: task.ID + ":permission:" + s.newID(), DevelopmentTaskID: task.DevelopmentTaskID, Rules: core.PermissionRulesAfterScopeApproval(current, approved), CreatedAt: s.now().UTC()}
	recorded.ControlAccepted, recorded.PermissionVersionID = true, permission.ID
	if err := s.complexExecution.AcceptClearDevComplexExceptionScope(ctx, recorded, permission, s.now().UTC()); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (s *Service) handleComplexExceptionAfterBuilderSettled(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, planTask core.ComplexPlanTask, builder core.ComplexExecutionRoleBinding, step core.AgentStep) (handled, changed, done bool, err error) {
	if request, found := pendingComplexScopeRequest(execution, dispatch.ID); found {
		_ = request
		return true, false, false, nil
	}
	if _, decision, found := approvedComplexScopeRequest(execution, dispatch.ID); found {
		return s.advanceComplexExceptionBuilderContinue(ctx, execution, task, dispatch, builder, decision)
	}
	requested, parseErr := core.ParseComplexScopeExpansionRequest([]byte(step.FinalMessageText), dispatch.ID, task.DevelopmentTaskID, dispatch.Round, execution.Run.RequirementVersionID, execution.Run.RequirementSHA256, execution.Run.PlanID, execution.Run.PlanSHA256)
	if parseErr == nil {
		if _, err := core.ApprovedSharedPaths(planTask, requested.RequestedPaths); err != nil {
			failChanged, failDone, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonScopePathNotListed)
			return true, failChanged, failDone, failErr
		}
		record := core.ComplexScopeExpansionRequest{
			ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, Round: dispatch.Round,
			RequirementVersionID: execution.Run.RequirementVersionID, RequirementSHA256: execution.Run.RequirementSHA256,
			PlanID: execution.Run.PlanID, PlanSHA256: execution.Run.PlanSHA256, RequestedPaths: append([]string{}, requested.RequestedPaths...),
			AgentStepID: step.ID, Status: "PENDING", CreatedAt: s.now().UTC(),
		}
		if err := s.complexExecution.RecordClearDevComplexExceptionScopeRequest(ctx, record); err != nil {
			return true, false, false, err
		}
		return true, true, false, nil
	}
	return false, false, false, nil
}

func (s *Service) advanceComplexExceptionBuilderContinue(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, builder core.ComplexExecutionRoleBinding, decision core.ComplexScopeExpansionDecision) (bool, bool, bool, error) {
	step, found := exceptionStepByKind(execution, core.ComplexExceptionAgentStepBuilderContinue, dispatch.ID)
	prompt := complexExceptionBuilderContinuePrompt([]byte(task.ExecutionPackageJSON), dispatch.ID, task.DevelopmentTaskID, dispatch.Round, decision.Paths)
	if !found {
		now := s.now().UTC()
		stepID := s.newID()
		step = core.AgentStep{ID: stepID, RoleBindingID: builder.ID, Kind: core.ComplexExceptionAgentStepBuilderContinue, RequestID: dispatch.ID, ClientMessageID: "cleardev-complex-exception-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now}
		if err := s.createExceptionAgentStep(ctx, execution, task.ID, core.ComplexExceptionBudgetBuilder, execution.Run.ID, builder.ID, "", step); err != nil {
			return true, false, true, err
		}
		return true, true, false, nil
	}
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return true, false, true, errComplexExecutionStopped
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.occupyExceptionBudget(ctx, execution, task.ID, core.ComplexExceptionBudgetBuilder, "", step.ID); err != nil {
			return true, false, true, err
		}
		if err := s.relayAgentTurn(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryException, step, builder.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return true, false, false, err
		}
		_, err := s.complexExecution.MarkClearDevComplexExceptionAgentStepSent(ctx, step.ID, s.now().UTC())
		return true, err == nil, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		validate := func(raw []byte) error {
			_, err := core.ParseComplexExecutionBuilderResult(raw, dispatch.ID, task.DevelopmentTaskID, dispatch.Round)
			return err
		}
		fail := func(ctx context.Context, item core.AgentStep, reason core.ReasonCode) error {
			if reason != core.ReasonCode("BUILDER_RESULT_INVALID") {
				return fmt.Errorf("builder continue observation failed: %s", reason)
			}
			_, _, err := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonCode("BUILDER_RESULT_INVALID"))
			return err
		}
		message, stopped, err := s.awaitValidAgentJSON(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryException, builder.AOSessionID, step, prompt, validate, standardStepReasons{
			Invalid: "BUILDER_RESULT_INVALID", Timeout: "TIMEOUT", Unavailable: "UNAVAILABLE",
		}, fail, errComplexExecutionStopped)
		if err != nil || stopped {
			return true, false, stopped, err
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, message.TurnID, message.MessageID, message.Text, coreDigest([]byte(message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexExceptionAgentStep(ctx, step)
		return true, err == nil, false, err
	}
	if step.SendStatus != core.AgentStepSendStatusSettled {
		return true, false, true, errComplexExecutionStopped
	}
	result, err := core.ParseComplexExecutionBuilderResult([]byte(step.FinalMessageText), dispatch.ID, task.DevelopmentTaskID, dispatch.Round)
	if err != nil {
		changed, done, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonCode("BUILDER_RESULT_INVALID"))
		return true, changed, done, failErr
	}
	if result.Outcome != "CANDIDATE_READY" {
		changed, done, failErr := s.failComplexExecutionDispatch(ctx, execution, task, dispatch, false, core.ReasonCode("BUILDER_RESULT_INVALID"))
		return true, changed, done, failErr
	}
	return false, false, false, nil
}

func (s *Service) ensureComplexGeneratedProof(ctx context.Context, execution core.ComplexExecutionSnapshot, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, workspace string) (bool, error) {
	if execution.Exception == nil {
		return false, nil
	}
	var command core.ComplexGeneratedCommandFact
	found := false
	for _, item := range execution.Exception.GeneratedCommands {
		if item.ComplexExecutionTaskID == task.ID {
			command, found = item, true
			break
		}
	}
	if !found {
		return false, nil
	}
	var proof core.ComplexGeneratedProof
	proofFound := false
	for _, item := range execution.Exception.GeneratedProofs {
		if item.DispatchID == dispatch.ID && item.CommandFactID == command.ID {
			proof, proofFound = item, true
			break
		}
	}
	if !proofFound {
		proof = core.ComplexGeneratedProof{
			ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID,
			CandidateCommitID: dispatch.CandidateCommitID, CandidateCommitSHA: dispatch.CandidateCommitSHA, CommandFactID: command.ID,
			Status: "PENDING", CreatedAt: s.now().UTC(),
		}
		if err := s.complexExecution.RecordClearDevComplexExceptionGeneratedProof(ctx, proof); err != nil {
			return false, err
		}
		return true, nil
	}
	if proof.Status == "PENDING" {
		_, err := s.complexExecution.StartClearDevComplexExceptionGeneratedProof(ctx, proof.ID)
		return err == nil, err
	}
	if proof.Status == "STARTED" {
		runner := s.generatedProofRunner()
		if runner == nil {
			proof.Status, proof.ReasonCode = "FAILED", core.ReasonGeneratedProofMissing
			_, err := s.complexExecution.SettleClearDevComplexExceptionGeneratedProof(ctx, proof, s.now().UTC())
			return err == nil, err
		}
		result, runErr := runner.RunGeneratedProof(ctx, ports.ClearDevGeneratedProofRequest{
			RunID: proof.ID, WorkspacePath: workspace, CandidateSHA: dispatch.CandidateCommitSHA, Image: command.Image,
			Argv: append([]string(nil), command.Argv...), OutputPaths: append([]string(nil), command.OutputPaths...),
			Timeout: time.Duration(command.TimeoutSeconds) * time.Second, MemoryBytes: standardCheckMemoryBytes, PidsLimit: standardCheckPidsLimit,
		})
		proof.ContainerImageID, proof.OutputSHA256JSON = result.ImageID, result.OutputSHA256JSON
		if runErr != nil || result.Outcome != ports.ClearDevCheckPass {
			proof.Status, proof.Result, proof.ReasonCode = "SETTLED", core.EvidenceResultFail, core.ReasonGeneratedProofMismatch
			if result.ReasonCode != "" {
				proof.ReasonCode = core.ReasonCode(result.ReasonCode)
			}
		} else {
			proof.Status, proof.Result = "SETTLED", core.EvidenceResultPass
		}
		_, err := s.complexExecution.SettleClearDevComplexExceptionGeneratedProof(ctx, proof, s.now().UTC())
		return err == nil, err
	}
	if proof.Status != "SETTLED" || proof.Result != core.EvidenceResultPass {
		return false, errComplexExecutionStopped
	}
	return false, nil
}

func complexExceptionInfraRetryEligible(execution core.ComplexExecutionSnapshot, run core.ComplexExecutionCheckRun) bool {
	if run.RetryOrdinal != 0 || run.ReasonCode != core.ReasonCode("CHECKER_UNAVAILABLE") {
		return false
	}
	if run.Status != core.ComplexExecutionCheckRunFailed && run.Result == core.EvidenceResultPass {
		return false
	}
	for _, existing := range execution.CheckRuns {
		if existing.CheckSpecFactID == run.CheckSpecFactID && existing.DispatchID == run.DispatchID && existing.CandidateCommitID == run.CandidateCommitID && existing.RetryOrdinal == 1 {
			return false
		}
	}
	return true
}

func (s *Service) advanceComplexExceptionRecovery(ctx context.Context, execution core.ComplexExecutionSnapshot, projectID string) (bool, bool, error) {
	binding, found := activeOnDemandBinding(execution, core.ComplexOnDemandModeRecovery)
	failed, ok := recoveryTriggerCheck(execution, binding)
	fixed, isFixed := fixedRecoveryForFailure(execution, failed.ID)
	if !found {
		if !ok || !core.RecoveryTriggerAllowed(failed.ReasonCode) || (!isFixed && !complexExceptionInfraRetryEligible(execution, failed)) {
			return false, true, errComplexExecutionStopped
		}
		if err := s.requireExceptionBudget(execution, "", core.ComplexExceptionBudgetRecovery); err != nil {
			return false, true, err
		}
		now := s.now().UTC()
		binding = core.ComplexOnDemandBinding{
			ID: s.newID(), ExecutionRunID: execution.Run.ID, Mode: core.ComplexOnDemandModeRecovery, TriggerReason: failed.ReasonCode,
			SessionCreationIdempotencyKey: "cleardev-complex-exception:recovery:" + failed.ID,
			Status:                        core.RoleBindingStatusRequested, RequestedAt: now,
		}
		_, _, err := s.complexExecution.CreateClearDevComplexExceptionOnDemandBinding(ctx, binding)
		return err == nil, false, err
	}
	if !ok || !core.RecoveryTriggerAllowed(failed.ReasonCode) {
		return false, true, errComplexExecutionStopped
	}
	prompt := complexExceptionRecoveryPrompt(execution.Run, failed.ReasonCode, failed.ID)
	if isFixed {
		target, _ := json.Marshal(fixed.Request)
		prompt += "\nExact recovery target (immutable):\n" + string(target) + "\nOnly RESTORE_ORIGINAL_SESSION is permitted for Builder; Reviewer also permits REBUILD_INDEPENDENT_REVIEWER. A proposal is not execution evidence."
	}

	resolvedModel := ""
	pendingStepID := ""
	if binding.Status == core.RoleBindingStatusRequested {
		blocked, resolved, inspectErr := s.inspectConfiguredPreflight(ctx, execution.Run.DevelopmentRequirementID, binding.ID, domain.ProjectID(projectID), domain.KindWorker)
		if inspectErr != nil {
			return false, false, inspectErr
		}
		if blocked {
			return false, false, nil
		}
		resolvedModel = resolved
		pendingStepID = s.newID()
		if err := s.occupyExceptionBudget(ctx, execution, "", core.ComplexExceptionBudgetRecovery, "", pendingStepID); err != nil {
			return false, true, err
		}
	}
	builderID := execution.Run.BuilderRoleBindingID
	if isFixed {
		for _, dispatch := range execution.Dispatches {
			if dispatch.ID == fixed.Request.DispatchID && dispatch.BuilderRoleBindingID != "" {
				builderID = dispatch.BuilderRoleBindingID
			}
		}
	}
	builder, builderOK := complexExecutionBindingByID(execution, builderID)
	if !builderOK || builder.Status != core.RoleBindingStatusBound {
		return false, true, errComplexExecutionStopped
	}
	builderRecord, found, err := s.ao.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil || !found {
		return false, false, err
	}
	if binding.Status == core.RoleBindingStatusRequested {
		branch := "cleardev-complex-recovery-" + failed.ID
		if isFixed {
			branch = "cleardev-complex-recovery-" + coreDigest([]byte(failed.ID))[:20]
		}
		if err := s.inspector.PrepareReviewBranch(ctx, builderRecord.Metadata.WorkspacePath, branch, failed.CandidateCommitSHA); err != nil {
			_, _ = s.complexExecution.FailClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonCode("REVIEW_WORKTREE_INVALID"), s.now().UTC())
			return false, true, errComplexExecutionStopped
		}
		session, spawnErr := s.spawnResolvedChatSession(ctx, ports.SpawnConfig{
			ProjectID: builderRecord.ProjectID, Kind: domain.KindWorker, Branch: branch, Prompt: "",
			RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto},
			DisplayName: "ClearDev Recovery", CreationIdempotencyKey: binding.SessionCreationIdempotencyKey,
		}, resolvedModel)
		if spawnErr != nil || !s.validComplexExecutionWorker(ctx, session.SessionRecord, core.ComplexExecutionRoleBinding{SessionCreationIdempotencyKey: binding.SessionCreationIdempotencyKey, Status: core.RoleBindingStatusRequested}, projectID) {
			_, _ = s.complexExecution.FailClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonCode("BUILDER_UNAVAILABLE"), s.now().UTC())
			return false, true, errComplexExecutionStopped
		}
		if _, err = s.complexExecution.BindClearDevComplexExceptionOnDemand(ctx, binding.ID, string(session.ID), session.Metadata.WorkspacePath, failed.CandidateCommitSHA, s.now().UTC()); err != nil {
			return false, false, err
		}
		now := s.now().UTC()
		step := core.AgentStep{
			ID: pendingStepID, Kind: core.ComplexExceptionAgentStepRecovery, RequestID: binding.ID,
			ClientMessageID: "cleardev-complex-exception-step-" + pendingStepID,
			PromptSHA256:    coreDigest([]byte(prompt)), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now,
		}
		_, _, err = s.complexExecution.CreateClearDevComplexExceptionAgentStep(ctx, execution.Run.ID, "", binding.ID, step)
		return err == nil, false, err
	}
	step, stepFound := exceptionStepByKind(execution, core.ComplexExceptionAgentStepRecovery, binding.ID)
	if !stepFound {
		if err := s.reserveExceptionAgentStep(ctx, execution, "", core.ComplexExceptionBudgetRecovery, core.ComplexExceptionAgentStepRecovery, binding, prompt); err != nil {
			return false, true, err
		}
		return true, false, nil
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.occupyExceptionBudget(ctx, execution, "", core.ComplexExceptionBudgetRecovery, "", step.ID); err != nil {
			return false, true, err
		}
		if err := s.relayAgentTurn(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryException, step, binding.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			return false, false, err
		}
		_, err = s.complexExecution.MarkClearDevComplexExceptionAgentStepSent(ctx, step.ID, s.now().UTC())
		return err == nil, false, err
	}
	if step.SendStatus == core.AgentStepSendStatusSent {
		validate := func(raw []byte) error {
			_, err := core.ParseComplexRecoveryResult(raw, execution.Run.ID, string(failed.ReasonCode), failed.ID)
			return err
		}
		fail := func(ctx context.Context, item core.AgentStep, reason core.ReasonCode) error {
			if reason != core.ReasonRecoveryNeedsHuman {
				return fmt.Errorf("recovery observation failed: %s", reason)
			}
			_, err := s.complexExecution.EndClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonRecoveryNeedsHuman, s.now().UTC())
			return err
		}
		message, stopped, err := s.awaitValidAgentJSON(ctx, execution.Run.DevelopmentRequirementID, core.AgentStepCategoryException, binding.AOSessionID, step, prompt, validate, standardStepReasons{
			Invalid: core.ReasonRecoveryNeedsHuman, Timeout: "TIMEOUT", Unavailable: "UNAVAILABLE",
		}, fail, errComplexExecutionStopped)
		if err != nil || stopped {
			return false, stopped, err
		}
		now := s.now().UTC()
		step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText, step.MessageSHA256, step.CompletedAt = core.AgentStepSendStatusSettled, message.TurnID, message.MessageID, message.Text, coreDigest([]byte(message.Text)), &now
		_, err = s.complexExecution.SettleClearDevComplexExceptionAgentStep(ctx, step)
		return err == nil, false, err
	}
	parsed, parseErr := core.ParseComplexRecoveryResult([]byte(step.FinalMessageText), execution.Run.ID, string(failed.ReasonCode), failed.ID)
	if parseErr != nil || parsed.Outcome != "PASS" || (!isFixed && parsed.Action != core.ComplexRecoveryActionRetryInfraCheck) {
		_, _ = s.complexExecution.EndClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonRecoveryNeedsHuman, s.now().UTC())
		return false, true, errComplexExecutionStopped
	}
	if isFixed {
		var proposal core.ComplexRecoveryAction
		if execution.Exception != nil {
			for _, a := range execution.Exception.RecoveryActions {
				if a.AgentStepID == step.ID {
					proposal = a
				}
			}
		}
		if proposal.ID == "" {
			proposal = core.ComplexRecoveryAction{ID: fixed.Request.ID + ":proposal", ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: fixed.Request.TaskID, OnDemandBindingID: binding.ID, AgentStepID: step.ID, TriggerReason: failed.ReasonCode, TriggerFactID: failed.ID, Action: parsed.Action, Outcome: parsed.Outcome, Summary: parsed.Summary, CreatedAt: s.now().UTC()}
			err := s.complexExecution.RecordClearDevComplexExceptionRecovery(ctx, proposal)
			return err == nil, false, err
		}
		if fixed.Result == nil {
			return s.executeFixedRecovery(ctx, execution, fixed, proposal)
		}
		if fixed.Result.Outcome != "PASS" {
			return false, true, errComplexExecutionStopped
		}
		_, err = s.complexExecution.EndClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonNone, s.now().UTC())
		return err == nil, false, err
	}
	if !recoveryActionRecorded(execution, step.ID) {
		retryID := existingInfraRetryCheckID(execution, failed)
		if retryID == "" {
			retryID = s.newID()
			retry := core.ComplexExecutionCheckRun{
				ID: retryID, ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: failed.ComplexExecutionTaskID, DispatchID: failed.DispatchID,
				CandidateCommitID: failed.CandidateCommitID, CandidateCommitSHA: failed.CandidateCommitSHA, CheckSpecFactID: failed.CheckSpecFactID,
				Kind: failed.Kind, Argv: append([]string(nil), failed.Argv...), Status: core.ComplexExecutionCheckRunPending, RetryOrdinal: 1, CreatedAt: s.now().UTC(),
			}
			if _, _, err = s.complexExecution.CreateClearDevComplexExecutionCheckRun(ctx, retry); err != nil {
				return false, false, err
			}
		}
		action := core.ComplexRecoveryAction{
			ID: s.newID(), ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: failed.ComplexExecutionTaskID, OnDemandBindingID: binding.ID, AgentStepID: step.ID,
			TriggerReason: failed.ReasonCode, TriggerFactID: failed.ID, Action: parsed.Action, Outcome: parsed.Outcome,
			RetryCheckRunID: retryID, ReasonCode: core.ReasonCode(parsed.ReasonCode), Summary: parsed.Summary, CreatedAt: s.now().UTC(),
		}
		if err := s.complexExecution.RecordClearDevComplexExceptionRecovery(ctx, action); err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	requestExists := false
	for _, fixed := range execution.FixedRecoveries {
		if fixed.Request.CheckRunID == failed.ID {
			requestExists = true
		}
	}
	if !requestExists {
		store, ok := s.complexExecution.(fixedRecoveryStore)
		if !ok {
			return false, true, errors.New("fixed recovery storage unavailable")
		}
		request := core.FixedRecoveryRequest{ID: failed.ID + ":fixed-recovery", RequirementID: execution.Run.DevelopmentRequirementID, ExecutionRunID: execution.Run.ID, ProjectID: projectID, TaskID: failed.ComplexExecutionTaskID, DispatchID: failed.DispatchID, CheckRunID: failed.ID, RetryCheckRunID: existingInfraRetryCheckID(execution, failed), CandidateID: failed.CandidateCommitID, CandidateSHA: failed.CandidateCommitSHA, ExpectedSHA: failed.CandidateCommitSHA, CreatedAt: s.now().UTC()}
		_, err := store.EnsureClearDevFixedRecoveryRequest(ctx, request)
		return err == nil, false, err
	}
	_, err = s.complexExecution.EndClearDevComplexExceptionOnDemand(ctx, binding.ID, core.ReasonNone, s.now().UTC())
	return err == nil, false, err
}

func recoveryActionRecorded(execution core.ComplexExecutionSnapshot, stepID string) bool {
	if execution.Exception == nil {
		return false
	}
	for _, action := range execution.Exception.RecoveryActions {
		if action.AgentStepID == stepID {
			return true
		}
	}
	return false
}

func failedInfraIntegrationCheck(execution core.ComplexExecutionSnapshot) (core.ComplexExecutionCheckRun, bool) {
	for _, run := range execution.CheckRuns {
		if run.Kind != core.CandidateCheckIntegration {
			continue
		}
		if (run.Status == core.ComplexExecutionCheckRunFailed || run.Result != core.EvidenceResultPass) && complexExceptionInfraRetryEligible(execution, run) {
			return run, true
		}
	}
	return core.ComplexExecutionCheckRun{}, false
}

func recoveryTriggerCheck(execution core.ComplexExecutionSnapshot, binding core.ComplexOnDemandBinding) (core.ComplexExecutionCheckRun, bool) {
	const prefix = "cleardev-complex-exception:recovery:"
	if strings.HasPrefix(binding.SessionCreationIdempotencyKey, prefix) {
		id := strings.TrimPrefix(binding.SessionCreationIdempotencyKey, prefix)
		for _, run := range execution.CheckRuns {
			if run.ID == id {
				return run, true
			}
		}
	}
	for _, fixed := range execution.FixedRecoveries {
		if fixed.Result != nil && binding.ID == "" {
			continue
		}
		if binding.ID != "" && binding.SessionCreationIdempotencyKey != prefix+fixed.Request.FailureEventID {
			continue
		}
		reason := core.ReasonCode("BUILDER_UNAVAILABLE")
		if fixed.Request.ReviewID != "" {
			reason = "REVIEWER_UNAVAILABLE"
		}
		return core.ComplexExecutionCheckRun{ID: fixed.Request.FailureEventID, ExecutionRunID: execution.Run.ID, ComplexExecutionTaskID: fixed.Request.TaskID, DispatchID: fixed.Request.DispatchID, CandidateCommitID: fixed.Request.CandidateID, CandidateCommitSHA: fixed.Request.ExpectedSHA, ReasonCode: reason}, true
	}
	return failedInfraIntegrationCheck(execution)
}

func existingInfraRetryCheckID(execution core.ComplexExecutionSnapshot, failed core.ComplexExecutionCheckRun) string {
	for _, run := range execution.CheckRuns {
		if run.CheckSpecFactID == failed.CheckSpecFactID && run.DispatchID == failed.DispatchID && run.CandidateCommitID == failed.CandidateCommitID && run.RetryOrdinal == 1 {
			return run.ID
		}
	}
	return ""
}

func (s *Service) integrationCheckRequest(runID, workspace, candidateSHA string, spec core.ComplexExecutionCheckSpecFact) ports.ClearDevCheckRequest {
	request := complexCandidateCheckRequest(runID, workspace, candidateSHA, spec)
	request.AllowInfraInject = true
	return request
}

func complexPlanTaskFromExecution(task core.ComplexExecutionTask) (core.ComplexPlanTask, bool) {
	pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
	if err != nil {
		return core.ComplexPlanTask{}, false
	}
	return core.ComplexPlanTask{
		Key: pkg.TaskKey, Title: pkg.Title, Objective: pkg.Objective, RequirementIDs: pkg.RequirementIDs, AcceptanceIDs: pkg.AcceptanceIDs,
		WritePaths: pkg.WritePaths, GeneratedPaths: pkg.GeneratedPaths, SharedPathsRequireApproval: pkg.SharedPathsRequireApproval,
		ForbiddenPaths: pkg.ForbiddenPaths, DependencyKeys: pkg.DependencyTaskKeys,
	}, true
}

func exceptionBuilderContinueReady(execution core.ComplexExecutionSnapshot, dispatchID string) bool {
	step, ok := exceptionStepByKind(execution, core.ComplexExceptionAgentStepBuilderContinue, dispatchID)
	return ok && step.SendStatus == core.AgentStepSendStatusSettled
}
