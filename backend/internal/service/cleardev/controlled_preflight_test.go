package cleardev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type scriptedControlledPreflight struct {
	mu    sync.Mutex
	calls int
	fn    func(requested string) (ports.ChatControlledPreflight, error)
}

type recordingControlledTurnSettings struct {
	StandardChatService
	boundSession domain.SessionID
	settings     domain.ConversationSettings
}

func (r *recordingControlledTurnSettings) SetTurnSettings(_ context.Context, session domain.SessionID, settings domain.ConversationSettings) (domain.ConversationSettings, error) {
	r.boundSession, r.settings = session, settings
	return settings, nil
}

func TestControlledCodexRoleBindsConfiguredEffortBeforeFirstTurn(t *testing.T) {
	store, harness, _, clock, _ := newComplexFixture(t)
	if err := store.UpsertProject(context.Background(), domain.ProjectRecord{
		ID: "s04-project", Path: "/tmp/s04-project", Kind: domain.ProjectKindSingleRepo, RegisteredAt: clock(),
		Config: domain.ProjectConfig{AgentConfig: domain.AgentConfig{Model: "gpt-5.6-terra", Mode: "medium"},
			Worker: domain.RoleOverride{AgentConfig: domain.AgentConfig{Mode: "high"}}},
	}); err != nil {
		t.Fatal(err)
	}
	chat := &recordingControlledTurnSettings{StandardChatService: harness}
	s := &Service{ao: store, sessions: harness, chat: chat}
	session, err := s.spawnResolvedChatSession(context.Background(), ports.SpawnConfig{
		ProjectID: "s04-project", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		RequestedMode: domain.SessionModeChat, CreationIdempotencyKey: "effort-binding-test",
		AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto},
	}, "gpt-5.6-terra")
	if err != nil || session.ID == "" || chat.boundSession != session.ID || chat.settings.Model != "gpt-5.6-terra" ||
		chat.settings.ReasoningEffort != "high" || chat.settings.ApprovalMode != domain.PermissionModeAuto {
		t.Fatalf("controlled role effort was not bound before dispatch: session=%+v settings=%+v err=%v", session.ID, chat.settings, err)
	}
}

func (c *scriptedControlledPreflight) CheckControlledPreflight(_ context.Context, _ domain.AgentHarness, requested string) (ports.ChatControlledPreflight, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.fn == nil {
		return ports.ChatControlledPreflight{}, ports.ErrChatProviderUnavailable
	}
	return c.fn(requested)
}

func (c *scriptedControlledPreflight) ncalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func livePreflightCatalog(ids []string, resolved string) ports.ChatControlledPreflight {
	raw, _ := json.Marshal(ids)
	sum := sha256.Sum256(raw)
	return ports.ChatControlledPreflight{
		ResolvedModel: resolved,
		Provider:      string(domain.HarnessCodex),
		Models:        []ports.ChatModel{},
		CatalogJSON:   string(raw),
		CatalogSHA256: hex.EncodeToString(sum[:]),
	}
}

