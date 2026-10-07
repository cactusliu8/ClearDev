package cleardev

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// Model, Git and check execution are explicit doubles here. All plan, check,
// review and completion facts use real SQLite transactions. cleardevlocal has
// separate real Git/Docker tests of the proof-producing implementation.
type mailFlowChecks struct {
	*baselineGateHarness
	failure       string
	deliveryCalls int
}

func (h *mailFlowChecks) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, messageID string) (string, error) {
	turn, err := h.standardAgentHarness.RelayChatTurnWithID(ctx, id, prompt, messageID)
	if err != nil {
		return turn, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := h.snapshots[id]
	for i := range snapshot.Messages {
		var result core.LocalReviewResult
		if json.Unmarshal([]byte(snapshot.Messages[i].Text), &result) == nil && result.Kind == "LOCAL_REVIEW" && result.Verdict == "REWORK" {
			for j := range result.Findings {
				result.Findings[j].Path = "backend/src/emails.ts"
			}
			raw, _ := core.MarshalAgentChosenResult(result)
			snapshot.Messages[i].Text = string(raw)
		}
	}
	h.snapshots[id] = snapshot
	return turn, nil
}

func (h *mailFlowChecks) IdentifyMailProject(context.Context, string) (ports.ClearDevBaselineResult, error) {
	return ports.ClearDevBaselineResult{Required: true, CandidateSHA: forty("a")}, nil
}

func mailScopeFixture(request ports.ClearDevDeliveryRequest) core.MailScopeProof {
	policy := request.DeliveryPolicy
	if policy == "" {
		policy = core.MailDeliveryPolicyV1
	}
	return core.MailScopeProof{Policy: policy, BaseSHA: request.BaseSHA, CandidateSHA: request.CandidateSHA, SourceManifestID: strings.Repeat("d", 64), SourceTreeOID: forty("e"), OldTestCount: 8, ExtraTestPaths: []string{"test/new.test.js"}}
}

func (h *mailFlowChecks) CheckMailCandidateScope(_ context.Context, request ports.ClearDevDeliveryRequest) (core.MailScopeProof, error) {
	if h.failure == "scope" {
		return core.MailScopeProof{}, ports.ErrClearDevMailScopeViolation
	}
	return mailScopeFixture(request), nil
}

func (h *mailFlowChecks) InspectCandidate(ctx context.Context, workspace, sha string) (ports.ClearDevCandidateInspection, error) {
	h.mu.Lock()
	isReviewer := h.reviewerWorkspaces[workspace] != ""
	reviewed := h.reviewerRuns > 0
	h.mu.Unlock()
	if h.failure == "review-workspace" && isReviewer && reviewed {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevWorkspaceDirty
	}
	if h.failure == "final-workspace" && !isReviewer && h.deliveryCalls > 0 {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevWorkspaceDirty
	}
	return h.baselineGateHarness.InspectCandidate(ctx, workspace, sha)
}

func (h *mailFlowChecks) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	result, err := h.standardAgentHarness.RunCandidateCheck(ctx, request)
	result.CandidateSHA, result.Image = request.CandidateSHA, core.StandardCandidateCheckImage
	result.SourceManifestID, result.SourceRootTreeOID, result.CheckEnvironmentID = strings.Repeat("d", 64), forty("e"), strings.Repeat("f", 64)
	if h.failure == "stale-required" {
		result.CandidateSHA = forty("a")
	}
	return result, err
}

