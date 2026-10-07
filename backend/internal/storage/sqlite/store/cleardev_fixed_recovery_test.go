package store_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func fixedRecoveryStoreFixture(t *testing.T, roles ...string) (*sqlite.Store, complexExecutionFlowState, core.FixedRecoveryRequest, core.AgentStepAttempt, core.FixedRecoveryClaim) {
	t.Helper()
	ctx := context.Background()
	s, state := seedControlledExceptionFlow(t)
	role := core.ComplexExceptionBudgetBuilder
	if len(roles) > 0 {
		role = roles[0]
	}
	step, category, session := messageBudgetRoleStep(t, s, state, role)
	e, _, err := s.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range e.Exception.Budgets {
		if b.RoleKind == role && b.ComplexExecutionTaskID == state.tasks[0].ID {
			if _, err = s.OccupyClearDevComplexExceptionBudget(ctx, b.ID, step.ID, step.ID, state.now); err != nil {
				t.Fatal(err)
			}
		}
	}
	at := state.now.Add(time.Minute)
	a := core.AgentStepAttempt{ID: step.ID + ":attempt:1", DevelopmentRequirementID: state.prep.RequirementID, LogicalStepID: step.ID, StepCategory: category, StepKind: step.Kind, AttemptNumber: 1, RoleBindingID: step.RoleBindingID, AOSessionID: session, ClientMessageID: step.ClientMessageID, PromptSHA256: step.PromptSHA256, RequestedAt: at, CreatedAt: &at, RequestedAtSemantics: core.AttemptTimeActualCreation}
	if _, _, err = s.EnsureClearDevAgentStepAttempt(ctx, a); err != nil {
		t.Fatal(err)
	}
	event := core.AgentAttemptEvent{ID: "fixed-failure", AttemptID: a.ID, Status: core.AgentAttemptFailed, ClientMessageID: a.ClientMessageID, TurnID: "failed-turn", TurnState: domain.TurnStateFailed, FailureCategory: domain.AgentFailureProviderUnavailable, Retryable: true, RecordedAt: at}
	if err = s.RecordClearDevAgentAttemptEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	request := core.FixedRecoveryRequest{ID: "fixed-request", RequirementID: state.prep.RequirementID, ExecutionRunID: state.run.ID, TaskID: state.tasks[0].ID, DispatchID: "budget-dispatch", LogicalStepID: a.LogicalStepID, FirstAttemptID: a.ID, FailureEventID: event.ID, RoleBindingID: a.RoleBindingID, SessionID: a.AOSessionID, ProjectID: state.projectID, ExpectedSHA: state.initialBase, PromptSHA256: a.PromptSHA256, CreatedAt: at}
	if role == core.ComplexExceptionBudgetReviewer {
		for _, review := range e.Reviews {
			if review.AgentStepID == step.ID {
				request.ReviewID = review.ID
				request.CandidateID = review.CandidateCommitID
				request.CandidateSHA = review.CandidateCommitSHA
				request.ExpectedSHA = review.CandidateCommitSHA
				request.ReviewPacketJSON = review.ReviewPacketJSON
				request.ReviewPacketSHA256 = review.ReviewPacketSHA256
			}
		}
	}
	binding := core.ComplexOnDemandBinding{ID: "fixed-recovery-role", ExecutionRunID: state.run.ID, Mode: core.ComplexOnDemandModeRecovery, SessionCreationIdempotencyKey: "fixed-recovery-role-key", Status: core.RoleBindingStatusRequested, RequestedAt: at}
	if _, _, err = s.CreateClearDevComplexExceptionOnDemandBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BindClearDevComplexExceptionOnDemand(ctx, binding.ID, state.builder.AOSessionID, state.builder.WorkspacePath, state.initialBase, at); err != nil {
		t.Fatal(err)
	}
	recovery := core.AgentStep{ID: "fixed-recovery-step", RoleBindingID: binding.ID, Kind: core.ComplexExceptionAgentStepRecovery, RequestID: binding.ID, ClientMessageID: "fixed-recovery-message", PromptSHA256: a.PromptSHA256, SendStatus: core.AgentStepSendStatusPending, RequestedAt: at}
	if _, _, err = s.CreateClearDevComplexExceptionAgentStep(ctx, state.run.ID, "", binding.ID, recovery); err != nil {
		t.Fatal(err)
	}
	proposal := core.ComplexRecoveryAction{ID: "fixed-proposal", ExecutionRunID: state.run.ID, ComplexExecutionTaskID: state.tasks[0].ID, OnDemandBindingID: recovery.RoleBindingID, AgentStepID: recovery.ID, TriggerReason: "BUILDER_UNAVAILABLE", TriggerFactID: event.ID, Action: core.ComplexRecoveryActionRestoreSession, Outcome: "PASS", Summary: "restore exact session", CreatedAt: at}
	if err = s.RecordClearDevComplexExceptionRecovery(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	claim := core.FixedRecoveryClaim{RequestID: request.ID, ProposalID: proposal.ID, Action: proposal.Action, OperationID: request.ID + ":operation", ClaimedAt: at}
	return s, state, request, a, claim
}

func TestClearDevFixedRecoveryExclusiveActionAndSecondMessage(t *testing.T) {
	s, _, r, first, claim := fixedRecoveryStoreFixture(t)
	ctx := context.Background()
	if _, err := s.EnsureClearDevFixedRecoveryRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := s.ClaimClearDevFixedRecoveryAction(ctx, claim)
			if err != nil {
				t.Error(err)
			}
			if won {
				calls.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("external calls=%d", calls.Load())
	}
	second := first
	second.ID = first.LogicalStepID + ":attempt:2"
	second.AttemptNumber = 2
	second.TriggerFailureEventID = r.FailureEventID
	second.ClientMessageID += ":attempt:2"
	if _, _, err := s.EnsureClearDevAgentStepAttempt(ctx, second); err == nil {
		t.Fatal("proposal PASS was treated as executed recovery")
	}
	if err := s.RecordClearDevFixedRecoveryResult(ctx, core.FixedRecoveryResult{RequestID: r.ID, Outcome: "PASS", SessionID: r.SessionID, RoleBindingID: r.RoleBindingID, RecordedAt: claim.ClaimedAt}); err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			saved, _, err := s.EnsureClearDevAgentStepAttempt(ctx, second)
			if err != nil {
				t.Error(err)
				return
			}
			won, err := s.ReserveClearDevAgentMessage(ctx, budgetCommand(saved, core.AgentMessageRecoveryOriginal))
			if err != nil {
				t.Error(err)
			}
			if won {
				sends.Add(1)
			}
		}()
	}
	wg.Wait()
	if sends.Load() != 1 {
		t.Fatalf("actual second sends=%d", sends.Load())
	}
	if _, err := s.ReserveClearDevAgentMessage(ctx, budgetCommand(second, core.AgentMessageParseCorrection)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveClearDevAgentMessage(ctx, budgetCommand(second, core.AgentMessageRecoveryOriginal)); err != nil {
		t.Fatal(err)
	}
	t.Logf("concurrent claims: external action=%d; second-attempt sends=%d", calls.Load(), sends.Load())
}

