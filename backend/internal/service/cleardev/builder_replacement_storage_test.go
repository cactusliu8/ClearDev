package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

var builderReplacementFactTables = []string{"cleardev_builder_replacement_requests", "cleardev_builder_replacement_grants", "cleardev_builder_replacement_handoffs", "cleardev_builder_replacement_aliases", "cleardev_builder_replacement_observations", "cleardev_builder_session_fences", "cleardev_builder_session_operations", "cleardev_builder_session_operation_ends", "cleardev_builder_handoff_contexts"}

func builderReplacementStorageDB(t *testing.T, f *projectPlanningFixture) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func builderReplacementStorageRecovery(input WorkflowRecoveryInput) core.WorkflowRecovery {
	return core.WorkflowRecovery{ID: input.RequestID, ExecutionRunID: input.ExecutionRunID, Action: input.Action, TargetID: input.TargetID, Supplement: input.Supplement}
}

func reopenBuilderReplacementStorage(t *testing.T, f *projectPlanningFixture, h *builderReplacementHarness) {
	t.Helper()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.service()
	h.store, h.service = f.store, f.s
	f.s.sessions, f.s.chat, f.s.inspector = h, h, h
	f.s.checks = h.projectExecutionFlowHarness
	f.s.finalReviews, f.s.resultPreview = f.store, h.trial
	f.s.desktopRunID = "explicit-fake-handoff-desktop"
	f.s.runBackground = func(func()) {}
}

func TestBuilderReplacementStorageReopensEveryDurableStageWithoutNewFacts(t *testing.T) {
	f, h, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	db := builderReplacementStorageDB(t, f)
	request := builderReplacementInput(t, f, id, "reopen-request-handoff", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, request); err != nil {
		t.Fatal(err)
	}
	assertReopenReplay := func(stage string, input WorkflowRecoveryInput, registered, bound bool) {
		t.Helper()
		before := builderReplacementStorageRows(t, db)
		spawns, copies, sends := h.spawnCalls, h.restoreCalls, len(h.relays)
		reopenBuilderReplacementStorage(t, f, h)
		state, err := f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
		if err != nil || state.SourceSHA256 != state.Binding.SourceSHA256 || state.Binding.TargetID != request.TargetID || (state.Handoff != nil) != registered {
			t.Fatal(stage, "persistent source or registration changed", err)
		}
		if registered {
			if (state.Handoff.NewAOSessionID != "") != bound || len(state.Execution.BuilderReplacementTransitions) != 1 || state.Execution.BuilderReplacementTransitions[0].AliasBound != bound {
				t.Fatal(stage, "persistent binding projection changed")
			}
		}
		if stage == "REGISTERED" {
			if _, err := f.store.ContinueClearDevBuilderReplacement(ctx, id, builderReplacementStorageRecovery(input), time.Now()); err != nil {
				t.Fatal(stage, "exact Store replay", err)
			}
		} else if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
			t.Fatal(stage, "exact Service replay", err)
		}
		if h.spawnCalls != spawns || h.restoreCalls != copies || len(h.relays) != sends || !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
			t.Fatal(stage, "reopen/replay changed authority, operations, aliases, attempts, budget or CDC")
		}
	}
	assertReopenReplay("REQUEST", request, false, false)
	approveBuilderReplacementStorage(t, f)
	assertReopenReplay("APPROVED", request, false, false)
	cont := builderReplacementInput(t, f, id, "reopen-continue-handoff", core.RecoveryContinueBuilderReplacement)
	if _, err := f.store.ContinueClearDevBuilderReplacement(ctx, id, builderReplacementStorageRecovery(cont), time.Now()); err != nil {
		t.Fatal(err)
	}
	assertReopenReplay("REGISTERED", cont, true, false)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil {
		t.Fatal(err)
	}
	assertReopenReplay("BOUND", cont, true, true)
}

