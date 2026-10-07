package cleardevlocal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	checkDependencyCacheIndexVersion = 1
	checkDependencyCacheIndexName    = ".cache-index.json"
	checkDependencyCacheLockName     = ".cache-index.lock"
)

type dependencyCacheIndex struct {
	Version      int                                   `json:"version"`
	NextUse      uint64                                `json:"nextUse"`
	Entries      map[string]dependencyCacheEntry       `json:"entries"`
	References   map[string]dependencyCacheReference   `json:"references"`
	Leases       map[string]dependencyCacheLease       `json:"leases"`
	Reservations map[string]dependencyCacheReservation `json:"reservations"`
}

type dependencyCacheEntry struct {
	Key       string `json:"key"`
	Bytes     int64  `json:"bytes"`
	Items     int64  `json:"items"`
	LastUse   uint64 `json:"lastUse"`
	Evicting  bool   `json:"evicting,omitempty"`
	TrashName string `json:"trashName,omitempty"`
}

type dependencyCacheReference struct {
	Key string `json:"key"`
}

type dependencyCacheLease struct {
	Key        string `json:"key"`
	OwnerPID   int    `json:"ownerPid"`
	OwnerMark  string `json:"ownerMark"`
	OwnerStart string `json:"ownerStart"`
	CIDFile    string `json:"cidFile,omitempty"`
}

type dependencyCacheReservation struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Ref        string `json:"ref,omitempty"`
	Directory  string `json:"directory"`
	Bytes      int64  `json:"bytes"`
	Items      int64  `json:"items"`
	State      string `json:"state"`
	OwnerPID   int    `json:"ownerPid"`
	OwnerMark  string `json:"ownerMark"`
	OwnerStart string `json:"ownerStart"`
}

type dependencyCacheManager struct {
	root string
}

type dependencyCacheReservationResult struct {
	Hit           bool
	Directory     string
	ReservationID string
	ReferenceID   string
}

var checkCacheOwnerMark = newOpaqueCheckID()

func newOpaqueCheckID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return sha256Hex([]byte(fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())))[:32]
}

func openDependencyCacheManager(ctx context.Context) (*dependencyCacheManager, error) {
	root, err := checkDependencyRoot()
	if err != nil {
		return nil, err
	}
	manager := &dependencyCacheManager{root: root}
	if err := manager.withIndex(ctx, func(index *dependencyCacheIndex) error {
		return manager.recoverIndex(ctx, index)
	}); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *dependencyCacheManager) reserve(ctx context.Context, key, referenceID string) (dependencyCacheReservationResult, error) {
	if !validLowerHex(key, 64) {
		return dependencyCacheReservationResult{}, errors.New("invalid dependency cache key")
	}
	ref := cacheHolderID(referenceID)
	var result dependencyCacheReservationResult
	err := m.withIndex(ctx, func(index *dependencyCacheIndex) error {
		if err := m.recoverIndex(ctx, index); err != nil {
			return err
		}
		if entry, ok := index.Entries[key]; ok {
			if entry.Evicting {
				return errors.New("dependency cache item is being reclaimed")
			}
			if err := m.verifyEntry(key, entry); err != nil {
				return err
			}
			if ref != "" {
				if err := bindCacheReference(index, ref, key); err != nil {
					return err
				}
			}
			entry.LastUse = nextCacheUse(index)
			index.Entries[key] = entry
			result = dependencyCacheReservationResult{Hit: true, Directory: filepath.Join(m.root, key), ReferenceID: ref}
			return nil
		}
		if _, err := os.Lstat(filepath.Join(m.root, key)); err == nil {
			return errors.New("unindexed dependency cache item exists; trusted cleanup is required")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect dependency cache item: %w", err)
		}
		for _, pending := range index.Reservations {
			if pending.Key == key {
				return errors.New("dependency cache item preparation is already active")
			}
		}
		limits := productionCheckCapacityLimits()
		if err := m.makeCapacity(index, limits.Dependency.Bytes); err != nil {
			return err
		}
		available, err := checkAvailableBytes(m.root)
		if err != nil {
			return fmt.Errorf("inspect dependency cache capacity: %w", err)
		}
		if available < limits.Dependency.Bytes {
			return fmt.Errorf("%w: dependency cache filesystem cannot reserve %d bytes", errCheckCapacityExceeded, limits.Dependency.Bytes)
		}
		reservationID := newOpaqueCheckID()
		directory := ".prepare-" + reservationID
		if err := os.Mkdir(filepath.Join(m.root, directory), 0o700); err != nil {
			return fmt.Errorf("create dependency cache preparation directory: %w", err)
		}
		index.Reservations[reservationID] = dependencyCacheReservation{
			ID: reservationID, Key: key, Ref: ref, Directory: directory,
			Bytes: limits.Dependency.Bytes, Items: limits.Dependency.Items, State: "preparing",
			OwnerPID: os.Getpid(), OwnerMark: checkCacheOwnerMark,
			OwnerStart: checkProcessIdentity(os.Getpid()),
		}
		result = dependencyCacheReservationResult{
			Directory: filepath.Join(m.root, directory), ReservationID: reservationID, ReferenceID: ref,
		}
		return nil
	})
	return result, err
}

