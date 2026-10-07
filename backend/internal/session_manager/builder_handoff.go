package sessionmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type builderHandoffContextStore interface {
	SaveClearDevBuilderHandoffContext(context.Context, domain.SessionID, ports.BuilderHandoffContext, string, string) error
	GetClearDevBuilderHandoffContext(context.Context, domain.SessionID) (ports.BuilderHandoffContext, string, string, bool, error)
}

type builderSessionOperationStore interface {
	IsClearDevBuilderSessionFenced(context.Context, domain.SessionID) (bool, error)
	HasOpenClearDevBuilderSessionOperation(context.Context, domain.SessionID) (bool, error)
	BeginClearDevBuilderSessionOperation(context.Context, domain.SessionID, string, string, time.Time) (bool, error)
	EndClearDevBuilderSessionOperation(context.Context, string, string, time.Time) error
}

type builderSessionOperation struct {
	id       string
	store    builderSessionOperationStore
	action   bool
	resolved bool
}

func (m *Manager) beginBuilderSessionOperation(ctx context.Context, id domain.SessionID, kind string) (*builderSessionOperation, error) {
	store, ok := m.store.(builderSessionOperationStore)
	if !ok || ports.BuilderSessionOperationAdmitted(ctx, id) {
		return nil, nil
	}
	op := &builderSessionOperation{id: "builder-session-operation-" + uuid.NewString(), store: store}
	claimed, err := store.BeginClearDevBuilderSessionOperation(ctx, id, op.id, kind, m.clock())
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, errors.New("builder lifecycle operation is already claimed")
	}
	managed, err := store.HasOpenClearDevBuilderSessionOperation(ctx, id)
	if err != nil {
		return nil, err
	}
	if !managed {
		return nil, nil
	}
	return op, nil
}

// Unknown external outcomes have no end fact. They survive daemon restart and
// continue to exclude a handoff rather than asserting that no action happened.
func (m *Manager) endBuilderSessionOperation(op *builderSessionOperation) {
	if op == nil || (op.action && !op.resolved) {
		return
	}
	outcome := "FAILED_BEFORE_ACTION"
	if op.resolved {
		outcome = "COMPLETED"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := op.store.EndClearDevBuilderSessionOperation(ctx, op.id, outcome, m.clock()); err != nil {
		m.logger.Error("Builder lifecycle outcome could not be recorded", "operationID", op.id, "error", err)
	}
}

func (m *Manager) markBuilderSessionAction(id domain.SessionID) {
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	if op := m.builderOperations[id]; op != nil {
		op.action = true
	}
}

func (m *Manager) completeBuilderSessionOperation(id domain.SessionID, err error) {
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	if op := m.builderOperations[id]; op != nil {
		op.resolved = err == nil
	}
}

func handoffDigestValid(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}

func (m *Manager) verifyBuilderHandoffContext(c ports.BuilderHandoffContext) (retErr error) {
	if !handoffDigestValid(c.SHA256) || !handoffDigestValid(c.SnapshotSHA256) || !filepath.IsAbs(c.ReferencePath) || filepath.Clean(c.ReferencePath) != c.ReferencePath {
		return ports.ErrBuilderHandoffInvalid
	}
	root, err := filepath.Abs(filepath.Join(m.dataDir, "cleardev-builder-handoffs"))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, c.ReferencePath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) || filepath.Base(rel) != "handoff.txt" || len(strings.Split(filepath.ToSlash(rel), "/")) != 2 {
		return ports.ErrBuilderHandoffInvalid
	}
	resolved, err := filepath.EvalSymlinks(c.ReferencePath)
	if err != nil || resolved != c.ReferencePath {
		return ports.ErrBuilderHandoffInvalid
	}
	before, err := os.Lstat(c.ReferencePath)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 64<<10 {
		return ports.ErrBuilderHandoffInvalid
	}
	f, err := os.Open(c.ReferencePath)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, f.Close()) }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return ports.ErrBuilderHandoffChanged
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return err
	}
	after, err := os.Lstat(c.ReferencePath)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || len(data) > 64<<10 || fmt.Sprintf("%x", sha256.Sum256(data)) != c.SHA256 {
		return ports.ErrBuilderHandoffChanged
	}
	return nil
}

func (m *Manager) validateBuilderHandoffSpawn(cfg ports.SpawnConfig) error {
	if cfg.BuilderHandoffContext == nil {
		return nil
	}
	if cfg.CreationIdempotencyKey == "" || cfg.WorkspaceBaseCommitSHA == "" || cfg.Kind != domain.KindWorker || cfg.RequestedMode != domain.SessionModeChat || cfg.Prompt != "" || cfg.IssueID != "" || cfg.IssueContext != "" || len(cfg.Attachments) != 0 {
		return ports.ErrBuilderHandoffInvalid
	}
	if _, ok := m.store.(builderHandoffContextStore); !ok {
		return errors.New("private Builder launch context persistence is unavailable")
	}
	if _, ok := m.store.(builderSessionOperationStore); !ok {
		return errors.New("private Builder lifecycle persistence is unavailable")
	}
	return m.verifyBuilderHandoffContext(*cfg.BuilderHandoffContext)
}

