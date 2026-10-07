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
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type projectDependencyRecoveryHarness struct {
	*projectExecutionFlowHarness
	terminated bool
	failures   int
	failedID   string
	eligible   bool
}

func (h *projectDependencyRecoveryHarness) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	if h.failures > 0 {
		h.failures--
		h.failedID = request.RunID
		if h.terminated {
			result, err := h.projectExecutionFlowHarness.RunCandidateCheck(ctx, request)
			if err != nil {
				return result, err
			}
			result.Outcome, result.ExitCode = ports.ClearDevCheckFail, 137
			result.TrialCommandExecuted = true
			result.OutputSummary = "build process terminated with signal 9"
			result.OutputSHA256 = coreDigest([]byte(result.OutputSummary))
			return result, nil
		}
		h.checkRequests = append(h.checkRequests, request)
		output := "offline npm dependency preparation failed: ENOTCACHED"
		return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, CandidateSHA: request.CandidateSHA, OutputSummary: output, OutputSHA256: coreDigest([]byte(output))}, errors.New(output)
	}
	return h.projectExecutionFlowHarness.RunCandidateCheck(ctx, request)
}

func (h *projectDependencyRecoveryHarness) CanRetryProjectDependencyCheck(_ context.Context, request ports.ClearDevCheckRequest) (bool, error) {
	return h.eligible && request.ProjectExecution != nil && request.RunID == h.failedID, nil
}

func blockedProjectDependencyFixture(t *testing.T) (*projectPlanningFixture, *projectDependencyRecoveryHarness, string, core.ComplexExecutionSnapshot) {
	t.Helper()
	return projectDependencyFixture(t, false, false)
}

func projectDependencyFixture(t *testing.T, second, business bool) (*projectPlanningFixture, *projectDependencyRecoveryHarness, string, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, child, admission, preparer := plannedDependencyChecksFixture(t, second)
	// These tests cover the immutable pre-policy recovery contract.
	f.s.complexExecution = &legacyFailureAdmissionStore{f.store}
	h := &projectDependencyRecoveryHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), failures: 1, eligible: true}
	if business {
		h.failures = 0
		h.checkFailure = true
	}
	f.s.checks = h
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		e, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		if business {
			for _, check := range e.CheckRuns {
				if check.Kind == core.CandidateCheckRequired && check.Result == core.EvidenceResultFail {
					return f, h, child.Requirement.ID, e
				}
			}
		}
		if phase, reason := core.DeriveComplexExecutionPhase(e); phase == core.ComplexExecutionBlocked && reason == "CHECKER_UNAVAILABLE" {
			return f, h, child.Requirement.ID, e
		}
		changed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID)
		if err != nil || stopped || !changed {
			t.Fatalf("prepare blocked fixture: changed=%v stop=%v err=%v", changed, stopped, err)
		}
	}
	t.Fatal("did not reach dependency failure")
	return nil, nil, "", core.ComplexExecutionSnapshot{}
}

func TestProjectDependencyCheckRecoveryPreservesCandidateBudgetAndOriginalFailure(t *testing.T) {
	f, h, id, before := blockedProjectDependencyFixture(t)
	old, err := json.Marshal(before.CheckRuns)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Reviews) != 0 {
		t.Fatal("review preceded check")
	}
	changed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), id)
	if err != nil || !changed || stopped {
		t.Fatalf("recover: changed=%v stop=%v err=%v", changed, stopped, err)
	}
	// Reopen the actual database. The same retry row, attempt and budget survive.
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.service()
	h.store = f.store
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h
	f.s.finalReviews, f.s.resultPreview = f.store, h.trial
	h.service = f.s
	f.s.runBackground = func(func()) {}
	after := driveProjectFlow(t, f, id, false)
	if len(after.Dispatches) != len(before.Dispatches) || after.Dispatches[0].CandidateCommitSHA != before.Dispatches[0].CandidateCommitSHA || after.Run.CompletedAt == nil {
		t.Fatal("recovery rebuilt work or candidate")
	}
	if len(after.CheckRuns) != len(before.CheckRuns)+2 {
		t.Fatalf("unexpected check count: before=%d after=%d", len(before.CheckRuns), len(after.CheckRuns))
	}
	retained := []core.ComplexExecutionCheckRun{}
	for _, prior := range before.CheckRuns {
		for _, check := range after.CheckRuns {
			if prior.ID == check.ID {
				retained = append(retained, check)
			}
		}
	}
	got, _ := json.Marshal(retained)
	if string(got) != string(old) {
		t.Fatal("old check history changed")
	}
	for _, budget := range after.Exception.Budgets {
		if budget.RoleKind == core.ComplexExceptionBudgetBuilder && budget.UsedTurns != 1 {
			t.Fatalf("recovery consumed Builder budget: %+v", budget)
		}
	}
	if changed, err := f.store.RecoverClearDevProjectDependencyCheck(context.Background(), h.failedID, time.Now()); err != nil || changed {
		t.Fatalf("replay recovery: %v %v", changed, err)
	}
}

