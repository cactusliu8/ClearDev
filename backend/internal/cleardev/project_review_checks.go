package cleardev

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

// ProjectReviewCheckVersion preserves the old four-command mail protocol as
// version 1. Version 2 still accepts identifiers only, once per review, and
// resolves them exclusively from the admitted immutable project basis.
const ProjectReviewCheckVersion = 2

// ReviewCheckRequestsAvailable recognizes only a valid versioned supplemental-check policy.
func ReviewCheckRequestsAvailable(run ComplexExecutionRun) bool {
	_, project, err := ProjectContractFromRun(run)
	if err != nil {
		return false
	}
	if project {
		return true
	}
	_, mail, err := MailPolicyFromRun(run)
	return err == nil && mail
}

// ParseExecutionReviewCheckRequest resolves requested identifiers in the admitted run's catalog.
func ParseExecutionReviewCheckRequest(raw []byte, run ComplexExecutionRun) (ReviewCheckRequestResult, error) {
	contract, project, err := ProjectContractFromRun(run)
	if err != nil {
		return ReviewCheckRequestResult{}, err
	}
	if project {
		return parseReviewCheckRequest(raw, ProjectReviewCheckVersion, func(id string) (ComplexCheckSpec, bool) {
			return ComplexCheckByID(contract.Basis.CheckCatalog(), id)
		})
	}
	if !ReviewCheckRequestsAvailable(run) {
		return ReviewCheckRequestResult{}, errors.New("this execution has no bounded Reviewer check policy")
	}
	return ParseReviewCheckRequest(raw)
}

// ReviewCheckSpec never interprets a project identifier as an old demo alias.
func ReviewCheckSpec(request ReviewCheckRequest, id string) (ComplexCheckSpec, bool) {
	if request.ProjectExecution != nil {
		if ValidateProjectExecutionContract(*request.ProjectExecution) != nil {
			return ComplexCheckSpec{}, false
		}
		return ComplexCheckByID(request.ProjectExecution.Basis.CheckCatalog(), id)
	}
	return ApprovedReviewCheck(id)
}

// ValidateReviewCheckRequestForRun is repeated at request persistence, reads,
// review settlement and final packet validation. A caller-supplied catalog or
// a task-local contract cannot replace the actual immutable execution grant.
func ValidateReviewCheckRequestForRun(request ReviewCheckRequest, run ComplexExecutionRun) error {
	contract, project, err := ProjectContractFromRun(run)
	if err != nil {
		return err
	}
	if project {
		if request.ProjectExecution == nil {
			return errors.New("project Reviewer checks require their original admitted execution")
		}
		replacementIDs := []string{request.ReplacementRecoveryID, request.RequestAttemptID, request.RequestResultID}
		replacementCount := 0
		for _, id := range replacementIDs {
			if id == "" {
				continue
			}
			replacementCount++
			if !validExecutionIdentifier(id) {
				return errors.New("project Reviewer replacement provenance is invalid")
			}
		}
		if replacementCount != 0 && replacementCount != len(replacementIDs) {
			return errors.New("project Reviewer replacement provenance is incomplete")
		}
		expected, err := json.Marshal(contract)
		if err != nil {
			return err
		}
		actual, err := json.Marshal(request.ProjectExecution)
		if err != nil || !bytes.Equal(actual, expected) {
			return errors.New("reviewer check request changed the admitted project contract")
		}
	} else if request.ProjectExecution != nil || !ReviewCheckRequestsAvailable(run) {
		return errors.New("a historical Reviewer request cannot be upgraded to a project request")
	}
	if len(request.CheckIDs) < 1 || len(request.CheckIDs) > 4 {
		return errors.New("reviewer supplemental checks must remain bounded")
	}
	seen := make(map[string]bool, len(request.CheckIDs))
	for _, id := range request.CheckIDs {
		if _, ok := ReviewCheckSpec(request, id); !ok || seen[id] {
			return errors.New("reviewer check is duplicated or outside its approved execution catalog")
		}
		seen[id] = true
	}
	return nil
}

func validateProjectReviewCheckEvidence(request ReviewCheckRequest, result ReviewCheckEvidence) error {
	if result.Outcome == "INFRA_ERROR" {
		if result.ProjectReceipt != nil || result.Proof.Passed || result.Proof.ExitCode != -1 {
			return errors.New("an unexecuted project Reviewer check cannot acquire a runtime receipt")
		}
		return nil
	}
	if result.ProjectReceipt == nil {
		return errors.New("project Reviewer check is missing its exact runtime receipt")
	}
	r := *result.ProjectReceipt
	if err := ValidateProjectCheckReceipt(r, *request.ProjectExecution, ReviewCheckRunID(request.ReviewID, result.CheckID), result.CheckID, request.CandidateSHA); err != nil {
		return err
	}
	p := result.Proof
	if r.Outcome != result.Outcome || r.OutputSummary != result.Output || r.CheckRunID != p.RunID || r.CandidateSHA != p.CandidateSHA ||
		!slices.Equal(r.Argv, p.Argv) || r.ExitCode != p.ExitCode || r.TimedOut != p.TimedOut || r.OutputTruncated != p.Truncated ||
		r.SourceManifestID != p.SourceManifestID || r.SourceRootTreeOID != p.SourceTreeOID || r.ImageID != p.ImageID ||
		r.CheckEnvironmentID != p.EnvironmentID || r.OutputSHA256 != p.OutputSHA256 {
		return errors.New("project Reviewer check summary no longer matches the actual runtime receipt")
	}
	return nil
}
