package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestClearDevMessageBudgetCountsOriginalCorrectionAndRecovery(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, _, _, _, chat, step, _ := recoveryFixture(now)
	store := sqlitetest.MustOpen(t)
	requirementID := seedStandardRequirement(t, store, &standardTestIDs{}, func() time.Time { return now }, false)
	service.attempts = store
	service.facts = store
	for _, correction := range []bool{false, true} {
		message, prompt, status := step.ClientMessageID, "prompt", core.AgentAttemptSent
		if correction {
			message += ":parse-correction"
			prompt = "correction"
			status = core.AgentAttemptCorrectionSent
		}
		if err := service.relayAgentTurn(ctx, requirementID, core.AgentStepCategoryStandard, step, "session-1", prompt, message, status, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.recordAgentObservationFailure(ctx, requirementID, step, step.ClientMessageID+":parse-correction", ports.WithChatFailure(errors.New("provider unavailable"), domain.ConversationFailure{Category: domain.AgentFailureProviderUnavailable, Retryable: true})); err != nil {
		t.Fatal(err)
	}
	if err := service.relayAgentTurn(ctx, requirementID, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); err != nil {
		t.Fatal(err)
	}
	if chat.relays != 3 {
		t.Fatalf("actual sends=%d", chat.relays)
	}
	view, err := service.GetRequirement(ctx, requirementID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Budget *struct {
			Steps []struct {
				ReservedMessages      *int `json:"reservedMessages"`
				ConfirmedSentMessages *int `json:"confirmedSentMessages"`
			} `json:"steps"`
		} `json:"messageBudget"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Budget == nil || len(response.Budget.Steps) != 1 || response.Budget.Steps[0].ReservedMessages == nil || *response.Budget.Steps[0].ReservedMessages != 3 || response.Budget.Steps[0].ConfirmedSentMessages == nil || *response.Budget.Steps[0].ConfirmedSentMessages != 3 {
		t.Fatalf("three actual messages share one logical step but lack separate reservations/confirmations: budget=%#v", response.Budget)
	}
}

type messageBudgetCorrectionFailure struct {
	*standardAgentHarness
	fail bool
}

func (h *messageBudgetCorrectionFailure) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, messageID string) (string, error) {
	turnID, err := h.standardAgentHarness.RelayChatTurnWithID(ctx, session, prompt, messageID)
	if err != nil || !strings.HasSuffix(messageID, ":parse-correction") || !h.fail {
		return turnID, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fail = false
	snapshot := h.snapshots[session]
	for i := range snapshot.Turns {
		if snapshot.Turns[i].ID == turnID {
			snapshot.Turns[i].State = domain.TurnStateFailed
			snapshot.Turns[i].Failure = &domain.ConversationFailure{Category: domain.AgentFailureProviderUnavailable, Retryable: true, ErrorSummary: "provider temporarily unavailable"}
		}
	}
	h.snapshots[session] = snapshot
	return turnID, nil
}

func TestClearDevMessageBudgetProtocolRecoveryAndCompletion(t *testing.T) {
	for _, order := range []string{"correction-before-recovery", "correction-after-recovery"} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			store, err := sqlitetest.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			ids := &standardTestIDs{}
			requirementID := seedConfirmedStandardRequirement(t, store, ids, clock)
			harness := newStandardAgentHarness(store, false)
			harness.builderInvalidLeft = 1
			service := standardTestService(store, store, harness, ids, clock, ctx)
			if order == "correction-before-recovery" {
				service.chat = &messageBudgetCorrectionFailure{standardAgentHarness: harness, fail: true}
			} else {
				harness.builderTurnFailures = 1
			}
			if _, err := service.StartStandardFlow(ctx, requirementID); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			if err := service.ResumeStandardFlows(ctx); err != nil {
				t.Fatal(err)
			}
			view := assertCompletedStandardFlow(t, service, requirementID)
			var stepID string
			for _, a := range view.AgentStepAttempts {
				if a.StepKind == core.AgentStepBuilderResult {
					stepID = a.LogicalStepID
				}
			}
			var usage *core.MessageBudgetUsage
			for i := range view.MessageBudget.Steps {
				if view.MessageBudget.Steps[i].LogicalStepID == stepID {
					usage = &view.MessageBudget.Steps[i]
				}
			}
			if usage == nil || *usage.ReservedSteps != 1 || *usage.ReservedMessages != 3 || *usage.ConfirmedSentMessages != 3 || *usage.RemainingMessages != 0 {
				t.Fatalf("usage=%#v", usage)
			}
			messages := map[string]int{}
			for _, r := range harness.relays {
				if strings.Contains(r.prompt, `"kind":"BUILDER_RESULT"`) || strings.HasSuffix(r.clientMessageID, ":parse-correction") {
					messages[r.clientMessageID]++
				}
			}
			if len(messages) != 3 || len(harness.checkRequests) == 0 {
				t.Fatalf("messages=%v checks=%d", messages, len(harness.checkRequests))
			}
			for id, n := range messages {
				if n != 1 {
					t.Fatalf("message %s sent %d times", id, n)
				}
			}
			sends := len(harness.relays)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			harness.store = reopened
			restarted := standardTestService(reopened, reopened, harness, ids, clock, ctx)
			if err := restarted.ResumeStandardFlows(ctx); err != nil {
				t.Fatal(err)
			}
			after := assertCompletedStandardFlow(t, restarted, requirementID)
			if len(harness.relays) != sends {
				t.Fatal("reopen sent a new message")
			}
			for _, u := range after.MessageBudget.Steps {
				if u.LogicalStepID == stepID && (*u.ReservedMessages != 3 || *u.ConfirmedSentMessages != 3) {
					t.Fatalf("reopen budget=%#v", u)
				}
			}
			t.Logf("%s: one step, 3 reservations, 3 confirmations, 3 distinct messages each sent once, %d candidate checks; reopen sends=0", order, len(harness.checkRequests))
		})
	}
}

// Reuse the real-database concurrent recovery test and also include it in the stage race filter.
func TestClearDevMessageBudgetConcurrentActualSend(t *testing.T) {
	TestSQLiteConcurrentRecoveryCreatesAndSendsOnce(t)
}

type messageBudgetFailConfirmation struct{ AgentAttemptStore }

func (s messageBudgetFailConfirmation) ConfirmClearDevAgentMessage(context.Context, core.AgentAttemptEvent) error {
	return errors.New("injected confirmation failure")
}

func TestClearDevMessageBudgetFailureReopenNeverResends(t *testing.T) {
	for _, failure := range []string{"confirmation", "unknown", "empty-turn", "timeout", "reserved-before-crash"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC)
			service, _, _, _, chat, step, _ := recoveryFixture(now)
			dir := t.TempDir()
			store := sqlitetest.MustOpenAt(t, dir)
			id := seedStandardRequirement(t, store, &standardTestIDs{}, func() time.Time { return now }, false)
			service.attempts = store
			if failure == "confirmation" {
				service.attempts = messageBudgetFailConfirmation{store}
			}
			if failure == "empty-turn" {
				service.chat = messageBudgetEmptyTurn{chat}
			}
			if failure == "unknown" {
				chat.err = errors.New("connection disappeared")
			}
			if failure == "reserved-before-crash" {
				a, err := service.ensureAgentAttempt(ctx, id, core.AgentStepCategoryStandard, step, "session-1", 1, "")
				if err != nil {
					t.Fatal(err)
				}
				_, err = store.ReserveClearDevAgentMessage(ctx, core.ReserveAgentMessageCommand{Attempt: a, Source: core.AgentMessageOriginal, Boundary: core.AgentAttemptEvent{ID: "crash-boundary", AttemptID: a.ID, ClientMessageID: step.ClientMessageID, PromptSHA256: step.PromptSHA256, Status: core.AgentAttemptDeliveryUnknown, FailureCategory: domain.AgentFailureDeliveryUnknown, RecordedAt: now}})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				err := service.relayAgentTurn(ctx, id, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now)
				if failure != "timeout" && err == nil {
					t.Fatal("failure was ignored")
				}
				if failure == "timeout" {
					if err != nil {
						t.Fatal(err)
					}
					if err := service.recordAgentObservationFailure(ctx, id, step, step.ClientMessageID, context.DeadlineExceeded); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := store.GetClearDevMessageBudget(ctx, id)
			if err != nil || *before.ReservedMessages != 1 {
				t.Fatalf("budget=%#v err=%v", before, err)
			}
			wantSent := int64(0)
			if failure == "timeout" {
				wantSent = 1
			}
			if *before.ConfirmedSentMessages != wantSent {
				t.Fatalf("confirmed=%d", *before.ConfirmedSentMessages)
			}
			sends := chat.relays
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			service.attempts = reopened
			_ = service.relayAgentTurn(ctx, id, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now)
			if chat.relays != sends {
				t.Fatal("reopen resent message")
			}
			if failure != "timeout" {
				chat.snapshot = chatsvc.Snapshot{SessionID: "session-1", Turns: []domain.ConversationTurn{{ID: "turn-recovery", HandledBySessionID: "session-1", State: domain.TurnStateRunning}}, Messages: []domain.ConversationMessage{{ID: "user", TurnID: "turn-recovery", Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: "prompt", ClientMessageID: step.ClientMessageID}}}
				for range 2 {
					if err := service.relayAgentTurn(ctx, id, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); err != nil {
						t.Fatal(err)
					}
				}
			}
			after, err := reopened.GetClearDevMessageBudget(ctx, id)
			if err != nil || *after.ReservedMessages != 1 || *after.ConfirmedSentMessages != 1 || chat.relays != sends {
				t.Fatalf("reconciled=%#v sends=%d err=%v", after, chat.relays, err)
			}
			t.Logf("%s: reserved=1, confirmed before=%d after=1; reopen sends=0", failure, wantSent)
		})
	}
}

func TestClearDevMessageBudgetLegacyReconcilesAndProcessesExistingResult(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC)
	service, _, _, _, chat, step, _ := recoveryFixture(now)
	dir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	id := seedStandardRequirement(t, store, &standardTestIDs{}, func() time.Time { return now }, false)
	service.attempts = store
	service.corrections = store
	a, err := service.ensureAgentAttempt(ctx, id, core.AgentStepCategoryStandard, step, "session-1", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: "old-boundary", AttemptID: a.ID, Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: step.ClientMessageID, PromptSHA256: step.PromptSHA256, FailureCategory: domain.AgentFailureDeliveryUnknown, RecordedAt: now}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER cleardev_message_budget_versions_update_forbidden; UPDATE cleardev_message_budget_versions SET version='LEGACY_UNMEASURED'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	chat.snapshot = chatsvc.Snapshot{SessionID: "session-1", Turns: []domain.ConversationTurn{{ID: "old-turn", HandledBySessionID: "session-1", State: domain.TurnStateCompleted}}, Messages: []domain.ConversationMessage{{ID: "old-user", TurnID: "old-turn", Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: "prompt", ClientMessageID: step.ClientMessageID}}}
	for range 2 {
		if err := service.relayAgentTurn(ctx, id, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); err != nil {
			t.Fatal(err)
		}
		result, stopped, err := service.acceptOrCorrectAgentJSON(ctx, id, core.AgentStepCategoryStandard, "session-1", step, standardMessage{TurnID: "old-turn", MessageID: "old-final", Text: `{"old":"result"}`}, func(raw []byte) error { var v map[string]string; return json.Unmarshal(raw, &v) }, standardStepReasons{}, nil, errors.New("stop"))
		if err != nil || stopped || result.MessageID != "old-final" {
			t.Fatalf("old result processing: %+v stopped=%v err=%v", result, stopped, err)
		}
	}
	if _, err := service.sendParseCorrection(ctx, id, core.AgentStepCategoryStandard, "session-1", step, errors.New("bad JSON")); !messageBudgetReason(err, core.ReasonMessageBudgetUnknown) {
		t.Fatalf("legacy correction: %v", err)
	}
	step.ID = "new-step"
	step.ClientMessageID = "new-message"
	if err := service.relayAgentTurn(ctx, id, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); !messageBudgetReason(err, core.ReasonMessageBudgetUnknown) {
		t.Fatalf("legacy new step: %v", err)
	}
	view, err := store.GetClearDevMessageBudget(ctx, id)
	if err != nil || view.ReservedMessages != nil || view.ConfirmedSentMessages != nil || view.RemainingMessages != nil || chat.relays != 0 {
		t.Fatalf("legacy=%#v sends=%d err=%v", view, chat.relays, err)
	}
}

type messageBudgetEmptyTurn struct{ *recoveryChat }

func (c messageBudgetEmptyTurn) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, id string) (string, error) {
	_, err := c.recoveryChat.RelayChatTurnWithID(ctx, session, prompt, id)
	return "", err
}

type messageBudgetRefusal struct {
	AgentAttemptStore
	code core.ReasonCode
}

func (s messageBudgetRefusal) ReserveClearDevAgentMessage(context.Context, core.ReserveAgentMessageCommand) (bool, error) {
	return false, &core.RuleError{Code: s.code, Message: "budget refused"}
}

func TestClearDevMessageBudgetCallersPreserveRefusal(t *testing.T) {
	for _, caller := range []string{"standard", "planning", "direction", "progress"} {
		for _, code := range []core.ReasonCode{core.ReasonMessageBudgetUnknown, core.ReasonMessageBudgetExhausted, core.ReasonMessageBudgetBinding} {
			t.Run(caller+"/"+string(code), func(t *testing.T) {
				ctx := context.Background()
				now := time.Now().UTC()
				service, _, _, _, chat, step, _ := recoveryFixture(now)
				store := sqlitetest.MustOpen(t)
				id := seedStandardRequirement(t, store, &standardTestIDs{}, func() time.Time { return now }, false)
				service.attempts = messageBudgetRefusal{store, code}
				step.SendStatus = core.AgentStepSendStatusPending
				step.RequestID = "request"
				binding := core.ComplexRoleBinding{ID: step.RoleBindingID, DevelopmentRequirementID: id, AOSessionID: "session-1"}
				validate := func([]byte) error { t.Fatal("budget error reached parser"); return nil }
				var err error
				switch caller {
				case "standard":
					_, _, err = service.runAgentStep(ctx, core.StandardFlowSnapshot{DevelopmentRequirementID: id, AgentSteps: []core.AgentStep{step}}, core.RoleSessionBinding{ID: step.RoleBindingID, AOSessionID: "session-1"}, step.RequestID, step.Kind, "prompt", standardStepReasons{}, validate)
				case "planning":
					_, _, err = service.runComplexAgentStep(ctx, core.ComplexPlanningSnapshot{Requirement: core.ComplexRequirement{DevelopmentRequirementID: id}, AgentSteps: []core.AgentStep{step}}, binding, step.RequestID, step.Kind, "prompt", standardStepReasons{}, validate)
				case "direction":
					_, _, err = service.runDirectionAgentStep(ctx, core.ComplexPlanningSnapshot{}, core.DirectionChangeSnapshot{AgentSteps: []core.AgentStep{step}}, binding, step.RequestID, step.Kind, "prompt", standardStepReasons{}, validate)
				case "progress":
					_, _, err = service.sendProgressExplanation(ctx, core.ProgressExplanationRequest{ID: step.ID, DevelopmentRequirementID: id, AOSessionID: "session-1", ClientMessageID: step.ClientMessageID, PromptText: "prompt", PromptSHA256: step.PromptSHA256, CreatedAt: now}, now)
				}
				if !messageBudgetReason(err, code) || chat.relays != 0 {
					t.Fatalf("reason lost or sent: err=%v sends=%d", err, chat.relays)
				}
			})
		}
	}
}

func TestClearDevMessageBudgetFactOnlyReadKeepsUnknownReason(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	now := time.Now().UTC()
	id := seedStandardRequirement(t, store, &standardTestIDs{}, func() time.Time { return now }, false)
	service := New(Deps{Facts: store})
	view, err := service.GetRequirement(context.Background(), id)
	if err != nil || view.MessageBudget == nil || view.MessageBudget.UnknownReason != "MESSAGE_BUDGET_STORE_UNAVAILABLE" || view.MessageBudget.ReservedMessages != nil {
		t.Fatalf("fact-only budget=%#v err=%v", view.MessageBudget, err)
	}
}
