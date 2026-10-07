package cleardevtest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestSeedComplexStandardV2BuildsFrozenS06Entry(t *testing.T) {
	store := sqlitetest.MustOpenAt(t, t.TempDir())
	prep := SeedComplexStandardV2(t, store, "ao-complex-standard-prep", initPrepProjectRepo(t), t.TempDir())
	snapshot, ok, err := store.GetClearDevRequirement(context.Background(), prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("load ComplexStandard requirement: ok=%t err=%v", ok, err)
	}
	var current core.RequirementVersion
	for _, version := range snapshot.RequirementVersions {
		if version.ID == prep.V2ID {
			current = version
		}
	}
	if current.Status != core.RequirementVersionStatusConfirmed || current.Version != 2 || current.TaskSetVersion != 0 {
		t.Fatalf("ComplexStandard v2 = %#v", current)
	}
	if active, err := store.HasActiveClearDevDirectionStop(context.Background(), prep.V1ID); err != nil || !active {
		t.Fatalf("v1 gate active=%t err=%v", active, err)
	}
	if active, err := store.HasActiveClearDevDirectionStop(context.Background(), prep.V2ID); err != nil || active {
		t.Fatalf("v2 gate active=%t err=%v", active, err)
	}
	planning, ok, err := store.GetClearDevComplexPlanning(context.Background(), prep.RequirementID)
	if err != nil || !ok {
		t.Fatalf("load ComplexStandard planning: ok=%t err=%v", ok, err)
	}
	var plan core.ComplexEngineeringPlan
	var approved bool
	for _, item := range planning.Plans {
		if item.ID == prep.V2PlanID {
			plan = item
		}
	}
	for _, item := range planning.Reviews {
		if item.ID == prep.V2ReviewID && item.Verdict == core.PlanReviewApproved {
			approved = true
		}
	}
	if !approved || plan.RequirementVersionID != prep.V2ID {
		t.Fatalf("ComplexStandard v2 plan/review = %#v %#v", plan, planning.Reviews)
	}
	roles := map[core.StandardRole]string{}
	for _, binding := range planning.RoleBindings {
		if binding.Status == core.RoleBindingStatusBound {
			roles[binding.Role] = binding.AOSessionID
		}
	}
	if roles[core.StandardRoleSteward] != prep.StewardSessionID || roles[core.StandardRoleEngineeringPlanner] != prep.PlannerSessionID {
		t.Fatalf("ComplexStandard S04 role bindings = %#v", roles)
	}
	var parsed core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(plan.PlanJSON), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tasks) != 2 || parsed.ParallelSuggestion.RecommendedBuilderCount != 1 || parsed.Tasks[0].Key != "normalize-and-deduplicate" || parsed.Tasks[1].Key != "build-summary" {
		t.Fatalf("ComplexStandard plan = %#v", parsed)
	}
	if prep.StewardSessionID == prep.PlannerSessionID || prep.PlannerWorkspacePath == "" {
		t.Fatalf("ComplexStandard role sessions = %#v", prep)
	}
}

func TestSeedComplexStandardV2UsesProductionSessionBindings(t *testing.T) {
	store := sqlitetest.MustOpenAt(t, t.TempDir())
	repo := initPrepProjectRepo(t)
	dataDir := t.TempDir()
	const projectID = "ao-complex-standard-production-sessions"
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(context.Background(), domain.ProjectRecord{ID: projectID, Path: repo, Kind: domain.ProjectKindSingleRepo, RegisteredAt: now, Config: domain.ProjectConfig{DefaultBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	// The public production spawn route does not accept a caller-selected
	// creation idempotency key; mirror those resulting session records here.
	steward := complexStandardSession(t, store, projectID, repo, dataDir, now, domain.KindOrchestrator, "", "")
	prep := SeedComplexStandardV2WithSessions(t, store, projectID, repo, dataDir, ComplexStandardPrepSessions{StewardSessionID: string(steward.ID)})
	if prep.StewardSessionID != string(steward.ID) || prep.PlannerSessionID == "" || prep.PlannerSessionID == prep.StewardSessionID || prep.PlannerWorkspacePath == "" {
		t.Fatalf("production Steward and durable Planner were not preserved: prep=%#v steward=%#v", prep, steward)
	}
}
