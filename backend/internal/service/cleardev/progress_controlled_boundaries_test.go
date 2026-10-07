package cleardev_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type controlledReadChecker struct {
	retry   *time.Time
	failure error
	calls   int
}

func (c *controlledReadChecker) CheckControlledPreflight(context.Context, domain.AgentHarness, string) (ports.ChatControlledPreflight, error) {
	c.calls++
	return ports.ChatControlledPreflight{RetryAt: c.retry}, c.failure
}

func TestClearDevControlledProgressStoredPreflight(t *testing.T) {
	for _, login := range []bool{false, true} {
		t.Run(map[bool]string{false: "quota", true: "login"}[login], func(t *testing.T) {
			store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
			now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
			later := now.Add(time.Hour)
			checker := &controlledReadChecker{retry: &later, failure: ports.ErrChatQuotaExhausted}
			if login {
				checker.failure = ports.ErrChatAuthRequired
			}
			deps.Clock = func() time.Time { return now }
			deps.ControlledPreflightChecker = checker
			svc := cleardevsvc.New(deps)
			_, _ = svc.StartComplexStandardExecution(context.Background(), id)
			p, ok, err := store.GetLatestClearDevControlledPreflight(context.Background(), id)
			if err != nil || !ok || p.Outcome != core.ControlledPreflightFailed {
				t.Fatal("missing durable failed preflight")
			}
			expected := "WAITING_RETRY"
			if login {
				expected = "AWAITING_USER"
			}
			calls, sends := checker.calls, h.sends
			for _, advance := range []bool{false, true} {
				if advance {
					now = later
					if !login {
						expected = "RETRY_ELIGIBLE"
					}
				}
				v, err := svc.GetRequirement(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, w := range v.TrustedProgress.ControlledWork {
					if w.State == expected && w.PreflightID == p.ID {
						found = true
					}
				}
				if !found {
					t.Fatalf("want %s: %+v", expected, v.TrustedProgress)
				}
				controlledProgressHTTPCLI(t, svc, id, v.Requirement.AOProjectID, v.TrustedProgress.FactSummarySHA256)
			}
			if calls != checker.calls || sends != h.sends || h.actualCreates != 0 {
				t.Fatal("reading time boundary invoked preflight/send/create")
			}
		})
	}
}

func TestClearDevControlledProgressZeroBudgetProcessesExistingMessage(t *testing.T) {
	store, h, deps, id, data := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
	h.failKind = "never-fail"
	reads := &controlledAttemptReads{Store: store}
	deps.AgentAttempts = reads
	svc := cleardevsvc.New(deps)
	seen := false
	reads.afterConfirm = func(event core.AgentAttemptEvent) {
		if seen {
			return
		}
		a, err := store.GetClearDevAgentAttemptState(context.Background(), id, event.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if a.StepKind != core.ComplexExecutionAgentStepBuilderTask {
			return
		}
		seen = true
		e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		var budget core.ComplexExceptionBudget
		for _, b := range e.Exception.Budgets {
			if b.RoleKind == core.ComplexExceptionBudgetBuilder && b.ComplexExecutionTaskID == e.Tasks[0].ID {
				budget = b
			}
		}
		db, err := sql.Open("sqlite", "file:"+filepath.Join(data, "ao.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM cleardev_agent_message_reservations WHERE budget_id=?`, budget.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		for n := count; n < budget.MaxTurns*5; n++ {
			_, err = db.Exec(`INSERT INTO cleardev_agent_message_reservations(client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,budget_id,reserved_at) VALUES (?,?,'MESSAGE_BUDGET_V1',?,?,'ORIGINAL',?,?,?,?)`, fmt.Sprintf("fixture-reservation-%d", n), id, fmt.Sprintf("fixture-step-%d", n), a.ID, a.AOSessionID, a.PromptSHA256, budget.ID, event.RecordedAt)
			if err != nil {
				t.Fatal(err)
			}
		}
		v, err := svc.GetRequirement(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, w := range v.TrustedProgress.ControlledWork {
			if w.AttemptID == a.ID && w.State == "OBSERVING" && w.RoleBudget != nil && w.RoleBudget.RemainingMessages != nil && *w.RoleBudget.RemainingMessages == 0 {
				found = true
			}
		}
		if !found {
			t.Fatalf("zero budget blocked existing message: %+v", v.TrustedProgress)
		}
		controlledProgressHTTPCLI(t, svc, id, v.Requirement.AOProjectID, v.TrustedProgress.FactSummarySHA256)
		event.ID += ":fixture-observation-timeout"
		event.Status = core.AgentAttemptObservationTimedOut
		event.FailureCategory = domain.AgentFailureObservationTimeout
		if err = store.RecordClearDevAgentAttemptEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		timed, err := svc.GetRequirement(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		found = false
		for _, w := range timed.TrustedProgress.ControlledWork {
			if w.AttemptID == a.ID && w.State == "OBSERVING" && w.EvidenceID == event.ID {
				found = true
			}
		}
		if !found {
			t.Fatal("persisted observation timeout changed into a new attempt or budget block")
		}
	}
	for wake := 0; wake < 4; wake++ {
		if _, err := svc.StartComplexStandardExecution(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		e, _, _ := store.GetClearDevComplexExecution(context.Background(), id)
		if e.Run.CompletedAt != nil {
			break
		}
	}
	v, err := svc.GetRequirement(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !seen || v.TrustedProgress.Phase != core.TrustedPhaseCompleted || h.restores != 0 {
		t.Fatalf("normal path after preseeded budget: seen=%v phase=%s restores=%d", seen, v.TrustedProgress.Phase, h.restores)
	}
	t.Logf("preseeded exhausted role ledger; original successful message processed to DONE; actual sends=%d restores=%d", h.sends, h.restores)
}