func (h *mailFlowChecks) RunMailDeliveryCheck(_ context.Context, request ports.ClearDevDeliveryRequest) (ports.ClearDevCheckResult, error) {
	h.deliveryCalls++
	scope := mailScopeFixture(request)
	check := func(suffix string, argv []string) core.MailCheckProof {
		return core.MailCheckProof{RunID: request.RunID + suffix, CandidateSHA: request.CandidateSHA, SourceManifestID: scope.SourceManifestID, SourceTreeOID: scope.SourceTreeOID, ImageID: "sha256:standard-test-image", EnvironmentID: strings.Repeat("f", 64), OutputSHA256: coreDigest([]byte("test double pass")), Argv: argv, Passed: true}
	}
	extra := check(":extra-tests", core.MailExtraTestArgv(scope.ExtraTestPaths))
	proof := core.MailDeliveryProof{Scope: scope, Tests: check(":npm-test", []string{"npm", "test"}), ExtraTests: &extra, Health: check(":health", []string{"trusted-mail-health-v1"}), HealthProbeSHA256: core.MailHealthProbeSHA256}
	raw, _ := json.Marshal(proof)
	result := ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckPass, CandidateSHA: request.CandidateSHA, Image: core.StandardCandidateCheckImage, ImageID: proof.Tests.ImageID, SourceManifestID: scope.SourceManifestID, SourceRootTreeOID: scope.SourceTreeOID, CheckEnvironmentID: proof.Tests.EnvironmentID, OutputSummary: string(raw), OutputSHA256: coreDigest(raw)}
	if h.failure == "health" {
		result.Outcome, result.ExitCode = ports.ClearDevCheckFail, 1
	}
	return result, nil
}

func newMailFlowFixture(t *testing.T) (*autoExecutionFixture, *mailFlowChecks) {
	t.Helper()
	f := newAutoExecutionFixture(t)
	f.harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
		task := plan.Tasks[0]
		task.DependencyKeys = []string{}
		task.WritePaths = []string{"backend/src/**", "frontend/**", "test/**"}
		task.RequiredCheckIDs = []string{"demo-integration"}
		plan.Tasks = []core.ComplexPlanTask{task}
		plan.IntegrationCheckIDs = []string{"demo-integration"}
		plan.ParallelSuggestion.RecommendedBuilderCount = 1
	}
	f.harness.inspectionPaths = []ports.ClearDevDiffPath{{Status: "M", Path: "backend/src/emails.ts"}, {Status: "A", Path: "test/new.test.js"}}
	h := &mailFlowChecks{baselineGateHarness: &baselineGateHarness{standardAgentHarness: f.harness}}
	f.service.checks, f.service.inspector, f.service.chat = h, h, h
	return f, h
}

