package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Explicit remote model and workspace doubles; authorization, attempts, raw
// results, requested checks, rework and completion all use production SQLite.
type replacementMailHarness struct {
	*requestingReviewer
	failed              bool
	checksEnabled       bool
	repair              bool
	replacementTurns    int
	replacementSession  domain.SessionID
	relayCalls          map[string]int
	repairLimit         int
	developmentFailures int
	whitespaceSummary   bool
}

func newReplacementMailFixture(t *testing.T, checks, repair bool) (*autoExecutionFixture, *replacementMailHarness) {
	t.Helper()
	f, base := newRequestingReviewer(t)
	h := &replacementMailHarness{requestingReviewer: base, checksEnabled: checks, repair: repair, relayCalls: map[string]int{}, repairLimit: 1}
	f.service.boundedMailAttempts = true
	f.service.logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	f.service.chat, f.service.checks, f.service.inspector, f.service.sessions = h, h, h, h
	return f, h
}

func (h *replacementMailHarness) Spawn(ctx context.Context, config ports.SpawnConfig) (domain.Session, int, int, error) {
	session, a, b, err := h.standardAgentHarness.Spawn(ctx, config)
	if err == nil && config.DisplayName == "ClearDev Replacement Reviewer" {
		h.replacementSession = session.ID
	}
	return session, a, b, err
}

func (h *replacementMailHarness) PrepareReviewBranch(ctx context.Context, workspace, branch, sha string) error {
	h.mu.Lock()
	if h.reviewerWorkspaces[workspace] == sha && strings.HasPrefix(branch, "cleardev-complex-review-recovery-") {
		h.reviewBranchCandidates[branch] = sha
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	return h.mailFlowChecks.PrepareReviewBranch(ctx, workspace, branch, sha)
}

func (h *replacementMailHarness) ValidateRecoveryWorkspace(ctx context.Context, workspace, branch, sha string) error {
	observed, err := h.InspectCandidate(ctx, workspace, sha)
	if err != nil || observed.CandidateSHA != sha || len(observed.Paths) != 0 {
		return errors.New("explicit fake recovery workspace mismatch")
	}
	return nil
}

func (h *replacementMailHarness) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, key string) (string, error) {
	h.mu.Lock()
	h.relayCalls[key]++
	previous := h.turnByClientMessageID[key]
	h.mu.Unlock()
	if previous != "" {
		return previous, nil
	}
	if strings.Contains(prompt, `"kind":"RECOVERY_RESULT"`) {
		raw, err := promptJSONObject(prompt, "RECOVERY_RESULT")
		if err != nil {
			return "", err
		}
		var result core.ComplexRecoveryResultWire
		if err = json.Unmarshal([]byte(raw), &result); err != nil {
			return "", err
		}
		result.Action = core.ComplexRecoveryActionRebuildReviewer
		rawBytes, err := core.MarshalAgentChosenResult(result)
		if err != nil {
			return "", err
		}
		return h.saveSyntheticTurn(session, prompt, key, string(rawBytes)), nil
	}
	turn, err := h.mailFlowChecks.RelayChatTurnWithID(ctx, session, prompt, key)
	if err != nil {
		return turn, err
	}
	isReview := strings.Contains(prompt, `"kind":"LOCAL_REVIEW"`) && !strings.Contains(prompt, `"kind":"BUILDER_RESULT"`)
	if !isReview {
		return turn, nil
	}
	h.mu.Lock()
	snapshot := h.snapshots[session]
	if !h.failed {
		h.failed = true
		for i := range snapshot.Turns {
			if snapshot.Turns[i].ID == turn {
				snapshot.Turns[i].State = domain.TurnStateFailed
				snapshot.Turns[i].Failure = &domain.ConversationFailure{Category: domain.AgentFailureProviderUnavailable, Retryable: true, ErrorSummary: "explicit original Reviewer exited"}
			}
		}
		messages := snapshot.Messages[:0]
		for _, m := range snapshot.Messages {
			if m.TurnID != turn || m.Role != domain.MessageRoleAssistant {
				messages = append(messages, m)
			}
		}
		snapshot.Messages = messages
		h.snapshots[session] = snapshot
		h.mu.Unlock()
		record, found, readErr := h.store.GetSession(ctx, session)
		if readErr != nil || !found {
			return turn, errors.New("fake failed Reviewer missing")
		}
		record.Activity.State = domain.ActivityExited
		return turn, h.store.UpdateSession(ctx, record)
	}
	h.mu.Unlock()
	isReport := strings.Contains(prompt, "Trusted requested-check results for the SAME candidate")
	h.sessions = append(h.sessions, session)
	if isReport {
		h.reports = append(h.reports, prompt)
	}
	response := `{"schemaVersion":1,"kind":"LOCAL_REVIEW","verdict":"PASS","reasonCode":"REVIEW_PASSED","summary":"Independent fake Reviewer accepted this exact candidate","findings":[]}`
	if h.checksEnabled && !isReport && !strings.Contains(prompt, parseCorrectionPromptPrefix) {
		response = h.raw
	} else {
		h.replacementTurns++
		if h.repair && h.replacementTurns <= h.repairLimit {
			response = `{"schemaVersion":1,"kind":"LOCAL_REVIEW","verdict":"REWORK","reasonCode":"REVIEW_CHANGES_REQUIRED","summary":"Fix the input edge case","findings":[{"severity":"BLOCKING","path":"backend/src/emails.ts","message":"Input edge case needs correction"}]}`
		}
	}
	if h.whitespaceSummary {
		var message map[string]any
		if json.Unmarshal([]byte(response), &message) == nil {
			message["summary"] = " \n" + message["summary"].(string) + "\t "
			raw, _ := json.Marshal(message)
			response = string(raw)
		}
	}
	h.mu.Lock()
	snapshot = h.snapshots[session]
	for i := range snapshot.Messages {
		if snapshot.Messages[i].TurnID == turn && snapshot.Messages[i].Role == domain.MessageRoleAssistant {
			snapshot.Messages[i].Text = response
		}
	}
	h.snapshots[session] = snapshot
	h.mu.Unlock()
	return turn, nil
}

