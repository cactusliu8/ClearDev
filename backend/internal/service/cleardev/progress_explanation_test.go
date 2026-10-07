package cleardev_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type progressAO struct {
	store *sqlite.Store
	ended map[domain.SessionID]bool
}

func (a *progressAO) GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error) {
	return a.store.GetProject(ctx, id)
}

func (a *progressAO) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	record, ok, err := a.store.GetSession(ctx, id)
	if a.ended[id] {
		record.IsTerminated = true
		record.Activity.State = domain.ActivityExited
	}
	return record, ok, err
}

type progressAgents struct {
	store       *sqlite.Store
	invalid     bool
	invalidLeft int
	lastPrompt  string
	mu          sync.Mutex
	spawns      []ports.SpawnConfig
	relays      int
	shots       map[domain.SessionID]chatsvc.Snapshot
	turns       map[string]string
	afterSpawn  func(domain.SessionID)
}

func (h *progressAgents) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spawns = append(h.spawns, cfg)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	record, _, err := h.store.CreateSessionIdempotent(ctx, domain.SessionRecord{
		ProjectID: cfg.ProjectID, Kind: cfg.Kind, Harness: cfg.Harness,
		Mode: cfg.RequestedMode, PermissionMode: cfg.AgentConfig.Permissions,
		CreationIdempotencyKey:     cfg.CreationIdempotencyKey,
		CreationRequestFingerprint: "progress-fp-" + cfg.CreationIdempotencyKey,
		DisplayName:                cfg.DisplayName, Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		Metadata: domain.SessionMetadata{
			WorkspacePath: "/managed/progress/" + cfg.CreationIdempotencyKey, WorkspaceRepoPath: "/tmp/" + string(cfg.ProjectID),
		},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return domain.Session{}, 0, 0, err
	}
	if h.afterSpawn != nil {
		h.afterSpawn(record.ID)
	}
	return domain.Session{SessionRecord: record}, 0, 0, nil
}

func (h *progressAgents) RelayChatTurnWithID(_ context.Context, sessionID domain.SessionID, prompt, clientMessageID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing := h.turns[clientMessageID]; existing != "" {
		return existing, nil
	}
	h.relays++
	route := prompt
	if strings.Contains(prompt, "The previous JSON result was rejected by the protocol parser.") {
		if h.lastPrompt != "" {
			route = h.lastPrompt
		}
	} else {
		h.lastPrompt = prompt
	}
	invalid := h.invalid
	if h.invalidLeft > 0 {
		invalid = true
		h.invalidLeft--
	}
	turnID := fmt.Sprintf("turn-explain-%d", h.relays)
	now := time.Date(2026, 8, 28, 12, 1, 0, 0, time.UTC)
	text := matchingProgressExplanation(route, invalid)
	snapshot := h.shots[sessionID]
	snapshot.SessionID = sessionID
	snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{
		ID: turnID, HandledBySessionID: sessionID, State: domain.TurnStateCompleted, CompletedAt: &now,
	})
	snapshot.Messages = append(snapshot.Messages,
		domain.ConversationMessage{
			ID: "user-" + turnID, TurnID: turnID, Sequence: int64(len(snapshot.Messages) + 1), Role: domain.MessageRoleUser,
			Origin: domain.MessageOriginAutomation, Text: prompt, ClientMessageID: clientMessageID,
		},
		domain.ConversationMessage{
			ID: "assistant-" + turnID, TurnID: turnID, Sequence: int64(len(snapshot.Messages) + 2), Role: domain.MessageRoleAssistant,
			Origin: domain.MessageOriginProvider, Text: text,
		},
	)
	if h.shots == nil {
		h.shots = map[domain.SessionID]chatsvc.Snapshot{}
	}
	h.shots[sessionID] = snapshot
	if h.turns == nil {
		h.turns = map[string]string{}
	}
	h.turns[clientMessageID] = turnID
	return turnID, nil
}

func (h *progressAgents) Snapshot(_ context.Context, sessionID domain.SessionID) (chatsvc.Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := h.shots[sessionID]
	snapshot.SessionID = sessionID
	return snapshot, nil
}