func (m *dependencyCacheManager) publish(ctx context.Context, reservationID string, usage checkUsage) error {
	limits := productionCheckCapacityLimits()
	if usage.Bytes < 0 || usage.Items < 0 || usage.Bytes > limits.Dependency.Bytes || usage.Items > limits.Dependency.Items {
		return fmt.Errorf("%w: dependency environment exceeds its fixed limit", errCheckCapacityExceeded)
	}
	return m.withIndex(ctx, func(index *dependencyCacheIndex) error {
		pending, ok := index.Reservations[reservationID]
		if !ok || pending.State != "preparing" || pending.OwnerPID != os.Getpid() || pending.OwnerMark != checkCacheOwnerMark {
			return errors.New("dependency cache reservation is missing or not owned by this action")
		}
		if _, exists := index.Entries[pending.Key]; exists {
			return errors.New("dependency cache item was published concurrently")
		}
		pending.State, pending.Bytes, pending.Items = "publishing", usage.Bytes, usage.Items
		index.Reservations[reservationID] = pending
		if err := m.writeIndex(index); err != nil {
			return err
		}
		staging := filepath.Join(m.root, pending.Directory)
		final := filepath.Join(m.root, pending.Key)
		if err := os.Rename(staging, final); err != nil {
			return fmt.Errorf("publish dependency cache item: %w", err)
		}
		index.Entries[pending.Key] = dependencyCacheEntry{
			Key: pending.Key, Bytes: usage.Bytes, Items: usage.Items, LastUse: nextCacheUse(index),
		}
		if pending.Ref != "" {
			if err := bindCacheReference(index, pending.Ref, pending.Key); err != nil {
				return err
			}
		}
		delete(index.Reservations, reservationID)
		return nil
	})
}

func (m *dependencyCacheManager) abort(ctx context.Context, reservationID string) error {
	if reservationID == "" {
		return nil
	}
	return m.withIndex(ctx, func(index *dependencyCacheIndex) error {
		pending, ok := index.Reservations[reservationID]
		if !ok {
			return nil
		}
		if pending.State == "publishing" {
			return errors.New("cannot abort a dependency cache item after publication began")
		}
		cleanupPreparedTree(filepath.Join(m.root, pending.Directory))
		if _, err := os.Lstat(filepath.Join(m.root, pending.Directory)); !os.IsNotExist(err) {
			return errors.New("dependency cache preparation directory could not be removed")
		}
		delete(index.Reservations, reservationID)
		return nil
	})
}

func (m *dependencyCacheManager) ensureReference(ctx context.Context, referenceID, key string) error {
	ref := cacheHolderID(referenceID)
	if ref == "" || !validLowerHex(key, 64) {
		return errors.New("invalid dependency cache reference")
	}
	return m.withIndex(ctx, func(index *dependencyCacheIndex) error {
		entry, ok := index.Entries[key]
		if !ok || entry.Evicting {
			return errors.New("referenced dependency cache item is unavailable")
		}
		if err := m.verifyEntry(key, entry); err != nil {
			return err
		}
		return bindCacheReference(index, ref, key)
	})
}

func (m *dependencyCacheManager) releaseReference(ctx context.Context, referenceID string) error {
	ref := cacheHolderID(referenceID)
	if ref == "" {
		return nil
	}
	return m.withIndex(ctx, func(index *dependencyCacheIndex) error {
		delete(index.References, ref)
		return nil
	})
}

