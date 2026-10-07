package cleardev

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
)

func (s *Service) BackfillHumanDecisionRequests(ctx context.Context) error {
	if s.humanDecisions == nil {
		return nil
	}
	return s.humanDecisions.BackfillClearDevHumanDecisionRequests(ctx)
}

func (s *Service) IssueHumanDecisionOffer(ctx context.Context, desktopRunID string) (core.HumanDecisionOffer, bool, error) {
	if s.humanDecisions == nil || strings.TrimSpace(desktopRunID) == "" {
		return core.HumanDecisionOffer{}, false, nil
	}
	if err := s.humanDecisions.BackfillClearDevHumanDecisionRequests(ctx); err != nil {
		return core.HumanDecisionOffer{}, false, err
	}
	pending, err := s.humanDecisions.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		return core.HumanDecisionOffer{}, false, err
	}
	now := s.now().UTC()
	reopenStore, canReopen := s.humanDecisions.(humanDecisionReopenStore)
	if canReopen {
		reopens, err := reopenStore.ListRunnableClearDevHumanDecisionReopens(ctx, desktopRunID)
		if err != nil {
			return core.HumanDecisionOffer{}, false, err
		}
		for _, r := range reopens {
			req, found, err := s.humanDecisions.GetClearDevHumanDecisionRequest(ctx, r.DecisionRequestID)
			if err != nil {
				return core.HumanDecisionOffer{}, false, err
			}
			if !found {
				continue
			}
			current, err := s.humanDecisionSourceCurrent(ctx, req)
			if err != nil {
				return core.HumanDecisionOffer{}, false, err
			}
			if !current {
				continue
			}
			if err := reopenStore.ValidateClearDevHumanDecisionReopen(ctx, req.ID, now); err != nil {
				continue
			}
			nonce, err := humanauthority.NewToken()
			if err != nil {
				return core.HumanDecisionOffer{}, false, err
			}
			offer, err := s.humanDecisions.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: req.ID, DesktopRunID: desktopRunID, Nonce: nonce, IssuedAt: now, ExpiresAt: now.Add(core.HumanDecisionOfferTTL), ReopenRequestID: r.RequestID})
			if err != nil {
				return core.HumanDecisionOffer{}, false, err
			}
			return offer, true, nil
		}
	}
	for _, request := range pending {
		if canReopen {
			if err := reopenStore.ValidateClearDevHumanDecisionReopen(ctx, request.ID, now); err != nil {
				continue
			}
			history, err := reopenStore.ListClearDevHumanDecisionDispatchHistory(ctx, request.ID)
			if err != nil {
				return core.HumanDecisionOffer{}, false, err
			}
			shown := false
			for _, d := range history {
				if d.DesktopRunID == desktopRunID {
					shown = true
				}
			}
			if shown {
				continue
			}
		}

		current, err := s.humanDecisionSourceCurrent(ctx, request)
		if err != nil {
			return core.HumanDecisionOffer{}, false, err
		}
		if !current {
			continue // Keep the audit request, but never offer a stale project source.
		}
		nonce, err := humanauthority.NewToken()
		if err != nil {
			return core.HumanDecisionOffer{}, false, err
		}
		offer, err := s.humanDecisions.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{
			RequestID: request.ID, DesktopRunID: desktopRunID, Nonce: nonce, InitialOnly: true,
			IssuedAt: now, ExpiresAt: now.Add(core.HumanDecisionOfferTTL),
		})
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "already exists") {
				continue
			}
			return core.HumanDecisionOffer{}, false, err
		}
		return offer, true, nil
	}
	return core.HumanDecisionOffer{}, false, nil
}

