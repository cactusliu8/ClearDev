package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// idempotentFakeStore adds the S02 durable-create seam without changing the
// broad fakeStore used by older manager tests.
type idempotentFakeStore struct {
	*fakeStore
	claimMu      sync.Mutex
	claimToken   string
	claimExpires time.Time
}

type controlledIdempotentStore struct{ *idempotentFakeStore }

func (*controlledIdempotentStore) IsClearDevControlledSession(context.Context, string) (bool, error) {
	return true, nil
}

func (s *idempotentFakeStore) CreateSessionIdempotent(_ context.Context, rec domain.SessionRecord) (domain.SessionRecord, bool, error) {
	for _, existing := range s.sessions {
		if existing.CreationIdempotencyKey != rec.CreationIdempotencyKey {
			continue
		}
		if existing.CreationRequestFingerprint != rec.CreationRequestFingerprint {
			return domain.SessionRecord{}, false, errors.New("creation key is bound to a different request")
		}
		return existing, false, nil
	}
	created, err := s.fakeStore.CreateSession(context.Background(), rec)
	return created, err == nil, err
}

func (s *idempotentFakeStore) TryClaimSessionCreation(
	_ context.Context,
	_ domain.SessionID,
	_, _, token string,
	now, expiresAt time.Time,
) (bool, error) {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	if s.claimToken != "" && s.claimToken != token && s.claimExpires.After(now) {
		return false, nil
	}
	s.claimToken = token
	s.claimExpires = expiresAt
	return true, nil
}

func (s *idempotentFakeStore) RenewSessionCreationClaim(
	_ context.Context,
	_ domain.SessionID,
	token string,
	expiresAt time.Time,
) (bool, error) {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	if s.claimToken != token {
		return false, nil
	}
	s.claimExpires = expiresAt
	return true, nil
}

func (s *idempotentFakeStore) ReleaseSessionCreationClaim(_ context.Context, _ domain.SessionID, token string) error {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	if s.claimToken == token {
		s.claimToken = ""
		s.claimExpires = time.Time{}
	}
	return nil
}

type durableProviderRegistry struct {
	providerID string
	creates    int
	recovers   int
}

type recoveringChatLauncher struct {
	registry *durableProviderRegistry
}

func (*recoveringChatLauncher) SupportsChat(domain.AgentHarness) bool { return true }
func (*recoveringChatLauncher) PreflightChat(context.Context, domain.AgentHarness) error {
	return nil
}
func (l *recoveringChatLauncher) StartChat(_ context.Context, cfg ChatStart) (ChatStarted, error) {
	if cfg.CreationIdempotencyKey == "" {
		return ChatStarted{}, errors.New("missing creation key")
	}
	if cfg.RecoverProviderConversation {
		l.registry.recovers++
		if l.registry.providerID == "" {
			return ChatStarted{}, errors.New("no tagged provider conversation")
		}
	} else {
		l.registry.creates++
		if l.registry.providerID != "" {
			return ChatStarted{}, errors.New("duplicate provider create")
		}
		l.registry.providerID = fmt.Sprintf("thread-%d", l.registry.creates)
	}
	started := ChatStarted{
		ProviderConversationID: l.registry.providerID,
		ControllerGeneration:   "generation-1",
	}
	if cfg.ControllerReady != nil {
		if _, err := cfg.ControllerReady(started); err != nil {
			// This is the exact crash window under test: thread/start has already
			// committed providerID, but the AO session has not persisted it.
			return ChatStarted{}, err
		}
	}
	return started, nil
}
func (*recoveringChatLauncher) StartChatTurn(context.Context, domain.SessionID, string) (string, error) {
	return "", nil
}
func (*recoveringChatLauncher) RelayChatTurn(context.Context, domain.SessionID, string) (string, error) {
	return "", nil
}
func (*recoveringChatLauncher) RelayChatTurnWithID(context.Context, domain.SessionID, string, string) (string, error) {
	return "", nil
}
func (*recoveringChatLauncher) HasLiveChatController(domain.SessionID) bool      { return false }
func (*recoveringChatLauncher) StopChat(context.Context, domain.SessionID) error { return nil }

func TestSpawnControlledChatDisablesProviderMemoriesOnFreshCreate(t *testing.T) {
	base := newFakeStore()
	base.projects[string(chatTestProject)] = domain.ProjectRecord{ID: string(chatTestProject), Config: testRoleAgents()}
	store := &controlledIdempotentStore{idempotentFakeStore: &idempotentFakeStore{fakeStore: base}}
	launcher := &recordingLauncher{}
	manager := New(Deps{
		Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: &fakeWorkspace{},
		Store: store, Messenger: &fakeMessenger{}, Chat: launcher, Lifecycle: &fakeLCM{store: base},
		DataDir: "/ao-test-data", LookPath: func(string) (string, error) { return "/bin/true", nil },
	})
	_, _, _, err := manager.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: chatTestProject, Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		RequestedMode:          domain.SessionModeChat,
		AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		CreationIdempotencyKey: "cleardev-product-steward:test",
		Prompt:                 "inspect only",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(launcher.started) != 1 || !launcher.started[0].DisableMemories {
		t.Fatalf("fresh controlled Chat start = %+v, want memories disabled", launcher.started)
	}
}

