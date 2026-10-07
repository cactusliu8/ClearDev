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

type projectReplacementReviewer struct {
	*projectRequestingReviewer
	failedOriginal     bool
	replacementSession domain.SessionID
}

func attachProjectReplacementReviewer(f *projectPlanningFixture, p *projectExecutionPreparer) *projectReplacementReviewer {
	base := attachProjectRequestingReviewer(f, p)
	h := &projectReplacementReviewer{projectRequestingReviewer: base}
	f.s.chat, f.s.checks, f.s.inspector, f.s.sessions = h, h, h, h
	return h
}

func (h *projectReplacementReviewer) ValidateRecoveryWorkspace(ctx context.Context, workspace, branch, sha string) error {
	observed, err := h.InspectCandidate(ctx, workspace, sha)
	if err != nil || observed.BaseSHA != sha || observed.CandidateSHA != sha || len(observed.Paths) != 0 {
		return errors.New("project replacement recovery workspace mismatch")
	}
	return nil
}

func (h *projectReplacementReviewer) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	session, input, output, err := h.projectExecutionFlowHarness.Spawn(ctx, cfg)
	if err == nil && cfg.DisplayName == "ClearDev Replacement Reviewer" {
		h.replacementSession = session.ID
	}
	return session, input, output, err
}

func (h *projectReplacementReviewer) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, key string) (string, error) {
	h.mu.Lock()
	prior := h.turnByClientMessageID[key]
	h.mu.Unlock()
	if prior != "" {
		return prior, nil
	}
	if strings.HasPrefix(prompt, "你是 ClearDev Recovery。") {
		raw, err := promptJSONObject(prompt, "RECOVERY_RESULT")
		if err != nil {
			return "", err
		}
		var result core.ComplexRecoveryResultWire
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return "", err
		}
		result.Action = core.ComplexRecoveryActionRebuildReviewer
		encoded, err := core.MarshalAgentChosenResult(result)
		if err != nil {
			return "", err
		}
		return h.saveProjectReplacementTurn(ctx, session, prompt, key, string(encoded))
	}

	isReview := strings.Contains(prompt, `"kind":"LOCAL_REVIEW"`) && !strings.Contains(prompt, `"kind":"BUILDER_RESULT"`)
	if isReview && !h.failedOriginal && session != h.replacementSession {
		turn, err := h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, session, prompt, key)
		if err != nil {
			return turn, err
		}
		h.failedOriginal = true
		h.mu.Lock()
		snapshot := h.snapshots[session]
		for index := range snapshot.Turns {
			if snapshot.Turns[index].ID == turn {
				snapshot.Turns[index].State = domain.TurnStateFailed
				snapshot.Turns[index].Failure = &domain.ConversationFailure{
					Category: domain.AgentFailureProviderUnavailable, Retryable: true,
					ErrorSummary: "explicit project Reviewer provider failure",
				}
			}
		}
		messages := snapshot.Messages[:0]
		for _, message := range snapshot.Messages {
			if message.TurnID != turn || message.Role != domain.MessageRoleAssistant {
				messages = append(messages, message)
			}
		}
		snapshot.Messages = messages
		h.snapshots[session] = snapshot
		h.mu.Unlock()
		record, found, readErr := h.store.GetSession(ctx, session)
		if readErr != nil || !found {
			return turn, errors.New("failed project Reviewer session is missing")
		}
		record.Activity.State = domain.ActivityExited
		return turn, h.store.UpdateSession(ctx, record)
	}
	return h.projectRequestingReviewer.RelayChatTurnWithID(ctx, session, prompt, key)
}

func (h *projectReplacementReviewer) saveProjectReplacementTurn(ctx context.Context, session domain.SessionID, prompt, key, response string) (string, error) {
	h.mu.Lock()
	turn := fmt.Sprintf("project-replacement-recovery-%03d", len(h.relays)+1)
	now := time.Now().UTC()
	snapshot := h.snapshots[session]
	snapshot.SessionID = session
	snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{
		ID: turn, HandledBySessionID: session, State: domain.TurnStateCompleted, CompletedAt: &now,
	})
	snapshot.Messages = append(snapshot.Messages,
		domain.ConversationMessage{
			ID: turn + ":user", TurnID: turn, Sequence: int64(len(snapshot.Messages) + 1),
			Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: prompt, ClientMessageID: key,
		},
		domain.ConversationMessage{
			ID: turn + ":reply", TurnID: turn, Sequence: int64(len(snapshot.Messages) + 2),
			Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Text: response,
		},
	)
	h.snapshots[session] = snapshot
	h.turnByClientMessageID[key] = turn
	h.relays = append(h.relays, standardRelay{sessionID: session, clientMessageID: key, prompt: prompt, response: response})
	h.mu.Unlock()

	record, found, err := h.store.GetSession(ctx, session)
	if err != nil || !found {
		return "", errors.New("project Recovery session is missing")
	}
	record.Activity.State = domain.ActivityIdle
	if err := h.store.UpdateSession(ctx, record); err != nil {
		return "", err
	}
	return turn, nil
}

