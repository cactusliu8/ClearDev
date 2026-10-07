package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type standardStepStore struct {
	StandardFactStore
	step  core.AgentStep
	order *[]string
}

type failOnceStandardStepSettleStore struct {
	StandardFactStore
	err error
}

func (s *failOnceStandardStepSettleStore) SettleClearDevStandardAgentStep(ctx context.Context, step core.AgentStep) (bool, error) {
	if s.err != nil {
		err := s.err
		s.err = nil
		return false, err
	}
	return s.StandardFactStore.SettleClearDevStandardAgentStep(ctx, step)
}

func TestStandardWorkerPromptsAreUnattended(t *testing.T) {
	tests := map[string]string{
		"builder":  builderPrompt([]byte(`{"dispatchId":"dispatch","taskId":"task"}`), 0, ""),
		"reviewer": reviewerPrompt([]byte(`{}`), "assignment", "candidate", strings.Repeat("a", 40), strings.Repeat("b", 64)),
	}
	for name, prompt := range tests {
		t.Run(name, func(t *testing.T) {
			for _, required := range []string{"unattended bounded workflow", "do not ask questions", "request user input", "interactive process"} {
				if !strings.Contains(strings.ToLower(prompt), required) {
					t.Fatalf("%s prompt does not forbid unattended input behavior %q", name, required)
				}
			}
			if name == "reviewer" && (!strings.Contains(prompt, "intermediate messages") || !strings.Contains(prompt, "final assistant message exactly one JSON object")) {
				t.Fatal("Reviewer prompt does not separate required progress updates from its strict final result")
			}
		})
	}
}

func (s *standardStepStore) CreateClearDevStandardAgentStep(_ context.Context, step core.AgentStep) (core.AgentStep, bool, error) {
	*s.order = append(*s.order, "create")
	s.step = step
	return step, true, nil
}

func (s *standardStepStore) MarkClearDevStandardAgentStepSent(_ context.Context, _ string, at time.Time) (bool, error) {
	*s.order = append(*s.order, "sent")
	s.step.SendStatus, s.step.SentAt = core.AgentStepSendStatusSent, &at
	return true, nil
}

func (s *standardStepStore) SettleClearDevStandardAgentStep(_ context.Context, step core.AgentStep) (bool, error) {
	*s.order = append(*s.order, "settle")
	s.step = step
	return true, nil
}

type exactStepChat struct {
	order       *[]string
	snapshot    chatsvc.Snapshot
	snapshotErr error
	relays      int
	response    string
	responses   []string
}

func (c *exactStepChat) RelayChatTurnWithID(_ context.Context, sessionID domain.SessionID, prompt, clientID string) (string, error) {
	*c.order = append(*c.order, "relay")
	c.relays++
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	turnID, userID, assistantID := "turn-1", "user-1", "assistant-1"
	if c.relays > 1 {
		turnID = fmt.Sprintf("turn-%d", c.relays)
		userID = fmt.Sprintf("user-%d", c.relays)
		assistantID = fmt.Sprintf("assistant-%d", c.relays)
	}
	response := c.response
	if index := c.relays - 1; index >= 0 && index < len(c.responses) {
		response = c.responses[index]
	}
	c.snapshot.SessionID = sessionID
	c.snapshot.Turns = append(c.snapshot.Turns, domain.ConversationTurn{
		ID: turnID, HandledBySessionID: sessionID, State: domain.TurnStateCompleted, CompletedAt: &now,
	})
	c.snapshot.Messages = append(c.snapshot.Messages,
		domain.ConversationMessage{
			ID: userID, TurnID: turnID, Sequence: int64(len(c.snapshot.Messages) + 1),
			Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: prompt, ClientMessageID: clientID,
		},
		domain.ConversationMessage{
			ID: assistantID, TurnID: turnID, Sequence: int64(len(c.snapshot.Messages) + 2),
			Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Text: response,
		},
	)
	return turnID, nil
}

func (c *exactStepChat) Snapshot(context.Context, domain.SessionID) (chatsvc.Snapshot, error) {
	return c.snapshot, c.snapshotErr
}

func (c *exactStepChat) Interrupt(context.Context, domain.SessionID) error {
	return nil
}

type exactStepAO struct{ record domain.SessionRecord }

func (a exactStepAO) GetProject(context.Context, string) (domain.ProjectRecord, bool, error) {
	return domain.ProjectRecord{ID: "project", Kind: domain.ProjectKindSingleRepo}, true, nil
}

func (a exactStepAO) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	return a.record, a.record.ID == id, nil
}

func TestRunAgentStepPersistsBeforeSendAndRecoversExactSettledTurn(t *testing.T) {
	order := []string{}
	store := &standardStepStore{order: &order}
	chat := &exactStepChat{order: &order, response: `{"ok":true}`}
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	binding := core.RoleSessionBinding{ID: "binding-1", AOSessionID: "session-1", Status: core.RoleBindingStatusBound}
	service := New(Deps{
		ParseCorrections:           newMemoryParseCorrections(),
		AgentAttempts:              newMemoryAgentAttempts(),
		ControlledPreflights:       newMemoryControlledPreflights(),
		ControlledPreflightChecker: alwaysPassControlledPreflight{},

		StandardFacts: store, AO: exactStepAO{record: domain.SessionRecord{ID: "session-1"}}, Chat: chat,
		NewID: func() string { return "step-1" }, Clock: func() time.Time { return now },
		StepTimeout: time.Second, PollInterval: time.Millisecond,
	})
	message, changed, err := service.runAgentStep(context.Background(), core.StandardFlowSnapshot{}, binding, "request-1", core.AgentStepRequestPlanning, "prompt",
		standardStepReasons{Invalid: "INVALID", Timeout: "TIMEOUT", Unavailable: "UNAVAILABLE"}, func(raw []byte) error {
			var value map[string]bool
			return json.Unmarshal(raw, &value)
		})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || message.StepID != "step-1" || message.TurnID != "turn-1" || message.MessageID != "assistant-1" {
		t.Fatalf("message = %#v, changed=%v", message, changed)
	}
	if want := []string{"create", "relay", "sent", "settle"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("operation order = %v, want %v", order, want)
	}
	if store.step.SendStatus != core.AgentStepSendStatusSettled || store.step.FinalMessageText != chat.response || store.step.MessageSHA256 != coreDigest([]byte(chat.response)) {
		t.Fatalf("settled step = %#v", store.step)
	}

	chat.snapshotErr = errors.New("transcript temporarily unavailable after restart")
	flow := core.StandardFlowSnapshot{AgentSteps: []core.AgentStep{store.step}}
	message, changed, err = service.runAgentStep(context.Background(), flow, binding, "request-1", core.AgentStepRequestPlanning, "prompt",
		standardStepReasons{Invalid: "INVALID", Timeout: "TIMEOUT", Unavailable: "UNAVAILABLE"}, func([]byte) error { return nil })
	if err != nil || changed || message.StepID != "step-1" {
		t.Fatalf("restart read = %#v, changed=%v, err=%v", message, changed, err)
	}
	if chat.relays != 1 {
		t.Fatalf("restart relayed %d times, want one total", chat.relays)
	}
}

func TestRunAgentStepResumesAfterSQLiteRestartBetweenParseAndSettle(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := sqlitetest.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ids := &standardTestIDs{}
	tick := 0
	clock := func() time.Time {
		tick++
		return time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC).Add(time.Duration(tick) * time.Second)
	}
	requirementID := seedConfirmedStandardRequirement(t, store, ids, clock)
	binding, created, err := store.StartClearDevStandardFlow(ctx, core.StartStandardFlowCommand{
		DevelopmentRequirementID: requirementID, StewardRoleBindingID: "restart-binding",
		StewardSessionIdempotencyKey: "restart-session-key", At: clock(),
	})
	if err != nil || !created {
		t.Fatalf("start flow: created=%v err=%v", created, err)
	}
	harness := newStandardAgentHarness(store, false)
	session, _, _, err := harness.Spawn(ctx, ports.SpawnConfig{
		ProjectID: "s02-project", Kind: domain.KindOrchestrator, Harness: domain.HarnessCodex,
		RequestedMode: domain.SessionModeChat, AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		CreationIdempotencyKey: binding.SessionCreationIdempotencyKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed, bindErr := store.BindClearDevRoleBinding(ctx, binding.ID, string(session.ID), clock()); bindErr != nil || !changed {
		t.Fatalf("bind flow: changed=%v err=%v", changed, bindErr)
	}
	flow, ok, err := store.GetClearDevStandardFlow(ctx, requirementID)
	if err != nil || !ok || len(flow.RoleBindings) != 1 {
		t.Fatalf("load bound flow = %#v, ok=%v err=%v", flow, ok, err)
	}
	binding = flow.RoleBindings[0]
	order := []string{}
	chat := &exactStepChat{order: &order, response: `{"ok":true}`}
	settleFailure := errors.New("stop after parse evidence")
	facts := &failOnceStandardStepSettleStore{StandardFactStore: store, err: settleFailure}
	first := New(Deps{
		ControlledPreflights:       newMemoryControlledPreflights(),
		ControlledPreflightChecker: alwaysPassControlledPreflight{},

		StandardFacts: facts, AgentAttempts: store, ParseCorrections: store,
		Chat: chat, NewID: func() string { return "restart-step" }, Clock: clock,
		StepTimeout: time.Second, PollInterval: time.Millisecond,
	})
	validate := func(raw []byte) error {
		var value map[string]bool
		return json.Unmarshal(raw, &value)
	}
	reasons := standardStepReasons{Invalid: "INVALID", Timeout: "TIMEOUT", Unavailable: "UNAVAILABLE"}
	if _, _, err := first.runAgentStep(ctx, flow, binding, "request-1", core.AgentStepRequestPlanning, "prompt", reasons, validate); !errors.Is(err, settleFailure) {
		t.Fatalf("first run error = %v, want settle boundary failure", err)
	}
	if chat.relays != 1 {
		t.Fatalf("first run relays = %d, want 1", chat.relays)
	}
	beforeRestart, ok, err := store.GetClearDevStandardFlow(ctx, requirementID)
	if err != nil || !ok || len(beforeRestart.AgentSteps) != 1 || beforeRestart.AgentSteps[0].SendStatus != core.AgentStepSendStatusSent {
		t.Fatalf("flow before restart = %#v, ok=%v err=%v", beforeRestart, ok, err)
	}
	views, err := store.ListClearDevAgentStepAttempts(ctx, requirementID)
	if err != nil || len(views) != 1 || len(views[0].Results) != 1 || views[0].Results[0].ParseConclusion != core.AgentResultParseValid {
		t.Fatalf("evidence before restart = %#v, err=%v", views, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	flow, ok, err = reopened.GetClearDevStandardFlow(ctx, requirementID)
	if err != nil || !ok || len(flow.RoleBindings) != 1 {
		t.Fatalf("load restarted flow = %#v, ok=%v err=%v", flow, ok, err)
	}
	binding = flow.RoleBindings[0]
	restarted := New(Deps{
		ControlledPreflights:       newMemoryControlledPreflights(),
		ControlledPreflightChecker: alwaysPassControlledPreflight{},

		StandardFacts: reopened, AgentAttempts: reopened, ParseCorrections: reopened,
		Chat: chat, NewID: func() string { return "unexpected-new-step" }, Clock: clock,
		StepTimeout: time.Second, PollInterval: time.Millisecond,
	})
	message, changed, err := restarted.runAgentStep(ctx, flow, binding, "request-1", core.AgentStepRequestPlanning, "prompt", reasons, validate)
	if err != nil || !changed || message.StepID != "restart-step" {
		t.Fatalf("restart result = %#v, changed=%v err=%v", message, changed, err)
	}
	if chat.relays != 1 {
		t.Fatalf("restart resent the request: relays=%d", chat.relays)
	}
	views, err = reopened.ListClearDevAgentStepAttempts(ctx, requirementID)
	if err != nil || len(views) != 1 || len(views[0].Results) != 1 {
		t.Fatalf("restart evidence = %#v, err=%v", views, err)
	}
	completed, ok, err := reopened.GetClearDevStandardFlow(ctx, requirementID)
	if err != nil || !ok || len(completed.AgentSteps) != 1 || completed.AgentSteps[0].SendStatus != core.AgentStepSendStatusSettled {
		t.Fatalf("completed restarted flow = %#v, ok=%v err=%v", completed, ok, err)
	}
}

func TestExactAgentMessageRejectsSpoofedInterruptedAndStreamingResults(t *testing.T) {
	now := time.Now().UTC()
	base := chatsvc.Snapshot{
		SessionID: "session-1",
		Turns:     []domain.ConversationTurn{{ID: "turn-1", HandledBySessionID: "session-1", State: domain.TurnStateCompleted, CompletedAt: &now}},
		Messages: []domain.ConversationMessage{
			{ID: "user", TurnID: "turn-1", Sequence: 1, Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: "prompt", ClientMessageID: "client-1"},
			{ID: "answer", TurnID: "turn-1", Sequence: 2, Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Text: `{}`},
		},
	}
	if got, err := exactAgentMessage(base, "session-1", "client-1", "prompt"); err != nil || got.MessageID != "answer" {
		t.Fatalf("valid exact message = %#v, err=%v", got, err)
	}
	emptySession := base
	emptySession.SessionID = ""
	if _, err := exactAgentMessage(emptySession, "", "client-1", "prompt"); err == nil {
		t.Fatal("empty expected and snapshot session IDs were accepted")
	}
	tests := map[string]func(*chatsvc.Snapshot){
		"missing snapshot session": func(value *chatsvc.Snapshot) { value.SessionID = "" },
		"other snapshot":           func(value *chatsvc.Snapshot) { value.SessionID = "session-2" },
		"other session":            func(value *chatsvc.Snapshot) { value.Turns[0].HandledBySessionID = "session-2" },
		"interrupted":              func(value *chatsvc.Snapshot) { value.Turns[0].State = domain.TurnStateInterrupted },
		"no completion fact": func(value *chatsvc.Snapshot) {
			value.Turns[0].CompletedAt = nil
		},
		"rolled back": func(value *chatsvc.Snapshot) {
			value.Turns[0].RolledBackAt = &now
		},
		"last streaming": func(value *chatsvc.Snapshot) {
			value.Messages = append(value.Messages, domain.ConversationMessage{ID: "stream", TurnID: "turn-1", Sequence: 3, Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Streaming: true})
		},
		"duplicate client id": func(value *chatsvc.Snapshot) {
			value.Messages = append(value.Messages, domain.ConversationMessage{ID: "user-2", TurnID: "turn-1", Sequence: 3, Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: "prompt", ClientMessageID: "client-1"})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := base
			value.Turns = append([]domain.ConversationTurn(nil), base.Turns...)
			value.Messages = append([]domain.ConversationMessage(nil), base.Messages...)
			mutate(&value)
			if _, err := exactAgentMessage(value, "session-1", "client-1", "prompt"); err == nil {
				t.Fatal("spoofed or non-final result was accepted")
			}
		})
	}
}

func TestValidateStandardSessionRejectsEmptyWorkspaceForEveryRole(t *testing.T) {
	roles := []core.StandardRole{
		core.StandardRoleSteward,
		core.StandardRoleEngineeringPlanner,
		core.StandardRoleBuilder,
		core.StandardRoleReviewer,
	}
	for _, role := range roles {
		t.Run(string(role), func(t *testing.T) {
			kind := domain.KindWorker
			if role == core.StandardRoleSteward {
				kind = domain.KindOrchestrator
			}
			binding := core.RoleSessionBinding{
				ID: "binding-" + string(role), Role: role,
				SessionCreationIdempotencyKey: "spawn-" + string(role), Status: core.RoleBindingStatusBound,
			}
			record := domain.SessionRecord{
				ID: domain.SessionID("session-" + string(role)), ProjectID: "project", Kind: kind, Harness: domain.HarnessCodex,
				Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto,
				CreationIdempotencyKey: binding.SessionCreationIdempotencyKey,
			}
			if err := validateStandardSession(record, binding, "project", kind, core.StandardFlowSnapshot{RoleBindings: []core.RoleSessionBinding{binding}}); err == nil {
				t.Fatal("session with an empty managed workspace was accepted")
			}
			record.Metadata.WorkspacePath = "/managed/" + string(role)
			if err := validateStandardSession(record, binding, "project", kind, core.StandardFlowSnapshot{RoleBindings: []core.RoleSessionBinding{binding}}); err != nil {
				t.Fatalf("valid managed session rejected: %v", err)
			}
		})
	}
}

func TestRunAgentStepPersistsUnavailableForTerminatedBoundSession(t *testing.T) {
	order := []string{}
	store := &standardStepStore{order: &order}
	chat := &exactStepChat{order: &order, response: `{}`}
	binding := core.RoleSessionBinding{
		ID: "builder-binding", Role: core.StandardRoleBuilder, AOSessionID: "builder-session",
		SessionCreationIdempotencyKey: "builder-key", Status: core.RoleBindingStatusBound,
	}
	record := domain.SessionRecord{
		ID: "builder-session", ProjectID: "project", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto,
		CreationIdempotencyKey: "builder-key", IsTerminated: true,
	}
	service := New(Deps{
		ParseCorrections:           newMemoryParseCorrections(),
		AgentAttempts:              newMemoryAgentAttempts(),
		ControlledPreflights:       newMemoryControlledPreflights(),
		ControlledPreflightChecker: alwaysPassControlledPreflight{},

		StandardFacts: store, AO: exactStepAO{record: record}, Chat: chat,
		NewID: func() string { return "unavailable-step" }, Clock: func() time.Time { return time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC) },
	})
	_, changed, err := service.runAgentStep(context.Background(), core.StandardFlowSnapshot{RoleBindings: []core.RoleSessionBinding{binding}}, binding,
		"dispatch:round:0", core.AgentStepBuilderResult, "builder prompt",
		standardStepReasons{Invalid: "INVALID", Timeout: "TIMEOUT", Unavailable: "BUILDER_UNAVAILABLE", ProjectID: "project"},
		func([]byte) error { return nil })
	if !errors.Is(err, errStandardStopped) || !changed {
		t.Fatalf("terminated bound session result: changed=%v err=%v", changed, err)
	}
	if store.step.SendStatus != core.AgentStepSendStatusFailed || store.step.ReasonCode != "BUILDER_UNAVAILABLE" || chat.relays != 0 {
		t.Fatalf("durable unavailable step = %#v, relays=%d", store.step, chat.relays)
	}
	if want := []string{"create", "settle"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("unavailable operation order = %v, want %v", order, want)
	}
}

type pendingStepChat struct {
	order    *[]string
	relays   int
	prompts  []string
	relayErr error
}

func (c *pendingStepChat) RelayChatTurnWithID(_ context.Context, _ domain.SessionID, prompt, _ string) (string, error) {
	*c.order = append(*c.order, "relay")
	c.relays++
	c.prompts = append(c.prompts, prompt)
	if c.relayErr != nil {
		return "", c.relayErr
	}
	return fmt.Sprintf("turn-%d", c.relays), nil
}

func (c *pendingStepChat) Snapshot(_ context.Context, sessionID domain.SessionID) (chatsvc.Snapshot, error) {
	return chatsvc.Snapshot{SessionID: sessionID}, nil
}

func (c *pendingStepChat) Interrupt(context.Context, domain.SessionID) error { return nil }

type claimingStepChat struct {
	mu     sync.Mutex
	relays int
}

func (c *claimingStepChat) RelayChatTurnWithID(context.Context, domain.SessionID, string, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.relays++
	return "turn-claimed", nil
}

func (*claimingStepChat) Snapshot(context.Context, domain.SessionID) (chatsvc.Snapshot, error) {
	return chatsvc.Snapshot{}, nil
}

func (*claimingStepChat) Interrupt(context.Context, domain.SessionID) error { return nil }

func (c *claimingStepChat) relayCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.relays
}

