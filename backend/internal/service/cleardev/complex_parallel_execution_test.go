package cleardev_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestComplexParallelExecutionCompletesThreeTaskTwoBuilderFlow(t *testing.T) {
	store, harness, service, prep := newComplexParallelService(t, false)
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	assertComplexParallelCompleted(t, view, harness)
	harness.mu.Lock()
	if len(harness.preflightRequests) != 2 {
		harness.mu.Unlock()
		t.Fatalf("parallel check preflights = %d, want one per Builder binding", len(harness.preflightRequests))
	}
	for _, request := range harness.preflightRequests {
		if request.RunID == "" || request.CandidateSHA != view.ComplexExecution.Run.InitialBaseCommitSHA || request.Image != core.StandardCandidateCheckImage {
			harness.mu.Unlock()
			t.Fatalf("parallel check preflight = %#v", request)
		}
	}
	harness.mu.Unlock()
	retried, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ComplexExecution == nil || retried.ComplexExecution.Run.ID != view.ComplexExecution.Run.ID {
		t.Fatalf("idempotent retry changed the run: %#v", retried.ComplexExecution)
	}
	harness.mu.Lock()
	missingReleases := []string{}
	for _, request := range harness.preflightRequests {
		released := false
		for _, runID := range harness.checkReleaseIDs {
			released = released || runID == request.RunID
		}
		if !released {
			missingReleases = append(missingReleases, request.RunID)
		}
	}
	if len(missingReleases) != 0 {
		harness.mu.Unlock()
		t.Fatalf("completed PARALLEL flow did not release %#v: releases=%#v run=%q", missingReleases, harness.checkReleaseIDs, retried.ComplexExecution.Run.ID)
	}
	harness.mu.Unlock()
	snapshot, ok, err := store.GetClearDevComplexExecution(context.Background(), prep.RequirementID)
	if err != nil || !ok || snapshot.Run.ID != view.ComplexExecution.Run.ID {
		t.Fatalf("durable retry read = ok:%t err:%v run:%s", ok, err, snapshot.Run.ID)
	}
}

func TestMailV2ParallelCompletesOnlyAfterFinalCandidateReview(t *testing.T) {
	store, harness, service, prep := newMailComplexParallelService(t, false)
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64 && (view.ComplexExecution == nil || view.ComplexExecution.Phase != core.ComplexExecutionCompleted); i++ {
		if err := service.ResumeComplexStandardExecutions(context.Background()); err != nil {
			t.Fatal(err)
		}
		view, err = service.GetRequirement(context.Background(), prep.RequirementID)
		if err != nil {
			t.Fatal(err)
		}
		if view.ComplexExecution != nil && (view.ComplexExecution.Phase == core.ComplexExecutionBlocked || view.ComplexExecution.Phase == core.ComplexExecutionNeedsHuman) {
			break
		}
	}
	execution := view.ComplexExecution
	if execution == nil || execution.Phase != core.ComplexExecutionCompleted || execution.Integration == nil || execution.FinalReview == nil {
		t.Fatalf("mail V2 parallel completion = %#v", execution)
	}
	policy, base, required, err := core.MailDeliveryPolicyFromRun(execution.Run)
	if err != nil || !required || policy != core.MailDeliveryPolicyV2 || base != forty("a") {
		t.Fatalf("mail V2 policy=%q base=%q required=%t err=%v", policy, base, required, err)
	}
	if execution.Run.Mode != core.WorkModeParallel || execution.Run.FixedBuilderCount != 2 || len(execution.Tasks) != 3 || len(execution.Verifications) != 3 || len(execution.Reviews) != 3 {
		t.Fatalf("mail V2 execution shape = %#v", execution.Run)
	}
	if len(execution.Compositions) != 2 {
		t.Fatalf("mail V2 compositions = %#v", execution.Compositions)
	}
	firstComposition := execution.Compositions[0]
	finalComposition := execution.Compositions[len(execution.Compositions)-1]
	if execution.Integration.CandidateCommitSHA != finalComposition.OutputCommitSHA ||
		execution.FinalReview.CandidateCommitSHA != finalComposition.OutputCommitSHA ||
		execution.FinalReview.Status != "SETTLED" || execution.FinalReview.Verdict != "PASS" {
		t.Fatalf("mail V2 final F/review mismatch: integration=%#v finalReview=%#v final=%#v", execution.Integration, execution.FinalReview, finalComposition)
	}
	byKey := map[string]core.ComplexExecutionTask{}
	for _, task := range execution.Tasks {
		byKey[task.TaskKey] = task
	}
	dispatchByTask := map[string]core.ComplexExecutionDispatch{}
	for _, dispatch := range execution.Dispatches {
		dispatchByTask[dispatch.ComplexExecutionTaskID] = dispatch
	}
	followup := dispatchByTask[byKey["frontend-followup"].ID]
	if followup.BaseCommitSHA != firstComposition.OutputCommitSHA {
		t.Fatalf("dependent task base=%s want composed=%s", followup.BaseCommitSHA, firstComposition.OutputCommitSHA)
	}
	for _, verification := range execution.Verifications {
		dispatch := dispatchByTask[verification.ComplexExecutionTaskID]
		var scope core.MailScopeProof
		for _, check := range execution.CheckRuns {
			if check.ID == verification.ScopeEvidenceID {
				if err := json.Unmarshal([]byte(check.OutputSummary), &scope); err != nil {
					t.Fatal(err)
				}
			}
		}
		if scope.Policy != core.MailDeliveryPolicyV2 || scope.BaseSHA != dispatch.BaseCommitSHA || scope.CandidateSHA != verification.CandidateCommitSHA {
			t.Fatalf("task-local mail V2 scope=%#v dispatch=%#v verification=%#v", scope, dispatch, verification)
		}
	}
	var finalCheck core.ComplexExecutionCheckRun
	for _, check := range execution.CheckRuns {
		if check.Kind == core.CandidateCheckIntegration && check.CandidateCommitSHA == finalComposition.OutputCommitSHA {
			finalCheck = check
		}
	}
	if finalCheck.ID == "" {
		t.Fatal("mail V2 final F has no integration check")
	}
	if err := core.ValidateMailDeliveryProofForPolicy(finalCheck.OutputSummary, core.MailDeliveryPolicyV2, base, finalComposition.OutputCommitSHA, finalCheck.ID, finalCheck.ContainerImageID); err != nil {
		t.Fatal(err)
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if harness.freezeCalls != 3 || harness.mailDeliveryCalls != 1 {
		t.Fatalf("trusted freezes=%d final deliveries=%d, want 3 task freezes and one final F delivery", harness.freezeCalls, harness.mailDeliveryCalls)
	}
	builders, finalReviewers := 0, 0
	for _, cfg := range harness.spawnConfigs {
		if strings.Contains(cfg.Branch, "cleardev-complex-builder-") {
			builders++
		}
		if strings.Contains(cfg.Branch, "cleardev-requirement-final-review-") {
			finalReviewers++
		}
	}
	if builders != 2 || finalReviewers != 1 {
		t.Fatalf("mail V2 spawns builders=%d finalReviewers=%d", builders, finalReviewers)
	}
	replay := core.CompleteComplexExecutionCommand{ExecutionRunID: execution.Run.ID, Integration: *execution.Integration, At: time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC)}
	if err := store.CompleteClearDevComplexExecution(context.Background(), replay); err != nil {
		t.Fatalf("mail V2 completed F did not replay idempotently: %v", err)
	}
}

