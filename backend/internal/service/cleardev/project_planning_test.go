package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sqlitedb "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// Provider replies, source observations and the desktop transport below are
// explicit test doubles. All discussion, plan, confirmation and execution gates
// use the real migrated SQLite store. This is not real model or Electron proof.
type projectPlanningAgent struct {
	*productTestAgent
	source       ports.ClearDevProjectSource
	sourceErr    error
	prepareErr   error
	observations int
	mailCalls    int
	afterRelay   func()
}

func (h *projectPlanningAgent) RelayChatTurnWithID(ctx context.Context, sessionID domain.SessionID, prompt, clientID string) (string, error) {
	id, err := h.productTestAgent.RelayChatTurnWithID(ctx, sessionID, prompt, clientID)
	if err == nil && h.afterRelay != nil {
		h.afterRelay()
	}
	return id, err
}

func (h *projectPlanningAgent) InspectProjectSource(context.Context, string, string) (ports.ClearDevProjectSource, error) {
	h.observations++
	return h.source, h.sourceErr
}

func (h *projectPlanningAgent) PrepareProjectSource(ctx context.Context, in ports.ClearDevSourcePreparation) (ports.ClearDevProjectSource, error) {
	if err := in.Current(ctx); err != nil {
		return ports.ClearDevProjectSource{}, err
	}
	if h.prepareErr != nil {
		return ports.ClearDevProjectSource{}, h.prepareErr
	}
	if repositoryIdentity(h.source.RepositoryURL) != repositoryIdentity(in.RepositoryURL) {
		return ports.ClearDevProjectSource{}, errors.New("test source has no verified ancestry")
	}
	return h.source, h.sourceErr
}

func (h *projectPlanningAgent) IdentifyMailProject(context.Context, string) (ports.ClearDevBaselineResult, error) {
	h.mailCalls++
	return ports.ClearDevBaselineResult{}, errors.New("non-mail project must not use the mail template")
}

type projectPlanningFixture struct {
	dir   string
	store *sqlitestore.Store
	s     *Service
	h     *projectPlanningAgent
	ids   *standardTestIDs
}

func newProjectPlanningFixture(t *testing.T, origin string) *projectPlanningFixture {
	t.Helper()
	dir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	for _, project := range []domain.ProjectRecord{
		{ID: "s04-project", Path: "/tmp/discussion-container", Kind: domain.ProjectKindSingleRepo, RegisteredAt: time.Now().UTC()},
		{ID: "notes-project", Path: "/tmp/selected-notes", Kind: domain.ProjectKindSingleRepo, RegisteredAt: time.Now().UTC()},
	} {
		if err := store.UpsertProject(context.Background(), project); err != nil {
			t.Fatal(err)
		}
	}
	h := &projectPlanningAgent{productTestAgent: &productTestAgent{standardAgentHarness: newStandardAgentHarness(store, false), base: forty("a")},
		source: ports.ClearDevProjectSource{BaseCommitSHA: forty("a"), Empty: origin == "EMPTY"}}
	if origin == "DISCOVERED" {
		h.source.RepositoryURL = "git@example.org:notes/notes.git"
	}
	f := &projectPlanningFixture{dir: dir, store: store, h: h, ids: &standardTestIDs{}}
	f.service()
	return f
}

func (f *projectPlanningFixture) service() {
	f.h.store = f.store
	f.s = New(Deps{
		Facts: f.store, StandardFacts: f.store, ComplexFacts: f.store, ComplexExecutionFacts: f.store, DirectionFacts: f.store, HumanDecisions: f.store,
		ParseCorrections: f.store, AgentAttempts: f.store, ControlledPreflights: f.store, ControlledPreflightChecker: alwaysPassControlledPreflight{},
		ProgressExplanations: f.store, Workspace: gitWorkspaceObserver{},
		RecoverAgentSession: func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },
		AO:                  f.store, Sessions: f.h, Chat: f.h, Inspector: f.h, Checks: f.h, Human: allowStandardHuman{},
		AutoAdvanceComplexPlans: true, PlannerTaskContracts: true,
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: f.ids.New, Clock: time.Now,
		BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
	})
}

