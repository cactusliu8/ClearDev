package cleardev

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func coordinationRepairFixture(t *testing.T, mode string) (*projectPlanningFixture, *projectPlannerHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, base, before := settledProjectFailure(t, "BLOCKED")
	h := &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}
	h.candidateSHAs = append(h.candidateSHAs, forty("d"), forty("e"))
	// The granted round-cap chain spends rounds up to the repaired horizon;
	// the fake Builder indexes one candidate per round.
	for i := len(h.candidateSHAs); i <= 12; i++ {
		h.candidateSHAs = append(h.candidateSHAs, fmt.Sprintf("%040x", 100+i))
	}
	h.builderDiagnosis = true
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`UPDATE cleardev_work_items SET max_rework_count=8 WHERE complex_execution_task_id=?`, before.Tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "budget":
		for range 2 {
			if _, err := db.Exec(`UPDATE cleardev_complex_exception_budgets SET used_turns=used_turns+1 WHERE execution_run_id=? AND role_kind='BUILDER' AND used_turns<max_turns+authorized_extra_turns`, before.Run.ID); err != nil {
				t.Fatal(err)
			}
		}
	case "rounds":
		// The round-cap experiment models a recorded live setup whose
		// Builder budget is frozen at 8 rework rounds, a value the shipped
		// budget bound (1,3) cannot express, so this test database widens
		// that check exactly as the recorded live test-database exception
		// did. The
		// frozen-budget update trigger only admits spend and grant steps,
		// so the freeze change applies with that trigger saved, removed and
		// restored in place, mirroring the recorded live procedure. Shipped
		// plan defaults stay unchanged.
		if _, err := db.Exec(`PRAGMA writable_schema = ON`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE sqlite_master SET sql=replace(sql,'max_rework_count IN (1,3)','max_rework_count IN (1,3,8)')
WHERE type='table' AND name='cleardev_complex_exception_budgets'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA writable_schema = RESET`); err != nil {
			t.Fatal(err)
		}
		var budgetTrigger string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='cleardev_complex_exception_budget_update_valid'`).Scan(&budgetTrigger); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP TRIGGER cleardev_complex_exception_budget_update_valid`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE cleardev_complex_exception_budgets SET max_rework_count=8 WHERE role_kind='BUILDER'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(budgetTrigger); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown fixture mode %s", mode)
	}
	f.s.chat = h
	return f, h, before
}

// seedCapDispatchHistory fills the settled dispatch history the product
// round cap implies: rounds 2..6 share the frozen base of the latest real
// round, and round 6 is the Builder-blocked attempt a repair grant reopens.
// Real runs reach this history by spending those rounds; the fixture freezes
// the coordination request mid-flight and only carries rounds 0..1, so the
// rows are cloned from the latest real attempt. The insert triggers are
// saved, dropped and restored around the clones: they police the product
// entry points, not fixture history, and restoring them keeps the subsequent
// real round 7 dispatch under the shipped guards.
func seedCapDispatchHistory(t *testing.T, db *sql.DB, taskMappingID string) {
	t.Helper()
	// A valid JSON message that is not a Builder result keeps the runtime
	// capture out of seeded history while satisfying the settled-step digest
	// check.
	const seededHistoryMessage = `{"kind":"SEED_HISTORY"}`
	var attemptID, stepID string
	err := db.QueryRow(`SELECT id, agent_step_id FROM cleardev_complex_execution_task_attempts
WHERE task_mapping_id=? ORDER BY round DESC LIMIT 1`, taskMappingID).Scan(&attemptID, &stepID)
	if err != nil {
		t.Fatal(err)
	}
	triggers := map[string]string{}
	for _, name := range []string{"cleardev_complex_execution_attempt_insert_valid", "cleardev_complex_execution_agent_step_insert_valid", "cleardev_planner_runtime_dispatch_guard"} {
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		triggers[name] = ddl
		if _, err := db.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
	for round := 2; round <= 6; round++ {
		attempt, step := fmt.Sprintf("seed-attempt-%d", round), fmt.Sprintf("seed-step-%d", round)
		at := fmt.Sprintf("2026-10-01T00:00:0%dZ", round)
		status, reason := "REWORK", "BUILDER_BLOCKED"
		if round == 6 {
			status = "BLOCKED"
		}
		if _, err := db.Exec(`INSERT INTO cleardev_complex_execution_agent_steps
