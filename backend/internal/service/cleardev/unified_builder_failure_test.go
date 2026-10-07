package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestUnifiedHistoricalFinalFeedbackIncludesSavedResult(t *testing.T) {
	f, h, before := settledHistoricalProjectFailure(t)
	db := builderReplacementStorageDB(t, f)
	var raw string
	if err := db.QueryRow(`SELECT result.raw_message_text FROM cleardev_agent_step_results result JOIN cleardev_requirement_final_reviews review ON review.result_id=result.id WHERE review.id=?`, before.FinalReview.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "" || before.FinalReview.FailureResultJSON != raw {
		t.Fatal("historical final result was not read exactly")
	}
	if _, changed, err := f.s.advanceBuilderFirstFinalFailure(context.Background(), before); err != nil || !changed {
		t.Fatal(changed, err)
	}
	h.finalVerdict = "PASS"
	_ = driveProjectFlow(t, f, before.Run.DevelopmentRequirementID, false)
	for _, relay := range h.relays {
		if strings.Contains(relay.prompt, before.FinalReview.ID) && strings.Contains(relay.prompt, "原最终验收失败") {
			if !strings.Contains(relay.prompt, raw) {
				t.Fatal("new Builder feedback omitted the saved detailed result")
			}
			return
		}
	}
	t.Fatal("failure was not relayed to Builder")
}

func TestUnifiedHistoricalBlockedRequiredFailureRepairsAndPreservesHistory(t *testing.T) {
	for _, cacheRetry := range []bool{false, true} {
		t.Run(map[bool]string{false: "general-runner", true: "cache-not-eligible"}[cacheRetry], func(t *testing.T) {
			f, h, id, before := blockedProjectDependencyFixture(t)
			f.s.complexExecution = f.store
			h.eligible = false
			if !cacheRetry {
				f.s.checks = knownBuilderFailureReceipt(h.projectExecutionFlowHarness, h.checkRequests...)
			}
			oldChecks, _ := json.Marshal(before.CheckRuns)
			oldDispatch, _ := json.Marshal(before.Dispatches[0])
			changed, stop, err := f.s.advanceComplexStandardExecution(context.Background(), id)
			if err != nil || !changed || stop {
				t.Fatal("known old failure stayed blocked", changed, stop, err)
			}
			if changed, err := f.store.ReturnClearDevRequiredFailureToBuilder(context.Background(), h.failedID, f.s.now().UTC()); err != nil || changed {
				t.Fatal("repeated registration", changed, err)
			}
			after := driveProjectFlow(t, f, id, false)
			if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 || after.Tasks[0].ReworkCount != 1 || after.Dispatches[1].CandidateCommitSHA == before.Dispatches[0].CandidateCommitSHA {
				t.Fatal("repair did not traverse new-candidate gates")
			}
			retained := []core.ComplexExecutionCheckRun{}
			for _, prior := range before.CheckRuns {
				for _, check := range after.CheckRuns {
					if prior.ID == check.ID {
						retained = append(retained, check)
					}
				}
			}
			gotChecks, _ := json.Marshal(retained)
			gotDispatch, _ := json.Marshal(after.Dispatches[0])
			if string(gotChecks) != string(oldChecks) || string(gotDispatch) != string(oldDispatch) || after.Run.ExecutionPackageJSON != before.Run.ExecutionPackageJSON {
				t.Fatal("repair rewrote old check/attempt/contract history")
			}
			feedback := false
			for _, relay := range h.relays {
				feedback = feedback || strings.Contains(relay.prompt, h.failedID) && strings.Contains(relay.prompt, "ENOTCACHED") && strings.Contains(relay.prompt, "检查失败反馈")
			}
			if !feedback {
				t.Fatal("historical exact failure omitted from feedback")
			}
		})
	}
}

func TestUnifiedHistoricalRequiredFailureConcurrentAndReopenIsUnique(t *testing.T) {
	f, h, id, before := blockedProjectDependencyFixture(t)
	f.s.complexExecution = f.store
	second, err := sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for _, store := range []*sqlite.Store{f.store, second} {
		wg.Add(1)
		go func(store *sqlite.Store) {
			defer wg.Done()
			changed, err := store.ReturnClearDevRequiredFailureToBuilder(context.Background(), h.failedID, f.s.now().UTC())
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Log("concurrent SQLite registration rejected", err)
			}
			if changed {
				wins++
			}
		}(store)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("repair registrations=%d", wins)
	}
	for _, store := range []*sqlite.Store{f.store, second} {
		if changed, err := store.ReturnClearDevRequiredFailureToBuilder(context.Background(), h.failedID, f.s.now().UTC()); err != nil || changed {
			t.Fatal("reopened registration repeated", changed, err)
		}
	}
	after, _, err := second.GetClearDevComplexExecution(context.Background(), id)
	if err != nil || after.Tasks[0].ReworkCount != 1 || len(after.Dispatches) != len(before.Dispatches) || after.Dispatches[0].Status != core.ComplexExecutionDispatchBlocked || after.Tasks[0].Status != core.DevelopmentTaskStatusRework {
		t.Fatal("persisted repair or history changed", err)
	}
}

