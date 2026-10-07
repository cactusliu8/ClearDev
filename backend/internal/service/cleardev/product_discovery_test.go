package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sqlite "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// Only the provider and Git inspection are explicit test doubles. Product,
// Agent-message budget, stage links and native confirmation requests use SQLite.
type productTestAgent struct {
	*standardAgentHarness
	replies []string
	base    string
	dirty   bool
}

func (h *productTestAgent) InspectCandidate(_ context.Context, _, base string) (ports.ClearDevCandidateInspection, error) {
	if h.dirty {
		return ports.ClearDevCandidateInspection{}, errors.New("test worktree changed")
	}
	return ports.ClearDevCandidateInspection{BaseSHA: base, CandidateSHA: base, Paths: []ports.ClearDevDiffPath{}}, nil
}

func (h *productTestAgent) IdentifyMailProject(context.Context, string) (ports.ClearDevBaselineResult, error) {
	return ports.ClearDevBaselineResult{Required: true, CandidateSHA: h.base}, nil
}

func (h *productTestAgent) RelayChatTurnWithID(_ context.Context, sessionID domain.SessionID, prompt, clientID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if id := h.turnByClientMessageID[clientID]; id != "" {
		return id, nil
	}
	if len(h.replies) == 0 {
		return "", errors.New("unexpected test model call")
	}
	response := h.replies[0]
	h.replies = h.replies[1:]
	id, messageID := fmt.Sprintf("product-turn-%d", len(h.relays)), fmt.Sprintf("product-result-%d", len(h.relays))
	now := time.Now().UTC()
	snapshot := h.snapshots[sessionID]
	snapshot.SessionID = sessionID
	snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{ID: id, HandledBySessionID: sessionID, State: domain.TurnStateCompleted, CompletedAt: &now})
	snapshot.Messages = append(snapshot.Messages,
		domain.ConversationMessage{ID: "user-" + messageID, TurnID: id, Sequence: int64(len(snapshot.Messages) + 1), Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: prompt, ClientMessageID: clientID},
		domain.ConversationMessage{ID: messageID, TurnID: id, Sequence: int64(len(snapshot.Messages) + 2), Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Text: response})
	h.snapshots[sessionID] = snapshot
	h.turnByClientMessageID[clientID] = id
	h.relays = append(h.relays, standardRelay{sessionID: sessionID, clientMessageID: clientID, prompt: prompt, response: response})
	return id, nil
}

func productReadyReply() string {
	r := core.ProductDiscoveryResult{SchemaVersion: 1, Kind: "PRODUCT_DISCOVERY", Outcome: "READY", Message: "先完成本地查找，再讨论真实邮箱连接。", FeasibilitySummary: "查看了现有前端与本地 API；当前权限不含认证和外部服务。", Questions: []core.ProductQuestion{},
		Features: []core.ProductFeature{{Key: "search", Title: "邮箱搜索", Description: "按邮箱查找联系人"}, {Key: "accounts", Title: "真实邮箱", Description: "连接外部邮箱账号"}},
		Stages: []core.ProductStageDefinition{
			{Key: "search", Title: "本地邮箱搜索", Goal: "用户能查找联系人", FeatureKeys: []string{"search"}, AcceptanceCriteria: []string{"输入邮箱子串时展示匹配联系人。"}, NonGoals: []string{"本阶段不连接外部邮箱。"}, Feasibility: "SUPPORTED", FeasibilityReason: "已有本地联系人页面"},
			{Key: "accounts", Title: "邮箱连接", Goal: "连接真实账号", FeatureKeys: []string{"accounts"}, AcceptanceCriteria: []string{"用户可以连接自己的邮箱。"}, NonGoals: []string{}, Feasibility: "NEEDS_CAPABILITY", FeasibilityReason: "认证与外部服务不在现有权限内"},
		}}
	data, _ := json.Marshal(r)
	return string(data)
}

