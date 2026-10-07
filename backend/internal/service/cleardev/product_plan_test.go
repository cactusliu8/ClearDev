package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sqlitedb "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func automaticProductFixture(t *testing.T, third ...bool) (*projectPlanningFixture, ProductGoalView) {
	t.Helper()
	f := newProjectPlanningFixture(t, "EMPTY")
	initial := f.create(t)
	var result core.ProductDiscoveryResult
	if e := json.Unmarshal([]byte(genericProjectReply("empty")), &result); e != nil {
		t.Fatal(e)
	}
	var next core.ProductStageDefinition
	encoded, _ := json.Marshal(result.Stages[0])
	_ = json.Unmarshal(encoded, &next)
	next.FeatureKeys = []string{"edit"}
	result.Features = append(result.Features, core.ProductFeature{Key: "edit", Title: "修改笔记", Description: "修改已保存笔记"})
	next.Key = "edit"
	next.Title = "修改已有笔记"
	next.Goal = "修改后重启仍保留笔记"
	next.AcceptanceCriteria = []string{"修改后的笔记重启后仍保留。"}
	for i := range next.ExecutionBasis.Trial.Steps {
		next.ExecutionBasis.Trial.Steps[i].AcceptanceCriteria = next.AcceptanceCriteria
	}
	result.Stages = append(result.Stages, next)
	if len(third) > 0 && third[0] {
		var last core.ProductStageDefinition
		encoded, _ = json.Marshal(next)
		_ = json.Unmarshal(encoded, &last)
		last.Key = "archive"
		last.FeatureKeys = []string{"archive"}
		last.Title = "归档笔记"
		last.Goal = "归档后保留笔记内容"
		last.AcceptanceCriteria = []string{"归档笔记重启后仍保留。"}
		for i := range last.ExecutionBasis.Trial.Steps {
			last.ExecutionBasis.Trial.Steps[i].AcceptanceCriteria = last.AcceptanceCriteria
		}
		result.Stages = append(result.Stages, last)
		result.Features = append(result.Features, core.ProductFeature{Key: "archive", Title: "归档笔记", Description: "归档已有笔记"})
	}
	raw, _ := json.Marshal(result)
	f.h.replies = append(f.h.replies, string(raw))
	p, e := f.s.SubmitProductDiscussion(context.Background(), initial.Goal.ID, projectChoice(initial, "empty"))
	if e != nil || len(p.Stages) != len(result.Stages) {
		t.Fatalf("two-stage proposal: %v %+v", e, p)
	}
	f.s.runBackground = func(func()) {}
	return f, p
}
func authorizeAutomaticProduct(t *testing.T, f *projectPlanningFixture, p ProductGoalView) {
	t.Helper()
	stage := p.Stages[0].Stage
	if _, e := f.s.PrepareProductStage(context.Background(), p.Goal.ID, stage.ID, PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256, Automatic: true}); e != nil {
		t.Fatal(e)
	}
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, p.Goal.ID, core.HumanDecisionKindProductPlan, core.HumanDecisionApprove)
}
func compileAutomaticStage(t *testing.T, f *projectPlanningFixture, productID string, index int) RequirementView {
	t.Helper()
	ctx := context.Background()
	if e := f.s.runComplexFlow(ctx, productID); e != nil {
		t.Fatalf("parent advance: %#v", e)
	}
	p, e := f.s.GetProductGoal(ctx, productID)
	if e != nil {
		t.Fatal(e)
	}
	stage := p.Stages[index].Stage
	if stage.DevelopmentRequirementID == "" {
		t.Fatal("stage was not prepared")
	}
	f.s.chat = f.h
	f.h.replies = append(f.h.replies, genericStageReply(stage.Definition))
	for i := 0; i < 20; i++ {
		if _, _, e = f.s.advanceComplexFlow(ctx, stage.DevelopmentRequirementID); e != nil {
			t.Fatal(e)
		}
		child := mustGetComplex(t, f.s, stage.DevelopmentRequirementID)
		if len(child.RequirementVersions) > 0 {
			f.h.replies = append(f.h.replies, genericEngineeringReply(t, child.RequirementVersions[0]))
			if e = f.s.runComplexFlow(ctx, stage.DevelopmentRequirementID); e != nil {
				t.Fatalf("child planning/admission: %#v", e)
			}
			return mustGetComplex(t, f.s, stage.DevelopmentRequirementID)
		}
	}
	t.Fatal("compilation did not produce a version")
	return RequirementView{}
}
func TestProductPlanAutomaticThreeStagesAndRestart(t *testing.T) {
	ctx := context.Background()
	f, p := automaticProductFixture(t, true)
	before, e := f.s.GetProductGoal(ctx, p.Goal.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.runComplexFlow(ctx, p.Goal.ID); e != nil {
		t.Fatal(e)
	}
	unchanged, _ := f.s.GetProductGoal(ctx, p.Goal.ID)
	if unchanged.Stages[0].Stage.DevelopmentRequirementID != "" {
		t.Fatal("unapproved proposal executed")
	}
	authorizeAutomaticProduct(t, f, p)
	preparer := &projectExecutionPreparer{projectPlanningAgent: f.h}
	f.s.checks, f.s.finalReviews = preparer, f.store
	first := compileAutomaticStage(t, f, p.Goal.ID, 0)
	if first.ComplexExecution == nil || first.RequirementVersions[0].Status != core.RequirementVersionStatusConfirmed {
		t.Fatal("approved whole plan did not automatically admit first stage")
	}
	if requests, e := f.store.ListClearDevHumanDecisionRequestsForRequirement(ctx, first.Requirement.ID); e != nil || len(requests) != 0 {
		t.Fatal("automatic confirmation forged a per-stage human decision", e)
	}
	// A task or an arbitrary candidate is not a completed predecessor.
	authorized, _ := f.s.GetProductGoal(ctx, p.Goal.ID)
	fake := *p.Stages[1].Stage.Selection
	fake.ChoiceDiscussionID = ""
	fake.PlanAuthorizationID = authorized.Automation.RequestID
	fake.SourceDiscussionID = p.Stages[1].Stage.DiscussionID
	fake.Delivery = &core.ProductDeliveryBaseline{RequirementID: first.Requirement.ID, ExecutionRunID: first.ComplexExecution.Run.ID, ResultID: "unaccepted", IntegrationCandidateID: "missing", CandidateSHA: forty("a")}
	fake.BaseCommitSHA = forty("a")
	if e = f.store.BindClearDevAutomaticStageSource(ctx, p.Stages[1].Stage.ID, fake.PlanAuthorizationID, fake, time.Now()); e == nil {
		t.Fatal("non-final delivery became the next stage source")
	}
	h := &automaticPlanHarness{projectDeliveryFlowHarness: &projectDeliveryFlowHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer)}}
	f.s.inspector = h
	done := driveProjectFlow(t, f, first.Requirement.ID, false)
	f.s.resultPreview = &projectResultPreviewDouble{}
	second := compileAutomaticStage(t, f, p.Goal.ID, 1)
	if second.ComplexExecution == nil {
		t.Fatal("successor was not automatically admitted")
	}
	contract, _, e := core.ProjectContractFromRun(second.ComplexExecution.Run)
	if e != nil {
		t.Fatal(e)
	}
	if contract.BaseCommitSHA != done.Integration.CandidateCommitSHA || contract.Selection.Delivery == nil || contract.Selection.Delivery.RequirementID != first.Requirement.ID {
		t.Fatal("successor did not inherit exact previous delivery")
	}
	after, _ := f.s.GetProductGoal(ctx, p.Goal.ID)
	for i := range before.Stages {
		if after.Stages[i].Stage.DefinitionSHA256 != before.Stages[i].Stage.DefinitionSHA256 || after.Stages[i].Stage.DiscussionID != before.Stages[i].Stage.DiscussionID {
			t.Fatal("original product plan was rewritten")
		}
	}
	id := second.ComplexExecution.Run.ID
	if e = f.s.runComplexFlow(ctx, p.Goal.ID); e != nil {
		t.Fatal(e)
	}
	again := mustGetComplex(t, f.s, second.Requirement.ID)
	if again.ComplexExecution.Run.ID != id {
		t.Fatal("repeat wake created another execution")
	}
	h.currentCandidate = ""
	h.candidateSHAs = []string{forty("c"), forty("d")}
	f.s.chat = h
	f.s.sessions = h
	f.s.checks = h
	f.s.inspector = h
	f.s.resultPreview = &projectResultPreviewDouble{trial: h.trial}
	final := driveProjectFlow(t, f, second.Requirement.ID, false)
	if final.Run.CompletedAt == nil {
		t.Fatal("second stage did not retain real final-review completion gate")
	}
	beforeRelays, beforeSpawns := len(h.relays), len(h.spawnConfigs)
	if e = f.store.Close(); e != nil {
		t.Fatal(e)
	}
	f.store, e = sqlitedb.Open(f.dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.service()
	f.s.runBackground = func(func()) {}
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks, f.s.finalReviews = h, h, h, h, f.store
	h.service = f.s
	runtime := &automaticPlanPreview{projectResultPreviewDouble: projectResultPreviewDouble{trial: h.trial}}
	f.s.resultPreview = runtime
	var restartJobs []func()
	f.s.runBackground = func(run func()) { restartJobs = append(restartJobs, run) }
	if e = f.s.ResumeComplexFlows(ctx); e != nil {
		t.Fatal(e)
	}
	if len(restartJobs) == 0 {
		t.Fatal("restart failed to schedule approved product")
	}
	for _, job := range append([]func(){}, restartJobs...) {
		job()
	}
	restarted, e := f.s.GetProductGoal(ctx, p.Goal.ID)
	if e != nil || restarted.Stages[2].Stage.DevelopmentRequirementID == "" {
		t.Fatal("restart scheduler did not prepare successor", e)
	}
	f.s.runBackground = func(func()) {}
	third := compileAutomaticStage(t, f, p.Goal.ID, 2)
	if runtime.preparations != 1 || third.ComplexExecution == nil {
		t.Fatal("third stage did not prepare intermediate delivery data and start")
	}
	thirdContract, _, e := core.ProjectContractFromRun(third.ComplexExecution.Run)
	if e != nil || thirdContract.BaseCommitSHA != final.Integration.CandidateCommitSHA {
		t.Fatal("third stage lost exact intermediate delivery", e)
	}
	if len(h.spawnConfigs) < beforeSpawns || len(h.relays) < beforeRelays {
		t.Fatal("history lost")
	}
	h.currentCandidate = ""
	h.candidateSHAs = []string{forty("e"), forty("f")}
	f.s.chat = h
	last := driveProjectFlow(t, f, third.Requirement.ID, false)
	if last.Run.CompletedAt == nil {
		t.Fatal("third stage unfinished")
	}
	sends, spawns := len(h.relays), len(h.spawnConfigs)
	for range 3 {
		if e = f.s.runComplexFlow(ctx, p.Goal.ID); e != nil {
			t.Fatal(e)
		}
	}
	if len(h.relays) != sends || len(h.spawnConfigs) != spawns {
		t.Fatal("completed plan repeated work")
	}
	finished, e := f.s.GetProductGoal(ctx, p.Goal.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, stage := range finished.Stages {
		if stage.Phase != "COMPLETED" {
			t.Fatal("not all stages delivered")
		}
	}

}

// Only the runtime/model/Git sides are doubles. SQLite, scheduler, authorization
// and final-review gates above are actual production paths.
type automaticPlanPreview struct {
	projectResultPreviewDouble
	preparations int
}

func (p *automaticPlanPreview) PrepareProjectProgression(ctx context.Context, owner domain.SessionID, workspace, candidate string, contract core.ProjectExecutionContract, prepare ports.ClearDevProjectResultPrepare) error {
	p.preparations++
	_, e := p.StartProject(ctx, owner, workspace, candidate, contract, prepare)
	if e != nil {
		return e
	}
	_, e = p.Stop(ctx, owner)
	return e
}
func TestProductPlanConcurrentRequestsAndRejection(t *testing.T) {
	ctx := context.Background()
	f, p := automaticProductFixture(t)
	raw, _ := json.Marshal(p.Selection)
	latest := p.Discussions[len(p.Discussions)-1]
	binding := core.ProductPlanBinding{ProductID: p.Goal.ID, DiscussionID: latest.ID, ResultSHA256: latest.ResultSHA256, SelectionSHA256: coreDigest(raw)}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := f.store.RequestClearDevProductPlan(ctx, binding, time.Now())
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	requests, e := f.store.ListClearDevHumanDecisionRequestsForRequirement(ctx, p.Goal.ID)
	if e != nil || len(requests) != 1 {
		t.Fatal("request repeated", e, len(requests))
	}
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, p.Goal.ID, core.HumanDecisionKindProductPlan, core.HumanDecisionReject)
	if e = f.s.runComplexFlow(ctx, p.Goal.ID); e != nil {
		t.Fatal(e)
	}
	after, e := f.s.GetProductGoal(ctx, p.Goal.ID)
	if e != nil || after.Stages[0].Stage.DevelopmentRequirementID != "" {
		t.Fatal("rejected plan ran", e)
	}
}
func TestProductPlanCancellationAndChangedSpecification(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed-specification", true: "cancelled"}[cancelled], func(t *testing.T) {
			ctx := context.Background()
			f, p := automaticProductFixture(t)
			authorizeAutomaticProduct(t, f, p)
			if cancelled {
				if e := f.s.CancelRequirement(ctx, p.Goal.ID, "test stop"); e != nil {
					t.Fatal(e)
				}
				if e := f.s.runComplexFlow(ctx, p.Goal.ID); e != nil {
					t.Fatal(e)
				}
				after, _ := f.s.GetProductGoal(ctx, p.Goal.ID)
				if after.Stages[0].Stage.DevelopmentRequirementID != "" {
					t.Fatal("cancelled plan ran")
				}
				return
			}
			if e := f.s.runComplexFlow(ctx, p.Goal.ID); e != nil {
				t.Fatal(e)
			}
			after, _ := f.s.GetProductGoal(ctx, p.Goal.ID)
			stage := after.Stages[0].Stage
			var reply core.RequirementCompilationResult
			_ = json.Unmarshal([]byte(genericStageReply(stage.Definition)), &reply)
			reply.Requirements[0].Text = "Add an unapproved account system"
			raw, _ := core.MarshalAgentChosenResult(reply)
			f.h.replies = append(f.h.replies, string(raw))
			e := f.s.runComplexFlow(ctx, stage.DevelopmentRequirementID)
			if e == nil {
				t.Fatal("changed requirement was automatically confirmed")
			}
			child := mustGetComplex(t, f.s, stage.DevelopmentRequirementID)
			if child.ComplexExecution != nil || child.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation {
				t.Fatal("changed scope executed")
			}
			after, e = f.s.GetProductGoal(ctx, p.Goal.ID)
			if e != nil || after.Automation.ReasonCode != "PRODUCT_PLAN_SCOPE_CHANGED" {
				t.Fatal("stop reason not visible", e)
			}
		})
	}
}