func TestProjectDependencyCheckRecoveryOnlyOnceAndUnknownStops(t *testing.T) {
	for _, mode := range []string{"second-failure", "unknown", "source-drift", "cancelled", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			f, h, id, before := blockedProjectDependencyFixture(t)
			original := h.failedID
			switch mode {
			case "second-failure":
				h.failures = 1
			case "unknown":
				h.eligible = false
			case "source-drift":
				h.source.BaseCommitSHA = forty("b")
			case "cancelled":
				if err := f.s.CancelRequirement(context.Background(), id, "Explicit cancellation fixture"); err != nil {
					t.Fatal(err)
				}
			case "concurrent":
				var wg sync.WaitGroup
				var mu sync.Mutex
				wins := 0
				for i := 0; i < 6; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						changed, err := f.store.RecoverClearDevProjectDependencyCheck(context.Background(), original, time.Now().UTC())
						mu.Lock()
						defer mu.Unlock()
						if err != nil {
							t.Error(err)
						}
						if changed {
							wins++
						}
					}()
				}
				wg.Wait()
				if wins != 1 {
					t.Fatalf("concurrent recovery winners=%d", wins)
				}
			}
			for i := 0; i < 100; i++ {
				changed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), id)
				if (mode == "source-drift" || mode == "cancelled") && errors.Is(err, errComplexExecutionStopped) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if stopped || !changed {
					break
				}
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			retries := 0
			for _, check := range after.CheckRuns {
				if check.RetryOrdinal == 1 {
					retries++
				}
			}
			if mode == "unknown" || mode == "source-drift" || mode == "cancelled" {
				if retries != 0 || len(after.CheckRuns) != len(before.CheckRuns) {
					t.Fatal("unsafe recovery")
				}
			}
			if mode == "second-failure" {
				if retries != 1 || len(after.Reviews) != 0 {
					t.Fatal("second failure retried or reviewed")
				}
			}
		})
	}
}

func TestProjectBuilderDependencyInstructionsPreserveAlreadySentPrompt(t *testing.T) {
	f, child, admission, _ := plannedProjectExecutionFixture(t, "EXISTING")
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	e, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy := executionBuilderPromptForRunLegacy(e.Run, []byte("{}"), "dispatch", "task", 0, "")
	next := executionBuilderPromptForRun(e.Run, []byte("{}"), "dispatch", "task", 0, "")
	if !strings.Contains(next, "npm ci --offline=false") || legacy == next {
		t.Fatal("Builder cannot install missing dependencies")
	}
	priorProject := strings.TrimSuffix(executionBuilderPromptBeforeEnvironmentGuidance(e.Run, []byte("{}"), "dispatch", "task", 0, ""), core.ProjectPlannerRuntimeBuilderPromptSuffix())
	if got := executionBuilderPromptForExistingStep(e.Run, []byte("{}"), "dispatch", "task", 0, "", coreDigest([]byte(priorProject))); got != priorProject {
		t.Fatal("pre-coordination Builder prompt changed")
	}
	writable := strings.Replace(legacy, projectBuilderOfflineDependencies, projectBuilderWritableDependencies, 1)
	if got := executionBuilderPromptForExistingStep(e.Run, []byte("{}"), "dispatch", "task", 0, "", coreDigest([]byte(writable))); got != writable {
		t.Fatal("previous writable instructions changed")
	}
	installed := strings.Replace(legacy, projectBuilderOfflineDependencies, projectBuilderInstallDependencies, 1)
	if got := executionBuilderPromptForExistingStep(e.Run, []byte("{}"), "dispatch", "task", 0, "", coreDigest([]byte(installed))); got != installed {
		t.Fatal("previous install instructions changed")
	}
	if !strings.Contains(next, "独立可写") || !strings.Contains(next, "npm rebuild") {
		t.Fatal("new work lacks writable dependency instructions")
	}
	if got := executionBuilderPromptForExistingStep(e.Run, []byte("{}"), "dispatch", "task", 0, "", coreDigest([]byte(legacy))); got != legacy {
		t.Fatal("historical prompt changed")
	}
}