func appendBuilderHandoffSystemContext(systemPrompt string, c ports.BuilderHandoffContext) string {
	return systemPrompt + "\n\nClearDev authorized Builder handoff material:\nRead the sealed context reference " + c.ReferencePath + ".\nContext SHA256: " + c.SHA256 + ". Snapshot SHA256: " + c.SnapshotSHA256 + ".\nThis reference is standing context. Await the original task message; it does not grant another message or change task scope or budget.\n"
}

func (m *Manager) persistBuilderHandoffContext(ctx context.Context, rec domain.SessionRecord, cfg ports.SpawnConfig, systemPrompt string) error {
	if cfg.BuilderHandoffContext == nil {
		return nil
	}
	store, ok := m.store.(builderHandoffContextStore)
	if !ok {
		return ports.ErrBuilderHandoffInvalid
	}
	return store.SaveClearDevBuilderHandoffContext(ctx, rec.ID, *cfg.BuilderHandoffContext, systemPrompt, rec.CreationRequestFingerprint)
}

// The exact launch system prompt is reused, even after project rules change.
// A missing ordinary-session receipt preserves its existing restore behavior.
func (m *Manager) builderHandoffRestoreSystemPrompt(ctx context.Context, rec domain.SessionRecord, ordinary string) (string, error) {
	store, ok := m.store.(builderHandoffContextStore)
	if !ok {
		return ordinary, nil
	}
	c, saved, fingerprint, found, err := store.GetClearDevBuilderHandoffContext(ctx, rec.ID)
	if err != nil || !found {
		return ordinary, err
	}
	if fingerprint == "" || fingerprint != rec.CreationRequestFingerprint || saved == "" {
		return "", ports.ErrBuilderHandoffChanged
	}
	if err := m.verifyBuilderHandoffContext(c); err != nil {
		return "", err
	}
	if !strings.Contains(saved, appendBuilderHandoffSystemContext("", c)) {
		return "", ports.ErrBuilderHandoffChanged
	}
	return saved, nil
}

type builderConversationTurnObserver interface {
	ConversationForSession(context.Context, domain.SessionID) (domain.ConversationRecord, error)
	HasOpenClearDevBuilderConversationTurn(context.Context, domain.SessionID) (bool, error)
}

type builderChatRuntimeObserver interface {
	ObserveBuilderHandoffChatRuntime(context.Context, domain.SessionID, string, string) (bool, error)
}

// ObserveBuilderHandoffSession reads exact launch, native and lifecycle facts
// without creating a conversation, waking a controller or ending an operation.
func (m *Manager) ObserveBuilderHandoffSession(ctx context.Context, id domain.SessionID) (ports.BuilderHandoffSessionObservation, error) {
	observation := ports.BuilderHandoffSessionObservation{NativeAvailability: ports.NativeSessionAvailabilityUnknown}
	rec, found, err := m.store.GetSession(ctx, id)
	if err != nil {
		return observation, err
	}
	if !found {
		return observation, ports.ErrSessionNotFound
	}
	observation.Session = rec
	if operations, ok := m.store.(builderSessionOperationStore); ok {
		open, err := operations.HasOpenClearDevBuilderSessionOperation(ctx, id)
		if err != nil {
			return observation, err
		}
		observation.OperationIdle = !open && !m.SessionMutationInProgress(id)
		if switches, ok := m.store.(ports.AgentSwitchStore); ok {
			_, active, err := switches.GetActiveAgentSwitch(ctx, id)
			if err != nil {
				return observation, err
			}
			if active {
				observation.OperationIdle = false
			}
		} else {
			observation.OperationIdle = false
		}
		if active, err := m.hasActiveInterfaceTransition(ctx, id); err != nil {
			return observation, err
		} else if active {
			observation.OperationIdle = false
		}
	}
	if turns, ok := m.store.(builderConversationTurnObserver); ok {
		conv, err := turns.ConversationForSession(ctx, id)
		if err != nil {
			return observation, err
		}
		if conv.ID == "" || conv.SessionID != id {
			return observation, ports.ErrBuilderHandoffChanged
		}
		open, err := turns.HasOpenClearDevBuilderConversationTurn(ctx, id)
		if err != nil {
			return observation, err
		}
		observation.NoOpenTurn = !open
	}
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		if rec.Metadata.ProviderConversationID != "" && rec.Metadata.ControllerGeneration != "" {
			if observer, ok := m.chat.(builderChatRuntimeObserver); ok {
				observation.RuntimeStopped, err = observer.ObserveBuilderHandoffChatRuntime(ctx, id, rec.Metadata.ProviderConversationID, rec.Metadata.ControllerGeneration)
				if err != nil {
					return observation, err
				}
			}
		}
	}
	// A TUI alive probe only promises positive liveness. Its false result may
	// include a missing or unobservable generation and cannot prove OS exit.
	// This handoff therefore leaves TUI runtime stop evidence unavailable.
	nativeID, generation := rec.Metadata.AgentSessionID, rec.Metadata.RuntimeLaunchID
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		nativeID, generation = rec.Metadata.ProviderConversationID, rec.Metadata.ControllerGeneration
	}
	if registry, ok := m.store.(ports.AgentSwitchStore); ok && nativeID != "" && generation != "" {
		records, err := registry.ListAgentNativeSessions(ctx, id)
		if err != nil {
			return observation, err
		}
		for _, native := range records {
			if native.AOSessionID != id || native.Harness != rec.Harness || native.NativeSessionID != nativeID || string(native.LastGenerationID) != generation || !filepath.IsAbs(native.ConfigDir) {
				continue
			}
			ref := ports.NativeSessionRef{NativeSessionID: nativeID, ConfigDir: native.ConfigDir}
			if observation.NativeRef != (ports.NativeSessionRef{}) && observation.NativeRef != ref {
				return observation, ports.ErrBuilderHandoffChanged
			}
			observation.NativeRef = ref
		}
		if observation.NativeRef != (ports.NativeSessionRef{}) {
			if agent, ok := m.agents.Agent(rec.Harness); ok {
				if prober, ok := agent.(ports.AgentNativeSessionProber); ok {
					observation.NativeAvailability, err = prober.ProbeNativeSession(ctx, observation.NativeRef)
					if err != nil {
						observation.NativeAvailability = ports.NativeSessionAvailabilityUnknown
						return observation, err
					}
				}
			}
		}
	}
	current, found, err := m.store.GetSession(ctx, id)
	if err != nil {
		return observation, err
	}
	if !found || !reflect.DeepEqual(current, rec) {
		return observation, ports.ErrBuilderHandoffChanged
	}
	observation.ObservedAt = m.clock()
	return observation, ctx.Err()
}

