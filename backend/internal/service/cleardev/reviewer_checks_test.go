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
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// Explicit external-model/checker doubles; all requests, steps, budgets,
// verdicts and completions are persisted through the real SQLite store.
type requestingReviewer struct {
	*mailFlowChecks
	raw      string
	mode     string
	checks   []ports.ClearDevCheckRequest
	sessions []domain.SessionID
	reports  []string
	memo     map[string]ports.ClearDevCheckResult
}

func newRequestingReviewer(t *testing.T) (*autoExecutionFixture, *requestingReviewer) {
	t.Helper()
	f, mail := newMailFlowFixture(t)
	h := &requestingReviewer{mailFlowChecks: mail, raw: `{"schemaVersion":1,"kind":"REVIEW_CHECK_REQUEST","checkIds":["demo-api","demo-frontend"],"summary":"Verify API and page behavior independently"}`, memo: map[string]ports.ClearDevCheckResult{}}
	f.service.chat, f.service.checks, f.service.inspector = h, h, h
	return f, h
}

func (h *requestingReviewer) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, key string) (string, error) {
	turn, err := h.mailFlowChecks.RelayChatTurnWithID(ctx, session, prompt, key)
	if err != nil {
		return turn, err
	}
	isReport := strings.Contains(prompt, "Trusted requested-check results for the SAME candidate")
	isRequest := strings.Contains(prompt, reviewerCheckInstructions)
	if strings.Contains(prompt, parseCorrectionPromptPrefix) {
		h.mu.Lock()
		previous := h.lastPromptBySession[session]
		h.mu.Unlock()
		isReport = strings.Contains(previous, "Trusted requested-check results for the SAME candidate")
		isRequest = strings.Contains(previous, reviewerCheckInstructions)
	}
	if !isReport && !isRequest {
		return turn, nil
	}
	h.sessions = append(h.sessions, session)
	if isReport {
		h.reports = append(h.reports, prompt)
	}
	if isRequest || isReport && h.mode == "request-again" {
		h.mu.Lock()
		snapshot := h.snapshots[session]
		for i := range snapshot.Messages {
			if snapshot.Messages[i].TurnID == turn && snapshot.Messages[i].Role == domain.MessageRoleAssistant {
				snapshot.Messages[i].Text = h.raw
			}
		}
		h.snapshots[session] = snapshot
		h.mu.Unlock()
	}
	return turn, nil
}

func (h *requestingReviewer) Snapshot(ctx context.Context, session domain.SessionID) (chatsvc.Snapshot, error) {
	if h.mode == "unknown-receipt" && len(h.reports) > 0 {
		return chatsvc.Snapshot{}, errors.New("fake provider delivery remains unknown")
	}
	return h.mailFlowChecks.Snapshot(ctx, session)
}

func (h *requestingReviewer) PrepareBaseWorkspace(ctx context.Context, workspace, oldSHA, newSHA string) error {
	h.mu.Lock()
	if old, exists := h.reviewerWorkspaces[workspace]; exists {
		defer h.mu.Unlock()
		if old != oldSHA {
			return ports.ErrClearDevCandidateInvalid
		}
		h.reviewerWorkspaces[workspace] = newSHA
		return nil
	}
	h.mu.Unlock()
	return h.mailFlowChecks.PrepareBaseWorkspace(ctx, workspace, oldSHA, newSHA)
}

func (h *requestingReviewer) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	if !strings.Contains(request.RunID, ":requested-check:") {
		return h.mailFlowChecks.RunCandidateCheck(ctx, request)
	}
	if result, exists := h.memo[request.RunID]; exists {
		return result, nil
	}
	h.checks = append(h.checks, request)
	result, err := h.mailFlowChecks.RunCandidateCheck(ctx, request)
	result.OutputSummary = "trusted additional check output"
	result.OutputSHA256 = coreDigest([]byte(result.OutputSummary))
	switch h.mode {
	case "fail":
		result.Outcome, result.ExitCode = ports.ClearDevCheckFail, 1
	case "infra":
		result.Outcome, result.ExitCode = ports.ClearDevCheckInfraError, -1
	case "stale":
		result.CandidateSHA = forty("f")
	case "truncated":
		result.OutputTruncated = true
	}
	h.memo[request.RunID] = result
	return result, err
}

