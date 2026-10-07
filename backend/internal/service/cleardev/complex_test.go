package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

const complexFixturePRD = `实现一个 Node.js 邮件名单处理程序。
必须把邮箱两端空白去掉并转为小写，按规范化后的邮箱去重并保留第一次出现的记录，处理完成后输出接受数量和拒绝数量。
不要访问外网，也不要发送邮件。
需要事先澄清：格式无效的地址是否计入拒绝数量；重复地址是计入拒绝数量还是忽略不计。`

func TestComplexCreateLeavesZeroVersionsAndBlocksStandard(t *testing.T) {
	store, harness, ids, clock, _ := newComplexFixture(t)
	service := complexTestService(store, harness, ids, clock, context.Background())
	service.runBackground = func(func()) {}
	view, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "s04-project", Name: "复杂邮件名单", PRDText: complexFixturePRD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.RequirementVersions) != 0 || len(view.DevelopmentTasks) != 0 {
		t.Fatalf("complex create left versions/tasks: %#v", view)
	}
	if view.OverallProgress.Phase != core.OverallPhaseDefiningRequirement {
		t.Fatalf("S01 phase = %s", view.OverallProgress.Phase)
	}
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningCompiling {
		t.Fatalf("complex planning = %#v", view.ComplexPlanning)
	}
	if _, err := service.StartStandardFlow(context.Background(), view.Requirement.ID); err == nil {
		t.Fatal("standard flow started for a complex requirement")
	} else if api, ok := err.(*apierr.Error); !ok || api.Code != string(core.ReasonComplexPlanRequired) {
		t.Fatalf("standard gate = %v", err)
	}
	if _, ok, err := store.GetClearDevStandardFlow(context.Background(), view.Requirement.ID); err != nil || ok {
		t.Fatalf("STANDARD facts written: ok=%v err=%v", ok, err)
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if len(harness.spawnConfigs) != 0 {
		t.Fatalf("paused create spawned sessions: %d", len(harness.spawnConfigs))
	}
}

func TestComplexCreateRejectsInvalidInputWithoutFacts(t *testing.T) {
	store, harness, ids, clock, _ := newComplexFixture(t)
	service := complexTestService(store, harness, ids, clock, context.Background())
	if _, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "s04-project", Name: "empty", PRDText: "   ",
	}); err == nil {
		t.Fatal("empty PRD was accepted")
	}
	if _, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "missing", Name: "n", PRDText: "prd",
	}); err == nil {
		t.Fatal("missing project was accepted")
	}
}

func TestComplexHappyPathReadyPlanApprovedWithoutTasks(t *testing.T) {
	store, harness, _, _, service := newComplexService(t)
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningAwaitingConfirmation {
		t.Fatalf("after answers phase = %#v", view.ComplexPlanning)
	}
	if len(view.RequirementVersions) != 1 || view.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation {
		t.Fatalf("pending v1 = %#v", view.RequirementVersions)
	}
	if view.OverallProgress.Phase != core.OverallPhaseAwaitingConfirmation {
		t.Fatalf("S01 phase while pending = %s", view.OverallProgress.Phase)
	}
	assertNoDevelopmentWork(t, store, harness, view.Requirement.ID)

	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved {
		t.Fatalf("approved planning = %#v", view.ComplexPlanning)
	}
	if len(view.RequirementVersions) != 1 || view.RequirementVersions[0].Status != core.RequirementVersionStatusConfirmed {
		t.Fatalf("confirmed v1 = %#v", view.RequirementVersions)
	}
	if len(view.ComplexPlanning.IDMaps) == 0 {
		t.Fatal("stable identifiers were not saved")
	}
	if len(view.ComplexPlanning.Plans) != 1 || len(view.ComplexPlanning.Reviews) != 1 || view.ComplexPlanning.Reviews[0].Verdict != core.PlanReviewApproved {
		t.Fatalf("plan/review = plans=%d reviews=%#v", len(view.ComplexPlanning.Plans), view.ComplexPlanning.Reviews)
	}
	if !core.ComplexPlanIsDispatchable(planningSnapshot(view), view.ComplexPlanning.Plans[0].ID) {
		t.Fatal("approved plan was not dispatchable")
	}
	assertNoDevelopmentWork(t, store, harness, view.Requirement.ID)
	assertDistinctComplexSessions(t, view, harness)

	relays, spawns := harnessCounts(harness)
	if err := service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	relaysAfter, spawnsAfter := harnessCounts(harness)
	if relaysAfter != relays || spawnsAfter != spawns {
		t.Fatalf("approved resume changed Agent facts: relays %d->%d spawns %d->%d", relays, relaysAfter, spawns, spawnsAfter)
	}
	if _, err := service.StartStandardFlow(context.Background(), view.Requirement.ID); err == nil {
		t.Fatal("standard flow started after APPROVED")
	}
}

