package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type clearDevPlannerClarificationService interface {
	SubmitPlannerClarifications(context.Context, string, cleardevsvc.SubmitPlannerClarificationsInput) (cleardevsvc.RequirementView, error)
}

func (c *ClearDevController) plannerClarify(w http.ResponseWriter, r *http.Request) {
	service, ok := c.Svc.(clearDevPlannerClarificationService)
	if !ok {
		apispec.NotImplemented(w, r, r.Method, "/api/v1/cleardev/requirements/{id}/planner-clarifications")
		return
	}
	var input ClearDevPlannerClarificationRequest
	if err := decodeClearDevJSON(r, &input); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid Planner answer request", nil)
		return
	}
	result, err := service.SubmitPlannerClarifications(r.Context(), chi.URLParam(r, "id"), cleardevsvc.SubmitPlannerClarificationsInput(input))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClearDevRequirementResponse(result))
}
