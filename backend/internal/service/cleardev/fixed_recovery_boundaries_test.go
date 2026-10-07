package cleardev_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type fixedResultSaveFailure struct{ *sqlite.Store }

func (f fixedResultSaveFailure) RecordClearDevFixedRecoveryResult(context.Context, core.FixedRecoveryResult) error {
	return fmt.Errorf("injected after-effect result save failure")
}

func TestClearDevFixedRecoveryAfterEffectSaveFailureReopen(t *testing.T) {
	for _, action := range []string{core.ComplexRecoveryActionRestoreSession, core.ComplexRecoveryActionRebuildReviewer} {
		t.Run(action, func(t *testing.T) {
			store, h, deps, id, data := fixedRecoveryFlowDeps(t, "STANDARD", action)
			deps.ComplexExecutionFacts = fixedResultSaveFailure{store}
			service := cleardevsvc.New(deps)
			_, _ = service.StartComplexStandardExecution(context.Background(), id)
			e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].Claim == nil || e.FixedRecoveries[0].Result != nil {
				t.Fatalf("unknown external result lost: %+v", e.FixedRecoveries)
			}
			if action == core.ComplexRecoveryActionRestoreSession && h.restores != 1 {
				t.Fatalf("restores=%d", h.restores)
			}
			if action == core.ComplexRecoveryActionRebuildReviewer {
				for _, binding := range e.RoleBindings {
					if binding.ID == e.FixedRecoveries[0].Claim.OperationID+":reviewer" {
						if err := os.WriteFile(filepath.Join(binding.WorkspacePath, "unconfirmed.txt"), []byte("preserve"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			sends, creates, restores := h.sends, h.actualCreates, h.restores
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.Open(data)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			rebindFixedRecoveryDeps(&deps, reopened, h)
			service = cleardevsvc.New(deps)
			for range 2 {
				_, _ = service.StartComplexStandardExecution(context.Background(), id)
			}
			if sends != h.sends || creates != h.actualCreates || restores != h.restores {
				t.Fatalf("repeat effect after reopen: sends %d/%d creates %d/%d restores %d/%d", sends, h.sends, creates, h.actualCreates, restores, h.restores)
			}
			e, _, err = reopened.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			phase, _ := core.DeriveComplexExecutionPhase(e)
			if phase == core.ComplexExecutionCompleted || e.FixedRecoveries[0].Result != nil {
				t.Fatal("unknown effect became success")
			}
		})
	}
}

func TestClearDevFixedRecoveryRejectsChangedTargetBeforeEffect(t *testing.T) {
	for _, name := range []string{"dirty", "candidate", "provider", "generation", "terminated", "running"} {
		t.Run(name, func(t *testing.T) {
			store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
			h.beforeAction = func() {
				rec, found, err := store.GetSession(context.Background(), h.failedSession)
				if err != nil || !found {
					t.Fatal("missing failure target")
				}
				switch name {
				case "dirty":
					if err = os.WriteFile(filepath.Join(rec.Metadata.WorkspacePath, "dirty.txt"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "candidate":
					h.git(rec.Metadata.WorkspacePath, "commit", "--allow-empty", "-m", "candidate changed")
				case "provider":
					rec.Metadata.ProviderConversationID = "changed"
				case "generation":
					rec.Metadata.ControllerGeneration = "changed"
				case "terminated":
					rec.IsTerminated = true
				case "running":
					rec.Activity.State = domain.ActivityActive
				}
				if err = store.UpdateSession(context.Background(), rec); err != nil {
					t.Fatal(err)
				}
			}
			_, _ = cleardevsvc.New(deps).StartComplexStandardExecution(context.Background(), id)
			if h.restores != 0 {
				t.Fatalf("refused target had %d restores", h.restores)
			}
			e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].Claim != nil {
				t.Fatal("unsafe target acquired external action")
			}
			if e.FixedRecoveries[0].Refusal == nil || e.FixedRecoveries[0].Refusal.ReasonCode == "" {
				t.Fatal("refusal reason was not persisted")
			}
			if name == "dirty" {
				raw, err := os.ReadFile(filepath.Join(e.FixedRecoveries[0].Request.WorkspacePath, "dirty.txt"))
				if err != nil || string(raw) != "keep" {
					t.Fatal("dirty scene not preserved")
				}
			}
		})
	}
}

func TestClearDevFixedRecoveryRejectsUnknownAndSemanticFailure(t *testing.T) {
	for _, category := range []domain.AgentFailureCategory{domain.AgentFailureDeliveryUnknown, domain.AgentFailureObservationTimeout, domain.AgentFailureResultInvalid} {
		t.Run(string(category), func(t *testing.T) {
			store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
			h.failureCategory = category
			_, _ = cleardevsvc.New(deps).StartComplexStandardExecution(context.Background(), id)
			e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if len(e.FixedRecoveries) != 0 || h.restores != 0 {
				t.Fatal("enum or unknown delivery granted fixed recovery")
			}
		})
	}
}

func TestClearDevFixedRecoverySecondFailureAndSharedCorrection(t *testing.T) {
	for _, correction := range []bool{false, true} {
		t.Run(fmt.Sprint(correction), func(t *testing.T) {
			store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRebuildReviewer)
			if correction {
				h.malformedFirst = true
				h.malformedSecond = true
			} else {
				h.secondFailure = true
			}
			service := cleardevsvc.New(deps)
			for range 3 {
				_, _ = service.StartComplexStandardExecution(context.Background(), id)
			}
			e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			phase, _ := core.DeriveComplexExecutionPhase(e)
			if phase == core.ComplexExecutionCompleted {
				t.Fatal("second failure completed execution")
			}
			if len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].Result == nil || e.FixedRecoveries[0].Result.Outcome != "PASS" {
				t.Fatalf("action did not complete: %+v", e.FixedRecoveries)
			}
			step := e.FixedRecoveries[0].Request.LogicalStepID
			attempts, err := store.ListClearDevAgentStepAttemptStates(context.Background(), id, step)
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 2 {
				t.Fatalf("attempts=%d", len(attempts))
			}
			budget, err := store.GetClearDevMessageBudget(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			_ = budget
			correctionCount := 0
			for _, relay := range h.relays {
				if relay.clientMessageID == attempts[0].ClientMessageID+":parse-correction" || relay.clientMessageID == attempts[1].ClientMessageID+":parse-correction" {
					correctionCount++
				}
			}
			if correction && correctionCount != 1 {
				t.Fatalf("shared corrections=%d", correctionCount)
			}
		})
	}
}

type fixedPreflightGate struct{ h *fixedRecoveryHarness }

func (g fixedPreflightGate) CheckControlledPreflight(ctx context.Context, harness domain.AgentHarness, model string) (ports.ChatControlledPreflight, error) {
	if g.h.proposed {
		return missingModelPreflight{}.CheckControlledPreflight(ctx, harness, model)
	}
	return passingProgressPreflight{}.CheckControlledPreflight(ctx, harness, model)
}
func TestClearDevFixedRecoveryPreflightBeforeNewReviewer(t *testing.T) {
	store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRebuildReviewer)
	deps.ControlledPreflightChecker = fixedPreflightGate{h}
	_, _ = cleardevsvc.New(deps).StartComplexStandardExecution(context.Background(), id)
	e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].Claim != nil {
		t.Fatal("preflight rejection acquired external action")
	}
	for _, cfg := range h.spawnConfigs {
		if cfg.DisplayName == "ClearDev Replacement Reviewer" {
			t.Fatal("spawn preceded preflight")
		}
	}
}
func TestClearDevFixedRecoveryReviewerWithoutProviderHistory(t *testing.T) {
	store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRebuildReviewer)
	h.emptyProvider = true
	service := cleardevsvc.New(deps)
	_, _ = service.StartComplexStandardExecution(context.Background(), id)
	e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	phase, _ := core.DeriveComplexExecutionPhase(e)
	if phase != core.ComplexExecutionCompleted || len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].Request.ProviderConversationID != "" || h.restores != 0 {
		t.Fatalf("missing-provider rebuild not proven: phase=%s", phase)
	}
}