func TestComplexHyphenTaskKeysPassPlanningReviewAndModeSelection(t *testing.T) {
	store, _, _, _, service := newComplexService(t)
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved || len(view.ComplexPlanning.Plans) != 1 || len(view.ComplexPlanning.Reviews) != 1 {
		t.Fatalf("planning did not reach one approved plan: %#v", view.ComplexPlanning)
	}
	var parsed core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(view.ComplexPlanning.Plans[0].PlanJSON), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tasks) != 2 || parsed.Tasks[0].Key != "implement-core" || parsed.Tasks[1].Key != "complete-summary" ||
		len(parsed.Tasks[1].DependencyKeys) != 1 || parsed.Tasks[1].DependencyKeys[0] != parsed.Tasks[0].Key {
		t.Fatalf("approved hyphen task keys = %#v", parsed.Tasks)
	}

	service.runBackground = func(func()) {}
	if _, err := service.StartComplexStandardExecution(context.Background(), view.Requirement.ID); err != nil {
		t.Fatal(err)
	}
	execution, ok, err := store.GetClearDevComplexExecution(context.Background(), view.Requirement.ID)
	if err != nil || !ok {
		t.Fatalf("load selected execution: ok=%v err=%v", ok, err)
	}
	if execution.Run.Mode != core.WorkModeStandard || execution.Run.FixedBuilderCount != 1 {
		t.Fatalf("mode selection = %s builders=%d", execution.Run.Mode, execution.Run.FixedBuilderCount)
	}
}

func TestComplexInvalidTaskKeysAndDependenciesDoNotCreatePlanFacts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*core.ComplexEngineeringPlanResult)
	}{
		{name: "underscore", mutate: func(plan *core.ComplexEngineeringPlanResult) {
			plan.Tasks[0].Key = "implement_core"
			plan.Tasks[1].DependencyKeys = []string{"implement_core"}
		}},
		{name: "space", mutate: func(plan *core.ComplexEngineeringPlanResult) {
			plan.Tasks[0].Key = "implement core"
			plan.Tasks[1].DependencyKeys = []string{"implement core"}
		}},
		{name: "uppercase", mutate: func(plan *core.ComplexEngineeringPlanResult) {
			plan.Tasks[0].Key = "Implement-core"
			plan.Tasks[1].DependencyKeys = []string{"Implement-core"}
		}},
		{name: "too-long", mutate: func(plan *core.ComplexEngineeringPlanResult) {
			plan.Tasks[0].Key = "a" + strings.Repeat("b", 40)
			plan.Tasks[1].DependencyKeys = []string{plan.Tasks[0].Key}
		}},
		{name: "unknown-dependency", mutate: func(plan *core.ComplexEngineeringPlanResult) {
			plan.Tasks[1].DependencyKeys = []string{"missing-task"}
		}},
		{name: "inconsistent-dependency", mutate: func(plan *core.ComplexEngineeringPlanResult) {
			plan.Tasks[0].Key = "implement-core-v2"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, harness, _, _, service := newComplexService(t)
			view := createComplexUntilClarification(t, service)
			view = answerComplexQuestions(t, service, view)
			harness.complexPlanMutator = tc.mutate
			if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
				t.Fatal(err)
			}
			view = mustGetComplex(t, service, view.Requirement.ID)
			if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningNeedsHuman {
				t.Fatalf("invalid result phase = %#v", view.ComplexPlanning)
			}
			if len(view.ComplexPlanning.Plans) != 0 {
				t.Fatalf("invalid result created plan facts: %#v", view.ComplexPlanning.Plans)
			}
		})
	}
}

func overflowPlanTaskAcceptanceIDs(plan *core.ComplexEngineeringPlanResult, n int) {
	if len(plan.Tasks) == 0 {
		return
	}
	ids := append([]string{}, plan.Tasks[0].AcceptanceIDs...)
	next := 1
	for len(ids) < n {
		candidate := fmt.Sprintf("ACC-%03d", next)
		next++
		exists := false
		for _, id := range ids {
			if id == candidate {
				exists = true
				break
			}
		}
		if !exists {
			ids = append(ids, candidate)
		}
	}
	plan.Tasks[0].AcceptanceIDs = ids
}

