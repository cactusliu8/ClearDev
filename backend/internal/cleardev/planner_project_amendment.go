package cleardev

import (
	"errors"
	"reflect"
	"slices"
)

// ProjectContractFromRun selects the effective engineering contract while
// validating its immutable admission first. Storage alone supplies revisions.
func ProjectContractFromRun(run ComplexExecutionRun) (ProjectExecutionContract, bool, error) {
	original, project, err := ProjectAdmissionContractFromRun(run)
	if err != nil || !project || run.RuntimeProjectExecution == nil {
		return original, project, err
	}
	revised := *run.RuntimeProjectExecution
	if err := ValidateProjectEngineeringRevision(original, revised); err != nil {
		return original, false, err
	}
	return revised, true, nil
}

// ValidateProjectEngineeringRevision preserves product identity and execution
// authority. The Planner may change executable means, never original outcomes.
func ValidateProjectEngineeringRevision(before, after ProjectExecutionContract) error {
	if err := ValidateProjectExecutionContract(after); err != nil {
		return err
	}
	originalBasis, nextBasis := before.Basis, after.Basis
	lockRepair := projectLockfileScopeRepair(before, after)
	before.Basis, before.Runtime = after.Basis, after.Runtime
	if !reflect.DeepEqual(before, after) || (!slices.Equal(originalBasis.WritePaths, nextBasis.WritePaths) && !lockRepair) {
		return errors.New("engineering revision cannot change admission, product identity or write scope")
	}
	if len(originalBasis.Checks) != len(nextBasis.Checks) {
		return errors.New("engineering revision must retain every original check")
	}
	for i, check := range originalBasis.Checks {
		next := nextBasis.Checks[i]
		coveragePreserved := slices.Equal(check.MainPaths, next.MainPaths) || (lockRepair && slices.Contains(check.MainPaths, "package.json") && appendOnlyLockfile(check.MainPaths, next.MainPaths))
		if check.ID != next.ID || !coveragePreserved || next.TimeoutSeconds != check.TimeoutSeconds || !slices.Equal(check.Argv, next.Argv) {
			return errors.New("engineering revision must retain check identity, coverage and resource ceiling")
		}
	}
	// Existing observations remain verbatim. New preparation/cleanup operations
	// may be appended; a browser assertion cannot become a mock or command.
	if originalBasis.Trial != nil {
		if nextBasis.Trial == nil || originalBasis.Trial.Service != nextBasis.Trial.Service {
			return errors.New("engineering revision cannot remove the original trial")
		}
		for _, old := range originalBasis.Trial.Steps {
			found := false
			for _, next := range nextBasis.Trial.Steps {
				if next.ID != old.ID {
					continue
				}
				found = true
				if next.Kind != old.Kind || next.Observe != old.Observe || !slices.Equal(next.AcceptanceCriteria, old.AcceptanceCriteria) || next.ExpectedExitCode != old.ExpectedExitCode || !slices.Equal(next.OutputFiles, old.OutputFiles) || next.TimeoutSeconds > old.TimeoutSeconds {
					return errors.New("engineering revision cannot weaken original trial observations")
				}
			}
			if !found {
				return errors.New("engineering revision cannot delete an original trial step")
			}
		}
	} else if nextBasis.Trial != nil && !nextBasis.Trial.Service {
		return errors.New("engineering revision cannot remove the original service trial")
	}
	return nil
}

