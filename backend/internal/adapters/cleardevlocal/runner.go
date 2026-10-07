// Package cleardevlocal implements the local Git and one-shot Docker checks
// used by the frozen S02 STANDARD demonstration.
package cleardevlocal

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Runner inspects builder worktrees, runs restricted checks, and composes
// verified PARALLEL candidates into a managed worktree.
type Runner struct {
	managedRoot string
}

func New() *Runner { return &Runner{} }

// NewWithRoot stores compose worktrees under the AO-managed worktree root.
func NewWithRoot(root string) *Runner {
	return &Runner{managedRoot: strings.TrimSpace(root)}
}

const (
	checkRunStateDirectory          = "cleardev-check-runs"
	generatedProofRunStateDirectory = "cleardev-generated-proof-runs"
	checkRunStateVersion            = 3
)

type checkRunState struct {
	ExecutionProfile string `json:"executionProfile,omitempty"`
	// Missing metadata identifies legacy evidence, never permission to retry.
	Attempt        int                        `json:"attempt,omitempty"`
	ExecutionState string                     `json:"executionState,omitempty"`
	Version        int                        `json:"version"`
	Fingerprint    string                     `json:"fingerprint"`
	State          string                     `json:"state"`
	Result         *ports.ClearDevCheckResult `json:"result,omitempty"`
	SourceManifest *candidateSourceManifest   `json:"sourceManifest,omitempty"`
	Error          string                     `json:"error,omitempty"`
}

type checkRequestFingerprint struct {
	ExecutionProfile string                         `json:"executionProfile,omitempty"`
	TrialStepID      string                         `json:"trialStepId,omitempty"`
	ProjectExecution *core.ProjectExecutionContract `json:"projectExecution,omitempty"`
	WorkspacePath    string
	CandidateSHA     string
	Image            string
	Argv             []string
	Timeout          time.Duration
	MemoryBytes      int64
	PidsLimit        int64
	OutputLimit      int
}

type generatedProofRunState struct {
	Version     int                                 `json:"version"`
	Fingerprint string                              `json:"fingerprint"`
	State       string                              `json:"state"`
	Result      *ports.ClearDevGeneratedProofResult `json:"result,omitempty"`
	Error       string                              `json:"error,omitempty"`
}

type generatedProofRequestFingerprint struct {
	WorkspacePath string
	CandidateSHA  string
	Image         string
	Argv          []string
	OutputPaths   []string
	Timeout       time.Duration
	MemoryBytes   int64
	PidsLimit     int64
}

func (r *Runner) InspectCandidate(ctx context.Context, workspacePath, baseSHA string) (ports.ClearDevCandidateInspection, error) {
	return r.inspectCandidate(ctx, workspacePath, baseSHA, false)
}

// InspectProjectCandidate excludes only untracked root npm installation
// material. Source discovery and historical non-project checks stay strict.
func (r *Runner) InspectProjectCandidate(ctx context.Context, workspacePath, baseSHA string) (ports.ClearDevCandidateInspection, error) {
	return r.inspectCandidate(ctx, workspacePath, baseSHA, true)
}

func (r *Runner) inspectCandidate(ctx context.Context, workspacePath, baseSHA string, project bool) (ports.ClearDevCandidateInspection, error) {
	workspacePath = filepath.Clean(strings.TrimSpace(workspacePath))
	baseSHA = strings.TrimSpace(baseSHA)
	if workspacePath == "." || !validCommit(baseSHA) {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	status, err := gitOutput(ctx, workspacePath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return ports.ClearDevCandidateInspection{}, fmt.Errorf("%w: status: %v", ports.ErrClearDevGitUnavailable, err)
	}
	if len(status) != 0 && (!project || !onlyUntrackedProjectDependencies(workspacePath, status)) {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevWorkspaceDirty
	}
	headRaw, err := gitOutput(ctx, workspacePath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return ports.ClearDevCandidateInspection{}, fmt.Errorf("%w: resolve HEAD: %v", ports.ErrClearDevGitUnavailable, err)
	}
	head := strings.TrimSpace(string(headRaw))
	if !validCommit(head) {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	ancestor := exec.CommandContext(ctx, "git", "-C", workspacePath, "merge-base", "--is-ancestor", baseSHA, head) //nolint:gosec // argv is explicit and never interpreted by a shell.
	if err := ancestor.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateNotDescendant
		}
		return ports.ClearDevCandidateInspection{}, fmt.Errorf("%w: verify ancestry: %v", ports.ErrClearDevGitUnavailable, err)
	}
	diffRaw, err := gitOutput(ctx, workspacePath, "diff", "--name-status", "-z", "--find-renames", baseSHA, head)
	if err != nil {
		return ports.ClearDevCandidateInspection{}, fmt.Errorf("%w: diff: %v", ports.ErrClearDevGitUnavailable, err)
	}
	paths, err := parseNameStatusZ(diffRaw)
	if err != nil {
		return ports.ClearDevCandidateInspection{}, fmt.Errorf("%w: parse diff: %v", ports.ErrClearDevGitUnavailable, err)
	}
	return ports.ClearDevCandidateInspection{BaseSHA: baseSHA, CandidateSHA: head, Paths: paths}, nil
}