(id, role_binding_id, step_kind, request_id, client_message_id, prompt_sha256, send_status, turn_id, final_message_id, final_message_text, message_sha256, requested_at, sent_at, completed_at, reason_code)
SELECT ?, role_binding_id, step_kind, ?, ?, prompt_sha256, 'SETTLED', ?, ?, ?, ?, ?, ?, ?, ''
FROM cleardev_complex_execution_agent_steps WHERE id=?`,
			step, attempt, "seed-message-"+step, "seed-turn-"+step, "seed-final-"+step,
			seededHistoryMessage, fmt.Sprintf("%x", sha256.Sum256([]byte(seededHistoryMessage))), at, at, at, stepID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO cleardev_complex_execution_task_attempts
(id, execution_run_id, task_mapping_id, builder_role_binding_id, agent_step_id, round, base_commit_sha, status, reason_code, dispatched_at, settled_at, batch_id)
SELECT ?, execution_run_id, task_mapping_id, builder_role_binding_id, ?, ?, base_commit_sha, ?, ?, ?, ?, batch_id
FROM cleardev_complex_execution_task_attempts WHERE id=?`,
			attempt, step, round, status, reason, at, at, attemptID); err != nil {
			t.Fatal(err)
		}
	}
	for name, ddl := range triggers {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("restore %s: %v", name, err)
		}
	}
}

func waitForCoordinationStop(t *testing.T, f *projectPlanningFixture, requirementID, mode, taskMappingID string) core.ComplexExecutionSnapshot {
	t.Helper()
	var after core.ComplexExecutionSnapshot
	bumped := false
	for range 80 {
		var err error
		after, _, err = f.store.GetClearDevComplexExecution(context.Background(), requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if after.PlannerRuntime != nil && len(after.PlannerRuntime.Decisions) > 0 {
			return after
		}
		// The repair-round refusal needs the task past the product round cap
		// when the coordination request freezes its context. The event is
		// captured first and the request is reserved on a later step, so the
		// counter bump fits exactly between them.
		if mode == "rounds" && !bumped && after.PlannerRuntime != nil && len(after.PlannerRuntime.Events) > 0 && len(after.PlannerRuntime.Requests) == 0 {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`UPDATE cleardev_work_items SET rework_count=6 WHERE complex_execution_task_id=?`, taskMappingID)
			_ = db.Close()
			if err != nil {
				t.Fatal(err)
			}
			bumped = true
		}
		changed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
		if !changed {
			t.Logf("advance quiet: stopped=%v err=%v", stopped, err)
		}
	}
	statuses := []string{}
	for _, d := range after.Dispatches {
		statuses = append(statuses, fmt.Sprintf("%d:%s", d.Round, d.Status))
	}
	checks := []string{}
	for _, c := range after.CheckRuns {
		checks = append(checks, string(c.Status))
	}
	fr := "nil"
	if after.FinalReview != nil {
		fr = after.FinalReview.Status
	}
	tasks := []string{}
	for _, tk := range after.Tasks {
		tasks = append(tasks, fmt.Sprintf("%s rework=%d round=%d", tk.Status, tk.ReworkCount, tk.CurrentRound))
	}
	t.Fatalf("coordination decision was not recorded: events=%d requests=%d decisions=%d dispatches=%v checks=%v finalReview=%s tasks=%v",
		len(after.PlannerRuntime.Events), len(after.PlannerRuntime.Requests), len(after.PlannerRuntime.Decisions), statuses, checks, fr, tasks)
	return after
}

