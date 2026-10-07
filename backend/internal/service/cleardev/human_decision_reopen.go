package cleardev

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// ReopenHumanDecisionInput contains only display intent, never a decision capability.
type ReopenHumanDecisionInput struct {
	RequestID          string `json:"requestId"`
	DecisionRequestID  string `json:"decisionRequestId"`
	ContentSHA256      string `json:"contentSha256"`
	PreviousDispatchID string `json:"previousDispatchId"`
}

// HumanDecisionDisplayDispatch is a public receipt without nonce, hashes or desktop credentials.
type HumanDecisionDisplayDispatch struct {
	ID        string                            `json:"id"`
	ExpiresAt time.Time                         `json:"expiresAt"`
	Outcome   core.HumanDecisionDispatchOutcome `json:"outcome"`
}

// HumanDecisionDisplayItem binds a read-only display status to the original request.
type HumanDecisionDisplayItem struct {
	DecisionRequestID string                         `json:"decisionRequestId"`
	DecisionKind      string                         `json:"decisionKind"`
	ContentSHA256     string                         `json:"contentSha256"`
	Title             string                         `json:"title"`
	Pending           bool                           `json:"pending"`
	CanReopen         bool                           `json:"canReopen"`
	ReasonCode        string                         `json:"reasonCode"`
	Dispatches        []HumanDecisionDisplayDispatch `json:"dispatches"`
	Reopens           []core.HumanDecisionReopen     `json:"reopens"`
}

// HumanDecisionDisplaysView reports display facts and the observed private connection.
type HumanDecisionDisplaysView struct {
	DesktopAvailable bool                       `json:"desktopAvailable"`
	Connected        bool                       `json:"connected"`
	Items            []HumanDecisionDisplayItem `json:"items"`
}
type humanDecisionReopenStore interface {
	ListClearDevHumanDecisionRequestsForRequirement(context.Context, string) ([]core.HumanDecisionRequest, error)
	ListClearDevHumanDecisionDispatchHistory(context.Context, string) ([]core.HumanDecisionDispatch, error)
	ListClearDevHumanDecisionReopens(context.Context, string) ([]core.HumanDecisionReopen, error)
	ListRunnableClearDevHumanDecisionReopens(context.Context, string) ([]core.HumanDecisionReopen, error)
	ValidateClearDevHumanDecisionReopen(context.Context, string, time.Time) error
	RecordClearDevHumanDecisionReopen(context.Context, string, core.HumanDecisionReopen) (core.HumanDecisionReopen, error)
}