func (r *Runner) PrepareReviewBranch(ctx context.Context, workspacePath, branch, candidateSHA string) error {
	workspacePath = filepath.Clean(strings.TrimSpace(workspacePath))
	branch = strings.TrimSpace(branch)
	candidateSHA = strings.TrimSpace(candidateSHA)
	if workspacePath == "." || branch == "" || strings.HasPrefix(branch, "-") || !validCommit(candidateSHA) {
		return ports.ErrClearDevCandidateInvalid
	}
	current, err := gitOutput(ctx, workspacePath, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err == nil {
		if strings.TrimSpace(string(current)) != candidateSHA {
			return fmt.Errorf("%w: review branch already points to another commit", ports.ErrClearDevCandidateInvalid)
		}
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", workspacePath, "branch", branch, candidateSHA) //nolint:gosec // explicit argv, stable branch supplied by the control plane.
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		return fmt.Errorf("%w: create review branch: %v: %s", ports.ErrClearDevGitUnavailable, runErr, strings.TrimSpace(string(output)))
	}
	return nil
}

// PrepareBaseWorkspace switches one builder worktree onto a new frozen base.
// It refuses a dirty tree or an unexpected HEAD so a lost candidate can never
// be silently overwritten by the rebase.
func (r *Runner) PrepareBaseWorkspace(ctx context.Context, workspacePath, expectedHeadSHA, newBaseSHA string) error {
	workspacePath = filepath.Clean(strings.TrimSpace(workspacePath))
	expectedHeadSHA = strings.TrimSpace(expectedHeadSHA)
	newBaseSHA = strings.TrimSpace(newBaseSHA)
	if workspacePath == "." || !validCommit(expectedHeadSHA) || !validCommit(newBaseSHA) {
		return ports.ErrClearDevCandidateInvalid
	}
	status, err := gitOutput(ctx, workspacePath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("%w: status: %w", ports.ErrClearDevGitUnavailable, err)
	}
	if len(status) != 0 {
		return ports.ErrClearDevWorkspaceDirty
	}
	headRaw, err := gitOutput(ctx, workspacePath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("%w: resolve HEAD: %w", ports.ErrClearDevGitUnavailable, err)
	}
	if head := strings.TrimSpace(string(headRaw)); head != expectedHeadSHA {
		return fmt.Errorf("%w: builder worktree is parked on an unexpected commit", ports.ErrClearDevCandidateInvalid)
	}
	checkout := exec.CommandContext(ctx, "git", "-C", workspacePath, "checkout", "--detach", newBaseSHA) //nolint:gosec // explicit argv, commit supplied by the control plane.
	if output, runErr := checkout.CombinedOutput(); runErr != nil {
		return fmt.Errorf("%w: detach to base: %w: %s", ports.ErrClearDevGitUnavailable, runErr, strings.TrimSpace(string(output)))
	}
	headRaw, err = gitOutput(ctx, workspacePath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("%w: resolve HEAD: %w", ports.ErrClearDevGitUnavailable, err)
	}
	if head := strings.TrimSpace(string(headRaw)); head != newBaseSHA {
		return fmt.Errorf("%w: base switch did not settle", ports.ErrClearDevCandidateInvalid)
	}
	return nil
}

// ComposeCandidates replays the ordered candidates onto the common base inside
// a deterministic managed worktree. A conflicting cherry-pick stops with
// ErrClearDevCompositionConflict and the conflicted paths; nothing is resolved
// automatically.
func (r *Runner) ComposeCandidates(ctx context.Context, request ports.ClearDevComposeRequest) (ports.ClearDevComposeResult, error) {
	if request.CompleteTaskDeltas {
		return r.composeMailCandidates(ctx, request)
	}
	requestID := strings.TrimSpace(request.RequestID)
	repoPath := filepath.Clean(strings.TrimSpace(request.RepoPath))
	if requestID == "" || repoPath == "." || !validCommit(request.BaseSHA) || len(request.CandidateSHAs) == 0 {
		return ports.ClearDevComposeResult{}, ports.ErrClearDevCandidateInvalid
	}
	for _, candidate := range request.CandidateSHAs {
		if !validCommit(strings.TrimSpace(candidate)) {
			return ports.ClearDevComposeResult{}, ports.ErrClearDevCandidateInvalid
		}
	}
	digest := sha256.Sum256([]byte(requestID))
	workspace, err := r.composeWorkspace(digest)
	if err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	if err := removeManagedWorktree(ctx, repoPath, workspace); err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(workspace), 0o750); err != nil {
		return ports.ClearDevComposeResult{}, fmt.Errorf("%w: create compose parent: %w", ports.ErrClearDevGitUnavailable, err)
	}
	add := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "add", "--detach", workspace, request.BaseSHA) //nolint:gosec // explicit argv, deterministic path and commit from the control plane.
	if output, runErr := add.CombinedOutput(); runErr != nil {
		return ports.ClearDevComposeResult{}, fmt.Errorf("%w: add compose worktree: %w: %s", ports.ErrClearDevGitUnavailable, runErr, strings.TrimSpace(string(output)))
	}
	result := ports.ClearDevComposeResult{WorkspacePath: workspace}
	defer func() {
		if result.OutputSHA == "" && len(result.ConflictPaths) == 0 {
			_ = removeManagedWorktree(context.WithoutCancel(ctx), repoPath, workspace)
		}
	}()
	for _, candidate := range request.CandidateSHAs {
		candidate = strings.TrimSpace(candidate)
		pick := exec.CommandContext(ctx, "git", "-C", workspace, "cherry-pick", candidate) //nolint:gosec // explicit argv, commits come from verified durable facts.
		output, pickErr := pick.CombinedOutput()
		if pickErr == nil {
			continue
		}
		conflicts, conflictErr := gitOutput(ctx, workspace, "diff", "--name-only", "--diff-filter=U")
		if conflictErr == nil {
			paths := []string{}
			for _, line := range strings.Split(strings.TrimSpace(string(conflicts)), "\n") {
				if trimmed := strings.TrimSpace(line); trimmed != "" {
					paths = append(paths, trimmed)
				}
			}
			if len(paths) > 0 {
				abort := exec.CommandContext(ctx, "git", "-C", workspace, "cherry-pick", "--abort") //nolint:gosec // explicit argv.
				_, _ = abort.CombinedOutput()
				result.ConflictPaths = paths
				return result, ports.ErrClearDevCompositionConflict
			}
		}
		return ports.ClearDevComposeResult{}, fmt.Errorf("%w: cherry-pick %s: %w: %s", ports.ErrClearDevGitUnavailable, candidate, pickErr, strings.TrimSpace(string(output)))
	}
	headRaw, err := gitOutput(ctx, workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return ports.ClearDevComposeResult{}, fmt.Errorf("%w: resolve composed HEAD: %w", ports.ErrClearDevGitUnavailable, err)
	}
	head := strings.TrimSpace(string(headRaw))
	if !validCommit(head) {
		return ports.ClearDevComposeResult{}, ports.ErrClearDevCandidateInvalid
	}
	result.OutputSHA = head
	return result, nil
}

func (r *Runner) composeWorkspace(digest [32]byte) (string, error) {
	root := r.managedRoot
	if root == "" {
		if dataDir := strings.TrimSpace(os.Getenv("AO_DATA_DIR")); dataDir != "" {
			root = filepath.Join(dataDir, "worktrees")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("%w: resolve compose root: %w", ports.ErrClearDevGitUnavailable, err)
			}
			root = filepath.Join(home, ".ao", "worktrees")
		}
	}
	return filepath.Join(root, fmt.Sprintf("cleardev-compose-%x", digest[:16])), nil
}

// removeManagedWorktree deletes a previously created compose worktree and its
// Git registration. It is only ever called with control-plane-owned paths.
func removeManagedWorktree(ctx context.Context, repoPath, workspace string) error {
	if _, err := os.Stat(workspace); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%w: inspect compose worktree: %w", ports.ErrClearDevGitUnavailable, err)
	}
	remove := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", workspace) //nolint:gosec // explicit argv, deterministic control-plane path.
	if output, runErr := remove.CombinedOutput(); runErr != nil {
		if rmErr := os.RemoveAll(workspace); rmErr != nil {
			return fmt.Errorf("%w: remove compose worktree: %w: %s", ports.ErrClearDevGitUnavailable, runErr, strings.TrimSpace(string(output)))
		}
		prune := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "prune") //nolint:gosec // explicit argv.
		_, _ = prune.CombinedOutput()
	}
	return nil
}

func (r *Runner) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (result ports.ClearDevCheckResult, retErr error) {
	request, err := normalizeCheckRequest(request)
	if err == nil && request.ProjectExecution != nil && request.ExecutionProfile == "" {
		request.ExecutionProfile = core.NodeCheckSmallThreadsV1
		err = validateNodeCheckProfile(request.ExecutionProfile, &request.ProjectExecution.Runtime)
	}
	if err != nil {
		return result, err
	}
	if request.RunID == "" {
		return r.runCandidateCheck(ctx, request, nil, nil)
	}

	return r.runIdempotentCandidateCheck(ctx, request)
}

