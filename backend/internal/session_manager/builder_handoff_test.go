package sessionmanager

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/gitworktree"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type savedHandoffContext struct {
	context             ports.BuilderHandoffContext
	prompt, fingerprint string
}
type handoffManagerStore struct {
	*idempotentFakeStore
	ports.AgentSwitchStore
	interfaceTransitionStore
	mu                  sync.Mutex
	fenced              bool
	operations          map[string]domain.SessionID
	ends                []string
	contexts            map[domain.SessionID]savedHandoffContext
	openTurn            bool
	conversationMissing bool
	contextErr          error
}

func newHandoffManagerStore(base *fakeStore) *handoffManagerStore {
	return &handoffManagerStore{idempotentFakeStore: &idempotentFakeStore{fakeStore: base}, operations: map[string]domain.SessionID{}, contexts: map[domain.SessionID]savedHandoffContext{}}
}
func (s *handoffManagerStore) IsClearDevBuilderSessionFenced(context.Context, domain.SessionID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fenced, nil
}
func (s *handoffManagerStore) HasOpenClearDevBuilderSessionOperation(_ context.Context, id domain.SessionID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, owner := range s.operations {
		if owner == id {
			return true, nil
		}
	}
	return false, nil
}
func (s *handoffManagerStore) BeginClearDevBuilderSessionOperation(_ context.Context, id domain.SessionID, op, kind string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fenced {
		return false, ports.ErrBuilderSessionFenced
	}
	for _, owner := range s.operations {
		if owner == id {
			return false, errors.New("unresolved operation")
		}
	}
	if kind == "" || op == "" {
		return false, errors.New("missing internal operation identity")
	}
	s.operations[op] = id
	return true, nil
}
func (s *handoffManagerStore) EndClearDevBuilderSessionOperation(_ context.Context, id, outcome string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.operations[id]; !ok {
		return errors.New("unknown operation")
	}
	if outcome != "COMPLETED" && outcome != "FAILED_BEFORE_ACTION" {
		return errors.New("bad operation result")
	}
	delete(s.operations, id)
	s.ends = append(s.ends, outcome)
	return nil
}
func (s *handoffManagerStore) SaveClearDevBuilderHandoffContext(_ context.Context, id domain.SessionID, c ports.BuilderHandoffContext, prompt, fingerprint string) error {
	if s.contextErr != nil {
		return s.contextErr
	}
	if old, ok := s.contexts[id]; ok && old != (savedHandoffContext{c, prompt, fingerprint}) {
		return ports.ErrBuilderHandoffChanged
	}
	if s.sessions[id].CreationRequestFingerprint != fingerprint {
		return ports.ErrBuilderHandoffChanged
	}
	s.contexts[id] = savedHandoffContext{c, prompt, fingerprint}
	return nil
}
func (s *handoffManagerStore) GetClearDevBuilderHandoffContext(_ context.Context, id domain.SessionID) (ports.BuilderHandoffContext, string, string, bool, error) {
	v, ok := s.contexts[id]
	return v.context, v.prompt, v.fingerprint, ok, s.contextErr
}
func (s *handoffManagerStore) ConversationForSession(_ context.Context, id domain.SessionID) (domain.ConversationRecord, error) {
	if s.conversationMissing {
		return domain.ConversationRecord{}, errors.New("no durable conversation")
	}
	return domain.ConversationRecord{ID: "conversation-" + string(id), SessionID: id}, nil
}
func (s *handoffManagerStore) HasOpenClearDevBuilderConversationTurn(context.Context, domain.SessionID) (bool, error) {
	return s.openTurn, nil
}
func (s *handoffManagerStore) GetActiveSessionInterfaceTransition(context.Context, domain.SessionID) (domain.SessionInterfaceTransition, bool, error) {
	return domain.SessionInterfaceTransition{}, false, nil
}

type handoffRestoreOrderStore struct {
	*handoffManagerStore
	t      *testing.T
	begins int
}