func (m *dependencyCacheManager) acquireLease(ctx context.Context, leaseID, key, cidFile string) error {
	id := cacheHolderID(leaseID)
	if id == "" || !validLowerHex(key, 64) {
		return errors.New("invalid dependency cache lease")
	}
	return m.withIndex(ctx, func(index *dependencyCacheIndex) error {
		entry, ok := index.Entries[key]
		if !ok || entry.Evicting {
			return errors.New("leased dependency cache item is unavailable")
		}
		if existing, ok := index.Leases[id]; ok && existing.Key != key {
			return errors.New("dependency cache lease is already bound to another item")
		}
		index.Leases[id] = dependencyCacheLease{Key: key, OwnerPID: os.Getpid(), OwnerMark: checkCacheOwnerMark, OwnerStart: checkProcessIdentity(os.Getpid()), CIDFile: cidFile}
		return nil
	})
}

func (m *dependencyCacheManager) releaseLease(ctx context.Context, leaseID string) error {
	id := cacheHolderID(leaseID)
	if id == "" {
		return nil
	}
	return m.withIndex(ctx, func(index *dependencyCacheIndex) error {
		delete(index.Leases, id)
		return nil
	})
}

// ReleaseCandidateChecks releases the durable cache reference held by a
// completed or canceled ClearDev run.
func (r *Runner) ReleaseCandidateChecks(ctx context.Context, runID string) error {
	if strings.TrimSpace(runID) == "" || !validRunID(strings.TrimSpace(runID)) {
		return errors.New("invalid ClearDev check reference RunID")
	}
	manager, err := openDependencyCacheManager(ctx)
	if err != nil {
		return err
	}
	return manager.releaseReference(ctx, strings.TrimSpace(runID))
}

func (m *dependencyCacheManager) makeCapacity(index *dependencyCacheIndex, reservationBytes int64) error {
	limits := productionCheckCapacityLimits()
	for cacheReservedBytes(index)+reservationBytes > limits.PersistentBytes {
		candidates := make([]dependencyCacheEntry, 0, len(index.Entries))
		for _, entry := range index.Entries {
			if !entry.Evicting && !cacheKeyHeld(index, entry.Key) {
				candidates = append(candidates, entry)
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].LastUse != candidates[j].LastUse {
				return candidates[i].LastUse < candidates[j].LastUse
			}
			return candidates[i].Key < candidates[j].Key
		})
		if len(candidates) == 0 {
			return fmt.Errorf("%w: dependency cache capacity is held by active references or leases", errCheckCapacityExceeded)
		}
		entry := candidates[0]
		trash := ".trash-" + newOpaqueCheckID()
		entry.Evicting, entry.TrashName = true, trash
		index.Entries[entry.Key] = entry
		if err := m.writeIndex(index); err != nil {
			return err
		}
		final, trashPath := filepath.Join(m.root, entry.Key), filepath.Join(m.root, trash)
		if err := os.Rename(final, trashPath); err != nil {
			return fmt.Errorf("stage dependency cache reclamation: %w", err)
		}
		cleanupPreparedTree(trashPath)
		if _, err := os.Lstat(trashPath); !os.IsNotExist(err) {
			return errors.New("reclaimed dependency cache directory could not be removed")
		}
		delete(index.Entries, entry.Key)
	}
	return nil
}

