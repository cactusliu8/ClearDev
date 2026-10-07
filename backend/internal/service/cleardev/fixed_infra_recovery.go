package cleardev

import (
	"context"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// runFixedInfraRetry owns the actual check invocation. A stored claim is never permission to invoke again.
func (s *Service) runFixedInfraRetry(ctx context.Context, e core.ComplexExecutionSnapshot, run core.ComplexExecutionCheckRun, workspace string, spec core.ComplexExecutionCheckSpecFact) (bool, bool, error) {
	var fixed core.FixedRecoveryEvidence
	for _, f := range e.FixedRecoveries {
		if f.Request.RetryCheckRunID == run.ID {
			fixed = f
		}
	}
	if fixed.Request.ID == "" {
		return false, true, errComplexExecutionStopped
	}
	store, ok := s.complexExecution.(fixedRecoveryStore)
	if !ok {
		return false, true, errors.New("fixed recovery storage unavailable")
	}
	if fixed.Result != nil {
		return false, fixed.Result.Outcome != "PASS", nil
	}
	if run.Status == core.ComplexExecutionCheckRunSettled || run.Status == core.ComplexExecutionCheckRunFailed {
		outcome := "FAILED"
		if run.Result == core.EvidenceResultPass {
			outcome = "PASS"
		}
		err := store.RecordClearDevFixedRecoveryResult(ctx, core.FixedRecoveryResult{RequestID: fixed.Request.ID, Outcome: outcome, ReasonCode: string(run.ReasonCode), CheckRunID: run.ID, RecordedAt: s.now().UTC()})
		return err == nil, outcome != "PASS", err
	}
	var proposal core.ComplexRecoveryAction
	if e.Exception != nil {
		for _, p := range e.Exception.RecoveryActions {
			if p.RetryCheckRunID == run.ID {
				proposal = p
			}
		}
	}
	claim := core.FixedRecoveryClaim{RequestID: fixed.Request.ID, ProposalID: proposal.ID, Action: core.ComplexRecoveryActionRetryInfraCheck, OperationID: fixed.Request.ID + ":operation", ClaimedAt: s.now().UTC()}
	won, err := store.ClaimClearDevFixedRecoveryAction(ctx, claim)
	if err != nil || !won {
		return false, true, err
	}
	if run.Status == core.ComplexExecutionCheckRunPending {
		if _, err := s.complexExecution.StartClearDevComplexExecutionCheckRun(ctx, run.ID, s.now().UTC()); err != nil {
			return false, true, err
		}
	}
	result, runErr := s.runMailIntegration(ctx, e, s.integrationCheckRequest(run.ID, workspace, run.CandidateCommitSHA, spec))
	command := complexSettleCheckCommand(run.ID, result, runErr, s.now().UTC())
	if result.OutputSummary != "" {
		command.OutputSummary = result.OutputSummary
	}
	_, err = s.complexExecution.SettleClearDevComplexExecutionCheckRun(ctx, command)
	return err == nil, false, err
}