func TestMailV2ParallelCompositionConflictStopsBeforeFinalReview(t *testing.T) {
	_, harness, service, prep := newMailComplexParallelService(t, true)
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64 && (view.ComplexExecution == nil || (view.ComplexExecution.Phase != core.ComplexExecutionBlocked && view.ComplexExecution.Phase != core.ComplexExecutionCompleted)); i++ {
		if err := service.ResumeComplexStandardExecutions(context.Background()); err != nil {
			t.Fatal(err)
		}
		view, err = service.GetRequirement(context.Background(), prep.RequirementID)
		if err != nil {
			t.Fatal(err)
		}
	}
	execution := view.ComplexExecution
	if execution == nil || execution.Phase != core.ComplexExecutionBlocked || execution.PhaseReason != core.ReasonCompositionConflict {
		t.Fatalf("mail V2 conflict did not stop safely: %#v", execution)
	}
	if execution.Integration != nil || execution.FinalReview != nil || len(execution.Compositions) != 1 ||
		execution.Compositions[0].Status != core.ComplexExecutionCompositionBlocked ||
		len(execution.Compositions[0].ConflictPaths) == 0 {
		t.Fatalf("mail V2 conflict lost scene or reached final gates: %#v", execution)
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	for _, cfg := range harness.spawnConfigs {
		if strings.Contains(cfg.Branch, "cleardev-requirement-final-review-") {
			t.Fatal("composition conflict incorrectly spawned requirement final reviewer")
		}
	}
	if harness.mailDeliveryCalls != 0 {
		t.Fatalf("composition conflict incorrectly ran final F delivery %d time(s)", harness.mailDeliveryCalls)
	}
}

func TestComplexParallelCheckPreflightStopsBeforeBuilderFanout(t *testing.T) {
	_, harness, service, prep := newComplexParallelService(t, false)
	harness.preflightErr = errors.New("offline dependency environment is unavailable")
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ComplexExecution == nil || view.ComplexExecution.Phase != core.ComplexExecutionBlocked || len(view.ComplexExecution.Dispatches) != 0 {
		t.Fatalf("preflight stop = %#v", view.ComplexExecution)
	}
	failed := 0
	for _, binding := range view.ComplexExecution.RoleBindings {
		if binding.Role == core.StandardRoleBuilder && binding.Status == core.RoleBindingStatusFailed && binding.ReasonCode == core.ReasonCode("CHECKER_UNAVAILABLE") {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed preflight Builder bindings = %d, want one", failed)
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if len(harness.preflightRequests) != 1 {
		t.Fatalf("preflight requests = %d, want stop on first Builder", len(harness.preflightRequests))
	}
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) {
			t.Fatalf("Builder received a task after failed preflight: %#v", relay)
		}
	}
}

func TestComplexParallelExecutionDegradesRecommendedOneToStandard(t *testing.T) {
	_, harness, service, prep := newComplexStandardExecutionService(t)
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ComplexExecution == nil || view.ComplexExecution.Phase != core.ComplexExecutionCompleted {
		t.Fatalf("STANDARD degrade phase = %#v", view.ComplexExecution)
	}
	run := view.ComplexExecution.Run
	if run.Mode != core.WorkModeStandard || run.FixedBuilderCount != 1 || run.ModeReason != string(core.ReasonOneBuilderRequired) {
		t.Fatalf("STANDARD degrade mode = %#v", run)
	}
	if len(view.ComplexExecution.Tasks) != 2 || len(view.ComplexExecution.Batches) != 0 || len(view.ComplexExecution.Compositions) != 0 {
		t.Fatalf("STANDARD degrade graph = tasks:%d batches:%d compositions:%d", len(view.ComplexExecution.Tasks), len(view.ComplexExecution.Batches), len(view.ComplexExecution.Compositions))
	}
	dispatches := map[string]core.ComplexExecutionDispatch{}
	for _, dispatch := range view.ComplexExecution.Dispatches {
		dispatches[dispatch.ComplexExecutionTaskID] = dispatch
	}
	var first, second core.ComplexExecutionTask
	for _, task := range view.ComplexExecution.Tasks {
		if task.Ordinal == 0 {
			first = task
		}
		if task.Ordinal == 1 {
			second = task
		}
	}
	if dispatches[second.ID].BaseCommitSHA != dispatches[first.ID].CandidateCommitSHA {
		t.Fatalf("STANDARD successor base = %s, want predecessor candidate %s", dispatches[second.ID].BaseCommitSHA, dispatches[first.ID].CandidateCommitSHA)
	}
	builders, reviewers := 0, 0
	for _, cfg := range harness.spawnConfigs {
		if strings.Contains(cfg.Branch, "cleardev-complex-builder-") {
			builders++
		}
		if strings.Contains(cfg.Branch, "cleardev-complex-review-") {
			reviewers++
		}
	}
	if builders != 1 || reviewers != 2 {
		t.Fatalf("STANDARD spawn counts builders=%d reviewers=%d", builders, reviewers)
	}
}

func TestComplexSingleTaskExecutionCompletesStandardFlow(t *testing.T) {
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initParallelPrepRepo(t)
	prep := cleardevtest.SeedComplexSingleTaskV2(t, store, "ao-s12b-single-task", repo, dataDir)
	harness := newParallelAgentHarness(store, false)
	service := parallelTestService(store, harness)
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	execution := view.ComplexExecution
	if execution == nil || execution.Phase != core.ComplexExecutionCompleted {
		t.Fatalf("single-task execution = %#v", execution)
	}
	run := execution.Run
	if run.Mode != core.WorkModeStandard || run.FixedBuilderCount != 1 || run.ModeReason != string(core.ReasonOneBuilderRequired) {
		t.Fatalf("single-task mode = %#v", run)
	}
	if len(execution.Tasks) != 1 || len(execution.Batches) != 0 || len(execution.Compositions) != 0 || len(execution.Reviews) != 1 {
		t.Fatalf("single-task graph = tasks:%d batches:%d compositions:%d reviews:%d", len(execution.Tasks), len(execution.Batches), len(execution.Compositions), len(execution.Reviews))
	}
	if execution.Tasks[0].Status != core.DevelopmentTaskStatusDone {
		t.Fatalf("single task = %#v", execution.Tasks[0])
	}
	builders, reviewers := 0, 0
	for _, cfg := range harness.spawnConfigs {
		if strings.Contains(cfg.Branch, "cleardev-complex-builder-") {
			builders++
		}
		if strings.Contains(cfg.Branch, "cleardev-complex-review-") {
			reviewers++
		}
	}
	if builders != 1 || reviewers != 1 {
		t.Fatalf("single-task spawn counts builders=%d reviewers=%d", builders, reviewers)
	}
}

func TestControlledExceptionScopeExpansionGeneratedProofSpecialistRecoveryCompletesFromEmptyBody(t *testing.T) {
	store, harness, service, prep := newControlledExceptionService(t)
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	execution := view.ComplexExecution
	if execution == nil || execution.Phase != core.ComplexExecutionCompleted || execution.Exception == nil {
		t.Fatalf("controlled exception completion = %#v", execution)
	}
	if execution.Run.Mode != core.WorkModeStandard || execution.Run.FixedBuilderCount != 1 {
		t.Fatalf("exception mode = %#v", execution.Run)
	}
	specialists, recoveries, live := 0, 0, 0
	for _, binding := range execution.Exception.OnDemandBindings {
		switch binding.Mode {
		case core.ComplexOnDemandModeSpecialist:
			specialists++
		case core.ComplexOnDemandModeRecovery:
			recoveries++
		}
		if binding.Status == core.RoleBindingStatusRequested || binding.Status == core.RoleBindingStatusBound {
			live++
		}
	}
	if specialists != 1 || recoveries != 1 || live != 0 {
		t.Fatalf("on-demand roles specialists=%d recoveries=%d live=%d bindings=%#v", specialists, recoveries, live, execution.Exception.OnDemandBindings)
	}
	if len(execution.Exception.ScopeRequests) != 1 || len(execution.Exception.ScopeDecisions) != 1 || !execution.Exception.ScopeDecisions[0].ControlAccepted {
		t.Fatalf("scope facts requests=%#v decisions=%#v", execution.Exception.ScopeRequests, execution.Exception.ScopeDecisions)
	}
	maxPermission := int64(0)
	for _, permission := range execution.Exception.PermissionVersions {
		if permission.Version > maxPermission {
			maxPermission = permission.Version
		}
	}
	if maxPermission < 2 {
		t.Fatalf("permission versions = %#v", execution.Exception.PermissionVersions)
	}
	if len(execution.Exception.GeneratedProofs) != 1 || execution.Exception.GeneratedProofs[0].Result != core.EvidenceResultPass {
		t.Fatalf("generated proofs = %#v", execution.Exception.GeneratedProofs)
	}
	injected, retried := 0, 0
	for _, run := range execution.CheckRuns {
		if run.Kind != core.CandidateCheckIntegration {
			continue
		}
		if run.RetryOrdinal == 0 && strings.Contains(run.OutputSummary, "INJECTED_INFRASTRUCTURE_FAILURE") {
			injected++
		}
		if run.RetryOrdinal == 1 && run.Status == core.ComplexExecutionCheckRunSettled && run.Result == core.EvidenceResultPass {
			if strings.Contains(run.OutputSummary, "INJECTED_INFRASTRUCTURE_FAILURE") {
				t.Fatal("retry check reused the injected failure summary")
			}
			retried++
		}
	}
	if injected != 1 || retried != 1 {
		t.Fatalf("integration checks injected=%d retried=%d runs=%#v", injected, retried, execution.CheckRuns)
	}
	if len(execution.Exception.RecoveryActions) != 1 || execution.Exception.RecoveryActions[0].Action != core.ComplexRecoveryActionRetryInfraCheck {
		t.Fatalf("recovery actions = %#v", execution.Exception.RecoveryActions)
	}
	if harness.integrationChecks < 2 {
		t.Fatalf("integration check calls = %d, want at least 2", harness.integrationChecks)
	}
	retriedView, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if retriedView.ComplexExecution == nil || retriedView.ComplexExecution.Run.ID != execution.Run.ID {
		t.Fatalf("idempotent retry changed the run: %#v", retriedView.ComplexExecution)
	}
	snapshot, ok, err := store.GetClearDevComplexExecution(context.Background(), prep.RequirementID)
	if err != nil || !ok || snapshot.Run.ID != execution.Run.ID {
		t.Fatalf("durable retry read = ok:%t err:%v", ok, err)
	}
}

func TestComplexParallelExecutionCompositionConflictBlocks(t *testing.T) {
	_, harness, service, prep := newComplexParallelService(t, true)
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ComplexExecution == nil || view.ComplexExecution.Phase != core.ComplexExecutionBlocked {
		t.Fatalf("conflict phase = %#v", view.ComplexExecution)
	}
	if view.OverallProgress.Phase == core.OverallPhaseCompleted || view.ComplexExecution.Integration != nil {
		t.Fatalf("conflict completed the run: %#v", view.ComplexExecution)
	}
	for _, task := range view.ComplexExecution.Tasks {
		if task.Status == core.DevelopmentTaskStatusDone {
			t.Fatalf("conflict marked %s DONE", task.TaskKey)
		}
	}
	if len(view.ComplexExecution.Compositions) != 1 || view.ComplexExecution.Compositions[0].Status != core.ComplexExecutionCompositionBlocked {
		t.Fatalf("conflict composition = %#v", view.ComplexExecution.Compositions)
	}
	if harness.composeCalls == 0 {
		t.Fatal("conflict path never called ComposeCandidates")
	}
}

func assertComplexParallelCompleted(t *testing.T, view cleardevsvc.RequirementView, harness *parallelAgentHarness) {
	t.Helper()
	execution := view.ComplexExecution
	if execution == nil || execution.Phase != core.ComplexExecutionCompleted || execution.Integration == nil || execution.Run.CompletedAt == nil {
		t.Fatalf("PARALLEL completion = %#v", execution)
	}
	if execution.Run.Mode != core.WorkModeParallel || execution.Run.FixedBuilderCount != 2 || execution.Run.ModeReason != string(core.ReasonParallelPlanApproved) {
		t.Fatalf("PARALLEL mode = %#v", execution.Run)
	}
	if len(execution.Tasks) != 3 || len(execution.Batches) != 2 || len(execution.Compositions) != 2 || len(execution.Reviews) != 3 {
		t.Fatalf("PARALLEL graph tasks=%d batches=%d compositions=%d reviews=%d", len(execution.Tasks), len(execution.Batches), len(execution.Compositions), len(execution.Reviews))
	}
	byKey := map[string]core.ComplexExecutionTask{}
	for _, task := range execution.Tasks {
		byKey[task.TaskKey] = task
		if task.Status != core.DevelopmentTaskStatusDone {
			t.Fatalf("task %s = %#v", task.TaskKey, task)
		}
	}
	if byKey["normalize-email"].ID == "" || byKey["deduplicate-email"].ID == "" || byKey["build-summary"].ID == "" {
		t.Fatalf("PARALLEL task keys = %#v", execution.Tasks)
	}
	dispatches := map[string]core.ComplexExecutionDispatch{}
	for _, dispatch := range execution.Dispatches {
		dispatches[dispatch.ComplexExecutionTaskID] = dispatch
	}
	firstCompose := execution.Compositions[0]
	if firstCompose.Status != core.ComplexExecutionCompositionComposed || firstCompose.OutputCommitSHA == "" {
		t.Fatalf("wave-0 composition = %#v", firstCompose)
	}
	summary := dispatches[byKey["build-summary"].ID]
	if summary.BaseCommitSHA != firstCompose.OutputCommitSHA {
		t.Fatalf("wave-1 base = %s, want composed %s", summary.BaseCommitSHA, firstCompose.OutputCommitSHA)
	}
	final := execution.Compositions[len(execution.Compositions)-1]
	if execution.Integration.CandidateCommitSHA != final.OutputCommitSHA {
		t.Fatalf("integration SHA = %s, want composed %s", execution.Integration.CandidateCommitSHA, final.OutputCommitSHA)
	}
	if execution.FinalReview == nil || execution.FinalReview.Status != "SETTLED" || execution.FinalReview.Verdict != "PASS" ||
		execution.FinalReview.CandidateCommitSHA != final.OutputCommitSHA ||
		len(execution.FinalReview.CheckRunIDs) != 1 || len(execution.Integration.CheckRunIDs) != 1 ||
		execution.FinalReview.CheckRunIDs[0] != execution.Integration.CheckRunIDs[0] {
		t.Fatalf("parallel requirement final review = %#v integration=%#v", execution.FinalReview, execution.Integration)
	}
	builders, reviewers, finalReviewers, boundReviewers := 0, 0, 0, 0
	for _, cfg := range harness.spawnConfigs {
		if strings.Contains(cfg.Branch, "cleardev-complex-builder-") {
			builders++
		}
		if strings.Contains(cfg.Branch, "cleardev-complex-review-") {
			reviewers++
		}
		if strings.Contains(cfg.Branch, "cleardev-requirement-final-review-") {
			finalReviewers++
		}
		if strings.Contains(cfg.Branch, "cleardev-builder-") && !strings.Contains(cfg.Branch, "cleardev-complex-builder-") {
			t.Fatalf("STANDARD builder branch leaked into PARALLEL: %s", cfg.Branch)
		}
	}
	for _, binding := range execution.RoleBindings {
		if binding.Role == core.StandardRoleReviewer && binding.Status == core.RoleBindingStatusBound {
			boundReviewers++
		}
	}
	if builders != 2 || reviewers != 3 || finalReviewers != 1 || boundReviewers != 0 {
		t.Fatalf("PARALLEL roles builders=%d reviewers=%d finalReviewers=%d boundReviewers=%d", builders, reviewers, finalReviewers, boundReviewers)
	}
	if view.OverallProgress.Phase != core.OverallPhaseCompleted {
		t.Fatalf("overall progress = %#v", view.OverallProgress)
	}
}

type parallelAgentHarness struct {
	store *sqlite.Store

	mu                     sync.Mutex
	spawnConfigs           []ports.SpawnConfig
	snapshots              map[domain.SessionID]chatsvc.Snapshot
	lastPromptBySession    map[domain.SessionID]string
	turnByClientMessageID  map[string]string
	relays                 []parallelRelay
	reviewBranchCandidates map[string]string
	workspaces             map[string]*parallelWorkspace
	sessionWorkspace       map[domain.SessionID]string
	nextSHA                int
	composeConflict        bool
	composeCalls           int
	prepareCalls           int
	preflightRequests      []ports.ClearDevCheckPreflightRequest
	checkReleaseIDs        []string
	preflightErr           error
	quickOverride          func(string) string
	injectInfraOnce        bool
	checkFailure           bool
	checkFailureFor        func(ports.ClearDevCheckRequest) bool
	integrationChecks      int
	mail                   bool
	repoPath               string
	freezeCalls            int
	mailDeliveryCalls      int
	responseOverride       func(session domain.SessionID, kind, response string) string
}

type parallelWorkspace struct {
	branch    string
	headSHA   string
	originSHA string
	paths     []ports.ClearDevDiffPath
	reviewer  bool
	dirty     bool
}

type parallelRelay struct {
	sessionID       domain.SessionID
	clientMessageID string
	prompt          string
	response        string
}

type allowParallelHuman struct{}

func (allowParallelHuman) Authorize(context.Context, cleardevsvc.HumanDecision) error { return nil }

type parallelTestIDs struct {
	mu   sync.Mutex
	next int
}

func (ids *parallelTestIDs) New() string {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.next++
	return fmt.Sprintf("s07-test-%03d", ids.next)
}

func forty(value string) string {
	result := ""
	for len(result) < 40 {
		result += value
	}
	return result[:40]
}

func parallelDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("%x", digest)
}

