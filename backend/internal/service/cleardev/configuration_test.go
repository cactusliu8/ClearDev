package cleardev

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestBenchmarkManifestRequiresBindingStoreAtControlledStartup(t *testing.T) {
	store, harness, ids, clock, _ := newStandardIntegrationFixture(t, false)
	deps := Deps{
		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store,
		DirectionFacts: store, HumanDecisions: store, ProgressExplanations: store,
		ParseCorrections: store, AgentAttempts: store, ControlledPreflights: store,
		ControlledPreflightChecker: alwaysPassControlledPreflight{}, AO: store,
		Workspace: gitWorkspaceObserver{}, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		RecoverAgentSession: func(context.Context, domain.SessionID) error { return nil },
		RunBackground:       func(func()) {}, NewID: ids.New, Clock: clock,
		BenchmarkManifest: &core.BenchmarkBackendManifest{},
	}
	if err := New(deps).ValidateControlledConfiguration(); err == nil || !strings.Contains(err.Error(), "BenchmarkFacts") {
		t.Fatalf("benchmark manifest without binding store accepted: %v", err)
	}
	deps.BenchmarkFacts = store
	if err := New(deps).ValidateControlledConfiguration(); err != nil {
		t.Fatalf("benchmark binding store was not accepted: %v", err)
	}
}

func TestNewDoesNotInventControlledDependencies(t *testing.T) {
	s := New(Deps{})
	if s.corrections != nil || s.attempts != nil || s.preflights != nil || s.preflightChecker != nil {
		t.Fatal("missing controlled dependencies were silently replaced by memory evidence or passing preflight")
	}
}

func TestControlledConfigurationRejectsEachMissingDependencyBeforeEffects(t *testing.T) {
	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	deps := Deps{
		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store,
		DirectionFacts: store, HumanDecisions: store, ProgressExplanations: store,
		ParseCorrections: store, AgentAttempts: store, ControlledPreflights: store,
		ControlledPreflightChecker: alwaysPassControlledPreflight{}, AO: store,
		Workspace: gitWorkspaceObserver{}, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		RecoverAgentSession: func(context.Context, domain.SessionID) error { t.Fatal("recovery before configuration"); return nil },
		RunBackground:       func(func()) { t.Fatal("background work before configuration") },
		NewID:               ids.New, Clock: clock,
	}
	if err := New(deps).ValidateControlledConfiguration(); err != nil {
		t.Fatal(err)
	}
	fields := []string{"Facts", "StandardFacts", "ComplexFacts", "ComplexExecutionFacts", "DirectionFacts", "HumanDecisions", "ProgressExplanations", "ParseCorrections", "AgentAttempts", "ControlledPreflights", "ControlledPreflightChecker", "AO", "Workspace", "Sessions", "RecoverAgentSession", "Chat", "Inspector", "Checks"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			broken := deps
			v := reflect.ValueOf(&broken).Elem().FieldByName(field)
			v.Set(reflect.Zero(v.Type()))
			broken.NewID = func() string { t.Fatal("identifier allocated before configuration"); return "" }
			service := New(broken)
			ctx := context.Background()
			checks := []func() error{
				service.ValidateControlledConfiguration,
				func() error {
					_, err := service.CreateComplexRequirement(ctx, CreateComplexRequirementInput{})
					return err
				},
				func() error {
					_, err := service.SubmitComplexClarifications(ctx, requirementID, SubmitComplexClarificationsInput{})
					return err
				},
				func() error { _, err := service.StartStandardFlow(ctx, requirementID); return err },
				func() error { _, err := service.StartComplexStandardExecution(ctx, requirementID); return err },
				func() error {
					_, err := service.ProposeDirectionIntent(ctx, requirementID, ProposeDirectionIntentInput{})
					return err
				},
				func() error { _, err := service.RequestProgressExplanation(ctx, requirementID); return err },
				func() error { return service.ResumeStandardFlows(ctx) },
				func() error { return service.ResumeComplexFlows(ctx) },
				func() error { return service.ResumeDirectionChanges(ctx) },
				func() error { return service.ResumeComplexStandardExecutions(ctx) },
				func() error { return service.ResumeProgressExplanations(ctx) },
			}
			for i, check := range checks {
				if err := check(); err == nil || !strings.Contains(fmt.Sprint(err), field) {
					t.Fatalf("entry %d: %v", i, err)
				}
			}
			// Pure reads retain their original requirements, not the controlled set.
			if field != "Facts" {
				if _, err := service.GetRequirement(ctx, requirementID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	var absent *standardAgentHarness
	deps.Chat = absent
	if err := New(deps).ValidateControlledConfiguration(); err == nil || !strings.Contains(err.Error(), "Chat") {
		t.Fatalf("typed nil accepted: %v", err)
	}
}
