package cleardev

import (
	"context"
	"encoding/json"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (s *Service) mailRequirementBaseline(ctx context.Context, requirement core.DevelopmentRequirement) (string, bool, error) {
	if _, bound, err := s.benchmarkBindingForRequirement(ctx, requirement); err != nil || bound {
		return "", false, err
	}
	checker, ok := s.checks.(ports.ClearDevMailProjectIdentifier)
	if !ok {
		return "", false, errors.New("MAIL_POLICY_CHECKER_UNAVAILABLE")
	}
	project, found, err := s.ao.GetProject(ctx, requirement.AOProjectID)
	if err != nil || !found {
		return "", false, errors.New("MAIL_POLICY_PROJECT_UNAVAILABLE")
	}
	var identity ports.ClearDevBaselineResult
	if selector, supported := s.checks.(ports.ClearDevMailBaselineSelector); supported {
		identity, err = selector.IdentifyMailProjectAtBranch(ctx, project.Path, project.Config.DefaultBranch)
	} else {
		identity, err = checker.IdentifyMailProject(ctx, project.Path)
		// Non-mail projects retain their existing branch contract. Only a mail
		// checker which cannot inspect the selected branch must fail closed.
		if err == nil && identity.Required && project.Config.DefaultBranch != "" && project.Config.DefaultBranch != "auto" {
			return "", false, errors.New("MAIL_SELECTED_BASELINE_UNAVAILABLE")
		}
	}
	if err != nil {
		return "", false, err
	}
	if identity.Required && !validComplexExecutionCommitSHA(identity.CandidateSHA) {
		return "", false, errors.New("MAIL_POLICY_BASELINE_INVALID")
	}
	if stage, linked, stageErr := s.productStageSource(ctx, requirement.ID); stageErr != nil {
		return "", false, stageErr
	} else if linked && (!identity.Required || identity.CandidateSHA != stage.BaseCommitSHA) {
		return "", false, errors.New("PRODUCT_STAGE_BASELINE_CHANGED: restore the explicitly selected stage baseline before continuing")
	}
	return identity.CandidateSHA, identity.Required, nil
}

func (s *Service) mailScopeSummary(ctx context.Context, execution core.ComplexExecutionSnapshot, dispatch core.ComplexExecutionDispatch, workspace string) (string, bool, error) {
	policy, base, required, err := core.MailDeliveryPolicyFromRun(execution.Run)
	if err != nil || !required {
		return "", required, err
	}
	// V1 has only one task and historically proves scope from the requirement
	// baseline. V2 task scope is local to the dispatch base so a dependent task
	// does not re-own changes that were already composed by an earlier batch.
	if policy == core.MailDeliveryPolicyV2 {
		base = dispatch.BaseCommitSHA
	}
	checker, ok := s.checks.(ports.ClearDevMailDeliveryChecker)
	if !ok {
		return "", true, errors.New("MAIL_DELIVERY_CHECKER_UNAVAILABLE")
	}
	proof, err := checker.CheckMailCandidateScope(ctx, ports.ClearDevDeliveryRequest{RunID: dispatch.ID, DeliveryPolicy: policy, WorkspacePath: workspace, BaseSHA: base, CandidateSHA: dispatch.CandidateCommitSHA})
	if err != nil {
		return "", true, err
	}
	if err := core.ValidateMailScopeProofForPolicy(proof, policy, base, dispatch.CandidateCommitSHA); err != nil {
		return "", true, err
	}
	raw, err := json.Marshal(proof)
	return string(raw), true, err
}

func mailRolePrompt(run core.ComplexExecutionRun, prompt string) string {
	policy, _, required, err := core.MailDeliveryPolicyFromRun(run)
	if err != nil || !required {
		return prompt
	}
	if policy == core.MailDeliveryPolicyV2 {
		return prompt + "\n" + core.MailDeliveryInstructionsV2
	}
	return prompt + "\n" + core.MailDeliveryInstructions
}

func (s *Service) verifyMailRunBaseline(ctx context.Context, run core.ComplexExecutionRun, baseline ports.ClearDevBaselineResult, workspace string) error {
	base, required, err := core.MailPolicyFromRun(run)
	if err != nil {
		return err
	}
	if required && (!baseline.Required || baseline.CandidateSHA != base) {
		return &baselineGateError{reason: "BASELINE_WORKSPACE_CHANGED", err: errors.New("mail execution policy is bound to another initial SHA")}
	}
	if !required && baseline.Required {
		identifier, ok := s.checks.(ports.ClearDevMailProjectIdentifier)
		if !ok {
			return errors.New("MAIL_POLICY_CHECKER_UNAVAILABLE")
		}
		identity, err := identifier.IdentifyMailProject(ctx, workspace)
		if err != nil {
			return err
		}
		if identity.Required {
			return &baselineGateError{reason: "MAIL_EXECUTION_POLICY_REQUIRED", err: errors.New("legacy mail execution has no frozen delivery policy; start a new requirement")}
		}
	}
	return nil
}

func validMailRunnerResult(result ports.ClearDevCheckResult, sha string) bool {
	return result.CandidateSHA == sha && result.Image == core.StandardCandidateCheckImage && result.ImageID != "" && result.SourceManifestID != "" && result.SourceRootTreeOID != "" && result.CheckEnvironmentID != "" && result.OutputSHA256 != "" &&
		(result.Outcome != ports.ClearDevCheckPass || result.ExitCode == 0 && !result.TimedOut && !result.OutputTruncated)
}

func (s *Service) runMailIntegration(ctx context.Context, execution core.ComplexExecutionSnapshot, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	_, project, projectErr := core.ProjectContractFromRun(execution.Run)
	if projectErr != nil {
		return ports.ClearDevCheckResult{}, projectErr
	}
	if project {
		checkID, err := projectIntegrationCheckID(execution, request.RunID)
		if err != nil {
			return ports.ClearDevCheckResult{}, err
		}
		return s.runExecutionCandidateCheck(ctx, execution.Run, request, checkID)
	}
	policy, base, required, err := core.MailDeliveryPolicyFromRun(execution.Run)
	if err != nil {
		return ports.ClearDevCheckResult{}, err
	}
	if !required {
		if s.benchmarkManifest != nil {
			requirement, found, readErr := s.facts.GetClearDevRequirement(ctx, execution.Run.DevelopmentRequirementID)
			if readErr != nil || !found {
				return ports.ClearDevCheckResult{}, errors.New("benchmark requirement unavailable")
			}
			_, bound, bindErr := s.benchmarkBindingForRequirement(ctx, requirement.Requirement)
			if bindErr != nil {
				return ports.ClearDevCheckResult{}, bindErr
			}
			if bound {
				return s.checks.RunCandidateCheck(ctx, request)
			}
		}
		identifier, ok := s.checks.(ports.ClearDevMailProjectIdentifier)
		if !ok {
			return ports.ClearDevCheckResult{}, errors.New("MAIL_POLICY_CHECKER_UNAVAILABLE")
		}
		identity, identityErr := identifier.IdentifyMailProject(ctx, request.WorkspacePath)
		if identityErr != nil {
			return ports.ClearDevCheckResult{}, identityErr
		}
		if identity.Required {
			return ports.ClearDevCheckResult{}, errors.New("MAIL_EXECUTION_POLICY_REQUIRED")
		}
		return s.checks.RunCandidateCheck(ctx, request)
	}
	checker, ok := s.checks.(ports.ClearDevMailDeliveryChecker)
	if !ok {
		return ports.ClearDevCheckResult{}, errors.New("MAIL_DELIVERY_CHECKER_UNAVAILABLE")
	}
	result, err := checker.RunMailDeliveryCheck(ctx, ports.ClearDevDeliveryRequest{RunID: request.RunID, DeliveryPolicy: policy, WorkspacePath: request.WorkspacePath, BaseSHA: base, CandidateSHA: request.CandidateSHA})
	if err == nil && result.Outcome == ports.ClearDevCheckPass {
		if !validMailRunnerResult(result, request.CandidateSHA) {
			return result, errors.New("MAIL_DELIVERY_CHECK_BINDING_INVALID")
		}
		err = core.ValidateMailDeliveryProofForPolicy(result.OutputSummary, policy, base, request.CandidateSHA, request.RunID, result.ImageID)
	}
	return result, err
}