func TestComplexPlanAcceptanceOverflowCorrectsOnce(t *testing.T) {
	_, harness, _, _, service := newComplexService(t)
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	remaining := 1
	harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
		if remaining == 0 {
			return
		}
		remaining--
		overflowPlanTaskAcceptanceIDs(plan, 25)
	}
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved || len(view.ComplexPlanning.Plans) != 1 {
		t.Fatalf("corrected plan was not accepted: %#v", view.ComplexPlanning)
	}
	var parsed core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(view.ComplexPlanning.Plans[0].PlanJSON), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tasks) == 0 || len(parsed.Tasks[0].AcceptanceIDs) > 20 {
		t.Fatalf("accepted overflowed acceptance ids: %#v", parsed.Tasks)
	}
	var sawCorrection bool
	harness.mu.Lock()
	defer harness.mu.Unlock()
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, parseCorrectionPromptPrefix) {
			sawCorrection = true
			if !strings.Contains(relay.prompt, "need 1-20 items, got 25") {
				t.Fatalf("correction did not include the parser error: %q", relay.prompt)
			}
		}
	}
	if !sawCorrection {
		t.Fatal("engineering plan correction was not sent")
	}
}

func TestComplexPlanAcceptanceOverflowTwiceStaysInvalid(t *testing.T) {
	_, harness, _, _, service := newComplexService(t)
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
		overflowPlanTaskAcceptanceIDs(plan, 25)
	}
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningNeedsHuman {
		t.Fatalf("double overflow phase = %#v", view.ComplexPlanning)
	}
	if view.ComplexPlanning.ReasonCode != "PLANNER_RESULT_INVALID" {
		t.Fatalf("double overflow reason = %s", view.ComplexPlanning.ReasonCode)
	}
	if len(view.ComplexPlanning.Plans) != 0 {
		t.Fatalf("double overflow created plan facts: %#v", view.ComplexPlanning.Plans)
	}
}

func TestComplexCompilationJSONCorrectsOnce(t *testing.T) {
	store, harness, ids, clock, _ := newComplexFixture(t)
	harness.compilationInvalidLeft = 1
	service := complexTestService(store, harness, ids, clock, context.Background())
	view, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "s04-project", Name: "corrected compilation", PRDText: complexFixturePRD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningAwaitingClarification {
		t.Fatalf("corrected compilation phase = %#v", view.ComplexPlanning)
	}
	if len(view.ComplexPlanning.Questions) == 0 {
		t.Fatal("corrected compilation did not produce questions")
	}
	var sawCorrection bool
	harness.mu.Lock()
	defer harness.mu.Unlock()
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, parseCorrectionPromptPrefix) {
			sawCorrection = true
		}
	}
	if !sawCorrection {
		t.Fatal("compilation correction was not sent")
	}
}

func TestComplexInvalidAgentJSONDoesNotCreateVersion(t *testing.T) {
	store, harness, ids, clock, _ := newComplexFixture(t)
	harness.invalidCompilation = true
	service := complexTestService(store, harness, ids, clock, context.Background())
	view, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "s04-project", Name: "invalid", PRDText: complexFixturePRD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.RequirementVersions) != 0 {
		t.Fatalf("invalid JSON created a version: %#v", view.RequirementVersions)
	}
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningNeedsHuman {
		t.Fatalf("invalid JSON phase = %#v", view.ComplexPlanning)
	}
	assertNoDevelopmentWork(t, store, harness, view.Requirement.ID)
}

func TestComplexReplanCreatesNextPlanAndKeepsOldPlanUndispatchable(t *testing.T) {
	store, harness, ids, clock, _ := newComplexFixture(t)
	harness.planReviewVerdicts = []string{"REPLAN", "APPROVED"}
	service := complexTestService(store, harness, ids, clock, context.Background())
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved {
		t.Fatalf("after replan planning = %#v", view.ComplexPlanning)
	}
	if len(view.ComplexPlanning.Plans) != 2 || len(view.ComplexPlanning.Reviews) != 2 {
		t.Fatalf("replan facts plans=%d reviews=%d", len(view.ComplexPlanning.Plans), len(view.ComplexPlanning.Reviews))
	}
	firstJSON := view.ComplexPlanning.Plans[0].PlanJSON
	if firstJSON != view.ComplexPlanning.Plans[0].PlanJSON {
		t.Fatal("first plan JSON changed")
	}
	if core.ComplexPlanIsDispatchable(planningSnapshot(view), view.ComplexPlanning.Plans[0].ID) {
		t.Fatal("REPLAN plan remained dispatchable")
	}
	if !core.ComplexPlanIsDispatchable(planningSnapshot(view), view.ComplexPlanning.Plans[1].ID) {
		t.Fatal("second approved plan was not dispatchable")
	}
	assertNoDevelopmentWork(t, store, harness, view.Requirement.ID)
}