func productDiscussReply() string {
	return `{"schemaVersion":1,"kind":"PRODUCT_DISCOVERY","outcome":"DISCUSS","message":"先确定首阶段用户路径。","feasibilitySummary":"当前为本地联系人应用，尚无外部账号接入。","questions":[{"key":"audience","text":"第一阶段服务个人还是团队？","reason":"影响范围和验收"}],"features":[],"stages":[]}`
}

func newProductTestService(t *testing.T, replies ...string) (*Service, *productTestAgent, *sqlite.Store) {
	t.Helper()
	store, harness, ids, clock, _ := newComplexFixture(t)
	h := &productTestAgent{standardAgentHarness: harness, replies: replies, base: forty("a")}
	service := New(Deps{
		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store,
		ParseCorrections: store, AgentAttempts: store, ControlledPreflights: store, ControlledPreflightChecker: alwaysPassControlledPreflight{},
		ProgressExplanations: store, Workspace: gitWorkspaceObserver{},
		RecoverAgentSession: func(context.Context, domain.SessionID) error {
			return errors.New("unexpected product session recovery")
		},
		AO: store, Sessions: h, Chat: h, Inspector: h, Checks: h, Human: allowStandardHuman{},
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock, BackgroundContext: context.Background(),
		RunBackground: func(run func()) { run() },
	})
	return service, h, store
}

