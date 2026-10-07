package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

// Admission is additive: the old empty-body execution endpoint cannot turn a
// historical Plan V3 into an executable run. The service rechecks every source
// and the existing Human Authority confirmation before any runtime work.
type clearDevProjectExecutionService interface {
	StartProjectExecution(context.Context, string, core.ProjectExecutionAdmission) (cleardevsvc.RequirementView, error)
}

func (c *ClearDevController) startProjectExecution(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.Svc.(clearDevProjectExecutionService)
	if !ok {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/{id}/project-execution-runs")
		return
	}
	var input *ClearDevProjectExecutionInput
	r.Body = http.MaxBytesReader(w, r.Body, maxClearDevEmptyBodyBytes)
	if err := decodeClearDevJSON(r, &input); err != nil || input == nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid project execution admission", nil)
		return
	}
	view, err := svc.StartProjectExecution(r.Context(), chi.URLParam(r, "id"), core.ProjectExecutionAdmission(*input))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ClearDevRequirementResponse(view))
}
