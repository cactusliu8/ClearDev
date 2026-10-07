package store

import (
	"context"
	"database/sql"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func currentPlannerRuntimeDecision(ctx context.Context, q *gen.Queries, eventID string) (gen.CleardevPlannerRuntimeDecision, error) {
	if _, err := q.GetClearDevExtraCoordinationGrant(ctx, eventID); err == nil {
		d, err := q.GetClearDevExtraCoordinationDecision(ctx, eventID)
		return gen.CleardevPlannerRuntimeDecision(d), err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return gen.CleardevPlannerRuntimeDecision{}, err
	}
	if _, err := q.GetClearDevPlannerCoordinationRecoveryForEvent(ctx, eventID); err == nil {
		row, err := q.GetClearDevPlannerCoordinationRecoveryDecision(ctx, eventID)
		return gen.CleardevPlannerRuntimeDecision(row), err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return gen.CleardevPlannerRuntimeDecision{}, err
	}
	return q.GetClearDevPlannerRuntimeDecision(ctx, eventID)
}

func insertCurrentPlannerRuntimeDecision(ctx context.Context, q *gen.Queries, params gen.InsertClearDevPlannerRuntimeDecisionParams) error {
	if _, err := q.GetClearDevExtraCoordinationGrant(ctx, params.EventID); err == nil {
		return q.InsertClearDevExtraCoordinationDecision(ctx, gen.InsertClearDevExtraCoordinationDecisionParams(params))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := q.GetClearDevPlannerCoordinationRecoveryForEvent(ctx, params.EventID); err == nil {
		return q.InsertClearDevPlannerCoordinationRecoveryDecision(ctx, gen.InsertClearDevPlannerCoordinationRecoveryDecisionParams(params))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return q.InsertClearDevPlannerRuntimeDecision(ctx, params)
}

func loadPlannerCoordinationRecoveries(ctx context.Context, q *gen.Queries, runID string, history *core.PlannerRuntimeSnapshot) error {
	rows, err := q.ListClearDevPlannerCoordinationRecoveries(ctx, runID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		r, err := plannerCoordinationHistory(row)
		if err != nil {
			return err
		}
		history.Recoveries = append(history.Recoveries, r)
	}
	decisions, err := q.ListClearDevPlannerCoordinationRecoveryDecisions(ctx, runID)
	if err != nil {
		return err
	}
	for _, row := range decisions {
		if row.ResultJson.Valid && complexExecutionRawDigest([]byte(row.ResultJson.String)) != row.ResultSha256.String {
			return complexExecutionRule("recovered decision does not match its exact bytes")
		}
		history.RecoveryDecisions = append(history.RecoveryDecisions, plannerRuntimeDecisionFromGen(gen.CleardevPlannerRuntimeDecision(row)))
	}
	return nil
}

// Exclude only the technical stop produced by this exact frozen request.
// All other decisions (including recovery outcomes) remain context inputs.
// Empty recovery history introduces no fields, preserving old packet hashes.
func plannerRecoveryContextHistory(history *core.PlannerRuntimeSnapshot, eventID string, facts map[string]any) {
	decisions := make([]core.PlannerCoordinationDecision, 0, len(history.Decisions))
	for _, d := range history.Decisions {
		if d.EventID == eventID && d.Source == "CONTROL_PLANE" && d.Outcome == "STOP" && d.ReasonCode == core.ReasonPlannerRuntimeUnavailable && d.ResultJSON == "" {
			continue
		}
		decisions = append(decisions, d)
	}
	facts["priorDecisions"] = decisions
	recovered := []core.PlannerCoordinationDecision{}
	for _, d := range history.RecoveryDecisions {
		if d.EventID != eventID {
			recovered = append(recovered, d)
		}
	}
	for _, d := range history.ExtraCoordinationDecisions {
		if d.EventID != eventID {
			recovered = append(recovered, d)
		}
	}
	if len(recovered) != 0 {
		facts["priorRecoveryDecisions"] = recovered
	}
}
