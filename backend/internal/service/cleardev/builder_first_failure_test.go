package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func settledProjectFailure(t *testing.T, verdict string) (*projectPlanningFixture, *projectExecutionFlowHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	h := attachProjectFlow(f, preparer)
	h.finalVerdict = verdict
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	driveProjectFlow(t, f, child.Requirement.ID, true)
	for range 30 {
		x, found, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
		if err != nil || !found {
			t.Fatal(err)
		}
		if x.FinalReview.Status == "SETTLED" {
			return f, h, x
		}
		if _, _, err := f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("final review did not settle")
	return nil, nil, core.ComplexExecutionSnapshot{}
}

func TestBuilderFirstFinalFailureRepairsAndReviewsNewCandidate(t *testing.T) {
	for _, verdict := range []string{"BLOCKED", "REWORK"} {
		t.Run(verdict, func(t *testing.T) {
			f, h, before := settledProjectFailure(t, verdict)
			handled, changed, err := f.s.advanceBuilderFirstFinalFailure(context.Background(), before)
			if err != nil || !handled || !changed {
				t.Fatalf("route failure: %v %v %v", handled, changed, err)
			}
			if changed, err := f.store.ReturnClearDevFinalFailureToBuilder(context.Background(), before.FinalReview.ID, f.s.now().UTC()); err != nil || changed {
				t.Fatalf("same failure spent twice: %v %v", changed, err)
			}
			h.finalVerdict = "PASS"
			after := driveProjectFlow(t, f, before.Run.DevelopmentRequirementID, false)
			if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 || after.Tasks[0].ReworkCount != 1 || len(after.Verifications) != 2 || after.FinalReview.CandidateCommitSHA == before.FinalReview.CandidateCommitSHA || after.FinalReview.Verdict != "PASS" {
				t.Fatalf("repair skipped new candidate/review: %+v", after)
			}
			matched := false
			for _, r := range h.relays {
				if strings.Contains(r.prompt, "原最终验收失败") {
					matched = matched || strings.Contains(r.prompt, before.FinalReview.ID) && strings.Contains(r.prompt, "The model double considered the whole Stage contract") && strings.Contains(r.prompt, "控制程序失败先由Builder诊断")
				}
			}
			if !matched {
				t.Fatal("Builder did not get full final failure and diagnosis instructions")
			}
		})
	}
}

func TestBuilderFirstFinalFailureConcurrentReturnIsUnique(t *testing.T) {
	f, _, before := settledProjectFailure(t, "BLOCKED")
	var wg sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := f.store.ReturnClearDevFinalFailureToBuilder(context.Background(), before.FinalReview.ID, f.s.now().UTC())
			if err != nil {
				t.Error(err)
			}
			if changed {
				mu.Lock()
				count++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if count != 1 {
		t.Fatalf("returned %d times", count)
	}
}

func TestBuilderFirstFinalFailureNeedsHumanStaysStopped(t *testing.T) {
	f, h, before := settledProjectFailure(t, "NEEDS_HUMAN")
	sends := len(h.relays)
	handled, changed, err := f.s.advanceBuilderFirstFinalFailure(context.Background(), before)
	if err != nil || handled || changed || len(h.relays) != sends {
		t.Fatal("human decision auto repaired")
	}
	if _, err := f.store.ReturnClearDevFinalFailureToBuilder(context.Background(), before.FinalReview.ID, f.s.now().UTC()); err == nil {
		t.Fatal("store accepted nonrepairable result")
	}
}

func TestBuilderFirstCheckEnvironmentFailureReachesOriginalBuilder(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	h := attachProjectFlow(f, preparer)
	h.infraFailure = true
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	f.s.checks = knownBuilderFailureReceipt(h)
	routed := false
	for range 100 {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.Tasks) > 0 && x.Tasks[0].Status == core.DevelopmentTaskStatusRework {
			routed = true
			break
		}
		_, stop, err := f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID)
		if err != nil || stop {
			t.Fatalf("environment failure stopped before Builder diagnosis: %v", err)
		}
	}
	if !routed {
		t.Fatal("no Builder repair round")
	}
	h.infraFailure = false
	after := driveProjectFlow(t, f, child.Requirement.ID, false)
	if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 {
		t.Fatal("environment preparation fix did not return through normal gates")
	}
	matched := false
	for _, r := range h.relays {
		if strings.Contains(r.prompt, "fixture native-driver build directory missing") {
			matched = true
		}
	}
	if !matched {
		t.Fatal("concrete infrastructure cause lost in Builder feedback")
	}
}

func TestBuilderFirstFinalFailureRestartKeepsUniqueReturn(t *testing.T) {
	f, _, before := settledProjectFailure(t, "BLOCKED")
	if changed, err := f.store.ReturnClearDevFinalFailureToBuilder(context.Background(), before.FinalReview.ID, f.s.now().UTC()); err != nil || !changed {
		t.Fatal(changed, err)
	}
	// Open another production store on the same durable facts, rather than
	// reusing an in-memory idempotency key.
	reopened, err := sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	if changed, err := reopened.ReturnClearDevFinalFailureToBuilder(context.Background(), before.FinalReview.ID, f.s.now().UTC()); err != nil || changed {
		t.Fatal("reopen repeated return", changed, err)
	}
}