func TestRelayAgentTurnClaimsDeliveryBoundaryOnce(t *testing.T) {
	now := time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC)
	attempts := newMemoryAgentAttempts()
	chat := &claimingStepChat{}
	service := New(Deps{
		ParseCorrections:           newMemoryParseCorrections(),
		ControlledPreflights:       newMemoryControlledPreflights(),
		ControlledPreflightChecker: alwaysPassControlledPreflight{},
		AgentAttempts:              attempts, Chat: chat, Clock: func() time.Time { return now }})
	step := core.AgentStep{
		ID: "concurrent-step", Kind: core.AgentStepEngineeringPlan,
		ClientMessageID: "message-concurrent", PromptSHA256: coreDigest([]byte("prompt")), RequestedAt: now,
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := service.relayAgentTurn(context.Background(), "requirement-1", core.AgentStepCategoryStandard,
				step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now)
			if failure, ok := ports.ChatFailureFromError(err); ok && failure.Category == domain.AgentFailureDeliveryUnknown {
				err = nil
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if chat.relayCount() != 1 {
		t.Fatalf("provider relays = %d, want 1", chat.relayCount())
	}
	views, err := attempts.ListClearDevAgentStepAttempts(context.Background(), "requirement-1")
	if err != nil || len(views) != 1 || views[0].SendStatus != core.AgentAttemptSent {
		t.Fatalf("attempt views = %#v err=%v", views, err)
	}
}

func TestRelayAgentTurnDoesNotResendAfterDeliveryBoundaryRestart(t *testing.T) {
	now := time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC)
	attempts := newMemoryAgentAttempts()
	step := core.AgentStep{
		ID: "restart-step", Kind: core.AgentStepEngineeringPlan,
		ClientMessageID: "message-restart", PromptSHA256: coreDigest([]byte("prompt")), RequestedAt: now,
	}
	service := New(Deps{AgentAttempts: attempts, Clock: func() time.Time { return now }})
	attempt, err := service.ensureAgentAttempt(context.Background(), "requirement-1", core.AgentStepCategoryStandard, step, "session-1", agentFirstAttemptNumber, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attempts.EnsureClearDevAgentAttemptEvent(context.Background(), core.AgentAttemptEvent{
		ID: agentEvidenceID(attempt.ID, "delivery-boundary", step.ClientMessageID), AttemptID: attempt.ID,
		Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: step.ClientMessageID,
		PromptSHA256: step.PromptSHA256, FailureCategory: domain.AgentFailureDeliveryUnknown,
		ErrorSummary: safeAgentFailureSummary(domain.AgentFailureDeliveryUnknown), RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	chat := &claimingStepChat{}
	restarted := New(Deps{
		ParseCorrections:           newMemoryParseCorrections(),
		ControlledPreflights:       newMemoryControlledPreflights(),
		ControlledPreflightChecker: alwaysPassControlledPreflight{},
		AgentAttempts:              attempts, Chat: chat, Clock: func() time.Time { return now }})
	err = restarted.relayAgentTurn(context.Background(), "requirement-1", core.AgentStepCategoryStandard,
		step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now)
	failure, ok := ports.ChatFailureFromError(err)
	if !ok || failure.Category != domain.AgentFailureDeliveryUnknown || chat.relayCount() != 0 {
		t.Fatalf("restart failure=%#v found=%v relays=%d", failure, ok, chat.relayCount())
	}
}

func TestRunAgentStepTimeoutDoesNotSendParseCorrection(t *testing.T) {
	order := []string{}
	store := &standardStepStore{order: &order}
	chat := &pendingStepChat{order: &order}
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	binding := core.RoleSessionBinding{ID: "binding-1", AOSessionID: "session-1", Status: core.RoleBindingStatusBound}
	service := New(Deps{
		ParseCorrections:           newMemoryParseCorrections(),
		AgentAttempts:              newMemoryAgentAttempts(),
		ControlledPreflights:       newMemoryControlledPreflights(),
		ControlledPreflightChecker: alwaysPassControlledPreflight{},

		StandardFacts: store, AO: exactStepAO{record: domain.SessionRecord{ID: "session-1"}}, Chat: chat,
		NewID: func() string { return "timeout-step" }, Clock: func() time.Time { return now },
		StepTimeout: 20 * time.Millisecond, PollInterval: 5 * time.Millisecond,
	})
	_, changed, err := service.runAgentStep(context.Background(), core.StandardFlowSnapshot{}, binding, "request-1", core.AgentStepRequestPlanning, "prompt",
		standardStepReasons{Invalid: "INVALID", Timeout: "TIMEOUT", Unavailable: "UNAVAILABLE"}, func([]byte) error {
			t.Fatal("timeout path must not parse an Agent body")
			return errors.New("should not parse")
		})
	if !errors.Is(err, errStandardStopped) || !changed {
		t.Fatalf("timeout result: changed=%v err=%v", changed, err)
	}
	if store.step.SendStatus != core.AgentStepSendStatusSent || store.step.ReasonCode != "" {
		t.Fatalf("timeout step = %#v", store.step)
	}
	if chat.relays != 1 {
		t.Fatalf("timeout relayed %d times, want one original send", chat.relays)
	}
	for _, prompt := range chat.prompts {
		if strings.Contains(prompt, parseCorrectionPromptPrefix) {
			t.Fatalf("timeout sent a parse correction: %q", prompt)
		}
	}
	attempts, loadErr := service.attempts.ListClearDevAgentStepAttempts(context.Background(), "")
	if loadErr != nil || len(attempts) != 1 || attempts[0].SendStatus != core.AgentAttemptObservationTimedOut ||
		attempts[0].FailureCategory != domain.AgentFailureObservationTimeout {
		t.Fatalf("timeout attempt evidence = %#v err=%v", attempts, loadErr)
	}
}

func TestRunAgentStepDeliveryUnknownStopsWithoutFailureOrResend(t *testing.T) {
	order := []string{}
	store := &standardStepStore{order: &order}
	chat := &pendingStepChat{order: &order, relayErr: errors.New("transport ended after write")}
	now := time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC)
	binding := core.RoleSessionBinding{ID: "binding-1", AOSessionID: "session-1", Status: core.RoleBindingStatusBound}
	service := New(Deps{
		ParseCorrections:           newMemoryParseCorrections(),
		AgentAttempts:              newMemoryAgentAttempts(),
		ControlledPreflights:       newMemoryControlledPreflights(),
		ControlledPreflightChecker: alwaysPassControlledPreflight{},

		StandardFacts: store, AO: exactStepAO{record: domain.SessionRecord{ID: "session-1"}}, Chat: chat,
		NewID: func() string { return "unknown-step" }, Clock: func() time.Time { return now },
		StepTimeout: time.Second, PollInterval: time.Millisecond,
	})
	run := func(flow core.StandardFlowSnapshot) {
		t.Helper()
		_, changed, err := service.runAgentStep(context.Background(), flow, binding, "request-1", core.AgentStepRequestPlanning, "prompt",
			standardStepReasons{Invalid: "INVALID", Timeout: "TIMEOUT", Unavailable: "UNAVAILABLE"}, func([]byte) error { return nil })
		if !errors.Is(err, errStandardStopped) || !changed {
			t.Fatalf("delivery unknown result: changed=%v err=%v", changed, err)
		}
	}
	run(core.StandardFlowSnapshot{})
	if store.step.SendStatus != core.AgentStepSendStatusPending {
		t.Fatalf("delivery unknown changed logical step to %s", store.step.SendStatus)
	}
	attempts, err := service.attempts.ListClearDevAgentStepAttempts(context.Background(), "")
	if err != nil || len(attempts) != 1 || attempts[0].SendStatus != core.AgentAttemptDeliveryUnknown ||
		attempts[0].FailureCategory != domain.AgentFailureDeliveryUnknown || attempts[0].Retryable {
		t.Fatalf("delivery unknown attempt = %#v err=%v", attempts, err)
	}
	chat.relayErr = nil
	run(core.StandardFlowSnapshot{AgentSteps: []core.AgentStep{store.step}})
	if chat.relays != 1 {
		t.Fatalf("delivery unknown was resent %d times", chat.relays)
	}
}

func TestExactAgentMessagePreservesInterruptedTurn(t *testing.T) {
	now := time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC)
	snapshot := chatsvc.Snapshot{
		SessionID: "session-1",
		Turns: []domain.ConversationTurn{{
			ID: "turn-1", HandledBySessionID: "session-1", State: domain.TurnStateInterrupted,
			CompletedAt: &now, Failure: &domain.ConversationFailure{
				Category: domain.AgentFailureTurnInterrupted, ErrorSummary: "Agent turn was interrupted",
			},
		}},
		Messages: []domain.ConversationMessage{{
			ID: "user-1", TurnID: "turn-1", Role: domain.MessageRoleUser,
			Origin: domain.MessageOriginAutomation, Text: "prompt", ClientMessageID: "message-1",
		}},
	}
	_, err := exactAgentMessage(snapshot, "session-1", "message-1", "prompt")
	var terminal *agentTerminalError
	if !errors.As(err, &terminal) || terminal.turn.State != domain.TurnStateInterrupted {
		t.Fatalf("interrupted observation error = %#v", err)
	}
	failure := stableObservationFailure(err)
	if failure.Category != domain.AgentFailureTurnInterrupted {
		t.Fatalf("interrupted failure = %#v", failure)
	}
}

func TestStableAgentFailureClassificationUsesTypedContracts(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		category  domain.AgentFailureCategory
		retryable bool
	}{
		{name: "model", err: ports.ErrChatModelNotAvailable, category: domain.AgentFailureModelUnavailable},
		{name: "authentication", err: ports.ErrChatAuthRequired, category: domain.AgentFailureAuthentication},
		{name: "quota", err: ports.ErrChatQuotaExhausted, category: domain.AgentFailureQuotaExhausted},
		{name: "rate", err: ports.ErrChatRateLimited, category: domain.AgentFailureRateLimited, retryable: true},
		{name: "provider", err: ports.ErrChatProviderUnavailable, category: domain.AgentFailureProviderUnavailable, retryable: true},
		{name: "driver", err: ports.ErrChatDriverIncompatible, category: domain.AgentFailureDriverIncompatible},
		{name: "session", err: ports.ErrChatResumeFailed, category: domain.AgentFailureSessionLost, retryable: true},
		{name: "unknown delivery", err: errors.New("本地化且不可分类的发送错误"), category: domain.AgentFailureDeliveryUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := stableSendFailure(test.err)
			if failure.Category != test.category || failure.Retryable != test.retryable || failure.ErrorSummary == "" {
				t.Fatalf("stable failure = %#v", failure)
			}
		})
	}

	retryAt := time.Date(2026, 9, 5, 10, 30, 0, 0, time.UTC)
	failure := stableSendFailure(ports.WithChatFailure(errors.New("provider detail"), domain.ConversationFailure{
		Category: domain.AgentFailureRateLimited, ProviderErrorCode: "rateLimitExceeded",
		Retryable: true, RetryAt: &retryAt,
	}))
	if failure.ProviderErrorCode != "rateLimitExceeded" || failure.RetryAt == nil || !failure.RetryAt.Equal(retryAt) {
		t.Fatalf("typed provider details were not preserved: %#v", failure)
	}
}