func TestUnifiedHistoricalRequiredFailureRefusesStoppedAndUnknownWork(t *testing.T) {
	for _, mode := range []string{"paused", "stale-spec", "cancelled", "candidate-drift", "busy", "budget", "unsettled-check"} {
		t.Run(mode, func(t *testing.T) {
			f, h, id, before := blockedProjectDependencyFixture(t)
			f.s.complexExecution, f.s.checks = f.store, h.projectExecutionFlowHarness
			switch mode {
			case "paused", "stale-spec":
				stopBuilderFirstFixture(t, f, before, mode)
			case "cancelled":
				if err := f.s.CancelRequirement(context.Background(), id, "fixture cancellation"); err != nil {
					t.Fatal(err)
				}
			case "candidate-drift":
				h.currentCandidate = forty("e")
			case "busy":
				binding, _ := complexExecutionBindingByID(before, before.Dispatches[0].BuilderRoleBindingID)
				session, _, err := f.store.GetSession(context.Background(), domain.SessionID(binding.AOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				session.Activity.State = domain.ActivityActive
				if err := f.store.UpdateSession(context.Background(), session); err != nil {
					t.Fatal(err)
				}
			case "budget":
				db := builderReplacementStorageDB(t, f)
				for range 3 {
					if _, err := db.Exec(`UPDATE cleardev_complex_exception_budgets SET used_turns=used_turns+1 WHERE execution_run_id=? AND role_kind='BUILDER'`, before.Run.ID); err != nil {
						t.Fatal(err)
					}
				}
			case "unsettled-check":
				if changed, err := f.store.RecoverClearDevProjectDependencyCheck(context.Background(), h.failedID, f.s.now().UTC()); err != nil || !changed {
					t.Fatal("prepare exact pending retry", changed, err)
				}
				if changed, err := f.store.ReturnClearDevRequiredFailureToBuilder(context.Background(), h.failedID, f.s.now().UTC()); err == nil || changed {
					t.Fatal("pending check allowed a Builder repair", changed, err)
				}
				return
			}
			changed, _, _ := f.s.advanceComplexStandardExecution(context.Background(), id)
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil || changed || after.Tasks[0].ReworkCount != 0 || len(after.Dispatches) != 1 {
				t.Fatal("unsafe historical repair registered", changed, err)
			}
		})
	}
}

func TestUnifiedHistoricalRequiredMigrationRejectsBareRoundAndDowngrade(t *testing.T) {
	f, h, id, before := blockedProjectDependencyFixture(t)
	f.s.complexExecution, f.s.checks = f.store, knownBuilderFailureReceipt(h.projectExecutionFlowHarness, h.checkRequests...)
	db := builderReplacementStorageDB(t, f)
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var beforeVersion int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(context.Background(), 185); err == nil {
		t.Fatal("downgrade removed protected historical facts")
	}
	migrated, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
	old, _ := json.Marshal(before)
	got, _ := json.Marshal(migrated)
	if err != nil || string(old) != string(got) {
		t.Fatal("migration changed old execution facts", err)
	}
	d := before.Dispatches[0]
	d.ID, d.AgentStepID, d.Round, d.Status = "bare-round", "bare-step", 1, core.ComplexExecutionDispatchPending
	d.CreatedAt = f.s.now().UTC()
	step := core.AgentStep{ID: d.AgentStepID, RoleBindingID: d.BuilderRoleBindingID, Kind: core.ComplexExecutionAgentStepBuilderTask, RequestID: d.ID, ClientMessageID: "bare-message", PromptSHA256: coreDigest([]byte("bare")), SendStatus: core.AgentStepSendStatusPending, RequestedAt: d.CreatedAt}
	if _, created, err := f.store.CreateClearDevComplexExecutionDispatch(context.Background(), core.CreateComplexExecutionDispatchCommand{Dispatch: d, AgentStep: step}); err == nil || created {
		t.Fatal("new round accepted without exact task restart evidence")
	}
	if changed, _, err := f.s.advanceComplexStandardExecution(context.Background(), id); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if changed, _, err := f.s.advanceComplexStandardExecution(context.Background(), id); err != nil || !changed {
		t.Fatal("registered next round rejected", changed, err)
	}
	if _, err := provider.DownTo(context.Background(), 185); err == nil {
		t.Fatal("downgrade removed a rule used by the saved next round")
	}
	var version int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != beforeVersion {
		t.Fatal("refused downgrade changed migration version", version, err)
	}
}

// Only the external model/Git/check processes are doubles. Registration,
// budget accounting, new-candidate gates and replay use the production SQLite.
type unifiedFailureHarness struct {
	*projectExecutionFlowHarness
	requirementID string
	failure       string
}

func (h *unifiedFailureHarness) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	result, err := h.projectExecutionFlowHarness.RunCandidateCheck(ctx, request)
	if err != nil || h.failure == "" {
		return result, err
	}
	snapshot, _, err := h.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		return result, err
	}
	for _, check := range snapshot.CheckRuns {
		if check.ID != request.RunID || check.Kind != core.CandidateCheckIntegration {
			continue
		}
		result.OutputSummary = "fixture process/thread limit: resource temporarily unavailable; reduce project test concurrency"
		result.OutputSHA256 = coreDigest([]byte(result.OutputSummary))
		switch h.failure {
		case "infra":
			result.Outcome = ports.ClearDevCheckInfraError
			return result, errors.New("fixture preparation failed")
		case "timeout":
			result.Outcome = ports.ClearDevCheckTimedOut
			result.TimedOut = true
			result.ExitCode = 137
		default:
			result.Outcome = ports.ClearDevCheckFail
			result.ExitCode = 1
		}
	}
	return result, nil
}