func genericProjectOptions() []core.ProductOption {
	return []core.ProductOption{
		{Key: "empty", Title: "从零构建笔记", Origin: "EMPTY", Description: "只实现需要的本地笔记功能。", Tradeoffs: []string{"没有遗留约束，但要创建存储和检查。"}},
		{Key: "existing", Title: "改造自己的笔记项目", Origin: "EXISTING", Description: "沿用用户已有的数据结构。", Tradeoffs: []string{"保留已有兼容性，需要调查现有测试。"}},
		{Key: "discovered", Title: "改造调研项目", Origin: "DISCOVERED", RepositoryURL: "https://example.org/notes/notes.git", Description: "先核对来源与许可，再基于选定版本规划。", Tradeoffs: []string{"可复用已有结构，但许可与适用性仍须核实。"}},
	}
}

func TestProjectPromptsExplainExactPathGrammar(t *testing.T) {
	if core.ProjectPath("frontend/src/renderer/i18n/*.json", false) {
		t.Fatal("parser unexpectedly accepts a non-terminal wildcard")
	}
	current := core.ProductDiscussion{ID: "discussion", ProductID: "product", UserMessage: "inspect the project", ProtocolVersion: core.ProjectDiscoveryVersion}
	product := core.ProductSnapshot{
		Goal:        core.ProductGoal{ID: "product", GoalText: "build one local page"},
		Discussions: []core.ProductDiscussion{current},
	}
	discovery := projectDiscoveryPrompt(product, current, domain.SessionRecord{Metadata: domain.SessionMetadata{
		WorkspacePath: "/tmp/project",
		DiffBaseSHA:   forty("a"),
	}})
	for _, want := range []string{"唯一允许的通配形式是末尾 /**", "不要使用 *.json", "mainPaths 1–20项，并遵守相同路径语法", "每个 writePaths 边界必须被至少一个 check.mainPaths 覆盖", "禁止读取 HOME、CODEX_HOME、~/.codex、memories", "绝不能改去读取个人记忆或其它本地资料"} {
		if !strings.Contains(discovery, want) {
			t.Fatalf("discovery prompt does not explain %q", want)
		}
	}
	for _, want := range []string{`{"key":"next-goal","text":"需要用户决定的具体问题","reason":"为什么影响范围或验收"}`, "不能用 question 字段", "不能把数组元素改为字符串", "不沿用旧阶段作为新规格"} {
		if !strings.Contains(discovery, want) {
			t.Fatalf("discovery prompt does not explain the strict question contract: %q", want)
		}
	}
	if strings.Contains(discovery, "通用执行尚未开放") || !strings.Contains(discovery, "显式执行准入") {
		t.Fatal("discovery prompt describes the old planning-only product state")
	}

	planner := projectEngineeringPrompt("request", &core.RequirementVersion{}, core.ComplexCompilation{}, core.NormalizedRequirementDocument{}, "product goal", core.ProductStage{}, nil)
	for _, want := range []string{"The only wildcard form allowed is a terminal /**", "do not use *.json", "never read HOME, CODEX_HOME, ~/.codex, memories", "instead of falling back to personal memory"} {
		if !strings.Contains(planner, want) {
			t.Fatalf("planner prompt does not explain %q", want)
		}
	}
	if strings.Contains(planner, "execution is currently gated") || !strings.Contains(planner, "explicit execution admission") {
		t.Fatal("planner prompt describes the old planning-only product state")
	}
}

func genericProjectBasis() core.ProjectExecutionBasis {
	return core.ProjectExecutionBasis{
		WritePaths: []string{"src/**", "tests/**", "package.json", "package-lock.json", "migrations/**"}, DependencyNeeds: []string{"提出本地存储依赖，规划时不安装。"},
		Checks: []core.ProjectCheckSpec{{ID: "notes-tests", Argv: []string{"npm", "test"}, TimeoutSeconds: 120, MainPaths: []string{"src/**", "tests/**", "package.json", "package-lock.json", "migrations/**"}}},
		Launch: core.ProjectLaunch{Argv: []string{"npm", "start"}, WorkingDirectory: ".", Description: "启动本地笔记界面；此时尚未运行。"},
	}
}

