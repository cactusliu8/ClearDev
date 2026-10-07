package cleardevlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDependencyCacheReferenceBindingIsIdempotentAndConflictsFail(t *testing.T) {
	index := newDependencyCacheIndex()
	ref := cacheHolderID("run-one")
	firstKey := whiteBoxCacheKey("a")
	secondKey := whiteBoxCacheKey("b")

	if err := bindCacheReference(&index, ref, firstKey); err != nil {
		t.Fatalf("bind first reference: %v", err)
	}
	if err := bindCacheReference(&index, ref, firstKey); err != nil {
		t.Fatalf("repeat same reference binding: %v", err)
	}
	if len(index.References) != 1 || index.References[ref].Key != firstKey {
		t.Fatalf("idempotent reference binding changed the index: %#v", index.References)
	}
	if err := bindCacheReference(&index, ref, secondKey); err == nil {
		t.Fatal("one reference was allowed to bind to two dependency keys")
	}
	if index.References[ref].Key != firstKey {
		t.Fatal("conflicting reference binding replaced the original key")
	}
}

func TestDependencyCacheReservationDeduplicatesAnActiveKey(t *testing.T) {
	manager := newWhiteBoxDependencyCacheManager(t)
	key := whiteBoxCacheKey("c")
	reservationID := whiteBoxReservationID("1")
	directory := ".prepare-" + reservationID
	if err := os.Mkdir(filepath.Join(manager.root, directory), 0o700); err != nil {
		t.Fatalf("create existing preparation directory: %v", err)
	}
	index := newDependencyCacheIndex()
	index.Reservations[reservationID] = dependencyCacheReservation{
		ID: reservationID, Key: key, Directory: directory,
		Bytes: 1, Items: 1, State: "preparing",
		OwnerPID: os.Getpid(), OwnerMark: checkCacheOwnerMark,
		OwnerStart: whiteBoxCurrentOwnerStart(t),
	}
	writeWhiteBoxDependencyCacheIndex(t, manager, &index)

	if _, err := manager.reserve(context.Background(), key, "another-run"); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("reserve same active key returned %v", err)
	}
	stored, err := manager.readIndex()
	if err != nil {
		t.Fatalf("read cache index after duplicate reservation: %v", err)
	}
	if len(stored.Reservations) != 1 {
		t.Fatalf("duplicate key created %d reservations", len(stored.Reservations))
	}
	entries, err := os.ReadDir(manager.root)
	if err != nil {
		t.Fatalf("read cache root: %v", err)
	}
	preparations := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".prepare-") {
			preparations++
		}
	}
	if preparations != 1 {
		t.Fatalf("duplicate key left %d preparation directories", preparations)
	}
}

func TestDependencyCacheReserveReusesAPublishedKey(t *testing.T) {
	manager := newWhiteBoxDependencyCacheManager(t)
	key, usage := writeWhiteBoxDependencyEnvironment(t, manager.root)
	index := newDependencyCacheIndex()
	index.NextUse = 1
	index.Entries[key] = dependencyCacheEntry{
		Key: key, Bytes: usage.Bytes, Items: usage.Items, LastUse: 1,
	}
	writeWhiteBoxDependencyCacheIndex(t, manager, &index)

	result, err := manager.reserve(context.Background(), key, "reuse-run")
	if err != nil {
		t.Fatalf("reuse published dependency key: %v", err)
	}
	if !result.Hit || result.ReservationID != "" || result.Directory != filepath.Join(manager.root, key) {
		t.Fatalf("published dependency key was not reused: %#v", result)
	}
	stored, err := manager.readIndex()
	if err != nil {
		t.Fatalf("read reused dependency index: %v", err)
	}
	if len(stored.Reservations) != 0 || len(stored.Entries) != 1 {
		t.Fatalf("reused dependency key created duplicate state: %#v", stored)
	}
	if stored.References[result.ReferenceID].Key != key {
		t.Fatal("reused dependency key did not bind its reference")
	}
	if stored.Entries[key].LastUse <= 1 {
		t.Fatal("reused dependency key did not refresh LastUse")
	}
}

