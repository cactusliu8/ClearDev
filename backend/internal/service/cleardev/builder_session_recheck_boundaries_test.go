package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type recheckPreflightError struct{ err error }

func (p recheckPreflightError) CheckControlledPreflight(context.Context, domain.AgentHarness, string) (ports.ChatControlledPreflight, error) {
	return ports.ChatControlledPreflight{}, p.err
}

func assertBuilderRecheckFailure(t *testing.T, f *projectPlanningFixture, id, request, stage, reason string) {
	t.Helper()
	view, err := f.s.GetWorkflowRecovery(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range view.BuilderSessionChecks {
		if check.RecoveryID == request && check.Outcome == "FAILED" && check.Stage == stage && check.ReasonCode == reason {
			return
		}
	}
	t.Fatalf("missing precise failure %s/%s: %+v", stage, reason, view.BuilderSessionChecks)
}

func TestBuilderSessionRecheckClassifiesGatesWithoutSending(t *testing.T) {
	for _, kind := range []string{"preflight-auth", "native-error", "not-native", "changed-identity", "busy-after-restore", "native-turn-open", "database-turn-open"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f, h, id, before, original := newBuilderSessionRecheckFixture(t)
			input := builderSessionRecheckInput(t, f, id, "recheck-"+kind)
			calls, restores := len(h.relays), 0
			stage, reason := "RESTORE", "BUILDER_RESTORE_FAILED"
			f.s.restoreOriginalAgentSession = func(ctx context.Context, sid domain.SessionID) (string, error) {
				restores++
				if kind == "native-error" {
					return "", errors.New("unclassified native error with token=must-not-be-exposed")
				}
				if kind == "not-native" {
					return "new-conversation", nil
				}
				record, _, err := f.store.GetSession(ctx, sid)
				if err != nil {
					return "", err
				}
				record.Activity.State = domain.ActivityIdle
				if kind == "changed-identity" {
					record.Metadata.ProviderConversationID = "different-native-session"
				}
				if kind == "busy-after-restore" {
					record.Activity.State = domain.ActivityActive
				}
				if kind == "native-turn-open" {
					snapshot := h.snapshots[sid]
					snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{ID: "other-live-turn", HandledBySessionID: sid, State: domain.TurnStateRunning})
					h.snapshots[sid] = snapshot
				}
				return "native", f.store.UpdateSession(ctx, record)
			}
			switch kind {
			case "preflight-auth":
				f.s.preflightChecker = recheckPreflightError{err: ports.ErrChatAuthRequired}
				stage, reason = "PREFLIGHT", "LOGIN_REQUIRED"
			case "not-native":
				reason = "BUILDER_RESTORE_NOT_NATIVE"
			case "changed-identity":
				stage, reason = "IDENTITY", "BUILDER_RESTORE_IDENTITY_CHANGED"
			case "busy-after-restore", "native-turn-open":
				stage, reason = "SESSION", "BUILDER_SESSION_BUSY"
			case "database-turn-open":
				conversation, err := f.store.CreateConversation(ctx, "original-recheck-conversation", domain.ConversationScopeSession, original.ProjectID, original.ID, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.AdoptProviderTurn(ctx, conversation.ID, original.ID, "test-generation", "other-open-turn", "native-other-open-turn", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				stage, reason = "SESSION", "BUILDER_SESSION_BUSY"
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
				t.Fatal(err)
			}
			after := stoppedWorkflow(t, f, id)
			assertBuilderRecheckFailure(t, f, id, input.RequestID, stage, reason)
			if len(h.relays) != calls || len(after.Dispatches) != len(before.Dispatches) {
				t.Fatal("failed recheck delivered new work")
			}
			if (kind == "preflight-auth" || kind == "database-turn-open") && restores != 0 {
				t.Fatal("restored before establishing safe prerequisites")
			}
			checks, err := f.store.ListClearDevBuilderSessionChecks(ctx, before.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(checks)
			if strings.Contains(string(raw), "must-not-be-exposed") || strings.Contains(string(raw), "original-native-recheck-fixture") || strings.Contains(string(raw), "bindingSha256") {
				t.Fatal("new public checkpoint leaked a credential or internal native binding")
			}
		})
	}
}

func TestBuilderSessionRecheckFailureThenExplicitRetryKeepsHistory(t *testing.T) {
	ctx := context.Background()
	f, h, id, before, _ := newBuilderSessionRecheckFixture(t)
	first := builderSessionRecheckInput(t, f, id, "first-recheck")
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, first); err != nil {
		t.Fatal(err)
	}
	stoppedWorkflow(t, f, id)
	assertBuilderRecheckFailure(t, f, id, first.RequestID, "RESTORE", "PROVIDER_UNAVAILABLE")
	f.s.restoreOriginalAgentSession = func(ctx context.Context, sid domain.SessionID) (string, error) {
		record, _, err := f.store.GetSession(ctx, sid)
		if err != nil {
			return "", err
		}
		record.Activity.State = domain.ActivityIdle
		return "native", f.store.UpdateSession(ctx, record)
	}
	second := builderSessionRecheckInput(t, f, id, "second-explicit-recheck")
	if second.TargetID == first.TargetID {
		t.Fatal("a later failed recheck did not have its own stopped target")
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, second); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if after.Run.CompletedAt == nil || len(after.Dispatches) != len(before.Dispatches) {
		t.Fatal("the same original dispatch did not continue")
	}
	assertBuilderRecheckFailure(t, f, id, first.RequestID, "RESTORE", "PROVIDER_UNAVAILABLE")
	for _, b := range after.Exception.Budgets {
		if b.RoleKind == core.ComplexExceptionBudgetBuilder && b.UsedTurns != 2 {
			t.Fatal("failed rechecks spent or reset Builder rounds")
		}
	}
	beforeReplay := len(h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, first); err != nil || len(h.relays) != beforeReplay {
		t.Fatal("old request replay changed completed work")
	}
}

