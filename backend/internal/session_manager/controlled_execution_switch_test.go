package sessionmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestExplicitControlledProjectRejectsAgentSwitchBeforeAdmission(t *testing.T) {
	manager, store, _ := newSwitchTestManager(t, &fakeRestartRuntime{fakeRuntime: &fakeRuntime{}})
	project := store.projects["proj"]
	project.Config.ClearDev = &domain.ClearDevExecutionConfig{Harness: domain.HarnessCodex, Model: "fixed-model"}
	store.projects["proj"] = project
	record := store.sessions["proj-1"]
	record.Harness = domain.HarnessCodex
	record.CreationIdempotencyKey = "cleardev:bound-role"
	record.Metadata.Model = "fixed-model"
	store.sessions["proj-1"] = record
	_, admitted, err := manager.admitAgentSwitch(context.Background(), "proj-1", SwitchAgentConfig{TargetHarness: domain.HarnessClaudeCode, IdempotencyKey: "refused-switch"})
	if !errors.Is(err, domain.ErrClearDevExecutionFrozen) || admitted != nil {
		t.Fatalf("controlled role reached switch admission: %v %+v", err, admitted)
	}
	if len(store.switches) != 0 || len(store.native) != 0 {
		t.Fatal("refused switch created durable switch or native-session records")
	}
}

func TestControlledProjectSwitchFollowsHumanAuthorizedEngineChange(t *testing.T) {
	manager, store, _ := newSwitchTestManager(t, &fakeRestartRuntime{fakeRuntime: &fakeRuntime{}})
	project := store.projects["proj"]
	project.Config.ClearDev = &domain.ClearDevExecutionConfig{Harness: domain.HarnessClaudeCode, Model: "fixed-model"}
	store.projects["proj"] = project
	record := store.sessions["proj-1"]
	record.Harness = domain.HarnessClaudeCode
	record.CreationIdempotencyKey = "cleardev:bound-role"
	record.Metadata.Model = "fixed-model"
	store.sessions["proj-1"] = record
	// A grant for another engine must not authorize this switch.
	store.engineChangeAuthorized["proj|codex|other-model"] = true
	_, admitted, err := manager.admitAgentSwitch(context.Background(), "proj-1", SwitchAgentConfig{TargetHarness: domain.HarnessCodex, Model: "target-model", IdempotencyKey: "refused-switch"})
	if !errors.Is(err, domain.ErrClearDevExecutionFrozen) || admitted != nil {
		t.Fatalf("unauthorized engine change reached switch admission: %v %+v", err, admitted)
	}
	// The exact human-authorized target is admitted.
	store.engineChangeAuthorized["proj|codex|target-model"] = true
	result, admitted, err := manager.admitAgentSwitch(context.Background(), "proj-1", SwitchAgentConfig{TargetHarness: domain.HarnessCodex, Model: "target-model", IdempotencyKey: "authorized-switch"})
	if err != nil || admitted == nil {
		t.Fatalf("human-authorized engine change was refused: %v %+v", err, result)
	}
	if result.FromHarness != domain.HarnessClaudeCode || result.TargetHarness != domain.HarnessCodex {
		t.Fatalf("authorized switch has wrong harness pair: %+v", result)
	}
}