func TestDependencyCacheLRUProtectsReferencesAndLeases(t *testing.T) {
	manager := newWhiteBoxDependencyCacheManager(t)
	limits := productionCheckCapacityLimits()
	keys := []string{
		whiteBoxCacheKey("a"),
		whiteBoxCacheKey("b"),
		whiteBoxCacheKey("c"),
		whiteBoxCacheKey("d"),
	}
	index := newDependencyCacheIndex()
	for position, key := range keys {
		index.Entries[key] = dependencyCacheEntry{
			Key: key, Bytes: limits.Dependency.Bytes,
			Items: 1, LastUse: uint64(position + 1),
		}
		if err := os.Mkdir(filepath.Join(manager.root, key), 0o700); err != nil {
			t.Fatalf("create cache entry directory: %v", err)
		}
	}
	index.References[cacheHolderID("held-reference")] = dependencyCacheReference{Key: keys[0]}
	index.Leases[cacheHolderID("held-lease")] = dependencyCacheLease{
		Key: keys[1], OwnerPID: os.Getpid(), OwnerMark: checkCacheOwnerMark,
		OwnerStart: whiteBoxCurrentOwnerStart(t),
	}

	if got := cacheReservedBytes(&index); got != limits.PersistentBytes {
		t.Fatalf("cache exact-boundary usage = %d, want %d", got, limits.PersistentBytes)
	}
	if err := manager.makeCapacity(&index, 0); err != nil {
		t.Fatalf("exact persistent boundary was rejected: %v", err)
	}
	if err := manager.makeCapacity(&index, 1); err != nil {
		t.Fatalf("reclaim one byte beyond the boundary: %v", err)
	}
	if _, ok := index.Entries[keys[2]]; ok {
		t.Fatal("oldest unheld dependency entry was not reclaimed")
	}
	for _, key := range []string{keys[0], keys[1], keys[3]} {
		if _, ok := index.Entries[key]; !ok {
			t.Fatalf("protected or newer dependency entry %s was reclaimed", key)
		}
		if _, err := os.Lstat(filepath.Join(manager.root, key)); err != nil {
			t.Fatalf("retained dependency directory %s is unavailable: %v", key, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(manager.root, keys[2])); !os.IsNotExist(err) {
		t.Fatalf("reclaimed dependency directory still exists: %v", err)
	}
	writeWhiteBoxDependencyCacheIndex(t, manager, &index)
}

func TestDependencyCacheCapacityBoundariesUseTheCapacitySentinel(t *testing.T) {
	limits := productionCheckCapacityLimits()
	exact := newDependencyCacheIndex()
	exactKey := whiteBoxCacheKey("e")
	exact.Entries[exactKey] = dependencyCacheEntry{
		Key: exactKey, Bytes: limits.Dependency.Bytes, Items: limits.Dependency.Items,
	}
	if err := validateDependencyCacheIndex(exact); err != nil {
		t.Fatalf("exact per-item boundary was rejected: %v", err)
	}

	tooManyBytes := newDependencyCacheIndex()
	tooManyBytes.Entries[exactKey] = dependencyCacheEntry{
		Key: exactKey, Bytes: limits.Dependency.Bytes + 1, Items: limits.Dependency.Items,
	}
	if err := validateDependencyCacheIndex(tooManyBytes); err == nil {
		t.Fatal("dependency bytes limit+1 was accepted")
	}
	tooManyItems := newDependencyCacheIndex()
	tooManyItems.Entries[exactKey] = dependencyCacheEntry{
		Key: exactKey, Bytes: limits.Dependency.Bytes, Items: limits.Dependency.Items + 1,
	}
	if err := validateDependencyCacheIndex(tooManyItems); err == nil {
		t.Fatal("dependency item limit+1 was accepted")
	}

	manager := newWhiteBoxDependencyCacheManager(t)
	if err := manager.publish(context.Background(), "missing", checkUsage{Bytes: limits.Dependency.Bytes + 1}); !errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("bytes limit+1 did not return the capacity sentinel: %v", err)
	}
	if err := manager.publish(context.Background(), "missing", checkUsage{Items: limits.Dependency.Items + 1}); !errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("items limit+1 did not return the capacity sentinel: %v", err)
	}
	if err := manager.publish(context.Background(), "missing", checkUsage{Bytes: limits.Dependency.Bytes, Items: limits.Dependency.Items}); err == nil || errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("exact per-item boundary returned the wrong result: %v", err)
	}

	held := newDependencyCacheIndex()
	for position, character := range []string{"1", "2", "3", "4"} {
		key := whiteBoxCacheKey(character)
		held.Entries[key] = dependencyCacheEntry{
			Key: key, Bytes: limits.Dependency.Bytes, Items: 1, LastUse: uint64(position + 1),
		}
		held.References[cacheHolderID("holder-"+character)] = dependencyCacheReference{Key: key}
	}
	if err := validateDependencyCacheIndex(held); err != nil {
		t.Fatalf("exact persistent boundary was rejected: %v", err)
	}
	if err := manager.makeCapacity(&held, 0); err != nil {
		t.Fatalf("exact persistent boundary required reclamation: %v", err)
	}
	if err := manager.makeCapacity(&held, 1); !errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("held total limit+1 did not return the capacity sentinel: %v", err)
	}
}

