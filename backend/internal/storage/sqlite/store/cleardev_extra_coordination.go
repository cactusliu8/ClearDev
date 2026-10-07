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

type extraCoordinationProof struct {
	state       core.ExtraCoordinationState
	event       core.PlannerCoordinationEvent
	contextJSON string
}

// ReadClearDevExtraCoordination is observation only. Native approval, not a
// recovery read or the caller's request, is the sole source of the extra round.
func (s *Store) ReadClearDevExtraCoordination(ctx context.Context, id string) (core.ExtraCoordinationState, error) {
	p, err := extraCoordinationEvidence(ctx, s.qr, id, false)
	if err != nil {
		return p.state, err
	}
	runs, err := s.qr.ListClearDevComplexExecutionRuns(ctx, id)
	if err != nil || len(runs) == 0 {
		return p.state, err
	}
	offer, err := s.qr.GetClearDevExtraCoordinationRequestForRun(ctx, runs[len(runs)-1].ID)
	if errors.Is(err, sql.ErrNoRows) {
		return p.state, nil
	}
	if err != nil {
		return p.state, err
	}
	b, err := core.ParseExtraCoordinationBinding([]byte(offer.BindingJson))
	if err != nil {
		return p.state, err
	}
	target, err := core.ExtraCoordinationTarget(b)
	if err != nil {
		return p.state, err
	}
	stop, err := s.qr.GetClearDevPlannerRuntimeDecision(ctx, offer.EventID)
	if err != nil {
		return p.state, err
	}
	p.state.History = append(p.state.History, core.WorkflowRecovery{
		ID: offer.ID, ExecutionRunID: offer.ExecutionRunID, Action: core.RecoveryRequestExtraCoordination,
		TargetID: target, StepID: offer.EventID + ":planner", BindingID: b.PlannerRoleBindingID,
		SuccessorID: offer.DecisionRequestID, OriginalStatus: "LIMIT_REACHED", OriginalReason: string(core.ReasonPlannerRuntimeBudget),
		OriginalSummary: stop.Summary, OriginalStoppedAt: stop.CreatedAt,
		Supplement: offer.Supplement, ProviderConversationID: b.ProviderConversationID, CreatedAt: offer.CreatedAt,
	})
	human, err := s.qr.GetClearDevHumanDecisionRequest(ctx, offer.DecisionRequestID)
	if err != nil {
		return p.state, err
	}
	if human.Status == "RESOLVED" && human.Decision == "APPROVE" {
		p.state.Option = core.WorkflowRecoveryOption{}
		return p.state, nil
	}
	if p.state.Option.Action == "" {
		return p.state, nil
	}
	if b != p.state.Binding {
		p.state.Option.UnavailableReason = "EXECUTION_NOT_CURRENT"
		return p.state, nil
	}
	p.state.Option.UnavailableReason = "EXTRA_COORDINATION_DECISION_PENDING"
	if human.Status == "RESOLVED" {
		p.state.Option.UnavailableReason = "EXTRA_COORDINATION_DECISION_REJECTED"
	}
	return p.state, nil
}