func settledIntegrationFailure(t *testing.T, historical bool, failure string) (*projectPlanningFixture, *unifiedFailureHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	if historical {
		f.s.complexExecution = historicalProjectAdmissionStore{f.store}
	}
	base := attachProjectFlow(f, preparer)
	h := &unifiedFailureHarness{base, child.Requirement.ID, failure}
	f.s.checks = h
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		snapshot, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, found := core.CurrentIntegrationCheckFailure(snapshot); found {
			if failure == "infra" {
				f.s.checks = knownBuilderFailureReceipt(h.projectExecutionFlowHarness, h.checkRequests...)
			}
			return f, h, snapshot
		}
		changed, stop, err := f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID)
		if err != nil || stop || !changed {
			t.Fatalf("before failure: changed=%v stop=%v error=%v", changed, stop, err)
		}
	}
	t.Fatal("integration did not fail")
	return nil, nil, core.ComplexExecutionSnapshot{}
}

func TestUnifiedIntegrationFailureRepairsThroughAllGates(t *testing.T) {
	for _, failure := range []string{"fail", "timeout", "infra"} {
		for _, historical := range []bool{false, true} {
			name := failure + "/marked"
			if historical {
				name = failure + "/historical"
			}
			t.Run(name, func(t *testing.T) {
				f, h, before := settledIntegrationFailure(t, historical, failure)
				failed, _ := core.CurrentIntegrationCheckFailure(before)
				if phase, reason := core.DeriveComplexExecutionPhase(before); phase != core.ComplexExecutionBlocked || reason != failed.ReasonCode {
					t.Fatalf("incorrect current stop: %s %s", phase, reason)
				}
				handled, changed, err := f.s.advanceBuilderFirstIntegrationFailure(context.Background(), before)
				if !handled || !changed || err != nil {
					t.Fatalf("failure did not return: %v %v %v", handled, changed, err)
				}
				h.failure = ""
				after := driveProjectFlow(t, f, before.Run.DevelopmentRequirementID, false)
				if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 || len(after.Verifications) != 2 || after.Tasks[0].ReworkCount != 1 || after.FinalReview == nil || after.FinalReview.Verdict != "PASS" || after.FinalReview.CandidateCommitSHA == failed.CandidateCommitSHA {
					t.Fatal("new candidate did not traverse all gates")
				}
				if before.Run.ExecutionPackageJSON != after.Run.ExecutionPackageJSON || before.Run.ExecutionPackageSHA256 != after.Run.ExecutionPackageSHA256 {
					t.Fatal("old contract rewritten")
				}
				sourceFound := false
				for _, check := range after.CheckRuns {
					if check.ID == failed.ID {
						sourceFound = check.Status == failed.Status && check.Result == failed.Result && check.OutputSummary == failed.OutputSummary && check.OutputSHA256 == failed.OutputSHA256
					}
				}
				if !sourceFound {
					t.Fatal("original failure modified")
				}
				for _, old := range before.AgentSteps {
					current, found := complexExecutionStepByID(after, old.ID)
					if !found || current.PromptSHA256 != old.PromptSHA256 || current.ClientMessageID != old.ClientMessageID {
						t.Fatal("old message modified")
					}
				}
				matched := false
				for _, relay := range h.relays {
					if strings.Contains(relay.prompt, failed.ID) && strings.Contains(relay.prompt, "检查失败反馈") {
						matched = strings.Contains(relay.prompt, "进程和线程合计64") && strings.Contains(relay.prompt, "并发") && strings.Contains(relay.prompt, "resource temporarily unavailable") && strings.Contains(relay.prompt, failed.CandidateCommitSHA) && strings.Contains(relay.prompt, "保留所有检查和断言")
					}
				}
				if !matched {
					t.Fatal("Builder did not receive actionable failure and constraints")
				}
			})
		}
	}
}

