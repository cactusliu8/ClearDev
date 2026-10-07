package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

type stoppedCheckProof struct {
	state         core.StoppedCheckRecoveryState
	event         core.PlannerCoordinationEvent
	decision      gen.CleardevPlannerRuntimeDecision
	attempt       gen.CleardevComplexExecutionTaskAttempt
	item          gen.CleardevWorkItem
	check         gen.CleardevComplexExecutionCheckRun
	contextJSON   string
	latestCheck   gen.CleardevComplexExecutionCheckRun
	predecessorID string
}

// ReadClearDevStoppedCheckRecovery exposes an intent, never permission to run.
// The native decision and original STOP remain separate historical facts.
func (s *Store) ReadClearDevStoppedCheckRecovery(ctx context.Context, id string) (core.StoppedCheckRecoveryState, error) {
	var state core.StoppedCheckRecoveryState
	runs, err := s.qr.ListClearDevComplexExecutionRuns(ctx, id)
	if err != nil || len(runs) == 0 {
		return state, err
	}
	offer, offerErr := s.qr.GetClearDevStoppedCheckRequestForRun(ctx, runs[len(runs)-1].ID)
	if offerErr != nil && !errors.Is(offerErr, sql.ErrNoRows) {
		return state, offerErr
	}
	if offerErr == nil {
		b, err := core.ParseStoppedCheckRecoveryBinding([]byte(offer.BindingJson))
		if err != nil {
			return state, err
		}
		target, err := core.StoppedCheckRecoveryTarget(b)
		if err != nil {
			return state, err
		}
		state.Binding = b
		stop, err := currentPlannerRuntimeDecision(ctx, s.qr, b.EventID)
		if err != nil {
			return state, err
		}
		state.History = []core.WorkflowRecovery{{ID: offer.ID, ExecutionRunID: b.ExecutionRunID, Action: core.RecoveryRequestStoppedCheck, TargetID: target, DispatchID: b.DispatchID, TaskID: b.TaskID, BindingID: b.BuilderRoleBindingID, SuccessorID: offer.DecisionRequestID, CandidateSHA: b.CandidateSHA, ProviderConversationID: b.ProviderConversationID, OriginalStatus: "STOP", OriginalReason: stop.ReasonCode, OriginalSummary: stop.Summary, OriginalStoppedAt: stop.CreatedAt, Supplement: offer.Supplement, CreatedAt: offer.CreatedAt}}
		human, err := s.qr.GetClearDevHumanDecisionRequest(ctx, offer.DecisionRequestID)
		if err != nil {
			return state, err
		}
		if human.Status == "RESOLVED" && human.Decision == "APPROVE" {
			return state, nil
		}
		p, err := stoppedCheckEvidence(ctx, s.qr, id, nil)
		if err != nil {
			return state, err
		}
		state.Option = p.state.Option
		if state.Option.Action != "" {
			if p.state.Binding != b {
				state.Option.UnavailableReason = "EXECUTION_NOT_CURRENT"
			} else if human.Status == "RESOLVED" {
				state.Option.UnavailableReason = "STOPPED_CHECK_DECISION_REJECTED"
			} else {
				state.Option.UnavailableReason = "STOPPED_CHECK_DECISION_PENDING"
			}
		}
		return state, nil
	}
	p, err := stoppedCheckEvidence(ctx, s.qr, id, nil)
	return p.state, err
}