// Observe the original source before exposing a new native decision window.
// SQLite independently checks its persisted bindings when issuing the offer.
func (s *Service) humanDecisionSourceCurrent(ctx context.Context, req core.HumanDecisionRequest) (bool, error) {
	if req.DecisionKind == core.HumanDecisionKindCoordinationRepair {
		binding, err := core.ParseCoordinationRepairBinding([]byte(req.BindingJSON))
		if err != nil {
			return false, err
		}
		// Unavailable is an ordinary offer-eligibility result. Do not let one
		// unsafe repair prevent other independent pending offers from showing.
		ready := s.crossTaskCoordinationRepairReady(ctx, binding) == nil
		if !ready {
			return false, nil
		}
	}
	if req.DecisionKind == core.HumanDecisionKindStoppedCheckRecovery {
		b, err := core.ParseStoppedCheckRecoveryBinding([]byte(req.BindingJSON))
		if err != nil {
			return false, err
		}
		return b.DevelopmentRequirementID == req.DevelopmentRequirementID && s.stoppedCheckRecoveryReady(ctx, b) == "", nil
	}
	if req.DecisionKind == core.HumanDecisionKindExtraCoordination {
		b, err := core.ParseExtraCoordinationBinding([]byte(req.BindingJSON))
		if err != nil {
			return false, err
		}
		if b.DevelopmentRequirementID != req.DevelopmentRequirementID {
			return false, nil
		}
		return s.extraCoordinationReady(ctx, b, false) == "", nil
	}
	if req.DecisionKind == core.HumanDecisionKindBuilderReplacement {
		binding, err := core.ParseBuilderReplacementBinding([]byte(req.BindingJSON))
		if err != nil {
			return false, err
		}
		state, err := s.builderReplacementState(ctx, req.DevelopmentRequirementID)
		if err != nil {
			return false, err
		}
		if state.DecisionRequestID != req.ID || state.Binding != binding {
			return false, nil
		}
		return s.replacementExternalCurrent(ctx, state) == nil, nil
	}
	if req.DecisionKind == core.HumanDecisionKindExtraPlanningAttempt {
		binding, err := core.ParseExtraPlanningAttemptBinding([]byte(req.BindingJSON))
		if err != nil {
			return false, err
		}
		if binding.DevelopmentRequirementID != req.DevelopmentRequirementID {
			return false, nil
		}
		store, ok := s.complex.(extraPlanningAttemptStore)
		if !ok {
			return false, nil
		}
		state, err := store.ReadClearDevExtraPlanningAttempt(ctx, req.DevelopmentRequirementID, s.now().UTC())
		if err != nil {
			return false, err
		}
		if state.DecisionRequestID != req.ID || state.Binding != binding || state.Option.UnavailableReason != "EXTRA_ATTEMPT_DECISION_PENDING" {
			return false, nil
		}
		return s.planningRecoveryReady(ctx, extraPlanningSource(state), false) == "", nil
	}
	state, err := s.projectPlanningState(ctx, req.DevelopmentRequirementID)
	if err != nil {
		return false, err
	}
	return state == nil || (state.Current && state.SourceCurrent && state.ReasonCode == core.ReasonNone), nil
}