func TestReviewerRequestedChecksReturnToSameSessionAndComplete(t *testing.T) {
	f, h := newRequestingReviewer(t)
	f.confirm(t)
	execution := f.completed(t)
	if len(h.checks) != 2 || len(h.sessions) != 2 || h.sessions[0] != h.sessions[1] || len(h.reports) != 1 || f.counts().spawns != 4 {
		t.Fatalf("checks=%d sessions=%v reports=%d", len(h.checks), h.sessions, len(h.reports))
	}
	review := execution.Reviews[0]
	request, results, found, err := f.store.GetClearDevReviewerChecks(context.Background(), review.ID)
	if err != nil || !found || len(results) != 2 || request.CandidateSHA != execution.Integration.CandidateCommitSHA {
		t.Fatalf("request/results: %+v %+v %v", request, results, err)
	}
	original, _ := complexExecutionStepByID(execution, review.AgentStepID)
	final, ok := effectiveReviewStep(execution, review)
	if !ok || original.ID == final.ID || original.TurnID == final.TurnID || peekAgentResultKind([]byte(original.FinalMessageText)) != core.ReviewCheckRequestKind || peekAgentResultKind([]byte(final.FinalMessageText)) != "LOCAL_REVIEW" {
		t.Fatal("request was overwritten or used as final review")
	}
	for _, check := range h.checks {
		if check.CandidateSHA != review.CandidateCommitSHA || check.RunID == "" || check.WorkspacePath != review.CandidateWorkspacePath || check.OutputLimit != standardCheckOutputLimit {
			t.Fatalf("unbound check: %+v", check)
		}
	}
	counts := f.counts()
	f.reopen(t)
	f.service.chat, f.service.checks, f.service.inspector = h, h, h.mailFlowChecks
	if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.completed(t)
	if len(h.checks) != 2 || len(h.sessions) != 2 || counts != f.counts() {
		t.Fatal("completed recovery repeated work")
	}
}

// The task review packet carries the trusted executor's full check log, so
// the Reviewer reads recorded evidence bound by its receipt instead of
// re-running the check.
func TestReviewPacketCarriesFullCheckLogs(t *testing.T) {
	f, h := newRequestingReviewer(t)
	f.confirm(t)
	execution := f.completed(t)
	if len(h.checks) == 0 {
		t.Fatal("no checks ran")
	}
	var packet struct {
		Checks []struct {
			ID            string `json:"id"`
			Result        string `json:"result"`
			OutputSummary string `json:"outputSummary"`
			OutputSHA256  string `json:"outputSha256"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(execution.Reviews[0].ReviewPacketJSON), &packet); err != nil {
		t.Fatal(err)
	}
	if len(packet.Checks) == 0 {
		t.Fatal("review packet has no settled checks")
	}
	for _, check := range packet.Checks {
		if check.Result != string(core.EvidenceResultPass) {
			t.Fatalf("unexpected non-pass check in packet: %+v", check)
		}
		if check.OutputSummary == "" {
			t.Fatalf("check %s log missing from the review packet", check.ID)
		}
		if coreDigest([]byte(check.OutputSummary)) != check.OutputSHA256 {
			t.Fatalf("check %s log does not match its receipt", check.ID)
		}
	}
}

func TestReviewerRequestedChecksCannotWaiveFailures(t *testing.T) {
	for _, mode := range []string{"fail", "infra", "stale", "truncated", "request-again"} {
		t.Run(mode, func(t *testing.T) {
			f, h := newRequestingReviewer(t)
			h.mode = mode
			f.confirm(t)
			assertMailNotCompleted(t, f)
			if len(h.checks) != 2 || len(h.reports) == 0 {
				t.Fatal("requested checks/results did not reach original Reviewer")
			}
			for _, session := range h.sessions {
				if session != h.sessions[0] {
					t.Fatal("spawned another Reviewer")
				}
			}
		})
	}
}

func TestReviewerRequestedChecksRejectUnapprovedInput(t *testing.T) {
	for _, raw := range []string{
		`{"schemaVersion":1,"kind":"REVIEW_CHECK_REQUEST","checkIds":["demo-database"],"summary":"no"}`,
		`{"schemaVersion":1,"kind":"REVIEW_CHECK_REQUEST","checkIds":["demo-api"],"summary":"no","argv":["sh","-c","true"]}`,
		`{"schemaVersion":1,"kind":"REVIEW_CHECK_REQUEST","checkIds":["demo-api","demo-api"],"summary":"no"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			f, h := newRequestingReviewer(t)
			h.raw = raw
			f.confirm(t)
			assertMailNotCompleted(t, f)
			if len(h.checks) != 0 {
				t.Fatal("executed unapproved check")
			}
		})
	}
}

