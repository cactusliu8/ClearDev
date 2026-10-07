package cleardev

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// ProjectRuntimeNodeNPMV1 is the supported local single-package Node/npm runtime.
const ProjectRuntimeNodeNPMV1 = "NODE_NPM_V1"

// NodeCheckSmallThreadsV1 fixes executor-owned pools without changing quotas.
const NodeCheckSmallThreadsV1 = "NODE_SMALL_THREADS_V1"

// ProjectRuntime declares local runtime configuration, not a command grant.
// The execution contract freezes the resolved values separately from the
// planning basis so old proposals keep their original JSON and digest.
type ProjectRuntime struct {
	Environment           string            `json:"environment"`
	PrepareArgv           []string          `json:"prepareArgv"`
	EnvironmentVariables  map[string]string `json:"env"`
	HostVariable          string            `json:"hostVariable"`
	PortVariable          string            `json:"portVariable"`
	DataDirectoryVariable string            `json:"dataDirectoryVariable"`
	HealthPath            string            `json:"healthPath"`
	HealthStatus          int               `json:"healthStatus"`
	HealthTimeoutSeconds  int               `json:"healthTimeoutSeconds"`
}

// DefaultProjectRuntimeV1 is a versioned convention for Stage-1 proposals which
// could specify a launch command but had no structured runtime configuration.
// This convention must be shown at admission and sent to the Builder/Reviewers.
func DefaultProjectRuntimeV1() ProjectRuntime {
	return ProjectRuntime{
		Environment: ProjectRuntimeNodeNPMV1, PrepareArgv: []string{}, EnvironmentVariables: map[string]string{},
		HostVariable: "HOST", PortVariable: "PORT", DataDirectoryVariable: "CLEARDEV_DATA_DIR",
		HealthPath: "/", HealthStatus: 200, HealthTimeoutSeconds: 30,
	}
}

// ResolveProjectRuntime validates capability without touching the repository,
// running scripts, or installing packages. Missing runtime resources are checked
// separately by the adapter before dispatch and again for the exact candidate.
func ResolveProjectRuntime(basis ProjectExecutionBasis) (ProjectRuntime, error) {
	if err := ValidateProjectExecutionBasis(basis); err != nil {
		return ProjectRuntime{}, err
	}
	runtime := DefaultProjectRuntimeV1()
	if basis.Runtime != nil {
		encoded, err := json.Marshal(basis.Runtime)
		if err != nil {
			return ProjectRuntime{}, err
		}
		if err := json.Unmarshal(encoded, &runtime); err != nil {
			return ProjectRuntime{}, err
		}
	}
	if runtime.PrepareArgv == nil {
		runtime.PrepareArgv = []string{}
	}
	if runtime.EnvironmentVariables == nil {
		runtime.EnvironmentVariables = map[string]string{}
	}
	if err := ValidateProjectRuntime(runtime); err != nil {
		return ProjectRuntime{}, err
	}
	if basis.Launch.WorkingDirectory != "." {
		return ProjectRuntime{}, fmt.Errorf("%s: this runtime requires a single Node/npm package at the repository root", ReasonProjectRuntime)
	}
	if len(basis.Launch.Argv) == 0 && ProjectTrialNeedsService(basis) {
		return ProjectRuntime{}, fmt.Errorf("%s: a local application startup command is required", ReasonProjectRuntime)
	}
	if err := ValidateProjectNodeCommand(basis.Launch.Argv); len(basis.Launch.Argv) > 0 && err != nil {
		return ProjectRuntime{}, err
	}
	for _, check := range basis.Checks {
		if err := ValidateProjectNodeCommand(check.Argv); err != nil {
			return ProjectRuntime{}, fmt.Errorf("check %s: %w", check.ID, err)
		}
	}
	return runtime, nil
}

// ValidateProjectRuntime bounds the declared environment, managed variables and health probe.
func ValidateProjectRuntime(runtime ProjectRuntime) error {
	if runtime.Environment != ProjectRuntimeNodeNPMV1 {
		return fmt.Errorf("%s: only a local Node/npm environment with offline dependencies is available", ReasonProjectRuntime)
	}
	variables := []string{runtime.HostVariable, runtime.PortVariable, runtime.DataDirectoryVariable}
	for index, name := range variables {
		if !projectEnvironmentName(name) || projectReservedEnvironment(name) || slices.Contains(variables[:index], name) {
			return fmt.Errorf("%s: invalid or conflicting host/port/data environment variables", ReasonProjectRuntime)
		}
	}
	if len(runtime.EnvironmentVariables) > 20 {
		return fmt.Errorf("%s: too many runtime environment variables", ReasonProjectRuntime)
	}
	for name, value := range runtime.EnvironmentVariables {
		if !projectEnvironmentName(name) || projectReservedEnvironment(name) || slices.Contains(variables, name) || len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%s: runtime cannot override control-plane environment %q", ReasonProjectRuntime, name)
		}
	}
	if len(runtime.PrepareArgv) > 0 {
		if err := ValidateProjectNodeCommand(runtime.PrepareArgv); err != nil {
			return fmt.Errorf("runtime preparation: %w", err)
		}
	}
	parsed, err := url.ParseRequestURI(runtime.HealthPath)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(runtime.HealthPath, "/") || strings.HasPrefix(runtime.HealthPath, "//") ||
		strings.ContainsAny(runtime.HealthPath, "\\\x00\r\n#") || len(runtime.HealthPath) > 500 || runtime.HealthStatus < 200 || runtime.HealthStatus > 299 ||
		runtime.HealthTimeoutSeconds < 1 || runtime.HealthTimeoutSeconds > 120 {
		return fmt.Errorf("%s: a bounded loopback HTTP health check is required", ReasonProjectRuntime)
	}
	return nil
}