func (s *Service) ApplyHumanDecisionResult(ctx context.Context, result core.HumanDecisionResult) error {
	if result.DecisionKind == core.HumanDecisionKindCoordinationRepair && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParseCoordinationRepairBinding(result.Binding)
		if err != nil {
			return err
		}
		if err := s.crossTaskCoordinationRepairReady(ctx, binding); err != nil {
			return apierr.Conflict("COORDINATION_REPAIR_NOT_READY", "Original coordination source is not safely recoverable", nil)
		}
	}
	if result.DecisionKind == core.HumanDecisionKindStoppedCheckRecovery && result.Decision == core.HumanDecisionApprove {
		b, err := core.ParseStoppedCheckRecoveryBinding(result.Binding)
		if err != nil {
			return err
		}
		if reason := s.stoppedCheckRecoveryReady(ctx, b); reason != "" {
			return apierr.Conflict(reason, "The stopped checker source or receipt is no longer current", nil)
		}
	}
	if result.DecisionKind == core.HumanDecisionKindExtraCoordination && result.Decision == core.HumanDecisionApprove {
		b, err := core.ParseExtraCoordinationBinding(result.Binding)
		if err != nil {
			return err
		}
		if reason := s.extraCoordinationReady(ctx, b, false); reason != "" {
			return apierr.Conflict(reason, "The approved coordination source or execution result is no longer current", nil)
		}
	}
	if s.humanDecisions == nil {
		return nil
	}
	if result.DecisionKind == core.HumanDecisionKindBuilderReplacement && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParseBuilderReplacementBinding(result.Binding)
		if err != nil {
			return err
		}
		state, err := s.builderReplacementState(ctx, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if state.Binding != binding {
			return apierr.Conflict("BUILDER_REPLACEMENT_NOT_CURRENT", "Original worker replacement changed", nil)
		}
		if err := s.replacementExternalCurrent(ctx, state); err != nil {
			return apierr.Conflict("BUILDER_REPLACEMENT_NOT_READY", "Original worker, source or sealed code could not be verified", nil)
		}
	}
	// Git is external to SQLite. Observe it at the actual receipt boundary;
	// the settlement transaction independently rechecks discussion/authority.
	if result.DecisionKind == core.HumanDecisionKindConfirmVersion && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParseConfirmRequirementBinding(result.Binding)
		if err != nil {
			return err
		}
		state, err := s.projectPlanningState(ctx, binding.DevelopmentRequirementID)
		if err != nil {
			return err
		}
		if state != nil && (!state.Current || !state.SourceCurrent || state.ReasonCode != core.ReasonNone) {
			return apierr.Conflict(string(state.ReasonCode), "The project discussion or selected repository is no longer current; this specification cannot be approved", nil)
		}
	}
	if result.DecisionKind == core.HumanDecisionKindExtraPlanningAttempt && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParseExtraPlanningAttemptBinding(result.Binding)
		if err != nil {
			return err
		}
		store, ok := s.complex.(extraPlanningAttemptStore)
		if !ok {
			return apierr.Conflict("RECOVERY_UNAVAILABLE", "Extra planning recovery is unavailable", nil)
		}
		state, err := store.ReadClearDevExtraPlanningAttempt(ctx, binding.DevelopmentRequirementID, s.now().UTC())
		if err != nil {
			return err
		}
		if reason := s.planningRecoveryReady(ctx, extraPlanningSource(state), false); reason != "" {
			return apierr.Conflict(reason, "Original planning source or session changed", nil)
		}
	}
	if err := s.humanDecisions.SettleClearDevHumanDecision(ctx, result, s.now().UTC()); err != nil {
		return err
	}
	if result.DecisionKind == core.HumanDecisionKindFinalReviewRecheck && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParseFinalReviewRecheckBinding(result.Binding)
		if err != nil {
			return err
		}
		s.scheduleComplexStandardExecution(binding.DevelopmentRequirementID)
	}
	if result.DecisionKind == core.HumanDecisionKindPlanningRecovery && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParsePlanningRecoveryBinding(result.Binding)
		if err != nil {
			return err
		}
		s.scheduleComplexFlow(binding.DevelopmentRequirementID)
	}
	if result.DecisionKind == core.HumanDecisionKindExtraMailAttempt && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParseExtraMailAttemptBinding(result.Binding)
		if err != nil {
			return err
		}
		s.scheduleComplexStandardExecution(binding.DevelopmentRequirementID)
	}
	if result.DecisionKind == core.HumanDecisionKindExtraReviewBudget && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParseExtraReviewBudgetBinding(result.Binding)
		if err != nil {
			return err
		}
		// The budget effect commits inside the settlement transaction; only
		// the wake-up happens here.
		s.scheduleComplexFlow(binding.DevelopmentRequirementID)
	}
	if result.DecisionKind == core.HumanDecisionKindExtraBuilderTurn && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParseExtraBuilderTurnBinding(result.Binding)
		if err != nil {
			return err
		}
		// The budget effect commits inside the settlement transaction; only
		// the wake-up happens here.
		s.scheduleComplexStandardExecution(binding.DevelopmentRequirementID)
	}
	if result.DecisionKind == core.HumanDecisionKindExtraCoordination && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParseExtraCoordinationBinding(result.Binding)
		if err != nil {
			return err
		}
		s.scheduleComplexStandardExecution(binding.DevelopmentRequirementID)
	}
	if result.DecisionKind == core.HumanDecisionKindStoppedCheckRecovery && result.Decision == core.HumanDecisionApprove {
		b, err := core.ParseStoppedCheckRecoveryBinding(result.Binding)
		if err != nil {
			return err
		}
		s.scheduleComplexStandardExecution(b.DevelopmentRequirementID)
	}
	if result.DecisionKind == core.HumanDecisionKindCoordinationRepair && result.Decision == core.HumanDecisionApprove {
		binding, err := core.ParseCoordinationRepairBinding(result.Binding)
		if err != nil {
			return err
		}
		// The budget grant and reopened attempt commit inside the settlement
		// transaction; only the wake-up happens here. Without it the reworked
		// task stays undispatched until some unrelated entry point runs.
		s.scheduleComplexStandardExecution(binding.DevelopmentRequirementID)
	}
	if result.DecisionKind == core.HumanDecisionKindProductPlan && result.Decision == core.HumanDecisionApprove {
		b, e := core.ParseProductPlanBinding(result.Binding)
		if e != nil {
			return e
		}
		s.scheduleComplexFlow(b.ProductID)
	}
	if result.DecisionKind == core.HumanDecisionKindConfirmVersion {
		if result.Decision == core.HumanDecisionApprove {
			binding, err := core.ParseConfirmRequirementBinding(result.Binding)
			if err == nil {
				s.scheduleComplexFlow(binding.DevelopmentRequirementID)
				if s.direction != nil {
					change, exists, changeErr := s.direction.GetClearDevDirectionChange(ctx, binding.DevelopmentRequirementID)
					if changeErr == nil && exists && change.Revision != nil &&
						change.Revision.TargetRequirementVersionID == binding.RequirementVersionID {
						s.scheduleDirectionChange(binding.DevelopmentRequirementID)
					}
				}
			}
		}
	}
	if result.DecisionKind == core.HumanDecisionKindApproveDirectionChange {
		binding, err := core.ParseApproveDirectionChangeBinding(result.Binding)
		if err == nil {
			if result.Decision == core.HumanDecisionApprove {
				s.scheduleDirectionChange(binding.DevelopmentRequirementID)
			}
			if result.Decision == core.HumanDecisionReject {
				s.scheduleStandardFlow(binding.DevelopmentRequirementID)
				s.scheduleComplexFlow(binding.DevelopmentRequirementID)
			}
		}
	}
	return nil
}

