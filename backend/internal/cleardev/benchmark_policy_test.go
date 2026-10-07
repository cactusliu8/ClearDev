package cleardev

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadBenchmarkBackendManifestRejectsRelativeAndBindsRawDigest(t *testing.T) {
	if _, err := LoadBenchmarkBackendManifest("relative.json"); err == nil {
		t.Fatal("relative benchmark manifest was accepted")
	}
	isolation := t.TempDir()
	project := filepath.Join(isolation, "product")
	manifest := validBenchmarkManifest(project, isolation)
	path := filepath.Join(t.TempDir(), "scene.json")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadBenchmarkBackendManifest(path)
	if err != nil {
		t.Fatalf("LoadBenchmarkBackendManifest: %v", err)
	}
	if loaded.Path != path || len(loaded.ManifestSHA256) != 64 {
		t.Fatalf("runtime binding = path %q digest %q", loaded.Path, loaded.ManifestSHA256)
	}
	binding, err := NewBenchmarkBinding(*loaded, "req", "project", time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatalf("NewBenchmarkBinding: %v", err)
	}
	if !binding.MatchesManifest(loaded) || binding.CheckProfileSHA256 == "" {
		t.Fatalf("binding did not preserve manifest/profile: %+v", binding)
	}
}

func TestValidateBenchmarkBackendManifestRejectsCrossGroupPolicies(t *testing.T) {
	isolation := t.TempDir()
	manifest := validBenchmarkManifest(filepath.Join(isolation, "product"), isolation)
	manifest.Policy = BenchmarkModeDynamic
	if err := ValidateBenchmarkBackendManifest(manifest); err == nil || !strings.Contains(err.Error(), "G3") {
		t.Fatalf("G3 dynamic policy error = %v", err)
	}
	manifest.Group = BenchmarkGroupG4
	manifest.Policy = BenchmarkModeStandardOnly
	if err := ValidateBenchmarkBackendManifest(manifest); err == nil || !strings.Contains(err.Error(), "G4") {
		t.Fatalf("G4 fixed policy error = %v", err)
	}
}

func TestBenchmarkCheckCatalogAndPolicySelection(t *testing.T) {
	catalog, err := BenchmarkComplexCheckCatalog("CHK-DEV")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 5 {
		t.Fatalf("catalog size = %d", len(catalog))
	}
	all, ok := ComplexCheckByID(catalog, "CHK-DEV-ALL")
	if !ok || strings.Join(all.Argv, " ") != "npm test" {
		t.Fatalf("integration check = %+v ok=%v", all, ok)
	}
	tasks := []ComplexPlanTask{
		{Key: "left", WritePaths: []string{"src/left.ts"}},
		{Key: "right", WritePaths: []string{"src/right.ts"}},
	}
	dynamic, err := SelectComplexExecutionModeForPolicy(tasks, 2, BenchmarkModeDynamic)
	if err != nil {
		t.Fatal(err)
	}
	if dynamic.Mode != WorkModeParallel || dynamic.BuilderCount != 2 {
		t.Fatalf("dynamic selection = %+v", dynamic)
	}
	fixed, err := SelectComplexExecutionModeForPolicy(tasks, 2, BenchmarkModeStandardOnly)
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Mode != WorkModeStandard || fixed.BuilderCount != 1 || fixed.ReasonCode != ReasonParallelUnsafeDegrade || len(fixed.Batches) != 0 {
		t.Fatalf("fixed selection = %+v", fixed)
	}
	serial := []ComplexPlanTask{
		{Key: "first", WritePaths: []string{"src/first.ts"}},
		{Key: "second", WritePaths: []string{"src/second.ts"}, DependencyKeys: []string{"first"}},
	}
	serialDynamic, err := SelectComplexExecutionModeForPolicy(serial, 2, BenchmarkModeDynamic)
	if err != nil {
		t.Fatal(err)
	}
	if serialDynamic.Mode != WorkModeStandard || serialDynamic.BuilderCount != 1 {
		t.Fatalf("dynamic serial selection = %+v", serialDynamic)
	}
}

func validBenchmarkManifest(project, isolation string) BenchmarkBackendManifest {
	return BenchmarkBackendManifest{
		SchemaVersion: 1, Kind: BenchmarkBackendManifestKind, Purpose: BenchmarkPurposeOffline,
		FacilityVersion:  BenchmarkFacilityVersionV1,
		RepositoryCommit: strings.Repeat("a", 40), FacilityCommit: strings.Repeat("b", 40), ProtocolCommit: strings.Repeat("c", 40), MaterialCommit: strings.Repeat("d", 40),
		MaterialSHA256: strings.Repeat("e", 64), PublicInputSHA256: strings.Repeat("f", 64),
		PlanUnitID: "DEV-G3-PARALLEL", Scene: 1, Group: BenchmarkGroupG3, Policy: BenchmarkModeStandardOnly,
		CheckProfile: BenchmarkCheckProfileNodeTS, CheckPrefix: "CHK-DEV", ProjectRoot: project, IsolationRoot: isolation,
	}
}