func (m *dependencyCacheManager) recoverIndex(ctx context.Context, index *dependencyCacheIndex) error {
	for id := range index.References {
		if !validLowerHex(id, 64) {
			return errors.New("dependency cache reference id is invalid")
		}
		statePath := filepath.Join(filepath.Dir(m.root), checkRunStateDirectory, id+".json")
		payload, err := os.ReadFile(statePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect dependency reference check state: %w", err)
		}
		if len(payload) >= 1<<20 {
			continue
		}
		var state struct {
			State string `json:"state"`
		}
		if json.Unmarshal(payload, &state) == nil && state.State == "settled" {
			delete(index.References, id)
		}
	}
	for id, lease := range index.Leases {
		if lease.OwnerMark == checkCacheOwnerMark && lease.OwnerPID == os.Getpid() {
			continue
		}
		if checkProcessAlive(lease.OwnerPID) && lease.OwnerStart != "" && checkProcessIdentity(lease.OwnerPID) == lease.OwnerStart {
			continue
		}
		if lease.CIDFile != "" {
			if err := removeCheckContainerAndConfirm(ctx, lease.CIDFile); err != nil {
				return fmt.Errorf("recover dependency cache lease: %w", err)
			}
		}
		delete(index.Leases, id)
	}
	for id, pending := range index.Reservations {
		if pending.OwnerMark == checkCacheOwnerMark && pending.OwnerPID == os.Getpid() {
			continue
		}
		if checkProcessAlive(pending.OwnerPID) && pending.OwnerStart != "" && checkProcessIdentity(pending.OwnerPID) == pending.OwnerStart {
			continue
		}
		staging := filepath.Join(m.root, pending.Directory)
		final := filepath.Join(m.root, pending.Key)
		if pending.State == "publishing" {
			stagingExists := checkPathExists(staging)
			finalExists := checkPathExists(final)
			switch {
			case !stagingExists && finalExists:
				binding, err := verifyDependencyEnvironment(final, pending.Key)
				if err != nil {
					return fmt.Errorf("recover published dependency environment: %w", err)
				}
				_ = binding
				usage, err := measureCheckTree(final, productionCheckCapacityLimits().Dependency)
				if err != nil || usage.Bytes != pending.Bytes || usage.Items != pending.Items {
					return errors.New("published dependency environment usage does not match its reservation")
				}
				index.Entries[pending.Key] = dependencyCacheEntry{Key: pending.Key, Bytes: usage.Bytes, Items: usage.Items, LastUse: nextCacheUse(index)}
				if pending.Ref != "" {
					if err := bindCacheReference(index, pending.Ref, pending.Key); err != nil {
						return err
					}
				}
				delete(index.Reservations, id)
				continue
			case stagingExists && !finalExists:
				cleanupPreparedTree(staging)
				delete(index.Reservations, id)
				continue
			default:
				return errors.New("dependency cache publication has an ambiguous recovered state")
			}
		}
		cleanupPreparedTree(staging)
		if checkPathExists(staging) {
			return errors.New("stale dependency preparation could not be removed")
		}
		delete(index.Reservations, id)
	}
	for key, entry := range index.Entries {
		if !entry.Evicting {
			continue
		}
		final, trash := filepath.Join(m.root, key), filepath.Join(m.root, entry.TrashName)
		if checkPathExists(final) && !checkPathExists(trash) {
			if err := os.Rename(final, trash); err != nil {
				return fmt.Errorf("resume dependency cache reclamation: %w", err)
			}
		}
		if checkPathExists(trash) {
			cleanupPreparedTree(trash)
		}
		if checkPathExists(final) || checkPathExists(trash) {
			return errors.New("dependency cache reclamation could not finish")
		}
		delete(index.Entries, key)
	}
	return m.recoverOrphanPaths(index)
}

func (m *dependencyCacheManager) recoverOrphanPaths(index *dependencyCacheIndex) error {
	known := map[string]bool{
		checkDependencyCacheIndexName: true,
		checkDependencyCacheLockName:  true,
	}
	for key, entry := range index.Entries {
		known[key] = true
		if entry.Evicting {
			known[entry.TrashName] = true
		}
	}
	for _, pending := range index.Reservations {
		known[pending.Directory] = true
		if pending.State == "publishing" {
			known[pending.Key] = true
		}
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return fmt.Errorf("scan dependency cache root: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if known[name] {
			continue
		}
		path := filepath.Join(m.root, name)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fmt.Errorf("inspect unmanaged dependency cache path %q: %w", name, statErr)
		}
		if strings.HasPrefix(name, ".cache-index-") {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unmanaged dependency cache index path %q is not a regular file", name)
			}
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove stale dependency cache index file %q: %w", name, err)
			}
			continue
		}
		orphanPrepare := strings.HasPrefix(name, ".prepare-") && validLowerHex(strings.TrimPrefix(name, ".prepare-"), 32)
		orphanTrash := strings.HasPrefix(name, ".trash-") && validLowerHex(strings.TrimPrefix(name, ".trash-"), 32)
		if !orphanPrepare && !orphanTrash {
			return fmt.Errorf("unindexed dependency cache path %q requires trusted cleanup", name)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("orphan dependency cache path %q is not a real directory", name)
		}
		cleanupPreparedTree(path)
		if checkPathExists(path) {
			return fmt.Errorf("orphan dependency cache directory %q could not be removed", name)
		}
	}
	return nil
}