func TestDependencyCacheIndexRejectsRecoveryPathsOutsideItsRoot(t *testing.T) {
	key := whiteBoxCacheKey("a")
	tests := []struct {
		name  string
		index dependencyCacheIndex
	}{
		{
			name: "preparation directory traversal",
			index: func() dependencyCacheIndex {
				index := newDependencyCacheIndex()
				id := whiteBoxReservationID("2")
				index.Reservations[id] = dependencyCacheReservation{
					ID: id, Key: key, Directory: "../outside",
					Bytes: 1, Items: 1, State: "preparing",
					OwnerPID: os.Getpid(), OwnerMark: checkCacheOwnerMark, OwnerStart: "current-owner",
				}
				return index
			}(),
		},
		{
			name: "reclamation directory traversal",
			index: func() dependencyCacheIndex {
				index := newDependencyCacheIndex()
				index.Entries[key] = dependencyCacheEntry{
					Key: key, Bytes: 1, Items: 1, Evicting: true, TrashName: "../outside",
				}
				return index
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateDependencyCacheIndex(test.index); err == nil {
				t.Fatal("dependency cache index accepted an out-of-root recovery path")
			}
		})
	}
}

func TestDependencyCacheDamagedIndexedItemFailsClosed(t *testing.T) {
	manager := newWhiteBoxDependencyCacheManager(t)
	key := whiteBoxCacheKey("f")
	if err := os.Mkdir(filepath.Join(manager.root, key), 0o700); err != nil {
		t.Fatalf("create damaged cache directory: %v", err)
	}
	index := newDependencyCacheIndex()
	index.Entries[key] = dependencyCacheEntry{Key: key, LastUse: 7}
	writeWhiteBoxDependencyCacheIndex(t, manager, &index)

	if _, err := manager.reserve(context.Background(), key, "damaged-run"); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("damaged indexed entry returned %v", err)
	}
	stored, err := manager.readIndex()
	if err != nil {
		t.Fatalf("read damaged cache index: %v", err)
	}
	if _, ok := stored.Entries[key]; !ok {
		t.Fatal("damaged indexed entry was silently removed for rebuilding")
	}
	if len(stored.Reservations) != 0 || len(stored.References) != 0 {
		t.Fatalf("damaged indexed entry created new state: %#v", stored)
	}
	entries, err := os.ReadDir(manager.root)
	if err != nil {
		t.Fatalf("read damaged cache root: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".prepare-") {
			t.Fatalf("damaged indexed entry was automatically rebuilt in %s", entry.Name())
		}
	}
}