func createTestProduct(t *testing.T, service *Service) ProductGoalView {
	t.Helper()
	// Seed a historical V1 product exactly as the old release persisted it.
	// New-entry V2 behavior is exercised separately by project planning tests.
	command := service.productContainer("s04-project", "邮箱软件", "先帮助个人用户管理现有联系人，以后再接入真实邮箱。")
	goal := core.ProductGoal{ID: command.Requirement.ID, RequestID: "product-create-1", AOProjectID: command.Requirement.AOProjectID, Name: command.Requirement.Name, GoalText: command.OriginalPRDText, CreatedAt: command.Requirement.CreatedAt}
	store, err := service.productStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateClearDevProductGoal(context.Background(), core.CreateProductGoalCommand{
		Goal: goal, Container: command,
		Discussion: core.ProductDiscussion{ID: "product-message:" + goal.RequestID, ProductID: goal.ID, UserMessage: goal.GoalText, CreatedAt: goal.CreatedAt},
	}); err != nil {
		t.Fatal(err)
	}
	service.scheduleComplexFlow(goal.ID)
	view, err := service.GetProductGoal(context.Background(), goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestProductDiscussionCreatesStagesWithoutPlannerOrExecution(t *testing.T) {
	service, h, store := newProductTestService(t, productDiscussReply(), productReadyReply())
	first := createTestProduct(t, service)
	if first.Phase != "AWAITING_INPUT" || len(first.Stages) != 0 || len(h.relays) != 1 {
		t.Fatalf("first discussion: %+v relays=%d", first, len(h.relays))
	}
	input := ProductDiscussionInput{RequestID: "discuss-2", ExpectedPreviousID: first.Discussions[0].ID, Message: "个人使用，先做本地查找。"}
	next, err := service.SubmitProductDiscussion(context.Background(), first.Goal.ID, input)
	if err != nil || next.Phase != "READY" || len(next.Stages) != 2 || len(h.relays) != 2 {
		t.Fatalf("ready proposal: %+v %v", next, err)
	}
	if next.Stages[1].Phase != "NEEDS_CAPABILITY" || next.Stages[0].Progress != nil {
		t.Fatalf("proposal confused with execution: %+v", next.Stages)
	}
	if _, err := service.SubmitProductDiscussion(context.Background(), first.Goal.ID, input); err != nil || len(h.relays) != 2 {
		t.Fatalf("duplicate message was sent twice: %v", err)
	}
	if err := service.ResumeComplexFlows(context.Background()); err != nil || len(h.relays) != 2 {
		t.Fatalf("resume duplicated settled discussion: %v", err)
	}
	view, err := service.GetRequirement(context.Background(), next.Goal.ID)
	if err != nil || len(view.RequirementVersions) != 0 || view.ComplexExecution != nil {
		t.Fatalf("product became executable: %+v %v", view, err)
	}
	for _, binding := range view.ComplexPlanning.RoleBindings {
		if binding.Role != core.StandardRoleSteward {
			t.Fatalf("discovery spawned %s", binding.Role)
		}
	}
	for _, cfg := range h.spawnConfigs {
		if cfg.Kind != domain.KindWorker || !strings.Contains(cfg.Branch, "product-steward") {
			t.Fatalf("product must investigate an isolated worktree: %+v", cfg)
		}
	}
	if !strings.Contains(h.relays[1].prompt, input.Message) || !strings.Contains(h.relays[1].prompt, first.Discussions[0].Result.Message) {
		t.Fatal("Steward did not receive durable discussion history")
	}
	ids, err := store.ListClearDevProductGoalIDs(context.Background(), "s04-project")
	if err != nil || len(ids) != 1 {
		t.Fatalf("product list %v %v", ids, err)
	}
}

func TestProductProposalRevisionRejectsStaleOrUnsupportedStage(t *testing.T) {
	service, _, _ := newProductTestService(t, productReadyReply(), productReadyReply())
	first := createTestProduct(t, service)
	if _, err := service.PrepareProductStage(context.Background(), first.Goal.ID, first.Stages[1].Stage.ID, PrepareProductStageInput{DefinitionSHA256: first.Stages[1].Stage.DefinitionSHA256}); err == nil {
		t.Fatal("unsupported future capability was executable")
	}
	next, err := service.SubmitProductDiscussion(context.Background(), first.Goal.ID, ProductDiscussionInput{RequestID: "revision", ExpectedPreviousID: first.Discussions[0].ID, Message: "保持这两个阶段，但请重新解释取舍。"})
	if err != nil || len(next.Stages) != 4 || next.Stages[0].Current {
		t.Fatalf("revision lost old proposal: %+v %v", next, err)
	}
	if _, err := service.PrepareProductStage(context.Background(), first.Goal.ID, first.Stages[0].Stage.ID, PrepareProductStageInput{DefinitionSHA256: first.Stages[0].Stage.DefinitionSHA256}); err == nil {
		t.Fatal("old proposal could start a stage")
	}
	if _, err := service.SubmitProductDiscussion(context.Background(), first.Goal.ID, ProductDiscussionInput{RequestID: "stale", ExpectedPreviousID: first.Discussions[0].ID, Message: "来自旧页面"}); err == nil {
		t.Fatal("stale discussion accepted")
	}
}

func stageReadyReply(stage core.ProductStageDefinition) string {
	r := core.RequirementCompilationResult{SchemaVersion: 1, Kind: "REQUIREMENT_COMPILATION", Outcome: "READY", Summary: stage.Goal,
		Requirements:        []core.CompilationRequirement{{Key: "stage-goal", Priority: "MUST", Text: stage.Goal, AcceptanceKeys: []string{"outcome"}}},
		AcceptanceScenarios: []core.CompilationAcceptance{{Key: "outcome", Text: stage.AcceptanceCriteria[0]}}, NonGoals: stage.NonGoals,
		Constraints: []string{}, Terms: []core.CompilationTerm{}, Assumptions: []string{}, Conflicts: []core.CompilationConflict{}, BlockingQuestions: []core.CompilationBlockingQuestion{}}
	data, _ := core.MarshalAgentChosenResult(r)
	return string(data)
}

func TestProductStagePrepareIsIdempotentAndStillNeedsNativeConfirmation(t *testing.T) {
	service, h, store := newProductTestService(t, productReadyReply())
	product := createTestProduct(t, service)
	stage := product.Stages[0].Stage
	h.replies = append(h.replies, stageReadyReply(stage.Definition))
	input := PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256}
	prepared, err := service.PrepareProductStage(context.Background(), product.Goal.ID, stage.ID, input)
	if err != nil || prepared.Phase != "FROZEN" {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	childID := prepared.Stages[0].Stage.DevelopmentRequirementID
	child, err := service.GetRequirement(context.Background(), childID)
	if err != nil || len(child.RequirementVersions) != 1 || child.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation || child.ComplexExecution != nil {
		t.Fatalf("stage did not stop at native confirmation: %+v %v", child, err)
	}
	if !strings.Contains(child.RequirementVersions[0].RequirementText, stage.Definition.AcceptanceCriteria[0]) || !strings.Contains(child.RequirementVersions[0].RequirementText, stage.Definition.NonGoals[0]) {
		t.Fatal("stage contract was weakened at handoff")
	}
	for _, binding := range child.ComplexPlanning.RoleBindings {
		if binding.Role != core.StandardRoleSteward {
			t.Fatalf("unconfirmed stage spawned %s", binding.Role)
		}
	}
	calls := len(h.relays)
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := service.PrepareProductStage(context.Background(), product.Goal.ID, stage.ID, input)
			if err != nil || v.Stages[0].Stage.DevelopmentRequirementID != childID {
				failures <- fmt.Errorf("duplicate prepare: %w", err)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if len(h.relays) != calls {
		t.Fatal("duplicate prepare resent model input")
	}
	if _, err := service.SubmitProductDiscussion(context.Background(), product.Goal.ID, ProductDiscussionInput{RequestID: "after-prepare", ExpectedPreviousID: product.Discussions[0].ID, Message: "改变已发送的阶段"}); err == nil {
		t.Fatal("sent stage proposal could be silently replaced")
	}
	if _, err := service.StartComplexStandardExecution(context.Background(), childID); err == nil {
		t.Fatal("unconfirmed stage started execution")
	}
	if _, err := service.ProposeDirectionIntent(context.Background(), childID, ProposeDirectionIntentInput{RequestID: "rewrite-stage", DevelopmentRequirementID: childID, Message: "Drop the confirmed product outcomes"}); err == nil || !strings.Contains(err.Error(), "frozen product proposal") {
		t.Fatalf("standalone direction path can rewrite a frozen stage: %v", err)
	}
	persisted, found, err := store.GetClearDevProductStageByRequirement(context.Background(), childID)
	if err != nil || !found || persisted.BaseCommitSHA != forty("a") {
		t.Fatalf("stage baseline: %+v %v", persisted, err)
	}
	h.base = forty("b")
	if _, _, err := service.mailRequirementBaseline(context.Background(), child.Requirement); err == nil {
		t.Fatal("prepared stage silently followed a moved default branch")
	}
}

func TestProductChangedInvestigationStopsWithoutSendingOrLosingReason(t *testing.T) {
	service, h, _ := newProductTestService(t, productReadyReply())
	h.dirty = true
	view := createTestProduct(t, service)
	if view.Phase != "BLOCKED" || view.Reason != "PRODUCT_STEWARD_WORKSPACE_CHANGED" || len(h.relays) != 0 || len(view.Stages) != 0 {
		t.Fatalf("changed investigation was not stopped: %+v", view)
	}
	h.dirty = false
	if err := service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := service.GetProductGoal(context.Background(), view.Goal.ID)
	if err != nil || after.Phase != "BLOCKED" || len(h.relays) != 0 {
		t.Fatalf("restoring worktree reopened failed investigation: %+v %v", after, err)
	}
}

func TestProductInvalidReplyCannotCreateStagesOrResetMessageBudget(t *testing.T) {
	service, h, _ := newProductTestService(t, `{"kind":"APPROVED"}`, `{"kind":"APPROVED"}`, `{"kind":"APPROVED"}`)
	view := createTestProduct(t, service)
	if view.Phase != "BLOCKED" || len(view.Stages) != 0 || len(h.relays) > 3 {
		t.Fatalf("invalid product reply escaped controlled parser: phase=%s reason=%s sends=%d stages=%d", view.Phase, view.Reason, len(h.relays), len(view.Stages))
	}
	before := len(h.relays)
	if err := service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.relays) != before {
		t.Fatal("invalid result reopened after resume")
	}
}

func TestProductLaterStageRequiresPredecessorDelivery(t *testing.T) {
	service, h, _ := newProductTestService(t, strings.ReplaceAll(productReadyReply(), "NEEDS_CAPABILITY", "SUPPORTED"))
	view := createTestProduct(t, service)
	stage := view.Stages[1].Stage
	_, err := service.PrepareProductStage(context.Background(), view.Goal.ID, stage.ID, PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256})
	if err == nil || len(h.relays) != 1 {
		t.Fatalf("later stage skipped predecessor: %v sends=%d", err, len(h.relays))
	}
	after, err := service.GetProductGoal(context.Background(), view.Goal.ID)
	if err != nil || after.Stages[1].Stage.DevelopmentRequirementID != "" {
		t.Fatalf("rejected preparation left child link: %+v %v", after, err)
	}
}

func TestProductGoalCannotEnterRequirementExecutionFlow(t *testing.T) {
	service, h, store := newProductTestService(t, productReadyReply())
	product := createTestProduct(t, service)
	productID := product.Goal.ID
	if _, err := service.CreateRequirementVersion(context.Background(), productID, "把产品目标直接变成可执行规格"); err == nil {
		t.Fatal("product goal accepted an executable requirement version")
	}
	if _, err := service.StartComplexStandardExecution(context.Background(), productID); err == nil {
		t.Fatal("product goal started an execution")
	}
	if _, err := service.SubmitComplexClarifications(context.Background(), productID, SubmitComplexClarificationsInput{
		CompilationRequestID: product.Discussions[0].ID, ClarificationRound: 0,
		Answers: []ComplexClarificationAnswerInput{{QuestionKey: "audience", Text: "个人用户"}},
	}); err == nil {
		t.Fatal("product discussion was treated as requirement clarification")
	}
	if _, err := service.ProposeDirectionIntent(context.Background(), productID, ProposeDirectionIntentInput{
		RequestID: "product-direction", DevelopmentRequirementID: productID, Message: "Change the frozen product direction",
	}); err == nil {
		t.Fatal("product goal accepted a runtime direction change")
	}
	if len(h.relays) != 1 {
		t.Fatalf("guarded calls triggered model sends: %d", len(h.relays))
	}
	view, err := service.GetRequirement(context.Background(), productID)
	if err != nil || len(view.RequirementVersions) != 0 || view.ComplexExecution != nil {
		t.Fatalf("product container became executable: %+v %v", view, err)
	}
	ids, err := store.ListClearDevRequirementIDsByAOProject(context.Background(), "s04-project")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == productID {
			t.Fatal("product goal was listed as an executable development requirement")
		}
	}
}

