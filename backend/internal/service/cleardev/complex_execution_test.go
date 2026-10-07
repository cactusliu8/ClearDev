package cleardev

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestComplexStandardExecutionRejectsUnsupportedPlan(t *testing.T) {
	plan := core.ComplexEngineeringPlan{PlanJSON: `{"parallelSuggestion":{"recommendedBuilderCount":4},"tasks":[]}`}
	plan.PlanSHA256 = coreDigest([]byte(plan.PlanJSON))
	if _, err := parseComplexExecutionPlan(plan); err == nil {
		t.Fatal("four-Builder complex plan was accepted")
	}
	plan.PlanJSON = `{"parallelSuggestion":{"recommendedBuilderCount":1},"tasks":[{"generatedPaths":["src/generated.js"],"sharedPathsRequireApproval":[]}]}`
	plan.PlanSHA256 = coreDigest([]byte(plan.PlanJSON))
	if _, err := parseComplexExecutionPlan(plan); err == nil {
		t.Fatal("generated-path complex plan was accepted")
	}
}

func TestComplexScopePathsPassChecksBothSidesOfRename(t *testing.T) {
	plan := core.ComplexPlanTask{WritePaths: []string{"src/**"}, ForbiddenPaths: []string{".git/**"}}
	if !complexScopePathsPass(plan, []ports.ClearDevDiffPath{{Status: "R", OldPath: "src/old.go", Path: "src/new.go"}}) {
		t.Fatal("rename within approved paths was rejected")
	}
	if complexScopePathsPass(plan, []ports.ClearDevDiffPath{{Status: "R", OldPath: ".git/config", Path: "src/new.go"}}) {
		t.Fatal("rename from forbidden old path was accepted")
	}
}

func TestNextComplexExecutionDispatchTaskAllowsReworksWithinBudget(t *testing.T) {
	snapshot := core.ComplexExecutionSnapshot{
		Exception: &core.ComplexExceptionFacts{Budgets: []core.ComplexExceptionBudget{{ID: "task:budget:builder", RoleKind: core.ComplexExceptionBudgetBuilder, ComplexExecutionTaskID: "task", MaxReworkCount: core.ComplexProjectMaxReworkCount}}},
		Tasks:     []core.ComplexExecutionTask{{ID: "task", TaskKey: "task", Status: core.DevelopmentTaskStatusRework, ReworkCount: 1, CurrentRound: 1}},
	}
	// Rounds one through the frozen budget stay eligible; the budget's own
	// count and any human-authorized recovery turn set the bound.
	for round := 1; round <= core.ComplexProjectMaxReworkCount; round++ {
		snapshot.Tasks[0].CurrentRound = round
		task, ok := nextComplexExecutionDispatchTask(snapshot)
		if !ok || task.ID != "task" {
			t.Fatalf("round %d rework task = %#v ok=%v", round, task, ok)
		}
	}
	snapshot.Tasks[0].CurrentRound = core.ComplexProjectMaxReworkCount + 1
	if _, ok := nextComplexExecutionDispatchTask(snapshot); ok {
		t.Fatal("a rework beyond the frozen budget was made eligible")
	}
	// A human-authorized recovery turn reopens exactly one more round.
	snapshot.Exception.Budgets[0].AuthorizedExtraTurns = 1
	if _, ok := nextComplexExecutionDispatchTask(snapshot); !ok {
		t.Fatal("the authorized recovery round was not eligible")
	}
	snapshot.Exception.Budgets[0].AuthorizedExtraTurns = 0
	if _, ok := nextComplexExecutionDispatchTask(snapshot); ok {
		t.Fatal("the recovery round stayed eligible without authorization")
	}
	// A generic execution without frozen budgets never re-dispatches reworks.
	bare := core.ComplexExecutionSnapshot{Tasks: []core.ComplexExecutionTask{{ID: "task", TaskKey: "task", Status: core.DevelopmentTaskStatusRework, ReworkCount: 1, CurrentRound: 1}}}
	if _, ok := nextComplexExecutionDispatchTask(bare); ok {
		t.Fatal("a rework without a frozen budget was made eligible")
	}
}

