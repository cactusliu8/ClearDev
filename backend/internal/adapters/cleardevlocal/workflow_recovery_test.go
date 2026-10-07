package cleardevlocal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestWorkflowRecoveryRequiresSettledExecutorReceipt(t *testing.T) {
	runner, freeze := newProjectFreezeFixture(t, true)
	check := freeze.ProjectExecution.Basis.Checks[0]
	request, err := normalizeCheckRequest(ports.ClearDevCheckRequest{RunID: "workflow-receipt", WorkspacePath: freeze.WorkspacePath, CandidateSHA: freeze.BaseSHA, Image: core.StandardCandidateCheckImage, Argv: check.Argv, Timeout: time.Duration(check.TimeoutSeconds) * time.Second, ProjectExecution: freeze.ProjectExecution})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := checkRequestSHA256(request)
	if err != nil {
		t.Fatal(err)
	}
	path, err := checkRunStatePath(request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := inspectCandidateSource(context.Background(), freeze.WorkspacePath, freeze.BaseSHA, t.TempDir(), defaultCandidateSourceLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"settled", "unknown", "wrong-request", "business-failure", "corrupt", "terminated", "terminated-timeout", "terminated-truncated", "terminated-unknown"} {
		t.Run(mode, func(t *testing.T) {
			state := checkRunState{Version: checkRunStateVersion, Fingerprint: fingerprint, State: "settled", Result: &ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError}, Error: "docker unavailable before materialization"}
			if strings.HasPrefix(mode, "terminated") {
				state.SourceManifest = &facts.Manifest
				state.Result = &ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckFail, ExitCode: 137, Image: projectInstallImage, TrialCommandExecuted: true, CandidateSHA: request.CandidateSHA, SourceManifestID: facts.ManifestID, SourceRootTreeOID: facts.Manifest.RootTreeOID, SourceBytes: facts.Manifest.BlobBytes, SourceItems: int64(facts.Manifest.ItemCount)}
				state.Result.TimedOut = mode == "terminated-timeout"
				state.Result.OutputTruncated = mode == "terminated-truncated"
				if mode == "terminated-unknown" {
					state.State = "started"
				}
			}
			switch mode {
			case "unknown":
				state.State = "started"
			case "wrong-request":
				state.Fingerprint = "wrong"
			case "business-failure":
				state.Result.Outcome = ports.ClearDevCheckFail
			}
			if err := writeCheckRunState(path, state); err != nil {
				t.Fatal(err)
			}
			if mode == "corrupt" {
				if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			allowed, err := runner.CanRetryWorkflowCheck(context.Background(), request)
			if allowed != (mode == "settled" || mode == "terminated") || (err != nil) != (mode == "corrupt") {
				t.Fatalf("eligible=%v err=%v", allowed, err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("original receipt changed")
			}
		})
	}
}

func TestWorkflowRecoveryRejectsOutstandingCheckReservation(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	ctx := context.Background()
	lease, err := acquireCheckTemporary(ctx, "stopped-check", "candidate-check", checkRunReservationBytes)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := workflowCheckResourcesReleased(ctx, "stopped-check")
	if err != nil || allowed {
		t.Fatalf("outstanding container cleanup permitted retry: %v %v", allowed, err)
	}
	// This fixture never launches a container. The real release path must confirm
	// cleanup before removing the reservation; an unknown cleanup retains it.
	if err = lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	allowed, err = workflowCheckResourcesReleased(ctx, "stopped-check")
	if err != nil || !allowed {
		t.Fatalf("confirmed cleanup did not unblock retry: %v %v", allowed, err)
	}
}

func TestWorkflowRecoveryRejectsProbeCleanupUnknown(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	bin := t.TempDir()
	docker := filepath.Join(bin, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	ctx := context.Background()
	lease, err := acquireCheckTemporary(ctx, "random-probe", "executable-probe", checkProbeReservationBytes, "original-check")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(lease.CIDFile(), []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if err = lease.Release(ctx); err == nil {
		t.Fatal("unknown cleanup accepted")
	}
	allowed, err := workflowCheckResourcesReleased(ctx, "original-check")
	if err != nil || allowed {
		t.Fatalf("unknown probe cleanup allowed new check: %v %v", allowed, err)
	}
	// Another action is not confused with this run's durable parent binding.
	allowed, err = workflowCheckResourcesReleased(ctx, "other-check")
	if err != nil || !allowed {
		t.Fatalf("probe was not bound to its run: %v %v", allowed, err)
	}
	if err = os.WriteFile(docker, []byte("#!/bin/sh\n[ \"$1\" = inspect ] && printf '[]\\n'\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	allowed, err = workflowCheckResourcesReleased(ctx, "original-check")
	if err != nil || !allowed {
		t.Fatalf("confirmed probe cleanup remained blocked: %v %v", allowed, err)
	}
}
