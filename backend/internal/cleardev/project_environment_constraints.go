package cleardev

import "fmt"

// ValidateProposedProjectEnvironment checks fresh planning input, not historical
// decoding or already admitted contracts. Environment requirements must be
// satisfiable within the proposed scope before they are handed to a Builder.
func ValidateProposedProjectEnvironment(basis ProjectExecutionBasis) error {
	runtime, err := ResolveProjectRuntime(basis)
	if err != nil {
		return err
	}
	if runtime.Environment != ProjectRuntimeNodeNPMV1 || len(basis.DependencyNeeds) == 0 {
		return nil
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		if !matchesAny(basis.WritePaths, name) {
			return fmt.Errorf("PROJECT_ENVIRONMENT_SCOPE_INCOMPLETE: %s with declared dependencies requires %s in writePaths; correct the proposal before confirmation", runtime.Environment, name)
		}
		covered := false
		for _, check := range basis.Checks {
			covered = covered || matchesAny(check.MainPaths, name)
		}
		if !covered {
			return fmt.Errorf("PROJECT_ENVIRONMENT_SCOPE_INCOMPLETE: %s needs check coverage for %s", runtime.Environment, name)
		}
	}
	return nil
}

// ValidateProposedProjectTaskEnvironments makes dependency-changing tasks own
// both manifest and lockfile. Tasks that only consume dependencies need neither.
func ValidateProposedProjectTaskEnvironments(plan ComplexEngineeringPlanResult, basis ProjectExecutionBasis) error {
	if err := ValidateProposedProjectEnvironment(basis); err != nil {
		return err
	}
	if len(basis.DependencyNeeds) == 0 {
		return nil
	}
	for _, task := range plan.Tasks {
		paths := append(append([]string{}, task.WritePaths...), task.GeneratedPaths...)
		if !matchesAny(paths, "package.json") && !matchesAny(paths, "package-lock.json") {
			continue
		}
		rules := PathRules{WritePaths: paths, ForbiddenPaths: task.ForbiddenPaths}
		for _, name := range []string{"package.json", "package-lock.json"} {
			classification, err := rules.ClassifyPath(name)
			if err != nil || classification != PathAllowed {
				return fmt.Errorf("PROJECT_ENVIRONMENT_SCOPE_INCOMPLETE: task %s changes npm dependencies but cannot write %s; correct the task within the confirmed basis", task.Key, name)
			}
			covered := false
			for _, id := range task.RequiredCheckIDs {
				if check, ok := ComplexCheckByID(basis.CheckCatalog(), id); ok {
					covered = covered || matchesAny(check.MainPaths, name)
				}
			}
			if !covered {
				return fmt.Errorf("PROJECT_ENVIRONMENT_SCOPE_INCOMPLETE: task %s needs a required check covering %s", task.Key, name)
			}
		}
	}
	return nil
}