func genericProjectReply(selected string) string {
	basis := genericProjectBasis()
	basis.Trial = &core.ProjectTrial{SchemaVersion: 1, Service: true, Steps: []core.ProjectTrialStep{{ID: "notes-ui", Kind: "BROWSER", AcceptanceCriteria: []string{"保存的笔记在重启后仍能读取。"}, Observe: "保存笔记，重启服务，核对原笔记。"}}}
	result := map[string]any{
		"schemaVersion": 2, "kind": "PRODUCT_DISCOVERY", "outcome": "DISCUSS", "message": "比较项目起点，由用户明确选择。", "feasibilitySummary": "技术检查和启动方式是提案，不是已执行证据。",
		"questions": []core.ProductQuestion{}, "features": []core.ProductFeature{}, "stages": []core.ProductStageDefinition{},
		"options": genericProjectOptions(), "evidence": []core.ProductEvidence{{Status: "UNVERIFIED", Claim: "新检查入口是否有效尚未运行验证。", Source: "本测试模型仅提供协议响应，不冒充实际调查。"}},
	}
	if selected != "" {
		result["outcome"], result["selectedOptionKey"] = "READY", selected
		result["features"] = []core.ProductFeature{{Key: "notes", Title: "保存笔记", Description: "保存并重新读取笔记。"}}
		result["stages"] = []core.ProductStageDefinition{{Key: "notes", Title: "本地笔记", Goal: "用户能保存笔记并在重启后读取。", FeatureKeys: []string{"notes"}, AcceptanceCriteria: []string{"保存的笔记在重启后仍能读取。"}, NonGoals: []string{"不做账号和云同步。"}, Feasibility: "NEEDS_CAPABILITY", FeasibilityReason: "可以规划，但通用执行尚未开放。", ExecutionBasis: &basis}}
	}
	raw, _ := json.Marshal(result)
	return string(raw)
}

func (f *projectPlanningFixture) create(t *testing.T) ProductGoalView {
	t.Helper()
	f.h.replies = append(f.h.replies, genericProjectReply(""))
	view, err := f.s.CreateProductGoal(context.Background(), CreateProductGoalInput{AOProjectID: "s04-project", RequestID: "notes-goal", Name: "本地笔记", GoalText: "个人保存笔记，重启后还可以读取。"})
	if err != nil || view.Phase != "AWAITING_INPUT" || view.Selection != nil || len(f.h.relays) != 1 {
		t.Fatalf("initial discussion: %+v err=%v sends=%d", view, err, len(f.h.relays))
	}
	return view
}

func projectChoice(view ProductGoalView, key string) ProductDiscussionInput {
	return ProductDiscussionInput{RequestID: "choose-notes", ExpectedPreviousID: view.Discussions[len(view.Discussions)-1].ID, Message: "采用这条路径，先完成本地保存。",
		Choice: &ProductChoiceInput{OptionKey: key, AOProjectID: "notes-project", Reason: "符合个人使用和本地存储目标。", ExpectedBaseCommitSHA: forty("a")}}
}

func (f *projectPlanningFixture) choose(t *testing.T, view ProductGoalView, key string) ProductGoalView {
	t.Helper()
	f.h.replies = append(f.h.replies, genericProjectReply(key))
	chosen, err := f.s.SubmitProductDiscussion(context.Background(), view.Goal.ID, projectChoice(view, key))
	if err != nil || chosen.Phase != "READY" || chosen.Selection == nil || !chosen.SourceCurrent || len(chosen.Stages) != 1 {
		t.Fatalf("choose source: %+v %v", chosen, err)
	}
	if chosen.Selection.AOProjectID != "notes-project" || chosen.Selection.BaseCommitSHA != forty("a") || f.h.observations == 0 {
		t.Fatalf("unbound source: %+v", chosen.Selection)
	}
	return chosen
}

func genericStageReply(stage core.ProductStageDefinition) string {
	var result map[string]any
	_ = json.Unmarshal([]byte(stageReadyReply(stage)), &result)
	result["constraints"] = []string{core.ProjectStageBasisConstraint(stage)}
	raw, _ := json.Marshal(result)
	return string(raw)
}