func TestDependencyCachePublishAndAbortLifecycle(t *testing.T) {
	t.Run("publish", func(t *testing.T) {
		manager := newWhiteBoxDependencyCacheManager(t)
		key, usage := writeWhiteBoxDependencyEnvironment(t, manager.root)
		reservationID := whiteBoxReservationID("3")
		stageName := ".prepare-" + reservationID
		stage := filepath.Join(manager.root, stageName)
		if err := os.Rename(filepath.Join(manager.root, key), stage); err != nil {
			t.Fatalf("stage verified dependency environment: %v", err)
		}
		ref := cacheHolderID("publish-run")
		index := newDependencyCacheIndex()
		index.Reservations[reservationID] = dependencyCacheReservation{
			ID: reservationID, Key: key, Ref: ref, Directory: stageName,
			Bytes: checkDependencyBytesLimit, Items: checkDependencyItemsLimit,
			State: "preparing", OwnerPID: os.Getpid(), OwnerMark: checkCacheOwnerMark,
			OwnerStart: whiteBoxCurrentOwnerStart(t),
		}
		writeWhiteBoxDependencyCacheIndex(t, manager, &index)

		if err := manager.publish(context.Background(), reservationID, usage); err != nil {
			t.Fatalf("publish dependency entry: %v", err)
		}
		stored, err := manager.readIndex()
		if err != nil {
			t.Fatalf("read published cache index: %v", err)
		}
		if _, ok := stored.Reservations[reservationID]; ok {
			t.Fatal("published reservation remained active")
		}
		entry, ok := stored.Entries[key]
		if !ok || entry.Bytes != usage.Bytes || entry.Items != usage.Items {
			t.Fatalf("published entry usage = %#v, want %#v", entry, usage)
		}
		if stored.References[ref].Key != key {
			t.Fatal("publish did not bind its durable reference")
		}
		if _, err := os.Lstat(stage); !os.IsNotExist(err) {
			t.Fatalf("publish stage still exists: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(manager.root, key)); err != nil {
			t.Fatalf("published dependency directory is unavailable: %v", err)
		}
	})

	t.Run("abort preparing", func(t *testing.T) {
		manager := newWhiteBoxDependencyCacheManager(t)
		key := whiteBoxCacheKey("8")
		reservationID := whiteBoxReservationID("4")
		stageName := ".prepare-" + reservationID
		stage := filepath.Join(manager.root, stageName)
		if err := os.MkdirAll(filepath.Join(stage, "nested"), 0o700); err != nil {
			t.Fatalf("create abort stage: %v", err)
		}
		if err := os.WriteFile(filepath.Join(stage, "nested", "sealed"), []byte("data"), 0o400); err != nil {
			t.Fatalf("write abort stage: %v", err)
		}
		if err := os.Chmod(filepath.Join(stage, "nested"), 0o500); err != nil {
			t.Fatalf("seal abort directory: %v", err)
		}
		index := newDependencyCacheIndex()
		index.Reservations[reservationID] = dependencyCacheReservation{
			ID: reservationID, Key: key, Directory: stageName,
			Bytes: 1, Items: 2, State: "preparing",
			OwnerPID: os.Getpid(), OwnerMark: checkCacheOwnerMark,
			OwnerStart: whiteBoxCurrentOwnerStart(t),
		}
		writeWhiteBoxDependencyCacheIndex(t, manager, &index)

		if err := manager.abort(context.Background(), reservationID); err != nil {
			t.Fatalf("abort dependency preparation: %v", err)
		}
		if checkPathExists(stage) {
			t.Fatal("aborted dependency preparation directory still exists")
		}
		stored, err := manager.readIndex()
		if err != nil {
			t.Fatalf("read aborted cache index: %v", err)
		}
		if _, ok := stored.Reservations[reservationID]; ok {
			t.Fatal("aborted dependency reservation still consumes capacity")
		}
		if err := manager.abort(context.Background(), reservationID); err != nil {
			t.Fatalf("repeat abort was not idempotent: %v", err)
		}
	})
}

func TestDependencyCacheRecoversInterruptedPublish(t *testing.T) {
	t.Run("publishing before rename is discarded", func(t *testing.T) {
		manager := newWhiteBoxDependencyCacheManager(t)
		key := whiteBoxCacheKey("9")
		reservationID := whiteBoxReservationID("5")
		stageName := ".prepare-" + reservationID
		stage := filepath.Join(manager.root, stageName)
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatalf("create interrupted publish stage: %v", err)
		}
		index := newDependencyCacheIndex()
		index.Reservations[reservationID] = dependencyCacheReservation{
			ID: reservationID, Key: key, Directory: stageName,
			Bytes: 1, Items: 1, State: "publishing",
			OwnerPID: whiteBoxDeadPID(t), OwnerMark: "dead-owner", OwnerStart: "dead-start",
		}

		if err := manager.recoverIndex(context.Background(), &index); err != nil {
			t.Fatalf("recover publish before rename: %v", err)
		}
		if checkPathExists(stage) {
			t.Fatal("interrupted publish stage was not removed")
		}
		if _, ok := index.Reservations[reservationID]; ok {
			t.Fatal("interrupted publish reservation was not released")
		}
	})

	t.Run("publishing after rename is completed", func(t *testing.T) {
		manager := newWhiteBoxDependencyCacheManager(t)
		key, usage := writeWhiteBoxDependencyEnvironment(t, manager.root)
		reservationID := whiteBoxReservationID("6")
		ref := cacheHolderID("recovered-publish-run")
		index := newDependencyCacheIndex()
		index.Reservations[reservationID] = dependencyCacheReservation{
			ID: reservationID, Key: key, Ref: ref, Directory: ".prepare-" + reservationID,
			Bytes: usage.Bytes, Items: usage.Items, State: "publishing",
			OwnerPID: whiteBoxDeadPID(t), OwnerMark: "dead-owner", OwnerStart: "dead-start",
		}

		if err := manager.recoverIndex(context.Background(), &index); err != nil {
			t.Fatalf("recover publish after rename: %v", err)
		}
		if _, ok := index.Reservations[reservationID]; ok {
			t.Fatal("recovered publish reservation remained active")
		}
		entry, ok := index.Entries[key]
		if !ok || entry.Bytes != usage.Bytes || entry.Items != usage.Items {
			t.Fatalf("recovered publish entry = %#v, want usage %#v", entry, usage)
		}
		if index.References[ref].Key != key {
			t.Fatal("recovered publish did not bind its reference")
		}
	})

	t.Run("damaged final item fails closed", func(t *testing.T) {
		manager := newWhiteBoxDependencyCacheManager(t)
		key := whiteBoxCacheKey("0")
		if err := os.Mkdir(filepath.Join(manager.root, key), 0o700); err != nil {
			t.Fatalf("create damaged recovered item: %v", err)
		}
		reservationID := whiteBoxReservationID("7")
		index := newDependencyCacheIndex()
		index.Reservations[reservationID] = dependencyCacheReservation{
			ID: reservationID, Key: key, Directory: ".prepare-" + reservationID,
			State: "publishing", OwnerPID: whiteBoxDeadPID(t), OwnerMark: "dead-owner", OwnerStart: "dead-start",
		}

		if err := manager.recoverIndex(context.Background(), &index); err == nil {
			t.Fatal("damaged post-rename dependency item was adopted")
		}
		if _, ok := index.Reservations[reservationID]; !ok {
			t.Fatal("failed recovery released the reservation")
		}
		if _, ok := index.Entries[key]; ok {
			t.Fatal("failed recovery published a damaged cache entry")
		}
		if !checkPathExists(filepath.Join(manager.root, key)) {
			t.Fatal("failed recovery silently removed evidence")
		}
	})
}