func assertMailNotCompleted(t *testing.T, f *autoExecutionFixture) {
	t.Helper()
	execution, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found && (execution.Run.CompletedAt != nil || execution.Integration != nil) {
		t.Fatal("invalid delivery completed")
	}
	view, err := f.service.GetRequirement(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.TrustedProgress.Phase == core.TrustedPhaseCompleted {
		t.Fatal("false trusted completion")
	}
	for _, task := range view.DevelopmentTasks {
		if task.DevelopmentTask.Status == core.DevelopmentTaskStatusDone {
			t.Fatal("partial DONE escaped completion rollback")
		}
	}
	for _, event := range view.Events {
		if event.Action == core.ActionCompleteComplexExecution && event.Outcome == core.EventAccepted {
			t.Fatal("completion event escaped rejection")
		}
	}
}

func TestMailDeliveryCompletesThroughOriginalTransactionAndReopens(t *testing.T) {
	f, h := newMailFlowFixture(t)
	f.confirm(t)
	view := mustGetComplex(t, f.service, f.view.Requirement.ID)
	for _, step := range view.ComplexPlanning.AgentSteps {
		if step.SendStatus == core.AgentStepSendStatusFailed {
			t.Fatalf("failed planning step: %s %s", step.Kind, step.ReasonCode)
		}
	}
	execution := f.completed(t)
	policy, base, required, err := core.MailDeliveryPolicyFromRun(execution.Run)
	if err != nil || !required || base != forty("a") || h.deliveryCalls != 1 {
		t.Fatalf("policy=%s base=%s required=%v checks=%d err=%v", policy, base, required, h.deliveryCalls, err)
	}
	var proofRun core.ComplexExecutionCheckRun
	for _, check := range execution.CheckRuns {
		if check.Kind == core.CandidateCheckIntegration {
			proofRun = check
		}
	}
	if err := core.ValidateMailDeliveryProofForPolicy(proofRun.OutputSummary, policy, base, execution.Integration.CandidateCommitSHA, proofRun.ID, proofRun.ContainerImageID); err != nil {
		t.Fatal(err)
	}
	counts := f.counts()
	f.reopen(t)
	f.service.checks, f.service.inspector = h, h
	if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.completed(t)
	if h.deliveryCalls != 1 || f.counts() != counts {
		t.Fatal("completed recovery repeated work")
	}
	wrong := *execution.Integration
	wrong.CandidateCommitSHA = forty("f")
	if err := f.store.CompleteClearDevComplexExecution(context.Background(), core.CompleteComplexExecutionCommand{ExecutionRunID: execution.Run.ID, Integration: wrong, At: f.clock()}); err == nil {
		t.Fatal("completed transaction acknowledged an unreviewed new SHA")
	}
}

func TestMailDeliveryCannotCompleteAfterScopeCheckOrHealthFailure(t *testing.T) {
	for _, mode := range []string{"scope", "stale-required", "health", "review-workspace", "final-workspace"} {
		t.Run(mode, func(t *testing.T) {
			f, h := newMailFlowFixture(t)
			h.failure = mode
			f.confirm(t)
			assertMailNotCompleted(t, f)
		})
	}
}

// Inject corruption at the trusted persistence seam AFTER service validation.
// This proves the completion transaction independently reads the stored facts;
// no SQL updates, triggers disabled, or manually-created terminal state are used.
type corruptMailPersistence struct {
	ComplexExecutionFactStore
	store           *sqlite.Store
	requirementID   string
	mode            string
	rejected        error
	foreignReviewID string
}

func (s *corruptMailPersistence) SettleClearDevComplexExecutionCheckRun(ctx context.Context, c core.SettleComplexExecutionCheckCommand) (bool, error) {
	var proof core.MailDeliveryProof
	if json.Unmarshal([]byte(c.OutputSummary), &proof) == nil && (proof.Scope.Policy == core.MailDeliveryPolicyV1 || proof.Scope.Policy == core.MailDeliveryPolicyV2) {
		switch s.mode {
		case "missing-health":
			proof.Health = core.MailCheckProof{}
		case "stale-health":
			proof.Health.CandidateSHA = proof.Scope.BaseSHA
		case "unexecuted-tests":
			proof.ExtraTests = nil
		case "failed-tests":
			proof.Tests.ExitCode = 1
		case "truncated":
			proof.Tests.Truncated = true
		case "scope-drift":
			proof.Scope.OldTestCount++
		case "wrong-probe":
			proof.HealthProbeSHA256 = strings.Repeat("0", 64)
		}
		raw, _ := json.Marshal(proof)
		c.OutputSummary, c.OutputSHA256 = string(raw), coreDigest(raw)
		if s.mode == "agent-pass" {
			c.OutputSummary = `{"summary":"PASS"}`
			c.OutputSHA256 = coreDigest([]byte(c.OutputSummary))
		}
	}
	return s.ComplexExecutionFactStore.SettleClearDevComplexExecutionCheckRun(ctx, c)
}

func (s *corruptMailPersistence) CompleteClearDevComplexExecution(ctx context.Context, c core.CompleteComplexExecutionCommand) error {
	if s.mode == "new-sha" {
		c.Integration.CandidateCommitSHA = forty("f")
	}
	if s.mode == "missing-integration-check" {
		c.Integration.CheckRunIDs = nil
	}
	err := s.ComplexExecutionFactStore.CompleteClearDevComplexExecution(ctx, c)
	s.rejected = err
	return err
}

func (s *corruptMailPersistence) VerifyClearDevComplexExecutionCandidate(ctx context.Context, c core.VerifyComplexExecutionCandidateCommand) error {
	if s.foreignReviewID != "" {
		c.Verification.LocalReviewID = s.foreignReviewID
	}
	if s.mode == "stale-review" {
		execution, _, err := s.store.GetClearDevComplexExecution(ctx, s.requirementID)
		if err != nil {
			return err
		}
		if len(execution.Reviews) > 1 {
			c.Verification.LocalReviewID = execution.Reviews[0].ID
		}
	}
	err := s.ComplexExecutionFactStore.VerifyClearDevComplexExecutionCandidate(ctx, c)
	if err != nil {
		s.rejected = err
	}
	return err
}

func TestMailCompletionTransactionRejectsMissingOrStaleStoredEvidence(t *testing.T) {
	for _, mode := range []string{"missing-health", "stale-health", "unexecuted-tests", "failed-tests", "truncated", "scope-drift", "wrong-probe", "agent-pass", "new-sha", "missing-integration-check", "stale-review"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newMailFlowFixture(t)
			fault := &corruptMailPersistence{ComplexExecutionFactStore: f.store, store: f.store, requirementID: f.view.Requirement.ID, mode: mode}
			f.service.complexExecution = fault
			if mode == "stale-review" {
				f.harness.reworkOnce = true
			}
			f.confirm(t)
			if fault.rejected == nil {
				t.Fatal("fault did not reach a rejecting storage transaction")
			}
			assertMailNotCompleted(t, f)
			f.reopen(t)
			assertMailNotCompleted(t, f)
		})
	}
}