func TestPlannerInvalidReasonDistinguishesMalformedFromFrozenScopeDeviation(t *testing.T) {
	if got := standardPlannerInvalidReason(errors.New("decode agent result")); got != "PLANNER_RESULT_INVALID" {
		t.Fatalf("malformed planner reason = %s", got)
	}
	if got := standardPlannerInvalidReason(errors.New("PLAN_OUT_OF_SCOPE")); got != "PLAN_OUT_OF_SCOPE" {
		t.Fatalf("scope-deviating planner reason = %s", got)
	}
}

func TestScopeChecksBothSidesOfRename(t *testing.T) {
	plan := core.FrozenStandardTaskPlan()
	if passed, _ := scopePathsPass(plan, []ports.ClearDevDiffPath{{Status: "R100", OldPath: "src/email.js", Path: "package.json"}}); passed {
		t.Fatal("rename into a forbidden path passed scope")
	}
	if passed, _ := scopePathsPass(plan, []ports.ClearDevDiffPath{{Status: "R100", OldPath: "src/email.js", Path: "test/email.test.js"}}); !passed {
		t.Fatal("rename whose old and new paths are allowed failed scope")
	}
}

func TestBuildStandardReviewPacketExcludesStaleCandidateEvidence(t *testing.T) {
	planResult := core.EngineeringPlanResult{SchemaVersion: 1, Kind: "ENGINEERING_PLAN", Task: core.FrozenStandardTaskPlan()}
	planJSON, _ := json.Marshal(planResult)
	plan := core.EngineeringPlan{ID: "plan", PlanJSON: string(planJSON), PlanSHA256: coreDigest(planJSON)}
	dispatch := core.Dispatch{ID: "dispatch", ExecutionPackageJSON: `{"schemaVersion":1}`, BaseCommitSHA: forty("a")}
	candidate := core.CandidateCommit{ID: "candidate-2", CommitSHA: forty("c")}
	runs := []core.CandidateCheckRun{
		{CandidateCommitID: "candidate-1", Kind: core.CandidateCheckRequired, Name: "stale", Status: core.CandidateCheckRunStatusSettled, Result: core.EvidenceResultPass},
		{CandidateCommitID: candidate.ID, Kind: core.CandidateCheckScope, Name: "scope", Status: core.CandidateCheckRunStatusSettled, Result: core.EvidenceResultPass, OutputSHA256: coreDigest(nil)},
	}
	encoded, _, _, err := buildStandardReviewPacket("review", "requirement", plan, dispatch, candidate,
		ports.ClearDevCandidateInspection{BaseSHA: dispatch.BaseCommitSHA, CandidateSHA: candidate.CommitSHA}, runs)
	if err != nil {
		t.Fatal(err)
	}
	var packet standardReviewPacket
	if err := json.Unmarshal(encoded, &packet); err != nil {
		t.Fatal(err)
	}
	if len(packet.Checks) != 1 || packet.Checks[0].Name != "scope" {
		t.Fatalf("review packet checks = %#v", packet.Checks)
	}
}

