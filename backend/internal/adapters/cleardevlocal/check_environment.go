package cleardevlocal

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	checkPreflightStateDirectory = "cleardev-check-preflights"
	checkDependencyDirectory     = "cleardev-check-dependencies"
	checkEnvironmentVersion      = 2
	checkManifestLimit           = 4 * 1024 * 1024
	checkPreparationOutputLimit  = core.ProjectCheckOutputLimit
	checkPreparationTimeout      = 2 * time.Minute
)

var npmInstallArgv = []string{"npm", "ci", "--offline", "--ignore-scripts", "--no-audit", "--no-fund"}

type checkPreflightState struct {
	Version            int                            `json:"version"`
	Fingerprint        string                         `json:"fingerprint"`
	State              string                         `json:"state"`
	Environment        ports.ClearDevCheckEnvironment `json:"environment"`
	SourceManifest     candidateSourceManifest        `json:"sourceManifest"`
	DependencyCacheKey string                         `json:"dependencyCacheKey,omitempty"`
	Error              string                         `json:"error,omitempty"`
}

type checkPreflightFingerprint struct {
	ProjectExecution *core.ProjectExecutionContract `json:"projectExecution,omitempty"`
	CandidateSHA     string
	Image            string
	Argv             [][]string
	Timeout          time.Duration
	MemoryBytes      int64
	PidsLimit        int64
}

type preparedCheckEnvironment struct {
	public              ports.ClearDevCheckEnvironment
	sourceManifest      candidateSourceManifest
	dependencyCacheKey  string
	dependencyDirectory string
	dependencyReference string
}

type dependencyEnvironmentManifest struct {
	Version                 int      `json:"version"`
	CacheKey                string   `json:"cacheKey"`
	Image                   string   `json:"image"`
	ImageID                 string   `json:"imageId"`
	NodeVersion             string   `json:"nodeVersion"`
	NPMVersion              string   `json:"npmVersion"`
	PackageJSONSHA256       string   `json:"packageJsonSha256"`
	PackageLockSHA256       string   `json:"packageLockSha256"`
	InstallArgv             []string `json:"installArgv"`
	InstallScriptsDisabled  bool     `json:"installScriptsDisabled"`
	NetworkDisabled         bool     `json:"networkDisabled"`
	DependencyTreeSHA256    string   `json:"dependencyTreeSha256"`
	DependencyEnvironmentID string   `json:"dependencyEnvironmentId"`
	Bytes                   int64    `json:"bytes"`
	Items                   int64    `json:"items"`
}

type dependencyCacheKeyInput struct {
	Version            int
	Image              string
	ImageID            string
	NodeVersion        string
	NPMVersion         string
	PackageJSONSHA     string
	PackageLockSHA     string
	InstallArgv        []string
	ScriptsDisabled    bool
	NetworkDisabled    bool
	IsolationVersion   int
	DependencyMaxBytes int64
	DependencyMaxItems int64
}

// PrepareCandidateChecks proves the immutable image and any exact offline npm
// dependency environment before a Builder receives work. A non-empty RunID is
// durable and fail-closed in the same way as a formal check action.
func (r *Runner) PrepareCandidateChecks(ctx context.Context, request ports.ClearDevCheckPreflightRequest) (ports.ClearDevCheckEnvironment, error) {
	normalized, err := normalizeCheckPreflightRequest(request)
	if err != nil {
		return ports.ClearDevCheckEnvironment{}, err
	}
	if normalized.RunID == "" {
		prepared, prepareErr := r.prepareCheckEnvironment(ctx, normalized)
		if prepared.dependencyReference != "" {
			manager, managerErr := openDependencyCacheManager(context.WithoutCancel(ctx))
			if managerErr == nil {
				managerErr = manager.releaseReference(context.WithoutCancel(ctx), prepared.dependencyReference)
			}
			if prepareErr == nil && managerErr != nil {
				prepareErr = managerErr
			}
		}
		return prepared.public, prepareErr
	}

	fingerprint, err := checkPreflightRequestSHA256(normalized)
	if err != nil {
		return ports.ClearDevCheckEnvironment{}, fmt.Errorf("encode ClearDev check preflight: %w", err)
	}
	statePath, err := durableRunStatePath(checkPreflightStateDirectory, normalized.RunID, "check preflight")
	if err != nil {
		return ports.ClearDevCheckEnvironment{}, err
	}
	started := checkPreflightState{Version: checkEnvironmentVersion, Fingerprint: fingerprint, State: "started"}
	created, payload, err := createOrReadJSONState(statePath, "ClearDev check preflight RunID", started)
	if err != nil {
		return ports.ClearDevCheckEnvironment{}, err
	}
	if !created {
		var state checkPreflightState
		if err := json.Unmarshal(payload, &state); err != nil {
			return ports.ClearDevCheckEnvironment{}, fmt.Errorf("decode ClearDev check preflight RunID: %w", err)
		}
		if state.Version != checkEnvironmentVersion || state.Fingerprint != fingerprint {
			return ports.ClearDevCheckEnvironment{}, errors.New("ClearDev check preflight RunID is already bound to a different request")
		}
		if state.State == "started" {
			return ports.ClearDevCheckEnvironment{}, errors.New("ClearDev check preflight RunID has an unsettled external action")
		}
		if state.State != "settled" {
			return ports.ClearDevCheckEnvironment{}, errors.New("ClearDev check preflight RunID has an invalid state")
		}
		if state.Error != "" {
			return state.Environment, errors.New(state.Error)
		}
		if err := verifySavedCandidateManifest(state.SourceManifest, state.Environment); err != nil {
			return state.Environment, err
		}
		if err := verifyStoredCheckEnvironment(state.Environment, state.DependencyCacheKey); err != nil {
			return state.Environment, err
		}
		if state.DependencyCacheKey != "" {
			manager, err := openDependencyCacheManager(ctx)
			if err != nil {
				return state.Environment, err
			}
			if err := manager.ensureReference(ctx, normalized.RunID, state.DependencyCacheKey); err != nil {
				return state.Environment, err
			}
		}
		return state.Environment, nil
	}

	prepared, prepareErr := r.prepareCheckEnvironment(ctx, normalized)
	settled := checkPreflightState{
		Version: checkEnvironmentVersion, Fingerprint: fingerprint, State: "settled",
		Environment: prepared.public, SourceManifest: prepared.sourceManifest, DependencyCacheKey: prepared.dependencyCacheKey,
	}
	if prepareErr != nil {
		settled.Error = prepareErr.Error()
	}
	if err := writeJSONState(statePath, "ClearDev check preflight RunID", settled); err != nil {
		if prepared.dependencyReference != "" {
			if manager, managerErr := openDependencyCacheManager(context.WithoutCancel(ctx)); managerErr == nil {
				_ = manager.releaseReference(context.WithoutCancel(ctx), prepared.dependencyReference)
			}
		}
		return ports.ClearDevCheckEnvironment{}, fmt.Errorf("settle ClearDev check preflight RunID: %w", err)
	}
	return prepared.public, prepareErr
}

