package sessionmanager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/cleardevlocal"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/gitworktree"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// This fixture uses real Git and the complete Manager.Spawn workspace path.
// Only persistence/agent launch use existing explicit manager test doubles.
type projectSourceWorkspace struct {
	*gitworktree.Workspace
	resolves int
	fetches  int
}

func (w *projectSourceWorkspace) ResolveDefaultBranch(ctx context.Context, repo, branch string) (ports.WorkspaceDefaultBranch, error) {
	w.resolves++
	return w.Workspace.ResolveDefaultBranch(ctx, repo, branch)
}

func (w *projectSourceWorkspace) FetchDefaultBranch(ctx context.Context, repo string, target ports.WorkspaceDefaultBranch) error {
	w.fetches++
	return w.Workspace.FetchDefaultBranch(ctx, repo, target)
}

func TestProjectSourceManagerSpawnKeepsSavedCommit(t *testing.T) {
	ctx := context.Background()
	m, store, launcher := newIdempotentChatManager()
	m.dataDir = t.TempDir()
	repo, remote := newManagerGitRepo(t), t.TempDir()
	runManagerGit(t, remote, "init", "--bare", "-b", "main")
	runManagerGit(t, repo, "remote", "add", "origin", remote)
	runManagerGit(t, repo, "branch", "release/2026")
	runManagerGit(t, repo, "switch", "-c", "remote-update")
	runManagerGit(t, repo, "commit", "--allow-empty", "-m", "Remote release differs from local release")
	remoteSHA := strings.TrimSpace(runManagerGit(t, repo, "rev-parse", "HEAD"))
	runManagerGit(t, repo, "push", "origin", "HEAD:refs/heads/release/2026")
	config := testRoleAgents()
	config.DefaultBranch = "release/2026"
	store.projects["mer"] = domain.ProjectRecord{ID: "mer", Path: repo, Config: config}
	native, err := gitworktree.New(gitworktree.Options{ManagedRoot: t.TempDir(), RepoResolver: gitworktree.StaticRepoResolver{"mer": repo}})
	if err != nil {
		t.Fatal(err)
	}
	workspace := &projectSourceWorkspace{Workspace: native}
	m.workspace = workspace
	observed, err := cleardevlocal.New().InspectProjectSource(ctx, repo, config.DefaultBranch)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ports.SpawnConfig{
		ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Branch: "cleardev-project-plan", RequestedMode: domain.SessionModeChat,
		AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		CreationIdempotencyKey: "cleardev/project-stage/planner/source-test",
		WorkspaceBaseCommitSHA: observed.BaseCommitSHA,
	}
	record, _, _, err := m.Spawn(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	actual := strings.TrimSpace(runManagerGit(t, record.Metadata.WorkspacePath, "rev-parse", "HEAD"))
	if actual != observed.BaseCommitSHA || record.Metadata.DiffBaseSHA != observed.BaseCommitSHA {
		t.Fatalf("Manager changed selected source %s to worktree=%s metadata=%s (remote=%s)", observed.BaseCommitSHA, actual, record.Metadata.DiffBaseSHA, remoteSHA)
	}
	if record.Metadata.DiffBaseRef != observed.BaseCommitSHA {
		t.Fatalf("recovery source is not pinned: %q", record.Metadata.DiffBaseRef)
	}
	if workspace.resolves != 0 || workspace.fetches != 0 {
		t.Fatalf("a pinned source still used moving default resolution: resolves=%d fetches=%d", workspace.resolves, workspace.fetches)
	}
	replayed, _, _, err := m.Spawn(ctx, cfg)
	if err != nil || replayed.ID != record.ID || len(launcher.started) != 1 {
		t.Fatalf("pinned source replay created another launch: %+v %v", replayed, err)
	}
	cfg.WorkspaceBaseCommitSHA = remoteSHA
	if _, _, _, err := m.Spawn(ctx, cfg); err == nil || len(launcher.started) != 1 {
		t.Fatal("same creation key silently rebound its pinned source")
	}
}

func TestProjectPinnedStewardsKeepSeparateWorktrees(t *testing.T) {
	ctx := context.Background()
	repo := newManagerGitRepo(t)
	selected := strings.TrimSpace(runManagerGit(t, repo, "rev-parse", "HEAD"))
	m, store, _ := newIdempotentChatManager()
	m.dataDir = t.TempDir()
	config := testRoleAgents()
	config.DefaultBranch = "main"
	store.projects["mer"] = domain.ProjectRecord{ID: "mer", Path: repo, Config: config}
	native, err := gitworktree.New(gitworktree.Options{ManagedRoot: t.TempDir(), RepoResolver: gitworktree.StaticRepoResolver{"mer": repo}})
	if err != nil {
		t.Fatal(err)
	}
	m.workspace = native
	spawn := func(branch, key string) domain.SessionRecord {
		t.Helper()
		record, _, _, err := m.Spawn(ctx, ports.SpawnConfig{
			ProjectID: "mer", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
			Branch: branch, RequestedMode: domain.SessionModeChat,
			AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
			CreationIdempotencyKey: key, WorkspaceBaseCommitSHA: selected,
		})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	first := spawn("codex/cleardev-stage-steward-first", "cleardev/steward/first")
	second := spawn("codex/cleardev-stage-steward-second", "cleardev/steward/second")
	if first.Metadata.WorkspacePath == second.Metadata.WorkspacePath {
		t.Fatal("separate pinned Stewards shared one mutable worktree")
	}
	for _, record := range []domain.SessionRecord{first, second} {
		if actual := strings.TrimSpace(runManagerGit(t, record.Metadata.WorkspacePath, "rev-parse", "HEAD")); actual != selected {
			t.Fatalf("pinned Steward %s moved or lost its worktree: %s", record.ID, actual)
		}
	}
}

func TestProjectSourcePinnedSpawnKeepsCommitWhenSessionBranchAlreadyExists(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runManagerGit(t, repo, "init", "-b", "main")
	runManagerGit(t, repo, "config", "user.email", "ao@example.com")
	runManagerGit(t, repo, "config", "user.name", "AO Tests")
	runManagerGit(t, repo, "commit", "--allow-empty", "-m", "empty initial commit")
	selected := strings.TrimSpace(runManagerGit(t, repo, "rev-parse", "HEAD"))
	branch := "cleardev-empty-existing"
	runManagerGit(t, repo, "branch", branch, selected)

	m, store, launcher := newIdempotentChatManager()
	m.dataDir = t.TempDir()
	config := testRoleAgents()
	config.DefaultBranch = "main"
	store.projects["mer"] = domain.ProjectRecord{ID: "mer", Path: repo, Config: config}
	native, err := gitworktree.New(gitworktree.Options{ManagedRoot: t.TempDir(), RepoResolver: gitworktree.StaticRepoResolver{"mer": repo}})
	if err != nil {
		t.Fatal(err)
	}
	m.workspace = &projectSourceWorkspace{Workspace: native}
	record, _, _, err := m.Spawn(ctx, ports.SpawnConfig{
		ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Branch: branch, RequestedMode: domain.SessionModeChat,
		AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		CreationIdempotencyKey: "cleardev/project-stage/steward/empty-existing",
		WorkspaceBaseCommitSHA: selected,
	})
	if err != nil {
		t.Fatalf("pinned existing branch spawn: %v", err)
	}
	if actual := strings.TrimSpace(runManagerGit(t, record.Metadata.WorkspacePath, "rev-parse", "HEAD")); actual != selected {
		t.Fatalf("worktree HEAD = %s, want pinned %s", actual, selected)
	}
	if record.Metadata.DiffBaseSHA != selected || record.Metadata.DiffBaseRef != selected {
		t.Fatalf("pinned metadata = sha:%q ref:%q, want %s", record.Metadata.DiffBaseSHA, record.Metadata.DiffBaseRef, selected)
	}
	if len(launcher.started) != 1 {
		t.Fatalf("provider launches = %d, want 1", len(launcher.started))
	}
}

func TestProjectSourcePinnedPreparedLaunchSurvivesManagerRestart(t *testing.T) {
	ctx := context.Background()
	repo := newManagerGitRepo(t)
	selected := strings.TrimSpace(runManagerGit(t, repo, "rev-parse", "HEAD"))
	base := newFakeStore()
	base.projects["mer"] = domain.ProjectRecord{ID: "mer", Path: repo, Config: testRoleAgents()}
	store := &idempotentFakeStore{fakeStore: base}
	native, err := gitworktree.New(gitworktree.Options{ManagedRoot: t.TempDir(), RepoResolver: gitworktree.StaticRepoResolver{"mer": repo}})
	if err != nil {
		t.Fatal(err)
	}
	workspace := &projectSourceWorkspace{Workspace: native}
	registry := &durableProviderRegistry{}
	launcher := &recoveringChatLauncher{registry: registry}
	dataDir := t.TempDir()
	manager := func(lcm *fakeLCM) *Manager {
		return New(Deps{
			Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: workspace, Store: store,
			Messenger: &fakeMessenger{}, Chat: launcher, Lifecycle: lcm, DataDir: dataDir,
			LookPath: func(string) (string, error) { return "/bin/true", nil },
		})
	}
	cfg := ports.SpawnConfig{
		ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Branch: "cleardev-project-recover", RequestedMode: domain.SessionModeChat,
		AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		CreationIdempotencyKey: "cleardev/project-stage/planner/recover",
		WorkspaceBaseCommitSHA: selected,
	}
	if _, _, _, err := manager(&fakeLCM{store: base, markSpawnedErr: errors.New("crash before provider ID persistence")}).Spawn(ctx, cfg); err == nil {
		t.Fatal("crash fixture unexpectedly succeeded")
	}
	prepared := base.sessions["mer-1"]
	if prepared.Metadata.DiffBaseSHA != selected || prepared.Metadata.DiffBaseRef != selected || registry.creates != 1 {
		t.Fatalf("prepared intent lost its source: %+v creates=%d", prepared, registry.creates)
	}
	// The manager recovers its prior launch lease; the product service separately
	// checks source currentness before sending any new Steward/Planner work.
	runManagerGit(t, repo, "commit", "--allow-empty", "-m", "Default moved after durable launch intent")
	restarted := manager(&fakeLCM{store: base})
	recovered, _, _, err := restarted.Spawn(ctx, cfg)
	if err != nil || recovered.ID != prepared.ID || recovered.Metadata.DiffBaseSHA != selected || recovered.Metadata.DiffBaseRef != selected {
		t.Fatalf("recovery changed the pinned launch source: %+v %v", recovered, err)
	}
	if actual := strings.TrimSpace(runManagerGit(t, recovered.Metadata.WorkspacePath, "rev-parse", "HEAD")); actual != selected {
		t.Fatalf("restored worktree used %s instead of %s", actual, selected)
	}
	if registry.creates != 1 || registry.recovers != 1 || workspace.resolves != 0 || workspace.fetches != 0 {
		t.Fatalf("recovery duplicated or rebound the launch: creates=%d recovers=%d resolve=%d fetch=%d", registry.creates, registry.recovers, workspace.resolves, workspace.fetches)
	}
}

func TestProjectSourcePinRequiresInternalExactIdentity(t *testing.T) {
	for _, tc := range []struct{ name, key, sha string }{
		{"no-key", "", strings.Repeat("a", 40)},
		{"branch-not-sha", "internal-key", "main"},
		{"non-canonical", "internal-key", strings.Repeat("A", 40)},
		{"malformed", "internal-key", strings.Repeat("z", 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, store, launcher := newIdempotentChatManager()
			_, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{
				ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
				RequestedMode: domain.SessionModeChat, CreationIdempotencyKey: tc.key, WorkspaceBaseCommitSHA: tc.sha,
			})
			if err == nil || len(store.sessions) != 0 || len(launcher.started) != 0 {
				t.Fatalf("invalid pin created durable state or launched: %v", err)
			}
		})
	}
	sha := strings.Repeat("a", 40)
	if refs := spawnDiffBaseRefCandidates(sha); len(refs) != 1 || refs[0] != sha {
		t.Fatalf("full commit treated as a moving branch: %v", refs)
	}
}
