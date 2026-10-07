package cleardev

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TrialCommandObservation is backend output, never a functional PASS verdict.
type TrialCommandObservation struct {
	Artifacts      []core.TrialArtifact `json:"artifacts,omitempty"`
	StepID         string               `json:"stepId"`
	RunID          string               `json:"runId"`
	CandidateSHA   string               `json:"candidateSha"`
	ContractSHA256 string               `json:"contractSha256"`
	ExitCode       int                  `json:"exitCode"`
	Output         string               `json:"output"`
	OutputSHA256   string               `json:"outputSha256"`
	Completed      bool                 `json:"completed"`
	Error          string               `json:"error,omitempty"`
}

func stageTrialCommandRequest(review core.RequirementFinalReview, contract core.ProjectExecutionContract, step core.ProjectTrialStep) ports.ClearDevCheckRequest {
	return ports.ClearDevCheckRequest{TrialStepID: step.ID, RunID: "stage-trial-" + coreDigest([]byte(review.ID+"\x00"+step.ID)), WorkspacePath: review.WorkspacePath, CandidateSHA: review.CandidateCommitSHA, Image: core.StandardCandidateCheckImage, Argv: step.Argv, Timeout: time.Duration(step.TimeoutSeconds) * time.Second, MemoryBytes: standardCheckMemoryBytes, PidsLimit: standardCheckPidsLimit, OutputLimit: standardCheckOutputLimit, ProjectExecution: &contract}
}

func trialCommandCompleted(result ports.ClearDevCheckResult, contract core.ProjectExecutionContract, candidate string, step core.ProjectTrialStep) bool {
	var artifacts []core.TrialArtifact
	if result.TrialArtifactsJSON != "" && json.Unmarshal([]byte(result.TrialArtifactsJSON), &artifacts) != nil {
		return false
	}
	if len(artifacts) != len(step.OutputFiles) {
		return false
	}
	for i, file := range artifacts {
		raw, err := base64.StdEncoding.DecodeString(file.Base64)
		if err != nil || file.Path != step.OutputFiles[i] || file.SHA256 != coreDigest(raw) {
			return false
		}
	}
	if _, err := core.ProjectExecutionContractDigest(contract); err != nil {
		return false
	}
	return result.TrialCommandExecuted && result.CandidateSHA == candidate && core.ProjectExecutionDigestMatches(result.ProjectExecutionSHA256, contract) && result.SourceManifestID != "" && result.OutputSHA256 == coreDigest([]byte(result.OutputSummary)) && !result.TimedOut && !result.OutputTruncated && (result.Outcome == ports.ClearDevCheckPass || result.Outcome == ports.ClearDevCheckFail) && result.ExitCode == step.ExpectedExitCode
}

func (s *Service) stageTrialCommandsComplete(ctx context.Context, review core.RequirementFinalReview, contract core.ProjectExecutionContract) bool {
	reader, ok := s.checks.(ports.ClearDevTrialReceiptReader)
	if contract.Basis.Trial == nil {
		return true
	}
	for _, step := range contract.Basis.Trial.Steps {
		if step.Kind != "COMMAND" {
			continue
		}
		if !ok {
			return false
		}
		result, found, err := reader.ReadCandidateCheck(ctx, stageTrialCommandRequest(review, contract, step))
		if err != nil || !found || !trialCommandCompleted(result, contract, review.CandidateCommitSHA, step) {
			return false
		}
	}
	return true
}

func (s *Service) runStageTrialCommand(ctx context.Context, review core.RequirementFinalReview, contract core.ProjectExecutionContract, id string) (TrialCommandObservation, error) {
	step, found := core.ProjectTrialCommand(contract.Basis, id)
	if !found {
		return TrialCommandObservation{}, errors.New("STAGE_TRIAL_STEP_INVALID: select a frozen command step")
	}
	request := stageTrialCommandRequest(review, contract, step)
	digest, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		return TrialCommandObservation{}, err
	}
	out := TrialCommandObservation{StepID: id, RunID: request.RunID, CandidateSHA: review.CandidateCommitSHA, ContractSHA256: digest}
	if s.checks == nil {
		return out, errors.New("STAGE_TRIAL_UNAVAILABLE")
	}
	result, runErr := s.checks.RunCandidateCheck(ctx, request)
	if result.TrialArtifactsJSON != "" {
		_ = json.Unmarshal([]byte(result.TrialArtifactsJSON), &out.Artifacts)
	}
	out.ExitCode, out.Output, out.OutputSHA256 = result.ExitCode, result.OutputSummary, result.OutputSHA256
	out.Completed = runErr == nil && trialCommandCompleted(result, contract, review.CandidateCommitSHA, step)
	if runErr != nil {
		out.Error = runErr.Error()
	} else if !out.Completed {
		out.Error = fmt.Sprintf("STAGE_TRIAL_OBSERVATION_INCOMPLETE: expected exit %d; received %d; outcome %s", step.ExpectedExitCode, result.ExitCode, result.Outcome)
	}
	return out, nil
}
