package store_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sqlitedb "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestOpenCodeChoiceModelRepairAndAdmissionAreDurable(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	project := domain.ProjectRecord{ID: "ao-product", Path: "/tmp/ao-product", RegisteredAt: now,
		Config: domain.ProjectConfig{Env: map[string]string{"PROJECT_SETTING": "preserved"}}}
	if err := store.UpsertProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	command := productStoreCommand("opencode-product", now)
	command.Goal.RequestedExecution = &domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "local/typo"}
	id, created, err := store.CreateClearDevProductGoal(ctx, command)
	if err != nil || !created || id != command.Goal.ID {
		t.Fatalf("create: id=%s created=%v err=%v", id, created, err)
	}
	project, found, err := store.GetProject(ctx, project.ID)
	if err != nil || !found || !reflect.DeepEqual(project.Config.ClearDev, command.Goal.RequestedExecution) || project.Config.Env["PROJECT_SETTING"] != "preserved" {
		t.Fatalf("choice was not atomically preserved: %+v, %v", project, err)
	}
	toolLocked, modelLocked, err := store.GetClearDevExecutionChoiceLocks(ctx, project.ID)
	if err != nil || !toolLocked || modelLocked {
		t.Fatalf("before admission locks: %v %v %v", toolLocked, modelLocked, err)
	}
	preflight := core.ControlledPreflight{
		ID: "unknown-model", DevelopmentRequirementID: id, RoleBindingID: command.Container.StewardRoleBinding.ID,
		AOProjectID: project.ID, RequestedModel: "local/typo", Provider: "opencode",
		CatalogJSON: `["local/model"]`, CatalogSHA256: requirementDigest(`["local/model"]`),
		Outcome: core.ControlledPreflightFailed, ReasonCode: core.ReasonModelNotAvailable,
		Retryable: true, CheckedAt: now,
		Evidence: &domain.ControlledPreflightEvidence{Scope: "LOCAL_CONFIGURATION", Installation: "AVAILABLE", Configuration: "VALID", Model: "UNAVAILABLE", Authentication: "UNKNOWN", Service: "UNKNOWN", Quota: "UNKNOWN"},
	}
	if err := store.RecordClearDevControlledPreflight(ctx, preflight); err != nil {
		t.Fatal(err)
	}
	// Correcting a model before any admission does not switch the project tool.
	project.Config.ClearDev = &domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "local/model"}
	if err := store.UpsertProject(ctx, project); err != nil {
		t.Fatalf("first failed preflight must be repairable: %v", err)
	}
	stale := preflight
	stale.ID, stale.Outcome, stale.ReasonCode = "stale-inspection", core.ControlledPreflightPassed, core.ReasonNone
	stale.ResolvedModel, stale.CheckedAt = "local/typo", now.Add(time.Second)
	if err := store.RecordClearDevControlledPreflight(ctx, stale); !errors.Is(err, core.ErrControlledExecutionChoiceChanged) {
		t.Fatalf("stale admission must fail after recording its fact: %v", err)
	}
	latest, found, err := store.GetLatestClearDevControlledPreflight(ctx, id)
	if err != nil || !found || latest.ID != stale.ID || latest.Outcome != core.ControlledPreflightFailed || latest.ReasonCode != "EXECUTION_CHOICE_CHANGED" || !latest.Retryable {
		t.Fatalf("stale result was lost or falsely admitted: %+v %v", latest, err)
	}
	_, modelLocked, err = store.GetClearDevExecutionChoiceLocks(ctx, project.ID)
	if err != nil || modelLocked {
		t.Fatalf("failed inspection froze a repairable model: %v %v", modelLocked, err)
	}
	admitted := stale
	admitted.ID, admitted.RequestedModel, admitted.ResolvedModel = "admitted", "local/model", "local/model"
	admitted.CheckedAt = now.Add(2 * time.Second)
	admitted.Evidence = &domain.ControlledPreflightEvidence{Scope: "LOCAL_CONFIGURATION", Installation: "AVAILABLE", Configuration: "VALID", Model: "LISTED", Authentication: "UNKNOWN", Service: "UNKNOWN", Quota: "UNKNOWN"}
	if err := store.RecordClearDevControlledPreflight(ctx, admitted); err != nil {
		t.Fatal(err)
	}
	toolLocked, modelLocked, err = store.GetClearDevExecutionChoiceLocks(ctx, project.ID)
	if err != nil || !toolLocked || !modelLocked {
		t.Fatalf("admitted choice not locked: %v %v %v", toolLocked, modelLocked, err)
	}
	for _, rejected := range []*domain.ClearDevExecutionConfig{
		nil,
		{Harness: domain.HarnessCodex, Model: "gpt-test"},
		{Harness: domain.HarnessOpenCode, Model: "local/another"},
	} {
		changed := project
		changed.Config.ClearDev = rejected
		if err := store.UpsertProject(ctx, changed); !errors.Is(err, domain.ErrClearDevExecutionFrozen) {
			t.Fatalf("admitted project allowed choice change to %+v: %v", rejected, err)
		}
	}
	project.DisplayName = "Renamed"
	if _, err := store.UpdateProjectSettings(ctx, project.ID, project.DisplayName, project.Config); err != nil {
		t.Fatalf("unrelated project settings must remain editable: %v", err)
	}
	// A retry is checked against the original request, even after model repair.
	if replayID, replayCreated, err := store.CreateClearDevProductGoal(ctx, command); err != nil || replayCreated || replayID != id {
		t.Fatalf("original request replay changed: %s %v %v", replayID, replayCreated, err)
	}
	wrong := command
	wrong.Goal.RequestedExecution = &domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "local/model"}
	if _, _, err := store.CreateClearDevProductGoal(ctx, wrong); err == nil {
		t.Fatal("reused request id accepted a rewritten model selection")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlitedb.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, found, err := reopened.GetClearDevProduct(ctx, id)
	if err != nil || !found || len(loaded.Discussions) != 1 || !reflect.DeepEqual(loaded.Goal.RequestedExecution, command.Goal.RequestedExecution) {
		t.Fatalf("restart rewrote original request/history: %+v %v", loaded, err)
	}
	latest, found, err = reopened.GetLatestClearDevControlledPreflight(ctx, id)
	if err != nil || !found || latest.ID != admitted.ID || !reflect.DeepEqual(latest.Evidence, admitted.Evidence) {
		t.Fatalf("restart lost explicit unknown capability facts: %+v %v", latest, err)
	}
	raw := openClearDevRawDB(t, dir)
	defer raw.Close()
	var count int
	if err := raw.QueryRow(`SELECT count(*) FROM cleardev_controlled_preflights WHERE development_project_id=?`, id).Scan(&count); err != nil || count != 3 {
		t.Fatalf("old failed evidence was rewritten: count=%d err=%v", count, err)
	}
}