func (s *handoffRestoreOrderStore) ListAgentSwitches(context.Context, domain.SessionID) ([]domain.AgentSwitch, error) {
	return nil, nil
}

func (s *handoffRestoreOrderStore) BeginClearDevBuilderSessionOperation(ctx context.Context, id domain.SessionID, op, kind string, at time.Time) (bool, error) {
	s.mu.Lock()
	open := len(s.operations)
	s.mu.Unlock()
	if open != 0 {
		s.t.Fatal("restore began another session before releasing the completed session")
	}
	s.begins++
	return s.handoffManagerStore.BeginClearDevBuilderSessionOperation(ctx, id, op, kind, at)
}

func TestBuilderHandoffRestoreAllReleasesEachCompletedSession(t *testing.T) {
	m, base, rt, _ := newLifecycleManager()
	store := &handoffRestoreOrderStore{handoffManagerStore: newHandoffManagerStore(base), t: t}
	m.store = store
	for _, id := range []domain.SessionID{"mer-1", "mer-2"} {
		base.sessions[id] = domain.SessionRecord{
			ID: id, ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
			IsTerminated: true,
			Metadata:     domain.SessionMetadata{WorkspacePath: "/ws/" + string(id), Branch: "ao/" + string(id) + "/root", AgentSessionID: "agent-" + string(id)},
			Activity:     domain.Activity{State: domain.ActivityExited},
		}
		base.worktrees[id] = []domain.SessionWorktreeRecord{{SessionID: id, RepoName: domain.RootWorkspaceRepoName, State: "removed"}}
	}
	if err := m.RestoreAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.begins != 2 || rt.created != 2 || len(store.operations) != 0 || len(store.ends) != 2 {
		t.Fatal("restore did not complete and release both sessions", store.begins, rt.created, store.operations, store.ends)
	}
}

func newHandoffContextFixture(t *testing.T) (*Manager, *handoffManagerStore, *recordingLauncher, ports.SpawnConfig) {
	t.Helper()
	m, original, launcher := newIdempotentChatManager()
	m.dataDir = t.TempDir()
	store := newHandoffManagerStore(original.fakeStore)
	m.store = store
	repo := newManagerGitRepo(t)
	project := store.projects["mer"]
	project.Path = repo
	store.projects["mer"] = project
	workspace, err := gitworktree.New(gitworktree.Options{ManagedRoot: t.TempDir(), RepoResolver: gitworktree.StaticRepoResolver{"mer": repo}})
	if err != nil {
		t.Fatal(err)
	}
	m.workspace = workspace
	dir := filepath.Join(m.dataDir, "cleardev-builder-handoffs", strings.Repeat("a", 64))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	text := []byte("Verified original task and prior working-state references.")
	path := filepath.Join(dir, "handoff.txt")
	if err := os.WriteFile(path, text, 0o400); err != nil {
		t.Fatal(err)
	}
	c := &ports.BuilderHandoffContext{ReferencePath: path, SHA256: fmt.Sprintf("%x", sha256.Sum256(text)), SnapshotSHA256: strings.Repeat("b", 64)}
	cfg := ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex, RequestedMode: domain.SessionModeChat, CreationIdempotencyKey: "cleardev-handoff-test", WorkspaceBaseCommitSHA: strings.TrimSpace(runManagerGit(t, repo, "rev-parse", "HEAD")), Branch: "cleardev-complex-builder-private-context", BuilderHandoffContext: c}
	return m, store, launcher, cfg
}

