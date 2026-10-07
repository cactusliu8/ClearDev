package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// Historical admission uses a valid unmarked package before its first insert.
// The real store, transactions and historical messages remain unchanged.
type historicalProjectAdmissionStore struct{ *store.Store }

func (s historicalProjectAdmissionStore) StartClearDevComplexExecution(ctx context.Context, command core.StartComplexExecutionCommand) (core.ComplexExecutionRun, bool, error) {
	var packet core.ComplexExecutionRunPackage
	if err := json.Unmarshal([]byte(command.Run.ExecutionPackageJSON), &packet); err != nil {
		return core.ComplexExecutionRun{}, false, err
	}
	packet.BuilderFailurePolicy = ""
	raw, err := core.CanonicalJSONBytes(packet)
	if err != nil {
		return core.ComplexExecutionRun{}, false, err
	}
	command.Run.ExecutionPackageJSON, command.Run.ExecutionPackageSHA256 = string(raw), coreDigest(raw)
	return s.Store.StartClearDevComplexExecution(ctx, command)
}

func settledHistoricalProjectFailure(t *testing.T) (*projectPlanningFixture, *projectExecutionFlowHarness, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	f.s.complexExecution = historicalProjectAdmissionStore{f.store}
	h := attachProjectFlow(f, preparer)
	h.finalVerdict = "BLOCKED"
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	driveProjectFlow(t, f, child.Requirement.ID, true)
	for range 30 {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		if x.FinalReview.Status == "SETTLED" {
			return f, h, x
		}
		if _, _, err := f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("historical final review did not settle")
	return nil, nil, core.ComplexExecutionSnapshot{}
}

func TestManualBlockedFinalReturnPreservesHistoryAndRechecksNewCandidate(t *testing.T) {
	f, h, before := settledHistoricalProjectFailure(t)
	ctx := context.Background()
	if core.BuilderFirstFailureRun(before.Run) {
		t.Fatal("historical fixture acquired a new package marker")
	}
	sends := len(h.relays)
	if changed, _, err := f.s.advanceComplexStandardExecution(ctx, before.Run.DevelopmentRequirementID); err != nil || !changed || len(h.relays) != sends {
		t.Fatal("default diagnosis did not register one repair while preserving old sends")
	}
	if err := f.s.RequestDevelopmentTaskRework(ctx, before.Run.DevelopmentRequirementID, before.Tasks[0].DevelopmentTaskID); err != nil {
		t.Fatalf("explicit manual return rejected: %v", err)
	}
	if err := f.s.RequestDevelopmentTaskRework(ctx, before.Run.DevelopmentRequirementID, before.Tasks[0].DevelopmentTaskID); err != nil {
		t.Fatalf("repeat of registered return: %v", err)
	}
	h.finalVerdict = "PASS"
	after := driveProjectFlow(t, f, before.Run.DevelopmentRequirementID, false)
	if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 || after.Tasks[0].ReworkCount != 1 || len(after.Verifications) != 2 || after.FinalReview.CandidateCommitSHA == before.FinalReview.CandidateCommitSHA {
		t.Fatal("manual return skipped the fresh candidate and ordinary review pipeline")
	}
	if after.Run.ExecutionPackageJSON != before.Run.ExecutionPackageJSON || after.Run.ExecutionPackageSHA256 != before.Run.ExecutionPackageSHA256 {
		t.Fatal("historical execution package changed")
	}
	for _, old := range before.AgentSteps {
		current, found := complexExecutionStepByID(after, old.ID)
		if !found || current.PromptSHA256 != old.PromptSHA256 || current.ClientMessageID != old.ClientMessageID {
			t.Fatal("an original message identity or prompt changed")
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var oldJSON, oldPrompt, oldVerdict string
	err = db.QueryRowContext(ctx, "SELECT review_packet_json,prompt_sha256,verdict FROM cleardev_requirement_final_reviews WHERE id=?", before.FinalReview.ID).Scan(&oldJSON, &oldPrompt, &oldVerdict)
	if err != nil || oldJSON != before.FinalReview.ReviewPacketJSON || oldPrompt != before.FinalReview.PromptSHA256 || oldVerdict != "BLOCKED" {
		t.Fatal("old review or prompt was rewritten")
	}
	matched := false
	for _, relay := range h.relays[sends:] {
		if strings.Contains(relay.prompt, "原最终验收失败") && strings.Contains(relay.prompt, before.FinalReview.ID) && strings.Contains(relay.prompt, before.FinalReview.CandidateCommitSHA) {
			matched = true
		}
	}
	if !matched {
		t.Fatal("Builder did not receive the original final-review gap")
	}
}

func TestManualBlockedFinalReturnPublicEntryRunsExecutionScheduler(t *testing.T) {
	assertManualBlockedReturnScheduled(t, false)
}

func TestManualBlockedFinalReturnRegisteredRequestResumesAfterRestart(t *testing.T) {
	assertManualBlockedReturnScheduled(t, true)
}

func assertManualBlockedReturnScheduled(t *testing.T, restart bool) {
	t.Helper()
	f, h, before := settledHistoricalProjectFailure(t)
	ctx := context.Background()
	// Fixture setup drove transitions manually while discarding background
	// callbacks. Model a stopped daemon runner before enabling the real queues.
	delete(f.s.complexRunning, before.Run.DevelopmentRequirementID)
	delete(f.s.complexExecutionRunning, before.Run.DevelopmentRequirementID)
	var queued []func()
	f.s.runBackground = func(run func()) { queued = append(queued, run) }
	h.finalVerdict = "PASS"
	sends := len(h.relays)
	for range 2 {
		if err := f.s.RequestDevelopmentTaskRework(ctx, before.Run.DevelopmentRequirementID, before.Tasks[0].DevelopmentTaskID); err != nil {
			t.Fatal(err)
		}
	}
	if len(queued) != 1 {
		t.Fatalf("expected one background runner, got %d", len(queued))
	}
	if restart {
		// The registered human request survives process restart. Discard the
		// old callback/runner flags and use the production resume entry.
		queued = nil
		delete(f.s.complexExecutionRunning, before.Run.DevelopmentRequirementID)
		delete(f.s.complexExecutionWake, before.Run.DevelopmentRequirementID)
		if err := f.s.ResumeComplexStandardExecutions(ctx); err != nil {
			t.Fatal(err)
		}
		if len(queued) != 1 {
			t.Fatalf("registered return did not resume exactly once: %d", len(queued))
		}
	}
	queued[0]() // Production scheduler/loop; no manual advance shortcut.
	after, _, err := f.store.GetClearDevComplexExecution(ctx, before.Run.DevelopmentRequirementID)
	if err != nil || after.Run.CompletedAt == nil || len(after.Dispatches) != 2 || after.Tasks[0].ReworkCount != 1 || len(after.Verifications) != 2 {
		t.Fatal("public return registered a round but did not run the execution scheduler", err)
	}
	feedbackSends := 0
	for _, relay := range h.relays[sends:] {
		if strings.Contains(relay.prompt, "原最终验收失败") && strings.Contains(relay.prompt, before.FinalReview.ID) {
			feedbackSends++
		}
	}
	if feedbackSends != 1 {
		t.Fatalf("background runner sent failure feedback %d times", feedbackSends)
	}
}

func TestManualBlockedFinalReturnRejectsChangedBindings(t *testing.T) {
	for _, boundary := range []string{"busy", "candidate", "stale-spec", "paused", "budget", "wrong-task"} {
		t.Run(boundary, func(t *testing.T) {
			f, h, before := settledHistoricalProjectFailure(t)
			taskID := before.Tasks[0].DevelopmentTaskID
			switch boundary {
			case "busy":
				record, _, err := f.store.GetSession(context.Background(), domain.SessionID(before.RoleBindings[0].AOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				record.Activity.State = domain.ActivityActive
				if err := f.store.UpdateSession(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			case "candidate":
				h.reviewerWorkspaces[before.FinalReview.WorkspacePath] = strings.Repeat("f", 40)
			case "stale-spec", "paused":
				stopBuilderFirstFixture(t, f, before, boundary)
			case "budget":
				db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				if _, err := db.Exec("UPDATE cleardev_work_items SET max_rework_count=0 WHERE id=?", taskID); err != nil {
					t.Fatal(err)
				}
			case "wrong-task":
				taskID = "not-this-task"
			}
			sends := len(h.relays)
			if err := f.s.RequestDevelopmentTaskRework(context.Background(), before.Run.DevelopmentRequirementID, taskID); err == nil {
				t.Fatalf("accepted %s", boundary)
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
			if err != nil || after.Tasks[0].ReworkCount != before.Tasks[0].ReworkCount || len(after.Dispatches) != len(before.Dispatches) || len(h.relays) != sends {
				t.Fatal("rejected return changed workflow")
			}
		})
	}
}

func TestManualBlockedFinalReturnPauseBeforeSendKeepsRegisteredReturn(t *testing.T) {
	f, h, before := settledHistoricalProjectFailure(t)
	if err := f.s.RequestDevelopmentTaskRework(context.Background(), before.Run.DevelopmentRequirementID, before.Tasks[0].DevelopmentTaskID); err != nil {
		t.Fatal(err)
	}
	stopBuilderFirstFixture(t, f, before, "paused")
	sends := len(h.relays)
	changed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), before.Run.DevelopmentRequirementID)
	if err != nil || changed || !stopped || len(h.relays) != sends {
		t.Fatal("paused manual repair sent a turn")
	}
}

func TestManualBlockedFinalReturnRejectsUnsettledReview(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	f.s.complexExecution = historicalProjectAdmissionStore{f.store}
	h := attachProjectFlow(f, preparer)
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	x := driveProjectFlow(t, f, child.Requirement.ID, true)
	sends := len(h.relays)
	if x.FinalReview.Status == "SETTLED" || f.s.RequestDevelopmentTaskRework(context.Background(), child.Requirement.ID, x.Tasks[0].DevelopmentTaskID) == nil {
		t.Fatal("unsettled final review accepted manual return")
	}
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	if err != nil || after.Tasks[0].ReworkCount != 0 || len(after.Dispatches) != 1 || len(h.relays) != sends {
		t.Fatal("unsettled review rejection changed execution")
	}
}

func TestManualBlockedFinalReturnShowsBuilderDiagnosis(t *testing.T) {
	f, h, before := settledHistoricalProjectFailure(t)
	h.builderDiagnosis = true
	if err := f.s.RequestDevelopmentTaskRework(context.Background(), before.Run.DevelopmentRequirementID, before.Tasks[0].DevelopmentTaskID); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		x, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.Dispatches) == 2 && x.Dispatches[1].Status == core.ComplexExecutionDispatchBlocked {
			phase, reason := core.DeriveComplexExecutionPhase(x)
			if phase != core.ComplexExecutionCoordinating || reason != core.ReasonPlannerRuntimePending || h.finalSends != 1 {
				t.Fatalf("new diagnosis hidden behind old review: %s %s", phase, reason)
			}
			return
		}
		if _, _, err := f.s.advanceComplexStandardExecution(context.Background(), before.Run.DevelopmentRequirementID); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("Builder diagnosis did not settle")
}

func TestManualBlockedFinalReturnUsesExistingExtraTurnAfterExhaustion(t *testing.T) {
	f, h, x := settledHistoricalProjectFailure(t)
	h.candidateSHAs = []string{forty("b"), forty("c"), forty("d"), forty("e"), forty("f")}
	ctx := context.Background()
	advanceToNewReview := func(oldReviewID string) core.ComplexExecutionSnapshot {
		t.Helper()
		for range 100 {
			current, _, err := f.store.GetClearDevComplexExecution(ctx, x.Run.DevelopmentRequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if current.FinalReview.ID != oldReviewID && current.FinalReview.Status == "SETTLED" {
				return current
			}
			if changed, stopped, err := f.s.advanceComplexStandardExecution(ctx, x.Run.DevelopmentRequirementID); err != nil || !changed || stopped {
				t.Fatalf("could not advance rework: %v %v %v", changed, stopped, err)
			}
		}
		t.Fatal("next review did not settle")
		return core.ComplexExecutionSnapshot{}
	}
	for range 3 {
		if err := f.s.RequestDevelopmentTaskRework(ctx, x.Run.DevelopmentRequirementID, x.Tasks[0].DevelopmentTaskID); err != nil {
			t.Fatal(err)
		}
		x = advanceToNewReview(x.FinalReview.ID)
	}
	if x.Tasks[0].ReworkCount != 3 || len(x.Dispatches) != 4 {
		t.Fatal("fixture did not exhaust the original rounds")
	}
	if err := f.s.RequestDevelopmentTaskRework(ctx, x.Run.DevelopmentRequirementID, x.Tasks[0].DevelopmentTaskID); err == nil {
		t.Fatal("exhausted task accepted a return")
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// The experiment only adds allowance to existing counters. Production
	// triggers stay enabled and used turns/history are never cleared.
	if _, err := db.ExecContext(ctx, "UPDATE cleardev_complex_exception_budgets SET authorized_extra_turns=1 WHERE execution_run_id=? AND role_kind IN ('BUILDER','REVIEWER')", x.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE cleardev_work_items SET max_rework_count=4 WHERE id=?", x.Tasks[0].DevelopmentTaskID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RequestDevelopmentTaskRework(ctx, x.Run.DevelopmentRequirementID, x.Tasks[0].DevelopmentTaskID); err != nil {
		t.Fatal(err)
	}
	h.finalVerdict = "PASS"
	after := driveProjectFlow(t, f, x.Run.DevelopmentRequirementID, false)
	if after.Run.CompletedAt == nil || len(after.Dispatches) != 5 || after.Dispatches[4].Round != 4 || after.Tasks[0].ReworkCount != 4 || len(after.Verifications) != 5 {
		t.Fatal("extra allowance did not run and review exactly one new round")
	}
}

// Historical recovery facts are a read seam; all new rounds, messages and
// review facts still use the production SQLite store and public scheduler.
type manualPriorRecoveryStore struct {
	*store.Store
	recovery        core.WorkflowRecovery
	requirementID   string
	legacyPending   bool
	frozenPromptSHA string
}

func (s *manualPriorRecoveryStore) GetClearDevComplexExecution(ctx context.Context, id string) (core.ComplexExecutionSnapshot, bool, error) {
	x, found, err := s.Store.GetClearDevComplexExecution(ctx, id)
	if err == nil && found && id == s.requirementID {
		x.WorkflowRecoveries = append(x.WorkflowRecoveries, s.recovery)
	}
	return x, found, err
}

func (s *manualPriorRecoveryStore) CreateClearDevComplexExecutionDispatch(ctx context.Context, command core.CreateComplexExecutionDispatchCommand) (core.ComplexExecutionDispatch, bool, error) {
	if s.legacyPending {
		// Reproduce a request durably created by the prior version, before its
		// CurrentRound advanced. The new code must keep those exact prompt bytes.
		x, _, err := s.GetClearDevComplexExecution(ctx, s.requirementID)
		if err != nil {
			return core.ComplexExecutionDispatch{}, false, err
		}
		if !core.BuilderFirstFailureRun(x.Run) && x.FinalReview != nil {
			// The former reader omitted saved detailed results on historical runs.
			final := *x.FinalReview
			final.FailureResultJSON = ""
			x.FinalReview = &final
		}
		var task core.ComplexExecutionTask
		for _, item := range x.Tasks {
			if item.ID == command.Dispatch.ComplexExecutionTaskID {
				task = item
			}
		}
		prompt := executionBuilderPromptForRun(x.Run, []byte(task.ExecutionPackageJSON), command.Dispatch.ID, task.DevelopmentTaskID, command.Dispatch.Round, complexExecutionReworkFeedbackBeforeUnified(x, task))
		command.AgentStep.PromptSHA256 = coreDigest([]byte(prompt))
		s.frozenPromptSHA = command.AgentStep.PromptSHA256
	}
	return s.Store.CreateClearDevComplexExecutionDispatch(ctx, command)
}

func TestManualBlockedFinalReturnPriorRecoveryKeepsPromptStable(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "new_round"
		if legacy {
			name = "already_registered_prompt"
		}
		t.Run(name, func(t *testing.T) {
			f, h, before := settledHistoricalProjectFailure(t)
			h.benchmarkSequentialCandidates = true
			// Seed the historical pause after a settled review; the default
			// auto policy is exercised separately by the unified flow tests.
			f.s.finalReviews = manualStoppedFinalFailureStore{f.store}
			ctx := context.Background()
			id := before.Run.DevelopmentRequirementID
			delete(f.s.complexRunning, id)
			delete(f.s.complexExecutionRunning, id)
			var queued []func()
			f.s.runBackground = func(run func()) { queued = append(queued, run) }
			// Settle one prior manual round so an older recovery supplement belongs
			// to that round, rather than to the new one requested below.
			if err := f.s.RequestDevelopmentTaskRework(ctx, id, before.Tasks[0].DevelopmentTaskID); err != nil {
				t.Fatal(err)
			}
			queued[0]()
			prior, _, err := f.store.GetClearDevComplexExecution(ctx, id)
			if err != nil || prior.FinalReview.Status != "SETTLED" || prior.Tasks[0].CurrentRound != 1 {
				t.Fatal("prior blocked round did not settle", err)
			}
			seam := &manualPriorRecoveryStore{Store: f.store, legacyPending: legacy, requirementID: id, recovery: core.WorkflowRecovery{ID: "prior-recovery", ExecutionRunID: prior.Run.ID, Action: core.RecoveryContinueBuilder, TaskID: prior.Tasks[0].ID, DispatchID: prior.Dispatches[0].ID, Supplement: "old recovery explanation"}}
			f.s.complexExecution = seam
			queued = nil
			sends := len(h.relays)
			h.finalVerdict = "PASS"
			if err := f.s.RequestDevelopmentTaskRework(ctx, id, prior.Tasks[0].DevelopmentTaskID); err != nil {
				t.Fatal(err)
			}
			if len(queued) != 1 {
				t.Fatalf("expected one scheduled runner, got %d", len(queued))
			}
			queued[0]()
			after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
			if err != nil || after.Run.CompletedAt == nil || len(after.Dispatches) != 3 || after.Tasks[0].ReworkCount != 2 || len(after.Verifications) != 3 {
				t.Fatal("prior recovery prevented actual delivery and fresh review", err)
			}
			builderSends := 0
			for _, relay := range h.relays[sends:] {
				if !strings.Contains(relay.prompt, "原最终验收失败") {
					continue
				}
				builderSends++
				if strings.Contains(relay.prompt, "old recovery explanation") != legacy {
					t.Fatal("new round leaked old context or legacy prompt was changed")
				}
				if legacy && coreDigest([]byte(relay.prompt)) != seam.frozenPromptSHA {
					t.Fatal("registered prompt identity changed")
				}
			}
			if builderSends != 1 {
				t.Fatalf("expected one actual Builder send, got %d", builderSends)
			}
		})
	}
}

// A historical test seam stops automatic registration at the old review while
// preserving the public explicit rework path and all production storage rules.
type manualStoppedFinalFailureStore struct{ *store.Store }

func (s manualStoppedFinalFailureStore) ReturnClearDevFinalFailureToBuilder(context.Context, string, time.Time) (bool, error) {
	return false, nil
}
