package cleardev

import (
	"context"
	"errors"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Source observation here is a double. Actual Git, Node and SQLite data
// preservation are exercised by the independent cleardevlocal runtime tests.
type projectDeliveryFlowHarness struct {
	*projectExecutionFlowHarness
	sourceFailure bool
}

func (h *projectDeliveryFlowHarness) InspectDeliveredProjectSource(_ context.Context, _, _, sha string) (ports.ClearDevProjectSource, error) {
	if !h.sourceFailure {
		for _, candidate := range h.frozen {
			if candidate.CandidateSHA == sha {
				return ports.ClearDevProjectSource{BaseCommitSHA: sha, RepositoryURL: h.source.RepositoryURL}, nil
			}
		}
	}
	return ports.ClearDevProjectSource{}, errors.New("fixture delivered Git source is unavailable")
}

func TestProjectDeliveryExplicitSelectionAfterFinalReview(t *testing.T) {
	for _, origin := range []string{"EMPTY", "EXISTING"} {
		t.Run(origin, func(t *testing.T) {
			ctx := context.Background()
			f, child, admission, preparer := plannedProjectExecutionFixture(t, origin)
			h := &projectDeliveryFlowHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer)}
			f.s.inspector = h
			f.s.resultPreview = &projectResultPreviewDouble{}
			if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
				t.Fatal(err)
			}
			pending := driveProjectFlow(t, f, child.Requirement.ID, true)
			contract, _, err := core.ProjectContractFromRun(pending.Run)
			if err != nil {
				t.Fatal(err)
			}
			product, err := f.s.GetProductGoal(ctx, contract.ProductID)
			if err != nil || product.CanDiscuss || product.Stages[0].PlanningOnly {
				t.Fatalf("admitted execution is incorrectly shown as planning or revisable: %+v %v", product, err)
			}
			candidate := pending.Verifications[0].CandidateCommitSHA
			input := ProductDiscussionInput{RequestID: "choose-final-delivery", ExpectedPreviousID: product.Discussions[len(product.Discussions)-1].ID,
				Message: "Use the completed delivery as the next Stage source; discuss a further small feature before approval.",
				Choice: &ProductChoiceInput{OptionKey: contract.Selection.Option.Key, AOProjectID: contract.Selection.AOProjectID,
					Reason: "Continue from the exact reviewed delivery", ExpectedBaseCommitSHA: candidate, DeliveryRequirementID: child.Requirement.ID}}
			if _, err := f.s.SubmitProductDiscussion(ctx, contract.ProductID, input); err == nil {
				t.Fatal("task PASS allowed selection before final review")
			}
			completed := driveProjectFlow(t, f, child.Requirement.ID, false)
			product, err = f.s.GetProductGoal(ctx, contract.ProductID)
			if err != nil || !product.CanDiscuss || product.Stages[0].PlanningOnly || product.Stages[0].Phase != "COMPLETED" {
				t.Fatalf("final delivery cannot continue: %+v %v", product, err)
			}
			projectBefore, _, err := f.store.GetProject(ctx, contract.Selection.AOProjectID)
			if err != nil {
				t.Fatal(err)
			}
			changedOption := contract.Selection.Option
			changedOption.Description += " This is a different proposal that reused the old key."
			if _, err := f.s.observeDeliveredProductChoice(ctx, contract.ProductID, "new-proposal-with-reused-key", *input.Choice, changedOption, projectBefore); err == nil {
				t.Fatal("a changed proposal reused the old option key and acquired the prior delivery")
			}
			binding, _, err := f.s.completedProjectBaseline(ctx, contract.ProductID, child.Requirement.ID, candidate)
			if err != nil {
				t.Fatal(err)
			}
			storeSelection := contract.Selection
			storeSelection.SourceDiscussionID = product.Discussions[len(product.Discussions)-1].ID
			storeSelection.ChoiceDiscussionID = "store-reused-option-key"
			storeSelection.Option = changedOption
			storeSelection.BaseCommitSHA = candidate
			storeSelection.Delivery = &binding
			if _, err := f.store.AppendClearDevProductDiscussion(ctx, core.AppendProductDiscussionCommand{
				ExpectedPreviousID: storeSelection.SourceDiscussionID, Selection: &storeSelection,
				Discussion: core.ProductDiscussion{ID: storeSelection.ChoiceDiscussionID, ProductID: contract.ProductID,
					UserMessage: "attempt to bind a changed proposal directly through storage", CreatedAt: product.Discussions[len(product.Discussions)-1].CreatedAt.Add(1)},
			}); err == nil {
				t.Fatal("storage accepted a changed proposal that only reused the prior option key")
			}
			h.sourceFailure = true
			if _, err := f.s.SubmitProductDiscussion(ctx, contract.ProductID, input); err == nil {
				t.Fatal("missing Git source was accepted")
			}
			h.sourceFailure = false
			wrong := input
			choice := *input.Choice
			choice.ExpectedBaseCommitSHA = forty("e")
			wrong.Choice = &choice
			if _, err := f.s.SubmitProductDiscussion(ctx, contract.ProductID, wrong); err == nil {
				t.Fatal("arbitrary candidate became a final delivery baseline")
			}
			selected, err := f.s.SubmitProductDiscussion(ctx, contract.ProductID, input)
			if err != nil {
				t.Fatalf("select completed exact delivery: %v", err)
			}
			if selected.Selection == nil || selected.Selection.Delivery == nil || selected.Selection.BaseCommitSHA != candidate ||
				selected.Selection.Delivery.ExecutionRunID != completed.Run.ID || selected.Selection.Delivery.IntegrationCandidateID != completed.Integration.IntegrationCandidateID ||
				!selected.SourceCurrent || selected.Stages[0].Current || selected.Stages[0].Phase != "COMPLETED" {
				t.Fatalf("selection lost final provenance or history: %+v", selected)
			}
			for range 2 {
				replay, err := f.s.SubmitProductDiscussion(ctx, contract.ProductID, input)
				if err != nil || len(replay.Discussions) != len(selected.Discussions) || replay.Selection.BaseCommitSHA != candidate {
					t.Fatalf("delivery choice replay changed facts: %v", err)
				}
			}
			projectAfter, _, _ := f.store.GetProject(ctx, contract.Selection.AOProjectID)
			if projectBefore.Config.DefaultBranch != projectAfter.Config.DefaultBranch {
				t.Fatal("choosing a generic delivery silently moved the project default branch")
			}
			if selected.Stages[0].Stage.BaseCommitSHA != admission.BaseCommitSHA {
				t.Fatal("selecting a new delivery rewrote the historical Stage baseline")
			}
		})
	}
}