func TestClearDevFixedRecoveryRejectsNonNativeSuccess(t *testing.T) {
	store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
	deps.RestoreOriginalAgentSession = func(ctx context.Context, id domain.SessionID) (string, error) {
		_, err := h.restore(ctx, id)
		return "saved_prompt", err
	}
	service := cleardevsvc.New(deps)
	_, _ = service.StartComplexStandardExecution(context.Background(), id)
	e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].Result == nil || e.FixedRecoveries[0].Result.Outcome == "PASS" || h.restores != 1 {
		t.Fatal("non-native result counted as restored")
	}
	attempts, err := store.ListClearDevAgentStepAttemptStates(context.Background(), id, e.FixedRecoveries[0].Request.LogicalStepID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatal("saved prompt granted a second request")
	}
}

func TestClearDevFixedRecoveryMissingNativeIdentity(t *testing.T) {
	store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
	h.emptyProvider = true
	h.failKind = "LOCAL_REVIEW"
	_, _ = cleardevsvc.New(deps).StartComplexStandardExecution(context.Background(), id)
	e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].Request.ProviderConversationID != "" || e.FixedRecoveries[0].Claim != nil || h.restores != 0 {
		t.Fatal("missing provider identity invoked native restore")
	}
}

func TestClearDevFixedRecoveryInfrastructureActualRetry(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRetryInfraCheck)
			h.failKind = "NO_AGENT_FAILURE"
			h.injectInfraOnce = true
			h.infraRetryFail = failed
			service := cleardevsvc.New(deps)
			_, _ = service.StartComplexStandardExecution(context.Background(), id)
			e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			phase, _ := core.DeriveComplexExecutionPhase(e)
			if h.integrationChecks != 2 || (phase == core.ComplexExecutionCompleted) == failed {
				t.Fatalf("actual checks=%d phase=%s failed=%v", h.integrationChecks, phase, failed)
			}
			if len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].Result == nil || e.FixedRecoveries[0].Result.CheckRunID == "" {
				t.Fatal("missing actual check outcome")
			}
			count := h.integrationChecks
			if err = service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			if h.integrationChecks != count {
				t.Fatal("retry invoked again")
			}
		})
	}
}

