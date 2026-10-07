package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func decisionReopenFromGen(r gen.CleardevHumanDecisionReopen) core.HumanDecisionReopen {
	return core.HumanDecisionReopen{RequestID: r.RequestID, DecisionRequestID: r.DecisionRequestID, ContentSHA256: r.ContentSha256, PreviousDispatchID: r.PreviousDispatchID, DesktopRunID: r.DesktopRunID, NextDispatchID: r.NextDispatchID, CreatedAt: r.CreatedAt}
}

// ListClearDevHumanDecisionRequestsForRequirement reads the retained decision requests.
func (s *Store) ListClearDevHumanDecisionRequestsForRequirement(ctx context.Context, id string) ([]core.HumanDecisionRequest, error) {
	rows, err := s.qr.ListClearDevHumanDecisionRequestsForRequirement(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]core.HumanDecisionRequest, 0, len(rows))
	for _, r := range rows {
		out = append(out, humanDecisionRequestFromGen(r))
	}
	return out, nil
}

// ListClearDevHumanDecisionDispatchHistory retains identifiers and outcomes; callers must not expose nonce hashes.
func (s *Store) ListClearDevHumanDecisionDispatchHistory(ctx context.Context, id string) ([]core.HumanDecisionDispatch, error) {
	rows, err := s.qr.ListClearDevHumanDecisionDispatchHistory(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]core.HumanDecisionDispatch, 0, len(rows))
	for _, r := range rows {
		d := core.HumanDecisionDispatch{ID: r.ID, RequestID: r.RequestID, DesktopRunID: r.DesktopRunID, IssuedAt: r.IssuedAt, ExpiresAt: r.ExpiresAt, Outcome: core.HumanDecisionDispatchOutcome(r.Outcome)}
		if r.ConsumedAt.Valid {
			at := r.ConsumedAt.Time
			d.ConsumedAt = &at
		}
		out = append(out, d)
	}
	return out, nil
}

// ListClearDevHumanDecisionReopens reads immutable reopen history.
func (s *Store) ListClearDevHumanDecisionReopens(ctx context.Context, id string) ([]core.HumanDecisionReopen, error) {
	rows, err := s.qr.ListClearDevHumanDecisionReopens(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]core.HumanDecisionReopen, 0, len(rows))
	for _, r := range rows {
		out = append(out, decisionReopenFromGen(r))
	}
	return out, nil
}

// ListRunnableClearDevHumanDecisionReopens reads intents not yet issued on this desktop.
func (s *Store) ListRunnableClearDevHumanDecisionReopens(ctx context.Context, desktopID string) ([]core.HumanDecisionReopen, error) {
	rows, err := s.qr.ListRunnableClearDevHumanDecisionReopens(ctx, desktopID)
	if err != nil {
		return nil, err
	}
	out := make([]core.HumanDecisionReopen, 0, len(rows))
	for _, r := range rows {
		out = append(out, decisionReopenFromGen(r))
	}
	return out, nil
}

// ValidateClearDevHumanDecisionReopen only checks current domain facts.
func (s *Store) ValidateClearDevHumanDecisionReopen(ctx context.Context, id string, at time.Time) error {
	r, err := s.qr.GetClearDevHumanDecisionRequest(ctx, id)
	if err != nil {
		return err
	}
	return validateDecisionReopen(ctx, s.qr, r, at)
}

// RecordClearDevHumanDecisionReopen consumes only an expired display and records an immutable new display intent.
func (s *Store) RecordClearDevHumanDecisionReopen(ctx context.Context, id string, input core.HumanDecisionReopen) (core.HumanDecisionReopen, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out core.HumanDecisionReopen
	err := s.inTx(ctx, "record human decision reopen", func(q *gen.Queries) error {
		req, err := q.GetClearDevHumanDecisionRequest(ctx, input.DecisionRequestID)
		if err != nil {
			return err
		}
		if req.DevelopmentProjectID != id {
			return errors.New("decision requirement does not match")
		}
		old, err := q.GetClearDevHumanDecisionReopen(ctx, input.RequestID)
		if err == nil {
			out = decisionReopenFromGen(old)
			if old.DecisionRequestID != input.DecisionRequestID || old.ContentSha256 != input.ContentSHA256 || old.PreviousDispatchID != input.PreviousDispatchID {
				return errors.New("decision reopen request conflicts with its original content")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if req.ContentSha256 != input.ContentSHA256 {
			return errors.New("decision content changed")
		}
		if err := validateDecisionReopen(ctx, q, req, input.CreatedAt); err != nil {
			return err
		}
		if err := expireOpenHumanDecisionDispatches(ctx, q, req, input.CreatedAt); err != nil {
			return err
		}
		history, err := q.ListClearDevHumanDecisionDispatchHistory(ctx, req.ID)
		if err != nil {
			return err
		}
		if len(history) == 0 {
			return errors.New("decision has not been displayed")
		}
		previous := history[len(history)-1]
		if previous.ID != input.PreviousDispatchID || !previous.ConsumedAt.Valid || (previous.Outcome != "EXPIRED" && previous.Outcome != "DISCONNECTED" && previous.Outcome != "LATER") {
			return errors.New("decision display is still open or changed")
		}
		// Supersede every older outstanding nonce, including a prior desktop's
		// orphaned offer. None may settle the original after this registration.
		open, err := q.ListOpenClearDevHumanDecisionDispatchesForRequest(ctx, req.ID)
		if err != nil {
			return err
		}
		project, err := q.GetClearDevRequirement(ctx, id)
		if err != nil {
			return err
		}
		for _, d := range open {
			changed, err := q.ConsumeClearDevHumanDecisionDispatchCAS(ctx, gen.ConsumeClearDevHumanDecisionDispatchCASParams{ConsumedAt: nullableTime(input.CreatedAt), Outcome: string(core.HumanDecisionDispatchDisconnected), NonceSha256: d.NonceSha256, DesktopRunID: d.DesktopRunID})
			if err != nil {
				return err
			}
			if changed != 1 {
				return errors.New("older decision display changed")
			}
			if err := insertClearDevEvent(ctx, q, core.RequirementEvent{AOProjectID: project.AoProjectID, DevelopmentRequirementID: id, SubjectType: core.SubjectHumanDecisionDispatch, SubjectID: d.ID, Action: core.ActionDismissHumanDecisionDispatch, Outcome: core.EventAccepted, ReasonText: string(core.HumanDecisionDispatchDisconnected), Source: core.EventSourceControlPlane, CreatedAt: input.CreatedAt}); err != nil {
				return err
			}
		}
		input.NextDispatchID = uuid.NewString()
		if err := q.InsertClearDevHumanDecisionReopen(ctx, gen.InsertClearDevHumanDecisionReopenParams{RequestID: input.RequestID, DecisionRequestID: input.DecisionRequestID, ContentSha256: input.ContentSHA256, PreviousDispatchID: input.PreviousDispatchID, DesktopRunID: input.DesktopRunID, NextDispatchID: input.NextDispatchID, CreatedAt: input.CreatedAt}); err != nil {
			return err
		}
		out = input
		return nil
	})
	return out, err
}
