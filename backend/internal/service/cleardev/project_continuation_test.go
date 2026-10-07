package cleardev

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	sqlitedb "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func archiveProjectProposal(t *testing.T, optionKey string) string {
	t.Helper()
	var proposal map[string]any
	if json.Unmarshal([]byte(genericProjectReply(optionKey)), &proposal) != nil {
		t.Fatal("invalid proposal fixture")
	}
	basis := genericProjectBasis()
	// 新提案必须声明结构化试用方式；命令/浏览器操作由审核者亲自执行。
	basis.Trial = &core.ProjectTrial{SchemaVersion: 1, Service: true, Steps: []core.ProjectTrialStep{{ID: "archive-restart", Kind: "BROWSER",
		AcceptanceCriteria: []string{"原有笔记内容保留，归档状态在重启后仍然存在。"}, Observe: "归档一条已有笔记，重启服务，核对原内容与归档状态。"}}}
	proposal["features"] = []core.ProductFeature{{Key: "archive", Title: "归档已有笔记", Description: "添加归档状态，保留已有笔记内容。"}}
	proposal["stages"] = []core.ProductStageDefinition{{Key: "archive", Title: "笔记归档增量", Goal: "在上次交付上增加笔记归档，已有内容仍可读取。",
		FeatureKeys: []string{"archive"}, AcceptanceCriteria: []string{"原有笔记内容保留，归档状态在重启后仍然存在。"},
		NonGoals: []string{"不增加云同步。"}, Feasibility: "NEEDS_CAPABILITY", FeasibilityReason: "本测试使用模型和执行器替身，不冒充实际迁移。", ExecutionBasis: &basis}}
	raw, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Two actual service/store rounds, each with native-authority and AO/model/Git
