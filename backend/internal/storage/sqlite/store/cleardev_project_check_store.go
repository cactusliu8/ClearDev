package store

import (
	"context"
	"encoding/json"
	"slices"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// validateProjectCheckSettlement uses durable run/spec/candidate identities,
// never a checker's self-selected contract. Unknown/unexecuted outcomes may be
// recorded as infrastructure failures, but cannot acquire PASS/FAIL evidence.
func validateProjectCheckSettlement(ctx context.Context, q *gen.Queries, command core.SettleComplexExecutionCheckCommand) error {
	check, err := q.GetClearDevComplexExecutionCheckRun(ctx, command.CheckRunID)
	if err != nil {
		return err
	}
	spec, err := q.GetClearDevProjectCheckSpec(ctx, check.CheckSpecID)
	if err != nil {
		return err
	}
	run, err := q.GetClearDevComplexExecutionRun(ctx, spec.ExecutionRunID)
	if err != nil {
		return err
	}
	effective, err := effectiveProjectRun(ctx, q, run)
	if err != nil {
		return err
	}
	contract, project, err := core.ProjectContractFromRun(effective)
	if err != nil || !project || spec.CheckKind == string(core.CandidateCheckScope) {
		return err
	}
	if command.Result != core.EvidenceResultPass && command.Result != core.EvidenceResultFail {
		return nil
	}
	return validateProjectReceiptEvidence(contract, spec, check.ID, check.CandidateCommitSha, command)
}

func validateProjectReceiptEvidence(contract core.ProjectExecutionContract, spec gen.CleardevComplexExecutionCheckSpec, checkRunID, candidateSHA string, command core.SettleComplexExecutionCheckCommand) error {
	approved, found := core.ComplexCheckByID(contract.Basis.CheckCatalog(), spec.CheckName)
	var argv []string
	if !found || spec.ExecutionRunID != contract.ExecutionRunID || json.Unmarshal([]byte(spec.ArgvJson), &argv) != nil ||
		!slices.Equal(argv, approved.Argv) || spec.TimeoutSeconds != int64(approved.TimeoutSeconds) ||
		complexExecutionRawDigest([]byte(spec.ArgvJson)) != spec.CheckSpecSha256 ||
		complexExecutionRawDigest([]byte(command.OutputSummary)) != command.OutputSHA256 {
		return complexExecutionRule("project check evidence changed its admitted check specification or output digest")
	}
	receipt, err := core.ParseProjectCheckReceipt(command.OutputSummary, contract, checkRunID, spec.CheckName, candidateSHA)
	if err != nil {
		return complexExecutionRule(err.Error())
	}
	if command.ExitCode == nil || *command.ExitCode != receipt.ExitCode || command.TimedOut != receipt.TimedOut || command.ContainerImageID != receipt.ImageID ||
		(command.Result == core.EvidenceResultPass) != (receipt.Outcome == "PASS") ||
		(command.Result != core.EvidenceResultPass && command.Result != core.EvidenceResultFail) {
		return complexExecutionRule("project check settlement differs from its actual process receipt")
	}
	return nil
}

// Re-read the saved check at verification and completion. A stale PASS flag,
// another candidate's output or an old non-project receipt is not evidence.
func validateProjectStoredCheck(ctx context.Context, q *gen.Queries, contract core.ProjectExecutionContract, checkID, candidateSHA string) error {
	check, err := q.GetClearDevComplexExecutionCheckRun(ctx, checkID)
	if err != nil {
		return err
	}
	spec, err := q.GetClearDevProjectCheckSpec(ctx, check.CheckSpecID)
	if err != nil {
		return err
	}
	if check.Status != "SETTLED" || check.Result.String != "PASS" || !check.ExitCode.Valid || check.ExitCode.Int64 != 0 || !check.TimedOut.Valid || check.TimedOut.Bool ||
		!check.StartedAt.Valid || !check.SettledAt.Valid || check.SettledAt.Time.Before(check.StartedAt.Time) ||
		check.CandidateCommitSha != candidateSHA || spec.CheckKind == string(core.CandidateCheckScope) || spec.ExecutionRunID != contract.ExecutionRunID {
		return complexExecutionRule("project completion requires actual successful checks of its exact candidate")
	}
	exit := int(check.ExitCode.Int64)
	return validateProjectReceiptEvidence(contract, spec, check.ID, candidateSHA, core.SettleComplexExecutionCheckCommand{
		CheckRunID: check.ID, Result: core.EvidenceResult(check.Result.String), ContainerImageID: check.ContainerImageID.String,
		ExitCode: &exit, TimedOut: check.TimedOut.Bool, OutputSummary: check.OutputSummary.String, OutputSHA256: check.OutputSha256.String,
	})
}