// applied is supplied only for the exact retry after native settlement. The
// changed task/attempt state is normalized against preserved source bytes; no
// other trusted candidate, counter, contract or budget change is ignored.
func stoppedCheckEvidence(ctx context.Context, q *gen.Queries, id string, applied *gen.CleardevStoppedCheckRequest) (stoppedCheckProof, error) {
	var p stoppedCheckProof
	runs, err := q.ListClearDevComplexExecutionRuns(ctx, id)
	if err != nil || len(runs) == 0 {
		return p, err
	}
	run := runs[len(runs)-1]
	if run.Mode != "STANDARD" || !core.BuilderFirstFailureEnabled(complexExecutionRunFromGen(run)) {
		return p, nil
	}
	events, err := q.ListClearDevPlannerRuntimeEvents(ctx, run.ID)
	if err != nil || len(events) == 0 {
		return p, err
	}
	event, err := plannerRuntimeEventFromGen(events[len(events)-1])
	if err != nil {
		return p, err
	}
	decision, err := currentPlannerRuntimeDecision(ctx, q, event.ID)
	if err != nil {
		return p, missingBuilderProof(err)
	}
	var result core.PlannerCoordinationResult
	if event.Report.Category != "ENGINEERING" || decision.Source != "PLANNER" || decision.Outcome != "STOP" || !decision.ResultJson.Valid || !decision.ResultSha256.Valid || complexExecutionRawDigest([]byte(decision.ResultJson.String)) != decision.ResultSha256.String {
		return p, nil
	}
	if err := json.Unmarshal([]byte(decision.ResultJson.String), &result); err != nil {
		return p, err
	}
	if result.Decision != "STOP" || result.EventID != event.ID || len(result.Questions) != 0 || len(result.Amendments) != 0 {
		return p, nil
	}
	p.event, p.decision = event, decision
	p.state.Option = core.WorkflowRecoveryOption{Action: core.RecoveryRequestStoppedCheck, TargetID: event.ID, Role: "CHECKER", Reason: "CHECKER_UNAVAILABLE", Summary: "原 Planner 已停止。申请真人批准只重跑当前候选的一次不可用检查；原 STOP、消息、返工数和角色预算保留。", UnavailableReason: "EXECUTION_NOT_CURRENT"}
	current, err := plannerRuntimeCurrent(ctx, q, run)
	if err != nil || !current {
		return p, err
	}
	barriers, err := q.ListClearDevPlannerBarrierEvents(ctx, run.ID)
	if err != nil {
		return p, err
	}
	for _, other := range barriers {
		if other != event.ID {
			return p, nil
		}
	}
	p.state.Option.UnavailableReason = "ORIGINAL_PLANNER_UNAVAILABLE"
	request, err := q.GetClearDevPlannerRuntimeRequest(ctx, event.ID)
	if err != nil {
		return p, missingBuilderProof(err)
	}
	step, err := q.GetClearDevComplexAgentStep(ctx, request.AgentStepID)
	if err != nil {
		return p, err
	}
	if step.SendStatus != "SETTLED" || step.RoleBindingID != request.PlannerRoleBindingID || step.RequestID != event.ID || !step.FinalMessageID.Valid || result.ContextSHA256 != request.ContextSha256 {
		return p, nil
	}
	planner, err := q.GetClearDevComplexRoleBinding(ctx, request.PlannerRoleBindingID)
	if err != nil {
		return p, err
	}
	if planner.Status != "BOUND" || planner.Role != "ENGINEERING_PLANNER" || planner.DevelopmentProjectID != id || planner.AoSessionID.String != request.AoSessionID {
		return p, nil
	}
	plannerSession, err := q.GetSession(ctx, domain.SessionID(request.AoSessionID))
	if err != nil {
		return p, missingBuilderProof(err)
	}
	if plannerSession.IsTerminated || plannerSession.ActivityState != domain.ActivityIdle || plannerSession.PermissionMode != "auto" || plannerSession.CreationIdempotencyKey != planner.SessionCreationIdempotencyKey {
		return p, nil
	}
	if n, err := q.CountClearDevUnsettledSessionTurns(ctx, plannerSession.ID); err != nil || n != 0 {
		return p, err
	}
	p.state.Option.UnavailableReason = "CHECK_ATTEMPT_NOT_CURRENT"
	a, err := q.GetClearDevComplexExecutionTaskAttempt(ctx, event.DispatchID)
	if err != nil {
		return p, err
	}
	p.attempt = a
	if a.ExecutionRunID != run.ID {
		return p, nil
	}
	item, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, a.TaskMappingID)
	if err != nil {
		return p, err
	}
	p.item = item
	if applied == nil {
		if (a.Status != "REWORK" && a.Status != "BLOCKED") || a.ReasonCode != "CHECKER_UNAVAILABLE" || !a.SettledAt.Valid || (item.State != "REWORK" && item.State != "BLOCKED") || item.ReworkCount < a.Round || item.ReworkCount > a.Round+1 {
			return p, nil
		}
	} else {
		active := a.Status == "OBSERVED" && a.ReasonCode == "" && !a.SettledAt.Valid && item.State == "RUNNING"
		blocked := a.Status == "BLOCKED" && a.ReasonCode == "CHECKER_UNAVAILABLE" && a.SettledAt.Valid && item.State == "BLOCKED"
		if applied.EventID != event.ID || applied.ExecutionRunID != run.ID || (!active && !blocked) {
			return p, nil
		}
	}
	attempts, err := q.ListClearDevComplexExecutionTaskAttempts(ctx, run.ID)
	if err != nil {
		return p, err
	}
	for _, other := range attempts {
		if other.TaskMappingID == a.TaskMappingID && other.Round > a.Round {
			return p, nil
		}
		if other.ID != a.ID && (other.Status == "PENDING" || other.Status == "RUNNING" || other.Status == "OBSERVED" || other.Status == "REVIEWING") {
			return p, nil
		}
	}

	// This authority finishes a single existing candidate, not unfinished sibling
	// tasks. A downstream Builder dispatch requires its own normal authority.
	mappings, err := q.ListClearDevComplexExecutionTaskMappings(ctx, run.ID)
	if err != nil {
		return p, err
	}
	for _, mapping := range mappings {
		if mapping.ID == a.TaskMappingID {
			continue
		}
		var latest gen.CleardevComplexExecutionTaskAttempt
		for _, other := range attempts {
			if other.TaskMappingID == mapping.ID && (latest.ID == "" || other.Round > latest.Round) {
				latest = other
			}
		}
		otherItem, err := q.GetClearDevComplexExecutionTaskByWorkItem(ctx, mapping.ID)
		if err != nil {
			return p, err
		}
		if latest.ID == "" || latest.Status != "VERIFIED" || otherItem.ReworkCount > latest.Round {
			return p, nil
		}
	}
	steps, err := q.ListClearDevComplexExceptionAgentSteps(ctx, run.ID)
	if err != nil {
		return p, err
	}
	for _, other := range steps {
		if other.SendStatus == "PENDING" || other.SendStatus == "SENT" {
			return p, nil
		}
	}
	p.state.Option.UnavailableReason = "CHECK_SOURCE_NOT_CURRENT"
	checks, err := q.ListClearDevComplexExecutionCheckRuns(ctx, run.ID)
	if err != nil {
		return p, err
	}
	specs, err := q.ListClearDevComplexExecutionCheckSpecs(ctx, run.ID)
	if err != nil {
		return p, err
	}
	candidate, err := q.GetClearDevComplexExecutionCandidate(ctx, nullableString(a.ID))
	if err != nil {
		return p, missingBuilderProof(err)
	}
	foundCheck := false
	for _, spec := range specs {
		if spec.TaskMappingID.String != a.TaskMappingID || spec.CheckKind != "REQUIRED_CHECK" {
			continue
		}
		for _, check := range checks {
			if check.TaskAttemptID != a.ID || check.CheckSpecID != spec.ID || check.CandidateCommitID != candidate.ID {
				continue
			}
			if check.RetryOrdinal != 0 {
				continue
			}
			if check.Status == "FAILED" && check.ReasonCode == "CHECKER_UNAVAILABLE" && check.SettledAt.Valid && !check.Result.Valid && !check.ExitCode.Valid {
				if foundCheck {
					return p, nil
				}
				foundCheck = true
				p.check = check
			} else if check.Status != "SETTLED" || check.Result.String != "PASS" {
				return p, nil
			}
		}
	}
	if !foundCheck || p.check.CandidateCommitSha != candidate.CommitSha {
		return p, nil
	}
	retryIDs := []string{}
	chainIDs := map[string]bool{}
	if applied != nil {
		chain, err := q.ListClearDevStoppedCheckRetryChain(ctx, run.ID)
		if err != nil {
			return p, err
		}
		if len(chain) == 0 || chain[len(chain)-1].CheckID != applied.RetryCheckID {
			return p, nil
		}
		for _, link := range chain {
			if link.EventID != applied.EventID || link.RootCheckID != p.check.ID {
				return p, nil
			}
			chainIDs[link.CheckID] = true
			retryIDs = append(retryIDs, link.CheckID)
			if link.CheckID == applied.RetryCheckID {
				p.predecessorID = link.PredecessorCheckID
			}
		}
	}
	for _, check := range checks {
		if chainIDs[check.ID] {
			if check.CheckSpecID != p.check.CheckSpecID || check.TaskAttemptID != a.ID || check.CandidateCommitID != candidate.ID || check.CandidateCommitSha != candidate.CommitSha {
				return p, nil
			}
			if check.ID == applied.RetryCheckID && a.Status == "OBSERVED" {
				if check.Status != "PENDING" && check.Status != "STARTED" {
					return p, nil
				}
			} else if check.Status != "FAILED" || check.ReasonCode != "CHECKER_UNAVAILABLE" || !check.SettledAt.Valid || check.Result.Valid || check.ExitCode.Valid {
				return p, nil
			}
			if check.ID == applied.RetryCheckID {
				p.latestCheck = check
			}
			continue
		}
		if check.Status == "PENDING" || check.Status == "STARTED" || (check.TaskAttemptID == a.ID && check.CheckSpecID == p.check.CheckSpecID && check.RetryOrdinal > 0) {
			return p, nil
		}
	}
	if applied != nil && p.latestCheck.ID == "" {
		return p, nil
	}

	verified, err := q.ListClearDevComplexExecutionVerifiedCandidates(ctx, run.ID)
	if err != nil {
		return p, err
	}
	for _, v := range verified {
		if v.TaskAttemptID == a.ID {
			return p, nil
		}
	}
	reviews, err := q.ListClearDevComplexExecutionReviews(ctx, run.ID)
	if err != nil {
		return p, err
	}
	for _, v := range reviews {
		if v.TaskAttemptID == a.ID {
			return p, nil
		}
	}
	p.state.Option.UnavailableReason = "RECOVERY_SESSION_UNAVAILABLE"
	role, err := q.GetClearDevComplexExecutionRoleBinding(ctx, a.BuilderRoleBindingID)
	if err != nil {
		return p, err
	}
	if role.Status != "BOUND" || role.Role != "BUILDER" || role.ExecutionRunID != run.ID || !role.AoSessionID.Valid {
		return p, nil
	}
	session, err := q.GetSession(ctx, domain.SessionID(role.AoSessionID.String))
	if err != nil {
		return p, missingBuilderProof(err)
	}
	requirement, err := q.GetClearDevRequirement(ctx, id)
	if err != nil {
		return p, err
	}
	matches, err := q.IsClearDevPlanningContinuationSession(ctx, string(session.ID))
	if err != nil {
		return p, err
	}
	if !matches || string(session.ProjectID) != requirement.AoProjectID || session.Kind != domain.KindWorker || session.SessionMode != domain.SessionModeChat || session.PermissionMode != "auto" || session.IsTerminated || session.ActivityState != domain.ActivityIdle || session.WorkspacePath == "" || session.ProviderConversationID == "" || session.CreationRequestFingerprint == "" || session.WorkspacePath != role.WorkspacePath || session.CreationIdempotencyKey != role.SessionCreationIdempotencyKey {
		return p, nil
	}
	if n, err := q.CountClearDevUnsettledSessionTurns(ctx, session.ID); err != nil || n != 0 {
		return p, err
	}
	effective, err := q.GetClearDevPlannerRuntimeEffectiveTask(ctx, a.TaskMappingID)
	if err != nil {
		return p, err
	}
	raw, err := plannerRuntimeContext(ctx, q, run, event)
	if err != nil {
		return p, err
	}
	normalized, err := stoppedCheckContext(raw, applied, a, item, p.check, plannerSession, retryIDs)
	if err != nil {
		return p, err
	}
	p.contextJSON = string(normalized)
	p.state.Binding = core.StoppedCheckRecoveryBinding{DevelopmentRequirementID: id, ExecutionRunID: run.ID, EventID: event.ID, StopSHA256: decision.ResultSha256.String, ContextSHA256: complexExecutionRawDigest(normalized), TaskID: a.TaskMappingID, DispatchID: a.ID, CheckRunID: p.check.ID, RetryCheckRunID: p.check.ID + ":stopped-check-retry", CandidateSHA: candidate.CommitSha, TaskPacketSHA256: effective.TaskPacketSha256, ReworkCount: int(item.ReworkCount), BuilderRoleBindingID: role.ID, AOSessionID: string(session.ID), ProviderConversationID: session.ProviderConversationID, WorkspacePath: session.WorkspacePath, SessionCreationKey: session.CreationIdempotencyKey, CreationFingerprint: session.CreationRequestFingerprint, Harness: string(session.Harness), Model: session.Model}
	p.state.Option.TargetID, err = core.StoppedCheckRecoveryTarget(p.state.Binding)
	p.state.Option.TaskID = a.TaskMappingID
	p.state.Option.UnavailableReason = ""
	return p, err
}

