package cleardev

import (
	"context"
	"reflect"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestPlanningStepContinuationKeepsGenericProjectSelection(t *testing.T) {
	for _, compilation := range []bool{false, true} {
		name := "discussion"
		if compilation {
			name = "compilation"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newProjectPlanningFixture(t, "EMPTY")
			f.s.stepTimeout = 10 * time.Second
			product := f.create(t)
			agent := &planningStepFailureAgent{productTestAgent: f.h.productTestAgent, fail: !compilation}
			id, reply := product.Goal.ID, genericProjectReply("empty")
			if compilation {
				product = f.choose(t, product, "empty")
				f.s.chat = agent
				f.h.replies = append(f.h.replies, "invalid compilation", "invalid correction")
				stage := product.Stages[0].Stage
				prepared, err := f.s.PrepareProductStage(ctx, product.Goal.ID, stage.ID, PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256})
				if err != nil {
					t.Fatal(err)
				}
				id, reply = prepared.Stages[0].Stage.DevelopmentRequirementID, genericStageReply(stage.Definition)
			} else {
				f.s.chat = agent
				f.h.replies = append(f.h.replies, reply)
				if _, err := f.s.SubmitProductDiscussion(ctx, id, projectChoice(product, "empty")); err != nil {
					t.Fatal(err)
				}
			}
			planning, _, err := f.store.GetClearDevComplexPlanning(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var step core.AgentStep
			for _, item := range planning.AgentSteps {
				if item.SendStatus == core.AgentStepSendStatusFailed {
					step = item
				}
			}
			if step.ID == "" {
				t.Fatal("generic project fixture did not stop")
			}
			for _, binding := range planning.RoleBindings {
				if binding.ID != step.RoleBindingID {
					continue
				}
				record, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				record.Metadata.ProviderConversationID = "original-generic-planning-native"
				record.Activity.State = domain.ActivityIdle
				if err := f.store.UpdateSession(ctx, record); err != nil {
					t.Fatal(err)
				}
			}
			state, err := f.store.ReadClearDevPlanningStepRecovery(ctx, id, time.Now().UTC())
			if err != nil || state.Selection == nil || state.Option.UnavailableReason != "" {
				t.Fatalf("missing exact selected source: %+v %v", state, err)
			}
			input := WorkflowRecoveryInput{RequestID: "generic-planning-retry", Action: core.RecoveryRetryPlanningStep, TargetID: state.Option.TargetID}
			calls := len(f.h.relays)
			// Changing the selected Git baseline must reject the action without
			// creating a new attempt. Restoring it permits the same request.
			originalBase := f.h.source.BaseCommitSHA
			f.h.source.BaseCommitSHA = forty("f")
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err == nil || calls != len(f.h.relays) {
				t.Fatal("changed selected baseline allowed continuation")
			}
			f.h.source.BaseCommitSHA = originalBase
			agent.fail = false
			f.h.replies = append(f.h.replies, reply)
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil || len(f.h.relays) != calls+1 {
				t.Fatalf("original source did not continue: %v", err)
			}
			after, _, err := f.store.GetClearDevProduct(ctx, product.Goal.ID)
			if err != nil || !reflect.DeepEqual(state.Selection, after.Discussions[len(after.Discussions)-1].Selection) {
				t.Fatalf("selected project was changed by recovery: %v", err)
			}
			if compilation {
				child, err := f.s.GetRequirement(ctx, id)
				if err != nil || len(child.RequirementVersions) != 1 || child.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation {
					t.Fatalf("recovered specification skipped normal confirmation: %v", err)
				}
				beforeExecution, err := f.s.GetWorkflowRecovery(ctx, id)
				if err != nil || len(beforeExecution.History) != 1 {
					t.Fatalf("missing compilation recovery history: %v", err)
				}
				// Explicit desktop/model/runtime doubles, using the actual
				// confirmation, planning and admission SQLite transactions.
				f.h.replies = append(f.h.replies, genericEngineeringReply(t, child.RequirementVersions[0]))
				applyFakeDesktopDecision(t, f.store, f.s, time.Now, id, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
				child = mustGetComplex(t, f.s, id)
				if child.ComplexPlanning == nil || len(child.ComplexPlanning.Plans) != 1 {
					t.Fatal("confirmed recovered compilation did not produce a plan")
				}
				plan := child.ComplexPlanning.Plans[0]
				admission := core.ProjectExecutionAdmission{RequestID: "start-recovered-stage", PlanID: plan.ID, PlanSHA256: plan.PlanSHA256,
					RequirementSHA256: child.RequirementVersions[0].SHA256, BaseCommitSHA: state.Selection.BaseCommitSHA}
				f.s.checks, f.s.finalReviews = &projectExecutionPreparer{projectPlanningAgent: f.h}, f.store
				f.s.runBackground = func(func()) {}
				if _, err := f.s.StartProjectExecution(ctx, id, admission); err != nil {
					t.Fatal(err)
				}
				duringExecution, err := f.s.GetWorkflowRecovery(ctx, id)
				if err != nil || duringExecution.ExecutionRunID == "" || !reflect.DeepEqual(duringExecution.History, beforeExecution.History) {
					t.Fatalf("execution hid the original compilation recovery history: %+v %v", duringExecution, err)
				}
			}
		})
	}
}