func genericEngineeringReply(t *testing.T, version core.RequirementVersion) string {
	t.Helper()
	var doc core.NormalizedRequirementDocument
	if err := json.Unmarshal([]byte(version.RequirementText), &doc); err != nil {
		t.Fatal(err)
	}
	coverage := core.CoverageFromNormalizedDocument(doc)
	requirements, acceptance := []string{}, []string{}
	for id := range coverage.RequirementIDs {
		requirements = append(requirements, id)
	}
	for id := range coverage.AcceptanceIDs {
		acceptance = append(acceptance, id)
	}
	result := map[string]any{
		"schemaVersion": 3, "kind": "COMPLEX_ENGINEERING_PLAN", "technicalApproach": "在选定基线上规划 src/storage.ts 与迁移及持久化测试；这是拟建结构，不是已有通过证据。",
		"tasks": []any{map[string]any{"key": "save-notes", "title": "本地保存笔记", "objective": "重启后仍能读取笔记。", "requirementIds": requirements, "acceptanceIds": acceptance,
			"writePaths": []string{"src/storage.ts", "tests/storage.test.ts", "package.json", "package-lock.json", "migrations/001.sql"}, "generatedPaths": []string{}, "sharedPathsRequireApproval": []string{}, "forbiddenPaths": []string{".git/**"}, "requiredCheckIds": []string{"notes-tests"}, "dependencyKeys": []string{}, "reviewCriteria": []string{"创建笔记并重启后内容不丢失，空笔记库可以正常打开。"}}},
		"integrationCheckIds": []string{"notes-tests"}, "parallelSuggestion": map[string]any{"recommendedBuilderCount": 1, "reason": "一个完整的持久化用户路径。"}, "risks": []string{},
	}
	raw, _ := json.Marshal(result)
	return string(raw)
}

func (f *projectPlanningFixture) prepare(t *testing.T, product ProductGoalView) (ProductGoalView, RequirementView) {
	t.Helper()
	stage := product.Stages[0].Stage
	f.h.replies = append(f.h.replies, genericStageReply(stage.Definition))
	prepared, err := f.s.PrepareProductStage(context.Background(), product.Goal.ID, stage.ID, PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256})
	if err != nil {
		t.Fatal(err)
	}
	child := mustGetComplex(t, f.s, prepared.Stages[0].Stage.DevelopmentRequirementID)
	if len(child.RequirementVersions) != 1 || child.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation || child.Requirement.AOProjectID != "notes-project" {
		t.Fatalf("stage did not bind selected source and native confirmation: %+v", child)
	}
	assertNoDevelopmentWork(t, f.store, f.h.standardAgentHarness, child.Requirement.ID)
	return prepared, child
}

