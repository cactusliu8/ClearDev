package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func messageBudgetError(code core.ReasonCode, detail string) error {
	return &core.RuleError{Code: code, Message: detail}
}

// ReserveClearDevAgentMessage atomically reserves a stable message and its delivery boundary.
func (s *Store) ReserveClearDevAgentMessage(ctx context.Context, command core.ReserveAgentMessageCommand) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	created := false
	err := s.inTx(ctx, "reserve ClearDev Agent message", func(q *gen.Queries) error {
		input := command.Attempt
		event := command.Boundary
		row, err := q.GetClearDevAgentAttemptState(ctx, gen.GetClearDevAgentAttemptStateParams{DevelopmentProjectID: input.DevelopmentRequirementID, ID: input.ID})
		if err != nil {
			return err
		}
		attempt := agentStepAttemptToDomain(row)
		if !core.SameAgentStepAttempt(attempt, input) || !core.ValidAgentMessageIdentity(attempt, command.Source, event.ClientMessageID, event.PromptSHA256) || event.AttemptID != attempt.ID || event.Status != core.AgentAttemptDeliveryUnknown {
			return messageBudgetError(core.ReasonMessageBudgetBinding, "message does not match immutable attempt")
		}
		version, err := q.GetClearDevMessageBudgetVersion(ctx, attempt.DevelopmentRequirementID)
		if errors.Is(err, sql.ErrNoRows) {
			return messageBudgetError(core.ReasonMessageBudgetUnknown, "requirement has no message budget version")
		}
		if err != nil {
			return err
		}
		if version.Version != string(core.MessageBudgetV1) {
			old, err := q.GetLatestClearDevAgentMessageEvent(ctx, gen.GetLatestClearDevAgentMessageEventParams{AttemptID: attempt.ID, ClientMessageID: event.ClientMessageID})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if old.PromptSha256 != "" && old.PromptSha256 != event.PromptSHA256 {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "legacy message prompt changed")
			}
			return messageBudgetError(core.ReasonMessageBudgetUnknown, "historical message usage is unknown; only reconcile existing messages")
		}
		budget, err := resolveMessageRoleBudget(ctx, q, attempt)
		if err != nil {
			return err
		}
		reservation := core.AgentMessageReservation{ClientMessageID: event.ClientMessageID, DevelopmentRequirementID: attempt.DevelopmentRequirementID, BudgetVersion: core.MessageBudgetV1, LogicalStepID: attempt.LogicalStepID, AttemptID: attempt.ID, Source: command.Source, AOSessionID: attempt.AOSessionID, PromptSHA256: event.PromptSHA256, BudgetID: budget.ID, ReservedAt: event.RecordedAt}
		saved, err := q.GetClearDevAgentMessageReservation(ctx, event.ClientMessageID)
		if err == nil {
			if !core.SameAgentMessageReservation(messageReservationFromRow(saved), reservation) {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "message reservation bindings changed")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if authorized, err := authorizedBuilderReplacementAttempt(ctx, q, attempt); err != nil {
			return err
		} else if authorized {
			if _, found, err := builderReplacementBeforeSend(ctx, q, attempt.DevelopmentRequirementID, attempt.LogicalStepID); err != nil || !found {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "Builder replacement grant or source changed before delivery")
			}
		}
		if err := validatePlannerAnswerBeforeSend(ctx, q, attempt); err != nil {
			return messageBudgetError(core.ReasonMessageBudgetBinding, "Planner continuation source, question or original session changed")
		}
		if attempt.StepCategory == core.AgentStepCategoryComplexPlanning && attempt.StepKind == core.ComplexAgentStepCompilation && attempt.AttemptNumber == 2 {
			state, registered, err := planningRecoveryBeforeSend(ctx, q, attempt.DevelopmentRequirementID, attempt.LogicalStepID, event.RecordedAt)
			if err != nil {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "planning continuation source or failure evidence changed")
			}
			if registered {
				// Eligibility may admit an exited original session so it can
				// be restored. Actual message admission requires it idle.
				session, err := q.GetSession(ctx, domain.SessionID(state.Binding.AOSessionID))
				if err != nil {
					return err
				}
				if session.ActivityState != domain.ActivityIdle {
					return messageBudgetError(core.ReasonMessageBudgetBinding, "original planning session is not idle before delivery")
				}
			}
		}
		if command.Source == core.AgentMessageHumanAuthorizedOriginal {
			state, found, err := extraPlanningBeforeSend(ctx, q, attempt.DevelopmentRequirementID, attempt.LogicalStepID, event.RecordedAt)
			if err != nil || !found || (state.Option.UnavailableReason != "" && state.Option.UnavailableReason != "CONTINUATION_REGISTERED") {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "extra planning grant or current source changed")
			}
			b := state.Binding.Source
			if attempt.AttemptNumber != 3 || attempt.ID != b.LogicalStepID+":attempt:3" || attempt.ClientMessageID != b.ClientMessageID+":attempt:3" || attempt.PromptSHA256 != b.PromptSHA256 || attempt.AOSessionID != b.AOSessionID || attempt.RoleBindingID != b.RoleBindingID || attempt.TriggerFailureEventID != state.Binding.FailureEventID || budget.ID != "" {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "extra planning attempt binding changed")
			}
			session, err := q.GetSession(ctx, domain.SessionID(b.AOSessionID))
			if err != nil {
				return err
			}
			if session.ActivityState != domain.ActivityIdle {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "original planning session is not idle")
			}
		}
		if attempt.AttemptNumber == 1 && command.Source == core.AgentMessageOriginal {
			if err := validateBuilderSessionRecheckBeforeSend(ctx, q, attempt); err != nil {
				return err
			}
		}
		// A boundary saved before this ledger is not new permission to send.
		if _, err := q.GetLatestClearDevAgentMessageEvent(ctx, gen.GetLatestClearDevAgentMessageEventParams{AttemptID: attempt.ID, ClientMessageID: event.ClientMessageID}); err == nil {
			return messageBudgetError(core.ReasonMessageBudgetUnknown, "existing delivery boundary has no measured reservation")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		messages, err := q.ListClearDevStepMessageReservations(ctx, attempt.LogicalStepID)
		if err != nil {
			return err
		}
		stepCount := 0
		for _, m := range messages {
			if m.LogicalStepID == attempt.LogicalStepID {
				stepCount++
				if m.Source == string(command.Source) {
					return messageBudgetError(core.ReasonMessageBudgetExhausted, "logical step already reserved this message source")
				}
			}
		}
		ceiling := 3
		if command.Source == core.AgentMessageHumanAuthorizedOriginal {
			ceiling = 4
		}
		if stepCount >= ceiling {
			return messageBudgetError(core.ReasonMessageBudgetExhausted, "logical step message budget exhausted")
		}
		if budget.ID != "" {
			count, err := q.CountClearDevRoleMessages(ctx, sql.NullString{String: budget.ID, Valid: true})
			if err != nil {
				return err
			}
			if count >= (budget.MaxTurns+budget.AuthorizedExtraTurns)*5 {
				return messageBudgetError(core.ReasonMessageBudgetExhausted, "role message budget exhausted")
			}
		}
		if err := q.InsertClearDevAgentMessageReservation(ctx, gen.InsertClearDevAgentMessageReservationParams{ClientMessageID: reservation.ClientMessageID, DevelopmentProjectID: reservation.DevelopmentRequirementID, BudgetVersion: string(reservation.BudgetVersion), LogicalStepID: reservation.LogicalStepID, AttemptID: reservation.AttemptID, Source: string(reservation.Source), AoSessionID: reservation.AOSessionID, PromptSha256: reservation.PromptSHA256, BudgetID: nullableString(reservation.BudgetID), ReservedAt: reservation.ReservedAt}); err != nil {
			return err
		}
		if err := insertMessageEvent(ctx, q, event); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created && err == nil, err
}