func normalizeCheckPreflightRequest(request ports.ClearDevCheckPreflightRequest) (ports.ClearDevCheckPreflightRequest, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.WorkspacePath = filepath.Clean(strings.TrimSpace(request.WorkspacePath))
	request.CandidateSHA = strings.TrimSpace(request.CandidateSHA)
	request.Image = strings.TrimSpace(request.Image)
	if request.RunID != "" && !validRunID(request.RunID) {
		return ports.ClearDevCheckPreflightRequest{}, errors.New("invalid ClearDev check preflight RunID")
	}
	if request.WorkspacePath == "." || !validCommit(request.CandidateSHA) || request.Image == "" || len(request.Argv) == 0 || len(request.Argv) > 64 {
		return ports.ClearDevCheckPreflightRequest{}, errors.New("invalid ClearDev check preflight request")
	}
	contract, err := normalizedProjectCheckContract(request.ProjectExecution, request.Argv, 0)
	if err != nil {
		return ports.ClearDevCheckPreflightRequest{}, err
	}
	if contract != nil && request.Image != core.StandardCandidateCheckImage {
		return ports.ClearDevCheckPreflightRequest{}, errors.New("project checks require the pinned checker image")
	}
	request.ProjectExecution = contract
	request.Argv = cloneCheckArgv(request.Argv)
	for _, argv := range request.Argv {
		if len(argv) == 0 || len(argv) > 64 || strings.TrimSpace(argv[0]) == "" {
			return ports.ClearDevCheckPreflightRequest{}, errors.New("invalid ClearDev check preflight argv")
		}
		for _, value := range argv {
			if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
				return ports.ClearDevCheckPreflightRequest{}, errors.New("invalid ClearDev check preflight argv")
			}
		}
	}
	if request.Timeout <= 0 {
		request.Timeout = checkPreparationTimeout
	}
	if request.MemoryBytes <= 0 {
		request.MemoryBytes = 512 * 1024 * 1024
	}
	if request.PidsLimit <= 0 {
		request.PidsLimit = 64
	}
	return request, nil
}

func cloneCheckArgv(values [][]string) [][]string {
	cloned := make([][]string, len(values))
	for index := range values {
		cloned[index] = append([]string(nil), values[index]...)
	}
	return cloned
}

func checkPreflightRequestSHA256(request ports.ClearDevCheckPreflightRequest) (string, error) {
	payload, err := json.Marshal(checkPreflightFingerprint{
		ProjectExecution: request.ProjectExecution,
		CandidateSHA:     request.CandidateSHA, Image: request.Image, Argv: request.Argv,
		Timeout: request.Timeout, MemoryBytes: request.MemoryBytes, PidsLimit: request.PidsLimit,
	})
	if err != nil {
		return "", err
	}
	return sha256Hex(payload), nil
}

