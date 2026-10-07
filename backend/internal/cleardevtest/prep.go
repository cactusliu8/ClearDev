package cleardevtest

import (
	"context"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// PendingConfirmation is one S01 version waiting for a desktop decision.
type PendingConfirmation struct {
	RequirementID string
	VersionID     string
	RequestID     string
}

// SeedTwoPendingConfirmations uses the production S01 service to create two
// independent pending requirement versions. It is for tests and acceptance
// only and must never be imported by daemon, HTTP, or CLI production code.
func SeedTwoPendingConfirmations(t testing.TB, store *sqlite.Store, aoProjectID string) [2]PendingConfirmation {
	t.Helper()
	ctx := context.Background()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: aoProjectID, Path: "/tmp/" + aoProjectID, Kind: domain.ProjectKindSingleRepo,
		RegisteredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed AO project: %v", err)
	}
	service := cleardevsvc.New(cleardevsvc.Deps{Facts: store, HumanDecisions: store, AO: store})
	var seeded [2]PendingConfirmation
	for index, name := range []string{"Desktop approve fixture", "Desktop reject fixture"} {
		view, err := service.CreateRequirement(ctx, cleardevsvc.CreateRequirementInput{
			AOProjectID: aoProjectID, Name: name, RequirementText: name + " body",
		})
		if err != nil {
			t.Fatalf("create requirement %d: %v", index, err)
		}
		versionID := view.RequirementVersions[0].ID
		if err := service.SubmitRequirementForConfirmation(ctx, versionID); err != nil {
			t.Fatalf("submit requirement %d: %v", index, err)
		}
		pending, err := store.ListPendingClearDevHumanDecisionRequests(ctx)
		if err != nil {
			t.Fatal(err)
		}
		requestID := ""
		for _, request := range pending {
			if request.DevelopmentRequirementID == view.Requirement.ID {
				requestID = request.ID
			}
		}
		if requestID == "" {
			t.Fatalf("submit did not create a human decision request for %s", view.Requirement.ID)
		}
		seeded[index] = PendingConfirmation{
			RequirementID: view.Requirement.ID, VersionID: versionID, RequestID: requestID,
		}
	}
	return seeded
}

func RequirementStatus(t testing.TB, store *sqlite.Store, requirementID, versionID string) core.RequirementVersionStatus {
	t.Helper()
	snapshot, ok, err := store.GetClearDevRequirement(context.Background(), requirementID)
	if err != nil || !ok {
		t.Fatalf("get requirement %s: ok=%v err=%v", requirementID, ok, err)
	}
	for _, version := range snapshot.RequirementVersions {
		if version.ID == versionID {
			return version.Status
		}
	}
	t.Fatalf("version %s was not found", versionID)
	return ""
}
