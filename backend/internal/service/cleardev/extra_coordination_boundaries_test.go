package cleardev

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func assertExtraCoordinationHistoryImmutable(t *testing.T, f *projectPlanningFixture) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, table := range []string{"cleardev_extra_coordination_requests", "cleardev_extra_coordination_grants", "cleardev_extra_coordination_decisions"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing immutable history fixture: %s count=%d err=%v", table, count, err)
		}
		for _, query := range []string{
			"UPDATE " + table + " SET created_at=created_at",
			"DELETE FROM " + table,
			"INSERT OR REPLACE INTO " + table + " SELECT * FROM " + table,
		} {
			if _, err := db.Exec(query); err == nil {
				t.Fatal("authority history could be overwritten", query)
			}
		}
	}
}

func TestExtraCoordinationPausedOrActiveSourceCannotGrant(t *testing.T) {
	ctx := context.Background()
	f, h, before := extraCoordinationFixture(t)
	input := extraCoordinationInput(t, f, h)
	planner, found, err := f.store.GetSession(ctx, h.planner)
	if err != nil || !found {
		t.Fatal("missing original Planner", found, err)
	}
	active := planner
	active.Activity.State = domain.ActivityActive
	if err := f.store.UpdateSession(ctx, active); err != nil {
		t.Fatal(err)
	}
	state, err := f.store.ReadClearDevExtraCoordination(ctx, h.requirementID)
	if err != nil || state.Option.UnavailableReason == "" {
		t.Fatal("active source offered extra coordination", state.Option, err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err == nil {
		t.Fatal("active source obtained a native decision")
	}
	if err := f.store.UpdateSession(ctx, planner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	request := extraCoordinationRequest(t, f)
	result := coordinationRepairResult(t, f, request, core.HumanDecisionApprove)

	// Persist the existing legal pause marker in this isolated test database;
	// production has no requirement-pause API. No trigger is disabled.
	stopBuilderFirstFixture(t, f, before, "paused")
	state, err = f.store.ReadClearDevExtraCoordination(ctx, h.requirementID)
	if err != nil || state.Option.UnavailableReason == "" {
		t.Fatal("paused source remained eligible", state.Option, err)
	}
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err == nil {
		t.Fatal("approval revived a paused source")
	}
	after, found, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil || !found || len(after.PlannerRuntime.ExtraCoordinationGrants) != 0 ||
		len(after.PlannerRuntime.Requests) != 2 || !reflect.DeepEqual(after.PlannerRuntime.Decisions, before.PlannerRuntime.Decisions) ||
		!reflect.DeepEqual(after.Exception.Budgets, before.Exception.Budgets) {
		t.Fatal("rejected authority changed original history or budgets", found, err)
	}
	pending, found, err := f.store.GetClearDevHumanDecisionRequest(ctx, request.ID)
	if err != nil || !found || pending.Status != "PENDING" {
		t.Fatal("rejected stale approval consumed the pending decision", found, err)
	}
	h.conv.mu.Lock()
	defer h.conv.mu.Unlock()
	if len(h.conv.sent) != 2 {
		t.Fatal("paused/active source sent a third message", len(h.conv.sent))
	}
}
