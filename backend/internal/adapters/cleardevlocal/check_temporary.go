package cleardevlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CheckCleanupTimeout bounds container, volume and abandoned-resource cleanup.
const CheckCleanupTimeout = 10 * time.Minute

const (
	checkTemporaryDirectory      = "cleardev-check-temporary"
	checkTemporaryIndexVersion   = 1
	checkTemporaryIndexName      = "index.json"
	checkTemporaryLockName       = "index.lock"
	checkProbeReservationBytes   = int64(16 * 1024 * 1024)
	checkSourceReservationBytes  = checkSourceBytesLimit
	checkRunReservationBytes     = checkSourceBytesLimit + checkSourceBytesLimit + checkOutputBytesLimit + checkProbeReservationBytes
	checkPrepareReservationBytes = checkDependencyBytesLimit + checkDependencyBytesLimit
)

type checkTemporaryIndex struct {
	Version      int                                  `json:"version"`
	Reservations map[string]checkTemporaryReservation `json:"reservations"`
}

type checkTemporaryReservation struct {
	RunID      string `json:"runId,omitempty"`
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Bytes      int64  `json:"bytes"`
	Directory  string `json:"directory"`
	CIDFile    string `json:"cidFile"`
	OwnerPID   int    `json:"ownerPid"`
	OwnerMark  string `json:"ownerMark"`
	OwnerStart string `json:"ownerStart"`
}

type checkTemporaryLease struct {
	manager *checkTemporaryManager
	id      string
	root    string
	cidFile string
}

type checkTemporaryManager struct {
	root string
}

// RecoverCheckResources resumes existing cleanup for dead owners on daemon startup.
// Live owners and unconfirmed Docker resources retain their reservations.
func (r *Runner) RecoverCheckResources(ctx context.Context) error {
	root, err := checkTemporaryRoot()
	if err != nil {
		return err
	}
	manager := &checkTemporaryManager{root: root}
	return manager.withIndex(ctx, func(index *checkTemporaryIndex) error {
		return manager.recover(ctx, index)
	})
}

func acquireCheckTemporary(ctx context.Context, actionID, kind string, bytes int64, runIDs ...string) (*checkTemporaryLease, error) {
	if strings.TrimSpace(actionID) == "" {
		actionID = newOpaqueCheckID()
	}
	if !validRunID(actionID) || strings.TrimSpace(kind) == "" || bytes <= 0 || bytes > productionCheckCapacityLimits().ActiveTemporaryBytes {
		return nil, errors.New("invalid ClearDev temporary reservation")
	}
	runID := ""
	if len(runIDs) > 0 {
		runID = runIDs[0]
		if runID != "" && !validRunID(runID) {
			return nil, errors.New("invalid temporary parent RunID")
		}
	}
	root, err := checkTemporaryRoot()
	if err != nil {
		return nil, err
	}
	manager := &checkTemporaryManager{root: root}
	id := cacheHolderID(actionID + "\x00" + kind)
	var lease *checkTemporaryLease
	err = manager.withIndex(ctx, func(index *checkTemporaryIndex) error {
		if err := manager.recover(ctx, index); err != nil {
			return err
		}
		if _, exists := index.Reservations[id]; exists {
			return errors.New("ClearDev temporary reservation is already active")
		}
		var used int64
		for _, reservation := range index.Reservations {
			if reservation.Bytes > productionCheckCapacityLimits().ActiveTemporaryBytes-used {
				return errors.New("ClearDev temporary index exceeds its fixed total")
			}
			used += reservation.Bytes
		}
		if bytes > productionCheckCapacityLimits().ActiveTemporaryBytes-used {
			return fmt.Errorf("%w: active temporary areas are at their fixed total", errCheckCapacityExceeded)
		}
		available, err := checkAvailableBytes(root)
		if err != nil {
			return fmt.Errorf("inspect ClearDev temporary filesystem capacity: %w", err)
		}
		if required := checkTemporaryHostBytes(kind); available < required {
			return fmt.Errorf("%w: temporary filesystem cannot reserve %d bytes", errCheckCapacityExceeded, required)
		}
		directoryName := "action-" + id
		directory := filepath.Join(root, directoryName)
		if err := os.Mkdir(directory, 0o700); err != nil {
			return fmt.Errorf("create ClearDev temporary directory: %w", err)
		}
		cidFile := filepath.Join(directory, "container.cid")
		index.Reservations[id] = checkTemporaryReservation{
			ID: id, Kind: kind, Bytes: bytes, Directory: directoryName, CIDFile: cidFile, RunID: runID,
			OwnerPID: os.Getpid(), OwnerMark: checkCacheOwnerMark,
			OwnerStart: checkProcessIdentity(os.Getpid()),
		}
		lease = &checkTemporaryLease{manager: manager, id: id, root: directory, cidFile: cidFile}
		return nil
	})
	return lease, err
}

