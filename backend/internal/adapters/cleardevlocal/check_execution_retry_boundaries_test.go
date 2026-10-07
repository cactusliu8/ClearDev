package cleardevlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func readRetryFixtureState(t *testing.T, request ports.ClearDevCheckRequest) (string, checkRunState) {
	t.Helper()
	path, err := checkRunStatePath(request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var state checkRunState
	if err := json.Unmarshal([]byte(readTestFile(t, path)), &state); err != nil {
		t.Fatal(err)
	}
	return path, state
}

func failRetryFixturePreparation(t *testing.T, runner *Runner, request ports.ClearDevCheckRequest) *checkTemporaryLease {
	t.Helper()
	lease := occupyRetryFixtureCapacity(t)
	result, err := runner.RunCandidateCheck(context.Background(), request)
	if err == nil || result.Outcome != ports.ClearDevCheckInfraError {
		t.Fatalf("preparation did not fail: %+v %v", result, err)
	}
	_, state := readRetryFixtureState(t, request)
	if state.Attempt != 1 || state.ExecutionState != checkExecutionNotStarted {
		t.Fatalf("preparation failure not classified: %+v", state)
	}
	return lease
}

func TestStageTrialPreparationRetryIsBoundedAndPreservesBothFailures(t *testing.T) {
	runner, request := newRetryableTrialFixture(t)
	logPath := installDockerShim(t)
	lease := failRetryFixturePreparation(t, runner, request)
	path, _ := readRetryFixtureState(t, request)
	first := []byte(readTestFile(t, path))
	if _, err := runner.RunCandidateCheck(context.Background(), request); err == nil {
		t.Fatal("occupied capacity unexpectedly succeeded")
	}
	_, second := readRetryFixtureState(t, request)
	if second.Attempt != 2 || second.ExecutionState != checkExecutionNotStarted {
		t.Fatalf("second preparation failure: %+v", second)
	}
	if !bytes.Equal(first, []byte(readTestFile(t, path+".attempt-1"))) {
		t.Fatal("first failure was rewritten")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := []byte(readTestFile(t, path))
	for range 3 {
		if _, err := New().RunCandidateCheck(context.Background(), request); err == nil {
			t.Fatal("attempt budget reset on restart or repeat")
		}
	}
	if !bytes.Equal(before, []byte(readTestFile(t, path))) || readTestFile(t, logPath) != "" {
		t.Fatal("exhausted preparation retry changed evidence or invoked Docker")
	}
}

func TestStageTrialRetryRejectsLegacyUnknownAndConflictingEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*checkRunState)
	}{
		{"legacy", func(s *checkRunState) { s.Attempt, s.ExecutionState = 0, "" }},
		{"entered-with-empty-container-fields", func(s *checkRunState) { s.ExecutionState = checkExecutionStartedOrUnknown }},
		{"crashed-during-preparation", func(s *checkRunState) { s.State = "started" }},
		{"executed-flag", func(s *checkRunState) { s.Result.TrialCommandExecuted = true }},
		{"timeout", func(s *checkRunState) { s.Result.TimedOut = true }},
		{"different-candidate-result", func(s *checkRunState) { s.Result.CandidateSHA = strings.Repeat("f", 40) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner, request := newRetryableTrialFixture(t)
			logPath := installDockerShim(t)
			lease := failRetryFixturePreparation(t, runner, request)
			if err := lease.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
			path, state := readRetryFixtureState(t, request)
			tc.mutate(&state)
			if err := writeCheckRunState(path, state); err != nil {
				t.Fatal(err)
			}
			before := []byte(readTestFile(t, path))
			if _, err := New().RunCandidateCheck(context.Background(), request); err == nil {
				t.Fatal("untrusted/unsettled failure allowed retry")
			}
			if !bytes.Equal(before, []byte(readTestFile(t, path))) || readTestFile(t, logPath) != "" {
				t.Fatal("untrusted/unsettled evidence was rewritten or executed")
			}
		})
	}
}

func TestStageTrialRetryRechecksResourcesAndRequestIdentity(t *testing.T) {
	runner, request := newRetryableTrialFixture(t)
	logPath := installDockerShim(t)
	lease := failRetryFixturePreparation(t, runner, request)
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	path, _ := readRetryFixtureState(t, request)
	original := []byte(readTestFile(t, path))
	changed := request
	changed.CandidateSHA = strings.Repeat("f", 40)
	if _, err := runner.RunCandidateCheck(context.Background(), changed); err == nil {
		t.Fatal("different candidate reused the old RunID")
	}
	owned, err := acquireCheckTemporary(context.Background(), "owned-probe", "executable-probe", checkProbeReservationBytes, request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owned.Release(context.Background()) })
	if _, err := runner.RunCandidateCheck(context.Background(), request); err == nil {
		t.Fatal("outstanding preparation resource did not prevent retry")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.RunCandidateCheck(ctx, request); err == nil {
		t.Fatal("canceled retry did not stop")
	}
	if !bytes.Equal(original, []byte(readTestFile(t, path))) || readTestFile(t, logPath) != "" {
		t.Fatal("rejected retry consumed a new attempt or invoked Docker")
	}
	if err := owned.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunCandidateCheck(context.Background(), request)
	if err != nil || !result.TrialCommandExecuted {
		t.Fatalf("released original request cannot continue: %+v %v", result, err)
	}
}

func TestStageTrialRetryArchiveCrashAndConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict=%v", conflict), func(t *testing.T) {
			runner, request := newRetryableTrialFixture(t)
			logPath := installDockerShim(t)
			lease := failRetryFixturePreparation(t, runner, request)
			if err := lease.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
			path, _ := readRetryFixtureState(t, request)
			original := []byte(readTestFile(t, path))
			if conflict {
				if err := os.WriteFile(path+".attempt-1", []byte("different history"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := archiveUnstartedCheckAttempt(path); err != nil {
				t.Fatal(err)
			}
			result, err := New().RunCandidateCheck(context.Background(), request)
			if conflict {
				if err == nil || !strings.Contains(err.Error(), "conflicts") || readTestFile(t, logPath) != "" ||
					!bytes.Equal(original, []byte(readTestFile(t, path))) || readTestFile(t, path+".attempt-1") != "different history" {
					t.Fatalf("conflicting history was not protected: %+v %v", result, err)
				}
			} else if err != nil || !result.TrialCommandExecuted || !bytes.Equal(original, []byte(readTestFile(t, path+".attempt-1"))) {
				t.Fatalf("archive-only crash cannot safely resume: %+v %v", result, err)
			}
		})
	}
}

func TestStageTrialConcurrentRetryAcrossProcesses(t *testing.T) {
	if raw := os.Getenv("CLEARDEV_TRIAL_RETRY_CHILD_REQUEST"); raw != "" {
		var request ports.ClearDevCheckRequest
		if err := json.Unmarshal([]byte(raw), &request); err != nil {
			t.Fatal(err)
		}
		result, err := New().RunCandidateCheck(context.Background(), request)
		if err != nil || result.Outcome != ports.ClearDevCheckPass || !result.TrialCommandExecuted {
			t.Fatalf("child retry: %+v %v", result, err)
		}
		return
	}
	runner, request := newRetryableTrialFixture(t)
	logPath := installDockerShim(t)
	lease := failRetryFixturePreparation(t, runner, request)
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	path, _ := readRetryFixtureState(t, request)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	unlock, err := lockCheckFile(ctx, path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, 2)
	for i := range commands {
		commands[i] = exec.CommandContext(ctx, executable, "-test.run=^TestStageTrialConcurrentRetryAcrossProcesses$", "-test.count=1")
		commands[i].Env = append(os.Environ(), "CLEARDEV_TRIAL_RETRY_CHILD_REQUEST="+string(payload))
		commands[i].Stdout, commands[i].Stderr = &outputs[i], &outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	unlock()
	unlock = nil
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, outputs[i].String())
		}
	}
	if got := strings.Fields(readTestFile(t, logPath)); !reflect.DeepEqual(got, []string{"image", "run", "run"}) {
		t.Fatalf("concurrent processes duplicated preparation/formal container execution: %v", got)
	}
	_, state := readRetryFixtureState(t, request)
	if state.Attempt != 2 || state.State != "settled" || state.ExecutionState != checkExecutionStartedOrUnknown {
		t.Fatalf("concurrent retry lost its execution claim: %+v", state)
	}
}

func TestStageTrialCancellationCannotEraseExecutionBoundary(t *testing.T) {
	runner, request := newRetryableTrialFixture(t)
	logPath := installDockerCancellationShim(t, "formal")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type response struct {
		result ports.ClearDevCheckResult
		err    error
	}
	done := make(chan response, 1)
	go func() {
		result, err := runner.RunCandidateCheck(ctx, request)
		done <- response{result, err}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("CLEARDEV_DOCKER_READY")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("candidate executor did not reach the instrumented boundary")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, running := readRetryFixtureState(t, request)
	if running.State != "started" || running.ExecutionState != checkExecutionStartedOrUnknown {
		t.Fatalf("execution started before its marker was durable: %+v", running)
	}
	cancel()
	var first response
	select {
	case first = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("canceled command did not settle")
	}
	if first.err == nil || first.result.TrialCommandExecuted {
		t.Fatalf("fixture must model an error whose execution flag was cleared: %+v", first)
	}
	_, settled := readRetryFixtureState(t, request)
	if settled.ExecutionState != checkExecutionStartedOrUnknown {
		t.Fatal("cancellation erased the execution boundary")
	}
	before := readTestFile(t, logPath)
	second, err := New().RunCandidateCheck(context.Background(), request)
	if err == nil || !reflect.DeepEqual(second, first.result) || before != readTestFile(t, logPath) {
		t.Fatalf("cancellation was mistaken for safe preparation retry: %+v %v", second, err)
	}
}

func TestStageTrialPreExecutionRecoveryRealContainer(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	runner, request := newRetryableTrialFixture(t)
	lease := failRetryFixturePreparation(t, runner, request)
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunCandidateCheck(context.Background(), request)
	if err != nil || result.Outcome != ports.ClearDevCheckPass || !result.TrialCommandExecuted || !strings.Contains(result.OutputSummary, "trial-ran\n") {
		t.Fatalf("real candidate command did not execute after capacity recovered: %+v %v", result, err)
	}
	read, found, err := New().ReadCandidateCheck(context.Background(), request)
	if err != nil || !found || !reflect.DeepEqual(result, read) {
		t.Fatalf("real retry receipt did not survive restart: %+v %v", read, err)
	}
}
