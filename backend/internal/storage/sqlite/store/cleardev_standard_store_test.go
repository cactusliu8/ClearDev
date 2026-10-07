package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestStartClearDevStandardFlowIsIdempotentAndRestartDiscoverable(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedClearDevAO(t, store, "ao-standard-restart")
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("requirement-standard-restart", "ao-standard-restart", now)); err != nil {
		t.Fatal(err)
	}
	confirmVersion(t, store, "requirement-standard-restart-v1", now.Add(time.Minute))

	command := core.StartStandardFlowCommand{
		DevelopmentRequirementID:     "requirement-standard-restart",
		StewardRoleBindingID:         "steward-standard-restart",
		StewardSessionIdempotencyKey: "steward-spawn-standard-restart",
		At:                           now.Add(2 * time.Minute),
	}
	first, created, err := store.StartClearDevStandardFlow(ctx, command)
	if err != nil || !created {
		t.Fatalf("first STANDARD start = (%#v, created=%t, err=%v)", first, created, err)
	}
	second, created, err := store.StartClearDevStandardFlow(ctx, command)
	if err != nil || created || !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated STANDARD start = (%#v, created=%t, err=%v), want same persisted binding", second, created, err)
	}

	runnable, err := store.ListClearDevRunnableStandardFlows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runnable, []string{"requirement-standard-restart"}) {
		t.Fatalf("restart scan = %#v, want current approved requirement", runnable)
	}
	flow, ok, err := store.GetClearDevStandardFlow(ctx, command.DevelopmentRequirementID)
	if err != nil || !ok {
		t.Fatalf("read STANDARD facts: ok=%t err=%v", ok, err)
	}
	if flow.RequirementVersionID != "requirement-standard-restart-v1" || len(flow.RoleBindings) != 1 ||
		flow.RoleBindings[0].Status != core.RoleBindingStatusRequested || len(flow.AgentSteps) != 0 {
		t.Fatalf("STANDARD facts after restart-safe start = %#v", flow)
	}
}

func TestStandardFlowRemainsAnchoredWhenNewRequirementVersionIsConfirmed(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedClearDevAO(t, store, "ao-standard-version-anchor")
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("requirement-standard-version-anchor", "ao-standard-version-anchor", now)); err != nil {
		t.Fatal(err)
	}
	confirmVersion(t, store, "requirement-standard-version-anchor-v1", now.Add(time.Minute))
	command := core.StartStandardFlowCommand{
		DevelopmentRequirementID:     "requirement-standard-version-anchor",
		StewardRoleBindingID:         "steward-standard-version-anchor-v1",
		StewardSessionIdempotencyKey: "steward-spawn-standard-version-anchor-v1",
		At:                           now.Add(2 * time.Minute),
	}
	if _, created, err := store.StartClearDevStandardFlow(ctx, command); err != nil || !created {
		t.Fatalf("start v1 STANDARD flow: created=%t err=%v", created, err)
	}

	text := "requirement v2"
	if err := store.CreateClearDevRequirementVersion(ctx, core.RequirementVersion{
		ID: "requirement-standard-version-anchor-v2", DevelopmentRequirementID: command.DevelopmentRequirementID,
		RequirementText: text, SHA256: requirementDigest(text), Status: core.RequirementVersionStatusDraft,
		CreatedAt: now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	confirmVersion(t, store, "requirement-standard-version-anchor-v2", now.Add(4*time.Minute))

	flow, ok, err := store.GetClearDevStandardFlow(ctx, command.DevelopmentRequirementID)
	if err != nil || !ok || flow.RequirementVersionID != "requirement-standard-version-anchor-v1" {
		t.Fatalf("version-anchored flow = %#v, ok=%t err=%v", flow, ok, err)
	}
	runnable, err := store.ListClearDevRunnableStandardFlows(ctx)
	if err != nil || !reflect.DeepEqual(runnable, []string{command.DevelopmentRequirementID}) {
		t.Fatalf("version-replaced runnable scan = %#v err=%v", runnable, err)
	}
	command.StewardRoleBindingID = "steward-standard-version-anchor-v2"
	command.StewardSessionIdempotencyKey = "steward-spawn-standard-version-anchor-v2"
	if _, _, err := store.StartClearDevStandardFlow(ctx, command); err == nil {
		t.Fatal("replacement version started a second STANDARD flow")
	} else {
		var rule *core.RuleError
		if !errors.As(err, &rule) || rule.Code != core.ReasonStandardFlowExists {
			t.Fatalf("second version start error = %T %#v", err, err)
		}
	}
}

func TestBindStandardRolesRejectsSharedAOSession(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedClearDevAO(t, store, "ao-standard-session-exclusion")
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("requirement-standard-session-exclusion", "ao-standard-session-exclusion", now)); err != nil {
		t.Fatal(err)
	}
	versionID := "requirement-standard-session-exclusion-v1"
	confirmVersion(t, store, versionID, now.Add(time.Minute))
	start := core.StartStandardFlowCommand{
		DevelopmentRequirementID:     "requirement-standard-session-exclusion",
		StewardRoleBindingID:         "steward-standard-session-exclusion",
		StewardSessionIdempotencyKey: "steward-spawn-standard-session-exclusion",
		At:                           now.Add(2 * time.Minute),
	}
	if _, _, err := store.StartClearDevStandardFlow(ctx, start); err != nil {
		t.Fatal(err)
	}

	record := sampleRecord("ao-standard-session-exclusion")
	record.Kind = domain.KindWorker
	record.Harness = domain.HarnessCodex
	record.Mode = domain.SessionModeChat
	record.PermissionMode = domain.PermissionModeAuto
	record.CreationIdempotencyKey = "planner-spawn-standard-session-exclusion"
	record.CreationRequestFingerprint = requirementDigest("planner-spawn-standard-session-exclusion")
	record.Metadata.WorkspacePath = "/managed/planner-standard-session-exclusion"
	shared, _, err := store.CreateSessionIdempotent(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindClearDevRoleBinding(ctx, start.StewardRoleBindingID, string(shared.ID), now.Add(3*time.Minute)); err != nil {
		t.Fatalf("bind Steward compatibility session: %v", err)
	}
	planner := core.RoleSessionBinding{
		ID: "planner-standard-session-exclusion", DevelopmentRequirementID: start.DevelopmentRequirementID,
		RequirementVersionID: versionID, Role: core.StandardRoleEngineeringPlanner,
		SessionCreationIdempotencyKey: record.CreationIdempotencyKey,
		Status:                        core.RoleBindingStatusRequested, RequestedAt: now.Add(4 * time.Minute),
	}
	if _, _, err := store.CreateClearDevRoleBinding(ctx, core.CreateRoleBindingCommand{Binding: planner}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindClearDevRoleBinding(ctx, planner.ID, string(shared.ID), now.Add(5*time.Minute)); err == nil {
		t.Fatal("same AO session bound to Steward and Planner")
	} else {
		var rule *core.RuleError
		if !errors.As(err, &rule) || rule.Code != core.ReasonSessionMismatch {
			t.Fatalf("shared-session bind error = %T %#v", err, err)
		}
	}
	flow, _, err := store.GetClearDevStandardFlow(ctx, start.DevelopmentRequirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range flow.RoleBindings {
		if binding.ID == planner.ID && (binding.Status != core.RoleBindingStatusRequested || binding.AOSessionID != "") {
			t.Fatalf("rejected Planner binding mutated = %#v", binding)
		}
	}
}
