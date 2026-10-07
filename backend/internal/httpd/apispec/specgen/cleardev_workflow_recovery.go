package specgen

import (
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

func clearDevWorkflowRecoveryOperations() []operation {
	out := make([]operation, 0, 2)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		op := operation{method: method, path: "/api/v1/cleardev/requirements/{id}/recoveries", id: "getClearDevWorkflowRecovery", tag: "cleardev", summary: "Inspect current stopped workflow actions and immutable recovery history", pathParams: []any{controllers.ClearDevRequirementIDParam{}}, resps: []respUnit{{http.StatusOK, controllers.ClearDevWorkflowRecoveryResponse{}}, {http.StatusBadRequest, envelope.APIError{}}, {http.StatusConflict, envelope.APIError{}}, {http.StatusNotFound, envelope.APIError{}}, {http.StatusInternalServerError, envelope.APIError{}}, {http.StatusNotImplemented, envelope.APIError{}}}}
		if method == http.MethodPost {
			op.id = "requestClearDevWorkflowRecovery"
			op.summary = "Continue a current stopped step with supplementary context"
			op.reqBody = controllers.ClearDevWorkflowRecoveryRequest{}
		}
		out = append(out, op)
	}
	return out
}