func setProjectModel(t *testing.T, store interface {
	UpsertProject(context.Context, domain.ProjectRecord) error
}, model string, clock func() time.Time) {
	t.Helper()
	if err := store.UpsertProject(context.Background(), domain.ProjectRecord{
		ID: "s04-project", Path: "/tmp/s04-project", Kind: domain.ProjectKindSingleRepo, RegisteredAt: clock(),
		Config: domain.ProjectConfig{AgentConfig: domain.AgentConfig{Model: model}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestControlledPreflightRejectsUnknownModelBeforeSpawn(t *testing.T) {
	checker := &scriptedControlledPreflight{fn: func(requested string) (ports.ChatControlledPreflight, error) {
		result := livePreflightCatalog([]string{"gpt-5.4", "gpt-5.3-codex"}, "")
		result.RequestedModel = requested
		result.ErrorSummary = "requested model is not in the provider catalog"
		return result, ports.ErrChatModelNotAvailable
	}}
	store, harness, ids, clock, _ := newComplexFixture(t)
	setProjectModel(t, store, "gpt-5.6-sol", clock)
	service := New(Deps{
		ParseCorrections:     store,
		AgentAttempts:        store,
		ProgressExplanations: store,
		Workspace:            gitWorkspaceObserver{},
		RecoverAgentSession:  func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },

		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store,
		AO: store, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		ControlledPreflights: store, ControlledPreflightChecker: checker,
		Human: allowStandardHuman{}, BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})
	view, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "s04-project", Name: "复杂邮件名单", PRDText: complexFixturePRD,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, spawns := harnessCounts(harness)
	if spawns != 0 {
		t.Fatalf("spawned %d sessions before the model was available", spawns)
	}
	if view.ComplexPlanning == nil || len(view.ComplexPlanning.RoleBindings) != 1 || view.ComplexPlanning.RoleBindings[0].Status != core.RoleBindingStatusRequested {
		t.Fatalf("binding = %#v", view.ComplexPlanning)
	}
	if len(view.ComplexPlanning.AgentSteps) != 0 {
		t.Fatalf("agent steps were created: %#v", view.ComplexPlanning.AgentSteps)
	}
	if view.LatestControlledPreflight == nil || view.LatestControlledPreflight.Outcome != core.ControlledPreflightFailed ||
		view.LatestControlledPreflight.ReasonCode != core.ReasonModelNotAvailable ||
		view.LatestControlledPreflight.RequestedModel != "gpt-5.6-sol" ||
		view.LatestControlledPreflight.CatalogSHA256 == "" ||
		len(view.LatestControlledPreflight.CatalogModelIDs) != 2 {
		t.Fatalf("preflight = %#v", view.LatestControlledPreflight)
	}
	if view.TrustedProgress.Attention != core.OverallAttentionBlocked || view.TrustedProgress.ReasonCode != core.ReasonModelNotAvailable {
		t.Fatalf("trusted progress = %+v", view.TrustedProgress)
	}
}

func TestControlledPreflightLoginQuotaAndProviderReasons(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		reason core.ReasonCode
		retry  *time.Time
	}{
		{name: "login", err: ports.ErrChatAuthRequired, reason: core.ReasonLoginRequired},
		{name: "quota", err: ports.ErrChatQuotaExhausted, reason: core.ReasonQuotaExhausted, retry: timePtr(time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC))},
		{name: "rate", err: ports.ErrChatRateLimited, reason: core.ReasonRateLimited, retry: timePtr(time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC))},
		{name: "provider", err: ports.ErrChatProviderUnavailable, reason: core.ReasonProviderUnavailable},
		{name: "driver", err: ports.ErrChatDriverIncompatible, reason: core.ReasonDriverIncompatible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checker := &scriptedControlledPreflight{fn: func(requested string) (ports.ChatControlledPreflight, error) {
				result := livePreflightCatalog([]string{"gpt-5.4"}, "")
				result.RequestedModel = requested
				result.RetryAt = tc.retry
				result.ProviderErrorCode = "provider-code"
				return result, tc.err
			}}
			store, harness, ids, clock, _ := newComplexFixture(t)
			service := New(Deps{
				ParseCorrections:     store,
				AgentAttempts:        store,
				ProgressExplanations: store,
				Workspace:            gitWorkspaceObserver{},
				RecoverAgentSession:  func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },

				Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store,
				AO: store, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
				ControlledPreflights: store, ControlledPreflightChecker: checker,
				Human: allowStandardHuman{}, BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
				StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
			})
			view, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
				AOProjectID: "s04-project", Name: "复杂邮件名单", PRDText: complexFixturePRD,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, spawns := harnessCounts(harness)
			if spawns != 0 {
				t.Fatalf("spawned %d sessions", spawns)
			}
			if view.LatestControlledPreflight == nil || view.LatestControlledPreflight.ReasonCode != tc.reason {
				t.Fatalf("preflight = %#v", view.LatestControlledPreflight)
			}
			if tc.retry != nil {
				if view.LatestControlledPreflight.RetryAt == nil || !view.LatestControlledPreflight.RetryAt.Equal(*tc.retry) {
					t.Fatalf("retryAt = %v want %v", view.LatestControlledPreflight.RetryAt, tc.retry)
				}
			}
			if view.ComplexPlanning.RoleBindings[0].Status != core.RoleBindingStatusRequested {
				t.Fatalf("binding status = %s", view.ComplexPlanning.RoleBindings[0].Status)
			}
		})
	}
}

