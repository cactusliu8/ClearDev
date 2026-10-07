package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type projectPlannerHarness struct {
	*projectExecutionFlowHarness
	requirementID string
	plannerTurns  int
	mutateResult  func(*core.PlannerCoordinationResult)
}

func (h *projectPlannerHarness) RelayChatTurnWithID(ctx context.Context, sessionID domain.SessionID, prompt, key string) (string, error) {
	if !strings.HasPrefix(prompt, "你是本阶段原工程 Planner") {
		turn, err := h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, sessionID, prompt, key)
		if promptLineValue(prompt, "round=") == "1" {
			h.builderDiagnosis = false
		}
		return turn, err
	}
	if prior := h.turnByClientMessageID[key]; prior != "" {
		return prior, nil
	}
	x, _, err := h.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		return "", err
	}
	contract, _, err := core.ProjectContractFromRun(x.Run)
	if err != nil {
		return "", err
	}
	stage, _, err := h.service.productStageSource(ctx, h.requirementID)
	if err != nil {
		return "", err
	}
	basis := contract.Basis
	runtime := contract.Runtime
	runtime.PrepareArgv = []string{"npm", "run", "prepare-trial"}
	basis.Runtime = &runtime
	basis.Trial = &core.ProjectTrial{SchemaVersion: 1, Service: true, Steps: append(append([]core.ProjectTrialStep{}, contract.Basis.Trial.Steps...), core.ProjectTrialStep{ID: "real-browser", Kind: "BROWSER", AcceptanceCriteria: stage.Definition.AcceptanceCriteria, Observe: "Personally verify the original required behavior in the isolated application."})}
	result := core.PlannerCoordinationResult{SchemaVersion: 1, Kind: core.PlannerRuntimeResultKind, Decision: core.PlannerRuntimeAmend, Summary: "The original goal is unchanged; the project must provide isolated trial preparation and real browser observations.", Questions: []string{}, Amendments: []core.PlannerRemainingAmendment{{TaskKey: x.Tasks[0].TaskKey, AdditionalReviewCriteria: []string{"Trial preparation stays isolated and cannot replace the original acceptance."}, ExecutionBasis: &basis}}}
	if h.mutateResult != nil {
		h.mutateResult(&result)
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": result.SchemaVersion, "kind": result.Kind, "decision": result.Decision, "summary": result.Summary, "questions": result.Questions, "amendments": result.Amendments})
	if err != nil {
		return "", err
	}
	h.replies = append(h.replies, string(raw))
	turn, err := h.productTestAgent.RelayChatTurnWithID(ctx, sessionID, prompt, key)
	if err != nil {
		return turn, err
	}
	record, _, err := h.store.GetSession(ctx, sessionID)
	if err != nil {
		return turn, err
	}
	record.Activity.State = domain.ActivityIdle
	h.lastPromptBySession[sessionID] = prompt
	h.plannerTurns++
	return turn, h.store.UpdateSession(ctx, record)
}

