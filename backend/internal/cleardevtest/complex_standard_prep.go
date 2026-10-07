package cleardevtest

// This file deliberately builds the S06 entry state through the public
// ClearDev stores and service methods.  It is test support only: production
// code must never import cleardevtest.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/gitworktree"
	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// ComplexStandardPrep is a fixed, production-shaped S06 start point.  V1's
// ACTIVE direction gate remains intentionally: it protects v1 only.  V2 has
// no gate and is the current confirmed version.
type ComplexStandardPrep struct {
	RequirementID        string
	V1ID                 string
	V2ID                 string
	V1GateID             string
	V1PlanID             string
	V1ReviewID           string
	V2PlanID             string
	V2ReviewID           string
	StewardSessionID     string
	PlannerSessionID     string
	PlannerWorkspacePath string
}

// ComplexStandardPrepSessions lets an acceptance test supply the Steward AO
// session created through the production session service with a real Chat
// controller. The already-finished Engineering Planner remains a durable
// production-store fixture because S06 must not send it another turn.
type ComplexStandardPrepSessions struct {
	StewardSessionID string
}

// SeedComplexStandardV1 creates a confirmed v1 with task-set version zero and
// an APPROVED two-task plan. It does not apply a direction change. Tests may
// use this to prove STANDARD/PARALLEL execution on the current confirmed v1.
// Production demo code must not import this package.
func SeedComplexStandardV1(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string) ComplexStandardPrep {
	return seedComplexStandardV2(t, store, projectID, repo, dataDir, ComplexStandardPrepSessions{}, false, true, false, false)
}

// SeedMailBenchCampaignV1 creates a confirmed v1 with a production-shaped
// two-task Campaign plan and demo-* checks. It is test support only.
func SeedMailBenchCampaignV1(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string) ComplexStandardPrep {
	return seedComplexStandardV2(t, store, projectID, repo, dataDir, ComplexStandardPrepSessions{}, false, true, true, false)
}

// SeedComplexStandardV2 creates a confirmed v2 with task-set version zero and
// an APPROVED, exact two-task, one-Builder plan.  Every ClearDev fact is
// written by an existing production store or service method; it uses no SQL.
func SeedComplexStandardV2(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string) ComplexStandardPrep {
	return seedComplexStandardV2(t, store, projectID, repo, dataDir, ComplexStandardPrepSessions{}, false, false, false, false)
}

// SeedComplexExceptionV2 is the S09 start point: the ordinary two-task plan
// lists shared package.json and generated package-lock.json on the first task.
func SeedComplexExceptionV2(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string) ComplexStandardPrep {
	return seedComplexStandardV2(t, store, projectID, repo, dataDir, ComplexStandardPrepSessions{}, true, false, false, false)
}

// SeedComplexStandardV2WithSessions is the real-daemon S06 preparer. It does
// not manufacture AO sessions: both supplied identifiers must already resolve
// to the exact production-created Steward and Planner sessions.
func SeedComplexStandardV2WithSessions(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string, sessions ComplexStandardPrepSessions) ComplexStandardPrep {
	if sessions.StewardSessionID == "" {
		t.Fatal("ComplexStandard production Steward session identifier is required")
	}
	return seedComplexStandardV2(t, store, projectID, repo, dataDir, sessions, false, false, false, false)
}

// SeedComplexExceptionV2WithSessions is the real-daemon S09 preparer. It
// requires an existing production Steward session identifier.
func SeedComplexExceptionV2WithSessions(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string, sessions ComplexStandardPrepSessions) ComplexStandardPrep {
	if sessions.StewardSessionID == "" {
		t.Fatal("ComplexException production Steward session identifier is required")
	}
	return seedComplexStandardV2(t, store, projectID, repo, dataDir, sessions, true, false, false, false)
}

// SeedComplexSingleTaskV2 creates a confirmed v1 with one approved natural
// task and recommends one Builder. It is test support for the v20 one-task
// materialization path; no SQL is written directly.
func SeedComplexSingleTaskV2(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string) ComplexStandardPrep {
	return seedComplexStandardV2(t, store, projectID, repo, dataDir, ComplexStandardPrepSessions{}, false, true, false, true)
}