func TestClearDevFixedRecoveryAutomaticAdmissionRace(t *testing.T) {
	for range 3 {
		s, _, r, first, _ := fixedRecoveryStoreFixture(t)
		ctx := context.Background()
		var fixed, automatic atomic.Int32
		var wg sync.WaitGroup
		second := first
		second.ID = first.LogicalStepID + ":attempt:2"
		second.AttemptNumber = 2
		second.TriggerFailureEventID = r.FailureEventID
		second.ClientMessageID += ":attempt:2"
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := s.EnsureClearDevFixedRecoveryRequest(ctx, r); err == nil {
				fixed.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			if _, created, err := s.EnsureClearDevAgentStepAttempt(ctx, second); err == nil && created {
				automatic.Add(1)
			}
		}()
		wg.Wait()
		if fixed.Load()+automatic.Load() != 1 {
			t.Fatalf("admission fixed=%d automatic=%d", fixed.Load(), automatic.Load())
		}
	}
}

func TestClearDevFixedRecoveryStaleFailureAndBudgetStopBeforeClaim(t *testing.T) {
	for _, kind := range []string{"latest-failure", "budget"} {
		t.Run(kind, func(t *testing.T) {
			s, state, r, first, claim := fixedRecoveryStoreFixture(t)
			ctx := context.Background()
			if _, err := s.EnsureClearDevFixedRecoveryRequest(ctx, r); err != nil {
				t.Fatal(err)
			}
			if kind == "latest-failure" {
				if err := s.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: "new-terminal", AttemptID: first.ID, Status: core.AgentAttemptFailed, TurnID: "turn-next", TurnState: domain.TurnStateFailed, FailureCategory: domain.AgentFailureResultInvalid, RecordedAt: claim.ClaimedAt.Add(time.Second)}); err != nil {
					t.Fatal(err)
				}
			} else {
				e, _, err := s.GetClearDevComplexExecution(ctx, r.RequirementID)
				if err != nil {
					t.Fatal(err)
				}
				var budget core.ComplexExceptionBudget
				for _, b := range e.Exception.Budgets {
					if b.RoleKind == core.ComplexExceptionBudgetBuilder && b.ComplexExecutionTaskID == r.TaskID {
						budget = b
					}
				}
				db := openClearDevRawDB(t, state.dataDir)
				defer func() { _ = db.Close() }()
				for i := 0; i < budget.MaxTurns*5; i++ {
					if _, err := db.Exec(`INSERT INTO cleardev_agent_message_reservations(client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,budget_id,reserved_at) VALUES (?,?,'MESSAGE_BUDGET_V1',?,?,'ORIGINAL',?,?,?,?)`, fmt.Sprintf("occupied-%d", i), r.RequirementID, fmt.Sprintf("other-step-%d", i), first.ID, first.AOSessionID, first.PromptSHA256, budget.ID, claim.ClaimedAt); err != nil {
						t.Fatal(err)
					}
				}
			}
			if won, err := s.ClaimClearDevFixedRecoveryAction(ctx, claim); err == nil || won {
				t.Fatal("stale or exhausted target acquired external action")
			}
			e, _, err := s.GetClearDevComplexExecution(ctx, r.RequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if e.FixedRecoveries[0].Claim != nil {
				t.Fatal("failed transaction left a claim")
			}
		})
	}
}