func TestConcurrentFirstProductCannotMixExecutionTools(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "ao-product", Path: "/tmp/ao-product", RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	commands := []core.CreateProductGoalCommand{productStoreCommand("codex-first", now), productStoreCommand("opencode-first", now)}
	commands[0].Goal.RequestID = "codex-request"
	commands[0].Goal.RequestedExecution = &domain.ClearDevExecutionConfig{Harness: domain.HarnessCodex, Model: "gpt-test"}
	commands[1].Goal.RequestID = "opencode-request"
	commands[1].Goal.RequestedExecution = &domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "local/model"}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, len(commands))
	for i := range commands {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, errs[i] = store.CreateClearDevProductGoal(ctx, commands[i])
		}(i)
	}
	close(start)
	wg.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatal("both first tool choices were accepted")
			}
			winner = i
		} else if !errors.Is(err, domain.ErrClearDevExecutionFrozen) {
			t.Fatalf("unexpected conflict: %v", err)
		}
	}
	if winner < 0 {
		t.Fatal("no first product was created")
	}
	project, found, err := store.GetProject(ctx, "ao-product")
	if err != nil || !found || !reflect.DeepEqual(project.Config.ClearDev, commands[winner].Goal.RequestedExecution) {
		t.Fatalf("project choice does not match the admitted product: %+v %v", project, err)
	}
	ids, err := store.ListClearDevProductGoalIDs(ctx, "ao-product")
	if err != nil || len(ids) != 1 || ids[0] != commands[winner].Goal.ID {
		t.Fatalf("losing registration was not rolled back: %v %v", ids, err)
	}
	if _, found, err := store.GetClearDevRequirement(ctx, commands[1-winner].Goal.ID); err != nil || found {
		t.Fatalf("loser left a controlled requirement: found=%v err=%v", found, err)
	}
}