// ValidateClearDevStoppedCheckBeforeRun validates only the exact authorized
// retry. It never turns a changed source or an unknown prior result into PASS.
func (s *Store) ValidateClearDevStoppedCheckBeforeRun(ctx context.Context, id, checkID string) (core.StoppedCheckRecoveryBinding, bool, error) {
	offer, err := s.qr.GetClearDevStoppedCheckRequestForChain(ctx, checkID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.StoppedCheckRecoveryBinding{}, false, nil
	}
	if err != nil {
		return core.StoppedCheckRecoveryBinding{}, true, err
	}
	b, err := core.ParseStoppedCheckRecoveryBinding([]byte(offer.BindingJson))
	if err != nil {
		return b, true, err
	}
	if _, err := s.qr.GetClearDevStoppedCheckGrant(ctx, b.EventID); err != nil {
		return b, true, err
	}
	offer.RetryCheckID = checkID // ephemeral lookup of this exact technical successor
	p, err := stoppedCheckEvidence(ctx, s.qr, id, &offer)
	if err != nil {
		return b, true, err
	}
	if p.state.Binding != b || p.state.Option.UnavailableReason != "" || p.contextJSON != offer.ContextJson {
		return b, true, complexExecutionRule("approved same-candidate check source changed before execution")
	}
	b.CheckRunID, b.RetryCheckRunID = p.predecessorID, checkID
	return b, true, nil
}