func TestActiveComplexExecutionDispatchSkipsVerifiedPredecessor(t *testing.T) {
	run := core.ComplexExecutionRun{ID: "run"}
	first := core.ComplexExecutionTask{ID: "first", ExecutionRunID: run.ID, Status: core.DevelopmentTaskStatusReview, CurrentRound: 0, CurrentDispatchID: "dispatch-first"}
	second := core.ComplexExecutionTask{ID: "second", ExecutionRunID: run.ID, Status: core.DevelopmentTaskStatusRunning, CurrentRound: 0, CurrentDispatchID: "dispatch-second"}
	snapshot := core.ComplexExecutionSnapshot{
		Run:        run,
		Tasks:      []core.ComplexExecutionTask{first, second},
		Dispatches: []core.ComplexExecutionDispatch{{ID: first.CurrentDispatchID, ComplexExecutionTaskID: first.ID}, {ID: second.CurrentDispatchID, ComplexExecutionTaskID: second.ID}},
		Verifications: []core.ComplexExecutionVerification{{
			ExecutionRunID: run.ID, ComplexExecutionTaskID: first.ID, DispatchID: first.CurrentDispatchID, Round: first.CurrentRound,
			CandidateCommitID: "candidate-first", CandidateCommitSHA: strings.Repeat("a", 40), ScopeEvidenceID: "scope-first",
			RequiredCheckRunIDs: []string{"check-first"}, LocalReviewID: "review-first",
		}},
	}

	task, dispatch, ok := activeComplexExecutionDispatch(snapshot)
	if !ok || task.ID != second.ID || dispatch.ID != second.CurrentDispatchID {
		t.Fatalf("active dispatch = task:%#v dispatch:%#v ok:%v", task, dispatch, ok)
	}
}