func (l *checkTemporaryLease) Root() string {
	if l == nil {
		return ""
	}
	return l.root
}

func (l *checkTemporaryLease) CIDFile() string {
	if l == nil {
		return ""
	}
	return l.cidFile
}

func (l *checkTemporaryLease) Release(ctx context.Context) error {
	if l == nil || l.manager == nil || l.id == "" {
		return nil
	}
	err := l.manager.withIndex(ctx, func(index *checkTemporaryIndex) error {
		reservation, ok := index.Reservations[l.id]
		if !ok {
			return nil
		}
		if err := removeCheckContainerAndConfirm(ctx, reservation.CIDFile); err != nil {
			return err
		}
		cleanupPreparedTree(filepath.Join(l.manager.root, reservation.Directory))
		if checkPathExists(filepath.Join(l.manager.root, reservation.Directory)) {
			return errors.New("ClearDev temporary directory could not be removed")
		}
		delete(index.Reservations, l.id)
		return nil
	})
	if err == nil {
		l.id = ""
	}
	return err
}

func checkTemporaryRoot() (string, error) {
	dataDir := strings.TrimSpace(os.Getenv("AO_DATA_DIR"))
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve ClearDev temporary data directory: %w", err)
		}
		dataDir = filepath.Join(home, ".ao")
	}
	root := filepath.Join(filepath.Clean(dataDir), checkTemporaryDirectory)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create ClearDev temporary data directory: %w", err)
	}
	return root, nil
}

func (m *checkTemporaryManager) recover(ctx context.Context, index *checkTemporaryIndex) error {
	for id, reservation := range index.Reservations {
		if reservation.OwnerPID == os.Getpid() && reservation.OwnerMark == checkCacheOwnerMark {
			continue
		}
		if checkProcessAlive(reservation.OwnerPID) && reservation.OwnerStart != "" && checkProcessIdentity(reservation.OwnerPID) == reservation.OwnerStart {
			continue
		}
		if err := removeCheckContainerAndConfirm(ctx, reservation.CIDFile); err != nil {
			return fmt.Errorf("recover stale ClearDev container: %w", err)
		}
		directory := filepath.Join(m.root, reservation.Directory)
		cleanupPreparedTree(directory)
		if checkPathExists(directory) {
			return errors.New("stale ClearDev temporary directory could not be removed")
		}
		delete(index.Reservations, id)
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return fmt.Errorf("scan ClearDev temporary directory: %w", err)
	}
	known := make(map[string]bool, len(index.Reservations))
	for _, reservation := range index.Reservations {
		known[reservation.Directory] = true
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == checkTemporaryIndexName || name == checkTemporaryLockName || known[name] {
			continue
		}
		if !strings.HasPrefix(name, "action-") || !entry.IsDir() {
			return fmt.Errorf("unmanaged path %q exists in the ClearDev temporary root", name)
		}
		directory := filepath.Join(m.root, name)
		cleanupPreparedTree(directory)
		if checkPathExists(directory) {
			return fmt.Errorf("orphan ClearDev temporary directory %q could not be removed", name)
		}
	}
	return nil
}

func (m *checkTemporaryManager) withIndex(ctx context.Context, action func(*checkTemporaryIndex) error) error {
	unlock, err := lockCheckFile(ctx, filepath.Join(m.root, checkTemporaryLockName))
	if err != nil {
		return err
	}
	defer unlock()
	index, err := m.readIndex()
	if err != nil {
		return err
	}
	if err := action(&index); err != nil {
		return err
	}
	return m.writeIndex(index)
}

func (m *checkTemporaryManager) readIndex() (checkTemporaryIndex, error) {
	payload, err := os.ReadFile(filepath.Join(m.root, checkTemporaryIndexName))
	if os.IsNotExist(err) {
		return checkTemporaryIndex{Version: checkTemporaryIndexVersion, Reservations: map[string]checkTemporaryReservation{}}, nil
	}
	if err != nil {
		return checkTemporaryIndex{}, fmt.Errorf("read ClearDev temporary index: %w", err)
	}
	if len(payload) > 4*1024*1024 {
		return checkTemporaryIndex{}, errors.New("ClearDev temporary index is too large")
	}
	var index checkTemporaryIndex
	if err := json.Unmarshal(payload, &index); err != nil {
		return checkTemporaryIndex{}, errors.New("ClearDev temporary index is invalid")
	}
	if err := m.validateIndex(index); err != nil {
		return checkTemporaryIndex{}, err
	}
	return index, nil
}

