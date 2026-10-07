package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// These are protocol/transaction tests, not live model or Docker acceptance.
// SQLite is real and reopened from disk; Git, Human Authority, check execution
// and Chat are explicitly inherited test doubles. No production database or
// hand-written terminal SQL is used to make a requirement pass.
type requirementFinalReviewHarness struct {
	*mailFlowChecks
	finalSessions map[domain.SessionID]bool
	verdict       string
	rawReply      string
	finalSends    int
	dirtySource   bool
	dirtyReviewer bool
	sourceSHA     string
}

func newRequirementFinalReviewFixture(t *testing.T) (*autoExecutionFixture, *requirementFinalReviewHarness) {
	t.Helper()
	f, mail := newMailFlowFixture(t)
	h := &requirementFinalReviewHarness{mailFlowChecks: mail, finalSessions: make(map[domain.SessionID]bool), verdict: "PASS"}
	attachRequirementFinalReviewFixture(f, h)
	return f, h
}

func attachRequirementFinalReviewFixture(f *autoExecutionFixture, h *requirementFinalReviewHarness) {
	f.service.finalReviews = f.store
	f.service.sessions, f.service.chat, f.service.inspector, f.service.checks = h, h, h, h
}

func (h *requirementFinalReviewHarness) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	session, a, b, err := h.standardAgentHarness.Spawn(ctx, cfg)
	if err != nil || !strings.HasPrefix(cfg.CreationIdempotencyKey, "cleardev-requirement-final-review:") {
		return session, a, b, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	sha := h.reviewBranchCandidates[cfg.Branch]
	if sha == "" {
		return domain.Session{}, 0, 0, errors.New("final reviewer branch was not prepared")
	}
	// The UI diff baseline may differ from HEAD; the real acceptance source is
	// InspectCandidate on the prepared exact final commit, not this metadata.
	h.reviewerWorkspaces[session.Metadata.WorkspacePath] = sha
	h.finalSessions[session.ID] = true
	return session, a, b, nil
}

