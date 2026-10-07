package cleardevtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestSeedDirectionChangeV1WorkBindsManagedDirtyWorktree(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initPrepProjectRepo(t)
	projectID := "ao-s05-prep"
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: projectID, Path: repo, Kind: domain.ProjectKindSingleRepo,
		RegisteredAt: now, Config: domain.ProjectConfig{DefaultBranch: "main"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateClearDevRequirement(ctx, core.InitialRequirement{
		Requirement: core.DevelopmentRequirement{
			ID: "req-s05-prep", AOProjectID: projectID, Name: "S05 prep", CreatedAt: now, UpdatedAt: now,
		},
		Version: core.RequirementVersion{
			ID: "req-s05-prep-v1", DevelopmentRequirementID: "req-s05-prep", Version: 1,
			RequirementText: "requirement v1", SHA256: digestHex("requirement v1"),
			Status: core.RequirementVersionStatusDraft, CreatedAt: now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyClearDevAction(ctx, core.ActionRequest{
		Action: core.ActionSubmitRequirementConfirmation, SubjectID: "req-s05-prep-v1", At: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyClearDevAction(ctx, core.ActionRequest{
		Action: core.ActionConfirmRequirementVersion, SubjectID: "req-s05-prep-v1",
		TrustedHumanDecision: true, At: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	prep := SeedDirectionChangeV1Work(t, store, "req-s05-prep", dataDir)
	if !strings.HasPrefix(prep.DirtyRepo, filepath.Join(dataDir, "worktrees")) {
		t.Fatalf("managed worktree %q is not under %s/worktrees", prep.DirtyRepo, dataDir)
	}
	dispatchID, baseSHA, sessionID, err := store.GetClearDevTaskDispatchFacts(ctx, prep.RunningTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if dispatchID == "" || dispatchID != prep.DispatchID || baseSHA != prep.DirtyHeadSHA || sessionID != prep.BuilderSessionID {
		t.Fatalf("dispatch facts dispatch=%q base=%q session=%q prep=%#v", dispatchID, baseSHA, sessionID, prep)
	}
	status := strings.TrimSpace(runPrepGit(t, prep.DirtyRepo, "status", "--porcelain=v1"))
	if !strings.Contains(status, "?? dirty.txt") {
		t.Fatalf("managed worktree status = %q", status)
	}
	if strings.TrimSpace(runPrepGit(t, prep.DirtyRepo, "rev-parse", "HEAD")) != prep.DirtyHeadSHA {
		t.Fatal("managed worktree HEAD changed during seed")
	}
	flow, ok, err := store.GetClearDevStandardFlow(ctx, "req-s05-prep")
	if err != nil || !ok {
		t.Fatalf("read STANDARD facts: ok=%t err=%v", ok, err)
	}
	var stewardEnded, builderBound bool
	for _, binding := range flow.RoleBindings {
		if binding.Role == core.StandardRoleSteward && binding.Status == core.RoleBindingStatusEnded {
			stewardEnded = true
		}
		if binding.Role == core.StandardRoleBuilder && binding.Status == core.RoleBindingStatusBound &&
			binding.AOSessionID == prep.BuilderSessionID {
			builderBound = true
		}
	}
	if !stewardEnded || !builderBound || len(flow.Dispatches) != 1 ||
		flow.Dispatches[0].Status != core.DispatchStatusAccepted {
		t.Fatalf("STANDARD facts after prep = %#v", flow)
	}
	plannedFactsDispatch, _, plannedSession, err := store.GetClearDevTaskDispatchFacts(ctx, prep.PlannedTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if plannedFactsDispatch != "" || plannedSession != "" {
		t.Fatalf("planned task should stay unbound, got dispatch=%q session=%q", plannedFactsDispatch, plannedSession)
	}
}

func initPrepProjectRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cleardev-s05-prep-repo-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	runPrepGit(t, dir, "init", "-b", "main")
	runPrepGit(t, dir, "config", "user.email", "test@example.com")
	runPrepGit(t, dir, "config", "user.name", "ClearDev Test")
	if err := os.WriteFile(filepath.Join(dir, "src.js"), []byte("export const n = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPrepGit(t, dir, "add", "src.js")
	runPrepGit(t, dir, "commit", "-m", "base")
	runPrepGit(t, dir, "remote", "add", "origin", ".")
	runPrepGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	runPrepGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return dir
}

func digestHex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