func TestComplexRequiredCheckUsesPersistedRunIDAfterStarted(t *testing.T) {
	store := &complexExecutionCheckStore{}
	runner := &complexExecutionCheckRunner{}
	service := &Service{complexExecution: store, checks: runner, now: func() time.Time { return time.Unix(1, 0).UTC() }}
	dispatch := core.ComplexExecutionDispatch{ID: "dispatch", CandidateCommitID: "candidate", CandidateCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BaseCommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	task := core.ComplexExecutionTask{ID: "task"}
	spec := core.ComplexExecutionCheckSpecFact{ID: "check-spec", ComplexExecutionTaskID: task.ID, Kind: core.CandidateCheckRequired, CheckID: "email-unit", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 30}
	execution := core.ComplexExecutionSnapshot{Run: core.ComplexExecutionRun{ID: "run"}, CheckRuns: []core.ComplexExecutionCheckRun{{ID: "check-run", DispatchID: dispatch.ID, CandidateCommitID: dispatch.CandidateCommitID, CheckSpecFactID: spec.ID, Status: core.ComplexExecutionCheckRunStarted}}}
	changed, err := service.ensureComplexRequiredCheck(context.Background(), execution, task, dispatch, "/builder", spec)
	if err != nil || !changed {
		t.Fatalf("required check changed=%v err=%v", changed, err)
	}
	if runner.request.RunID != "check-run" {
		t.Fatalf("runner RunID=%q, want persisted check-run", runner.request.RunID)
	}
	if store.settled.CheckRunID != "check-run" || store.settled.Result != core.EvidenceResultPass {
		t.Fatalf("settled=%#v", store.settled)
	}
}

func TestComplexExecutionTaskSetGateMovesToOneAfterMaterialization(t *testing.T) {
	if got := complexExecutionExpectedRequirementTaskSet(core.ComplexExecutionAwaitingSteward); got != 0 {
		t.Fatalf("pending task-set gate = %d, want 0", got)
	}
	for _, phase := range []core.ComplexExecutionPhase{core.ComplexExecutionBindingBuilder, core.ComplexExecutionReadyToDispatch, core.ComplexExecutionBuilding, core.ComplexExecutionReviewing, core.ComplexExecutionReworking} {
		if got := complexExecutionExpectedRequirementTaskSet(phase); got != core.ComplexStandardTaskSetVersion {
			t.Fatalf("materialized phase %s task-set gate = %d, want 1", phase, got)
		}
	}
}

func TestComplexStandardExecutionRetryMatchesOriginalRunAfterMaterialization(t *testing.T) {
	version := core.RequirementVersion{ID: "version-2", DevelopmentRequirementID: "requirement", Version: 2, SHA256: strings.Repeat("a", 64), TaskSetVersion: 1}
	plan := core.ComplexEngineeringPlan{ID: "plan", PlanSHA256: strings.Repeat("b", 64)}
	review := core.ComplexPlanReview{ID: "review", PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, Verdict: core.PlanReviewApproved}
	raw, digest, err := core.BuildComplexExecutionRunPackage(core.WorkModeStandard, "run", version.ID, version.SHA256, plan.ID, plan.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	execution := core.ComplexExecutionSnapshot{
		Run: core.ComplexExecutionRun{
			ID: "run", DevelopmentRequirementID: version.DevelopmentRequirementID, RequirementVersionID: version.ID,
			RequirementSHA256: version.SHA256, PlanID: plan.ID, PlanReviewID: review.ID, PlanSHA256: plan.PlanSHA256,
			Mode: core.WorkModeStandard, ModeReason: "ONE_BUILDER_REQUIRED", FixedBuilderCount: 1, ExpectedTaskSetVersion: 0, TaskSetVersion: 1,
			ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest,
		},
		Tasks: []core.ComplexExecutionTask{{ID: "task"}},
	}
	if !sameComplexExecutionRequest(execution, version, plan, review) {
		t.Fatal("materialized retry did not match the original execution")
	}
	version.TaskSetVersion = 0
	if sameComplexExecutionRequest(execution, version, plan, review) {
		t.Fatal("materialized execution matched an old task-set version")
	}
	execution.Tasks = nil
	if !sameComplexExecutionRequest(execution, version, plan, review) {
		t.Fatal("pending retry did not match the original execution")
	}
	execution.Run.PlanReviewID = "stale-review"
	if sameComplexExecutionRequest(execution, version, plan, review) {
		t.Fatal("stale plan review matched the current request")
	}
}

func TestComplexReviewPacketCarriesBothRenamePaths(t *testing.T) {
	task := core.ComplexExecutionTask{ID: "task", DevelopmentTaskID: "work", ExecutionPackageJSON: `{}`}
	dispatch := core.ComplexExecutionDispatch{ID: "dispatch", CandidateCommitID: "candidate", CandidateCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BaseCommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	raw, digest, paths, err := buildComplexExecutionReviewPacket("review", task, dispatch, core.ComplexExecutionSnapshot{}, []ports.ClearDevDiffPath{{Status: "R", OldPath: "src/old.go", Path: "src/new.go"}})
	if err != nil || digest != coreDigest(raw) {
		t.Fatalf("review packet digest=%q err=%v", digest, err)
	}
	if len(paths) != 2 || paths[0] != "src/old.go" || paths[1] != "src/new.go" {
		t.Fatalf("review paths=%#v", paths)
	}
}

type complexExecutionCheckStore struct {
	ComplexExecutionFactStore
	settled core.SettleComplexExecutionCheckCommand
}

func (s *complexExecutionCheckStore) SettleClearDevComplexExecutionCheckRun(_ context.Context, command core.SettleComplexExecutionCheckCommand) (bool, error) {
	s.settled = command
	return true, nil
}

type complexExecutionCheckRunner struct{ request ports.ClearDevCheckRequest }

func (r *complexExecutionCheckRunner) PrepareCandidateChecks(_ context.Context, request ports.ClearDevCheckPreflightRequest) (ports.ClearDevCheckEnvironment, error) {
	return ports.ClearDevCheckEnvironment{CandidateSHA: request.CandidateSHA, Image: request.Image, ImageID: "sha256:test", CheckEnvironmentID: "test-environment"}, nil
}

func (r *complexExecutionCheckRunner) RunCandidateCheck(_ context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	r.request = request
	return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckPass, ImageID: "sha256:test", ExitCode: 0, OutputSummary: "pass", OutputSHA256: coreDigest([]byte("pass"))}, nil
}

func TestComplexStandardExecutionPreviousVerifiedCandidateUsesLatestPredecessor(t *testing.T) {
	execution := core.ComplexExecutionSnapshot{
		Run: core.ComplexExecutionRun{ID: "run"},
		Tasks: []core.ComplexExecutionTask{
			{ID: "one", TaskKey: "one", Ordinal: 0, Status: core.DevelopmentTaskStatusReview, CurrentRound: 0, CurrentDispatchID: "d1"},
			{ID: "two", TaskKey: "two", Ordinal: 1, Status: core.DevelopmentTaskStatusReview, CurrentRound: 0, CurrentDispatchID: "d2"},
		},
		Verifications: []core.ComplexExecutionVerification{
			{ExecutionRunID: "run", ComplexExecutionTaskID: "one", DispatchID: "d1", Round: 0, CandidateCommitID: "c1", CandidateCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ScopeEvidenceID: "scope1", RequiredCheckRunIDs: []string{"check1"}, LocalReviewID: "review1"},
			{ExecutionRunID: "run", ComplexExecutionTaskID: "two", DispatchID: "d2", Round: 0, CandidateCommitID: "c2", CandidateCommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ScopeEvidenceID: "scope2", RequiredCheckRunIDs: []string{"check2"}, LocalReviewID: "review2"},
		},
	}
	got, ok := previousVerifiedCandidate(execution, 2)
	if !ok || got != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("previous verified candidate = %q ok=%v", got, ok)
	}
}

func TestComplexExecutionStewardTerminationCreatesOneDurableContinuation(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	store := &complexExecutionContinuationStore{}
	ao := &complexExecutionContinuationAO{record: domain.SessionRecord{ID: "source-session", IsTerminated: true}, found: true}
	ids := []string{"continuation-binding"}
	service := &Service{complexExecution: store, ao: ao, now: func() time.Time { return now }, newID: func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}}
	execution := core.ComplexExecutionSnapshot{
		Run:          core.ComplexExecutionRun{ID: "run", RequirementVersionID: "v2", PlanID: "plan", PlanSHA256: strings.Repeat("a", 64)},
		RoleBindings: []core.ComplexExecutionRoleBinding{{ID: "original-steward", ExecutionRunID: "run", SourceComplexRoleBindingID: "source-steward", Role: core.StandardRoleSteward, AOSessionID: "source-session", Status: core.RoleBindingStatusBound}},
	}
	planning := core.ComplexPlanningSnapshot{RoleBindings: []core.ComplexRoleBinding{{ID: "source-steward", AOSessionID: "source-session", Role: core.StandardRoleSteward, Status: core.RoleBindingStatusBound}}}
	requirement := core.RequirementSnapshot{Requirement: core.DevelopmentRequirement{AOProjectID: "project"}}
	version := core.RequirementVersion{ID: "v2", SHA256: strings.Repeat("b", 64)}
	plan := core.ComplexEngineeringPlan{ID: "plan", PlanSHA256: strings.Repeat("a", 64)}
	review := core.ComplexPlanReview{ID: "review", PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, Verdict: core.PlanReviewApproved}
	progressed, done, err := service.advanceComplexExecutionSteward(context.Background(), execution, planning, requirement, version, plan, review)
	if err != nil || !progressed || done {
		t.Fatalf("continuation advance = progressed:%t done:%t err:%v", progressed, done, err)
	}
	if store.priorID != "original-steward" || store.binding.ID != "continuation-binding" || store.binding.ContinuationOfRoleBindingID != "original-steward" || store.binding.SourceComplexRoleBindingID != "source-steward" || store.binding.Status != core.RoleBindingStatusRequested {
		t.Fatalf("continuation binding = prior:%q binding:%#v", store.priorID, store.binding)
	}
	if store.step.ID != "" {
		t.Fatalf("continuation created an agent step before preflight: %#v", store.step)
	}
}

