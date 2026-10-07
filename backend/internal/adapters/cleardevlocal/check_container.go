package cleardevlocal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// The source and the disposable output share one tmpfs.  The source is
// accounted for by the trusted materializer; the output allowance is fixed
// here and cannot be widened by a caller.
const (
	// CheckContainerMaxSourceBytes is the hard candidate source byte limit.
	CheckContainerMaxSourceBytes = checkSourceBytesLimit
	// CheckContainerMaxSourceItems is the hard candidate source item limit.
	CheckContainerMaxSourceItems = checkSourceItemsLimit
	// CheckContainerMaxOutputBytes is the fixed writable check-area byte limit.
	CheckContainerMaxOutputBytes = checkOutputBytesLimit
	// CheckContainerMaxOutputItems is the fixed writable check-area item limit.
	CheckContainerMaxOutputItems = checkOutputItemsLimit

	checkContainerMaxMemoryBytes   = core.ProjectCheckMemoryBytes
	checkContainerMaxPids          = int64(1024)
	checkContainerMaxOutputSummary = int64(64 * 1024 * 1024)
	checkContainerProbeTimeout     = 10 * time.Second
	checkContainerProbeInterval    = 100 * time.Millisecond
)

// CheckContainerRequest describes one trusted candidate check. SourceDir is
// a materialized, immutable directory; SourceBytes and SourceItems are the
// manifest totals and deliberately exclude SourceDir itself. DependencyDir,
// when present, is immutable. Project checks copy it into writable node_modules.
type CheckContainerRequest struct {
	ExecutionProfile string
	OutputFiles      []string
	Image            string
	Argv             []string
	SourceDir        string
	SourceBytes      int64
	SourceItems      int64
	DependencyDir    string
	ProjectRuntime   *core.ProjectRuntime
	// DependencyExport is used only by delivered-project preparation. It is an
	// empty backend-owned directory, never a bind mount or shared cache path.
	DependencyExport      string
	DownloadSeed          string
	DependenciesInstalled bool
	FreshInstall          bool
	NPMArchives           string
	PreparationDeadline   time.Time
	BeforeExecute         func() error
	BuildFromSource       bool
	Offline               bool
	// CIDFile optionally names a caller-owned cidfile. It is retained while a
	// container may exist and removed only after cleanup is confirmed. An empty
	// value uses a private temporary cidfile.
	CIDFile     string
	Timeout     time.Duration
	MemoryBytes int64
	PidsLimit   int64
	OutputLimit int
}

// CheckContainerOutcome is the stable classification of a container run.
type CheckContainerOutcome string

const (
	// CheckContainerPass means the candidate command exited zero.
	CheckContainerPass CheckContainerOutcome = "PASS"
	// CheckContainerFail means the candidate command exited non-zero.
	CheckContainerFail CheckContainerOutcome = "FAIL"
	// CheckContainerTimedOut means the candidate exceeded its deadline.
	CheckContainerTimedOut CheckContainerOutcome = "TIMED_OUT"
	// CheckContainerInfraError means setup, Docker, or capacity failed.
	CheckContainerInfraError CheckContainerOutcome = "INFRA_ERROR"
)

// CheckContainerResult contains facts needed by the Runner to classify a
// check. CapacityFull is set only from a fixed filesystem probe, never from
// candidate output text. InfraError is set for Docker/setup/capacity errors;
// a regular non-zero candidate exit is CheckContainerFail.
type CheckContainerResult struct {
	CommandExecuted bool
	Artifacts       []core.TrialArtifact
	Outcome         CheckContainerOutcome
	ExitCode        int
	Output          string
	OutputSHA256    string
	TimedOut        bool
	OutputCapped    bool
	CapacityFull    bool
	InfraError      bool
}

// CheckContainerRunArgs exposes the exact safe container argv for tests and
// for the Runner integration. It never binds a host-writable workspace.
func CheckContainerRunArgs(request CheckContainerRequest, cidFile string) ([]string, error) {
	normalized, err := normalizeCheckContainerRequest(request)
	if err != nil {
		return nil, err
	}
	if normalized.CIDFile != "" {
		if cidFile == "" {
			cidFile = normalized.CIDFile
		}
		if cidFile != normalized.CIDFile {
			return nil, errors.New("cidfile does not match the request")
		}
	}
	if !filepath.IsAbs(cidFile) {
		return nil, errors.New("cidfile must be an absolute path")
	}
	return checkContainerRunArgs(normalized, cidFile), nil
}

