package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// These regression tests use the real service and SQLite. Models, Git, checks,
// browser, and ended command receipts are explicit doubles, not live acceptance.
func projectCoordinationProjectPlan(t *testing.T, command, multiple bool, mutations ...func(map[string]any)) (*projectPlanningFixture, RequirementView, core.ProjectExecutionAdmission, *projectExecutionPreparer) {
	t.Helper()
	f := newProjectPlanningFixture(t, "EXISTING")
	initial := f.create(t)
	var proposal core.ProductDiscoveryResult
	if err := json.Unmarshal([]byte(genericProjectReply("existing")), &proposal); err != nil {
		t.Fatal(err)
	}
	if command {
		trial := proposal.Stages[0].ExecutionBasis.Trial
		trial.Steps = append(trial.Steps, core.ProjectTrialStep{ID: "notes-command", Kind: "COMMAND", Argv: []string{"npm", "run", "verify-persistence"}, TimeoutSeconds: 30, AcceptanceCriteria: trial.Steps[0].AcceptanceCriteria, Observe: "核对原保存笔记及重启读取要求"})
	}
	raw, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	f.h.replies = append(f.h.replies, string(raw))
	selected, err := f.s.SubmitProductDiscussion(context.Background(), initial.Goal.ID, projectChoice(initial, "existing"))
	if err != nil {
		t.Fatal(err)
	}
	_, child := f.prepare(t, selected)
	planraw := genericEngineeringReply(t, child.RequirementVersions[0])
	if multiple {
		var plan map[string]any
		if err := json.Unmarshal([]byte(planraw), &plan); err != nil {
			t.Fatal(err)
		}
		tasks := plan["tasks"].([]any)
		clonebytes, _ := json.Marshal(tasks[0])
		var second map[string]any
		_ = json.Unmarshal(clonebytes, &second)
		second["key"] = "read-notes"
		second["title"] = "核对重新读取笔记"
		second["objective"] = "原保存的笔记可重新读取。"
		second["dependencyKeys"] = []string{"save-notes"}
		second["writePaths"] = []string{"src/storage.ts", "tests/storage.test.ts", "package.json", "package-lock.json", "migrations/001.sql"}
		plan["tasks"] = append(tasks, second)
		raw, _ = json.Marshal(plan)
		planraw = string(raw)
	}
	if len(mutations) != 0 {
		var plan map[string]any
		if err := json.Unmarshal([]byte(planraw), &plan); err != nil {
			t.Fatal(err)
		}
		for _, mutate := range mutations {
			mutate(plan)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		planraw = string(raw)
	}
	f.h.replies = append(f.h.replies, planraw)
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	child = mustGetComplex(t, f.s, child.Requirement.ID)
	if child.ComplexPlanning == nil || len(child.ComplexPlanning.Plans) != 1 {
		t.Fatal("test plan did not freeze")
	}
	plan := child.ComplexPlanning.Plans[0]
	admission := core.ProjectExecutionAdmission{RequestID: "projectCoordination-start", PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, RequirementSHA256: child.RequirementVersions[0].SHA256, BaseCommitSHA: selected.Selection.BaseCommitSHA}
	preparer := &projectExecutionPreparer{projectPlanningAgent: f.h}
	f.s.checks, f.s.finalReviews = preparer, f.store
	f.s.runBackground = func(func()) {}
	return f, child, admission, preparer
}

func projectCoordinationSettledFailure(t *testing.T, command, multiple bool, mutations ...func(map[string]any)) (*projectPlanningFixture, *projectExecutionFlowHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, child, admission, preparer := projectCoordinationProjectPlan(t, command, multiple, mutations...)
	h := attachProjectFlow(f, preparer)
	h.finalVerdict = "BLOCKED"
	h.benchmarkSequentialCandidates = multiple
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		if x.FinalReview != nil {
			break
		}
		progressed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID)
		if err != nil || stopped || !progressed {
			t.Fatalf("initial transition %d: progressed=%v stopped=%v err=%v tasks=%+v dispatches=%+v", i, progressed, stopped, err, x.Tasks, x.Dispatches)
		}
	}
	for range 40 {
		x, found, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
		if err != nil || !found {
			t.Fatal(err)
		}
		if x.FinalReview != nil && x.FinalReview.Status == "SETTLED" {
			return f, h, x
		}
		if _, _, err := f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("test final failure did not settle")
	return nil, nil, core.ComplexExecutionSnapshot{}
}

type projectCoordinationBoundReceipt struct {
	*projectExecutionFlowHarness
	expected   map[string]ports.ClearDevCheckRequest
	mismatches int
	readErr    error
	unreleased bool
}

