package specgen

import (
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

func clearDevProductOperations() []operation {
	items := []struct {
		method, path, id, summary string
		params, body, response    any
		status                    int
	}{
		{http.MethodPut, "/api/v1/cleardev/projects/{projectId}/execution", "setClearDevProjectExecution", "Set the project-wide tool and model before their durable locks; never grant authority", controllers.ClearDevAOProjectIDParam{}, controllers.ClearDevSetExecutionRequest{}, controllers.ClearDevExecutionResponse{}, http.StatusOK},
		{http.MethodPost, "/api/v1/cleardev/requirements/{id}/preflight-retries", "retryClearDevPreflight", "Retry the current failed preflight in its original workflow without replacing identities or budgets", controllers.ClearDevRequirementIDParam{}, controllers.ClearDevRetryPreflightRequest{}, cleardevsvc.RequirementView{}, http.StatusAccepted},
		{http.MethodPost, "/api/v1/cleardev/products", "createClearDevProduct", "Start bounded product discovery with the Steward", nil, controllers.ClearDevCreateProductRequest{}, controllers.ClearDevProductResponse{}, http.StatusCreated},
		{http.MethodGet, "/api/v1/cleardev/products/{id}", "getClearDevProduct", "Read immutable product discussion and stage execution facts", controllers.ClearDevRequirementIDParam{}, nil, controllers.ClearDevProductResponse{}, http.StatusOK},
		{http.MethodGet, "/api/v1/cleardev/projects/{projectId}/products", "listClearDevProducts", "List product goals for a project", controllers.ClearDevAOProjectIDParam{}, nil, controllers.ClearDevProductsResponse{}, http.StatusOK},
		{http.MethodPost, "/api/v1/cleardev/products/{id}/discussions", "discussClearDevProduct", "Submit another product discussion without approving development", controllers.ClearDevRequirementIDParam{}, controllers.ClearDevProductDiscussionRequest{}, controllers.ClearDevProductResponse{}, http.StatusAccepted},
		{http.MethodPost, "/api/v1/cleardev/products/{id}/stages/{stageId}/prepare", "prepareClearDevProductStage", "Prepare an exact stage for existing Electron specification confirmation; never approve", controllers.ClearDevProductStageParam{}, cleardevsvc.PrepareProductStageInput{}, controllers.ClearDevProductResponse{}, http.StatusAccepted},
	}
	ops := make([]operation, 0, len(items))
	for _, item := range items {
		op := operation{method: item.method, path: item.path, id: item.id, tag: "cleardev", summary: item.summary, reqBody: item.body,
			resps: []respUnit{
				{item.status, item.response},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			}}
		if item.params != nil {
			op.pathParams = []any{item.params}
		}
		ops = append(ops, op)
	}
	return ops
}