// GetHumanDecisionDisplays performs no backfill, registration, dispatch or domain action.
func (s *Service) GetHumanDecisionDisplays(ctx context.Context, id string) (HumanDecisionDisplaysView, error) {
	out := HumanDecisionDisplaysView{DesktopAvailable: s.desktopRunID != "", Items: []HumanDecisionDisplayItem{}}
	if s.desktopChannelConnected != nil {
		out.Connected = s.desktopChannelConnected()
	}
	store, ok := s.humanDecisions.(humanDecisionReopenStore)
	if !ok {
		return out, nil
	}
	if _, found, err := s.facts.GetClearDevRequirement(ctx, id); err != nil {
		return out, err
	} else if !found {
		return out, apierr.NotFound("REQUIREMENT_NOT_FOUND", "Requirement was not found")
	}
	requests, err := store.ListClearDevHumanDecisionRequestsForRequirement(ctx, id)
	if err != nil {
		return out, err
	}
	for _, req := range requests {
		var display core.HumanDecisionDisplay
		if err := json.Unmarshal([]byte(req.DisplayJSON), &display); err != nil {
			return out, err
		}
		item := HumanDecisionDisplayItem{DecisionRequestID: req.ID, DecisionKind: req.DecisionKind, ContentSHA256: req.ContentSHA256, Title: display.Title, Pending: req.Status == core.HumanDecisionRequestPending, Dispatches: []HumanDecisionDisplayDispatch{}}
		history, err := store.ListClearDevHumanDecisionDispatchHistory(ctx, req.ID)
		if err != nil {
			return out, err
		}
		for _, d := range history {
			item.Dispatches = append(item.Dispatches, HumanDecisionDisplayDispatch{ID: d.ID, ExpiresAt: d.ExpiresAt, Outcome: d.Outcome})
		}
		item.Reopens, err = store.ListClearDevHumanDecisionReopens(ctx, req.ID)
		if err != nil {
			return out, err
		}
		item.ReasonCode = s.decisionReopenReason(ctx, store, req, history, item.Reopens)
		item.CanReopen = item.ReasonCode == ""
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func (s *Service) decisionReopenReason(ctx context.Context, store humanDecisionReopenStore, req core.HumanDecisionRequest, history []core.HumanDecisionDispatch, reopens []core.HumanDecisionReopen) string {
	if req.Status != core.HumanDecisionRequestPending {
		return "DECISION_RESOLVED"
	}
	if s.desktopRunID == "" {
		return "DESKTOP_UNAVAILABLE"
	}
	if err := store.ValidateClearDevHumanDecisionReopen(ctx, req.ID, s.now().UTC()); err != nil {
		return "DECISION_NOT_CURRENT"
	}
	var current bool
	var err error
	if req.DecisionKind == core.HumanDecisionKindBuilderReplacement {
		current, err = s.builderReplacementDisplayCurrent(ctx, req)
	} else {
		current, err = s.humanDecisionSourceCurrent(ctx, req)
	}
	if err != nil {
		return "DECISION_FACTS_UNAVAILABLE"
	}
	if !current {
		return "DECISION_NOT_CURRENT"
	}
	if len(history) == 0 {
		return "DECISION_NOT_DISPLAYED"
	}
	previous := history[len(history)-1]
	for _, r := range reopens {
		if r.PreviousDispatchID == previous.ID {
			return "REOPEN_REGISTERED"
		}
	}
	if previous.ConsumedAt == nil && s.now().Before(previous.ExpiresAt) {
		return "DECISION_WINDOW_OPEN"
	}
	if previous.ConsumedAt != nil && previous.Outcome != core.HumanDecisionDispatchExpired && previous.Outcome != core.HumanDecisionDispatchDisconnected && previous.Outcome != core.HumanDecisionDispatchLater {
		return "DECISION_NOT_CURRENT"
	}
	return ""
}

// ReopenHumanDecision records one display intent. The private client alone issues the fresh offer.
func (s *Service) ReopenHumanDecision(ctx context.Context, id string, input ReopenHumanDecisionInput) (HumanDecisionDisplaysView, error) {
	if input.RequestID == "" || strings.TrimSpace(input.RequestID) != input.RequestID || utf8.RuneCountInString(input.RequestID) > 200 || input.DecisionRequestID == "" || input.PreviousDispatchID == "" || len(input.ContentSHA256) != 64 {
		return HumanDecisionDisplaysView{}, apierr.Invalid("DECISION_REOPEN_INVALID", "An exact decision display and request identity are required", nil)
	}
	store, ok := s.humanDecisions.(humanDecisionReopenStore)
	if !ok || s.desktopRunID == "" {
		return HumanDecisionDisplaysView{}, apierr.Conflict("DESKTOP_UNAVAILABLE", "A trusted desktop must be running to reopen this confirmation", nil)
	}
	// A recorded replay returns its original receipt even if facts changed after registration.
	prior, err := store.ListClearDevHumanDecisionReopens(ctx, input.DecisionRequestID)
	if err != nil {
		return HumanDecisionDisplaysView{}, err
	}
	replay := false
	for _, r := range prior {
		if r.RequestID == input.RequestID {
			replay = true
		}
	}
	if !replay {
		req, found, err := s.humanDecisions.GetClearDevHumanDecisionRequest(ctx, input.DecisionRequestID)
		if err != nil {
			return HumanDecisionDisplaysView{}, err
		}
		if !found || req.DevelopmentRequirementID != id {
			return HumanDecisionDisplaysView{}, apierr.NotFound("DECISION_NOT_FOUND", "Decision was not found")
		}
		current, err := s.humanDecisionSourceCurrent(ctx, req)
		if err != nil {
			return HumanDecisionDisplaysView{}, err
		}
		if !current {
			return HumanDecisionDisplaysView{}, apierr.Conflict("DECISION_NOT_CURRENT", "The original decision is no longer current", nil)
		}
	}
	_, err = store.RecordClearDevHumanDecisionReopen(ctx, id, core.HumanDecisionReopen{RequestID: input.RequestID, DecisionRequestID: input.DecisionRequestID, ContentSHA256: input.ContentSHA256, PreviousDispatchID: input.PreviousDispatchID, DesktopRunID: s.desktopRunID, CreatedAt: s.now().UTC()})
	if err != nil {
		return HumanDecisionDisplaysView{}, apierr.Conflict("DECISION_REOPEN_REJECTED", err.Error(), nil)
	}
	return s.GetHumanDecisionDisplays(ctx, id)
}