func TestNextComplexPlanningRequestIDIsDeterministicPerRound(t *testing.T) {
	snapshot := core.ComplexPlanningSnapshot{Plans: []core.ComplexEngineeringPlan{{ID: "p1", RequirementVersionID: "v1", Version: 1}}}
	first := nextComplexPlanningRequestID(snapshot, "v1")
	if second := nextComplexPlanningRequestID(snapshot, "v1"); second != first {
		t.Fatalf("same round produced %q then %q", first, second)
	}
	if other := nextComplexPlanningRequestID(snapshot, "v2"); other == first {
		t.Fatalf("another version reused %q", first)
	}
	snapshot.Plans = append(snapshot.Plans, core.ComplexEngineeringPlan{ID: "p2", RequirementVersionID: "v1", Version: 2})
	if next := nextComplexPlanningRequestID(snapshot, "v1"); next == first {
		t.Fatalf("the next round reused %q", first)
	}
}

func TestComplexReplanPromptCarriesTheExactPreviousReview(t *testing.T) {
	store, harness, ids, clock, _ := newComplexFixture(t)
	harness.planReviewVerdicts = []string{"REPLAN", "APPROVED"}
	service := complexTestService(store, harness, ids, clock, context.Background())
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if len(view.ComplexPlanning.Plans) != 2 {
		t.Fatalf("plans = %d, want 2", len(view.ComplexPlanning.Plans))
	}
	harness.mu.Lock()
	var planPrompts []string
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, "kind=COMPLEX_ENGINEERING_PLAN") {
			planPrompts = append(planPrompts, relay.prompt)
		}
	}
	harness.mu.Unlock()
	if len(planPrompts) != 2 {
		t.Fatalf("planner relays = %d, want 2", len(planPrompts))
	}
	if strings.Contains(planPrompts[0], "Production Steward review of your previous plan") {
		t.Fatal("the first planning prompt carried previous feedback")
	}
	previous := view.ComplexPlanning.Plans[0]
	second := planPrompts[1]
	for _, want := range []string{
		"Production Steward review of your previous plan",
		"planId=" + previous.ID,
		"planSha256=" + previous.PlanSHA256,
		"verdict=REPLAN",
		"reasonCode=REPLAN_REQUIRED",
		"SPLIT_TASKS",
		"任务边界需要再拆一次。",
		"previousPlan=" + previous.PlanJSON,
	} {
		if !strings.Contains(second, want) {
			t.Fatalf("the second planning prompt lacks %q", want)
		}
	}
	if view.ComplexPlanning.Reviews[0].Verdict != core.PlanReviewReplan || view.ComplexPlanning.Reviews[1].Verdict != core.PlanReviewApproved {
		t.Fatalf("reviews = %#v", view.ComplexPlanning.Reviews)
	}
}

func TestComplexReplanLimitStopsBeforeTheFifthPlanningStep(t *testing.T) {
	store, harness, ids, clock, _ := newComplexFixture(t)
	harness.planReviewVerdicts = []string{"REPLAN", "REPLAN", "REPLAN", "REPLAN"}
	service := complexTestService(store, harness, ids, clock, context.Background())
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning.Phase != core.ComplexPlanningNeedsHuman || view.ComplexPlanning.ReasonCode != core.ReasonComplexReplanLimitReached {
		t.Fatalf("planning after four replans = %s/%s", view.ComplexPlanning.Phase, view.ComplexPlanning.ReasonCode)
	}
	if len(view.ComplexPlanning.Plans) != 4 || len(view.ComplexPlanning.Reviews) != 4 {
		t.Fatalf("plans/reviews = %d/%d, want 4/4", len(view.ComplexPlanning.Plans), len(view.ComplexPlanning.Reviews))
	}
	planningSteps := 0
	for _, step := range view.ComplexPlanning.AgentSteps {
		if step.Kind == core.ComplexAgentStepEngineeringPlan {
			planningSteps++
		}
	}
	if planningSteps != 4 {
		t.Fatalf("planning steps = %d, want 4", planningSteps)
	}
	harness.mu.Lock()
	plannerRelays := 0
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, "kind=COMPLEX_ENGINEERING_PLAN") {
			plannerRelays++
		}
	}
	harness.mu.Unlock()
	if plannerRelays != 4 {
		t.Fatalf("planner relays = %d, want 4", plannerRelays)
	}
	var planner core.ComplexRoleBinding
	for _, binding := range view.ComplexPlanning.RoleBindings {
		if binding.Role == core.StandardRoleEngineeringPlanner {
			planner = binding
		}
	}
	if planner.ID == "" {
		t.Fatal("planner binding missing")
	}
	_, _, err := store.CreateClearDevComplexAgentStep(context.Background(), core.AgentStep{
		ID: ids.New(), RoleBindingID: planner.ID, Kind: core.ComplexAgentStepEngineeringPlan,
		RequestID: "manual-fifth-round", ClientMessageID: "manual-fifth-round-message",
		PromptSHA256: strings.Repeat("a", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: clock(),
	})
	var rule *core.RuleError
	if err == nil || !errors.As(err, &rule) || rule.Code != core.ReasonComplexReplanLimitReached {
		t.Fatalf("fifth planning step error = %v, want %s", err, core.ReasonComplexReplanLimitReached)
	}
}