func (s *Service) DismissHumanDecisionDispatch(ctx context.Context, command core.DismissHumanDecisionDispatchCommand) error {
	if s.humanDecisions == nil {
		return nil
	}
	if command.At.IsZero() {
		command.At = s.now().UTC()
	}
	return s.humanDecisions.DismissClearDevHumanDecisionDispatch(ctx, command)
}

func (s *Service) DismissOpenHumanDecisionDispatches(ctx context.Context, desktopRunID string, outcome core.HumanDecisionDispatchOutcome, at time.Time) error {
	if s.humanDecisions == nil {
		return nil
	}
	if at.IsZero() {
		at = s.now().UTC()
	}
	return s.humanDecisions.DismissOpenClearDevHumanDecisionDispatches(ctx, desktopRunID, outcome, at)
}

// RequestExtraReviewBudget asks the person at the desktop to authorize one
// more reviewer turn for the rework round of one task. It records the request
// and nothing else: only the native Human Authority decision grants the turn.
// requestExtraBudgetTurn records the pending offer behind one extra budget
// turn; only the native Human Authority decision grants it.
func (s *Service) requestExtraBudgetTurn(ctx context.Context, requirementID, taskID, roleKind, missingCode, missingText string, create func(context.Context, core.DevelopmentRequirement, core.ExtraReviewBudgetBinding, time.Time) (string, error)) (RequirementView, error) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return RequirementView{}, err
	}
	if s.humanDecisions == nil || s.complexExecution == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev decision stores are not configured")
	}
	execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil {
		return RequirementView{}, mapStoreError(err, "READ_EXECUTION_FAILED")
	}
	if !found {
		return RequirementView{}, apierr.NotFound("EXECUTION_NOT_FOUND", "No project execution is running for this requirement")
	}
	// The desktop names tasks by their work item; the execution snapshot keys
	// them by mapping id, so accept either and use the snapshot's own id.
	mappingID, workItemID, round := "", "", -1
	for _, task := range execution.Tasks {
		if task.ID == taskID || task.DevelopmentTaskID == taskID {
			mappingID, workItemID, round = task.ID, task.DevelopmentTaskID, task.CurrentRound
		}
	}
	if round < 0 {
		return RequirementView{}, apierr.NotFound("TASK_NOT_FOUND", "The development task is not part of this execution")
	}
	budgetID := exceptionBudgetID(execution, mappingID, roleKind)
	if budgetID == "" {
		return RequirementView{}, apierr.Invalid(missingCode, missingText, nil)
	}
	snapshot, exists, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return RequirementView{}, mapStoreError(err, "READ_REQUIREMENT_FAILED")
	}
	if !exists {
		return RequirementView{}, apierr.NotFound("REQUIREMENT_NOT_FOUND", "ClearDev requirement was not found")
	}
	binding := core.ExtraReviewBudgetBinding{
		DevelopmentRequirementID: requirementID, ExecutionRunID: execution.Run.ID,
		TaskID: workItemID, BudgetID: budgetID, Round: round,
	}
	if _, err := create(ctx, snapshot.Requirement, binding, s.now().UTC()); err != nil {
		return RequirementView{}, mapStoreError(err, "CREATE_BUDGET_AUTHORIZATION_FAILED")
	}
	return s.GetRequirement(ctx, requirementID)
}

