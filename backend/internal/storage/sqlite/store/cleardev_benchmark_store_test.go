package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestClearDevBenchmarkBindingIsAtomicUniqueAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Join(t.TempDir(), "isolation", "product")
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "benchmark-project", Path: projectRoot, Kind: domain.ProjectKindSingleRepo, RegisteredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	first := initialRequirement("benchmark-req-1", "benchmark-project", now)
	first.BenchmarkBinding = benchmarkBindingFixture("benchmark-req-1", "benchmark-project", projectRoot, now)
	if err := store.CreateClearDevRequirement(ctx, first); err != nil {
		t.Fatalf("create first benchmark requirement: %v", err)
	}
	binding, found, err := store.GetClearDevBenchmarkBinding(ctx, "benchmark-req-1")
	if err != nil || !found {
		t.Fatalf("read first binding: found=%v err=%v", found, err)
	}
	if binding.Policy != core.BenchmarkModeStandardOnly || binding.Group != core.BenchmarkGroupG3 || binding.CheckPrefix != "CHK-DEV" || binding.ProjectRoot != projectRoot {
		t.Fatalf("binding = %+v", binding)
	}

	second := initialRequirement("benchmark-req-2", "benchmark-project", now.Add(time.Second))
	second.BenchmarkBinding = benchmarkBindingFixture("benchmark-req-2", "benchmark-project", projectRoot, now.Add(time.Second))
	if err := store.CreateClearDevRequirement(ctx, second); err == nil {
		t.Fatal("duplicate manifest binding unexpectedly created a second requirement")
	}
	if _, found, err := store.GetClearDevRequirement(ctx, "benchmark-req-2"); err != nil || found {
		t.Fatalf("duplicate binding left a partial requirement: found=%v err=%v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	binding, found, err = reopened.GetClearDevBenchmarkBinding(ctx, "benchmark-req-1")
	if err != nil || !found {
		t.Fatalf("read binding after restart: found=%v err=%v", found, err)
	}
	if binding.ManifestSHA256 != strings.Repeat("1", 64) || binding.CheckProfileSHA256 != core.BenchmarkCheckProfileSHA256("CHK-DEV") {
		t.Fatalf("restarted binding = %+v", binding)
	}
}

func TestClearDevBenchmarkBindingConcurrentDuplicateHasOneWinnerAndNoPartialLoser(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	projectRoot := filepath.Join(t.TempDir(), "isolation", "product")
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "benchmark-concurrent-project", Path: projectRoot, Kind: domain.ProjectKindSingleRepo, RegisteredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	ids := []string{"benchmark-concurrent-a", "benchmark-concurrent-b"}
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for index, id := range ids {
		wg.Add(1)
		go func(index int, id string) {
			defer wg.Done()
			initial := initialRequirement(id, "benchmark-concurrent-project", now.Add(time.Duration(index)*time.Second))
			initial.BenchmarkBinding = benchmarkBindingFixture(id, "benchmark-concurrent-project", projectRoot, now.Add(time.Duration(index)*time.Second))
			errs[index] = store.CreateClearDevRequirement(ctx, initial)
		}(index, id)
	}
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent same-manifest successes = %d, errors=%v", successes, errs)
	}
	foundCount := 0
	for _, id := range ids {
		if _, found, err := store.GetClearDevRequirement(ctx, id); err != nil {
			t.Fatal(err)
		} else if found {
			foundCount++
		}
	}
	if foundCount != 1 {
		t.Fatalf("concurrent binding left %d requirements, want exactly one", foundCount)
	}
}

func benchmarkBindingFixture(requirementID, projectID, projectRoot string, at time.Time) *core.BenchmarkBinding {
	return &core.BenchmarkBinding{
		DevelopmentRequirementID: requirementID, AOProjectID: projectID, ProjectRoot: projectRoot,
		ManifestPath: filepath.Join(filepath.Dir(projectRoot), "private", "scene.json"), ManifestSHA256: strings.Repeat("1", 64),
		FacilityVersion: core.BenchmarkFacilityVersionV1, Purpose: core.BenchmarkPurposeOffline, PlanUnitID: "DEV-G3-PARALLEL", Scene: 1,
		Group: core.BenchmarkGroupG3, Policy: core.BenchmarkModeStandardOnly, CheckProfile: core.BenchmarkCheckProfileNodeTS, CheckPrefix: "CHK-DEV",
		CheckProfileSHA256: core.BenchmarkCheckProfileSHA256("CHK-DEV"), PublicInputSHA256: strings.Repeat("2", 64), MaterialSHA256: strings.Repeat("3", 64), CreatedAt: at,
	}
}

func TestClearDevBenchmarkLiveBindingPersistsUnboundedSceneAndRejectsPurposeMix(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	projectRoot := filepath.Join(t.TempDir(), "product")
	if err := st.UpsertProject(ctx, domain.ProjectRecord{ID: "live-project", Path: projectRoot, Kind: domain.ProjectKindSingleRepo, RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	initial := initialRequirement("live-req", "live-project", now)
	initial.BenchmarkBinding = benchmarkBindingFixture("live-req", "live-project", projectRoot, now)
	initial.BenchmarkBinding.Purpose = core.BenchmarkPurposeDevLive
	initial.BenchmarkBinding.FacilityVersion = core.BenchmarkFacilityVersionV17
	initial.BenchmarkBinding.Scene = 99
	initial.BenchmarkBinding.PlanUnitID = "DEV-SENSOR-G3"
	if err := st.CreateClearDevRequirement(ctx, initial); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = sqlite.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	binding, found, err := st.GetClearDevBenchmarkBinding(ctx, "live-req")
	if err != nil || !found || binding.Scene != 99 || binding.Purpose != core.BenchmarkPurposeDevLive {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}
	bad := initialRequirement("bad-live", "live-project", now)
	bad.BenchmarkBinding = benchmarkBindingFixture("bad-live", "live-project", projectRoot, now)
	bad.BenchmarkBinding.ManifestSHA256 = strings.Repeat("e", 64)
	bad.BenchmarkBinding.Purpose = core.BenchmarkPurposeFormal
	bad.BenchmarkBinding.FacilityVersion = core.BenchmarkFacilityVersionV17
	bad.BenchmarkBinding.PlanUnitID = "R1-T1-G3"
	bad.BenchmarkBinding.Scene = 99
	if st.CreateClearDevRequirement(ctx, bad) == nil {
		t.Fatal("formal scene 99 was accepted")
	}
	if _, found, err := st.GetClearDevRequirement(ctx, "bad-live"); err != nil || found {
		t.Fatalf("partial failed requirement found=%v err=%v", found, err)
	}
}