type recheckReservationInterception struct {
	AgentAttemptStore
	step   string
	before func(context.Context) error
}

func (s *recheckReservationInterception) ReserveClearDevAgentMessage(ctx context.Context, command core.ReserveAgentMessageCommand) (bool, error) {
	if command.Attempt.LogicalStepID == s.step && s.before != nil {
		before := s.before
		s.before = nil
		if err := before(ctx); err != nil {
			return false, err
		}
	}
	return s.AgentAttemptStore.ReserveClearDevAgentMessage(ctx, command)
}

func TestBuilderSessionRecheckAdmissionRaceCanRetrySameUnsentAttempt(t *testing.T) {
	ctx := context.Background()
	f, h, id, before, original := newBuilderSessionRecheckFixture(t)
	input := builderSessionRecheckInput(t, f, id, "admission-race")
	restore := func(ctx context.Context, sid domain.SessionID) (string, error) {
		record, _, err := f.store.GetSession(ctx, sid)
		if err != nil {
			return "", err
		}
		record.Activity.State = domain.ActivityIdle
		return "native", f.store.UpdateSession(ctx, record)
	}
	f.s.restoreOriginalAgentSession = restore
	stepID := before.Dispatches[1].AgentStepID
	f.s.attempts = &recheckReservationInterception{AgentAttemptStore: f.store, step: stepID, before: func(ctx context.Context) error {
		record, _, err := f.store.GetSession(ctx, original.ID)
		if err != nil {
			return err
		}
		record.Activity.State = domain.ActivityActive
		return f.store.UpdateSession(ctx, record)
	}}
	calls := len(h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	stoppedWorkflow(t, f, id)
	assertBuilderRecheckFailure(t, f, id, input.RequestID, "DELIVERY", "BUILDER_RECHECK_REQUIRED")
	if len(h.relays) != calls {
		t.Fatal("transaction admission sent to the now-busy session")
	}
	attempts, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, stepID)
	if err != nil || len(attempts) != 1 || attempts[0].LastEventID != "" {
		t.Fatalf("failed reservation did not preserve the single unreserved attempt: %+v %v", attempts, err)
	}
	if _, err := restore(ctx, original.ID); err != nil {
		t.Fatal(err)
	}
	retry := builderSessionRecheckInput(t, f, id, "admission-race-fixed")
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, retry); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	attemptsAfter, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, stepID)
	if err != nil || len(attemptsAfter) != 1 || attemptsAfter[0].ID != attempts[0].ID || attemptsAfter[0].SendStatus != core.AgentAttemptCompleted || after.Run.CompletedAt == nil {
		t.Fatalf("recheck did not reuse original attempt: %+v %v", attemptsAfter, err)
	}
}

func TestBuilderSessionRecheckOtherActionsStillRequireContext(t *testing.T) {
	f, _, id, before, _ := newBuilderSessionRecheckFixture(t)
	for _, action := range []string{core.RecoveryContinueBuilder, core.RecoveryRetryCheck, core.RecoveryRetryReview, core.RecoveryRetryStage, "UNKNOWN"} {
		if _, err := f.s.RequestWorkflowRecovery(context.Background(), id, WorkflowRecoveryInput{RequestID: "empty-other", ExecutionRunID: before.Run.ID, Action: action, TargetID: "target"}); err == nil {
			t.Fatalf("blank technical recheck broadened %s", action)
		}
	}
	if builderSessionRestoreReason(fmt.Errorf("wrapped: %w; token=secret", ports.ErrChatAuthRequired)) != "LOGIN_REQUIRED" {
		t.Fatal("typed cause was lost behind arbitrary wrapped text")
	}
}
