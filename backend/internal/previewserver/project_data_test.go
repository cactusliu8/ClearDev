package previewserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestProjectDataRejectsRedirectedStoredVersion(t *testing.T) {
	dataDir := t.TempDir()
	manager := New(nil, dataDir)
	t.Cleanup(manager.Close)

	contract := projectDataTestContract(t)
	first := strings.Repeat("a", 40)
	prepared, err := manager.prepareProjectData(context.Background(), contract, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.directory, "notes.db"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepared.publish(); err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "notes.db"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(prepared.directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, prepared.directory); err != nil {
		t.Fatal(err)
	}

	contract.BaseCommitSHA = first
	_, err = manager.prepareProjectData(context.Background(), contract, strings.Repeat("b", 40))
	if err == nil || !strings.Contains(err.Error(), "managed local directory") {
		t.Fatalf("redirected prior data error=%v", err)
	}
}

func TestProjectDataRejectsRedirectedFileDuringUpgrade(t *testing.T) {
	dataDir := t.TempDir()
	manager := New(nil, dataDir)
	t.Cleanup(manager.Close)

	contract := projectDataTestContract(t)
	first := strings.Repeat("c", 40)
	prepared, err := manager.prepareProjectData(context.Background(), contract, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.directory, "notes.db"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepared.publish(); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "outside.db")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(prepared.directory, "notes.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(prepared.directory, "notes.db")); err != nil {
		t.Fatal(err)
	}

	contract.BaseCommitSHA = first
	_, err = manager.prepareProjectData(context.Background(), contract, strings.Repeat("d", 40))
	if err == nil || !strings.Contains(err.Error(), "redirected paths") {
		t.Fatalf("redirected project data file error=%v", err)
	}
}

func TestProjectDataRequiresIntermediateDeliveryMigration(t *testing.T) {
	manager := New(nil, t.TempDir())
	t.Cleanup(manager.Close)
	ctx := context.Background()
	first := projectDataTestContract(t)
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	if current, err := manager.ProjectDataBaselineCurrent(ctx, first, a); err != nil || !current {
		t.Fatalf("unopened first delivery baseline current=%v err=%v", current, err)
	}
	versionA, err := manager.prepareProjectData(ctx, first, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionA.directory, "notes.db"), []byte("retained note"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := versionA.publish(); err != nil {
		t.Fatal(err)
	}
	if current, err := manager.ProjectDataBaselineCurrent(ctx, first, a); err != nil || !current {
		t.Fatalf("published A baseline current=%v err=%v", current, err)
	}
	second := nextProjectDataTestContract(t, first, a, "second")
	if ready, err := manager.ProjectDataReady(ctx, second, b); err != nil || ready {
		t.Fatalf("unopened B data ready=%v err=%v", ready, err)
	}
	third := nextProjectDataTestContract(t, second, b, "third")
	if _, err := manager.prepareProjectData(ctx, third, c); err == nil || !strings.Contains(err.Error(), "RESULT_DATA_BASELINE_CHANGED") {
		t.Fatalf("skipped B migration was accepted: %v", err)
	}
	versionB, err := manager.prepareProjectData(ctx, second, b)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(versionB.directory, "notes.db")); err != nil || string(data) != "retained note" {
		t.Fatalf("B lost A's data: %q %v", data, err)
	}
	if err := versionB.publish(); err != nil {
		t.Fatal(err)
	}
	if current, err := manager.ProjectDataBaselineCurrent(ctx, first, a); err != nil || current {
		t.Fatalf("superseded A baseline current=%v err=%v", current, err)
	}
	if ready, err := manager.ProjectDataReady(ctx, second, b); err != nil || !ready {
		t.Fatalf("migrated B data ready=%v err=%v", ready, err)
	}
	versionC, err := manager.prepareProjectData(ctx, third, c)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(versionC.directory, "notes.db")); err != nil || string(data) != "retained note" {
		t.Fatalf("C lost A's data across B: %q %v", data, err)
	}
}

func nextProjectDataTestContract(t *testing.T, prior core.ProjectExecutionContract, base, suffix string) core.ProjectExecutionContract {
	t.Helper()
	selection := prior.Selection
	selection.BaseCommitSHA = base
	contract, err := core.BuildProjectExecutionContract(core.ProjectExecutionAdmission{
		RequestID: "admission-" + suffix, PlanID: "plan-" + suffix, PlanSHA256: strings.Repeat("2", 64),
		RequirementSHA256: strings.Repeat("1", 64), BaseCommitSHA: base,
	}, core.ProductStage{
		ID: "stage-" + suffix, ProductID: prior.ProductID, DiscussionID: prior.DiscussionID,
		Definition: core.ProductStageDefinition{ExecutionBasis: &prior.Basis}, DefinitionSHA256: strings.Repeat("3", 64),
		DevelopmentRequirementID: "requirement-" + suffix, BaseCommitSHA: base, Selection: &selection,
	}, "execution-"+suffix, "version-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func projectDataTestContract(t *testing.T) core.ProjectExecutionContract {
	t.Helper()
	base := strings.Repeat("0", 40)
	selection := core.ProductSelection{
		SourceDiscussionID: "discussion-data-test",
		Option: core.ProductOption{
			Key: "existing", Origin: "EXISTING", Title: "Existing project",
			Description: "Existing local project", Tradeoffs: []string{"Uses the registered local source."},
		},
		Reason: "Continue the exact delivered project.", AOProjectID: "project-data-test",
		RepositoryPath: "/tmp/project-data-test", BaseCommitSHA: base,
	}
	basis := core.ProjectExecutionBasis{
		WritePaths: []string{"app/**"}, DependencyNeeds: []string{},
		Checks: []core.ProjectCheckSpec{{
			ID: "test", Argv: []string{"node", "--test", "app/test.js"}, TimeoutSeconds: 60, MainPaths: []string{"app/**"},
		}},
		Launch: core.ProjectLaunch{Argv: []string{"node", "app/server.js"}, WorkingDirectory: ".", Description: "Run the local test application."},
	}
	contract, err := core.BuildProjectExecutionContract(core.ProjectExecutionAdmission{
		RequestID: "admission-data-test", PlanID: "plan-data-test", PlanSHA256: strings.Repeat("2", 64),
		RequirementSHA256: strings.Repeat("1", 64), BaseCommitSHA: base,
	}, core.ProductStage{
		ID: "stage-data-test", ProductID: "product-data-test", DiscussionID: "discussion-data-test",
		Definition: core.ProductStageDefinition{ExecutionBasis: &basis}, DefinitionSHA256: strings.Repeat("3", 64),
		DevelopmentRequirementID: "requirement-data-test", BaseCommitSHA: base, Selection: &selection,
	}, "execution-data-test", "version-data-test")
	if err != nil {
		t.Fatal(err)
	}
	return contract
}
