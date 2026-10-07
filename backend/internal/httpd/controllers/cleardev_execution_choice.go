package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type clearDevExecutionChoiceService interface {
	SetProjectExecution(context.Context, string, domain.ClearDevExecutionConfig) (*cleardevsvc.ExecutionChoiceView, error)
	RetryControlledPreflight(context.Context, string, cleardevsvc.RetryPreflightInput) (cleardevsvc.RequirementView, error)
}

func (c *ClearDevController) setProjectExecution(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.Svc.(clearDevExecutionChoiceService)
	if !ok {
		apispec.NotImplemented(w, r, r.Method, "/api/v1/cleardev/projects/{projectId}/execution")
		return
	}
	var input ClearDevSetExecutionRequest
	if !decodeProductInput(w, r, &input) {
		return
	}
	view, err := svc.SetProjectExecution(r.Context(), chi.URLParam(r, "projectId"), domain.ClearDevExecutionConfig(input))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, view)
}

func (c *ClearDevController) retryPreflight(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.Svc.(clearDevExecutionChoiceService)
	if !ok {
		apispec.NotImplemented(w, r, r.Method, "/api/v1/cleardev/requirements/{id}/preflight-retries")
		return
	}
	var input ClearDevRetryPreflightRequest
	if !decodeProductInput(w, r, &input) {
		return
	}
	view, err := svc.RetryControlledPreflight(r.Context(), chi.URLParam(r, "id"), cleardevsvc.RetryPreflightInput(input))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ClearDevRequirementResponse(view))
}