func TestProjectPlannerRepairsSettledBuilderContractAndRechecks(t *testing.T) {
	f, base, before := settledProjectFailure(t, "BLOCKED")
	h := &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}
	h.candidateSHAs = append(h.candidateSHAs, forty("d"))
	h.builderDiagnosis = true
	h.finalVerdict = "PASS"
	f.s.chat = h
	var after core.ComplexExecutionSnapshot
	for i := 0; i < 80; i++ {
		var err error
		after, _, err = f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Run.CompletedAt != nil {
			break
		}
		progressed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatalf("transition %d err=%v progress=%v stopped=%v plannerTurns=%d", i, err, progressed, stopped, h.plannerTurns)
		}
	}
	if after.Run.CompletedAt == nil || h.plannerTurns != 1 || after.PlannerRuntime == nil || len(after.PlannerRuntime.Amendments) != 1 || len(after.Dispatches) != 3 || after.FinalReview.Verdict != "PASS" {
		planning, _, _ := f.store.GetClearDevComplexPlanning(context.Background(), h.requirementID)
		for _, step := range planning.AgentSteps {
			if strings.Contains(step.ID, "planner-coordination") {
				t.Logf("planner step status=%s reason=%s text=%s", step.SendStatus, step.ReasonCode, step.FinalMessageText)
			}
		}
		views, _ := f.store.ListLatestClearDevAgentAttemptStates(context.Background(), h.requirementID)
		for _, v := range views {
			if strings.Contains(v.LogicalStepID, "planner-coordination") {
				t.Logf("planner attempt=%+v", v)
			}
		}
		phase, reason := core.DeriveComplexExecutionPhase(after)
		t.Fatalf("engineering revision did not complete the actual pipeline: phase=%s reason=%s planner=%d decisions=%+v dispatches=%+v", phase, reason, h.plannerTurns, after.PlannerRuntime.Decisions, after.Dispatches)
	}
	if after.Run.ExecutionPackageJSON != before.Run.ExecutionPackageJSON || after.Run.ExecutionPackageSHA256 != before.Run.ExecutionPackageSHA256 {
		t.Fatal("revision rewrote admission")
	}
	if after.Dispatches[0].ExecutionPackageSHA256 != before.Dispatches[0].ExecutionPackageSHA256 || after.Dispatches[2].ExecutionPackageSHA256 == before.Dispatches[0].ExecutionPackageSHA256 {
		t.Fatal("historical/new dispatch contract identities were mixed")
	}
	if len(after.Verifications) != 2 || after.FinalReview.CandidateCommitSHA == before.FinalReview.CandidateCommitSHA {
		t.Fatal("revision reused old passing evidence")
	}
	if after.Run.RuntimeProjectExecution == nil || after.Run.RuntimeProjectExecution.Runtime.PrepareArgv[2] != "prepare-trial" {
		t.Fatal("effective execution agreement missing")
	}
	reopened, err := sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := reopened.ApplyClearDevPlannerRuntime(context.Background(), after.PlannerRuntime.Events[0].ID, f.s.now().UTC())
			if err != nil || changed {
				t.Errorf("decision replay changed history: %v %v", changed, err)
			}
		}()
	}
	wg.Wait()
	replayed, _, err := reopened.GetClearDevComplexExecution(context.Background(), h.requirementID)
	if err != nil || !reflect.DeepEqual(after, replayed) {
		t.Fatal("restart changed effective or historical contract", err)
	}
	contract := *after.Run.RuntimeProjectExecution
	goal, err := f.s.GetProductGoal(context.Background(), contract.ProductID)
	if err != nil {
		t.Fatal(err)
	}
	binding, _, err := f.s.completedProjectBaseline(context.Background(), contract.ProductID, h.requirementID, after.Integration.CandidateCommitSHA)
	if err != nil {
		t.Fatal(err)
	}
	selection := contract.Selection
	selection.SourceDiscussionID = goal.Discussions[len(goal.Discussions)-1].ID
	selection.ChoiceDiscussionID = "select-revised-delivery"
	selection.BaseCommitSHA = after.Integration.CandidateCommitSHA
	selection.Delivery = &binding
	if _, err := f.store.AppendClearDevProductDiscussion(context.Background(), core.AppendProductDiscussionCommand{
		ExpectedPreviousID: selection.SourceDiscussionID, Selection: &selection,
		Discussion: core.ProductDiscussion{ID: selection.ChoiceDiscussionID, ProductID: contract.ProductID, UserMessage: "Continue from the independently rechecked engineering revision.", CreatedAt: f.s.now().UTC()},
	}); err != nil {
		t.Fatalf("revised delivery cannot be selected for continuation: %v", err)
	}

}

