package cleardevtest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/gitworktree"
	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// DirectionChangePrep is the S05 test-only v1 work snapshot. It uses production
// store and workspace adapters, never raw SQL.
type DirectionChangePrep struct {
	PlannedTaskID    string
	RunningTaskID    string
	DirtyRepo        string
	DirtyHeadSHA     string
	BuilderSessionID string
	DispatchID       string
	BaseSHA          string
}

// SeedDirectionChangeV1Work creates one PLANNED task and one RUNNING task, then
// binds the running task to an accepted dispatch, Builder session, and dirty
// managed worktree under dataDir/worktrees.
func SeedDirectionChangeV1Work(t testing.TB, store *sqlite.Store, requirementID, dataDir string) DirectionChangePrep {
	t.Helper()
	ctx := context.Background()
	service := cleardevsvc.New(cleardevsvc.Deps{Facts: store, HumanDecisions: store, AO: store})
	snapshot, ok, err := store.GetClearDevRequirement(ctx, requirementID)
	if err != nil || !ok {
		t.Fatalf("load requirement for direction prep: ok=%v err=%v", ok, err)
	}
	planned, err := service.CreateDevelopmentTask(ctx, requirementID, cleardevsvc.CreateDevelopmentTaskInput{
		Title: "planned v1 task", Mode: core.WorkModeQuick, MaxReworkCount: 0,
		Permissions:    cleardevsvc.CreatePathPermissionsInput{WritePaths: []string{"src/**"}},
		RequiredChecks: []cleardevsvc.CreateRequiredCheckInput{{Name: "node-all", Kind: "command"}},
	})
	if err != nil {
		t.Fatalf("create planned task: %v", err)
	}
	running, err := service.CreateDevelopmentTask(ctx, requirementID, cleardevsvc.CreateDevelopmentTaskInput{
		Title: "running v1 task", Mode: core.WorkModeQuick, MaxReworkCount: 0,
		Permissions:    cleardevsvc.CreatePathPermissionsInput{WritePaths: []string{"src/**"}},
		RequiredChecks: []cleardevsvc.CreateRequiredCheckInput{{Name: "node-all", Kind: "command"}},
	})
	if err != nil {
		t.Fatalf("create running task: %v", err)
	}
	if err := service.StartDevelopmentTask(ctx, running.ID); err != nil {
		t.Fatalf("start running task: %v", err)
	}
	project, found, err := store.GetProject(ctx, snapshot.Requirement.AOProjectID)
	if err != nil || !found {
		t.Fatalf("load AO project for direction prep: found=%v err=%v", found, err)
	}
	now := time.Now().UTC()
	stewardSession, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: domain.ProjectID(project.ID), Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create Steward session: %v", err)
	}
	plannerKey := uuid.NewString()
	plannerSession, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: domain.ProjectID(project.ID), Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto,
		CreationIdempotencyKey: plannerKey, CreationRequestFingerprint: "s05-direction-prep-planner",
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create Planner session: %v", err)
	}
	builderKey := uuid.NewString()
	builderSession, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: domain.ProjectID(project.ID), Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto,
		CreationIdempotencyKey: builderKey, CreationRequestFingerprint: "s05-direction-prep-builder",
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create Builder session: %v", err)
	}
	managedRoot := filepath.Join(dataDir, "worktrees")
	if err := os.MkdirAll(filepath.Join(managedRoot, project.ID), 0o750); err != nil {
		t.Fatalf("create managed worktree root: %v", err)
	}
	workspace, err := gitworktree.New(gitworktree.Options{
		ManagedRoot:  managedRoot,
		RepoResolver: gitworktree.StaticRepoResolver{domain.ProjectID(project.ID): project.Path},
	})
	if err != nil {
		t.Fatalf("open managed workspace adapter: %v", err)
	}
	info, err := workspace.Create(ctx, ports.WorkspaceConfig{
		ProjectID:  domain.ProjectID(project.ID),
		SessionID:  builderSession.ID,
		Kind:       domain.KindWorker,
		Branch:     "ao/" + string(builderSession.ID),
		BaseBranch: project.Config.WorktreeBaseBranch(),
	})
	if err != nil {
		t.Fatalf("create managed dirty worktree: %v", err)
	}
	head := strings.TrimSpace(runPrepGit(t, info.Path, "rev-parse", "HEAD"))
	if len(head) != 40 {
		t.Fatalf("managed worktree HEAD %q is not a 40-character SHA-1", head)
	}
	builderSession.Metadata.Branch = info.Branch
	builderSession.Metadata.WorkspacePath = info.Path
	builderSession.Metadata.WorkspaceRepoPath = info.RepoPath
	builderSession.Metadata.DiffBaseSHA = head
	builderSession.Metadata.DiffBaseRef = info.BaseRef
	builderSession.UpdatedAt = now
	if err := store.UpdateSession(ctx, builderSession); err != nil {
		t.Fatalf("bind Builder session workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "dirty.txt"), []byte("dirty"), 0o600); err != nil {
		t.Fatalf("write uncommitted dirty.txt: %v", err)
	}
	if err := store.BindClearDevRunningTaskWorkspaceForTest(ctx, sqlitestore.BindClearDevRunningTaskWorkspaceCommand{
		DevelopmentRequirementID: requirementID, RunningTaskID: running.ID,
		BuilderSessionID: string(builderSession.ID), StewardSessionID: string(stewardSession.ID),
		PlannerSessionID: string(plannerSession.ID), BuilderSpawnKey: builderKey,
		PlannerSpawnKey: plannerKey, StewardSpawnKey: uuid.NewString(),
		BaseCommitSHA: head, At: now,
	}); err != nil {
		t.Fatalf("bind running task dispatch workspace: %v", err)
	}
	dispatchID, baseSHA, sessionID, err := store.GetClearDevTaskDispatchFacts(ctx, running.ID)
	if err != nil {
		t.Fatalf("read bound dispatch facts: %v", err)
	}
	if dispatchID == "" || baseSHA != head || sessionID != string(builderSession.ID) {
		t.Fatalf("bound dispatch facts dispatch=%q base=%q session=%q, want base=%s session=%s",
			dispatchID, baseSHA, sessionID, head, builderSession.ID)
	}
	return DirectionChangePrep{
		PlannedTaskID: planned.ID, RunningTaskID: running.ID,
		DirtyRepo: info.Path, DirtyHeadSHA: head,
		BuilderSessionID: string(builderSession.ID), DispatchID: dispatchID, BaseSHA: baseSHA,
	}
}

func runPrepGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