// Fault injection is at the storage seam, never SQL state manipulation.
type reviewerCheckFaultStore struct {
	ComplexExecutionFactStore
	ReviewerCheckStore
	mode string
	hit  bool
}

func (s *reviewerCheckFaultStore) RequestClearDevReviewerChecks(ctx context.Context, request core.ReviewCheckRequest) error {
	err := s.ReviewerCheckStore.RequestClearDevReviewerChecks(ctx, request)
	if err == nil && s.mode == "after-request" && !s.hit {
		s.hit = true
		return errors.New("injected crash after request")
	}
	return err
}
func (s *reviewerCheckFaultStore) RecordClearDevReviewerCheck(ctx context.Context, result core.ReviewCheckEvidence) error {
	if s.mode == "before-result" && !s.hit {
		s.hit = true
		return errors.New("injected crash before receipt")
	}
	err := s.ReviewerCheckStore.RecordClearDevReviewerCheck(ctx, result)
	if err == nil && s.mode == "after-result" && !s.hit {
		s.hit = true
		return errors.New("injected crash after receipt")
	}
	return err
}
func (s *reviewerCheckFaultStore) CreateClearDevComplexExecutionAgentStep(ctx context.Context, step core.AgentStep) (core.AgentStep, bool, error) {
	saved, created, err := s.ComplexExecutionFactStore.CreateClearDevComplexExecutionAgentStep(ctx, step)
	if err == nil && strings.HasSuffix(step.ID, ":check-results") && s.mode == "after-followup" && !s.hit {
		s.hit = true
		return saved, created, errors.New("injected crash after followup")
	}
	return saved, created, err
}
func (s *reviewerCheckFaultStore) MarkClearDevComplexExecutionAgentStepSent(ctx context.Context, id string, at time.Time) (bool, error) {
	if strings.HasSuffix(id, ":check-results") && s.mode == "after-send" && !s.hit {
		s.hit = true
		return false, errors.New("injected crash after send")
	}
	return s.ComplexExecutionFactStore.MarkClearDevComplexExecutionAgentStepSent(ctx, id, at)
}

func TestReviewerRequestedChecksResumeDurableBoundaries(t *testing.T) {
	for _, mode := range []string{"after-request", "before-result", "after-result", "after-followup", "after-send"} {
		t.Run(mode, func(t *testing.T) {
			f, h := newRequestingReviewer(t)
			fault := &reviewerCheckFaultStore{ComplexExecutionFactStore: f.store, ReviewerCheckStore: f.store, mode: mode}
			f.service.complexExecution = fault
			f.confirm(t)
			if !fault.hit {
				t.Fatal("crash boundary not reached")
			}
			assertMailNotCompleted(t, f)
			f.reopen(t)
			f.service.chat, f.service.checks, f.service.inspector = h, h, h.mailFlowChecks
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.completed(t)
			if len(h.checks) != 2 || len(h.sessions) != 2 || f.counts().spawns != 4 {
				t.Fatalf("duplicated work checks=%d sessions=%d", len(h.checks), len(h.sessions))
			}
		})
	}
}
