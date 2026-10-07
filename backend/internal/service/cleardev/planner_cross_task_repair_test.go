package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type crossTaskPlannerDiagnostic struct {
	*projectPlannerHarness
	t *testing.T
}

func (h *crossTaskPlannerDiagnostic) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	turn, err := h.projectPlannerHarness.RelayChatTurnWithID(ctx, id, prompt, key)
	if err != nil {
		h.t.Logf("synthetic relay failed: %v; prompt prefix %.240s", err, prompt)
	}
	return turn, err
}

// Real Service/SQLite and private-decision validation; model, Git and trial are
// explicit test doubles. No native approval or live business data is used.
func crossTaskRepairFixture(t *testing.T) (*projectPlanningFixture, *projectPlannerHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, base, initial := projectCoordinationSettledFailure(t, false, true, func(plan map[string]any) {
		first := plan["tasks"].([]any)[0].(map[string]any)
		criteria := []string{}
		for i := 0; i < 12; i++ {
			criteria = append(criteria, fmt.Sprintf("Verify observable persistence behavior %d under the original acceptance.", i))
		}
		first["reviewCriteria"] = criteria
	})
	for _, binding := range initial.RoleBindings {
		if binding.Role != core.StandardRoleBuilder || binding.AOSessionID == "" {
			continue
		}
		record, _, err := f.store.GetSession(context.Background(), domain.SessionID(binding.AOSessionID))
		if err != nil {
			t.Fatal(err)
		}
		record.Metadata.ProviderConversationID = "synthetic-original-builder"
		if err := f.store.UpdateSession(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	h := &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: initial.Run.DevelopmentRequirementID}
	h.builderDiagnosis = true
	h.mutateResult = func(r *core.PlannerCoordinationResult) {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		template := r.Amendments[0]
		r.Amendments = nil
		for i, task := range x.Tasks {
			a := template
			a.TaskKey = task.TaskKey
			if i != 0 {
				a.AdditionalReviewCriteria = []string{}
			}
			r.Amendments = append(r.Amendments, a)
		}
	}
	f.s.chat = &crossTaskPlannerDiagnostic{projectPlannerHarness: h, t: t}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		planning, _, _ := f.store.GetClearDevComplexPlanning(context.Background(), h.requirementID)
		for _, step := range planning.AgentSteps {
			if strings.Contains(step.ID, "planner-coordination") {
				t.Logf("coordination step %s %s reason=%s result=%s", step.ID, step.SendStatus, step.ReasonCode, step.FinalMessageText)
			}
		}
		states, _ := f.store.ListLatestClearDevAgentAttemptStates(context.Background(), h.requirementID)
		for _, state := range states {
			if strings.Contains(state.LogicalStepID, "planner-coordination") {
				t.Logf("coordination observation: status=%s reason=%s result=%+v", state.SendStatus, state.ErrorSummary, state.Results)
			}
		}
	})
	stopped := waitForCoordinationStop(t, f, h.requirementID, "criteria", initial.Tasks[0].ID)
	if len(stopped.Tasks) != 2 || stopped.Tasks[0].Status != core.DevelopmentTaskStatusReview || stopped.Tasks[1].Status != core.DevelopmentTaskStatusBlocked {
		t.Fatal("fixture must have a verified grant target and a different blocked source")
	}
	if !strings.Contains(stopped.PlannerRuntime.Decisions[0].Summary, "review criteria ceiling") {
		t.Fatalf("wrong stop: %+v", stopped.PlannerRuntime.Decisions)
	}
	return f, h, stopped
}