func seedComplexStandardV2(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string, sessions ComplexStandardPrepSessions, exception, stopAtV1, mailBench, singleTask bool) ComplexStandardPrep {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: projectID, Path: repo, Kind: domain.ProjectKindSingleRepo, RegisteredAt: now,
		Config: domain.ProjectConfig{DefaultBranch: "main"},
	}); err != nil {
		t.Fatalf("create ComplexStandard AO project: %v", err)
	}

	const requirementID = "complex-standard-requirement"
	const v1ID = "complex-standard-v1"
	const v2ID = "complex-standard-v2"
	const stewardBindingID = "complex-standard-steward"
	const plannerBindingID = "complex-standard-planner"
	prd := "处理邮件名单：规范化、去重并输出摘要。"
	if mailBench {
		prd = "实现 Campaign 创建、编辑、冻结、历史和发送流程。"
	}
	var steward, planner domain.SessionRecord
	if sessions.StewardSessionID == "" {
		steward = complexStandardSession(t, store, projectID, repo, dataDir, now, domain.KindOrchestrator, "", "complex-standard-steward-key")
		planner = complexStandardSession(t, store, projectID, repo, dataDir, now, domain.KindWorker, "ao/complex-standard-planner", "complex-standard-planner-key")
	} else {
		steward = complexStandardExistingSession(t, store, sessions.StewardSessionID, projectID, domain.KindOrchestrator)
		planner = complexStandardSession(t, store, projectID, repo, dataDir, now, domain.KindWorker, "ao/complex-standard-planner", "complex-standard-planner-key")
	}
	// Public AO session creation deliberately has no caller-controlled creation
	// key. The frozen S05 fixture still needs durable, non-empty role request
	// keys, while retaining the exact production-created Chat session ids.
	stewardRoleKey := steward.CreationIdempotencyKey
	if stewardRoleKey == "" {
		stewardRoleKey = "complex-standard-steward-key"
	}
	plannerRoleKey := planner.CreationIdempotencyKey
	if plannerRoleKey == "" {
		plannerRoleKey = "complex-standard-planner-key"
	}
	if err := store.CreateClearDevComplexRequirement(ctx, core.CreateComplexRequirementCommand{
		Requirement:     core.DevelopmentRequirement{ID: requirementID, AOProjectID: projectID, Name: "ComplexStandard fixture", CreatedAt: now, UpdatedAt: now},
		OriginalPRDText: prd, OriginalPRDSHA256: complexStandardSHA(prd), TargetRequirementVersionID: v1ID,
		StewardRoleBinding: core.ComplexRoleBinding{ID: stewardBindingID, DevelopmentRequirementID: requirementID, Role: core.StandardRoleSteward,
			SessionCreationIdempotencyKey: stewardRoleKey, Status: core.RoleBindingStatusRequested, RequestedAt: now},
	}); err != nil {
		t.Fatalf("create ComplexStandard requirement: %v", err)
	}

	if changed, err := store.BindClearDevComplexRoleBinding(ctx, stewardBindingID, string(steward.ID), now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("bind ComplexStandard Steward: changed=%t err=%v", changed, err)
	}
	if _, created, err := store.CreateClearDevComplexRoleBinding(ctx, core.CreateComplexRoleBindingCommand{Binding: core.ComplexRoleBinding{
		ID: plannerBindingID, DevelopmentRequirementID: requirementID, Role: core.StandardRoleEngineeringPlanner,
		SessionCreationIdempotencyKey: plannerRoleKey, Status: core.RoleBindingStatusRequested, RequestedAt: now.Add(time.Second),
	}}); err != nil || !created {
		t.Fatalf("create ComplexStandard Planner binding: created=%t err=%v", created, err)
	}
	if changed, err := store.BindClearDevComplexRoleBinding(ctx, plannerBindingID, string(planner.ID), now.Add(2*time.Second)); err != nil || !changed {
		t.Fatalf("bind ComplexStandard Planner: changed=%t err=%v", changed, err)
	}

	v1Doc, v1Compilation := complexStandardInitialCompilation(t, requirementID, v1ID, prd, now.Add(3*time.Second))
	complexStandardSettleComplexStep(t, store, stewardBindingID, "complex-standard-v1-compile-step", core.ComplexAgentStepCompilation, "complex-standard-v1-compile-request", now.Add(3*time.Second), "ready")
	if err := store.SettleClearDevComplexCompilation(ctx, core.SettleComplexCompilationCommand{
		Request:     core.ComplexCompilationRequest{ID: "complex-standard-v1-compile-request", DevelopmentRequirementID: requirementID, AgentStepID: "complex-standard-v1-compile-step", ClarificationRound: 1, CompilationContextSHA256: complexStandardSHA("v1-context"), CreatedAt: now.Add(3 * time.Second)},
		Compilation: v1Compilation.Compilation, IDMaps: v1Compilation.IDMaps,
		Version: core.RequirementVersion{ID: v1ID, DevelopmentRequirementID: requirementID, Version: 1, RequirementText: string(v1Doc), SHA256: v1Compilation.Compilation.CompilationSHA256, Status: core.RequirementVersionStatusDraft, CreatedAt: now.Add(3 * time.Second)}, At: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("settle ComplexStandard v1 compilation: %v", err)
	}
	complexStandardConfirm(t, store, v1ID, now.Add(4*time.Second))

	if stopAtV1 {
		v1PlanID, v1ReviewID := complexStandardPlanAndReview(t, store, requirementID, v1ID, v1Compilation.Compilation.CompilationSHA256, plannerBindingID, stewardBindingID, core.CoverageFromNormalizedDocument(v1Compilation.Document), 1, now.Add(5*time.Second), exception, mailBench, singleTask)
		return ComplexStandardPrep{
			RequirementID: requirementID, V1ID: v1ID, V1PlanID: v1PlanID, V1ReviewID: v1ReviewID,
			StewardSessionID: string(steward.ID), PlannerSessionID: string(planner.ID), PlannerWorkspacePath: planner.Metadata.WorkspacePath,
		}
	}

	service := cleardevsvc.New(cleardevsvc.Deps{Facts: store, AO: store})
	for _, title := range []string{"v1 direction task one", "v1 direction task two"} {
		if _, err := service.CreateDevelopmentTask(ctx, requirementID, cleardevsvc.CreateDevelopmentTaskInput{
			Title: title, Mode: core.WorkModeQuick, MaxReworkCount: 0,
			Permissions:    cleardevsvc.CreatePathPermissionsInput{WritePaths: []string{"src/**"}, ForbiddenPaths: []string{".git/**"}},
			RequiredChecks: []cleardevsvc.CreateRequiredCheckInput{{Name: "all-tests", Kind: "command"}},
		}); err != nil {
			t.Fatalf("create ComplexStandard v1 task %q: %v", title, err)
		}
	}
	v1PlanID, v1ReviewID := complexStandardPlanAndReview(t, store, requirementID, v1ID, v1Compilation.Compilation.CompilationSHA256, plannerBindingID, stewardBindingID, core.CoverageFromNormalizedDocument(v1Compilation.Document), 1, now.Add(5*time.Second), false, false, singleTask)

	gateID := complexStandardApproveDirection(t, store, requirementID, v1ID, v1Compilation.Compilation.CompilationSHA256, stewardBindingID, now.Add(6*time.Second))
	direction, ok, err := store.GetClearDevDirectionChange(ctx, requirementID)
	if err != nil || !ok || len(direction.Processings) != 2 {
		t.Fatalf("load approved ComplexStandard direction processings: ok=%t processings=%d err=%v", ok, len(direction.Processings), err)
	}
	for index, processing := range direction.Processings {
		if err := store.FinalizeClearDevDirectionTask(ctx, core.FinalizeDirectionTaskCommand{
			Processing: core.DirectionTaskProcessing{ID: processing.ID, CancelResult: core.DirectionCancelCancelled, ReasonCode: core.ReasonDirectionPlannedUnstarted},
			CancelTask: true, Checkpoint: &core.DirectionCheckpoint{ID: fmt.Sprintf("complex-standard-v1-checkpoint-%d", index), Kind: core.DirectionCheckpointUnstarted, ChangeSummaryJSON: "[]", CreatedAt: now.Add(7 * time.Second)}, At: now.Add(7 * time.Second),
		}); err != nil {
			t.Fatalf("finalize ComplexStandard v1 task %s: %v", processing.TaskID, err)
		}
	}
	if err := store.CreateClearDevDirectionRevision(ctx, core.CreateDirectionRevisionCommand{Revision: core.DirectionRevision{
		DirectionRequestID: direction.Request.ID, PreviousRequirementVersionID: v1ID, TargetRequirementVersionID: v2ID, CreatedAt: now.Add(8 * time.Second),
	}}); err != nil {
		t.Fatalf("create ComplexStandard v2 revision: %v", err)
	}

	v2Doc, v2Compilation := complexStandardV2Compilation(t, requirementID, v2ID, prd, v1Compilation.Document, now.Add(9*time.Second))
	complexStandardSettleDirectionStep(t, store, stewardBindingID, "complex-standard-v2-compile-step", "complex-standard-v2-compile-request", now.Add(9*time.Second), "ready v2")
	if err := store.SettleClearDevDirectionCompilation(ctx, core.SettleDirectionCompilationCommand{
		DirectionRequestID: direction.Request.ID,
		Request:            core.ComplexCompilationRequest{ID: "complex-standard-v2-compile-request", DevelopmentRequirementID: requirementID, AgentStepID: "complex-standard-v2-compile-step", ClarificationRound: 1, CompilationContextSHA256: complexStandardSHA("v2-context"), CreatedAt: now.Add(9 * time.Second)},
		Compilation:        v2Compilation.Compilation, IDMaps: v2Compilation.IDMaps,
		Version: core.RequirementVersion{ID: v2ID, DevelopmentRequirementID: requirementID, Version: 2, RequirementText: string(v2Doc), SHA256: v2Compilation.Compilation.CompilationSHA256, Status: core.RequirementVersionStatusDraft, CreatedAt: now.Add(9 * time.Second)}, At: now.Add(9 * time.Second),
	}); err != nil {
		t.Fatalf("settle ComplexStandard v2 compilation: %v", err)
	}
	complexStandardConfirm(t, store, v2ID, now.Add(10*time.Second))
	v2PlanID, v2ReviewID := complexStandardPlanAndReview(t, store, requirementID, v2ID, v2Compilation.Compilation.CompilationSHA256, plannerBindingID, stewardBindingID, core.CoverageFromNormalizedDocument(v2Compilation.Document), 2, now.Add(11*time.Second), exception, false, singleTask)

	if active, err := store.HasActiveClearDevDirectionStop(ctx, v1ID); err != nil || !active {
		t.Fatalf("ComplexStandard v1 direction gate: active=%t err=%v", active, err)
	}
	if active, err := store.HasActiveClearDevDirectionStop(ctx, v2ID); err != nil || active {
		t.Fatalf("ComplexStandard v2 direction gate: active=%t err=%v", active, err)
	}
	return ComplexStandardPrep{
		RequirementID: requirementID, V1ID: v1ID, V2ID: v2ID, V1GateID: gateID,
		V1PlanID: v1PlanID, V1ReviewID: v1ReviewID, V2PlanID: v2PlanID, V2ReviewID: v2ReviewID,
		StewardSessionID: string(steward.ID), PlannerSessionID: string(planner.ID), PlannerWorkspacePath: planner.Metadata.WorkspacePath,
	}
}

