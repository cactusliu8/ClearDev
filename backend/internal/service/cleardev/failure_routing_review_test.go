package cleardev

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestUnifiedFailureReviewerFindingReturnsArtifactToBuilderNotFavourableRereview(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	base := attachProjectFlow(f, preparer)
	h := &unifiedHandoffHarness{projectExecutionFlowHarness: base, digest: strings.Repeat("b", 64)}
	f.s.inspector = h
	f.s.stepTimeout = 15 * time.Second
	base.reviewVerdicts = []string{"BLOCKED", "PASS"}
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, child.Requirement.ID)
	if len(before.Reviews) != 1 || before.Reviews[0].Verdict != core.LocalReviewBlocked {
		t.Fatal("fixture did not produce independent BLOCKED")
	}
	f.s.automaticFailureRouting = true
	view, err := f.s.GetWorkflowRecovery(context.Background(), child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, handling := range view.FailureHandling {
		found = found || handling.SourceRole == core.TrustedOwnerReviewer && handling.Owner == core.TrustedOwnerBuilder && handling.Action == core.FailureRepair
	}
	if !found {
		t.Fatal("task finding was assigned to wrong repair owner", view.FailureHandling)
	}
	after := driveWorkflowRecovery(t, f, child.Requirement.ID)
	if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 || len(after.Reviews) != 2 || len(after.WorkflowRecoveries) != 1 {
		t.Fatal("review failure never produced a new checked candidate")
	}
	if after.WorkflowRecoveries[0].Action != core.RecoveryContinueBuilder || !reflect.DeepEqual(before.Reviews[0], after.Reviews[0]) || after.Dispatches[0].CandidateCommitSHA == after.Dispatches[1].CandidateCommitSHA {
		t.Fatal("old review changed or same candidate was simply reviewed until PASS")
	}
	if !strings.Contains(after.WorkflowRecoveries[0].Supplement, "independent task review") {
		t.Fatal("Builder did not receive Reviewer failure evidence")
	}
	for _, review := range after.Reviews {
		if review.ReviewerRoleBindingID == after.Run.BuilderRoleBindingID {
			t.Fatal("Builder became its own Reviewer")
		}
	}
}

func TestUnifiedFailureReviewerHumanAndUnfinishedNativeActionCannotStartBuilder(t *testing.T) {
	for _, mode := range []string{"human", "active-review-turn", "active-builder-turn"} {
		t.Run(mode, func(t *testing.T) {
			f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
			base := attachProjectFlow(f, preparer)
			h := &unifiedHandoffHarness{projectExecutionFlowHarness: base, digest: strings.Repeat("b", 64)}
			f.s.inspector = h
			f.s.stepTimeout = 15 * time.Second
			base.reviewVerdicts = []string{"BLOCKED", "PASS"}
			if mode == "human" {
				base.reviewVerdicts[0] = "NEEDS_HUMAN"
			}
			if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
				t.Fatal(err)
			}
			before := stoppedWorkflow(t, f, child.Requirement.ID)
			if mode != "human" {
				bindingID := before.Reviews[0].ReviewerRoleBindingID
				if mode == "active-builder-turn" {
					bindingID = before.Run.BuilderRoleBindingID
				}
				binding, found := complexExecutionBindingByID(before, bindingID)
				if !found {
					t.Fatal("missing fixture binding")
				}
				sid := domain.SessionID(binding.AOSessionID)
				snapshot := base.snapshots[sid]
				snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{ID: "active-unknown-native", State: domain.TurnStateRunning})
				base.snapshots[sid] = snapshot
			}
			f.s.automaticFailureRouting = true
			calls := len(base.relays)
			changed, err := f.s.advanceUnifiedFailureRecovery(context.Background(), child.Requirement.ID, &before)
			if err != nil || changed || calls != len(base.relays) {
				t.Fatal("review authority/unknown native action was bypassed", changed, err)
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
			if err != nil || len(after.WorkflowRecoveries) != 0 || !reflect.DeepEqual(before.Dispatches, after.Dispatches) {
				t.Fatal("unsafe review changed task state", err)
			}
		})
	}
}

func TestUnifiedFailureNativeAuthCannotHideBehindRoleUnavailable(t *testing.T) {
	for _, category := range []domain.AgentFailureCategory{domain.AgentFailureAuthentication, domain.AgentFailureQuotaExhausted, domain.AgentFailureModelUnavailable, domain.AgentFailureDriverIncompatible} {
		in := core.WorkflowFailureInput{Role: core.TrustedOwnerSteward, ReasonCode: "PRODUCT_STEWARD_UNAVAILABLE", Current: true, Eligible: true, RecoveryAction: core.RecoveryRetryPlanningStep}
		protectNativeFailure(&in, core.AgentStepAttemptView{FailureCategory: category})
		decision := core.RouteWorkflowFailure(in)
		if decision.Action != core.FailureHuman || decision.Owner != core.TrustedOwnerHuman {
			t.Fatal("account/configuration failure became an automatic retry", category, decision)
		}
	}
}
