package cleardevtest

import (
	"context"
	"encoding/json"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestSeedComplexParallelV2BuildsThreeTaskPlan(t *testing.T) {
	store := sqlitetest.MustOpenAt(t, t.TempDir())
	prep := SeedComplexParallelV2(t, store, "ao-complex-parallel-prep", initPrepProjectRepo(t), t.TempDir())
	snapshot, ok, err := store.GetClearDevRequirement(context.Background(), prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("load ComplexParallel requirement: ok=%t err=%v", ok, err)
	}
	var current core.RequirementVersion
	for _, version := range snapshot.RequirementVersions {
		if version.ID == prep.V2ID {
			current = version
		}
	}
	if current.Status != core.RequirementVersionStatusConfirmed || current.Version != 2 || current.TaskSetVersion != 0 {
		t.Fatalf("ComplexParallel v2 = %#v", current)
	}
	planning, ok, err := store.GetClearDevComplexPlanning(context.Background(), prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("load ComplexParallel planning: ok=%t err=%v", ok, err)
	}
	var plan core.ComplexEngineeringPlan
	for _, item := range planning.Plans {
		if item.ID == prep.V2PlanID {
			plan = item
		}
	}
	var parsed core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(plan.PlanJSON), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tasks) != 3 || parsed.ParallelSuggestion.RecommendedBuilderCount != 2 {
		t.Fatalf("ComplexParallel plan = %#v", parsed)
	}
	if parsed.Tasks[0].Key != "normalize-email" || parsed.Tasks[1].Key != "deduplicate-email" || parsed.Tasks[2].Key != "build-summary" {
		t.Fatalf("ComplexParallel task keys = %#v", parsed.Tasks)
	}
	if len(parsed.Tasks[0].DependencyKeys) != 0 || len(parsed.Tasks[1].DependencyKeys) != 0 || len(parsed.Tasks[2].DependencyKeys) != 2 {
		t.Fatalf("ComplexParallel dependencies = %#v", parsed.Tasks)
	}
	selection, err := core.SelectComplexExecutionMode(parsed.Tasks, parsed.ParallelSuggestion.RecommendedBuilderCount)
	if err != nil || selection.Mode != core.WorkModeParallel || selection.BuilderCount != 2 {
		t.Fatalf("ComplexParallel mode selection = %#v err=%v", selection, err)
	}
}
