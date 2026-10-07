package cleardev

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func extraAttemptOffer(t *testing.T, f *autoExecutionFixture) (core.HumanDecisionOffer, core.HumanDecisionResult) {
	t.Helper()
	pending := pendingHumanRequestByKind(t, f.store, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt)
	now := f.clock()
	offer, err := f.store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{RequestID: pending.ID, DesktopRunID: "explicit-extra-authority-test", Nonce: mustComplexNonce(t), IssuedAt: now, ExpiresAt: now.Add(core.HumanDecisionOfferTTL)})
	if err != nil {
		t.Fatal(err)
	}
	result := core.HumanDecisionResult{ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind, BindingSchemaVersion: offer.BindingSchemaVersion, Binding: offer.Binding, ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove}
	return offer, result
}

func TestBoundedMailExtraAuthorityRejectsWrongAndExpiredBindings(t *testing.T) {
	for _, mode := range []string{"nonce", "desktop", "sha", "round", "plan", "task", "request", "expired", "cancelled", "direction"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newBoundedMailFixture(t, 3)
			f.confirm(t)
			before := boundedExecution(t, f)
			offer, result := extraAttemptOffer(t, f)
			var b core.ExtraMailAttemptBinding
			if err := json.Unmarshal(result.Binding, &b); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "nonce":
				result.Nonce = mustComplexNonce(t)
			case "desktop":
				result.DesktopRunID = "another-desktop"
			case "sha":
				b.CandidateSHA = forty("e")
			case "round":
				b.NextRound++
			case "plan":
				b.PlanSHA256 = strings.Repeat("e", 64)
			case "task":
				b.TaskID = "another-task"
			case "request":
				result.RequestID = "another-request"
			case "cancelled":
				if err := f.service.CancelRequirement(context.Background(), f.view.Requirement.ID, "explicit cancellation test"); err != nil {
					t.Fatal(err)
				}
			case "direction":
				f.service.runBackground = func(func()) {}
				if _, err := f.service.ProposeDirectionIntent(context.Background(), f.view.Requirement.ID, ProposeDirectionIntentInput{RequestID: "extra-stale-direction", DevelopmentRequirementID: f.view.Requirement.ID, Message: directionMessage}); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			result.Binding, err = json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			counts := f.counts()
			if mode == "expired" {
				err = f.store.SettleClearDevHumanDecision(context.Background(), result, offerExpiry(t, offer).Add(time.Second))
			} else {
				err = f.service.ApplyHumanDecisionResult(context.Background(), result)
			}
			if err == nil {
				t.Fatal("invalid extra authorization accepted")
			}
			after := boundedExecution(t, f)
			if !reflect.DeepEqual(before.Dispatches, after.Dispatches) || f.counts() != counts {
				t.Fatal("invalid authority changed attempts or sent messages")
			}
		})
	}
}
func offerExpiry(t *testing.T, o core.HumanDecisionOffer) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, o.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestBoundedMailExtraRejectLaterAndSingleUse(t *testing.T) {
	for _, choice := range []core.HumanDecisionChoice{core.HumanDecisionReject, core.HumanDecisionLater, core.HumanDecisionApprove} {
		t.Run(string(choice), func(t *testing.T) {
			f, _ := newBoundedMailFixture(t, 4)
			f.confirm(t)
			_, result := extraAttemptOffer(t, f)
			result.Decision = choice
			if err := f.service.ApplyHumanDecisionResult(context.Background(), result); err != nil {
				t.Fatal(err)
			}
			counts := f.counts()
			slots, err := f.store.ListClearDevMailAttempts(context.Background(), boundedExecution(t, f).Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := 3
			if choice == core.HumanDecisionApprove {
				want = 4
			}
			if len(slots) != want {
				t.Fatalf("slots=%+v", slots)
			}
			if err := f.service.ApplyHumanDecisionResult(context.Background(), result); err == nil {
				t.Fatal("consumed nonce was reusable")
			}
			if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			if counts != f.counts() {
				t.Fatal("rejected/pending/exhausted attempt was retried")
			}
			assertMailNotCompleted(t, f)
			request, found, err := f.store.GetClearDevHumanDecisionRequest(context.Background(), result.RequestID)
			if err != nil || !found {
				t.Fatal(err)
			}
			if choice == core.HumanDecisionLater && request.Status != core.HumanDecisionRequestPending {
				t.Fatal("Later silently resolved the decision")
			}
		})
	}
}

func TestBoundedMailConcurrentDesktopGrantAndRestartDispatchOnce(t *testing.T) {
	f, h := newBoundedMailFixture(t, 3)
	f.confirm(t)
	_, result := extraAttemptOffer(t, f)
	f.service.runBackground = func(func()) {}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = f.service.ApplyHumanDecisionResult(context.Background(), result)
		}(i)
	}
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("approval successes=%d", successes)
	}
	f.reopen(t)
	f.service.checks = h
	f.service.inspector = h
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := boundedExecution(t, f)
	phase, reason := core.DeriveComplexExecutionPhase(e)
	if phase != core.ComplexExecutionCompleted || len(e.Dispatches) != 4 || f.counts().builderSends != 4 {
		t.Fatalf("phase=%s reason=%s dispatches=%d sends=%d", phase, reason, len(e.Dispatches), f.counts().builderSends)
	}
	counts := f.counts()
	if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if counts != f.counts() {
		t.Fatal("replayed grant caused another dispatch")
	}
}