// CheckContainerExecArgs returns a direct-argv docker exec invocation. The
// candidate command is appended unchanged and is never interpreted by a
// shell.
func CheckContainerExecArgs(containerID string, argv []string) ([]string, error) {
	if !validDockerID(containerID) || !validCheckContainerArgv(argv) {
		return nil, errors.New("invalid check container id or argv")
	}
	return append([]string{"exec", "--user=65532:65532", "--workdir=/workspace", containerID}, argv...), nil
}

// RunCheckContainer creates a long-lived restricted container, imports the
// trusted source with docker cp, executes the approved argv, and always
// removes the container. The only writable workspace is the container tmpfs;
// no host-writable workspace bind is used.
func RunCheckContainer(ctx context.Context, request CheckContainerRequest) (result CheckContainerResult, retErr error) {
	request, err := normalizeCheckContainerRequest(request)
	if err != nil {
		return infraCheckContainerResult(), err
	}
	if err := validateCheckMaterializedSource(request.SourceDir, request.SourceBytes, request.SourceItems); err != nil {
		return infraCheckContainerResult(), err
	}
	if request.DependencyDir != "" {
		if err := validateCheckDependencyDirectory(request.DependencyDir); err != nil {
			return infraCheckContainerResult(), err
		}
	}

	if request.DownloadSeed != "" {
		if request.ProjectRuntime == nil || !filepath.IsAbs(request.DownloadSeed) {
			return infraCheckContainerResult(), errors.New("invalid private download seed")
		}
		if err := validateCheckDependencyDirectory(request.DownloadSeed); err != nil {
			return infraCheckContainerResult(), err
		}
	}
	cidPath, _, err := prepareCheckContainerCIDFile(request.CIDFile)
	if err != nil {
		return infraCheckContainerResult(), err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), CheckCleanupTimeout)
		defer cancel()
		cleanupErr := removeCheckContainerAndConfirm(cleanupCtx, cidPath)
		if cleanupErr == nil {
			if removeErr := os.Remove(cidPath); removeErr != nil && !os.IsNotExist(removeErr) {
				cleanupErr = fmt.Errorf("remove settled check container cidfile: %w", removeErr)
			}
		}
		if cleanupErr != nil {
			result.Outcome = CheckContainerInfraError
			result.InfraError = true
			result.ExitCode = -1
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()

	request.CIDFile = cidPath
	containerID, startErr := startCheckContainer(ctx, request, cidPath)
	if startErr != nil {
		// Docker may have created the container before its client was canceled.
		// The same deferred identity-preserving cleanup applies to this path.
		return infraCheckContainerResult(), startErr
	}

	if request.FreshInstall {
		if _, err := retainCheckVolumes(ctx, cidPath, containerID); err != nil {
			return infraCheckContainerResult(), err
		}
		if out, err := exec.CommandContext(ctx, "docker", "exec", "--user=0:0", containerID, "chmod", "1777", "/workspace", "/install-cache").CombinedOutput(); err != nil {
			return infraCheckContainerResult(), fmt.Errorf("prepare private install volumes: %w: %s", err, out)
		}
	}
	if err := copyCheckSource(ctx, request.SourceDir, containerID); err != nil {
		full, probeErr := checkContainerCapacityForRequest(ctx, containerID, request)
		if full {
			result = infraCheckContainerResult()
			result.CapacityFull = true
			return result, fmt.Errorf("copy trusted check source filled the check filesystem: %w", err)
		}
		if probeErr != nil {
			return infraCheckContainerResult(), errors.Join(
				fmt.Errorf("copy trusted check source: %w", err),
				fmt.Errorf("probe check filesystem capacity: %w", probeErr),
			)
		}
		return infraCheckContainerResult(), fmt.Errorf("copy trusted check source: %w", err)
	}

	installationOutput := ""
	if request.FreshInstall {
		var err error
		installationOutput, err = installProjectInCheckContainer(ctx, containerID, request)
		if err != nil {
			result = infraCheckContainerResult()
			result.CapacityFull = errors.Is(err, errCheckCapacityExceeded)
			result.Output = installationOutput
			result.OutputSHA256 = sha256Hex([]byte(installationOutput))
			return result, fmt.Errorf("project dependency installation failed: %w", err)
		}
		request.DependenciesInstalled = true
	}

	checkCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	runCtx, cancelRun := context.WithCancel(checkCtx)
	collector := newCheckContainerOutputCollector(request.OutputLimit, cancelRun)
	if installationOutput != "" {
		defer func() {
			combined := newOutputCollector(request.OutputLimit, func() {})
			_, _ = combined.Write([]byte(installationOutput))
			_, _ = combined.Write([]byte(result.Output))
			result.Output = combined.String()
			result.OutputSHA256 = sha256Hex([]byte(result.Output))
			result.OutputCapped = result.OutputCapped || combined.LimitExceeded()
		}()
	}
	capacityEvents := make(chan checkContainerCapacityEvent, 1)
	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	var monitorWait sync.WaitGroup
	monitorWait.Add(1)
	go monitorCheckContainerCapacity(monitorCtx, containerID, cancelRun, capacityEvents, &monitorWait, request)
	commands := [][]string{request.Argv}
	if request.ProjectRuntime != nil {
		commands = [][]string{{"mkdir", "-p", "/tmp/cleardev-project-data", "/workspace/node_modules"}}
		if request.DependencyDir != "" {
			commands = append(commands,
				[]string{"cp", "-R", "--no-preserve=ownership", "/dependency-template/.", "/workspace/node_modules"},
				[]string{"chmod", "-R", "u+rwX", "/workspace/node_modules"})
		}
		// The template deliberately contains no lifecycle outputs. Run these
		// against this check's disposable copy, with the exact project sources
		// available for root-package scripts. Never rebuild the shared template.
		if request.DownloadSeed != "" {
			commands = append(commands, []string{"mkdir", "-p", "/workspace/node_modules/.cleardev-npm-cache"}, []string{"cp", "-R", "--no-preserve=ownership", "/download-seed/.", "/workspace/node_modules/.cleardev-npm-cache"}, []string{"chmod", "-R", "u+rwX", "/workspace/node_modules/.cleardev-npm-cache"})
		}
		if _, err := os.Stat(filepath.Join(request.SourceDir, "package.json")); err == nil && !request.DependenciesInstalled {
			commands = append(commands, []string{"npm", "rebuild", "--foreground-scripts", "--ignore-scripts=false", "--offline=false", "--no-audit", "--no-fund"})
		}
		if len(request.ProjectRuntime.PrepareArgv) > 0 && !request.FreshInstall {
			commands = append(commands, request.ProjectRuntime.PrepareArgv)
		}
		if request.DependencyExport == "" {
			commands = append(commands, request.Argv)
		}
	}
	var runErr error
	executionBoundaryFailed := false
	for index, argv := range commands {
		if index == len(commands)-1 && request.DependencyExport == "" && request.BeforeExecute != nil {
			if err := request.BeforeExecute(); err != nil {
				executionBoundaryFailed = true
				runErr = fmt.Errorf("persist candidate execution boundary: %w", err)
				break
			}
		}
		if request.ProjectRuntime != nil {
			_, _ = fmt.Fprintf(collector, "[ClearDev isolated project command %d/%d]\n", index+1, len(commands))
		}
		execArgs, _ := CheckContainerExecArgs(containerID, argv)
		command := exec.CommandContext(runCtx, "docker", execArgs...) //nolint:gosec // validated argv in the bounded isolated container.
		command.Stdout, command.Stderr = collector, collector
		runErr = command.Run()
		if request.DependencyExport == "" && index == len(commands)-1 && command.ProcessState != nil {
			result.CommandExecuted = true
		}
		if runErr != nil {
			break
		}
	}
	stopMonitor()
	monitorWait.Wait()
	cancelRun()
	result.Output = collector.String()
	result.OutputSHA256 = collector.SHA256()
	result.OutputCapped = collector.Exceeded()
	if executionBoundaryFailed {
		result.Outcome = CheckContainerInfraError
		result.InfraError = true
		result.ExitCode = -1
		return result, runErr
	}
	var capacityEvent *checkContainerCapacityEvent
	select {
	case event := <-capacityEvents:
		capacityEvent = &event
	default:
	}
	if capacityEvent != nil {
		result = infraCheckContainerResult()
		result.Output, result.OutputSHA256 = collector.String(), collector.SHA256()
		if capacityEvent.Full {
			result.CapacityFull = true
		}
		if capacityEvent.Err != nil {
			return result, fmt.Errorf("monitor check filesystem capacity: %w", capacityEvent.Err)
		}
		return result, errors.New("check filesystem capacity exhausted during candidate execution")
	}
	if collector.Exceeded() {
		result.Outcome = CheckContainerFail
		result.ExitCode = -1
		return result, nil
	}
	if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
		result.Outcome = CheckContainerTimedOut
		result.ExitCode = -1
		result.TimedOut = true
		return result, nil
	}
	if errors.Is(checkCtx.Err(), context.Canceled) {
		result = infraCheckContainerResult()
		result.Output, result.OutputSHA256 = collector.String(), collector.SHA256()
		return result, errors.New("check container was canceled")
	}

	// Probe after execution. This is also used when the candidate exits nonzero,
	// so an ENOSPC-induced candidate failure cannot be mistaken for FAIL.
	full, probeErr := checkContainerCapacityForRequest(ctx, containerID, request)
	if probeErr != nil {
		result = infraCheckContainerResult()
		result.Output, result.OutputSHA256 = collector.String(), collector.SHA256()
		return result, fmt.Errorf("probe check filesystem capacity: %w", probeErr)
	}
	if full {
		result = infraCheckContainerResult()
		result.Output, result.OutputSHA256 = collector.String(), collector.SHA256()
		result.CapacityFull = true
		return result, errors.New("check filesystem capacity exhausted")
	}
	if len(request.OutputFiles) > 0 {
		artifacts, err := readTrialArtifacts(checkCtx, containerID, request.OutputFiles)
		if err != nil {
			return result, err
		}
		result.Artifacts = artifacts
	}
	if runErr == nil {
		if request.DependencyExport != "" {
			// Export may contain the template plus this check's generated files.
			counter, err := newCheckUsageCounter(checkUsageLimit{Bytes: checkDependencyBytesLimit + checkOutputBytesLimit, Items: checkDependencyItemsLimit + checkOutputItemsLimit})
			if err != nil {
				return infraCheckContainerResult(), err
			}
			if err := exportContainerDependencyTree(checkCtx, containerID, "/workspace/node_modules", request.DependencyExport, counter); err != nil {
				return infraCheckContainerResult(), err
			}
		}
		result.Outcome, result.ExitCode = CheckContainerPass, 0
		return result, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		result = infraCheckContainerResult()
		result.Output, result.OutputSHA256 = collector.String(), collector.SHA256()
		return result, fmt.Errorf("start candidate command in check container: %w", runErr)
	}
	result.ExitCode = exitErr.ExitCode()
	if result.ExitCode == 125 {
		result.Outcome = CheckContainerInfraError
		result.InfraError = true
		result.Output, result.OutputSHA256 = collector.String(), collector.SHA256()
		return result, fmt.Errorf("docker could not execute candidate command (exit %d)", exitErr.ExitCode())
	}
	result.Outcome = CheckContainerFail
	return result, nil
}

