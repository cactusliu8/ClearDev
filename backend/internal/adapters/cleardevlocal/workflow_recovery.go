package cleardevlocal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CanRetryWorkflowCheck accepts only the exact executor's settled infrastructure
// failure. A database failure caused by an unknown Docker action is insufficient.
func (r *Runner) CanRetryWorkflowCheck(ctx context.Context, request ports.ClearDevCheckRequest) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if request.ProjectExecution == nil || request.RunID == "" {
		return false, nil
	}
	request, err := normalizeCheckRequest(request)
	if err != nil {
		return false, err
	}
	path, err := checkRunStatePath(request.RunID)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var state checkRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		return false, err
	}
	fingerprint, err := checkReceiptFingerprint(request, state)
	if err != nil {
		return false, err
	}
	if state.Version != checkRunStateVersion || state.Fingerprint != fingerprint || state.State != "settled" || state.Result == nil {
		return false, nil
	}
	terminated := state.Result.Outcome == ports.ClearDevCheckFail && state.Result.ExitCode == 137 && !state.Result.TimedOut && !state.Result.OutputTruncated && state.Result.Image == projectInstallImage && state.Result.TrialCommandExecuted && state.SourceManifest != nil && state.Result.SourceManifestID != ""
	if state.Result.Outcome != ports.ClearDevCheckInfraError && !terminated {
		return false, nil
	}
	if state.Result.CandidateSHA != "" && state.Result.CandidateSHA != request.CandidateSHA {
		return false, nil
	}
	if state.Result.SourceManifestID != "" {
		if state.SourceManifest == nil {
			return false, nil
		}
		if err := verifySavedCandidateManifest(*state.SourceManifest, checkEnvironmentForResult(*state.Result)); err != nil {
			return false, err
		}
	}
	return workflowCheckResourcesReleased(ctx, request.RunID)
}

// A settled error does not prove Docker stopped. Outstanding reservations and
// leases survive failed cleanup and therefore veto a new RunID.
func workflowCheckResourcesReleased(ctx context.Context, runID string) (bool, error) {
	root, err := checkTemporaryRoot()
	if err != nil {
		return false, err
	}
	unlock, err := lockCheckFile(ctx, filepath.Join(root, checkTemporaryLockName))
	if err != nil {
		return false, err
	}
	index, err := (&checkTemporaryManager{root: root}).readIndex()
	unlock()
	if err != nil {
		return false, err
	}
	for _, reservation := range index.Reservations {
		if reservation.RunID == runID || (reservation.RunID == "" && (reservation.Kind == "executable-probe" || reservation.Kind == "dependency-preparation")) {
			return false, nil
		}
	}
	for _, kind := range []string{"candidate-check", "generated-proof"} {
		if _, ok := index.Reservations[cacheHolderID(runID+"\x00"+kind)]; ok {
			return false, nil
		}
	}
	cacheRoot, err := checkDependencyRoot()
	if err != nil {
		return false, err
	}
	unlockCache, err := lockCheckFile(ctx, filepath.Join(cacheRoot, checkDependencyCacheLockName))
	if err != nil {
		return false, err
	}
	defer unlockCache()
	cacheIndex, err := (&dependencyCacheManager{root: cacheRoot}).readIndex()
	if err != nil {
		return false, err
	}
	_, held := cacheIndex.Leases[cacheHolderID(runID+":dependency-lease")]
	return !held, nil
}