func (r *Runner) prepareCheckEnvironment(ctx context.Context, request ports.ClearDevCheckPreflightRequest) (preparedCheckEnvironment, error) {
	prepared := preparedCheckEnvironment{public: ports.ClearDevCheckEnvironment{CandidateSHA: request.CandidateSHA, Image: request.Image}}
	actionID := request.RunID
	if actionID == "" {
		actionID = "source-inspection:" + request.CandidateSHA + ":" + newOpaqueCheckID()
	}
	sourceTemporary, err := acquireCheckTemporary(ctx, actionID, "source-inspection", checkSourceReservationBytes, request.RunID)
	if err != nil {
		return prepared, err
	}
	scratch := filepath.Join(sourceTemporary.Root(), "git")
	facts, err := inspectCandidateSource(ctx, request.WorkspacePath, request.CandidateSHA, scratch, defaultCandidateSourceLimits())
	if err != nil {
		_ = sourceTemporary.Release(context.WithoutCancel(ctx))
		return prepared, fmt.Errorf("inspect exact candidate source: %w", err)
	}
	prepared.sourceManifest = facts.Manifest
	prepared.public.SourceManifestID = facts.ManifestID
	prepared.public.SourceRootTreeOID = facts.Manifest.RootTreeOID
	prepared.public.SourceBytes = facts.Manifest.BlobBytes
	prepared.public.SourceItems = int64(facts.Manifest.ItemCount)
	approvedArgv, err := json.Marshal(request.Argv)
	if err != nil {
		_ = sourceTemporary.Release(context.WithoutCancel(ctx))
		return prepared, fmt.Errorf("encode approved check argv: %w", err)
	}
	prepared.public.ApprovedArgvSHA256 = sha256Hex(approvedArgv)
	prepared.public.ProjectExecutionSHA256, err = projectCheckContractDigest(request.ProjectExecution)
	if err != nil {
		_ = sourceTemporary.Release(context.WithoutCancel(ctx))
		return prepared, err
	}
	requiresNPM := checkArgvRequiresNPM(request.Argv)
	if request.ProjectExecution != nil {
		// node --test may import declared packages too. Never depend on the
		// Builder's node_modules or silently omit a candidate lockfile.
		requiresNPM = requiresNPM || candidateManifestHasRootEntry(facts.Manifest, "package.json") || candidateManifestHasRootEntry(facts.Manifest, "package-lock.json")
	}
	if candidateManifestHasRootEntry(facts.Manifest, "node_modules") || (request.ProjectExecution == nil && requiresNPM && candidateManifestHasRootEntry(facts.Manifest, "dist")) {
		_ = sourceTemporary.Release(context.WithoutCancel(ctx))
		return prepared, errors.New("candidate source contains a reserved top-level check path")
	}
	var packageJSON, packageLock []byte
	if requiresNPM {
		packageJSON, err = readCandidateSourceRootFile(ctx, request.WorkspacePath, scratch, facts.Manifest, "package.json", checkManifestLimit)
		if err != nil {
			_ = sourceTemporary.Release(context.WithoutCancel(ctx))
			return prepared, fmt.Errorf("read candidate package.json: %w", err)
		}
		if !json.Valid(packageJSON) {
			_ = sourceTemporary.Release(context.WithoutCancel(ctx))
			return prepared, errors.New("candidate npm manifest is not valid JSON")
		}
		prepared.public.PackageJSONSHA256 = sha256Hex(packageJSON)
		if candidateManifestHasRootEntry(facts.Manifest, "package-lock.json") {
			packageLock, err = readCandidateSourceRootFile(ctx, request.WorkspacePath, scratch, facts.Manifest, "package-lock.json", checkManifestLimit)
			if err != nil {
				_ = sourceTemporary.Release(context.WithoutCancel(ctx))
				return prepared, fmt.Errorf("read candidate package-lock.json: %w", err)
			}
			if !json.Valid(packageLock) {
				_ = sourceTemporary.Release(context.WithoutCancel(ctx))
				return prepared, errors.New("candidate package-lock.json is not valid JSON")
			}
			prepared.public.PackageLockSHA256 = sha256Hex(packageLock)
		} else if request.ProjectExecution == nil || packageDeclaresDependencies(packageJSON) {
			_ = sourceTemporary.Release(context.WithoutCancel(ctx))
			return prepared, errors.New("candidate with npm dependencies requires package-lock.json")
		}
	}
	if err := sourceTemporary.Release(context.WithoutCancel(ctx)); err != nil {
		return prepared, fmt.Errorf("clean candidate source inspection: %w", err)
	}

	if request.ProjectExecution != nil && requiresNPM {
		if _, err := inspectCheckImage(ctx, projectInstallImage); err != nil {
			return prepared, fmt.Errorf("pinned project installation toolchain unavailable: %w", err)
		}
	}
	effectiveImage := request.Image
	if request.ProjectExecution != nil {
		effectiveImage = projectInstallImage
		request.MemoryBytes = core.ProjectCheckMemoryBytes
		request.PidsLimit = core.ProjectCheckPidsLimit
		prepared.public.Image = effectiveImage
	}
	imageID, err := inspectCheckImage(ctx, effectiveImage)
	if err != nil {
		return prepared, err
	}
	prepared.public.ImageID = imageID
	executables := uniqueCheckExecutables(request.Argv)
	if requiresNPM && !checkArgvRequiresNPM(request.Argv) {
		executables = append(executables, "npm")
	}
	for _, executable := range executables {
		version, probeErr := probeCheckExecutable(ctx, request, imageID, executable)
		if probeErr != nil {
			return prepared, probeErr
		}
		switch executable {
		case "node":
			prepared.public.NodeVersion = version
		case "npm":
			prepared.public.NPMVersion = version
		}
	}
	if len(packageLock) != 0 && request.ProjectExecution == nil {
		binding, directory, referenceID, prepareErr := prepareNPMDependencies(ctx, request, imageID, prepared.public.NodeVersion, prepared.public.NPMVersion, packageJSON, packageLock)
		if prepareErr != nil {
			return prepared, prepareErr
		}
		prepared.dependencyCacheKey = binding.CacheKey
		prepared.dependencyDirectory = directory
		prepared.dependencyReference = referenceID
		prepared.public.DependencyCacheKey = binding.CacheKey
		prepared.public.DependencyEnvironmentID = binding.DependencyEnvironmentID
		prepared.public.DependencyTreeSHA256 = binding.DependencyTreeSHA256
		prepared.public.DependencyBytes = binding.Bytes
		prepared.public.DependencyItems = binding.Items
	}
	policy := ""
	if request.ProjectExecution != nil {
		policy = core.ProjectCheckPolicyV2 + ":" + projectInstallImage + ":memory=" + strconv.FormatInt(core.ProjectCheckMemoryBytes, 10) + ":pids=" + strconv.FormatInt(core.ProjectCheckPidsLimit, 10) + ":output=" + strconv.Itoa(core.ProjectCheckOutputLimit) + ":source=" + strconv.FormatInt(checkSourceBytesLimit, 10) + ":dependency=" + strconv.FormatInt(checkDependencyBytesLimit, 10)
	}
	environmentPayload, err := json.Marshal(struct {
		Version                 int
		Environment             ports.ClearDevCheckEnvironment
		ProjectDependencyPolicy string `json:"projectDependencyPolicy,omitempty"`
	}{Version: checkEnvironmentVersion, Environment: prepared.public, ProjectDependencyPolicy: policy})
	if err != nil {
		return prepared, fmt.Errorf("encode check environment binding: %w", err)
	}
	prepared.public.CheckEnvironmentID = sha256Hex(environmentPayload)
	return prepared, nil
}