func TestStandardDispatchAcceptanceFreezesTaskAndChecks(t *testing.T) {
	plan := core.FrozenStandardTaskPlan()
	pack, packSHA, err := encodeStandardExecutionPackage(standardExecutionPackage{
		SchemaVersion: 1, Mode: "STANDARD", DispatchID: "dispatch", TaskID: "task", RequirementVersionID: "version",
		RequirementSHA256: coreDigest([]byte("requirement")), RequirementText: "requirement", PlanID: "plan", PlanSHA256: coreDigest([]byte("plan")), Task: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	next := 0
	acceptance, err := standardDispatchAcceptance(core.Dispatch{
		ID: "dispatch", PreallocatedDevelopmentTaskID: "task", RequirementVersionID: "version", EngineeringPlanID: "plan", ExecutionPackageJSON: string(pack),
		ExecutionPackageSHA256: packSHA,
	}, domain.SessionRecord{ID: "builder", Metadata: domain.SessionMetadata{DiffBaseSHA: forty("a")}}, core.RequirementVersion{ID: "version"}, time.Now().UTC(), func() string {
		next++
		return string(rune('a' + next))
	})
	if err != nil {
		t.Fatal(err)
	}
	if acceptance.InitialTask.Task.Status != core.DevelopmentTaskStatusPlanned || acceptance.InitialTask.Task.MaxReworkCount != 1 ||
		len(acceptance.InitialTask.Checks) != 1 || acceptance.InitialTask.Checks[0].Kind != "REQUIRED_CHECK" || acceptance.IntegrationCheck.Kind != "INTEGRATION" {
		t.Fatalf("acceptance = %#v", acceptance)
	}
}

func TestStandardStoreRejectsCorruptedImmutableFacts(t *testing.T) {
	tests := []struct {
		fact         string
		assertAbsent func(*testing.T, core.StandardFlowSnapshot)
	}{
		{fact: "plan-hash", assertAbsent: func(t *testing.T, flow core.StandardFlowSnapshot) {
			if len(flow.EngineeringPlans) != 0 {
				t.Fatalf("corrupted plan was persisted: %#v", flow.EngineeringPlans)
			}
		}},
		{fact: "dispatch-hash", assertAbsent: func(t *testing.T, flow core.StandardFlowSnapshot) {
			if len(flow.Dispatches) != 0 {
				t.Fatalf("corrupted execution package was persisted: %#v", flow.Dispatches)
			}
		}},
		{fact: "check-spec", assertAbsent: func(t *testing.T, flow core.StandardFlowSnapshot) {
			if len(flow.CandidateCheckRuns) != 0 {
				t.Fatalf("unauthorized checker spec was persisted: %#v", flow.CandidateCheckRuns)
			}
		}},
		{fact: "review-hash", assertAbsent: func(t *testing.T, flow core.StandardFlowSnapshot) {
			if len(flow.LocalReviews) != 0 {
				t.Fatalf("corrupted review packet was persisted: %#v", flow.LocalReviews)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.fact, func(t *testing.T) {
			store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
			facts := corruptStandardFactStore{StandardFactStore: store, fact: test.fact}
			service := standardTestService(store, facts, harness, ids, clock, context.Background())
			if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			if err := service.runStandardFlow(context.Background(), requirementID); err == nil {
				t.Fatalf("corrupted %s crossed the durable trust boundary", test.fact)
			}
			flow, ok, err := store.GetClearDevStandardFlow(context.Background(), requirementID)
			if err != nil || !ok {
				t.Fatalf("read flow after rejected %s: ok=%t err=%v", test.fact, ok, err)
			}
			test.assertAbsent(t, flow)
		})
	}
}

func TestStandardFailureMappingCannotBeForgedByCaller(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, true)
	service := standardTestService(store, forgedStandardFailureStore{StandardFactStore: store}, harness, ids, clock, context.Background())
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	if err := service.runStandardFlow(context.Background(), requirementID); err == nil {
		t.Fatal("forged infrastructure classification was accepted")
	}
	view, err := service.GetRequirement(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.DevelopmentTasks) != 1 || view.DevelopmentTasks[0].DevelopmentTask.Status != core.DevelopmentTaskStatusReview ||
		view.DevelopmentTasks[0].DevelopmentTask.ReworkCount != 0 {
		t.Fatalf("forged failure command changed task state: %#v", view.DevelopmentTasks)
	}
	for _, event := range view.Events {
		if event.Action == core.ActionBlockDevelopmentTask || event.Action == core.ActionEscalateDevelopmentTask || event.Action == core.ActionRequestDevelopmentTaskRework {
			t.Fatalf("forged failure command persisted transition event: %#v", event)
		}
	}
}

func TestDeriveStandardFlowStatusHandlesPretaskFailuresAndReplacement(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	baseSnapshot := core.RequirementSnapshot{
		Requirement:         core.DevelopmentRequirement{ID: "requirement"},
		RequirementVersions: []core.RequirementVersion{{ID: "version-1", Status: core.RequirementVersionStatusConfirmed}},
	}
	overall := core.OverallProgress{Phase: core.OverallPhasePlanningTasks, Attention: core.OverallAttentionNone}
	tests := []struct {
		name          string
		snapshot      core.RequirementSnapshot
		flow          core.StandardFlowSnapshot
		wantAttention StandardFlowAttention
		wantReason    core.ReasonCode
	}{
		{
			name:     "Planner step failed",
			snapshot: baseSnapshot,
			flow: core.StandardFlowSnapshot{RequirementVersionID: "version-1",
				RoleBindings: []core.RoleSessionBinding{{ID: "planner", Role: core.StandardRoleEngineeringPlanner}},
				AgentSteps:   []core.AgentStep{{RoleBindingID: "planner", Kind: core.AgentStepEngineeringPlan, SendStatus: core.AgentStepSendStatusFailed, ReasonCode: "PLANNER_TIMEOUT", FailedAt: &now}},
			},
			wantAttention: StandardFlowAttentionNeedsHuman, wantReason: "PLANNER_TIMEOUT",
		},
		{
			name: "bound version replaced",
			snapshot: core.RequirementSnapshot{Requirement: baseSnapshot.Requirement, RequirementVersions: []core.RequirementVersion{
				{ID: "version-1", Status: core.RequirementVersionStatusSuperseded},
				{ID: "version-2", Status: core.RequirementVersionStatusConfirmed},
			}},
			flow:          core.StandardFlowSnapshot{RequirementVersionID: "version-1"},
			wantAttention: StandardFlowAttentionReplan, wantReason: core.ReasonStalePlan,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := deriveStandardFlowStatus(test.snapshot, test.flow, overall)
			if got.Phase != core.OverallPhasePlanningTasks || got.Attention != test.wantAttention || got.ReasonCode != test.wantReason {
				t.Fatalf("derived flow status = %#v", got)
			}
		})
	}
}

func forty(value string) string {
	result := ""
	for len(result) < 40 {
		result += value
	}
	return result[:40]
}

const standardFixtureRequirement = "实现 `normalizeEmail(input)`。输入必须是字符串；先去除首尾空白，再要求恰好一个 `@`，且本地部分和域名都非空，否则抛出 `TypeError(\"invalid email\")`。输出保留本地部分大小写，把域名转为小写。至少测试空白处理、域名大小写、非字符串、缺少或多个 `@`、空本地部分和空域名。不实现登录、网络、数据库、国际化域名或完整 RFC 邮箱校验。"

type allowStandardHuman struct{}

func (allowStandardHuman) Authorize(context.Context, HumanDecision) error { return nil }

type standardTestIDs struct {
	mu   sync.Mutex
	next int
}

func (ids *standardTestIDs) New() string {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.next++
	return fmt.Sprintf("s02-test-%03d", ids.next)
}

type standardRelay struct {
	sessionID       domain.SessionID
	clientMessageID string
	prompt          string
	response        string
}

// standardAgentHarness keeps the fake boundary deliberately outside the
// coordinator. AO session facts still go through SQLite, while Chat, Git and
// the restricted runner return deterministic observations without a network.
type standardAgentHarness struct {
	store *sqlite.Store

	mu                            sync.Mutex
	spawnConfigs                  []ports.SpawnConfig
	snapshots                     map[domain.SessionID]chatsvc.Snapshot
	lastPromptBySession           map[domain.SessionID]string
	turnByClientMessageID         map[string]string
	relays                        []standardRelay
	preflightRequests             []ports.ClearDevCheckPreflightRequest
	checkReleaseIDs               []string
	preflightErr                  error
	checkRequests                 []ports.ClearDevCheckRequest
	reviewBranchCandidates        map[string]string
	reviewerWorkspaces            map[string]string
	currentCandidate              string
	candidateSHAs                 []string
	reviewerRuns                  int
	reworkOnce                    bool
	reviewVerdicts                []string
	reviewFindingPath             string
	invalidReview                 bool
	reviewerSharesBuilder         bool
	builderWorkspace              string
	inspectionErr                 error
	inspectionBase                string
	inspectionPaths               []ports.ClearDevDiffPath
	checkResults                  []ports.ClearDevCheckResult
	checkErrors                   []error
	planReviewVerdict             string
	planReviewVerdicts            []string
	planReviews                   int
	complexPlanMutator            func(*core.ComplexEngineeringPlanResult)
	complexPlannerOutcome         string
	invalidCompilation            bool
	compilationInvalidLeft        int
	invalidStatus                 bool
	invalidBuilder                bool
	builderInvalidLeft            int
	builderRelayErr               error
	builderTurnFailures           int
	builderRetryAt                *time.Time
	builderSnapshotErr            error
	builderSnapshotSession        domain.SessionID
	reviewerSpawnErr              error
	benchmarkSequentialCandidates bool
	benchmarkCandidateOrdinal     int
}

func newStandardAgentHarness(store *sqlite.Store, rework bool) *standardAgentHarness {
	return &standardAgentHarness{
		store: store, snapshots: make(map[domain.SessionID]chatsvc.Snapshot),
		lastPromptBySession:    make(map[domain.SessionID]string),
		turnByClientMessageID:  make(map[string]string),
		reviewBranchCandidates: make(map[string]string), reviewerWorkspaces: make(map[string]string),
		candidateSHAs: []string{forty("b"), forty("c")}, reworkOnce: rework,
	}
}

func (h *standardAgentHarness) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if cfg.ProjectID == "" || cfg.CreationIdempotencyKey == "" {
		return domain.Session{}, 0, 0, errors.New("fake spawn requires project and idempotency key")
	}
	workspace := "/managed/" + strings.NewReplacer(":", "-", "/", "-").Replace(cfg.CreationIdempotencyKey)
	metadata := domain.SessionMetadata{
		Branch: cfg.Branch, WorkspacePath: workspace, WorkspaceRepoPath: "/tmp/" + string(cfg.ProjectID),
		DiffBaseRef: "refs/heads/main", DiffBaseSHA: forty("a"), Model: cfg.AgentConfig.Model,
	}
	if strings.Contains(cfg.Branch, "cleardev-review-") || strings.Contains(cfg.Branch, "cleardev-complex-review-") {
		if h.reviewerSpawnErr != nil {
			return domain.Session{}, 0, 0, h.reviewerSpawnErr
		}
		candidateSHA := h.reviewBranchCandidates[cfg.Branch]
		if candidateSHA == "" {
			return domain.Session{}, 0, 0, errors.New("Reviewer branch was not prepared")
		}
		metadata.DiffBaseSHA = candidateSHA
		if h.reviewerSharesBuilder {
			workspace = h.builderWorkspace
			metadata.WorkspacePath = workspace
		} else {
			h.reviewerWorkspaces[workspace] = candidateSHA
		}
	}
	if strings.Contains(cfg.Branch, "cleardev-builder-") || strings.Contains(cfg.Branch, "cleardev-complex-builder-") {
		h.builderWorkspace = workspace
	}
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	record, _, err := h.store.CreateSessionIdempotent(ctx, domain.SessionRecord{
		ProjectID: cfg.ProjectID, Kind: cfg.Kind, Harness: cfg.Harness,
		Mode: cfg.RequestedMode, PermissionMode: cfg.AgentConfig.Permissions,
		CreationIdempotencyKey:     cfg.CreationIdempotencyKey,
		CreationRequestFingerprint: coreDigest([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s", cfg.ProjectID, cfg.Kind, cfg.Harness, cfg.Branch, cfg.RequestedMode, cfg.AgentConfig.Permissions))),
		DisplayName:                cfg.DisplayName, Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		Metadata: metadata, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return domain.Session{}, 0, 0, err
	}
	h.spawnConfigs = append(h.spawnConfigs, cfg)
	return domain.Session{SessionRecord: record}, 0, 0, nil
}

func (h *standardAgentHarness) RelayChatTurnWithID(_ context.Context, sessionID domain.SessionID, prompt, clientMessageID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing := h.turnByClientMessageID[clientMessageID]; existing != "" {
		return existing, nil
	}
	builderPrompt := strings.Contains(prompt, `"kind":"BUILDER_RESULT"`)
	if builderPrompt && h.builderRelayErr != nil {
		return "", h.builderRelayErr
	}
	routePrompt := prompt
	if strings.Contains(prompt, parseCorrectionPromptPrefix) {
		if original := h.lastPromptBySession[sessionID]; original != "" {
			routePrompt = original
		}
	} else {
		h.lastPromptBySession[sessionID] = prompt
	}
	failedBuilderTurn := builderPrompt && h.builderTurnFailures > 0
	response := ""
	if !failedBuilderTurn {
		var err error
		response, err = h.responseForPromptLocked(routePrompt)
		if err != nil {
			return "", err
		}
	}
	turnID := fmt.Sprintf("turn-%03d", len(h.relays)+1)
	messageID := fmt.Sprintf("message-%03d", len(h.relays)+1)
	now := time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC).Add(time.Duration(len(h.relays)) * time.Second)
	snapshot := h.snapshots[sessionID]
	snapshot.SessionID = sessionID
	turn := domain.ConversationTurn{
		ID: turnID, HandledBySessionID: sessionID, State: domain.TurnStateCompleted, CompletedAt: &now,
	}
	if failedBuilderTurn {
		turn.State = domain.TurnStateFailed
		turn.Failure = &domain.ConversationFailure{
			Category:     domain.AgentFailureProviderUnavailable,
			ErrorSummary: safeAgentFailureSummary(domain.AgentFailureProviderUnavailable),
			Retryable:    true, RetryAt: h.builderRetryAt,
		}
		h.builderTurnFailures--
	}
	snapshot.Turns = append(snapshot.Turns, turn)
	snapshot.Messages = append(snapshot.Messages, domain.ConversationMessage{
		ID: "user-" + messageID, TurnID: turnID, Sequence: int64(len(snapshot.Messages) + 1),
		Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: prompt, ClientMessageID: clientMessageID,
	})
	if !failedBuilderTurn {
		snapshot.Messages = append(snapshot.Messages, domain.ConversationMessage{
			ID: messageID, TurnID: turnID, Sequence: int64(len(snapshot.Messages) + 1),
			Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Text: response,
		})
	}
	h.snapshots[sessionID] = snapshot
	h.turnByClientMessageID[clientMessageID] = turnID
	h.relays = append(h.relays, standardRelay{sessionID: sessionID, clientMessageID: clientMessageID, prompt: prompt, response: response})
	if builderPrompt {
		h.builderSnapshotSession = sessionID
	}
	return turnID, nil
}

func (h *standardAgentHarness) Interrupt(context.Context, domain.SessionID) error {
	return nil
}

func (h *standardAgentHarness) Snapshot(_ context.Context, sessionID domain.SessionID) (chatsvc.Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sessionID == h.builderSnapshotSession && h.builderSnapshotErr != nil {
		return chatsvc.Snapshot{}, h.builderSnapshotErr
	}
	snapshot, ok := h.snapshots[sessionID]
	if !ok {
		return chatsvc.Snapshot{SessionID: sessionID}, nil
	}
	snapshot.Turns = append([]domain.ConversationTurn(nil), snapshot.Turns...)
	snapshot.Messages = append([]domain.ConversationMessage(nil), snapshot.Messages...)
	return snapshot, nil
}

func (h *standardAgentHarness) responseForPromptLocked(prompt string) (string, error) {
	// Prompts can embed prior immutable protocol objects. The requested result
	// object is always the final protocol example, so route by its last kind.
	kinds := []string{"STATUS_REPORT", "BUILDER_RESULT", "COMPLEX_EXECUTION_REQUEST", "DISPATCH_REQUEST", "COMPLEX_PLAN_REVIEW", "PLAN_REVIEW", "COMPLEX_ENGINEERING_PLAN", "ENGINEERING_PLAN", "DIRECTION_CHANGE_REQUEST", "REQUIREMENT_COMPILATION", "REQUEST_PLANNING", "LOCAL_REVIEW"}
	lastKind, lastIndex := "", -1
	for _, kind := range kinds {
		for _, marker := range []string{`"kind":"` + kind + `"`, "kind=" + kind} {
			if index := strings.LastIndex(prompt, marker); index > lastIndex {
				lastKind, lastIndex = kind, index
			}
		}
	}
	if lastKind == "LOCAL_REVIEW" {
		example, err := promptJSONObject(prompt, "LOCAL_REVIEW")
		if err != nil {
			return "", err
		}
		var result core.LocalReviewResult
		if err := json.Unmarshal([]byte(example), &result); err != nil {
			return "", err
		}
		h.reviewerRuns++
		if h.invalidReview {
			return `{}`, nil
		}
		if h.reworkOnce && h.reviewerRuns == 1 {
			result.Verdict = "REWORK"
			result.ReasonCode = "REVIEW_CHANGES_REQUIRED"
			result.Summary = "邮箱实现仍有一个阻塞问题。"
			path := h.reviewFindingPath
			if path == "" {
				path = "src/email.js"
			}
			result.Findings = []core.ReviewFinding{{Severity: "BLOCKING", Path: path, Message: "需要修正无效输入处理。"}}
		}
		if index := h.reviewerRuns - 1; index < len(h.reviewVerdicts) {
			result.Verdict = h.reviewVerdicts[index]
			result.Findings = []core.ReviewFinding{}
			switch result.Verdict {
			case "REWORK":
				result.ReasonCode = "REVIEW_CHANGES_REQUIRED"
				path := h.reviewFindingPath
				if path == "" {
					path = "src/email.js"
				}
				result.Findings = []core.ReviewFinding{{Severity: "BLOCKING", Path: path, Message: "需要返工。"}}
			case "BLOCKED":
				result.ReasonCode = "REVIEW_BLOCKED"
			case "NEEDS_HUMAN":
				result.ReasonCode = "REVIEW_NEEDS_HUMAN"
			default:
				result.Verdict, result.ReasonCode = "PASS", "REVIEW_PASSED"
			}
		}
		encoded, err := core.MarshalAgentChosenResult(result)
		return string(encoded), err
	}
	for _, kind := range kinds[:len(kinds)-1] {
		if kind == lastKind {
			if kind == "COMPLEX_ENGINEERING_PLAN" {
				if h.complexPlannerOutcome != "" {
					return h.complexPlannerOutcome, nil
				}
				return complexEngineeringPlanHarnessResponse(prompt, h.complexPlanMutator)
			}
			if kind == "COMPLEX_PLAN_REVIEW" {
				return h.complexPlanReviewHarnessResponse(prompt)
			}
			response, err := promptJSONObject(prompt, kind)
			if err != nil {
				return "", err
			}
			if kind == "BUILDER_RESULT" {
				if h.invalidBuilder || h.builderInvalidLeft > 0 {
					if h.builderInvalidLeft > 0 {
						h.builderInvalidLeft--
					}
					return `{}`, nil
				}
				round := 0
				if value := promptLineValue(prompt, "round="); value != "" {
					parsed, convErr := strconv.Atoi(value)
					if convErr != nil {
						return "", convErr
					}
					round = parsed
				}
				if h.benchmarkSequentialCandidates {
					h.benchmarkCandidateOrdinal++
					h.currentCandidate = fmt.Sprintf("%040x", 100+h.benchmarkCandidateOrdinal)
				} else {
					if round < 0 || round >= len(h.candidateSHAs) {
						return "", fmt.Errorf("unexpected Builder round %d", round)
					}
					h.currentCandidate = h.candidateSHAs[round]
				}
			}
			if kind == "STATUS_REPORT" && h.invalidStatus {
				return `{}`, nil
			}
			if kind == "REQUIREMENT_COMPILATION" && (h.invalidCompilation || h.compilationInvalidLeft > 0) {
				if h.compilationInvalidLeft > 0 {
					h.compilationInvalidLeft--
				}
				return `{}`, nil
			}
			if kind == "PLAN_REVIEW" && h.planReviewVerdict != "" {
				var result core.PlanReviewResult
				if err := json.Unmarshal([]byte(response), &result); err != nil {
					return "", err
				}
				result.Verdict = h.planReviewVerdict
				switch result.Verdict {
				case "REPLAN":
					result.ReasonCode = "REPLAN_REQUIRED"
				case "NEEDS_HUMAN":
					result.ReasonCode = "HUMAN_DECISION_REQUIRED"
				default:
					result.Verdict, result.ReasonCode = "APPROVED", "PLAN_ACCEPTABLE"
				}
				encoded, err := core.MarshalAgentChosenResult(result)
				return string(encoded), err
			}
			return response, nil
		}
	}
	return "", errors.New("fake Chat received an unknown STANDARD prompt")
}

func complexEngineeringPlanHarnessResponse(prompt string, mutate func(*core.ComplexEngineeringPlanResult)) (string, error) {
	requirementIDs := promptCSVLine(prompt, "stableRequirementIds=")
	acceptanceIDs := promptCSVLine(prompt, "stableAcceptanceIds=")
	requestID := promptLineValue(prompt, "planningRequestId=")
	versionID := promptLineValue(prompt, "requirementVersionId=")
	versionSHA := promptLineValue(prompt, "requirementVersionSha256=")
	compilationSHA := promptLineValue(prompt, "compilationSha256=")
	if len(requirementIDs) == 0 || len(acceptanceIDs) == 0 || requestID == "" || versionID == "" || versionSHA == "" || compilationSHA == "" {
		return "", errors.New("complex engineering prompt lacks required bindings")
	}
	lastRequirement := requirementIDs[len(requirementIDs)-1]
	lastAcceptance := acceptanceIDs[len(acceptanceIDs)-1]
	result := core.ComplexEngineeringPlanResult{
		SchemaVersion:            core.ComplexProtocolVersion,
		Kind:                     "COMPLEX_ENGINEERING_PLAN",
		PlanningRequestID:        requestID,
		RequirementVersionID:     versionID,
		RequirementVersionSHA256: versionSHA,
		CompilationSHA256:        compilationSHA,
		TechnicalApproach:        "Use two repository-local modules and preserve the confirmed requirement boundaries.",
		Tasks: []core.ComplexPlanTask{
			{
				Key: "implement-core", Title: "Implement the confirmed core behavior", Objective: "Cover every confirmed requirement and acceptance scenario.",
				RequirementIDs: requirementIDs, AcceptanceIDs: acceptanceIDs,
				WritePaths: []string{"src/email.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{},
				ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"email-unit"}, DependencyKeys: []string{},
			},
			{
				Key: "complete-summary", Title: "Complete the observable summary", Objective: "Finish the final mapped behavior after the core task.",
				RequirementIDs: []string{lastRequirement}, AcceptanceIDs: []string{lastAcceptance},
				WritePaths: []string{"src/summary.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{},
				ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"summary-unit"}, DependencyKeys: []string{"implement-core"},
			},
		},
		IntegrationCheckIDs: []string{"all-tests"},
		ParallelSuggestion: core.ComplexParallelSuggestion{
			RecommendedBuilderCount: 1,
			Reason:                  "The second task depends on the first, so another builder would add coordination without concurrency.",
		},
		Risks: []string{},
	}
	if mutate != nil {
		mutate(&result)
	}
	encoded, err := core.MarshalAgentChosenResult(result)
	return string(encoded), err
}

func (h *standardAgentHarness) complexPlanReviewHarnessResponse(prompt string) (string, error) {
	requestID := promptLineValue(prompt, "reviewRequestId=")
	planID := promptLineValue(prompt, "planId=")
	planSHA := promptLineValue(prompt, "planSha256=")
	if requestID == "" || planID == "" || planSHA == "" {
		return "", errors.New("complex plan review prompt lacks required bindings")
	}
	h.planReviews++
	verdict := h.planReviewVerdict
	if index := h.planReviews - 1; index < len(h.planReviewVerdicts) {
		verdict = h.planReviewVerdicts[index]
	}
	if verdict == "" {
		verdict = "APPROVED"
	}
	result := core.ComplexPlanReviewResult{
		SchemaVersion:   core.ComplexProtocolVersion,
		Kind:            "COMPLEX_PLAN_REVIEW",
		ReviewRequestID: requestID,
		PlanID:          planID,
		PlanSHA256:      planSHA,
		Verdict:         verdict,
	}
	switch verdict {
	case "REPLAN":
		result.ReasonCode = "REPLAN_REQUIRED"
		result.Summary = "The candidate plan needs a correctable task-boundary change."
		result.Findings = []core.ComplexReviewFinding{{
			Code: "SPLIT_TASKS", Message: "任务边界需要再拆一次。", RequirementIDs: []string{}, TaskKeys: []string{},
		}}
	case "NEEDS_HUMAN":
		result.ReasonCode = "HUMAN_DECISION_REQUIRED"
		result.Summary = "A human decision is required before planning can continue."
		result.Findings = []core.ComplexReviewFinding{{
			Code: "HUMAN_DECISION", Message: "需要人工决定。", RequirementIDs: []string{}, TaskKeys: []string{},
		}}
	default:
		result.Verdict = "APPROVED"
		result.ReasonCode = "PLAN_ACCEPTABLE"
		result.Summary = "The plan covers the confirmed requirements with valid boundaries and checks."
		result.Findings = []core.ComplexReviewFinding{}
	}
	encoded, err := core.MarshalAgentChosenResult(result)
	return string(encoded), err
}

func promptCSVLine(prompt, prefix string) []string {
	line := promptLineValue(prompt, prefix)
	parts := strings.Split(line, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func promptLineValue(prompt, prefix string) string {
	index := strings.Index(prompt, prefix)
	if index < 0 {
		return ""
	}
	line := prompt[index+len(prefix):]
	if newline := strings.IndexByte(line, '\n'); newline >= 0 {
		line = line[:newline]
	}
	return strings.TrimSpace(line)
}

func promptJSONObject(prompt, kind string) (string, error) {
	needle := `"kind":"` + kind + `"`
	index := strings.LastIndex(prompt, needle)
	if index < 0 {
		return "", fmt.Errorf("prompt has no %s object", kind)
	}
	start := strings.LastIndex(prompt[:index], "{")
	if start < 0 {
		return "", fmt.Errorf("prompt has no opening object for %s", kind)
	}
	depth, quoted, escaped := 0, false, false
	for offset := start; offset < len(prompt); offset++ {
		value := prompt[offset]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if value == '\\' {
				escaped = true
				continue
			}
			if value == '"' {
				quoted = false
			}
			continue
		}
		switch value {
		case '"':
			quoted = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return prompt[start : offset+1], nil
			}
		}
	}
	return "", fmt.Errorf("prompt has an unterminated %s object", kind)
}

func (h *standardAgentHarness) InspectCandidate(_ context.Context, workspacePath, baseSHA string) (ports.ClearDevCandidateInspection, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if candidateSHA := h.reviewerWorkspaces[workspacePath]; candidateSHA != "" {
		if baseSHA != candidateSHA {
			return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
		}
		return ports.ClearDevCandidateInspection{BaseSHA: candidateSHA, CandidateSHA: candidateSHA, Paths: []ports.ClearDevDiffPath{}}, nil
	}
	if !strings.Contains(workspacePath, "builder") {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	if h.benchmarkSequentialCandidates && (h.currentCandidate == "" || h.currentCandidate == baseSHA) {
		return ports.ClearDevCandidateInspection{BaseSHA: baseSHA, CandidateSHA: baseSHA, Paths: []ports.ClearDevDiffPath{}}, nil
	}
	if h.currentCandidate == "" {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	if !h.benchmarkSequentialCandidates && baseSHA != forty("a") {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	if h.inspectionErr != nil {
		return ports.ClearDevCandidateInspection{}, h.inspectionErr
	}
	inspectionBase := baseSHA
	if h.inspectionBase != "" {
		inspectionBase = h.inspectionBase
	}
	paths := h.inspectionPaths
	if paths == nil {
		paths = []ports.ClearDevDiffPath{{Status: "M", Path: "src/email.js"}, {Status: "A", Path: "test/email.test.js"}}
	}
	return ports.ClearDevCandidateInspection{
		BaseSHA: inspectionBase, CandidateSHA: h.currentCandidate, Paths: append([]ports.ClearDevDiffPath(nil), paths...),
	}, nil
}

func (h *standardAgentHarness) PrepareBaseWorkspace(_ context.Context, workspacePath, expectedHeadSHA, newBaseSHA string) error {
	return nil
}

func (h *standardAgentHarness) ComposeCandidates(_ context.Context, request ports.ClearDevComposeRequest) (ports.ClearDevComposeResult, error) {
	return ports.ClearDevComposeResult{}, ports.ErrClearDevCandidateInvalid
}

func (h *standardAgentHarness) PrepareReviewBranch(_ context.Context, workspacePath, branch, candidateSHA string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !strings.Contains(workspacePath, "builder") || candidateSHA == "" {
		return ports.ErrClearDevCandidateInvalid
	}
	h.reviewBranchCandidates[branch] = candidateSHA
	return nil
}

func (h *standardAgentHarness) RunCandidateCheck(_ context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkRequests = append(h.checkRequests, request)
	index := len(h.checkRequests) - 1
	if index < len(h.checkResults) {
		var err error
		if index < len(h.checkErrors) {
			err = h.checkErrors[index]
		}
		return h.checkResults[index], err
	}
	return ports.ClearDevCheckResult{
		Outcome: ports.ClearDevCheckPass, ImageID: "sha256:standard-test-image", ExitCode: 0,
		OutputSummary: "pass", OutputSHA256: coreDigest([]byte("pass")),
	}, nil
}

func (h *standardAgentHarness) PrepareCandidateChecks(_ context.Context, request ports.ClearDevCheckPreflightRequest) (ports.ClearDevCheckEnvironment, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.preflightRequests = append(h.preflightRequests, request)
	if h.preflightErr != nil {
		return ports.ClearDevCheckEnvironment{}, h.preflightErr
	}
	return ports.ClearDevCheckEnvironment{CandidateSHA: request.CandidateSHA, Image: request.Image, ImageID: "sha256:standard-test-image", CheckEnvironmentID: "standard-test-environment"}, nil
}

func (h *standardAgentHarness) ReleaseCandidateChecks(_ context.Context, runID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkReleaseIDs = append(h.checkReleaseIDs, runID)
	return nil
}

type interruptingStandardStore struct {
	StandardFactStore
	kind   core.AgentStepKind
	cancel context.CancelFunc
	once   sync.Once
}

func (s *interruptingStandardStore) SettleClearDevStandardAgentStep(ctx context.Context, step core.AgentStep) (bool, error) {
	changed, err := s.StandardFactStore.SettleClearDevStandardAgentStep(ctx, step)
	if err == nil && step.Kind == s.kind && step.SendStatus == core.AgentStepSendStatusSettled {
		s.once.Do(s.cancel)
	}
	return changed, err
}

type interruptingPendingDispatchStore struct {
	StandardFactStore
	cancel context.CancelFunc
	once   sync.Once
}

func (s *interruptingPendingDispatchStore) CreateClearDevPendingDispatch(ctx context.Context, command core.CreatePendingDispatchCommand) (core.Dispatch, bool, error) {
	dispatch, created, err := s.StandardFactStore.CreateClearDevPendingDispatch(ctx, command)
	if err == nil && created {
		s.once.Do(s.cancel)
	}
	return dispatch, created, err
}

type interruptingReviewerBindingFailureStore struct {
	StandardFactStore
	cancel context.CancelFunc
	once   sync.Once
}

func (s *interruptingReviewerBindingFailureStore) FailClearDevRoleBinding(ctx context.Context, command core.FailRoleBindingCommand) (bool, error) {
	changed, err := s.StandardFactStore.FailClearDevRoleBinding(ctx, command)
	if err == nil && changed && command.ReasonCode == core.ReasonCode("REVIEWER_UNAVAILABLE") {
		s.once.Do(s.cancel)
	}
	return changed, err
}

type blockingStandardCompletionStore struct{ StandardFactStore }

func (blockingStandardCompletionStore) CompleteClearDevStandardTask(context.Context, core.CompleteStandardTaskCommand) error {
	return errors.New("test stops dedicated STANDARD completion")
}

type corruptStandardFactStore struct {
	StandardFactStore
	fact string
}

func (s corruptStandardFactStore) CreateClearDevEngineeringPlan(ctx context.Context, plan core.EngineeringPlan) error {
	if s.fact == "plan-hash" {
		plan.PlanSHA256 = strings.Repeat("0", 64)
	}
	return s.StandardFactStore.CreateClearDevEngineeringPlan(ctx, plan)
}

func (s corruptStandardFactStore) CreateClearDevPendingDispatch(ctx context.Context, command core.CreatePendingDispatchCommand) (core.Dispatch, bool, error) {
	if s.fact == "dispatch-hash" {
		command.Dispatch.ExecutionPackageSHA256 = strings.Repeat("0", 64)
	}
	return s.StandardFactStore.CreateClearDevPendingDispatch(ctx, command)
}

func (s corruptStandardFactStore) CreateClearDevCandidateCheckRun(ctx context.Context, run core.CandidateCheckRun) (core.CandidateCheckRun, bool, error) {
	if s.fact == "check-spec" {
		run.CheckSpecSHA256 = strings.Repeat("0", 64)
	}
	return s.StandardFactStore.CreateClearDevCandidateCheckRun(ctx, run)
}

func (s corruptStandardFactStore) CreateClearDevLocalReview(ctx context.Context, review core.LocalReview) (core.LocalReview, bool, error) {
	if s.fact == "review-hash" {
		review.ReviewPacketSHA256 = strings.Repeat("0", 64)
	}
	return s.StandardFactStore.CreateClearDevLocalReview(ctx, review)
}

type forgedStandardFailureStore struct{ StandardFactStore }

func (s forgedStandardFailureStore) ApplyClearDevStandardFailure(ctx context.Context, command core.StandardFailureCommand) error {
	command.InfrastructureFailure = true
	command.ReasonCode = core.ReasonCode("CHECKER_UNAVAILABLE")
	return s.StandardFactStore.ApplyClearDevStandardFailure(ctx, command)
}

func seedStandardRequirement(t *testing.T, store *sqlite.Store, ids *standardTestIDs, clock func() time.Time, confirm bool) string {
	t.Helper()
	ctx := context.Background()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "s02-project", Path: "/tmp/s02-project", Kind: domain.ProjectKindSingleRepo, RegisteredAt: clock(),
	}); err != nil {
		t.Fatal(err)
	}
	seed := New(Deps{Facts: store, AO: store, Human: allowStandardHuman{}, NewID: ids.New, Clock: clock})
	view, err := seed.CreateRequirement(ctx, CreateRequirementInput{
		AOProjectID: "s02-project", Name: "S02 邮箱规范化", RequirementText: standardFixtureRequirement,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !confirm {
		return view.Requirement.ID
	}
	versionID := view.RequirementVersions[0].ID
	if err := seed.SubmitRequirementForConfirmation(ctx, versionID); err != nil {
		t.Fatal(err)
	}
	if err := seed.ConfirmRequirementVersion(ctx, versionID); err != nil {
		t.Fatal(err)
	}
	return view.Requirement.ID
}

func seedConfirmedStandardRequirement(t *testing.T, store *sqlite.Store, ids *standardTestIDs, clock func() time.Time) string {
	t.Helper()
	return seedStandardRequirement(t, store, ids, clock, true)
}

func standardTestService(store *sqlite.Store, facts StandardFactStore, harness *standardAgentHarness, ids *standardTestIDs, clock func() time.Time, background context.Context) *Service {
	return New(Deps{
		ControlledPreflightChecker: alwaysPassControlledPreflight{},
		ComplexFacts:               store,
		ComplexExecutionFacts:      store,
		DirectionFacts:             store,
		HumanDecisions:             store,
		ProgressExplanations:       store,
		Workspace:                  gitWorkspaceObserver{},
		RecoverAgentSession:        func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },

		Facts: store, StandardFacts: facts, AO: store, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		AgentAttempts: store, ParseCorrections: store, ControlledPreflights: store,
		BackgroundContext: background, RunBackground: func(run func()) { run() },
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})
}

func newStandardIntegrationFixture(t *testing.T, rework bool) (*sqlite.Store, *standardAgentHarness, *standardTestIDs, func() time.Time, string) {
	t.Helper()
	store := sqlitetest.MustOpen(t)
	ids := &standardTestIDs{}
	var clockMu sync.Mutex
	tick := 0
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		tick++
		return time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC).Add(time.Duration(tick) * time.Second)
	}
	requirementID := seedConfirmedStandardRequirement(t, store, ids, clock)
	return store, newStandardAgentHarness(store, rework), ids, clock, requirementID
}

func assertCompletedStandardFlow(t *testing.T, service *Service, requirementID string) RequirementView {
	t.Helper()
	view, err := service.GetRequirement(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if view.OverallProgress.Phase != core.OverallPhaseCompleted || view.OverallProgress.Attention != core.OverallAttentionNone {
		t.Fatalf("overall progress = %#v; STANDARD flow = %#v", view.OverallProgress, view.StandardFlow)
	}
	if len(view.DevelopmentTasks) != 1 || view.DevelopmentTasks[0].DevelopmentTask.Status != core.DevelopmentTaskStatusDone {
		t.Fatalf("development tasks = %#v", view.DevelopmentTasks)
	}
	task := view.DevelopmentTasks[0]
	if len(task.RequiredChecks) != 1 || task.RequiredChecks[0].Name != "email-unit" || task.RequiredChecks[0].Kind != "REQUIRED_CHECK" {
		t.Fatalf("completed task required checks = %#v, want only the frozen email-unit check", task.RequiredChecks)
	}
	if len(task.MissingEvidence) != 0 {
		t.Fatalf("completed task still reports missing evidence: %#v", task.MissingEvidence)
	}
	if view.StandardFlow == nil || len(view.StandardFlow.EngineeringPlans) != 1 || len(view.StandardFlow.PlanReviews) != 1 || len(view.StandardFlow.Dispatches) != 1 {
		t.Fatalf("STANDARD facts = %#v", view.StandardFlow)
	}
	return view
}

func TestStandardStartRejectsInvalidAndDuplicateIntentBeforeAnySessionOrTask(t *testing.T) {
	t.Run("unconfirmed", func(t *testing.T) {
		store := sqlitetest.MustOpen(t)
		ids := &standardTestIDs{}
		clock := func() time.Time { return time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC) }
		requirementID := seedStandardRequirement(t, store, ids, clock, false)
		harness := newStandardAgentHarness(store, false)
		service := standardTestService(store, store, harness, ids, clock, context.Background())
		if _, err := service.StartStandardFlow(context.Background(), requirementID); err == nil {
			t.Fatal("unconfirmed requirement started STANDARD")
		}
		assertNoStandardSessionsOrTasks(t, store, harness, requirementID)
	})

	t.Run("cancelled", func(t *testing.T) {
		store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
		control := New(Deps{Facts: store, AO: store, Human: allowStandardHuman{}, NewID: ids.New, Clock: clock})
		if err := control.CancelRequirement(context.Background(), requirementID, "cancel test requirement"); err != nil {
			t.Fatal(err)
		}
		service := standardTestService(store, store, harness, ids, clock, context.Background())
		if _, err := service.StartStandardFlow(context.Background(), requirementID); err == nil {
			t.Fatal("cancelled requirement started STANDARD")
		}
		assertNoStandardSessionsOrTasks(t, store, harness, requirementID)
	})

	t.Run("duplicate intent", func(t *testing.T) {
		store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
		service := standardTestService(store, store, harness, ids, clock, context.Background())
		service.runBackground = func(func()) {}
		if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
			t.Fatal(err)
		}
		if _, err := service.StartStandardFlow(context.Background(), requirementID); err == nil {
			t.Fatal("duplicate STANDARD intent succeeded")
		}
		assertNoStandardSessionsOrTasks(t, store, harness, requirementID)
		flow, ok, err := store.GetClearDevStandardFlow(context.Background(), requirementID)
		if err != nil || !ok || len(flow.RoleBindings) != 1 || flow.RoleBindings[0].Status != core.RoleBindingStatusRequested {
			t.Fatalf("saved start intent = %#v, ok=%v err=%v", flow, ok, err)
		}
	})
}

func assertNoStandardSessionsOrTasks(t *testing.T, store *sqlite.Store, harness *standardAgentHarness, requirementID string) {
	t.Helper()
	snapshot, ok, err := store.GetClearDevRequirement(context.Background(), requirementID)
	if err != nil || !ok {
		t.Fatalf("load requirement: ok=%v err=%v", ok, err)
	}
	harness.mu.Lock()
	spawnCount := len(harness.spawnConfigs)
	harness.mu.Unlock()
	if spawnCount != 0 || len(snapshot.DevelopmentTasks) != 0 {
		t.Fatalf("rejected start created sessions/tasks: sessions=%d tasks=%d", spawnCount, len(snapshot.DevelopmentTasks))
	}
}

func TestStandardFlowCompletesWithFourExactSessionsAndIdempotentResume(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	service := standardTestService(store, store, harness, ids, clock, context.Background())
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	if err := service.runStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatalf("continue successful STANDARD flow: %T %#v", err, err)
	}
	view := assertCompletedStandardFlow(t, service, requirementID)

	flow := view.StandardFlow
	if len(flow.RoleBindings) != 4 {
		t.Fatalf("role bindings = %d, want four", len(flow.RoleBindings))
	}
	sessionIDs := make(map[string]struct{}, 4)
	for _, binding := range flow.RoleBindings {
		if binding.Status != core.RoleBindingStatusBound && binding.Status != core.RoleBindingStatusEnded {
			t.Fatalf("role binding = %#v", binding)
		}
		if binding.AOSessionID == "" {
			t.Fatalf("role %s has no AO session", binding.Role)
		}
		sessionIDs[binding.AOSessionID] = struct{}{}
	}
	if len(sessionIDs) != 4 {
		t.Fatalf("distinct sessions = %v", sessionIDs)
	}
	harness.mu.Lock()
	if len(harness.spawnConfigs) != 4 {
		t.Fatalf("spawn configs = %d, want four", len(harness.spawnConfigs))
	}
	if len(harness.preflightRequests) != 1 {
		t.Fatalf("check preflights = %d, want one before Builder dispatch", len(harness.preflightRequests))
	}
	preflight := harness.preflightRequests[0]
	if preflight.CandidateSHA != forty("a") || preflight.RunID == "" || preflight.Image != core.StandardCandidateCheckImage || len(preflight.Argv) != 2 || preflight.MemoryBytes != standardCheckMemoryBytes || preflight.PidsLimit != standardCheckPidsLimit || preflight.Timeout != checkPreflightTimeout {
		t.Fatalf("check preflight = %#v", preflight)
	}
	for _, request := range harness.checkRequests {
		if request.RunID == "" || request.MemoryBytes != standardCheckMemoryBytes || request.PidsLimit != standardCheckPidsLimit || request.OutputLimit != standardCheckOutputLimit || request.Timeout != time.Minute {
			t.Fatalf("formal check has no durable RunID: %#v", request)
		}
	}
	wantRelease := flow.Dispatches[0].ID + ":check-preflight"
	released := false
	for _, runID := range harness.checkReleaseIDs {
		released = released || runID == wantRelease
	}
	if !released {
		t.Fatalf("completed STANDARD flow did not release %q: %#v", wantRelease, harness.checkReleaseIDs)
	}
	for _, cfg := range harness.spawnConfigs {
		if cfg.Harness != domain.HarnessCodex || cfg.RequestedMode != domain.SessionModeChat || cfg.AgentConfig.Permissions != domain.PermissionModeAuto || cfg.Prompt != "" || cfg.CreationIdempotencyKey == "" {
			t.Fatalf("spawn config = %#v", cfg)
		}
	}
	relaysBefore, spawnsBefore := len(harness.relays), len(harness.spawnConfigs)
	harness.mu.Unlock()
	for _, step := range flow.AgentSteps {
		if step.SendStatus != core.AgentStepSendStatusSettled || step.TurnID == "" || step.FinalMessageID == "" || len(step.MessageSHA256) != 64 {
			t.Fatalf("Agent step = %#v", step)
		}
	}
	if err := service.ResumeStandardFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err == nil {
		t.Fatal("duplicate STANDARD start succeeded")
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if len(harness.relays) != relaysBefore || len(harness.spawnConfigs) != spawnsBefore {
		t.Fatalf("completed resume/duplicate start changed Agent facts: relays %d->%d, spawns %d->%d", relaysBefore, len(harness.relays), spawnsBefore, len(harness.spawnConfigs))
	}
}

func TestStandardCheckPreflightFailsBeforeBuilderTaskAndCandidateFacts(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	harness.preflightErr = errors.New("offline dependency cache is unavailable")
	service := standardTestService(store, store, harness, ids, clock, context.Background())
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	if err := service.runStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatalf("run stopped preflight: %v", err)
	}
	flow, ok, err := store.GetClearDevStandardFlow(context.Background(), requirementID)
	if err != nil || !ok || len(flow.Dispatches) != 1 {
		t.Fatalf("preflight flow ok=%v err=%v flow=%#v", ok, err, flow)
	}
	dispatch := flow.Dispatches[0]
	if dispatch.Status != core.DispatchStatusFailed || dispatch.ReasonCode != core.ReasonCode("CHECKER_UNAVAILABLE") {
		t.Fatalf("preflight dispatch = %#v", dispatch)
	}
	snapshot, ok, err := store.GetClearDevRequirement(context.Background(), requirementID)
	if err != nil || !ok || len(snapshot.DevelopmentTasks) != 0 || len(snapshot.Candidates) != 0 {
		t.Fatalf("preflight left partial task/candidate facts: ok=%v err=%v tasks=%d candidates=%d", ok, err, len(snapshot.DevelopmentTasks), len(snapshot.Candidates))
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if len(harness.preflightRequests) != 1 || len(harness.checkRequests) != 0 {
		t.Fatalf("preflight requests=%d formal checks=%d", len(harness.preflightRequests), len(harness.checkRequests))
	}
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) {
			t.Fatalf("Builder received a task after failed check preflight: %#v", relay)
		}
	}
}

