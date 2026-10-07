package cleardev

import (
	"context"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRequirementFinalReviewCurrentProgressUsesWholeRequirementTarget(t *testing.T) {
	f, _, review := pauseAtFinalReview(t, "SENT")
	view, err := f.service.GetRequirement(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, work := range view.TrustedProgress.ControlledWork {
		if work.RoleBindingID != review.ID {
			continue
		}
		found = true
		if work.Role != core.RequirementFinalReviewerRole || work.AOSessionID != review.AOSessionID || work.LogicalStepID != review.Step().ID || work.CandidateSHA != review.CandidateCommitSHA || work.ReviewID != review.ID || work.TaskMappingID != "" || work.DevelopmentTaskID != "" || work.State != "OBSERVING" || work.StepBudget == nil {
			t.Fatalf("final review progress was task-scoped, unbound or missing its message budget: %+v", work)
		}
	}
	if !found {
		t.Fatal("current progress omitted the independent final reviewer")
	}
	missing := false
	for _, evidence := range view.ComplexExecution.MissingEvidence {
		if evidence == "REQUIREMENT_FINAL_REVIEW_PASS" {
			missing = true
		}
	}
	if !missing || view.TrustedProgress.Phase == core.TrustedPhaseCompleted {
		t.Fatal("current progress omitted the requirement-level completion gate")
	}
}

func TestRequirementFinalReviewPreflightFailureIsVisible(t *testing.T) {
	f, h, review := pauseAtFinalReview(t, "REQUESTED")
	attachRequirementFinalReviewFixture(f, h)
	f.service.preflightChecker = &scriptedControlledPreflight{fn: func(requested string) (ports.ChatControlledPreflight, error) {
		result := livePreflightCatalog([]string{"gpt-5.6-terra"}, "")
		result.RequestedModel = requested
		return result, ports.ErrChatModelNotAvailable
	}}
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	view, err := f.service.GetRequirement(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	visible := false
	for _, work := range view.TrustedProgress.ControlledWork {
		if work.RoleBindingID == review.ID && work.Role == core.RequirementFinalReviewerRole && work.PreflightID != "" && work.ReasonCode == core.ReasonModelNotAvailable && work.State == "AWAITING_USER" {
			visible = true
		}
	}
	if !visible || h.finalSends != 0 || finalReviewExecution(t, f).FinalReview.Status != "REQUESTED" {
		t.Fatalf("final reviewer preflight gate is hidden or bypassed: work=%+v sends=%d", view.TrustedProgress.ControlledWork, h.finalSends)
	}
	assertMailNotCompleted(t, f)
}

func TestRequirementFinalReviewCurrentCompletionRequiresExactPass(t *testing.T) {
	f, _ := newRequirementFinalReviewFixture(t)
	f.confirm(t)
	execution := f.completed(t)
	for _, mode := range []string{"missing", "plan", "sha", "non-pass"} {
		t.Run(mode, func(t *testing.T) {
			copyOfExecution := execution
			copyOfReview := *execution.FinalReview
			copyOfExecution.FinalReview = &copyOfReview
			switch mode {
			case "missing":
				copyOfExecution.FinalReview = nil
			case "plan":
				copyOfReview.PlanID = "other-plan"
			case "sha":
				copyOfReview.CandidateCommitSHA = forty("f")
			case "non-pass":
				copyOfReview.Verdict = "BLOCKED"
			}
			phase, _, _ := core.CurrentExecutionProgress(copyOfExecution, nil, f.clock())
			if phase == core.ComplexExecutionCompleted {
				t.Fatal("current completion projection ignored missing or mismatched final review")
			}
		})
	}
}

func TestRequirementFinalReviewCancellationStopsCompletionAndDelivery(t *testing.T) {
	for _, boundary := range []string{"PENDING", "SENT", "SETTLED"} {
		t.Run(boundary, func(t *testing.T) {
			f, h, review := pauseAtFinalReview(t, boundary)
			execution := finalReviewExecution(t, f)
			command := finalReviewCompletionCommand(t, f, execution)
			if err := f.service.CancelRequirement(context.Background(), f.view.Requirement.ID, "explicit final-review cancellation test"); err != nil {
				t.Fatal(err)
			}
			beforeSends := h.finalSends
			attachRequirementFinalReviewFixture(f, h)
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			if h.finalSends != beforeSends || f.store.CompleteClearDevComplexExecution(context.Background(), command) == nil {
				t.Fatal("cancelled requirement sent a new final message or completed")
			}
			if boundary == "PENDING" {
				step := review.Step()
				if err := f.service.relayAgentTurn(context.Background(), f.view.Requirement.ID, core.AgentStepCategoryComplexExecution, step, review.AOSessionID, requirementFinalReviewPrompt(review), step.ClientMessageID, core.AgentAttemptSent, f.clock()); err == nil {
					t.Fatal("message ledger allowed a final provider send after cancellation")
				}
				if h.finalSends != beforeSends {
					t.Fatal("cancellation was checked only after contacting the provider")
				}
			}
			assertMailNotCompleted(t, f)
		})
	}
}