func (m *checkTemporaryManager) validateIndex(index checkTemporaryIndex) error {
	if index.Version != checkTemporaryIndexVersion || index.Reservations == nil {
		return errors.New("ClearDev temporary index is invalid")
	}
	var total int64
	for id, reservation := range index.Reservations {
		expectedDirectory := "action-" + id
		expectedCID := filepath.Join(m.root, expectedDirectory, "container.cid")
		if !validLowerHex(id, 64) || reservation.ID != id || reservation.Directory != expectedDirectory || filepath.Clean(reservation.CIDFile) != expectedCID || reservation.Bytes <= 0 || reservation.Bytes > checkActiveTemporaryBytesLimit || reservation.OwnerPID <= 0 || reservation.OwnerStart == "" || !validTemporaryKind(reservation.Kind) {
			return errors.New("ClearDev temporary reservation is invalid")
		}
		if info, statErr := os.Lstat(filepath.Join(m.root, expectedDirectory)); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return errors.New("ClearDev temporary reservation directory is not real")
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect ClearDev temporary reservation directory: %w", statErr)
		}
		if reservation.Bytes > checkActiveTemporaryBytesLimit-total {
			return errors.New("ClearDev temporary reservations exceed their fixed total")
		}
		total += reservation.Bytes
	}
	return nil
}

func validTemporaryKind(kind string) bool {
	switch kind {
	case "source-inspection", "executable-probe", "dependency-preparation", "candidate-check", "generated-proof", "project-preview":
		return true
	default:
		return false
	}
}

func checkTemporaryHostBytes(kind string) int64 {
	switch kind {
	case "dependency-preparation":
		return checkDependencyBytesLimit
	case "project-preview":
		return checkSourceBytesLimit + checkDependencyBytesLimit + checkOutputBytesLimit
	case "candidate-check":
		return checkSourceBytesLimit + checkDependencyBytesLimit + checkOutputBytesLimit
	case "source-inspection", "generated-proof":
		return checkSourceBytesLimit
	case "executable-probe":
		return 1024 * 1024
	default:
		return checkActiveTemporaryBytesLimit
	}
}

func (m *checkTemporaryManager) writeIndex(index checkTemporaryIndex) error {
	if err := m.validateIndex(index); err != nil {
		return err
	}
	payload, err := json.Marshal(index)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(m.root, ".temporary-index-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(m.root, checkTemporaryIndexName))
}

func removeCheckContainerAndConfirm(ctx context.Context, cidFile string) error {
	raw, err := os.ReadFile(cidFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read ClearDev container id: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	if !validDockerID(id) {
		return errors.New("ClearDev container id is invalid")
	}
	removeCtx, cancel := context.WithTimeout(ctx, CheckCleanupTimeout)
	defer cancel()
	volumes, err := retainCheckVolumes(removeCtx, cidFile, id)
	if err != nil {
		return err
	}
	remove := exec.CommandContext(removeCtx, "docker", "rm", "--force", "--volumes", id) //nolint:gosec // exact validated id from the trusted action directory.
	removeOutput, removeErr := remove.CombinedOutput()
	// Docker removal can outlive rm (including a concurrent auto-removal).
	// Only confirmed absence releases resources; an accepted rm is not proof.
	for {
		list := exec.CommandContext(removeCtx, "docker", "ps", "-a", "--no-trunc", "--filter=id="+id, "--format={{.ID}}") //nolint:gosec // exact validated id.
		listOutput, listErr := list.CombinedOutput()
		if listErr != nil {
			return fmt.Errorf("confirm ClearDev container cleanup: %w: %s", listErr, strings.TrimSpace(string(listOutput)))
		}
		if strings.TrimSpace(string(listOutput)) == "" {
			break
		}
		select {
		case <-removeCtx.Done():
			return fmt.Errorf("ClearDev container still exists after cleanup: %w: %s", errors.Join(removeCtx.Err(), removeErr), strings.TrimSpace(string(removeOutput)))
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err := removeRetainedCheckVolumes(removeCtx, volumes); err != nil {
		return err
	}
	if err := os.Remove(cidFile + ".volumes.json"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
