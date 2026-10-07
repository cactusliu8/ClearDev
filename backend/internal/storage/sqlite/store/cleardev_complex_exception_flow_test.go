package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestControlledExceptionStoreFlow(t *testing.T) {
	t.Run("materializes frozen budgets and generated command", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil || !ok || snapshot.Exception == nil {
			t.Fatalf("exception snapshot = ok:%t err:%v facts:%#v", ok, err, snapshot.Exception)
		}
		if len(snapshot.Exception.Budgets) == 0 || len(snapshot.Exception.GeneratedCommands) != 1 {
			t.Fatalf("budgets=%d commands=%d", len(snapshot.Exception.Budgets), len(snapshot.Exception.GeneratedCommands))
		}
		if snapshot.Exception.GeneratedCommands[0].CommandID != "npm-package-lock" {
			t.Fatalf("generated command = %#v", snapshot.Exception.GeneratedCommands[0])
		}
		foundSpecialist := false
		for _, budget := range snapshot.Exception.Budgets {
			if budget.RoleKind == core.ComplexExceptionBudgetSpecialist && budget.MaxTurns == 1 {
				foundSpecialist = true
			}
		}
		if !foundSpecialist {
			t.Fatal("specialist budget missing")
		}
	})

	t.Run("budget occupancy is atomic and same step does not recount", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		snapshot, _, _ := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		budgetID := ""
		for _, budget := range snapshot.Exception.Budgets {
			if budget.RoleKind == core.ComplexExceptionBudgetBuilder && budget.ComplexExecutionTaskID == state.tasks[0].ID {
				budgetID = budget.ID
			}
		}
		at := state.now.Add(3 * time.Second)
		if occupied, err := store.OccupyClearDevComplexExceptionBudget(ctx, budgetID, "step-a", "step-a", at); err != nil || !occupied {
			t.Fatalf("first occupy: occupied=%t err=%v", occupied, err)
		}
		if occupied, err := store.OccupyClearDevComplexExceptionBudget(ctx, budgetID, "step-a", "step-a", at); err != nil || !occupied {
			t.Fatalf("same-step occupy: occupied=%t err=%v", occupied, err)
		}
		if occupied, err := store.OccupyClearDevComplexExceptionBudget(ctx, budgetID, "step-b", "step-b", at); err != nil || !occupied {
			t.Fatalf("second occupy: occupied=%t err=%v", occupied, err)
		}
		if occupied, err := store.OccupyClearDevComplexExceptionBudget(ctx, budgetID, "step-c", "step-c", at); err != nil || !occupied {
			t.Fatalf("third occupy: occupied=%t err=%v", occupied, err)
		}
		if occupied, err := store.OccupyClearDevComplexExceptionBudget(ctx, budgetID, "step-d", "step-d", at); err == nil || occupied {
			t.Fatal("exhausted builder budget was occupied again")
		}
		snapshot, _, _ = store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		for _, budget := range snapshot.Exception.Budgets {
			if budget.ID == budgetID && budget.UsedTurns != 3 {
				t.Fatalf("used turns = %d, want 3", budget.UsedTurns)
			}
		}
	})

	t.Run("a round spends one turn no matter how many messages it sends", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		snapshot, _, _ := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		budgetID, maxTurns := "", 0
		for _, budget := range snapshot.Exception.Budgets {
			if budget.RoleKind == core.ComplexExceptionBudgetReviewer && budget.ComplexExecutionTaskID == state.tasks[0].ID {
				budgetID, maxTurns = budget.ID, budget.MaxTurns
			}
		}
		if budgetID == "" {
			t.Fatal("reviewer budget missing")
		}
		at := state.now.Add(3 * time.Second)
		// Round one legitimately sends the verdict first and the check-results
		// follow-up second; only the round spends a turn.
		if occupied, err := store.OccupyClearDevComplexExceptionBudget(ctx, budgetID, "review-round-1", "review-round-1-verdict", at); err != nil || !occupied {
			t.Fatalf("round one verdict: occupied=%t err=%v", occupied, err)
		}
		if occupied, err := store.OccupyClearDevComplexExceptionBudget(ctx, budgetID, "review-round-1", "review-round-1-check-results", at); err != nil || !occupied {
			t.Fatalf("round one follow-up must not spend another turn: occupied=%t err=%v", occupied, err)
		}
		snapshot, _, _ = store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		for _, budget := range snapshot.Exception.Budgets {
			if budget.ID == budgetID && budget.UsedTurns != 1 {
				t.Fatalf("used turns after one round = %d, want 1", budget.UsedTurns)
			}
		}
		// Every further round spends exactly one more turn, and the budget still
		// stops the one after the last.
		for round := 2; round <= maxTurns; round++ {
			roundKey := fmt.Sprintf("review-round-%d", round)
			if occupied, err := store.OccupyClearDevComplexExceptionBudget(ctx, budgetID, roundKey, roundKey+"-verdict", at); err != nil || !occupied {
				t.Fatalf("round %d: occupied=%t err=%v", round, occupied, err)
			}
		}
		if occupied, err := store.OccupyClearDevComplexExceptionBudget(ctx, budgetID, "review-round-extra", "review-round-extra-verdict", at); err == nil || occupied {
			t.Fatal("a round beyond the budget was occupied")
		}
		snapshot, _, _ = store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		for _, budget := range snapshot.Exception.Budgets {
			if budget.ID == budgetID && budget.UsedTurns != maxTurns {
				t.Fatalf("used turns = %d, want %d", budget.UsedTurns, maxTurns)
			}
		}
	})

	t.Run("concurrent occupancy does not overspend the budget", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		snapshot, _, _ := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		budgetID := ""
		remaining := 0
		for _, budget := range snapshot.Exception.Budgets {
			if budget.RoleKind == core.ComplexExceptionBudgetBuilder && budget.ComplexExecutionTaskID == state.tasks[0].ID {
				budgetID = budget.ID
				remaining = budget.MaxTurns - budget.UsedTurns
			}
		}
		if remaining < 2 {
			t.Fatalf("builder remaining turns = %d", remaining)
		}
		at := state.now.Add(3 * time.Second)
		var wg sync.WaitGroup
		type occupyResult struct {
			occupied bool
			err      error
		}
		results := make([]occupyResult, remaining+2)
		for i := range results {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				occupied, err := store.OccupyClearDevComplexExceptionBudget(ctx, budgetID, fmt.Sprintf("concurrent-step-%d", index), fmt.Sprintf("concurrent-step-%d", index), at)
				results[index] = occupyResult{occupied: occupied, err: err}
			}(i)
		}
		wg.Wait()
		ok := 0
		for _, item := range results {
			if item.err == nil && item.occupied {
				ok++
			}
		}
		if ok != remaining {
			t.Fatalf("successful occupies = %d, want %d results=%#v", ok, remaining, results)
		}
		snapshot, _, _ = store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		for _, budget := range snapshot.Exception.Budgets {
			if budget.ID == budgetID && budget.UsedTurns != budget.MaxTurns {
				t.Fatalf("used turns = %d, want %d", budget.UsedTurns, budget.MaxTurns)
			}
		}
	})

	t.Run("scope approval appends a new permission version", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		task := state.tasks[0]
		dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "s09-scope-dispatch")
		if err != nil {
			t.Fatal(err)
		}
		request, step, permission, decision := exceptionScopeApproval(state, task, dispatch, "s09-scope")
		if err := store.RecordClearDevComplexExceptionScopeRequest(ctx, request); err != nil {
			t.Fatalf("record scope request: %v", err)
		}
		if _, created, err := store.CreateClearDevComplexExceptionAgentStep(ctx, state.run.ID, "s09-flow-steward", "", step); err != nil || !created {
			t.Fatalf("create scope decision step: created=%t err=%v", created, err)
		}
		if err := store.AcceptClearDevComplexExceptionScope(ctx, decision, permission, state.now.Add(6*time.Second)); err != nil {
			t.Fatalf("accept scope: %v", err)
		}
		snapshot, _, _ := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		maxVersion := int64(0)
		for _, item := range snapshot.Exception.PermissionVersions {
			if item.DevelopmentTaskID == task.DevelopmentTaskID && item.Version > maxVersion {
				maxVersion = item.Version
			}
		}
		if maxVersion < 2 {
			t.Fatalf("permission version after approval = %d", maxVersion)
		}
		if len(snapshot.Exception.ScopeDecisions) != 1 || !snapshot.Exception.ScopeDecisions[0].ControlAccepted {
			t.Fatalf("scope decisions = %#v", snapshot.Exception.ScopeDecisions)
		}
	})

	t.Run("old round and settled dispatch cannot record or accept a scope request", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		task := state.tasks[0]
		dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "s09-old-round-dispatch")
		if err != nil {
			t.Fatal(err)
		}
		staleRound := exceptionScopeRequest(state, task, dispatch, "s09-old-round-request")
		staleRound.Round = 1
		if err := store.RecordClearDevComplexExceptionScopeRequest(ctx, staleRound); !isScopeStale(err) {
			t.Fatalf("old-round record err=%v, want SCOPE_STALE_BINDING", err)
		}
		request, step, permission, decision := exceptionScopeApproval(state, task, dispatch, "s09-settled-round")
		if err := store.RecordClearDevComplexExceptionScopeRequest(ctx, request); err != nil {
			t.Fatalf("record current-round request: %v", err)
		}
		if _, created, err := store.CreateClearDevComplexExceptionAgentStep(ctx, state.run.ID, "s09-flow-steward", "", step); err != nil || !created {
			t.Fatalf("create scope decision step: created=%t err=%v", created, err)
		}
		if err := store.ApplyClearDevComplexExecutionFailure(ctx, state.run.ID, task.ID, dispatch.ID, false, core.ReasonCode("BUILDER_RESULT_INVALID"), state.now.Add(5*time.Second)); err != nil {
			t.Fatalf("settle dispatch: %v", err)
		}
		if err := store.AcceptClearDevComplexExceptionScope(ctx, decision, permission, state.now.Add(6*time.Second)); !isScopeStale(err) {
			t.Fatalf("accept after round settled err=%v, want SCOPE_STALE_BINDING", err)
		}
	})

	t.Run("stop gate and task-set change reject scope record and accept", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		task := state.tasks[0]
		dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "s09-stop-dispatch")
		if err != nil {
			t.Fatal(err)
		}
		injectActiveDirectionStop(t, state.dataDir, state.prep.RequirementID, state.run.RequirementVersionID)
		stopped := exceptionScopeRequest(state, task, dispatch, "s09-stopped-request")
		if err := store.RecordClearDevComplexExceptionScopeRequest(ctx, stopped); !isDirectionStopped(err) {
			t.Fatalf("record with stop gate err=%v, want direction stop", err)
		}

		store, state = seedControlledExceptionFlow(t)
		task = state.tasks[0]
		dispatch, _, err = dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "s09-taskset-dispatch")
		if err != nil {
			t.Fatal(err)
		}
		request, step, permission, decision := exceptionScopeApproval(state, task, dispatch, "s09-taskset")
		if err := store.RecordClearDevComplexExceptionScopeRequest(ctx, request); err != nil {
			t.Fatalf("record before task-set change: %v", err)
		}
		if _, created, err := store.CreateClearDevComplexExceptionAgentStep(ctx, state.run.ID, "s09-flow-steward", "", step); err != nil || !created {
			t.Fatalf("create scope decision step: created=%t err=%v", created, err)
		}
		bumpTaskSetVersion(t, state.dataDir, state.run.RequirementVersionID)
		if err := store.AcceptClearDevComplexExceptionScope(ctx, decision, permission, state.now.Add(6*time.Second)); !isScopeStale(err) {
			t.Fatalf("accept after task-set change err=%v, want SCOPE_STALE_BINDING", err)
		}
	})

	t.Run("path mismatch and extra permission writes are rejected", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		task := state.tasks[0]
		dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "s09-path-dispatch")
		if err != nil {
			t.Fatal(err)
		}
		unlisted := exceptionScopeRequest(state, task, dispatch, "s09-unlisted-request")
		unlisted.RequestedPaths = []string{"README.md"}
		if err := store.RecordClearDevComplexExceptionScopeRequest(ctx, unlisted); err == nil {
			t.Fatal("unlisted path was recorded")
		}

		request, step, permission, decision := exceptionScopeApproval(state, task, dispatch, "s09-path")
		if err := store.RecordClearDevComplexExceptionScopeRequest(ctx, request); err != nil {
			t.Fatalf("record listed path: %v", err)
		}
		if _, created, err := store.CreateClearDevComplexExceptionAgentStep(ctx, state.run.ID, "s09-flow-steward", "", step); err != nil || !created {
			t.Fatalf("create scope decision step: created=%t err=%v", created, err)
		}
		mismatch := decision
		mismatch.Paths = []string{"src/email.js"}
		if err := store.AcceptClearDevComplexExceptionScope(ctx, mismatch, permission, state.now.Add(6*time.Second)); !isScopeStale(err) {
			t.Fatalf("accept mismatched paths err=%v, want SCOPE_STALE_BINDING", err)
		}
		extra := permission
		extra.Rules.WritePaths = append(append([]string{}, extra.Rules.WritePaths...), "README.md")
		if err := store.AcceptClearDevComplexExceptionScope(ctx, decision, extra, state.now.Add(6*time.Second)); !isScopeStale(err) {
			t.Fatalf("accept extra write path err=%v, want SCOPE_STALE_BINDING", err)
		}
	})

	t.Run("one on-demand role at a time and retry ordinal after infrastructure failure", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		task := state.tasks[0]
		first := core.ComplexOnDemandBinding{
			ID: "s09-specialist", ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID,
			Mode: core.ComplexOnDemandModeSpecialist, TriggerReason: core.ReasonSpecialistRequired,
			SessionCreationIdempotencyKey: "s09-specialist-key", Status: core.RoleBindingStatusRequested,
			BindingFingerprint: strings.Repeat("b", 64), RequestedAt: state.now.Add(3 * time.Second),
		}
		if _, created, err := store.CreateClearDevComplexExceptionOnDemandBinding(ctx, first); err != nil || !created {
			t.Fatalf("create specialist: created=%t err=%v", created, err)
		}
		second := first
		second.ID = "s09-recovery"
		second.Mode = core.ComplexOnDemandModeRecovery
		second.SessionCreationIdempotencyKey = "s09-recovery-key"
		if _, created, err := store.CreateClearDevComplexExceptionOnDemandBinding(ctx, second); err == nil || created {
			t.Fatal("second concurrent on-demand role was created")
		}

		dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "s09-retry-dispatch")
		if err != nil {
			t.Fatal(err)
		}
		held := 0
		snapshot, _, _ := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		for _, lease := range snapshot.Exception.PathLeases {
			if lease.Status == "HELD" {
				held++
			}
		}
		if held == 0 {
			t.Fatal("shared or generated path was not leased")
		}
		candidateID := "s09-retry-candidate"
		candidateSHA := strings.Repeat("c", 40)
		if _, err := store.AppendClearDevComplexExecutionCandidate(ctx, core.AppendComplexExecutionCandidateCommand{
			ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, Round: 0, BaseCommitSHA: state.initialBase,
			Candidate: core.CandidateObservation{ID: candidateID, DevelopmentTaskID: task.DevelopmentTaskID, AOSessionID: state.builder.AOSessionID, CommitSHA: candidateSHA, ObservedAt: state.now.Add(10 * time.Second)},
		}); err != nil {
			t.Fatal(err)
		}
		specID := ""
		for _, spec := range state.checkSpecs {
			if spec.Kind == core.CandidateCheckIntegration {
				specID = spec.ID
			}
		}
		failed := core.ComplexExecutionCheckRun{
			ID: "s09-infra-fail", ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID,
			CandidateCommitID: candidateID, CandidateCommitSHA: candidateSHA, CheckSpecFactID: specID,
			Kind: core.CandidateCheckIntegration, Status: core.ComplexExecutionCheckRunPending, CreatedAt: state.now.Add(11 * time.Second),
		}
		if _, created, err := store.CreateClearDevComplexExecutionCheckRun(ctx, failed); err != nil || !created {
			t.Fatalf("create failed check: created=%t err=%v", created, err)
		}
		if changed, err := store.StartClearDevComplexExecutionCheckRun(ctx, failed.ID, state.now.Add(12*time.Second)); err != nil || !changed {
			t.Fatalf("start failed check: changed=%t err=%v", changed, err)
		}
		if changed, err := store.SettleClearDevComplexExecutionCheckRun(ctx, core.SettleComplexExecutionCheckCommand{
			CheckRunID: failed.ID, ReasonCode: core.ReasonCode("CHECKER_UNAVAILABLE"), OutputSummary: "INJECTED_INFRASTRUCTURE_FAILURE",
			At: state.now.Add(13 * time.Second),
		}); err != nil || !changed {
			t.Fatalf("settle failed check: changed=%t err=%v", changed, err)
		}
		retry := failed
		retry.ID = "s09-infra-retry"
		retry.RetryOrdinal = 1
		retry.Status = core.ComplexExecutionCheckRunPending
		retry.CreatedAt = state.now.Add(14 * time.Second)
		if _, created, err := store.CreateClearDevComplexExecutionCheckRun(ctx, retry); err != nil || !created {
			t.Fatalf("create retry check: created=%t err=%v", created, err)
		}
		snapshot, _, _ = store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		foundRetry := false
		for _, run := range snapshot.CheckRuns {
			if run.ID == retry.ID && run.RetryOrdinal == 1 {
				foundRetry = true
			}
		}
		if !foundRetry {
			t.Fatal("retry_ordinal=1 check run was not stored")
		}
	})

	t.Run("corrupt exception JSON is not treated as an empty array", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		execSQL(t, state.dataDir,
			`DROP TRIGGER IF EXISTS cleardev_complex_exception_budget_update_valid`,
			`UPDATE cleardev_complex_exception_budgets SET allowed_agent_types_json = '[1]'`,
		)
		if _, _, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID); err == nil {
			t.Fatal("corrupt budget JSON was accepted")
		}

		store, state = seedControlledExceptionFlow(t)
		execSQL(t, state.dataDir, `UPDATE cleardev_complex_exception_generated_commands SET argv_json = '[1]'`)
		if _, _, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID); err == nil {
			t.Fatal("corrupt generated-command JSON was accepted")
		}

		store, state = seedControlledExceptionFlow(t)
		execSQL(t, state.dataDir,
			`DROP TRIGGER IF EXISTS cleardev_path_permissions_append_only_update`,
			`UPDATE cleardev_path_permission_versions SET write_paths = '[1]'`,
		)
		if _, _, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID); err == nil {
			t.Fatal("corrupt permission JSON was accepted")
		}
	})

	t.Run("scope accept rolls back when the pending update matches zero rows", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		task := state.tasks[0]
		dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "s11a-accept-race-dispatch")
		if err != nil {
			t.Fatal(err)
		}
		request, step, permission, decision := exceptionScopeApproval(state, task, dispatch, "s11a-accept-race")
		if err := store.RecordClearDevComplexExceptionScopeRequest(ctx, request); err != nil {
			t.Fatalf("record scope request: %v", err)
		}
		if _, created, err := store.CreateClearDevComplexExceptionAgentStep(ctx, state.run.ID, "s09-flow-steward", "", step); err != nil || !created {
			t.Fatalf("create scope decision step: created=%t err=%v", created, err)
		}
		other, err := sqlite.Open(state.dataDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = other.Close() })
		otherPermission := permission
		otherPermission.ID = permission.ID + "-other"
		otherDecision := decision
		otherDecision.ID = decision.ID + "-other"
		errs := make([]error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs[0] = store.AcceptClearDevComplexExceptionScope(ctx, decision, permission, state.now.Add(6*time.Second))
		}()
		go func() {
			defer wg.Done()
			errs[1] = other.AcceptClearDevComplexExceptionScope(ctx, otherDecision, otherPermission, state.now.Add(6*time.Second))
		}()
		wg.Wait()
		ok, fail := 0, 0
		for _, item := range errs {
			if item == nil {
				ok++
			} else {
				fail++
			}
		}
		if ok != 1 || fail != 1 {
			t.Fatalf("accept race ok=%d fail=%d errs=%v", ok, fail, errs)
		}
		snapshot, _, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Exception.ScopeDecisions) != 1 {
			t.Fatalf("scope decisions after zero-row rollback = %#v", snapshot.Exception.ScopeDecisions)
		}
		approved := 0
		for _, item := range snapshot.Exception.ScopeRequests {
			if item.Status == "APPROVED" {
				approved++
			}
		}
		if approved != 1 {
			t.Fatalf("approved requests = %d", approved)
		}
	})

	t.Run("scope reject rolls back when the pending update matches zero rows", func(t *testing.T) {
		store, state := seedControlledExceptionFlow(t)
		ctx := context.Background()
		task := state.tasks[0]
		dispatch, _, err := dispatchComplexExecutionTask(ctx, store, state, task, 0, state.initialBase, "s11a-reject-race-dispatch")
		if err != nil {
			t.Fatal(err)
		}
		request, step, _, _ := exceptionScopeApproval(state, task, dispatch, "s11a-reject-race")
		if err := store.RecordClearDevComplexExceptionScopeRequest(ctx, request); err != nil {
			t.Fatalf("record scope request: %v", err)
		}
		if _, created, err := store.CreateClearDevComplexExceptionAgentStep(ctx, state.run.ID, "s09-flow-steward", "", step); err != nil || !created {
			t.Fatalf("create scope decision step: created=%t err=%v", created, err)
		}
		decision := core.ComplexScopeExpansionDecision{
			ID: "s11a-reject-a", RequestID: request.ID, ExecutionRunID: state.run.ID,
			RequirementVersionID: state.run.RequirementVersionID, RequirementSHA256: state.run.RequirementSHA256,
			PlanID: state.run.PlanID, PlanSHA256: state.run.PlanSHA256, Paths: []string{"package.json"},
			Decision: core.ComplexScopeDecisionNeedsHuman, ReasonCode: core.ReasonHumanDecisionRequired, Summary: "需要人决定。",
			StewardRoleBindingID: "s09-flow-steward", AgentStepID: step.ID, CreatedAt: state.now.Add(6 * time.Second),
		}
		other, err := sqlite.Open(state.dataDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = other.Close() })
		otherDecision := decision
		otherDecision.ID = "s11a-reject-b"
		errs := make([]error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs[0] = store.RejectClearDevComplexExceptionScope(ctx, decision, state.now.Add(6*time.Second))
		}()
		go func() {
			defer wg.Done()
			errs[1] = other.RejectClearDevComplexExceptionScope(ctx, otherDecision, state.now.Add(6*time.Second))
		}()
		wg.Wait()
		ok, fail := 0, 0
		for _, item := range errs {
			if item == nil {
				ok++
			} else {
				fail++
			}
		}
		if ok != 1 || fail != 1 {
			t.Fatalf("reject race ok=%d fail=%d errs=%v", ok, fail, errs)
		}
		snapshot, _, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Exception.ScopeDecisions) != 1 {
			t.Fatalf("scope decisions after reject zero-row rollback = %#v", snapshot.Exception.ScopeDecisions)
		}
	})
}