func TestSpawnIdempotentChatRecoversProviderCreateAcrossManagerRestart(t *testing.T) {
	base := newFakeStore()
	base.projects[string(chatTestProject)] = domain.ProjectRecord{ID: string(chatTestProject), Config: testRoleAgents()}
	store := &idempotentFakeStore{fakeStore: base}
	workspace := &fakeWorkspace{}
	registry := &durableProviderRegistry{}
	launcher := &recoveringChatLauncher{registry: registry}
	firstLCM := &fakeLCM{store: base, markSpawnedErr: errors.New("simulated process crash before provider id persistence")}
	newManager := func(lcm *fakeLCM) *Manager {
		return New(Deps{
			Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: workspace,
			Store: store, Messenger: &fakeMessenger{}, Chat: launcher, Lifecycle: lcm,
			DataDir: "/ao-test-data", LookPath: func(string) (string, error) { return "/bin/true", nil },
		})
	}
	cfg := ports.SpawnConfig{
		ProjectID: chatTestProject, Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		RequestedMode:          domain.SessionModeChat,
		AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		CreationIdempotencyKey: "cleardev/requirement-1/builder/attempt-1",
	}

	if _, _, _, err := newManager(firstLCM).Spawn(context.Background(), cfg); err == nil {
		t.Fatal("first Spawn succeeded despite simulated crash")
	}
	if registry.creates != 1 || registry.providerID == "" {
		t.Fatalf("provider creates=%d id=%q, want one committed provider thread", registry.creates, registry.providerID)
	}
	prepared, ok := base.sessions["mer-1"]
	if !ok || prepared.Metadata.WorkspacePath == "" || prepared.Metadata.ProviderConversationID != "" || prepared.IsTerminated {
		t.Fatalf("durable launch intent after crash = %+v", prepared)
	}

	secondLCM := &fakeLCM{store: base}
	secondManager := newManager(secondLCM)
	if err := secondManager.reconcileLive(context.Background(), prepared); err != nil {
		t.Fatalf("general boot reconciliation of prepared launch intent: %v", err)
	}
	if after := base.sessions[prepared.ID]; after.IsTerminated || after.Metadata.WorkspacePath == "" || workspace.destroyed != 0 {
		t.Fatalf("boot reconciliation destroyed launch intent: session=%+v destroyed=%d", after, workspace.destroyed)
	}
	recovered, _, _, err := secondManager.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatalf("retry Spawn after manager restart: %v", err)
	}
	if recovered.ID != prepared.ID || recovered.Metadata.ProviderConversationID != registry.providerID {
		t.Fatalf("recovered session = id:%q provider:%q", recovered.ID, recovered.Metadata.ProviderConversationID)
	}
	if registry.creates != 1 || registry.recovers != 1 {
		t.Fatalf("provider creates=%d recoveries=%d, want 1/1", registry.creates, registry.recovers)
	}
}

func newIdempotentChatManager() (*Manager, *idempotentFakeStore, *recordingLauncher) {
	base := newFakeStore()
	base.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: testRoleAgents()}
	store := &idempotentFakeStore{fakeStore: base}
	launcher := &recordingLauncher{}
	mgr := New(Deps{
		Runtime:   &fakeRuntime{},
		Agents:    fakeAgents{},
		Workspace: &fakeWorkspace{},
		Store:     store,
		Messenger: &fakeMessenger{},
		Chat:      launcher,
		Lifecycle: &fakeLCM{store: base},
		DataDir:   "/ao-test-data",
		LookPath:  func(string) (string, error) { return "/bin/true", nil },
	})
	return mgr, store, launcher
}