type automaticPlanHarness struct{ *projectDeliveryFlowHarness }

func (h *automaticPlanHarness) InspectDeliveryBranch(_ context.Context, _, _, candidate string) error {
	for _, frozen := range h.frozen {
		if frozen.CandidateSHA == candidate {
			return nil
		}
	}
	return errors.New("unknown delivered branch candidate in test")
}

func TestProductPlanAdoptsPendingManualStageWithoutForgingHumanApproval(t *testing.T) {
	ctx := context.Background()
	f, p := automaticProductFixture(t)
	f.s.runBackground = func(run func()) { run() }
	_, child := f.prepare(t, p)
	f.s.runBackground = func(func()) {}
	requests, e := f.store.ListClearDevHumanDecisionRequestsForRequirement(ctx, child.Requirement.ID)
	if e != nil || len(requests) != 1 {
		t.Fatal("legacy request missing", e)
	}
	authorizeAutomaticProduct(t, f, p)
	preparer := &projectExecutionPreparer{projectPlanningAgent: f.h}
	f.s.checks, f.s.finalReviews = preparer, f.store
	f.h.replies = append(f.h.replies, genericEngineeringReply(t, child.RequirementVersions[0]))
	if e = f.s.runComplexFlow(ctx, child.Requirement.ID); e != nil {
		t.Fatal(e)
	}
	current := mustGetComplex(t, f.s, child.Requirement.ID)
	if current.ComplexExecution == nil || current.RequirementVersions[0].Status != core.RequirementVersionStatusConfirmed {
		t.Fatal("legacy pending stage still requires per-stage approval")
	}
	old, found, e := f.store.GetClearDevHumanDecisionRequest(ctx, requests[0].ID)
	if e != nil || !found || old.Status != core.HumanDecisionRequestPending {
		t.Fatal("legacy human request was rewritten", e)
	}
	if _, found, e := f.store.GetClearDevHumanDecisionEffect(ctx, old.ID); e != nil || found {
		t.Fatal("control-plane confirmation forged a human effect", e)
	}
	pending, e := f.store.ListPendingClearDevHumanDecisionRequests(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range pending {
		if r.ID == old.ID {
			t.Fatal("historical per-stage request offered again")
		}
	}
}

func TestProductPlanPauseStopsNextStagePreparation(t *testing.T) {
	ctx := context.Background()
	f, p := automaticProductFixture(t)
	authorizeAutomaticProduct(t, f, p)
	stopBuilderFirstFixture(t, f, core.ComplexExecutionSnapshot{Run: core.ComplexExecutionRun{DevelopmentRequirementID: p.Goal.ID}}, "paused")
	if e := f.s.runComplexFlow(ctx, p.Goal.ID); e != nil {
		t.Fatal(e)
	}
	view, e := f.s.GetProductGoal(ctx, p.Goal.ID)
	if e != nil {
		t.Fatal(e)
	}
	if view.Automation.Status != "STALE" || view.Stages[0].Stage.DevelopmentRequirementID != "" {
		t.Fatal("paused product continued automatically")
	}
}

func TestProductPlanNewDiscussionInvalidatesEarlierGrantAndOffer(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "approved"}[approve], func(t *testing.T) {
			ctx := context.Background()
			f, p := automaticProductFixture(t)
			stage := p.Stages[0].Stage
			if approve {
				authorizeAutomaticProduct(t, f, p)
			} else {
				if _, e := f.s.PrepareProductStage(ctx, p.Goal.ID, stage.ID, PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256, Automatic: true}); e != nil {
					t.Fatal(e)
				}
			}
			previous := p.Discussions[len(p.Discussions)-1].ID
			if _, e := f.s.SubmitProductDiscussion(ctx, p.Goal.ID, ProductDiscussionInput{RequestID: "revise-product", ExpectedPreviousID: previous, Message: "Change the planned scope"}); e != nil {
				t.Fatal(e)
			}
			requests, e := f.store.ListPendingClearDevHumanDecisionRequests(ctx)
			if e != nil {
				t.Fatal(e)
			}
			for _, r := range requests {
				if r.DecisionKind == core.HumanDecisionKindProductPlan {
					t.Fatal("stale whole-plan request was offered")
				}
			}
			ids, e := f.store.ListClearDevAutomaticProducts(ctx)
			if e != nil || len(ids) != 0 {
				t.Fatal("old grant remained runnable", e)
			}
		})
	}
}

