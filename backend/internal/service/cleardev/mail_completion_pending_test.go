package cleardev

import (
	"context"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// A user action may land after the last check but before the completion
// transaction. Inject it through existing service actions, never SQL mutation.
type pendingMailCompletion struct {
	ComplexExecutionFactStore
	service       *Service
	requirementID string
	mode          string
	injected      bool
	injectionErr  error
	completionErr error
	resolve       func()
}

func (s *pendingMailCompletion) CompleteClearDevComplexExecution(ctx context.Context, c core.CompleteComplexExecutionCommand) error {
	if !s.injected {
		s.injected = true
		if s.mode == "cancelled" {
			s.injectionErr = s.service.CancelRequirement(ctx, s.requirementID, "cancel before final completion")
		} else {
			runBackground := s.service.runBackground
			if s.mode == "direction-intent" {
				// Simulate the scheduling gap before the Steward has read it.
				s.service.runBackground = func(func()) {}
			}
			_, s.injectionErr = s.service.ProposeDirectionIntent(ctx, s.requirementID, ProposeDirectionIntentInput{
				RequestID: "last-moment-direction", DevelopmentRequirementID: s.requirementID, Message: directionMessage,
			})
			s.service.runBackground = runBackground
		}
		if s.injectionErr != nil {
			return s.injectionErr
		}
		if s.resolve != nil {
			s.resolve()
		}
	}
	s.completionErr = s.ComplexExecutionFactStore.CompleteClearDevComplexExecution(ctx, c)
	return s.completionErr
}

func TestMailCompletionRejectsUnresolvedActionsAtTransactionBoundary(t *testing.T) {
	for _, mode := range []string{"direction-intent", "pending-direction-decision", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newMailFlowFixture(t)
			fault := &pendingMailCompletion{ComplexExecutionFactStore: f.store, service: f.service, requirementID: f.view.Requirement.ID, mode: mode}
			f.service.complexExecution = fault
			f.confirm(t)
			if !fault.injected || fault.injectionErr != nil || fault.completionErr == nil {
				t.Fatalf("transaction boundary not proved: injected=%t action=%v completion=%v", fault.injected, fault.injectionErr, fault.completionErr)
			}
			assertMailNotCompleted(t, f)
			f.reopen(t)
			assertMailNotCompleted(t, f)
		})
	}
}

func TestMailCompletionAllowsRejectedDirectionOnOriginalCandidate(t *testing.T) {
	f, _ := newMailFlowFixture(t)
	fault := &pendingMailCompletion{ComplexExecutionFactStore: f.store, service: f.service, requirementID: f.view.Requirement.ID, mode: "rejected-direction"}
	fault.resolve = func() {
		applyFakeDesktopDecision(t, f.store, f.service, f.clock, f.view.Requirement.ID, core.HumanDecisionKindApproveDirectionChange, core.HumanDecisionReject)
	}
	f.service.complexExecution = fault
	f.confirm(t)
	if !fault.injected || fault.injectionErr != nil || fault.completionErr != nil {
		t.Fatalf("rejected change blocked original delivery: injected=%t action=%v completion=%v", fault.injected, fault.injectionErr, fault.completionErr)
	}
	f.completed(t)
}

func TestMailCompletionRequiresReviewerPass(t *testing.T) {
	for _, verdict := range []string{"BLOCKED", "NEEDS_HUMAN"} {
		t.Run(verdict, func(t *testing.T) {
			f, _ := newMailFlowFixture(t)
			f.harness.reviewVerdicts = []string{verdict}
			f.confirm(t)
			execution, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
			if err != nil || !found || len(execution.Reviews) != 1 || string(execution.Reviews[0].Verdict) != verdict {
				t.Fatalf("requested Reviewer verdict not observed: found=%t reviews=%+v err=%v", found, execution.Reviews, err)
			}
			for _, check := range execution.CheckRuns {
				if check.Kind == core.CandidateCheckIntegration && strings.Contains(check.OutputSummary, core.MailDeliveryPolicyV1) {
					t.Fatal("delivery checks ran without Reviewer PASS")
				}
			}
			assertMailNotCompleted(t, f)
		})
	}
}