func normalizeCheckContainerRequest(request CheckContainerRequest) (CheckContainerRequest, error) {
	if request.DependencyExport != "" {
		if request.ProjectRuntime == nil || !filepath.IsAbs(request.DependencyExport) || len(request.Argv) != 0 || len(request.OutputFiles) != 0 || len(request.ProjectRuntime.PrepareArgv) != 0 {
			return request, errors.New("dependency export requires an owned project preparation directory")
		}
		if _, err := validateEmptyCheckTarDestination(request.DependencyExport); err != nil {
			return request, err
		}
	}
	if len(request.OutputFiles) > 8 {
		return request, errors.New("too many trial files")
	}
	for _, path := range request.OutputFiles {
		if !core.ProjectPath(path, false) || strings.HasSuffix(path, "/**") {
			return request, errors.New("invalid trial file")
		}
	}
	request.Image = strings.TrimSpace(request.Image)
	request.SourceDir = filepath.Clean(strings.TrimSpace(request.SourceDir))
	request.DependencyDir = filepath.Clean(strings.TrimSpace(request.DependencyDir))
	request.CIDFile = strings.TrimSpace(request.CIDFile)
	if request.DependencyDir == "." {
		request.DependencyDir = ""
	}
	if request.Image == "" || !filepath.IsAbs(request.SourceDir) || request.SourceDir == "/" || (request.DependencyExport == "" && !validCheckContainerArgv(request.Argv)) {
		return CheckContainerRequest{}, errors.New("invalid check container request")
	}
	if strings.Contains(request.SourceDir, ",") {
		return CheckContainerRequest{}, errors.New("trusted source directory cannot contain a comma")
	}
	if request.DependencyDir != "" && !filepath.IsAbs(request.DependencyDir) {
		return CheckContainerRequest{}, errors.New("invalid check dependency directory")
	}
	if request.SourceBytes < 0 || request.SourceBytes > CheckContainerMaxSourceBytes || request.SourceItems < 0 || request.SourceItems > CheckContainerMaxSourceItems {
		return CheckContainerRequest{}, errors.New("trusted check source exceeds its hard limit")
	}
	if request.Timeout <= 0 || request.MemoryBytes <= 0 || request.MemoryBytes > checkContainerMaxMemoryBytes || request.PidsLimit <= 0 || request.PidsLimit > checkContainerMaxPids {
		return CheckContainerRequest{}, errors.New("invalid check container resource limit")
	}
	if request.OutputLimit <= 0 || int64(request.OutputLimit) > checkContainerMaxOutputSummary {
		return CheckContainerRequest{}, errors.New("invalid check output limit")
	}
	if err := validateNodeCheckProfile(request.ExecutionProfile, request.ProjectRuntime); err != nil {
		return CheckContainerRequest{}, err
	}
	if request.ProjectRuntime != nil {
		if err := core.ValidateProjectRuntime(*request.ProjectRuntime); err != nil {
			return CheckContainerRequest{}, err
		}
		if request.DependencyExport == "" {
			if err := core.ValidateProjectNodeCommand(request.Argv); err != nil {
				return CheckContainerRequest{}, err
			}
		}
	}
	if request.FreshInstall && (request.ProjectRuntime == nil || request.DependencyDir != "" || request.DependenciesInstalled) {
		return request, errors.New("invalid fresh project installation request")
	}
	if request.NPMArchives != "" {
		if !request.FreshInstall || !filepath.IsAbs(request.NPMArchives) || strings.Contains(request.NPMArchives, ",") {
			return request, errors.New("invalid project archive staging directory")
		}
		if err := validateCheckDependencyDirectory(request.NPMArchives); err != nil {
			return request, err
		}
	}
	request.Argv = append([]string(nil), request.Argv...)
	return request, nil
}

