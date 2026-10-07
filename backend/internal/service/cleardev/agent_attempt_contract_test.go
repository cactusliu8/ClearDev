package cleardev

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestAgentAttemptStorageContract(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"memory", "sqlite"} {
		t.Run(name, func(t *testing.T) {
			var attempts AgentAttemptStore
			requirementID := "requirement"
			if name == "memory" {
				attempts = newMemoryAgentAttempts()
			} else {
				store := sqlitetest.MustOpen(t)
				requirementID = seedStandardRequirement(t, store, &standardTestIDs{}, func() time.Time { return now }, false)
				attempts = store
			}
			first := core.AgentStepAttempt{ID: "contract-attempt", DevelopmentRequirementID: requirementID, LogicalStepID: "contract-step", StepCategory: core.AgentStepCategoryStandard, StepKind: core.AgentStepBuilderResult, AttemptNumber: 1, RoleBindingID: "binding", AOSessionID: "session", ClientMessageID: "message", PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation}
			stored, created, err := attempts.EnsureClearDevAgentStepAttempt(ctx, first)
			if err != nil || !created {
				t.Fatalf("create=%v err=%v", created, err)
			}
			later := now.Add(time.Hour)
			replay := first
			replay.RequestedAt = later
			replay.CreatedAt = &later
			saved, created, err := attempts.EnsureClearDevAgentStepAttempt(ctx, replay)
			if err != nil || created || saved.CreatedAt == nil || !saved.CreatedAt.Equal(*stored.CreatedAt) || !saved.RequestedAt.Equal(now) {
				t.Fatalf("replay=%#v created=%v err=%v", saved, created, err)
			}
			for _, mutate := range []func(*core.AgentStepAttempt){func(a *core.AgentStepAttempt) { a.PromptSHA256 = strings.Repeat("b", 64) }, func(a *core.AgentStepAttempt) { a.AOSessionID = "other" }, func(a *core.AgentStepAttempt) { a.TriggerFailureEventID = "other" }, func(a *core.AgentStepAttempt) { a.ClientMessageID = "other" }} {
				conflict := first
				mutate(&conflict)
				if _, _, err := attempts.EnsureClearDevAgentStepAttempt(ctx, conflict); err == nil {
					t.Fatal("accepted business conflict")
				}
			}
			event := core.AgentAttemptEvent{ID: "contract-event", AttemptID: first.ID, Status: core.AgentAttemptSent, ClientMessageID: first.ClientMessageID, RecordedAt: now}
			if err := attempts.RecordClearDevAgentAttemptEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
			event.RecordedAt = later
			if err := attempts.RecordClearDevAgentAttemptEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
			event.ClientMessageID = "other"
			if err := attempts.RecordClearDevAgentAttemptEvent(ctx, event); err == nil {
				t.Fatal("accepted event conflict")
			}
			result := core.AgentStepResult{ID: "contract-result", AttemptID: first.ID, ResultIndex: 1, Source: core.AgentResultOriginal, ClientMessageID: first.ClientMessageID, TurnID: "turn", FinalMessageID: "final", RawMessageText: "raw", RawMessageSHA256: coreDigest([]byte("raw")), ObservedAt: now}
			if err := attempts.RecordClearDevAgentStepResult(ctx, result); err != nil {
				t.Fatal(err)
			}
			result.ObservedAt = later
			if err := attempts.RecordClearDevAgentStepResult(ctx, result); err != nil {
				t.Fatal(err)
			}
			result.RawMessageText = "changed"
			if err := attempts.RecordClearDevAgentStepResult(ctx, result); err == nil {
				t.Fatal("accepted raw conflict")
			}
			parse := core.AgentStepResultParse{ResultID: result.ID, Conclusion: core.AgentResultParseValid, ParsedAt: now}
			if err := attempts.RecordClearDevAgentStepResultParse(ctx, parse); err != nil {
				t.Fatal(err)
			}
			parse.ParsedAt = later
			if err := attempts.RecordClearDevAgentStepResultParse(ctx, parse); err != nil {
				t.Fatal(err)
			}
			parse.Conclusion = core.AgentResultParseInvalid
			if err := attempts.RecordClearDevAgentStepResultParse(ctx, parse); err == nil {
				t.Fatal("accepted parse conflict")
			}
			views, err := attempts.ListClearDevAgentStepAttempts(ctx, requirementID)
			if err != nil || len(views) != 1 || views[0].LastObservedAt == nil || !views[0].LastObservedAt.Equal(now) || len(views[0].Results) != 1 || !views[0].Results[0].ObservedAt.Equal(now) || views[0].Results[0].ParseConclusion != core.AgentResultParseValid {
				t.Fatalf("first evidence lost: %#v err=%v", views, err)
			}
		})
	}
}

type failingAttemptEvidence struct {
	AgentAttemptStore
	operation string
}