func TestProductPlanDowngradeKeepsAuthorizationHistory(t *testing.T) {
	ctx := context.Background()
	f, p := automaticProductFixture(t)
	authorizeAutomaticProduct(t, f, p)
	db := builderReplacementStorageDB(t, f)
	provider, e := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = provider.DownTo(ctx, 196); e == nil {
		t.Fatal("downgrade erased whole-plan authority")
	}
	var version int
	if e = db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); e != nil || version != 197 {
		t.Fatal("failed downgrade changed schema", version, e)
	}
	view, e := f.s.GetProductGoal(ctx, p.Goal.ID)
	if e != nil || view.Automation.Decision != "APPROVE" {
		t.Fatal("authorization lost", e)
	}
}

func TestProductPlanReopensOriginalNativeDecision(t *testing.T) {
	ctx := context.Background()
	f, p := automaticProductFixture(t)
	stage := p.Stages[0].Stage
	if _, err := f.s.PrepareProductStage(ctx, p.Goal.ID, stage.ID, PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256, Automatic: true}); err != nil {
		t.Fatal(err)
	}
	assertDecisionKindReopens(t, f.s, p.Goal.ID, core.HumanDecisionKindProductPlan)
	after, err := f.s.GetProductGoal(ctx, p.Goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Automation == nil || after.Automation.Status != "PENDING" || after.Stages[0].Stage.DevelopmentRequirementID != "" {
		t.Fatal("reopening granted or started plan")
	}
}