type complexExecutionContinuationStore struct {
	ComplexExecutionFactStore
	priorID string
	binding core.ComplexExecutionRoleBinding
	step    core.AgentStep
}

func (s *complexExecutionContinuationStore) ContinueClearDevComplexExecutionSteward(_ context.Context, priorID string, binding core.ComplexExecutionRoleBinding, step core.AgentStep, _ time.Time) (bool, error) {
	s.priorID, s.binding, s.step = priorID, binding, step
	return true, nil
}

type complexExecutionContinuationAO struct {
	AOReader
	record domain.SessionRecord
	found  bool
	err    error
}

func (a *complexExecutionContinuationAO) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	return a.record, a.found, a.err
}

func TestComplexQuickStewardTerminationCreatesOneDurableContinuation(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	store := &complexQuickContinuationStore{}
	ao := &complexExecutionContinuationAO{record: domain.SessionRecord{ID: "source-session", IsTerminated: true}, found: true}
	ids := []string{"continuation-binding"}
	service := &Service{complexExecution: store, ao: ao, now: func() time.Time { return now }, newID: func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}}
	snapshot := core.ComplexQuickSnapshot{
		Run:          core.ComplexQuickRun{ID: "run", RequirementVersionID: "v2", PlanID: "plan", PlanSHA256: strings.Repeat("a", 64)},
		RoleBindings: []core.ComplexExecutionRoleBinding{{ID: "original-steward", ExecutionRunID: "run", SourceComplexRoleBindingID: "source-steward", Role: core.StandardRoleSteward, AOSessionID: "source-session", Status: core.RoleBindingStatusBound}},
	}
	planning := core.ComplexPlanningSnapshot{RoleBindings: []core.ComplexRoleBinding{{ID: "source-steward", AOSessionID: "source-session", Role: core.StandardRoleSteward, Status: core.RoleBindingStatusBound}}}
	requirement := core.RequirementSnapshot{Requirement: core.DevelopmentRequirement{AOProjectID: "project"}}
	version := core.RequirementVersion{ID: "v2", SHA256: strings.Repeat("b", 64)}
	plan := core.ComplexEngineeringPlan{ID: "plan", PlanSHA256: strings.Repeat("a", 64)}
	review := core.ComplexPlanReview{ID: "review", PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, Verdict: core.PlanReviewApproved}
	progressed, done, err := service.advanceComplexQuickSteward(context.Background(), snapshot, planning, requirement, version, plan, review, core.ComplexExecutionSnapshot{}, core.ComplexEngineeringPlanResult{})
	if err != nil || !progressed || done {
		t.Fatalf("continuation advance = progressed:%t done:%t err:%v", progressed, done, err)
	}
	if store.priorID != "original-steward" || store.binding.ID != "continuation-binding" || store.binding.ContinuationOfRoleBindingID != "original-steward" || store.binding.Status != core.RoleBindingStatusRequested {
		t.Fatalf("continuation binding = prior:%q binding:%#v", store.priorID, store.binding)
	}
	if store.step.ID != "" {
		t.Fatalf("continuation created an agent step before preflight: %#v", store.step)
	}
}

