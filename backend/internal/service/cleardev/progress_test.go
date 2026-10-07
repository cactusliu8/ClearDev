package cleardev_test

import (
	"context"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestListProjectProgressSortsNeedsHumanBeforeCompleted(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	service := newService(t, store, &workspaceObserver{}, allowHuman{})
	pending := createRequirement(t, service)
	if err := service.SubmitRequirementForConfirmation(context.Background(), pending.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	completed := createConfirmedRequirement(t, service)
	if err := service.CancelRequirement(context.Background(), completed.Requirement.ID, "done with this requirement"); err != nil {
		t.Fatal(err)
	}

	view, err := service.ListProjectProgress(context.Background(), "ao-service")
	if err != nil {
		t.Fatal(err)
	}
	if view.AOProjectID != "ao-service" || len(view.Requirements) != 2 {
		t.Fatalf("progress = %+v", view)
	}
	if view.Requirements[0].DevelopmentRequirementID != pending.Requirement.ID || view.Requirements[0].Phase != core.TrustedPhaseAwaitingConfirmation {
		t.Fatalf("first requirement = %+v", view.Requirements[0])
	}
	if view.Requirements[1].DevelopmentRequirementID != completed.Requirement.ID || view.Requirements[1].Phase != core.TrustedPhaseCancelled {
		t.Fatalf("second requirement = %+v", view.Requirements[1])
	}
	if view.Requirements[0].FactSummarySHA256 == "" || view.Requirements[0].FactSummarySHA256 == view.Requirements[1].FactSummarySHA256 {
		t.Fatalf("fact hashes = %s %s", view.Requirements[0].FactSummarySHA256, view.Requirements[1].FactSummarySHA256)
	}
}

func TestListProjectProgressEmptyProject(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	service := newService(t, store, &workspaceObserver{}, allowHuman{})
	view, err := service.ListProjectProgress(context.Background(), "ao-service")
	if err != nil {
		t.Fatal(err)
	}
	if view.AOProjectID != "ao-service" || len(view.Requirements) != 0 {
		t.Fatalf("empty progress = %+v", view)
	}
}

func TestGetRequirementIncludesTrustedProgress(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	service := newService(t, store, &workspaceObserver{}, allowHuman{})
	view := createRequirement(t, service)
	if view.TrustedProgress.DevelopmentRequirementID != view.Requirement.ID || view.TrustedProgress.Phase == "" {
		t.Fatalf("trusted progress = %+v", view.TrustedProgress)
	}
	again, err := service.GetRequirement(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.TrustedProgress.FactSummarySHA256 != view.TrustedProgress.FactSummarySHA256 {
		t.Fatalf("reread hash = %s, want %s", again.TrustedProgress.FactSummarySHA256, view.TrustedProgress.FactSummarySHA256)
	}
}

func TestRequestProgressExplanationRequiresSteward(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	service := newProgressService(t, store, &progressAO{store: store}, &progressAgents{store: store})
	view := createRequirement(t, service)
	_, err := service.RequestProgressExplanation(context.Background(), view.Requirement.ID)
	if apiCode(t, err) != "STEWARD_UNAVAILABLE" && apiCode(t, err) != "CLEARDEV_UNAVAILABLE" {
		t.Fatalf("explain without steward = %v", err)
	}
}