// A grant may create only the third request. For before-send revalidation that
// exact request may already exist, but never another event or ordinal.
func extraCoordinationEvidence(ctx context.Context, q *gen.Queries, id string, sending bool) (extraCoordinationProof, error) {
	var p extraCoordinationProof
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
	stop, err := q.GetClearDevPlannerRuntimeDecision(ctx, event.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if stop.Source != "CONTROL_PLANE" || stop.Outcome != "LIMIT_REACHED" || stop.ReasonCode != string(core.ReasonPlannerRuntimeBudget) || stop.ResultJson.Valid || event.Report.Category != "ENGINEERING" {
		return p, nil
	}
	p.event = event
	p.state.Option = core.WorkflowRecoveryOption{Action: core.RecoveryRequestExtraCoordination, TargetID: event.ID, Role: "ENGINEERING_PLANNER", Reason: stop.ReasonCode,
		Summary: "原两轮协调已用完。申请真人批准原 Planner 对本事件再协调一次；不增加任务、审核、修订或其它消息预算。", UnavailableReason: "EXECUTION_NOT_CURRENT"}
	current, err := plannerRuntimeCurrent(ctx, q, run)
	if err != nil || !current {
		return p, err
	}
	barriers, err := q.ListClearDevPlannerBarrierEvents(ctx, run.ID)
	if err != nil {
		return p, err
	}
	for _, blocked := range barriers {
		if blocked != event.ID {
			return p, nil
		}
	}
	requests, err := q.ListClearDevPlannerRuntimeRequests(ctx, run.ID)
	if err != nil {
		return p, err
	}
	if len(requests) != core.PlannerRuntimeMaxRounds {
		if !sending || len(requests) != core.ExtraCoordinationOrdinal || requests[len(requests)-1].EventID != event.ID || requests[len(requests)-1].Ordinal != 3 {
			return p, nil
		}
	}
	for _, previous := range requests[:core.PlannerRuntimeMaxRounds] {
		if previous.EventID == event.ID {
			return p, nil
		}
		if _, err := currentPlannerRuntimeDecision(ctx, q, previous.EventID); err != nil {
			return p, missingBuilderProof(err)
		}
		step, err := q.GetClearDevComplexAgentStep(ctx, previous.AgentStepID)
		if err != nil {
			return p, err
		}
		if step.SendStatus != "SETTLED" && step.SendStatus != "FAILED" {
			return p, nil
		}
	}
	p.state.Option.UnavailableReason = "RESULT_NOT_SETTLED"
	active, err := q.CountClearDevPlannerRuntimeActiveAttempts(ctx, run.ID)
	if err != nil || active != 0 {
		return p, err
	}
	quiet, err := plannerProjectQuiescent(ctx, q, run)
	if err != nil || !quiet {
		return p, err
	}
	plan, err := q.GetClearDevPlannerRuntimePlan(ctx, run.PlanID)
	if err != nil {
		return p, err
	}
	role, err := q.GetClearDevComplexRoleBinding(ctx, plan.PlannerRoleBindingID)
	if err != nil {
		return p, err
	}
	p.state.Option.UnavailableReason = "ORIGINAL_PLANNER_UNAVAILABLE"
	if role.Status != "BOUND" || role.Role != "ENGINEERING_PLANNER" || role.DevelopmentProjectID != id || !role.AoSessionID.Valid {
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
	matches, err := q.IsClearDevPlanningContinuationSession(ctx, role.AoSessionID.String)
	if err != nil {
		return p, err
	}
	if !matches || string(session.ProjectID) != requirement.AoProjectID || session.IsTerminated || session.Kind != domain.KindWorker || session.SessionMode != domain.SessionModeChat || session.PermissionMode != "auto" || session.ProviderConversationID == "" || session.WorkspacePath == "" || session.CreationRequestFingerprint == "" || session.CreationIdempotencyKey != role.SessionCreationIdempotencyKey {
		return p, nil
	}
	open, err := q.CountClearDevUnsettledSessionTurns(ctx, domain.SessionID(role.AoSessionID.String))
	if err != nil {
		return p, err
	}
	if open != 0 || session.ActivityState != domain.ActivityIdle {
		p.state.Option.UnavailableReason = "RESULT_NOT_SETTLED"
		return p, nil
	}
	contextJSON, err := plannerRuntimeContext(ctx, q, run, event)
	if err != nil {
		return p, err
	}
	stopJSON, err := core.CanonicalJSONBytes(plannerRuntimeDecisionFromGen(stop))
	if err != nil {
		return p, err
	}
	p.contextJSON = string(contextJSON)
	p.state.Binding = core.ExtraCoordinationBinding{
		DevelopmentRequirementID: id, ExecutionRunID: run.ID, EventID: event.ID, StopSHA256: complexExecutionRawDigest(stopJSON), ContextSHA256: complexExecutionRawDigest(contextJSON),
		PlannerRoleBindingID: role.ID, AOSessionID: role.AoSessionID.String, ProviderConversationID: session.ProviderConversationID, WorkspacePath: session.WorkspacePath,
		SessionCreationKey: session.CreationIdempotencyKey, CreationFingerprint: session.CreationRequestFingerprint, Harness: string(session.Harness), Model: session.Model, Ordinal: core.ExtraCoordinationOrdinal,
	}
	p.state.Option.TargetID, err = core.ExtraCoordinationTarget(p.state.Binding)
	p.state.Option.UnavailableReason = ""
	return p, err
}

// ValidateClearDevExtraCoordinationBeforeSend re-reads the source after approval
// and after a restart. It authorizes no unrelated step or changed native session.
func (s *Store) ValidateClearDevExtraCoordinationBeforeSend(ctx context.Context, id, stepID string) (core.ExtraCoordinationBinding, bool, error) {
	offer, err := s.qr.GetClearDevExtraCoordinationRequestForStep(ctx, stepID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ExtraCoordinationBinding{}, false, nil
	}
	if err != nil {
		return core.ExtraCoordinationBinding{}, true, err
	}
	binding, err := core.ParseExtraCoordinationBinding([]byte(offer.BindingJson))
	if err != nil {
		return binding, true, err
	}
	if _, err := s.qr.GetClearDevExtraCoordinationGrant(ctx, offer.EventID); err != nil {
		return binding, true, err
	}
	p, err := extraCoordinationEvidence(ctx, s.qr, id, true)
	if err != nil {
		return binding, true, err
	}
	if p.state.Binding != binding || p.state.Option.UnavailableReason != "" || p.contextJSON != offer.ContextJson {
		return binding, true, complexExecutionRule("extra coordination source changed before send")
	}
	return binding, true, nil
}

func loadExtraCoordination(ctx context.Context, q *gen.Queries, runID string, h *core.PlannerRuntimeSnapshot) error {
	grants, err := q.ListClearDevExtraCoordinationGrants(ctx, runID)
	if err != nil {
		return err
	}
	for _, g := range grants {
		h.ExtraCoordinationGrants = append(h.ExtraCoordinationGrants, core.ExtraCoordinationGrant{EventID: g.EventID, DecisionRequestID: g.DecisionRequestID, Ordinal: 3})
	}
	decisions, err := q.ListClearDevExtraCoordinationDecisions(ctx, runID)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if d.ResultJson.Valid && complexExecutionRawDigest([]byte(d.ResultJson.String)) != d.ResultSha256.String {
			return complexExecutionRule("extra coordination result digest changed")
		}
		h.ExtraCoordinationDecisions = append(h.ExtraCoordinationDecisions, plannerRuntimeDecisionFromGen(gen.CleardevPlannerRuntimeDecision(d)))
	}
	return nil
}

func sameExtraCoordinationBinding(raw string, b core.ExtraCoordinationBinding) bool {
	want, err := json.Marshal(b)
	return err == nil && raw == string(want)
}