func complexStandardExistingSession(t testing.TB, store *sqlite.Store, sessionID, projectID string, kind domain.SessionKind) domain.SessionRecord {
	t.Helper()
	record, found, err := store.GetSession(context.Background(), domain.SessionID(sessionID))
	if err != nil || !found {
		t.Fatalf("load production ComplexStandard session %s: found=%t err=%v", sessionID, found, err)
	}
	if string(record.ProjectID) != projectID || record.Kind != kind || record.Harness != domain.HarnessCodex || domain.NormalizeSessionMode(record.Mode) != domain.SessionModeChat || record.PermissionMode != domain.PermissionModeAuto || record.IsTerminated || record.Metadata.WorkspacePath == "" {
		t.Fatalf("production ComplexStandard session has invalid binding: %#v", record)
	}
	return record
}

type complexStandardCompilation struct {
	Document    core.NormalizedRequirementDocument
	Compilation core.ComplexCompilation
	IDMaps      []core.ComplexIDMap
}

func complexStandardInitialCompilation(t testing.TB, requirementID, versionID, prd string, at time.Time) ([]byte, complexStandardCompilation) {
	t.Helper()
	result := complexStandardRequirementResult(versionID, false)
	compilation := core.ComplexCompilation{ID: "complex-standard-v1-compilation", DevelopmentRequirementID: requirementID, CompilationRequestID: "complex-standard-v1-compile-request", AgentStepID: "complex-standard-v1-compile-step", Outcome: "READY", Summary: result.Summary, TurnID: "complex-standard-v1-compile-step-turn", FinalMessageID: "complex-standard-v1-compile-step-message", RawMessageText: "ready", RawMessageSHA256: complexStandardSHA("ready"), CreatedAt: at}
	reqMaps, accMaps := core.AssignStableCompilationIDs(compilation.ID, result)
	doc, raw, digest, err := core.BuildNormalizedRequirementDocument(result, complexStandardSHA(prd), reqMaps, accMaps)
	if err != nil {
		t.Fatalf("build ComplexStandard v1 document: %v", err)
	}
	compilation.NormalizedRequirementJSON, compilation.CompilationSHA256 = string(raw), digest
	return raw, complexStandardCompilation{Document: doc, Compilation: compilation, IDMaps: append(reqMaps, accMaps...)}
}