func promptJSONObject(prompt, kind string) (string, error) {
	needle := `"kind":"` + kind + `"`
	index := strings.LastIndex(prompt, needle)
	if index < 0 {
		return "", fmt.Errorf("prompt has no %s object", kind)
	}
	start := strings.LastIndex(prompt[:index], "{")
	if start < 0 {
		return "", fmt.Errorf("prompt has no opening object for %s", kind)
	}
	depth, quoted, escaped := 0, false, false
	for offset := start; offset < len(prompt); offset++ {
		value := prompt[offset]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if value == '\\' {
				escaped = true
				continue
			}
			if value == '"' {
				quoted = false
			}
			continue
		}
		switch value {
		case '"':
			quoted = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return prompt[start : offset+1], nil
			}
		}
	}
	return "", fmt.Errorf("prompt has an unterminated %s object", kind)
}

func newParallelAgentHarness(store *sqlite.Store, composeConflict bool) *parallelAgentHarness {
	return &parallelAgentHarness{
		store: store, snapshots: make(map[domain.SessionID]chatsvc.Snapshot),
		lastPromptBySession:    make(map[domain.SessionID]string),
		turnByClientMessageID:  make(map[string]string),
		reviewBranchCandidates: make(map[string]string),
		workspaces:             make(map[string]*parallelWorkspace),
		sessionWorkspace:       make(map[domain.SessionID]string),
		composeConflict:        composeConflict,
	}
}

