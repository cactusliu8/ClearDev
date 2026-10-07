package cleardev

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

type benchmarkFactsFixture struct {
	binding core.BenchmarkBinding
	found   bool
	err     error
}

func (fixture benchmarkFactsFixture) GetClearDevBenchmarkBinding(context.Context, string) (core.BenchmarkBinding, bool, error) {
	return fixture.binding, fixture.found, fixture.err
}

func TestBenchmarkBindingMustMatchStartupManifestAfterRestart(t *testing.T) {
	manifest := benchmarkManifestFixture(t, core.BenchmarkGroupG3, core.BenchmarkModeStandardOnly)
	binding, err := core.NewBenchmarkBinding(manifest, "req", "project", time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	requirement := core.DevelopmentRequirement{ID: "req", AOProjectID: "project"}

	service := &Service{benchmarkFacts: benchmarkFactsFixture{binding: binding, found: true}, benchmarkManifest: &manifest}
	got, found, err := service.benchmarkBindingForRequirement(context.Background(), requirement)
	if err != nil || !found || got.ManifestSHA256 != binding.ManifestSHA256 {
		t.Fatalf("matching restart binding: found=%v err=%v binding=%+v", found, err, got)
	}

	service.benchmarkManifest = nil
	_, _, err = service.benchmarkBindingForRequirement(context.Background(), requirement)
	if apiErrorCode(err) != "BENCHMARK_BINDING_MISMATCH" {
		t.Fatalf("restart without manifest error = %v", err)
	}

	changed := manifest
	changed.ManifestSHA256 = strings.Repeat("9", 64)
	service.benchmarkManifest = &changed
	_, _, err = service.benchmarkBindingForRequirement(context.Background(), requirement)
	if apiErrorCode(err) != "BENCHMARK_BINDING_MISMATCH" {
		t.Fatalf("changed manifest error = %v", err)
	}
}

func TestBenchmarkServiceSelectionAndCheckCatalogFollowPersistedBinding(t *testing.T) {
	tasks := []core.ComplexPlanTask{
		{Key: "one", WritePaths: []string{"src/one.ts"}},
		{Key: "two", WritePaths: []string{"src/two.ts"}},
	}
	for _, tc := range []struct {
		group    string
		policy   core.BenchmarkModePolicy
		mode     core.WorkMode
		builders int
	}{
		{group: core.BenchmarkGroupG3, policy: core.BenchmarkModeStandardOnly, mode: core.WorkModeStandard, builders: 1},
		{group: core.BenchmarkGroupG4, policy: core.BenchmarkModeDynamic, mode: core.WorkModeParallel, builders: 2},
	} {
		t.Run(tc.group, func(t *testing.T) {
			manifest := benchmarkManifestFixture(t, tc.group, tc.policy)
			binding, err := core.NewBenchmarkBinding(manifest, "req", "project", time.Unix(1, 0).UTC())
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{benchmarkFacts: benchmarkFactsFixture{binding: binding, found: true}, benchmarkManifest: &manifest}
			requirement := core.DevelopmentRequirement{ID: "req", AOProjectID: "project"}
			selection, err := service.selectComplexModeForRequirement(context.Background(), requirement, tasks, 2)
			if err != nil || selection.Mode != tc.mode || selection.BuilderCount != tc.builders {
				t.Fatalf("selection = %+v err=%v", selection, err)
			}
			catalog, err := service.complexCheckCatalogForRequirement(context.Background(), requirement)
			if err != nil || len(catalog) != 5 || catalog[4].ID != "CHK-DEV-ALL" || strings.Join(catalog[4].Argv, " ") != "npm test" {
				t.Fatalf("catalog = %+v err=%v", catalog, err)
			}
		})
	}
}

func benchmarkManifestFixture(t *testing.T, group string, policy core.BenchmarkModePolicy) core.BenchmarkBackendManifest {
	t.Helper()
	isolation := t.TempDir()
	project := filepath.Join(isolation, "product")
	return core.BenchmarkBackendManifest{
		SchemaVersion: 1, Kind: core.BenchmarkBackendManifestKind, Purpose: core.BenchmarkPurposeOffline, FacilityVersion: core.BenchmarkFacilityVersionV1,
		RepositoryCommit: strings.Repeat("a", 40), FacilityCommit: strings.Repeat("b", 40), ProtocolCommit: strings.Repeat("c", 40), MaterialCommit: strings.Repeat("d", 40),
		MaterialSHA256: strings.Repeat("e", 64), PublicInputSHA256: strings.Repeat("f", 64), PlanUnitID: "DEV-R1-T3-" + group, Scene: 1,
		Group: group, Policy: policy, CheckProfile: core.BenchmarkCheckProfileNodeTS, CheckPrefix: "CHK-DEV", ProjectRoot: project, IsolationRoot: isolation,
		Path: filepath.Join(isolation, "private", "scene.json"), ManifestSHA256: strings.Repeat("1", 64),
	}
}

func apiErrorCode(err error) string {
	var apiError *apierr.Error
	if errors.As(err, &apiError) {
		return apiError.Code
	}
	return ""
}
