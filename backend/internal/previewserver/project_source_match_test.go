package previewserver

import (
	"context"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// A source prepared by an earlier build carries the historical HTML-escaped
// environment digest. It still identifies the same contract object, so the
// preview must accept it; a different contract, a different candidate or a
// missing binding stays rejected.
func TestProjectPreviewSourceAcceptsBothContractSpellings(t *testing.T) {
	contract := previewMatchTestContract(t, "Run the local test application for <n> tasks & retries.")
	other := previewMatchTestContract(t, "Run a different local test application.")
	canonical, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		t.Fatal(err)
	}
	legacy := core.LegacyProjectExecutionContractDigest(contract)
	if legacy == canonical {
		t.Fatal("fixture must contain HTML-escapable text so the two spellings differ")
	}
	project := &projectPreviewConfiguration{contract: contract, contractSHA: canonical, candidateSHA: strings.Repeat("c", 40)}
	valid := func() ports.ClearDevProjectResultSource {
		return ports.ClearDevProjectResultSource{
			WorkspacePath: "/tmp/preview-source", CandidateSHA: project.candidateSHA, ContractSHA256: canonical,
			Environment: ports.ClearDevCheckEnvironment{CandidateSHA: project.candidateSHA, SourceManifestID: strings.Repeat("a", 64), ProjectExecutionSHA256: canonical},
			Release:     func(context.Context) error { return nil },
		}
	}
	if !projectPreviewSourceMatches(valid(), project) {
		t.Fatal("the canonical environment digest must be accepted")
	}
	historical := valid()
	historical.Environment.ProjectExecutionSHA256 = legacy
	if !projectPreviewSourceMatches(historical, project) {
		t.Fatal("the historical escaped environment digest must still be accepted")
	}

	otherCanonical, err := core.ProjectExecutionContractDigest(other)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ports.ClearDevProjectResultSource){
		"another contract": func(source *ports.ClearDevProjectResultSource) {
			source.Environment.ProjectExecutionSHA256 = otherCanonical
		},
		"another legacy spelling": func(source *ports.ClearDevProjectResultSource) {
			source.Environment.ProjectExecutionSHA256 = core.LegacyProjectExecutionContractDigest(other)
		},
		"another candidate":       func(source *ports.ClearDevProjectResultSource) { source.CandidateSHA = strings.Repeat("d", 40) },
		"missing source manifest": func(source *ports.ClearDevProjectResultSource) { source.Environment.SourceManifestID = "" },
		"missing environment":     func(source *ports.ClearDevProjectResultSource) { source.Environment = ports.ClearDevCheckEnvironment{} },
		"rewritten source digest": func(source *ports.ClearDevProjectResultSource) { source.ContractSHA256 = legacy },
		"missing release":         func(source *ports.ClearDevProjectResultSource) { source.Release = nil },
		"relative workspace":      func(source *ports.ClearDevProjectResultSource) { source.WorkspacePath = "preview-source" },
	} {
		mutated := valid()
		mutate(&mutated)
		if projectPreviewSourceMatches(mutated, project) {
			t.Fatalf("%s must stay rejected", name)
		}
	}
}

func previewMatchTestContract(t *testing.T, launchDescription string) core.ProjectExecutionContract {
	t.Helper()
	base := strings.Repeat("0", 40)
	selection := core.ProductSelection{
		SourceDiscussionID: "preview-match-discussion",
		Option: core.ProductOption{
			Key: "existing", Origin: "EXISTING", Title: "Existing project",
			Description: "Existing local project", Tradeoffs: []string{"Uses the registered local source."},
		},
		Reason: "Continue the exact delivered project.", AOProjectID: "preview-match-project",
		RepositoryPath: "/tmp/preview-match-project", BaseCommitSHA: base,
	}
	basis := core.ProjectExecutionBasis{
		WritePaths: []string{"app/**"}, DependencyNeeds: []string{},
		Checks: []core.ProjectCheckSpec{{
			ID: "test", Argv: []string{"node", "--test", "app/test.js"}, TimeoutSeconds: 60, MainPaths: []string{"app/**"},
		}},
		Launch: core.ProjectLaunch{Argv: []string{"node", "app/server.js"}, WorkingDirectory: ".", Description: launchDescription},
	}
	contract, err := core.BuildProjectExecutionContract(core.ProjectExecutionAdmission{
		RequestID: "admission-preview-match", PlanID: "plan-preview-match", PlanSHA256: strings.Repeat("2", 64),
		RequirementSHA256: strings.Repeat("1", 64), BaseCommitSHA: base,
	}, core.ProductStage{
		ID: "stage-preview-match", ProductID: "product-preview-match", DiscussionID: "preview-match-discussion",
		Definition: core.ProductStageDefinition{ExecutionBasis: &basis}, DefinitionSHA256: strings.Repeat("3", 64),
		DevelopmentRequirementID: "requirement-preview-match", BaseCommitSHA: base, Selection: &selection,
	}, "execution-preview-match", "version-preview-match")
	if err != nil {
		t.Fatal(err)
	}
	return contract
}