func (h *parallelAgentHarness) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cfg.ProjectID == "" || cfg.CreationIdempotencyKey == "" {
		return domain.Session{}, 0, 0, fmt.Errorf("fake spawn requires project and idempotency key")
	}
	workspace := "/managed/" + strings.NewReplacer(":", "-", "/", "-").Replace(cfg.CreationIdempotencyKey)
	repoPath := "/tmp/" + string(cfg.ProjectID)
	if h.repoPath != "" {
		repoPath = h.repoPath
	}
	metadata := domain.SessionMetadata{
		Branch: cfg.Branch, WorkspacePath: workspace, WorkspaceRepoPath: repoPath,
		DiffBaseRef: "refs/heads/main", DiffBaseSHA: forty("a"),
	}
	state := &parallelWorkspace{branch: cfg.Branch, headSHA: forty("a"), originSHA: forty("a")}
	if strings.Contains(cfg.Branch, "cleardev-complex-review-") || strings.Contains(cfg.Branch, "cleardev-complex-specialist-") || strings.Contains(cfg.Branch, "cleardev-complex-recovery-") || strings.Contains(cfg.Branch, "cleardev-requirement-final-review-") {
		candidateSHA := h.reviewBranchCandidates[cfg.Branch]
		if candidateSHA == "" {
			return domain.Session{}, 0, 0, fmt.Errorf("Reviewer branch was not prepared")
		}
		metadata.DiffBaseSHA = candidateSHA
		state.headSHA, state.originSHA, state.reviewer = candidateSHA, candidateSHA, true
	}
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	activity := domain.ActivityActive
	if h.mail {
		activity = domain.ActivityIdle
	}
	record, _, err := h.store.CreateSessionIdempotent(ctx, domain.SessionRecord{
		ProjectID: cfg.ProjectID, Kind: cfg.Kind, Harness: cfg.Harness,
		Mode: cfg.RequestedMode, PermissionMode: cfg.AgentConfig.Permissions,
		CreationIdempotencyKey:     cfg.CreationIdempotencyKey,
		CreationRequestFingerprint: parallelDigest([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s", cfg.ProjectID, cfg.Kind, cfg.Harness, cfg.Branch, cfg.RequestedMode, cfg.AgentConfig.Permissions))),
		DisplayName:                cfg.DisplayName, Activity: domain.Activity{State: activity, LastActivityAt: now},
		Metadata: metadata, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return domain.Session{}, 0, 0, err
	}
	h.workspaces[workspace] = state
	h.sessionWorkspace[record.ID] = workspace
	h.spawnConfigs = append(h.spawnConfigs, cfg)
	return domain.Session{SessionRecord: record}, 0, 0, nil
}

func (h *parallelAgentHarness) RelayChatTurnWithID(_ context.Context, sessionID domain.SessionID, prompt, clientMessageID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing := h.turnByClientMessageID[clientMessageID]; existing != "" {
		return existing, nil
	}
	routePrompt := prompt
	if strings.Contains(prompt, "The previous JSON result was rejected by the protocol parser.") {
		if original := h.lastPromptBySession[sessionID]; original != "" {
			routePrompt = original
		}
	} else {
		h.lastPromptBySession[sessionID] = prompt
	}
	response, err := h.responseForPromptLocked(sessionID, routePrompt)
	if err != nil {
		return "", err
	}
	turnID := fmt.Sprintf("turn-%03d", len(h.relays)+1)
	messageID := fmt.Sprintf("message-%03d", len(h.relays)+1)
	now := time.Date(2026, 8, 27, 11, 0, 0, 0, time.UTC).Add(time.Duration(len(h.relays)) * time.Second)
	snapshot := h.snapshots[sessionID]
	snapshot.SessionID = sessionID
	snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{
		ID: turnID, HandledBySessionID: sessionID, State: domain.TurnStateCompleted, CompletedAt: &now,
	})
	snapshot.Messages = append(snapshot.Messages,
		domain.ConversationMessage{
			ID: "user-" + messageID, TurnID: turnID, Sequence: int64(len(snapshot.Messages) + 1),
			Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: prompt, ClientMessageID: clientMessageID,
		},
		domain.ConversationMessage{
			ID: messageID, TurnID: turnID, Sequence: int64(len(snapshot.Messages) + 2),
			Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Text: response,
		},
	)
	h.snapshots[sessionID] = snapshot
	h.turnByClientMessageID[clientMessageID] = turnID
	h.relays = append(h.relays, parallelRelay{sessionID: sessionID, clientMessageID: clientMessageID, prompt: prompt, response: response})
	return turnID, nil
}

func (h *parallelAgentHarness) Interrupt(context.Context, domain.SessionID) error { return nil }

func (h *parallelAgentHarness) Snapshot(_ context.Context, sessionID domain.SessionID) (chatsvc.Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot, ok := h.snapshots[sessionID]
	if !ok {
		return chatsvc.Snapshot{SessionID: sessionID}, nil
	}
	snapshot.Turns = append([]domain.ConversationTurn(nil), snapshot.Turns...)
	snapshot.Messages = append([]domain.ConversationMessage(nil), snapshot.Messages...)
	return snapshot, nil
}

func TestParallelHarnessFinalReviewIgnoresEmbeddedProtocolKinds(t *testing.T) {
	harness := newParallelAgentHarness(nil, false)
	prompt := `{"schemaVersion":1,"kind":"REQUIREMENT_FINAL_REVIEW_RESULT","verdict":"PASS"}

--- REQUIREMENT FINAL REVIEW PACKET ---
{"recoveryEvidence":{"kind":"RECOVERY_RESULT"}}`
	response, err := harness.responseForPromptLocked("final-review-session", prompt)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "REQUIREMENT_FINAL_REVIEW_RESULT" {
		t.Fatalf("final-review test double followed an embedded historical protocol: %s", response)
	}
}

func (h *parallelAgentHarness) responseForPromptLocked(sessionID domain.SessionID, prompt string) (string, error) {
	// A final-review packet can contain old protocol results as evidence.
	// Route this test double using the requested output, not those old results.
	if header, _, found := strings.Cut(prompt, "\n--- REQUIREMENT FINAL REVIEW PACKET ---\n"); found {
		prompt = header
	}
	kinds := []string{"COMPLEX_QUICK_TASK_REQUEST", "COMPLEX_EXECUTION_REQUEST", "SCOPE_EXPANSION_REQUEST", "SCOPE_EXPANSION_DECISION", "SPECIALIST_RESULT", "RECOVERY_RESULT", "BUILDER_RESULT", "LOCAL_REVIEW", "REQUIREMENT_FINAL_REVIEW_RESULT", "STATUS_REPORT"}
	lastKind, lastIndex := "", -1
	for _, kind := range kinds {
		if index := strings.LastIndex(prompt, `"kind":"`+kind+`"`); index > lastIndex {
			lastKind, lastIndex = kind, index
		}
	}
	if lastKind == "" {
		return "", fmt.Errorf("fake Chat received an unknown PARALLEL prompt")
	}
	response, err := promptJSONObject(prompt, lastKind)
	if err != nil {
		return "", err
	}
	if lastKind == "COMPLEX_QUICK_TASK_REQUEST" && h.quickOverride != nil {
		response = h.quickOverride(response)
	}
	if lastKind == "BUILDER_RESULT" {
		workspace := h.sessionWorkspace[sessionID]
		state := h.workspaces[workspace]
		if state == nil || state.reviewer {
			return "", fmt.Errorf("BUILDER_RESULT for unknown workspace %s", workspace)
		}
		paths := writePathsFromExecutionPackage(prompt)
		if len(paths) == 0 {
			return "", fmt.Errorf("BUILDER_RESULT prompt has no writePaths")
		}
		state.originSHA = state.headSHA
		diff := make([]ports.ClearDevDiffPath, 0, len(paths))
		for _, path := range paths {
			diff = append(diff, ports.ClearDevDiffPath{Status: "M", Path: path})
		}
		state.paths = diff
		if h.mail {
			state.dirty = true
		} else {
			h.nextSHA++
			state.headSHA = fmt.Sprintf("%040x", h.nextSHA)
		}
	}
	if lastKind == "RECOVERY_RESULT" && h.quickOverride != nil {
		response = h.quickOverride(response)
	}
	if h.responseOverride != nil {
		response = h.responseOverride(sessionID, lastKind, response)
	}
	return response, nil
}

func writePathsFromExecutionPackage(prompt string) []string {
	start := strings.Index(prompt, "执行包")
	if start < 0 {
		return nil
	}
	brace := strings.Index(prompt[start:], "{")
	if brace < 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(prompt[start+brace:])))
	var pkg struct {
		WritePaths                 []string `json:"writePaths"`
		SharedPathsRequireApproval []string `json:"sharedPathsRequireApproval"`
		GeneratedPaths             []string `json:"generatedPaths"`
	}
	if err := decoder.Decode(&pkg); err != nil {
		return nil
	}
	return append(append(append([]string{}, pkg.WritePaths...), pkg.SharedPathsRequireApproval...), pkg.GeneratedPaths...)
}

