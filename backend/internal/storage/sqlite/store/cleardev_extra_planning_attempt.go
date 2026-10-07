package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

type extraPlanningProof struct {
	state   core.ExtraPlanningAttemptState
	step    gen.CleardevComplexAgentStep
	first   gen.CleardevAgentStepAttempt
	second  gen.CleardevAgentStepAttempt
	failure gen.CleardevAgentAttemptEvent
}

// ReadClearDevExtraPlanningAttempt derives an offer from persistent facts.
// It never grants a budget, reopens a step or performs an external operation.
func (s *Store) ReadClearDevExtraPlanningAttempt(ctx context.Context, id string, at time.Time) (core.ExtraPlanningAttemptState, error) {
	p, err := extraPlanningEvidence(ctx, s.qr, id, at)
	if err != nil {
		return p.state, err
	}
	if err := extraPlanningOfferState(ctx, s.qr, &p); err != nil {
		return p.state, err
	}
	p.state.History, err = extraPlanningHistory(ctx, s.qr, id)
	return p.state, err
}

func extraPlanningEvidence(ctx context.Context, q *gen.Queries, id string, at time.Time) (extraPlanningProof, error) {
	var p extraPlanningProof
	step, err := q.GetClearDevPlanningContinuationStep(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.step = step
	attempts, err := q.ListClearDevAgentStepAttemptStates(ctx, gen.ListClearDevAgentStepAttemptStatesParams{DevelopmentProjectID: id, LogicalStepID: step.ID})
	if err != nil || len(attempts) < 2 {
		return p, err
	}
	p.state.Option = core.WorkflowRecoveryOption{Action: core.RecoveryRequestExtraPlanningAttempt, TargetID: step.ID, Role: "STEWARD", Reason: step.ReasonCode, UnavailableReason: "RESULT_NOT_SETTLED"}
	if len(attempts) > 3 {
		return p, nil
	}
	p.first, p.second = attempts[0], attempts[1]
	b, selection, current, err := planningContinuationSource(ctx, q, id, step)
	p.state.Selection = selection
	p.state.Binding.DevelopmentRequirementID = id
	p.state.Binding.Source = b
	if err != nil {
		return p, err
	}
	if !current || step.SendStatus == "SETTLED" {
		p.state.Option.UnavailableReason = "PLANNING_NOT_CURRENT"
		return p, nil
	}
	version, err := q.GetClearDevMessageBudgetVersion(ctx, id)
	if err != nil || version.Version != string(core.MessageBudgetV1) {
		p.state.Option.UnavailableReason = string(core.ReasonMessageBudgetUnknown)
		return p, missingBuilderProof(err)
	}
	for index, attempt := range []gen.CleardevAgentStepAttempt{p.first, p.second} {
		expectedClient := step.ClientMessageID
		if index == 1 {
			expectedClient += ":attempt:2"
		}
		if attempt.AttemptNumber != int64(index+1) || attempt.DevelopmentProjectID != id || attempt.LogicalStepID != step.ID ||
			attempt.StepCategory != string(core.AgentStepCategoryComplexPlanning) || attempt.StepKind != string(core.ComplexAgentStepCompilation) ||
			attempt.RoleBindingID != b.RoleBindingID || attempt.AoSessionID != b.AOSessionID || attempt.ClientMessageID != expectedClient || attempt.PromptSha256 != step.PromptSha256 ||
			!core.ValidAgentAttemptCreation(agentStepAttemptToDomain(attempt)) || at.Before(attempt.CreatedAt.Time) {
			return p, nil
		}
	}
	if p.first.TriggerFailureEventID.Valid || p.first.TriggerFailureEventID.String != "" {
		return p, nil
	}
	if len(attempts) == 3 {
		third := attempts[2]
		continuation, err := q.GetClearDevPlanningExtraContinuationForStep(ctx, step.ID)
		if err != nil {
			return p, missingBuilderProof(err)
		}
		if third.ID != step.ID+":attempt:3" || third.ID != continuation.ThirdAttemptID || third.AttemptNumber != 3 || third.DevelopmentProjectID != id ||
			third.LogicalStepID != step.ID || third.StepCategory != p.second.StepCategory || third.StepKind != p.second.StepKind ||
			third.RoleBindingID != b.RoleBindingID || third.AoSessionID != b.AOSessionID || third.ClientMessageID != step.ClientMessageID+":attempt:3" || third.PromptSha256 != step.PromptSha256 ||
			!core.ValidAgentAttemptCreation(agentStepAttemptToDomain(third)) || at.Before(third.CreatedAt.Time) {
			return p, nil
		}
		if _, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, third.ID); err == nil {
			p.state.Option.UnavailableReason = "EXTRA_ATTEMPT_CONSUMED"
			return p, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return p, err
		}
		if step.SendStatus != "PENDING" {
			return p, nil
		}
	} else if step.SendStatus != "SENT" && step.SendStatus != "FAILED" {
		return p, nil
	}
	correction, correctionErr := q.GetClearDevParseCorrection(ctx, step.ID)
	if correctionErr != nil && !errors.Is(correctionErr, sql.ErrNoRows) {
		return p, correctionErr
	}
	if correctionErr == nil && (correction.AttemptNumber < 1 || correction.AttemptNumber > 2 || correction.ClientMessageID != step.ClientMessageID+":parse-correction" || correction.PromptSha256 != complexExecutionRawDigest([]byte(correction.PromptText))) {
		return p, nil
	}
	messages := []invalidBuilderMessage{}
	for _, attempt := range []gen.CleardevAgentStepAttempt{p.first, p.second} {
		source := core.AgentMessageOriginal
		if attempt.AttemptNumber == 2 {
			source = core.AgentMessageRecoveryOriginal
		}
		messages = append(messages, invalidBuilderMessage{attempt: attempt, id: attempt.ClientMessageID, prompt: attempt.PromptSha256, source: string(source), resultKind: string(core.AgentResultOriginal), result: 1, sendStatus: "SENT"})
		if correctionErr == nil && correction.AttemptNumber == attempt.AttemptNumber {
			messages = append(messages, invalidBuilderMessage{attempt: attempt, id: attempt.ClientMessageID + ":parse-correction", prompt: correction.PromptSha256, source: string(core.AgentMessageParseCorrection), resultKind: string(core.AgentResultCorrection), result: 2, sendStatus: "CORRECTION_SENT"})
		}
		failure, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, attempt.ID)
		if err != nil {
			return p, missingBuilderProof(err)
		}
		if (failure.Status != "FAILED" && failure.Status != "INTERRUPTED") || (failure.TurnState != "failed" && failure.TurnState != "interrupted") ||
			!planningFailureCanContinue(failure.FailureCategory) || failure.ClientMessageID != messages[len(messages)-1].id || at.Before(failure.RecordedAt) {
			return p, nil
		}
		if failure.RetryAt.Valid && at.Before(failure.RetryAt.Time) {
			p.state.Option.UnavailableReason = "RETRY_NOT_DUE"
			return p, nil
		}
		if attempt.AttemptNumber == 1 {
			p.state.Binding.Source.FirstAttemptID = attempt.ID
			p.state.Binding.Source.FailureEventID = failure.ID
		} else {
			p.failure = failure
			p.state.Binding.SecondAttemptID = attempt.ID
			p.state.Binding.FailureEventID = failure.ID
			p.state.Option.Summary = failure.ErrorSummary
		}
	}
	if !p.second.TriggerFailureEventID.Valid || p.second.TriggerFailureEventID.String != p.state.Binding.Source.FailureEventID {
		return p, nil
	}
	if len(attempts) == 3 && attempts[2].TriggerFailureEventID.String != p.failure.ID {
		return p, nil
	}
	reservations, err := q.ListClearDevStepMessageReservations(ctx, step.ID)
	if err != nil {
		return p, err
	}
	oldCount := 0
	for _, r := range reservations {
		if r.AttemptID == p.first.ID || r.AttemptID == p.second.ID {
			oldCount++
		} else {
			return p, nil // A reserved third is consumed, not fresh permission.
		}
	}
	if oldCount != len(messages) || oldCount < 2 || oldCount > 3 {
		return p, nil
	}
	for _, message := range messages {
		r, err := q.GetClearDevAgentMessageReservation(ctx, message.id)
		if err != nil {
			return p, missingBuilderProof(err)
		}
		if r.DevelopmentProjectID != id || r.LogicalStepID != step.ID || r.AttemptID != message.attempt.ID || r.AoSessionID != b.AOSessionID ||
			r.BudgetVersion != string(core.MessageBudgetV1) || r.BudgetID.Valid || r.PromptSha256 != message.prompt || r.Source != message.source {
			return p, nil
		}
		if _, ended, err := invalidBuilderMessageEnded(ctx, q, message); err != nil || !ended {
			return p, err
		}
		confirmation, err := q.GetClearDevAgentMessageConfirmation(ctx, message.id)
		if err != nil {
			return p, missingBuilderProof(err)
		}
		p.state.Messages = append(p.state.Messages, core.PlanningStepRecoveryMessage{ClientMessageID: message.id, PromptSHA256: message.prompt, TurnID: confirmation.TurnID})
	}
	session, err := q.GetSession(ctx, domain.SessionID(b.AOSessionID))
	if err != nil {
		return p, err
	}
	if session.ActivityState != domain.ActivityIdle && session.ActivityState != domain.ActivityExited {
		return p, nil
	}
	active, err := q.CountClearDevUnsettledSessionTurns(ctx, domain.SessionID(b.AOSessionID))
	if err != nil || active != 0 {
		return p, err
	}
	p.state.Binding.MessagesSHA256, err = extraPlanningMessagesDigest(ctx, q, id, p.first, p.second, reservations, correction, correctionErr == nil)
	if err != nil {
		return p, err
	}
	p.state.Option.TargetID, err = core.ExtraPlanningAttemptTarget(p.state.Binding)
	if err == nil {
		p.state.Option.UnavailableReason = ""
	}
	return p, err
}

