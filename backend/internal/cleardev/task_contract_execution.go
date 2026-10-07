package cleardev

import (
	"encoding/json"
	"errors"
)

// ComplexExecutionPreparingTasks is deterministic materialization, not Steward approval.
const ComplexExecutionPreparingTasks ComplexExecutionPhase = "PREPARING_TASKS"

// BindPlannerTaskContractPolicy freezes the admission route into the existing
// run hash chain. It cannot be added later to an already dispatched run.
func BindPlannerTaskContractPolicy(raw []byte) ([]byte, string, error) {
	var pkg ComplexExecutionRunPackage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, "", err
	}
	if pkg.SchemaVersion != ComplexExecutionProtocolVersion || !validComplexExecutionMode(pkg.Mode) ||
		!validExecutionIdentifier(pkg.ExecutionRunID) || !validExecutionIdentifier(pkg.RequirementVersionID) ||
		!validProtocolSHA256(pkg.RequirementSHA256) || !validExecutionIdentifier(pkg.PlanID) ||
		!validProtocolSHA256(pkg.PlanSHA256) || pkg.TaskSetVersion != ComplexStandardTaskSetVersion || pkg.ProjectExecution != nil ||
		(pkg.PlanValidationPolicy != "" && pkg.PlanValidationPolicy != PlannerTaskContractPolicy) {
		return nil, "", errors.New("invalid run bindings for Planner Task Contract admission")
	}
	pkg.PlanValidationPolicy = PlannerTaskContractPolicy
	encoded, err := marshalCanonicalJSON(pkg)
	if err != nil {
		return nil, "", err
	}
	return encoded, sha256Hex(encoded), nil
}

// PlannerTaskContractRun recognizes only an exact immutable run binding. An
// empty policy retains the historical Steward-reviewed route, never an
// implicit admission of a V2 plan; storage checks that pairing separately.
func PlannerTaskContractRun(run ComplexExecutionRun) (bool, error) {
	if run.ExecutionPackageJSON == "" {
		return false, nil
	}
	var pkg ComplexExecutionRunPackage
	if err := json.Unmarshal([]byte(run.ExecutionPackageJSON), &pkg); err != nil {
		return false, err
	}
	if pkg.PlanValidationPolicy == ProjectExecutionPolicyV1 || pkg.ProjectExecution != nil {
		_, admitted, err := ProjectContractFromRun(run)
		return admitted, err
	}
	if pkg.PlanValidationPolicy == "" {
		return false, nil
	}
	if pkg.PlanValidationPolicy != PlannerTaskContractPolicy || pkg.SchemaVersion != ComplexExecutionProtocolVersion ||
		pkg.ExecutionRunID != run.ID || pkg.RequirementVersionID != run.RequirementVersionID || pkg.RequirementSHA256 != run.RequirementSHA256 ||
		pkg.PlanID != run.PlanID || pkg.PlanSHA256 != run.PlanSHA256 || pkg.Mode != string(run.Mode) ||
		pkg.TaskSetVersion != run.TaskSetVersion || pkg.TaskSetVersion != ComplexStandardTaskSetVersion ||
		run.PlanReviewID != "" || run.FixedBuilderCount < 1 || run.FixedBuilderCount > 2 ||
		sha256Hex([]byte(run.ExecutionPackageJSON)) != run.ExecutionPackageSHA256 {
		return false, errors.New("planner Task Contract run lost its immutable admission binding")
	}
	return true, nil
}