// This validates an ordinary technical retry under the existing native STOP
// continuation. It records no new authority and never refunds a role budget.
func validateStoppedTechnicalRetry(ctx context.Context, q *gen.Queries, id, sourceID string) error {
	offer, err := q.GetClearDevStoppedCheckRequestForChain(ctx, sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	original, err := core.ParseStoppedCheckRecoveryBinding([]byte(offer.BindingJson))
	if err != nil {
		return err
	}
	offer.RetryCheckID = sourceID
	p, err := stoppedCheckEvidence(ctx, q, id, &offer)
	if err != nil {
		return err
	}
	if p.state.Binding != original || p.state.Option.UnavailableReason != "" || p.contextJSON != offer.ContextJson || p.latestCheck.ID != sourceID || p.attempt.Status != "BLOCKED" {
		return complexExecutionRule("stopped candidate technical retry source changed")
	}
	return nil
}

func loadStoppedCheckRecoveries(ctx context.Context, q *gen.Queries, runID string, h *core.PlannerRuntimeSnapshot) error {
	rows, err := q.ListClearDevStoppedCheckGrants(ctx, runID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		b, err := core.ParseStoppedCheckRecoveryBinding([]byte(row.BindingJson))
		if err != nil {
			return err
		}
		h.CheckRecoveries = append(h.CheckRecoveries, core.StoppedCheckRecovery{EventID: b.EventID, DecisionRequestID: row.DecisionRequestID, StopSHA256: b.StopSHA256, TaskID: b.TaskID, DispatchID: b.DispatchID, CandidateSHA: b.CandidateSHA, TaskPacketSHA256: b.TaskPacketSHA256, CheckRunID: b.CheckRunID, RetryCheckRunID: b.RetryCheckRunID, ReworkCount: b.ReworkCount})
	}
	return nil
}

// A failed check/review after this grant cannot spend a new Builder round. Keep
// the actual failure reason/result, but settle the task as blocked, not REWORK.
func stoppedCheckFailureIsBounded(ctx context.Context, q *gen.Queries, a gen.CleardevComplexExecutionTaskAttempt) (bool, error) {
	offer, err := q.GetClearDevStoppedCheckRequestForRun(ctx, a.ExecutionRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	b, err := core.ParseStoppedCheckRecoveryBinding([]byte(offer.BindingJson))
	if err != nil {
		return false, err
	}
	if b.DispatchID != a.ID || b.ExecutionRunID != a.ExecutionRunID {
		return false, nil
	}
	_, err = q.GetClearDevStoppedCheckGrant(ctx, b.EventID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