func TestBuilderReplacementStorageConcurrentContinuationAndStageClaimHaveOneWinner(t *testing.T) {
	f, _, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	request := builderReplacementInput(t, f, id, "race-request-handoff", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, request); err != nil {
		t.Fatal(err)
	}
	approveBuilderReplacementStorage(t, f)
	cont := builderReplacementInput(t, f, id, "race-continue-a", core.RecoveryContinueBuilderReplacement)
	other, err := sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, store := range []*sqlite.Store{f.store, other} {
		wg.Add(1)
		go func(i int, store *sqlite.Store) {
			defer wg.Done()
			input := builderReplacementStorageRecovery(cont)
			if i == 1 {
				input.ID = "race-continue-b"
			}
			<-start
			_, err := store.ContinueClearDevBuilderReplacement(ctx, id, input, time.Now())
			results <- err
		}(i, store)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("independent SQLite writers created continuation winners", winners)
	}
	state, err := f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
	if err != nil || state.Handoff == nil || state.Handoff.NewAOSessionID != "" {
		t.Fatal("unique unsent registered handoff", err)
	}
	db := builderReplacementStorageDB(t, f)
	for _, table := range []string{"cleardev_builder_replacement_handoffs", "cleardev_builder_session_fences"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatal("unique successor fact", table, count, err)
		}
	}
	var successors, retirements int
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_complex_execution_role_bindings WHERE continuation_of_role_binding_id=?`, state.Binding.OldRoleBindingID).Scan(&successors); err != nil || successors != 1 {
		t.Fatal("unique requested successor", successors, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_agent_attempt_events WHERE status='RETIRED_BEFORE_SEND'`).Scan(&retirements); err != nil || retirements != 1 {
		t.Fatal("unique unsent retirement", retirements, err)
	}
	claims := make(chan bool, 2)
	claimErrors := make(chan error, 2)
	start = make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			created, err := f.store.ClaimClearDevBuilderReplacementOperation(ctx, state.Handoff.ID, "COPY", state.Handoff.ID+":COPY", time.Now())
			claims <- created
			claimErrors <- err
		}()
	}
	close(start)
	wg.Wait()
	close(claims)
	close(claimErrors)
	winners = 0
	for created := range claims {
		if created {
			winners++
		}
	}
	for err := range claimErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("stage claim winners", winners)
	}
	before := builderReplacementStorageRows(t, db)
	if created, err := f.store.ClaimClearDevBuilderReplacementOperation(ctx, state.Handoff.ID, "COPY", state.Handoff.ID+":COPY", time.Now()); err != nil || created {
		t.Fatal("saved STARTED claim authorized a repeated copy", created, err)
	}
	if _, err := f.store.ClaimClearDevBuilderReplacementOperation(ctx, state.Handoff.ID, "COPY", "different-copy-key", time.Now()); err == nil {
		t.Fatal("stage operation key changed")
	}
	if !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
		t.Fatal("claim replay changed durable facts or CDC")
	}
}