func resolveMessageRoleBudget(ctx context.Context, q *gen.Queries, a core.AgentStepAttempt) (gen.CleardevComplexExceptionBudget, error) {
	var empty gen.CleardevComplexExceptionBudget
	if a.StepKind == core.AgentStepRequirementFinalReview {
		// One immutable final step per execution, using the existing three-message
		// step ceiling. It cannot consume or masquerade as a task review budget.
		return empty, finalReviewMessageBudget(ctx, q, a)
	}
	bindings, err := q.GetClearDevMessageExecutionBinding(ctx, a.LogicalStepID)
	if err != nil {
		return empty, err
	}
	if len(bindings) == 0 {
		if _, boundErr := q.GetClearDevMessageRoleBudget(ctx, a.LogicalStepID); boundErr == nil {
			return empty, messageBudgetError(core.ReasonMessageBudgetBinding, "budget occupancy has no matching execution step")
		} else if !errors.Is(boundErr, sql.ErrNoRows) {
			return empty, boundErr
		}
		if a.StepCategory == core.AgentStepCategoryComplexExecution || a.StepCategory == core.AgentStepCategoryException {
			return empty, messageBudgetError(core.ReasonMessageBudgetBinding, "controlled execution step binding is missing")
		}
		return empty, nil
	}
	if len(bindings) != 1 {
		return empty, messageBudgetError(core.ReasonMessageBudgetBinding, "ambiguous execution step binding")
	}
	binding := bindings[0]
	if authorized, err := authorizedReplacementAttempt(ctx, q, a); err != nil {
		return empty, err
	} else if authorized {
		binding.RoleBindingID = a.RoleBindingID
		binding.AoSessionID = nullableString(a.AOSessionID)
	}
	if authorized, err := authorizedBuilderReplacementAttempt(ctx, q, a); err != nil {
		return empty, err
	} else if authorized {
		binding.RoleBindingID = a.RoleBindingID
		binding.AoSessionID = nullableString(a.AOSessionID)
	}
	baseMessage := a.ClientMessageID
	if a.AttemptNumber == 2 {
		baseMessage = strings.TrimSuffix(baseMessage, ":attempt:2")
	}
	if binding.Category != string(a.StepCategory) || binding.DevelopmentProjectID != a.DevelopmentRequirementID || binding.RoleBindingID != a.RoleBindingID || binding.AoSessionID.String != a.AOSessionID || binding.ClientMessageID != baseMessage || binding.PromptSha256 != a.PromptSHA256 || binding.StepKind != string(a.StepKind) {
		return empty, messageBudgetError(core.ReasonMessageBudgetBinding, "execution message changed role, session, category or task binding")
	}
	if binding.Role == "STEWARD" {
		if _, err := q.GetClearDevMessageRoleBudget(ctx, a.LogicalStepID); !errors.Is(err, sql.ErrNoRows) {
			if err != nil {
				return empty, err
			}
			return empty, messageBudgetError(core.ReasonMessageBudgetBinding, "dispatch step has an incompatible role occupancy")
		}
		return empty, nil
	}
	budget, err := q.GetClearDevMessageRoleBudget(ctx, a.LogicalStepID)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, messageBudgetError(core.ReasonMessageBudgetBinding, "execution step has no durable role budget occupancy")
	}
	if err != nil {
		return empty, err
	}
	if budget.ExecutionRunID != binding.ExecutionRunID || budget.RoleKind != binding.Role || budget.ComplexExecutionTaskID.String != binding.TaskID {
		return empty, messageBudgetError(core.ReasonMessageBudgetBinding, "step occupancy belongs to a different role or task")
	}
	return budget, nil
}

