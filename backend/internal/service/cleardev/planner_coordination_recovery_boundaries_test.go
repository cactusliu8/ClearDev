package cleardev

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func coordinationFixtureDB(t *testing.T, f *projectPlanningFixture) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestCoordinationRecoveryMissingOrLateTerminalArchiveRefuses(t *testing.T) {
	for _, mode := range []string{"missing", "history-only", "wrong-turn", "after-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, h, _ := newStoppedCoordinationFixtureMode(t, true)
			f.s.runBackground = func(func()) {}
			input := coordinationRecoveryInput(t, f, h)
			state, err := f.store.ReadClearDevPlannerCoordinationRecovery(ctx, h.requirementID, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			// Only this isolated test archive is changed, to prove that an ended
			// projection or an imported history event is not sufficient evidence.
			db := coordinationFixtureDB(t, f)
			query := "DELETE FROM conversation_provider_events WHERE id=?"
			switch mode {
			case "history-only":
				query = "UPDATE conversation_provider_events SET provider_event_id='acp-history:only' WHERE id=?"
			case "wrong-turn":
				query = "UPDATE conversation_provider_events SET payload_json=json_set(payload_json,'$.providerTurnId','another-turn') WHERE id=?"
			case "after-failure":
				query = "UPDATE conversation_provider_events SET received_at='2099-01-01T00:00:00Z' WHERE id=?"
			}
			if _, err := db.Exec(query, state.Binding.TerminalEventID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err == nil {
				t.Fatal("unsupported no-result recovery accepted", mode)
			}
		})
	}
}