func complexStandardV2Compilation(t testing.TB, requirementID, versionID, prd string, previous core.NormalizedRequirementDocument, at time.Time) ([]byte, complexStandardCompilation) {
	t.Helper()
	result := complexStandardRequirementResult(versionID, true)
	compilation := core.ComplexCompilation{ID: "complex-standard-v2-compilation", DevelopmentRequirementID: requirementID, CompilationRequestID: "complex-standard-v2-compile-request", AgentStepID: "complex-standard-v2-compile-step", Outcome: "READY", Summary: result.Summary, TurnID: "complex-standard-v2-compile-step-turn", FinalMessageID: "complex-standard-v2-compile-step-message", RawMessageText: "ready v2", RawMessageSHA256: complexStandardSHA("ready v2"), CreatedAt: at}
	reqIDs, accIDs := core.StableIDsFromDocument(previous)
	reqMaps, accMaps, err := core.AssignStableCompilationIDsFromPrevious(compilation.ID, result, reqIDs, accIDs)
	if err != nil {
		t.Fatalf("assign ComplexStandard v2 identifiers: %v", err)
	}
	doc, raw, digest, err := core.BuildNormalizedRequirementDocument(result, complexStandardSHA(prd), reqMaps, accMaps)
	if err != nil {
		t.Fatalf("build ComplexStandard v2 document: %v", err)
	}
	compilation.NormalizedRequirementJSON, compilation.CompilationSHA256 = string(raw), digest
	return raw, complexStandardCompilation{Document: doc, Compilation: compilation, IDMaps: append(reqMaps, accMaps...)}
}