func TestStandardBuilderFailedStepEscalatesCurrentTask(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		reason core.ReasonCode
		setup  func(*standardAgentHarness)
	}{
		{
			name:   "invalid result",
			reason: "BUILDER_RESULT_INVALID",
			setup:  func(h *standardAgentHarness) { h.invalidBuilder = true },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
			testCase.setup(harness)
			service := standardTestService(store, store, harness, ids, clock, context.Background())
			if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			view, err := service.GetRequirement(context.Background(), requirementID)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.DevelopmentTasks) != 1 || view.DevelopmentTasks[0].DevelopmentTask.Status != core.DevelopmentTaskStatusNeedsHuman {
				t.Fatalf("failed Builder result left task state = %#v, want NEEDS_HUMAN", view.DevelopmentTasks)
			}
			if view.StandardFlow == nil {
				t.Fatal("failed Builder result lost STANDARD facts")
			}
			var failed bool
			for _, step := range view.StandardFlow.AgentSteps {
				if step.Kind == core.AgentStepBuilderResult && step.SendStatus == core.AgentStepSendStatusFailed && step.ReasonCode == testCase.reason {
					failed = true
				}
			}
			if !failed {
				t.Fatalf("missing persisted failed Builder step with reason %s: %#v", testCase.reason, view.StandardFlow.AgentSteps)
			}
			var escalated bool
			for _, event := range view.Events {
				if event.Action == core.ActionEscalateDevelopmentTask && event.Outcome == core.EventAccepted && event.Reason == testCase.reason {
					escalated = true
				}
			}
			if !escalated {
				t.Fatalf("missing accepted NEEDS_HUMAN event with reason %s: %#v", testCase.reason, view.Events)
			}
		})
	}
}