func (m *Manager) existingBuilderHandoffSpawnSystemPrompt(ctx context.Context, cfg ports.SpawnConfig) (string, bool, error) {
	if cfg.BuilderHandoffContext == nil {
		return "", false, nil
	}
	records, err := m.store.ListSessions(ctx, cfg.ProjectID)
	if err != nil {
		return "", false, err
	}
	for _, rec := range records {
		if rec.CreationIdempotencyKey != cfg.CreationIdempotencyKey {
			continue
		}
		store, ok := m.store.(builderHandoffContextStore)
		if !ok {
			return "", false, ports.ErrBuilderHandoffInvalid
		}
		savedContext, _, _, found, err := store.GetClearDevBuilderHandoffContext(ctx, rec.ID)
		if err != nil {
			return "", false, err
		}
		if !found {
			return "", false, nil
		}
		if savedContext != *cfg.BuilderHandoffContext {
			return "", false, ports.ErrBuilderHandoffChanged
		}
		saved, err := m.builderHandoffRestoreSystemPrompt(ctx, rec, "")
		return saved, err == nil, err
	}
	return "", false, nil
}

func (m *Manager) buildRestoreSystemPrompt(ctx context.Context, rec domain.SessionRecord) (string, error) {
	saved, err := m.builderHandoffRestoreSystemPrompt(ctx, rec, "")
	if err != nil || saved != "" {
		return saved, err
	}
	return m.buildSystemPrompt(ctx, rec.Kind, rec.ProjectID)
}

func (m *Manager) checkBuilderSessionFence(ctx context.Context, id domain.SessionID) error {
	store, ok := m.store.(builderSessionOperationStore)
	if !ok {
		return nil
	}
	fenced, err := store.IsClearDevBuilderSessionFenced(ctx, id)
	if err != nil {
		return err
	}
	if fenced {
		return ports.ErrBuilderSessionFenced
	}
	return nil
}

// Record new controlled launch evidence from the actual launch environment.
// Old launches are never backfilled using today's configuration.
func (m *Manager) recordBuilderChatNativeBinding(ctx context.Context, rec domain.SessionRecord, metadata domain.SessionMetadata, env map[string]string) error {
	lookup, ok := m.store.(clearDevControlledSessionLookup)
	if !ok {
		return nil
	}
	controlled, err := lookup.IsClearDevControlledSession(ctx, string(rec.ID))
	if err != nil || !controlled {
		return err
	}
	agent, found := m.agents.Agent(rec.Harness)
	if !found {
		return ErrUnknownHarness
	}
	if _, ok := agent.(ports.AgentNativeSessionConfigProvider); !ok {
		return nil
	}
	registry, ok := m.store.(ports.AgentSwitchStore)
	if !ok {
		return errors.New("controlled Chat native launch evidence persistence is unavailable")
	}
	if metadata.ProviderConversationID == "" || metadata.ControllerGeneration == "" {
		return ports.ErrBuilderHandoffChanged
	}
	rec.Metadata = metadata
	rec.Metadata.AgentSessionID = metadata.ProviderConversationID
	_, err = m.preserveCurrentNativeSession(ctx, registry, rec, agent, env, domain.AgentGenerationID(metadata.ControllerGeneration))
	return err
}