func (r *Runner) runIdempotentCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, err
	}
	fingerprint, err := checkRequestSHA256(request)
	if err != nil {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, fmt.Errorf("encode ClearDev check request: %w", err)
	}
	statePath, err := checkRunStatePath(request.RunID)
	if err != nil {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, err
	}

	// Serialize only the same RunID, including a permitted preparation retry.
	// The OS releases this lock on process exit; a durable started record still
	// blocks replay after a crash, rather than guessing whether Docker ran.
	unlock, err := lockCheckFile(ctx, statePath+".lock")
	if err != nil {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, err
	}
	started := checkRunState{ExecutionProfile: request.ExecutionProfile, Version: checkRunStateVersion, Fingerprint: fingerprint, State: "started", Attempt: 1, ExecutionState: checkExecutionNotStarted}
	created, state, err := createOrReadCheckRunState(statePath, started)
	if err != nil {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, err
	}
	if !created {
		if state.Version != checkRunStateVersion || state.Fingerprint != fingerprint || state.ExecutionProfile != request.ExecutionProfile {
			return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, errors.New("ClearDev check RunID is already bound to a different request")
		}
		switch state.State {
		case "settled":
			if state.Result == nil {
				return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, errors.New("ClearDev check RunID has an invalid settled result")
			}
			if state.Result.SourceManifestID != "" {
				if state.SourceManifest == nil || verifySavedCandidateManifest(*state.SourceManifest, checkEnvironmentForResult(*state.Result)) != nil {
					return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, errors.New("ClearDev check RunID has an invalid saved source manifest")
				}
			}
			retry, retryErr := canRetryUnstartedTrial(ctx, request, state)
			if retryErr != nil {
				return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, retryErr
			}
			if retry {
				if err := archiveUnstartedCheckAttempt(statePath); err != nil {
					return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, err
				}
				started.Attempt = 2
				if err := writeCheckRunState(statePath, started); err != nil {
					return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, err
				}
				break
			}
			if state.Error != "" {
				return *state.Result, errors.New(state.Error)
			}
			return *state.Result, nil
		case "started":
			// The process may have stopped after the Docker action was marked
			// started. There is no safe way to prove that action did not reach
			// Docker, so fail closed and let the control plane mark it BLOCKED.
			return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, errors.New("ClearDev check RunID has an unsettled external action")
		default:
			return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, errors.New("ClearDev check RunID has an invalid state")
		}
	}

	var sourceManifest candidateSourceManifest
	beforeExecute := func() error {
		// Mark the boundary BEFORE handing control to the candidate executor,
		// including project preparation commands. False/empty output flags can
		// never erase this fact after cancellation or cleanup failure.
		started.ExecutionState = checkExecutionStartedOrUnknown
		return writeCheckRunState(statePath, started)
	}
	result, runErr := r.runCandidateCheck(ctx, request, &sourceManifest, beforeExecute)
	cleanupKnown := true
	if result.DependencyCacheKey != "" {
		if err := r.releaseFormalCheckReference(ctx, request.RunID); err != nil {
			cleanupKnown = false
			result.Outcome, result.ExitCode = ports.ClearDevCheckInfraError, -1
			runErr = errors.Join(runErr, fmt.Errorf("release settled ClearDev check reference: %w", err))
		}
	}
	settled := checkRunState{ExecutionProfile: request.ExecutionProfile, Version: checkRunStateVersion, Fingerprint: fingerprint, State: "settled", Result: &result, Attempt: started.Attempt, ExecutionState: checkExecutionStartedOrUnknown}
	if sourceManifest.Version != 0 {
		settled.SourceManifest = &sourceManifest
	}
	if started.ExecutionState == checkExecutionNotStarted && runErr != nil && ctx.Err() == nil && cleanupKnown {
		// Preparation itself can start probes or acquire dependency leases.
		// A normal pre-boundary return alone does not prove those are released.
		released, err := trialPreparationResourcesReleased(ctx, request.RunID)
		if err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("verify preparation cleanup: %w", err))
		} else if released {
			settled.ExecutionState = checkExecutionNotStarted
		}
	}
	if runErr != nil {
		settled.Error = runErr.Error()
	}
	if err := writeCheckRunState(statePath, settled); err != nil {
		// Keep the durable state as started if settling fails. Retrying the
		// same RunID must not risk a second Docker invocation.
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, fmt.Errorf("settle ClearDev check RunID: %w", err)
	}
	return result, runErr
}

func (r *Runner) releaseFormalCheckReference(ctx context.Context, runID string) error {
	manager, err := openDependencyCacheManager(ctx)
	if err != nil {
		return err
	}
	return manager.releaseReference(ctx, runID)
}

func normalizeCheckRequest(request ports.ClearDevCheckRequest) (ports.ClearDevCheckRequest, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.WorkspacePath = filepath.Clean(strings.TrimSpace(request.WorkspacePath))
	request.CandidateSHA = strings.TrimSpace(request.CandidateSHA)
	request.Image = strings.TrimSpace(request.Image)
	if request.RunID != "" && !validRunID(request.RunID) {
		return ports.ClearDevCheckRequest{}, errors.New("invalid ClearDev check RunID")
	}
	if request.WorkspacePath == "." || !validCommit(request.CandidateSHA) || request.Image == "" || len(request.Argv) == 0 || strings.TrimSpace(request.Argv[0]) == "" {
		return ports.ClearDevCheckRequest{}, errors.New("invalid ClearDev check request")
	}
	if request.Timeout <= 0 {
		return ports.ClearDevCheckRequest{}, errors.New("ClearDev check timeout must be positive")
	}
	if request.MemoryBytes <= 0 {
		request.MemoryBytes = 256 * 1024 * 1024
	}
	if request.PidsLimit <= 0 {
		request.PidsLimit = 64
	}
	if request.OutputLimit <= 0 {
		request.OutputLimit = 64 * 1024
	}
	contract, err := normalizedProjectCheckContract(request.ProjectExecution, [][]string{request.Argv}, request.Timeout)
	if err != nil {
		return ports.ClearDevCheckRequest{}, err
	}
	if contract != nil && request.Image != core.StandardCandidateCheckImage {
		return ports.ClearDevCheckRequest{}, errors.New("project checks require the pinned checker image")
	}
	if request.TrialStepID != "" {
		if contract == nil {
			return ports.ClearDevCheckRequest{}, errors.New("trial requires an admitted project")
		}
		step, found := core.ProjectTrialCommand(contract.Basis, request.TrialStepID)
		if !found || !slices.Equal(step.Argv, request.Argv) || request.Timeout != time.Duration(step.TimeoutSeconds)*time.Second {
			return ports.ClearDevCheckRequest{}, errors.New("trial command differs from its frozen step")
		}
	}
	var runtime *core.ProjectRuntime
	if contract != nil {
		runtime = &contract.Runtime
	}
	if err := validateNodeCheckProfile(request.ExecutionProfile, runtime); err != nil {
		return ports.ClearDevCheckRequest{}, err
	}
	request.ProjectExecution = contract
	request.Argv = append([]string(nil), request.Argv...)
	return request, nil
}

func checkRequestSHA256(request ports.ClearDevCheckRequest) (string, error) {
	payload, err := json.Marshal(checkRequestFingerprint{
		ExecutionProfile: request.ExecutionProfile,
		TrialStepID:      request.TrialStepID,
		ProjectExecution: request.ProjectExecution,
		WorkspacePath:    request.WorkspacePath,
		CandidateSHA:     request.CandidateSHA,
		Image:            request.Image,
		Argv:             request.Argv,
		Timeout:          request.Timeout,
		MemoryBytes:      request.MemoryBytes,
		PidsLimit:        request.PidsLimit,
		OutputLimit:      request.OutputLimit,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func checkRunStatePath(runID string) (string, error) {
	return durableRunStatePath(checkRunStateDirectory, runID, "check")
}

func generatedProofRunStatePath(runID string) (string, error) {
	return durableRunStatePath(generatedProofRunStateDirectory, runID, "generated-proof")
}

func durableRunStatePath(subdir, runID, kind string) (string, error) {
	dataDir := strings.TrimSpace(os.Getenv("AO_DATA_DIR"))
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve ClearDev %s data directory: %w", kind, err)
		}
		dataDir = filepath.Join(home, ".ao")
	}
	directory := filepath.Join(filepath.Clean(dataDir), subdir)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create ClearDev %s data directory: %w", kind, err)
	}
	// Never put a caller-controlled RunID in the filesystem or the durable
	// record. The hash is sufficient for a stable lookup and prevents a key
	// from becoming a path or being written alongside results.
	key := sha256.Sum256([]byte(runID))
	return filepath.Join(directory, fmt.Sprintf("%x.json", key)), nil
}