func TestStandardBuilderProviderFailureRecoversWithSecondAttempt(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	harness.builderRelayErr = ports.ErrChatProviderUnavailable
	service := standardTestService(store, store, harness, ids, clock, context.Background())
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	before, err := service.GetRequirement(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.DevelopmentTasks) != 1 || before.DevelopmentTasks[0].DevelopmentTask.Status != core.DevelopmentTaskStatusRunning {
		t.Fatalf("first provider failure changed task terminally: %#v", before.DevelopmentTasks)
	}

	harness.builderRelayErr = nil
	restarted := standardTestService(store, store, harness, ids, clock, context.Background())
	preflight := &recoveryPreflight{err: ports.ErrChatAuthRequired}
	restarted.preflightChecker = preflight
	if err := restarted.ResumeStandardFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	waiting, err := restarted.GetRequirement(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	var waitingBuilderAttempts int
	for _, attempt := range waiting.AgentStepAttempts {
		if attempt.StepKind == core.AgentStepBuilderResult {
			waitingBuilderAttempts++
		}
	}
	if waitingBuilderAttempts != 1 {
		t.Fatalf("login preflight created recovery attempt: %#v", waiting.AgentStepAttempts)
	}

	preflight.setError(nil)
	if err := restarted.ResumeStandardFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := assertCompletedStandardFlow(t, restarted, requirementID)
	var builderAttempts []core.AgentStepAttemptView
	for _, attempt := range after.AgentStepAttempts {
		if attempt.StepKind == core.AgentStepBuilderResult {
			builderAttempts = append(builderAttempts, attempt)
		}
	}
	if len(builderAttempts) != 2 || builderAttempts[0].AttemptNumber != 1 || builderAttempts[1].AttemptNumber != 2 ||
		builderAttempts[1].TriggerFailureEventID == "" || builderAttempts[1].ClientMessageID == builderAttempts[0].ClientMessageID {
		t.Fatalf("builder recovery attempts=%#v", builderAttempts)
	}
}

func TestStandardBuilderSentTurnFailureResendsSecondAttemptOnce(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	harness.builderTurnFailures = 1
	service := standardTestService(store, store, harness, ids, clock, context.Background())
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	before, err := service.GetRequirement(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.DevelopmentTasks) != 1 || before.DevelopmentTasks[0].DevelopmentTask.Status != core.DevelopmentTaskStatusRunning {
		t.Fatalf("first failed turn changed task terminally: %#v", before.DevelopmentTasks)
	}
	if before.StandardFlow == nil {
		t.Fatal("first failed turn lost STANDARD facts")
	}
	var builderStep core.AgentStep
	for _, step := range before.StandardFlow.AgentSteps {
		if step.Kind == core.AgentStepBuilderResult {
			builderStep = step
		}
	}
	if builderStep.ID == "" || builderStep.SendStatus != core.AgentStepSendStatusSent {
		t.Fatalf("logical Builder step=%#v, want durable SENT", builderStep)
	}
	var firstAttempts []core.AgentStepAttemptView
	for _, attempt := range before.AgentStepAttempts {
		if attempt.StepKind == core.AgentStepBuilderResult {
			firstAttempts = append(firstAttempts, attempt)
		}
	}
	if len(firstAttempts) != 1 || firstAttempts[0].SendStatus != core.AgentAttemptFailed || !firstAttempts[0].Retryable {
		t.Fatalf("first Builder attempt=%#v", firstAttempts)
	}

	restarted := standardTestService(store, store, harness, ids, clock, context.Background())
	if err := restarted.ResumeStandardFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := assertCompletedStandardFlow(t, restarted, requirementID)
	var builderAttempts []core.AgentStepAttemptView
	for _, attempt := range after.AgentStepAttempts {
		if attempt.StepKind == core.AgentStepBuilderResult {
			builderAttempts = append(builderAttempts, attempt)
		}
	}
	if len(builderAttempts) != 2 || builderAttempts[0].AttemptNumber != 1 || builderAttempts[1].AttemptNumber != 2 ||
		builderAttempts[1].SendStatus != core.AgentAttemptCompleted || builderAttempts[1].TriggerFailureEventID == "" {
		t.Fatalf("builder recovery attempts=%#v", builderAttempts)
	}

	harness.mu.Lock()
	defer harness.mu.Unlock()
	var builderRelays []standardRelay
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) {
			builderRelays = append(builderRelays, relay)
		}
	}
	if len(builderRelays) != 2 || builderRelays[0].sessionID != builderRelays[1].sessionID ||
		builderRelays[0].clientMessageID == builderRelays[1].clientMessageID ||
		builderRelays[1].clientMessageID != builderStep.ClientMessageID+":attempt:2" {
		t.Fatalf("builder relays=%#v", builderRelays)
	}
}

