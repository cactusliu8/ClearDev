package cleardev

import (
	"strings"
	"testing"
)

func environmentBasis() ProjectExecutionBasis {
	return ProjectExecutionBasis{
		WritePaths:      []string{"src/**", "package.json", "package-lock.json"},
		DependencyNeeds: []string{"react"},
		Checks:          []ProjectCheckSpec{{ID: "build", Argv: []string{"npm", "run", "build"}, TimeoutSeconds: 60, MainPaths: []string{"src/**", "package.json", "package-lock.json"}}},
		Launch:          ProjectLaunch{Argv: []string{"npm", "start"}, WorkingDirectory: ".", Description: "Serve the app"},
	}
}

func TestProposedEnvironmentConstraints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ProjectExecutionBasis)
		want   string
	}{
		{"npm", func(*ProjectExecutionBasis) {}, ""},
		{"missing-lock", func(b *ProjectExecutionBasis) { b.WritePaths = b.WritePaths[:2] }, "package-lock.json"},
		{"missing-coverage", func(b *ProjectExecutionBasis) { b.Checks[0].MainPaths = b.Checks[0].MainPaths[:2] }, "not covered"},
		{"node-no-dependencies", func(b *ProjectExecutionBasis) {
			b.DependencyNeeds = []string{}
			b.WritePaths = []string{"src/**"}
			b.Checks[0].Argv = []string{"node", "--test", "src/check.js"}
			b.Launch.Argv = []string{"node", "src/main.js"}
		}, ""},
		{"python-not-npm", func(b *ProjectExecutionBasis) {
			r := DefaultProjectRuntimeV1()
			r.Environment = "PYTHON"
			b.Runtime = &r
			b.WritePaths = []string{"src/**"}
		}, "PROJECT_RUNTIME_UNSUPPORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := environmentBasis()
			tc.change(&b)
			err := ValidateProposedProjectEnvironment(b)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %s", err, tc.want)
			}
		})
	}
}

func TestProposedTaskDependencyOwnership(t *testing.T) {
	b := environmentBasis()
	p := ComplexEngineeringPlanResult{Tasks: []ComplexPlanTask{{Key: "setup", WritePaths: []string{"package.json"}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"build"}}}}
	if err := ValidateProposedProjectTaskEnvironments(p, b); err == nil || !strings.Contains(err.Error(), "package-lock.json") {
		t.Fatalf("missing lock accepted: %v", err)
	}
	p.Tasks[0].WritePaths = append(p.Tasks[0].WritePaths, "package-lock.json")
	if err := ValidateProposedProjectTaskEnvironments(p, b); err != nil {
		t.Fatal(err)
	}
	p.Tasks[0].ForbiddenPaths = append(p.Tasks[0].ForbiddenPaths, "package-lock.json")
	if err := ValidateProposedProjectTaskEnvironments(p, b); err == nil {
		t.Fatal("forbidden lock accepted")
	}
	p.Tasks[0].WritePaths = []string{"src/**"}
	if err := ValidateProposedProjectTaskEnvironments(p, b); err != nil {
		t.Fatalf("consumer needs no lock write: %v", err)
	}
}
