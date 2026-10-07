package specgen

import (
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

func clearDevResultPreviewOperations() []operation {
	result := make([]operation, 0, 3)
	for _, item := range []struct{ method, id, summary string }{
		{http.MethodGet, "getClearDevResultPreview", "Read the interactive preview of an already completed mail delivery"},
		{http.MethodPost, "startClearDevResultPreview", "Open the exact completed mail application with persistent project data; no caller overrides"},
		{http.MethodDelete, "stopClearDevResultPreview", "Stop the completed application's preview without deleting data or changing completion"},
	} {
		result = append(result, operation{
			method: item.method, path: "/api/v1/cleardev/requirements/{id}/result-preview", id: item.id, tag: "cleardev",
			summary: item.summary, pathParams: []any{controllers.ClearDevRequirementIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ClearDevResultPreviewResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		})
	}
	return result
}