func TestStandardBuilderJSONCorrectsOnce(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	harness.builderInvalidLeft = 1
	service := standardTestService(store, store, harness, ids, clock, context.Background())
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	assertCompletedStandardFlow(t, service, requirementID)
	var sawCorrection bool
	harness.mu.Lock()
	defer harness.mu.Unlock()
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, parseCorrectionPromptPrefix) {
			sawCorrection = true
			if !strings.Contains(relay.prompt, "Return exactly one complete valid JSON object") {
				t.Fatalf("correction prompt missing rewrite instruction: %q", relay.prompt)
			}
		}
	}
	if !sawCorrection {
		t.Fatal("Builder JSON correction was not sent")
	}
}

func TestGenericCompletionRejectsAcceptedStandardTask(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	service := standardTestService(store, blockingStandardCompletionStore{StandardFactStore: store}, harness, ids, clock, context.Background())
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	before, err := service.GetRequirement(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.DevelopmentTasks) != 1 || before.DevelopmentTasks[0].DevelopmentTask.Status != core.DevelopmentTaskStatusReview {
		t.Fatalf("dedicated completion stop left task = %#v, want REVIEW", before.DevelopmentTasks)
	}
	taskID := before.DevelopmentTasks[0].DevelopmentTask.ID
	if err := service.CompleteDevelopmentTask(context.Background(), taskID); err == nil {
		t.Fatal("generic completion accepted an accepted-dispatch STANDARD task")
	}
	after, err := service.GetRequirement(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if after.DevelopmentTasks[0].DevelopmentTask.Status != core.DevelopmentTaskStatusReview {
		t.Fatalf("generic completion changed accepted STANDARD task to %s", after.DevelopmentTasks[0].DevelopmentTask.Status)
	}
	var rejected bool
	for _, event := range after.Events {
		if event.SubjectID == taskID && event.Action == core.ActionCompleteDevelopmentTask &&
			event.Outcome == core.EventRejected && event.Reason == core.ReasonPreconditionNotMet {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("generic STANDARD completion did not persist its REJECTED event: %#v", after.Events)
	}
}

func TestStandardReviewerBindingFailureResumesToBlockedWithoutNewReviewer(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	harness.reviewerSpawnErr = errors.New("Reviewer unavailable")
	interrupted, cancel := context.WithCancel(context.Background())
	facts := &interruptingReviewerBindingFailureStore{StandardFactStore: store, cancel: cancel}
	service := standardTestService(store, facts, harness, ids, clock, interrupted)
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}

	before, err := service.GetRequirement(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.DevelopmentTasks) != 1 || before.DevelopmentTasks[0].DevelopmentTask.Status != core.DevelopmentTaskStatusReview || before.StandardFlow == nil {
		t.Fatalf("interrupted Reviewer spawn facts = tasks:%#v flow:%#v", before.DevelopmentTasks, before.StandardFlow)
	}
	if len(before.DevelopmentTasks[0].Candidates) != 1 || len(before.StandardFlow.LocalReviews) != 0 {
		t.Fatalf("interrupted Reviewer spawn changed candidate/review facts: tasks:%#v reviews:%#v", before.DevelopmentTasks, before.StandardFlow.LocalReviews)
	}
	var reviewerID string
	for _, binding := range before.StandardFlow.RoleBindings {
		if binding.Role == core.StandardRoleReviewer {
			if binding.Status != core.RoleBindingStatusFailed || binding.ReasonCode != core.ReasonCode("REVIEWER_UNAVAILABLE") {
				t.Fatalf("persisted Reviewer failure = %#v", binding)
			}
			reviewerID = binding.ID
		}
	}
	if reviewerID == "" {
		t.Fatal("interrupted Reviewer spawn did not persist a failed binding")
	}

	resumed := standardTestService(store, store, harness, ids, clock, context.Background())
	if err := resumed.ResumeStandardFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := resumed.GetRequirement(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.DevelopmentTasks) != 1 || after.DevelopmentTasks[0].DevelopmentTask.Status != core.DevelopmentTaskStatusBlocked {
		t.Fatalf("resumed Reviewer failure task = %#v, want BLOCKED", after.DevelopmentTasks)
	}
	if after.StandardFlow == nil || len(after.DevelopmentTasks[0].Candidates) != 1 || len(after.StandardFlow.LocalReviews) != 0 {
		t.Fatalf("resume created a new candidate or local review: tasks:%#v reviews:%#v", after.DevelopmentTasks, after.StandardFlow)
	}
	var reviewerCount, blockedEvents int
	for _, binding := range after.StandardFlow.RoleBindings {
		if binding.Role == core.StandardRoleReviewer {
			reviewerCount++
			if binding.ID != reviewerID || binding.Status != core.RoleBindingStatusFailed || binding.ReasonCode != core.ReasonCode("REVIEWER_UNAVAILABLE") {
				t.Fatalf("resume changed persisted Reviewer failure: %#v", binding)
			}
		}
	}
	for _, event := range after.Events {
		if event.Action == core.ActionBlockDevelopmentTask && event.Outcome == core.EventAccepted && event.Reason == core.ReasonCode("REVIEWER_UNAVAILABLE") {
			blockedEvents++
		}
	}
	if reviewerCount != 1 || blockedEvents != 1 {
		t.Fatalf("resume reviewer bindings/events = %d/%d, want 1/1", reviewerCount, blockedEvents)
	}
}

func TestStandardPlanReviewStopsBeforeDispatchWithoutApproval(t *testing.T) {
	for _, verdict := range []string{"REPLAN", "NEEDS_HUMAN"} {
		t.Run(verdict, func(t *testing.T) {
			store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
			harness.planReviewVerdict = verdict
			service := standardTestService(store, store, harness, ids, clock, context.Background())
			if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			if err := service.runStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			view, err := service.GetRequirement(context.Background(), requirementID)
			if err != nil {
				t.Fatal(err)
			}
			if view.StandardFlow == nil || len(view.StandardFlow.PlanReviews) != 1 || string(view.StandardFlow.PlanReviews[0].Verdict) != verdict ||
				len(view.StandardFlow.Dispatches) != 0 || len(view.DevelopmentTasks) != 0 {
				t.Fatalf("non-approved plan advanced: %#v", view.StandardFlow)
			}
			wantAttention, wantReason := StandardFlowAttentionReplan, core.ReasonCode("REPLAN_REQUIRED")
			if verdict == "NEEDS_HUMAN" {
				wantAttention, wantReason = StandardFlowAttentionNeedsHuman, core.ReasonHumanDecisionRequired
			}
			if view.StandardFlowStatus == nil || view.StandardFlowStatus.Phase != core.OverallPhasePlanningTasks ||
				view.StandardFlowStatus.Attention != wantAttention || view.StandardFlowStatus.ReasonCode != wantReason {
				t.Fatalf("derived non-approved flow status = %#v", view.StandardFlowStatus)
			}
			harness.mu.Lock()
			spawnCount := len(harness.spawnConfigs)
			harness.mu.Unlock()
			if spawnCount != 2 {
				t.Fatalf("non-approved plan spawned %d sessions, want Steward and Planner only", spawnCount)
			}
		})
	}
}

func TestStandardPendingDispatchRejectsChangedTaskSetWithoutEmptyTask(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	background, cancel := context.WithCancel(context.Background())
	interrupt := &interruptingPendingDispatchStore{StandardFactStore: store, cancel: cancel}
	service := standardTestService(store, interrupt, harness, ids, clock, background)
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	flow, ok, err := store.GetClearDevStandardFlow(context.Background(), requirementID)
	if err != nil || !ok || len(flow.Dispatches) != 1 || flow.Dispatches[0].Status != core.DispatchStatusPending {
		t.Fatalf("interrupted pending dispatch = %#v, ok=%v err=%v", flow.Dispatches, ok, err)
	}
	dispatchTaskID := flow.Dispatches[0].PreallocatedDevelopmentTaskID
	control := New(Deps{Facts: store, AO: store, NewID: ids.New, Clock: clock})
	if _, err := control.CreateDevelopmentTask(context.Background(), requirementID, CreateDevelopmentTaskInput{
		Title: "concurrent task-set change", Mode: core.WorkModeQuick, MaxReworkCount: 0,
		Permissions:    CreatePathPermissionsInput{WritePaths: []string{"src/project.js"}},
		RequiredChecks: []CreateRequiredCheckInput{{Name: "node-all", Kind: "command"}},
	}); err != nil {
		t.Fatal(err)
	}
	recovered := standardTestService(store, store, harness, ids, clock, context.Background())
	if err := recovered.ResumeStandardFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := recovered.runStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	flow, ok, err = store.GetClearDevStandardFlow(context.Background(), requirementID)
	if err != nil || !ok || len(flow.Dispatches) != 1 || flow.Dispatches[0].Status != core.DispatchStatusRejected || flow.Dispatches[0].ReasonCode != core.ReasonTaskSetChanged {
		t.Fatalf("stale pending dispatch = %#v, ok=%v err=%v", flow.Dispatches, ok, err)
	}
	snapshot, ok, err := store.GetClearDevRequirement(context.Background(), requirementID)
	if err != nil || !ok {
		t.Fatalf("load stale dispatch snapshot: ok=%v err=%v", ok, err)
	}
	for _, task := range snapshot.DevelopmentTasks {
		if task.ID == dispatchTaskID {
			t.Fatalf("stale dispatch left an empty preallocated task: %#v", task)
		}
	}
}

func TestStandardStatusReportFailureCannotUndoCompletion(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	harness.invalidStatus = true
	service := standardTestService(store, store, harness, ids, clock, context.Background())
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	if err := service.runStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	view := assertCompletedStandardFlow(t, service, requirementID)
	if view.StandardFlowStatus == nil || view.StandardFlowStatus.Phase != core.OverallPhaseCompleted ||
		view.StandardFlowStatus.Attention != StandardFlowAttentionNone || view.StandardFlowStatus.ReasonCode != core.ReasonNone {
		t.Fatalf("completed display status = %#v", view.StandardFlowStatus)
	}
	found := false
	for _, step := range view.StandardFlow.AgentSteps {
		if step.Kind == core.AgentStepStatusReport {
			found = step.SendStatus == core.AgentStepSendStatusFailed && step.ReasonCode == "STEWARD_RESULT_INVALID"
		}
	}
	if !found {
		t.Fatal("invalid display-only status report was not recorded without changing completion")
	}
}

func TestStandardFlowResumesAcrossSettledStepFactGaps(t *testing.T) {
	tests := []struct {
		name string
		kind core.AgentStepKind
		gap  func(*testing.T, *sqlite.Store, core.StandardFlowSnapshot, string)
	}{
		{
			name: "planning result before Planner binding", kind: core.AgentStepRequestPlanning,
			gap: func(t *testing.T, _ *sqlite.Store, flow core.StandardFlowSnapshot, _ string) {
				if _, ok := roleBinding(flow, core.StandardRoleEngineeringPlanner, ""); ok {
					t.Fatal("Planner binding exists before recovery")
				}
			},
		},
		{
			name: "Builder result before candidate observation", kind: core.AgentStepBuilderResult,
			gap: func(t *testing.T, store *sqlite.Store, _ core.StandardFlowSnapshot, requirementID string) {
				snapshot, ok, err := store.GetClearDevRequirement(context.Background(), requirementID)
				if err != nil || !ok {
					t.Fatalf("load gap snapshot: ok=%v err=%v", ok, err)
				}
				if len(snapshot.Candidates) != 0 {
					t.Fatalf("candidates before recovery = %#v", snapshot.Candidates)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
			background, cancel := context.WithCancel(context.Background())
			interrupt := &interruptingStandardStore{StandardFactStore: store, kind: test.kind, cancel: cancel}
			service := standardTestService(store, interrupt, harness, ids, clock, background)
			if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			flow, ok, err := store.GetClearDevStandardFlow(context.Background(), requirementID)
			if err != nil || !ok {
				t.Fatalf("load interrupted flow: ok=%v err=%v", ok, err)
			}
			step, found := func() (core.AgentStep, bool) {
				for _, candidate := range flow.AgentSteps {
					if candidate.Kind == test.kind {
						return candidate, true
					}
				}
				return core.AgentStep{}, false
			}()
			if !found || step.SendStatus != core.AgentStepSendStatusSettled {
				t.Fatalf("interrupted step = %#v, found=%v", step, found)
			}
			test.gap(t, store, flow, requirementID)

			recovered := standardTestService(store, store, harness, ids, clock, context.Background())
			if err := recovered.ResumeStandardFlows(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertCompletedStandardFlow(t, recovered, requirementID)
			harness.mu.Lock()
			count := 0
			for _, relay := range harness.relays {
				if relay.clientMessageID == step.ClientMessageID {
					count++
				}
			}
			harness.mu.Unlock()
			if count != 1 {
				t.Fatalf("settled step relayed %d times after recovery, want one", count)
			}
		})
	}
}

func TestStandardFlowReworksOnceWithNewReviewerAndFreshEvidence(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, true)
	service := standardTestService(store, store, harness, ids, clock, context.Background())
	if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatal(err)
	}
	if err := service.runStandardFlow(context.Background(), requirementID); err != nil {
		t.Fatalf("continue rework STANDARD flow: %T %#v", err, err)
	}
	view := assertCompletedStandardFlow(t, service, requirementID)
	task := view.DevelopmentTasks[0]
	if task.DevelopmentTask.ReworkCount != 1 || len(task.Candidates) != 2 || task.CurrentCandidate == nil || task.CurrentCandidate.CommitSHA != forty("c") {
		t.Fatalf("reworked task = %#v", task)
	}
	flow := view.StandardFlow
	builders, reviewers := []core.RoleSessionBinding{}, []core.RoleSessionBinding{}
	for _, binding := range flow.RoleBindings {
		switch binding.Role {
		case core.StandardRoleBuilder:
			builders = append(builders, binding)
		case core.StandardRoleReviewer:
			reviewers = append(reviewers, binding)
		}
	}
	if len(builders) != 1 || len(reviewers) != 2 || reviewers[0].AOSessionID == reviewers[1].AOSessionID {
		t.Fatalf("Builder/Reviewer bindings = builders:%#v reviewers:%#v", builders, reviewers)
	}
	if reviewers[0].Status != core.RoleBindingStatusEnded || reviewers[1].Status != core.RoleBindingStatusEnded {
		t.Fatalf("Reviewer bindings were not ended after their exact reviews: %#v", reviewers)
	}
	if len(flow.LocalReviews) != 2 || flow.LocalReviews[0].Verdict != core.LocalReviewRework || flow.LocalReviews[1].Verdict != core.LocalReviewPass {
		t.Fatalf("local reviews = %#v", flow.LocalReviews)
	}
	currentCandidateID := task.CurrentCandidate.ID
	currentRuns := 0
	for _, run := range flow.CandidateCheckRuns {
		if run.CandidateCommitID == currentCandidateID {
			currentRuns++
			if run.Status != core.CandidateCheckRunStatusSettled || run.Result != core.EvidenceResultPass {
				t.Fatalf("current run = %#v", run)
			}
		}
	}
	if currentRuns != 3 {
		t.Fatalf("current-candidate runs = %d, want scope, required and integration", currentRuns)
	}
	finalPacket := standardReviewPacket{}
	if err := json.Unmarshal([]byte(flow.LocalReviews[1].ReviewPacketJSON), &finalPacket); err != nil {
		t.Fatal(err)
	}
	if finalPacket.CandidateID != currentCandidateID || finalPacket.CandidateSHA != forty("c") || len(finalPacket.Checks) != 3 {
		t.Fatalf("final review packet = %#v", finalPacket)
	}
	for _, check := range finalPacket.Checks {
		if check.Result != string(core.EvidenceResultPass) {
			t.Fatalf("final packet reused a stale result: %#v", check)
		}
	}
	harness.mu.Lock()
	var builderRelays []standardRelay
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) {
			builderRelays = append(builderRelays, relay)
		}
	}
	harness.mu.Unlock()
	if len(builderRelays) != 2 || builderRelays[0].sessionID != builderRelays[1].sessionID ||
		builderRelays[0].clientMessageID == builderRelays[1].clientMessageID {
		t.Fatalf("Builder rework relays = %#v", builderRelays)
	}
	for _, want := range []string{`"source":"LOCAL_REVIEW"`, `"kind":"LOCAL_REVIEW"`, `"reasonCode":"REVIEW_CHANGES_REQUIRED"`, `"result":"REWORK"`, `"summary":"邮箱实现仍有一个阻塞问题。"`} {
		if !strings.Contains(builderRelays[1].prompt, want) {
			t.Fatalf("second Builder prompt does not contain durable failure field %s: %s", want, builderRelays[1].prompt)
		}
	}
}