func (m *dependencyCacheManager) verifyEntry(key string, entry dependencyCacheEntry) error {
	directory := filepath.Join(m.root, key)
	if _, err := verifyDependencyEnvironment(directory, key); err != nil {
		return fmt.Errorf("referenced dependency environment is damaged: %w", err)
	}
	usage, err := measureCheckTree(directory, productionCheckCapacityLimits().Dependency)
	if err != nil {
		return fmt.Errorf("measure dependency environment: %w", err)
	}
	if usage.Bytes != entry.Bytes || usage.Items != entry.Items {
		return errors.New("dependency environment usage record is damaged")
	}
	return nil
}

func (m *dependencyCacheManager) withIndex(ctx context.Context, action func(*dependencyCacheIndex) error) error {
	unlock, err := lockCheckFile(ctx, filepath.Join(m.root, checkDependencyCacheLockName))
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
	return m.writeIndex(&index)
}

func (m *dependencyCacheManager) readIndex() (dependencyCacheIndex, error) {
	path := filepath.Join(m.root, checkDependencyCacheIndexName)
	payload, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newDependencyCacheIndex(), nil
	}
	if err != nil {
		return dependencyCacheIndex{}, fmt.Errorf("read dependency cache index: %w", err)
	}
	if len(payload) > 16*1024*1024 {
		return dependencyCacheIndex{}, errors.New("dependency cache index is too large")
	}
	var index dependencyCacheIndex
	if err := json.Unmarshal(payload, &index); err != nil {
		return dependencyCacheIndex{}, fmt.Errorf("decode dependency cache index: %w", err)
	}
	if err := validateDependencyCacheIndex(index); err != nil {
		return dependencyCacheIndex{}, err
	}
	if err := m.validateIndexPaths(index); err != nil {
		return dependencyCacheIndex{}, err
	}
	return index, nil
}

func (m *dependencyCacheManager) writeIndex(index *dependencyCacheIndex) error {
	if err := validateDependencyCacheIndex(*index); err != nil {
		return err
	}
	if err := m.validateIndexPaths(*index); err != nil {
		return err
	}
	payload, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("encode dependency cache index: %w", err)
	}
	path := filepath.Join(m.root, checkDependencyCacheIndexName)
	file, err := os.CreateTemp(m.root, ".cache-index-")
	if err != nil {
		return fmt.Errorf("create dependency cache index: %w", err)
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write dependency cache index: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync dependency cache index: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish dependency cache index: %w", err)
	}
	return nil
}

func newDependencyCacheIndex() dependencyCacheIndex {
	return dependencyCacheIndex{
		Version: checkDependencyCacheIndexVersion, Entries: map[string]dependencyCacheEntry{},
		References: map[string]dependencyCacheReference{}, Leases: map[string]dependencyCacheLease{},
		Reservations: map[string]dependencyCacheReservation{},
	}
}