func TestBuilderHandoffPrivateContextIsSystemOnlyPersistentAndReused(t *testing.T) {
	m, store, launcher, cfg := newHandoffContextFixture(t)
	launcher.afterReady = func() {
		if len(store.contexts) != 1 {
			t.Fatal("provider started before private context was persisted")
		}
		if len(store.operations) != 1 {
			t.Fatal("provider started outside the persistent launch operation")
		}
	}
	record, _, _, err := m.Spawn(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if record.Metadata.Prompt != "" || len(launcher.turns) != 0 || len(launcher.started) != 1 {
		t.Fatal("handoff generated an initial task turn")
	}
	if len(store.operations) != 0 || len(store.ends) != 1 || store.ends[0] != "COMPLETED" {
		t.Fatal("successful launch was not settled", store.ends)
	}
	first := launcher.started[0].SystemPrompt
	if !strings.Contains(first, cfg.BuilderHandoffContext.ReferencePath) || store.contexts[record.ID].fingerprint != record.CreationRequestFingerprint {
		t.Fatal("launch reference/fingerprint was not durably bound")
	}
	project := store.projects["mer"]
	project.Config.AgentRules = "changed project instructions after launch"
	store.projects["mer"] = project
	replayed, _, _, err := m.Spawn(context.Background(), cfg)
	if err != nil || replayed.ID != record.ID || len(launcher.started) != 1 {
		t.Fatal("private context replay created another worker", err)
	}
	// A fresh manager reuses the persisted exact system prompt on normal resume.
	newManager := New(Deps{Store: store, Runtime: m.runtime, Agents: m.agents, Workspace: m.workspace, Messenger: &fakeMessenger{}, Chat: launcher, Lifecycle: &fakeLCM{store: store.fakeStore}, DataDir: m.dataDir, LookPath: func(string) (string, error) { return "/bin/true", nil }})
	resumeRecord := store.sessions[record.ID]
	resumeRecord.Activity.State = domain.ActivityExited
	store.sessions[record.ID] = resumeRecord
	if _, err := newManager.ResumeAgentWithMode(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	if len(launcher.started) != 2 || launcher.started[1].SystemPrompt != first || len(launcher.turns) != 0 {
		t.Fatal("resume changed context or generated a user turn")
	}
	changed := cfg
	other := *cfg.BuilderHandoffContext
	other.SnapshotSHA256 = strings.Repeat("c", 64)
	changed.BuilderHandoffContext = &other
	if _, _, _, err := m.Spawn(context.Background(), changed); err == nil || len(launcher.started) != 2 {
		t.Fatal("changed launch binding was reused")
	}
	if err := os.Chmod(cfg.BuilderHandoffContext.ReferencePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.BuilderHandoffContext.ReferencePath, []byte("tampered"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := newManager.ResumeAgentWithMode(context.Background(), record.ID); err == nil || len(launcher.started) != 2 {
		t.Fatal("tampered private context reached provider")
	}
}

type handoffResponseLostChat struct{ *recordingLauncher }

func (l *handoffResponseLostChat) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	started, err := l.recordingLauncher.StartChat(ctx, cfg)
	if err != nil {
		return started, err
	}
	return ChatStarted{}, errors.New("provider launch response was lost after its commit")
}

type handoffNegativeAliveRuntime struct {
	ports.Runtime
	probes int
}

func (r *handoffNegativeAliveRuntime) IsExactSupervisedProcessAlive(context.Context, ports.RuntimeHandle, ports.SupervisedProcessRef) (bool, error) {
	r.probes++
	return false, nil
}

func TestBuilderHandoffUnknownLaunchRemainsOpenAcrossKeyedReplay(t *testing.T) {
	m, store, launcher, cfg := newHandoffContextFixture(t)
	m.chat = &handoffResponseLostChat{recordingLauncher: launcher}
	if _, _, _, err := m.Spawn(context.Background(), cfg); err == nil {
		t.Fatal("unknown launch was reported complete")
	}
	if len(store.operations) != 1 || len(store.ends) != 0 || len(launcher.started) != 1 || len(store.sessions) != 1 {
		t.Fatal("unknown launch was erased or settled", store.operations, store.ends)
	}
	// The controller-ready row exists. Its enum/metadata still do not settle
	// the operation whose original provider response was lost.
	if _, _, _, err := m.Spawn(context.Background(), cfg); err == nil || len(launcher.started) != 1 {
		t.Fatal("same-daemon replay restarted an unknown launch", err)
	}
	fresh := New(Deps{Store: store, Runtime: m.runtime, Agents: m.agents, Workspace: m.workspace, Messenger: &fakeMessenger{}, Chat: m.chat, Lifecycle: &fakeLCM{store: store.fakeStore}, DataDir: m.dataDir, LookPath: func(string) (string, error) { return "/bin/true", nil }})
	if _, _, _, err := fresh.Spawn(context.Background(), cfg); err == nil || len(launcher.started) != 1 || len(store.operations) != 1 {
		t.Fatal("fresh-daemon replay settled or restarted an unknown launch", err)
	}
}

func TestBuilderHandoffPrivateSpawnRejectsPromptAndMissingDurability(t *testing.T) {
	for _, mode := range []string{"prompt", "issue", "attachment", "tui", "no-key", "no-base", "store-failure", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			m, store, launcher, cfg := newHandoffContextFixture(t)
			switch mode {
			case "prompt":
				cfg.Prompt = "unauthorized initial turn"
			case "issue":
				cfg.IssueContext = "external task"
			case "attachment":
				cfg.Attachments = []ports.SpawnAttachment{{Data: []byte("x")}}
			case "tui":
				cfg.RequestedMode = domain.SessionModeTUI
			case "no-key":
				cfg.CreationIdempotencyKey = ""
			case "no-base":
				cfg.WorkspaceBaseCommitSHA = ""
			case "store-failure":
				store.contextErr = errors.New("private receipt unavailable")
			case "symlink":
				path := cfg.BuilderHandoffContext.ReferencePath
				other := filepath.Join(t.TempDir(), "other")
				if err := os.WriteFile(other, []byte("x"), 0o400); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, _, err := m.Spawn(context.Background(), cfg); err == nil || len(launcher.started) != 0 || len(launcher.turns) != 0 {
				t.Fatal("invalid private launch reached provider", err)
			}
		})
	}
}

func TestBuilderHandoffDurableFenceBlocksOldManagerLifecycleAfterRestart(t *testing.T) {
	m, base, rt, ws := newManager()
	store := newHandoffManagerStore(base)
	m.store = store
	store.fenced = true
	id := domain.SessionID("mer-1")
	base.sessions[id] = domain.SessionRecord{ID: id, ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, Metadata: domain.SessionMetadata{WorkspacePath: t.TempDir(), Branch: "old-builder", ProviderConversationID: "old-provider", ControllerGeneration: "old-generation"}, Activity: domain.Activity{State: domain.ActivityExited}}
	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{"kill", func() error { _, err := m.Kill(context.Background(), id); return err }},
		{"restore", func() error { _, err := m.RestoreWithMode(context.Background(), id); return err }},
		{"resume", func() error { _, err := m.ResumeAgentWithMode(context.Background(), id); return err }},
		{"retire", func() error { return m.RetireForReplacement(context.Background(), id) }},
		{"send", func() error {
			return m.Send(context.Background(), id, "old task", &ports.SpawnAttachment{Data: []byte("x")})
		}},
		{"delete", func() error { _, _, err := m.RollbackSpawn(context.Background(), id); return err }},
		{"attachments", func() error {
			_, err := m.StageAttachments(context.Background(), id, []ports.SpawnAttachment{{Data: []byte("x")}})
			return err
		}},
		{"interface", func() error {
			_, err := m.StartInterfaceTransition(context.Background(), id, domain.SessionModeTUI, domain.SessionInterfaceTransitionInterrupt)
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, ports.ErrBuilderSessionFenced) {
				t.Fatal("retired Builder operation admitted", err)
			}
		})
	}
	if err := m.reconcileLive(context.Background(), base.sessions[id]); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveAndTeardownAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := base.sessions[id]
	r.IsTerminated = true
	base.sessions[id] = r
	base.worktrees[id] = []domain.SessionWorktreeRecord{{State: "removed", WorktreePath: r.Metadata.WorkspacePath, Branch: r.Metadata.Branch}}
	if err := m.RestoreAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := m.Cleanup(context.Background(), "mer")
	if err != nil || len(result.Skipped) != 1 {
		t.Fatal("fenced cleanup did not preserve old tree", err, result)
	}
	if rt.created != 0 || rt.destroyed != 0 || ws.destroyed != 0 || len(store.operations) != 0 || len(base.sessions) != 1 {
		t.Fatal("fenced lifecycle changed old runtime/tree/identity")
	}
}