func TestClearDevFixedRecoveryBudgetStopsCreationAndSending(t *testing.T) {
	for _, when := range []string{"before-action", "after-create", "legacy"} {
		t.Run(when, func(t *testing.T) {
			store, h, deps, id, data := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRebuildReviewer)
			exhaust := func() {
				db, err := sql.Open("sqlite", "file:"+filepath.Join(data, "ao.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				if when == "legacy" {
					if _, err = db.Exec(`DROP TRIGGER cleardev_message_budget_versions_update_forbidden`); err != nil {
						t.Fatal(err)
					}
					if _, err = db.Exec(`UPDATE cleardev_message_budget_versions SET version='LEGACY_UNMEASURED' WHERE development_project_id=?`, id); err != nil {
						t.Fatal(err)
					}
					return
				}
				e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				r := e.FixedRecoveries[0].Request
				var budget core.ComplexExceptionBudget
				for _, b := range e.Exception.Budgets {
					if b.RoleKind == core.ComplexExceptionBudgetReviewer && b.ComplexExecutionTaskID == r.TaskID {
						budget = b
					}
				}
				var count int
				if err = db.QueryRow(`SELECT count(*) FROM cleardev_agent_message_reservations WHERE budget_id=?`, budget.ID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				for n := count; n < budget.MaxTurns*5; n++ {
					if _, err = db.Exec(`INSERT INTO cleardev_agent_message_reservations(client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,budget_id,reserved_at) VALUES (?,?,'MESSAGE_BUDGET_V1',?,?,'ORIGINAL',?,?,?,?)`, fmt.Sprintf("other-reservation-%d", n), id, fmt.Sprintf("other-step-%d", n), r.FirstAttemptID, r.SessionID, r.PromptSHA256, budget.ID, r.CreatedAt); err != nil {
						t.Fatal(err)
					}
				}
			}
			if when == "after-create" {
				h.afterReplacementSpawn = exhaust
			} else {
				h.beforeAction = exhaust
			}
			service := cleardevsvc.New(deps)
			_, _ = service.StartComplexStandardExecution(context.Background(), id)
			e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if len(e.FixedRecoveries) != 1 {
				t.Fatal("missing request")
			}
			r := e.FixedRecoveries[0]
			creates := 0
			for _, cfg := range h.spawnConfigs {
				if cfg.DisplayName == "ClearDev Replacement Reviewer" {
					creates++
				}
			}
			if when == "after-create" {
				if creates != 1 || r.Result == nil || r.Result.SessionID == "" {
					t.Fatal("new reviewer association lost")
				}
			} else if creates != 0 || r.Claim != nil {
				t.Fatal("budget refusal followed by creation")
			}
			for _, relay := range h.relays {
				if relay.clientMessageID == "cleardev-complex-execution-step-"+r.Request.LogicalStepID+":attempt:2" {
					t.Fatal("exhausted or unknown budget sent second original")
				}
			}
		})
	}
}

type fixedClaimPause struct{ *sqlite.Store }

func (f fixedClaimPause) ClaimClearDevFixedRecoveryAction(context.Context, core.FixedRecoveryClaim) (bool, error) {
	return false, nil
}

func TestClearDevFixedRecoveryConcurrentActualRestore(t *testing.T) {
	store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
	deps.ComplexExecutionFacts = fixedClaimPause{store}
	_, _ = cleardevsvc.New(deps).StartComplexStandardExecution(context.Background(), id)
	e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].Claim != nil || len(e.Exception.RecoveryActions) != 1 {
		t.Fatal("proposal was not prepared before concurrency")
	}
	deps.ComplexExecutionFacts = store
	at := e.FixedRecoveries[0].Request.CreatedAt.Add(time.Hour)
	deps.Clock = func() time.Time { return at }
	var calls atomic.Int32
	deps.RestoreOriginalAgentSession = func(ctx context.Context, id domain.SessionID) (string, error) {
		calls.Add(1)
		_, err := h.restore(ctx, id)
		return "saved_prompt", err
	}
	var wg sync.WaitGroup
	for range 2 {
		service := cleardevsvc.New(deps)
		wg.Add(1)
		go func() { defer wg.Done(); _ = service.ResumeComplexStandardExecutions(context.Background()) }()
	}
	wg.Wait()
	if calls.Load() != 1 || h.restores != 1 {
		t.Fatalf("actual restore callback calls=%d restored sessions=%d", calls.Load(), h.restores)
	}
	e, _, err = store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if e.FixedRecoveries[0].Claim == nil || e.FixedRecoveries[0].Result == nil || e.FixedRecoveries[0].Result.Outcome != "FAILED" {
		t.Fatal("concurrent operation lost its exact result")
	}
}