type complexQuickContinuationStore struct {
	ComplexExecutionFactStore
	priorID string
	binding core.ComplexExecutionRoleBinding
	step    core.AgentStep
}

func (s *complexQuickContinuationStore) ContinueClearDevComplexQuickSteward(_ context.Context, priorID string, binding core.ComplexExecutionRoleBinding, step core.AgentStep, _ time.Time) (bool, error) {
	s.priorID, s.binding, s.step = priorID, binding, step
	return true, nil
}

func TestMailCheckFailureFeedbackKeepsAssertionFromNpmTest(t *testing.T) {
	dispatch := core.ComplexExecutionDispatch{ID: "d1", ComplexExecutionTaskID: "task", Round: 0, ReasonCode: "CHECK_FAILED"}
	snapshot := core.ComplexExecutionSnapshot{
		Tasks:      []core.ComplexExecutionTask{{ID: "task", CurrentRound: 1}},
		Dispatches: []core.ComplexExecutionDispatch{dispatch},
		CheckRuns: []core.ComplexExecutionCheckRun{{
			DispatchID: dispatch.ID, Kind: core.CandidateCheckRequired, Status: core.ComplexExecutionCheckRunSettled,
			Result: core.EvidenceResultFail, ReasonCode: "CHECK_FAILED",
			OutputSummary: "ok 1\nnot ok 7 - display sorting\n  location: '/workspace/test/app.test.js:134:1'\n  +   'z@example.com (example.com)'\n  -   'z@example.com'\n",
		}},
	}
	got := complexExecutionSavedReworkFeedback(snapshot, snapshot.Tasks[0])
	if !strings.Contains(got, "CHECK_FAILED") || !strings.Contains(got, "z@example.com (example.com)") || !strings.Contains(got, "test/app.test.js") {
		t.Fatalf("feedback omitted check output: %q", got)
	}
	if strings.HasPrefix(got, "ok 1") {
		t.Fatal("passing TAP prefix was kept instead of the failing assertion")
	}
}

