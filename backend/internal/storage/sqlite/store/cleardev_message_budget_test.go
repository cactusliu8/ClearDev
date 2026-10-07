package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func messageBudgetFixture(t *testing.T) (*sqlite.Store, string, core.AgentStepAttempt) {
	t.Helper()
	dir := t.TempDir()
	s, err := sqlitetest.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	seedClearDevAO(t, s, "message-project")
	if err := s.CreateClearDevRequirement(context.Background(), initialRequirement("message-requirement", "message-project", now)); err != nil {
		t.Fatal(err)
	}
	a := core.AgentStepAttempt{ID: "attempt-1", DevelopmentRequirementID: "message-requirement", LogicalStepID: "step", StepCategory: core.AgentStepCategoryStandard, StepKind: core.AgentStepBuilderResult, AttemptNumber: 1, RoleBindingID: "binding", AOSessionID: "session", ClientMessageID: "message", PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation}
	if _, _, err := s.EnsureClearDevAgentStepAttempt(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return s, dir, a
}
func budgetCommand(a core.AgentStepAttempt, source core.AgentMessageSource) core.ReserveAgentMessageCommand {
	message := a.ClientMessageID
	if source == core.AgentMessageParseCorrection {
		message += ":parse-correction"
	}
	return core.ReserveAgentMessageCommand{Attempt: a, Source: source, Boundary: core.AgentAttemptEvent{ID: message + ":boundary", AttemptID: a.ID, Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: message, PromptSHA256: a.PromptSHA256, FailureCategory: domain.AgentFailureDeliveryUnknown, ErrorSummary: "delivery unknown", RecordedAt: a.RequestedAt}}
}
func TestClearDevMessageBudgetAtomicReservationConfirmationAndConflict(t *testing.T) {
	ctx := context.Background()
	s, dir, a := messageBudgetFixture(t)
	command := budgetCommand(a, core.AgentMessageOriginal)
	var created atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := s.ReserveClearDevAgentMessage(ctx, command)
			if claimed {
				created.Add(1)
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
	if created.Load() != 1 {
		t.Fatalf("claims=%d", created.Load())
	}
	view, err := s.GetClearDevMessageBudget(ctx, a.DevelopmentRequirementID)
	if err != nil || *view.Steps[0].ReservedMessages != 1 || *view.Steps[0].ConfirmedSentMessages != 0 {
		t.Fatalf("reserved without confirmed send: %#v err=%v", view, err)
	}
	e := command.Boundary
	e.ID = "sent"
	e.Status = core.AgentAttemptSent
	e.TurnID = "turn"
	e.FailureCategory = ""
	e.ErrorSummary = ""
	for range 2 {
		if err := s.ConfirmClearDevAgentMessage(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	db := openClearDevRawDB(t, dir)
	defer func() { _ = db.Close() }()
	for _, table := range []string{"cleardev_agent_message_reservations", "cleardev_agent_message_confirmations"} {
		for _, statement := range []string{"DELETE FROM " + table, "UPDATE " + table + " SET client_message_id=client_message_id"} {
			if _, err := db.Exec(statement); err == nil {
				t.Fatal("message fact was mutable")
			}
		}
	}
	empty := e
	empty.TurnID = ""
	if err := s.ConfirmClearDevAgentMessage(ctx, empty); err == nil {
		t.Fatal("empty turn confirmation accepted")
	}
	e.TurnID = "other"
	if err := s.ConfirmClearDevAgentMessage(ctx, e); err == nil {
		t.Fatal("changed confirmed turn")
	}
	for _, change := range []func(*core.ReserveAgentMessageCommand){func(c *core.ReserveAgentMessageCommand) { c.Boundary.PromptSHA256 = strings.Repeat("b", 64) }, func(c *core.ReserveAgentMessageCommand) { c.Source = core.AgentMessageRecoveryOriginal }, func(c *core.ReserveAgentMessageCommand) { c.Attempt.AOSessionID = "other" }, func(c *core.ReserveAgentMessageCommand) { c.Boundary.ClientMessageID = "other" }} {
		bad := command
		change(&bad)
		if _, err := s.ReserveClearDevAgentMessage(ctx, bad); err == nil {
			t.Fatal("message identity conflict accepted")
		}
	}
	view, err = s.GetClearDevMessageBudget(ctx, a.DevelopmentRequirementID)
	if err != nil || *view.Steps[0].ReservedMessages != 1 || *view.Steps[0].ConfirmedSentMessages != 1 {
		t.Fatalf("replay counters=%#v err=%v", view, err)
	}
}
func TestClearDevMessageBudgetTransactionsRollback(t *testing.T) {
	for _, operation := range []string{"reserve", "confirm", "root"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			s, dir, a := messageBudgetFixture(t)
			db := openClearDevRawDB(t, dir)
			defer func() { _ = db.Close() }()
			command := budgetCommand(a, core.AgentMessageOriginal)
			if operation == "confirm" {
				if _, err := s.ReserveClearDevAgentMessage(ctx, command); err != nil {
					t.Fatal(err)
				}
			}
			table := "cleardev_agent_attempt_events"
			if operation == "root" {
				table = "cleardev_message_budget_versions"
			}
			if _, err := db.Exec("CREATE TRIGGER message_budget_test_failure BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'injected write failure'); END"); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "root":
				if err := s.CreateClearDevRequirement(ctx, initialRequirement("new-root", "message-project", a.RequestedAt)); err == nil {
					t.Fatal("root insert ignored failing version write")
				}
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM cleardev_development_projects WHERE id='new-root'`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("partial root=%d err=%v", n, err)
				}
			case "reserve":
				if claimed, err := s.ReserveClearDevAgentMessage(ctx, command); err == nil || claimed {
					t.Fatal("partial reservation accepted")
				}
			case "confirm":
				e := command.Boundary
				e.ID = "sent"
				e.Status = core.AgentAttemptSent
				e.TurnID = "turn"
				e.FailureCategory = ""
				e.ErrorSummary = ""
				if err := s.ConfirmClearDevAgentMessage(ctx, e); err == nil {
					t.Fatal("partial confirmation accepted")
				}
			}
			view, err := s.GetClearDevMessageBudget(ctx, a.DevelopmentRequirementID)
			if err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if operation == "confirm" {
				want = 1
			}
			if *view.Steps[0].ReservedMessages != want || *view.Steps[0].ConfirmedSentMessages != 0 {
				t.Fatalf("rollback=%#v", view)
			}
		})
	}
}
func TestClearDevMessageBudgetMissingAndLegacyVersionRejectNewMessage(t *testing.T) {
	for _, version := range []string{"LEGACY_UNMEASURED", "missing"} {
		t.Run(version, func(t *testing.T) {
			ctx := context.Background()
			s, dir, a := messageBudgetFixture(t)
			db := openClearDevRawDB(t, dir)
			defer func() { _ = db.Close() }()
			// Deliberately malformed/legacy fixture; production triggers are checked separately.
			if _, err := db.Exec(`DROP TRIGGER cleardev_message_budget_versions_update_forbidden; DROP TRIGGER cleardev_message_budget_versions_delete_forbidden`); err != nil {
				t.Fatal(err)
			}
			statement := `UPDATE cleardev_message_budget_versions SET version='LEGACY_UNMEASURED'`
			if version == "missing" {
				statement = `DELETE FROM cleardev_message_budget_versions`
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			claimed, err := s.ReserveClearDevAgentMessage(ctx, budgetCommand(a, core.AgentMessageOriginal))
			var rule *core.RuleError
			if claimed || !errors.As(err, &rule) || rule.Code != core.ReasonMessageBudgetUnknown {
				t.Fatalf("legacy claim=%v err=%v", claimed, err)
			}
			view, err := s.GetClearDevMessageBudget(ctx, a.DevelopmentRequirementID)
			if err != nil || view.Steps[0].ReservedMessages != nil || view.Steps[0].ConfirmedSentMessages != nil || view.Steps[0].RemainingMessages != nil || view.UnknownReason == "" {
				t.Fatalf("unknown counts=%#v err=%v", view, err)
			}
		})
	}
}

func TestClearDevMessageBudgetCannotHideExceptionOccupancy(t *testing.T) {
	s, state := seedControlledExceptionFlow(t)
	ctx := context.Background()
	snapshot, _, err := s.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	for index, b := range snapshot.Exception.Budgets {
		stepID := fmt.Sprintf("orphan-step-%d", index)
		if _, err := s.OccupyClearDevComplexExceptionBudget(ctx, b.ID, stepID, stepID, state.now); err != nil {
			t.Fatal(err)
		}
		now := state.now
		a := core.AgentStepAttempt{ID: stepID + ":attempt:1", DevelopmentRequirementID: state.prep.RequirementID, LogicalStepID: stepID, StepCategory: core.AgentStepCategoryStandard, StepKind: core.AgentStepBuilderResult, AttemptNumber: 1, RoleBindingID: "forged", AOSessionID: "forged", ClientMessageID: stepID, PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation}
		if _, _, err := s.EnsureClearDevAgentStepAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReserveClearDevAgentMessage(ctx, budgetCommand(a, core.AgentMessageOriginal)); err == nil {
			t.Fatalf("role %s escaped via STANDARD", b.RoleKind)
		}
	}
}

func TestClearDevMessageBudgetStepSourcesCannotBeRefilled(t *testing.T) {
	ctx := context.Background()
	s, _, a := messageBudgetFixture(t)
	for _, source := range []core.AgentMessageSource{core.AgentMessageOriginal, core.AgentMessageParseCorrection} {
		if _, err := s.ReserveClearDevAgentMessage(ctx, budgetCommand(a, source)); err != nil {
			t.Fatal(err)
		}
	}
	failure := core.AgentAttemptEvent{ID: "retry-failure", AttemptID: a.ID, Status: core.AgentAttemptFailed, ClientMessageID: a.ClientMessageID, TurnID: "turn", TurnState: domain.TurnStateFailed, FailureCategory: domain.AgentFailureProviderUnavailable, Retryable: true, RecordedAt: a.RequestedAt}
	if err := s.RecordClearDevAgentAttemptEvent(ctx, failure); err != nil {
		t.Fatal(err)
	}
	second := a
	second.ID = "attempt-2"
	second.AttemptNumber = 2
	second.ClientMessageID += ":attempt:2"
	second.TriggerFailureEventID = failure.ID
	if _, _, err := s.EnsureClearDevAgentStepAttempt(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveClearDevAgentMessage(ctx, budgetCommand(second, core.AgentMessageRecoveryOriginal)); err != nil {
		t.Fatal(err)
	}
	for _, command := range []core.ReserveAgentMessageCommand{budgetCommand(second, core.AgentMessageParseCorrection), budgetCommand(second, core.AgentMessageOriginal)} {
		if ok, err := s.ReserveClearDevAgentMessage(ctx, command); err == nil || ok {
			t.Fatal("fourth message/source refill accepted")
		}
	}
	different := budgetCommand(second, core.AgentMessageRecoveryOriginal)
	different.Boundary.ClientMessageID = "new-id-at-cap"
	if ok, err := s.ReserveClearDevAgentMessage(ctx, different); err == nil || ok {
		t.Fatal("different message bypassed exhausted step")
	}
	view, err := s.GetClearDevMessageBudget(ctx, a.DevelopmentRequirementID)
	if err != nil || *view.Steps[0].ReservedSteps != 1 || *view.Steps[0].ReservedMessages != 3 || *view.Steps[0].RemainingMessages != 0 {
		t.Fatalf("view=%#v err=%v", view, err)
	}
}

func TestClearDevMessageBudgetRoleCapsAndBinding(t *testing.T) {
	for _, role := range []string{core.ComplexExceptionBudgetBuilder, core.ComplexExceptionBudgetReviewer, core.ComplexExceptionBudgetStewardException, core.ComplexExceptionBudgetSpecialist, core.ComplexExceptionBudgetRecovery} {
		t.Run(role, func(t *testing.T) {
			ctx := context.Background()
			s, state := seedControlledExceptionFlow(t)
			snapshot, _, err := s.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
			if err != nil {
				t.Fatal(err)
			}
			var budget core.ComplexExceptionBudget
			for _, b := range snapshot.Exception.Budgets {
				if b.RoleKind == role && (b.ComplexExecutionTaskID == state.tasks[0].ID || role == core.ComplexExceptionBudgetRecovery) {
					budget = b
					break
				}
			}
			if budget.ID == "" {
				t.Fatal("missing role budget")
			}
			step, category, session := messageBudgetRoleStep(t, s, state, role)
			if _, err := s.OccupyClearDevComplexExceptionBudget(ctx, budget.ID, step.ID, step.ID, state.now); err != nil {
				t.Fatal(err)
			}
			now := state.now
			a := core.AgentStepAttempt{ID: step.ID + ":attempt:1", DevelopmentRequirementID: state.prep.RequirementID, LogicalStepID: step.ID, StepCategory: category, StepKind: step.Kind, AttemptNumber: 1, RoleBindingID: step.RoleBindingID, AOSessionID: session, ClientMessageID: step.ClientMessageID, PromptSHA256: step.PromptSHA256, RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation}
			if _, _, err := s.EnsureClearDevAgentStepAttempt(ctx, a); err != nil {
				t.Fatal(err)
			}
			// Seed already reserved, distinct logical steps to isolate the role cap from this step's cap.
			db := openClearDevRawDB(t, state.dataDir)
			defer func() { _ = db.Close() }()
			for n := 0; n < budget.MaxTurns*5-1; n++ {
				if _, err := db.Exec(`INSERT INTO cleardev_agent_message_reservations(client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,budget_id,reserved_at) VALUES (?,?,'MESSAGE_BUDGET_V1',?,?,'ORIGINAL',?,?,?,?)`, fmt.Sprintf("previous-%d", n), a.DevelopmentRequirementID, fmt.Sprintf("previous-step-%d", n), a.ID, a.AOSessionID, a.PromptSHA256, budget.ID, now); err != nil {
					t.Fatal(err)
				}
			}
			if ok, err := s.ReserveClearDevAgentMessage(ctx, budgetCommand(a, core.AgentMessageOriginal)); err != nil || !ok {
				t.Fatalf("last role slot: %v %v", ok, err)
			}
			if ok, err := s.ReserveClearDevAgentMessage(ctx, budgetCommand(a, core.AgentMessageParseCorrection)); err == nil || ok {
				t.Fatal("role cap bypassed by correction")
			} else {
				var rule *core.RuleError
				if !errors.As(err, &rule) || rule.Code != core.ReasonMessageBudgetExhausted {
					t.Fatalf("expected cap refusal: %v", err)
				}
			}
			view, err := s.GetClearDevMessageBudget(ctx, a.DevelopmentRequirementID)
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range view.Roles {
				if u.BudgetID == budget.ID && (*u.MaxMessages != int64(budget.MaxTurns*5) || *u.ReservedMessages != *u.MaxMessages || *u.RemainingMessages != 0) {
					t.Fatalf("role counters=%#v", u)
				}
			}
			// Corrupt only the occupancy link to prove replay cannot use a different task/role budget.
			var other string
			for _, b := range snapshot.Exception.Budgets {
				if b.ID != budget.ID {
					other = b.ID
					break
				}
			}
			triggers, err := func() ([]string, error) {
				rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name='cleardev_complex_exception_budget_occupancies'`)
				if err != nil {
					return nil, err
				}
				defer func() { _ = rows.Close() }()
				var names []string
				for rows.Next() {
					var name string
					if err := rows.Scan(&name); err != nil {
						return nil, err
					}
					names = append(names, name)
				}
				return names, rows.Err()
			}()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range triggers {
				if _, err := db.Exec(`DROP TRIGGER "` + name + `"`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`UPDATE cleardev_complex_exception_budget_occupancies SET budget_id=? WHERE agent_step_id=?`, other, step.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReserveClearDevAgentMessage(ctx, budgetCommand(a, core.AgentMessageOriginal)); err == nil {
				t.Fatal("cross-role/task occupancy accepted")
			}
			t.Logf("%s: cap=%d, last slot accepted, next distinct message denied; changed role/task denied", role, budget.MaxTurns*5)
		})
	}
}

func messageBudgetRoleStep(t *testing.T, s *sqlite.Store, state complexExecutionFlowState, role string) (core.AgentStep, core.AgentStepCategory, string) {
	t.Helper()
	ctx := context.Background()
	task := state.tasks[0]
	step := core.AgentStep{ID: "budget-step", RequestID: "budget-request", ClientMessageID: "budget-message", PromptSHA256: strings.Repeat("a", 64), RequestedAt: state.now}
	if role == core.ComplexExceptionBudgetSpecialist || role == core.ComplexExceptionBudgetRecovery {
		mode := core.ComplexOnDemandModeSpecialist
		step.Kind = core.ComplexExceptionAgentStepSpecialist
		if role == core.ComplexExceptionBudgetRecovery {
			mode = core.ComplexOnDemandModeRecovery
			step.Kind = core.ComplexExceptionAgentStepRecovery
		}
		binding := core.ComplexOnDemandBinding{ID: "budget-ondemand", ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, Mode: mode, SessionCreationIdempotencyKey: "budget-ondemand-key", Status: core.RoleBindingStatusRequested, RequestedAt: state.now}
		if _, _, err := s.CreateClearDevComplexExceptionOnDemandBinding(ctx, binding); err != nil {
			t.Fatal(err)
		}
		if _, err := s.BindClearDevComplexExceptionOnDemand(ctx, binding.ID, state.builder.AOSessionID, state.builder.WorkspacePath, state.initialBase, state.now); err != nil {
			t.Fatal(err)
		}
		step.RoleBindingID = binding.ID
		if _, _, err := s.CreateClearDevComplexExceptionAgentStep(ctx, state.run.ID, "", binding.ID, step); err != nil {
			t.Fatal(err)
		}
		return step, core.AgentStepCategoryException, state.builder.AOSessionID
	}
	dispatch, _, err := dispatchComplexExecutionTask(ctx, s, state, task, 0, state.initialBase, "budget-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if role == core.ComplexExceptionBudgetBuilder {
		snapshot, _, err := s.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range snapshot.AgentSteps {
			if item.ID == dispatch.AgentStepID {
				return item, core.AgentStepCategoryComplexExecution, state.builder.AOSessionID
			}
		}
		t.Fatal("missing builder step")
	}
	if role == core.ComplexExceptionBudgetStewardException {
		request, scope, _, _ := exceptionScopeApproval(state, task, dispatch, "budget-scope")
		if err := s.RecordClearDevComplexExceptionScopeRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.CreateClearDevComplexExceptionAgentStep(ctx, state.run.ID, scope.RoleBindingID, "", scope); err != nil {
			t.Fatal(err)
		}
		return scope, core.AgentStepCategoryException, state.prep.StewardSessionID
	}
	candidateID := "budget-candidate"
	candidateSHA := strings.Repeat("c", 40)
	if _, err := s.AppendClearDevComplexExecutionCandidate(ctx, core.AppendComplexExecutionCandidateCommand{ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, BaseCommitSHA: state.initialBase, Candidate: core.CandidateObservation{ID: candidateID, DevelopmentTaskID: task.DevelopmentTaskID, AOSessionID: state.builder.AOSessionID, CommitSHA: candidateSHA, ObservedAt: state.now}}); err != nil {
		t.Fatal(err)
	}
	reviewer := core.ComplexExecutionRoleBinding{ID: "budget-reviewer", ExecutionRunID: state.run.ID, Role: core.StandardRoleReviewer, TaskMappingID: task.ID, CandidateCommitID: candidateID, SessionCreationIdempotencyKey: "budget-reviewer-key", Status: core.RoleBindingStatusRequested, RequestedAt: state.now}
	if _, _, err := s.CreateClearDevComplexExecutionRoleBinding(ctx, reviewer); err != nil {
		t.Fatal(err)
	}
	session := createComplexExecutionSession(t, s, state.projectID, "budget-review-session", "budget-reviewer-key", "/worktrees/budget-reviewer", state.initialBase, state.now)
	if _, err := s.BindClearDevComplexExecutionRoleBinding(ctx, reviewer.ID, string(session.ID), "/worktrees/budget-reviewer", candidateSHA, state.now); err != nil {
		t.Fatal(err)
	}
	reviewer.AOSessionID = string(session.ID)
	reviewer.WorkspacePath = "/worktrees/budget-reviewer"
	reviewer.BaseCommitSHA = candidateSHA
	reviewer.Status = core.RoleBindingStatusBound
	reviewer.BoundAt = &state.now
	step.SendStatus = core.AgentStepSendStatusPending
	step.RoleBindingID = reviewer.ID
	step.Kind = core.AgentStepLocalReview
	review := core.ComplexExecutionReview{ID: step.RequestID, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: candidateID, CandidateCommitSHA: candidateSHA, BaseCommitSHA: state.initialBase, ReviewPacketJSON: "{}", ReviewPacketSHA256: strings.Repeat("e", 64), CandidateWorkspacePath: reviewer.WorkspacePath, ReviewerRoleBindingID: reviewer.ID, AgentStepID: step.ID, Status: core.LocalReviewStatusPending, CreatedAt: state.now}
	if _, _, err := s.CreateClearDevComplexExecutionReview(ctx, core.CreateComplexExecutionReviewCommand{Review: review, ReviewerBinding: reviewer, AgentStep: step}); err != nil {
		t.Fatal(err)
	}
	return step, core.AgentStepCategoryComplexExecution, string(session.ID)
}

func TestClearDevMessageBudgetRoleBudgetsSurviveRefusedHistoricalDowngrade(t *testing.T) {
	ctx := context.Background()
	s, state := seedControlledExceptionFlow(t)
	before, _, err := s.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range before.Exception.Budgets {
		if _, err := s.OccupyClearDevComplexExceptionBudget(ctx, b.ID, fmt.Sprintf("old-occupied-%d", i), fmt.Sprintf("old-occupied-%d", i), state.now); err != nil {
			t.Fatal(err)
		}
	}
	before, _, err = s.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	budgetBefore, err := s.GetClearDevMessageBudget(ctx, state.prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := openClearDevRawDB(t, state.dataDir)
	// Existing execution history cannot be downgraded past contract admission.
	// Verify rejection preserves occupied budgets rather than disabling that
	// guard to simulate an older database. The genuine pre-124 upgrade and
	// LEGACY_UNMEASURED semantics are covered by
	// TestClearDevMessageBudgetMigrationPreservesUnknownUsage in package sqlite.
	var beforeVersion, afterVersion int64
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownTo(db, "migrations", 123); err == nil {
		t.Fatal("downgrade erased protected execution history")
	}
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&afterVersion); err != nil || beforeVersion != afterVersion {
		t.Fatalf("refused downgrade changed migration version: before=%d after=%d err=%v", beforeVersion, afterVersion, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(state.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	after, _, err := reopened.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Exception.Budgets, after.Exception.Budgets) {
		t.Fatal("historical step budget changed")
	}
	view, err := reopened.GetClearDevMessageBudget(ctx, state.prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(budgetBefore, view) {
		t.Fatal("refused downgrade or reopen changed the message-budget interpretation")
	}
	for _, u := range view.Roles {
		if u.ReservedSteps == nil || *u.ReservedSteps != 1 {
			t.Fatalf("occupied historical role budget=%#v", u)
		}
	}
}
