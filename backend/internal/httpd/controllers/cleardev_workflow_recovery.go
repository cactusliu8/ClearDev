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

type clearDevWorkflowRecoveryService interface {
	GetWorkflowRecovery(context.Context, string) (core.WorkflowRecoveryView, error)
	RequestWorkflowRecovery(context.Context, string, cleardevsvc.WorkflowRecoveryInput) (core.WorkflowRecoveryView, error)
}

func (c *ClearDevController) workflowRecovery(w http.ResponseWriter, r *http.Request) {
	service, ok := c.Svc.(clearDevWorkflowRecoveryService)
	if !ok {
		apispec.NotImplemented(w, r, r.Method, "/api/v1/cleardev/requirements/{id}/recoveries")
		return
	}
	var result core.WorkflowRecoveryView
	var err error
	if r.Method == http.MethodGet {
		result, err = service.GetWorkflowRecovery(r.Context(), chi.URLParam(r, "id"))
	} else {
		var input ClearDevWorkflowRecoveryRequest
		if err = decodeClearDevJSON(r, &input); err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid recovery request", nil)
			return
		}
		result, err = service.RequestWorkflowRecovery(r.Context(), chi.URLParam(r, "id"), cleardevsvc.WorkflowRecoveryInput(input))
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClearDevWorkflowRecoveryResponse{
		ExecutionRunID: result.ExecutionRunID, Options: result.Options, History: result.History,
		Diagnosis: result.Diagnosis, BuilderSessionChecks: result.BuilderSessionChecks,
		BuilderReplacements: result.BuilderReplacements, FailureHandling: result.FailureHandling,
	})
}
