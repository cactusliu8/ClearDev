package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// runExecutionCandidateCheck keeps one runner and one durable evidence path.
// Only a validated admitted run can attach a project contract. The runner's
// receipt is checked before it is adapted into the existing output column.
func (s *Service) runExecutionCandidateCheck(ctx context.Context, run core.ComplexExecutionRun, request ports.ClearDevCheckRequest, checkID string) (ports.ClearDevCheckResult, error) {
	contract, project, err := core.ProjectContractFromRun(run)
	if err != nil {
		return ports.ClearDevCheckResult{}, err
	}
	if !project {
		return s.checks.RunCandidateCheck(ctx, request)
	}
	check, found := core.ComplexCheckByID(contract.Basis.CheckCatalog(), checkID)
	if !found || !slices.Equal(request.Argv, check.Argv) || request.Timeout != time.Duration(check.TimeoutSeconds)*time.Second || request.Image != core.StandardCandidateCheckImage {
		return ports.ClearDevCheckResult{}, errors.New("PROJECT_CHECK_NOT_IN_ADMITTED_CATALOGUE")
	}
	request.ProjectExecution = &contract
	result, err := s.checks.RunCandidateCheck(ctx, request)
	if err != nil || result.Outcome == ports.ClearDevCheckInfraError {
		return result, err
	}
	digest, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		return result, err
	}
	// A durable receipt written before the canonical encoder carries the
	// default-encoder digest of the same contract; accept that historical form.
	if (result.ProjectExecutionSHA256 != digest && result.ProjectExecutionSHA256 != core.LegacyProjectExecutionContractDigest(contract)) || result.CandidateSHA != request.CandidateSHA {
		return result, errors.New("PROJECT_CHECK_RESULT_BINDING_CHANGED")
	}
	receipt := core.ProjectCheckReceipt{
		SchemaVersion: 1, Policy: core.ProjectCheckPolicyV1, ExecutionRunID: run.ID, ContractSHA256: digest,
		CheckRunID: request.RunID, CheckID: checkID, Argv: append([]string(nil), check.Argv...), TimeoutSeconds: check.TimeoutSeconds,
		CandidateSHA: result.CandidateSHA, Image: result.Image, ImageID: result.ImageID,
		SourceManifestID: result.SourceManifestID, SourceRootTreeOID: result.SourceRootTreeOID,
		CheckEnvironmentID: result.CheckEnvironmentID, ApprovedArgvSHA256: result.ApprovedArgvSHA256,
		NodeVersion: result.NodeVersion, NPMVersion: result.NPMVersion,
		PackageJSONSHA256: result.PackageJSONSHA256, PackageLockSHA256: result.PackageLockSHA256,
		DependencyCacheKey: result.DependencyCacheKey, DependencyEnvironment: result.DependencyEnvironmentID, DependencyTreeSHA256: result.DependencyTreeSHA256,
		Outcome: string(result.Outcome), ExitCode: result.ExitCode, TimedOut: result.TimedOut,
		OutputTruncated: result.OutputTruncated, OutputSummary: result.OutputSummary, OutputSHA256: result.OutputSHA256,
	}
	if result.Image == core.ProjectCandidateCheckImage {
		receipt.Policy = core.ProjectCheckPolicyV2
	}
	if err := core.ValidateProjectCheckReceipt(receipt, contract, request.RunID, checkID, request.CandidateSHA); err != nil {
		return result, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return result, err
	}
	result.OutputSummary, result.OutputSHA256 = string(encoded), coreDigest(encoded)
	return result, nil
}

func projectIntegrationCheckID(execution core.ComplexExecutionSnapshot, checkRunID string) (string, error) {
	for _, run := range execution.CheckRuns {
		if run.ID != checkRunID || run.Kind != core.CandidateCheckIntegration {
			continue
		}
		if spec, found := complexCheckSpecByID(execution, run.CheckSpecFactID); found && spec.Kind == core.CandidateCheckIntegration {
			return spec.CheckID, nil
		}
	}
	return "", errors.New("PROJECT_INTEGRATION_CHECK_BINDING_MISSING")
}