func (h *projectCoordinationBoundReceipt) ReadCandidateCheck(_ context.Context, req ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, bool, error) {
	old, found := h.expected[req.RunID]
	if !found {
		return ports.ClearDevCheckResult{}, false, nil
	}
	if !reflect.DeepEqual(old, req) {
		h.mismatches++
		return ports.ClearDevCheckResult{}, false, errors.New("trial receipt binding changed")
	}
	if h.readErr != nil {
		return ports.ClearDevCheckResult{}, false, h.readErr
	}
	if h.unreleased {
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError}, true, nil
	}
	return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckPass}, true, nil
}

func (h *projectCoordinationBoundReceipt) CanRetryWorkflowCheck(_ context.Context, _ ports.ClearDevCheckRequest) (bool, error) {
	return !h.unreleased, nil
}

type projectCoordinationSecondBlock struct{ *projectPlannerHarness }

func (h *projectCoordinationSecondBlock) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	if promptLineValue(prompt, "round=") == "2" && !strings.HasPrefix(prompt, "你是本阶段原工程 Planner") {
		h.builderDiagnosis = true
	}
	return h.projectPlannerHarness.RelayChatTurnWithID(ctx, id, prompt, key)
}

func TestProjectPlannerSecondRoundKeepsOriginalCommandReceipt(t *testing.T) {
	f, base, before := projectCoordinationSettledFailure(t, true, false)
	contract, _, err := core.ProjectContractFromRun(before.Run)
	if err != nil {
		t.Fatal(err)
	}
	receipts := &projectCoordinationBoundReceipt{projectExecutionFlowHarness: base, expected: map[string]ports.ClearDevCheckRequest{}}
	for _, step := range contract.Basis.Trial.Steps {
		if step.Kind == "COMMAND" {
			req := stageTrialCommandRequest(*before.FinalReview, contract, step)
			receipts.expected[req.RunID] = req
		}
	}
	f.s.checks = receipts
	h := &projectCoordinationSecondBlock{projectPlannerHarness: &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}}
	h.builderDiagnosis = true
	h.mutateResult = func(r *core.PlannerCoordinationResult) {
		if h.plannerTurns == 1 {
			amendment := &r.Amendments[0]
			steps := amendment.ExecutionBasis.Trial.Steps
			steps[len(steps)-1].ID = "real-browser-second-repair"
			amendment.AdditionalReviewCriteria = []string{"Recheck the original acceptance after the second bounded preparation repair."}
		}
	}
	receiptBoundaryChecked := false
	f.s.chat = h
	for i := 0; i < 80; i++ {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if x.PlannerRuntime != nil && len(x.PlannerRuntime.Amendments) == 2 {
			if !receiptBoundaryChecked || h.plannerTurns != 2 || len(x.PlannerRuntime.Requests) != 2 || receipts.mismatches != 0 {
				t.Fatalf("second repair did not use the exact old receipt: turns=%d requests=%d mismatches=%d", h.plannerTurns, len(x.PlannerRuntime.Requests), receipts.mismatches)
			}
			return
		}
		if !receiptBoundaryChecked && x.PlannerRuntime != nil && len(x.PlannerRuntime.Amendments) == 1 && len(x.PlannerRuntime.Events) == 2 && core.PlannerRuntimeQuiescent(x) {
			if x.FinalReview.ID != before.FinalReview.ID {
				t.Fatal("probe no longer uses original final review")
			}
			t.Logf("real SQLite revision=%d pending events=%d dispatches=%d original final=%s", len(x.PlannerRuntime.Amendments), len(x.PlannerRuntime.Events), len(x.Dispatches), x.FinalReview.ID)
			// A historical packet must retain the original run binding and
			// hash; missing/unfinished receipts must still stop coordination.
			for _, mutate := range []func(*core.ComplexExecutionSnapshot){
				func(v *core.ComplexExecutionSnapshot) { v.Run.ID = "other-run" },
				func(v *core.ComplexExecutionSnapshot) { v.Run.ExecutionPackageSHA256 = strings.Repeat("f", 64) },
				func(v *core.ComplexExecutionSnapshot) {
					review := *v.FinalReview
					review.ReviewPacketJSON += " "
					v.FinalReview = &review
				},
			} {
				bad := x
				mutate(&bad)
				if settled, err := f.s.plannerHistoricalFinalCommandsSettled(context.Background(), bad); err == nil || settled {
					t.Fatalf("unbound historical packet accepted: settled=%v err=%v", settled, err)
				}
			}
			receipts.readErr = errors.New("receipt still starting or unreadable")
			if handled, changed, stopped, err := f.s.advancePlannerRuntime(context.Background(), x); !handled || changed || !stopped || err == nil {
				t.Fatalf("unknown command allowed Planner progression: %v %v %v %v", handled, changed, stopped, err)
			}
			receipts.readErr = nil
			receipts.unreleased = true
			if handled, changed, stopped, err := f.s.advancePlannerRuntime(context.Background(), x); !handled || changed || !stopped || err != nil {
				t.Fatalf("unreleased command allowed Planner progression: %v %v %v %v", handled, changed, stopped, err)
			}
			receipts.unreleased = false
			handled, changed, stopped, err := f.s.advancePlannerRuntime(context.Background(), x)
			if err != nil || !handled || !changed || stopped {
				t.Fatalf("settled original command blocked the second permitted Planner round: handled=%v changed=%v stopped=%v mismatches=%d err=%v", handled, changed, stopped, receipts.mismatches, err)
			}
			receiptBoundaryChecked = true
		}
		_, _, err = f.s.advanceComplexStandardExecution(context.Background(), h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatalf("transition %d: %v", i, err)
		}
	}
	t.Fatal("second coordination event was not reached")
}