func TestUnifiedIntegrationFailureDoesNotOfferOldFinalReview(t *testing.T) {
	f, _, before := settledIntegrationFailure(t, true, "fail")
	failed, _ := core.CurrentIntegrationCheckFailure(before)
	// Old review is a historical fact; no current action may target it.
	before.FinalReview = &core.RequirementFinalReview{ID: "old-review", Status: "SETTLED", Verdict: "BLOCKED", CandidateCommitSHA: forty("a"), ReasonCode: "REQUIREMENT_FINAL_REVIEW_BLOCKED"}
	for _, option := range core.WorkflowRecoveryOptions(before) {
		if option.TargetID == "old-review" {
			t.Fatal("offered stale final retry for integration failure")
		}
	}
	view, err := f.s.GetRequirement(context.Background(), before.Run.DevelopmentRequirementID)
	if err != nil {
		t.Fatal(err)
	}
	view.ComplexExecution = &before
	diagnosis := workflowDiagnosisFromFacts(view, core.WorkflowRecoveryView{ExecutionRunID: before.Run.ID}, f.s.now().UTC())
	found := false
	for _, issue := range diagnosis.Issues {
		found = found || issue.Relationship == "CURRENT" && issue.SubjectID == failed.ID && issue.SubjectType == "CHECK_RUN"
	}
	if !found {
		t.Fatal("current check failure missing from diagnosis")
	}
}