func validRunID(runID string) bool {
	if runID == "" || len(runID) > 512 || !utf8.ValidString(runID) {
		return false
	}
	for _, char := range runID {
		if char == '\x00' || char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func createOrReadCheckRunState(path string, started checkRunState) (bool, checkRunState, error) {
	created, payload, err := createOrReadJSONState(path, "ClearDev check RunID", started)
	if err != nil {
		return false, checkRunState{}, err
	}
	if created {
		return true, started, nil
	}
	var state checkRunState
	if err := json.Unmarshal(payload, &state); err != nil {
		return false, checkRunState{}, fmt.Errorf("decode ClearDev check RunID: %w", err)
	}
	return false, state, nil
}

func createOrReadGeneratedProofRunState(path string, started generatedProofRunState) (bool, generatedProofRunState, error) {
	created, payload, err := createOrReadJSONState(path, "ClearDev generated-proof RunID", started)
	if err != nil {
		return false, generatedProofRunState{}, err
	}
	if created {
		return true, started, nil
	}
	var state generatedProofRunState
	if err := json.Unmarshal(payload, &state); err != nil {
		return false, generatedProofRunState{}, fmt.Errorf("decode ClearDev generated-proof RunID: %w", err)
	}
	return false, state, nil
}

func createOrReadJSONState(path, kind string, started any) (bool, []byte, error) {
	payload, err := json.Marshal(started)
	if err != nil {
		return false, nil, fmt.Errorf("encode started %s: %w", kind, err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".started-")
	if err != nil {
		return false, nil, fmt.Errorf("create started %s: %w", kind, err)
	}
	temporaryPath := file.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return false, nil, fmt.Errorf("set started %s permissions: %w", kind, err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return false, nil, fmt.Errorf("write started %s: %w", kind, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, nil, fmt.Errorf("sync started %s: %w", kind, err)
	}
	if err := file.Close(); err != nil {
		return false, nil, fmt.Errorf("close started %s: %w", kind, err)
	}
	if err := os.Link(temporaryPath, path); err == nil {
		return true, payload, nil
	} else if !errors.Is(err, os.ErrExist) {
		return false, nil, fmt.Errorf("reserve %s: %w", kind, err)
	}
	existing, err := readJSONStateBytes(path, kind)
	if err != nil {
		return false, nil, err
	}
	return false, existing, nil
}

func readJSONStateBytes(path, kind string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", kind, err)
	}
	defer func() { _ = file.Close() }()
	limit := maxJSONStateBytes(kind)
	payload, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", kind, err)
	}
	if int64(len(payload)) > limit {
		return nil, fmt.Errorf("%s state is too large", kind)
	}
	return payload, nil
}

func maxJSONStateBytes(kind string) int64 {
	if kind == "ClearDev check preflight RunID" || kind == "ClearDev check RunID" {
		// A settled preflight saves the complete trusted source manifest. The
		// manifest and source share the fixed source allowance; the extra MiB
		// covers the small environment and state envelope.
		return checkSourceBytesLimit + 1<<20
	}
	return 1 << 20
}

func writeCheckRunState(path string, state checkRunState) error {
	if err := writeJSONState(path, "ClearDev check RunID", state); err != nil {
		return err
	}
	return syncCheckRunDirectory(path)
}

func writeGeneratedProofRunState(path string, state generatedProofRunState) error {
	return writeJSONState(path, "ClearDev generated-proof RunID", state)
}

func writeJSONState(path, kind string, state any) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode settled %s: %w", kind, err)
	}
	if int64(len(payload)) > maxJSONStateBytes(kind) {
		return fmt.Errorf("%s state is too large", kind)
	}
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".settled-")
	if err != nil {
		return fmt.Errorf("create settled %s: %w", kind, err)
	}
	temporaryPath := file.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set settled %s permissions: %w", kind, err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write settled %s: %w", kind, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync settled %s: %w", kind, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close settled %s: %w", kind, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace settled %s: %w", kind, err)
	}
	return nil
}

