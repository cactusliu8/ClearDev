package cleardevlocal

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReviewerRequestedCheckUsesProductionRunnerAndStableSHA(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	baselineGateData(t)
	repo := baselineGateRepo(t, false)
	sha := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	spec, ok := core.ApprovedReviewCheck("demo-api")
	if !ok {
		t.Fatal("missing approved check")
	}
	request := ports.ClearDevCheckRequest{RunID: core.ReviewCheckRunID("review", "demo-api"), WorkspacePath: repo, CandidateSHA: sha, Image: core.StandardCandidateCheckImage, Argv: spec.Argv, Timeout: time.Duration(spec.TimeoutSeconds) * time.Second, MemoryBytes: baselineMemoryBytes, PidsLimit: 64, OutputLimit: 64 * 1024}
	first, err := New().RunCandidateCheck(context.Background(), request)
	if err != nil || first.Outcome != ports.ClearDevCheckPass || first.CandidateSHA != sha || first.ExitCode != 0 || first.SourceManifestID == "" {
		t.Fatalf("check=%+v err=%v", first, err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 93\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	second, err := New().RunCandidateCheck(context.Background(), request)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated external check: %v", err)
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain", "--untracked-files=all")); status != "" {
		t.Fatal("check changed candidate")
	}
	if err := os.WriteFile(filepath.Join(repo, "frontend", "extra.css"), []byte("/* next candidate */\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.CandidateSHA = mailCommit(t, repo)
	if _, err := New().RunCandidateCheck(context.Background(), request); err == nil {
		t.Fatal("old RunID accepted another SHA")
	}
	t.Logf("approved demo-api ran in production isolation on %s; restart reused exact receipt", sha)
}
