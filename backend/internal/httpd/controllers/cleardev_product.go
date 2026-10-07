package controllers

import (
	"context"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

// Product discovery is an additive surface. Preparing a stage creates only a
// requirement awaiting the existing native confirmation, never an approval.
type clearDevProductService interface {
	CreateProductGoal(context.Context, cleardevsvc.CreateProductGoalInput) (cleardevsvc.ProductGoalView, error)
	GetProductGoal(context.Context, string) (cleardevsvc.ProductGoalView, error)
	ListProductGoals(context.Context, string) (cleardevsvc.ProductGoalsView, error)
	SubmitProductDiscussion(context.Context, string, cleardevsvc.ProductDiscussionInput) (cleardevsvc.ProductGoalView, error)
	PrepareProductStage(context.Context, string, string, cleardevsvc.PrepareProductStageInput) (cleardevsvc.ProductGoalView, error)
}

func (c *ClearDevController) registerProducts(r chi.Router) {
	r.Post("/cleardev/products", c.createProduct)
	r.Get("/cleardev/products/{id}", c.getProduct)
	r.Get("/cleardev/projects/{projectId}/products", c.listProducts)
	r.Post("/cleardev/products/{id}/discussions", c.discussProduct)
	r.Post("/cleardev/products/{id}/stages/{stageId}/prepare", c.prepareProductStage)
}

func (c *ClearDevController) productService(w http.ResponseWriter, r *http.Request, path string) (clearDevProductService, bool) {
	svc, ok := c.Svc.(clearDevProductService)
	if !ok {
		apispec.NotImplemented(w, r, r.Method, path)
	}
	return svc, ok
}

func decodeProductInput(w http.ResponseWriter, r *http.Request, input any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 80*1024)
	if err := decodeClearDevJSON(r, input); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid product request", nil)
		return false
	}
	return true
}

func writeProductResult(w http.ResponseWriter, r *http.Request, view cleardevsvc.ProductGoalView, err error, status int) {
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, status, ClearDevProductResponse(view))
}

func (c *ClearDevController) createProduct(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.productService(w, r, "/api/v1/cleardev/products")
	if !ok {
		return
	}
	var input ClearDevCreateProductRequest
	if !decodeProductInput(w, r, &input) {
		return
	}
	view, err := svc.CreateProductGoal(r.Context(), cleardevsvc.CreateProductGoalInput(input))
	writeProductResult(w, r, view, err, http.StatusCreated)
}

func (c *ClearDevController) getProduct(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.productService(w, r, "/api/v1/cleardev/products/{id}")
	if !ok {
		return
	}
	view, err := svc.GetProductGoal(r.Context(), chi.URLParam(r, "id"))
	writeProductResult(w, r, view, err, http.StatusOK)
}

func (c *ClearDevController) listProducts(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.productService(w, r, "/api/v1/cleardev/projects/{projectId}/products")
	if !ok {
		return
	}
	view, err := svc.ListProductGoals(r.Context(), chi.URLParam(r, "projectId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClearDevProductsResponse(view))
}

func (c *ClearDevController) discussProduct(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.productService(w, r, "/api/v1/cleardev/products/{id}/discussions")
	if !ok {
		return
	}
	var input ClearDevProductDiscussionRequest
	if !decodeProductInput(w, r, &input) {
		return
	}
	view, err := svc.SubmitProductDiscussion(r.Context(), chi.URLParam(r, "id"), cleardevsvc.ProductDiscussionInput(input))
	writeProductResult(w, r, view, err, http.StatusAccepted)
}

func (c *ClearDevController) prepareProductStage(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.productService(w, r, "/api/v1/cleardev/products/{id}/stages/{stageId}/prepare")
	if !ok {
		return
	}
	var input PrepareClearDevProductStageRequest
	if !decodeProductInput(w, r, &input) {
		return
	}
	stageID := chi.URLParam(r, "stageId")
	// Chi matches RawPath when present. The browser percent-encodes the
	// colons in generated stage IDs; decode that route value exactly once.
	// With no RawPath, net/http has already decoded URL.Path.
	if r.URL.RawPath != "" {
		decoded, err := url.PathUnescape(stageID)
		if err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_STAGE_ID", "Invalid stage path", nil)
			return
		}
		stageID = decoded
	}
	view, err := svc.PrepareProductStage(r.Context(), chi.URLParam(r, "id"), stageID, cleardevsvc.PrepareProductStageInput(input))
	writeProductResult(w, r, view, err, http.StatusAccepted)
}