func TestProjectPlannerRejectsUnsafeAgreementAtomically(t *testing.T) {
	for _, mode := range []string{"write-scope", "remove-browser", "replace-check", "budget"} {
		t.Run(mode, func(t *testing.T) {
			f, base, before := settledProjectFailure(t, "BLOCKED")
			h := &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}
			if mode == "budget" {
				db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				for range 2 {
					if _, err := db.Exec("UPDATE cleardev_complex_exception_budgets SET used_turns=used_turns+1 WHERE execution_run_id=? AND role_kind='BUILDER' AND used_turns<max_turns+authorized_extra_turns", before.Run.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			h.builderDiagnosis = true
			h.mutateResult = func(result *core.PlannerCoordinationResult) {
				basis := result.Amendments[0].ExecutionBasis
				switch mode {
				case "write-scope":
					basis.WritePaths = append(basis.WritePaths, "other-project/**")
				case "remove-browser":
					basis.Trial = nil
				case "replace-check":
					basis.Checks[0].Argv = []string{"npm", "run", "always-pass"}
				}
			}
			f.s.chat = h
			var after core.ComplexExecutionSnapshot
			for range 60 {
				var err error
				after, _, err = f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
				if err != nil {
					t.Fatal(err)
				}
				if after.PlannerRuntime != nil && len(after.PlannerRuntime.Decisions) > 0 {
					break
				}
				_, _, err = f.s.advanceComplexStandardExecution(context.Background(), h.requirementID)
				if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
					t.Fatal(err)
				}
			}
			if after.PlannerRuntime == nil || len(after.PlannerRuntime.Decisions) != 1 || after.PlannerRuntime.Decisions[0].Outcome != "STOP" {
				t.Fatalf("invalid proposal was not durably stopped: mode=%s plannerTurns=%d", mode, h.plannerTurns)
			}
			if len(after.PlannerRuntime.Amendments) != 0 || after.Run.RuntimeProjectExecution != nil || after.Run.CompletedAt != nil || len(after.Dispatches) != 2 || after.Tasks[0].ReworkCount != 1 {
				t.Fatal("rejected proposal partially changed agreement, task rounds or completion")
			}
			if after.Run.ExecutionPackageSHA256 != before.Run.ExecutionPackageSHA256 || h.plannerTurns != 1 {
				t.Fatal("rejected proposal rewrote admission or repeated Planner")
			}
		})
	}
}

func TestProjectPlannerWaitsForWorkerAndRejectsCancelledContext(t *testing.T) {
	ctx := context.Background()
	f, h, before := settledProjectFailure(t, "BLOCKED")
	h.builderDiagnosis = true
	var pending core.ComplexExecutionSnapshot
	for range 30 {
		var err error
		pending, _, err = f.store.GetClearDevComplexExecution(ctx, before.Run.DevelopmentRequirementID)
		if err != nil {
			t.Fatal(err)
		}
		if pending.PlannerRuntime != nil && len(pending.PlannerRuntime.Events) == 1 && len(pending.Dispatches) == 2 && pending.Dispatches[1].Status == core.ComplexExecutionDispatchBlocked {
			break
		}
		if _, _, err := f.s.advanceComplexStandardExecution(ctx, before.Run.DevelopmentRequirementID); err != nil {
			t.Fatal(err)
		}
	}
	if pending.PlannerRuntime == nil || len(pending.PlannerRuntime.Events) != 1 {
		t.Fatal("missing settled diagnosis")
	}
	binding, _ := complexExecutionBindingByID(pending, pending.Dispatches[1].BuilderRoleBindingID)
	record, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	record.Activity.State = domain.ActivityActive
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	event := pending.PlannerRuntime.Events[0]
	request, changed, err := f.store.PrepareClearDevPlannerRuntime(ctx, event.ID, f.s.now().UTC())
	if err != nil || changed || request.EventID != "" {
		t.Fatal("busy worker acquired a Planner round", changed, err)
	}
	record.Activity.State = domain.ActivityIdle
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CancelRequirement(ctx, before.Run.DevelopmentRequirementID, "test cancellation before coordination"); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := f.store.PrepareClearDevPlannerRuntime(ctx, event.ID, f.s.now().UTC()); err != nil || !changed {
		t.Fatal(changed, err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, before.Run.DevelopmentRequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.PlannerRuntime.Requests) != 0 || len(after.PlannerRuntime.Amendments) != 0 || len(after.PlannerRuntime.Decisions) != 1 || after.PlannerRuntime.Decisions[0].Outcome != "STALE" || len(after.Dispatches) != 2 {
		t.Fatal("busy or cancelled context advanced the contract")
	}
}