func (h *parallelAgentHarness) InspectCandidate(_ context.Context, workspacePath, baseSHA string) (ports.ClearDevCandidateInspection, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.workspaces[workspacePath]
	if state == nil {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	if state.dirty {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevWorkspaceDirty
	}
	if state.reviewer || baseSHA == state.headSHA {
		if baseSHA != state.headSHA {
			return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
		}
		return ports.ClearDevCandidateInspection{BaseSHA: baseSHA, CandidateSHA: state.headSHA, Paths: []ports.ClearDevDiffPath{}}, nil
	}
	if baseSHA != state.originSHA || state.headSHA == state.originSHA {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	return ports.ClearDevCandidateInspection{
		BaseSHA: state.originSHA, CandidateSHA: state.headSHA, Paths: append([]ports.ClearDevDiffPath(nil), state.paths...),
	}, nil
}

func (h *parallelAgentHarness) PrepareBaseWorkspace(_ context.Context, workspacePath, expectedHeadSHA, newBaseSHA string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.workspaces[workspacePath]
	if state == nil || state.headSHA != expectedHeadSHA || newBaseSHA == "" {
		return ports.ErrClearDevCandidateInvalid
	}
	h.prepareCalls++
	state.headSHA, state.originSHA, state.paths = newBaseSHA, newBaseSHA, nil
	return nil
}

func (h *parallelAgentHarness) PrepareMailBuilderBase(ctx context.Context, request ports.ClearDevMailBuilderBaseRequest) error {
	if request.RepoPath != h.repoPath || !strings.HasPrefix(request.Branch, "cleardev-complex-builder-") {
		return ports.ErrClearDevCandidateInvalid
	}
	return h.PrepareBaseWorkspace(ctx, request.WorkspacePath, request.ExpectedHeadSHA, request.BaseSHA)
}

func (h *parallelAgentHarness) ComposeCandidates(_ context.Context, request ports.ClearDevComposeRequest) (ports.ClearDevComposeResult, error) {
	if request.CompleteTaskDeltas != h.mail {
		return ports.ClearDevComposeResult{}, errors.New("mail and legacy composition policies must not be mixed")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.composeCalls++
	workspace := "/managed/compose-" + strings.NewReplacer(":", "-", "/", "-").Replace(request.RequestID)
	if h.composeConflict {
		return ports.ClearDevComposeResult{WorkspacePath: workspace, ConflictPaths: []string{"src/email.js"}}, ports.ErrClearDevCompositionConflict
	}
	h.nextSHA++
	outputSHA := fmt.Sprintf("%040x", h.nextSHA)
	h.workspaces[workspace] = &parallelWorkspace{branch: core.ComplexCompositionBranch(request.RequestID), headSHA: outputSHA, originSHA: outputSHA}
	return ports.ClearDevComposeResult{WorkspacePath: workspace, OutputSHA: outputSHA}, nil
}

func (h *parallelAgentHarness) InspectDeliveryBranch(_ context.Context, workspace, branch, sha string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.workspaces[workspace]
	if state == nil || state.branch != branch || state.headSHA != sha {
		return ports.ErrClearDevCandidateInvalid
	}
	return nil
}

func (h *parallelAgentHarness) PrepareReviewBranch(_ context.Context, workspacePath, branch, candidateSHA string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mail && workspacePath == h.repoPath && candidateSHA == forty("a") {
		h.reviewBranchCandidates[branch] = candidateSHA
		return nil
	}
	state := h.workspaces[workspacePath]
	if state == nil || candidateSHA == "" || state.headSHA != candidateSHA || state.dirty {
		return ports.ErrClearDevCandidateInvalid
	}
	h.reviewBranchCandidates[branch] = candidateSHA
	return nil
}

func (h *parallelAgentHarness) RunCandidateCheck(_ context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	failed := h.checkFailure || (h.checkFailureFor != nil && h.checkFailureFor(request))
	if !failed && h.injectInfraOnce && request.AllowInfraInject {
		h.integrationChecks++
		if h.integrationChecks == 1 {
			return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1, OutputSummary: "INJECTED_INFRASTRUCTURE_FAILURE"}, nil
		}
	}
	result := ports.ClearDevCheckResult{
		Outcome: ports.ClearDevCheckPass, ImageID: "sha256:parallel-test-image", ExitCode: 0,
		OutputSummary: "pass", OutputSHA256: parallelDigest([]byte("pass")),
	}
	if failed {
		result.Outcome, result.ExitCode = ports.ClearDevCheckFail, 1
		result.OutputSummary, result.OutputSHA256 = "failed", parallelDigest([]byte("failed"))
	}
	if h.mail {
		result.CandidateSHA = request.CandidateSHA
		result.Image = core.StandardCandidateCheckImage
		result.SourceManifestID = parallelDigest([]byte("manifest:" + request.CandidateSHA))
		result.SourceRootTreeOID = request.CandidateSHA
		result.CheckEnvironmentID = strings.Repeat("f", 64)
	}
	return result, nil
}

func (h *parallelAgentHarness) IdentifyMailProjectAtBranch(context.Context, string, string) (ports.ClearDevBaselineResult, error) {
	return h.IdentifyMailProject(context.Background(), "")
}

func (h *parallelAgentHarness) FreezeMailCandidate(_ context.Context, request ports.ClearDevMailFreezeRequest) (ports.ClearDevCandidateInspection, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.mail || request.RepoPath != h.repoPath || request.BaseSHA == "" || request.ParentSHA == "" {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	state := h.workspaces[request.WorkspacePath]
	if state == nil || state.reviewer || !state.dirty || state.headSHA != request.ParentSHA || len(state.paths) == 0 {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	h.nextSHA++
	candidate := fmt.Sprintf("%040x", h.nextSHA)
	state.headSHA, state.originSHA, state.dirty = candidate, request.BaseSHA, false
	h.freezeCalls++
	return ports.ClearDevCandidateInspection{
		BaseSHA: request.BaseSHA, CandidateSHA: candidate, Paths: append([]ports.ClearDevDiffPath(nil), state.paths...),
	}, nil
}

func parallelMailScopeFixture(request ports.ClearDevDeliveryRequest) core.MailScopeProof {
	return core.MailScopeProof{
		Policy: request.DeliveryPolicy, BaseSHA: request.BaseSHA, CandidateSHA: request.CandidateSHA,
		SourceManifestID: parallelDigest([]byte("manifest:" + request.CandidateSHA)), SourceTreeOID: request.CandidateSHA,
		OldTestCount: 8, ExtraTestPaths: []string{},
	}
}

func (h *parallelAgentHarness) CheckMailCandidateScope(_ context.Context, request ports.ClearDevDeliveryRequest) (core.MailScopeProof, error) {
	if !h.mail || request.DeliveryPolicy != core.MailDeliveryPolicyV2 {
		return core.MailScopeProof{}, ports.ErrClearDevMailScopeViolation
	}
	return parallelMailScopeFixture(request), nil
}

func (h *parallelAgentHarness) RunMailDeliveryCheck(_ context.Context, request ports.ClearDevDeliveryRequest) (ports.ClearDevCheckResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.mail || request.DeliveryPolicy != core.MailDeliveryPolicyV2 {
		return ports.ClearDevCheckResult{}, ports.ErrClearDevMailScopeViolation
	}
	scope := parallelMailScopeFixture(request)
	check := func(suffix string, argv []string) core.MailCheckProof {
		return core.MailCheckProof{
			RunID: request.RunID + suffix, CandidateSHA: request.CandidateSHA,
			SourceManifestID: scope.SourceManifestID, SourceTreeOID: scope.SourceTreeOID,
			ImageID: "sha256:parallel-test-image", EnvironmentID: strings.Repeat("f", 64),
			OutputSHA256: parallelDigest([]byte(request.RunID + suffix)), Argv: argv, Passed: true, ExitCode: 0,
		}
	}
	proof := core.MailDeliveryProof{
		Scope: scope, Tests: check(":npm-test", []string{"npm", "test"}),
		Health: check(":health", []string{"trusted-mail-health-v1"}), HealthProbeSHA256: core.MailHealthProbeSHA256,
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return ports.ClearDevCheckResult{}, err
	}
	h.mailDeliveryCalls++
	return ports.ClearDevCheckResult{
		Outcome: ports.ClearDevCheckPass, CandidateSHA: request.CandidateSHA, Image: core.StandardCandidateCheckImage,
		ImageID: "sha256:parallel-test-image", SourceManifestID: scope.SourceManifestID, SourceRootTreeOID: scope.SourceTreeOID,
		CheckEnvironmentID: strings.Repeat("f", 64), OutputSummary: string(raw), OutputSHA256: parallelDigest(raw), ExitCode: 0,
	}, nil
}

func (h *parallelAgentHarness) PrepareCandidateChecks(_ context.Context, request ports.ClearDevCheckPreflightRequest) (ports.ClearDevCheckEnvironment, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.preflightRequests = append(h.preflightRequests, request)
	if h.preflightErr != nil {
		return ports.ClearDevCheckEnvironment{}, h.preflightErr
	}
	return ports.ClearDevCheckEnvironment{CandidateSHA: request.CandidateSHA, Image: request.Image, ImageID: "sha256:parallel-test-image", CheckEnvironmentID: "parallel-test-environment"}, nil
}

func (h *parallelAgentHarness) ReleaseCandidateChecks(_ context.Context, runID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkReleaseIDs = append(h.checkReleaseIDs, runID)
	return nil
}

func (h *parallelAgentHarness) RunGeneratedProof(context.Context, ports.ClearDevGeneratedProofRequest) (ports.ClearDevGeneratedProofResult, error) {
	return ports.ClearDevGeneratedProofResult{Outcome: ports.ClearDevCheckPass, ImageID: "sha256:parallel-test-image", OutputSHA256JSON: "{}"}, nil
}

func newComplexParallelService(t *testing.T, composeConflict bool) (*sqlite.Store, *parallelAgentHarness, *cleardevsvc.Service, cleardevtest.ComplexParallelPrep) {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initParallelPrepRepo(t)
	prep := cleardevtest.SeedComplexParallelV2(t, store, "ao-s07-service", repo, dataDir)
	harness := newParallelAgentHarness(store, composeConflict)
	return store, harness, parallelTestService(store, harness), prep
}

func newMailComplexParallelService(t *testing.T, composeConflict bool) (*sqlite.Store, *parallelAgentHarness, *cleardevsvc.Service, cleardevtest.ComplexParallelPrep) {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initParallelPrepRepo(t)
	prep := cleardevtest.SeedMailParallelV2(t, store, "ao-mail-v2-parallel", repo, dataDir)
	harness := newParallelAgentHarness(store, composeConflict)
	harness.mail, harness.repoPath = true, repo
	return store, harness, parallelTestService(store, harness), prep
}

func TestComplexContinuationDoesNotCreateStepWhenModelMissing(t *testing.T) {
	store, harness, _, prep := newComplexStandardExecutionService(t)
	terminateSeededSession(t, store, prep.StewardSessionID)
	service := parallelTestServiceWithChecker(store, harness, missingModelPreflight{})
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ComplexExecution == nil {
		t.Fatal("expected a STANDARD execution request")
	}
	assertControlledPreflightBlockedContinuation(t, harness, 0, 0, 0, view.ComplexExecution.RoleBindings, view.ComplexExecution.AgentSteps, view.LatestControlledPreflight)
}

func TestQuickContinuationDoesNotCreateStepWhenModelMissing(t *testing.T) {
	store, harness, service, prep := newComplexStandardExecutionService(t)
	campaign, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.ComplexExecution == nil || campaign.ComplexExecution.Phase != core.ComplexExecutionCompleted {
		t.Fatalf("STANDARD campaign = %#v", campaign.ComplexExecution)
	}
	harness.mu.Lock()
	spawnsBefore := len(harness.spawnConfigs)
	relaysBefore := len(harness.relays)
	workspacesBefore := len(harness.workspaces)
	harness.mu.Unlock()
	terminateSeededSession(t, store, prep.StewardSessionID)
	blocked := parallelTestServiceWithChecker(store, harness, missingModelPreflight{})
	view, err := blocked.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if view.QuickExecution == nil {
		t.Fatal("expected a QUICK run after the completed STANDARD campaign")
	}
	assertControlledPreflightBlockedContinuation(t, harness, spawnsBefore, relaysBefore, workspacesBefore, view.QuickExecution.RoleBindings, view.QuickExecution.AgentSteps, view.LatestControlledPreflight)
}

func TestComplexStandardExecutionAcceptsCurrentConfirmedV1(t *testing.T) {
	_, harness, service, prep := newComplexStandardV1ExecutionService(t)
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ComplexExecution == nil || view.ComplexExecution.Phase != core.ComplexExecutionCompleted {
		t.Fatalf("v1 execution phase = %#v", view.ComplexExecution)
	}
	run := view.ComplexExecution.Run
	if run.RequirementVersionID != prep.V1ID || run.PlanID != prep.V1PlanID {
		t.Fatalf("v1 execution bound %#v, want version %s plan %s", run, prep.V1ID, prep.V1PlanID)
	}
	if run.ExpectedTaskSetVersion != 0 || run.TaskSetVersion != core.ComplexStandardTaskSetVersion {
		t.Fatalf("v1 task-set binding = expected:%d accepted:%d", run.ExpectedTaskSetVersion, run.TaskSetVersion)
	}
	if harness == nil {
		t.Fatal("missing test harness")
	}
}

func newComplexStandardV1ExecutionService(t *testing.T) (*sqlite.Store, *parallelAgentHarness, *cleardevsvc.Service, cleardevtest.ComplexStandardPrep) {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initParallelPrepRepo(t)
	prep := cleardevtest.SeedComplexStandardV1(t, store, "ao-s11-v1-service", repo, dataDir)
	harness := newParallelAgentHarness(store, false)
	return store, harness, parallelTestService(store, harness), prep
}

func newMailBenchQuickService(t *testing.T) (*sqlite.Store, *parallelAgentHarness, *cleardevsvc.Service, cleardevtest.ComplexStandardPrep) {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initParallelPrepRepo(t)
	prep := cleardevtest.SeedMailBenchCampaignV1(t, store, "ao-s12a2-mailbench-service", repo, dataDir)
	harness := newParallelAgentHarness(store, false)
	return store, harness, parallelTestService(store, harness), prep
}

func newComplexStandardExecutionService(t *testing.T) (*sqlite.Store, *parallelAgentHarness, *cleardevsvc.Service, cleardevtest.ComplexStandardPrep) {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initParallelPrepRepo(t)
	prep := cleardevtest.SeedComplexStandardV2(t, store, "ao-s07-standard-service", repo, dataDir)
	harness := newParallelAgentHarness(store, false)
	return store, harness, parallelTestService(store, harness), prep
}

func newControlledExceptionService(t *testing.T) (*sqlite.Store, *parallelAgentHarness, *cleardevsvc.Service, cleardevtest.ComplexStandardPrep) {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initParallelPrepRepo(t)
	prep := cleardevtest.SeedComplexExceptionV2(t, store, "ao-s09-exception-service", repo, dataDir)
	harness := newParallelAgentHarness(store, false)
	harness.injectInfraOnce = true
	return store, harness, parallelTestService(store, harness), prep
}

func parallelTestService(store *sqlite.Store, harness *parallelAgentHarness) *cleardevsvc.Service {
	return parallelTestServiceWithOptions(store, harness, nil)
}

func parallelTestServiceWithOptions(store *sqlite.Store, harness *parallelAgentHarness, configure func(*cleardevsvc.Deps)) *cleardevsvc.Service {
	ids := &parallelTestIDs{}
	var clockMu sync.Mutex
	tick := 0
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		tick++
		return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC).Add(time.Duration(tick) * time.Second)
	}
	deps := cleardevsvc.Deps{
		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, RequirementFinalReviews: store,
		ProgressExplanations: store, ParseCorrections: store, AgentAttempts: store, Workspace: &workspaceObserver{},
		RecoverAgentSession: func(context.Context, domain.SessionID) error { return fmt.Errorf("unexpected recovery") },
		DirectionFacts:      store, HumanDecisions: store, AO: store,
		Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		ControlledPreflights: store, ControlledPreflightChecker: passingProgressPreflight{},
		Human: allowParallelHuman{}, BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		BoundedMailAttempts: true,
		StepTimeout:         time.Minute, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	}
	if configure != nil {
		configure(&deps)
	}
	return cleardevsvc.New(deps)
}

func parallelTestServiceWithChecker(store *sqlite.Store, harness *parallelAgentHarness, checker cleardevsvc.ControlledPreflightChecker) *cleardevsvc.Service {
	ids := &parallelTestIDs{next: 1000}
	var clockMu sync.Mutex
	tick := 0
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		tick++
		return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC).Add(time.Duration(tick) * time.Second)
	}
	return cleardevsvc.New(cleardevsvc.Deps{
		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, RequirementFinalReviews: store,
		ProgressExplanations: store, ParseCorrections: store, AgentAttempts: store, Workspace: &workspaceObserver{},
		RecoverAgentSession: func(context.Context, domain.SessionID) error { return fmt.Errorf("unexpected recovery") },
		DirectionFacts:      store, HumanDecisions: store, AO: store,
		Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		ControlledPreflights: store, ControlledPreflightChecker: checker,
		Human: allowParallelHuman{}, BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		BoundedMailAttempts: true,
		StepTimeout:         time.Minute, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})
}