func TestDependencyCacheRecoveryCleansOnlyRecognizedOrphans(t *testing.T) {
	manager := newWhiteBoxDependencyCacheManager(t)
	prepare := filepath.Join(manager.root, ".prepare-"+whiteBoxReservationID("a"))
	trash := filepath.Join(manager.root, ".trash-"+whiteBoxReservationID("b"))
	for _, directory := range []string{prepare, trash} {
		if err := os.MkdirAll(filepath.Join(directory, "nested"), 0o700); err != nil {
			t.Fatalf("create orphan dependency directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(directory, "nested", "sealed"), []byte("data"), 0o400); err != nil {
			t.Fatalf("write orphan dependency file: %v", err)
		}
		if err := os.Chmod(filepath.Join(directory, "nested"), 0o500); err != nil {
			t.Fatalf("seal orphan dependency directory: %v", err)
		}
	}
	staleIndex := filepath.Join(manager.root, ".cache-index-stale")
	if err := os.WriteFile(staleIndex, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	index := newDependencyCacheIndex()
	if err := manager.recoverIndex(context.Background(), &index); err != nil {
		t.Fatalf("recover recognized dependency orphans: %v", err)
	}
	for _, path := range []string{prepare, trash, staleIndex} {
		if checkPathExists(path) {
			t.Fatalf("recognized orphan still exists: %s", path)
		}
	}

	unknown := filepath.Join(manager.root, whiteBoxCacheKey("c"))
	if err := os.Mkdir(unknown, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.recoverIndex(context.Background(), &index); err == nil || !strings.Contains(err.Error(), "unindexed") {
		t.Fatalf("unknown cache item recovery error = %v", err)
	}
	if !checkPathExists(unknown) {
		t.Fatal("unknown cache item was silently deleted")
	}
}

func TestCheckTemporaryReservationsReachLimitReleaseAndReuseCapacity(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	ctx := context.Background()
	limits := productionCheckCapacityLimits()
	actionIDs := make([]string, limits.ActiveTemporaryBytes/checkSourceReservationBytes)
	for i := range actionIDs {
		actionIDs[i] = fmt.Sprintf("temporary-source-%d", i)
	}
	leases := make([]*checkTemporaryLease, 0, len(actionIDs))
	for _, actionID := range actionIDs {
		lease, err := acquireCheckTemporary(ctx, actionID, "source-inspection", checkSourceReservationBytes)
		if err != nil {
			t.Fatalf("acquire source temporary reservation: %v", err)
		}
		leases = append(leases, lease)
	}
	if _, err := acquireCheckTemporary(ctx, "temporary-source-five", "source-inspection", checkSourceReservationBytes); !errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("temporary reservation over the total did not return the capacity sentinel: %v", err)
	}
	index, err := leases[0].manager.readIndex()
	if err != nil {
		t.Fatalf("read full temporary index: %v", err)
	}
	if got := whiteBoxTemporaryReservedBytes(index); got != limits.ActiveTemporaryBytes {
		t.Fatalf("temporary reserved bytes = %d, want %d", got, limits.ActiveTemporaryBytes)
	}

	if err := leases[0].Release(ctx); err != nil {
		t.Fatalf("release first temporary reservation: %v", err)
	}
	reused, err := acquireCheckTemporary(ctx, "temporary-source-five", "source-inspection", checkSourceReservationBytes)
	if err != nil {
		t.Fatalf("reuse released temporary capacity: %v", err)
	}
	if err := leases[0].Release(ctx); err != nil {
		t.Fatalf("repeat temporary release was not idempotent: %v", err)
	}
	for _, lease := range leases[1:] {
		if err := lease.Release(ctx); err != nil {
			t.Fatalf("release source temporary reservation: %v", err)
		}
	}
	if err := reused.Release(ctx); err != nil {
		t.Fatalf("release reused temporary reservation: %v", err)
	}
	index, err = reused.manager.readIndex()
	if err != nil {
		t.Fatalf("read released temporary index: %v", err)
	}
	if got := whiteBoxTemporaryReservedBytes(index); got != 0 {
		t.Fatalf("released temporary reservations still consume %d bytes", got)
	}
}

func TestCheckTemporaryRecoveryRemovesDeadOwnerAndOrphanDirectories(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	root, err := checkTemporaryRoot()
	if err != nil {
		t.Fatalf("create temporary root: %v", err)
	}
	manager := &checkTemporaryManager{root: root}
	id := cacheHolderID("dead-owner-reservation")
	directoryName := "action-" + id
	directory := filepath.Join(root, directoryName)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create dead owner directory: %v", err)
	}
	reusedPIDID := cacheHolderID("reused-pid-reservation")
	reusedPIDDirectoryName := "action-" + reusedPIDID
	reusedPIDDirectory := filepath.Join(root, reusedPIDDirectoryName)
	if err := os.Mkdir(reusedPIDDirectory, 0o700); err != nil {
		t.Fatalf("create reused PID directory: %v", err)
	}
	orphan := filepath.Join(root, "action-orphan")
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatalf("create orphan directory: %v", err)
	}
	index := checkTemporaryIndex{
		Version: checkTemporaryIndexVersion,
		Reservations: map[string]checkTemporaryReservation{
			id: {
				ID: id, Kind: "source-inspection", Bytes: checkSourceReservationBytes,
				Directory: directoryName, CIDFile: filepath.Join(directory, "container.cid"),
				OwnerPID: whiteBoxDeadPID(t), OwnerMark: "dead-owner", OwnerStart: "dead-start",
			},
			reusedPIDID: {
				ID: reusedPIDID, Kind: "source-inspection", Bytes: checkSourceReservationBytes,
				Directory: reusedPIDDirectoryName, CIDFile: filepath.Join(reusedPIDDirectory, "container.cid"),
				OwnerPID: os.Getpid(), OwnerMark: "previous-owner", OwnerStart: "previous-process-start",
			},
		},
	}
	if err := manager.writeIndex(index); err != nil {
		t.Fatalf("write stale temporary index: %v", err)
	}

	if err := New().RecoverCheckResources(context.Background()); err != nil {
		t.Fatalf("recover stale temporary state: %v", err)
	}
	if checkPathExists(directory) || checkPathExists(reusedPIDDirectory) || checkPathExists(orphan) {
		t.Fatal("restart recovery left a dead-owner, reused-PID, or orphan directory")
	}
	stored, err := manager.readIndex()
	if err != nil {
		t.Fatalf("read recovered temporary index: %v", err)
	}
	if len(stored.Reservations) != 0 {
		t.Fatalf("restart recovery retained stale reservations: %#v", stored.Reservations)
	}
}

