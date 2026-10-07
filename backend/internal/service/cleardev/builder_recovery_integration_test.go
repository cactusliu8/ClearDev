package cleardev

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	sqlitedb "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestProjectBuilderBudgetRecoveryResendsOnceAndSurvivesRestart(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := attachProjectFlow(f, preparer)
	h.benchmarkSequentialCandidates = true
	h.reviewFindingPath = "src/storage.ts"
	h.reviewVerdicts = []string{"REWORK", "REWORK", "REWORK", "REWORK", "PASS"}
	ctx := context.Background()
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}

	var blocked core.ComplexExecutionSnapshot
	for transition := 0; transition < 240; transition++ {
		execution, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
		if err != nil || !found {
			t.Fatalf("read execution: found=%v err=%v", found, err)
		}
		for _, dispatch := range execution.Dispatches {
			if dispatch.Status == core.ComplexExecutionDispatchBlocked && dispatch.ReasonCode == "BUILDER_BUDGET_EXHAUSTED" {
				blocked = execution
				break
			}
		}
		if blocked.Run.ID != "" {
			break
		}
		progressed, stopped, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
		if err != nil || stopped || !progressed {
			t.Fatalf("transition %d: progressed=%v stopped=%v err=%v", transition, progressed, stopped, err)
		}
	}
	if blocked.Run.ID == "" || len(blocked.Tasks) != 1 {
		t.Fatal("generic project did not reach a budget-exhausted builder round")
	}
	task := blocked.Tasks[0]
	if task.Status != core.DevelopmentTaskStatusBlocked || task.CurrentRound != core.ComplexProjectMaxReworkCount {
		t.Fatalf("blocked task = status:%s round:%d rework:%d", task.Status, task.CurrentRound, task.ReworkCount)
	}
	blockedDispatchID := task.CurrentDispatchID
	if blockedDispatchID == "" {
		t.Fatal("budget-exhausted task lost its stopped dispatch")
	}
	beforeBuilderSends := 0
	for _, relay := range h.relays {
		if relay.response != "" && relay.prompt != "" && containsBuilderResultPrompt(relay.prompt) {
			beforeBuilderSends++
		}
	}

	if _, err := f.s.RequestExtraBuilderTurn(ctx, child.Requirement.ID, task.DevelopmentTaskID); err != nil {
		t.Fatal(err)
	}
	applyFakeDesktopDecision(t, f.store, f.s, f.s.now, child.Requirement.ID, core.HumanDecisionKindExtraBuilderTurn, core.HumanDecisionApprove)
	recovered, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
	if err != nil || !found {
		t.Fatalf("read recovered execution: found=%v err=%v", found, err)
	}
	var recoveredAttempt *core.ComplexExecutionDispatch
	for i := range recovered.Dispatches {
		if recovered.Dispatches[i].ID == blockedDispatchID {
			recoveredAttempt = &recovered.Dispatches[i]
			break
		}
	}
	if recoveredAttempt == nil || recoveredAttempt.Status != core.ComplexExecutionDispatchRework || recoveredAttempt.ReasonCode != "BUILDER_BUDGET_EXHAUSTED" {
		t.Fatalf("recovered attempt = %#v", recoveredAttempt)
	}
	if recovered.Tasks[0].Status != core.DevelopmentTaskStatusRework || recovered.Tasks[0].ReworkCount != task.ReworkCount+1 {
		t.Fatalf("recovered task = %#v", recovered.Tasks[0])
	}
	builderBudget := core.ComplexExceptionBudget{}
	for _, budget := range recovered.Exception.Budgets {
		if budget.RoleKind == core.ComplexExceptionBudgetBuilder && budget.ComplexExecutionTaskID == task.ID {
			builderBudget = budget
		}
	}
	if builderBudget.ID == "" || builderBudget.AuthorizedExtraTurns != 1 {
		t.Fatalf("builder recovery budget = %#v", builderBudget)
	}

	// Advance only until the authorized successor has actually been sent once.
	for transition := 0; transition < 80; transition++ {
		progressed, stopped, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
		if err != nil || stopped || !progressed {
			t.Fatalf("recovery transition %d: progressed=%v stopped=%v err=%v", transition, progressed, stopped, err)
		}
		after := 0
		for _, relay := range h.relays {
			if relay.response != "" && relay.prompt != "" && containsBuilderResultPrompt(relay.prompt) {
				after++
			}
		}
		if after == beforeBuilderSends+1 {
			break
		}
		if transition == 79 {
			t.Fatal("authorized recovery never sent the successor builder turn")
		}
	}

	// Simulate a daemon restart after the durable send. The external chat double
	// survives, while SQLite is closed and reopened from disk.
	clock := f.s.now
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlitedb.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.h.store = f.store
	f.s = New(Deps{
		Facts: f.store, StandardFacts: f.store, ComplexFacts: f.store, ComplexExecutionFacts: f.store, DirectionFacts: f.store, HumanDecisions: f.store,
		ParseCorrections: f.store, AgentAttempts: f.store, ControlledPreflights: f.store, ControlledPreflightChecker: alwaysPassControlledPreflight{},
		ProgressExplanations: f.store, Workspace: gitWorkspaceObserver{}, AO: f.store,
		Sessions: h, Chat: h, Inspector: h, Checks: h, RequirementFinalReviews: f.store,
		Human: allowStandardHuman{}, AutoAdvanceComplexPlans: true, PlannerTaskContracts: true,
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: f.ids.New, Clock: clock,
		BackgroundContext: context.Background(), RunBackground: func(func()) {},
	})
	// Resume repeatedly after the restart. The recovered Builder step may be
	// settled and the flow may later stop on its independent Reviewer budget,
	// but the already-sent Builder turn must never be emitted again.
	for transition := 0; transition < 40; transition++ {
		progressed, stopped, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
		if err != nil {
			t.Fatalf("restart transition %d: %v", transition, err)
		}
		if stopped || !progressed {
			break
		}
	}
	afterBuilderSends := 0
	for _, relay := range h.relays {
		if relay.response != "" && relay.prompt != "" && containsBuilderResultPrompt(relay.prompt) {
			afterBuilderSends++
		}
	}
	if afterBuilderSends != beforeBuilderSends+1 {
		t.Fatalf("restart duplicated recovered builder send: before=%d after=%d", beforeBuilderSends, afterBuilderSends)
	}
	final, _, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	preserved := false
	for _, dispatch := range final.Dispatches {
		if dispatch.ID == blockedDispatchID && dispatch.ReasonCode == "BUILDER_BUDGET_EXHAUSTED" {
			preserved = true
		}
	}
	if !preserved {
		t.Fatal("controlled recovery erased the original budget-exhausted failure")
	}
}

func containsBuilderResultPrompt(prompt string) bool {
	return strings.Contains(prompt, `"kind":"BUILDER_RESULT"`)
}
