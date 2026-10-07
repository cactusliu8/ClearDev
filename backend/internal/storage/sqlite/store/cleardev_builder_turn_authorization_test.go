package store_test

import (
	"context"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// The controlled recovery of a budget-exhausted builder round records one
// idempotent pending decision, and the authorization grants the extra turn
// inside the budget's own guardrails.
func TestExtraBuilderTurnRequestAndAuthorization(t *testing.T) {
	store, state := seedControlledExceptionFlow(t)
	ctx := context.Background()
	snapshot, ok, err := store.GetClearDevComplexExecution(ctx, state.prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("read execution: ok=%t err=%v", ok, err)
	}
	budgetID := ""
	for _, budget := range snapshot.Exception.Budgets {
		if budget.RoleKind == core.ComplexExceptionBudgetBuilder && budget.ComplexExecutionTaskID == snapshot.Tasks[0].ID {
			budgetID = budget.ID
		}
	}
	if budgetID == "" {
		t.Fatal("no builder budget found")
	}
	requirement, ok, err := store.GetClearDevRequirement(ctx, state.prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("read requirement: ok=%t err=%v", ok, err)
	}
	binding := core.ExtraBuilderTurnBinding{
		DevelopmentRequirementID: state.prep.RequirementID, ExecutionRunID: snapshot.Run.ID,
		TaskID: snapshot.Tasks[0].ID, BudgetID: budgetID, Round: 0,
	}
	at := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	if _, err := store.CreateExtraBuilderTurnRequest(ctx, requirement.Requirement, binding, at); err != nil {
		t.Fatal(err)
	}
	// A repeated offer for the same budget stays one pending decision.
	if _, err := store.CreateExtraBuilderTurnRequest(ctx, requirement.Requirement, binding, at); err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, row := range pending {
		if row.DecisionKind == core.HumanDecisionKindExtraBuilderTurn {
			matched++
		}
	}
	if matched != 1 {
		t.Fatalf("pending builder-turn requests=%d, want exactly one", matched)
	}
	// The authorization grants one turn and the budget guard caps the total.
	if err := store.AuthorizeExtraBuilderTurn(ctx, budgetID); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeExtraBuilderTurn(ctx, budgetID); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeExtraBuilderTurn(ctx, budgetID); err == nil {
		t.Fatal("the third builder turn authorization exceeded the guard")
	}
	db := openClearDevRawDB(t, state.dataDir)
	defer func() { _ = db.Close() }()
	granted := 0
	if err := db.QueryRow(`SELECT authorized_extra_turns FROM cleardev_complex_exception_budgets WHERE id=?`, budgetID).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	if granted != 2 {
		t.Fatalf("authorized extra turns=%d, want the guard maximum of two", granted)
	}
}