func buildProjectEngineeringAmendment(task ComplexExecutionTask, pkg ComplexStandardExecutionPackage, amendment PlannerRemainingAmendment, eventID, decisionSHA string, humanRepairExtension int, sources ...*FailureCoordinationSource) ([]byte, string, error) {
	unpublished := len(sources) == 1 && sources[0] != nil && sources[0].EventID == eventID && sources[0].ExecutionRunID == task.ExecutionRunID && task.CurrentDispatchID != "" && sources[0].DispatchID == task.CurrentDispatchID && eventID == task.CurrentDispatchID+":planner-coordination"
	if task.Status != DevelopmentTaskStatusPlanned && task.Status != DevelopmentTaskStatusBlocked && task.Status != DevelopmentTaskStatusReview && task.Status != DevelopmentTaskStatusRework && (!unpublished || task.Status != DevelopmentTaskStatusNeedsHuman) {
		return nil, "", errors.New("engineering amendment requires a settled task")
	}
	if task.TaskKey != amendment.TaskKey || eventID == "" || !validProtocolSHA256(decisionSHA) || sha256Hex([]byte(task.ExecutionPackageJSON)) != task.ExecutionPackageSHA256 || pkg.TaskID != task.DevelopmentTaskID || pkg.ExecutionRunID != task.ExecutionRunID {
		return nil, "", errors.New("engineering revision lost its exact task or predecessor")
	}
	criteria, err := normalizeStringList(amendment.AdditionalReviewCriteria, 6)
	if err != nil || len(criteria) == 0 && amendment.ExecutionBasis == nil {
		return nil, "", errors.New("engineering revision requires a concrete change")
	}
	for _, criterion := range criteria {
		if slices.Contains(pkg.ReviewCriteria, criterion) {
			return nil, "", errors.New("engineering revision repeats an existing criterion")
		}
	}
	pkg.ReviewCriteria = append(pkg.ReviewCriteria, criteria...)
	if amendment.ExecutionBasis != nil {
		contract := *pkg.ProjectExecution
		contract.Basis = *amendment.ExecutionBasis
		contract.Runtime, err = ResolveProjectRuntime(contract.Basis)
		if err != nil {
			return nil, "", err
		}
		if err := ValidateProjectEngineeringRevision(*pkg.ProjectExecution, contract); err != nil {
			return nil, "", err
		}
		if reflect.DeepEqual(*pkg.ProjectExecution, contract) && len(criteria) == 0 {
			return nil, "", errors.New("engineering revision has no new work")
		}
		if projectLockfileScopeRepair(*pkg.ProjectExecution, contract) && slices.Contains(pkg.WritePaths, "package.json") && !matchesAny(pkg.WritePaths, "package-lock.json") {
			pkg.WritePaths = append(append([]string(nil), pkg.WritePaths...), "package-lock.json")
		}
		pkg.ProjectExecution = &contract
		for i, check := range pkg.RequiredChecks {
			updated, found := ComplexCheckByID(contract.Basis.CheckCatalog(), check.ID)
			if !found {
				return nil, "", errors.New("engineering revision omitted a required check")
			}
			pkg.RequiredChecks[i].Argv = updated.Argv
			pkg.RequiredChecks[i].TimeoutSeconds = updated.TimeoutSeconds
		}
	}
	pkg.RuntimeRevision = &PlannerTaskRevisionBinding{EventID: eventID, DecisionSHA256: decisionSHA, PreviousPackageSHA256: task.ExecutionPackageSHA256, HumanRepairExtension: humanRepairExtension}
	if task.Status != DevelopmentTaskStatusPlanned {
		pkg.RuntimeRevision.FirstRound = task.ReworkCount + 1
	}
	pkg.RuntimeRevision.ProjectExecutionSHA256, err = ProjectExecutionContractDigest(*pkg.ProjectExecution)
	if err != nil {
		return nil, "", err
	}
	if err := ValidateComplexStandardExecutionPackage(pkg); err != nil {
		return nil, "", err
	}
	raw, err := marshalCanonicalJSON(pkg)
	return raw, sha256Hex(raw), err
}

// The npm lockfile is the only implicit companion of an already permitted
// dependency manifest. This is not a general scope-expansion mechanism.
func projectLockfileScopeRepair(before, after ProjectExecutionContract) bool {
	return before.Runtime.Environment == ProjectRuntimeNodeNPMV1 &&
		after.Runtime.Environment == ProjectRuntimeNodeNPMV1 &&
		len(before.Basis.DependencyNeeds) > 0 &&
		slices.Contains(before.Basis.WritePaths, "package.json") &&
		!matchesAny(before.Basis.WritePaths, "package-lock.json") &&
		appendOnlyLockfile(before.Basis.WritePaths, after.Basis.WritePaths)
}

func appendOnlyLockfile(before, after []string) bool {
	return len(after) == len(before)+1 && after[len(before)] == "package-lock.json" &&
		!slices.Contains(before, "package-lock.json") && slices.Equal(before, after[:len(before)])
}