func (r *Runner) runCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest, savedManifest *candidateSourceManifest, beforeExecute func() error) (result ports.ClearDevCheckResult, retErr error) {
	if injected, ok := injectTestInfrastructureFailure(request); ok {
		return injected, nil
	}
	preflight, err := normalizeCheckPreflightRequest(ports.ClearDevCheckPreflightRequest{
		RunID: request.RunID, WorkspacePath: request.WorkspacePath, CandidateSHA: request.CandidateSHA, Image: request.Image,
		Argv: [][]string{append([]string(nil), request.Argv...)}, Timeout: checkPreparationTimeout,
		MemoryBytes: request.MemoryBytes, PidsLimit: request.PidsLimit, ProjectExecution: request.ProjectExecution,
	})
	if err != nil {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, err
	}
	prepared, err := r.prepareCheckEnvironment(ctx, preflight)
	result = checkResultForEnvironment(prepared.public)
	if savedManifest != nil && prepared.sourceManifest.Version != 0 {
		*savedManifest = prepared.sourceManifest
	}
	if err != nil {
		collector := newOutputCollector(checkPreparationOutputLimit, func() {})
		_, _ = collector.Write([]byte(err.Error()))
		result.OutputSummary = collector.String()
		result.OutputSHA256 = sha256Hex([]byte(result.OutputSummary))
		result.OutputTruncated = collector.LimitExceeded()
		return result, err
	}
	if request.RunID == "" && prepared.dependencyReference != "" {
		defer func() {
			manager, managerErr := openDependencyCacheManager(context.Background())
			if managerErr == nil {
				managerErr = manager.releaseReference(context.Background(), prepared.dependencyReference)
			}
			if managerErr != nil && retErr == nil {
				result.Outcome, result.ExitCode = ports.ClearDevCheckInfraError, -1
				retErr = fmt.Errorf("release one-shot dependency reference: %w", managerErr)
			}
		}()
	}
	imageID := prepared.public.ImageID
	actionID := request.RunID
	if actionID == "" {
		actionID = "candidate-check:" + request.CandidateSHA + ":" + newOpaqueCheckID()
	}
	reservationBytes := checkRunReservationBytes
	if request.ProjectExecution != nil {
		reservationBytes += checkDependencyBytesLimit + checkDependencyBytesLimit + checkOutputBytesLimit
	}
	temporary, err := acquireCheckTemporary(ctx, actionID, "candidate-check", reservationBytes, request.RunID)
	if err != nil {
		return result, fmt.Errorf("reserve candidate check temporary capacity: %w", err)
	}
	temporaryReleased := false
	defer func() {
		if !temporaryReleased {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), CheckCleanupTimeout)
			defer cancel()
			cleanupErr := temporary.Release(cleanupCtx)
			if cleanupErr != nil && retErr == nil {
				result.Outcome, result.ExitCode = ports.ClearDevCheckInfraError, -1
				retErr = fmt.Errorf("clean disposable check environment: %w", cleanupErr)
			}
		}
	}()
	sourceDirectory := filepath.Join(temporary.Root(), "candidate")
	scratchDirectory := filepath.Join(temporary.Root(), "git")
	manifestPath := filepath.Join(temporary.Root(), "candidate-manifest.json")
	facts, err := materializeCandidateSource(ctx, request.WorkspacePath, request.CandidateSHA, scratchDirectory, sourceDirectory, manifestPath, defaultCandidateSourceLimits())
	if err != nil {
		return result, fmt.Errorf("materialize trusted candidate source: %w", err)
	}
	if facts.ManifestID != prepared.public.SourceManifestID || facts.Manifest.CandidateSHA != prepared.public.CandidateSHA || facts.Manifest.RootTreeOID != prepared.public.SourceRootTreeOID || int64(facts.Manifest.ItemCount) != prepared.public.SourceItems || facts.Manifest.BlobBytes != prepared.public.SourceBytes {
		return result, errors.New("candidate source changed between preflight and formal materialization")
	}
	if request.ProjectExecution == nil && request.Argv[0] == "npm" && candidateManifestHasRootEntry(facts.Manifest, "dist") {
		return result, errors.New("candidate source contains reserved top-level path \"dist\"")
	}
	dependencyDirectory := ""
	cacheLeaseID := ""
	var cacheManager *dependencyCacheManager
	if prepared.dependencyDirectory != "" {
		dependencyDirectory = filepath.Join(prepared.dependencyDirectory, "node_modules")
		cacheManager, err = openDependencyCacheManager(ctx)
		if err != nil {
			return result, err
		}
		cacheLeaseID = actionID + ":dependency-lease"
		if err := cacheManager.acquireLease(ctx, cacheLeaseID, prepared.dependencyCacheKey, temporary.CIDFile()); err != nil {
			return result, err
		}
	}
	cacheLeaseReleased := cacheLeaseID == ""
	defer func() {
		if !cacheLeaseReleased && temporaryReleased {
			if releaseErr := cacheManager.releaseLease(context.Background(), cacheLeaseID); releaseErr != nil && retErr == nil {
				result.Outcome, result.ExitCode = ports.ClearDevCheckInfraError, -1
				retErr = fmt.Errorf("release dependency cache lease: %w", releaseErr)
			}
		}
	}()
	var projectRuntime *core.ProjectRuntime
	if request.ProjectExecution != nil {
		projectRuntime = &request.ProjectExecution.Runtime
	}
	var outputFiles []string
	if request.TrialStepID != "" && request.ProjectExecution != nil {
		step, _ := core.ProjectTrialCommand(request.ProjectExecution.Basis, request.TrialStepID)
		outputFiles = step.OutputFiles
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	freshInstall := projectRuntime != nil && prepared.public.PackageJSONSHA256 != ""
	npmArchives := ""
	var preparationDeadline time.Time
	if freshInstall {
		npmArchives, preparationDeadline, err = stageProjectNPMArchives(ctx, sourceDirectory, temporary.Root())
		if err != nil {
			return result, fmt.Errorf("project npm dependency preparation failed: %w", err)
		}
	}
	// Keep historical request fingerprints readable. The actual project resource
	// policy is bound independently in the prepared environment digest.
	memoryBytes := request.MemoryBytes
	pidsLimit, outputLimit := request.PidsLimit, request.OutputLimit
	if projectRuntime != nil {
		memoryBytes = core.ProjectCheckMemoryBytes
		pidsLimit, outputLimit = core.ProjectCheckPidsLimit, core.ProjectCheckOutputLimit
	}
	containerResult, runErr := RunCheckContainer(ctx, CheckContainerRequest{
		BeforeExecute: beforeExecute, OutputFiles: outputFiles, FreshInstall: freshInstall, NPMArchives: npmArchives, PreparationDeadline: preparationDeadline,
		Image: imageID, Argv: request.Argv, SourceDir: sourceDirectory, ProjectRuntime: projectRuntime, ExecutionProfile: request.ExecutionProfile,
		SourceBytes: facts.Manifest.BlobBytes, SourceItems: int64(facts.Manifest.ItemCount),
		DependencyDir: dependencyDirectory, CIDFile: temporary.CIDFile(), Timeout: request.Timeout,
		MemoryBytes: memoryBytes, PidsLimit: pidsLimit, OutputLimit: outputLimit,
	})
	if len(containerResult.Artifacts) > 0 {
		raw, err := json.Marshal(containerResult.Artifacts)
		if err != nil {
			return result, err
		}
		result.TrialArtifactsJSON = string(raw)
	}
	result.TrialCommandExecuted = containerResult.CommandExecuted
	result.ExitCode = containerResult.ExitCode
	result.OutputSummary = containerResult.Output
	result.OutputSHA256 = containerResult.OutputSHA256
	result.OutputTruncated = containerResult.OutputCapped
	result.TimedOut = containerResult.TimedOut
	cleanupCtx, cancel := context.WithTimeout(context.Background(), CheckCleanupTimeout)
	cleanupErr := temporary.Release(cleanupCtx)
	cancel()
	if cleanupErr != nil {
		result.Outcome, result.ExitCode = ports.ClearDevCheckInfraError, -1
		return result, fmt.Errorf("clean disposable check environment: %w", cleanupErr)
	}
	temporaryReleased = true
	if cacheLeaseID != "" {
		if err := cacheManager.releaseLease(context.WithoutCancel(ctx), cacheLeaseID); err != nil {
			result.Outcome, result.ExitCode = ports.ClearDevCheckInfraError, -1
			return result, fmt.Errorf("release dependency cache lease: %w", err)
		}
		cacheLeaseReleased = true
	}
	switch containerResult.Outcome {
	case CheckContainerPass:
		result.Outcome = ports.ClearDevCheckPass
	case CheckContainerFail:
		result.Outcome = ports.ClearDevCheckFail
	case CheckContainerTimedOut:
		result.Outcome = ports.ClearDevCheckTimedOut
	case CheckContainerInfraError:
		result.Outcome = ports.ClearDevCheckInfraError
	default:
		result.Outcome, result.ExitCode = ports.ClearDevCheckInfraError, -1
		return result, errors.New("check container returned an invalid outcome")
	}
	if runErr != nil {
		result.Outcome = ports.ClearDevCheckInfraError
		return result, runErr
	}
	return result, nil
}

func candidateManifestHasRootEntry(manifest candidateSourceManifest, name string) bool {
	for _, entry := range manifest.Entries {
		if entry.Path == name {
			return true
		}
	}
	return false
}

func checkResultForEnvironment(environment ports.ClearDevCheckEnvironment) ports.ClearDevCheckResult {
	return ports.ClearDevCheckResult{
		Outcome: ports.ClearDevCheckInfraError, CandidateSHA: environment.CandidateSHA, Image: environment.Image,
		SourceManifestID: environment.SourceManifestID, SourceRootTreeOID: environment.SourceRootTreeOID,
		SourceBytes: environment.SourceBytes, SourceItems: environment.SourceItems,
		ImageID: environment.ImageID, ApprovedArgvSHA256: environment.ApprovedArgvSHA256,
		CheckEnvironmentID: environment.CheckEnvironmentID,
		PackageJSONSHA256:  environment.PackageJSONSHA256, PackageLockSHA256: environment.PackageLockSHA256,
		DependencyCacheKey: environment.DependencyCacheKey, DependencyEnvironmentID: environment.DependencyEnvironmentID, DependencyTreeSHA256: environment.DependencyTreeSHA256,
		DependencyBytes: environment.DependencyBytes, DependencyItems: environment.DependencyItems,
		NodeVersion: environment.NodeVersion, NPMVersion: environment.NPMVersion, ProjectExecutionSHA256: environment.ProjectExecutionSHA256,
		ExitCode: -1,
	}
}

