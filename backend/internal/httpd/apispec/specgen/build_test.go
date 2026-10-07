package specgen_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec/specgen"
)

// TestBuild_MatchesEmbedded is the drift guard: the committed (embedded)
// openapi.yaml must equal fresh Build() output. If this fails, run
// `go generate ./...` and commit the result.
func TestBuild_MatchesEmbedded(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	embedded := apispec.Default().YAML()
	if !bytes.Equal(normalizeYAML(got), normalizeYAML(embedded)) {
		t.Fatalf("embedded openapi.yaml is stale — run `go generate ./...` and commit.\n"+
			"len(fresh)=%d len(embedded)=%d", len(got), len(embedded))
	}
}

func TestBuild_SpawnHarnessEnumIncludesPrimeAgent(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(string(got), "          - prime-agent\n") {
		t.Fatal("SpawnSessionRequest harness enum does not contain prime-agent")
	}
}

func TestBuild_DelegateAgentEnumIncludesPrimeAgent(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("parse generated OpenAPI: %v", err)
	}
	agents := doc.Components.Schemas["DelegateTaskRequest"].Properties["agent"].Enum
	if !slices.Contains(agents, "prime-agent") {
		t.Fatalf("DelegateTaskRequest agent enum = %v, want prime-agent", agents)
	}
}

func TestBuild_IncludesClearDevStandardStart(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct{} `yaml:"responses"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("parse generated OpenAPI: %v", err)
	}
	path, ok := doc.Paths["/api/v1/cleardev/requirements/{id}/standard-runs"]
	if !ok {
		t.Fatal("standard flow start path missing from generated OpenAPI")
	}
	post, ok := path["post"]
	if !ok {
		t.Fatal("standard flow start POST operation missing from generated OpenAPI")
	}
	if _, ok := post.Responses["202"]; !ok {
		t.Fatal("standard flow start operation missing 202 response")
	}
}

func TestBuild_IncludesClearDevComplexParallelExecutionStart(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(string(got), "the control plane selects STANDARD") || !strings.Contains(string(got), "PARALLEL") {
		t.Fatal("generated OpenAPI does not describe automatic STANDARD/PARALLEL selection")
	}
	if !strings.Contains(string(got), "fixedBuilderCount") || !strings.Contains(string(got), "ClearDevComplexExecutionBatch") {
		t.Fatal("generated OpenAPI is missing PARALLEL read fields")
	}
}

func TestBuild_IncludesClearDevQuickExecutionStart(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(string(got), "or QUICK") {
		t.Fatal("generated OpenAPI does not describe automatic QUICK selection")
	}
	if !strings.Contains(string(got), "ClearDevComplexQuickSnapshot") || !strings.Contains(string(got), "quickExecution") {
		t.Fatal("generated OpenAPI is missing QUICK read fields")
	}
}

func TestBuild_IncludesClearDevControlledExceptionFacts(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(string(got), "ClearDevComplexExceptionFacts") || !strings.Contains(string(got), "scopeRequests") || !strings.Contains(string(got), "generatedProofs") {
		t.Fatal("generated OpenAPI is missing controlled-exception read fields")
	}
}

func TestBuild_IncludesClearDevProjectProgress(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string              `yaml:"operationId"`
			RequestBody *struct{}           `yaml:"requestBody"`
			Responses   map[string]struct{} `yaml:"responses"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("parse generated OpenAPI: %v", err)
	}
	list, ok := doc.Paths["/api/v1/cleardev/projects/{projectId}/progress"]
	if !ok {
		t.Fatal("project progress path missing from generated OpenAPI")
	}
	if _, ok := list["get"]; !ok || list["get"].OperationID != "listClearDevProjectProgress" {
		t.Fatalf("project progress GET = %+v", list["get"])
	}
	if _, ok := list["get"].Responses["200"]; !ok {
		t.Fatal("project progress GET missing 200 response")
	}
	explain, ok := doc.Paths["/api/v1/cleardev/requirements/{id}/progress-explanations"]
	if !ok {
		t.Fatal("progress explanation path missing from generated OpenAPI")
	}
	if _, ok := explain["post"]; !ok || explain["post"].OperationID != "requestClearDevProgressExplanation" {
		t.Fatalf("progress explanation POST = %+v", explain["post"])
	}
	if explain["post"].RequestBody != nil {
		t.Fatal("progress explanation POST must not expose a request body")
	}
	if !strings.Contains(string(got), "trustedProgress") || !strings.Contains(string(got), "ClearDevTrustedProgressSummary") {
		t.Fatal("generated OpenAPI is missing trusted progress read fields")
	}
}

func TestBuild_IncludesClearDevComplexStandardExecutionStart(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string              `yaml:"operationId"`
			Responses   map[string]struct{} `yaml:"responses"`
			RequestBody *struct{}           `yaml:"requestBody"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("parse generated OpenAPI: %v", err)
	}
	path, ok := doc.Paths["/api/v1/cleardev/requirements/{id}/execution-runs"]
	if !ok {
		t.Fatal("complex standard execution start path missing from generated OpenAPI")
	}
	post, ok := path["post"]
	if !ok {
		t.Fatal("complex standard execution start POST operation missing from generated OpenAPI")
	}
	if post.OperationID != "startClearDevComplexStandardExecution" {
		t.Fatalf("operation id = %q", post.OperationID)
	}
	if _, ok := post.Responses["202"]; !ok {
		t.Fatal("complex standard execution start operation missing 202 response")
	}
	if post.RequestBody != nil {
		t.Fatal("complex standard execution start operation must not expose a request body")
	}
}

func TestBuild_IncludesClearDevComplexRoutes(t *testing.T) {
	got, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct{} `yaml:"responses"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("parse generated OpenAPI: %v", err)
	}
	create, ok := doc.Paths["/api/v1/cleardev/requirements/complex"]
	if !ok {
		t.Fatal("complex create path missing from generated OpenAPI")
	}
	if _, ok := create["post"]; !ok {
		t.Fatal("complex create POST operation missing from generated OpenAPI")
	}
	if _, ok := create["post"].Responses["201"]; !ok {
		t.Fatal("complex create operation missing 201 response")
	}
	clarify, ok := doc.Paths["/api/v1/cleardev/requirements/{id}/complex-clarifications"]
	if !ok {
		t.Fatal("complex clarification path missing from generated OpenAPI")
	}
	if _, ok := clarify["post"]; !ok {
		t.Fatal("complex clarification POST operation missing from generated OpenAPI")
	}
	if _, ok := clarify["post"].Responses["200"]; !ok {
		t.Fatal("complex clarification operation missing 200 response")
	}
}

// TestBuild_Deterministic guards against nondeterministic output (which would
// make the drift check flaky in CI).
func TestBuild_Deterministic(t *testing.T) {
	a, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	b, err := specgen.Build()
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("Build() is not deterministic across calls")
	}
}

func normalizeYAML(in []byte) []byte {
	return bytes.ReplaceAll(in, []byte("\r\n"), []byte("\n"))
}