func TestCrossTaskCoordinationRepairGrantPreservesVerifiedTargetAndContinuesSource(t *testing.T) {
	f, h, before := crossTaskRepairFixture(t)
	ctx := context.Background()
	request := coordinationRepairRequest(t, f)
	binding, err := core.ParseCoordinationRepairBinding([]byte(request.BindingJSON))
	if err != nil || binding.TaskID != before.Tasks[0].DevelopmentTaskID {
		t.Fatal("grant must name the task whose criteria limit was reached", err)
	}
	result := coordinationRepairResult(t, f, request, core.HumanDecisionApprove)
	if err := f.s.crossTaskCoordinationRepairReady(ctx, binding); err != nil {
		t.Fatalf("cross-task source preflight: %v", err)
	}
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
		t.Fatalf("cross-task native grant must not try reopening a VERIFIED target: %v", err)
	}
	granted, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Tasks[0], granted.Tasks[0]) {
		t.Fatal("grant rewrote the unrelated verified target before a valid new amendment")
	}
	if granted.Tasks[1].Status != core.DevelopmentTaskStatusRework || granted.Tasks[1].ReworkCount != before.Tasks[1].ReworkCount+1 {
		t.Fatal("original blocked source did not enter its existing bounded continuation")
	}
	if !reflect.DeepEqual(before.Dispatches, granted.Dispatches) || !reflect.DeepEqual(before.CheckRuns, granted.CheckRuns) || !reflect.DeepEqual(before.Reviews, granted.Reviews) || !reflect.DeepEqual(before.FinalReview, granted.FinalReview) {
		t.Fatal("approval changed historical dispatch/check/review evidence")
	}
	if len(granted.WorkflowRecoveries) != len(before.WorkflowRecoveries)+1 {
		t.Fatal("approval did not register exactly one existing source continuation")
	}
	recovery := granted.WorkflowRecoveries[len(granted.WorkflowRecoveries)-1]
	if recovery.Action != core.RecoveryContinueBuilder || recovery.DispatchID != before.PlannerRuntime.Events[0].DispatchID || recovery.TaskID != before.Tasks[1].ID || recovery.OriginalReason != "BUILDER_BLOCKED" {
		t.Fatalf("recovery did not bind the exact original failure: %+v", recovery)
	}
	for _, budget := range granted.Exception.Budgets {
		for _, old := range before.Exception.Budgets {
			if old.ID != budget.ID {
				continue
			}
			if budget.ID == binding.BudgetID {
				if budget.AuthorizedExtraTurns != old.AuthorizedExtraTurns+2 || budget.UsedTurns != old.UsedTurns {
					t.Fatal("bound budget grant is missing or spends a turn before delivery")
				}
			} else if !reflect.DeepEqual(old, budget) {
				t.Fatal("cross-task continuation expanded another task's budget")
			}
		}
	}
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err == nil {
		t.Fatal("approval replay accepted")
	}
	reopened, err := sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, _, readErr := reopened.GetClearDevComplexExecution(ctx, h.requirementID)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil || !reflect.DeepEqual(granted, reloaded) {
		t.Fatal("database reopen lost the unique source continuation", readErr)
	}

	// The resumed source is diagnosed again under its own budget. Only the
	// subsequent valid Planner decision can revise both tasks and reverify them.
	h.finalVerdict = "PASS"
	var done core.ComplexExecutionSnapshot
	for range 180 {
		done, _, err = f.store.GetClearDevComplexExecution(ctx, h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if done.Run.CompletedAt != nil {
			break
		}
		h.builderDiagnosis = h.plannerTurns < 2
		if _, _, err := f.s.advanceComplexStandardExecution(ctx, h.requirementID); err != nil {
			t.Fatal(err)
		}
	}
	if done.Run.CompletedAt == nil || done.FinalReview == nil || done.FinalReview.Verdict != "PASS" || done.FinalReview.CandidateCommitSHA == before.FinalReview.CandidateCommitSHA || len(done.PlannerRuntime.Amendments) != 2 {
		phase, reason := core.DeriveComplexExecutionPhase(done)
		t.Fatalf("cross-task repair did not recheck the new candidate: phase=%s reason=%s planner=%d tasks=%+v", phase, reason, h.plannerTurns, done.Tasks)
	}
	if !reflect.DeepEqual(before.PlannerRuntime.Decisions[0], done.PlannerRuntime.Decisions[0]) {
		t.Fatal("repair rewrote the original STOP")
	}
	var pkg core.ComplexStandardExecutionPackage
	if err := json.Unmarshal([]byte(done.Tasks[0].ExecutionPackageJSON), &pkg); err != nil || len(pkg.ReviewCriteria) != 13 || pkg.RuntimeRevision.HumanRepairExtension == 0 {
		t.Fatal("new task package did not use the exact human-authorized criteria extension", err)
	}
}