func TestLegacyControlledProjectNeverInheritsAOOpenCodeOverride(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	project := domain.ProjectRecord{ID: "ao-product", Path: "/tmp/ao-product", RegisteredAt: now,
		Config: domain.ProjectConfig{Worker: domain.RoleOverride{Harness: domain.HarnessOpenCode}}}
	if err := store.UpsertProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateClearDevProductGoal(ctx, productStoreCommand("legacy", now)); err != nil {
		t.Fatal(err)
	}
	project, _, err := store.GetProject(ctx, project.ID)
	if err != nil || project.Config.ClearDev != nil || project.Config.ClearDevHarness() != domain.HarnessCodex {
		t.Fatalf("legacy project tool changed: %+v %v", project, err)
	}
	project.Config.ClearDev = &domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "local/model"}
	if err := store.UpsertProject(ctx, project); !errors.Is(err, domain.ErrClearDevExecutionFrozen) {
		t.Fatalf("legacy controlled project was switched: %v", err)
	}
}

func TestNativeCodexEffortsPersistAndRemainFrozenAfterAdmission(t *testing.T) {
	for _, effort := range []string{"max", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			ctx, dir := context.Background(), t.TempDir()
			store := sqlitetest.MustOpenAt(t, dir)
			now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
			project := domain.ProjectRecord{ID: "ao-product", Path: "/tmp/native-product", RegisteredAt: now, Config: domain.ProjectConfig{Env: map[string]string{"PROJECT_SETTING": "preserved"}}}
			if err := store.UpsertProject(ctx, project); err != nil {
				t.Fatal(err)
			}
			choice := domain.ClearDevExecutionConfig{Harness: domain.HarnessCodex, Model: "native-model", Effort: effort}
			if err := store.SetClearDevProjectExecution(ctx, project.ID, choice); err != nil {
				t.Fatalf("advertised native effort could not be saved: %v", err)
			}
			command := productStoreCommand("native-product", now)
			command.Goal.RequestedExecution = &choice
			if _, _, err := store.CreateClearDevProductGoal(ctx, command); err != nil {
				t.Fatal(err)
			}
			catalog := `["native-model"]`
			preflight := core.ControlledPreflight{
				ID: "native-admitted", DevelopmentRequirementID: command.Goal.ID, RoleBindingID: command.Container.StewardRoleBinding.ID,
				AOProjectID: project.ID, RequestedModel: choice.Model, ResolvedModel: choice.Model, Provider: "codex",
				CatalogJSON: catalog, CatalogSHA256: requirementDigest(catalog), Outcome: core.ControlledPreflightPassed, CheckedAt: now,
			}
			if err := store.RecordClearDevControlledPreflight(ctx, preflight); err != nil {
				t.Fatal(err)
			}
			changed := choice
			changed.Effort = "high"
			if err := store.SetClearDevProjectExecution(ctx, project.ID, changed); !errors.Is(err, domain.ErrClearDevExecutionFrozen) {
				t.Fatalf("admitted native effort was not frozen: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlitedb.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			actual, found, err := reopened.GetProject(ctx, project.ID)
			if err != nil || !found || actual.Config.ClearDev == nil || *actual.Config.ClearDev != choice || actual.Config.Env["PROJECT_SETTING"] != "preserved" {
				t.Fatalf("native selection or project environment changed after restart: %+v %v", actual.Config.ClearDev, err)
			}
			toolLocked, modelLocked, err := reopened.GetClearDevExecutionChoiceLocks(ctx, project.ID)
			if err != nil || !toolLocked || !modelLocked {
				t.Fatalf("native selection lost admission locks: %v %v %v", toolLocked, modelLocked, err)
			}
		})
	}
}