// The digest retains every original event/result/parse, not only the last
// failure. An extra send never changes this two-attempt authority fingerprint.
func extraPlanningMessagesDigest(ctx context.Context, q *gen.Queries, id string, first, second gen.CleardevAgentStepAttempt, reservations []gen.CleardevAgentMessageReservation, correction gen.CleardevParseCorrection, hasCorrection bool) (string, error) {
	proof := struct {
		Attempts      []gen.CleardevAgentStepAttempt
		Events        []gen.CleardevAgentAttemptEvent
		Results       []gen.CleardevAgentStepResult
		Parses        []gen.CleardevAgentStepResultParse
		Reservations  []gen.CleardevAgentMessageReservation
		Confirmations []gen.CleardevAgentMessageConfirmation
		Correction    *gen.CleardevParseCorrection
	}{Attempts: []gen.CleardevAgentStepAttempt{first, second}, Reservations: reservations}
	if hasCorrection {
		proof.Correction = &correction
	}
	events, err := q.ListClearDevAgentAttemptEvents(ctx, id)
	if err != nil {
		return "", err
	}
	for _, event := range events {
		if event.AttemptID == first.ID || event.AttemptID == second.ID {
			proof.Events = append(proof.Events, event)
		}
	}
	results, err := q.ListClearDevAgentStepResults(ctx, id)
	if err != nil {
		return "", err
	}
	resultIDs := map[string]bool{}
	for _, result := range results {
		if result.AttemptID == first.ID || result.AttemptID == second.ID {
			proof.Results = append(proof.Results, result)
			resultIDs[result.ID] = true
		}
	}
	parses, err := q.ListClearDevAgentStepResultParses(ctx, id)
	if err != nil {
		return "", err
	}
	for _, parse := range parses {
		if resultIDs[parse.ResultID] {
			proof.Parses = append(proof.Parses, parse)
		}
	}
	for _, reservation := range reservations {
		confirmation, err := q.GetClearDevAgentMessageConfirmation(ctx, reservation.ClientMessageID)
		if err != nil {
			return "", err
		}
		proof.Confirmations = append(proof.Confirmations, confirmation)
	}
	sort.Slice(proof.Confirmations, func(i, j int) bool {
		return proof.Confirmations[i].ClientMessageID < proof.Confirmations[j].ClientMessageID
	})
	raw, err := core.CanonicalJSONBytes(proof)
	if err != nil {
		return "", err
	}
	return complexExecutionRawDigest(raw), nil
}

