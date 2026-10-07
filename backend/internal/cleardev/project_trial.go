package cleardev

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ProjectTrial describes operations, rather than a closed list of product types.
// Nil preserves the historical HTTP trial contract and its canonical digest.
type ProjectTrial struct {
	SchemaVersion int                `json:"schemaVersion"`
	Service       bool               `json:"service"`
	Steps         []ProjectTrialStep `json:"steps"`
}

// ProjectTrialStep freezes the operation and the functional observation expected
// from the reviewer. An observation is not an assertion supplied by the Builder.
type ProjectTrialStep struct {
	OutputFiles        []string `json:"outputFiles,omitempty"`
	ID                 string   `json:"id"`
	Kind               string   `json:"kind"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	Argv               []string `json:"argv,omitempty"`
	TimeoutSeconds     int      `json:"timeoutSeconds,omitempty"`
	ExpectedExitCode   int      `json:"expectedExitCode"`
	Observe            string   `json:"observe"`
}

// ValidateProjectTrial rejects unavailable operations before implementation.
func ValidateProjectTrial(trial *ProjectTrial) error {
	if trial == nil {
		return nil
	}
	fail := func(detail string) error {
		return fmt.Errorf("STAGE_TRIAL_UNSUPPORTED: %s; revise the trial operations before execution", detail)
	}
	if trial.SchemaVersion != 1 || len(trial.Steps) == 0 || len(trial.Steps) > 32 {
		return fail("use version 1 and 1-32 explicit operations")
	}
	seen := map[string]bool{}
	for i, step := range trial.Steps {
		if err := validateResultText(fmt.Sprintf("trial.steps[%d].observe", i), step.Observe, 10000); err != nil {
			return trialResultDiagnostic(err)
		}
		if !productKey(step.ID) || seen[step.ID] || strings.TrimSpace(step.Observe) == "" {
			return fail("each operation needs a unique id and observable functional result")
		}
		seen[step.ID] = true
		if len(step.AcceptanceCriteria) == 0 || len(step.AcceptanceCriteria) > 32 {
			return fail("each operation must name its acceptance criteria")
		}
		ids := map[string]bool{}
		for j, id := range step.AcceptanceCriteria {
			if err := validateResultText(fmt.Sprintf("trial.steps[%d].acceptanceCriteria[%d]", i, j), id, 10000); err != nil {
				return trialResultDiagnostic(err)
			}
			if strings.TrimSpace(id) == "" || ids[id] {
				return fail("invalid acceptance references")
			}
			ids[id] = true
		}
		if len(step.OutputFiles) > 8 {
			return fail("at most eight bounded output files per command")
		}
		for i, p := range step.OutputFiles {
			if !ProjectPath(p, false) || strings.HasSuffix(p, "/**") || slices.Contains(step.OutputFiles[:i], p) {
				return fail("invalid output file path")
			}
		}
		switch step.Kind {
		case "COMMAND":
			if step.TimeoutSeconds < 1 || step.TimeoutSeconds > 3600 || step.ExpectedExitCode < 0 || step.ExpectedExitCode > 255 || step.ExpectedExitCode == 125 {
				return fail("command requires a bounded timeout and expected exit code (125 is reserved for container failures)")
			}
			if err := ValidateProjectNodeCommand(step.Argv); err != nil {
				return fail(err.Error())
			}
		case "HTTP", "BROWSER":
			if !trial.Service {
				return fail("HTTP/browser operations require an explicitly declared service")
			}
			if len(step.OutputFiles) != 0 || len(step.Argv) != 0 || step.TimeoutSeconds != 0 || step.ExpectedExitCode != 0 {
				return fail("service interaction cannot override the launch command")
			}
		default:
			return fail("operation " + step.Kind + " is not supported by this runtime")
		}
	}
	return nil
}

func trialResultDiagnostic(err error) error {
	var diagnostic *AgentResultValidationError
	if errors.As(err, &diagnostic) {
		copied := *diagnostic
		copied.Policy = "STAGE_TRIAL_UNSUPPORTED"
		return &copied
	}
	return fmt.Errorf("STAGE_TRIAL_UNSUPPORTED: %w", err)
}

// ProjectTrialCommand returns the frozen command step with the given ID.
func ProjectTrialCommand(basis ProjectExecutionBasis, id string) (ProjectTrialStep, bool) {
	if basis.Trial != nil {
		for _, step := range basis.Trial.Steps {
			if step.ID == id && step.Kind == "COMMAND" {
				return step, true
			}
		}
	}
	return ProjectTrialStep{}, false
}

// ProjectTrialAllowsCommand matches an operation against the frozen command catalogue.
func ProjectTrialAllowsCommand(basis ProjectExecutionBasis, argv []string, timeoutSeconds int) bool {
	if basis.Trial != nil {
		for _, step := range basis.Trial.Steps {
			if step.Kind == "COMMAND" && slices.Equal(step.Argv, argv) && (timeoutSeconds == 0 || timeoutSeconds == step.TimeoutSeconds) {
				return true
			}
		}
	}
	return false
}

// ProjectTrialNeedsService preserves legacy HTTP semantics for contracts without trial operations.
func ProjectTrialNeedsService(basis ProjectExecutionBasis) bool {
	return basis.Trial == nil || basis.Trial.Service
}

// TrialArtifact captures actual bytes from a declared file inside the disposable
// execution container. Neither a path on the host nor a Builder report qualifies.
type TrialArtifact struct {
	Path   string `json:"path"`
	Base64 string `json:"base64"`
	SHA256 string `json:"sha256"`
}

// ValidateProjectTrialCoverage binds observations to verbatim stage criteria,
// which exist before compilation assigns normalized requirement IDs.
func ValidateProjectTrialCoverage(trial *ProjectTrial, criteria []string) error {
	if trial == nil {
		return nil
	}
	covered := map[string]bool{}
	for _, step := range trial.Steps {
		for _, text := range step.AcceptanceCriteria {
			if !slices.Contains(criteria, text) {
				return fmt.Errorf("STAGE_TRIAL_COVERAGE_INVALID: operation %s must reference an original stage acceptance criterion", step.ID)
			}
			covered[text] = true
		}
	}
	for _, text := range criteria {
		if !covered[text] {
			return fmt.Errorf("STAGE_TRIAL_COVERAGE_MISSING: declare how the reviewer will personally verify %s", text)
		}
	}
	return nil
}