func checkEnvironmentForResult(result ports.ClearDevCheckResult) ports.ClearDevCheckEnvironment {
	return ports.ClearDevCheckEnvironment{
		CandidateSHA: result.CandidateSHA, SourceManifestID: result.SourceManifestID,
		SourceRootTreeOID: result.SourceRootTreeOID, SourceBytes: result.SourceBytes, SourceItems: result.SourceItems,
		Image: result.Image, ImageID: result.ImageID, ApprovedArgvSHA256: result.ApprovedArgvSHA256,
		CheckEnvironmentID: result.CheckEnvironmentID, PackageJSONSHA256: result.PackageJSONSHA256,
		PackageLockSHA256: result.PackageLockSHA256, DependencyCacheKey: result.DependencyCacheKey,
		DependencyEnvironmentID: result.DependencyEnvironmentID, DependencyTreeSHA256: result.DependencyTreeSHA256,
		DependencyBytes: result.DependencyBytes, DependencyItems: result.DependencyItems,
		NodeVersion: result.NodeVersion, NPMVersion: result.NPMVersion, ProjectExecutionSHA256: result.ProjectExecutionSHA256,
	}
}

func validDockerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, char := range id {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func gitOutput(ctx context.Context, workspacePath string, args ...string) ([]byte, error) {
	argv := append([]string{"-C", workspacePath}, args...)
	return exec.CommandContext(ctx, "git", argv...).Output() //nolint:gosec // fixed executable and direct argv.
}

func validCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func parseNameStatusZ(raw []byte) ([]ports.ClearDevDiffPath, error) {
	if len(raw) == 0 {
		return []ports.ClearDevDiffPath{}, nil
	}
	parts := bytes.Split(raw, []byte{0})
	if len(parts) == 0 || len(parts[len(parts)-1]) != 0 {
		return nil, errors.New("name-status output is not NUL terminated")
	}
	parts = parts[:len(parts)-1]
	paths := make([]ports.ClearDevDiffPath, 0, len(parts)/2)
	for index := 0; index < len(parts); {
		status := string(parts[index])
		index++
		if status == "" || index >= len(parts) {
			return nil, errors.New("name-status record is incomplete")
		}
		if status[0] == 'R' || status[0] == 'C' {
			if index+1 >= len(parts) {
				return nil, errors.New("rename record is incomplete")
			}
			oldPath, newPath := string(parts[index]), string(parts[index+1])
			index += 2
			if !validGitPath(oldPath) || !validGitPath(newPath) {
				return nil, errors.New("rename contains an invalid path")
			}
			paths = append(paths, ports.ClearDevDiffPath{Status: status, OldPath: oldPath, Path: newPath})
			continue
		}
		path := string(parts[index])
		index++
		if !validGitPath(path) {
			return nil, errors.New("diff contains an invalid path")
		}
		paths = append(paths, ports.ClearDevDiffPath{Status: status, Path: path})
	}
	return paths, nil
}

func validGitPath(path string) bool {
	return path != "" && utf8.ValidString(path) && !strings.ContainsRune(path, '\x00')
}

type outputCollector struct {
	mu            sync.Mutex
	buffer        bytes.Buffer
	hash          hash.Hash
	limit         int
	truncated     bool
	limitExceeded bool
	onLimit       func()
}

func newOutputCollector(limit int, onLimit func()) *outputCollector {
	return &outputCollector{hash: sha256.New(), limit: limit, onLimit: onLimit}
}

func (c *outputCollector) Write(value []byte) (int, error) {
	c.mu.Lock()
	if c.limitExceeded {
		c.mu.Unlock()
		return len(value), nil
	}
	remaining := c.limit - c.buffer.Len()
	if remaining > 0 {
		chunk := value
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		_, _ = c.buffer.Write(chunk)
		_, _ = c.hash.Write(chunk)
	}
	var onLimit func()
	if len(value) > remaining {
		c.truncated = true
		c.limitExceeded = true
		onLimit = c.onLimit
	}
	c.mu.Unlock()
	if onLimit != nil {
		onLimit()
	}
	return len(value), nil
}

func (c *outputCollector) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buffer.String()
}

func (c *outputCollector) SHA256() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("%x", c.hash.Sum(nil))
}

func (c *outputCollector) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncated
}

func (c *outputCollector) LimitExceeded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limitExceeded
}

func injectTestInfrastructureFailure(request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, bool) {
	if !request.AllowInfraInject {
		return ports.ClearDevCheckResult{}, false
	}
	path := strings.TrimSpace(os.Getenv("AO_CLEARDEV_TEST_INJECT_INFRA_FILE"))
	if path == "" {
		return ports.ClearDevCheckResult{}, false
	}
	if _, err := os.Stat(path); err == nil {
		return ports.ClearDevCheckResult{}, false
	} else if !os.IsNotExist(err) {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1, OutputSummary: "INJECTED_INFRASTRUCTURE_FAILURE"}, true
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1, OutputSummary: "INJECTED_INFRASTRUCTURE_FAILURE"}, true
	}
	if err := os.WriteFile(path, []byte("INJECTED_INFRASTRUCTURE_FAILURE\n"), 0o600); err != nil {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1, OutputSummary: "INJECTED_INFRASTRUCTURE_FAILURE"}, true
	}
	return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1, OutputSummary: "INJECTED_INFRASTRUCTURE_FAILURE"}, true
}

// RunGeneratedProof rebuilds frozen generated files in a disposable checkout
// and compares each content hash to the candidate.
func (r *Runner) RunGeneratedProof(ctx context.Context, request ports.ClearDevGeneratedProofRequest) (ports.ClearDevGeneratedProofResult, error) {
	request, err := normalizeGeneratedProofRequest(request)
	if err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckFail, ReasonCode: "GENERATED_PROOF_MISSING"}, err
	}
	if request.RunID == "" {
		return r.runGeneratedProof(ctx, request)
	}
	return r.runIdempotentGeneratedProof(ctx, request)
}