func TestCheckTemporaryCleanupFailureRetainsReservedCapacity(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	ctx := context.Background()
	limits := productionCheckCapacityLimits()
	lease, err := acquireCheckTemporary(ctx, "cleanup-failure", "dependency-preparation", limits.ActiveTemporaryBytes)
	if err != nil {
		t.Fatalf("acquire full temporary reservation: %v", err)
	}
	if err := os.WriteFile(lease.CIDFile(), []byte("invalid-container-id"), 0o600); err != nil {
		t.Fatalf("write invalid container id fixture: %v", err)
	}

	if err := lease.Release(ctx); err == nil {
		t.Fatal("temporary cleanup with an invalid container id succeeded")
	}
	stored, err := lease.manager.readIndex()
	if err != nil {
		t.Fatalf("read temporary index after cleanup failure: %v", err)
	}
	reservation, ok := stored.Reservations[lease.id]
	if !ok || reservation.Bytes != limits.ActiveTemporaryBytes {
		t.Fatalf("cleanup failure released reserved capacity: %#v", stored.Reservations)
	}
	if !checkPathExists(lease.Root()) {
		t.Fatal("cleanup failure removed its evidence directory")
	}
	if _, err := acquireCheckTemporary(ctx, "blocked-by-cleanup", "source-inspection", checkSourceReservationBytes); !errors.Is(err, errCheckCapacityExceeded) {
		t.Fatalf("retained cleanup failure did not block capacity reuse: %v", err)
	}
}

func TestCheckTemporaryDockerUnavailableRetainsReservedCapacity(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()
	limits := productionCheckCapacityLimits()
	lease, err := acquireCheckTemporary(ctx, "docker-unavailable", "dependency-preparation", limits.ActiveTemporaryBytes)
	if err != nil {
		t.Fatalf("acquire full temporary reservation: %v", err)
	}
	if err := os.WriteFile(lease.CIDFile(), []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatalf("write valid container id fixture: %v", err)
	}
	reservationID := lease.id

	if err := lease.Release(ctx); err == nil {
		t.Fatal("temporary cleanup treated an unavailable Docker service as confirmed removal")
	}
	stored, err := lease.manager.readIndex()
	if err != nil {
		t.Fatalf("read temporary index after unavailable Docker cleanup: %v", err)
	}
	reservation, ok := stored.Reservations[reservationID]
	if !ok || reservation.Bytes != limits.ActiveTemporaryBytes {
		t.Fatalf("unconfirmed container cleanup released capacity: %#v", stored.Reservations)
	}
	if !checkPathExists(lease.Root()) {
		t.Fatal("unconfirmed container cleanup removed its evidence directory")
	}
}

