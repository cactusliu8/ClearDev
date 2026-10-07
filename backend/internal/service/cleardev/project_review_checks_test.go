package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Explicit model, Git and process doubles around the actual shared Service,
// SQLite, task/check contracts, review budgets and completion transactions.
type projectRequestingReviewer struct {
	*projectExecutionFlowHarness
	mode     string
	raw      string
	sessions []domain.SessionID
	reports  []string
	requests []ports.ClearDevCheckRequest
	memo     map[string]ports.ClearDevCheckResult
}

func attachProjectRequestingReviewer(f *projectPlanningFixture, p *projectExecutionPreparer) *projectRequestingReviewer {
	h := &projectRequestingReviewer{projectExecutionFlowHarness: attachProjectFlow(f, p),
		raw:  `{"schemaVersion":2,"kind":"REVIEW_CHECK_REQUEST","checkIds":["notes-tests"],"summary":"Check the admitted notes behavior against this exact candidate."}`,
		memo: map[string]ports.ClearDevCheckResult{}}
	f.s.chat, f.s.checks = h, h
	return h
}

func (h *projectRequestingReviewer) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, key string) (string, error) {
	_, alreadySent := h.turnByClientMessageID[key]
	turn, err := h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, session, prompt, key)
	if err != nil || alreadySent {
		return turn, err
	}
	isRequest := strings.Contains(prompt, "Optional bounded project check request:")
	isReport := strings.Contains(prompt, "Trusted requested-check results for the SAME candidate")
	if strings.Contains(prompt, parseCorrectionPromptPrefix) {
		h.mu.Lock()
		original := h.lastPromptBySession[session]
		h.mu.Unlock()
		isRequest = strings.Contains(original, "Optional bounded project check request:")
		isReport = strings.Contains(original, "Trusted requested-check results for the SAME candidate")
	}
	if !isRequest && !isReport {
		return turn, nil
	}
	h.sessions = append(h.sessions, session)
	if isReport {
		h.reports = append(h.reports, prompt)
	}
	if isRequest || isReport && h.mode == "request-again" {
		h.mu.Lock()
		snapshot := h.snapshots[session]
		for index := range snapshot.Messages {
			if snapshot.Messages[index].Role == domain.MessageRoleAssistant && snapshot.Messages[index].TurnID == turn {
				snapshot.Messages[index].Text = h.raw
			}
		}
		h.snapshots[session] = snapshot
		h.mu.Unlock()
	}
	return turn, nil
}

func (h *projectRequestingReviewer) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	if !strings.Contains(request.RunID, ":requested-check:") {
		return h.projectExecutionFlowHarness.RunCandidateCheck(ctx, request)
	}
	if result, ok := h.memo[request.RunID]; ok {
		return result, nil
	}
	h.requests = append(h.requests, request)
	result, err := h.projectExecutionFlowHarness.RunCandidateCheck(ctx, request)
	if err != nil {
		return result, err
	}
	switch h.mode {
	case "fail":
		result.Outcome, result.ExitCode = ports.ClearDevCheckFail, 3
	case "timeout":
		result.Outcome, result.ExitCode, result.TimedOut = ports.ClearDevCheckTimedOut, 137, true
	case "missing-environment":
		return ports.ClearDevCheckResult{}, errors.New("explicit missing project runtime fixture")
	case "stale":
		result.CandidateSHA = forty("e")
	case "truncated":
		result.OutputTruncated = true
	}
	h.memo[request.RunID] = result
	return result, nil
}

func TestProjectReviewerChecksStayBoundThroughFinalCompletion(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := attachProjectRequestingReviewer(f, preparer)
	ctx := context.Background()
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	completed := driveProjectFlow(t, f, child.Requirement.ID, false)
	if len(h.requests) != 1 || len(h.sessions) != 2 || h.sessions[0] != h.sessions[1] || len(h.reports) != 1 || h.finalSends != 1 {
		t.Fatalf("supplemental check/report/final review identities: requests=%d sessions=%v reports=%d final=%d", len(h.requests), h.sessions, len(h.reports), h.finalSends)
	}
	review := completed.Reviews[0]
	request, results, found, err := f.store.GetClearDevReviewerChecks(ctx, review.ID)
	if err != nil || !found || request.ProjectExecution == nil || len(results) != 1 || results[0].ProjectReceipt == nil {
		t.Fatalf("project request lost its runtime evidence: %+v %+v %v", request, results, err)
	}
	if request.CandidateSHA != completed.Integration.CandidateCommitSHA || h.requests[0].ProjectExecution == nil || h.requests[0].Timeout.Seconds() != 120 ||
		h.requests[0].WorkspacePath != review.CandidateWorkspacePath || core.ValidateReviewCheckEvidence(request, results[0]) != nil {
		t.Fatal("project Reviewer check was not run at the exact candidate with its frozen catalog")
	}
	original, _ := complexExecutionStepByID(completed, review.AgentStepID)
	final, ok := effectiveReviewStep(completed, review)
	if !ok || original.ID == final.ID || peekAgentResultKind([]byte(original.FinalMessageText)) != core.ReviewCheckRequestKind || peekAgentResultKind([]byte(final.FinalMessageText)) != "LOCAL_REVIEW" {
		t.Fatal("a check request replaced the final task review or its history")
	}
	var packet core.RequirementFinalReviewPacket
	if json.Unmarshal([]byte(completed.FinalReview.ReviewPacketJSON), &packet) != nil || len(packet.ReviewerChecks) != 1 ||
		packet.ReviewerChecks[0].Results[0].ProjectReceipt == nil || packet.ReviewerChecks[0].Request.ProjectExecution == nil {
		t.Fatal("the final reviewer did not receive the original request and trusted project receipt")
	}
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil || len(h.requests) != 1 || len(h.sessions) != 2 {
		t.Fatalf("completed replay dispatched more checks or model turns: %v", err)
	}
	changed := results[0]
	changed.Output = "changed stored output"
	if err := f.store.RecordClearDevReviewerCheck(ctx, changed); err == nil {
		t.Fatal("an immutable project review receipt was replaced")
	}
}

func TestProjectReviewerCannotWaiveFailedOrUnavailableChecks(t *testing.T) {
	for _, mode := range []string{"fail", "timeout", "missing-environment", "stale", "truncated", "request-again"} {
		t.Run(mode, func(t *testing.T) {
			f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
			h := attachProjectRequestingReviewer(f, preparer)
			h.mode = mode
			ctx := context.Background()
			if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
				t.Fatal(err)
			}
			stopped := false
			for transition := 0; transition < 240; transition++ {
				progressed, blocked, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
				if err != nil || blocked || !progressed {
					stopped = true
					break
				}
			}
			execution, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
			if err != nil || !found || !stopped || execution.Run.CompletedAt != nil || h.finalSends != 0 || len(h.requests) < 1 || len(h.reports) < 1 {
				t.Fatalf("failed/unknown checks passed or lost history: stopped=%v requests=%d reports=%d execution=%+v err=%v", stopped, len(h.requests), len(h.reports), execution.Run, err)
			}
			for _, review := range execution.Reviews {
				_, receipts, _, err := f.store.GetClearDevReviewerChecks(ctx, review.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, receipt := range receipts {
					if mode != "request-again" && receipt.Outcome == "PASS" {
						t.Fatal("an invalid or unsuccessful check became PASS")
					}
				}
			}
		})
	}
}