func verifySavedCandidateManifest(manifest candidateSourceManifest, environment ports.ClearDevCheckEnvironment) error {
	_, manifestID, err := canonicalCandidateSourceManifest(manifest)
	if err != nil {
		return fmt.Errorf("verify saved candidate source manifest: %w", err)
	}
	if manifest.CandidateSHA != environment.CandidateSHA || manifest.RootTreeOID != environment.SourceRootTreeOID || int64(manifest.ItemCount) != environment.SourceItems || manifest.BlobBytes != environment.SourceBytes || manifestID != environment.SourceManifestID {
		return errors.New("saved candidate source manifest does not match the check environment")
	}
	for name, digest := range map[string]string{"package.json": environment.PackageJSONSHA256, "package-lock.json": environment.PackageLockSHA256} {
		if digest == "" {
			continue
		}
		matched := false
		for _, entry := range manifest.Entries {
			if entry.Path == name && entry.ContentSHA256 == digest {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("saved candidate %s does not match the check environment", name)
		}
	}
	return nil
}

func checkArgvRequiresNPM(argv [][]string) bool {
	for _, command := range argv {
		if command[0] == "npm" {
			return true
		}
	}
	return false
}

// A lockfile is optional only for a project with no packages or workspaces to
// resolve. The candidate package.json is still bound to the source manifest.
func packageDeclaresDependencies(packageJSON []byte) bool {
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(packageJSON, &manifest); err != nil || manifest == nil {
		return true
	}
	for _, field := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies", "bundledDependencies", "bundleDependencies", "workspaces"} {
		value := bytes.TrimSpace(manifest[field])
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			continue
		}
		switch value[0] {
		case '{':
			var entries map[string]json.RawMessage
			if json.Unmarshal(value, &entries) == nil && len(entries) == 0 {
				continue
			}
		case '[':
			var entries []json.RawMessage
			if json.Unmarshal(value, &entries) == nil && len(entries) == 0 {
				continue
			}
		}
		return true
	}
	return false
}

func uniqueCheckExecutables(argv [][]string) []string {
	seen := map[string]bool{"node": true}
	values := []string{"node"}
	for _, command := range argv {
		if !seen[command[0]] {
			seen[command[0]] = true
			values = append(values, command[0])
		}
	}
	sort.Strings(values)
	return values
}

func inspectCheckImage(ctx context.Context, image string) (string, error) {
	output, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).CombinedOutput() //nolint:gosec // image is a frozen execution-package value.
	if err != nil {
		return "", fmt.Errorf("inspect check image: %w: %s", err, strings.TrimSpace(string(output)))
	}
	imageID := strings.TrimSpace(string(output))
	if imageID == "" {
		return "", errors.New("docker returned an empty image id")
	}
	return imageID, nil
}

func probeCheckExecutable(ctx context.Context, request ports.ClearDevCheckPreflightRequest, imageID, executable string) (string, error) {
	if executable != "node" && executable != "npm" {
		return "", fmt.Errorf("approved check executable %q has no trusted preflight", executable)
	}
	temporary, err := acquireCheckTemporary(ctx, "executable-probe:"+sha256Hex([]byte(imageID+"\x00"+executable))+":"+newOpaqueCheckID(), "executable-probe", checkProbeReservationBytes, request.RunID)
	if err != nil {
		return "", err
	}
	defer func() { _ = temporary.Release(context.Background()) }()
	limit := strconv.FormatInt(request.MemoryBytes, 10)
	args := []string{
		"run", "--rm", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--stop-timeout=1",
		"--cidfile=" + temporary.CIDFile(),
		"--memory=" + limit, "--memory-swap=" + limit,
		"--pids-limit=" + strconv.FormatInt(request.PidsLimit, 10),
		"--user=65532:65532", "--env=HOME=/tmp",
		"--tmpfs=/tmp:rw,noexec,nosuid,size=16777216",
		imageID, executable, "--version",
	}
	probeCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	command := exec.CommandContext(probeCtx, "docker", args...) //nolint:gosec // fixed Docker flags and direct approved executable argv.
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("preflight check executable %s: %w: %s", executable, err, strings.TrimSpace(stderr.String()))
	}
	if err := temporary.Release(context.WithoutCancel(ctx)); err != nil {
		return "", fmt.Errorf("clean executable probe container: %w", err)
	}
	version := strings.TrimSpace(string(output))
	if version == "" || len(version) > 256 {
		return "", fmt.Errorf("preflight check executable %s returned an invalid version", executable)
	}
	return version, nil
}