func TestDependencyCacheDeadLeaseRequiresConfirmedContainerCleanup(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	manager := newWhiteBoxDependencyCacheManager(t)
	key := whiteBoxCacheKey("b")
	temporaryRoot, err := checkTemporaryRoot()
	if err != nil {
		t.Fatalf("create managed temporary root: %v", err)
	}
	actionID := cacheHolderID("dead-cache-lease-action")
	actionDirectory := filepath.Join(temporaryRoot, "action-"+actionID)
	if err := os.Mkdir(actionDirectory, 0o700); err != nil {
		t.Fatalf("create dead lease action directory: %v", err)
	}
	cidFile := filepath.Join(actionDirectory, "container.cid")
	if err := os.WriteFile(cidFile, []byte(strings.Repeat("b", 64)), 0o600); err != nil {
		t.Fatalf("write dead lease container id: %v", err)
	}
	leaseID := cacheHolderID("dead-cache-lease")
	index := newDependencyCacheIndex()
	index.Entries[key] = dependencyCacheEntry{Key: key, Bytes: 1, Items: 1}
	index.Leases[leaseID] = dependencyCacheLease{
		Key: key, OwnerPID: whiteBoxDeadPID(t), OwnerMark: "dead-owner",
		OwnerStart: "dead-start", CIDFile: cidFile,
	}

	if err := manager.recoverIndex(context.Background(), &index); err == nil {
		t.Fatal("cache recovery released a lease without confirming container cleanup")
	}
	if _, ok := index.Leases[leaseID]; !ok {
		t.Fatal("failed container cleanup removed the dependency cache lease")
	}
}