func TestStandardRateLimitedPreflightSurfacesBlockedAndRemainsResumable(t *testing.T) {
	var available atomic.Bool
	retryAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	checker := &scriptedControlledPreflight{fn: func(requested string) (ports.ChatControlledPreflight, error) {
		result := livePreflightCatalog([]string{"gpt-5.4"}, requested)
		result.RequestedModel = requested
		if available.Load() {
			return result, nil
		}
		result.RetryAt = &retryAt
		return result, ports.ErrChatRateLimited
	}}

	store, harness, ids, clock, requirementID := newStandardIntegrationFixture(t, false)
	service := standardTestService(store, store, harness, ids, clock, context.Background())
	service.preflightChecker = checker

	view, err := service.StartStandardFlow(context.Background(), requirementID)
	if err != nil {
		t.Fatal(err)
	}
	harness.mu.Lock()
	spawns := len(harness.spawnConfigs)
	harness.mu.Unlock()
	if spawns != 0 {
		t.Fatalf("rate-limited STANDARD preflight spawned %d sessions", spawns)
	}
	if view.StandardFlow == nil || len(view.StandardFlow.RoleBindings) != 1 ||
		view.StandardFlow.RoleBindings[0].Status != core.RoleBindingStatusRequested ||
		view.StandardFlow.RoleBindings[0].AOSessionID != "" || len(view.StandardFlow.AgentSteps) != 0 {
		t.Fatalf("rate-limited STANDARD flow advanced past the resumable Steward gate: %#v", view.StandardFlow)
	}
	if view.LatestControlledPreflight == nil ||
		view.LatestControlledPreflight.Outcome != core.ControlledPreflightFailed ||
		view.LatestControlledPreflight.ReasonCode != core.ReasonRateLimited {
		t.Fatalf("rate-limited STANDARD preflight = %#v", view.LatestControlledPreflight)
	}
	if view.TrustedProgress.Phase != core.TrustedPhaseBlocked ||
		view.TrustedProgress.Attention != core.OverallAttentionBlocked ||
		view.TrustedProgress.ReasonCode != core.ReasonRateLimited ||
		len(view.TrustedProgress.Blockers) != 1 {
		t.Fatalf("rate-limited STANDARD trusted progress = %#v", view.TrustedProgress)
	}
	if len(view.TrustedProgress.ControlledWork) != 1 ||
		view.TrustedProgress.ControlledWork[0].State != "WAITING_RETRY" ||
		view.TrustedProgress.ControlledWork[0].NextOwner.Role != core.TrustedOwnerControlPlane {
		t.Fatalf("rate-limited STANDARD controlled work = %#v", view.TrustedProgress.ControlledWork)
	}

	available.Store(true)
	if err := service.ResumeStandardFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCompletedStandardFlow(t, service, requirementID)
}

func TestControlledPreflightRechecksAfterConfigFix(t *testing.T) {
	var pass atomic.Bool
	checker := &scriptedControlledPreflight{fn: func(requested string) (ports.ChatControlledPreflight, error) {
		if !pass.Load() {
			result := livePreflightCatalog([]string{"gpt-5.4"}, "")
			result.RequestedModel = requested
			return result, ports.ErrChatModelNotAvailable
		}
		result := livePreflightCatalog([]string{"gpt-5.4"}, requested)
		result.RequestedModel = requested
		return result, nil
	}}
	store, harness, ids, clock, _ := newComplexFixture(t)
	setProjectModel(t, store, "gpt-5.6-sol", clock)
	service := New(Deps{
		ParseCorrections:     store,
		AgentAttempts:        store,
		ProgressExplanations: store,
		Workspace:            gitWorkspaceObserver{},
		RecoverAgentSession:  func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },

		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store,
		AO: store, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		ControlledPreflights: store, ControlledPreflightChecker: checker,
		Human: allowStandardHuman{}, BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})
	view, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "s04-project", Name: "复杂邮件名单", PRDText: complexFixturePRD,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, spawns := harnessCounts(harness)
	if spawns != 0 {
		t.Fatal("spawned before the model existed")
	}
	setProjectModel(t, store, "gpt-5.4", clock)
	pass.Store(true)
	if err := service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	_, spawns = harnessCounts(harness)
	if spawns != 1 {
		t.Fatalf("spawns = %d, want 1", spawns)
	}
	if view.LatestControlledPreflight == nil || view.LatestControlledPreflight.Outcome != core.ControlledPreflightPassed ||
		view.LatestControlledPreflight.ResolvedModel != "gpt-5.4" {
		t.Fatalf("passed preflight = %#v", view.LatestControlledPreflight)
	}
	if view.ComplexPlanning == nil || view.ComplexPlanning.RoleBindings[0].Status != core.RoleBindingStatusBound {
		t.Fatalf("binding after fix = %#v", view.ComplexPlanning)
	}
	harness.mu.Lock()
	model := harness.spawnConfigs[0].AgentConfig.Model
	harness.mu.Unlock()
	if model != "gpt-5.4" {
		t.Fatalf("spawned model = %q", model)
	}
}

