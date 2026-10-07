package cleardevtest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// ComplexParallelPrep is the production-shaped S07 start point: confirmed v2,
// task-set version zero, and an APPROVED three-task two-Builder plan.
type ComplexParallelPrep struct {
	RequirementID        string
	V1ID                 string
	V2ID                 string
	V1GateID             string
	V2PlanID             string
	V2ReviewID           string
	StewardSessionID     string
	PlannerSessionID     string
	PlannerWorkspacePath string
}

// SeedComplexParallelV2 creates a confirmed v2 with a 3-task plan that
// recommends two Builders. The first two tasks are independent and path-disjoint;
// the third depends on both.
func SeedComplexParallelV2(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string) ComplexParallelPrep {
	return seedComplexParallelV2(t, store, projectID, repo, dataDir, ComplexStandardPrepSessions{}, false, false, false)
}

// SeedMailParallelV2 uses the same persisted S07 DAG/composition facts with the
// bounded mail V2 task/check contract. It exists only for production-shaped
// service tests; the scheduler itself is not duplicated.
func SeedMailParallelV2(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string) ComplexParallelPrep {
	return seedComplexParallelV2(t, store, projectID, repo, dataDir, ComplexStandardPrepSessions{}, true, false, false)
}

// SeedMailOverlappingV2 recommends two Builders but deliberately overlaps the
// first pair's write paths, so the production selector must degrade to STANDARD.
func SeedMailOverlappingV2(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string) ComplexParallelPrep {
	return seedComplexParallelV2(t, store, projectID, repo, dataDir, ComplexStandardPrepSessions{}, true, true, false)
}

// SeedMailIndependentV2 gives three ready, disjoint tasks to two Builder slots.
func SeedMailIndependentV2(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string) ComplexParallelPrep {
	return seedComplexParallelV2(t, store, projectID, repo, dataDir, ComplexStandardPrepSessions{}, true, false, true)
}

// SeedComplexParallelV2WithSessions is the real-daemon S07 preparer.
func SeedComplexParallelV2WithSessions(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string, sessions ComplexStandardPrepSessions) ComplexParallelPrep {
	if sessions.StewardSessionID == "" {
		t.Fatal("ComplexParallel production Steward session identifier is required")
	}
	return seedComplexParallelV2(t, store, projectID, repo, dataDir, sessions, false, false, false)
}