func validCheckContainerArgv(argv []string) bool {
	if len(argv) == 0 || len(argv) > 64 || strings.TrimSpace(argv[0]) == "" || strings.HasPrefix(argv[0], "-") {
		return false
	}
	for _, value := range argv {
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || len(value) > 64*1024 {
			return false
		}
	}
	return true
}

func checkContainerRunArgs(request CheckContainerRequest, cidFile string) []string {
	tmpfsBytes := request.SourceBytes + CheckContainerMaxOutputBytes
	tmpfsItems := request.SourceItems + CheckContainerMaxOutputItems
	network, temporaryMount, workspaceOptions := "none", "rw,noexec,nosuid,size=16777216", "rw,mode=1777"
	if request.ProjectRuntime != nil {
		tmpfsBytes += checkDependencyBytesLimit
		tmpfsItems += checkDependencyItemsLimit
		if !request.Offline {
			network = "bridge"
		}
		temporaryMount = "rw,exec,nosuid,nodev,size=16777216"
		workspaceOptions = "rw,exec,nosuid,nodev,mode=1777"
	}
	args := []string{
		"run", "--rm", "--detach", "--stop-timeout=1", "--network=" + network, "--read-only",
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--cidfile=" + cidFile,
		"--memory=" + strconv.FormatInt(request.MemoryBytes, 10),
		"--memory-swap=" + strconv.FormatInt(request.MemoryBytes, 10),
		"--pids-limit=" + strconv.FormatInt(request.PidsLimit, 10),
		"--user=65532:65532", "--env=HOME=/tmp", "--env=CLEARDEV_CHECK_SANDBOX=1",
		"--tmpfs=/tmp:" + temporaryMount,
		"--tmpfs=/workspace:" + workspaceOptions + ",size=" + strconv.FormatInt(tmpfsBytes, 10) + ",nr_inodes=" + strconv.FormatInt(tmpfsItems, 10),
		"--mount=type=bind,src=" + request.SourceDir + ",dst=/candidate-source,readonly",
	}
	if request.FreshInstall {
		filtered := make([]string, 0, len(args))
		for _, arg := range args {
			if !strings.HasPrefix(arg, "--tmpfs=/workspace:") {
				filtered = append(filtered, arg)
			}
		}
		args = filtered
		args = append(args, "--mount=type=volume,dst=/workspace", "--mount=type=volume,dst=/install-cache")
		if request.NPMArchives != "" {
			args = append(args, "--mount=type=bind,src="+request.NPMArchives+",dst=/npm-archives,readonly")
		}
	}
	if request.DownloadSeed != "" {
		args = append(args, "--mount=type=bind,src="+request.DownloadSeed+",dst=/download-seed,readonly")
	}
	if request.DependencyDir != "" {
		target := "/workspace/node_modules"
		if request.ProjectRuntime != nil {
			target = "/dependency-template"
		}
		args = append(args, "--mount=type=bind,src="+request.DependencyDir+",dst="+target+",readonly")
	}
	if request.ProjectRuntime != nil {
		args = append(args, "--env=NPM_CONFIG_CACHE=/workspace/node_modules/.cleardev-npm-cache", "--env=NPM_CONFIG_USERCONFIG=/tmp/.npmrc", "--env=NPM_CONFIG_GLOBALCONFIG=/tmp/.npm-globalrc", "--env=NPM_CONFIG_UPDATE_NOTIFIER=false", "--env=npm_config_cache=/workspace/node_modules/.cleardev-npm-cache")
		if request.Image == projectInstallImage || request.FreshInstall {
			args = append(args, "--env=npm_config_nodedir=/usr/local")
		}
		if request.BuildFromSource {
			args = append(args, "--env=npm_config_build_from_source=true")
		}
		for _, entry := range core.ProjectRuntimeEnvironment(*request.ProjectRuntime, "/tmp/cleardev-project-data", 4173, true) {
			args = append(args, "--env="+entry)
		}
	}
	if request.ExecutionProfile == core.NodeCheckSmallThreadsV1 {
		args = append(args, "--env=UV_THREADPOOL_SIZE=1", "--env=NODE_OPTIONS=--v8-pool-size=1")
	}
	args = append(args, request.Image, "sleep", "infinity")
	return args
}