func TestBuilderFirstFinalFailureCurrentBindingsRequired(t *testing.T) {
	for _, boundary := range []string{"busy", "terminated", "cancelled", "candidate-drift", "paused", "stale-spec"} {
		t.Run(boundary, func(t *testing.T) {
			f, h, before := settledProjectFailure(t, "BLOCKED")
			binding, _ := complexExecutionBindingByID(before, before.Dispatches[0].BuilderRoleBindingID)
			record, _, err := f.store.GetSession(context.Background(), domain.SessionID(binding.AOSessionID))
			if err != nil {
				t.Fatal(err)
			}
			switch boundary {
			case "busy":
				record.Activity.State = domain.ActivityActive
			case "terminated":
				record.IsTerminated = true
			case "cancelled":
				if err := f.s.CancelRequirement(context.Background(), before.Run.DevelopmentRequirementID, "test cancellation"); err != nil {
					t.Fatal(err)
				}
			case "candidate-drift":
				h.currentCandidate = forty("e")
			case "paused", "stale-spec":
				stopBuilderFirstFixture(t, f, before, boundary)
			}
			if boundary == "busy" || boundary == "terminated" {
				if err := f.store.UpdateSession(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			}
			_, changed, _ := f.s.advanceBuilderFirstFinalFailure(context.Background(), before)
			if changed {
				t.Fatal("stale/unavailable boundary spent repair round")
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Tasks[0].ReworkCount != before.Tasks[0].ReworkCount || len(after.Dispatches) != len(before.Dispatches) {
				t.Fatal("invalid route altered task")
			}
		})
	}
}

func stopBuilderFirstFixture(t *testing.T, f *projectPlanningFixture, x core.ComplexExecutionSnapshot, boundary string) {
	t.Helper()
	// Seed legal durable boundary facts only in this isolated database;
	// no requirement-pause API exists. Keep production triggers enabled.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	query, id := `UPDATE cleardev_development_projects SET paused_from_state=state,state='NEEDS_HUMAN' WHERE id=?`, x.Run.DevelopmentRequirementID
	if boundary == "stale-spec" {
		query, id = `UPDATE cleardev_contract_versions SET task_set_version=task_set_version+1 WHERE id=?`, x.Run.RequirementVersionID
	}
	if _, err := db.Exec(query, id); err != nil {
		t.Fatal(err)
	}
}

func TestBuilderFirstFailurePauseAfterReturnStopsRegisteredSend(t *testing.T) {
	for _, boundary := range []string{"paused", "stale-spec"} {
		t.Run(boundary, func(t *testing.T) {
			f, h, x := settledProjectFailure(t, "BLOCKED")
			if changed, err := f.store.ReturnClearDevFinalFailureToBuilder(context.Background(), x.FinalReview.ID, f.s.now().UTC()); err != nil || !changed {
				t.Fatal(changed, err)
			}
			// Register the next durable dispatch without sending its prompt yet.
			if changed, stop, err := f.s.advanceComplexStandardExecution(context.Background(), x.Run.DevelopmentRequirementID); err != nil || !changed || stop {
				t.Fatal(changed, stop, err)
			}
			before, _, err := f.store.GetClearDevComplexExecution(context.Background(), x.Run.DevelopmentRequirementID)
			if err != nil || len(before.Dispatches) != 2 || before.Dispatches[1].Status != core.ComplexExecutionDispatchRunning {
				t.Fatalf("missing registered-unsent dispatch: %+v %v", before.Dispatches, err)
			}
			step, exists := complexExecutionStepByID(before, before.Dispatches[1].AgentStepID)
			if !exists || step.SendStatus != core.AgentStepSendStatusPending {
				t.Fatal("fixture had already sent its registered prompt")
			}
			stopBuilderFirstFixture(t, f, before, boundary)
			sends := len(h.relays)
			for range 2 {
				if changed, stop, err := f.s.advanceComplexStandardExecution(context.Background(), x.Run.DevelopmentRequirementID); err != nil || changed || !stop {
					t.Fatal(changed, stop, err)
				}
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), x.Run.DevelopmentRequirementID)
			if err != nil || len(h.relays) != sends || after.Dispatches[1].Status != core.ComplexExecutionDispatchRunning || after.Tasks[0].ReworkCount != before.Tasks[0].ReworkCount {
				t.Fatal("stopped repair sent or spent another round", err)
			}
		})
	}
}

func TestBuilderFirstDiagnosisEscalatesMissingControlPlaneCapability(t *testing.T) {
	f, h, before := settledProjectFailure(t, "BLOCKED")
	h.builderDiagnosis = true
	for range 30 {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.Dispatches) == 2 && x.Dispatches[1].Status == core.ComplexExecutionDispatchBlocked {
			phase, reason := core.DeriveComplexExecutionPhase(x)
			if phase != core.ComplexExecutionCoordinating || reason != core.ReasonPlannerRuntimePending || h.finalSends != 1 || len(h.frozen) != 1 {
				t.Fatalf("new diagnosis hidden or retried old trial: %s %s", phase, reason)
			}
			if x.PlannerRuntime == nil || len(x.PlannerRuntime.Events) != 1 || len(x.PlannerRuntime.Amendments) != 0 {
				t.Fatal("settled Builder diagnosis was not routed to the original Planner")
			}
			return
		}
		changed, stop, err := f.s.advanceComplexStandardExecution(context.Background(), before.Run.DevelopmentRequirementID)
		if err != nil || stop || !changed {
			t.Fatalf("diagnosis path: %v %v %v", changed, stop, err)
		}
	}
	t.Fatal("Builder diagnosis did not settle")
}