func TestSpawnIdempotentChatWorkerReusesLiveSessionAndPersistsAutoPermission(t *testing.T) {
	mgr, store, launcher := newIdempotentChatManager()
	cfg := ports.SpawnConfig{
		ProjectID:              chatTestProject,
		Kind:                   domain.KindWorker,
		Harness:                domain.HarnessCodex,
		RequestedMode:          domain.SessionModeChat,
		AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		CreationIdempotencyKey: "cleardev/requirement-1/builder/attempt-1",
		// A ClearDev worker is started with no initial user task. Its first task
		// arrives as a separately persisted, idempotent Chat turn.
		Prompt: "",
	}

	first, _, _, err := mgr.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first Spawn: %v", err)
	}
	second, _, _, err := mgr.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatalf("retry Spawn: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("retry session id = %q, want %q", second.ID, first.ID)
	}
	if len(store.sessions) != 1 || len(launcher.started) != 1 {
		t.Fatalf("idempotent retry created sessions=%d chat controllers=%d, want 1 each", len(store.sessions), len(launcher.started))
	}
	if first.Mode != domain.SessionModeChat || first.Harness != domain.HarnessCodex {
		t.Fatalf("first session launch contract = mode:%q harness:%q", first.Mode, first.Harness)
	}
	if first.PermissionMode != domain.PermissionModeAuto {
		t.Fatalf("permission mode = %q, want %q", first.PermissionMode, domain.PermissionModeAuto)
	}
	if first.Metadata.Prompt != "" {
		t.Fatalf("worker seed prompt = %q, want empty", first.Metadata.Prompt)
	}
	if got := first.CreationIdempotencyKey; got != cfg.CreationIdempotencyKey {
		t.Fatalf("creation key = %q, want %q", got, cfg.CreationIdempotencyKey)
	}

	changedPermission := cfg
	changedPermission.AgentConfig.Permissions = domain.PermissionModeAcceptEdits
	if _, _, _, err := mgr.Spawn(context.Background(), changedPermission); err == nil {
		t.Fatal("same creation key with a different permission contract succeeded")
	}
	changedHarness := cfg
	changedHarness.Harness = domain.HarnessClaudeCode
	if _, _, _, err := mgr.Spawn(context.Background(), changedHarness); err == nil {
		t.Fatal("same creation key with a different worker configuration succeeded")
	}
}

func TestSpawnIdempotentChatSerializesConcurrentSameKey(t *testing.T) {
	mgr, store, launcher := newIdempotentChatManager()
	entered := make(chan struct{})
	release := make(chan struct{})
	launcher.afterReady = func() {
		close(entered)
		<-release
	}
	cfg := ports.SpawnConfig{
		ProjectID: chatTestProject, Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		RequestedMode:          domain.SessionModeChat,
		AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		CreationIdempotencyKey: "cleardev/requirement-1/builder/attempt-1",
	}
	type result struct {
		rec domain.SessionRecord
		err error
	}
	results := make(chan result, 2)
	spawn := func() {
		rec, _, _, err := mgr.Spawn(context.Background(), cfg)
		results <- result{rec: rec, err: err}
	}
	go spawn()
	<-entered
	go spawn()
	select {
	case got := <-results:
		t.Fatalf("same-key concurrent Spawn escaped creation gate early: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent Spawn errors = %v / %v", first.err, second.err)
	}
	if first.rec.ID == "" || first.rec.ID != second.rec.ID {
		t.Fatalf("concurrent sessions = %q / %q", first.rec.ID, second.rec.ID)
	}
	if len(store.sessions) != 1 || len(launcher.started) != 1 {
		t.Fatalf("same-key concurrency created sessions=%d providers=%d, want 1/1", len(store.sessions), len(launcher.started))
	}
}

type sqliteCreationLifecycle struct{ store *sqlite.Store }

func (*sqliteCreationLifecycle) PrepareLaunch(domain.SessionID, string) error { return nil }
func (*sqliteCreationLifecycle) CancelLaunch(domain.SessionID, string)        {}
func (*sqliteCreationLifecycle) ReleaseLaunch(domain.SessionID, string)       {}
func (l *sqliteCreationLifecycle) MarkSpawned(ctx context.Context, id domain.SessionID, metadata domain.SessionMetadata) error {
	rec, ok, err := l.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("session disappeared before MarkSpawned")
	}
	rec.Metadata = metadata
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now().UTC()}
	rec.UpdatedAt = time.Now().UTC()
	return l.store.UpdateSession(ctx, rec)
}
func (*sqliteCreationLifecycle) CommitControllerEpoch(context.Context, domain.SessionID, domain.SessionMode, domain.SessionMode, string, bool) (bool, error) {
	return false, nil
}
func (*sqliteCreationLifecycle) ConfirmAgentSwitchSourceStopped(context.Context, domain.AgentSwitchSourceStopConfirmation) (bool, error) {
	return false, nil
}
func (*sqliteCreationLifecycle) ActivateAgentSwitchTarget(context.Context, domain.AgentSwitchTargetActivation) (bool, error) {
	return false, nil
}
func (*sqliteCreationLifecycle) ActivateChatAgentSwitchTarget(context.Context, domain.AgentSwitchChatTargetActivation) (bool, error) {
	return false, nil
}
func (l *sqliteCreationLifecycle) MarkTerminated(ctx context.Context, id domain.SessionID) error {
	rec, ok, err := l.store.GetSession(ctx, id)
	if err != nil || !ok {
		return err
	}
	rec.IsTerminated = true
	return l.store.UpdateSession(ctx, rec)
}