func (h *requirementFinalReviewHarness) RelayChatTurnWithID(ctx context.Context, sessionID domain.SessionID, prompt, clientMessageID string) (string, error) {
	h.mu.Lock()
	final := h.finalSessions[sessionID]
	h.mu.Unlock()
	if !final {
		return h.mailFlowChecks.RelayChatTurnWithID(ctx, sessionID, prompt, clientMessageID)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if previous := h.turnByClientMessageID[clientMessageID]; previous != "" {
		return previous, nil
	}
	result := core.RequirementFinalReviewResult{
		SchemaVersion: 1, Kind: "REQUIREMENT_FINAL_REVIEW_RESULT", Verdict: h.verdict,
		Summary: "Whole-requirement protocol test result.", AcceptanceSummary: "All confirmed acceptance items were considered by this explicit model double.",
		ConsistencySummary: "The single task is consistent with the approved plan.", ScopeSummary: "The frozen scope proof is present.",
		RegressionSummary: "Old-test protection proof is present.", EvidenceSummary: "Task review and final npm-test/health proof reference the final candidate.",
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if h.rawReply != "" {
		raw = []byte(h.rawReply)
	}
	h.finalSends++
	turnID := fmt.Sprintf("final-turn-%d", h.finalSends)
	messageID := fmt.Sprintf("final-message-%d", h.finalSends)
	now := time.Date(2026, 8, 25, 10, 0, h.finalSends, 0, time.UTC)
	snapshot := h.snapshots[sessionID]
	snapshot.SessionID = sessionID
	snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{ID: turnID, HandledBySessionID: sessionID, State: domain.TurnStateCompleted, CompletedAt: &now})
	snapshot.Messages = append(snapshot.Messages,
		domain.ConversationMessage{ID: "user-" + messageID, TurnID: turnID, Sequence: int64(len(snapshot.Messages) + 1), Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: prompt, ClientMessageID: clientMessageID},
		domain.ConversationMessage{ID: messageID, TurnID: turnID, Sequence: int64(len(snapshot.Messages) + 2), Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Text: string(raw)},
	)
	h.snapshots[sessionID] = snapshot
	h.turnByClientMessageID[clientMessageID] = turnID
	h.relays = append(h.relays, standardRelay{sessionID: sessionID, clientMessageID: clientMessageID, prompt: prompt, response: string(raw)})
	return turnID, nil
}

func (h *requirementFinalReviewHarness) InspectCandidate(ctx context.Context, workspace, sha string) (ports.ClearDevCandidateInspection, error) {
	h.mu.Lock()
	dirty := h.dirtySource && workspace == h.builderWorkspace
	head := ""
	if workspace == h.builderWorkspace {
		head = h.sourceSHA
	}
	if h.dirtyReviewer && h.reviewerWorkspaces[workspace] != "" {
		dirty = true
	}
	h.mu.Unlock()
	if dirty {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevWorkspaceDirty
	}
	if head != "" {
		return ports.ClearDevCandidateInspection{BaseSHA: sha, CandidateSHA: head}, nil
	}
	return h.mailFlowChecks.InspectCandidate(ctx, workspace, sha)
}

type requirementFinalReviewRequestedChecksHarness struct {
	*requirementFinalReviewHarness
	requestRaw      string
	requestedChecks []ports.ClearDevCheckRequest
	reports         []string
	memo            map[string]ports.ClearDevCheckResult
}

func newRequirementFinalReviewRequestedChecksFixture(t *testing.T) (*autoExecutionFixture, *requirementFinalReviewRequestedChecksHarness) {
	t.Helper()
	f, base := newRequirementFinalReviewFixture(t)
	h := &requirementFinalReviewRequestedChecksHarness{
		requirementFinalReviewHarness: base,
		requestRaw:                    `{"schemaVersion":1,"kind":"REVIEW_CHECK_REQUEST","checkIds":["demo-api","demo-frontend"],"summary":"Verify the saved API and frontend evidence"}`,
		memo:                          make(map[string]ports.ClearDevCheckResult),
	}
	f.service.sessions, f.service.chat, f.service.inspector, f.service.checks = h, h, h, h
	return f, h
}

func (h *requirementFinalReviewRequestedChecksHarness) RelayChatTurnWithID(ctx context.Context, sessionID domain.SessionID, prompt, clientMessageID string) (string, error) {
	h.mu.Lock()
	final := h.finalSessions[sessionID]
	h.mu.Unlock()
	if final {
		return h.requirementFinalReviewHarness.RelayChatTurnWithID(ctx, sessionID, prompt, clientMessageID)
	}
	turnID, err := h.mailFlowChecks.RelayChatTurnWithID(ctx, sessionID, prompt, clientMessageID)
	if err != nil {
		return turnID, err
	}
	if strings.Contains(prompt, "Trusted requested-check results for the SAME candidate") {
		h.reports = append(h.reports, prompt)
	}
	if !strings.Contains(prompt, reviewerCheckInstructions) {
		return turnID, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := h.snapshots[sessionID]
	for i := range snapshot.Messages {
		if snapshot.Messages[i].TurnID == turnID && snapshot.Messages[i].Role == domain.MessageRoleAssistant {
			snapshot.Messages[i].Text = h.requestRaw
		}
	}
	h.snapshots[sessionID] = snapshot
	return turnID, nil
}

func (h *requirementFinalReviewRequestedChecksHarness) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	if !strings.Contains(request.RunID, ":requested-check:") {
		return h.mailFlowChecks.RunCandidateCheck(ctx, request)
	}
	if result, ok := h.memo[request.RunID]; ok {
		return result, nil
	}
	h.requestedChecks = append(h.requestedChecks, request)
	result, err := h.mailFlowChecks.RunCandidateCheck(ctx, request)
	result.OutputSummary = "trusted requirement-final-review supplemental check output"
	result.OutputSHA256 = coreDigest([]byte(result.OutputSummary))
	h.memo[request.RunID] = result
	return result, err
}

func finalReviewExecution(t *testing.T, f *autoExecutionFixture) core.ComplexExecutionSnapshot {
	t.Helper()
	snapshot, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil || !found {
		t.Fatalf("missing execution: found=%v err=%v", found, err)
	}
	return snapshot
}

func finalReviewCompletionCommand(t *testing.T, f *autoExecutionFixture, execution core.ComplexExecutionSnapshot) core.CompleteComplexExecutionCommand {
	t.Helper()
	if len(execution.Verifications) != 1 {
		t.Fatalf("need one real saved task verification, got %d", len(execution.Verifications))
	}
	checks := []string{}
	for _, check := range execution.CheckRuns {
		if check.Kind == core.CandidateCheckIntegration && check.Result == core.EvidenceResultPass {
			checks = append(checks, check.ID)
		}
	}
	if len(checks) == 0 {
		t.Fatal("test did not reach final integration checks")
	}
	at := f.clock()
	return core.CompleteComplexExecutionCommand{ExecutionRunID: execution.Run.ID, Integration: core.ComplexExecutionIntegration{
		ID: "final-review-gate-test-" + execution.Run.ID, ExecutionRunID: execution.Run.ID,
		IntegrationCandidateID: "s06-integration-" + execution.Run.ID,
		CandidateCommitSHA:     execution.Verifications[0].CandidateCommitSHA, CheckRunIDs: checks, CompletedAt: at,
	}, At: at}
}

func TestRequirementFinalReviewSingleTaskCompletion(t *testing.T) {
	f, h := newRequirementFinalReviewFixture(t)
	f.confirm(t)
	execution := f.completed(t)
	required, err := core.RequirementFinalReviewRequired(execution.Run)
	if err != nil || !required {
		t.Fatalf("new execution did not freeze final review: required=%v err=%v", required, err)
	}
	review := execution.FinalReview
	if review == nil || review.Status != "SETTLED" || review.Verdict != "PASS" || review.ResultID == "" || review.CandidateCommitSHA != execution.Integration.CandidateCommitSHA || h.finalSends != 1 {
		t.Fatalf("missing exact final PASS: review=%+v sends=%d", review, h.finalSends)
	}
	for _, binding := range execution.RoleBindings {
		if binding.AOSessionID == review.AOSessionID || binding.WorkspacePath == review.WorkspacePath {
			t.Fatal("final reviewer reused a task/planning role or workspace")
		}
	}
	if len(h.spawnConfigs) != 5 || len(execution.Reviews) != 1 {
		t.Fatalf("final review must be independent, not a second task review: sessions=%d taskReviews=%d", len(h.spawnConfigs), len(execution.Reviews))
	}
	var packet core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Requirement.RequirementText == "" || packet.Plan.PlanJSON == "" || len(packet.Tasks) != 1 || len(packet.TaskReviews) != 1 || len(packet.Verifications) != 1 || len(packet.CheckRuns) != len(execution.CheckRuns) || packet.Exception == nil {
		t.Fatalf("incomplete requirement-wide packet: %+v", packet)
	}
	expectedException, err := json.Marshal(execution.Exception)
	if err != nil {
		t.Fatal(err)
	}
	packetException, err := json.Marshal(packet.Exception)
	if err != nil {
		t.Fatal(err)
	}
	if string(packetException) != string(expectedException) {
		t.Fatalf("final packet exception authority differs from durable execution history:\nactual=%s\nexpected=%s", packetException, expectedException)
	}
	if err := core.ValidateRequirementFinalReviewBinding(*review, execution.Run, execution.Integration.CandidateCommitSHA, execution.Integration.CheckRunIDs); err != nil {
		t.Fatal(err)
	}
	counts := f.counts()
	f.reopen(t)
	attachRequirementFinalReviewFixture(f, h)
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := f.completed(t)
	if after.FinalReview == nil || after.FinalReview.ResultID != review.ResultID || f.counts() != counts {
		t.Fatal("completed restart changed final evidence or repeated work")
	}
}

func TestRequirementFinalReviewPacketIncludesReviewerRequestedChecks(t *testing.T) {
	f, h := newRequirementFinalReviewRequestedChecksFixture(t)
	f.confirm(t)
	execution := f.completed(t)
	if len(h.requestedChecks) != 2 || len(h.reports) != 1 {
		t.Fatalf("task Reviewer supplemental checks did not run normally: checks=%d reports=%d", len(h.requestedChecks), len(h.reports))
	}
	if execution.FinalReview == nil {
		t.Fatal("missing requirement final review")
	}
	var packet core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(execution.FinalReview.ReviewPacketJSON), &packet); err != nil {
		t.Fatal(err)
	}
	if len(packet.ReviewerChecks) != 1 || len(packet.TaskReviews) != 1 {
		t.Fatalf("final packet omitted task Reviewer supplemental evidence: %+v", packet.ReviewerChecks)
	}
	request, results, found, err := f.store.GetClearDevReviewerChecks(context.Background(), packet.TaskReviews[0].ID)
	if err != nil || !found || len(results) != 2 {
		t.Fatalf("missing durable task Reviewer requested checks: found=%v results=%d err=%v", found, len(results), err)
	}
	expectedRaw, err := json.Marshal(core.RequirementFinalReviewReviewerChecks{ReviewID: packet.TaskReviews[0].ID, Request: request, Results: results})
	if err != nil {
		t.Fatal(err)
	}
	actualRaw, err := json.Marshal(packet.ReviewerChecks[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(actualRaw) != string(expectedRaw) {
		t.Fatalf("final packet Reviewer checks differ from durable history:\nactual=%s\nexpected=%s", actualRaw, expectedRaw)
	}
}

func TestRequirementFinalReviewRejectsForgedReviewerRequestedChecks(t *testing.T) {
	f, _ := newRequirementFinalReviewRequestedChecksFixture(t)
	f.service.finalReviews = interceptFinalReviewStore{RequirementFinalReviewFactStore: f.store, create: func(review core.RequirementFinalReview) (core.RequirementFinalReview, error) {
		var packet core.RequirementFinalReviewPacket
		if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
			return review, err
		}
		if len(packet.ReviewerChecks) != 1 || len(packet.ReviewerChecks[0].Results) == 0 {
			return review, errors.New("test did not reach Reviewer requested-check evidence")
		}
		packet.ReviewerChecks[0].Results[0].Output = "invented supplemental evidence"
		raw, err := json.Marshal(packet)
		if err != nil {
			return review, err
		}
		review.ReviewPacketJSON, review.ReviewPacketSHA256 = string(raw), coreDigest(raw)
		review.PromptSHA256 = coreDigest([]byte(requirementFinalReviewPrompt(review)))
		return review, nil
	}}
	f.confirm(t)
	execution := finalReviewExecution(t, f)
	if execution.FinalReview != nil || len(execution.Reviews) != 1 || execution.Reviews[0].Verdict != core.LocalReviewPass {
		t.Fatalf("forged supplemental evidence was persisted or task review did not pass: %+v", execution.FinalReview)
	}
	if err := f.store.CompleteClearDevComplexExecution(context.Background(), finalReviewCompletionCommand(t, f, execution)); err == nil {
		t.Fatal("completion ignored missing exact requirement final review after forged supplemental evidence")
	}
	assertMailNotCompleted(t, f)
}

func TestRequirementFinalReviewNonPassCannotComplete(t *testing.T) {
	for _, verdict := range []string{"REWORK", "BLOCKED", "NEEDS_HUMAN"} {
		t.Run(verdict, func(t *testing.T) {
			f, h := newRequirementFinalReviewFixture(t)
			h.verdict = verdict
			f.confirm(t)
			assertMailNotCompleted(t, f)
			execution := finalReviewExecution(t, f)
			if execution.FinalReview == nil || execution.FinalReview.Status != "SETTLED" || execution.FinalReview.Verdict != verdict || len(execution.Reviews) != 1 || execution.Reviews[0].Verdict != core.LocalReviewPass {
				t.Fatalf("test did not reach independent non-PASS after task PASS: %+v", execution.FinalReview)
			}
			if err := f.store.CompleteClearDevComplexExecution(context.Background(), finalReviewCompletionCommand(t, f, execution)); err == nil {
				t.Fatal("completion transaction ignored non-PASS final review")
			}
			assertMailNotCompleted(t, f)
			before := f.counts()
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			if f.counts() != before {
				t.Fatal("non-PASS silently restarted review or task rework")
			}
		})
	}
}

type interceptFinalReviewStore struct {
	RequirementFinalReviewFactStore
	create func(core.RequirementFinalReview) (core.RequirementFinalReview, error)
}

func (s interceptFinalReviewStore) CreateClearDevRequirementFinalReview(ctx context.Context, review core.RequirementFinalReview) error {
	if s.create != nil {
		var err error
		review, err = s.create(review)
		if err != nil {
			return err
		}
	}
	return s.RequirementFinalReviewFactStore.CreateClearDevRequirementFinalReview(ctx, review)
}

func TestRequirementFinalReviewMissingOrWrongBindingCannotComplete(t *testing.T) {
	for _, mode := range []string{"missing", "requirement", "plan", "sha", "checks", "packet-check-summary", "packet-exception-authority"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newRequirementFinalReviewFixture(t)
			f.service.finalReviews = interceptFinalReviewStore{RequirementFinalReviewFactStore: f.store, create: func(review core.RequirementFinalReview) (core.RequirementFinalReview, error) {
				switch mode {
				case "missing":
					return review, errors.New("explicit test stop before final request persistence")
				case "requirement":
					review.DevelopmentRequirementID = "wrong-requirement"
				case "plan":
					review.PlanID = "wrong-plan"
				case "sha":
					review.CandidateCommitSHA = forty("f")
				case "checks":
					review.CheckRunIDs = []string{"foreign-check"}
				case "packet-check-summary", "packet-exception-authority":
					var packet core.RequirementFinalReviewPacket
					if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
						return review, err
					}
					if mode == "packet-check-summary" {
						packet.CheckRuns[0].OutputSummary = "invented passing evidence"
					} else {
						if packet.Exception == nil || len(packet.Exception.PermissionVersions) == 0 {
							return review, errors.New("test did not reach durable exception permission evidence")
						}
						packet.Exception.PermissionVersions[0].Version++
					}
					raw, err := json.Marshal(packet)
					if err != nil {
						return review, err
					}
					review.ReviewPacketJSON, review.ReviewPacketSHA256 = string(raw), coreDigest(raw)
					review.PromptSHA256 = coreDigest([]byte(requirementFinalReviewPrompt(review)))
				}
				return review, nil
			}}
			f.confirm(t)
			execution := finalReviewExecution(t, f)
			if execution.FinalReview != nil || len(execution.Reviews) != 1 || execution.Reviews[0].Verdict != core.LocalReviewPass {
				t.Fatalf("wrong final request was accepted or task review never passed: %+v", execution.FinalReview)
			}
			if err := f.store.CompleteClearDevComplexExecution(context.Background(), finalReviewCompletionCommand(t, f, execution)); err == nil {
				t.Fatal("task review/test PASS completed without an exact requirement final PASS")
			}
			assertMailNotCompleted(t, f)
		})
	}
}

func TestRequirementFinalReviewTaskResultIsNotAFinalResult(t *testing.T) {
	f, h := newRequirementFinalReviewFixture(t)
	h.rawReply = `{"schemaVersion":1,"kind":"LOCAL_REVIEW","verdict":"PASS","summary":"task PASS is not a final review"}`
	f.confirm(t)
	execution := finalReviewExecution(t, f)
	if execution.FinalReview == nil || execution.FinalReview.Status != "FAILED" || h.finalSends != 2 {
		t.Fatalf("wrong result did not stop after one bounded parse correction: final=%+v sends=%d", execution.FinalReview, h.finalSends)
	}
	assertMailNotCompleted(t, f)
}