func TestClearDevFixedRecoveryCannotReopenValidReview(t *testing.T) {
	for _, verdict := range []core.LocalReviewVerdict{core.LocalReviewPass, core.LocalReviewRework, core.LocalReviewBlocked, core.LocalReviewNeedsHuman} {
		t.Run(string(verdict), func(t *testing.T) {
			s, _, r, _, claim := fixedRecoveryStoreFixture(t, core.ComplexExceptionBudgetReviewer)
			ctx := context.Background()
			if _, err := s.EnsureClearDevFixedRecoveryRequest(ctx, r); err != nil {
				t.Fatal(err)
			}
			e, _, err := s.GetClearDevComplexExecution(ctx, r.RequirementID)
			if err != nil {
				t.Fatal(err)
			}
			var step core.AgentStep
			for _, item := range e.AgentSteps {
				if item.ID == r.LogicalStepID {
					step = item
				}
			}
			if _, err = s.MarkClearDevComplexExecutionAgentStepSent(ctx, step.ID, claim.ClaimedAt); err != nil {
				t.Fatal(err)
			}
			step.SendStatus = core.AgentStepSendStatusSettled
			step.TurnID = "conclusive-turn"
			step.FinalMessageID = "conclusive-message"
			step.FinalMessageText = "{}"
			step.MessageSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("{}")))
			step.CompletedAt = &claim.ClaimedAt
			if _, err = s.SettleClearDevComplexExecutionAgentStep(ctx, step); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SettleClearDevComplexExecutionReview(ctx, core.SettleComplexExecutionReviewCommand{ReviewID: r.ReviewID, TurnID: "conclusive-turn", FinalMessageID: "conclusive-message", Verdict: verdict, ReasonCode: "REVIEW_DONE", Summary: "valid review", At: claim.ClaimedAt}); err != nil {
				t.Fatal(err)
			}
			if won, err := s.ClaimClearDevFixedRecoveryAction(ctx, claim); err == nil || won {
				t.Fatal("valid verdict was reopened through fixed recovery")
			}
		})
	}
}