func projectEnvironmentName(name string) bool {
	if len(name) < 1 || len(name) > 100 || name[0] >= '0' && name[0] <= '9' {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func projectReservedEnvironment(name string) bool {
	upper := strings.ToUpper(name)
	return slices.Contains([]string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "NODE_OPTIONS", "NODE_PATH", "NODE_ENV", "SHELL", "BASH_ENV", "ENV"}, upper) ||
		strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") || strings.HasPrefix(upper, "GIT_") ||
		strings.HasPrefix(upper, "NPM_CONFIG_") || strings.HasPrefix(upper, "AO_")
}

// ProjectRuntimeEnvironment supplies only the declared values and controlled
// loopback/data bindings. Adapters add their minimal runtime PATH/HOME; they must
// never copy the orchestrator's credentials or user data into a check sandbox.
func ProjectRuntimeEnvironment(runtime ProjectRuntime, dataDirectory string, port int, testing bool) []string {
	keys := make([]string, 0, len(runtime.EnvironmentVariables))
	for key := range runtime.EnvironmentVariables {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys)+4)
	for _, key := range keys {
		out = append(out, key+"="+runtime.EnvironmentVariables[key])
	}
	out = append(out, runtime.HostVariable+"=127.0.0.1", runtime.PortVariable+"="+strconv.Itoa(port), runtime.DataDirectoryVariable+"="+dataDirectory)
	if testing {
		out = append(out, "NODE_ENV=test")
	} else {
		out = append(out, "NODE_ENV=production")
	}
	return out
}

// ValidateProjectNodeCommand permits explicit Node/npm argv. Project scripts
// are still untrusted code: only the backend's isolated checker executes them.
func ValidateProjectNodeCommand(argv []string) error {
	unsupported := func() error {
		return fmt.Errorf("%s: use a repository Node script, node -e/--eval <code>, node --test, npm test/start, or npm run <script>; shell, npx and dependency downloads are not supported", ReasonProjectRuntime)
	}
	if len(argv) < 2 || len(argv) > 40 {
		return unsupported()
	}
	// Inline source remains one literal argv element, never a shell command.
	// Node flags after the source are deliberately not part of this contract.
	inline := argv[0] == "node" && (argv[1] == "-e" || argv[1] == "--eval")
	if inline && (len(argv) != 3 || len(argv[2]) > 2000 || strings.TrimSpace(argv[2]) == "" || strings.HasPrefix(argv[2], "-")) {
		return unsupported()
	}
	for i, arg := range argv {
		if arg == "" || strings.ContainsRune(arg, '\x00') {
			return unsupported()
		}
		if (!inline || i != 2) && strings.ContainsAny(arg, "\r\n") {
			return unsupported()
		}
	}
	if inline {
		return nil
	}
	switch argv[0] {
	case "npm":
		switch argv[1] {
		case "test", "start":
			if len(argv) > 2 && argv[2] != "--" {
				return unsupported()
			}
		case "run", "run-script":
			if len(argv) < 3 || !projectScriptName(argv[2]) || (len(argv) > 3 && argv[3] != "--") {
				return unsupported()
			}
		default:
			return unsupported()
		}
	case "node":
		entry := argv[1]
		if entry != "--test" && (!ProjectPath(entry, false) || strings.HasPrefix(entry, "-")) {
			return unsupported()
		}
		if entry == "--test" {
			for _, arg := range argv[2:] {
				if arg == "--experimental-test-coverage" || arg == "--test-reporter=tap" || arg == "--test-reporter=spec" {
					continue
				}
				if !ProjectPath(arg, false) || strings.HasPrefix(arg, "-") {
					return unsupported()
				}
			}
		}
	default:
		return unsupported()
	}
	return nil
}

func projectScriptName(value string) bool {
	if value == "" || len(value) > 100 || strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("-_:./", r) {
			return false
		}
	}
	return true
}