func insertMessageEvent(ctx context.Context, q *gen.Queries, event core.AgentAttemptEvent) error {
	rows, err := q.InsertClearDevAgentAttemptEvent(ctx, gen.InsertClearDevAgentAttemptEventParams{ID: event.ID, AttemptID: event.AttemptID, Status: string(event.Status), ClientMessageID: event.ClientMessageID, PromptSha256: event.PromptSHA256, TurnID: event.TurnID, TurnState: string(event.TurnState), FailureCategory: string(event.FailureCategory), Retryable: boolInt64(event.Retryable), RetryAt: nullableTimePtr(event.RetryAt), ProviderErrorCode: event.ProviderErrorCode, ErrorSummary: event.ErrorSummary, RecordedAt: event.RecordedAt})
	if err != nil {
		return err
	}
	if rows == 0 {
		saved, err := q.GetClearDevAgentAttemptEvent(ctx, event.ID)
		if err != nil {
			return err
		}
		if !core.SameAgentAttemptEvent(agentAttemptEventToDomain(saved), event) {
			return messageBudgetError(core.ReasonMessageBudgetBinding, "message event conflicts with saved evidence")
		}
	}
	return nil
}

// ConfirmClearDevAgentMessage saves an acknowledged send and event in one transaction.
func (s *Store) ConfirmClearDevAgentMessage(ctx context.Context, event core.AgentAttemptEvent) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "confirm ClearDev Agent message", func(q *gen.Queries) error {
		if strings.TrimSpace(event.TurnID) == "" || (event.Status != core.AgentAttemptSent && event.Status != core.AgentAttemptCorrectionSent) {
			return messageBudgetError(core.ReasonMessageBudgetBinding, "send confirmation requires a nonempty matching turn")
		}
		reservation, err := q.GetClearDevAgentMessageReservation(ctx, event.ClientMessageID)
		if errors.Is(err, sql.ErrNoRows) {
			// Legacy delivery reconciliation preserves known events without inventing measured totals.
			attempt, loadErr := q.GetClearDevAgentStepAttemptByID(ctx, event.AttemptID)
			if loadErr != nil {
				return loadErr
			}
			version, loadErr := q.GetClearDevMessageBudgetVersion(ctx, attempt.DevelopmentProjectID)
			if loadErr != nil {
				return loadErr
			}
			if version.Version != string(core.MessageBudgetLegacy) {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "send has no message reservation")
			}
			source := core.AgentMessageOriginal
			if attempt.AttemptNumber == 2 {
				source = core.AgentMessageRecoveryOriginal
			}
			if event.Status == core.AgentAttemptCorrectionSent {
				source = core.AgentMessageParseCorrection
			}
			if !core.ValidAgentMessageIdentity(agentStepAttemptToDomain(attempt), source, event.ClientMessageID, event.PromptSHA256) {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "legacy message identity changed")
			}
			old, loadErr := q.GetLatestClearDevAgentMessageEvent(ctx, gen.GetLatestClearDevAgentMessageEventParams{AttemptID: event.AttemptID, ClientMessageID: event.ClientMessageID})
			if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
				return loadErr
			}
			if (old.PromptSha256 != "" && old.PromptSha256 != event.PromptSHA256) || (old.TurnID != "" && old.TurnID != event.TurnID) {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "legacy message prompt changed")
			}
			return insertMessageEvent(ctx, q, event)
		}
		if err != nil {
			return err
		}
		if reservation.AttemptID != event.AttemptID || reservation.PromptSha256 != event.PromptSHA256 || (reservation.Source == string(core.AgentMessageParseCorrection)) != (event.Status == core.AgentAttemptCorrectionSent) {
			return messageBudgetError(core.ReasonMessageBudgetBinding, "send confirmation changed message identity")
		}
		saved, err := q.GetClearDevAgentMessageConfirmation(ctx, event.ClientMessageID)
		if err == nil {
			if saved.TurnID != event.TurnID {
				return messageBudgetError(core.ReasonMessageBudgetBinding, "confirmed turn cannot change")
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			if err := q.InsertClearDevAgentMessageConfirmation(ctx, gen.InsertClearDevAgentMessageConfirmationParams{ClientMessageID: event.ClientMessageID, TurnID: event.TurnID, ConfirmedAt: event.RecordedAt}); err != nil {
				return err
			}
		} else {
			return err
		}
		return insertMessageEvent(ctx, q, event)
	})
}

