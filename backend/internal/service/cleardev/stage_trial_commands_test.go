package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
)

type trialReceiptDouble struct {
	ports.ClearDevCheckRunner
	result  ports.ClearDevCheckResult
	found   bool
	err     error
	request ports.ClearDevCheckRequest
}

func (d *trialReceiptDouble) ReadCandidateCheck(_ context.Context, r ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, bool, error) {
	d.request = r
	return d.result, d.found, d.err
}

func TestStageTrialPASSRequiresExecutedExactCommandReceipt(t *testing.T) {
	f, _, _, execution, _ := pendingStageTrialFixture(t)
	// Construct a separate in-memory prospective contract. Never mutate persisted
	// review/run history; the fixture's source/authority rejection tests remain real.
	var pkg core.ComplexExecutionRunPackage
	if err := json.Unmarshal([]byte(execution.Run.ExecutionPackageJSON), &pkg); err != nil {
		t.Fatal(err)
	}
	step := core.ProjectTrialStep{ID: "invalid-input", Kind: "COMMAND", Argv: []string{"node", "src/taskstat.mjs", "invalid"}, TimeoutSeconds: 30, ExpectedExitCode: 1, AcceptanceCriteria: []string{"ACC-001"}, Observe: "invalid input is rejected"}
	pkg.ProjectExecution.Basis.Trial = &core.ProjectTrial{SchemaVersion: 1, Steps: []core.ProjectTrialStep{step}}
	pkg.ProjectExecution.Basis.Launch.Argv = []string{}
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	run := execution.Run
	run.ExecutionPackageJSON = string(raw)
	run.ExecutionPackageSHA256 = coreDigest(raw)
	contract := *pkg.ProjectExecution
	digest, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		t.Fatal(err)
	}
	double := &trialReceiptDouble{found: true, result: ports.ClearDevCheckResult{TrialCommandExecuted: true, Outcome: ports.ClearDevCheckFail, ExitCode: 1, CandidateSHA: execution.FinalReview.CandidateCommitSHA, ProjectExecutionSHA256: digest, SourceManifestID: strings.Repeat("a", 64), OutputSummary: "invalid input", OutputSHA256: coreDigest([]byte("invalid input"))}}
	f.s.checks = double
	f.s.resultPreview = nil
	if !f.s.stageReviewTrialReady(context.Background(), *execution.FinalReview, run) {
		t.Fatal("expected failure CLI wrongly needs an HTTP server")
	}
	if double.request.TrialStepID != step.ID || double.request.RunID == "" || double.request.WorkspacePath != execution.FinalReview.WorkspacePath {
		t.Fatal("trial lost reviewer-specific frozen binding")
	}
	original := double.result
	for _, mutate := range []func(){
		func() { double.found = false }, func() { double.err = errors.New("unsettled") }, func() { double.result.TrialCommandExecuted = false }, func() { double.result.CandidateSHA = "changed-candidate" }, func() { double.result.OutputTruncated = true }, func() { double.result.ExitCode = 0 }, func() { double.result.ProjectExecutionSHA256 = "other" }, func() { double.result.OutputSummary = "rewritten" },
	} {
		double.result = original
		double.found = true
		double.err = nil
		mutate()
		if f.s.stageReviewTrialReady(context.Background(), *execution.FinalReview, run) {
			t.Fatal("missing/invalid trial evidence allowed PASS")
		}
	}
}

