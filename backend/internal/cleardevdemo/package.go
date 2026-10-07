package cleardevdemo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const buildSourceFileName = "cleardev-build-source.json"

type packagedRuntimeEvidence struct {
	BuildSource       BuildSource
	BuildSourcePath   string
	BuildSourceSHA256 string
	RuntimeSHA256     map[string]string
}

func inspectPackagedRuntime(opts Options) (packagedRuntimeEvidence, error) {
	electronPath, err := packagedElectronBinary(opts)
	if err != nil {
		return packagedRuntimeEvidence{}, err
	}
	packageRoot := filepath.Dir(electronPath)
	resources := filepath.Join(packageRoot, "resources")
	paths := map[string]string{
		"electron":      electronPath,
		"daemon":        filepath.Join(resources, "daemon", executableName("ao")),
		"agent-browser": filepath.Join(resources, "agent-browser", executableName("agent-browser")),
	}
	for name, candidate := range paths {
		info, inspectErr := inspectPackagedRegularFile(packageRoot, name, candidate)
		if inspectErr != nil {
			return packagedRuntimeEvidence{}, inspectErr
		}
		if runtime.GOOS != "windows" {
			if info.Mode().Perm() != 0o755 {
				return packagedRuntimeEvidence{}, fmt.Errorf("packaged %s mode is %03o, want 755", name, info.Mode().Perm())
			}
		}
	}
	buildSourcePath := filepath.Join(resources, buildSourceFileName)
	raw, err := os.ReadFile(buildSourcePath) //nolint:gosec // packaged local build metadata
	if err != nil {
		return packagedRuntimeEvidence{}, fmt.Errorf("packaged build source is missing: %w", err)
	}
	var buildSource BuildSource
	if err := json.Unmarshal(raw, &buildSource); err != nil {
		return packagedRuntimeEvidence{}, fmt.Errorf("parse packaged build source: %w", err)
	}
	if buildSource.SchemaVersion != 1 || !gitSHA1.MatchString(buildSource.CandidateCommit) || !buildSource.SourceClean {
		return packagedRuntimeEvidence{}, fmt.Errorf("packaged build source is not a clean candidate: %+v", buildSource)
	}
	if expected := strings.TrimSpace(os.Getenv("AO_CLEARDEV_EXPECTED_CANDIDATE")); expected != "" && expected != buildSource.CandidateCommit {
		return packagedRuntimeEvidence{}, fmt.Errorf("packaged candidate %s does not match expected %s", buildSource.CandidateCommit, expected)
	}
	runtimeSHA := make(map[string]string, len(paths))
	for name, candidate := range paths {
		hash, hashErr := sha256File(candidate)
		if hashErr != nil {
			return packagedRuntimeEvidence{}, fmt.Errorf("hash packaged %s: %w", name, hashErr)
		}
		runtimeSHA[name] = hash
	}
	buildSourceHash, err := sha256File(buildSourcePath)
	if err != nil {
		return packagedRuntimeEvidence{}, err
	}
	return packagedRuntimeEvidence{
		BuildSource:       buildSource,
		BuildSourcePath:   buildSourcePath,
		BuildSourceSHA256: buildSourceHash,
		RuntimeSHA256:     runtimeSHA,
	}, nil
}

func inspectPackagedRegularFile(packageRoot, name, candidate string) (os.FileInfo, error) {
	info, err := os.Lstat(candidate)
	if err != nil {
		return nil, fmt.Errorf("packaged %s is missing: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("packaged %s is a symbolic link: %s", name, candidate)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("packaged %s is not a regular file: %s", name, candidate)
	}

	realRoot, err := filepath.EvalSymlinks(packageRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve packaged root: %w", err)
	}
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, fmt.Errorf("resolve packaged %s: %w", name, err)
	}
	relative, err := filepath.Rel(realRoot, realCandidate)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("packaged %s resolves outside package root: %s", name, realCandidate)
	}
	return info, nil
}

func executableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}