func TestCoordinationRecoveryHistoryAndSecondIdentityAreImmutable(t *testing.T) {
	ctx := context.Background()
	f, h, before := newStoppedCoordinationFixture(t)
	f.s.runBackground = func(func()) {}
	input := coordinationRecoveryInput(t, f, h)
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	db := coordinationFixtureDB(t, f)
	for _, query := range []string{
		"UPDATE cleardev_planner_runtime_recoveries SET supplement='changed' WHERE id=?",
		"DELETE FROM cleardev_planner_runtime_recoveries WHERE id=?",
		"INSERT OR REPLACE INTO cleardev_planner_runtime_recoveries SELECT * FROM cleardev_planner_runtime_recoveries WHERE id=?",
	} {
		if _, err := db.Exec(query, input.RequestID); err == nil {
			t.Fatal("history mutation succeeded", query)
		}
	}
	step := before.PlannerRuntime.Requests[0].AgentStepID
	if _, err := db.Exec("INSERT OR REPLACE INTO cleardev_agent_step_attempts SELECT * FROM cleardev_agent_step_attempts WHERE id=?", step+":attempt:2"); err == nil {
		t.Fatal("second attempt replaced")
	}
	if _, err := db.Exec(`INSERT INTO cleardev_agent_step_attempts(id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,role_binding_id,ao_session_id,client_message_id,prompt_sha256,trigger_failure_event_id,requested_at,created_at,requested_at_semantics)
 SELECT id||':third',development_project_id,logical_step_id,step_category,step_kind,3,role_binding_id,ao_session_id,client_message_id||':third',prompt_sha256,trigger_failure_event_id,requested_at,created_at,requested_at_semantics FROM cleardev_agent_step_attempts WHERE id=?`, step+":attempt:2"); err == nil {
		t.Fatal("recovery manufactured a third attempt")
	}
	var barriers int
	if err := db.QueryRow("SELECT count(*) FROM cleardev_planner_runtime_barriers WHERE execution_run_id=?", before.Run.ID).Scan(&barriers); err != nil || barriers != 1 {
		t.Fatal("SQL barrier opened before successor decision", barriers, err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil || !reflect.DeepEqual(before.PlannerRuntime.Decisions, after.PlannerRuntime.Decisions) {
		t.Fatal("old STOP changed", err)
	}
}

type coordinationRecoveryCrashStore struct {
	*sqlite.Store
	boundary string
	fired    bool
}

func (s *coordinationRecoveryCrashStore) MarkClearDevComplexAgentStepSent(ctx context.Context, id string, at time.Time) (bool, error) {
	if strings.HasSuffix(id, ":planner") && s.boundary == "receipt" && !s.fired {
		s.fired = true
		return false, errAutoInjectedCrash
	}
	return s.Store.MarkClearDevComplexAgentStepSent(ctx, id, at)
}
func (s *coordinationRecoveryCrashStore) SettleClearDevComplexAgentStep(ctx context.Context, step core.AgentStep) (bool, error) {
	changed, err := s.Store.SettleClearDevComplexAgentStep(ctx, step)
	if err == nil && changed && strings.HasSuffix(step.ID, ":planner") && s.boundary == "settlement" && !s.fired {
		s.fired = true
		return changed, errAutoInjectedCrash
	}
	return changed, err
}
func (s *coordinationRecoveryCrashStore) ApplyClearDevPlannerRuntime(ctx context.Context, id string, at time.Time) (bool, error) {
	changed, err := s.Store.ApplyClearDevPlannerRuntime(ctx, id, at)
	if err == nil && changed && s.boundary == "application" && !s.fired {
		s.fired = true
		return changed, errAutoInjectedCrash
	}
	return changed, err
}

func TestCoordinationRecoveryRestartDoesNotRepeatAcceptedMessage(t *testing.T) {
	for _, boundary := range []string{"registration", "receipt", "settlement", "application"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			f, h, before := newStoppedCoordinationFixture(t)
			f.s.runBackground = func(func()) {}
			input := coordinationRecoveryInput(t, f, h)
			if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
				t.Fatal(err)
			}
			h.conv.mu.Lock()
			h.conv.fail = false
			h.conv.mu.Unlock()
			crash := &coordinationRecoveryCrashStore{Store: f.store, boundary: boundary}
			f.s.complex, f.s.complexExecution = crash, crash
			if boundary != "registration" {
				for range 15 {
					_, _, err := f.s.advanceComplexStandardExecution(ctx, h.requirementID)
					if crash.fired {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if !crash.fired {
					t.Fatal("crash boundary not reached")
				}
			}
			// Reopen the actual database and rebuild the control-plane service. The
			// provider test double survives, so a lost local acknowledgement cannot
			// turn the accepted original message into another native send.
			reopened, err := sqlite.Open(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			f.store = reopened
			f.service()
			h.store = reopened
			f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h
			f.s.resultPreview = h.trial
			f.s.finalReviews = f.store
			h.service = f.s
			f.s.runBackground = func(func()) {}
			f.s.stepTimeout = 5 * time.Second
			for range 80 {
				x, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
				if err != nil {
					t.Fatal(err)
				}
				if x.Run.CompletedAt != nil {
					if len(x.PlannerRuntime.Recoveries) != 1 || len(x.PlannerRuntime.RecoveryDecisions) != 1 || !reflect.DeepEqual(before.PlannerRuntime.Decisions, x.PlannerRuntime.Decisions) {
						t.Fatal("restart changed recovery history")
					}
					h.conv.mu.Lock()
					defer h.conv.mu.Unlock()
					if len(h.conv.sent) != 2 {
						t.Fatal("restart repeated an accepted Planner message", len(h.conv.sent))
					}
					return
				}
				_, _, err = f.s.advanceComplexStandardExecution(ctx, h.requirementID)
				if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
					t.Fatal(err)
				}
			}
			x, _, _ := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
			phase, reason := core.DeriveComplexExecutionPhase(x)
			states, _ := f.store.ListClearDevAgentStepAttemptStates(ctx, h.requirementID, before.PlannerRuntime.Requests[0].AgentStepID)
			proof, _, proofErr := f.store.ReadClearDevPlannerCoordinationRecoveryBeforeSend(ctx, h.requirementID, before.PlannerRuntime.Requests[0].AgentStepID, time.Now().UTC())
			ready := f.s.plannerCoordinationRecoveryReady(ctx, proof, true)
			t.Fatalf("restarted coordination: phase=%s reason=%s store=%v ready=%s decisions=%+v states=%+v", phase, reason, proofErr, ready, x.PlannerRuntime.RecoveryDecisions, states)
		})
	}
}