func (f failingAttemptEvidence) EnsureClearDevAgentStepAttempt(ctx context.Context, a core.AgentStepAttempt) (core.AgentStepAttempt, bool, error) {
	if f.operation == "create" {
		return core.AgentStepAttempt{}, false, errors.New("save unavailable")
	}
	return f.AgentAttemptStore.EnsureClearDevAgentStepAttempt(ctx, a)
}
func (f failingAttemptEvidence) EnsureClearDevAgentAttemptEvent(ctx context.Context, e core.AgentAttemptEvent) (bool, error) {
	if f.operation == "boundary" {
		return false, errors.New("save unavailable")
	}
	return f.AgentAttemptStore.EnsureClearDevAgentAttemptEvent(ctx, e)
}
func (f failingAttemptEvidence) ListClearDevAgentStepAttemptStates(ctx context.Context, r, s string) ([]core.AgentStepAttemptView, error) {
	if f.operation == "step" {
		return nil, errors.New("state unavailable")
	}
	return f.AgentAttemptStore.ListClearDevAgentStepAttemptStates(ctx, r, s)
}
func (f failingAttemptEvidence) GetClearDevAgentDeliveryState(ctx context.Context, r, a, m string) (core.AgentStepAttemptView, error) {
	if f.operation == "message" {
		return core.AgentStepAttemptView{}, errors.New("state unavailable")
	}
	return f.AgentAttemptStore.GetClearDevAgentDeliveryState(ctx, r, a, m)
}
func TestAttemptEvidenceFailureStopsSending(t *testing.T) {
	for _, operation := range []string{"create", "boundary", "step", "message"} {
		t.Run(operation, func(t *testing.T) {
			now := time.Now().UTC()
			service, attempts, _, _, chat, step, requirementID := recoveryFixture(now)
			service.attempts = failingAttemptEvidence{AgentAttemptStore: attempts, operation: operation}
			if err := service.relayAgentTurn(context.Background(), requirementID, core.AgentStepCategoryStandard, step, "session-1", "prompt", step.ClientMessageID, core.AgentAttemptSent, now); err == nil {
				t.Fatal("missing rejection")
			}
			if chat.relays != 0 {
				t.Fatalf("sent %d messages after %s failure", chat.relays, operation)
			}
		})
	}
}

func TestFirstAttemptUsesCreationClockAndPreservesReplay(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service, _, _, _, _, step, requirementID := recoveryFixture(now)
	step.RequestedAt = now.Add(-time.Hour)
	first, err := service.ensureAgentAttempt(context.Background(), requirementID, core.AgentStepCategoryStandard, step, "session-1", 1, "")
	if err != nil || first.CreatedAt == nil || !first.CreatedAt.Equal(now) || !first.RequestedAt.Equal(now) {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	service.now = func() time.Time { return now.Add(time.Hour) }
	replay, err := service.ensureAgentAttempt(context.Background(), requirementID, core.AgentStepCategoryStandard, step, "session-1", 1, "")
	if err != nil || !replay.CreatedAt.Equal(now) {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
}

func TestSecondAttemptRejectsStaleFailureAndEarlyClock(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"memory", "sqlite"} {
		t.Run(name, func(t *testing.T) {
			service, memory, _, _, _, step, requirementID := recoveryFixture(now)
			var attempts AgentAttemptStore = memory
			if name == "sqlite" {
				db := sqlitetest.MustOpen(t)
				requirementID = seedStandardRequirement(t, db, &standardTestIDs{}, func() time.Time { return now }, false)
				attempts = db
				service.attempts = db
			}
			triggerID := seedRetryableFirstAttempt(t, service, requirementID, step, now)
			views, err := attempts.ListClearDevAgentStepAttemptStates(ctx, requirementID, step.ID)
			if err != nil {
				t.Fatal(err)
			}
			second := agentAttemptFromView(requirementID, views[0])
			second.ID = agentAttemptID(step.ID, 2)
			second.AttemptNumber = 2
			second.ClientMessageID = step.ClientMessageID + ":attempt:2"
			second.TriggerFailureEventID = triggerID
			early := now.Add(-time.Second)
			second.CreatedAt = &early
			second.RequestedAt = early
			if _, _, err := attempts.EnsureClearDevAgentStepAttempt(ctx, second); err == nil {
				t.Fatal("accepted creation before failure")
			}
			second.CreatedAt = &now
			second.RequestedAt = now
			if err := attempts.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: "newer", AttemptID: views[0].ID, Status: core.AgentAttemptDeliveryUnknown, FailureCategory: domain.AgentFailureDeliveryUnknown, ErrorSummary: "message delivery outcome is unknown", ClientMessageID: step.ClientMessageID, RecordedAt: now.Add(-time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := attempts.EnsureClearDevAgentStepAttempt(ctx, second); err == nil {
				t.Fatal("accepted replaced failure despite latest insertion")
			}
		})
	}
}

func (f failingAttemptEvidence) ReserveClearDevAgentMessage(ctx context.Context, c core.ReserveAgentMessageCommand) (bool, error) {
	if f.operation == "boundary" {
		return false, errors.New("save unavailable")
	}
	return f.AgentAttemptStore.ReserveClearDevAgentMessage(ctx, c)
}