func TestGenericProjectPlanningPersistsWithoutExecution(t *testing.T) {
	for _, origin := range []string{"EMPTY", "EXISTING", "DISCOVERED"} {
		t.Run(origin, func(t *testing.T) {
			f := newProjectPlanningFixture(t, origin)
			initial := f.create(t)
			selected := f.choose(t, initial, strings.ToLower(origin))
			prepared, child := f.prepare(t, selected)
			f.h.replies = append(f.h.replies, genericEngineeringReply(t, child.RequirementVersions[0]))
			applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
			child = mustGetComplex(t, f.s, child.Requirement.ID)
			state := child.TrustedProgress.ProjectPlanning
			if child.ComplexPlanning.Phase != core.ComplexPlanningProjectPlanned || len(child.ComplexPlanning.Plans) != 1 || state == nil || !state.Current || !state.PlanReady || !state.SourceCurrent || state.ExecutionAvailable || child.TrustedProgress.ReasonCode != core.ReasonProjectPlanningOnly || child.ComplexExecution != nil {
				t.Fatalf("plan failed to stop safely: phase=%s state=%+v reason=%s plans=%+v", child.ComplexPlanning.Phase, state, child.TrustedProgress.ReasonCode, child.ComplexPlanning.Plans)
			}
			if f.h.mailCalls != 0 || len(f.h.checkRequests) != 0 {
				t.Fatal("generic planning invoked the mail template or checks")
			}
			pinnedRoles := 0
			for _, role := range child.ComplexPlanning.RoleBindings {
				for _, cfg := range f.h.spawnConfigs {
					if cfg.CreationIdempotencyKey == role.SessionCreationIdempotencyKey {
						pinnedRoles++
						if cfg.WorkspaceBaseCommitSHA != selected.Selection.BaseCommitSHA || string(cfg.ProjectID) != selected.Selection.AOProjectID {
							t.Fatalf("generic %s did not receive its exact selected source: %+v", role.Role, cfg)
						}
						if role.Role == core.StandardRoleSteward && cfg.Branch != complexBranch("stage-steward", role.ID) {
							t.Fatalf("stage Steward did not receive its independent branch: %+v", cfg)
						}
					}
				}
			}
			if pinnedRoles != 2 {
				t.Fatalf("expected pinned Steward and Planner, got %d", pinnedRoles)
			}
			assertNoDevelopmentWork(t, f.store, f.h.standardAgentHarness, child.Requirement.ID)
			_, err := f.s.StartComplexStandardExecution(context.Background(), child.Requirement.ID)
			assertAPICode(t, err, string(core.ReasonProjectPlanningOnly))
			_, err = f.s.StartStandardFlow(context.Background(), child.Requirement.ID)
			assertAPICode(t, err, string(core.ReasonProjectPlanningOnly))
			_, err = f.s.CreateDevelopmentTask(context.Background(), child.Requirement.ID, CreateDevelopmentTaskInput{})
			assertAPICode(t, err, string(core.ReasonProjectPlanningOnly))

			before, _, err := f.store.GetClearDevProduct(context.Background(), prepared.Goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			calls := len(f.h.relays)
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			f.store, err = sqlitedb.Open(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.store.Close() })
			f.service()
			if err := f.s.ResumeComplexFlows(context.Background()); err != nil {
				t.Fatal(err)
			}
			after, _, err := f.store.GetClearDevProduct(context.Background(), prepared.Goal.ID)
			if err != nil || !reflect.DeepEqual(before, after) || len(f.h.relays) != calls {
				t.Fatalf("restart changed saved decisions or resent work: %v", err)
			}
			restarted := mustGetComplex(t, f.s, child.Requirement.ID)
			if !reflect.DeepEqual(child.ComplexPlanning.Plans, restarted.ComplexPlanning.Plans) || restarted.ComplexExecution != nil {
				t.Fatal("restart lost the plan or started execution")
			}

			raw, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			for _, statement := range []string{
				`INSERT INTO cleardev_complex_execution_runs(id,development_project_id,plan_id) VALUES('forbidden-run',?,?)`,
				`INSERT INTO cleardev_work_items(id,development_project_id,contract_version_id) VALUES('forbidden-task',?,?)`,
			} {
				if _, err := raw.Exec(statement, child.Requirement.ID, child.ComplexPlanning.Plans[0].ID); err == nil || !strings.Contains(err.Error(), "generic project planning") {
					t.Fatalf("database execution guard did not reject: %v", err)
				}
			}
			for _, statement := range []string{
				`DELETE FROM cleardev_product_discussion_contexts`,
				`UPDATE cleardev_product_discussion_contexts SET selection_sha256='changed'`,
				`INSERT OR REPLACE INTO cleardev_product_discussion_contexts SELECT * FROM cleardev_product_discussion_contexts`,
			} {
				if _, err := raw.Exec(statement); err == nil {
					t.Fatalf("immutable context accepted %s", statement)
				}
			}

			f.h.replies = append(f.h.replies, genericProjectReply(strings.ToLower(origin)))
			revision, err := f.s.SubmitProductDiscussion(context.Background(), prepared.Goal.ID, ProductDiscussionInput{RequestID: "revise-notes", ExpectedPreviousID: prepared.Discussions[len(prepared.Discussions)-1].ID, Message: "重新讨论第一阶段的取舍，保留历史。"})
			if err != nil || len(revision.Stages) != 2 || revision.Stages[0].Current || revision.Stages[0].Phase != "SUPERSEDED" {
				t.Fatalf("revision lost history: %+v %v", revision, err)
			}
			old := mustGetComplex(t, f.s, child.Requirement.ID)
			if old.TrustedProgress.ReasonCode != core.ReasonProductPlanSuperseded || old.TrustedProgress.ProjectPlanning.Current || len(old.ComplexPlanning.Plans) != 1 {
				t.Fatal("old plan remained current or was deleted")
			}
			_, err = f.s.PrepareProductStage(context.Background(), prepared.Goal.ID, prepared.Stages[0].Stage.ID, PrepareProductStageInput{DefinitionSHA256: prepared.Stages[0].Stage.DefinitionSHA256})
			assertAPICode(t, err, "PRODUCT_PLAN_SUPERSEDED")
			if err := f.s.ResumeComplexFlows(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertNoDevelopmentWork(t, f.store, f.h.standardAgentHarness, child.Requirement.ID)
		})
	}
}

func TestGenericProjectChoiceRejectsStaleSourceAndChangedReplays(t *testing.T) {
	f := newProjectPlanningFixture(t, "EXISTING")
	initial := f.create(t)
	for _, mutate := range []func(*ProductDiscussionInput){
		func(v *ProductDiscussionInput) { v.Choice.OptionKey = "unknown" },
		func(v *ProductDiscussionInput) { v.Choice.AOProjectID = "unknown" },
		func(v *ProductDiscussionInput) { v.Choice.Reason = " " },
		func(v *ProductDiscussionInput) { v.Choice.ExpectedBaseCommitSHA = forty("b") },
		func(v *ProductDiscussionInput) { v.Choice.OptionKey = "empty" },
	} {
		input := projectChoice(initial, "existing")
		mutate(&input)
		if _, err := f.s.SubmitProductDiscussion(context.Background(), initial.Goal.ID, input); err == nil || len(f.h.relays) != 1 {
			t.Fatal("invalid choice was accepted or sent")
		}
	}
	chosen := f.choose(t, initial, "existing")
	if _, err := f.s.SubmitProductDiscussion(context.Background(), chosen.Goal.ID, projectChoice(initial, "existing")); err != nil || len(f.h.relays) != 2 {
		t.Fatalf("exact replay: %v", err)
	}
	for _, mutate := range []func(*ProductDiscussionInput){
		func(v *ProductDiscussionInput) { v.Choice = nil },
		func(v *ProductDiscussionInput) { v.Choice.Reason = "different" },
		func(v *ProductDiscussionInput) { v.Choice.OptionKey = "empty" },
		func(v *ProductDiscussionInput) { v.ExpectedPreviousID = "different" },
	} {
		input := projectChoice(initial, "existing")
		mutate(&input)
		_, err := f.s.SubmitProductDiscussion(context.Background(), chosen.Goal.ID, input)
		assertAPICode(t, err, "PRODUCT_REQUEST_CHANGED")
	}
	f.h.source.BaseCommitSHA = forty("b")
	moved, err := f.s.GetProductGoal(context.Background(), chosen.Goal.ID)
	if err != nil || moved.Phase != "BLOCKED" || moved.Reason != "PRODUCT_BASELINE_CHANGED" || moved.SourceCurrent || !moved.CanDiscuss {
		t.Fatalf("moved baseline not visible/reselectable: %+v %v", moved, err)
	}
	_, err = f.s.SubmitProductDiscussion(context.Background(), chosen.Goal.ID, ProductDiscussionInput{RequestID: "silent-inherit", ExpectedPreviousID: chosen.Discussions[1].ID, Message: "继续"})
	assertAPICode(t, err, "PRODUCT_BASELINE_CHANGED")
	if len(f.h.relays) != 2 {
		t.Fatal("source drift sent a model turn")
	}
	f.h.source.BaseCommitSHA = forty("a")
	_, child := f.prepare(t, chosen)
	pending := pendingHumanRequestByKind(t, f.store, child.Requirement.ID, core.HumanDecisionKindConfirmVersion)
	offer, err := f.store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{
		RequestID: pending.ID, DesktopRunID: "stale-window", Nonce: mustComplexNonce(t), IssuedAt: time.Now(), ExpiresAt: time.Now().Add(core.HumanDecisionOfferTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.h.replies = append(f.h.replies, genericProjectReply("existing"))
	if _, err := f.s.SubmitProductDiscussion(context.Background(), chosen.Goal.ID, ProductDiscussionInput{RequestID: "supersede-confirmation", ExpectedPreviousID: chosen.Discussions[1].ID, Message: "先修改方案，不批准旧版本。"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ConfirmRequirementVersion(context.Background(), child.RequirementVersions[0].ID); err == nil {
		t.Fatal("superseded specification was confirmed")
	}
	requests, err := f.store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		if request.ID == pending.ID {
			t.Fatal("superseded specification remained in the native confirmation queue")
		}
	}
	if _, err := f.store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{
		RequestID: pending.ID, DesktopRunID: "new-window", Nonce: mustComplexNonce(t), IssuedAt: time.Now(), ExpiresAt: time.Now().Add(core.HumanDecisionOfferTTL),
	}); err == nil {
		t.Fatal("superseded specification was offered to a new native window")
	}
	if err := f.s.ApplyHumanDecisionResult(context.Background(), core.HumanDecisionResult{
		ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: offer.DesktopRunID,
		RequestID: offer.RequestID, DecisionKind: offer.DecisionKind, BindingSchemaVersion: offer.BindingSchemaVersion,
		Binding: offer.Binding, ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove,
	}); err == nil {
		t.Fatal("an already-open native window approved a superseded specification")
	}
	if latest := mustGetComplex(t, f.s, child.Requirement.ID); latest.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation || len(latest.ComplexPlanning.Plans) != 0 {
		t.Fatal("stale native approval changed the saved specification or started planning")
	}
	assertNoDevelopmentWork(t, f.store, f.h.standardAgentHarness, child.Requirement.ID)
}

func TestGenericProjectSelectionCannotBeRewrittenByModel(t *testing.T) {
	var result core.ProductDiscoveryResult
	if err := json.Unmarshal([]byte(genericProjectReply("existing")), &result); err != nil {
		t.Fatal(err)
	}
	selection := &core.ProductSelection{Option: genericProjectOptions()[1]}
	if err := core.ValidateProductDiscussionSelection(result, 2, selection); err != nil {
		t.Fatal(err)
	}
	result.Options[1].Description = "偷偷更换方向但保留编号。"
	if err := core.ValidateProductDiscussionSelection(result, 2, selection); err == nil {
		t.Fatal("same key rewrote selected option")
	}
	if err := core.ValidateProductDiscussionSelection(result, 2, nil); err == nil {
		t.Fatal("model invented a saved user choice")
	}
	if err := core.ValidateProductDiscussionSelection(result, 0, nil); err == nil {
		t.Fatal("legacy in-flight result was reinterpreted")
	}
}

// Provider/source doubles propose two sequential user features. Real migrated
// SQLite and Service must reject early Stage 2 preparation, including on reopen.
func TestGenericLaterStagePreparationRequiresDelivery(t *testing.T) {
	for _, throughStore := range []bool{false, true} {
		name := "service"
		if throughStore {
			name = "direct-store"
		}
		t.Run(name, func(t *testing.T) {
			f := newProjectPlanningFixture(t, "EMPTY")
			initial := f.create(t)
			var reply map[string]any
			if err := json.Unmarshal([]byte(genericProjectReply("empty")), &reply); err != nil {
				t.Fatal(err)
			}
			var stages []core.ProductStageDefinition
			raw, _ := json.Marshal(reply["stages"])
			if err := json.Unmarshal(raw, &stages); err != nil {
				t.Fatal(err)
			}
			second := stages[0]
			second.Key, second.Title, second.Goal = "read-back", "读取已保存笔记", "用户能读取已有笔记。"
			second.FeatureKeys = []string{"read-back"}
			reply["features"] = []core.ProductFeature{{Key: "notes", Title: "保存笔记", Description: "保存笔记。"}, {Key: "read-back", Title: "读取笔记", Description: "读取已有笔记。"}}
			second.AcceptanceCriteria = []string{"展示第一阶段保存的笔记内容。"}
			// 验收项被改写后，试用步骤必须逐字覆盖新验收项，否则属于无效提案。
			secondBasis := *second.ExecutionBasis
			secondBasis.Trial = &core.ProjectTrial{SchemaVersion: 1, Service: true, Steps: []core.ProjectTrialStep{{ID: "read-back-ui", Kind: "BROWSER",
				AcceptanceCriteria: append([]string(nil), second.AcceptanceCriteria...), Observe: "打开已完成阶段保存的笔记并核对内容。"}}}
			second.ExecutionBasis = &secondBasis
			reply["stages"] = append(stages, second)
			raw, _ = json.Marshal(reply)
			f.h.replies = append(f.h.replies, string(raw))
			selected, err := f.s.SubmitProductDiscussion(context.Background(), initial.Goal.ID, projectChoice(initial, "empty"))
			if err != nil || len(selected.Stages) != 2 {
				t.Fatalf("two-stage proposal: %v %+v", err, selected)
			}
			stage := selected.Stages[1].Stage
			before := len(f.h.relays)
			for i := 0; i < 2; i++ {
				if throughStore {
					container := f.s.productContainer(selected.Selection.AOProjectID, stage.Definition.Title, core.ProductStagePRD(selected.Goal, stage.Definition))
					_, _, err = f.store.PrepareClearDevProductStage(context.Background(), core.PrepareProductStageCommand{ProductID: selected.Goal.ID, StageID: stage.ID, DefinitionSHA256: stage.DefinitionSHA256, BaseCommitSHA: selected.Selection.BaseCommitSHA, Container: container})
					var rule *core.RuleError
					if !errors.As(err, &rule) || rule.Code != core.ReasonPreconditionNotMet {
						t.Fatalf("store accepted an early Stage 2: %v", err)
					}
				} else {
					_, err = f.s.PrepareProductStage(context.Background(), selected.Goal.ID, stage.ID, PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256})
					assertAPICode(t, err, "PRODUCT_PREVIOUS_STAGE_REQUIRED")
				}
				if len(f.h.relays) != before {
					t.Fatal("early stage dispatched another worker")
				}
				snapshot, found, err := f.store.GetClearDevProduct(context.Background(), selected.Goal.ID)
				if err != nil || !found || snapshot.Stages[1].DevelopmentRequirementID != "" {
					t.Fatalf("early preparation changed durable facts: %v %+v", err, snapshot)
				}
				if i == 0 {
					if err := f.store.Close(); err != nil {
						t.Fatal(err)
					}
					f.store, err = sqlitedb.Open(f.dir)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = f.store.Close() })
					f.service()
				}
			}
			_, child := f.prepare(t, selected)
			if child.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation {
				t.Fatal("ordering gate bypassed native confirmation")
			}
		})
	}
}