func TestStandardGitAndScopeFailuresUseFrozenTransitions(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*standardAgentHarness)
		wantStatus core.DevelopmentTaskStatus
		wantRework int
	}{
		{
			name: "Git infrastructure unavailable blocks",
			configure: func(h *standardAgentHarness) {
				h.inspectionErr = ports.ErrClearDevGitUnavailable
			},
			wantStatus: core.DevelopmentTaskStatusBlocked,
		},
		{
			name: "moving baseline exhausts one rework",
			configure: func(h *standardAgentHarness) {
				h.inspectionBase = forty("d")
			},
			wantStatus: core.DevelopmentTaskStatusNeedsHuman, wantRework: 1,
		},
		{
			name: "forbidden path exhausts one rework",
			configure: func(h *standardAgentHarness) {
				h.inspectionPaths = []ports.ClearDevDiffPath{{Status: "M", Path: "package.json"}}
			},
			wantStatus: core.DevelopmentTaskStatusNeedsHuman, wantRework: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
			test.configure(harness)
			service := standardTestService(store, store, harness, ids, clock, context.Background())
			if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			if err := service.runStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			view, err := service.GetRequirement(context.Background(), requirementID)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.DevelopmentTasks) != 1 || view.DevelopmentTasks[0].DevelopmentTask.Status != test.wantStatus || view.DevelopmentTasks[0].DevelopmentTask.ReworkCount != test.wantRework {
				t.Fatalf("terminal task = %#v", view.DevelopmentTasks)
			}
			if test.wantStatus == core.DevelopmentTaskStatusNeedsHuman && !hasStandardEventReason(view.Events, core.ReasonReworkLimitReached) {
				t.Fatal("rework exhaustion did not persist REWORK_LIMIT_REACHED")
			}
		})
	}
}

func TestStandardCheckFailuresFailReworkOrBlockFromRunnerFacts(t *testing.T) {
	t.Run("one code failure then passing candidate", func(t *testing.T) {
		store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
		harness.checkResults = []ports.ClearDevCheckResult{
			standardCheckResult(ports.ClearDevCheckFail, false),
			standardCheckResult(ports.ClearDevCheckPass, false),
			standardCheckResult(ports.ClearDevCheckPass, false),
		}
		service := standardTestService(store, store, harness, ids, clock, context.Background())
		if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
			t.Fatal(err)
		}
		if err := service.runStandardFlow(context.Background(), requirementID); err != nil {
			t.Fatal(err)
		}
		view := assertCompletedStandardFlow(t, service, requirementID)
		if view.DevelopmentTasks[0].DevelopmentTask.ReworkCount != 1 || len(view.DevelopmentTasks[0].Candidates) != 2 {
			t.Fatalf("check rework task = %#v", view.DevelopmentTasks[0])
		}
	})

	tests := []struct {
		name       string
		results    []ports.ClearDevCheckResult
		errors     []error
		wantStatus core.DevelopmentTaskStatus
		wantRework int
	}{
		{
			name: "timeout exhausts rework",
			results: []ports.ClearDevCheckResult{
				standardCheckResult(ports.ClearDevCheckTimedOut, true),
				standardCheckResult(ports.ClearDevCheckTimedOut, true),
			},
			wantStatus: core.DevelopmentTaskStatusNeedsHuman, wantRework: 1,
		},
		{
			name: "runner infrastructure blocks",
			results: []ports.ClearDevCheckResult{
				{Outcome: ports.ClearDevCheckInfraError},
			},
			errors:     []error{errors.New("container runtime unavailable")},
			wantStatus: core.DevelopmentTaskStatusBlocked,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
			harness.checkResults, harness.checkErrors = test.results, test.errors
			service := standardTestService(store, store, harness, ids, clock, context.Background())
			if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			if err := service.runStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			view, err := service.GetRequirement(context.Background(), requirementID)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.DevelopmentTasks) != 1 || view.DevelopmentTasks[0].DevelopmentTask.Status != test.wantStatus || view.DevelopmentTasks[0].DevelopmentTask.ReworkCount != test.wantRework {
				t.Fatalf("terminal check task = %#v", view.DevelopmentTasks)
			}
		})
	}
}

func TestStandardReviewerFailuresUseIndependentBindingAndFrozenMapping(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*standardAgentHarness)
		wantStatus core.DevelopmentTaskStatus
		wantRework int
		wantReason core.ReasonCode
	}{
		{
			name:       "BLOCKED",
			configure:  func(h *standardAgentHarness) { h.reviewVerdicts = []string{"BLOCKED"} },
			wantStatus: core.DevelopmentTaskStatusBlocked,
		},
		{
			name:       "NEEDS_HUMAN",
			configure:  func(h *standardAgentHarness) { h.reviewVerdicts = []string{"NEEDS_HUMAN"} },
			wantStatus: core.DevelopmentTaskStatusNeedsHuman,
		},
		{
			name:       "invalid result blocks immediately",
			configure:  func(h *standardAgentHarness) { h.invalidReview = true },
			wantStatus: core.DevelopmentTaskStatusBlocked,
		},
		{
			name:       "shared Builder workspace blocks",
			configure:  func(h *standardAgentHarness) { h.reviewerSharesBuilder = true },
			wantStatus: core.DevelopmentTaskStatusBlocked,
		},
		{
			name:       "second REWORK needs human",
			configure:  func(h *standardAgentHarness) { h.reviewVerdicts = []string{"REWORK", "REWORK"} },
			wantStatus: core.DevelopmentTaskStatusNeedsHuman, wantRework: 1, wantReason: core.ReasonReworkLimitReached,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
			test.configure(harness)
			service := standardTestService(store, store, harness, ids, clock, context.Background())
			if _, err := service.StartStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			if err := service.runStandardFlow(context.Background(), requirementID); err != nil {
				t.Fatal(err)
			}
			view, err := service.GetRequirement(context.Background(), requirementID)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.DevelopmentTasks) != 1 || view.DevelopmentTasks[0].DevelopmentTask.Status != test.wantStatus || view.DevelopmentTasks[0].DevelopmentTask.ReworkCount != test.wantRework {
				t.Fatalf("terminal Reviewer task = %#v", view.DevelopmentTasks)
			}
			if test.wantReason != core.ReasonNone && !hasStandardEventReason(view.Events, test.wantReason) {
				t.Fatalf("missing terminal reason %s", test.wantReason)
			}
		})
	}
}

func standardCheckResult(outcome ports.ClearDevCheckOutcome, timedOut bool) ports.ClearDevCheckResult {
	exitCode := 0
	if outcome != ports.ClearDevCheckPass {
		exitCode = 1
	}
	return ports.ClearDevCheckResult{
		Outcome: outcome, ImageID: "sha256:standard-test-image", ExitCode: exitCode, TimedOut: timedOut,
		OutputSummary: string(outcome), OutputSHA256: coreDigest([]byte(outcome)),
	}
}

func hasStandardEventReason(events []core.RequirementEvent, reason core.ReasonCode) bool {
	for _, event := range events {
		if event.Reason == reason {
			return true
		}
	}
	return false
}