func validateDependencyCacheIndex(index dependencyCacheIndex) error {
	if index.Version != checkDependencyCacheIndexVersion || index.Entries == nil || index.References == nil || index.Leases == nil || index.Reservations == nil {
		return errors.New("dependency cache index is invalid")
	}
	limits := productionCheckCapacityLimits()
	var total int64
	for key, entry := range index.Entries {
		validTrash := entry.TrashName == ""
		if entry.Evicting {
			validTrash = strings.HasPrefix(entry.TrashName, ".trash-") && validLowerHex(strings.TrimPrefix(entry.TrashName, ".trash-"), 32)
		}
		if key != entry.Key || !validLowerHex(key, 64) || entry.Bytes < 0 || entry.Bytes > limits.Dependency.Bytes || entry.Items < 0 || entry.Items > limits.Dependency.Items || !validTrash {
			return errors.New("dependency cache entry is invalid")
		}
		if entry.Bytes > limits.PersistentBytes-total {
			return errors.New("dependency cache index exceeds its fixed total")
		}
		total += entry.Bytes
	}
	for id, reference := range index.References {
		if !validLowerHex(id, 64) {
			return errors.New("dependency cache reference id is invalid")
		}
		if _, ok := index.Entries[reference.Key]; !ok {
			return errors.New("dependency cache reference points to a missing item")
		}
	}
	for id, lease := range index.Leases {
		if !validLowerHex(id, 64) || lease.OwnerPID <= 0 || lease.OwnerStart == "" {
			return errors.New("dependency cache lease is invalid")
		}
		if _, ok := index.Entries[lease.Key]; !ok {
			return errors.New("dependency cache lease points to a missing item")
		}
	}
	for id, pending := range index.Reservations {
		if id != pending.ID || !validLowerHex(id, 32) || pending.Directory != ".prepare-"+id || !validLowerHex(pending.Key, 64) || (pending.State != "preparing" && pending.State != "publishing") || pending.Bytes < 0 || pending.Bytes > limits.Dependency.Bytes || pending.Items < 0 || pending.Items > limits.Dependency.Items || pending.OwnerPID <= 0 || pending.OwnerStart == "" {
			return errors.New("dependency cache reservation is invalid")
		}
		if pending.Bytes > limits.PersistentBytes-total {
			return errors.New("dependency cache reservations exceed their fixed total")
		}
		total += pending.Bytes
	}
	return nil
}

func (m *dependencyCacheManager) validateIndexPaths(index dependencyCacheIndex) error {
	temporaryRoot, err := checkTemporaryRoot()
	if err != nil {
		return err
	}
	for _, lease := range index.Leases {
		if lease.CIDFile == "" {
			continue
		}
		relative, err := filepath.Rel(temporaryRoot, filepath.Clean(lease.CIDFile))
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if err != nil || len(parts) != 2 || !strings.HasPrefix(parts[0], "action-") || !validLowerHex(strings.TrimPrefix(parts[0], "action-"), 64) || parts[1] != "container.cid" {
			return errors.New("dependency cache lease cidfile leaves the managed temporary root")
		}
		if info, statErr := os.Lstat(filepath.Join(temporaryRoot, parts[0])); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return errors.New("dependency cache lease directory is not a real directory")
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect dependency cache lease directory: %w", statErr)
		}
	}
	return nil
}

func bindCacheReference(index *dependencyCacheIndex, ref, key string) error {
	if existing, ok := index.References[ref]; ok && existing.Key != key {
		return errors.New("dependency cache reference is already bound to another item")
	}
	index.References[ref] = dependencyCacheReference{Key: key}
	return nil
}

func cacheKeyHeld(index *dependencyCacheIndex, key string) bool {
	for _, reference := range index.References {
		if reference.Key == key {
			return true
		}
	}
	for _, lease := range index.Leases {
		if lease.Key == key {
			return true
		}
	}
	return false
}

func cacheReservedBytes(index *dependencyCacheIndex) int64 {
	var total int64
	for _, entry := range index.Entries {
		total += entry.Bytes
	}
	for _, pending := range index.Reservations {
		total += pending.Bytes
	}
	return total
}

func nextCacheUse(index *dependencyCacheIndex) uint64 {
	index.NextUse++
	if index.NextUse == 0 {
		index.NextUse = 1
	}
	return index.NextUse
}

func cacheHolderID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return sha256Hex([]byte(value))
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func checkPathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func measureCheckTree(root string, limit checkUsageLimit) (checkUsage, error) {
	counter, err := newCheckUsageCounter(limit)
	if err != nil {
		return checkUsage{}, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return checkUsage{}, errors.New("ClearDev capacity root is not a real directory")
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return checkUsage{}, fmt.Errorf("open ClearDev capacity root: %w", err)
	}
	defer func() { _ = rootFS.Close() }()
	err = fs.WalkDir(rootFS.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		info, err := rootFS.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return counter.ChargeDirectory()
		case info.Mode()&os.ModeSymlink != 0:
			if err := validateDependencySymlink(rootFS, path); err != nil {
				return err
			}
			return counter.ChargeSymlink()
		case info.Mode().IsRegular():
			return counter.ChargeRegularFile(info.Size())
		default:
			return fmt.Errorf("ClearDev capacity root contains unsupported path %q", path)
		}
	})
	if err != nil {
		return checkUsage{}, err
	}
	return counter.Usage(), nil
}