func TestComplexSingleTaskPlanExecutesStandardOne(t *testing.T) {
	store, harness, ids, clock, _ := newComplexFixture(t)
	harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
		single := plan.Tasks[0]
		single.DependencyKeys = []string{}
		plan.Tasks = []core.ComplexPlanTask{single}
		plan.ParallelSuggestion.RecommendedBuilderCount = 1
	}
	service := complexTestService(store, harness, ids, clock, context.Background())
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning.Phase != core.ComplexPlanningApproved || len(view.ComplexPlanning.Plans) != 1 {
		t.Fatalf("single-task planning = %#v", view.ComplexPlanning)
	}
	var parsed core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(view.ComplexPlanning.Plans[0].PlanJSON), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tasks) != 1 || parsed.ParallelSuggestion.RecommendedBuilderCount != 1 {
		t.Fatalf("approved single-task plan = %#v", parsed)
	}
	service.runBackground = func(func()) {}
	if _, err := service.StartComplexStandardExecution(context.Background(), view.Requirement.ID); err != nil {
		t.Fatal(err)
	}
	execution, ok, err := store.GetClearDevComplexExecution(context.Background(), view.Requirement.ID)
	if err != nil || !ok {
		t.Fatalf("load single-task execution: ok=%v err=%v", ok, err)
	}
	if execution.Run.Mode != core.WorkModeStandard || execution.Run.FixedBuilderCount != 1 || execution.Run.ModeReason != string(core.ReasonOneBuilderRequired) {
		t.Fatalf("single-task mode = %s builders=%d reason=%s", execution.Run.Mode, execution.Run.FixedBuilderCount, execution.Run.ModeReason)
	}
}

