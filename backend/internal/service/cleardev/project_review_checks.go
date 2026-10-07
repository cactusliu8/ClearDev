package cleardev

import (
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// runExecutionCandidateCheck has already validated and serialized the same
// versioned project receipt used by task/integration checks. Preserve its actual
// raw output, environment and failures in the existing Reviewer evidence path.
func projectReviewCheckEvidence(request core.ReviewCheckRequest, checkID string, argv []string, result ports.ClearDevCheckResult, runErr error, now time.Time) core.ReviewCheckEvidence {
	unavailable := core.ReviewCheckEvidence{
		ReviewID: request.ReviewID, CheckID: checkID, Outcome: "INFRA_ERROR", RecordedAt: now,
		Proof:  core.MailCheckProof{RunID: core.ReviewCheckRunID(request.ReviewID, checkID), CandidateSHA: request.CandidateSHA, Argv: append([]string(nil), argv...), ExitCode: -1},
		Output: "Trusted project check unavailable or candidate/result binding invalid. No PASS or execution receipt was recorded.",
	}
	if runErr != nil {
		message := runErr.Error()
		if len(message) > 16000 {
			message = message[:16000]
		}
		unavailable.Output += " " + message
		return unavailable
	}
	if result.Outcome == ports.ClearDevCheckInfraError || result.CandidateSHA != request.CandidateSHA {
		return unavailable
	}
	receipt, err := core.ParseProjectCheckReceipt(result.OutputSummary, *request.ProjectExecution, unavailable.Proof.RunID, checkID, request.CandidateSHA)
	if err != nil || receipt.Outcome != string(result.Outcome) || receipt.ExitCode != result.ExitCode || receipt.TimedOut != result.TimedOut {
		return unavailable
	}
	proof := core.MailCheckProof{
		RunID: receipt.CheckRunID, CandidateSHA: receipt.CandidateSHA, Argv: append([]string(nil), receipt.Argv...),
		SourceManifestID: receipt.SourceManifestID, SourceTreeOID: receipt.SourceRootTreeOID, ImageID: receipt.ImageID,
		EnvironmentID: receipt.CheckEnvironmentID, OutputSHA256: receipt.OutputSHA256,
		Passed: receipt.Outcome == "PASS", ExitCode: receipt.ExitCode, TimedOut: receipt.TimedOut, Truncated: receipt.OutputTruncated,
	}
	evidence := core.ReviewCheckEvidence{ReviewID: request.ReviewID, CheckID: checkID, Proof: proof, ProjectReceipt: &receipt,
		Outcome: receipt.Outcome, Output: receipt.OutputSummary, RecordedAt: now}
	if core.ValidateReviewCheckEvidence(request, evidence) != nil {
		return unavailable
	}
	return evidence
}