func messageReservationFromRow(r gen.CleardevAgentMessageReservation) core.AgentMessageReservation {
	return core.AgentMessageReservation{ClientMessageID: r.ClientMessageID, DevelopmentRequirementID: r.DevelopmentProjectID, BudgetVersion: core.MessageBudgetVersion(r.BudgetVersion), LogicalStepID: r.LogicalStepID, AttemptID: r.AttemptID, Source: core.AgentMessageSource(r.Source), AOSessionID: r.AoSessionID, PromptSHA256: r.PromptSha256, BudgetID: r.BudgetID.String, ReservedAt: r.ReservedAt}
}

// GetClearDevMessageBudget derives all counters from a single transaction snapshot.
func (s *Store) GetClearDevMessageBudget(ctx context.Context, requirementID string) (core.MessageBudgetView, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	view := core.MessageBudgetView{Steps: []core.MessageBudgetUsage{}, Roles: []core.MessageBudgetUsage{}}
	err := s.inTx(ctx, "read ClearDev message budget", func(q *gen.Queries) error {
		version, err := q.GetClearDevMessageBudgetVersion(ctx, requirementID)
		if errors.Is(err, sql.ErrNoRows) {
			view.UnknownReason = "MESSAGE_BUDGET_VERSION_MISSING"
		} else if err != nil {
			return err
		} else {
			view.BudgetVersion = core.MessageBudgetVersion(version.Version)
			if view.BudgetVersion != core.MessageBudgetV1 {
				view.UnknownReason = "LEGACY_MESSAGE_USAGE_UNKNOWN"
			}
		}
		stepIDs, err := q.ListClearDevMessageBudgetStepIDs(ctx, requirementID)
		if err != nil {
			return err
		}
		messages, err := q.ListClearDevAgentMessageReservations(ctx, requirementID)
		if err != nil {
			return err
		}
		confirmations, err := q.ListClearDevAgentMessageConfirmations(ctx, requirementID)
		if err != nil {
			return err
		}
		if view.BudgetVersion == core.MessageBudgetV1 {
			reserved, sent := int64(len(messages)), int64(len(confirmations))
			view.ReservedMessages = &reserved
			view.ConfirmedSentMessages = &sent
			view.UnknownReason = "REQUIREMENT_WIDE_LIMIT_NOT_APPLICABLE"
		}
		confirmed := map[string]bool{}
		for _, c := range confirmations {
			confirmed[c.ClientMessageID] = true
		}
		grants, err := q.ListClearDevPlanningExtraGrants(ctx, requirementID)
		if err != nil {
			return err
		}
		extraSteps := map[string]bool{}
		for _, grant := range grants {
			extraSteps[grant.LogicalStepID] = true
		}
		usage := func(scope, stepID, budgetID string, maxSteps, steps int64) core.MessageBudgetUsage {
			remaining := maxSteps - steps
			u := core.MessageBudgetUsage{Scope: scope, LogicalStepID: stepID, BudgetID: budgetID, BudgetVersion: view.BudgetVersion, MaxSteps: &maxSteps, ReservedSteps: &steps, RemainingSteps: &remaining}
			if view.BudgetVersion != core.MessageBudgetV1 {
				u.UnknownReason = view.UnknownReason
				return u
			}
			// A logical step keeps its tight three-message ceiling (original,
			// one correction, one recovery send). A role's per-turn fuse is five
			// messages: one round may legitimately carry a verdict plus a
			// check-results follow-up without spending the next round's turn.
			maximum := maxSteps * 3
			if scope == "LOGICAL_STEP" && extraSteps[stepID] {
				maximum++
			}
			if scope != "LOGICAL_STEP" {
				maximum = maxSteps * 5
			}
			reserved, sent := int64(0), int64(0)
			for _, m := range messages {
				if (scope == "LOGICAL_STEP" && m.LogicalStepID == stepID) || (scope != "LOGICAL_STEP" && m.BudgetID.String == budgetID) {
					reserved++
					if confirmed[m.ClientMessageID] {
						sent++
					}
				}
			}
			left := maximum - reserved
			u.MaxMessages = &maximum
			u.ReservedMessages = &reserved
			u.ConfirmedSentMessages = &sent
			u.RemainingMessages = &left
			return u
		}
		for _, stepID := range stepIDs {
			view.Steps = append(view.Steps, usage("LOGICAL_STEP", stepID, "", 1, 1))
		}
		budgets, err := q.ListClearDevRequirementRoleBudgets(ctx, requirementID)
		if err != nil {
			return err
		}
		for _, b := range budgets {
			scope := "EXECUTION_TASK_ROLE"
			if b.RoleKind == "RECOVERY" {
				scope = "EXECUTION_ROLE"
			}
			u := usage(scope, "", b.ID, b.MaxTurns+b.AuthorizedExtraTurns, b.UsedTurns)
			u.ExecutionRunID = b.ExecutionRunID
			u.ComplexExecutionTaskID = b.ComplexExecutionTaskID.String
			u.RoleKind = b.RoleKind
			view.Roles = append(view.Roles, u)
		}
		if len(budgets) == 0 {
			view.Roles = append(view.Roles, core.MessageBudgetUsage{Scope: "NOT_APPLICABLE", BudgetVersion: view.BudgetVersion, UnknownReason: "ROLE_BUDGET_NOT_APPLICABLE"})
		}
		return nil
	})
	return view, err
}
