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
)

// Model replies, AO process creation, Git and checks in this file are explicit
// doubles. The entire task engine, source binding, Human Authority protocol,
// SQLite transactions and independent-review facts are real. Docker/Git tests
// live in cleardevlocal and are never attributed to these model doubles.
type projectExecutionFlowHarness struct {
	*projectExecutionPreparer
	frozen           map[string]ports.ClearDevCandidateInspection
	finalVerdict     string
	finalSends       int
	checkFailure     bool
	freshInstall     bool
	infraFailure     bool
	builderDiagnosis bool
	trial            *stageTrialFixtureManager
	service          *Service
}

func attachProjectFlow(f *projectPlanningFixture, preparer *projectExecutionPreparer) *projectExecutionFlowHarness {
	h := &projectExecutionFlowHarness{projectExecutionPreparer: preparer, frozen: map[string]ports.ClearDevCandidateInspection{}, finalVerdict: "PASS"}
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h
	h.trial = &stageTrialFixtureManager{}
	h.service = f.s
	f.s.resultPreview = h.trial
	return h
}

func (h *projectExecutionFlowHarness) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	session, a, b, err := h.standardAgentHarness.Spawn(ctx, cfg)
	if err != nil {
		return session, a, b, err
	}
	project, found, err := h.store.GetProject(ctx, string(cfg.ProjectID))
	if err != nil || !found {
		return session, a, b, errors.New("fixture project is missing")
	}
	session.Metadata.WorkspaceRepoPath = project.Path
	if cfg.WorkspaceBaseCommitSHA != "" {
		session.Metadata.DiffBaseSHA = cfg.WorkspaceBaseCommitSHA
	}
	if strings.HasPrefix(cfg.CreationIdempotencyKey, "cleardev-requirement-final-review:") {
		h.reviewerWorkspaces[session.Metadata.WorkspacePath] = h.reviewBranchCandidates[cfg.Branch]
	}
	if err := h.store.UpdateSession(ctx, session.SessionRecord); err != nil {
		return session, a, b, err
	}
	return session, a, b, nil
}

func (h *projectExecutionFlowHarness) RelayChatTurnWithID(ctx context.Context, sessionID domain.SessionID, prompt, key string) (string, error) {
	if prior := h.turnByClientMessageID[key]; prior != "" {
		return prior, nil
	}
	var turnID string
	var err error
	if strings.HasPrefix(prompt, "你是独立的 ClearDev Requirement-level Final Reviewer") {
		// Explicit trial/model double: this is not a real browser acceptance.
		if err := h.simulateStageTrial(ctx, sessionID, prompt); err != nil {
			return "", err
		}
		raw, encodeErr := json.Marshal(core.RequirementFinalReviewResult{
			SchemaVersion: 1, Kind: "REQUIREMENT_FINAL_REVIEW_RESULT", Verdict: h.finalVerdict,
			Summary:            "Explicit generic-project protocol fixture, not live acceptance.",
			AcceptanceSummary:  "The model double considered the whole Stage contract.",
			ConsistencySummary: "The task contract and the final candidate match.", ScopeSummary: "The confirmed project scope is used.",
			RegressionSummary: "The project permits justified test changes, not mailbox append-only behavior.",
			EvidenceSummary:   "Candidate-bound project check receipts are present; this is a test double.",
		})
		if encodeErr != nil {
			return "", encodeErr
		}
		h.replies = append(h.replies, string(raw))
		turnID, err = h.productTestAgent.RelayChatTurnWithID(ctx, sessionID, prompt, key)
		h.finalSends++
	} else if h.builderDiagnosis && (strings.HasPrefix(prompt, "控制程序失败先由Builder诊断") || strings.Contains(prompt, "用户通过原任务返工入口") || strings.Contains(prompt, "控制程序按默认诊断规则") || strings.Contains(prompt, "检查失败反馈")) && promptLineValue(prompt, "round=") != "0" {
		h.replies = append(h.replies, `{"schemaVersion":1,"kind":"BUILDER_RESULT","outcome":"BLOCKED","summary":"Control Plane lacks a supported persistence-failure action; candidate implementation cannot add an allowed trial command."}`)
		turnID, err = h.productTestAgent.RelayChatTurnWithID(ctx, sessionID, prompt, key)
	} else {
		turnID, err = h.standardAgentHarness.RelayChatTurnWithID(ctx, sessionID, prompt, key)
	}
	if err != nil {
		return turnID, err
	}
	record, found, err := h.store.GetSession(ctx, sessionID)
	if err != nil || !found {
		return turnID, errors.New("fixture session is missing")
	}
	record.Activity.State = domain.ActivityIdle
	return turnID, h.store.UpdateSession(ctx, record)
}

