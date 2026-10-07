package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type clearDevResultPreviewService interface {
	StartResultPreview(context.Context, string) (cleardevsvc.ResultPreviewView, error)
	GetResultPreview(context.Context, string) (cleardevsvc.ResultPreviewView, error)
	StopResultPreview(context.Context, string) (cleardevsvc.ResultPreviewView, error)
}

func (c *ClearDevController) resultPreview(w http.ResponseWriter, r *http.Request) {
	service, ok := c.Svc.(clearDevResultPreviewService)
	if !ok {
		apispec.NotImplemented(w, r, r.Method, "/api/v1/cleardev/requirements/{id}/result-preview")
		return
	}
	if r.Method != http.MethodGet {
		if err := requireStrictlyEmptyBody(r); err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Result preview takes no command, path or candidate override", nil)
			return
		}
	}
	var result cleardevsvc.ResultPreviewView
	var err error
	id := chi.URLParam(r, "id")
	switch r.Method {
	case http.MethodPost:
		result, err = service.StartResultPreview(r.Context(), id)
	case http.MethodDelete:
		result, err = service.StopResultPreview(r.Context(), id)
	default:
		result, err = service.GetResultPreview(r.Context(), id)
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClearDevResultPreviewResponse(result))
}