func seedControlledExceptionFlow(t *testing.T) (*sqlite.Store, complexExecutionFlowState) {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initComplexExecutionTestRepo(t)
	const projectID = "ao-s09-store-flow"
	prep := cleardevtest.SeedComplexExceptionV2(t, store, projectID, repo, dataDir)
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

	requirement, ok, err := store.GetClearDevRequirement(ctx, prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("read exception fixture requirement: ok=%t err=%v", ok, err)
	}
	var version core.RequirementVersion
	for _, item := range requirement.RequirementVersions {
		if item.ID == prep.V2ID {
			version = item
		}
	}
	planning, ok, err := store.GetClearDevComplexPlanning(ctx, prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("read exception fixture planning: ok=%t err=%v", ok, err)
	}
	var plan core.ComplexEngineeringPlan
	var steward core.ComplexRoleBinding
	for _, item := range planning.Plans {
		if item.ID == prep.V2PlanID {
			plan = item
		}
	}
	for _, item := range planning.RoleBindings {
		if item.Role == core.StandardRoleSteward && item.Status == core.RoleBindingStatusBound {
			steward = item
		}
	}
	if plan.ID == "" || steward.ID == "" {
		t.Fatalf("fixture missing approved plan or bound Steward")
	}
	var parsed core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(plan.PlanJSON), &parsed); err != nil {
		t.Fatalf("decode approved plan: %v", err)
	}

	initialBase := strings.Repeat("a", 40)
	raw, digest, err := core.BuildComplexExecutionRunPackage(core.WorkModeStandard, "s09-flow-run", version.ID, version.SHA256, plan.ID, plan.PlanSHA256)
	if err != nil {
		t.Fatalf("build run package: %v", err)
	}
	run := core.ComplexExecutionRun{
		ID: "s09-flow-run", DevelopmentRequirementID: prep.RequirementID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256,
		PlanID: plan.ID, PlanReviewID: prep.V2ReviewID, PlanSHA256: plan.PlanSHA256, Mode: core.WorkModeStandard, ModeReason: "ONE_BUILDER_REQUIRED",
		FixedBuilderCount: 1, ExpectedTaskSetVersion: 0, TaskSetVersion: core.ComplexStandardTaskSetVersion,
		ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest, StewardRoleBindingID: steward.ID, CreatedAt: now,
	}
	stewardBinding := core.ComplexExecutionRoleBinding{
		ID: "s09-flow-steward", ExecutionRunID: run.ID, SourceComplexRoleBindingID: steward.ID, Role: core.StandardRoleSteward,
		SessionCreationIdempotencyKey: "s09-flow-steward-continuation", AOSessionID: prep.StewardSessionID,
		Status: core.RoleBindingStatusBound, RequestedAt: now, BoundAt: &now,
	}
	step := core.AgentStep{
		ID: "s09-flow-steward-step", RoleBindingID: stewardBinding.ID, Kind: core.AgentStepDispatchRequest, RequestID: run.ID,
		ClientMessageID: "s09-flow-steward-client", PromptSHA256: strings.Repeat("1", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: now,
	}
	if _, created, err := store.StartClearDevComplexExecution(ctx, core.StartComplexExecutionCommand{Run: run, StewardRoleBinding: stewardBinding, AgentStep: step}); err != nil || !created {
		t.Fatalf("start exception execution: created=%t err=%v", created, err)
	}
	builderSession := createComplexExecutionSession(t, store, projectID, "s09-flow-builder-session", "s09-flow-builder-key", "/worktrees/s09-flow-builder", initialBase, now)
	builder := core.ComplexExecutionRoleBinding{
		ID: "s09-flow-builder", ExecutionRunID: run.ID, Role: core.StandardRoleBuilder, BuilderSlot: 1,
		SessionCreationIdempotencyKey: "s09-flow-builder-key", Status: core.RoleBindingStatusRequested, RequestedAt: now,
	}
	tasks, specs := complexExecutionMaterialization(t, run, version, parsed, now)
	if err := store.MaterializeClearDevComplexExecution(ctx, core.MaterializeComplexExecutionCommand{
		ExecutionRunID: run.ID, Tasks: tasks, CheckSpecs: specs, BuilderBindings: []core.ComplexExecutionRoleBinding{builder}, At: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("materialize exception tasks: %v", err)
	}
	if changed, err := store.BindClearDevComplexExecutionRoleBinding(ctx, builder.ID, string(builderSession.ID), "/worktrees/s09-flow-builder", initialBase, now.Add(2*time.Second)); err != nil || !changed {
		t.Fatalf("bind exception Builder: changed=%t err=%v", changed, err)
	}
	builder.AOSessionID, builder.WorkspacePath, builder.BaseCommitSHA = string(builderSession.ID), "/worktrees/s09-flow-builder", initialBase
	builder.Status = core.RoleBindingStatusBound
	return store, complexExecutionFlowState{prep: prep, projectID: projectID, dataDir: dataDir, run: run, tasks: tasks, checkSpecs: specs, builder: builder, initialBase: initialBase, now: now}
}

func exceptionScopeRequest(state complexExecutionFlowState, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, id string) core.ComplexScopeExpansionRequest {
	return core.ComplexScopeExpansionRequest{
		ID: id, ExecutionRunID: state.run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID,
		Round: dispatch.Round, RequirementVersionID: state.run.RequirementVersionID, RequirementSHA256: state.run.RequirementSHA256,
		PlanID: state.run.PlanID, PlanSHA256: state.run.PlanSHA256, RequestedPaths: []string{"package.json"},
		AgentStepID: dispatch.AgentStepID, Status: "PENDING", CreatedAt: state.now.Add(4 * time.Second),
	}
}

func exceptionScopeApproval(state complexExecutionFlowState, task core.ComplexExecutionTask, dispatch core.ComplexExecutionDispatch, prefix string) (core.ComplexScopeExpansionRequest, core.AgentStep, core.PermissionVersion, core.ComplexScopeExpansionDecision) {
	request := exceptionScopeRequest(state, task, dispatch, prefix+"-request")
	step := core.AgentStep{
		ID: prefix + "-decision-step", RoleBindingID: "s09-flow-steward", Kind: core.ComplexExceptionAgentStepScopeDecision,
		RequestID: request.ID, ClientMessageID: prefix + "-decision-client", PromptSHA256: strings.Repeat("a", 64),
		SendStatus: core.AgentStepSendStatusPending, RequestedAt: state.now.Add(5 * time.Second),
	}
	permission := core.PermissionVersion{
		ID: prefix + "-permission-v2", DevelopmentTaskID: task.DevelopmentTaskID, Rules: core.PathRules{
			WritePaths: []string{"src/email.js", "src/deduplicate.js", "package.json"}, GeneratedPaths: []string{"package-lock.json"},
			ForbiddenPaths: []string{".git/**"},
		}, CreatedAt: state.now.Add(6 * time.Second),
	}
	decision := core.ComplexScopeExpansionDecision{
		ID: prefix + "-decision", RequestID: request.ID, ExecutionRunID: state.run.ID,
		RequirementVersionID: state.run.RequirementVersionID, RequirementSHA256: state.run.RequirementSHA256,
		PlanID: state.run.PlanID, PlanSHA256: state.run.PlanSHA256, Paths: []string{"package.json"},
		Decision: core.ComplexScopeDecisionApprove, ReasonCode: "SCOPE_APPROVED", Summary: "批准共享路径。",
		StewardRoleBindingID: "s09-flow-steward", AgentStepID: step.ID, PermissionVersionID: permission.ID,
		ControlAccepted: true, CreatedAt: state.now.Add(6 * time.Second),
	}
	return request, step, permission, decision
}

func isScopeStale(err error) bool {
	var rule *core.RuleError
	return errors.As(err, &rule) && rule.Code == core.ReasonScopeStaleBinding
}

func isDirectionStopped(err error) bool {
	var rule *core.RuleError
	return errors.As(err, &rule) && rule.Code == core.ReasonDirectionChangeStopped
}

func injectActiveDirectionStop(t *testing.T, dataDir, projectID, versionID string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(OFF)")
	if err != nil {
		t.Fatalf("open stop-gate fixture connection: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO cleardev_direction_stop_gates (
		id, direction_request_id, development_project_id, requirement_version_id,
		task_set_version, snapshot_sha256, status, closed_at, close_reason, created_at
	) VALUES (?, ?, ?, ?, 1, ?, 'ACTIVE', NULL, '', ?)`,
		"s09-test-stop-gate", "s09-test-direction-request", projectID, versionID,
		strings.Repeat("a", 64), time.Date(2026, 8, 26, 12, 0, 7, 0, time.UTC),
	); err != nil {
		t.Fatalf("insert active direction stop gate: %v", err)
	}
}

func bumpTaskSetVersion(t *testing.T, dataDir, versionID string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open task-set fixture connection: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE cleardev_contract_versions SET task_set_version = task_set_version + 1 WHERE id = ?`, versionID); err != nil {
		t.Fatalf("bump task-set version: %v", err)
	}
}

func execSQL(t *testing.T, dataDir string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(OFF)")
	if err != nil {
		t.Fatalf("open fixture connection: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("exec %s: %v", statement, err)
		}
	}
}