func TestControlledPreflightQuotaRetryAtStillRechecksOnResume(t *testing.T) {
	retryAt := time.Date(2026, 8, 25, 18, 0, 0, 0, time.UTC)
	var pass atomic.Bool
	checker := &scriptedControlledPreflight{fn: func(requested string) (ports.ChatControlledPreflight, error) {
		if !pass.Load() {
			result := livePreflightCatalog([]string{"gpt-5.4"}, "")
			result.RequestedModel = requested
			result.RetryAt = &retryAt
			return result, ports.ErrChatQuotaExhausted
		}
		result := livePreflightCatalog([]string{"gpt-5.4"}, requested)
		result.RequestedModel = requested
		return result, nil
	}}
	store, harness, ids, clock, _ := newComplexFixture(t)
	service := New(Deps{
		ParseCorrections:     store,
		AgentAttempts:        store,
		ProgressExplanations: store,
		Workspace:            gitWorkspaceObserver{},
		RecoverAgentSession:  func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },

		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store,
		AO: store, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		ControlledPreflights: store, ControlledPreflightChecker: checker,
		Human: allowStandardHuman{}, BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})
	view, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "s04-project", Name: "复杂邮件名单", PRDText: complexFixturePRD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.LatestControlledPreflight == nil || view.LatestControlledPreflight.ReasonCode != core.ReasonQuotaExhausted ||
		view.LatestControlledPreflight.RetryAt == nil || !view.LatestControlledPreflight.RetryAt.Equal(retryAt) {
		t.Fatalf("quota preflight = %#v", view.LatestControlledPreflight)
	}
	firstCalls := checker.ncalls()
	pass.Store(true)
	if err := service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if checker.ncalls() <= firstCalls {
		t.Fatalf("resume skipped live inspect: calls %d -> %d", firstCalls, checker.ncalls())
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	_, spawns := harnessCounts(harness)
	if spawns != 1 {
		t.Fatalf("spawns = %d after quota was repaired", spawns)
	}
	if view.LatestControlledPreflight == nil || view.LatestControlledPreflight.Outcome != core.ControlledPreflightPassed {
		t.Fatalf("recheck preflight = %#v", view.LatestControlledPreflight)
	}
}

func TestControlledPreflightSurvivesRestartAndConcurrentResume(t *testing.T) {
	checker := &scriptedControlledPreflight{fn: func(requested string) (ports.ChatControlledPreflight, error) {
		result := livePreflightCatalog([]string{"gpt-5.4"}, "")
		result.RequestedModel = requested
		return result, ports.ErrChatModelNotAvailable
	}}
	store, harness, ids, clock, _ := newComplexFixture(t)
	setProjectModel(t, store, "gpt-5.6-sol", clock)
	deps := Deps{
		ProgressExplanations: store, ParseCorrections: store, AgentAttempts: store, Workspace: gitWorkspaceObserver{},
		RecoverAgentSession: func(context.Context, domain.SessionID) error { return errors.New("unexpected recovery") },
		Facts:               store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store,
		AO: store, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		ControlledPreflights: store, ControlledPreflightChecker: checker,
		Human: allowStandardHuman{}, BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	}
	first := New(deps)
	view, err := first.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "s04-project", Name: "复杂邮件名单", PRDText: complexFixturePRD,
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(deps)
	again := mustGetComplex(t, restarted, view.Requirement.ID)
	if again.LatestControlledPreflight == nil || again.LatestControlledPreflight.ReasonCode != core.ReasonModelNotAvailable ||
		again.LatestControlledPreflight.CatalogSHA256 == "" {
		t.Fatalf("restarted preflight = %#v", again.LatestControlledPreflight)
	}

	checker.mu.Lock()
	checker.fn = func(requested string) (ports.ChatControlledPreflight, error) {
		result := livePreflightCatalog([]string{"gpt-5.4"}, "gpt-5.4")
		result.RequestedModel = requested
		return result, nil
	}
	checker.mu.Unlock()
	setProjectModel(t, store, "gpt-5.4", clock)
	var wg sync.WaitGroup
	restarted.runBackground = func(run func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run()
		}()
	}
	for i := 0; i < 2; i++ {
		if err := restarted.ResumeComplexFlows(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	_, spawns := harnessCounts(harness)
	if spawns != 1 {
		t.Fatalf("concurrent resume spawned %d sessions", spawns)
	}
}

func TestControlledPreflightDoesNotCopyLocalizedErrorText(t *testing.T) {
	checker := &scriptedControlledPreflight{fn: func(string) (ports.ChatControlledPreflight, error) {
		result := livePreflightCatalog([]string{"gpt-5.4"}, "")
		result.ErrorSummary = "requested model is not in the provider catalog"
		return result, errors.Join(ports.ErrChatModelNotAvailable, errors.New("模型代码：不存在"))
	}}
	store, harness, ids, clock, _ := newComplexFixture(t)
	service := New(Deps{
		ParseCorrections:     store,
		AgentAttempts:        store,
		ProgressExplanations: store,
		Workspace:            gitWorkspaceObserver{},
		RecoverAgentSession:  func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },

		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store,
		AO: store, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		ControlledPreflights: store, ControlledPreflightChecker: checker,
		Human: allowStandardHuman{}, BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})
	view, err := service.CreateComplexRequirement(context.Background(), CreateComplexRequirementInput{
		AOProjectID: "s04-project", Name: "复杂邮件名单", PRDText: complexFixturePRD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.LatestControlledPreflight == nil || strings.Contains(view.LatestControlledPreflight.ErrorSummary, "模型") {
		t.Fatalf("copied localized text: %#v", view.LatestControlledPreflight)
	}
}

func timePtr(value time.Time) *time.Time { return &value }