func complexStandardRequirementResult(versionID string, previous bool) core.RequirementCompilationResult {
	requirement := core.CompilationRequirement{Key: "mail-list", Priority: "MUST", Text: "规范化邮箱、去重并生成摘要。", AcceptanceKeys: []string{"normalized", "summary"}}
	acceptances := []core.CompilationAcceptance{{Key: "normalized", Text: "邮箱规范化和去重。"}, {Key: "summary", Text: "输出接受和拒绝统计。"}}
	if previous {
		requirement.PreviousRequirementID = "REQ-001"
		acceptances[0].PreviousAcceptanceID, acceptances[1].PreviousAcceptanceID = "ACC-001", "ACC-002"
	}
	return core.RequirementCompilationResult{SchemaVersion: core.ComplexProtocolVersion, Kind: "REQUIREMENT_COMPILATION", CompilationRequestID: "fixture", RequirementVersionID: versionID, CompilationContextSHA256: complexStandardSHA("fixture"), ClarificationRound: 1, Outcome: "READY", Summary: "邮件名单处理。", Requirements: []core.CompilationRequirement{requirement}, AcceptanceScenarios: acceptances, Constraints: []string{}, NonGoals: []string{}, Terms: []core.CompilationTerm{}, Assumptions: []string{}, Conflicts: []core.CompilationConflict{}, BlockingQuestions: []core.CompilationBlockingQuestion{}}
}

