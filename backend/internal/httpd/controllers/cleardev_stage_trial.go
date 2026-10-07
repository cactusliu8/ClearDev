package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type clearDevStageTrialService interface {
	StageReviewTrial(context.Context, string, string, string, string) (cleardevsvc.StageReviewTrialView, error)
}

func (c *ClearDevController) stageTrial(w http.ResponseWriter, r *http.Request) {
	service, ok := c.Svc.(clearDevStageTrialService)
	if !ok {
		apispec.NotImplemented(w, r, r.Method, "/api/v1/cleardev/requirements/{id}/stage-trial")
		return
	}
	var input ClearDevStageTrialRequest
	if err := decodeClearDevJSON(r, &input); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Only sessionId and lifecycle action are accepted", nil)
		return
	}
	result, err := service.StageReviewTrial(r.Context(), chi.URLParam(r, "id"), input.SessionID, r.Header.Get(browserCapabilityHeader), input.Action)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClearDevStageTrialResponse(result))
}