func TestProjectPlannerMultiTaskLegacyBridgeCanAmendWholeAgreement(t *testing.T) {
	f, base, before := projectCoordinationSettledFailure(t, false, true)
	if len(before.Tasks) != 2 {
		t.Fatal("test plan does not contain two real tasks")
	}
	h := &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}
	h.builderDiagnosis = true
	h.mutateResult = func(r *core.PlannerCoordinationResult) {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		template := r.Amendments[0]
		r.Amendments = nil
		for _, task := range x.Tasks {
			amendment := template
			amendment.TaskKey = task.TaskKey
			r.Amendments = append(r.Amendments, amendment)
		}
	}
	f.s.chat = h
	for i := 0; i < 90; i++ {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if x.PlannerRuntime != nil && len(x.PlannerRuntime.Decisions) > 0 {
			d := x.PlannerRuntime.Decisions[0]
			t.Logf("legacy bridge affected=%v, valid project-wide proposal tasks=%d, decision source=%s decision=%s reason=%s summary=%s", x.PlannerRuntime.Events[0].Report.AffectedTaskKeys, len(x.Tasks), d.Source, d.Outcome, d.ReasonCode, d.Summary)
			if len(x.PlannerRuntime.Amendments) != 2 || d.Outcome != core.PlannerRuntimeAmend {
				t.Fatalf("project-wide amendment rejected after old final-review bridge: %+v", d)
			}
			report := x.PlannerRuntime.Events[0].Report
			if len(report.AffectedTaskKeys) != len(x.Tasks) || !strings.Contains(report.Summary, "Control Plane derived") || len(report.Evidence) != 1 {
				t.Fatalf("bridge did not retain explicit derived scope: %+v", report)
			}
			for _, task := range x.Tasks {
				if task.Status != "REWORK" {
					t.Fatalf("shared-contract task was not reopened for revalidation: key=%s status=%s", task.TaskKey, task.Status)
				}
			}
			return
		}
		progressed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatalf("transition %d progressed=%v stopped=%v: %v", i, progressed, stopped, err)
		}
	}
	x, _, _ := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
	planning, _, _ := f.store.GetClearDevComplexPlanning(context.Background(), h.requirementID)
	for _, step := range append(planning.AgentSteps, x.AgentSteps...) {
		if step.ID == x.PlannerRuntime.Requests[0].AgentStepID && step.FinalMessageText != "" {
			_, _, _, parseErr := core.ParsePlannerCoordinationResult([]byte(step.FinalMessageText), x.PlannerRuntime.Events[0], x.PlannerRuntime.Requests[0].ContextSHA256)
			t.Logf("two-task legacy bridge affected=%v, original Planner result status=%s reason=%s parser=%v", x.PlannerRuntime.Events[0].Report.AffectedTaskKeys, step.SendStatus, step.ReasonCode, parseErr)
		}
	}
	t.Fatalf("valid all-task agreement proposal cannot apply; planner replies=%d amendments=%d", h.plannerTurns, len(x.PlannerRuntime.Amendments))
}

func TestProjectPlannerMultiTaskSourceOnlyCannotApplyGlobalAgreement(t *testing.T) {
	f, base, before := projectCoordinationSettledFailure(t, false, true)
	h := &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}
	h.builderDiagnosis = true
	h.mutateResult = func(r *core.PlannerCoordinationResult) {
		r.Amendments[0].TaskKey = before.Tasks[len(before.Tasks)-1].TaskKey
	}
	f.s.chat = h
	for i := 0; i < 90; i++ {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if x.PlannerRuntime != nil && len(x.PlannerRuntime.Decisions) > 0 {
			d := x.PlannerRuntime.Decisions[0]
			t.Logf("source-only legacy proposal affected=%v outcome=%s reason=%s summary=%s", x.PlannerRuntime.Events[0].Report.AffectedTaskKeys, d.Outcome, d.ReasonCode, d.Summary)
			if d.Outcome != core.PlannerRuntimeStop || len(x.PlannerRuntime.Amendments) != 0 {
				t.Fatalf("source-only proposal must not partially revise a shared agreement: amendments=%d decision=%+v", len(x.PlannerRuntime.Amendments), d)
			}
			return
		}
		_, _, err = f.s.advanceComplexStandardExecution(context.Background(), h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
	}
	t.Fatal("source-only proposal did not settle")
}