func TestBuilderReplacementStorageKeepsOriginalBudgetAndRejectsAnotherHandoff(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(map[bool]string{false: "unoccupied", true: "occupied"}[occupied], func(t *testing.T) {
			f, h, id := newBuilderReplacementFixture(t)
			ctx := context.Background()
			state, err := f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
			if err != nil || state.OccupancyID != "" {
				t.Fatal("fixture requires a wholly unoccupied original step", err)
			}
			if occupied {
				if ok, err := f.store.OccupyClearDevComplexExceptionBudget(ctx, state.Budget.ID, state.Dispatch.ID, state.Step.ID, time.Now()); err != nil || !ok {
					t.Fatal("real original occupancy", ok, err)
				}
				state, err = f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
				if err != nil || state.OccupancyID == "" {
					t.Fatal("original occupancy absent", err)
				}
			}
			originalBudget, originalOccupancy := state.Budget, state.OccupancyID
			db := builderReplacementStorageDB(t, f)
			originalOccupancyRows := builderReplacementStorageRows(t, db)["cleardev_complex_exception_budget_occupancies"]
			request := builderReplacementInput(t, f, id, "budget-request-handoff", core.RecoveryRequestBuilderReplacement)
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, request); err != nil {
				t.Fatal(err)
			}
			approveBuilderReplacementStorage(t, f)
			cont := builderReplacementInput(t, f, id, "budget-continue-handoff", core.RecoveryContinueBuilderReplacement)
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil {
				t.Fatal(err)
			}
			state, err = f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
			if err != nil || state.Handoff == nil || !reflect.DeepEqual(state.Budget, originalBudget) || state.OccupancyID != originalOccupancy {
				t.Fatal("registration/binding changed original budget or occupancy", err)
			}
			if !reflect.DeepEqual(originalOccupancyRows, builderReplacementStorageRows(t, db)["cleardev_complex_exception_budget_occupancies"]) {
				t.Fatal("handoff introduced another role occupancy")
			}
			before := builderReplacementStorageRows(t, db)
			now := time.Now().UTC()
			third := core.AgentStepAttempt{ID: state.Step.ID + ":attempt:3", DevelopmentRequirementID: id, LogicalStepID: state.Step.ID, StepCategory: core.AgentStepCategoryComplexExecution, StepKind: core.ComplexExecutionAgentStepBuilderTask, AttemptNumber: 3, RoleBindingID: state.Binding.NewRoleBindingID, AOSessionID: state.Handoff.NewAOSessionID, ClientMessageID: state.Binding.ClientMessageID + ":attempt:3", PromptSHA256: state.Binding.PromptSHA256, TriggerFailureEventID: state.Handoff.RetirementEventID, RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation}
			if _, _, err := f.store.EnsureClearDevAgentStepAttempt(ctx, third); err == nil {
				t.Fatal("third Builder attempt admitted")
			}
			secondHandoff := builderReplacementStorageRecovery(cont)
			secondHandoff.ID = "another-handoff"
			if _, err := f.store.ContinueClearDevBuilderReplacement(ctx, id, secondHandoff, time.Now()); err == nil {
				t.Fatal("second handoff admitted for the same logical step")
			}
			secondRequest := request
			secondRequest.RequestID = "another-replacement-authority"
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, secondRequest); err == nil {
				t.Fatal("second replacement authority admitted for the same logical step")
			}
			if !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
				t.Fatal("rejected third attempt or second handoff changed facts, authority, budget or CDC")
			}
			sends := len(h.relays)
			if _, _, err := f.s.advanceComplexStandardExecution(ctx, id); err != nil {
				t.Fatal(err)
			}
			if len(h.relays) != sends+1 {
				t.Fatal("normal replacement path did not send one original message")
			}
			state, err = f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
			if err != nil || state.Budget.ID != originalBudget.ID || state.OccupancyID == "" {
				t.Fatal("normal send lost original budget", err)
			}
			wantBudget := originalBudget
			if !occupied {
				wantBudget.UsedTurns++
			}
			if !reflect.DeepEqual(state.Budget, wantBudget) || (occupied && state.OccupancyID != originalOccupancy) {
				t.Fatal("normal send reset or double-counted the original role budget")
			}
			var occupancyCount, messageCount int
			if err := db.QueryRow(`SELECT count(*) FROM cleardev_complex_exception_budget_occupancies WHERE agent_step_id=?`, state.Step.ID).Scan(&occupancyCount); err != nil || occupancyCount != 1 {
				t.Fatal("original occupancy count", occupancyCount, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM cleardev_agent_message_reservations WHERE logical_step_id=?`, state.Step.ID).Scan(&messageCount); err != nil || messageCount != 1 {
				t.Fatal("original message ledger count", messageCount, err)
			}
			before = builderReplacementStorageRows(t, db)
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, cont); err != nil {
				t.Fatal(err)
			}
			if len(h.relays) != sends+1 || !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
				t.Fatal("delivered continuation replay sent or charged again")
			}
		})
	}
}

func builderReplacementStorageRows(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	out := map[string][][]any{}
	tables := append(append([]string{}, builderReplacementFactTables...), "cleardev_agent_step_attempts", "cleardev_agent_attempt_events", "cleardev_agent_message_reservations", "cleardev_agent_message_confirmations", "cleardev_complex_exception_budgets", "cleardev_complex_exception_budget_occupancies", "cleardev_complex_execution_task_attempts", "cleardev_complex_execution_agent_steps", "cleardev_human_decision_requests", "cleardev_human_decision_dispatches", "cleardev_human_decision_effects", "change_log")
	for _, table := range tables {
		evidence := func() [][]any {
			rows, err := db.Query("SELECT rowid,* FROM " + table + " ORDER BY rowid")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			cols, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			var evidence [][]any
			for rows.Next() {
				v, p := make([]any, len(cols)), make([]any, len(cols))
				for i := range p {
					p[i] = &v[i]
				}
				if err := rows.Scan(p...); err != nil {
					t.Fatal(err)
				}
				for i, x := range v {
					if b, ok := x.([]byte); ok {
						v[i] = append([]byte(nil), b...)
					}
				}
				evidence = append(evidence, v)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return evidence
		}()
		if len(evidence) != 0 {
			out[table] = evidence
		}
	}
	return out
}

// This is a synthetic native decision result in an isolated fixture. The
// offer still traverses the production Service kind/currentness admission.
func approveBuilderReplacementStorage(t *testing.T, f *projectPlanningFixture) {
	t.Helper()
	offer, found, err := f.s.IssueHumanDecisionOffer(context.Background(), f.s.desktopRunID)
	if err != nil || !found || offer.DecisionKind != core.HumanDecisionKindBuilderReplacement {
		t.Fatalf("real offer admission: found=%t kind=%s err=%v", found, offer.DecisionKind, err)
	}
	result := core.HumanDecisionResult{ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind, BindingSchemaVersion: offer.BindingSchemaVersion, Binding: append(json.RawMessage(nil), offer.Binding...), ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove}
	if err := f.s.ApplyHumanDecisionResult(context.Background(), result); err != nil {
		t.Fatal(err)
	}
}

func registeredBuilderReplacementStorageFixture(t *testing.T) (*projectPlanningFixture, *builderReplacementHarness, string) {
	t.Helper()
	f, h, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	at := time.Now().UTC()
	claimed, err := f.store.BeginClearDevBuilderSessionOperation(ctx, domain.SessionID(h.oldSessionID), "storage-fixture-observe", "fixture-no-action", at)
	if err != nil || !claimed {
		t.Fatal("operation claim", claimed, err)
	}
	if err := f.store.EndClearDevBuilderSessionOperation(ctx, "storage-fixture-observe", "FAILED_BEFORE_ACTION", at.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	input := builderReplacementInput(t, f, id, "storage-request-handoff", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	approveBuilderReplacementStorage(t, f)
	input = builderReplacementInput(t, f, id, "storage-continue-handoff", core.RecoveryContinueBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	return f, h, id
}

func TestBuilderReplacementStorageFactsRejectMutationReplaceIgnoreAndRowID(t *testing.T) {
	f, h, id := registeredBuilderReplacementStorageFixture(t)
	state, err := f.store.ReadClearDevBuilderReplacement(context.Background(), id, time.Now())
	if err != nil || state.Handoff == nil || state.SourceSHA256 != state.Binding.SourceSHA256 {
		t.Fatal("registered source changed after its read-only transition projection", err)
	}
	proofs := state.Execution.BuilderReplacementTransitions
	if len(proofs) != 1 || !proofs[0].AliasBound || proofs[0].HandoffID != state.Handoff.ID || proofs[0].DecisionRequestID != state.DecisionRequestID || proofs[0].OldRoleBindingID != state.Binding.OldRoleBindingID || proofs[0].NewRoleBindingID != state.Binding.NewRoleBindingID || proofs[0].NewAOSessionID != state.Handoff.NewAOSessionID || proofs[0].LogicalStepID != state.Step.ID || proofs[0].DispatchID != state.Dispatch.ID || proofs[0].RetirementEventID != state.Handoff.RetirementEventID || proofs[0].AttemptID != state.Handoff.AttemptID {
		t.Fatal("exact durable Builder transition is absent", proofs)
	}
	raw, err := json.Marshal(state.Execution)
	if err != nil || strings.Contains(string(raw), "BuilderReplacementTransitions") || strings.Contains(string(raw), "builderReplacementTransitions") {
		t.Fatal("private transition projection escaped into snapshot JSON", err)
	}
	db := builderReplacementStorageDB(t, f)
	before := builderReplacementStorageRows(t, db)
	for _, table := range builderReplacementFactTables {
		if len(before[table]) == 0 {
			t.Fatal("positive production fact absent", table)
		}
		cols := func() []string {
			rows, err := db.Query("SELECT * FROM " + table + " LIMIT 1")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			cols, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return cols
		}()
		for _, statement := range []string{"UPDATE " + table + " SET " + cols[0] + "=" + cols[0], "DELETE FROM " + table, "INSERT OR REPLACE INTO " + table + " SELECT * FROM " + table, "INSERT OR IGNORE INTO " + table + " SELECT * FROM " + table, "INSERT OR REPLACE INTO " + table + "(rowid," + strings.Join(cols, ",") + ") SELECT rowid,* FROM " + table} {
			if _, err := db.Exec(statement); err == nil {
				t.Fatal("immutable fact bypass admitted", table, statement)
			}
			if !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
				t.Fatal("failed mutation changed rowid, facts, budget or CDC", table)
			}
		}
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO cleardev_agent_attempt_events SELECT * FROM cleardev_agent_attempt_events WHERE status='RETIRED_BEFORE_SEND'`); err == nil {
		t.Fatal("retirement replace admitted")
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO cleardev_agent_attempt_events SELECT * FROM cleardev_agent_attempt_events WHERE status='RETIRED_BEFORE_SEND'`); err == nil {
		t.Fatal("retirement ignore admitted")
	}
	if _, err := db.Exec(`UPDATE sessions SET workspace_path=workspace_path||'-changed' WHERE id=?`, h.oldSessionID); err == nil {
		t.Fatal("old fenced identity changed")
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE id=?`, h.oldSessionID); err == nil {
		t.Fatal("old fenced identity deleted")
	}
	state, err = f.store.ReadClearDevBuilderReplacement(context.Background(), id, time.Now())
	if err != nil || state.Handoff == nil {
		t.Fatal("read exact handoff", err)
	}
	if len(before["cleardev_agent_message_reservations"]) != len(builderReplacementStorageRows(t, db)["cleardev_agent_message_reservations"]) {
		t.Fatal("registration pre-reserved a message")
	}
	if !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
		t.Fatal("guard or reads changed persistent facts")
	}
}