func TestProductDiscussionBudgetDoesNotResetAcrossRounds(t *testing.T) {
	replies := make([]string, core.ProductMaxDiscussions)
	for i := range replies {
		replies[i] = productDiscussReply()
	}
	service, h, _ := newProductTestService(t, replies...)
	view := createTestProduct(t, service)
	for i := 1; i < core.ProductMaxDiscussions; i++ {
		var err error
		view, err = service.SubmitProductDiscussion(context.Background(), view.Goal.ID, ProductDiscussionInput{RequestID: fmt.Sprintf("round-%d", i), ExpectedPreviousID: view.Discussions[len(view.Discussions)-1].ID, Message: "继续细化，不要擅自认定已确认。"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if view.Phase != "BLOCKED" || view.RemainingDiscussions != 0 || len(h.relays) != core.ProductMaxDiscussions {
		t.Fatalf("budget view %+v sends=%d", view, len(h.relays))
	}
	if _, err := service.SubmitProductDiscussion(context.Background(), view.Goal.ID, ProductDiscussionInput{RequestID: "over-budget", ExpectedPreviousID: view.Discussions[len(view.Discussions)-1].ID, Message: "再来一次"}); err == nil || len(h.relays) != core.ProductMaxDiscussions {
		t.Fatal("discussion exhausted budget reopened")
	}
	if view.MessageBudget == nil || view.MessageBudget.ConfirmedSentMessages == nil || *view.MessageBudget.ConfirmedSentMessages != int64(core.ProductMaxDiscussions) {
		t.Fatalf("message ledger was not reused: %+v", view.MessageBudget)
	}
}
