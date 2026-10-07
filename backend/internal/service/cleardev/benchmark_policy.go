package cleardev

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func (s *Service) benchmarkBindingForNewRequirement(projectPath, requirementID, projectID string, at time.Time) (*core.BenchmarkBinding, error) {
	if s.benchmarkManifest == nil {
		return nil, nil
	}
	if err := requireBenchmarkProjectMatch(projectPath, s.benchmarkManifest); err != nil {
		return nil, apierr.Conflict("BENCHMARK_PROJECT_MISMATCH", "This daemon is bound to a different benchmark product", nil)
	}
	binding, err := core.NewBenchmarkBinding(*s.benchmarkManifest, requirementID, projectID, at)
	if err != nil {
		return nil, apierr.Conflict("BENCHMARK_BINDING_INVALID", "The benchmark startup manifest cannot bind this requirement", nil)
	}
	return &binding, nil
}

func (s *Service) benchmarkBindingForRequirement(ctx context.Context, requirement core.DevelopmentRequirement) (core.BenchmarkBinding, bool, error) {
	if s.benchmarkFacts == nil {
		if s.benchmarkManifest == nil {
			return core.BenchmarkBinding{}, false, nil
		}
		return core.BenchmarkBinding{}, false, apierr.Internal("BENCHMARK_BINDING_UNAVAILABLE", "Benchmark binding storage is not configured")
	}
	binding, found, err := s.benchmarkFacts.GetClearDevBenchmarkBinding(ctx, requirement.ID)
	if err != nil {
		return core.BenchmarkBinding{}, false, apierr.Internal("BENCHMARK_BINDING_READ_FAILED", "Could not read the benchmark binding")
	}
	if found {
		if !binding.MatchesManifest(s.benchmarkManifest) {
			return core.BenchmarkBinding{}, false, apierr.Conflict("BENCHMARK_BINDING_MISMATCH", "The benchmark-bound requirement does not match this daemon startup manifest", nil)
		}
		return binding, true, nil
	}
	if s.benchmarkManifest == nil || s.ao == nil {
		return core.BenchmarkBinding{}, false, nil
	}
	project, ok, projectErr := s.ao.GetProject(ctx, requirement.AOProjectID)
	if projectErr != nil {
		return core.BenchmarkBinding{}, false, apierr.Internal("AO_PROJECT_READ_FAILED", "Could not read the AO project")
	}
	if ok && filepath.Clean(project.Path) == filepath.Clean(s.benchmarkManifest.ProjectRoot) {
		return core.BenchmarkBinding{}, false, apierr.Conflict("BENCHMARK_BINDING_REQUIRED", "This benchmark product requires the startup manifest binding created with the requirement", nil)
	}
	return core.BenchmarkBinding{}, false, nil
}

func (s *Service) complexCheckCatalogForRequirement(ctx context.Context, requirement core.DevelopmentRequirement) ([]core.ComplexCheckSpec, error) {
	binding, bound, err := s.benchmarkBindingForRequirement(ctx, requirement)
	if err != nil {
		return nil, err
	}
	if !bound {
		return core.FrozenComplexCheckCatalog(), nil
	}
	catalog, catalogErr := core.BenchmarkComplexCheckCatalog(binding.CheckPrefix)
	if catalogErr != nil || binding.CheckProfileSHA256 != core.BenchmarkCheckProfileSHA256(binding.CheckPrefix) {
		return nil, apierr.Conflict("BENCHMARK_CHECK_PROFILE_INVALID", "The persisted benchmark check profile is invalid", nil)
	}
	return catalog, nil
}

func (s *Service) selectComplexModeForRequirement(ctx context.Context, requirement core.DevelopmentRequirement, tasks []core.ComplexPlanTask, suggested int) (core.ComplexModeSelection, error) {
	binding, bound, err := s.benchmarkBindingForRequirement(ctx, requirement)
	if err != nil {
		return core.ComplexModeSelection{}, err
	}
	policy := core.BenchmarkModePolicy("")
	if bound {
		policy = binding.Policy
	}
	selection, selectionErr := core.SelectComplexExecutionModeForPolicy(tasks, suggested, policy)
	if selectionErr != nil {
		return core.ComplexModeSelection{}, selectionErr
	}
	return selection, nil
}

func requireBenchmarkProjectMatch(projectPath string, manifest *core.BenchmarkBackendManifest) error {
	if manifest == nil {
		return nil
	}
	if filepath.Clean(projectPath) != filepath.Clean(manifest.ProjectRoot) {
		return fmt.Errorf("benchmark manifest project root %q does not match AO project %q", manifest.ProjectRoot, projectPath)
	}
	return nil
}