// TEST DOUBLES. The cleardevlocal tests separately prove real data migrations.
// This test proves that the second execution uses the explicitly selected final
// delivery, not main, an earlier Stage plan, or a task-local candidate.
func TestProjectSelectedDeliveryExecutesNextStageAndSurvivesRestart(t *testing.T) {
	for _, origin := range []string{"EMPTY", "EXISTING"} {
		t.Run(origin, func(t *testing.T) {
			ctx := context.Background()
			f, first, admission, preparer := plannedProjectExecutionFixture(t, origin)
			h := &projectDeliveryFlowHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer)}
			f.s.inspector = h
			f.s.resultPreview = &projectResultPreviewDouble{}
			if _, err := f.s.StartProjectExecution(ctx, first.Requirement.ID, admission); err != nil {
				t.Fatal(err)
			}
			firstDone := driveProjectFlow(t, f, first.Requirement.ID, false)
			firstContract, _, err := core.ProjectContractFromRun(firstDone.Run)
			if err != nil {
				t.Fatal(err)
			}
			goal, err := f.s.GetProductGoal(ctx, firstContract.ProductID)
			if err != nil {
				t.Fatal(err)
			}
			projectBefore, _, err := f.store.GetProject(ctx, firstContract.Selection.AOProjectID)
			if err != nil {
				t.Fatal(err)
			}
			// For discussion/compilation/planning, use explicit Stage-1 protocol
			// replies while retaining the selected final Git source double.
			f.s.chat = f.h
			f.s.runBackground = func(run func()) { run() }
			f.h.replies = append(f.h.replies, archiveProjectProposal(t, firstContract.Selection.Option.Key))
			selected, err := f.s.SubmitProductDiscussion(ctx, goal.Goal.ID, ProductDiscussionInput{
				RequestID: "choose-first-delivery-for-archive", ExpectedPreviousID: goal.Discussions[len(goal.Discussions)-1].ID,
				Message: "从准确的上一交付继续，讨论增加归档状态并保留已有笔记。",
				Choice: &ProductChoiceInput{OptionKey: firstContract.Selection.Option.Key, AOProjectID: firstContract.Selection.AOProjectID,
					Reason: "明确选择上一最终交付；没有批准下个阶段规格。", ExpectedBaseCommitSHA: firstDone.Integration.CandidateCommitSHA, DeliveryRequirementID: first.Requirement.ID},
			})
			if err != nil || selected.Selection == nil || selected.Selection.BaseCommitSHA != firstDone.Integration.CandidateCommitSHA {
				t.Fatalf("continuation choice: %+v %v", selected, err)
			}
			var nextStage core.ProductStage
			for _, stage := range selected.Stages {
				if stage.Current && stage.Stage.Definition.Key == "archive" {
					nextStage = stage.Stage
				}
			}
			// Before preparation the immutable selection is pinned; the
			// Stage's execution base is populated by PrepareProductStage.
			if nextStage.ID == "" || nextStage.ID == firstContract.StageID || nextStage.Selection == nil || nextStage.Selection.BaseCommitSHA != firstDone.Integration.CandidateCommitSHA {
				t.Fatalf("new discussion did not create a separately selected Stage: %s", nextStage.ID)
			}
			f.h.replies = append(f.h.replies, genericStageReply(nextStage.Definition))
			prepared, err := f.s.PrepareProductStage(ctx, goal.Goal.ID, nextStage.ID, PrepareProductStageInput{DefinitionSHA256: nextStage.DefinitionSHA256})
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range prepared.Stages {
				if stage.Stage.ID == nextStage.ID {
					nextStage = stage.Stage
				}
			}
			if nextStage.BaseCommitSHA != firstDone.Integration.CandidateCommitSHA {
				t.Fatal("prepared Stage did not freeze the exact selected delivery")
			}
			next := mustGetComplex(t, f.s, nextStage.DevelopmentRequirementID)
			if next.ComplexExecution != nil || next.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation {
				t.Fatal("choosing a delivery or preparing a Stage silently approved or executed it")
			}
			planReply := strings.ReplaceAll(genericEngineeringReply(t, next.RequirementVersions[0]), "重启后仍能读取笔记。", "为已有笔记增加持久归档状态，并保留内容。")
			planReply = strings.ReplaceAll(planReply, "创建笔记并重启后内容不丢失，空笔记库可以正常打开。", "已有笔记内容不丢失，归档状态持久保存。")
			f.h.replies = append(f.h.replies, planReply)
			// This is the existing test-only desktop authority double, not a
			// real user approval or a production HTTP approval path.
			applyFakeDesktopDecision(t, f.store, f.s, time.Now, next.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
			next = mustGetComplex(t, f.s, next.Requirement.ID)
			if next.ComplexExecution != nil || next.ComplexPlanning.Phase != core.ComplexPlanningProjectPlanned || !next.TrustedProgress.ProjectPlanning.SourceCurrent {
				t.Fatalf("confirmation did not stop at explicit execution admission: %+v", next.TrustedProgress.ProjectPlanning)
			}
			plan := next.ComplexPlanning.Plans[len(next.ComplexPlanning.Plans)-1]
			start := core.ProjectExecutionAdmission{RequestID: "start-archive-stage", PlanID: plan.ID, PlanSHA256: plan.PlanSHA256,
				RequirementSHA256: next.RequirementVersions[0].SHA256, BaseCommitSHA: firstDone.Integration.CandidateCommitSHA}
			f.s.chat, f.s.runBackground = h, func(func()) {}
			h.mu.Lock()
			h.currentCandidate = "" // The next Builder is a new isolated Git double.
			h.candidateSHAs = []string{forty("c"), forty("d")}
			h.mu.Unlock()
			wrong := start
			wrong.BaseCommitSHA = admission.BaseCommitSHA
			if _, err := f.s.StartProjectExecution(ctx, next.Requirement.ID, wrong); err == nil {
				t.Fatal("next-stage execution accepted main/original baseline instead of the selected delivery")
			}
			if _, err := f.s.StartProjectExecution(ctx, next.Requirement.ID, start); err != nil {
				t.Fatal(err)
			}
			secondDone := driveProjectFlow(t, f, next.Requirement.ID, false)
			secondContract, project, err := core.ProjectContractFromRun(secondDone.Run)
			if err != nil || !project || secondContract.BaseCommitSHA != firstDone.Integration.CandidateCommitSHA || secondContract.ProductID != firstContract.ProductID ||
				secondContract.Selection.Delivery == nil || secondContract.Selection.Delivery.ExecutionRunID != firstDone.Run.ID || secondDone.Integration.CandidateCommitSHA == firstDone.Integration.CandidateCommitSHA {
				t.Fatalf("second execution lost the explicit delivery/data-owner binding: %+v %v", secondContract, err)
			}
			// Completion of B cannot authorize a C baseline while the retained
			// data still belongs to A. Opening B publishes its migrated version.
			data := &projectResultPreviewDouble{candidate: secondDone.Integration.CandidateCommitSHA}
			f.s.resultPreview = data
			choice := ProductChoiceInput{OptionKey: secondContract.Selection.Option.Key, AOProjectID: secondContract.Selection.AOProjectID,
				Reason: "Continue after the second data migration.", ExpectedBaseCommitSHA: secondDone.Integration.CandidateCommitSHA,
				DeliveryRequirementID: next.Requirement.ID}
			if _, err := f.s.observeDeliveredProductChoice(ctx, goal.Goal.ID, "proposal-for-third", choice, secondContract.Selection.Option, projectBefore); err == nil {
				t.Fatal("unopened second delivery became a third-stage baseline")
			}
			data.dataReady = true
			if f.s.selectedProjectCurrent(ctx, selected.Selection) {
				t.Fatal("inherited A selection remained current after data advanced to B")
			}
			if selectedThird, err := f.s.observeDeliveredProductChoice(ctx, goal.Goal.ID, "proposal-for-third", choice, secondContract.Selection.Option, projectBefore); err != nil || selectedThird.BaseCommitSHA != secondDone.Integration.CandidateCommitSHA {
				t.Fatalf("migrated second delivery was not selectable: %+v %v", selectedThird, err)
			}
			beforeSends, beforeSpawns := len(h.relays), len(h.spawnConfigs)
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			f.store, err = sqlitedb.Open(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.store.Close() })
			f.service()
			f.s.sessions, f.s.chat, f.s.inspector, f.s.checks, f.s.finalReviews = h, h, h, h, f.store
			if err := f.s.ResumeComplexFlows(ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.s.ResumeComplexStandardExecutions(ctx); err != nil {
				t.Fatal(err)
			}
			if len(h.relays) != beforeSends || len(h.spawnConfigs) != beforeSpawns {
				t.Fatal("restart dispatched new sessions or messages for completed stages")
			}
			final, err := f.s.GetProductGoal(ctx, goal.Goal.ID)
			if err != nil || final.Selection.BaseCommitSHA != firstDone.Integration.CandidateCommitSHA || len(final.Stages) != 2 {
				t.Fatalf("completion silently selected the latest result or lost stage history: %+v %v", final.Selection, err)
			}
			for _, stage := range final.Stages {
				if stage.Phase != "COMPLETED" || stage.PlanningOnly {
					t.Fatalf("completed stage lost its durable workbench status after restart: %+v", stage)
				}
			}
			projectAfter, _, _ := f.store.GetProject(ctx, firstContract.Selection.AOProjectID)
			if projectAfter.Config.DefaultBranch != projectBefore.Config.DefaultBranch {
				t.Fatal("generic continuation moved the project default branch")
			}
		})
	}
}