func prepareNPMDependencies(ctx context.Context, request ports.ClearDevCheckPreflightRequest, imageID, nodeVersion, npmVersion string, packageJSON, packageLock []byte) (dependencyEnvironmentManifest, string, string, error) {
	if request.ProjectExecution != nil {
		timeout, err := dependencyPreparationTimeout()
		if err != nil {
			return dependencyEnvironmentManifest{}, "", "", err
		}
		request.Timeout = timeout
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	packageJSONSHA, packageLockSHA := sha256Hex(packageJSON), sha256Hex(packageLock)
	cacheKey, err := npmDependencyCacheKey(request.Image, imageID, nodeVersion, npmVersion, packageJSONSHA, packageLockSHA)
	if err != nil {
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("encode npm dependency binding: %w", err)
	}
	manager, err := openDependencyCacheManager(ctx)
	if err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	referenceID := request.RunID
	if referenceID == "" {
		referenceID = "ephemeral-dependency:" + newOpaqueCheckID()
	}
	reservation, err := manager.reserve(ctx, cacheKey, referenceID)
	if err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	if reservation.Hit {
		binding, verifyErr := verifyDependencyEnvironment(reservation.Directory, cacheKey)
		return binding, reservation.Directory, referenceID, verifyErr
	}
	published := false
	defer func() {
		if !published {
			_ = manager.abort(context.WithoutCancel(ctx), reservation.ReservationID)
			_ = manager.releaseReference(context.WithoutCancel(ctx), referenceID)
		}
	}()

	staging := reservation.Directory
	npmCache, err := dependencyPreparationNPMCache(staging, request.ProjectExecution != nil)
	if err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	defer func() { _ = os.RemoveAll(filepath.Join(staging, "empty-npm-cache")) }()
	if err := os.WriteFile(filepath.Join(staging, "package.json"), packageJSON, 0o600); err != nil {
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("stage package.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "package-lock.json"), packageLock, 0o600); err != nil {
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("stage package-lock.json: %w", err)
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		if err := os.Chmod(filepath.Join(staging, name), 0o444); err != nil { //nolint:gosec // the sealed manifests must be readable by the non-root preparation container.
			return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("seal staged dependency manifest %s: %w", name, err)
		}
	}
	// The cache root remains mode 0700. This directory is made readable only
	// so the non-networked preparation container can consume its two sealed
	// manifest files through a read-only bind mount.
	if err := os.Chmod(staging, 0o711); err != nil { //nolint:gosec // only traversal to the two known read-only manifests is exposed.
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("expose sealed dependency manifests to preparation container: %w", err)
	}

	temporary, err := acquireCheckTemporary(ctx, "dependency-preparation:"+cacheKey, "dependency-preparation", checkPrepareReservationBytes, request.RunID)
	if err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	cleanupTemporary := true
	defer func() {
		if cleanupTemporary {
			_ = temporary.Release(context.Background())
		}
	}()
	containerID, err := startDependencyPreparationContainer(ctx, request, imageID, staging, npmCache, temporary.CIDFile())
	if err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	if prepareErr := runDependencyPreparation(ctx, request, containerID, staging); prepareErr != nil {
		if request.ProjectExecution == nil || !strings.Contains(prepareErr.Error(), "ENOTCACHED") {
			return dependencyEnvironmentManifest{}, "", "", prepareErr
		}
		if err := prepareMissingProjectNPMDependencies(ctx, request, containerID, staging, packageLock); err != nil {
			return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("project npm dependency preparation failed: %w", err)
		}
	}
	if err := os.RemoveAll(filepath.Join(staging, "empty-npm-cache")); err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	if err := os.Chmod(staging, 0o700); err != nil { //nolint:gosec // staging is a private directory, not a data file.
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("close dependency preparation staging directory: %w", err)
	}
	nodeModules := filepath.Join(staging, "node_modules")
	if err := os.Mkdir(nodeModules, 0o700); err != nil {
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("create bounded dependency extraction root: %w", err)
	}
	limits := productionCheckCapacityLimits()
	reservedManifestBytes := int64(64 * 1024)
	extractLimit := checkUsageLimit{
		Bytes: limits.Dependency.Bytes - int64(len(packageJSON)) - int64(len(packageLock)) - reservedManifestBytes,
		Items: limits.Dependency.Items - 4,
	}
	if extractLimit.Bytes < 0 || extractLimit.Items < 0 {
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("%w: dependency manifests consume the fixed environment allowance", errCheckCapacityExceeded)
	}
	counter, err := newCheckUsageCounter(extractLimit)
	if err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	if err := exportDependencyTree(ctx, containerID, nodeModules, counter); err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	if err := temporary.Release(context.WithoutCancel(ctx)); err != nil {
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("clean dependency preparation container: %w", err)
	}
	cleanupTemporary = false
	if err := sealDependencyTree(nodeModules); err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	treeSHA, err := dependencyTreeSHA256(nodeModules)
	if err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	environmentID := sha256Hex([]byte(cacheKey + "\x00" + treeSHA))
	binding := dependencyEnvironmentManifest{
		Version: checkEnvironmentVersion, CacheKey: cacheKey, Image: request.Image, ImageID: imageID,
		NodeVersion: nodeVersion, NPMVersion: npmVersion, PackageJSONSHA256: packageJSONSHA,
		PackageLockSHA256: packageLockSHA, InstallArgv: append([]string(nil), npmInstallArgv...),
		InstallScriptsDisabled: true, NetworkDisabled: true, DependencyTreeSHA256: treeSHA,
		DependencyEnvironmentID: environmentID,
	}
	if err := writeDependencyEnvironmentManifest(staging, &binding); err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	if err := os.Chmod(filepath.Join(staging, "package.json"), 0o444); err != nil { //nolint:gosec // published manifests are immutable and must be readable by the checker.
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("seal prepared package.json: %w", err)
	}
	if err := os.Chmod(filepath.Join(staging, "package-lock.json"), 0o444); err != nil { //nolint:gosec // published manifests are immutable and must be readable by the checker.
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("seal prepared package-lock.json: %w", err)
	}
	if err := os.Chmod(staging, 0o555); err != nil { //nolint:gosec // the published dependency environment is a read-only bind source.
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("seal prepared npm environment: %w", err)
	}
	if _, err := verifyDependencyEnvironment(staging, cacheKey); err != nil {
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("verify new npm environment before publish: %w", err)
	}
	usage, err := measureCheckTree(staging, limits.Dependency)
	if err != nil || usage.Bytes != binding.Bytes || usage.Items != binding.Items {
		return dependencyEnvironmentManifest{}, "", "", errors.New("prepared npm environment capacity facts changed before publication")
	}
	if err := manager.publish(ctx, reservation.ReservationID, usage); err != nil {
		return dependencyEnvironmentManifest{}, "", "", err
	}
	published = true
	directory := filepath.Join(manager.root, cacheKey)
	verified, err := verifyDependencyEnvironment(directory, cacheKey)
	if err != nil {
		return dependencyEnvironmentManifest{}, "", "", fmt.Errorf("verify published npm environment: %w", err)
	}
	return verified, directory, referenceID, nil
}

func startDependencyPreparationContainer(ctx context.Context, request ports.ClearDevCheckPreflightRequest, imageID, staging, npmCache, cidFile string) (string, error) {
	limit := strconv.FormatInt(request.MemoryBytes, 10)
	dependencyLimit := productionCheckCapacityLimits().Dependency
	args := []string{
		"run", "--rm", "--detach", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--stop-timeout=1",
		"--cidfile=" + cidFile, "--memory=" + limit, "--memory-swap=" + limit,
		"--pids-limit=" + strconv.FormatInt(request.PidsLimit, 10), "--user=65532:65532",
		"--env=HOME=/prepare/.home", "--env=TMPDIR=/prepare/.tmp", "--env=NPM_CONFIG_CACHE=/npm-cache", "--env=NPM_CONFIG_OFFLINE=true",
		"--env=NPM_CONFIG_USERCONFIG=/prepare/.npm-userconfig", "--env=NPM_CONFIG_GLOBALCONFIG=/prepare/.npm-globalconfig", "--env=NPM_CONFIG_UPDATE_NOTIFIER=false",
		"--tmpfs=/prepare:rw,nosuid,nodev,mode=1777,size=" + strconv.FormatInt(dependencyLimit.Bytes, 10) + ",nr_inodes=" + strconv.FormatInt(dependencyLimit.Items+1, 10),
		"--mount=type=bind,src=" + staging + ",dst=/input,readonly", "--mount=type=bind,src=" + npmCache + ",dst=/npm-cache,readonly",
		imageID, "sleep", "infinity",
	}
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput() //nolint:gosec // fixed preparation isolation and exact trusted paths.
	if err != nil {
		return "", fmt.Errorf("start offline npm preparation container: %w: %s", err, strings.TrimSpace(string(output)))
	}
	containerID := strings.TrimSpace(string(output))
	if !validDockerID(containerID) {
		raw, readErr := os.ReadFile(cidFile)
		containerID = strings.TrimSpace(string(raw))
		if readErr != nil || !validDockerID(containerID) {
			return "", errors.New("docker returned no valid dependency preparation container id")
		}
	}
	return containerID, nil
}

func runDependencyPreparation(ctx context.Context, request ports.ClearDevCheckPreflightRequest, containerID, staging string) error {
	return runDependencyPreparationUsingCache(ctx, request, containerID, staging, "")
}

func runDependencyPreparationUsingCache(ctx context.Context, request ports.ClearDevCheckPreflightRequest, containerID, staging, cache string) error {
	prepareCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	for _, name := range []string{"package.json", "package-lock.json"} {
		if cache != "" {
			// The initial attempt already copied these immutable manifests.
			// With all capabilities dropped, even root cannot overwrite them.
			break
		}
		if _, err := os.Lstat(filepath.Join(staging, name)); err != nil {
			return fmt.Errorf("inspect staged dependency manifest %s: %w", name, err)
		}
		source := "/input/" + name
		destination := "/prepare/" + name
		if output, err := exec.CommandContext(prepareCtx, "docker", "exec", "--user=0:0", containerID, "cp", source, destination).CombinedOutput(); err != nil { //nolint:gosec // fixed direct preparation argv.
			return fmt.Errorf("copy trusted dependency manifest into preparation tmpfs: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	argv := make([]string, 0, 4+len(npmInstallArgv))
	argv = append(argv, "exec", "--user=65532:65532", "--workdir=/prepare", containerID)
	argv = append(argv, npmInstallArgv...)
	if cache != "" {
		argv = append(argv, "--cache="+cache)
	}
	collector := newOutputCollector(checkPreparationOutputLimit, cancel)
	command := exec.CommandContext(prepareCtx, "docker", argv...) //nolint:gosec // fixed offline npm argv without a shell.
	command.Stdout, command.Stderr = collector, collector
	if err := command.Run(); err != nil {
		if collector.LimitExceeded() {
			return errors.New("offline npm dependency preparation exceeded its output limit")
		}
		if errors.Is(prepareCtx.Err(), context.DeadlineExceeded) {
			return errors.New("offline npm dependency preparation timed out")
		}
		full, probeErr := checkContainerCapacityAt(context.WithoutCancel(ctx), containerID, "/prepare")
		if probeErr == nil && full {
			return fmt.Errorf("%w: offline npm dependency preparation filled its fixed filesystem", errCheckCapacityExceeded)
		}
		return fmt.Errorf("offline npm dependency preparation failed: %w: %s", err, collector.String())
	}
	// A successful npm ci for a zero-dependency lockfile need not create this
	// directory. Export an actual empty tree, only after npm has succeeded.
	if output, err := exec.CommandContext(prepareCtx, "docker", "exec", "--user=65532:65532", containerID, "mkdir", "-p", "/prepare/node_modules").CombinedOutput(); err != nil { //nolint:gosec // fixed directory in the non-networked preparation container.
		return fmt.Errorf("prepare dependency export directory: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func exportDependencyTree(ctx context.Context, containerID, destination string, counter *checkUsageCounter) error {
	return exportContainerDependencyTree(ctx, containerID, "/prepare/node_modules", destination, counter)
}

func exportContainerDependencyTree(ctx context.Context, containerID, source, destination string, counter *checkUsageCounter) error {
	if !validDockerID(containerID) || (source != "/prepare/node_modules" && source != "/workspace/node_modules") {
		return errors.New("invalid container dependency export source")
	}
	// docker cp cannot read a running container's tmpfs mount. Stream a fixed
	// tar command from the isolated container instead.
	command := exec.CommandContext(ctx, "docker", "exec", containerID, "tar", "-C", source, "-cf", "-", ".") //nolint:gosec // validated container id and fixed source path.
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open dependency archive stream: %w", err)
	}
	collector := newOutputCollector(checkPreparationOutputLimit, func() {})
	command.Stderr = collector
	if err := command.Start(); err != nil {
		return fmt.Errorf("start dependency archive export: %w", err)
	}
	extractErr := extractCheckTar(tar.NewReader(stdout), destination, counter)
	if extractErr != nil && command.Process != nil {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if extractErr != nil {
		return fmt.Errorf("extract bounded dependency archive: %w", extractErr)
	}
	if waitErr != nil {
		return fmt.Errorf("export dependency archive: %w: %s", waitErr, collector.String())
	}
	return nil
}

func writeDependencyEnvironmentManifest(directory string, binding *dependencyEnvironmentManifest) error {
	path := filepath.Join(directory, "environment.json")
	for attempt := 0; attempt < 4; attempt++ {
		encoded, err := json.Marshal(binding)
		if err != nil {
			return fmt.Errorf("encode prepared npm environment: %w", err)
		}
		if len(encoded) >= 64*1024 {
			return errors.New("prepared npm environment record is too large")
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			return fmt.Errorf("write prepared npm environment: %w", err)
		}
		usage, err := measureCheckTree(directory, productionCheckCapacityLimits().Dependency)
		if err != nil {
			return err
		}
		if binding.Bytes == usage.Bytes && binding.Items == usage.Items {
			return os.Chmod(path, 0o444) //nolint:gosec // the stable manifest is immutable and checker-readable.
		}
		binding.Bytes, binding.Items = usage.Bytes, usage.Items
	}
	return errors.New("prepared npm environment record did not reach a stable capacity encoding")
}

func npmDependencyCacheKey(image, imageID, nodeVersion, npmVersion, packageJSONSHA, packageLockSHA string) (string, error) {
	limits := productionCheckCapacityLimits()
	payload, err := json.Marshal(dependencyCacheKeyInput{
		Version: checkEnvironmentVersion, Image: image, ImageID: imageID,
		NodeVersion: nodeVersion, NPMVersion: npmVersion,
		PackageJSONSHA: packageJSONSHA, PackageLockSHA: packageLockSHA,
		InstallArgv: npmInstallArgv, ScriptsDisabled: true, NetworkDisabled: true,
		IsolationVersion:   checkEnvironmentVersion,
		DependencyMaxBytes: limits.Dependency.Bytes, DependencyMaxItems: limits.Dependency.Items,
	})
	if err != nil {
		return "", err
	}
	return sha256Hex(payload), nil
}

func checkDependencyRoot() (string, error) {
	dataDir := strings.TrimSpace(os.Getenv("AO_DATA_DIR"))
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve ClearDev dependency data directory: %w", err)
		}
		dataDir = filepath.Join(home, ".ao")
	}
	root := filepath.Join(filepath.Clean(dataDir), checkDependencyDirectory)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create ClearDev dependency data directory: %w", err)
	}
	return root, nil
}

// TrustedNPMCachePath resolves the cache from the trusted process environment,
// never from a candidate. Launchers that isolate HOME must call it before changing
// HOME or the working directory, then pass the result as CLEARDEV_NPM_CACHE_DIR.
func TrustedNPMCachePath() (string, error) {
	return trustedNPMCachePath()
}

func trustedNPMCachePath() (string, error) {
	cache := strings.TrimSpace(os.Getenv("CLEARDEV_NPM_CACHE_DIR"))
	if cache == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve trusted npm cache: %w", err)
		}
		cache = filepath.Join(home, ".npm")
	}
	abs, err := filepath.Abs(cache)
	if err != nil {
		return "", fmt.Errorf("resolve trusted npm cache path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve trusted npm cache target: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("trusted npm cache directory is unavailable")
	}
	return resolved, nil
}

func verifyStoredCheckEnvironment(environment ports.ClearDevCheckEnvironment, cacheKey string) error {
	if environment.CheckEnvironmentID == "" || environment.ImageID == "" || environment.CandidateSHA == "" || environment.SourceManifestID == "" || environment.SourceRootTreeOID == "" || environment.SourceBytes < 0 || environment.SourceItems < 0 {
		return errors.New("saved ClearDev check environment is incomplete")
	}
	environmentWithoutID := environment
	environmentWithoutID.CheckEnvironmentID = ""
	payload, err := json.Marshal(struct {
		Version     int
		Environment ports.ClearDevCheckEnvironment
	}{Version: checkEnvironmentVersion, Environment: environmentWithoutID})
	if err != nil || sha256Hex(payload) != environment.CheckEnvironmentID {
		return errors.New("saved ClearDev check environment identity does not match")
	}
	if environment.DependencyEnvironmentID == "" {
		noDependencyProject := environment.ProjectExecutionSHA256 != "" && validCandidateSourceSHA256(environment.PackageJSONSHA256) && environment.PackageLockSHA256 == "" && environment.NPMVersion != ""
		if cacheKey != "" || environment.DependencyCacheKey != "" || environment.DependencyTreeSHA256 != "" || environment.PackageLockSHA256 != "" || (!noDependencyProject && environment.PackageJSONSHA256 != "") || environment.DependencyBytes != 0 || environment.DependencyItems != 0 {
			return errors.New("saved ClearDev dependency binding is inconsistent")
		}
		return nil
	}
	root, err := checkDependencyRoot()
	if err != nil {
		return err
	}
	binding, err := verifyDependencyEnvironment(filepath.Join(root, cacheKey), cacheKey)
	if err != nil {
		return err
	}
	if cacheKey != environment.DependencyCacheKey || binding.DependencyEnvironmentID != environment.DependencyEnvironmentID || binding.DependencyTreeSHA256 != environment.DependencyTreeSHA256 || binding.PackageJSONSHA256 != environment.PackageJSONSHA256 || binding.PackageLockSHA256 != environment.PackageLockSHA256 || binding.Image != environment.Image || binding.ImageID != environment.ImageID || binding.NodeVersion != environment.NodeVersion || binding.NPMVersion != environment.NPMVersion || binding.Bytes != environment.DependencyBytes || binding.Items != environment.DependencyItems {
		return errors.New("saved ClearDev check and dependency environments do not match")
	}
	return nil
}

func verifyDependencyEnvironment(directory, cacheKey string) (dependencyEnvironmentManifest, error) {
	if err := verifyPreparedPath(directory, true, 0o555); err != nil {
		return dependencyEnvironmentManifest{}, err
	}
	payload, err := os.ReadFile(filepath.Join(directory, "environment.json"))
	if err != nil {
		return dependencyEnvironmentManifest{}, fmt.Errorf("read prepared npm environment: %w", err)
	}
	var binding dependencyEnvironmentManifest
	if err := json.Unmarshal(payload, &binding); err != nil {
		return dependencyEnvironmentManifest{}, fmt.Errorf("decode prepared npm environment: %w", err)
	}
	canonical, err := json.Marshal(binding)
	if err != nil || !bytes.Equal(payload, canonical) {
		return dependencyEnvironmentManifest{}, errors.New("prepared npm environment record is damaged")
	}
	recomputedCacheKey, err := npmDependencyCacheKey(binding.Image, binding.ImageID, binding.NodeVersion, binding.NPMVersion, binding.PackageJSONSHA256, binding.PackageLockSHA256)
	if err != nil {
		return dependencyEnvironmentManifest{}, fmt.Errorf("verify prepared npm dependency binding: %w", err)
	}
	if binding.Version != checkEnvironmentVersion || binding.CacheKey != cacheKey || binding.CacheKey != recomputedCacheKey || binding.Image == "" || binding.ImageID == "" || binding.NodeVersion == "" || binding.NPMVersion == "" || !binding.InstallScriptsDisabled || !binding.NetworkDisabled || strings.Join(binding.InstallArgv, "\x00") != strings.Join(npmInstallArgv, "\x00") {
		return dependencyEnvironmentManifest{}, errors.New("prepared npm environment binding is invalid")
	}
	if err := verifyPreparedPath(filepath.Join(directory, "environment.json"), false, 0o444); err != nil {
		return dependencyEnvironmentManifest{}, err
	}
	if err := verifyPreparedPath(filepath.Join(directory, "package.json"), false, 0o444); err != nil {
		return dependencyEnvironmentManifest{}, err
	}
	if err := verifyPreparedPath(filepath.Join(directory, "package-lock.json"), false, 0o444); err != nil {
		return dependencyEnvironmentManifest{}, err
	}
	if err := verifyPreparedPath(filepath.Join(directory, "node_modules"), true, 0o555); err != nil {
		return dependencyEnvironmentManifest{}, err
	}
	packageJSON, err := os.ReadFile(filepath.Join(directory, "package.json"))
	if err != nil || sha256Hex(packageJSON) != binding.PackageJSONSHA256 {
		return dependencyEnvironmentManifest{}, errors.New("prepared npm package.json is missing or damaged")
	}
	packageLock, err := os.ReadFile(filepath.Join(directory, "package-lock.json"))
	if err != nil || sha256Hex(packageLock) != binding.PackageLockSHA256 {
		return dependencyEnvironmentManifest{}, errors.New("prepared npm package-lock.json is missing or damaged")
	}
	treeSHA, err := dependencyTreeSHA256(filepath.Join(directory, "node_modules"))
	if err != nil {
		return dependencyEnvironmentManifest{}, err
	}
	if treeSHA != binding.DependencyTreeSHA256 || sha256Hex([]byte(cacheKey+"\x00"+treeSHA)) != binding.DependencyEnvironmentID {
		return dependencyEnvironmentManifest{}, errors.New("prepared npm dependency tree is damaged")
	}
	usage, err := measureCheckTree(directory, productionCheckCapacityLimits().Dependency)
	if err != nil || usage.Bytes != binding.Bytes || usage.Items != binding.Items {
		return dependencyEnvironmentManifest{}, errors.New("prepared npm dependency environment capacity facts are damaged")
	}
	return binding, nil
}

func verifyPreparedPath(path string, directory bool, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm() != mode {
		return errors.New("prepared npm environment path is missing or damaged")
	}
	return nil
}

func sealDependencyTree(root string) error {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open prepared npm dependency root: %w", err)
	}
	defer func() { _ = rootFS.Close() }()
	return fs.WalkDir(rootFS.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := rootFS.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return validateDependencySymlink(rootFS, path)
		}
		if info.IsDir() {
			return rootFS.Chmod(path, 0o555) //nolint:gosec // every published dependency directory is intentionally immutable and checker-readable.
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("prepared npm dependency contains unsupported file %q", path)
		}
		mode := os.FileMode(0o444)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o555
		}
		return rootFS.Chmod(path, mode) //nolint:gosec // files are sealed read-only while preserving the executable bit.
	})
}

