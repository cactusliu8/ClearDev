package cleardevlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	checkExecutionNotStarted       = "NOT_STARTED"
	checkExecutionStartedOrUnknown = "STARTED_OR_UNKNOWN"
)

// Only a new, explicitly classified preparation failure grants one further
// attempt for the same frozen trial. Legacy receipts and ordinary candidate
// checks retain their previous replay/explicit-workflow-recovery semantics.
// The caller holds the RunID file lock through validation and execution.
func canRetryUnstartedTrial(ctx context.Context, request ports.ClearDevCheckRequest, state checkRunState) (bool, error) {
	if request.TrialStepID == "" || request.ProjectExecution == nil || state.Attempt != 1 ||
		state.ExecutionState != checkExecutionNotStarted || state.State != "settled" || state.Error == "" ||
		state.Result == nil || state.Result.Outcome != ports.ClearDevCheckInfraError ||
		state.Result.TrialCommandExecuted || state.Result.TimedOut || state.Result.TrialArtifactsJSON != "" {
		return false, nil
	}
	if state.Result.CandidateSHA != "" && state.Result.CandidateSHA != request.CandidateSHA {
		return false, nil
	}
	return trialPreparationResourcesReleased(ctx, request.RunID)
}

// Preparation can fail before its public result contains a dependency key.
// The ordinary recovery guard checks execution leases, but a same-RunID retry
// must also prove cache preparation reservations and durable references gone.
// Inspect only: recovering or clearing the cache here would erase the evidence
// being used to decide whether another preparation attempt is safe.
func trialPreparationResourcesReleased(ctx context.Context, runID string) (bool, error) {
	released, err := workflowCheckResourcesReleased(ctx, runID)
	if err != nil || !released {
		return released, err
	}
	root, err := checkDependencyRoot()
	if err != nil {
		return false, err
	}
	unlock, err := lockCheckFile(ctx, filepath.Join(root, checkDependencyCacheLockName))
	if err != nil {
		return false, err
	}
	defer unlock()
	index, err := (&dependencyCacheManager{root: root}).readIndex()
	if err != nil {
		return false, err
	}
	ref := cacheHolderID(runID)
	for _, reservation := range index.Reservations {
		if reservation.Ref == ref {
			return false, fmt.Errorf("dependency cache preparation cleanup is incomplete (state %s)", reservation.State)
		}
	}
	if _, held := index.References[ref]; held {
		return false, errors.New("dependency cache preparation reference has not been released")
	}
	return true, nil
}

// Hard-link the exact first receipt before replacing the current state. State
// writes always replace the inode, so this audit copy is immutable. A crash
// after archiving but before claiming attempt 2 may reuse only identical bytes.
func archiveUnstartedCheckAttempt(path string) error {
	archive := path + ".attempt-1"
	if err := os.Link(path, archive); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("preserve unstarted check attempt: %w", err)
		}
		current, err := readJSONStateBytes(path, "ClearDev check RunID")
		if err != nil {
			return err
		}
		prior, err := readJSONStateBytes(archive, "ClearDev check RunID")
		if err != nil {
			return err
		}
		if !bytes.Equal(current, prior) {
			return errors.New("unstarted check attempt conflicts with preserved history")
		}
	}
	return syncCheckRunDirectory(path)
}

// Sync the directory as well as the file: the execution marker and the prior
// receipt must survive a replacement before the candidate can be invoked.
func syncCheckRunDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open check state directory for sync: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync check state directory: %w", err)
	}
	return nil
}