type missingModelPreflight struct{}

func (missingModelPreflight) CheckControlledPreflight(_ context.Context, _ domain.AgentHarness, requested string) (ports.ChatControlledPreflight, error) {
	raw, _ := json.Marshal([]string{"gpt-5.4"})
	sum := sha256.Sum256(raw)
	return ports.ChatControlledPreflight{
		RequestedModel: requested,
		Provider:       string(domain.HarnessCodex),
		CatalogJSON:    string(raw),
		CatalogSHA256:  hex.EncodeToString(sum[:]),
		ErrorSummary:   "requested model is not in the provider catalog",
	}, ports.ErrChatModelNotAvailable
}

func terminateSeededSession(t *testing.T, store *sqlite.Store, sessionID string) {
	t.Helper()
	record, found, err := store.GetSession(context.Background(), domain.SessionID(sessionID))
	if err != nil || !found {
		t.Fatalf("seeded session %s found=%t err=%v", sessionID, found, err)
	}
	record.IsTerminated = true
	if err := store.UpdateSession(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

func assertControlledPreflightBlockedContinuation(t *testing.T, harness *parallelAgentHarness, spawnsBefore, relaysBefore, workspacesBefore int, bindings []core.ComplexExecutionRoleBinding, steps []core.AgentStep, preflight *core.ControlledPreflightView) {
	t.Helper()
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if len(harness.spawnConfigs) != spawnsBefore || len(harness.relays) != relaysBefore || len(harness.workspaces) != workspacesBefore {
		t.Fatalf("continuation contacted the agent: spawns %d->%d relays %d->%d workspaces %d->%d",
			spawnsBefore, len(harness.spawnConfigs), relaysBefore, len(harness.relays), workspacesBefore, len(harness.workspaces))
	}
	requested := 0
	for _, binding := range bindings {
		if binding.Role != core.StandardRoleSteward {
			continue
		}
		if binding.Status == core.RoleBindingStatusRequested && binding.AOSessionID == "" {
			requested++
		}
	}
	if requested != 1 {
		t.Fatalf("requested Steward continuations = %d bindings=%#v", requested, bindings)
	}
	if len(steps) != 0 {
		t.Fatalf("agent steps were created before preflight: %#v", steps)
	}
	if preflight == nil || preflight.Outcome != core.ControlledPreflightFailed || preflight.ReasonCode != core.ReasonModelNotAvailable {
		t.Fatalf("preflight = %#v", preflight)
	}
}

func initParallelPrepRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "ClearDev test"}} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	return repo
}