func (h *projectExecutionFlowHarness) PrepareReviewBranch(_ context.Context, _, branch, candidate string) error {
	h.reviewBranchCandidates[branch] = candidate
	return nil
}

func (h *projectExecutionFlowHarness) InspectCandidate(_ context.Context, workspace, base string) (ports.ClearDevCandidateInspection, error) {
	if reviewerSHA := h.reviewerWorkspaces[workspace]; reviewerSHA != "" {
		return ports.ClearDevCandidateInspection{BaseSHA: base, CandidateSHA: reviewerSHA, Paths: []ports.ClearDevDiffPath{}}, nil
	}
	candidate := h.currentCandidate
	if candidate == "" || workspace != h.builderWorkspace {
		candidate = base
	}
	paths := []ports.ClearDevDiffPath{}
	if candidate != base {
		paths = []ports.ClearDevDiffPath{{Status: "M", Path: "src/storage.ts"}, {Status: "M", Path: "tests/storage.test.ts"}, {Status: "A", Path: "migrations/001.sql"}}
	}
	return ports.ClearDevCandidateInspection{BaseSHA: base, CandidateSHA: candidate, Paths: paths}, nil
}

func (h *projectExecutionFlowHarness) FreezeMailCandidate(ctx context.Context, request ports.ClearDevMailFreezeRequest) (ports.ClearDevCandidateInspection, error) {
	if request.ProjectExecution == nil || core.ValidateProjectExecutionContract(*request.ProjectExecution) != nil || request.RepoPath != request.ProjectExecution.Selection.RepositoryPath {
		return ports.ClearDevCandidateInspection{}, errors.New("fixture freeze has no admitted project")
	}
	if prior, found := h.frozen[request.RunID]; found {
		return prior, nil
	}
	candidate, err := h.InspectCandidate(ctx, request.WorkspacePath, request.BaseSHA)
	if err != nil {
		return candidate, err
	}
	h.frozen[request.RunID] = candidate
	return candidate, nil
}

func (h *projectExecutionFlowHarness) RunCandidateCheck(_ context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	if request.ProjectExecution == nil {
		return ports.ClearDevCheckResult{}, errors.New("project execution used the historical checker request")
	}
	known := false
	for _, candidate := range h.frozen {
		known = known || candidate.CandidateSHA == request.CandidateSHA
	}
	if !known {
		return ports.ClearDevCheckResult{}, errors.New("project check preceded backend candidate freeze")
	}
	h.checkRequests = append(h.checkRequests, request)
	contractDigest, err := core.ProjectExecutionContractDigest(*request.ProjectExecution)
	if err != nil {
		return ports.ClearDevCheckResult{}, err
	}
	argv, _ := json.Marshal([][]string{request.Argv})
	output := "Explicit check double; no actual Node process was invoked by this test."
	result := ports.ClearDevCheckResult{
		Outcome: ports.ClearDevCheckPass, CandidateSHA: request.CandidateSHA, ProjectExecutionSHA256: contractDigest,
		Image: request.Image, ImageID: "sha256:" + strings.Repeat("f", 64), SourceManifestID: strings.Repeat("1", 64), SourceRootTreeOID: forty("2"),
		CheckEnvironmentID: strings.Repeat("3", 64), ApprovedArgvSHA256: coreDigest(argv), NodeVersion: "v22.23.2", NPMVersion: "10.9.8",
		PackageJSONSHA256: strings.Repeat("4", 64), PackageLockSHA256: strings.Repeat("5", 64), DependencyCacheKey: strings.Repeat("6", 64),
		DependencyEnvironmentID: strings.Repeat("7", 64), DependencyTreeSHA256: strings.Repeat("8", 64),
		OutputSummary: output, OutputSHA256: coreDigest([]byte(output)),
	}
	if h.freshInstall {
		result.Image = core.ProjectCandidateCheckImage
		result.DependencyCacheKey, result.DependencyEnvironmentID, result.DependencyTreeSHA256 = "", "", ""
	}
	if h.infraFailure {
		result.Outcome = ports.ClearDevCheckInfraError
		return result, errors.New("fixture native-driver build directory missing")
	}
	if h.checkFailure {
		result.Outcome, result.ExitCode = ports.ClearDevCheckFail, 3
	}
	return result, nil
}

