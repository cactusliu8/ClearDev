package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	cleardev "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

const maxClearDevEmptyBodyBytes = 4 * 1024

// ClearDevService is the narrow ClearDev HTTP surface. Requirement
// confirmation, task creation, candidate registration and trusted evidence
// remain daemon-internal.
type ClearDevService interface {
	CreateRequirement(context.Context, cleardevsvc.CreateRequirementInput) (cleardevsvc.RequirementView, error)
	CreateComplexRequirement(context.Context, cleardevsvc.CreateComplexRequirementInput) (cleardevsvc.RequirementView, error)
	GetRequirement(context.Context, string) (cleardevsvc.RequirementView, error)
	SubmitComplexClarifications(context.Context, string, cleardevsvc.SubmitComplexClarificationsInput) (cleardevsvc.RequirementView, error)
	StartStandardFlow(context.Context, string) (cleardevsvc.RequirementView, error)
	StartComplexStandardExecution(context.Context, string) (cleardevsvc.RequirementView, error)
	ProposeDirectionIntent(context.Context, string, cleardevsvc.ProposeDirectionIntentInput) (cleardevsvc.RequirementView, error)
	ListProjectProgress(context.Context, string) (cleardevsvc.ProjectProgressView, error)
	RequestProgressExplanation(context.Context, string) (cleardevsvc.RequirementView, error)
	RequestDevelopmentTaskRework(context.Context, string, string) error
	RequestExtraReviewBudget(context.Context, string, string) (cleardevsvc.RequirementView, error)
	RequestExtraBuilderTurn(context.Context, string, string) (cleardevsvc.RequirementView, error)
	RequestControlledEngineChange(context.Context, string, cleardev.ControlledEngineChangeBinding) (cleardevsvc.RequirementView, error)
}

// ClearDevController owns the minimal /cleardev/requirements routes.
type ClearDevController struct {
	Svc ClearDevService
}

func (c *ClearDevController) Register(r chi.Router) {
	c.registerProducts(r)
	r.Put("/cleardev/projects/{projectId}/execution", c.setProjectExecution)
	r.Post("/cleardev/requirements/{id}/preflight-retries", c.retryPreflight)
	r.Get("/cleardev/requirements/{id}/recoveries", c.workflowRecovery)
	r.Post("/cleardev/requirements/{id}/recoveries", c.workflowRecovery)
	r.Post("/cleardev/requirements", c.create)
	r.Post("/cleardev/requirements/complex", c.createComplex)
	r.Get("/cleardev/requirements/{id}", c.get)
	r.Post("/cleardev/requirements/{id}/standard-runs", c.startStandard)
	r.Post("/cleardev/requirements/{id}/execution-runs", c.startComplexStandardExecution)
	r.Post("/cleardev/requirements/{id}/project-execution-runs", c.startProjectExecution)
	r.Post("/cleardev/requirements/{id}/complex-clarifications", c.clarify)
	r.Post("/cleardev/requirements/{id}/planner-clarifications", c.plannerClarify)
	r.Get("/cleardev/requirements/{id}/decision-displays", c.decisionDisplays)
	r.Post("/cleardev/requirements/{id}/decision-displays", c.decisionDisplays)
	r.Post("/cleardev/requirements/{id}/direction-intents", c.proposeDirection)
	r.Post("/cleardev/requirements/{id}/development-tasks/{taskId}/rework", c.reworkDevelopmentTask)
	r.Post("/cleardev/requirements/{id}/development-tasks/{taskId}/review-budget-authorizations", c.requestExtraReviewBudget)
	r.Post("/cleardev/requirements/{id}/development-tasks/{taskId}/builder-turn-authorizations", c.requestExtraBuilderTurn)
	r.Post("/cleardev/requirements/{id}/engine-change-authorizations", c.requestEngineChange)
	r.Get("/cleardev/projects/{projectId}/progress", c.listProgress)
	r.Post("/cleardev/requirements/{id}/progress-explanations", c.requestExplanation)
	r.Get("/cleardev/requirements/{id}/result-preview", c.resultPreview)
	r.Post("/cleardev/requirements/{id}/result-preview", c.resultPreview)
	r.Delete("/cleardev/requirements/{id}/result-preview", c.resultPreview)
	r.Post("/cleardev/requirements/{id}/stage-trial", c.stageTrial)
}

func (c *ClearDevController) create(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements")
		return
	}
	var input cleardevsvc.CreateRequirementInput
	if err := decodeClearDevJSON(r, &input); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	requirement, err := c.Svc.CreateRequirement(r.Context(), input)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, ClearDevRequirementResponse(requirement))
}

func (c *ClearDevController) createComplex(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/complex")
		return
	}
	var input cleardevsvc.CreateComplexRequirementInput
	if err := decodeClearDevJSON(r, &input); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	requirement, err := c.Svc.CreateComplexRequirement(r.Context(), input)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, ClearDevRequirementResponse(requirement))
}