func TestMailV2CompletionRejectsMissingOrStaleStoredEvidence(t *testing.T) {
	for _, mode := range []string{"missing-health", "stale-health", "unexecuted-tests", "failed-tests", "truncated", "scope-drift", "wrong-probe", "agent-pass", "new-sha", "missing-integration-check", "stale-review"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newMailFlowFixture(t)
			f.service.boundedMailAttempts = true
			fault := &corruptMailPersistence{ComplexExecutionFactStore: f.store, store: f.store, requirementID: f.view.Requirement.ID, mode: mode}
			f.service.complexExecution = fault
			if mode == "stale-review" {
				f.harness.reworkOnce = true
			}
			f.confirm(t)
			if fault.rejected == nil {
				t.Fatal("V2 corruption did not reach a rejecting storage transaction")
			}
			assertMailNotCompleted(t, f)
		})
	}
}

func TestMailPlannerCannotApproveAWidenedAgentWhitelist(t *testing.T) {
	f, _ := newMailFlowFixture(t)
	f.harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
		task := plan.Tasks[0]
		task.DependencyKeys = []string{}
		task.WritePaths = []string{"package.json"}
		task.RequiredCheckIDs = []string{"demo-integration"}
		plan.Tasks = []core.ComplexPlanTask{task}
		plan.IntegrationCheckIDs = []string{"demo-integration"}
		plan.ParallelSuggestion.RecommendedBuilderCount = 1
	}
	f.confirm(t)
	f.noExecution(t)
}

func TestMailMissingTrustedDeliveryCheckerFailsClosed(t *testing.T) {
	f, h := newMailFlowFixture(t)
	f.service.checks = &mailNoDeliveryChecker{ClearDevCheckRunner: h, ClearDevMailProjectIdentifier: h, ClearDevDevelopmentBaselineChecker: h}
	f.confirm(t)
	assertMailNotCompleted(t, f)
}

type mailNoDeliveryChecker struct {
	ports.ClearDevCheckRunner
	ports.ClearDevMailProjectIdentifier
	ports.ClearDevDevelopmentBaselineChecker
}

func TestMailCompletionRejectsPassingReviewFromAnotherCandidate(t *testing.T) {
	f, h := newMailFlowFixture(t)
	f.confirm(t)
	first := f.completed(t)
	if first.Reviews[0].Verdict != core.LocalReviewPass {
		t.Fatal("fixture did not produce a real stored PASS")
	}
	h.mu.Lock()
	h.currentCandidate = "" // Explicit fake Git for the second independent branch.
	h.mu.Unlock()
	f.view = createComplexUntilClarification(t, f.service)
	f.view = answerComplexQuestions(t, f.service, f.view)
	fault := &corruptMailPersistence{ComplexExecutionFactStore: f.store, store: f.store, requirementID: f.view.Requirement.ID, foreignReviewID: first.Reviews[0].ID}
	f.service.complexExecution = fault
	f.confirm(t)
	if fault.rejected == nil {
		t.Fatal("an existing PASS from another candidate was not rejected by storage")
	}
	assertMailNotCompleted(t, f)
}