func driveProjectFlow(t *testing.T, f *projectPlanningFixture, requirementID string, stopAtFinal bool) core.ComplexExecutionSnapshot {
	t.Helper()
	for transition := 0; transition < 160; transition++ {
		execution, found, err := f.store.GetClearDevComplexExecution(context.Background(), requirementID)
		if err != nil || !found {
			t.Fatalf("read project execution: %v", err)
		}
		phase, reason := core.DeriveComplexExecutionPhase(execution)
		if execution.Run.CompletedAt != nil || stopAtFinal && execution.FinalReview != nil {
			return execution
		}
		progressed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), requirementID)
		if err != nil || stopped || !progressed {
			t.Fatalf("project transition %d phase=%s reason=%s progressed=%v stopped=%v err=%v", transition, phase, reason, progressed, stopped, err)
		}
	}
	t.Fatal("project execution exceeded its bounded transition fixture")
	return core.ComplexExecutionSnapshot{}
}

func TestProjectExecutionSharedLoopRequiresIndependentFinalReview(t *testing.T) {
	for _, origin := range []string{"EMPTY", "EXISTING"} {
		t.Run(origin, func(t *testing.T) {
			f, child, admission, preparer := plannedProjectExecutionFixture(t, origin)
			h := attachProjectFlow(f, preparer)
			if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
				t.Fatal(err)
			}
			beforeFinal := driveProjectFlow(t, f, child.Requirement.ID, true)
			if len(beforeFinal.Verifications) != 1 || len(beforeFinal.Reviews) != 1 || beforeFinal.Run.CompletedAt != nil || h.finalSends != 0 {
				t.Fatal("Task verification replaced independent final acceptance")
			}
			var integrationChecks []string
			for _, check := range beforeFinal.CheckRuns {
				if check.Kind == core.CandidateCheckIntegration {
					integrationChecks = append(integrationChecks, check.ID)
				}
			}
			premature := core.CompleteComplexExecutionCommand{ExecutionRunID: beforeFinal.Run.ID, At: time.Now().UTC(),
				Integration: core.ComplexExecutionIntegration{ID: "premature-result", ExecutionRunID: beforeFinal.Run.ID, IntegrationCandidateID: "premature-candidate",
					CandidateCommitSHA: beforeFinal.Verifications[0].CandidateCommitSHA, CheckRunIDs: integrationChecks, CompletedAt: time.Now().UTC()}}
			if err := f.store.CompleteClearDevComplexExecution(context.Background(), premature); err == nil {
				t.Fatal("task PASS bypassed final review in the completion transaction")
			}
			completed := driveProjectFlow(t, f, child.Requirement.ID, false)
			if completed.Run.CompletedAt == nil || completed.FinalReview == nil || h.finalSends != 1 || len(h.frozen) != 1 || h.mailCalls != 0 {
				t.Fatal("generic shared loop was not independently finalized or invoked mailbox policy")
			}
			beforeCalls, beforeChecks := len(h.relays), len(h.checkRequests)
			if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
				t.Fatalf("completed admission replay: %v", err)
			}
			if len(h.relays) != beforeCalls || len(h.checkRequests) != beforeChecks {
				t.Fatal("completed request redispatched work")
			}
		})
	}
}

func TestProjectFreshInstallReceiptSurvivesCompletion(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := attachProjectFlow(f, preparer)
	h.freshInstall = true
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	completed := driveProjectFlow(t, f, child.Requirement.ID, false)
	if completed.Run.CompletedAt == nil || len(completed.CheckRuns) == 0 {
		t.Fatal("fresh check receipts did not reach completion")
	}
	for _, check := range completed.CheckRuns {
		if check.Kind == core.CandidateCheckScope {
			continue
		}
		var receipt core.ProjectCheckReceipt
		if err := json.Unmarshal([]byte(check.OutputSummary), &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Policy != core.ProjectCheckPolicyV2 || receipt.DependencyCacheKey != "" {
			t.Fatalf("wrong stored policy: %+v", receipt)
		}
	}
}
