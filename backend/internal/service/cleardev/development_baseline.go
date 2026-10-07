package cleardev

import (
	"context"
	"errors"
	"fmt"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type baselineGateError struct {
	reason core.ReasonCode
	err    error
}

func (e *baselineGateError) Error() string { return fmt.Sprintf("%s: %v", e.reason, e.err) }
func (e *baselineGateError) Unwrap() error { return e.err }

func (s *Service) stopStandardBaseline(ctx context.Context, dispatchID string, failure error) (bool, bool, error) {
	s.logger.Error("ClearDev baseline blocked Builder creation", "dispatchID", dispatchID, "error", failure)
	changed, err := s.standard.SettleClearDevStandardDispatchFailure(ctx, dispatchID, core.DispatchStatusFailed, baselineFailureReason(failure), s.now().UTC())
	if err != nil {
		return false, false, err
	}
	return changed, true, errStandardStopped
}

func (s *Service) stopComplexBaseline(ctx context.Context, binding core.ComplexExecutionRoleBinding, failure error) (bool, bool, error) {
	s.logger.Error("ClearDev baseline blocked Builder creation", "bindingID", binding.ID, "error", failure)
	var changed bool
	var err error
	if binding.Status == core.RoleBindingStatusBound {
		changed, err = s.complexExecution.EndClearDevComplexExecutionRoleBinding(ctx, binding.ID, baselineFailureReason(failure), s.now().UTC())
	} else {
		changed, err = s.complexExecution.FailClearDevComplexExecutionRoleBinding(ctx, binding.ID, baselineFailureReason(failure), s.now().UTC())
	}
	if err != nil {
		return false, false, err
	}
	return changed, true, errComplexExecutionStopped
}

func baselineFailureReason(err error) core.ReasonCode {
	var gate *baselineGateError
	if errors.As(err, &gate) {
		return gate.reason
	}
	return "BASELINE_CHECKER_UNAVAILABLE"
}

func checkedBaselineReason(reason string) core.ReasonCode {
	switch reason {
	case "BASELINE_WORKSPACE_INVALID", "BASELINE_WORKSPACE_CHANGED", "BASELINE_CONTRACT_CHANGED", "BASELINE_TEST_FAILED", "BASELINE_HEALTH_FAILED":
		return core.ReasonCode(reason)
	default:
		return "BASELINE_CHECKER_UNAVAILABLE"
	}
}

// checkBuilderBaseline runs before session creation, including replay of a
// pending dispatch after daemon restart. No Agent text can supply this receipt.
func (s *Service) checkBuilderBaseline(ctx context.Context, runID, projectID string) (ports.ClearDevBaselineResult, string, error) {
	var empty ports.ClearDevBaselineResult
	// The authorized benchmark has a separate frozen starting-product/check
	// contract and a bound checker that cannot inspect ordinary project roots.
	// This server-owned manifest cannot be selected through a requirement/plan.
	if s.benchmarkManifest != nil {
		return empty, "", nil
	}
	checker, ok := s.checks.(ports.ClearDevDevelopmentBaselineChecker)
	if !ok || s.ao == nil {
		return empty, "", &baselineGateError{reason: "BASELINE_CHECKER_UNAVAILABLE", err: errors.New("trusted baseline checker is not configured")}
	}
	project, found, err := s.ao.GetProject(ctx, projectID)
	if err != nil || !found || project.Path == "" || !project.ArchivedAt.IsZero() {
		return empty, "", &baselineGateError{reason: "BASELINE_WORKSPACE_INVALID", err: errors.New("registered project is unavailable")}
	}
	result, err := checker.CheckDevelopmentBaseline(ctx, ports.ClearDevBaselineRequest{RunID: runID, WorkspacePath: project.Path, BaseBranch: project.Config.DefaultBranch})
	if err != nil {
		return result, project.Path, &baselineGateError{reason: checkedBaselineReason(result.ReasonCode), err: err}
	}
	if !result.Required {
		return result, project.Path, nil
	}
	if !validComplexExecutionCommitSHA(result.CandidateSHA) || result.ReasonCode != "" || result.TestRunID != runID+":npm-test" || result.HealthRunID != runID+":health" {
		return result, project.Path, &baselineGateError{reason: "BASELINE_CHECKER_UNAVAILABLE", err: errors.New("baseline receipt has an invalid binding")}
	}
	for _, check := range []ports.ClearDevCheckResult{result.Tests, result.Health} {
		if check.CandidateSHA != result.CandidateSHA || check.Image != core.StandardCandidateCheckImage || check.ImageID == "" || check.CheckEnvironmentID == "" || check.OutputSHA256 == "" || check.Outcome != ports.ClearDevCheckPass || check.ExitCode != 0 || check.TimedOut || check.OutputTruncated {
			return result, project.Path, &baselineGateError{reason: "BASELINE_CHECKER_UNAVAILABLE", err: errors.New("baseline receipt has incomplete, failed, or stale checks")}
		}
	}
	if result.Tests.ImageID != result.Health.ImageID {
		return result, project.Path, &baselineGateError{reason: "BASELINE_CHECKER_UNAVAILABLE", err: errors.New("baseline check images differ")}
	}
	return result, project.Path, nil
}

func (s *Service) pinBaselineBuilderBranch(ctx context.Context, baseline ports.ClearDevBaselineResult, workspace, branch string) error {
	if !baseline.Required {
		return nil
	}
	if s.inspector == nil {
		return &baselineGateError{reason: "BASELINE_CHECKER_UNAVAILABLE", err: errors.New("baseline inspector is not configured")}
	}
	// Reuse the existing exact-commit branch preparation. A pre-existing branch
	// at another SHA fails instead of silently selecting the project default.
	if err := s.inspector.PrepareReviewBranch(ctx, workspace, branch, baseline.CandidateSHA); err != nil {
		return &baselineGateError{reason: "BASELINE_WORKSPACE_CHANGED", err: err}
	}
	return nil
}

func (s *Service) verifySpawnedBuilderBaseline(ctx context.Context, baseline ports.ClearDevBaselineResult, record domain.SessionRecord) error {
	if !baseline.Required {
		return nil
	}
	if s.inspector == nil || record.Metadata.DiffBaseSHA != baseline.CandidateSHA || record.Metadata.WorkspacePath == "" {
		return &baselineGateError{reason: "BASELINE_WORKSPACE_CHANGED", err: errors.New("builder was not created from the checked baseline")}
	}
	observed, err := s.inspector.InspectCandidate(ctx, record.Metadata.WorkspacePath, baseline.CandidateSHA)
	if err != nil || observed.BaseSHA != baseline.CandidateSHA || observed.CandidateSHA != baseline.CandidateSHA || len(observed.Paths) != 0 {
		return &baselineGateError{reason: "BASELINE_WORKSPACE_CHANGED", err: errors.New("builder workspace changed before its first task")}
	}
	return nil
}
