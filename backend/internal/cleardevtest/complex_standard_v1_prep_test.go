package cleardevtest

import (
	"context"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestSeedComplexStandardV1BuildsApprovedCurrentPlan(t *testing.T) {
	store := sqlitetest.MustOpenAt(t, t.TempDir())
	prep := SeedComplexStandardV1(t, store, "ao-complex-standard-v1-prep", initPrepProjectRepo(t), t.TempDir())
	snapshot, ok, err := store.GetClearDevRequirement(context.Background(), prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("load v1 requirement: ok=%t err=%v", ok, err)
	}
	if len(snapshot.RequirementVersions) != 1 || snapshot.RequirementVersions[0].ID != prep.V1ID ||
		snapshot.RequirementVersions[0].Status != core.RequirementVersionStatusConfirmed ||
		snapshot.RequirementVersions[0].Version != 1 || snapshot.RequirementVersions[0].TaskSetVersion != 0 {
		t.Fatalf("current v1 = %#v", snapshot.RequirementVersions)
	}
	if prep.V2ID != "" || prep.V1PlanID == "" || prep.V1ReviewID == "" {
		t.Fatalf("v1 prep leaked a v2 or missed the plan: %#v", prep)
	}
	if active, err := store.HasActiveClearDevDirectionStop(context.Background(), prep.V1ID); err != nil || active {
		t.Fatalf("v1-only seed has a stop gate: active=%t err=%v", active, err)
	}
}