// RequestExtraReviewBudget asks the person at the desktop to authorize one
// more reviewer turn for the rework round of one task. It records the request
// and nothing else: only the native Human Authority decision grants the turn.
func (s *Service) RequestExtraReviewBudget(ctx context.Context, requirementID, taskID string) (RequirementView, error) {
	return s.requestExtraBudgetTurn(ctx, requirementID, taskID, core.ComplexExceptionBudgetReviewer,
		"REVIEW_BUDGET_NOT_FOUND", "No reviewer budget exists for this task", s.humanDecisions.CreateExtraReviewBudgetRequest)
}

// RequestControlledEngineChange records the pending human decision offer for
// one deliberate mid-project engine change of a controlled project. It records
// the request and nothing else: only the native Human Authority decision
// authorizes changing the frozen engine choice and switching its sessions.
func (s *Service) RequestControlledEngineChange(ctx context.Context, requirementID string, binding core.ControlledEngineChangeBinding) (RequirementView, error) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return RequirementView{}, err
	}
	if s.humanDecisions == nil || s.complexExecution == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev decision stores are not configured")
	}
	execution, found, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil {
		return RequirementView{}, mapStoreError(err, "READ_EXECUTION_FAILED")
	}
	if !found {
		return RequirementView{}, apierr.NotFound("EXECUTION_NOT_FOUND", "No project execution is running for this requirement")
	}
	contract, project, err := core.ProjectContractFromRun(execution.Run)
	if err != nil || !project {
		return RequirementView{}, apierr.Invalid("ENGINE_CHANGE_UNSUPPORTED", "Engine change requires an admitted project execution", nil)
	}
	snapshot, exists, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return RequirementView{}, mapStoreError(err, "READ_REQUIREMENT_FAILED")
	}
	if !exists {
		return RequirementView{}, apierr.NotFound("REQUIREMENT_NOT_FOUND", "ClearDev requirement was not found")
	}
	binding.DevelopmentRequirementID = requirementID
	binding.AOProjectID = contract.Selection.AOProjectID
	raw, err := json.Marshal(binding)
	if err != nil {
		return RequirementView{}, mapStoreError(err, "ENGINE_CHANGE_INVALID")
	}
	if _, err := core.ParseControlledEngineChangeBinding(raw); err != nil {
		return RequirementView{}, apierr.Invalid("ENGINE_CHANGE_INVALID", err.Error(), nil)
	}
	if _, err := s.humanDecisions.CreateControlledEngineChangeRequest(ctx, snapshot.Requirement, binding, s.now().UTC()); err != nil {
		return RequirementView{}, mapStoreError(err, "CREATE_ENGINE_CHANGE_AUTHORIZATION_FAILED")
	}
	return s.GetRequirement(ctx, requirementID)
}

// RequestExtraBuilderTurn asks the person at the desktop to authorize the
// controlled recovery of one builder round that stopped because its rework
// budget ran out. It records the request and nothing else: only the native
// Human Authority decision grants the extra turn.
func (s *Service) RequestExtraBuilderTurn(ctx context.Context, requirementID, taskID string) (RequirementView, error) {
	return s.requestExtraBudgetTurn(ctx, requirementID, taskID, core.ComplexExceptionBudgetBuilder,
		"BUILDER_BUDGET_NOT_FOUND", "No builder budget exists for this task", s.humanDecisions.CreateExtraBuilderTurnRequest)
}