func prepareCheckContainerCIDFile(requested string) (path string, remove bool, err error) {
	if requested != "" {
		path = filepath.Clean(requested)
		if !filepath.IsAbs(path) || path == "/" {
			return "", false, errors.New("cidfile must be an absolute non-root path")
		}
		parent := filepath.Dir(path)
		parentInfo, parentErr := os.Lstat(parent)
		if parentErr != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
			return "", false, errors.New("cidfile parent must be a real directory")
		}
		if _, fileErr := os.Lstat(path); fileErr == nil {
			return "", false, errors.New("caller-owned cidfile already exists")
		} else if !os.IsNotExist(fileErr) {
			return "", false, fmt.Errorf("inspect caller-owned cidfile: %w", fileErr)
		}
		return path, false, nil
	}
	file, createErr := os.CreateTemp("", "cleardev-check-container-*.cid")
	if createErr != nil {
		return "", false, fmt.Errorf("create check container cidfile: %w", createErr)
	}
	path = file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return "", false, fmt.Errorf("close check container cidfile: %w", closeErr)
	}
	// Docker requires --cidfile to name a path that does not exist yet.
	if removeErr := os.Remove(path); removeErr != nil {
		return "", false, fmt.Errorf("prepare check container cidfile: %w", removeErr)
	}
	return path, true, nil
}

