package cleardev

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestExecutionChoiceActionPreservesOtherProjectSettings(t *testing.T) {
	ctx := context.Background()
	store, harness, ids, clock, projectID := newComplexFixture(t)
	project, _, _ := store.GetProject(ctx, projectID)
	project.Config.Env = map[string]string{"EXAMPLE_SETTING": "unchanged"}
	project.Config.Worker.AgentConfig.Model = "ordinary-model"
	project.Config.AgentRules = "Keep ordinary AO instructions."
	if err := store.UpsertProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	service := complexTestService(store, harness, ids, clock, ctx)
	choice := domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "example/model"}
	for i := 0; i < 2; i++ {
		view, err := service.SetProjectExecution(ctx, projectID, choice)
		if err != nil || view.Agent != choice.Harness || view.Model != choice.Model || !view.Known {
			t.Fatalf("save/replay: %+v %v", view, err)
		}
	}
	actual, _, err := store.GetProject(ctx, projectID)
	project.Config.ClearDev = &choice
	if err != nil || !reflect.DeepEqual(actual, project) {
		t.Fatalf("unrelated project settings changed: %+v %v", actual, err)
	}
	if _, err := service.SetProjectExecution(ctx, projectID, domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "missing-provider"}); err == nil {
		t.Fatal("invalid model accepted")
	}
	_, spawns := harnessCounts(harness)
	if spawns != 0 {
		t.Fatal("saving configuration spawned a role")
	}
}

func TestPreflightRetryOnlyWakesCurrentFailedWorkflow(t *testing.T) {
	for _, mode := range []string{"current", "missing-id", "old-id", "passed", "not-yet", "cancelled", "foreign-role"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, harness, ids, clock, projectID := newComplexFixture(t)
			service := complexTestService(store, harness, ids, clock, ctx)
			service.preflightChecker = &scriptedControlledPreflight{fn: func(requested string) (ports.ChatControlledPreflight, error) {
				result := livePreflightCatalog([]string{"catalog-model"}, "")
				result.RequestedModel = requested
				return result, ports.ErrChatModelNotAvailable
			}}
			view, err := service.CreateComplexRequirement(ctx, CreateComplexRequirementInput{AOProjectID: projectID, Name: "Retry admission", PRDText: complexFixturePRD})
			if err != nil || view.LatestControlledPreflight == nil {
				t.Fatalf("initial failed admission: %+v %v", view.LatestControlledPreflight, err)
			}
			record, found, err := store.GetLatestClearDevControlledPreflight(ctx, view.Requirement.ID)
			if err != nil || !found || record.Outcome != core.ControlledPreflightFailed {
				t.Fatalf("missing original failure: %+v %v", record, err)
			}
			input := RetryPreflightInput{PreflightID: record.ID}
			switch mode {
			case "missing-id":
				input.PreflightID = ""
			case "old-id":
				input.PreflightID = "not-the-current-observation"
			case "passed", "not-yet", "foreign-role":
				record.ID, record.CheckedAt = ids.New(), clock()
				switch mode {
				case "passed":
					record.Outcome, record.ReasonCode = core.ControlledPreflightPassed, core.ReasonNone
				case "not-yet":
					later := clock().Add(time.Hour)
					record.RetryAt = &later
				default:
					record.RoleBindingID = "unrelated-role"
				}
				if err := store.RecordClearDevControlledPreflight(ctx, record); err != nil {
					t.Fatal(err)
				}
				input.PreflightID = record.ID
			case "cancelled":
				if err := service.CancelRequirement(ctx, view.Requirement.ID, "test cancellation"); err != nil {
					t.Fatal(err)
				}
			}
			wakes := 0
			service.runBackground = func(func()) { wakes++ }
			_, err = service.RetryControlledPreflight(ctx, view.Requirement.ID, input)
			if mode == "current" {
				if err != nil || wakes != 1 {
					t.Fatalf("current retry did not wake its workflow: %v wakes=%d", err, wakes)
				}
				// A repeated HTTP request coalesces with the scheduled runner.
				if _, err := service.RetryControlledPreflight(ctx, view.Requirement.ID, input); err != nil || wakes != 1 {
					t.Fatalf("concurrent retry created another runner: %v wakes=%d", err, wakes)
				}
			} else {
				var problem *apierr.Error
				if !errors.As(err, &problem) || wakes != 0 {
					t.Fatalf("invalid retry woke a workflow: mode=%s err=%v wakes=%d", mode, err, wakes)
				}
			}
			_, spawns := harnessCounts(harness)
			if spawns != 0 {
				t.Fatal("retry admission created a provider session before its scheduled checks")
			}
			latest, _, err := store.GetLatestClearDevControlledPreflight(ctx, view.Requirement.ID)
			if err != nil || latest.ID != record.ID {
				t.Fatalf("retry rewrote the preflight history: %+v %v", latest, err)
			}
		})
	}
}