func seedComplexParallelV2(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string, sessions ComplexStandardPrepSessions, mailPlan, overlapping, independent bool) ComplexParallelPrep {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: projectID, Path: repo, Kind: domain.ProjectKindSingleRepo, RegisteredAt: now,
		Config: domain.ProjectConfig{DefaultBranch: "main"},
	}); err != nil {
		t.Fatalf("create ComplexParallel AO project: %v", err)
	}

	const requirementID = "complex-parallel-requirement"
	const v1ID = "complex-parallel-v1"
	const v2ID = "complex-parallel-v2"
	const stewardBindingID = "complex-parallel-steward"
	const plannerBindingID = "complex-parallel-planner"
	prd := "处理邮件名单：规范化、去重并输出摘要。"
	var steward, planner domain.SessionRecord
	if sessions.StewardSessionID == "" {
		steward = complexStandardSession(t, store, projectID, repo, dataDir, now, domain.KindOrchestrator, "", "complex-parallel-steward-key")
		planner = complexStandardSession(t, store, projectID, repo, dataDir, now, domain.KindWorker, "ao/complex-parallel-planner", "complex-parallel-planner-key")
	} else {
		steward = complexStandardExistingSession(t, store, sessions.StewardSessionID, projectID, domain.KindOrchestrator)
		planner = complexStandardSession(t, store, projectID, repo, dataDir, now, domain.KindWorker, "ao/complex-parallel-planner", "complex-parallel-planner-key")
	}
	stewardRoleKey := steward.CreationIdempotencyKey
	if stewardRoleKey == "" {
		stewardRoleKey = "complex-parallel-steward-key"
	}
	plannerRoleKey := planner.CreationIdempotencyKey
	if plannerRoleKey == "" {
		plannerRoleKey = "complex-parallel-planner-key"
	}
	if err := store.CreateClearDevComplexRequirement(ctx, core.CreateComplexRequirementCommand{
		Requirement:     core.DevelopmentRequirement{ID: requirementID, AOProjectID: projectID, Name: "ComplexParallel fixture", CreatedAt: now, UpdatedAt: now},
		OriginalPRDText: prd, OriginalPRDSHA256: complexStandardSHA(prd), TargetRequirementVersionID: v1ID,
		StewardRoleBinding: core.ComplexRoleBinding{ID: stewardBindingID, DevelopmentRequirementID: requirementID, Role: core.StandardRoleSteward,
			SessionCreationIdempotencyKey: stewardRoleKey, Status: core.RoleBindingStatusRequested, RequestedAt: now},
	}); err != nil {
		t.Fatalf("create ComplexParallel requirement: %v", err)
	}
	if changed, err := store.BindClearDevComplexRoleBinding(ctx, stewardBindingID, string(steward.ID), now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("bind ComplexParallel Steward: changed=%t err=%v", changed, err)
	}
	if _, created, err := store.CreateClearDevComplexRoleBinding(ctx, core.CreateComplexRoleBindingCommand{Binding: core.ComplexRoleBinding{
		ID: plannerBindingID, DevelopmentRequirementID: requirementID, Role: core.StandardRoleEngineeringPlanner,
		SessionCreationIdempotencyKey: plannerRoleKey, Status: core.RoleBindingStatusRequested, RequestedAt: now.Add(time.Second),
	}}); err != nil || !created {
		t.Fatalf("create ComplexParallel Planner binding: created=%t err=%v", created, err)
	}
	if changed, err := store.BindClearDevComplexRoleBinding(ctx, plannerBindingID, string(planner.ID), now.Add(2*time.Second)); err != nil || !changed {
		t.Fatalf("bind ComplexParallel Planner: changed=%t err=%v", changed, err)
	}

	v1Doc, v1Compilation := complexParallelCompilation(t, requirementID, v1ID, "complex-parallel-v1", prd, now.Add(3*time.Second), core.NormalizedRequirementDocument{}, false)
	complexStandardSettleComplexStep(t, store, stewardBindingID, "complex-parallel-v1-compile-step", core.ComplexAgentStepCompilation, "complex-parallel-v1-compile-request", now.Add(3*time.Second), "ready")
	if err := store.SettleClearDevComplexCompilation(ctx, core.SettleComplexCompilationCommand{
		Request:     core.ComplexCompilationRequest{ID: "complex-parallel-v1-compile-request", DevelopmentRequirementID: requirementID, AgentStepID: "complex-parallel-v1-compile-step", ClarificationRound: 1, CompilationContextSHA256: complexStandardSHA("v1-context"), CreatedAt: now.Add(3 * time.Second)},
		Compilation: v1Compilation.Compilation, IDMaps: v1Compilation.IDMaps,
		Version: core.RequirementVersion{ID: v1ID, DevelopmentRequirementID: requirementID, Version: 1, RequirementText: string(v1Doc), SHA256: v1Compilation.Compilation.CompilationSHA256, Status: core.RequirementVersionStatusDraft, CreatedAt: now.Add(3 * time.Second)}, At: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("settle ComplexParallel v1 compilation: %v", err)
	}
	complexStandardConfirm(t, store, v1ID, now.Add(4*time.Second))

	service := cleardevsvc.New(cleardevsvc.Deps{Facts: store, AO: store})
	for _, title := range []string{"v1 direction task one", "v1 direction task two"} {
		if _, err := service.CreateDevelopmentTask(ctx, requirementID, cleardevsvc.CreateDevelopmentTaskInput{
			Title: title, Mode: core.WorkModeQuick, MaxReworkCount: 0,
			Permissions:    cleardevsvc.CreatePathPermissionsInput{WritePaths: []string{"src/**"}, ForbiddenPaths: []string{".git/**"}},
			RequiredChecks: []cleardevsvc.CreateRequiredCheckInput{{Name: "all-tests", Kind: "command"}},
		}); err != nil {
			t.Fatalf("create ComplexParallel v1 task %q: %v", title, err)
		}
	}
	_, _ = complexParallelPlanAndReview(t, store, requirementID, v1ID, v1Compilation.Compilation.CompilationSHA256, plannerBindingID, stewardBindingID, core.CoverageFromNormalizedDocument(v1Compilation.Document), 1, 1, now.Add(5*time.Second), false, false, false)

	gateID := complexParallelApproveDirection(t, store, requirementID, v1ID, v1Compilation.Compilation.CompilationSHA256, stewardBindingID, now.Add(6*time.Second))
	direction, ok, err := store.GetClearDevDirectionChange(ctx, requirementID)
	if err != nil || !ok || len(direction.Processings) != 2 {
		t.Fatalf("load approved ComplexParallel direction processings: ok=%t processings=%d err=%v", ok, len(direction.Processings), err)
	}
	for index, processing := range direction.Processings {
		if err := store.FinalizeClearDevDirectionTask(ctx, core.FinalizeDirectionTaskCommand{
			Processing: core.DirectionTaskProcessing{ID: processing.ID, CancelResult: core.DirectionCancelCancelled, ReasonCode: core.ReasonDirectionPlannedUnstarted},
			CancelTask: true, Checkpoint: &core.DirectionCheckpoint{ID: fmt.Sprintf("complex-parallel-v1-checkpoint-%d", index), Kind: core.DirectionCheckpointUnstarted, ChangeSummaryJSON: "[]", CreatedAt: now.Add(7 * time.Second)}, At: now.Add(7 * time.Second),
		}); err != nil {
			t.Fatalf("finalize ComplexParallel v1 task %s: %v", processing.TaskID, err)
		}
	}
	if err := store.CreateClearDevDirectionRevision(ctx, core.CreateDirectionRevisionCommand{Revision: core.DirectionRevision{
		DirectionRequestID: direction.Request.ID, PreviousRequirementVersionID: v1ID, TargetRequirementVersionID: v2ID, CreatedAt: now.Add(8 * time.Second),
	}}); err != nil {
		t.Fatalf("create ComplexParallel v2 revision: %v", err)
	}

	v2Doc, v2Compilation := complexParallelCompilation(t, requirementID, v2ID, "complex-parallel-v2", prd, now.Add(9*time.Second), v1Compilation.Document, true)
	complexStandardSettleDirectionStep(t, store, stewardBindingID, "complex-parallel-v2-compile-step", "complex-parallel-v2-compile-request", now.Add(9*time.Second), "ready v2")
	if err := store.SettleClearDevDirectionCompilation(ctx, core.SettleDirectionCompilationCommand{
		DirectionRequestID: direction.Request.ID,
		Request:            core.ComplexCompilationRequest{ID: "complex-parallel-v2-compile-request", DevelopmentRequirementID: requirementID, AgentStepID: "complex-parallel-v2-compile-step", ClarificationRound: 1, CompilationContextSHA256: complexStandardSHA("v2-context"), CreatedAt: now.Add(9 * time.Second)},
		Compilation:        v2Compilation.Compilation, IDMaps: v2Compilation.IDMaps,
		Version: core.RequirementVersion{ID: v2ID, DevelopmentRequirementID: requirementID, Version: 2, RequirementText: string(v2Doc), SHA256: v2Compilation.Compilation.CompilationSHA256, Status: core.RequirementVersionStatusDraft, CreatedAt: now.Add(9 * time.Second)}, At: now.Add(9 * time.Second),
	}); err != nil {
		t.Fatalf("settle ComplexParallel v2 compilation: %v", err)
	}
	complexStandardConfirm(t, store, v2ID, now.Add(10*time.Second))
	v2PlanID, v2ReviewID := complexParallelPlanAndReview(t, store, requirementID, v2ID, v2Compilation.Compilation.CompilationSHA256, plannerBindingID, stewardBindingID, core.CoverageFromNormalizedDocument(v2Compilation.Document), 2, 2, now.Add(11*time.Second), mailPlan, overlapping, independent)

	if active, err := store.HasActiveClearDevDirectionStop(ctx, v1ID); err != nil || !active {
		t.Fatalf("ComplexParallel v1 direction gate: active=%t err=%v", active, err)
	}
	if active, err := store.HasActiveClearDevDirectionStop(ctx, v2ID); err != nil || active {
		t.Fatalf("ComplexParallel v2 direction gate: active=%t err=%v", active, err)
	}
	return ComplexParallelPrep{RequirementID: requirementID, V1ID: v1ID, V2ID: v2ID, V1GateID: gateID, V2PlanID: v2PlanID, V2ReviewID: v2ReviewID, StewardSessionID: string(steward.ID), PlannerSessionID: string(planner.ID), PlannerWorkspacePath: planner.Metadata.WorkspacePath}
}