func complexStandardPlanAndReview(t testing.TB, store *sqlite.Store, requirementID, versionID, compilationSHA, plannerBindingID, stewardBindingID string, coverage core.ComplexCoverage, number int64, at time.Time, exception, mailBench, singleTask bool) (string, string) {
	t.Helper()
	requestID := fmt.Sprintf("complex-standard-plan-request-v%d", number)
	first := core.ComplexPlanTask{Key: "normalize-and-deduplicate", Title: "规范化和去重", Objective: "实现邮箱规范化与去重。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"}, WritePaths: []string{"src/email.js", "src/deduplicate.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"email-unit", "deduplicate-unit"}, DependencyKeys: []string{}}
	if exception {
		first.SharedPathsRequireApproval = []string{"package.json"}
		first.GeneratedPaths = []string{"package-lock.json"}
		first.RequiredCheckIDs = []string{"email-unit", "deduplicate-unit", "all-tests"}
	}
	tasks := []core.ComplexPlanTask{
		first,
		{Key: "build-summary", Title: "生成摘要", Objective: "输出接受和拒绝数量。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-002"}, WritePaths: []string{"src/summary.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"summary-unit"}, DependencyKeys: []string{"normalize-and-deduplicate"}},
	}
	integrationChecks := []string{"all-tests"}
	approach := "先规范化并去重，再生成摘要。"
	risks := []string{"摘要必须基于去重结果。"}
	parallel := core.ComplexParallelSuggestion{RecommendedBuilderCount: 1, Reason: "任务有明确依赖，使用一个 Builder。"}
	if singleTask {
		tasks = []core.ComplexPlanTask{
			{Key: "complete-confirmed-work", Title: "完成已确认需求", Objective: "在单一自然边界内覆盖全部 MUST 和验收场景。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001", "ACC-002"}, WritePaths: []string{"src/email.js", "src/deduplicate.js", "src/summary.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"email-unit", "deduplicate-unit", "summary-unit"}, DependencyKeys: []string{}},
		}
	}
	if mailBench {
		tasks = []core.ComplexPlanTask{
			{Key: "campaign-history", Title: "Campaign 历史", Objective: "实现 Campaign 历史和筛选。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"}, WritePaths: []string{"frontend/src/campaigns/history.ts", "frontend/src/campaigns/history.test.ts"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**", "package.json", "package-lock.json"}, RequiredCheckIDs: []string{"demo-frontend"}, DependencyKeys: []string{}},
			{Key: "campaign-core", Title: "Campaign 核心", Objective: "实现 Campaign 存储、接口和行为。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-002"}, WritePaths: []string{"backend/src/campaigns/schema.ts", "backend/src/campaigns/service.ts"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**", "package.json", "package-lock.json"}, RequiredCheckIDs: []string{"demo-backend", "demo-database", "demo-api"}, DependencyKeys: []string{}},
		}
		integrationChecks = []string{"demo-integration"}
		approach = "并行完成 Campaign 历史与核心，再做集成检查。"
		risks = []string{"Campaign 状态必须由持久事实重建。"}
		parallel = core.ComplexParallelSuggestion{RecommendedBuilderCount: 2, Reason: "两个任务路径不重叠，可以并行。"}
	}
	planInput := core.ComplexEngineeringPlanResult{SchemaVersion: core.ComplexProtocolVersion, Kind: "COMPLEX_ENGINEERING_PLAN", PlanningRequestID: requestID, RequirementVersionID: versionID, RequirementVersionSHA256: complexStandardRequirementSHA(t, store, requirementID, versionID), CompilationSHA256: compilationSHA, TechnicalApproach: approach, Tasks: tasks, IntegrationCheckIDs: integrationChecks, ParallelSuggestion: parallel, Risks: risks}
	raw, err := core.MarshalAgentChosenResult(planInput)
	if err != nil {
		t.Fatalf("marshal ComplexStandard plan: %v", err)
	}
	parsed, normalized, planSHA, err := core.ParseComplexEngineeringPlanResult(raw, requestID, versionID, planInput.RequirementVersionSHA256, compilationSHA, coverage)
	if err != nil {
		t.Fatalf("parse ComplexStandard plan: %v", err)
	}
	planID := fmt.Sprintf("complex-standard-plan-v%d", number)
	planStepID := fmt.Sprintf("complex-standard-plan-step-v%d", number)
	complexStandardSettleComplexStep(t, store, plannerBindingID, planStepID, core.ComplexAgentStepEngineeringPlan, requestID, at, string(normalized))
	if err := store.CreateClearDevComplexEngineeringPlan(context.Background(), core.CreateComplexPlanCommand{Plan: core.ComplexEngineeringPlan{ID: planID, PlanningRequestID: requestID, DevelopmentRequirementID: requirementID, RequirementVersionID: versionID, RequirementSHA256: planInput.RequirementVersionSHA256, CompilationSHA256: compilationSHA, Version: number, PlannerRoleBindingID: plannerBindingID, AgentStepID: planStepID, TurnID: planStepID + "-turn", FinalMessageID: planStepID + "-message", PlanJSON: string(normalized), PlanSHA256: planSHA, CreatedAt: at}}); err != nil {
		t.Fatalf("save ComplexStandard plan: %v", err)
	}
	reviewRequestID := fmt.Sprintf("complex-standard-review-request-v%d", number)
	reviewInput := core.ComplexPlanReviewResult{SchemaVersion: core.ComplexProtocolVersion, Kind: "COMPLEX_PLAN_REVIEW", ReviewRequestID: reviewRequestID, PlanID: planID, PlanSHA256: planSHA, Verdict: "APPROVED", ReasonCode: "PLAN_ACCEPTABLE", Summary: "覆盖需求和验收，允许执行。", Findings: []core.ComplexReviewFinding{}}
	reviewRaw, err := core.MarshalAgentChosenResult(reviewInput)
	if err != nil {
		t.Fatalf("marshal ComplexStandard review: %v", err)
	}
	taskKeys := make([]string, 0, len(parsed.Tasks))
	for _, task := range parsed.Tasks {
		taskKeys = append(taskKeys, task.Key)
	}
	_, normalizedReview, err := core.ParseComplexPlanReviewResult(reviewRaw, reviewRequestID, planID, planSHA, coverage, taskKeys)
	if err != nil {
		t.Fatalf("parse ComplexStandard review: %v", err)
	}
	reviewID := fmt.Sprintf("complex-standard-review-v%d", number)
	reviewStepID := fmt.Sprintf("complex-standard-review-step-v%d", number)
	findingsJSON, err := json.Marshal(reviewInput.Findings)
	if err != nil {
		t.Fatalf("marshal ComplexStandard review findings: %v", err)
	}
	complexStandardSettleComplexStep(t, store, stewardBindingID, reviewStepID, core.ComplexAgentStepPlanReview, reviewRequestID, at, string(normalizedReview))
	if err := store.RecordClearDevComplexPlanReview(context.Background(), core.RecordComplexPlanReviewCommand{Review: core.ComplexPlanReview{ID: reviewID, PlanID: planID, StewardRoleBindingID: stewardBindingID, AgentStepID: reviewStepID, ReviewRequestID: reviewRequestID, Verdict: core.PlanReviewApproved, ReasonCode: "PLAN_ACCEPTABLE", Summary: reviewInput.Summary, FindingsJSON: string(findingsJSON), PlanSHA256: planSHA, TurnID: reviewStepID + "-turn", FinalMessageID: reviewStepID + "-message", CreatedAt: at}, At: at}); err != nil {
		t.Fatalf("save ComplexStandard review: %v", err)
	}
	return planID, reviewID
}

func complexStandardSettleComplexStep(t testing.TB, store *sqlite.Store, bindingID, stepID string, kind core.AgentStepKind, requestID string, at time.Time, message string) {
	t.Helper()
	step := core.AgentStep{ID: stepID, RoleBindingID: bindingID, Kind: kind, RequestID: requestID, ClientMessageID: stepID + "-client", PromptSHA256: complexStandardSHA(stepID + "-prompt"), SendStatus: core.AgentStepSendStatusPending, RequestedAt: at}
	if _, created, err := store.CreateClearDevComplexAgentStep(context.Background(), step); err != nil || !created {
		t.Fatalf("create ComplexStandard agent step %s: created=%t err=%v", stepID, created, err)
	}
	sentAt := at.Add(time.Millisecond)
	if changed, err := store.MarkClearDevComplexAgentStepSent(context.Background(), stepID, sentAt); err != nil || !changed {
		t.Fatalf("send ComplexStandard agent step %s: changed=%t err=%v", stepID, changed, err)
	}
	completedAt := sentAt.Add(time.Millisecond)
	step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText = core.AgentStepSendStatusSettled, stepID+"-turn", stepID+"-message", message
	step.MessageSHA256, step.SentAt, step.CompletedAt = complexStandardSHA(message), &sentAt, &completedAt
	if changed, err := store.SettleClearDevComplexAgentStep(context.Background(), step); err != nil || !changed {
		t.Fatalf("settle ComplexStandard agent step %s: changed=%t err=%v", stepID, changed, err)
	}
}

func complexStandardSettleDirectionStep(t testing.TB, store *sqlite.Store, bindingID, stepID, requestID string, at time.Time, message string) {
	t.Helper()
	step := core.AgentStep{ID: stepID, RoleBindingID: bindingID, Kind: core.ComplexAgentStepCompilation, RequestID: requestID, ClientMessageID: stepID + "-client", PromptSHA256: complexStandardSHA(stepID + "-prompt"), SendStatus: core.AgentStepSendStatusPending, RequestedAt: at}
	if _, created, err := store.CreateClearDevDirectionAgentStep(context.Background(), step); err != nil || !created {
		t.Fatalf("create ComplexStandard direction step %s: created=%t err=%v", stepID, created, err)
	}
	sentAt := at.Add(time.Millisecond)
	if changed, err := store.MarkClearDevDirectionAgentStepSent(context.Background(), stepID, sentAt); err != nil || !changed {
		t.Fatalf("send ComplexStandard direction step %s: changed=%t err=%v", stepID, changed, err)
	}
	completedAt := sentAt.Add(time.Millisecond)
	step.SendStatus, step.TurnID, step.FinalMessageID, step.FinalMessageText = core.AgentStepSendStatusSettled, stepID+"-turn", stepID+"-message", message
	step.MessageSHA256, step.SentAt, step.CompletedAt = complexStandardSHA(message), &sentAt, &completedAt
	if changed, err := store.SettleClearDevDirectionAgentStep(context.Background(), step); err != nil || !changed {
		t.Fatalf("settle ComplexStandard direction step %s: changed=%t err=%v", stepID, changed, err)
	}
}

func complexStandardApproveDirection(t testing.TB, store *sqlite.Store, requirementID, v1ID, v1SHA, stewardBindingID string, at time.Time) string {
	t.Helper()
	ctx := context.Background()
	step := core.AgentStep{ID: "complex-standard-direction-step", RoleBindingID: stewardBindingID, Kind: core.DirectionAgentStepChange, RequestID: "complex-standard-direction-request", ClientMessageID: "complex-standard-direction-client", PromptSHA256: complexStandardSHA("direction-prompt"), SendStatus: core.AgentStepSendStatusPending, RequestedAt: at}
	intent := core.DirectionIntent{RequestID: "complex-standard-direction-intent", DevelopmentRequirementID: requirementID, RequirementVersionID: v1ID, RequirementSHA256: v1SHA, Message: "只接受 example.com 域名。", MessageSHA256: core.DirectionMessageSHA256("只接受 example.com 域名。"), StewardRoleBindingID: stewardBindingID, DirectionRequestID: step.RequestID, AgentStepID: step.ID, CreatedAt: at}
	if _, created, err := store.CreateClearDevDirectionIntent(ctx, core.CreateDirectionIntentCommand{Intent: intent, Step: step}); err != nil || !created {
		t.Fatalf("create ComplexStandard direction intent: created=%t err=%v", created, err)
	}
	if changed, err := store.MarkClearDevDirectionAgentStepSent(ctx, step.ID, at.Add(time.Second)); err != nil || !changed {
		t.Fatalf("send ComplexStandard direction step: changed=%t err=%v", changed, err)
	}
	settledAt := at.Add(2 * time.Second)
	if err := store.AcceptClearDevDirectionChange(ctx, core.AcceptDirectionChangeCommand{Intent: intent, Request: core.DirectionRequest{ID: step.RequestID, IntentRequestID: intent.RequestID, DevelopmentRequirementID: requirementID, RequirementVersionID: v1ID, Summary: "停止 v1 并建立 v2。", AffectedRequirementIDs: []string{"REQ-001"}, ResultSHA256: complexStandardSHA("direction-result"), AgentStepID: step.ID, CreatedAt: settledAt}, Gate: core.DirectionStopGate{ID: "complex-standard-v1-gate", CreatedAt: settledAt}, AgentStep: core.AgentStep{ID: step.ID, SendStatus: core.AgentStepSendStatusSettled, TurnID: "direction-turn", FinalMessageID: "direction-message", FinalMessageText: "accepted", MessageSHA256: complexStandardSHA("accepted"), CompletedAt: &settledAt}, DecisionRequestID: "complex-standard-direction-decision", At: settledAt}); err != nil {
		t.Fatalf("accept ComplexStandard direction: %v", err)
	}
	pending, err := store.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		t.Fatalf("list ComplexStandard decisions: %v", err)
	}
	requestID := ""
	for _, request := range pending {
		if request.DevelopmentRequirementID == requirementID && request.DecisionKind == core.HumanDecisionKindApproveDirectionChange {
			requestID = request.ID
		}
	}
	if requestID == "" {
		t.Fatal("ComplexStandard direction decision request missing")
	}
	nonce, err := humanauthority.NewToken()
	if err != nil {
		t.Fatalf("new ComplexStandard nonce: %v", err)
	}
	offer, err := store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: requestID, DesktopRunID: "complex-standard-desktop", Nonce: nonce, IssuedAt: at.Add(3 * time.Second), ExpiresAt: at.Add(3 * time.Second).Add(core.HumanDecisionOfferTTL)})
	if err != nil {
		t.Fatalf("issue ComplexStandard decision: %v", err)
	}
	if err := store.SettleClearDevHumanDecision(ctx, core.HumanDecisionResult{ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind, BindingSchemaVersion: offer.BindingSchemaVersion, Binding: append(json.RawMessage(nil), offer.Binding...), ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove}, at.Add(4*time.Second)); err != nil {
		t.Fatalf("approve ComplexStandard direction: %v", err)
	}
	return "complex-standard-v1-gate"
}

func complexStandardConfirm(t testing.TB, store *sqlite.Store, versionID string, at time.Time) {
	t.Helper()
	if err := store.ApplyClearDevAction(context.Background(), core.ActionRequest{Action: core.ActionConfirmRequirementVersion, SubjectID: versionID, TrustedHumanDecision: true, At: at}); err != nil {
		t.Fatalf("confirm ComplexStandard version %s: %v", versionID, err)
	}
}

func complexStandardSession(t testing.TB, store *sqlite.Store, projectID, repo, dataDir string, at time.Time, kind domain.SessionKind, branch, key string) domain.SessionRecord {
	t.Helper()
	session, err := store.CreateSession(context.Background(), domain.SessionRecord{ProjectID: domain.ProjectID(projectID), Kind: kind, Harness: domain.HarnessCodex, Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto, CreationIdempotencyKey: key, CreationRequestFingerprint: key, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: at}, Metadata: domain.SessionMetadata{Branch: "main", WorkspacePath: repo, WorkspaceRepoPath: repo, DiffBaseRef: "refs/heads/main"}, CreatedAt: at, UpdatedAt: at})
	if err != nil {
		t.Fatalf("create ComplexStandard %s session: %v", kind, err)
	}
	if branch == "" {
		return session
	}
	workspace, err := gitworktree.New(gitworktree.Options{ManagedRoot: filepath.Join(dataDir, "worktrees"), RepoResolver: gitworktree.StaticRepoResolver{domain.ProjectID(projectID): repo}})
	if err != nil {
		t.Fatalf("create ComplexStandard workspace adapter: %v", err)
	}
	info, err := workspace.Create(context.Background(), ports.WorkspaceConfig{ProjectID: domain.ProjectID(projectID), SessionID: session.ID, Kind: kind, Branch: branch, BaseBranch: "main"})
	if err != nil {
		t.Fatalf("create ComplexStandard planner worktree: %v", err)
	}
	session.Metadata.Branch, session.Metadata.WorkspacePath, session.Metadata.WorkspaceRepoPath, session.Metadata.DiffBaseRef = info.Branch, info.Path, info.RepoPath, info.BaseRef
	session.UpdatedAt = at.Add(time.Second)
	if err := store.UpdateSession(context.Background(), session); err != nil {
		t.Fatalf("save ComplexStandard planner workspace: %v", err)
	}
	return session
}

func complexStandardRequirementSHA(t testing.TB, store *sqlite.Store, requirementID, versionID string) string {
	t.Helper()
	snapshot, ok, err := store.GetClearDevRequirement(context.Background(), requirementID)
	if err != nil || !ok {
		t.Fatalf("load ComplexStandard requirement: ok=%t err=%v", ok, err)
	}
	for _, version := range snapshot.RequirementVersions {
		if version.ID == versionID {
			return version.SHA256
		}
	}
	t.Fatalf("ComplexStandard version %s missing", versionID)
	return ""
}

func complexStandardSHA(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