type fullAttemptChecks struct {
	*requestingReviewer
	failures int
}

func (h *fullAttemptChecks) RunCandidateCheck(ctx context.Context, r ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	result, err := h.requestingReviewer.RunCandidateCheck(ctx, r)
	if !strings.Contains(r.RunID, ":requested-check:") && h.failures > 0 {
		h.failures--
		result.Outcome = ports.ClearDevCheckFail
		result.ExitCode = 1
		result.OutputSummary = "explicit normal implementation test failure"
		result.OutputSHA256 = coreDigest([]byte(result.OutputSummary))
	}
	return result, err
}
func TestBoundedMailFullFiveAttemptsRetainAllChecksAndOneReviewer(t *testing.T) {
	f, h := newRequestingReviewer(t)
	f.service.boundedMailAttempts = true
	h.reviewVerdicts = []string{"PASS", "REWORK", "PASS", "REWORK", "PASS", "PASS"}
	f.service.checks = &fullAttemptChecks{requestingReviewer: h, failures: 2}
	f.confirm(t)
	before := boundedExecution(t, f)
	assertMailNotCompleted(t, f)
	if len(before.Dispatches) != 4 || len(before.Reviews) != 2 {
		t.Fatalf("dispatches=%+v reviews=%+v", before.Dispatches, before.Reviews)
	}
	applyFakeDesktopDecision(t, f.store, f.service, f.clock, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt, core.HumanDecisionApprove)
	e := boundedExecution(t, f)
	phase, reason := core.DeriveComplexExecutionPhase(e)
	if phase != core.ComplexExecutionCompleted || len(e.Dispatches) != 5 || len(e.Reviews) != 3 || f.counts().builderSends != 5 || f.counts().spawns != 4 {
		t.Fatalf("phase=%s reason=%s dispatch=%+v review=%+v", phase, reason, e.Dispatches, e.Reviews)
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), e.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, slot := range slots {
		kinds = append(kinds, slot.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{core.MailAttemptDevelopment, core.MailAttemptDevelopment, core.MailAttemptDevelopment, core.MailAttemptReviewRepair, core.MailAttemptHumanExtra}) {
		t.Fatalf("kinds=%v", kinds)
	}
	for _, id := range h.sessions {
		if id != h.sessions[0] {
			t.Fatal("Reviewer context replaced")
		}
	}
	if len(h.reports) != 3 || len(h.checks) != 6 {
		t.Fatal("additional checks or result returns were skipped")
	}
	if e.Integration.CandidateCommitSHA != e.Reviews[2].CandidateCommitSHA || e.Reviews[2].Verdict != core.LocalReviewPass {
		t.Fatal("final SHA not independently reviewed")
	}
	ctx := context.Background()
	packet, err := core.ParseComplexStandardExecutionPackage([]byte(e.Tasks[0].ExecutionPackageJSON))
	if err != nil || packet.MaxReworkCount != 1 {
		t.Fatalf("mail execution packet rework limit=%d err=%v", packet.MaxReworkCount, err)
	}
	builderBudgetID := ""
	for _, budget := range e.Exception.Budgets {
		switch budget.RoleKind {
		case core.ComplexExceptionBudgetBuilder:
			if budget.MaxTurns != 5 || budget.MaxReworkCount != 1 {
				t.Fatalf("mail builder budget=%+v", budget)
			}
			builderBudgetID = budget.ID
		case core.ComplexExceptionBudgetReviewer:
			if budget.MaxTurns != 6 {
				t.Fatalf("mail reviewer budget=%+v", budget)
			}
		}
	}
	if builderBudgetID == "" {
		t.Fatal("mail builder budget is missing")
	}
	for i, slot := range slots {
		if slot.Round != i {
			t.Fatalf("mail slot %d has round %d", i, slot.Round)
		}
	}
	// Exercise the generic budget's most permissive stored state through its
	// real API. These are storage-test grants, not native human approvals.
	for grant := 0; grant < 2; grant++ {
		if err := f.store.AuthorizeExtraBuilderTurn(ctx, builderBudgetID); err != nil {
			t.Fatal(err)
		}
	}
	for _, forbidden := range []struct {
		id    string
		round int
		want  string
	}{
		{"mail-round-five", 5, "mail attempt has no exact extra authorization"},
		{"mail-round-six", 6, "mail attempt round does not match immutable task counters"},
	} {
		stepID := forbidden.id + ":step"
		command := core.CreateComplexExecutionDispatchCommand{
			Dispatch: core.ComplexExecutionDispatch{
				ID: forbidden.id, ExecutionRunID: e.Run.ID, ComplexExecutionTaskID: e.Tasks[0].ID,
				DevelopmentTaskID: e.Tasks[0].DevelopmentTaskID, Round: forbidden.round,
				BaseCommitSHA: e.Dispatches[0].BaseCommitSHA, AgentStepID: stepID,
				Status: core.ComplexExecutionDispatchPending, CreatedAt: f.clock(),
			},
			AgentStep: core.AgentStep{
				ID: stepID, RoleBindingID: e.Run.BuilderRoleBindingID, Kind: core.ComplexExecutionAgentStepBuilderTask,
				RequestID: forbidden.id, ClientMessageID: stepID, PromptSHA256: coreDigest([]byte("forbidden mail round")),
				SendStatus: core.AgentStepSendStatusPending, RequestedAt: f.clock(),
			},
		}
		if _, _, err := f.store.CreateClearDevComplexExecutionDispatch(ctx, command); err == nil || !strings.Contains(err.Error(), forbidden.want) {
			t.Fatalf("round %d was not rejected by the mail boundary: %v", forbidden.round, err)
		}
	}
	after := boundedExecution(t, f)
	if !reflect.DeepEqual(e.Tasks, after.Tasks) || !reflect.DeepEqual(e.Dispatches, after.Dispatches) ||
		!reflect.DeepEqual(e.AgentSteps, after.AgentSteps) || !reflect.DeepEqual(e.Reviews, after.Reviews) {
		t.Fatal("forbidden mail rounds changed task history or left partial steps")
	}
	afterSlots, err := f.store.ListClearDevMailAttempts(ctx, e.Run.ID)
	if err != nil || !reflect.DeepEqual(slots, afterSlots) {
		t.Fatalf("forbidden rounds changed immutable mail slots: slots=%+v err=%v", afterSlots, err)
	}
	for _, budget := range after.Exception.Budgets {
		if budget.ID == builderBudgetID && (budget.AuthorizedExtraTurns != 2 || budget.MaxTurns != 5 || budget.MaxReworkCount != 1) {
			t.Fatalf("mail boundary test did not retain the two real storage grants: %+v", budget)
		}
	}
}