func coordinationRepairRequest(t *testing.T, f *projectPlanningFixture) core.HumanDecisionRequest {
	t.Helper()
	requests, err := f.store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		if request.DecisionKind == core.HumanDecisionKindCoordinationRepair {
			return request
		}
	}
	t.Fatal("no coordination repair offer was registered")
	return core.HumanDecisionRequest{}
}

func coordinationRepairResult(t *testing.T, f *projectPlanningFixture, request core.HumanDecisionRequest, choice core.HumanDecisionChoice) core.HumanDecisionResult {
	t.Helper()
	now := f.s.now().UTC()
	offer, err := f.store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{
		RequestID: request.ID, DesktopRunID: "coordination-repair-test", Nonce: mustComplexNonce(t),
		IssuedAt: now, ExpiresAt: now.Add(core.HumanDecisionOfferTTL)})
	if err != nil {
		t.Fatal(err)
	}
	return core.HumanDecisionResult{ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
		DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
		BindingSchemaVersion: offer.BindingSchemaVersion, Binding: offer.Binding, ContentSHA256: offer.ContentSHA256,
		Nonce: offer.Nonce, Decision: choice}
}

// TestCoordinationRepairGrantWakesTheRun pins the settlement wake-up: a
// granted repair commits its budget and reopened attempt inside the settle
// transaction, and the run must schedule itself from there. Before this
// branch existed the REWORK task stayed undispatched until some unrelated
// entry point happened to run.
func TestCoordinationRepairGrantWakesTheRun(t *testing.T) {
	f, h, before := coordinationRepairFixture(t, "budget")
	after := waitForCoordinationStop(t, f, before.Run.DevelopmentRequirementID, "budget", before.Tasks[0].ID)
	request := coordinationRepairRequest(t, f)
	result := coordinationRepairResult(t, f, request, core.HumanDecisionApprove)
	h.finalVerdict = "PASS"
	h.builderDiagnosis = true
	// The fixture's background runner never executes its loop, so the
	// running guard a real runner would clear stays set. Release it to the
	// production state at this moment: a settled stop leaves no registered
	// runner, which is exactly the gap the wake-up covers.
	f.s.complexExecutionMu.Lock()
	delete(f.s.complexExecutionRunning, before.Run.DevelopmentRequirementID)
	f.s.complexExecutionMu.Unlock()
	scheduled := false
	f.s.runBackground = func(run func()) { scheduled = true; run() }
	if err := f.s.ApplyHumanDecisionResult(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if !scheduled {
		t.Fatal("settled coordination repair did not wake the execution")
	}
	done, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(done.Dispatches) <= len(after.Dispatches) {
		t.Fatalf("wake dispatched no repaired round: before=%d after=%d", len(after.Dispatches), len(done.Dispatches))
	}
}

func TestProjectCoordinationRepairHumanGrantResumesAndCompletes(t *testing.T) {
	for _, mode := range []string{"budget", "rounds"} {
		t.Run(mode, func(t *testing.T) {
			f, h, before := coordinationRepairFixture(t, mode)
			after := waitForCoordinationStop(t, f, before.Run.DevelopmentRequirementID, mode, before.Tasks[0].ID)
			if len(after.PlannerRuntime.Decisions) != 1 || after.PlannerRuntime.Decisions[0].Outcome != "STOP" {
				t.Fatalf("limit refusal was not stopped: %+v", after.PlannerRuntime.Decisions)
			}
			if len(after.PlannerRuntime.Amendments) != 0 {
				t.Fatal("refused proposal partially applied")
			}
			expectedRework := 1
			if mode == "rounds" {
				expectedRework = 6
			}
			if after.Tasks[0].ReworkCount != expectedRework {
				t.Fatalf("refused proposal changed task rounds: %d", after.Tasks[0].ReworkCount)
			}
			// The settled history the round cap implies (rounds 2..6 with the
			// frozen base) is seeded only now: the run is quiescent at its
			// recorded STOP, exactly like the live run that awaits the human
			// grant, so seeded rows cannot disturb in-flight work.
			if mode == "rounds" {
				db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
				if err != nil {
					t.Fatal(err)
				}
				seedCapDispatchHistory(t, db, before.Tasks[0].ID)
				_ = db.Close()
			}
			request := coordinationRepairRequest(t, f)
			if !strings.Contains(request.BindingJSON, after.PlannerRuntime.Events[0].ID) || !strings.Contains(request.BindingJSON, before.Tasks[0].DevelopmentTaskID) {
				t.Fatalf("repair offer is not bound to the stopped event and task: %s", request.BindingJSON)
			}
			// The desktop offer loop must surface the repair grant too.
			if offer, ok, err := f.s.IssueHumanDecisionOffer(context.Background(), "repair-desktop"); err != nil || !ok || offer.RequestID != request.ID {
				t.Fatalf("coordination repair offer was not issued to the desktop: ok=%v err=%v", ok, err)
			}
			if err := f.store.BackfillClearDevHumanDecisionRequests(context.Background()); err != nil {
				t.Fatal(err)
			}
			if again := coordinationRepairRequest(t, f); again.ID != request.ID {
				t.Fatal("backfill re-asked the human for a recorded offer")
			}
			result := coordinationRepairResult(t, f, request, core.HumanDecisionApprove)
			h.finalVerdict = "PASS"
			// The Builder re-diagnoses the missing conventions until the
			// revision lands; the round after the revision builds.
			h.builderDiagnosis = true
			if err := f.s.ApplyHumanDecisionResult(context.Background(), result); err != nil {
				t.Fatal(err)
			}
			granted, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if granted.Tasks[0].ReworkCount != expectedRework+1 {
				t.Fatalf("grant did not reopen the repaired task: %+v", granted.Tasks[0])
			}
			if blocked, reason := core.PlannerRuntimeBarrier(granted); blocked {
				t.Fatalf("approved grant still blocks: %s", reason)
			}
			if err := f.s.ApplyHumanDecisionResult(context.Background(), result); err == nil {
				t.Fatal("replayed approval accepted")
			}
			var done core.ComplexExecutionSnapshot
			for range 80 {
				if done, _, err = f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID); err != nil {
					t.Fatal(err)
				}
				if done.Run.CompletedAt != nil {
					break
				}
				if _, _, err := f.s.advanceComplexStandardExecution(context.Background(), before.Run.DevelopmentRequirementID); err != nil {
					t.Logf("advance error: %v", err)
					if !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) &&
						!strings.Contains(err.Error(), "invalid Builder, gate, or bounded round") {
						t.Fatalf("advance after grant: %v (task round=%d rework=%d)", err, done.Tasks[0].CurrentRound, done.Tasks[0].ReworkCount)
					}
				}
				h.builderDiagnosis = h.plannerTurns < 2
			}
			if done.Run.CompletedAt == nil || done.FinalReview == nil || done.FinalReview.Verdict != "PASS" {
				phase, reason := core.DeriveComplexExecutionPhase(done)
				t.Fatalf("granted run did not complete: phase=%s reason=%s taskState=%s rework=%d decisions=%d amendments=%d plannerTurns=%d dispatches=%d events=%d",
					phase, reason, done.Tasks[0].Status, done.Tasks[0].ReworkCount, len(done.PlannerRuntime.Decisions), len(done.PlannerRuntime.Amendments), h.plannerTurns, len(done.Dispatches), len(done.PlannerRuntime.Events))
			}
			if done.FinalReview.CandidateCommitSHA == before.FinalReview.CandidateCommitSHA {
				t.Fatal("granted run reused the old candidate evidence")
			}
			if len(done.PlannerRuntime.Amendments) != 1 {
				t.Fatalf("follow-up coordination did not apply the revision: %d", len(done.PlannerRuntime.Amendments))
			}
			if done.PlannerRuntime.Amendments[0].EventID == after.PlannerRuntime.Events[0].ID {
				t.Fatal("revision attached to the refused event instead of a fresh Planner decision")
			}
			var revision core.ComplexStandardExecutionPackage
			if err := json.Unmarshal([]byte(done.PlannerRuntime.Amendments[0].ExecutionPackageJSON), &revision); err != nil || revision.RuntimeRevision == nil {
				t.Fatalf("saved revision unreadable: %v", err)
			}
			// Two authorized budget turns plus one approved repair pair.
			if revision.RuntimeRevision.HumanRepairExtension != 4 || revision.RuntimeRevision.FirstRound == 0 {
				t.Fatalf("revision did not carry the human-authorized window: %+v", revision.RuntimeRevision)
			}
			if done.PlannerRuntime.Decisions[0].ResultJSON != after.PlannerRuntime.Decisions[0].ResultJSON ||
				done.PlannerRuntime.Decisions[0].Summary != after.PlannerRuntime.Decisions[0].Summary {
				t.Fatal("grant rewrote the recorded stop")
			}
		})
	}
}