func (r *Runner) runIdempotentGeneratedProof(ctx context.Context, request ports.ClearDevGeneratedProofRequest) (ports.ClearDevGeneratedProofResult, error) {
	fingerprint, err := generatedProofRequestSHA256(request)
	if err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ReasonCode: "CHECKER_UNAVAILABLE"}, fmt.Errorf("encode ClearDev generated-proof request: %w", err)
	}
	statePath, err := generatedProofRunStatePath(request.RunID)
	if err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ReasonCode: "CHECKER_UNAVAILABLE"}, err
	}

	started := generatedProofRunState{Version: 1, Fingerprint: fingerprint, State: "started"}
	created, state, err := createOrReadGeneratedProofRunState(statePath, started)
	if err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ReasonCode: "CHECKER_UNAVAILABLE"}, err
	}
	if !created {
		if state.Version != 1 || state.Fingerprint != fingerprint {
			return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ReasonCode: "CHECKER_UNAVAILABLE"}, errors.New("ClearDev generated-proof RunID is already bound to a different request")
		}
		switch state.State {
		case "settled":
			if state.Result == nil {
				return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ReasonCode: "CHECKER_UNAVAILABLE"}, errors.New("ClearDev generated-proof RunID has an invalid settled result")
			}
			if state.Error != "" {
				return *state.Result, errors.New(state.Error)
			}
			return *state.Result, nil
		case "started":
			// The process may have stopped after the Docker action was marked
			// started. There is no safe way to prove that action did not reach
			// Docker, so fail closed and do not invoke the generator again.
			return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ReasonCode: "CHECKER_UNAVAILABLE"}, errors.New("ClearDev generated-proof RunID has an unsettled external action")
		default:
			return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ReasonCode: "CHECKER_UNAVAILABLE"}, errors.New("ClearDev generated-proof RunID has an invalid state")
		}
	}

	result, runErr := r.runGeneratedProof(ctx, request)
	settled := generatedProofRunState{Version: 1, Fingerprint: fingerprint, State: "settled", Result: &result}
	if runErr != nil {
		settled.Error = runErr.Error()
	}
	if err := writeGeneratedProofRunState(statePath, settled); err != nil {
		// Keep the durable state as started if settling fails. Retrying the
		// same RunID must not risk a second Docker invocation.
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ReasonCode: "CHECKER_UNAVAILABLE"}, fmt.Errorf("settle ClearDev generated-proof RunID: %w", err)
	}
	return result, runErr
}

func normalizeGeneratedProofRequest(request ports.ClearDevGeneratedProofRequest) (ports.ClearDevGeneratedProofRequest, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.WorkspacePath = filepath.Clean(strings.TrimSpace(request.WorkspacePath))
	request.CandidateSHA = strings.TrimSpace(request.CandidateSHA)
	request.Image = strings.TrimSpace(request.Image)
	if request.RunID != "" && !validRunID(request.RunID) {
		return ports.ClearDevGeneratedProofRequest{}, errors.New("invalid ClearDev generated-proof RunID")
	}
	if request.WorkspacePath == "." || request.WorkspacePath == "" || !validCommit(request.CandidateSHA) || request.Image == "" || len(request.Argv) == 0 || len(request.OutputPaths) == 0 {
		return ports.ClearDevGeneratedProofRequest{}, errors.New("invalid generated proof request")
	}
	if request.Timeout <= 0 {
		request.Timeout = 60 * time.Second
	}
	if request.MemoryBytes <= 0 {
		request.MemoryBytes = 256 * 1024 * 1024
	}
	if request.PidsLimit <= 0 {
		request.PidsLimit = 64
	}
	return request, nil
}

func generatedProofRequestSHA256(request ports.ClearDevGeneratedProofRequest) (string, error) {
	payload, err := json.Marshal(generatedProofRequestFingerprint{
		WorkspacePath: request.WorkspacePath,
		CandidateSHA:  request.CandidateSHA,
		Image:         request.Image,
		Argv:          request.Argv,
		OutputPaths:   request.OutputPaths,
		Timeout:       request.Timeout,
		MemoryBytes:   request.MemoryBytes,
		PidsLimit:     request.PidsLimit,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func (r *Runner) runGeneratedProof(ctx context.Context, request ports.ClearDevGeneratedProofRequest) (result ports.ClearDevGeneratedProofResult, retErr error) {
	imageOutput, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", request.Image).CombinedOutput() //nolint:gosec // image is a fixed approved generated-command value.
	if err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ReasonCode: "CHECKER_UNAVAILABLE"}, fmt.Errorf("inspect generated-proof image: %w: %s", err, strings.TrimSpace(string(imageOutput)))
	}
	imageID := strings.TrimSpace(string(imageOutput))
	actionID := request.RunID
	if actionID == "" {
		actionID = "generated-proof:" + request.CandidateSHA + ":" + newOpaqueCheckID()
	}
	temporary, err := acquireCheckTemporary(ctx, actionID, "generated-proof", checkRunReservationBytes)
	if err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ImageID: imageID, ReasonCode: "CHECKER_UNAVAILABLE"}, err
	}
	released := false
	defer func() {
		if !released {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), CheckCleanupTimeout)
			cleanupErr := temporary.Release(cleanupCtx)
			cancel()
			if cleanupErr != nil {
				result = ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ImageID: imageID, ReasonCode: "CHECKER_UNAVAILABLE"}
				if retErr != nil {
					retErr = errors.Join(retErr, fmt.Errorf("clean generated-proof environment: %w", cleanupErr))
				} else {
					retErr = fmt.Errorf("clean generated-proof environment: %w", cleanupErr)
				}
			}
		}
	}()
	sourceDirectory := filepath.Join(temporary.Root(), "candidate")
	facts, err := materializeCandidateSource(ctx, request.WorkspacePath, request.CandidateSHA, filepath.Join(temporary.Root(), "git"), sourceDirectory, filepath.Join(temporary.Root(), "candidate-manifest.json"), defaultCandidateSourceLimits())
	if err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ImageID: imageID, ReasonCode: "CHECKER_UNAVAILABLE"}, fmt.Errorf("materialize generated-proof candidate: %w", err)
	}
	expected := map[string]string{}
	for _, rel := range request.OutputPaths {
		clean, ok := generatedProofRelPath(rel)
		if !ok {
			return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckFail, ImageID: imageID, ReasonCode: "GENERATED_PROOF_MISMATCH"}, errors.New("generated path is invalid")
		}
		if _, duplicate := expected[clean]; duplicate {
			return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckFail, ImageID: imageID, ReasonCode: "GENERATED_PROOF_MISMATCH"}, errors.New("generated path is duplicated")
		}
		entry, found := candidateManifestEntry(facts.Manifest, clean)
		if !found || (entry.Mode != "100644" && entry.Mode != "100755") {
			return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckFail, ImageID: imageID, ReasonCode: "GENERATED_PROOF_MISSING"}, errors.New("generated output is missing from the exact candidate manifest")
		}
		expected[clean] = entry.ContentSHA256
	}
	containerRequest := CheckContainerRequest{
		Image: imageID, Argv: request.Argv, SourceDir: sourceDirectory,
		SourceBytes: facts.Manifest.BlobBytes, SourceItems: int64(facts.Manifest.ItemCount), CIDFile: temporary.CIDFile(),
		Timeout: request.Timeout, MemoryBytes: request.MemoryBytes, PidsLimit: request.PidsLimit, OutputLimit: 64 * 1024,
	}
	if _, err := CheckContainerRunArgs(containerRequest, temporary.CIDFile()); err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ImageID: imageID, ReasonCode: "CHECKER_UNAVAILABLE"}, err
	}
	containerID, err := startCheckContainer(ctx, containerRequest, temporary.CIDFile())
	if err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ImageID: imageID, ReasonCode: "CHECKER_UNAVAILABLE"}, err
	}
	if err := copyCheckSource(ctx, sourceDirectory, containerID); err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ImageID: imageID, ReasonCode: "CHECKER_UNAVAILABLE"}, err
	}
	if output, err := exec.CommandContext(ctx, "docker", "exec", "--user=0:0", containerID, "chmod", "-R", "a+rwX", "--", "/workspace").CombinedOutput(); err != nil { //nolint:gosec // fixed trusted preparation command.
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ImageID: imageID, ReasonCode: "CHECKER_UNAVAILABLE"}, fmt.Errorf("open generated-proof workspace: %w: %s", err, strings.TrimSpace(string(output)))
	}
	for clean := range expected {
		path := "/workspace/" + clean
		if output, err := exec.CommandContext(ctx, "docker", "exec", "--user=0:0", containerID, "rm", "-f", "--", path).CombinedOutput(); err != nil { //nolint:gosec // validated manifest path and direct argv.
			return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ImageID: imageID, ReasonCode: "CHECKER_UNAVAILABLE"}, fmt.Errorf("remove generated-proof output: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	commandResult, runErr := runGeneratedProofContainerCommand(ctx, containerID, request)
	if runErr != nil {
		if commandResult.InfraError {
			return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ImageID: imageID, ReasonCode: "CHECKER_UNAVAILABLE"}, runErr
		}
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckFail, ImageID: imageID, ReasonCode: "GENERATED_PROOF_MISMATCH"}, runErr
	}
	got := map[string]string{}
	outputCounter, _ := newCheckUsageCounter(productionCheckCapacityLimits().Output)
	for clean := range expected {
		digest, err := hashGeneratedProofContainerFile(ctx, containerID, clean, outputCounter)
		if err != nil {
			return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckFail, ImageID: imageID, ReasonCode: "GENERATED_PROOF_MISSING"}, err
		}
		got[clean] = digest
	}
	payload, _ := json.Marshal(got)
	for path, hash := range expected {
		if got[path] != hash {
			return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckFail, ImageID: imageID, OutputSHA256JSON: string(payload), ReasonCode: "GENERATED_PROOF_MISMATCH"}, nil
		}
	}
	if len(got) != len(expected) {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckFail, ImageID: imageID, OutputSHA256JSON: string(payload), ReasonCode: "GENERATED_PROOF_MISMATCH"}, nil
	}
	if err := temporary.Release(context.WithoutCancel(ctx)); err != nil {
		return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckInfraError, ImageID: imageID, ReasonCode: "CHECKER_UNAVAILABLE"}, err
	}
	released = true
	return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckPass, ImageID: imageID, OutputSHA256JSON: string(payload)}, nil
}