func TestBuilderHandoffUnknownOperationSurvivesManagerRestart(t *testing.T) {
	m, base, _, _ := newManager()
	store := newHandoffManagerStore(base)
	m.store = store
	id := domain.SessionID("mer-1")
	base.sessions[id] = domain.SessionRecord{ID: id, ProjectID: "mer", Kind: domain.KindWorker, Mode: domain.SessionModeChat, Metadata: domain.SessionMetadata{WorkspacePath: t.TempDir()}, Activity: domain.Activity{State: domain.ActivityExited}}
	launcher := &recordingLauncher{turnErr: errors.New("provider response lost")}
	m.chat = launcher
	if err := m.Send(context.Background(), id, "uncertain send", nil); err == nil {
		t.Fatal("unknown delivery accepted")
	}
	if len(store.operations) != 1 || len(store.ends) != 0 {
		t.Fatal("unknown operation was released")
	}
	fresh := New(Deps{Store: store, Runtime: &fakeRuntime{}, Chat: launcher, Workspace: &fakeWorkspace{}, Lifecycle: &fakeLCM{store: base}})
	if _, err := fresh.ResumeAgentWithMode(context.Background(), id); err == nil || len(launcher.started) != 0 {
		t.Fatal("new daemon resumed an unresolved operation")
	}
}