// Real SQLite/source/role checks with an explicit command-executor double;
// actual container execution is independently exercised by the adapter test.
func TestStageTrialCommandUsesSavedContractAndOwningReviewer(t *testing.T) {
	ctx := context.Background()
	f := newProjectPlanningFixture(t, "EXISTING")
	product := f.create(t)
	var reply core.ProductDiscoveryResult
	if err := json.Unmarshal([]byte(genericProjectReply("existing")), &reply); err != nil {
		t.Fatal(err)
	}
	basis := reply.Stages[0].ExecutionBasis
	basis.Launch.Argv = []string{}
	basis.Trial = &core.ProjectTrial{SchemaVersion: 1, Steps: []core.ProjectTrialStep{{ID: "cli-input", Kind: "COMMAND", Argv: []string{"node", "src/storage.ts"}, TimeoutSeconds: 30, AcceptanceCriteria: reply.Stages[0].AcceptanceCriteria, Observe: "亲自运行并核对输出"}}}
	raw, _ := json.Marshal(reply)
	f.h.replies = append(f.h.replies, string(raw))
	chosen, err := f.s.SubmitProductDiscussion(ctx, product.Goal.ID, projectChoice(product, "existing"))
	if err != nil {
		t.Fatal(err)
	}
	_, child := f.prepare(t, chosen)
	f.h.replies = append(f.h.replies, genericEngineeringReply(t, child.RequirementVersions[0]))
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	child = mustGetComplex(t, f.s, child.Requirement.ID)
	plan := child.ComplexPlanning.Plans[0]
	preparer := &projectExecutionPreparer{projectPlanningAgent: f.h}
	f.s.checks, f.s.finalReviews = preparer, f.store
	f.s.runBackground = func(func()) {}
	h := attachProjectFlow(f, preparer)
	h.trial.disabled = true
	_, err = f.s.StartProjectExecution(ctx, child.Requirement.ID, core.ProjectExecutionAdmission{RequestID: "cli-admission", PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, RequirementSHA256: child.RequirementVersions[0].SHA256, BaseCommitSHA: chosen.Selection.BaseCommitSHA})
	if err != nil {
		t.Fatal(err)
	}
	execution := driveProjectFlow(t, f, child.Requirement.ID, true)
	for execution.FinalReview.Status != "SENT" {
		if _, stopped, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID); err != nil || stopped {
			t.Fatalf("send: %v", err)
		}
		execution, _, _ = f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
	}
	owner := domain.SessionID(execution.FinalReview.AOSessionID)
	token, verifier, err := browser.NewAuthority().Issue(owner)
	if err != nil {
		t.Fatal(err)
	}
	record, _, _ := f.store.GetSession(ctx, owner)
	record.Metadata.BrowserCapabilityVerifier = verifier
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	double := &trialExecutionDouble{projectExecutionFlowHarness: h}
	f.s.checks = double
	if f.s.stageReviewTrialReady(ctx, *execution.FinalReview, execution.Run) {
		t.Fatal("unexecuted trial accepted")
	}
	if _, err := f.s.StageReviewTrial(ctx, child.Requirement.ID, string(owner), "wrong", "run:cli-input"); err == nil {
		t.Fatal("invalid capability accepted")
	}
	status, err := f.s.StageReviewTrial(ctx, child.Requirement.ID, string(owner), token, "start")
	if err != nil || status.Preview.State == "ready" || h.trial.starts != 0 {
		t.Fatalf("CLI started HTTP: %+v %v", status, err)
	}
	out, err := f.s.StageReviewTrial(ctx, child.Requirement.ID, string(owner), token, "run:cli-input")
	if err != nil || out.Observation == nil || !out.Observation.Completed || double.calls != 1 {
		t.Fatalf("run: %+v %v", out, err)
	}
	if !f.s.stageReviewTrialReady(ctx, *execution.FinalReview, execution.Run) {
		t.Fatal("exact completed CLI evidence rejected")
	}
	if _, err := f.s.StageReviewTrial(ctx, child.Requirement.ID, string(owner), token, "run:unknown"); err == nil || double.calls != 1 {
		t.Fatal("unknown command was executed")
	}
}

type trialExecutionDouble struct {
	*projectExecutionFlowHarness
	calls int
	saved ports.ClearDevCheckResult
}

func (d *trialExecutionDouble) RunCandidateCheck(_ context.Context, r ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	d.calls++
	digest, _ := core.ProjectExecutionContractDigest(*r.ProjectExecution)
	d.saved = ports.ClearDevCheckResult{TrialCommandExecuted: true, Outcome: ports.ClearDevCheckPass, CandidateSHA: r.CandidateSHA, ProjectExecutionSHA256: digest, SourceManifestID: strings.Repeat("a", 64), OutputSummary: "actual executor double output", OutputSHA256: coreDigest([]byte("actual executor double output"))}
	return d.saved, nil
}
func (d *trialExecutionDouble) ReadCandidateCheck(context.Context, ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, bool, error) {
	return d.saved, d.calls > 0, nil
}

func TestFreshProjectProposalRequiresTrialButHistoricalDecodeRemainsValid(t *testing.T) {
	var result core.ProductDiscoveryResult
	if err := json.Unmarshal([]byte(genericProjectReply("empty")), &result); err != nil {
		t.Fatal(err)
	}
	result.Stages[0].ExecutionBasis.Trial = nil
	raw, _ := json.Marshal(result)
	if _, err := core.ParseProductDiscoveryResult(raw); err != nil {
		t.Fatalf("historical contract unreadable: %v", err)
	}
	if err := validateProjectDiscussion(raw, core.ProductDiscussion{}); err == nil || !strings.Contains(err.Error(), "STAGE_TRIAL_REQUIRED") {
		t.Fatalf("fresh proposal escaped trial: %v", err)
	}
}