func complexParallelCompilation(t testing.TB, requirementID, versionID, prefix, prd string, at time.Time, previous core.NormalizedRequirementDocument, hasPrevious bool) ([]byte, complexStandardCompilation) {
	t.Helper()
	result := complexStandardRequirementResult(versionID, hasPrevious)
	compilation := core.ComplexCompilation{ID: prefix + "-compilation", DevelopmentRequirementID: requirementID, CompilationRequestID: prefix + "-compile-request", AgentStepID: prefix + "-compile-step", Outcome: "READY", Summary: result.Summary, TurnID: prefix + "-compile-step-turn", FinalMessageID: prefix + "-compile-step-message", RawMessageText: "ready", RawMessageSHA256: complexStandardSHA("ready"), CreatedAt: at}
	reqMaps, accMaps := core.AssignStableCompilationIDs(compilation.ID, result)
	if hasPrevious {
		reqIDs, accIDs := core.StableIDsFromDocument(previous)
		var err error
		reqMaps, accMaps, err = core.AssignStableCompilationIDsFromPrevious(compilation.ID, result, reqIDs, accIDs)
		if err != nil {
			t.Fatalf("assign ComplexParallel identifiers: %v", err)
		}
	}
	doc, raw, digest, err := core.BuildNormalizedRequirementDocument(result, complexStandardSHA(prd), reqMaps, accMaps)
	if err != nil {
		t.Fatalf("build ComplexParallel document: %v", err)
	}
	if hasPrevious {
		compilation.RawMessageText, compilation.RawMessageSHA256 = "ready v2", complexStandardSHA("ready v2")
	}
	compilation.NormalizedRequirementJSON, compilation.CompilationSHA256 = string(raw), digest
	return raw, complexStandardCompilation{Document: doc, Compilation: compilation, IDMaps: append(reqMaps, accMaps...)}
}