type handoffObservationChat struct {
	*recordingLauncher
	stopped              bool
	probeErr             error
	provider, generation string
	probes               int
}

func (c *handoffObservationChat) ObserveBuilderHandoffChatRuntime(_ context.Context, _ domain.SessionID, provider, generation string) (bool, error) {
	c.probes++
	if provider != c.provider || generation != c.generation {
		return false, nil
	}
	return c.stopped, c.probeErr
}

func TestBuilderHandoffLossObservationRequiresExactFactsAndDoesNotWake(t *testing.T) {
	m, base, _, _ := newManager()
	store := newHandoffManagerStore(base)
	m.store = store
	registry := newSwitchTestStore()
	registry.fakeStore = base
	store.AgentSwitchStore = registry
	id := domain.SessionID("mer-1")
	base.sessions[id] = domain.SessionRecord{ID: id, ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, Metadata: domain.SessionMetadata{ProviderConversationID: "native-1", ControllerGeneration: "generation-1"}, Activity: domain.Activity{State: domain.ActivityExited}}
	chat := &handoffObservationChat{recordingLauncher: &recordingLauncher{}, provider: "native-1", generation: "generation-1", stopped: true}
	m.chat = chat
	agent := &switchTestAgent{available: map[string]ports.NativeSessionAvailability{"native-1": ports.NativeSessionAvailabilityUnavailable}}
	m.agents = switchTestAgents{domain.HarnessCodex: agent}
	registry.native["registered"] = domain.AgentNativeSession{ID: "registered", AOSessionID: id, Harness: domain.HarnessCodex, NativeSessionID: "native-1", ConfigDir: t.TempDir(), LastGenerationID: "generation-1"}
	observation, err := m.ObserveBuilderHandoffSession(context.Background(), id)
	if err != nil || !observation.RuntimeStopped || !observation.NoOpenTurn || !observation.OperationIdle || observation.NativeAvailability != ports.NativeSessionAvailabilityUnavailable || observation.ObservedAt.IsZero() {
		t.Fatal("accurate observation failed", observation, err)
	}
	if len(chat.started) != 0 || len(chat.turns) != 0 || len(store.operations) != 0 {
		t.Fatal("observation mutated lifecycle")
	}
	store.openTurn = true
	observation, err = m.ObserveBuilderHandoffSession(context.Background(), id)
	if err != nil || observation.NoOpenTurn {
		t.Fatal("open provider turn ignored", err)
	}
	store.openTurn = false
	store.operations["uncertain"] = id
	observation, err = m.ObserveBuilderHandoffSession(context.Background(), id)
	if err != nil || observation.OperationIdle {
		t.Fatal("unknown operation ignored", err)
	}
	delete(store.operations, "uncertain")
	registry.switches["active-before-operation-receipts"] = domain.AgentSwitch{ID: "active-before-operation-receipts", SessionID: id, State: domain.AgentSwitchPreparingHandoff}
	observation, err = m.ObserveBuilderHandoffSession(context.Background(), id)
	if err != nil || observation.OperationIdle {
		t.Fatal("durable active switch was ignored when its operation receipt was absent", err)
	}
	delete(registry.switches, "active-before-operation-receipts")
	delete(registry.native, "registered")
	observation, err = m.ObserveBuilderHandoffSession(context.Background(), id)
	if err != nil || observation.NativeAvailability != ports.NativeSessionAvailabilityUnknown {
		t.Fatal("missing native binding inferred unavailable", err)
	}
	chat.stopped = false
	observation, err = m.ObserveBuilderHandoffSession(context.Background(), id)
	if err != nil || observation.RuntimeStopped {
		t.Fatal("activity enum became runtime proof", err)
	}
	chat.probeErr = errors.New("runtime probe failed")
	if _, err := m.ObserveBuilderHandoffSession(context.Background(), id); err == nil {
		t.Fatal("failed process probe became stop proof")
	}
	store.conversationMissing = true
	chat.probeErr = nil
	if _, err := m.ObserveBuilderHandoffSession(context.Background(), id); err == nil {
		t.Fatal("missing conversation became empty-turn proof")
	}
	store.conversationMissing = false
	record := base.sessions[id]
	record.Mode = domain.SessionModeTUI
	record.Metadata.RuntimeHandleID = "old-runtime"
	record.Metadata.RuntimeLaunchID = "old-launch"
	base.sessions[id] = record
	runtime := &handoffNegativeAliveRuntime{Runtime: m.runtime}
	m.runtime = runtime
	observation, err = m.ObserveBuilderHandoffSession(context.Background(), id)
	if err != nil || observation.RuntimeStopped || runtime.probes != 0 {
		t.Fatal("negative TUI alive probe became exact stop proof", observation, err)
	}
}