func (h *replacementMailHarness) RunCandidateCheck(ctx context.Context, r ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	result, err := h.requestingReviewer.RunCandidateCheck(ctx, r)
	if !strings.Contains(r.RunID, ":requested-check:") && h.developmentFailures > 0 {
		h.developmentFailures--
		result.Outcome = ports.ClearDevCheckFail
		result.ExitCode = 1
		result.OutputSummary = "explicit development failure"
		result.OutputSHA256 = coreDigest([]byte(result.OutputSummary))
	}
	return result, err
}

func (h *replacementMailHarness) saveSyntheticTurn(session domain.SessionID, prompt, key, response string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	turn := fmt.Sprintf("replacement-recovery-turn-%d", len(h.relays)+1)
	now := time.Now().UTC()
	snapshot := h.snapshots[session]
	snapshot.SessionID = session
	snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{ID: turn, HandledBySessionID: session, State: domain.TurnStateCompleted, CompletedAt: &now})
	snapshot.Messages = append(snapshot.Messages, domain.ConversationMessage{ID: turn + ":user", TurnID: turn, Sequence: int64(len(snapshot.Messages) + 1), Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: prompt, ClientMessageID: key}, domain.ConversationMessage{ID: turn + ":reply", TurnID: turn, Sequence: int64(len(snapshot.Messages) + 2), Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Text: response})
	h.snapshots[session] = snapshot
	h.turnByClientMessageID[key] = turn
	h.relays = append(h.relays, standardRelay{sessionID: session, clientMessageID: key, prompt: prompt, response: response})
	return turn
}

func driveReplacementMail(t *testing.T, f *autoExecutionFixture) core.ComplexExecutionSnapshot {
	t.Helper()
	for n := 0; n < 8; n++ {
		if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
			t.Fatal(err)
		}
		e := boundedExecution(t, f)
		phase, _ := core.DeriveComplexExecutionPhase(e)
		if phase == core.ComplexExecutionCompleted {
			return e
		}
	}
	e := boundedExecution(t, f)
	phase, reason := core.DeriveComplexExecutionPhase(e)
	for _, fixed := range e.FixedRecoveries {
		t.Logf("fixed claim=%+v result=%+v verdict=%+v", fixed.Claim, fixed.Result, fixed.ReviewResult)
	}
	t.Fatalf("replacement flow stopped: phase=%s reason=%s dispatches=%d exception=%+v", phase, reason, len(e.Dispatches), e.Exception)
	return e
}

func TestMailReplacementCombinationsKeepExactOutcomeAndSession(t *testing.T) {
	for _, mode := range []string{"pass", "checks-pass", "rework", "checks-rework"} {
		t.Run(mode, func(t *testing.T) {
			checks, repair := strings.Contains(mode, "checks"), strings.Contains(mode, "rework")
			f, h := newReplacementMailFixture(t, checks, repair)
			f.confirm(t)
			e := driveReplacementMail(t, f)
			if len(e.FixedRecoveries) != 1 || e.FixedRecoveries[0].ReviewResult == nil || h.replacementSession == "" {
				t.Fatal("missing exact authorized replacement result")
			}
			fixed := e.FixedRecoveries[0]
			if e.Reviews[0].Verdict != "" || e.Reviews[0].ReviewerRoleBindingID != fixed.Request.RoleBindingID {
				t.Fatal("original failed review was overwritten")
			}
			want := 1
			if repair {
				want = 2
			}
			if len(e.Dispatches) != want || len(e.Reviews) != want || f.counts().spawns != 6 {
				t.Fatalf("counts dispatch=%d review=%d spawn=%d", len(e.Dispatches), len(e.Reviews), f.counts().spawns)
			}
			for _, id := range h.sessions {
				if id != h.replacementSession {
					t.Fatal("continuation lost replacement session")
				}
			}
			if checks && (len(h.reports) != want || len(h.checks) != want*2) {
				t.Fatal("requested checks or followups missing")
			}
			if repair && e.Integration.CandidateCommitSHA == fixed.Request.CandidateSHA {
				t.Fatal("old replacement verdict completed new SHA")
			}
			if repair {
				found := false
				for _, relay := range h.relays {
					if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) && strings.Contains(relay.prompt, "Input edge case needs correction") {
						found = true
					}
				}
				if !found {
					t.Fatal("Builder did not receive actual replacement findings")
				}
			}
			for key, n := range h.relayCalls {
				if n != 1 {
					t.Fatalf("duplicate actual relay call %s: %d", key, n)
				}
			}
			counts := f.counts()
			f.reopen(t)
			f.service.chat, f.service.checks, f.service.inspector, f.service.sessions = h, h, h, h
			again := driveReplacementMail(t, f)
			if !reflect.DeepEqual(e.Reviews, again.Reviews) || f.counts() != counts {
				t.Fatal("restart changed old reviews or repeated effects")
			}
		})
	}
}