func complexParallelPlanAndReview(t testing.TB, store *sqlite.Store, requirementID, versionID, compilationSHA, plannerBindingID, stewardBindingID string, coverage core.ComplexCoverage, number, recommended int64, at time.Time, mailPlan, overlapping, independent bool) (string, string) {
	t.Helper()
	requestID := fmt.Sprintf("complex-parallel-plan-request-v%d", number)
	tasks := []core.ComplexPlanTask{
		{Key: "normalize-and-deduplicate", Title: "规范化和去重", Objective: "实现邮箱规范化与去重。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"}, WritePaths: []string{"src/email.js", "src/deduplicate.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"email-unit", "deduplicate-unit"}, DependencyKeys: []string{}},
		{Key: "build-summary", Title: "生成摘要", Objective: "输出接受和拒绝数量。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-002"}, WritePaths: []string{"src/summary.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"summary-unit"}, DependencyKeys: []string{"normalize-and-deduplicate"}},
	}
	reason := "任务有明确依赖，使用一个 Builder。"
	approach := "先规范化并去重，再生成摘要。"
	if recommended == 2 {
		tasks = []core.ComplexPlanTask{
			{Key: "normalize-email", Title: "规范化邮箱", Objective: "实现邮箱规范化。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"}, WritePaths: []string{"src/email.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"email-unit"}, DependencyKeys: []string{}},
			{Key: "deduplicate-email", Title: "去重邮箱", Objective: "实现邮箱去重。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"}, WritePaths: []string{"src/deduplicate.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"deduplicate-unit"}, DependencyKeys: []string{}},
			{Key: "build-summary", Title: "生成摘要", Objective: "输出接受和拒绝数量。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-002"}, WritePaths: []string{"src/summary.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"summary-unit"}, DependencyKeys: []string{"normalize-email", "deduplicate-email"}},
		}
		reason = "前两项路径分离且无依赖，使用两个 Builder。"
		approach = "先并行规范化与去重，再组合后生成摘要。"
	}
	integrationCheckIDs := []string{"all-tests"}
	if mailPlan {
		tasks = []core.ComplexPlanTask{
			{Key: "frontend-state", Title: "前端状态", Objective: "实现联系人前端状态。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"}, WritePaths: []string{"frontend/app.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"demo-frontend", "demo-integration"}, DependencyKeys: []string{}},
			{Key: "frontend-regression", Title: "前端回归", Objective: "追加前端回归测试。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"}, WritePaths: []string{"test/frontend.test.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"demo-integration"}, DependencyKeys: []string{}},
			{Key: "frontend-followup", Title: "前端集成", Objective: "在前批成果上完成最终前端集成。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-002"}, WritePaths: []string{"frontend/index.html"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"demo-frontend", "demo-integration"}, DependencyKeys: []string{"frontend-state", "frontend-regression"}},
		}
		if overlapping {
			tasks[1].WritePaths = []string{"frontend/app.js"}
		}
		if independent {
			tasks[2].DependencyKeys = []string{}
		}
		integrationCheckIDs = []string{"demo-integration"}
		reason = "前两项写入边界互不冲突，使用两个 Builder；第三项等待前批组合结果。"
		approach = "前端实现和回归并行，可信组合后再执行依赖任务并形成最终 F。"
	}
	planInput := core.ComplexEngineeringPlanResult{SchemaVersion: core.ComplexProtocolVersion, Kind: "COMPLEX_ENGINEERING_PLAN", PlanningRequestID: requestID, RequirementVersionID: versionID, RequirementVersionSHA256: complexStandardRequirementSHA(t, store, requirementID, versionID), CompilationSHA256: compilationSHA, TechnicalApproach: approach, Tasks: tasks, IntegrationCheckIDs: integrationCheckIDs, ParallelSuggestion: core.ComplexParallelSuggestion{RecommendedBuilderCount: int(recommended), Reason: reason}, Risks: []string{"摘要必须基于去重结果。"}}
	raw, err := core.MarshalAgentChosenResult(planInput)
	if err != nil {
		t.Fatalf("marshal ComplexParallel plan: %v", err)
	}
	parsed, normalized, planSHA, err := core.ParseComplexEngineeringPlanResult(raw, requestID, versionID, planInput.RequirementVersionSHA256, compilationSHA, coverage)
	if err != nil {
		t.Fatalf("parse ComplexParallel plan: %v", err)
	}
	planID := fmt.Sprintf("complex-parallel-plan-v%d", number)
	planStepID := fmt.Sprintf("complex-parallel-plan-step-v%d", number)
	complexStandardSettleComplexStep(t, store, plannerBindingID, planStepID, core.ComplexAgentStepEngineeringPlan, requestID, at, string(normalized))
	if err := store.CreateClearDevComplexEngineeringPlan(context.Background(), core.CreateComplexPlanCommand{Plan: core.ComplexEngineeringPlan{ID: planID, PlanningRequestID: requestID, DevelopmentRequirementID: requirementID, RequirementVersionID: versionID, RequirementSHA256: planInput.RequirementVersionSHA256, CompilationSHA256: compilationSHA, Version: number, PlannerRoleBindingID: plannerBindingID, AgentStepID: planStepID, TurnID: planStepID + "-turn", FinalMessageID: planStepID + "-message", PlanJSON: string(normalized), PlanSHA256: planSHA, CreatedAt: at}}); err != nil {
		t.Fatalf("save ComplexParallel plan: %v", err)
	}
	reviewRequestID := fmt.Sprintf("complex-parallel-review-request-v%d", number)
	reviewInput := core.ComplexPlanReviewResult{SchemaVersion: core.ComplexProtocolVersion, Kind: "COMPLEX_PLAN_REVIEW", ReviewRequestID: reviewRequestID, PlanID: planID, PlanSHA256: planSHA, Verdict: "APPROVED", ReasonCode: "PLAN_ACCEPTABLE", Summary: "覆盖需求和验收，允许执行。", Findings: []core.ComplexReviewFinding{}}
	reviewRaw, err := core.MarshalAgentChosenResult(reviewInput)
	if err != nil {
		t.Fatalf("marshal ComplexParallel review: %v", err)
	}
	taskKeys := make([]string, 0, len(parsed.Tasks))
	for _, task := range parsed.Tasks {
		taskKeys = append(taskKeys, task.Key)
	}
	_, normalizedReview, err := core.ParseComplexPlanReviewResult(reviewRaw, reviewRequestID, planID, planSHA, coverage, taskKeys)
	if err != nil {
		t.Fatalf("parse ComplexParallel review: %v", err)
	}
	reviewID := fmt.Sprintf("complex-parallel-review-v%d", number)
	reviewStepID := fmt.Sprintf("complex-parallel-review-step-v%d", number)
	findingsJSON, err := json.Marshal(reviewInput.Findings)
	if err != nil {
		t.Fatalf("marshal ComplexParallel review findings: %v", err)
	}
	complexStandardSettleComplexStep(t, store, stewardBindingID, reviewStepID, core.ComplexAgentStepPlanReview, reviewRequestID, at, string(normalizedReview))
	if err := store.RecordClearDevComplexPlanReview(context.Background(), core.RecordComplexPlanReviewCommand{Review: core.ComplexPlanReview{ID: reviewID, PlanID: planID, StewardRoleBindingID: stewardBindingID, AgentStepID: reviewStepID, ReviewRequestID: reviewRequestID, Verdict: core.PlanReviewApproved, ReasonCode: "PLAN_ACCEPTABLE", Summary: reviewInput.Summary, FindingsJSON: string(findingsJSON), PlanSHA256: planSHA, TurnID: reviewStepID + "-turn", FinalMessageID: reviewStepID + "-message", CreatedAt: at}, At: at}); err != nil {
		t.Fatalf("save ComplexParallel review: %v", err)
	}
	return planID, reviewID
}

func complexParallelApproveDirection(t testing.TB, store *sqlite.Store, requirementID, v1ID, v1SHA, stewardBindingID string, at time.Time) string { //nolint:dupl // S07 IDs must stay distinct from the S06 helper.
	t.Helper()
	ctx := context.Background()
	step := core.AgentStep{ID: "complex-parallel-direction-step", RoleBindingID: stewardBindingID, Kind: core.DirectionAgentStepChange, RequestID: "complex-parallel-direction-request", ClientMessageID: "complex-parallel-direction-client", PromptSHA256: complexStandardSHA("direction-prompt"), SendStatus: core.AgentStepSendStatusPending, RequestedAt: at}
	intent := core.DirectionIntent{RequestID: "complex-parallel-direction-intent", DevelopmentRequirementID: requirementID, RequirementVersionID: v1ID, RequirementSHA256: v1SHA, Message: "只接受 example.com 域名。", MessageSHA256: core.DirectionMessageSHA256("只接受 example.com 域名。"), StewardRoleBindingID: stewardBindingID, DirectionRequestID: step.RequestID, AgentStepID: step.ID, CreatedAt: at}
	if _, created, err := store.CreateClearDevDirectionIntent(ctx, core.CreateDirectionIntentCommand{Intent: intent, Step: step}); err != nil || !created {
		t.Fatalf("create ComplexParallel direction intent: created=%t err=%v", created, err)
	}
	if changed, err := store.MarkClearDevDirectionAgentStepSent(ctx, step.ID, at.Add(time.Second)); err != nil || !changed {
		t.Fatalf("send ComplexParallel direction step: changed=%t err=%v", changed, err)
	}
	settledAt := at.Add(2 * time.Second)
	if err := store.AcceptClearDevDirectionChange(ctx, core.AcceptDirectionChangeCommand{Intent: intent, Request: core.DirectionRequest{ID: step.RequestID, IntentRequestID: intent.RequestID, DevelopmentRequirementID: requirementID, RequirementVersionID: v1ID, Summary: "停止 v1 并建立 v2。", AffectedRequirementIDs: []string{"REQ-001"}, ResultSHA256: complexStandardSHA("direction-result"), AgentStepID: step.ID, CreatedAt: settledAt}, Gate: core.DirectionStopGate{ID: "complex-parallel-v1-gate", CreatedAt: settledAt}, AgentStep: core.AgentStep{ID: step.ID, SendStatus: core.AgentStepSendStatusSettled, TurnID: "direction-turn", FinalMessageID: "direction-message", FinalMessageText: "accepted", MessageSHA256: complexStandardSHA("accepted"), CompletedAt: &settledAt}, DecisionRequestID: "complex-parallel-direction-decision", At: settledAt}); err != nil {
		t.Fatalf("accept ComplexParallel direction: %v", err)
	}
	pending, err := store.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		t.Fatalf("list ComplexParallel decisions: %v", err)
	}
	requestID := ""
	for _, request := range pending {
		if request.DevelopmentRequirementID == requirementID && request.DecisionKind == core.HumanDecisionKindApproveDirectionChange {
			requestID = request.ID
		}
	}
	if requestID == "" {
		t.Fatal("ComplexParallel direction decision request missing")
	}
	nonce, err := humanauthority.NewToken()
	if err != nil {
		t.Fatalf("new ComplexParallel nonce: %v", err)
	}
	offer, err := store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: requestID, DesktopRunID: "complex-parallel-desktop", Nonce: nonce, IssuedAt: at.Add(3 * time.Second), ExpiresAt: at.Add(3 * time.Second).Add(core.HumanDecisionOfferTTL)})
	if err != nil {
		t.Fatalf("issue ComplexParallel decision: %v", err)
	}
	if err := store.SettleClearDevHumanDecision(ctx, core.HumanDecisionResult{ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind, BindingSchemaVersion: offer.BindingSchemaVersion, Binding: append(json.RawMessage(nil), offer.Binding...), ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove}, at.Add(4*time.Second)); err != nil {
		t.Fatalf("approve ComplexParallel direction: %v", err)
	}
	return "complex-parallel-v1-gate"
}