func TestProjectBuilderReceiptFeedbackPreservesExistingStepPrompt(t *testing.T) {
	f, child, admission, _ := plannedProjectExecutionFixture(t, "EXISTING")
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	execution, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := core.ComplexExecutionDispatch{ID: "old-dispatch", ComplexExecutionTaskID: "task", Round: 0, ReasonCode: "CHECK_FAILED"}
	receipt := `{"schemaVersion":1,"policy":"PROJECT_CHECK_V1","contractSha256":"` + strings.Repeat("a", 1600) + `","outputSummary":"Error: EACCES: permission denied, mkdir '/workspace/test/artifacts/runtime'"}`
	execution.Tasks = []core.ComplexExecutionTask{{ID: "task", DevelopmentTaskID: "work-item", CurrentRound: 1}}
	execution.Dispatches = []core.ComplexExecutionDispatch{dispatch}
	execution.CheckRuns = []core.ComplexExecutionCheckRun{{DispatchID: dispatch.ID, Kind: core.CandidateCheckRequired, Status: core.ComplexExecutionCheckRunSettled, Result: core.EvidenceResultFail, ReasonCode: "CHECK_FAILED", OutputSummary: receipt}}
	historical := complexExecutionReworkFeedbackLegacy(execution, execution.Tasks[0])
	current := complexExecutionReworkFeedback(execution, execution.Tasks[0])
	if strings.Contains(historical, "EACCES") || !strings.Contains(current, "EACCES") {
		t.Fatalf("receipt feedback did not distinguish saved and new prompt bytes: old=%q new=%q", historical, current)
	}
	for _, oldPrompt := range []string{
		strings.Replace(executionBuilderPromptBeforeEnvironmentGuidance(execution.Run, []byte("{}"), "next-dispatch", "work-item", 1, historical), projectBuilderReliableDependencies, projectBuilderInstallDependencies, 1),
		executionBuilderPromptBeforeEnvironmentGuidance(execution.Run, []byte("{}"), "next-dispatch", "work-item", 1, historical),
		strings.TrimSuffix(executionBuilderPromptBeforeEnvironmentGuidance(execution.Run, []byte("{}"), "next-dispatch", "work-item", 1, historical), core.ProjectPlannerRuntimeBuilderPromptSuffix()),
		executionBuilderPromptForRunLegacy(execution.Run, []byte("{}"), "next-dispatch", "work-item", 1, historical),
	} {
		got := executionBuilderPromptForExistingStep(execution.Run, []byte("{}"), "next-dispatch", "work-item", 1, current, coreDigest([]byte(oldPrompt)), historical)
		if got != oldPrompt {
			t.Fatal("saved Builder step prompt changed across feedback repair")
		}
	}
}

func plannedDependencyChecksFixture(t *testing.T, second bool) (*projectPlanningFixture, RequirementView, core.ProjectExecutionAdmission, *projectExecutionPreparer) {
	t.Helper()
	if !second {
		return plannedProjectExecutionFixture(t, "EXISTING")
	}
	f := newProjectPlanningFixture(t, "EXISTING")
	initial := f.create(t)
	var proposal core.ProductDiscoveryResult
	if err := json.Unmarshal([]byte(genericProjectReply("existing")), &proposal); err != nil {
		t.Fatal(err)
	}
	extra := proposal.Stages[0].ExecutionBasis.Checks[0]
	extra.ID = "notes-tests-second"
	proposal.Stages[0].ExecutionBasis.Checks = append(proposal.Stages[0].ExecutionBasis.Checks, extra)
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
	var plan map[string]any
	if err := json.Unmarshal([]byte(genericEngineeringReply(t, child.RequirementVersions[0])), &plan); err != nil {
		t.Fatal(err)
	}
	plan["tasks"].([]any)[0].(map[string]any)["requiredCheckIds"] = []string{"notes-tests", extra.ID}
	raw, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	f.h.replies = append(f.h.replies, string(raw))
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	child = mustGetComplex(t, f.s, child.Requirement.ID)
	if child.ComplexPlanning == nil || len(child.ComplexPlanning.Plans) != 1 {
		t.Fatal("two-check plan not saved")
	}
	saved := child.ComplexPlanning.Plans[0]
	admission := core.ProjectExecutionAdmission{RequestID: "start-project", PlanID: saved.ID, PlanSHA256: saved.PlanSHA256, RequirementSHA256: child.RequirementVersions[0].SHA256, BaseCommitSHA: selected.Selection.BaseCommitSHA}
	preparer := &projectExecutionPreparer{projectPlanningAgent: f.h}
	f.s.checks, f.s.finalReviews = preparer, f.store
	f.s.runBackground = func(func()) {}
	return f, child, admission, preparer
}

