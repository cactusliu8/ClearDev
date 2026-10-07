package specgen

import (
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

func clearDevStageTrialOperations() []operation {
	return []operation{{method: http.MethodPost, path: "/api/v1/cleardev/requirements/{id}/stage-trial", id: "operateClearDevStageTrial", tag: "cleardev",
		summary:    "Operate the current Stage Reviewer's exact candidate with isolated trial data",
		pathParams: []any{controllers.ClearDevRequirementIDParam{}, controllers.BrowserCapabilityHeader{}},
		reqBody:    controllers.ClearDevStageTrialRequest{},
		resps: []respUnit{{http.StatusOK, controllers.ClearDevStageTrialResponse{}},
			{http.StatusBadRequest, envelope.APIError{}}, {http.StatusForbidden, envelope.APIError{}},
			{http.StatusConflict, envelope.APIError{}}, {http.StatusNotFound, envelope.APIError{}},
			{http.StatusInternalServerError, envelope.APIError{}}, {http.StatusNotImplemented, envelope.APIError{}}},
	}}
}