func (h *progressAgents) Interrupt(context.Context, domain.SessionID) error { return nil }

func matchingProgressExplanation(prompt string, invalid bool) string {
	if invalid {
		return `{"schemaVersion":1,"kind":"CLEARDEV_PROGRESS_EXPLANATION"}`
	}
	packet, err := core.PacketFromTrustedProgressPrompt(prompt)
	if err != nil || len(packet.Facts) == 0 {
		return `{"schemaVersion":1,"kind":"CLEARDEV_PROGRESS_EXPLANATION"}`
	}
	result := core.TrustedProgressExplanationResult{
		SchemaVersion: 1, Kind: core.TrustedProgressExplanationKind,
		FactSummarySHA256: packet.FactSummarySHA256, Phase: string(packet.Phase),
		Attention: string(packet.Attention), NextOwnerRole: packet.NextOwnerRole,
		PendingDecision: packet.PendingDecision, CitedFactIDs: []string{packet.Facts[0].ID},
		Summary: "The derived facts show the current phase, next owner, and whether a human decision is waiting.",
	}
	raw, err := core.MarshalAgentChosenResult(result)
	if err != nil {
		return `{"schemaVersion":1,"kind":"CLEARDEV_PROGRESS_EXPLANATION"}`
	}
	return string(raw)
}

func newProgressService(t *testing.T, store *sqlite.Store, ao *progressAO, agents *progressAgents) *cleardevsvc.Service {
	t.Helper()
	var mu sync.Mutex
	nextID, clockTick := 0, 0
	base := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	return cleardevsvc.New(cleardevsvc.Deps{
		Facts: store, StandardFacts: store, ProgressExplanations: store, HumanDecisions: store,
		ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, ParseCorrections: store, AgentAttempts: store,
		ControlledPreflights: store, ControlledPreflightChecker: passingProgressPreflight{}, Workspace: &workspaceObserver{},
		Inspector: unusedProgressChecks{}, Checks: unusedProgressChecks{}, RecoverAgentSession: func(context.Context, domain.SessionID) error { return fmt.Errorf("unexpected recovery") },
		AO: ao, Sessions: agents, Chat: agents, Human: allowHuman{},
		RunBackground: func(run func()) { run() },
		StepTimeout:   time.Second, PollInterval: time.Millisecond,
		NewID: func() string {
			mu.Lock()
			defer mu.Unlock()
			nextID++
			return fmt.Sprintf("progress-id-%03d", nextID)
		},
		Clock: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			clockTick++
			return base.Add(time.Duration(clockTick) * time.Second)
		},
	})
}

func bindIdleSteward(t *testing.T, store *sqlite.Store, requirementID string) domain.SessionID {
	t.Helper()
	return bindStewardSession(t, store, requirementID, domain.ActivityIdle)
}

func bindEndedSteward(t *testing.T, store *sqlite.Store, ao *progressAO, requirementID string) domain.SessionID {
	t.Helper()
	stewardID := bindStewardSession(t, store, requirementID, domain.ActivityActive)
	ao.ended[stewardID] = true
	return stewardID
}

func bindStewardSession(t *testing.T, store *sqlite.Store, requirementID string, state domain.ActivityState) domain.SessionID {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	steward, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "ao-service", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto,
		Activity:  domain.Activity{State: state, LastActivityAt: now},
		Metadata:  domain.SessionMetadata{WorkspacePath: "/managed/steward", WorkspaceRepoPath: "/tmp/ao-service"},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, created, err := store.StartClearDevStandardFlow(ctx, core.StartStandardFlowCommand{
		DevelopmentRequirementID: requirementID, StewardRoleBindingID: "steward-binding",
		StewardSessionIdempotencyKey: "steward-key", At: now,
	})
	if err != nil || !created {
		t.Fatalf("start standard flow = %+v created=%v err=%v", binding, created, err)
	}
	ok, err := store.BindClearDevRoleBinding(ctx, binding.ID, string(steward.ID), now.Add(time.Second))
	if err != nil || !ok {
		t.Fatalf("bind steward = %v err=%v", ok, err)
	}
	return steward.ID
}

