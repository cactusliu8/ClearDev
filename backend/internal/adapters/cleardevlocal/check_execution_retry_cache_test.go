package cleardevlocal

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Leave real cache-index facts behind, including an actual publish/abort
// failure. Candidate execution still uses the explicitly instrumented Docker
// shim; this fixture is not evidence of a real npm/container preparation.
func holdTrialPreparationCache(t *testing.T, runID, kind string) (*dependencyCacheManager, string) {
	t.Helper()
	ctx := context.Background()
	manager, err := openDependencyCacheManager(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind == "reference" {
		key, usage := writeWhiteBoxDependencyEnvironment(t, manager.root)
		if err := manager.withIndex(ctx, func(index *dependencyCacheIndex) error {
			index.Entries[key] = dependencyCacheEntry{Key: key, Bytes: usage.Bytes, Items: usage.Items, LastUse: nextCacheUse(index)}
			return bindCacheReference(index, cacheHolderID(runID), key)
		}); err != nil {
			t.Fatal(err)
		}
		return manager, ""
	}
	key := whiteBoxCacheKey("a")
	reservation, err := manager.reserve(ctx, key, runID)
	if err != nil || reservation.Hit {
		t.Fatalf("reserve dependency preparation: %+v %v", reservation, err)
	}
	if kind == "publishing" {
		// A nonempty destination makes the real rename fail after publish has
		// durably changed the reservation state. Abort must retain that fact.
		blocked := filepath.Join(manager.root, key)
		if err := os.Mkdir(blocked, 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(blocked, "retain"), "publication conflict")
		if err := manager.publish(ctx, reservation.ReservationID, checkUsage{}); err == nil || !strings.Contains(err.Error(), "publish dependency cache item") {
			t.Fatalf("publication did not fail at rename: %v", err)
		}
		if err := manager.abort(ctx, reservation.ReservationID); err == nil || !strings.Contains(err.Error(), "after publication began") {
			t.Fatalf("uncertain publication was incorrectly aborted: %v", err)
		}
		index, err := manager.readIndex()
		if err != nil || index.Reservations[reservation.ReservationID].State != "publishing" {
			t.Fatalf("publication failure lost its reservation: %+v %v", index, err)
		}
	}
	return manager, reservation.ReservationID
}

func TestStageTrialPreparationFailureKeepsUnreleasedCacheUnknown(t *testing.T) {
	for _, kind := range []string{"preparing", "publishing", "reference"} {
		t.Run(kind, func(t *testing.T) {
			runner, request := newRetryableTrialFixture(t)
			logPath := installDockerShim(t)
			manager, _ := holdTrialPreparationCache(t, request.RunID, kind)
			cachePath := filepath.Join(manager.root, checkDependencyCacheIndexName)
			cacheBefore := readTestFile(t, cachePath)
			lease := occupyRetryFixtureCapacity(t)
			result, err := runner.RunCandidateCheck(context.Background(), request)
			if err == nil || result.Outcome != ports.ClearDevCheckInfraError {
				t.Fatalf("preparation did not fail: %+v %v", result, err)
			}
			path, state := readRetryFixtureState(t, request)
			if state.Attempt != 1 || state.ExecutionState != checkExecutionStartedOrUnknown || !strings.Contains(state.Error, "dependency cache") {
				t.Fatalf("unreleased %s was classified as safe or lost cleanup detail: %+v", kind, state)
			}
			before := readTestFile(t, path)
			if err := lease.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := New().RunCandidateCheck(context.Background(), request); err == nil {
					t.Fatal("unreleased cache allowed another preparation attempt")
				}
			}
			if before != readTestFile(t, path) || cacheBefore != readTestFile(t, cachePath) || readTestFile(t, logPath) != "" {
				t.Fatal("unknown cleanup changed evidence, cache facts or invoked Docker")
			}
			if _, err := os.Stat(path + ".attempt-1"); !os.IsNotExist(err) {
				t.Fatalf("rejected retry archived/consumed attempt 1: %v", err)
			}
		})
	}
}

func TestStageTrialRetryRechecksCacheWithoutConsumingAttempt(t *testing.T) {
	for _, kind := range []string{"preparing", "publishing", "reference"} {
		t.Run(kind, func(t *testing.T) {
			runner, request := newRetryableTrialFixture(t)
			logPath := installDockerShim(t)
			lease := failRetryFixturePreparation(t, runner, request)
			if err := lease.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
			path, _ := readRetryFixtureState(t, request)
			before := []byte(readTestFile(t, path))
			manager, reservationID := holdTrialPreparationCache(t, request.RunID, kind)
			cachePath := filepath.Join(manager.root, checkDependencyCacheIndexName)
			cacheBefore := readTestFile(t, cachePath)
			for range 2 {
				if _, err := New().RunCandidateCheck(context.Background(), request); err == nil {
					t.Fatal("outstanding cache resource did not veto the second attempt")
				}
			}
			if !bytes.Equal(before, []byte(readTestFile(t, path))) || cacheBefore != readTestFile(t, cachePath) || readTestFile(t, logPath) != "" {
				t.Fatal("retry veto consumed the allowance, changed the cache or invoked Docker")
			}
			if _, err := os.Stat(path + ".attempt-1"); !os.IsNotExist(err) {
				t.Fatalf("vetoed retry created attempt history: %v", err)
			}
			if kind == "publishing" {
				return // This uncertain publication cannot be silently aborted.
			}
			if kind == "reference" {
				if err := manager.releaseReference(context.Background(), request.RunID); err != nil {
					t.Fatal(err)
				}
			} else if err := manager.abort(context.Background(), reservationID); err != nil {
				t.Fatal(err)
			}
			result, err := runner.RunCandidateCheck(context.Background(), request)
			if err != nil || !result.TrialCommandExecuted {
				t.Fatalf("released cache did not leave the second attempt available: %+v %v", result, err)
			}
			if !bytes.Equal(before, []byte(readTestFile(t, path+".attempt-1"))) {
				t.Fatal("eventual retry did not retain the original failure bytes")
			}
		})
	}
}

func TestStageTrialRetryDoesNotClaimOtherRunsCache(t *testing.T) {
	for _, kind := range []string{"preparing", "publishing", "reference"} {
		t.Run(kind, func(t *testing.T) {
			runner, request := newRetryableTrialFixture(t)
			installDockerShim(t)
			lease := failRetryFixturePreparation(t, runner, request)
			if err := lease.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
			manager, _ := holdTrialPreparationCache(t, "another-check-run", kind)
			cachePath := filepath.Join(manager.root, checkDependencyCacheIndexName)
			before := readTestFile(t, cachePath)
			result, err := runner.RunCandidateCheck(context.Background(), request)
			if err != nil || !result.TrialCommandExecuted {
				t.Fatalf("an unrelated cache resource blocked the original trial: %+v %v", result, err)
			}
			if before != readTestFile(t, cachePath) {
				t.Fatal("trial continuation altered another run's cache facts")
			}
		})
	}
}