type crossManagerChatLauncher struct {
	mu      sync.Mutex
	creates int
	entered chan struct{}
	release chan struct{}
}

type crossManagerWorkspace struct {
	*fakeWorkspace
	mu      sync.Mutex
	creates int
}

func (w *crossManagerWorkspace) Create(ctx context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	w.mu.Lock()
	w.creates++
	w.mu.Unlock()
	return w.fakeWorkspace.Create(ctx, cfg)
}

func (*crossManagerChatLauncher) SupportsChat(domain.AgentHarness) bool { return true }
func (*crossManagerChatLauncher) PreflightChat(context.Context, domain.AgentHarness) error {
	return nil
}
func (l *crossManagerChatLauncher) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	l.mu.Lock()
	l.creates++
	createNumber := l.creates
	l.mu.Unlock()
	if createNumber == 1 {
		close(l.entered)
	}
	select {
	case <-ctx.Done():
		return ChatStarted{}, ctx.Err()
	case <-l.release:
	}
	started := ChatStarted{ProviderConversationID: fmt.Sprintf("thread-%d", createNumber), ControllerGeneration: "generation-1"}
	if cfg.ControllerReady != nil {
		if _, err := cfg.ControllerReady(started); err != nil {
			return ChatStarted{}, err
		}
	}
	return started, nil
}
func (*crossManagerChatLauncher) StartChatTurn(context.Context, domain.SessionID, string) (string, error) {
	return "", nil
}
func (*crossManagerChatLauncher) RelayChatTurn(context.Context, domain.SessionID, string) (string, error) {
	return "", nil
}
func (*crossManagerChatLauncher) RelayChatTurnWithID(context.Context, domain.SessionID, string, string) (string, error) {
	return "", nil
}
func (*crossManagerChatLauncher) HasLiveChatController(domain.SessionID) bool { return false }
func (*crossManagerChatLauncher) StopChat(context.Context, domain.SessionID) error {
	return nil
}

func TestSpawnIdempotentChatSerializesAcrossManagersAndStores(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	firstStore, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	t.Cleanup(func() { _ = firstStore.Close() })
	secondStore, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	project := domain.ProjectRecord{
		ID: "mer", Path: "/repo/mer", Config: testRoleAgents(),
		RegisteredAt: time.Now().UTC(),
	}
	if err := firstStore.UpsertProject(ctx, project); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	launcher := &crossManagerChatLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	workspace := &crossManagerWorkspace{fakeWorkspace: &fakeWorkspace{}}
	newManager := func(store *sqlite.Store) *Manager {
		return New(Deps{
			Runtime: &fakeRuntime{}, Agents: fakeAgents{}, Workspace: workspace,
			Store: store, Messenger: &fakeMessenger{}, Chat: launcher,
			Lifecycle: &sqliteCreationLifecycle{store: store}, DataDir: "/ao-test-data",
			LookPath: func(string) (string, error) { return "/bin/true", nil },
		})
	}
	firstManager := newManager(firstStore)
	secondManager := newManager(secondStore)
	cfg := ports.SpawnConfig{
		ProjectID: chatTestProject, Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		RequestedMode:          domain.SessionModeChat,
		AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		CreationIdempotencyKey: "cleardev/requirement-1/builder/attempt-1",
	}
	type result struct {
		rec domain.SessionRecord
		err error
	}
	results := make(chan result, 2)
	go func() {
		rec, _, _, err := firstManager.Spawn(ctx, cfg)
		results <- result{rec: rec, err: err}
	}()
	<-launcher.entered
	go func() {
		rec, _, _, err := secondManager.Spawn(ctx, cfg)
		results <- result{rec: rec, err: err}
	}()
	select {
	case got := <-results:
		t.Fatalf("second Manager escaped the persistent creation claim: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(launcher.release)
	left, right := <-results, <-results
	if left.err != nil || right.err != nil {
		t.Fatalf("cross-Manager Spawn errors = %v / %v", left.err, right.err)
	}
	if left.rec.ID == "" || left.rec.ID != right.rec.ID {
		t.Fatalf("cross-Manager sessions = %q / %q", left.rec.ID, right.rec.ID)
	}
	launcher.mu.Lock()
	creates := launcher.creates
	launcher.mu.Unlock()
	if creates != 1 {
		t.Fatalf("provider creates = %d, want 1", creates)
	}
	workspace.mu.Lock()
	workspaceCreates := workspace.creates
	workspace.mu.Unlock()
	if workspaceCreates != 1 {
		t.Fatalf("workspace creates = %d, want 1", workspaceCreates)
	}
	if workspace.lastCfg.SessionID != left.rec.ID {
		t.Fatalf("workspace belongs to %q, want %q", workspace.lastCfg.SessionID, left.rec.ID)
	}
}