func (c *ClearDevController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/cleardev/requirements/{id}")
		return
	}
	requirement, err := c.Svc.GetRequirement(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClearDevRequirementResponse(requirement))
}

func (c *ClearDevController) startStandard(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/{id}/standard-runs")
		return
	}
	if err := requireEmptyBody(r); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	requirement, err := c.Svc.StartStandardFlow(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ClearDevRequirementResponse(requirement))
}

func (c *ClearDevController) startComplexStandardExecution(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/{id}/execution-runs")
		return
	}
	if err := requireStrictlyEmptyBody(r); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	requirement, err := c.Svc.StartComplexStandardExecution(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ClearDevRequirementResponse(requirement))
}

func (c *ClearDevController) clarify(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/{id}/complex-clarifications")
		return
	}
	var input cleardevsvc.SubmitComplexClarificationsInput
	if err := decodeClearDevJSON(r, &input); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	requirement, err := c.Svc.SubmitComplexClarifications(r.Context(), chi.URLParam(r, "id"), input)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClearDevRequirementResponse(requirement))
}

func (c *ClearDevController) proposeDirection(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/{id}/direction-intents")
		return
	}
	var input cleardevsvc.ProposeDirectionIntentInput
	if err := decodeClearDevJSON(r, &input); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	requirement, err := c.Svc.ProposeDirectionIntent(r.Context(), chi.URLParam(r, "id"), input)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ClearDevRequirementResponse(requirement))
}

func (c *ClearDevController) listProgress(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/cleardev/projects/{projectId}/progress")
		return
	}
	view, err := c.Svc.ListProjectProgress(r.Context(), chi.URLParam(r, "projectId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, view)
}

func (c *ClearDevController) requestExplanation(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/{id}/progress-explanations")
		return
	}
	if err := requireStrictlyEmptyBody(r); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	requirement, err := c.Svc.RequestProgressExplanation(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ClearDevRequirementResponse(requirement))
}

// requestExtraReviewBudget records the request for one human authorization of
// one extra reviewer turn on one task budget; only the native Human Authority
// decision grants it.
func (c *ClearDevController) requestExtraReviewBudget(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/review-budget-authorizations")
		return
	}
	if err := requireStrictlyEmptyBody(r); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	requirement, err := c.Svc.RequestExtraReviewBudget(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "taskId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ClearDevRequirementResponse(requirement))
}

// requestEngineChange records the pending human decision offer for one
// deliberate mid-project engine change of a controlled project. It only
// records the request; the native Human Authority decision authorizes the
// engine change and the session switches.
func (c *ClearDevController) requestEngineChange(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/{id}/engine-change-authorizations")
		return
	}
	var input ClearDevEngineChangeRequest
	if err := decodeClearDevJSON(r, &input); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	requirement, err := c.Svc.RequestControlledEngineChange(r.Context(), chi.URLParam(r, "id"),
		cleardev.ControlledEngineChangeBinding{TargetHarness: input.TargetHarness, Model: input.Model, Effort: input.Effort})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ClearDevRequirementResponse(requirement))
}

func (c *ClearDevController) requestExtraBuilderTurn(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/builder-turn-authorizations")
		return
	}
	if err := requireStrictlyEmptyBody(r); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	requirement, err := c.Svc.RequestExtraBuilderTurn(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "taskId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ClearDevRequirementResponse(requirement))
}

// reworkDevelopmentTask is the human DECIDE action after a requirement final
// review REWORK: it sends the exact failed task back to its Builder within the
// task's existing rework budget. It never approves or completes anything.
func (c *ClearDevController) reworkDevelopmentTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/rework")
		return
	}
	if err := requireStrictlyEmptyBody(r); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if err := c.Svc.RequestDevelopmentTaskRework(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "taskId")); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	requirement, err := c.Svc.GetRequirement(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ClearDevRequirementResponse(requirement))
}

// requireEmptyBody accepts an omitted body and whitespace-only bodies. The
// standard-flow endpoint has no request fields: any non-whitespace byte is a
// malformed request, including valid JSON such as null or {}.
func requireEmptyBody(r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxClearDevEmptyBodyBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxClearDevEmptyBodyBytes {
		return errors.New("request body is too large")
	}
	if len(strings.TrimSpace(string(body))) != 0 {
		return errors.New("request body must be empty")
	}
	return nil
}

// requireStrictlyEmptyBody accepts only an omitted or zero-byte body. S06 has
// no caller-controlled input: whitespace is as invalid as JSON null or {}.
func requireStrictlyEmptyBody(r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxClearDevEmptyBodyBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxClearDevEmptyBodyBytes {
		return errors.New("request body is too large")
	}
	if len(body) != 0 {
		return errors.New("request body must be strictly empty")
	}
	return nil
}

func decodeClearDevJSON(r *http.Request, out any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}