func TestBuilderReplacementStoragePopulatedDowngradeRefusesWithoutMutation(t *testing.T) {
	f, _, _ := registeredBuilderReplacementStorageFixture(t)
	db := builderReplacementStorageDB(t, f)
	before := builderReplacementStorageRows(t, db)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../storage/sqlite/migrations/0185_cleardev_builder_replacement.sql"))
	if err != nil {
		t.Fatal(err)
	}
	down := strings.Split(string(raw), "-- +goose Down")[1]
	guard := strings.Split(down, "PRAGMA foreign_keys=OFF;")[0]
	if _, err := db.Exec(guard); err == nil {
		t.Fatal("populated downgrade did not refuse actual handoff facts")
	}
	if !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
		t.Fatal("downgrade guard changed handoff, budget, messages or CDC")
	}
}

func TestBuilderReplacementStorageLifecycleClaimsAreExclusiveAndDurable(t *testing.T) {
	f, h, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	out := make(chan bool, 2)
	errs := make(chan error, 2)
	for i, key := range []string{"race-operation-a", "race-operation-b"} {
		wg.Add(1)
		go func(key string, i int) {
			defer wg.Done()
			v, e := f.store.BeginClearDevBuilderSessionOperation(ctx, domain.SessionID(h.oldSessionID), key, "resume", time.Now())
			out <- v
			errs <- e
		}(key, i)
	}
	wg.Wait()
	close(out)
	close(errs)
	wins := 0
	for v := range out {
		if v {
			wins++
		}
	}
	if wins != 1 {
		t.Fatal("lifecycle claim winners", wins)
	}
	for e := range errs {
		_ = e
	}
	open, err := f.store.HasOpenClearDevBuilderSessionOperation(ctx, domain.SessionID(h.oldSessionID))
	if err != nil || !open {
		t.Fatal("unknown operation not retained", open, err)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range view.Options {
		if o.Action == core.RecoveryRequestBuilderReplacement && o.UnavailableReason == "" {
			t.Fatal("in-flight operation offered handoff")
		}
	}
	db := builderReplacementStorageDB(t, f)
	var winner string
	if err := db.QueryRow(`SELECT id FROM cleardev_builder_session_operations WHERE ao_session_id=?`, h.oldSessionID).Scan(&winner); err != nil {
		t.Fatal(err)
	}
	if err := f.store.EndClearDevBuilderSessionOperation(ctx, winner, "UNKNOWN", time.Now()); err == nil {
		t.Fatal("unknown external result released operation")
	}
	if err := f.store.EndClearDevBuilderSessionOperation(ctx, winner, "FAILED_BEFORE_ACTION", time.Now()); err != nil {
		t.Fatal(err)
	}
	if open, err := f.store.HasOpenClearDevBuilderSessionOperation(ctx, domain.SessionID(h.oldSessionID)); err != nil || open {
		t.Fatal("confirmed no-action did not release", open, err)
	}
}

func TestBuilderReplacementStorageRowIDConflictHasLegalDistinctBusinessKeyControl(t *testing.T) {
	f, _, id := registeredBuilderReplacementStorageFixture(t)
	db := builderReplacementStorageDB(t, f)
	ctx := context.Background()
	state, err := f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
	if err != nil || state.Handoff == nil {
		t.Fatal(err)
	}
	before := builderReplacementStorageRows(t, db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var oldObservation, oldOperation int64
	if err := tx.QueryRow(`SELECT rowid FROM cleardev_builder_replacement_observations LIMIT 1`).Scan(&oldObservation); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT rowid FROM cleardev_builder_session_operations LIMIT 1`).Scan(&oldOperation); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct {
		label, table, columns, values string
		args                          []any
		rowid                         int64
	}{
		{"observation", "cleardev_builder_replacement_observations", "id,handoff_id,stage,operation_key,outcome,reason_code,ao_session_id,workspace_path,launch_sha256,snapshot_sha256,observed_at", "?,?,?,?,?,?,?,?,?,?,?", []any{"rowid-isolated-observation", state.Handoff.ID, "SEND", state.Handoff.ID + ":SEND", "UNKNOWN", "synthetic-no-side-effect", "", "", "", "", time.Now().UTC()}, oldObservation},
		{"operation", "cleardev_builder_session_operations", "id,ao_session_id,kind,created_at", "?,?,?,?", []any{"rowid-isolated-operation", state.Handoff.NewAOSessionID, "synthetic-no-side-effect", time.Now().UTC()}, oldOperation},
	} {
		if _, err := tx.Exec(`SAVEPOINT candidate_control`); err != nil {
			t.Fatal(err)
		}
		query := "INSERT INTO " + candidate.table + "(" + candidate.columns + ") VALUES(" + candidate.values + ")"
		if _, err := tx.Exec(query, candidate.args...); err != nil {
			t.Fatal("distinct business-key positive control rejected by source or FK guard", candidate.label, err)
		}
		if _, err := tx.Exec(`ROLLBACK TO candidate_control`); err != nil {
			t.Fatal(err)
		}
		query = "INSERT OR REPLACE INTO " + candidate.table + "(rowid," + candidate.columns + ") VALUES(?," + candidate.values + ")"
		args := append([]any{candidate.rowid}, candidate.args...)
		if _, err := tx.Exec(query, args...); err == nil || !strings.Contains(err.Error(), "identity already exists") {
			t.Fatal("isolated explicit rowid collision was not rejected by immutable identity guard", candidate.label, err)
		}
		if _, err := tx.Exec(`RELEASE candidate_control`); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
		t.Fatal("rowid conflict or rolled-back positive control changed original facts or CDC")
	}
}