func TestClearDevFixedRecoveryRebuildReconcilesExactCreatedReviewer(t *testing.T) {
	store, h, deps, id, data := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRebuildReviewer)
	deps.ComplexExecutionFacts = fixedResultSaveFailure{store}
	_, _ = cleardevsvc.New(deps).StartComplexStandardExecution(context.Background(), id)
	before, _, err := store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil || len(before.FixedRecoveries) != 1 || before.FixedRecoveries[0].Result != nil {
		t.Fatalf("missing fault boundary: %v", err)
	}
	creates, restores := h.actualCreates, h.restores
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	rebindFixedRecoveryDeps(&deps, reopened, h)
	service := cleardevsvc.New(deps)
	for range 2 {
		if err := service.ResumeComplexStandardExecutions(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	after, _, err := reopened.GetClearDevComplexExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	phase, _ := core.DeriveComplexExecutionPhase(after)
	if phase != core.ComplexExecutionCompleted || after.FixedRecoveries[0].Result == nil || after.FixedRecoveries[0].Result.Outcome != "PASS" {
		t.Fatal("exact created Reviewer was not reconciled")
	}
	replacementCalls := 0
	for _, config := range h.spawnConfigs {
		if config.CreationIdempotencyKey == before.FixedRecoveries[0].Claim.OperationID+":reviewer-session" {
			replacementCalls++
		}
	}
	// Finishing this fixture creates one ordinary Reviewer for its remaining task.
	if replacementCalls != 1 || h.actualCreates != creates+1 || h.restores != restores {
		t.Fatalf("replacement calls=%d total creates=%d/%d restores=%d/%d", replacementCalls, creates, h.actualCreates, restores, h.restores)
	}
	attempts, err := reopened.ListClearDevAgentStepAttemptStates(context.Background(), id, after.FixedRecoveries[0].Request.LogicalStepID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%v err=%v", attempts, err)
	}
	sends := 0
	for _, relay := range h.relays {
		if relay.clientMessageID == attempts[1].ClientMessageID {
			sends++
		}
	}
	if sends != 1 {
		t.Fatalf("second sends=%d", sends)
	}
}

func TestClearDevFixedRecoveryRebuildPreservesOriginalModel(t *testing.T) {
	_, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRebuildReviewer)
	h.originalReviewerModel = "frozen-review-model"
	_, _ = cleardevsvc.New(deps).StartComplexStandardExecution(context.Background(), id)
	found := false
	for _, config := range h.spawnConfigs {
		if config.DisplayName == "ClearDev Replacement Reviewer" {
			found = true
			if config.AgentConfig.Model != h.originalReviewerModel {
				t.Fatalf("replacement model=%q, want %q", config.AgentConfig.Model, h.originalReviewerModel)
			}
		}
	}
	if !found {
		t.Fatal("replacement was not invoked")
	}
}