func extraPlanningOfferState(ctx context.Context, q *gen.Queries, p *extraPlanningProof) error {
	if p.step.ID == "" {
		return nil
	}
	intent, err := q.GetClearDevPlanningExtraRequestForStep(ctx, p.step.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	b, err := core.ParseExtraPlanningAttemptBinding([]byte(intent.BindingJson))
	if err != nil {
		return err
	}
	target, err := core.ExtraPlanningAttemptTarget(b)
	if err != nil {
		return err
	}
	p.state.Option.Action, p.state.Option.TargetID = core.RecoveryContinueExtraPlanningAttempt, target
	p.state.DecisionRequestID = intent.DecisionRequestID
	if p.state.Option.UnavailableReason == "" && p.state.Binding != b {
		p.state.Option.UnavailableReason = "PLANNING_NOT_CURRENT"
	}
	continuation, continuationErr := q.GetClearDevPlanningExtraContinuationForStep(ctx, p.step.ID)
	if continuationErr == nil {
		if _, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, continuation.ThirdAttemptID); err == nil {
			p.state.Option.UnavailableReason = "EXTRA_ATTEMPT_CONSUMED"
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		} else if p.state.Option.UnavailableReason == "" {
			p.state.Option.UnavailableReason = "CONTINUATION_REGISTERED"
		}
		return nil
	}
	if !errors.Is(continuationErr, sql.ErrNoRows) {
		return continuationErr
	}
	if p.state.Option.UnavailableReason != "" {
		return nil
	}
	decision, err := q.GetClearDevHumanDecisionRequest(ctx, intent.DecisionRequestID)
	if err != nil {
		return err
	}
	if decision.Status == "PENDING" {
		p.state.Option.UnavailableReason = "EXTRA_ATTEMPT_DECISION_PENDING"
		return nil
	}
	if decision.Decision != "APPROVE" {
		p.state.Option.UnavailableReason = "EXTRA_ATTEMPT_DECISION_REJECTED"
		return nil
	}
	return validateExtraPlanningGrant(ctx, q, intent)
}