func candidateManifestEntry(manifest candidateSourceManifest, path string) (candidateSourceManifestEntry, bool) {
	for _, entry := range manifest.Entries {
		if entry.Path == path {
			return entry, true
		}
	}
	return candidateSourceManifestEntry{}, false
}

func runGeneratedProofContainerCommand(ctx context.Context, containerID string, request ports.ClearDevGeneratedProofRequest) (CheckContainerResult, error) {
	checkCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	runCtx, cancelRun := context.WithCancel(checkCtx)
	collector := newCheckContainerOutputCollector(64*1024, cancelRun)
	args, err := CheckContainerExecArgs(containerID, request.Argv)
	if err != nil {
		return infraCheckContainerResult(), err
	}
	command := exec.CommandContext(runCtx, "docker", args...) //nolint:gosec // direct validated argv.
	command.Stdout, command.Stderr = collector, collector
	events := make(chan checkContainerCapacityEvent, 1)
	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	var wait sync.WaitGroup
	wait.Add(1)
	go monitorCheckContainerCapacity(monitorCtx, containerID, cancelRun, events, &wait)
	runErr := command.Run()
	stopMonitor()
	wait.Wait()
	cancelRun()
	result := CheckContainerResult{Output: collector.String(), OutputSHA256: collector.SHA256(), OutputCapped: collector.Exceeded()}
	select {
	case event := <-events:
		result = infraCheckContainerResult()
		result.CapacityFull = event.Full
		if event.Err != nil {
			return result, event.Err
		}
		return result, errors.New("generated-proof filesystem capacity exhausted")
	default:
	}
	if collector.Exceeded() {
		result.Outcome, result.ExitCode = CheckContainerFail, -1
		return result, errors.New("generated-proof output exceeded its fixed limit")
	}
	if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
		result.Outcome, result.ExitCode, result.TimedOut = CheckContainerTimedOut, -1, true
		return result, errors.New("generated-proof command timed out")
	}
	if runErr == nil {
		result.Outcome = CheckContainerPass
		return result, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		return infraCheckContainerResult(), runErr
	}
	result.Outcome, result.ExitCode = CheckContainerFail, exitErr.ExitCode()
	return result, runErr
}

func hashGeneratedProofContainerFile(ctx context.Context, containerID, relative string, counter *checkUsageCounter) (string, error) {
	// docker cp cannot read the writable workspace tmpfs. The path was already
	// validated as a relative manifest path; prefixing it with ./ also prevents
	// tar option parsing.
	command := exec.CommandContext(ctx, "docker", "exec", containerID, "tar", "-C", "/workspace", "-cf", "-", "./"+relative) //nolint:gosec // validated manifest path and container id.
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	collector := newOutputCollector(checkPreparationOutputLimit, func() {})
	command.Stderr = collector
	if err := command.Start(); err != nil {
		return "", err
	}
	reader := tar.NewReader(stdout)
	digest := sha256.New()
	regularFiles := 0
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return "", nextErr
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != 0 {
			_ = command.Process.Kill()
			_ = command.Wait()
			return "", errors.New("generated-proof output is not a regular file")
		}
		regularFiles++
		if regularFiles != 1 || header.Size < 0 {
			_ = command.Process.Kill()
			_ = command.Wait()
			return "", errors.New("generated-proof output archive is invalid")
		}
		if err := counter.ChargeRegularFile(header.Size); err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return "", err
		}
		if _, err := io.CopyN(digest, reader, header.Size); err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return "", err
		}
	}
	if err := command.Wait(); err != nil {
		return "", fmt.Errorf("copy generated-proof output: %w: %s", err, collector.String())
	}
	if regularFiles != 1 {
		return "", errors.New("generated-proof output is missing")
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func generatedProofRelPath(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || !utf8.ValidString(value) {
		return "", false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "", false
		}
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	clean := strings.Join(parts, "/")
	return clean, true
}

// ValidateRecoveryWorkspace only inspects; refusal never cleans or removes a worktree.
func (r *Runner) ValidateRecoveryWorkspace(ctx context.Context, path, branch, expectedSHA string) error {
	if r.managedRoot == "" || !filepath.IsAbs(path) || branch == "" || !validCommit(expectedSHA) {
		return ports.ErrClearDevCandidateInvalid
	}
	root, err := filepath.EvalSymlinks(r.managedRoot)
	if err != nil {
		return err
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, actual)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || actual != filepath.Clean(path) {
		return ports.ErrClearDevCandidateInvalid
	}
	top, err := gitOutput(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(top)) != actual {
		return ports.ErrClearDevCandidateInvalid
	}
	listed, err := gitOutput(ctx, path, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	if !strings.Contains("\n"+string(listed), "\nworktree "+actual+"\n") {
		return ports.ErrClearDevCandidateInvalid
	}
	status, err := gitOutput(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return ports.ErrClearDevWorkspaceDirty
	}
	head, err := gitOutput(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	current, err := gitOutput(ctx, path, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != expectedSHA || strings.TrimSpace(string(current)) != branch {
		return ports.ErrClearDevCandidateInvalid
	}
	return nil
}

// Git's -z status reports literal names. Only the untracked marker is skipped;
// an index edit, tracked dependency, rename or root symlink remains a failure.
func onlyUntrackedProjectDependencies(workspace string, status []byte) bool {
	if len(status) == 0 {
		return true
	}
	directory, err := os.Lstat(filepath.Join(workspace, "node_modules"))
	if err != nil || !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 {
		return false
	}
	for _, entry := range strings.Split(strings.TrimSuffix(string(status), "\x00"), "\x00") {
		if !strings.HasPrefix(entry, "?? node_modules/") {
			return false
		}
	}
	return true
}