func TestComplexDesktopApproveSchedulesPlannerWithoutRestart(t *testing.T) {
	store, harness, _, clock, service := newComplexService(t)
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	pending := pendingHumanRequest(t, store, view.Requirement.ID)
	offer, err := store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{
		RequestID: pending.ID, DesktopRunID: "desk-1", Nonce: mustComplexNonce(t),
		IssuedAt: clock(), ExpiresAt: clock().Add(core.HumanDecisionOfferTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyHumanDecisionResult(context.Background(), core.HumanDecisionResult{
		ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
		DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
		BindingSchemaVersion: offer.BindingSchemaVersion, Binding: append(json.RawMessage(nil), offer.Binding...),
		ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved {
		t.Fatalf("desktop approve planning = %#v", view.ComplexPlanning)
	}
	assertNoDevelopmentWork(t, store, harness, view.Requirement.ID)
}

func TestComplexDesktopApproveDuringIdleWorkerStartsPlanner(t *testing.T) {
	store, harness, _, clock, service := newComplexService(t)
	view := createComplexUntilClarification(t, service)
	view = answerComplexQuestions(t, service, view)
	requirementID := view.Requirement.ID
	versionID := view.RequirementVersions[0].ID
	pending := pendingHumanRequest(t, store, requirementID)
	offer, err := store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{
		RequestID: pending.ID, DesktopRunID: "desk-wake", Nonce: mustComplexNonce(t),
		IssuedAt: clock(), ExpiresAt: clock().Add(core.HumanDecisionOfferTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := core.HumanDecisionResult{
		ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
		DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
		BindingSchemaVersion: offer.BindingSchemaVersion, Binding: append(json.RawMessage(nil), offer.Binding...),
		ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove,
	}

	var starts atomic.Int32
	var idleOnce sync.Once
	var wg sync.WaitGroup
	service.afterComplexIdle = func(string) {
		idleOnce.Do(func() {
			if applyErr := service.ApplyHumanDecisionResult(context.Background(), result); applyErr != nil {
				t.Errorf("approve while idle worker was exiting: %v", applyErr)
			}
		})
	}
	service.runBackground = func(run func()) {
		starts.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			run()
		}()
	}

	service.scheduleComplexFlow(requirementID)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("idle worker did not finish after desktop approve")
	}
	if starts.Load() != 1 {
		t.Fatalf("background starts = %d, want 1 so the idle worker continues without a restart", starts.Load())
	}
	view = mustGetComplex(t, service, requirementID)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved {
		t.Fatalf("approve during idle worker planning = %#v", view.ComplexPlanning)
	}
	if len(view.RequirementVersions) != 1 || view.RequirementVersions[0].ID != versionID ||
		view.RequirementVersions[0].Status != core.RequirementVersionStatusConfirmed {
		t.Fatalf("confirmed v1 = %#v", view.RequirementVersions)
	}
	if _, hasPlanner := core.ComplexRoleBindingByRole(planningSnapshot(view), core.StandardRoleEngineeringPlanner); !hasPlanner {
		t.Fatal("planner was not created without a restart")
	}
	assertNoDevelopmentWork(t, store, harness, requirementID)
}

func TestComplexDesktopRejectAndLaterDoNotStartPlanner(t *testing.T) {
	for _, decision := range []core.HumanDecisionChoice{core.HumanDecisionReject, core.HumanDecisionLater} {
		t.Run(string(decision), func(t *testing.T) {
			store, harness, _, clock, service := newComplexService(t)
			view := createComplexUntilClarification(t, service)
			view = answerComplexQuestions(t, service, view)
			pending := pendingHumanRequest(t, store, view.Requirement.ID)
			offer, err := store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{
				RequestID: pending.ID, DesktopRunID: "desk-" + string(decision), Nonce: mustComplexNonce(t),
				IssuedAt: clock(), ExpiresAt: clock().Add(core.HumanDecisionOfferTTL),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := service.ApplyHumanDecisionResult(context.Background(), core.HumanDecisionResult{
				ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
				DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
				BindingSchemaVersion: offer.BindingSchemaVersion, Binding: append(json.RawMessage(nil), offer.Binding...),
				ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: decision,
			}); err != nil {
				t.Fatal(err)
			}
			view = mustGetComplex(t, service, view.Requirement.ID)
			planner, hasPlanner := core.ComplexRoleBindingByRole(planningSnapshot(view), core.StandardRoleEngineeringPlanner)
			if hasPlanner {
				t.Fatalf("%s created planner binding %#v", decision, planner)
			}
			if len(view.ComplexPlanning.Plans) != 0 {
				t.Fatalf("%s created a plan: %#v", decision, view.ComplexPlanning.Plans)
			}
			assertNoDevelopmentWork(t, store, harness, view.Requirement.ID)
		})
	}
}

func TestComplexResumeAtClarificationAndPendingConfirmation(t *testing.T) {
	_, harness, _, _, service := newComplexService(t)
	view := createComplexUntilClarification(t, service)
	questions := len(view.ComplexPlanning.Questions)
	relays, spawns := harnessCounts(harness)
	if err := service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if len(view.ComplexPlanning.Questions) != questions {
		t.Fatalf("resume at clarification added questions: %d -> %d", questions, len(view.ComplexPlanning.Questions))
	}
	relaysAfter, spawnsAfter := harnessCounts(harness)
	if relaysAfter != relays || spawnsAfter != spawns {
		t.Fatalf("clarification resume changed Agent facts: relays %d->%d spawns %d->%d", relays, relaysAfter, spawns, spawnsAfter)
	}

	view = answerComplexQuestions(t, service, view)
	relays, spawns = harnessCounts(harness)
	if err := service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if _, hasPlanner := core.ComplexRoleBindingByRole(planningSnapshot(view), core.StandardRoleEngineeringPlanner); hasPlanner {
		t.Fatal("resume at pending confirmation created a planner")
	}
	relaysAfter, spawnsAfter = harnessCounts(harness)
	if relaysAfter != relays || spawnsAfter != spawns {
		t.Fatalf("pending confirmation resume changed Agent facts: relays %d->%d spawns %d->%d", relays, relaysAfter, spawns, spawnsAfter)
	}
}

func TestComplexConcurrentCreatesAndClarifies(t *testing.T) {
	_, _, _, _, service := newComplexService(t)
	var wg sync.WaitGroup
	views := make([]RequirementView, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			views[i], errs[i] = service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
				AOProjectID: "s04-project", Name: "concurrent", PRDText: complexFixturePRD,
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if views[i].Requirement.ID == "" || views[i].ComplexPlanning == nil {
			t.Fatalf("create %d returned empty view", i)
		}
	}
	if views[0].Requirement.ID == views[1].Requirement.ID {
		t.Fatal("concurrent creates shared a requirement id")
	}

	view := views[0]
	clarifyErrs := make([]error, 2)
	var clarifyWG sync.WaitGroup
	for i := 0; i < 2; i++ {
		clarifyWG.Add(1)
		go func(i int) {
			defer clarifyWG.Done()
			_, err := service.SubmitComplexClarifications(context.Background(), view.Requirement.ID, clarificationInput(view))
			clarifyErrs[i] = err
		}(i)
	}
	clarifyWG.Wait()
	successes := 0
	for _, err := range clarifyErrs {
		if err == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Fatalf("both clarification submits failed: %v", clarifyErrs)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning.Phase != core.ComplexPlanningAwaitingConfirmation {
		t.Fatalf("after concurrent clarify phase = %s", view.ComplexPlanning.Phase)
	}
}

func TestComplexParkedBoundRoleRemainsUsable(t *testing.T) {
	store, _, _, clock, service := newComplexService(t)
	now := clock()
	record, _, err := store.CreateSessionIdempotent(context.Background(), domain.SessionRecord{
		ProjectID: "s04-project", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto,
		CreationIdempotencyKey:     "parked-planner",
		CreationRequestFingerprint: "parked-planner-fingerprint",
		DisplayName:                "ClearDev Engineering Planner",
		Activity:                   domain.Activity{State: domain.ActivityExited, LastActivityAt: now},
		Metadata: domain.SessionMetadata{
			Branch: "codex/parked-planner", WorkspacePath: "/managed/parked-planner",
			WorkspaceRepoPath: "/tmp/s04-project", DiffBaseSHA: forty("a"),
		},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := core.ComplexRoleBinding{
		ID: "planner-binding", DevelopmentRequirementID: "req", Role: core.StandardRoleEngineeringPlanner,
		AOSessionID: string(record.ID), SessionCreationIdempotencyKey: "parked-planner",
		Status: core.RoleBindingStatusBound, RequestedAt: now,
	}
	planning := core.ComplexPlanningSnapshot{RoleBindings: []core.ComplexRoleBinding{binding}}
	got, progressed, err := service.ensureComplexRoleSession(
		context.Background(), planning, binding, "s04-project", domain.KindWorker,
		"codex/parked-planner", core.ReasonCode("PLANNER_UNAVAILABLE"),
	)
	if err != nil || progressed || got.ID != record.ID {
		t.Fatalf("parked role got=%#v progressed=%t err=%v", got, progressed, err)
	}
}

func newComplexFixture(t *testing.T) (*sqlite.Store, *standardAgentHarness, *standardTestIDs, func() time.Time, string) {
	t.Helper()
	store := sqlitetest.MustOpen(t)
	ids := &standardTestIDs{}
	var clockMu sync.Mutex
	tick := 0
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		tick++
		return time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC).Add(time.Duration(tick) * time.Second)
	}
	if err := store.UpsertProject(context.Background(), domain.ProjectRecord{
		ID: "s04-project", Path: "/tmp/s04-project", Kind: domain.ProjectKindSingleRepo, RegisteredAt: clock(),
	}); err != nil {
		t.Fatal(err)
	}
	return store, newStandardAgentHarness(store, false), ids, clock, "s04-project"
}

func newComplexService(t *testing.T) (*sqlite.Store, *standardAgentHarness, *standardTestIDs, func() time.Time, *Service) {
	t.Helper()
	store, harness, ids, clock, _ := newComplexFixture(t)
	return store, harness, ids, clock, complexTestService(store, harness, ids, clock, context.Background())
}

func complexTestService(store *sqlite.Store, harness *standardAgentHarness, ids *standardTestIDs, clock func() time.Time, background context.Context) *Service {
	return complexTestServiceWithBackground(background, store, harness, ids, clock, func(run func()) { run() })
}

func complexTestServiceWithBackground(background context.Context, store *sqlite.Store, harness *standardAgentHarness, ids *standardTestIDs, clock func() time.Time, runBackground func(func())) *Service {
	return New(Deps{
		ParseCorrections:           store,
		AgentAttempts:              store,
		ControlledPreflights:       store,
		ControlledPreflightChecker: alwaysPassControlledPreflight{},
		ProgressExplanations:       store,
		Workspace:                  gitWorkspaceObserver{},
		RecoverAgentSession:        func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },

		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store,
		AO: store, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		Human: allowStandardHuman{}, BackgroundContext: background, RunBackground: runBackground,
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})
}

func createComplexUntilClarification(t *testing.T, service *Service) RequirementView {
	t.Helper()
	view, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "s04-project", Name: "复杂邮件名单", PRDText: complexFixturePRD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningAwaitingClarification {
		t.Fatalf("create phase = %#v", view.ComplexPlanning)
	}
	if len(view.ComplexPlanning.Questions) == 0 || len(view.RequirementVersions) != 0 {
		t.Fatalf("clarification facts = %#v versions=%#v", view.ComplexPlanning, view.RequirementVersions)
	}
	return view
}

func answerComplexQuestions(t *testing.T, service *Service, view RequirementView) RequirementView {
	t.Helper()
	next, err := service.SubmitComplexClarifications(context.Background(), view.Requirement.ID, clarificationInput(view))
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func clarificationInput(view RequirementView) SubmitComplexClarificationsInput {
	if view.ComplexPlanning == nil || len(view.ComplexPlanning.CompilationRequests) == 0 {
		return SubmitComplexClarificationsInput{}
	}
	requestID := view.ComplexPlanning.CompilationRequests[len(view.ComplexPlanning.CompilationRequests)-1].ID
	round := view.ComplexPlanning.CompilationRequests[len(view.ComplexPlanning.CompilationRequests)-1].ClarificationRound
	answers := make([]ComplexClarificationAnswerInput, 0, len(view.ComplexPlanning.Questions))
	for _, question := range view.ComplexPlanning.Questions {
		if question.CompilationRequestID != requestID {
			continue
		}
		text := "计入拒绝数量。"
		if strings.Contains(question.QuestionKey, "duplicate") {
			text = "重复地址忽略不计，只保留第一次出现的记录。"
		}
		answers = append(answers, ComplexClarificationAnswerInput{QuestionKey: question.QuestionKey, Text: text})
	}
	return SubmitComplexClarificationsInput{CompilationRequestID: requestID, ClarificationRound: round, Answers: answers}
}

func mustGetComplex(t *testing.T, service *Service, id string) RequirementView {
	t.Helper()
	view, err := service.GetRequirement(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func planningSnapshot(view RequirementView) core.ComplexPlanningSnapshot {
	return core.ComplexPlanningSnapshot{
		Requirement: view.ComplexPlanning.Requirement, RoleBindings: view.ComplexPlanning.RoleBindings,
		AgentSteps: view.ComplexPlanning.AgentSteps, CompilationRequests: view.ComplexPlanning.CompilationRequests,
		Questions: view.ComplexPlanning.Questions, Answers: view.ComplexPlanning.Answers,
		Compilations: view.ComplexPlanning.Compilations, IDMaps: view.ComplexPlanning.IDMaps,
		Plans: view.ComplexPlanning.Plans, Reviews: view.ComplexPlanning.Reviews,
	}
}

func assertNoDevelopmentWork(t *testing.T, store *sqlite.Store, harness *standardAgentHarness, requirementID string) {
	t.Helper()
	snapshot, ok, err := store.GetClearDevRequirement(context.Background(), requirementID)
	if err != nil || !ok {
		t.Fatalf("load requirement: ok=%v err=%v", ok, err)
	}
	if len(snapshot.DevelopmentTasks) != 0 {
		t.Fatalf("development tasks = %#v", snapshot.DevelopmentTasks)
	}
	flow, exists, err := store.GetClearDevStandardFlow(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if exists && (len(flow.Dispatches) != 0 || len(flow.RoleBindings) != 0) {
		t.Fatalf("STANDARD work exists: %#v", flow)
	}
	for _, cfg := range harness.spawnConfigs {
		if strings.Contains(cfg.CreationIdempotencyKey, "builder") || strings.Contains(cfg.CreationIdempotencyKey, "reviewer") {
			t.Fatalf("Builder/Reviewer session was spawned: %#v", cfg)
		}
	}
}

func assertDistinctComplexSessions(t *testing.T, view RequirementView, harness *standardAgentHarness) {
	t.Helper()
	steward, ok := core.ComplexRoleBindingByRole(planningSnapshot(view), core.StandardRoleSteward)
	if !ok || steward.AOSessionID == "" || steward.Status != core.RoleBindingStatusBound {
		t.Fatalf("steward binding = %#v", steward)
	}
	planner, ok := core.ComplexRoleBindingByRole(planningSnapshot(view), core.StandardRoleEngineeringPlanner)
	if !ok || planner.AOSessionID == "" || planner.AOSessionID == steward.AOSessionID {
		t.Fatalf("planner binding = %#v steward=%s", planner, steward.AOSessionID)
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if len(harness.spawnConfigs) != 2 {
		t.Fatalf("spawn configs = %d, want 2", len(harness.spawnConfigs))
	}
	seen := map[domain.SessionKind]bool{}
	for _, cfg := range harness.spawnConfigs {
		if cfg.Harness != domain.HarnessCodex || cfg.RequestedMode != domain.SessionModeChat || cfg.AgentConfig.Permissions != domain.PermissionModeAuto || cfg.Prompt != "" {
			t.Fatalf("spawn config = %#v", cfg)
		}
		seen[cfg.Kind] = true
	}
	if !seen[domain.KindOrchestrator] || !seen[domain.KindWorker] {
		t.Fatalf("spawn kinds = %#v", seen)
	}
}

func harnessCounts(harness *standardAgentHarness) (relays, spawns int) {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	return len(harness.relays), len(harness.spawnConfigs)
}

func pendingHumanRequest(t *testing.T, store *sqlite.Store, requirementID string) core.HumanDecisionRequest {
	t.Helper()
	pending, err := store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range pending {
		if request.DevelopmentRequirementID == requirementID {
			return request
		}
	}
	t.Fatal("pending human decision request was not found")
	return core.HumanDecisionRequest{}
}

func mustComplexNonce(t *testing.T) string {
	t.Helper()
	nonce, err := humanauthority.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return nonce
}
