package cleardev

import (
	"bytes"
	"errors"
	"slices"
)

func validateProjectExecutionPackage(pkg ComplexStandardExecutionPackage) error {
	if pkg.SchemaVersion != ProjectExecutionProtocolVersion {
		if pkg.ProjectExecution != nil {
			return errors.New("historical execution packages cannot acquire a project contract")
		}
		return nil
	}
	if pkg.ProjectExecution == nil || pkg.Mode != string(WorkModeStandard) {
		return errors.New("project tasks require an explicit STANDARD project contract")
	}
	contract := *pkg.ProjectExecution
	if pkg.RuntimeRevision != nil {
		ext := pkg.RuntimeRevision.HumanRepairExtension
		digest, err := ProjectExecutionContractDigest(contract)
		if err != nil || digest != pkg.RuntimeRevision.ProjectExecutionSHA256 {
			return errors.New("project revision lost its contract or bounded round")
		}
		if pkg.RuntimeRevision.FirstRound < 0 || ext < 0 || ext > 6 || pkg.RuntimeRevision.FirstRound > 6+ext {
			return ErrRepairWindowExceeded
		}
	}
	if err := ValidateProjectExecutionContract(contract); err != nil {
		return err
	}
	if pkg.ExecutionRunID != contract.ExecutionRunID || pkg.RequirementVersionID != contract.RequirementVersionID ||
		pkg.RequirementSHA256 != contract.RequirementSHA256 || pkg.PlanID != contract.PlanID || pkg.PlanSHA256 != contract.PlanSHA256 {
		return errors.New("project task lost the admitted run/plan/specification binding")
	}
	for _, name := range slices.Concat(pkg.WritePaths, pkg.GeneratedPaths, pkg.SharedPathsRequireApproval) {
		if !ProjectPath(name, false) || !matchesAny(contract.Basis.WritePaths, name) {
			return errors.New("project task exceeds the confirmed write scope")
		}
	}
	if !slices.Contains(pkg.ForbiddenPaths, ".git/**") {
		return errors.New("project tasks must protect Git metadata")
	}
	for _, check := range pkg.RequiredChecks {
		expected, ok := ComplexCheckByID(contract.Basis.CheckCatalog(), check.ID)
		if !ok || !slices.Equal(check.Argv, expected.Argv) || check.TimeoutSeconds != expected.TimeoutSeconds {
			return errors.New("project task changed a confirmed check")
		}
	}
	return nil
}

// ProjectTaskMatchesRun checks the nested task contract against its durable run,
// not against the task's self-described catalog. Call at materialization and
// every dispatch/recovery boundary that accepts a task package.
func ProjectTaskMatchesRun(pkg ComplexStandardExecutionPackage, run ComplexExecutionRun) error {
	contract, project, err := ProjectContractFromRun(run)
	if err != nil {
		return err
	}
	if !project {
		if pkg.ProjectExecution != nil || pkg.SchemaVersion == ProjectExecutionProtocolVersion {
			return errors.New("project task has no admitted project run")
		}
		return nil
	}
	if pkg.ProjectExecution == nil || pkg.SchemaVersion != ProjectExecutionProtocolVersion {
		return errors.New("admitted project run requires its exact V4 task")
	}
	expected, err := marshalCanonicalJSON(contract)
	if err != nil {
		return err
	}
	actual, err := marshalCanonicalJSON(pkg.ProjectExecution)
	if err != nil || !bytes.Equal(actual, expected) {
		return errors.New("project task contract differs from the admitted run")
	}
	return validateProjectExecutionPackage(pkg)
}

// ProjectCandidateRules gives generated paths the new project's checked-write
// semantics. Historical generated files still require the legacy frozen command
// proof. Shared paths remain subject to their existing approval mechanism.
func ProjectCandidateRules(pkg ComplexStandardExecutionPackage) PathRules {
	rules := PathRules{
		WritePaths: append([]string(nil), pkg.WritePaths...), GeneratedPaths: append([]string(nil), pkg.GeneratedPaths...),
		SharedPathsRequireApproval: append([]string(nil), pkg.SharedPathsRequireApproval...), ForbiddenPaths: append([]string(nil), pkg.ForbiddenPaths...),
	}
	if pkg.SchemaVersion == ProjectExecutionProtocolVersion && pkg.ProjectExecution != nil {
		rules.WritePaths = append(rules.WritePaths, rules.GeneratedPaths...)
		rules.GeneratedPaths = nil
	}
	return rules
}