func validateDependencySymlink(root *os.Root, path string) error {
	target, err := root.Open(path)
	if err != nil {
		return fmt.Errorf("resolve prepared npm dependency symlink within its root: %w", err)
	}
	if err := target.Close(); err != nil {
		return fmt.Errorf("close prepared npm dependency symlink target: %w", err)
	}
	return nil
}

func dependencyTreeSHA256(root string) (string, error) {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return "", errors.New("prepared npm node_modules is missing")
	}
	defer func() { _ = rootFS.Close() }()
	digest := sha256.New()
	err = fs.WalkDir(rootFS.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		relative := path
		info, err := rootFS.Lstat(path)
		if err != nil {
			return err
		}
		kind := byte('f')
		switch {
		case info.IsDir():
			kind = 'd'
		case info.Mode()&os.ModeSymlink != 0:
			kind = 'l'
			if err := validateDependencySymlink(rootFS, path); err != nil {
				return err
			}
		case !info.Mode().IsRegular():
			return fmt.Errorf("prepared npm dependency contains unsupported file %q", relative)
		}
		_, _ = fmt.Fprintf(digest, "%c\x00%s\x00%04o\x00", kind, filepath.ToSlash(relative), info.Mode().Perm())
		if kind == 'l' {
			target, err := rootFS.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(digest, target)
			_, _ = io.WriteString(digest, "\x00")
			return nil
		}
		if kind == 'f' {
			file, err := rootFS.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(digest, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			_, _ = io.WriteString(digest, "\x00")
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash prepared npm dependency tree: %w", err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func cleanupPreparedTree(root string) {
	if root == "" {
		return
	}
	if rootFS, err := os.OpenRoot(root); err == nil {
		_ = fs.WalkDir(rootFS.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info, statErr := rootFS.Lstat(path); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				_ = rootFS.Chmod(path, 0o700) //nolint:gosec // private directories need owner write access for best-effort removal.
			}
			return nil
		})
		_ = rootFS.Close()
	}
	_ = os.RemoveAll(root)
}

func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest)
}