func TestProgressExplanationUsesContinuationAfterOriginalEnds(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	ao := &progressAO{store: store, ended: map[domain.SessionID]bool{}}
	agents := &progressAgents{store: store}
	service := newProgressService(t, store, ao, agents)
	view := createConfirmedRequirement(t, service)
	bindEndedSteward(t, store, ao, view.Requirement.ID)

	explained, err := service.RequestProgressExplanation(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if explained.TrustedProgress.Explanation == nil || explained.TrustedProgress.Explanation.Status != core.ProgressExplanationSettled {
		t.Fatalf("explanation = %+v", explained.TrustedProgress.Explanation)
	}
	if explained.TrustedProgress.Explanation.Stale || explained.TrustedProgress.Explanation.Phase != string(explained.TrustedProgress.Phase) {
		t.Fatalf("settled explanation = %+v phase=%s", explained.TrustedProgress.Explanation, explained.TrustedProgress.Phase)
	}
	if len(agents.spawns) != 1 || agents.spawns[0].Prompt != "" || agents.spawns[0].CreationIdempotencyKey == "" {
		t.Fatalf("spawns = %+v", agents.spawns)
	}
	if string(agents.spawns[0].ProjectID) != "ao-service" {
		t.Fatalf("spawn project = %s", agents.spawns[0].ProjectID)
	}
	again, err := service.RequestProgressExplanation(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.TrustedProgress.Explanation.RequestID != explained.TrustedProgress.Explanation.RequestID {
		t.Fatalf("second request created %s, want %s", again.TrustedProgress.Explanation.RequestID, explained.TrustedProgress.Explanation.RequestID)
	}
	if len(agents.spawns) != 1 || agents.relays != 1 {
		t.Fatalf("restart spawned or resent: spawns=%d relays=%d", len(agents.spawns), agents.relays)
	}
}

func TestProgressExplanationContinuationSeedIsControlledBeforeBinding(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	ao := &progressAO{store: store, ended: map[domain.SessionID]bool{}}
	agents := &progressAgents{store: store}
	service := newProgressService(t, store, ao, agents)
	view := createConfirmedRequirement(t, service)
	bindEndedSteward(t, store, ao, view.Requirement.ID)
	observed := false
	agents.afterSpawn = func(id domain.SessionID) {
		observed = true
		controlled, err := store.IsClearDevControlledSession(context.Background(), string(id))
		if err != nil || !controlled {
			t.Errorf("unbound progress Steward seed: controlled=%v err=%v", controlled, err)
		}
	}
	if _, err := service.RequestProgressExplanation(context.Background(), view.Requirement.ID); err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("test did not observe the unbound seed")
	}
}

func TestProgressExplanationUsesOriginalWhenIdle(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	ao := &progressAO{store: store, ended: map[domain.SessionID]bool{}}
	agents := &progressAgents{store: store}
	service := newProgressService(t, store, ao, agents)
	view := createConfirmedRequirement(t, service)
	stewardID := bindIdleSteward(t, store, view.Requirement.ID)

	explained, err := service.RequestProgressExplanation(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if explained.TrustedProgress.Explanation == nil || explained.TrustedProgress.Explanation.Status != core.ProgressExplanationSettled {
		t.Fatalf("idle original explanation = %+v", explained.TrustedProgress.Explanation)
	}
	if len(agents.spawns) != 0 || agents.relays != 1 {
		t.Fatalf("idle original should reuse steward %s: spawns=%d relays=%d", stewardID, len(agents.spawns), agents.relays)
	}
}

func TestProgressExplanationDoesNotSpawnWhileOriginalIsRunning(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	ao := &progressAO{store: store, ended: map[domain.SessionID]bool{}}
	agents := &progressAgents{store: store}
	service := newProgressService(t, store, ao, agents)
	view := createConfirmedRequirement(t, service)
	bindEndedSteward(t, store, ao, view.Requirement.ID)
	ao.ended = map[domain.SessionID]bool{}

	explained, err := service.RequestProgressExplanation(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if explained.TrustedProgress.Explanation == nil || explained.TrustedProgress.Explanation.Status != core.ProgressExplanationPending {
		t.Fatalf("running original explanation = %+v", explained.TrustedProgress.Explanation)
	}
	if len(agents.spawns) != 0 || agents.relays != 0 {
		t.Fatalf("spawned while original was running: spawns=%d relays=%d", len(agents.spawns), agents.relays)
	}
}

func TestProgressExplanationInvalidResultDoesNotChangeProgress(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	ao := &progressAO{store: store, ended: map[domain.SessionID]bool{}}
	agents := &progressAgents{store: store, invalid: true}
	service := newProgressService(t, store, ao, agents)
	view := createConfirmedRequirement(t, service)
	bindEndedSteward(t, store, ao, view.Requirement.ID)
	view, err := service.GetRequirement(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := view.TrustedProgress.FactSummarySHA256
	phase := view.TrustedProgress.Phase

	explained, err := service.RequestProgressExplanation(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if explained.TrustedProgress.FactSummarySHA256 != before || explained.TrustedProgress.Phase != phase {
		t.Fatalf("invalid explanation changed progress: %+v", explained.TrustedProgress)
	}
	if explained.TrustedProgress.Explanation == nil || explained.TrustedProgress.Explanation.Status != core.ProgressExplanationFailed {
		t.Fatalf("invalid explanation = %+v", explained.TrustedProgress.Explanation)
	}
	assertProgressAttemptResults(t, explained, core.AgentResultParseInvalid, core.AgentResultParseInvalid)
}

func TestProgressExplanationJSONCorrectsOnce(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	ao := &progressAO{store: store, ended: map[domain.SessionID]bool{}}
	agents := &progressAgents{store: store, invalidLeft: 1}
	service := newProgressService(t, store, ao, agents)
	view := createConfirmedRequirement(t, service)
	bindEndedSteward(t, store, ao, view.Requirement.ID)
	explained, err := service.RequestProgressExplanation(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if explained.TrustedProgress.Explanation == nil || explained.TrustedProgress.Explanation.Status != core.ProgressExplanationSettled {
		t.Fatalf("corrected explanation = %+v", explained.TrustedProgress.Explanation)
	}
	if agents.relays != 2 {
		t.Fatalf("progress correction relays = %d, want 2", agents.relays)
	}
	assertProgressAttemptResults(t, explained, core.AgentResultParseInvalid, core.AgentResultParseValid)
}

func assertProgressAttemptResults(t *testing.T, view cleardevsvc.RequirementView, conclusions ...core.AgentResultParseConclusion) {
	t.Helper()
	if len(view.AgentStepAttempts) != 1 {
		t.Fatalf("Agent attempts = %#v, want one progress attempt", view.AgentStepAttempts)
	}
	attempt := view.AgentStepAttempts[0]
	if attempt.StepCategory != core.AgentStepCategoryProgress || attempt.AttemptNumber != 1 || attempt.LegacyEvidenceMissing {
		t.Fatalf("progress attempt = %#v", attempt)
	}
	if len(attempt.Results) != len(conclusions) {
		t.Fatalf("progress results = %#v, want %d", attempt.Results, len(conclusions))
	}
	for index, conclusion := range conclusions {
		result := attempt.Results[index]
		if result.ResultIndex != int64(index+1) || result.ParseConclusion != conclusion || result.RawMessageSHA256 == "" || result.TurnID == "" || result.FinalMessageID == "" {
			t.Fatalf("progress result %d = %#v, want parse %s", index+1, result, conclusion)
		}
	}
}

// These tests never run candidate checks; any call through an embedded nil
// interface fails the test instead of inventing a successful check.
type unusedProgressChecks struct {
	ports.ClearDevCandidateInspector
	ports.ClearDevCheckRunner
}

type passingProgressPreflight struct{}

func (passingProgressPreflight) CheckControlledPreflight(_ context.Context, harness domain.AgentHarness, requested string) (ports.ChatControlledPreflight, error) {
	return ports.ChatControlledPreflight{RequestedModel: requested, ResolvedModel: requested, Provider: string(harness), Models: []ports.ChatModel{}}, nil
}