func TestProjectDependencyCheckRecoveryLimitsWholeCandidate(t *testing.T) {
	f, h, id, before := projectDependencyFixture(t, true, false)
	if changed, _, err := f.s.advanceComplexStandardExecution(context.Background(), id); err != nil || !changed {
		t.Fatalf("first recovery: %v %v", changed, err)
	}
	// Let the first recovered check pass, then independently fail the next check.
	for i := 0; i < 100; i++ {
		e, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		passed := false
		for _, c := range e.CheckRuns {
			passed = passed || (c.RetryOrdinal == 1 && c.Result == core.EvidenceResultPass)
		}
		if passed {
			break
		}
		if _, _, err := f.s.advanceComplexStandardExecution(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	h.failures = 1
	var blocked core.ComplexExecutionSnapshot
	for i := 0; i < 100; i++ {
		e, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if phase, _ := core.DeriveComplexExecutionPhase(e); phase == core.ComplexExecutionBlocked {
			blocked = e
			break
		}
		if _, _, err := f.s.advanceComplexStandardExecution(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if blocked.Run.ID == "" {
		t.Fatal("second required check did not block")
	}
	if h.failedID == before.CheckRuns[len(before.CheckRuns)-1].ID {
		t.Fatal("did not exercise another original check")
	}
	raw, _ := json.Marshal(blocked)
	if changed, _, err := f.s.advanceProjectDependencyCheckRecovery(context.Background(), blocked); err != nil || changed {
		t.Fatalf("second candidate recovery: %v %v", changed, err)
	}
	if changed, err := f.store.RecoverClearDevProjectDependencyCheck(context.Background(), h.failedID, time.Now().UTC()); err == nil || changed {
		t.Fatalf("transaction admitted second recovery: %v %v", changed, err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(after)
	if string(got) != string(raw) {
		t.Fatal("rejection changed candidate facts/budget/history")
	}
}

func TestProjectDependencyCheckRecoveryRejectsBusinessFailure(t *testing.T) {
	f, _, id, before := projectDependencyFixture(t, false, true)
	failed := ""
	for _, c := range before.CheckRuns {
		if c.Kind == core.CandidateCheckRequired && c.Result == core.EvidenceResultFail {
			failed = c.ID
		}
	}
	if failed == "" {
		t.Fatal("no business failure")
	}
	assertDependencyRecoveryRejected(t, f, id, before, failed)
}

func TestProjectDependencyCheckRecoveryRejectsStoredDirectionStop(t *testing.T) {
	f, h, id, before := blockedProjectDependencyFixture(t)
	// Seed an explicit test-only direction receipt and ACTIVE gate in real SQLite.
	// Product-stage direction revision is not exposed by today's public API; this
	// exercises the persisted stop guard without inventing a real user decision.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// The public stage policy prevents creating direction revisions. Seed this
	// defensive fixture with that unrelated admission trigger temporarily absent,
	// then restore its exact SQL before testing the recovery transaction. Keep all
	// foreign keys and the recovery guards enabled throughout.
	var admissionGuard string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='trigger' AND name='cleardev_product_stage_no_direction'").Scan(&admissionGuard); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TRIGGER cleardev_product_stage_no_direction"); err != nil {
		t.Fatal(err)
	}
	var steward string
	if err := db.QueryRow("SELECT id FROM cleardev_complex_role_bindings WHERE development_project_id=? AND role='STEWARD'", id).Scan(&steward); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO cleardev_direction_intents VALUES('dependency-stop-intent',?,?,?,?,?,?, 'dependency-stop-request','dependency-stop-step',?)`, []any{id, before.Run.RequirementVersionID, before.Run.RequirementSHA256, "Explicit test stop", digest, steward, at}},
		{`INSERT INTO cleardev_direction_agent_steps(id,role_binding_id,step_kind,request_id,client_message_id,prompt_sha256,send_status,turn_id,final_message_id,final_message_text,message_sha256,requested_at,sent_at,completed_at) VALUES('dependency-stop-step',?,'DIRECTION_CHANGE_REQUEST','dependency-stop-request','dependency-stop-message',?,'SETTLED','test-turn','test-result','Explicit test stop',?,?,?,?)`, []any{steward, digest, digest, at, at, at}},
		{`INSERT INTO cleardev_direction_requests VALUES('dependency-stop-request','dependency-stop-intent',?,?,'Explicit test stop','[]',?,'dependency-stop-step',?)`, []any{id, before.Run.RequirementVersionID, digest, at}},
		{`INSERT INTO cleardev_direction_stop_gates VALUES('dependency-stop-gate','dependency-stop-request',?,?,?,?, 'ACTIVE',NULL,'',?)`, []any{id, before.Run.RequirementVersionID, before.Run.TaskSetVersion, digest, at}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(admissionGuard); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='trigger' AND name='cleardev_product_stage_no_direction'").Scan(&restored); err != nil || restored != admissionGuard {
		t.Fatal("direction admission guard not restored")
	}
	if stopped, err := f.store.HasActiveClearDevDirectionStop(context.Background(), before.Run.RequirementVersionID); err != nil || !stopped {
		t.Fatalf("real stop gate missing: %v %v", stopped, err)
	}
	assertDependencyRecoveryRejected(t, f, id, before, h.failedID)
}

func assertDependencyRecoveryRejected(t *testing.T, f *projectPlanningFixture, id string, before core.ComplexExecutionSnapshot, failed string) {
	t.Helper()
	raw, _ := json.Marshal(before)
	_, changed, _ := f.s.advanceProjectDependencyCheckRecovery(context.Background(), before)
	if changed {
		t.Fatal("service recovered stopped/failed work")
	}
	if changed, err := f.store.RecoverClearDevProjectDependencyCheck(context.Background(), failed, time.Now().UTC()); err == nil || changed {
		t.Fatalf("transaction recovered stopped/failed work: %v %v", changed, err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(after)
	if string(got) != string(raw) {
		t.Fatal("rejection changed candidate facts/budget/history")
	}
}

func TestProjectDependencyCheckRecoveryContinuesAfterBuilderExit(t *testing.T) {
	for _, barrier := range []bool{false, true} {
		t.Run(map[bool]string{false: "exited-before-recovery", true: "existing-false-barrier"}[barrier], func(t *testing.T) {
			f, h, id, before := blockedProjectDependencyFixture(t)
			original := h.failedID
			if changed, _, err := f.s.advanceComplexStandardExecution(context.Background(), id); err != nil || !changed {
				t.Fatalf("create retry: %v %v", changed, err)
			}
			pending, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			builder := pending.RoleBindings[0]
			record, found, err := f.store.GetSession(context.Background(), domain.SessionID(builder.AOSessionID))
			if err != nil || !found {
				t.Fatal(err)
			}
			record.Activity.State = domain.ActivityExited
			if err := f.store.UpdateSession(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			retryID := original + ":project-dependency-retry"
			if barrier {
				if _, _, err := f.s.failComplexExecutionDispatch(context.Background(), pending, pending.Tasks[0], pending.Dispatches[0], true, core.ReasonBuilderSpawnFailed); err != nil {
					t.Fatal(err)
				}
				var wins int
				var mu sync.Mutex
				var wg sync.WaitGroup
				for i := 0; i < 4; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						changed, err := f.store.ContinueClearDevProjectDependencyCheck(context.Background(), retryID, time.Now().UTC())
						mu.Lock()
						defer mu.Unlock()
						if err != nil {
							t.Error(err)
						}
						if changed {
							wins++
						}
					}()
				}
				wg.Wait()
				if wins != 1 {
					t.Fatalf("continuation winners=%d", wins)
				}
			}
			after := driveProjectFlow(t, f, id, false)
			if len(after.CheckRuns) != len(before.CheckRuns)+2 || len(after.Dispatches) != 1 || after.Dispatches[0].CandidateCommitSHA != before.Dispatches[0].CandidateCommitSHA || len(after.Reviews) != 1 {
				t.Fatal("exited Builder did not reach review through original checks")
			}
			for _, budget := range after.Exception.Budgets {
				if budget.RoleKind == core.ComplexExceptionBudgetBuilder && budget.UsedTurns != 1 {
					t.Fatal("extra Builder work")
				}
			}
			if barrier {
				if changed, err := f.store.ContinueClearDevProjectDependencyCheck(context.Background(), retryID, time.Now().UTC()); err != nil || changed {
					t.Fatalf("continuation replay: %v %v", changed, err)
				}
			}
		})
	}
}

func TestProjectDependencyCheckContinuationRejectsStartedOrTerminated(t *testing.T) {
	for _, mode := range []string{"started", "terminated", "no-barrier"} {
		t.Run(mode, func(t *testing.T) {
			f, h, id, _ := blockedProjectDependencyFixture(t)
			if changed, _, err := f.s.advanceComplexStandardExecution(context.Background(), id); err != nil || !changed {
				t.Fatal(err)
			}
			pending, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			retryID := h.failedID + ":project-dependency-retry"
			record, found, err := f.store.GetSession(context.Background(), domain.SessionID(pending.RoleBindings[0].AOSessionID))
			if err != nil || !found {
				t.Fatal(err)
			}
			record.Activity.State = domain.ActivityExited
			record.IsTerminated = mode == "terminated"
			if err := f.store.UpdateSession(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if mode == "started" {
				if _, err := f.store.StartClearDevComplexExecutionCheckRun(context.Background(), retryID, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "no-barrier" {
				if _, _, err := f.s.failComplexExecutionDispatch(context.Background(), pending, pending.Tasks[0], pending.Dispatches[0], true, core.ReasonBuilderSpawnFailed); err != nil {
					t.Fatal(err)
				}
			}
			before, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(before)
			if changed, err := f.store.ContinueClearDevProjectDependencyCheck(context.Background(), retryID, time.Now().UTC()); err == nil || changed {
				t.Fatalf("unsafe continuation: %v %v", changed, err)
			}
			_, changed, _ := f.s.advanceProjectDependencyCheckRecovery(context.Background(), before)
			if changed {
				t.Fatal("service admitted unsafe continuation")
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(after)
			if string(raw) != string(got) {
				t.Fatal("rejection changed original checks/budget")
			}
		})
	}
}

// Seed the pre-policy stop. These cache-specific tests isolate their existing
// retry; unified tests remove this seam and use the production Builder return.
type legacyFailureAdmissionStore struct{ *sqlite.Store }

func (s *legacyFailureAdmissionStore) ReturnClearDevRequiredFailureToBuilder(context.Context, string, time.Time) (bool, error) {
	return false, nil
}

func (s *legacyFailureAdmissionStore) StartClearDevComplexExecution(ctx context.Context, command core.StartComplexExecutionCommand) (core.ComplexExecutionRun, bool, error) {
	var pkg core.ComplexExecutionRunPackage
	if err := json.Unmarshal([]byte(command.Run.ExecutionPackageJSON), &pkg); err != nil {
		return core.ComplexExecutionRun{}, false, err
	}
	pkg.BuilderFailurePolicy = ""
	raw, err := json.Marshal(pkg)
	if err != nil {
		return core.ComplexExecutionRun{}, false, err
	}
	command.Run.ExecutionPackageJSON, command.Run.ExecutionPackageSHA256 = string(raw), coreDigest(raw)
	return s.Store.StartClearDevComplexExecution(ctx, command)
}

// Seed a historical already-blocked required check using the former failure
// transition; the production default now routes fresh failures to Builder.
func (s *legacyFailureAdmissionStore) ApplyClearDevComplexExecutionFailure(ctx context.Context, runID, taskID, dispatchID string, infrastructure bool, reason core.ReasonCode, at time.Time) error {
	if reason == "CHECKER_UNAVAILABLE" {
		infrastructure = true
	}
	return s.Store.ApplyClearDevComplexExecutionFailure(ctx, runID, taskID, dispatchID, infrastructure, reason, at)
}
