package cleardev

import (
	"context"
	"reflect"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type projectPreflightFixture struct {
	last ports.ChatPreflightContext
}

func (p *projectPreflightFixture) CheckControlledPreflight(context.Context, domain.AgentHarness, string) (ports.ChatControlledPreflight, error) {
	return ports.ChatControlledPreflight{}, ports.ErrChatDriverIncompatible
}

func (p *projectPreflightFixture) CheckControlledProjectPreflight(_ context.Context, cfg ports.ChatPreflightContext) (ports.ChatControlledPreflight, error) {
	p.last = cfg
	result := livePreflightCatalog([]string{cfg.RequestedModel}, cfg.RequestedModel)
	result.Provider, result.RequestedModel = string(cfg.Harness), cfg.RequestedModel
	result.Evidence = &domain.ControlledPreflightEvidence{Scope: "LOCAL_CONFIGURATION", Installation: "AVAILABLE", Configuration: "VALID", Model: "LISTED", Authentication: "UNKNOWN", Service: "UNKNOWN", Quota: "UNKNOWN"}
	return result, nil
}

func TestOpenCodeControlledRoleCreationUsesOneFixedProjectChoice(t *testing.T) {
	ctx := context.Background()
	store, harness, ids, clock, projectID := newComplexFixture(t)
	project := domain.ProjectRecord{ID: projectID, Path: "/tmp/s04-project", RegisteredAt: clock(),
		Config: domain.ProjectConfig{
			ClearDev: &domain.ClearDevExecutionConfig{Harness: domain.HarnessOpenCode, Model: "user/model"},
			// Ordinary AO role overrides must not influence any controlled role.
			Worker: domain.RoleOverride{Harness: domain.HarnessCodex, AgentConfig: domain.AgentConfig{Model: "different-model", Mode: "high"}},
			Env:    map[string]string{"OPENCODE_CONFIG": "/user/project-config.json"},
		}}
	if err := store.UpsertProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	service := complexTestService(store, harness, ids, clock, ctx)
	checker := &projectPreflightFixture{}
	service.preflightChecker = checker
	view := createComplexUntilClarification(t, service)
	if checker.last.Harness != domain.HarnessOpenCode || checker.last.RequestedModel != "user/model" || checker.last.WorkspacePath != project.Path || !reflect.DeepEqual(checker.last.Env, project.Config.Env) {
		t.Fatalf("preflight lost project execution context: %+v", checker.last)
	}
	if view.LatestControlledPreflight == nil || view.LatestControlledPreflight.Provider != "opencode" || view.LatestControlledPreflight.Evidence == nil || view.LatestControlledPreflight.Evidence.Quota != "UNKNOWN" {
		t.Fatalf("local inspection was not exposed honestly: %+v", view.LatestControlledPreflight)
	}
	for _, role := range []string{"steward", "planner", "builder", "task-reviewer", "stage-reviewer", "recovery", "specialist"} {
		kind := domain.KindWorker
		if role == "steward" {
			kind = domain.KindOrchestrator
		}
		session, err := service.spawnResolvedChatSession(ctx, ports.SpawnConfig{
			ProjectID: domain.ProjectID(projectID), Kind: kind, Harness: domain.HarnessCodex,
			RequestedMode: domain.SessionModeChat, CreationIdempotencyKey: "tool-test:" + role,
			AgentConfig: ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		}, "user/model")
		if err != nil || session.Harness != domain.HarnessOpenCode || session.Metadata.Model != "user/model" || !service.controlledSessionMatches(ctx, session.SessionRecord) {
			t.Fatalf("%s did not inherit fixed choice: %+v err=%v", role, session.SessionRecord, err)
		}
		wrong := session.SessionRecord
		wrong.Harness = domain.HarnessCodex
		if service.controlledSessionMatches(ctx, wrong) {
			t.Fatalf("%s accepted a foreign tool's session", role)
		}
		wrong = session.SessionRecord
		wrong.Metadata.Model = "user/other"
		if service.controlledSessionMatches(ctx, wrong) {
			t.Fatalf("%s accepted a different model", role)
		}
	}
	if _, err := service.spawnResolvedChatSession(ctx, ports.SpawnConfig{ProjectID: domain.ProjectID(projectID)}, "user/other"); err == nil {
		t.Fatal("creation accepted a model different from the successful project preflight")
	}
}

func TestProjectRolePromptsDescribeTheActualTool(t *testing.T) {
	product := core.ProductSnapshot{Goal: core.ProductGoal{GoalText: "small local application"}}
	current := core.ProductDiscussion{}
	record := domain.SessionRecord{Harness: domain.HarnessOpenCode, Metadata: domain.SessionMetadata{Model: "user/model", WorkspacePath: "/workspace", DiffBaseSHA: forty("a")}}
	prompt := projectDiscoveryPrompt(product, current, record)
	if !strings.Contains(prompt, "本轮实际使用 OpenCode") || !strings.Contains(prompt, "user/model") || strings.Contains(prompt, "实际运行配置为 Codex Auto") || strings.Contains(prompt, "可用的 Codex 原生") || !strings.Contains(prompt, "不得代替用户点击原生 Approve") {
		t.Fatal("OpenCode discovery prompt misrepresents execution or authority")
	}
	version := &core.RequirementVersion{}
	legacy := projectEngineeringPrompt("request", version, core.ComplexCompilation{}, core.NormalizedRequirementDocument{}, "context", core.ProductStage{}, nil)
	codex := projectEngineeringPrompt("request", version, core.ComplexCompilation{}, core.NormalizedRequirementDocument{}, "context", core.ProductStage{}, nil, domain.HarnessCodex)
	opencode := projectEngineeringPrompt("request", version, core.ComplexCompilation{}, core.NormalizedRequirementDocument{}, "context", core.ProductStage{}, nil, domain.HarnessOpenCode)
	if legacy != codex || !strings.Contains(opencode, "OpenCode read/search/Git") || strings.Contains(opencode, "Codex read/search/Git") {
		t.Fatal("planner tool adaptation changed legacy prompts or kept the wrong tools")
	}
}