func TestCrossTaskCoordinationRepairRejectsMovingSourcesAtomically(t *testing.T) {
	for _, mode := range []string{"native-active", "native-unknown", "candidate-changed", "paused", "stale-spec", "source-budget"} {
		t.Run(mode, func(t *testing.T) {
			f, h, before := crossTaskRepairFixture(t)
			ctx := context.Background()
			request := coordinationRepairRequest(t, f)
			result := coordinationRepairResult(t, f, request, core.HumanDecisionApprove)
			source, _ := complexExecutionDispatchByID(before, before.PlannerRuntime.Events[0].DispatchID)
			var sessionID domain.SessionID
			for _, binding := range before.RoleBindings {
				if binding.ID == source.BuilderRoleBindingID {
					sessionID = domain.SessionID(binding.AOSessionID)
				}
			}
			switch mode {
			case "native-active":
				snapshot := h.snapshots[sessionID]
				snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{ID: "late-active", State: domain.TurnStateRunning})
				h.snapshots[sessionID] = snapshot
			case "native-unknown":
				snapshot := h.snapshots[sessionID]
				snapshot.Messages = nil
				h.snapshots[sessionID] = snapshot
			case "candidate-changed":
				h.currentCandidate = forty("f")
			case "paused", "stale-spec":
				stopBuilderFirstFixture(t, f, before, mode)
			case "source-budget":
				db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
				if err != nil {
					t.Fatal(err)
				}
				// Legal isolated-fixture spending, never relaxing a SQL guard.
				for range 8 {
					if _, err := db.Exec(`UPDATE cleardev_complex_exception_budgets SET used_turns=used_turns+1 WHERE complex_execution_task_id=? AND role_kind='BUILDER' AND used_turns<max_turns+authorized_extra_turns`, source.ComplexExecutionTaskID); err != nil {
						_ = db.Close()
						t.Fatal(err)
					}
				}
				_ = db.Close()
			}
			changed, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
			if err != nil {
				t.Fatal(err)
			}
			sends := len(h.relays)
			if err := f.s.ApplyHumanDecisionResult(ctx, result); err == nil {
				t.Fatal("unsafe cross-task source accepted")
			}
			after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
			if err != nil || !reflect.DeepEqual(changed, after) || len(h.relays) != sends {
				t.Fatal("refused approval partially granted, changed history or sent a message", err)
			}
			if pending := coordinationRepairRequest(t, f); pending.ID != request.ID || pending.ContentSHA256 != request.ContentSHA256 {
				t.Fatal("failed settlement changed the original pending decision")
			}
		})
	}
}

func TestCrossTaskCoordinationRepairRejectionKeepsOriginalStop(t *testing.T) {
	f, h, before := crossTaskRepairFixture(t)
	ctx := context.Background()
	request := coordinationRepairRequest(t, f)
	result := coordinationRepairResult(t, f, request, core.HumanDecisionReject)
	sends := len(h.relays)
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil || !reflect.DeepEqual(before, after) || len(h.relays) != sends {
		t.Fatal("rejection changed execution or its budgets", err)
	}
	if blocked, reason := core.PlannerRuntimeBarrier(after); !blocked || reason != core.ReasonPlannerRuntimeStopped {
		t.Fatal("rejection cleared the Planner barrier")
	}
}

func TestCrossTaskCoordinationRepairConcurrentApprovalCreatesOneContinuation(t *testing.T) {
	f, h, before := crossTaskRepairFixture(t)
	ctx := context.Background()
	request := coordinationRepairRequest(t, f)
	result := coordinationRepairResult(t, f, request, core.HumanDecisionApprove)
	binding, err := core.ParseCoordinationRepairBinding(result.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.crossTaskCoordinationRepairReady(ctx, binding); err != nil {
		t.Fatal(err)
	}
	// Exercise only the database commit boundary concurrently; native source
	// observation above is read-only. No fake model is called concurrently.
	var wg sync.WaitGroup
	results := make(chan error, 3)
	at := f.s.now().UTC()
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.store.SettleClearDevHumanDecision(ctx, result, at)
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected one exact native approval, got %d", succeeded)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil || len(after.WorkflowRecoveries) != len(before.WorkflowRecoveries)+1 || len(after.Dispatches) != len(before.Dispatches) {
		t.Fatal("concurrent approval created duplicate successors or dispatched directly", err)
	}
}