func TestProjectCoordinationRepairRejectKeepsStopAndHistory(t *testing.T) {
	f, h, before := coordinationRepairFixture(t, "budget")
	after := waitForCoordinationStop(t, f, before.Run.DevelopmentRequirementID, "budget", before.Tasks[0].ID)
	request := coordinationRepairRequest(t, f)
	result := coordinationRepairResult(t, f, request, core.HumanDecisionReject)
	if err := f.s.ApplyHumanDecisionResult(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	kept, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.PlannerRuntime.Amendments) != 0 || kept.Tasks[0].ReworkCount != 1 {
		t.Fatalf("rejected grant changed the task: %+v", kept.Tasks[0])
	}
	if kept.PlannerRuntime.Decisions[0].ResultJSON != after.PlannerRuntime.Decisions[0].ResultJSON ||
		kept.PlannerRuntime.Decisions[0].Summary != after.PlannerRuntime.Decisions[0].Summary {
		t.Fatal("rejected grant rewrote the recorded stop")
	}
	if blocked, reason := core.PlannerRuntimeBarrier(kept); !blocked || reason != core.ReasonPlannerRuntimeStopped {
		t.Fatalf("rejected grant must keep the stop: blocked=%v reason=%s", blocked, reason)
	}
	if err := f.store.BackfillClearDevHumanDecisionRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests, err := f.store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		if request.DecisionKind == core.HumanDecisionKindCoordinationRepair {
			t.Fatal("a decided grant was re-asked")
		}
	}
	h.finalVerdict = "PASS"
	if _, _, err := f.s.advanceComplexStandardExecution(context.Background(), before.Run.DevelopmentRequirementID); err != nil &&
		!errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) &&
		!strings.Contains(err.Error(), "Planner coordination must resolve before another dispatch") {
		t.Fatal(err)
	}
	if done, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID); err != nil {
		t.Fatal(err)
	} else if done.Run.CompletedAt != nil {
		t.Fatal("rejected grant let the stopped run complete")
	}
}

func TestProjectCoordinationRepairNoOfferForUnsafeProposal(t *testing.T) {
	f, base, before := settledProjectFailure(t, "BLOCKED")
	h := &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}
	h.builderDiagnosis = true
	h.mutateResult = func(result *core.PlannerCoordinationResult) {
		basis := result.Amendments[0].ExecutionBasis
		basis.Checks[0].Argv = []string{"npm", "run", "always-pass"}
	}
	f.s.chat = h
	after := waitForCoordinationStop(t, f, before.Run.DevelopmentRequirementID, "unsafe", before.Tasks[0].ID)
	if len(after.PlannerRuntime.Decisions) != 1 || after.PlannerRuntime.Decisions[0].Outcome != "STOP" {
		t.Fatalf("unsafe agreement was not stopped: %+v", after.PlannerRuntime.Decisions)
	}
	for range 2 {
		if err := f.store.BackfillClearDevHumanDecisionRequests(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	requests, err := f.store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		if request.DecisionKind == core.HumanDecisionKindCoordinationRepair {
			t.Fatal("an unsafe proposal asked the human for authority it cannot use")
		}
	}
}