func TestLaterProductStageUsesExactFinalDelivery(t *testing.T) {
	ctx := context.Background()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	attachProjectFlow(f, preparer)
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	pending := driveProjectFlow(t, f, child.Requirement.ID, true)
	contract, _, err := core.ProjectContractFromRun(pending.Run)
	if err != nil {
		t.Fatal(err)
	}
	product, found, err := f.store.GetClearDevProduct(ctx, contract.ProductID)
	if err != nil || !found || len(product.Stages) != 1 {
		t.Fatalf("product fixture: %v", err)
	}
	next := product.Stages[0]
	next.Ordinal = 1
	selection := *next.Selection
	next.Selection = &selection
	assertAPICode(t, f.s.requirePreviousProductStage(ctx, product, next), "PRODUCT_PREVIOUS_STAGE_REQUIRED")
	completed := driveProjectFlow(t, f, child.Requirement.ID, false)
	assertAPICode(t, f.s.requirePreviousProductStage(ctx, product, next), "PRODUCT_PREVIOUS_STAGE_BASELINE_REQUIRED")
	next.Selection.BaseCommitSHA = completed.Integration.CandidateCommitSHA
	if err := f.s.requirePreviousProductStage(ctx, product, next); err != nil {
		t.Fatalf("exact final delivery rejected: %v", err)
	}
	if pending.Run.CompletedAt != nil {
		t.Fatal("task PASS snapshot was unexpectedly completed")
	}
	// A predecessor from another discussion cannot fulfill this proposal.
	next.DiscussionID = "unrelated-proposal"
	assertAPICode(t, f.s.requirePreviousProductStage(ctx, product, next), "PRODUCT_PREVIOUS_STAGE_REQUIRED")
}