func validateExtraPlanningGrant(ctx context.Context, q *gen.Queries, intent gen.CleardevPlanningExtraRequest) error {
	grant, err := q.GetClearDevPlanningExtraGrant(ctx, intent.DecisionRequestID)
	if err != nil {
		return productConflict("extra planning attempt has no exact native grant")
	}
	decision, err := q.GetClearDevHumanDecisionRequest(ctx, intent.DecisionRequestID)
	if err != nil {
		return err
	}
	effect, err := q.GetClearDevHumanDecisionEffect(ctx, intent.DecisionRequestID)
	if err != nil || effect.Decision != "APPROVE" || grant.LogicalStepID != intent.LogicalStepID || decision.DevelopmentProjectID != intent.RequirementID ||
		decision.DecisionKind != core.HumanDecisionKindExtraPlanningAttempt || decision.Status != "RESOLVED" || decision.Decision != "APPROVE" || decision.BindingJson != intent.BindingJson {
		return productConflict("extra planning grant does not match its settled native decision")
	}
	return nil
}

func extraPlanningHistory(ctx context.Context, q *gen.Queries, id string) ([]core.WorkflowRecovery, error) {
	requests, err := q.ListClearDevPlanningExtraRequests(ctx, id)
	if err != nil {
		return nil, err
	}
	history := []core.WorkflowRecovery{}
	for _, r := range requests {
		b, err := core.ParseExtraPlanningAttemptBinding([]byte(r.BindingJson))
		if err != nil {
			return nil, err
		}
		target, err := core.ExtraPlanningAttemptTarget(b)
		if err != nil {
			return nil, err
		}
		history = append(history, core.WorkflowRecovery{ID: r.ID, Action: core.RecoveryRequestExtraPlanningAttempt, TargetID: target, StepID: r.LogicalStepID, BindingID: b.Source.RoleBindingID, SuccessorID: r.DecisionRequestID,
			ProviderConversationID: b.Source.ProviderConversationID, OriginalStatus: r.OriginalStatus, OriginalReason: r.OriginalReason, OriginalSummary: r.OriginalSummary, OriginalStoppedAt: r.OriginalStoppedAt, Supplement: r.Supplement, CreatedAt: r.CreatedAt})
		continuation, err := q.GetClearDevPlanningExtraContinuationForDecision(ctx, r.DecisionRequestID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		history = append(history, core.WorkflowRecovery{ID: continuation.ID, Action: core.RecoveryContinueExtraPlanningAttempt, TargetID: target, StepID: continuation.LogicalStepID, BindingID: b.Source.RoleBindingID, SuccessorID: continuation.ThirdAttemptID,
			ProviderConversationID: b.Source.ProviderConversationID, OriginalStatus: continuation.OriginalStatus, OriginalReason: continuation.OriginalReason, OriginalSummary: continuation.OriginalSummary, OriginalStoppedAt: continuation.OriginalStoppedAt, Supplement: continuation.Supplement, CreatedAt: continuation.CreatedAt})
	}
	sort.SliceStable(history, func(i, j int) bool { return history[i].CreatedAt.Before(history[j].CreatedAt) })
	return history, nil
}

func extraPlanningIntentMatches(input core.WorkflowRecovery, id, supplement, requirementID, bindingJSON string) bool {
	b, err := core.ParseExtraPlanningAttemptBinding([]byte(bindingJSON))
	if err != nil {
		return false
	}
	target, err := core.ExtraPlanningAttemptTarget(b)
	return err == nil && input.ID == id && input.TargetID == target && input.Supplement == supplement && requirementID == b.DevelopmentRequirementID && input.ExecutionRunID == ""
}

// RequestClearDevExtraPlanningAttempt records only a request for a native choice.
func (s *Store) RequestClearDevExtraPlanningAttempt(ctx context.Context, id string, input core.WorkflowRecovery, decisionID string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "request one extra original planning attempt", func(q *gen.Queries) error {
		if prior, err := q.GetClearDevPlanningExtraRequest(ctx, input.ID); err == nil {
			if input.Action != core.RecoveryRequestExtraPlanningAttempt || prior.RequirementID != id || !extraPlanningIntentMatches(input, prior.ID, prior.Supplement, id, prior.BindingJson) {
				return productConflict("extra planning request is bound to different content")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := q.GetClearDevPlanningExtraContinuation(ctx, input.ID); err == nil {
			return productConflict("request id already identifies an extra continuation")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := q.GetClearDevPlanningStepRecovery(ctx, input.ID); err == nil {
			return productConflict("request id already identifies the original planning recovery")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p, err := extraPlanningEvidence(ctx, q, id, at)
		if err != nil {
			return err
		}
		if err := extraPlanningOfferState(ctx, q, &p); err != nil {
			return err
		}
		if input.Action != core.RecoveryRequestExtraPlanningAttempt || input.ExecutionRunID != "" || p.state.Option.Action != input.Action || p.state.Option.UnavailableReason != "" || p.state.Option.TargetID != input.TargetID {
			return productConflict("extra planning request is no longer current and eligible")
		}
		binding, err := json.Marshal(p.state.Binding)
		if err != nil {
			return err
		}
		if _, err := core.ParseExtraPlanningAttemptBinding(binding); err != nil {
			return err
		}
		display, err := extraPlanningDisplay(ctx, q, p, input.Supplement)
		if err != nil {
			return err
		}
		displayRaw, err := json.Marshal(display)
		if err != nil {
			return err
		}
		digest, err := core.HumanDecisionContentSHA256(core.HumanDecisionKindExtraPlanningAttempt, binding, display)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{ID: decisionID, DevelopmentProjectID: id, DecisionKind: core.HumanDecisionKindExtraPlanningAttempt, BindingSchemaVersion: 1, BindingJson: string(binding), DisplayJson: string(displayRaw), ContentSha256: digest, CreatedAt: at}); err != nil {
			return err
		}
		old, err := json.Marshal(p.step)
		if err != nil {
			return err
		}
		if err := q.InsertClearDevPlanningExtraRequest(ctx, gen.InsertClearDevPlanningExtraRequestParams{ID: input.ID, RequirementID: id, LogicalStepID: p.step.ID, DecisionRequestID: decisionID,
			BindingJson: string(binding), OldStepJson: string(old), OriginalStatus: p.step.SendStatus, OriginalReason: p.step.ReasonCode, OriginalSummary: p.failure.ErrorSummary, OriginalStoppedAt: p.failure.RecordedAt, Supplement: input.Supplement, CreatedAt: at}); err != nil {
			return err
		}
		project, err := q.GetClearDevRequirement(ctx, id)
		if err != nil {
			return err
		}
		return insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: project.AoProjectID, DevelopmentRequirementID: id, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: decisionID, Action: core.ActionCreateHumanDecisionRequest, TargetState: "PENDING", Outcome: core.EventAccepted, Source: core.EventSourceControlPlane, CreatedAt: at})
	})
}

func extraPlanningDisplay(ctx context.Context, q *gen.Queries, p extraPlanningProof, supplement string) (core.HumanDecisionDisplay, error) {
	goal, err := q.GetClearDevProductGoal(ctx, p.state.Binding.Source.ProductID)
	if err != nil {
		return core.HumanDecisionDisplay{}, err
	}
	discussion, err := q.GetClearDevProductDiscussion(ctx, p.state.Binding.Source.DiscussionID)
	if err != nil {
		return core.HumanDecisionDisplay{}, err
	}
	content := fmt.Sprintf("Product / 产品: %s\n\nYour original discussion request / 原讨论事项:\n%s", goal.Name, discussion.UserMessage)
	if p.state.Binding.Source.StageID != "" {
		row, err := q.GetClearDevProductStage(ctx, p.state.Binding.Source.StageID)
		if err != nil {
			return core.HumanDecisionDisplay{}, err
		}
		stage, err := productStageFromRow(row)
		if err != nil {
			return core.HumanDecisionDisplay{}, err
		}
		content = fmt.Sprintf("Product / 产品: %s\n\nStage to compile / 待编译阶段: %s\nOriginal stage objective / 原阶段目标:\n%s", goal.Name, stage.Definition.Title, stage.Definition.Goal)
	}
	content += "\n\nBoth original attempts ended in explicit failure. The automatic limit remains two attempts and at most three messages. Approval permits at most one extra attempt and one original message; existing attempts, messages and failures stay recorded.\n原来的两次尝试均已明确失败。自动额度仍为两次尝试、最多三条消息。批准后最多追加一次尝试和一条原消息；已有尝试、消息和失败记录全部保留。\n\nApproval only grants permission and does not automatically continue. Choose the separate Continue action to resend this same original request. The third attempt has no additional correction message and cannot create a fourth attempt. This decision does not approve a specification, execution or a completed result.\n批准只授予权限，不会自动继续。需另行明确选择“继续”，重发同一个原请求。第三次不追加纠正消息，也不产生第四次尝试。本决定不批准规格、执行或完成结果。"
	if supplement != "" {
		content += "\n\nAdditional context for this decision (does not change the original request) / 本次决定的补充上下文（不改变原请求）:\n" + supplement
	}
	display := core.HumanDecisionDisplay{Title: "Authorize one extra original planning attempt", Summary: "Two original attempts failed. Authorize one additional original message, then continue the same request separately.\n\n原来的两次尝试均已明确失败。可批准追加一次原消息，再单独继续原事项。", FullContent: content,
		ChangeSummary: "Keep the automatic limit of two attempts and three messages. Approval permits one third attempt and one original message, with no new correction message or fourth attempt. Approval does not automatically continue or approve a specification, execution or completion.\n\n保留自动额度：两次尝试、最多三条消息。批准后仅允许第三次尝试和一条原消息，不追加纠正消息，不产生第四次尝试。批准不会自动继续，也不批准规格、执行或完成结果。"}
	encoded, err := json.Marshal(display)
	if err != nil {
		return core.HumanDecisionDisplay{}, err
	}
	if _, err := core.ParseHumanDecisionDisplay(encoded); err != nil {
		return core.HumanDecisionDisplay{}, &core.RuleError{Code: "EXTRA_ATTEMPT_DISPLAY_TOO_LARGE", Message: "The original planning item and additional context exceed the native decision display limit; no authority request was created"}
	}
	return display, nil
}

// ContinueClearDevExtraPlanningAttempt consumes the grant only through one
// explicit continuation, atomically preserving the stopped compatibility row.
func (s *Store) ContinueClearDevExtraPlanningAttempt(ctx context.Context, id string, input core.WorkflowRecovery, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "continue one human-authorized planning attempt", func(q *gen.Queries) error {
		if prior, err := q.GetClearDevPlanningExtraContinuation(ctx, input.ID); err == nil {
			intent, err := q.GetClearDevPlanningExtraRequestForDecision(ctx, prior.DecisionRequestID)
			if err != nil {
				return err
			}
			if input.Action != core.RecoveryContinueExtraPlanningAttempt || prior.RequirementID != id || !extraPlanningIntentMatches(input, prior.ID, prior.Supplement, id, intent.BindingJson) {
				return productConflict("extra planning continuation is bound to different content")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := q.GetClearDevPlanningExtraRequest(ctx, input.ID); err == nil {
			return productConflict("request id already identifies an extra decision request")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := q.GetClearDevPlanningStepRecovery(ctx, input.ID); err == nil {
			return productConflict("request id already identifies the original planning recovery")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p, err := extraPlanningEvidence(ctx, q, id, at)
		if err != nil {
			return err
		}
		if err := extraPlanningOfferState(ctx, q, &p); err != nil {
			return err
		}
		if input.Action != core.RecoveryContinueExtraPlanningAttempt || input.ExecutionRunID != "" || p.state.Option.Action != input.Action || p.state.Option.UnavailableReason != "" || p.state.Option.TargetID != input.TargetID {
			return productConflict("extra planning continuation lacks a current unused grant")
		}
		intent, err := q.GetClearDevPlanningExtraRequestForStep(ctx, p.step.ID)
		if err != nil {
			return err
		}
		if err := validateExtraPlanningGrant(ctx, q, intent); err != nil {
			return err
		}
		old, err := json.Marshal(p.step)
		if err != nil {
			return err
		}
		third := p.step.ID + ":attempt:3"
		if err := q.InsertClearDevPlanningExtraContinuation(ctx, gen.InsertClearDevPlanningExtraContinuationParams{ID: input.ID, RequirementID: id, LogicalStepID: p.step.ID, DecisionRequestID: intent.DecisionRequestID, ThirdAttemptID: third,
			OldStepJson: string(old), OriginalStatus: p.step.SendStatus, OriginalReason: p.step.ReasonCode, OriginalSummary: p.failure.ErrorSummary, OriginalStoppedAt: p.failure.RecordedAt, Supplement: input.Supplement, CreatedAt: at}); err != nil {
			return err
		}
		rows, err := q.InsertClearDevAgentStepAttempt(ctx, gen.InsertClearDevAgentStepAttemptParams{ID: third, DevelopmentProjectID: id, LogicalStepID: p.step.ID, StepCategory: p.second.StepCategory, StepKind: p.second.StepKind, AttemptNumber: 3,
			RoleBindingID: p.second.RoleBindingID, AoSessionID: p.second.AoSessionID, ClientMessageID: p.step.ClientMessageID + ":attempt:3", PromptSha256: p.second.PromptSha256,
			TriggerFailureEventID: nullableString(p.failure.ID), RequestedAt: at, CreatedAt: nullableTime(at), RequestedAtSemantics: string(core.AttemptTimeActualCreation)})
		if err != nil {
			return err
		}
		if rows != 1 {
			return productConflict("extra planning successor already exists")
		}
		rows, err = q.ReopenClearDevStoppedProductStep(ctx, gen.ReopenClearDevStoppedProductStepParams{ID: p.step.ID, OriginalStatus: p.step.SendStatus, OriginalReason: p.step.ReasonCode})
		if err != nil {
			return err
		}
		if rows != 1 {
			return productConflict("original planning step changed before extra continuation")
		}
		return nil
	})
}

// ReadClearDevExtraPlanningAttemptBeforeSend rechecks only a registered third
// attempt. Its absence never grants permission to create or send attempt 3.
func (s *Store) ReadClearDevExtraPlanningAttemptBeforeSend(ctx context.Context, id, stepID string, at time.Time) (core.ExtraPlanningAttemptState, bool, error) {
	return extraPlanningBeforeSend(ctx, s.qr, id, stepID, at)
}

func extraPlanningBeforeSend(ctx context.Context, q *gen.Queries, id, stepID string, at time.Time) (core.ExtraPlanningAttemptState, bool, error) {
	continuation, err := q.GetClearDevPlanningExtraContinuationForStep(ctx, stepID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ExtraPlanningAttemptState{}, false, nil
	}
	if err != nil {
		return core.ExtraPlanningAttemptState{}, false, err
	}
	intent, err := q.GetClearDevPlanningExtraRequestForDecision(ctx, continuation.DecisionRequestID)
	if err != nil {
		return core.ExtraPlanningAttemptState{}, true, err
	}
	b, err := core.ParseExtraPlanningAttemptBinding([]byte(intent.BindingJson))
	if err != nil {
		return core.ExtraPlanningAttemptState{}, true, err
	}
	p, err := extraPlanningEvidence(ctx, q, id, at)
	if err != nil {
		return p.state, true, err
	}
	if continuation.RequirementID != id || continuation.ThirdAttemptID != stepID+":attempt:3" || p.step.ID != stepID || p.state.Option.UnavailableReason != "" || p.state.Binding != b {
		return p.state, true, productConflict("registered extra planning source or old message evidence changed")
	}
	if err := validateExtraPlanningGrant(ctx, q, intent); err != nil {
		return p.state, true, err
	}
	p.state.DecisionRequestID = intent.DecisionRequestID
	return p.state, true, nil
}

func validateReopenExtraPlanning(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) error {
	b, err := core.ParseExtraPlanningAttemptBinding(result.Binding)
	if err != nil {
		return err
	}
	intent, err := q.GetClearDevPlanningExtraRequestForDecision(ctx, request.ID)
	if err != nil {
		return err
	}
	if intent.RequirementID != b.DevelopmentRequirementID || intent.LogicalStepID != b.Source.LogicalStepID || intent.BindingJson != request.BindingJson {
		return productConflict("extra planning decision does not match its original intent")
	}
	p, err := extraPlanningEvidence(ctx, q, b.DevelopmentRequirementID, at)
	if err != nil {
		return err
	}
	if p.state.Option.UnavailableReason != "" || p.state.Binding != b {
		return productConflict("extra planning decision source or failure evidence changed")
	}
	if _, err := q.GetClearDevPlanningExtraGrant(ctx, request.ID); err == nil {
		return productConflict("extra planning decision has already granted its message")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := q.GetClearDevPlanningExtraContinuationForStep(ctx, b.Source.LogicalStepID); err == nil {
		return productConflict("extra planning decision already has a successor")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func settleExtraPlanningAttempt(ctx context.Context, q *gen.Queries, result core.HumanDecisionResult, request gen.CleardevHumanDecisionRequest, at time.Time) (*core.RuleError, error) {
	if err := validateReopenExtraPlanning(ctx, q, result, request, at); err != nil {
		return nil, err
	}
	b, err := core.ParseExtraPlanningAttemptBinding(result.Binding)
	if err != nil {
		return nil, err
	}
	rows, err := q.SettleClearDevHumanDecisionRequestCAS(ctx, gen.SettleClearDevHumanDecisionRequestCASParams{ID: request.ID, Decision: string(result.Decision), ResolvedAt: nullableTime(at)})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, productConflict("extra planning decision was already settled")
	}
	outcome := core.HumanDecisionDispatchRejected
	if result.Decision == core.HumanDecisionApprove {
		outcome = core.HumanDecisionDispatchApproved
		if err := q.InsertClearDevPlanningExtraGrant(ctx, gen.InsertClearDevPlanningExtraGrantParams{DecisionRequestID: request.ID, LogicalStepID: b.Source.LogicalStepID, CreatedAt: at}); err != nil {
			return nil, err
		}
	}
	if err := consumeDispatchCAS(ctx, q, result.Nonce, result.DesktopRunID, outcome, at); err != nil {
		return nil, err
	}
	project, err := q.GetClearDevRequirement(ctx, b.DevelopmentRequirementID)
	if err != nil {
		return nil, err
	}
	if err := insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: project.AoProjectID, DevelopmentRequirementID: project.ID, SubjectType: core.SubjectHumanDecisionRequest, SubjectID: request.ID, Action: core.ActionSettleHumanDecision, PreviousState: "PENDING", TargetState: "RESOLVED", Outcome: core.EventAccepted, Source: core.EventSourceHumanDecision, CreatedAt: at}); err != nil {
		return nil, err
	}
	sequence, err := q.GetLatestClearDevRequirementEventSequenceForSubjectAction(ctx, gen.GetLatestClearDevRequirementEventSequenceForSubjectActionParams{SubjectID: request.ID, Action: string(core.ActionSettleHumanDecision)})
	if err != nil {
		return nil, err
	}
	return nil, q.InsertClearDevHumanDecisionEffect(ctx, gen.InsertClearDevHumanDecisionEffectParams{RequestID: request.ID, Decision: string(result.Decision), EventSequence: sequence, CreatedAt: at})
}
