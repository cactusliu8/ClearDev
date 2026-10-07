package cleardevlocal

import (
	"encoding/json"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// The adapter used to answer with the HTML-escaped json.Marshal spelling while
// admission, receipts and the preview moved to the single canonical encoder.
// A contract that contains "<", ">" or "&" therefore compared unequal to itself
// on the preview path. One contract keeps one identity digest everywhere.
func TestProjectCheckContractDigestKeepsOneIdentity(t *testing.T) {
	contract := digestTestContract(t, "Run the local test application for <n> tasks & retries.")
	canonical, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		t.Fatal(err)
	}
	legacy := core.LegacyProjectExecutionContractDigest(contract)
	if legacy == canonical {
		t.Fatal("fixture must contain HTML-escapable text so the two spellings differ")
	}
	digest, err := projectCheckContractDigest(&contract)
	if err != nil {
		t.Fatal(err)
	}
	if digest != canonical {
		t.Fatalf("adapter digest drifted from the canonical identity: %q != %q", digest, canonical)
	}
	if digest == legacy {
		t.Fatal("adapter still answers with the historical escaped spelling")
	}
	raw, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	if sha256Hex(raw) != legacy {
		t.Fatal("forced-failure fixture no longer proves the escaped spelling")
	}
	if empty, err := projectCheckContractDigest(nil); err != nil || empty != "" {
		t.Fatalf("a missing contract stays empty: %q %v", empty, err)
	}
}

func TestProjectCheckContractDigestRejectsInvalidContract(t *testing.T) {
	contract := digestTestContract(t, "Run the local test application.")
	contract.Basis.Launch.Argv = nil
	if digest, err := projectCheckContractDigest(&contract); err == nil || digest != "" {
		t.Fatalf("an invalid contract must not receive an identity digest: %q %v", digest, err)
	}
}

func digestTestContract(t *testing.T, launchDescription string) core.ProjectExecutionContract {
	t.Helper()
	base := strings.Repeat("0", 40)
	selection := core.ProductSelection{
		SourceDiscussionID: "digest-discussion",
		Option: core.ProductOption{
			Key: "existing", Origin: "EXISTING", Title: "Existing project",
			Description: "Existing local project", Tradeoffs: []string{"Uses the registered local source."},
		},
		Reason: "Continue the exact delivered project.", AOProjectID: "digest-project",
		RepositoryPath: "/tmp/digest-project", BaseCommitSHA: base,
	}
	basis := core.ProjectExecutionBasis{
		WritePaths: []string{"app/**"}, DependencyNeeds: []string{},
		Checks: []core.ProjectCheckSpec{{
			ID: "test", Argv: []string{"node", "--test", "app/test.js"}, TimeoutSeconds: 60, MainPaths: []string{"app/**"},
		}},
		Launch: core.ProjectLaunch{Argv: []string{"node", "app/server.js"}, WorkingDirectory: ".", Description: launchDescription},
	}
	contract, err := core.BuildProjectExecutionContract(core.ProjectExecutionAdmission{
		RequestID: "admission-digest", PlanID: "plan-digest", PlanSHA256: strings.Repeat("2", 64),
		RequirementSHA256: strings.Repeat("1", 64), BaseCommitSHA: base,
	}, core.ProductStage{
		ID: "stage-digest", ProductID: "product-digest", DiscussionID: "digest-discussion",
		Definition: core.ProductStageDefinition{ExecutionBasis: &basis}, DefinitionSHA256: strings.Repeat("3", 64),
		DevelopmentRequirementID: "requirement-digest", BaseCommitSHA: base, Selection: &selection,
	}, "execution-digest", "version-digest")
	if err != nil {
		t.Fatal(err)
	}
	return contract
}