func TestBuilderFirstFinalFailureDoesNotExpandBudget(t *testing.T) {
	f, h, before := settledProjectFailure(t, "BLOCKED")
	h.candidateSHAs = []string{forty("b"), forty("c"), forty("d"), forty("e")}
	for range 180 {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.Dispatches) == 4 && x.FinalReview.Status == "SETTLED" && x.Dispatches[3].CandidateCommitSHA != "" && x.FinalReview.CandidateCommitSHA == x.Dispatches[3].CandidateCommitSHA {
			if x.Tasks[0].ReworkCount != 3 || h.finalSends != 4 {
				t.Fatal("unexpected repair budget", x.Tasks[0].ReworkCount, h.finalSends)
			}
			for range 2 {
				_, changed, _ := f.s.advanceBuilderFirstFinalFailure(context.Background(), x)
				if changed {
					t.Fatal("exhausted budget obtained another round")
				}
			}
			y, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if len(y.Dispatches) != 4 || y.Tasks[0].ReworkCount != 3 {
				t.Fatal("budget changed")
			}
			return
		}
		changed, stop, err := f.s.advanceComplexStandardExecution(context.Background(), before.Run.DevelopmentRequirementID)
		if err != nil || stop || !changed {
			t.Fatalf("bounded repair flow: %v %v %v", changed, stop, err)
		}
	}
	t.Fatal("repair budget test exceeded transitions")
}

func TestBuilderFirstFailureFeedbackPreservesTailCause(t *testing.T) {
	f, child, admission, _ := plannedProjectExecutionFixture(t, "EXISTING")
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	x, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	output := "prepare begin\n" + strings.Repeat("dependency listing\n", 3000) + "SQLITE_CANTOPEN: native-driver build unavailable"
	command := complexSettleCheckCommand("check-source", ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, OutputSummary: output}, errors.New("last installation error"), f.s.now().UTC())
	if !strings.Contains(command.OutputSummary, output) || command.OutputSHA256 != coreDigest([]byte(command.OutputSummary)) {
		t.Fatal("durable failure output or hash changed")
	}
	receipt, err := json.Marshal(struct {
		SchemaVersion int    `json:"schemaVersion"`
		Policy        string `json:"policy"`
		OutputSummary string `json:"outputSummary"`
	}{1, core.ProjectCheckPolicyV1, output})
	if err != nil {
		t.Fatal(err)
	}
	task := core.ComplexExecutionTask{ID: "task", CurrentRound: 1}
	x.Dispatches = []core.ComplexExecutionDispatch{{ID: "old", ComplexExecutionTaskID: "task", Round: 0, ReasonCode: "CHECKER_UNAVAILABLE"}}
	x.CheckRuns = []core.ComplexExecutionCheckRun{{ID: "check-source", DispatchID: "old", Kind: core.CandidateCheckRequired, Status: core.ComplexExecutionCheckRunFailed, ReasonCode: "CHECKER_UNAVAILABLE", CandidateCommitSHA: forty("b"), OutputSummary: string(receipt)}}
	feedback := complexExecutionReworkFeedback(x, task)
	if !strings.Contains(feedback, "prepare begin") || !strings.Contains(feedback, "SQLITE_CANTOPEN") || !strings.Contains(feedback, "check-source") || !strings.Contains(feedback, forty("b")) {
		t.Fatal("Builder failure cause/source missing")
	}
}

func TestBuilderFailureUnwrapsFreshAndHistoricalReceipts(t *testing.T) {
	for _, policy := range []string{core.ProjectCheckPolicyV1, core.ProjectCheckPolicyV2} {
		raw, err := json.Marshal(map[string]string{"policy": policy, "outputSummary": "npm install failed\nexact cause"})
		if err != nil {
			t.Fatal(err)
		}
		if got := clipBuilderFailureReceiptOutput(string(raw)); got != "npm install failed\nexact cause" {
			t.Fatalf("%s: %q", policy, got)
		}
	}
}
