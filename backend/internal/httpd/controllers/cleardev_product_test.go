package controllers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type productHTTPFake struct {
	fakeClearDevService
	productID string
	stageID   string
	create    cleardevsvc.CreateProductGoalInput
	discuss   cleardevsvc.ProductDiscussionInput
	prepare   cleardevsvc.PrepareProductStageInput
	calls     int
	err       error
}

func (f *productHTTPFake) CreateProductGoal(_ context.Context, input cleardevsvc.CreateProductGoalInput) (cleardevsvc.ProductGoalView, error) {
	f.calls++
	f.create = input
	return cleardevsvc.ProductGoalView{Phase: "THINKING"}, f.err
}
func (f *productHTTPFake) GetProductGoal(_ context.Context, id string) (cleardevsvc.ProductGoalView, error) {
	f.calls++
	f.productID = id
	return cleardevsvc.ProductGoalView{Phase: "READY"}, f.err
}
func (f *productHTTPFake) ListProductGoals(_ context.Context, id string) (cleardevsvc.ProductGoalsView, error) {
	f.calls++
	f.productID = id
	return cleardevsvc.ProductGoalsView{AOProjectID: id, Products: []cleardevsvc.ProductGoalView{}}, f.err
}
func (f *productHTTPFake) SubmitProductDiscussion(_ context.Context, id string, input cleardevsvc.ProductDiscussionInput) (cleardevsvc.ProductGoalView, error) {
	f.calls++
	f.productID, f.discuss = id, input
	return cleardevsvc.ProductGoalView{Phase: "THINKING"}, f.err
}
func (f *productHTTPFake) PrepareProductStage(_ context.Context, id, stageID string, input cleardevsvc.PrepareProductStageInput) (cleardevsvc.ProductGoalView, error) {
	f.calls++
	f.productID, f.stageID, f.prepare = id, stageID, input
	return cleardevsvc.ProductGoalView{Phase: "FROZEN"}, f.err
}

func TestProductHTTPRoutesBindInputsAndHaveNoApprovalBypass(t *testing.T) {
	fake := &productHTTPFake{}
	server := clearDevHTTPServer(t, fake)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/api/v1/cleardev/products", `{"aoProjectId":"project","requestId":"request","name":"Mail","goalText":"Read imported mail"}`, http.StatusCreated},
		{http.MethodGet, "/api/v1/cleardev/products/product", "", http.StatusOK},
		{http.MethodGet, "/api/v1/cleardev/projects/project/products", "", http.StatusOK},
		{http.MethodPost, "/api/v1/cleardev/products/product/discussions", `{"requestId":"next","expectedPreviousId":"previous","message":"Local first"}`, http.StatusAccepted},
		{http.MethodPost, "/api/v1/cleardev/products/product/stages/stage/prepare", `{"definitionSha256":"digest"}`, http.StatusAccepted},
	} {
		body, status, headers := doRequest(t, server, test.method, test.path, test.body)
		assertJSON(t, headers)
		if status != test.status {
			t.Fatalf("%s %s: %d %s", test.method, test.path, status, body)
		}
	}
	if fake.create.AOProjectID != "project" || fake.discuss.ExpectedPreviousID != "previous" || fake.productID != "product" || fake.stageID != "stage" || fake.prepare.DefinitionSHA256 != "digest" || fake.calls != 5 {
		t.Fatalf("wrong request routing: %+v", fake)
	}
	for _, action := range []string{"approve", "confirm", "complete", "start"} {
		_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/products/product/stages/stage/"+action, `{}`)
		if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
			t.Fatalf("unexpected stage authority route %s: %d", action, status)
		}
	}
	if fake.startCalls != 0 || fake.complexExecutionCalls != 0 {
		t.Fatal("preparing a product stage directly started execution")
	}
}

func TestProductHTTPRejectsCallerAuthorityAndReturnsStableErrors(t *testing.T) {
	fake := &productHTTPFake{}
	server := clearDevHTTPServer(t, fake)
	for _, input := range []string{
		`{"definitionSha256":"a","approved":true}`,
		`{"definitionSha256":"a","baseCommitSha":"caller"}`,
		`{"definitionSha256":"a","candidateSha":"caller"}`,
		`{"definitionSha256":"a"} {}`,
		`{"definitionSha256":"` + strings.Repeat("a", 81*1024) + `"}`,
	} {
		body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/products/p/stages/s/prepare", input)
		assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
	}
	if fake.calls != 0 {
		t.Fatal("invalid product request reached service")
	}
	fake.err = apierr.Conflict("PRODUCT_STAGE_CHANGED", "refresh proposal", nil)
	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/products/p/stages/s/prepare", `{"definitionSha256":"a"}`)
	assertErrorCode(t, body, status, http.StatusConflict, "PRODUCT_STAGE_CHANGED")
	stub := clearDevHTTPServer(t, &fakeClearDevService{})
	body, status, _ = doRequest(t, stub, http.MethodGet, "/api/v1/cleardev/products/p", "")
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}

func TestProductHTTPStageIdentitySurvivesBrowserPathEncoding(t *testing.T) {
	for _, test := range []struct{ name, path, id string }{
		{"browser encoded generated id", "discussion%3Astage%3Acontact-search", "discussion:stage:contact-search"},
		{"unescaped generated id", "discussion:stage:contact-search", "discussion:stage:contact-search"},
		{"literal percent is not decoded twice", "discussion%253Astage", "discussion%3Astage"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &productHTTPFake{}
			server := clearDevHTTPServer(t, fake)
			body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/products/product/stages/"+test.path+"/prepare", `{"definitionSha256":"frozen-digest"}`)
			if status != http.StatusAccepted || fake.stageID != test.id || fake.productID != "product" || fake.prepare.DefinitionSHA256 != "frozen-digest" || fake.calls != 1 {
				t.Fatalf("stage identity changed in transport: status=%d id=%q want=%q body=%s", status, fake.stageID, test.id, body)
			}
		})
	}
}

func TestProductHTTPAutomaticFlagOnlyRequestsExistingDecision(t *testing.T) {
	fake := &productHTTPFake{}
	server := clearDevHTTPServer(t, fake)
	_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/cleardev/products/product/stages/stage/prepare", `{"definitionSha256":"exact","automatic":true}`)
	if status != http.StatusAccepted || !fake.prepare.Automatic || fake.prepare.DefinitionSHA256 != "exact" || fake.startCalls != 0 {
		t.Fatal("automatic request route changed authority", status)
	}
	_, status, _ = doRequest(t, server, http.MethodPost, "/api/v1/cleardev/products/product/stages/stage/prepare", `{"definitionSha256":"exact","automatic":true,"decision":"APPROVE"}`)
	if status != http.StatusBadRequest || fake.calls != 1 {
		t.Fatal("caller supplied approval")
	}
}
