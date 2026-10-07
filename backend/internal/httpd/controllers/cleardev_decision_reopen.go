package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	svc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type clearDevDecisionReopenService interface {
	GetHumanDecisionDisplays(context.Context, string) (svc.HumanDecisionDisplaysView, error)
	ReopenHumanDecision(context.Context, string, svc.ReopenHumanDecisionInput) (svc.HumanDecisionDisplaysView, error)
}

func (c *ClearDevController) decisionDisplays(w http.ResponseWriter, r *http.Request) {
	service, ok := c.Svc.(clearDevDecisionReopenService)
	if !ok {
		apispec.NotImplemented(w, r, r.Method, "/api/v1/cleardev/requirements/{id}/decision-displays")
		return
	}
	var out svc.HumanDecisionDisplaysView
	var err error
	if r.Method == http.MethodPost {
		var input ClearDevDecisionReopenRequest
		if err := decodeClearDevJSON(r, &input); err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid confirmation reopen request", nil)
			return
		}
		out, err = service.ReopenHumanDecision(r.Context(), chi.URLParam(r, "id"), svc.ReopenHumanDecisionInput(input))
	} else {
		out, err = service.GetHumanDecisionDisplays(r.Context(), chi.URLParam(r, "id"))
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClearDevDecisionDisplaysResponse(out))
}