func TestProjectCheckFailureFeedbackShowsCommandErrorAfterReceiptMetadata(t *testing.T) {
	dispatch := core.ComplexExecutionDispatch{ID: "d1", ComplexExecutionTaskID: "task", Round: 0, ReasonCode: "CHECK_FAILED"}
	log := "[ClearDev isolated project command 1/2]\n> npm run test:e2e\nError: EACCES: permission denied, mkdir '/workspace/test/artifacts/run-123/runtime'\n    at file:///workspace/scripts/e2e.mjs:8:1\n"
	receipt := `{"schemaVersion":1,"policy":"PROJECT_CHECK_V1","contractSha256":"` + strings.Repeat("a", 1600) + `","outputSummary":"[ClearDev isolated project command 1/2]\n> npm run test:e2e\nError: EACCES: permission denied, mkdir '/workspace/test/artifacts/run-123/runtime'\n    at file:///workspace/scripts/e2e.mjs:8:1\n"}`
	snapshot := core.ComplexExecutionSnapshot{
		Tasks:      []core.ComplexExecutionTask{{ID: "task", CurrentRound: 1}},
		Dispatches: []core.ComplexExecutionDispatch{dispatch},
		CheckRuns: []core.ComplexExecutionCheckRun{{
			DispatchID: dispatch.ID, Kind: core.CandidateCheckRequired, Status: core.ComplexExecutionCheckRunSettled,
			Result: core.EvidenceResultFail, ReasonCode: "CHECK_FAILED", OutputSummary: receipt,
		}},
	}
	got := complexExecutionSavedReworkFeedback(snapshot, snapshot.Tasks[0])
	if !strings.Contains(got, strings.TrimSpace(log)) || strings.Contains(got, strings.Repeat("a", 100)) {
		t.Fatalf("Builder feedback omitted the trusted command error or included receipt metadata: %q", got)
	}
}

func TestProjectCheckFailureFeedbackClipsCommandOutput(t *testing.T) {
	receipt := `{"schemaVersion":1,"policy":"PROJECT_CHECK_V1","outputSummary":"` + strings.Repeat("x", 1700) + `not ok 2 - failed assertion\n` + strings.Repeat("y", 1700) + `"}`
	got := clipMailCheckFailureOutput(receipt)
	if !strings.HasPrefix(got, "not ok 2 - failed assertion\n") || len([]rune(got)) > 1504 {
		t.Fatalf("Builder feedback did not focus and bound the failed command output: %q", got)
	}
}

func TestProjectCheckFailureFeedbackLeavesUnrelatedReceiptsUnchanged(t *testing.T) {
	for _, summary := range []string{
		`{"schemaVersion":1,"policy":"OTHER_CHECK","outputSummary":"invented failure"}`,
		`{"schemaVersion":2,"policy":"PROJECT_CHECK_V1","outputSummary":"invented failure"}`,
		`{"schemaVersion":1,"policy":"PROJECT_CHECK_V1","outputSummary":null}`,
		`{"schemaVersion":1,"policy":"PROJECT_CHECK_V1","outputSummary":7}`,
		`{"schemaVersion":1,"policy":"PROJECT_CHECK_V1","outputSummary":`,
	} {
		if got, want := clipMailCheckFailureOutput(summary), clipMailCheckFailureOutputLegacy(summary); got != want {
			t.Fatalf("unrelated or malformed receipt changed feedback: got %q, want %q", got, want)
		}
	}
}