// This verifies immutable prompt context, not a real model's compliance or
// product discussion sufficiency. Policy wording is independently reviewed.
func TestProjectDiscoveryReadinessKeepsDiscussionContext(t *testing.T) {
	selection := &core.ProductSelection{AOProjectID: "selected-project", BaseCommitSHA: forty("a"), Reason: "用户选择已有项目"}
	product := core.ProductSnapshot{Goal: core.ProductGoal{GoalText: "管理门店进货"}, Discussions: []core.ProductDiscussion{
		{Ordinal: 0, UserMessage: "只在一台电脑验证流程"},
		{Ordinal: 2, UserMessage: "不属于本轮的未来消息"},
	}}
	current := core.ProductDiscussion{Ordinal: 1, UserMessage: "金额口径还需要说明", Selection: selection}
	before, _ := json.Marshal(product)
	prompt := projectDiscoveryPrompt(product, current, domain.SessionRecord{})
	for _, text := range []string{product.Goal.GoalText, product.Discussions[0].UserMessage, current.UserMessage, selection.Reason, selection.BaseCommitSHA, "本产品最多12轮，本轮2"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt lost context %q", text)
		}
	}
	if strings.Contains(prompt, product.Discussions[1].UserMessage) {
		t.Fatal("future discussion leaked into current context")
	}
	after, _ := json.Marshal(product)
	if string(before) != string(after) || current.Selection != selection {
		t.Fatal("presentation policy rewrote stored context")
	}
}
