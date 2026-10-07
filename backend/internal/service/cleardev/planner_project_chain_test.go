package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// Real Service/SQLite, with explicit model, Git, checker, browser doubles.
// This checks both tasks through fresh verification and a new final acceptance.
func TestProjectPlannerMultiTaskRechecksEveryTask(t *testing.T) {
	f, base, before := projectCoordinationSettledFailure(t, false, true)
	h := &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: before.Run.DevelopmentRequirementID}
	h.builderDiagnosis = true
	h.finalVerdict = "PASS"
	h.mutateResult = func(r *core.PlannerCoordinationResult) {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		template := r.Amendments[0]
		r.Amendments = nil
		for _, task := range x.Tasks {
			a := template
			a.TaskKey = task.TaskKey
			r.Amendments = append(r.Amendments, a)
		}
	}
	f.s.chat = h
	var after core.ComplexExecutionSnapshot
	baseGuardChecked := false
	for i := 0; i < 180; i++ {
		var err error
		after, _, err = f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Run.CompletedAt != nil {
			break
		}
		if !baseGuardChecked && after.PlannerRuntime != nil && len(after.PlannerRuntime.Amendments) == 2 {
			task := after.Tasks[0]
			base, revised, err := plannerProjectDispatchBase(after, task, task.ReworkCount)
			if err != nil || !revised || base != before.FinalReview.CandidateCommitSHA {
				t.Fatalf("reopened first task did not retain the shared final head: base=%s revised=%v err=%v", base, revised, err)
			}
			d := core.ComplexExecutionDispatch{ID: "forged-engineering-base", ExecutionRunID: after.Run.ID, ComplexExecutionTaskID: task.ID, DevelopmentTaskID: task.DevelopmentTaskID, Round: task.ReworkCount, BaseCommitSHA: after.Run.InitialBaseCommitSHA, AgentStepID: "forged-engineering-step", ExecutionPackageSHA256: task.ExecutionPackageSHA256, ClientMessageID: "forged-engineering-message", Status: core.ComplexExecutionDispatchPending, CreatedAt: time.Now().UTC()}
			step := core.AgentStep{ID: d.AgentStepID, RoleBindingID: after.Run.BuilderRoleBindingID, Kind: core.ComplexExecutionAgentStepBuilderTask, RequestID: d.ID, ClientMessageID: d.ClientMessageID, PromptSHA256: strings.Repeat("a", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: d.CreatedAt}
			if _, _, err := f.store.CreateClearDevComplexExecutionDispatch(context.Background(), core.CreateComplexExecutionDispatchCommand{Dispatch: d, AgentStep: step}); err == nil {
				t.Fatal("transaction accepted the stale per-task base under a new revision")
			}
			// The second task's old PASS cannot satisfy its revised prerequisite.
			next := after.Tasks[1]
			later := d
			later.ID = "forged-early-dependent"
			later.AgentStepID = "forged-early-dependent-step"
			later.ClientMessageID = "forged-early-dependent-message"
			later.ComplexExecutionTaskID = next.ID
			later.DevelopmentTaskID = next.DevelopmentTaskID
			later.ExecutionPackageSHA256 = next.ExecutionPackageSHA256
			later.Round = next.ReworkCount
			later.BaseCommitSHA = base
			laterStep := step
			laterStep.ID = later.AgentStepID
			laterStep.RequestID = later.ID
			laterStep.ClientMessageID = later.ClientMessageID
			if _, _, err := f.store.CreateClearDevComplexExecutionDispatch(context.Background(), core.CreateComplexExecutionDispatchCommand{Dispatch: later, AgentStep: laterStep}); err == nil {
				t.Fatal("old prerequisite PASS allowed revised dependent dispatch")
			}
			unchanged, _, err := f.store.GetClearDevComplexExecution(context.Background(), h.requirementID)
			if err != nil || !reflect.DeepEqual(after, unchanged) {
				t.Fatal("rejected base left partial dispatch facts", err)
			}
			d.BaseCommitSHA = base
			savedHead := h.currentCandidate
			h.currentCandidate = forty("f")
			if err := f.s.validateComplexExecutionDispatchWorkspace(context.Background(), after, task, d, h.builderWorkspace); err == nil {
				t.Fatal("arbitrary workspace head accepted")
			}
			h.currentCandidate = savedHead
			if err := f.s.validateComplexExecutionDispatchWorkspace(context.Background(), after, task, d, h.builderWorkspace); err != nil {
				t.Fatal("exact shared frozen head rejected", err)
			}
			baseGuardChecked = true
		}
		progressed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatalf("transition %d: progress=%v stop=%v err=%v", i, progressed, stopped, err)
		}
	}
	if after.Run.CompletedAt == nil {
		for _, task := range after.Tasks {
			t.Logf("task key=%s status=%s round=%d rework=%d", task.TaskKey, task.Status, task.CurrentRound, task.ReworkCount)
		}
		for _, d := range after.Dispatches {
			t.Logf("dispatch id=%s task=%s round=%d status=%s reason=%s base=%s candidate=%s", d.ID, d.ComplexExecutionTaskID, d.Round, d.Status, d.ReasonCode, d.BaseCommitSHA, d.CandidateCommitSHA)
		}
		for _, step := range after.AgentSteps {
			t.Logf("step id=%s status=%s reason=%s final=%s", step.ID, step.SendStatus, step.ReasonCode, step.FinalMessageText)
		}
		for _, check := range after.CheckRuns {
			t.Logf("check kind=%s status=%s reason=%s candidate=%s", check.Kind, check.Status, check.ReasonCode, check.CandidateCommitSHA)
		}
		phase, reason := core.DeriveComplexExecutionPhase(after)
		t.Fatalf("fresh two-task verification did not complete: phase=%s reason=%s dispatches=%d verifications=%d checks=%d planner=%d", phase, reason, len(after.Dispatches), len(after.Verifications), len(after.CheckRuns), h.plannerTurns)
	}
	if !baseGuardChecked || len(after.Tasks) != 2 || h.plannerTurns != 1 || after.PlannerRuntime == nil || len(after.PlannerRuntime.Amendments) != 2 || len(after.Dispatches) != 5 || len(after.Verifications) != 4 || after.FinalReview.Verdict != "PASS" || after.FinalReview.CandidateCommitSHA == before.FinalReview.CandidateCommitSHA {
		t.Fatalf("two-task repair skipped fresh evidence: tasks=%d planner=%d dispatches=%d verifications=%d final=%+v", len(after.Tasks), h.plannerTurns, len(after.Dispatches), len(after.Verifications), after.FinalReview)
	}
	if after.Run.ExecutionPackageJSON != before.Run.ExecutionPackageJSON || after.Run.ExecutionPackageSHA256 != before.Run.ExecutionPackageSHA256 {
		t.Fatal("admission was changed")
	}
	for _, old := range before.Dispatches {
		found := false
		for _, current := range after.Dispatches {
			if old.ID == current.ID {
				found = true
				if !reflect.DeepEqual(old, current) {
					t.Fatal("old task dispatch changed")
				}
			}
		}
		if !found {
			t.Fatal("old task dispatch disappeared")
		}
	}
	contract, _, err := core.ProjectContractFromRun(after.Run)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range before.CheckRuns {
		found := false
		for _, check := range after.CheckRuns {
			if check.ID == old.ID {
				found = true
				if !reflect.DeepEqual(old, check) {
					t.Fatal("old check record was changed", old.ID)
				}
			}
		}
		if !found {
			t.Fatal("old check record disappeared", old.ID)
		}
	}
	for _, task := range after.Tasks {
		oldVerificationFound, newVerificationFound := false, false
		for _, v := range after.Verifications {
			if v.ComplexExecutionTaskID != task.ID {
				continue
			}
			var dispatch core.ComplexExecutionDispatch
			for _, d := range after.Dispatches {
				if d.ID == v.DispatchID {
					dispatch = d
				}
			}
			if dispatch.Round == 0 {
				oldVerificationFound = true
				continue
			}
			if dispatch.Round != task.CurrentRound {
				continue
			}
			if dispatch.ExecutionPackageSHA256 != task.ExecutionPackageSHA256 {
				t.Fatal("new dispatch uses wrong task contract", task.TaskKey)
			}
			newVerificationFound = true
			reviewFound := false
			for _, review := range after.Reviews {
				if review.ID == v.LocalReviewID && review.DispatchID == dispatch.ID && review.CandidateCommitSHA == v.CandidateCommitSHA && review.Verdict == "PASS" {
					reviewFound = true
				}
			}
			if !reviewFound {
				t.Fatal("fresh verification missing actual task review", task.TaskKey)
			}
			for _, id := range v.RequiredCheckRunIDs {
				found := false
				for _, check := range after.CheckRuns {
					if check.ID != id {
						continue
					}
					found = true
					if check.DispatchID != dispatch.ID || check.CandidateCommitSHA != v.CandidateCommitSHA || check.Status != core.ComplexExecutionCheckRunSettled || check.Result != core.EvidenceResultPass {
						t.Fatal("fresh check binding missing", task.TaskKey, id)
					}
					var receipt core.ProjectCheckReceipt
					if json.Unmarshal([]byte(check.OutputSummary), &receipt) != nil || receipt.ContractSHA256 != digest {
						t.Fatal("fresh check reused old contract receipt", task.TaskKey, id)
					}
				}
				if !found {
					t.Fatal("fresh required check missing", task.TaskKey, id)
				}
			}
		}
		if !oldVerificationFound || !newVerificationFound {
			t.Fatal("both original and new task evidence were not preserved", task.TaskKey)
		}
	}
	var packet core.RequirementFinalReviewPacket
	if json.Unmarshal([]byte(after.FinalReview.ReviewPacketJSON), &packet) != nil || packet.Run.RuntimeProjectExecution == nil || !reflect.DeepEqual(packet.Run.RuntimeProjectExecution, after.Run.RuntimeProjectExecution) || len(packet.Verifications) != 2 {
		t.Fatal("final review did not receive the revised agreement and current task evidence")
	}
	for _, v := range packet.Verifications {
		for _, task := range after.Tasks {
			if v.ComplexExecutionTaskID == task.ID && v.Round != task.CurrentRound {
				t.Fatal("new final review retained old verification", task.TaskKey)
			}
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?mode=ro&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var oldPacket, oldSHA, oldVerdict string
	err = db.QueryRowContext(context.Background(), "SELECT review_packet_json,review_packet_sha256,verdict FROM cleardev_requirement_final_reviews WHERE id=?", before.FinalReview.ID).Scan(&oldPacket, &oldSHA, &oldVerdict)
	if err != nil || oldPacket != before.FinalReview.ReviewPacketJSON || oldSHA != before.FinalReview.ReviewPacketSHA256 || oldVerdict != "BLOCKED" {
		t.Fatal("old final review was rewritten or lost", err)
	}
	reopened, err := sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	again, _, err := reopened.GetClearDevComplexExecution(context.Background(), h.requirementID)
	if err != nil || !reflect.DeepEqual(after, again) {
		t.Fatal("reopening database changed revised/current/history evidence", err)
	}
	t.Logf("actual SQLite repair completed: tasks=%d old/new verifications=%d dispatches=%d checks=%d new final=%s original final preserved=%s", len(after.Tasks), len(after.Verifications), len(after.Dispatches), len(after.CheckRuns), after.FinalReview.Verdict, oldVerdict)
}
