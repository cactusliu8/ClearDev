package cleardev

import (
	"fmt"
)

// ComplexPlanningProjectPlanned is a saved engineering result, not an execution
// admission. No Task Contract validation or Builder grant exists for version 3.
const ComplexPlanningProjectPlanned ComplexPlanningPhase = "PROJECT_PLANNED"

// ParseProjectEngineeringPlanResult uses the selected project's confirmed basis
// while retaining the existing bounded task, dependency and interface protocol.
// The immutable Stage link supplies its repository choice and code version.
func ParseProjectEngineeringPlanResult(raw []byte, requestID, versionID, versionSHA, compilationSHA string, coverage ComplexCoverage, basis ProjectExecutionBasis) (ComplexEngineeringPlanResult, []byte, string, error) {
	if err := ValidateProjectExecutionBasis(basis); err != nil {
		return ComplexEngineeringPlanResult{}, nil, "", err
	}
	plan, data, digest, err := parseComplexEngineeringPlanResult(raw, requestID, versionID, versionSHA, compilationSHA, coverage, basis.CheckCatalog(), ProjectPlanningVersion)
	if err != nil {
		return plan, nil, "", err
	}
	for _, task := range plan.Tasks {
		for _, p := range append(append(append([]string{}, task.WritePaths...), task.GeneratedPaths...), task.SharedPathsRequireApproval...) {
			if !matchesAny(basis.WritePaths, p) {
				return plan, nil, "", fmt.Errorf("task %s path %q exceeds the confirmed project basis", task.Key, p)
			}
		}
	}
	return plan, data, digest, nil
}