func TestProjectReplacementReviewerCanRequestBoundedChecks(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	h := attachProjectReplacementReviewer(f, preparer)
	ctx := context.Background()
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	var completed core.ComplexExecutionSnapshot
	for transition := 0; transition < 220; transition++ {
		execution, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
		if err != nil || !found {
			t.Fatalf("read replacement project execution: %v", err)
		}
		if execution.Run.CompletedAt != nil {
			completed = execution
			break
		}
		for _, fixed := range execution.FixedRecoveries {
			if fixed.Refusal != nil {
				t.Fatalf("replacement project recovery refused: %s request=%+v", fixed.Refusal.ReasonCode, fixed.Request)
			}
		}
		progressed, stopped, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
		if err != nil {
			phase, reason := core.DeriveComplexExecutionPhase(execution)
			finalStatus := ""
			if execution.FinalReview != nil {
				finalStatus = execution.FinalReview.Status
			}
			t.Fatalf("replacement project transition %d phase=%s reason=%s final=%s recoveries=%d: %v", transition, phase, reason, finalStatus, len(execution.FixedRecoveries), err)
		}
		if stopped && !progressed {
			phase, reason := core.DeriveComplexExecutionPhase(execution)
			t.Fatalf("replacement project stopped at %d phase=%s reason=%s fixed=%+v", transition, phase, reason, execution.FixedRecoveries)
		}
	}
	if completed.Run.ID == "" {
		execution, _, _ := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
		t.Fatalf("replacement project did not complete: fixed=%+v", execution.FixedRecoveries)
	}
	if !h.failedOriginal || h.replacementSession == "" || completed.Run.CompletedAt == nil {
		t.Fatalf("replacement project review did not complete: failed=%v replacement=%q completed=%v", h.failedOriginal, h.replacementSession, completed.Run.CompletedAt)
	}
	if len(completed.FixedRecoveries) != 1 || completed.FixedRecoveries[0].ReviewResult == nil || completed.FixedRecoveries[0].ReviewResult.Verdict != core.LocalReviewPass {
		t.Fatalf("missing replacement Reviewer result: %+v", completed.FixedRecoveries)
	}
	if len(h.requests) != 1 || len(h.reports) != 1 {
		t.Fatalf("replacement supplemental checks requests=%d reports=%d", len(h.requests), len(h.reports))
	}
	review := completed.Reviews[0]
	if review.Verdict != "" {
		t.Fatal("original failed project review was overwritten")
	}
	request, results, found, err := f.store.GetClearDevReviewerChecks(ctx, review.ID)
	if err != nil || !found || request.ProjectExecution == nil || len(results) != 1 || results[0].ProjectReceipt == nil {
		t.Fatalf("replacement project check evidence missing: request=%+v results=%+v err=%v", request, results, err)
	}
	fixed := completed.FixedRecoveries[0]
	if request.ReplacementRecoveryID != fixed.Request.ID || request.RequestAttemptID == "" || request.RequestResultID == "" {
		t.Fatalf("replacement provenance not bound to project check request: %+v fixed=%+v", request, fixed)
	}
	if core.ValidateReviewCheckRequestForRun(request, completed.Run) != nil || core.ValidateReviewCheckEvidence(request, results[0]) != nil {
		t.Fatal("persisted replacement project checks no longer validate")
	}
	bound := false
	for _, verification := range completed.Verifications {
		if verification.LocalReviewID == review.ID && verification.ReplacementRecoveryID == fixed.Request.ID &&
			verification.ReplacementAttemptID == fixed.ReviewResult.AttemptID && verification.ReplacementResultID == fixed.ReviewResult.ResultID {
			bound = true
		}
	}
	if !bound {
		t.Fatal("project verification did not retain exact replacement Reviewer provenance")
	}
	beforeRequests, beforeReports := len(h.requests), len(h.reports)
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	if len(h.requests) != beforeRequests || len(h.reports) != beforeReports {
		t.Fatal("completed project replay repeated replacement checks")
	}
}