func TestUnifiedIntegrationFailureConcurrentAndRestartIsUnique(t *testing.T) {
	f, _, before := settledIntegrationFailure(t, true, "fail")
	failed, _ := core.CurrentIntegrationCheckFailure(before)
	var wg sync.WaitGroup
	var mu sync.Mutex
	changes := 0
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := f.store.ReturnClearDevIntegrationFailureToBuilder(context.Background(), failed.ID, f.s.now().UTC())
			if err != nil {
				t.Error(err)
			}
			if changed {
				mu.Lock()
				changes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if changes != 1 {
		t.Fatalf("registered %d repairs", changes)
	}
	reopened, err := sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	changed, err := reopened.ReturnClearDevIntegrationFailureToBuilder(context.Background(), failed.ID, f.s.now().UTC())
	if err != nil || changed {
		t.Fatalf("restart duplicated repair: %v %v", changed, err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
	if err != nil || after.Tasks[0].ReworkCount != 1 || len(after.Dispatches) != 1 {
		t.Fatal("unexpected durable round or dispatch")
	}
}

func TestUnifiedIntegrationFailureRejectsInvalidBoundary(t *testing.T) {
	for _, boundary := range []string{"busy", "terminated", "candidate-drift", "paused", "stale-spec", "cancelled", "budget", "unsettled"} {
		t.Run(boundary, func(t *testing.T) {
			f, h, before := settledIntegrationFailure(t, true, "fail")
			if boundary == "candidate-drift" {
				h.currentCandidate = forty("e")
			}
			if boundary == "paused" || boundary == "stale-spec" {
				stopBuilderFirstFixture(t, f, before, boundary)
			}
			if boundary == "cancelled" {
				if err := f.s.CancelRequirement(context.Background(), before.Run.DevelopmentRequirementID, "fixture"); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "busy" || boundary == "terminated" {
				binding, _ := complexExecutionBindingByID(before, before.Dispatches[0].BuilderRoleBindingID)
				session, _, err := f.store.GetSession(context.Background(), domain.SessionID(binding.AOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				if boundary == "busy" {
					session.Activity.State = domain.ActivityActive
				} else {
					session.IsTerminated = true
				}
				if err := f.store.UpdateSession(context.Background(), session); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "budget" {
				// Isolated fixture consumes the remaining existing role budget; no grant.
				db := builderReplacementStorageDB(t, f)
				for range 3 {
					if _, err := db.Exec("UPDATE cleardev_complex_exception_budgets SET used_turns=used_turns+1 WHERE execution_run_id=? AND role_kind='BUILDER' AND used_turns<max_turns+authorized_extra_turns", before.Run.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if boundary == "unsettled" {
				failed, _ := core.CurrentIntegrationCheckFailure(before)
				for i := range before.CheckRuns {
					if before.CheckRuns[i].ID == failed.ID {
						before.CheckRuns[i].Status = core.ComplexExecutionCheckRunStarted
						before.CheckRuns[i].SettledAt = nil
					}
				}
			}
			_, changed, _ := f.s.advanceBuilderFirstIntegrationFailure(context.Background(), before)
			if changed {
				t.Fatal("invalid boundary consumed repair")
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
			if err != nil || after.Tasks[0].ReworkCount != 0 || len(after.Dispatches) != 1 {
				t.Fatal("invalid boundary changed persisted task")
			}
		})
	}
}

func TestUnifiedHistoricalRequiredEnvironmentFailureReturnsToBuilder(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	f.s.complexExecution = historicalProjectAdmissionStore{f.store}
	h := attachProjectFlow(f, preparer)
	h.infraFailure = true
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	f.s.checks = knownBuilderFailureReceipt(h)
	for range 100 {
		snapshot, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Tasks) > 0 && snapshot.Tasks[0].Status == core.DevelopmentTaskStatusRework {
			h.infraFailure = false
			after := driveProjectFlow(t, f, child.Requirement.ID, false)
			if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 {
				t.Fatal("required-check repair skipped new candidate")
			}
			found := false
			for _, relay := range h.relays {
				found = found || strings.Contains(relay.prompt, "检查失败反馈") && strings.Contains(relay.prompt, "fixture native-driver build directory missing")
			}
			if !found {
				t.Fatal("historical Builder did not receive concrete failure")
			}
			return
		}
		changed, stop, err := f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID)
		if err != nil || stop || !changed {
			t.Fatalf("required-check flow: %v %v %v", changed, stop, err)
		}
	}
	t.Fatal("required-check failure did not rework")
}

func TestUnifiedBuilderDiagnosisDoesNotLoop(t *testing.T) {
	f, h, before := settledIntegrationFailure(t, true, "fail")
	h.builderDiagnosis = true
	if _, changed, err := f.s.advanceBuilderFirstIntegrationFailure(context.Background(), before); err != nil || !changed {
		t.Fatal(changed, err)
	}
	for range 30 {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.Dispatches) == 2 && x.Dispatches[1].Status == core.ComplexExecutionDispatchBlocked {
			sends := len(h.relays)
			for range 3 {
				_, _, _ = f.s.advanceComplexStandardExecution(context.Background(), before.Run.DevelopmentRequirementID)
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
			if err != nil || len(h.relays) != sends || after.Tasks[0].ReworkCount != 1 || len(after.Dispatches) != 2 {
				t.Fatal("unfixable diagnosis repeated")
			}
			return
		}
		_, _, err = f.s.advanceComplexStandardExecution(context.Background(), before.Run.DevelopmentRequirementID)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("Builder diagnosis did not stop")
}