func startCheckContainer(ctx context.Context, request CheckContainerRequest, cidFile string) (string, error) {
	args := checkContainerRunArgs(request, cidFile)
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput() //nolint:gosec // fixed flags and validated image.
	if err != nil {
		return "", fmt.Errorf("start restricted check container: %w: %s", err, strings.TrimSpace(string(output)))
	}
	containerID := strings.TrimSpace(string(output))
	if !validDockerID(containerID) {
		cid, readErr := os.ReadFile(cidFile)
		if readErr != nil || !validDockerID(strings.TrimSpace(string(cid))) {
			return "", errors.New("docker returned no valid check container id")
		}
		containerID = strings.TrimSpace(string(cid))
	}
	return containerID, nil
}

func copyCheckSource(ctx context.Context, sourceDir, containerID string) error {
	if sourceDir == "" || !validDockerID(containerID) {
		return errors.New("invalid trusted source copy request")
	}
	// Docker rejects docker cp when the root filesystem is read-only, even if
	// the destination is a writable tmpfs. The source is therefore mounted
	// read-only and copied by a fixed root command into that tmpfs. cp -R does
	// not preserve host ownership; copied paths are root-owned and retain the
	// materializer's read-only permission bits.
	command := exec.CommandContext(ctx, "docker", "exec", "--user=0:0", containerID, "cp", "-R", "/candidate-source/.", "/workspace/") //nolint:gosec // fixed setup argv and validated container id.
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("copy trusted source inside check container: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func removeCheckContainer(ctx context.Context, containerID string) error {
	if !validDockerID(containerID) {
		return errors.New("invalid check container id during cleanup")
	}
	if output, err := exec.CommandContext(ctx, "docker", "rm", "--force", "--volumes", containerID).CombinedOutput(); err != nil {
		// --rm may have reaped a naturally exited keepalive process already.
		if strings.Contains(string(output), "No such container") {
			return nil
		}
		return fmt.Errorf("remove check container: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func validateCheckMaterializedSource(root string, expectedBytes, expectedItems int64) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect trusted check source: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("trusted check source must be a directory")
	}
	var bytesTotal, items int64
	seenFileIdentities := make(map[string]string)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		topLevel := strings.Split(relative, string(filepath.Separator))[0]
		if topLevel == ".git" || topLevel == "node_modules" {
			return fmt.Errorf("trusted check source contains reserved top-level path %q", topLevel)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		items++
		if items > CheckContainerMaxSourceItems {
			return errors.New("trusted check source item limit exceeded")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("trusted check source contains a symlink")
		}
		switch {
		case info.IsDir():
			if info.Mode().Perm() != 0o555 {
				return fmt.Errorf("trusted source directory %q is not mode 0555", path)
			}
		case info.Mode().IsRegular():
			if info.Mode().Perm() != 0o444 && info.Mode().Perm() != 0o555 {
				return fmt.Errorf("trusted source file %q is not mode 0444 or 0555", path)
			}
			// os.FileInfo exposes platform-specific identity through Sys. A
			// stable rendering lets this package remain buildable on Windows
			// while rejecting repeated identities (hard links) on Unix.
			if identity := fmt.Sprintf("%T:%#v", info.Sys(), info.Sys()); info.Sys() != nil {
				if previous, found := seenFileIdentities[identity]; found {
					return fmt.Errorf("trusted source file %q is hard-linked to %q", path, previous)
				}
				seenFileIdentities[identity] = path
			}
			bytesTotal += info.Size()
			if bytesTotal > CheckContainerMaxSourceBytes {
				return errors.New("trusted check source byte limit exceeded")
			}
		default:
			return fmt.Errorf("trusted check source contains unsupported entry %q", path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("validate trusted check source: %w", err)
	}
	if bytesTotal != expectedBytes || items != expectedItems {
		return fmt.Errorf("trusted check source manifest mismatch: got %d bytes/%d items, want %d/%d", bytesTotal, items, expectedBytes, expectedItems)
	}
	return nil
}

func validateCheckDependencyDirectory(path string) error {
	if strings.Contains(path, ",") {
		return errors.New("dependency directory cannot contain a comma")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("dependency directory must be a real directory")
	}
	return nil
}

func checkContainerCapacity(ctx context.Context, containerID string) (bool, error) {
	return checkContainerCapacityAt(ctx, containerID, "/workspace")
}

func checkContainerCapacityAt(ctx context.Context, containerID, path string) (bool, error) {
	if !validDockerID(containerID) || (path != "/workspace" && path != "/prepare") {
		return false, errors.New("invalid fixed statfs probe target")
	}
	probeCtx, cancel := context.WithTimeout(ctx, checkContainerProbeTimeout)
	defer cancel()
	// This is a fixed, non-candidate probe. Its output is numeric statfs data;
	// candidate stderr is never inspected for ENOSPC strings. Keep it direct
	// argv as well, so the probe itself does not depend on a candidate shell.
	output, err := exec.CommandContext(probeCtx, "docker", "exec", containerID, "stat", "-f", "-c", "%a %S %d", path).CombinedOutput() //nolint:gosec // fixed probe and validated container id/path.
	if err != nil {
		return false, fmt.Errorf("run fixed statfs probe: %w: %s", err, strings.TrimSpace(string(output)))
	}
	fields := strings.Fields(string(output))
	if len(fields) != 3 {
		return false, errors.New("fixed statfs probe returned malformed output")
	}
	availableBlocks, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return false, errors.New("fixed statfs probe returned invalid block count")
	}
	availableInodes, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return false, errors.New("fixed statfs probe returned invalid inode count")
	}
	return availableBlocks == 0 || availableInodes == 0, nil
}

type checkContainerCapacityEvent struct {
	Full bool
	Err  error
}

func monitorCheckContainerCapacity(ctx context.Context, containerID string, cancel context.CancelFunc, events chan<- checkContainerCapacityEvent, wait *sync.WaitGroup, requests ...CheckContainerRequest) {
	defer wait.Done()
	var request CheckContainerRequest
	if len(requests) > 0 {
		request = requests[0]
	}
	ticker := time.NewTicker(checkContainerProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			full, err := checkContainerCapacityForRequest(ctx, containerID, request)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				events <- checkContainerCapacityEvent{Err: err}
				cancel()
				return
			}
			if full {
				events <- checkContainerCapacityEvent{Full: true}
				cancel()
				return
			}
		}
	}
}

