package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func preSendStoreAttempt(t *testing.T, s *sqlite.Store, name string, now time.Time) (core.AgentStepAttempt, core.AgentAttemptEvent) {
	t.Helper()
	a := core.AgentStepAttempt{ID: "presend-" + name, DevelopmentRequirementID: "presend-requirement", LogicalStepID: "step-" + name, StepCategory: core.AgentStepCategoryStandard, StepKind: core.AgentStepBuilderResult, AttemptNumber: 1, RoleBindingID: "builder", AOSessionID: "session", ClientMessageID: "message-" + name, PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation}
	if _, _, err := s.EnsureClearDevAgentStepAttempt(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ReserveClearDevAgentMessage(context.Background(), budgetCommand(a, core.AgentMessageOriginal)); err != nil || !claimed {
		t.Fatal("reserve original", claimed, err)
	}
	e := core.AgentAttemptEvent{ID: a.ID + ":proof", AttemptID: a.ID, Status: core.AgentAttemptFailedBeforeSend, ClientMessageID: a.ClientMessageID, PromptSHA256: a.PromptSHA256, Retryable: true, ErrorSummary: "message send did not start", RecordedAt: now}
	return a, e
}

func TestPreSendStoreShapeBindingAndContraryEvidence(t *testing.T) {
	ctx := context.Background()
	s := sqlitetest.MustOpen(t)
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	seedClearDevAO(t, s, "presend-project")
	if err := s.CreateClearDevRequirement(ctx, initialRequirement("presend-requirement", "presend-project", now)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*core.AgentAttemptEvent)
	}{
		{"turn", func(e *core.AgentAttemptEvent) { e.TurnID = "invented" }},
		{"turn-state", func(e *core.AgentAttemptEvent) { e.TurnState = domain.TurnStateFailed }},
		{"provider", func(e *core.AgentAttemptEvent) { e.FailureCategory = domain.AgentFailureProvider }},
		{"retry-at", func(e *core.AgentAttemptEvent) { e.RetryAt = &now }},
		{"provider-code", func(e *core.AgentAttemptEvent) { e.ProviderErrorCode = "invented" }},
		{"no-retry", func(e *core.AgentAttemptEvent) { e.Retryable = false }},
		{"other-prompt", func(e *core.AgentAttemptEvent) { e.PromptSHA256 = strings.Repeat("b", 64) }},
		{"correction", func(e *core.AgentAttemptEvent) { e.ClientMessageID += ":parse-correction" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, proof := preSendStoreAttempt(t, s, tc.name, now)
			bad := proof
			tc.mutate(&bad)
			if err := s.RecordClearDevAgentAttemptEvent(ctx, bad); err == nil {
				t.Fatal("invalid no-send proof accepted")
			}
			if err := s.RecordClearDevAgentAttemptEvent(ctx, proof); err != nil {
				t.Fatal("valid proof after rejected mutation", err)
			}
			if err := s.RecordClearDevAgentAttemptEvent(ctx, proof); err != nil {
				t.Fatal("identical replay rejected", err)
			}
			proof.ID += ":second-proof"
			if err := s.RecordClearDevAgentAttemptEvent(ctx, proof); err == nil {
				t.Fatal("unbound second proof was appended")
			}
			states, err := s.ListClearDevAgentStepAttemptStates(ctx, a.DevelopmentRequirementID, a.LogicalStepID)
			if err != nil || len(states) != 1 || !core.FailedBeforeSendViewValid(states[0]) {
				t.Fatal("persisted proof did not survive projection", states, err)
			}
		})
	}
	for _, kind := range []string{"sent", "result", "confirmed"} {
		t.Run(kind, func(t *testing.T) {
			a, proof := preSendStoreAttempt(t, s, kind, now)
			accepted := core.AgentAttemptEvent{ID: a.ID + ":accepted", AttemptID: a.ID, Status: core.AgentAttemptSent, ClientMessageID: a.ClientMessageID, PromptSHA256: a.PromptSHA256, TurnID: "accepted-turn", TurnState: domain.TurnStateRunning, RecordedAt: now}
			var err error
			switch kind {
			case "sent":
				err = s.RecordClearDevAgentAttemptEvent(ctx, accepted)
			case "confirmed":
				err = s.ConfirmClearDevAgentMessage(ctx, accepted)
			case "result":
				err = s.RecordClearDevAgentStepResult(ctx, core.AgentStepResult{ID: a.ID + ":result", AttemptID: a.ID, ResultIndex: 1, Source: core.AgentResultOriginal, ClientMessageID: a.ClientMessageID, TurnID: "accepted-turn", FinalMessageID: "final", RawMessageText: "done", RawMessageSHA256: strings.Repeat("c", 64), ObservedAt: now})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RecordClearDevAgentAttemptEvent(ctx, proof); err == nil {
				t.Fatal("no-send proof erased contrary accepted evidence")
			}
		})
	}
}

func TestPreSendStoreReopenSecondAttemptAndDownRefusal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := sqlitetest.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	seedClearDevAO(t, s, "presend-project")
	if err := s.CreateClearDevRequirement(ctx, initialRequirement("presend-requirement", "presend-project", now)); err != nil {
		t.Fatal(err)
	}
	first, proof := preSendStoreAttempt(t, s, "reopen", now)
	if err := s.RecordClearDevAgentAttemptEvent(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	second := first
	second.ID, second.AttemptNumber, second.ClientMessageID, second.TriggerFailureEventID = "second", 2, first.ClientMessageID+":attempt:2", proof.ID
	if _, created, err := reopened.EnsureClearDevAgentStepAttempt(ctx, second); err != nil || !created {
		t.Fatal("bound retry after DB reopen", created, err)
	}
	if _, created, err := reopened.EnsureClearDevAgentStepAttempt(ctx, second); err != nil || created {
		t.Fatal("retry replay changed identity", created, err)
	}
	changed := second
	changed.AOSessionID = "other-session"
	if _, _, err := reopened.EnsureClearDevAgentStepAttempt(ctx, changed); err == nil {
		t.Fatal("replay changed session")
	}
	if claimed, err := reopened.ReserveClearDevAgentMessage(ctx, budgetCommand(second, core.AgentMessageRecoveryOriginal)); err != nil || !claimed {
		t.Fatal("second reservation", claimed, err)
	}
	ledger, err := reopened.GetClearDevMessageBudget(ctx, first.DevelopmentRequirementID)
	if err != nil || len(ledger.Steps) != 1 || *ledger.Steps[0].ReservedMessages != 2 || *ledger.Steps[0].ConfirmedSentMessages != 0 {
		t.Fatal("no-send proof refunded original message", ledger, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := goose.DownTo(db, "migrations", 191); err == nil || !strings.Contains(err.Error(), "ok=1") {
		t.Fatal("no-send history did not trigger the down guard", err)
	}
	var version int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != 192 {
		t.Fatal("refused down changed version", version, err)
	}
	var invalid int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatal("foreign keys changed", invalid, err)
	}
}
