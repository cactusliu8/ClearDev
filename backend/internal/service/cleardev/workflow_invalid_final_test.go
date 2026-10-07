package cleardev

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type malformedFinalReviewer struct {
	*projectExecutionFlowHarness
	session domain.SessionID
	invalid bool
}

func (h *malformedFinalReviewer) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, key string) (string, error) {
	if strings.HasPrefix(prompt, "你是独立的 ClearDev Requirement-level Final Reviewer") && h.invalid {
		h.session = session
	}
	if !h.invalid || session != h.session {
		return h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, session, prompt, key)
	}
	h.replies = append(h.replies, "Explanation before JSON: not a valid result")
	turn, err := h.productTestAgent.RelayChatTurnWithID(ctx, session, prompt, key)
	if err != nil {
		return turn, err
	}
	record, _, err := h.store.GetSession(ctx, session)
	if err != nil {
		return turn, err
	}
	record.Metadata.ProviderConversationID = "final-review-native"
	record.Activity.State = domain.ActivityIdle
	if err = h.store.UpdateSession(ctx, record); err != nil {
		return turn, err
	}
	conv, err := h.store.CreateConversation(ctx, "final-conversation-"+string(session), domain.ConversationScopeSession, record.ProjectID, session, time.Now().UTC())
	if err != nil {
		return turn, err
	}
	if err = h.store.AdoptProviderTurn(ctx, conv.ID, session, "generation", turn, "native-"+turn, time.Now().UTC()); err != nil {
		return turn, err
	}
	return turn, h.store.SettleTurnByID(ctx, turn, domain.TurnStateCompleted, "", time.Now().UTC())
}
func invalidFinalFixture(t *testing.T) (*projectPlanningFixture, *malformedFinalReviewer, string, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &malformedFinalReviewer{projectExecutionFlowHarness: attachProjectFlow(f, preparer), invalid: true}
	f.s.chat = h
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedInvalidBuilderWorkflow(t, f, child.Requirement.ID)
	if before.FinalReview == nil || before.FinalReview.ReasonCode != "REQUIREMENT_FINAL_REVIEW_RESULT_INVALID" {
		t.Fatal("not malformed final", before.FinalReview)
	}
	return f, h, child.Requirement.ID, before
}
func TestWorkflowInvalidFinalRetryPreservesCandidateAndFailure(t *testing.T) {
	f, h, id, before := invalidFinalFixture(t)
	ctx := context.Background()
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.Options) != 1 || view.Options[0].Action != core.RecoveryRetryStage || view.Options[0].UnavailableReason != "" {
		t.Fatalf("missing format recovery: %+v %v", view.Options, err)
	}
	input := WorkflowRecoveryInput{RequestID: "retry-invalid-final", ExecutionRunID: before.Run.ID, Action: core.RecoveryRetryStage, TargetID: before.FinalReview.ID, Supplement: "Repeat the original acceptance and return only JSON; keep the trial ready."}
	if _, err = f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	h.invalid = false
	after := driveWorkflowRecovery(t, f, id)
	if after.Run.CompletedAt == nil || after.FinalReview.CandidateCommitSHA != before.FinalReview.CandidateCommitSHA || after.FinalReview.AOSessionID == before.FinalReview.AOSessionID || len(after.WorkflowRecoveries) != 1 {
		t.Fatal("retry did not produce new bound review", after.FinalReview)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var status, reason, candidate string
	if err = db.QueryRow("SELECT status,reason_code,candidate_commit_sha FROM cleardev_requirement_final_reviews WHERE id=?", before.FinalReview.ID).Scan(&status, &reason, &candidate); err != nil || status != "FAILED" || reason != "REQUIREMENT_FINAL_REVIEW_RESULT_INVALID" || candidate != before.FinalReview.CandidateCommitSHA {
		t.Fatal("old review changed", err)
	}

}
func TestWorkflowInvalidFinalRejectsBusyAndChangedNative(t *testing.T) {
	f, _, id, before := invalidFinalFixture(t)
	for _, name := range []string{"busy", "native", "unsettled"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			session := domain.SessionID(before.FinalReview.AOSessionID)
			r, _, err := f.store.GetSession(ctx, session)
			if err != nil {
				t.Fatal(err)
			}
			original := r
			t.Cleanup(func() {
				if err := f.store.UpdateSession(ctx, original); err != nil {
					t.Error(err)
				}
			})
			if name == "busy" {
				r.Activity.State = domain.ActivityActive
			}
			if name == "native" {
				r.Metadata.ProviderConversationID = "different"
			}
			if err = f.store.UpdateSession(ctx, r); err != nil {
				t.Fatal(err)
			}
			if name == "unsettled" {
				if err = f.store.AdoptProviderTurn(ctx, "final-conversation-"+string(session), session, "generation", "new-running-turn", "new-native-turn", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			view, err := f.s.GetWorkflowRecovery(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range view.Options {
				if o.Action == core.RecoveryRetryStage && o.UnavailableReason == "" {
					t.Fatal("unsafe option", name)
				}
			}
			if _, err = f.s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: "unsafe", ExecutionRunID: before.Run.ID, Action: core.RecoveryRetryStage, TargetID: before.FinalReview.ID}); err == nil {
				t.Fatal("unsafe recovery")
			}
		})
	}
}

func TestWorkflowInvalidFinalRechecksEvidenceBeforeSending(t *testing.T) {
	f, h, id, before := invalidFinalFixture(t)
	ctx := context.Background()
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: "late-change", ExecutionRunID: before.Run.ID, Action: core.RecoveryRetryStage, TargetID: before.FinalReview.ID, Supplement: "Repeat original trial."}); err != nil {
		t.Fatal(err)
	}
	pending, _, err := f.store.GetClearDevComplexExecution(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := f.store.GetSession(ctx, domain.SessionID(before.FinalReview.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	record.Metadata.ProviderConversationID = "changed-after-acceptance"
	if err = f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	calls := len(h.relays)
	if err = f.s.validateFinalReviewInputRecovery(ctx, *pending.FinalReview); err == nil {
		t.Fatal("changed original proof accepted")
	}
	if len(h.relays) != calls {
		t.Fatal("validation sent a message")
	}
}