func infraCheckContainerResult() CheckContainerResult {
	return CheckContainerResult{Outcome: CheckContainerInfraError, ExitCode: -1, InfraError: true}
}

type checkContainerOutputCollector struct {
	mu       sync.Mutex
	limit    int
	buffer   bytes.Buffer
	stop     func()
	exceeded bool
}

func newCheckContainerOutputCollector(limit int, stop func()) *checkContainerOutputCollector {
	return &checkContainerOutputCollector{limit: limit, stop: stop}
}

func (collector *checkContainerOutputCollector) Write(payload []byte) (int, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	originalLength := len(payload)
	if collector.exceeded {
		return originalLength, nil
	}
	remaining := collector.limit - collector.buffer.Len()
	if len(payload) > remaining {
		payload = payload[:remaining]
		collector.exceeded = true
	}
	if len(payload) > 0 {
		_, _ = collector.buffer.Write(payload)
	}
	if collector.exceeded && collector.stop != nil {
		collector.stop()
		collector.stop = nil
	}
	return originalLength, nil
}

func (collector *checkContainerOutputCollector) String() string {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return collector.buffer.String()
}

func (collector *checkContainerOutputCollector) SHA256() string {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return fmt.Sprintf("%x", sha256.Sum256(collector.buffer.Bytes()))
}

func (collector *checkContainerOutputCollector) Exceeded() bool {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return collector.exceeded
}