func TestCheckTemporaryIndexRejectsUnsafeOrOversizedReservations(t *testing.T) {
	limits := productionCheckCapacityLimits()
	tests := []struct {
		name   string
		mutate func(root string, reservation *checkTemporaryReservation)
	}{
		{
			name: "directory traversal",
			mutate: func(_ string, reservation *checkTemporaryReservation) {
				reservation.Directory = "../outside"
			},
		},
		{
			name: "container id outside action directory",
			mutate: func(root string, reservation *checkTemporaryReservation) {
				reservation.CIDFile = filepath.Join(root, "outside.cid")
			},
		},
		{
			name: "single reservation over total",
			mutate: func(_ string, reservation *checkTemporaryReservation) {
				reservation.Bytes = limits.ActiveTemporaryBytes + 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			manager := &checkTemporaryManager{root: root}
			id := cacheHolderID("unsafe-temporary-reservation")
			directory := "action-" + id
			reservation := checkTemporaryReservation{
				ID: id, Kind: "source-inspection", Bytes: checkSourceReservationBytes, Directory: directory,
				CIDFile:  filepath.Join(root, directory, "container.cid"),
				OwnerPID: os.Getpid(), OwnerMark: checkCacheOwnerMark,
				OwnerStart: whiteBoxCurrentOwnerStart(t),
			}
			test.mutate(root, &reservation)
			index := checkTemporaryIndex{
				Version:      checkTemporaryIndexVersion,
				Reservations: map[string]checkTemporaryReservation{id: reservation},
			}
			if err := manager.writeIndex(index); err == nil {
				t.Error("temporary index writer accepted an unsafe or oversized reservation")
			}
			payload, err := json.Marshal(index)
			if err != nil {
				t.Fatalf("encode unsafe temporary index fixture: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, checkTemporaryIndexName), payload, 0o600); err != nil {
				t.Fatalf("write unsafe temporary index fixture: %v", err)
			}
			if _, err := manager.readIndex(); err == nil {
				t.Error("temporary index reader accepted an unsafe or oversized reservation")
			}
		})
	}
}

func newWhiteBoxDependencyCacheManager(t *testing.T) *dependencyCacheManager {
	t.Helper()
	t.Setenv("AO_DATA_DIR", t.TempDir())
	root := t.TempDir()
	t.Cleanup(func() { cleanupPreparedTree(root) })
	return &dependencyCacheManager{root: root}
}

func writeWhiteBoxDependencyCacheIndex(t *testing.T, manager *dependencyCacheManager, index *dependencyCacheIndex) {
	t.Helper()
	if err := manager.writeIndex(index); err != nil {
		t.Fatalf("write dependency cache index: %v", err)
	}
}

func whiteBoxCacheKey(character string) string {
	return strings.Repeat(character, 64)
}

func whiteBoxReservationID(character string) string {
	return strings.Repeat(character, 32)
}

func whiteBoxTemporaryReservedBytes(index checkTemporaryIndex) int64 {
	var total int64
	for _, reservation := range index.Reservations {
		total += reservation.Bytes
	}
	return total
}

func whiteBoxDeadPID(t *testing.T) int {
	t.Helper()
	const pid = 1 << 30
	if checkProcessAlive(pid) {
		t.Fatalf("white-box dead PID %d is unexpectedly alive", pid)
	}
	return pid
}

func whiteBoxCurrentOwnerStart(t *testing.T) string {
	t.Helper()
	identity := checkProcessIdentity(os.Getpid())
	if identity == "" {
		t.Fatal("current process identity is unavailable")
	}
	return identity
}

func writeWhiteBoxDependencyEnvironment(t *testing.T, root string) (string, checkUsage) {
	t.Helper()
	packageJSON := []byte(`{"name":"cache-fixture"}`)
	packageLock := []byte(`{"lockfileVersion":3}`)
	image := "node:test"
	imageID := "sha256:test-image"
	nodeVersion := "v22.0.0"
	npmVersion := "10.0.0"
	packageJSONSHA := sha256Hex(packageJSON)
	packageLockSHA := sha256Hex(packageLock)
	key, err := npmDependencyCacheKey(image, imageID, nodeVersion, npmVersion, packageJSONSHA, packageLockSHA)
	if err != nil {
		t.Fatalf("make dependency cache key: %v", err)
	}
	directory := filepath.Join(root, key)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create dependency environment: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "package.json"), packageJSON, 0o600); err != nil {
		t.Fatalf("write dependency package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "package-lock.json"), packageLock, 0o600); err != nil {
		t.Fatalf("write dependency package-lock.json: %v", err)
	}
	nodeModules := filepath.Join(directory, "node_modules")
	if err := os.Mkdir(nodeModules, 0o755); err != nil {
		t.Fatalf("create dependency node_modules: %v", err)
	}
	treeSHA, err := dependencyTreeSHA256(nodeModules)
	if err != nil {
		t.Fatalf("hash dependency tree: %v", err)
	}
	binding := dependencyEnvironmentManifest{
		Version: checkEnvironmentVersion, CacheKey: key,
		Image: image, ImageID: imageID, NodeVersion: nodeVersion, NPMVersion: npmVersion,
		PackageJSONSHA256: packageJSONSHA, PackageLockSHA256: packageLockSHA,
		InstallArgv:            append([]string(nil), npmInstallArgv...),
		InstallScriptsDisabled: true, NetworkDisabled: true,
		DependencyTreeSHA256:    treeSHA,
		DependencyEnvironmentID: sha256Hex([]byte(key + "\x00" + treeSHA)),
		Items:                   4,
	}
	var encoded []byte
	for attempts := 0; attempts < 10; attempts++ {
		encoded, err = json.Marshal(binding)
		if err != nil {
			t.Fatalf("encode dependency environment: %v", err)
		}
		logicalBytes := int64(len(packageJSON) + len(packageLock) + len(encoded))
		if binding.Bytes == logicalBytes {
			break
		}
		binding.Bytes = logicalBytes
	}
	encoded, err = json.Marshal(binding)
	if err != nil {
		t.Fatalf("encode final dependency environment: %v", err)
	}
	if got := int64(len(packageJSON) + len(packageLock) + len(encoded)); got != binding.Bytes {
		t.Fatalf("dependency environment byte fixed point = %d, record = %d", got, binding.Bytes)
	}
	if err := os.WriteFile(filepath.Join(directory, "environment.json"), encoded, 0o444); err != nil {
		t.Fatalf("write dependency environment record: %v", err)
	}
	for _, path := range []string{
		filepath.Join(directory, "package.json"),
		filepath.Join(directory, "package-lock.json"),
	} {
		if err := os.Chmod(path, 0o444); err != nil {
			t.Fatalf("seal dependency manifest %s: %v", path, err)
		}
	}
	if err := os.Chmod(nodeModules, 0o555); err != nil {
		t.Fatalf("seal dependency node_modules: %v", err)
	}
	if err := os.Chmod(directory, 0o555); err != nil {
		t.Fatalf("seal dependency environment: %v", err)
	}
	usage, err := measureCheckTree(directory, productionCheckCapacityLimits().Dependency)
	if err != nil {
		t.Fatalf("measure dependency environment: %v", err)
	}
	if usage.Bytes != binding.Bytes || usage.Items != binding.Items {
		t.Fatalf("dependency environment usage = %#v, record = %d/%d", usage, binding.Bytes, binding.Items)
	}
	return key, usage
}