func TestBuilderHandoffNewControlledChatBindingUsesActualLaunchConfiguration(t *testing.T) {
	m, base, _, _ := newManager()
	registry := newSwitchTestStore()
	registry.fakeStore = base
	m.store = registry
	id := domain.SessionID("mer-1")
	base.controlledSessions[id] = true
	rec := domain.SessionRecord{ID: id, ProjectID: "mer", Harness: domain.HarnessCodex}
	root := t.TempDir()
	agent := &switchTestAgent{configDir: root, available: map[string]ports.NativeSessionAvailability{}}
	m.agents = switchTestAgents{domain.HarnessCodex: agent}
	metadata := domain.SessionMetadata{ProviderConversationID: "actual-provider", ControllerGeneration: "actual-generation"}
	if err := m.recordBuilderChatNativeBinding(context.Background(), rec, metadata, map[string]string{"CODEX_HOME": root}); err != nil {
		t.Fatal(err)
	}
	if len(registry.native) != 1 {
		t.Fatal("actual native binding missing")
	}
	for _, native := range registry.native {
		if native.ConfigDir != root || native.NativeSessionID != metadata.ProviderConversationID || string(native.LastGenerationID) != metadata.ControllerGeneration {
			t.Fatal("native launch identity/config changed", native)
		}
	}
}
