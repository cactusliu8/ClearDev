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
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

const (
	extraPlanningRequestAction  = "REQUEST_EXTRA_PLANNING_ATTEMPT"
	extraPlanningContinueAction = "CONTINUE_EXTRA_PLANNING_ATTEMPT"
	extraPlanningDecisionKind   = "AUTHORIZE_EXTRA_PLANNING_ATTEMPT"
	extraPlanningTestDesktop    = "extra-test-desktop"
)

// These tests use an explicit synthetic desktop decision and provider/Git
// doubles. Workflow, native message identities and all ledgers use real SQLite.
// A synthetic APPROVE here is not a real human approval or a product demo.
func exhaustedExtraPlanningFixture(t *testing.T, compilation bool) planningContinuationFixture {
	t.Helper()
	ctx := context.Background()
	f := newPlanningContinuationFixture(t, compilation)
	f.s.desktopRunID = extraPlanningTestDesktop
	if compilation {
		f.h.replies = append(f.h.replies, "second compilation is still invalid")
	} else {
		// The failed provider turn has a confirmed send, no completed result,
		// and preserves the original pending product discussion.
		f.h.replies = append(f.h.replies, f.reply)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, f.input()); err != nil {
		t.Fatal("exhaust original second attempt", err)
	}
	attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
	items := logicalStepAttemptViews(attempts, f.step.ID)
	if err != nil || len(items) != 2 || items[1].SendStatus != core.AgentAttemptFailed {
		t.Fatalf("fixture did not fail both original attempts: %+v %v", items, err)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range view.Options {
		if option.Action == core.RecoveryRetryPlanningStep && option.UnavailableReason == string(core.ReasonMessageBudgetExhausted) {
			return f
		}
	}
	t.Fatalf("old two-attempt recovery did not remain exhausted: %+v", view.Options)
	return f
}

func extraPlanningOption(t *testing.T, f planningContinuationFixture, action string) core.WorkflowRecoveryOption {
	t.Helper()
	view, err := f.s.GetWorkflowRecovery(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range view.Options {
		if option.Action == action {
			return option
		}
	}
	t.Fatalf("missing %s option on the original exhausted step: %+v", action, view.Options)
	return core.WorkflowRecoveryOption{}
}

func extraPlanningInput(requestID, action, target string) WorkflowRecoveryInput {
	return WorkflowRecoveryInput{RequestID: requestID, Action: action, TargetID: target}
}

func requestExtraPlanningAttempt(t *testing.T, f planningContinuationFixture) WorkflowRecoveryInput {
	t.Helper()
	option := extraPlanningOption(t, f, extraPlanningRequestAction)
	if option.UnavailableReason != "" || !strings.HasPrefix(option.TargetID, f.step.ID+":extra:") {
		t.Fatalf("extra request is not eligible and bound to the original step: %+v", option)
	}
	state, err := f.store.ReadClearDevExtraPlanningAttempt(context.Background(), f.id, f.s.now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.ParseExtraPlanningAttemptBinding(encoded); err != nil {
		t.Fatalf("eligible native binding does not satisfy its production parser: %+v %v", state.Binding, err)
	}
	input := extraPlanningInput("request-one-extra", extraPlanningRequestAction, option.TargetID)
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), f.id, input); err != nil {
		t.Fatal("request one extra attempt", err)
	}
	return input
}

func extraPlanningNativeOffer(t *testing.T, f planningContinuationFixture) (core.HumanDecisionOffer, core.HumanDecisionResult) {
	t.Helper()
	// Exercise the actual service dispatch selector, including first-display
	// validation. Calling the store directly would conceal an unreachable kind.
	offer, found, err := f.s.IssueHumanDecisionOffer(context.Background(), extraPlanningTestDesktop)
	if err != nil || !found || offer.DecisionKind != extraPlanningDecisionKind {
		t.Fatalf("new native decision is unreachable through the service: found=%t offer=%+v err=%v", found, offer, err)
	}
	result := core.HumanDecisionResult{
		ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
		DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
		BindingSchemaVersion: offer.BindingSchemaVersion, Binding: offer.Binding,
		ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove,
	}
	return offer, result
}

func approveExtraPlanningAttempt(t *testing.T, f planningContinuationFixture) core.HumanDecisionOffer {
	t.Helper()
	offer, result := extraPlanningNativeOffer(t, f)
	if err := f.s.ApplyHumanDecisionResult(context.Background(), result); err != nil {
		t.Fatal("explicit synthetic desktop approval", err)
	}
	return offer
}

func extraPlanningAttempts(t *testing.T, f planningContinuationFixture) []core.AgentStepAttemptView {
	t.Helper()
	attempts, err := f.store.ListClearDevAgentStepAttempts(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	return logicalStepAttemptViews(attempts, f.step.ID)
}

func extraPlanningBudget(t *testing.T, f planningContinuationFixture) core.MessageBudgetUsage {
	t.Helper()
	view, err := f.store.GetClearDevMessageBudget(context.Background(), f.id)
	if err != nil || view.BudgetVersion != core.MessageBudgetV1 {
		t.Fatalf("missing measured original budget: %+v %v", view, err)
	}
	for _, step := range view.Steps {
		if step.LogicalStepID == f.step.ID {
			return step
		}
	}
	t.Fatal("missing original logical-step budget")
	return core.MessageBudgetUsage{}
}

func extraPlanningHistory(t *testing.T, f planningContinuationFixture, action string) []core.WorkflowRecovery {
	t.Helper()
	view, err := f.s.GetWorkflowRecovery(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	items := []core.WorkflowRecovery{}
	for _, item := range view.History {
		if item.Action == action {
			items = append(items, item)
		}
	}
	return items
}

func TestExtraPlanningAttemptRequiresNativeApprovalAndExplicitContinue(t *testing.T) {
	for _, compilation := range []bool{false, true} {
		name := "discussion-provider-failures"
		if compilation {
			name = "compilation-invalid-results"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := exhaustedExtraPlanningFixture(t, compilation)
			oldAttempts := extraPlanningAttempts(t, f)
			oldBudget := extraPlanningBudget(t, f)
			oldHistory := extraPlanningHistory(t, f, core.RecoveryRetryPlanningStep)
			productBefore, _, err := f.store.GetClearDevProduct(ctx, f.product)
			if err != nil {
				t.Fatal(err)
			}
			calls := len(f.h.relays)
			input := requestExtraPlanningAttempt(t, f)
			pending := extraPlanningOption(t, f, extraPlanningContinueAction)
			if pending.TargetID != input.TargetID || pending.UnavailableReason != "EXTRA_ATTEMPT_DECISION_PENDING" {
				t.Fatalf("request did not wait for the exact native decision: %+v", pending)
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, extraPlanningInput("too-early", extraPlanningContinueAction, input.TargetID)); err == nil {
				t.Fatal("ordinary HTTP intent authorized its own attempt")
			}
			if len(f.h.relays) != calls || !reflect.DeepEqual(oldAttempts, extraPlanningAttempts(t, f)) || !reflect.DeepEqual(oldBudget, extraPlanningBudget(t, f)) {
				t.Fatal("request or pending continuation changed attempts, messages or budget")
			}
			offer := approveExtraPlanningAttempt(t, f)
			if offer.Display.Summary != "Two original attempts failed. Authorize one additional original message, then continue the same request separately.\n\n原来的两次尝试均已明确失败。可批准追加一次原消息，再单独继续原事项。" ||
				!strings.Contains(offer.Display.FullContent, "批准只授予权限，不会自动继续。") || !strings.Contains(offer.Display.FullContent, "本决定不批准规格、执行或完成结果。") ||
				!strings.Contains(offer.Display.ChangeSummary, "不追加纠正消息，不产生第四次尝试。") || !strings.Contains(offer.Display.FullContent, "Product / 产品: "+productBefore.Goal.Name) {
				t.Fatalf("native display does not explain the same authority in both languages: %+v", offer.Display)
			}
			if compilation {
				stage := productBefore.Stages[0].Definition
				if !strings.Contains(offer.Display.FullContent, "Stage to compile / 待编译阶段: "+stage.Title) || !strings.Contains(offer.Display.FullContent, "Original stage objective / 原阶段目标:\n"+stage.Goal) {
					t.Fatal("bilingual native display changed or omitted the original stage text")
				}
			} else if !strings.Contains(offer.Display.FullContent, "Your original discussion request / 原讨论事项:\n"+productBefore.Discussions[0].UserMessage) {
				t.Fatal("bilingual native display changed or omitted the original user request")
			}
			approved := extraPlanningBudget(t, f)
			if len(f.h.relays) != calls || !reflect.DeepEqual(oldAttempts, extraPlanningAttempts(t, f)) {
				t.Fatal("native approval sent a message or created attempt 3")
			}
			if approved.MaxMessages == nil || *approved.MaxMessages != 4 || !reflect.DeepEqual(oldBudget.ReservedMessages, approved.ReservedMessages) || !reflect.DeepEqual(oldBudget.ConfirmedSentMessages, approved.ConfirmedSentMessages) {
				t.Fatalf("approval did not append one message permission while retaining old counts: old=%+v approved=%+v", oldBudget, approved)
			}
			requestHistory := extraPlanningHistory(t, f, extraPlanningRequestAction)
			if len(requestHistory) != 1 || requestHistory[0].ID != input.RequestID || requestHistory[0].SuccessorID != offer.RequestID || requestHistory[0].StepID != f.step.ID {
				t.Fatalf("request history does not identify the native request: %+v", requestHistory)
			}
			option := extraPlanningOption(t, f, extraPlanningContinueAction)
			if option.TargetID != input.TargetID || option.UnavailableReason != "" {
				t.Fatalf("approval did not enable the separately bound continuation: %+v", option)
			}
			f.h.fail = false
			f.h.replies = append(f.h.replies, f.reply)
			continuation := extraPlanningInput("continue-one-extra", extraPlanningContinueAction, input.TargetID)
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, continuation); err != nil {
				t.Fatal("continue explicitly approved original request", err)
			}
			if err := f.s.ResumeComplexFlows(ctx); err != nil {
				t.Fatal(err)
			}
			if len(f.h.relays) != calls+1 {
				t.Fatalf("third attempt sent %d messages, want exactly one", len(f.h.relays)-calls)
			}
			third := f.h.relays[calls]
			if third.clientMessageID != f.step.ClientMessageID+":attempt:3" || string(third.sessionID) != oldAttempts[1].AOSessionID {
				t.Fatalf("third request changed stable message or original session: %+v", third)
			}
			originalPrompt := ""
			for _, relay := range f.h.relays[:calls] {
				if relay.clientMessageID == f.step.ClientMessageID {
					originalPrompt = relay.prompt
				}
			}
			if originalPrompt == "" || third.prompt != originalPrompt {
				t.Fatal("extra attempt changed the original prompt")
			}
			after := extraPlanningAttempts(t, f)
			if len(after) != 3 || after[2].AttemptNumber != 3 || !reflect.DeepEqual(oldAttempts, after[:2]) {
				t.Fatalf("third attempt rewrote or reset original evidence: %+v", after)
			}
			budgetAfter := extraPlanningBudget(t, f)
			if *budgetAfter.ReservedMessages != *oldBudget.ReservedMessages+1 || *budgetAfter.ConfirmedSentMessages != *oldBudget.ConfirmedSentMessages+1 || *budgetAfter.MaxMessages != 4 {
				t.Fatalf("third send reset or overspent ledger: old=%+v after=%+v", oldBudget, budgetAfter)
			}
			continueHistory := extraPlanningHistory(t, f, extraPlanningContinueAction)
			if len(continueHistory) != 1 || continueHistory[0].SuccessorID != after[2].ID || continueHistory[0].ID != continuation.RequestID || continueHistory[0].TargetID != input.TargetID || !reflect.DeepEqual(oldHistory, extraPlanningHistory(t, f, core.RecoveryRetryPlanningStep)) {
				t.Fatalf("original or successor recovery history changed: %+v", continueHistory)
			}
			productAfter, _, err := f.store.GetClearDevProduct(ctx, f.product)
			if err != nil || len(productAfter.Discussions) != len(productBefore.Discussions) || productAfter.Discussions[0].ID != productBefore.Discussions[0].ID || productAfter.Discussions[0].UserMessage != productBefore.Discussions[0].UserMessage {
				t.Fatalf("extra attempt replaced the original product discussion: %v", err)
			}
			if _, found, err := f.store.GetClearDevComplexExecution(ctx, f.id); err != nil || found {
				t.Fatalf("planning authority started execution: found=%t err=%v", found, err)
			}
			if compilation {
				view, err := f.s.GetRequirement(ctx, f.id)
				if err != nil || len(view.ComplexPlanning.Compilations) != 1 || view.ComplexPlanning.Compilations[0].Outcome != "READY" || len(view.RequirementVersions) != 1 || view.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation || productAfter.Stages[0].DevelopmentRequirementID != f.id {
					t.Fatalf("third compilation did not return to its normal pending confirmation: %+v %v", view.ComplexPlanning, err)
				}
			} else if productAfter.Discussions[0].Result == nil || productAfter.Discussions[0].Result.Outcome != "READY" {
				t.Fatal("third discussion did not settle its original discussion")
			}
			for _, replay := range []WorkflowRecoveryInput{input, continuation} {
				if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, replay); err != nil || len(f.h.relays) != calls+1 {
					t.Fatalf("exact request replay failed or sent again: %v", err)
				}
				replay.Supplement = "changed after registration"
				if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, replay); err == nil || len(f.h.relays) != calls+1 {
					t.Fatal("request replay accepted different content or resent")
				}
			}
		})
	}
}

func TestExtraPlanningAttemptConcurrentPanelsClaimOneRequestAndSuccessor(t *testing.T) {
	ctx := context.Background()
	f := exhaustedExtraPlanningFixture(t, true)
	f.s.runBackground = func(func()) {}
	option := extraPlanningOption(t, f, extraPlanningRequestAction)
	race := func(action, prefix string) {
		t.Helper()
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for _, requestID := range []string{prefix + "-panel-a", prefix + "-panel-b"} {
			wg.Add(1)
			go func(requestID string) {
				defer wg.Done()
				_, err := f.s.RequestWorkflowRecovery(ctx, f.id, extraPlanningInput(requestID, action, option.TargetID))
				results <- err
			}(requestID)
		}
		wg.Wait()
		close(results)
		succeeded := 0
		for err := range results {
			if err == nil {
				succeeded++
			}
		}
		if succeeded != 1 {
			t.Fatalf("%s accepted %d different concurrent requests, want one", action, succeeded)
		}
	}
	calls := len(f.h.relays)
	race(extraPlanningRequestAction, "request")
	if len(extraPlanningHistory(t, f, extraPlanningRequestAction)) != 1 || len(extraPlanningAttempts(t, f)) != 2 || len(f.h.relays) != calls {
		t.Fatal("competing requests created duplicate authority or an unapproved attempt")
	}
	approveExtraPlanningAttempt(t, f)
	race(extraPlanningContinueAction, "continue")
	if len(extraPlanningHistory(t, f, extraPlanningContinueAction)) != 1 || len(extraPlanningAttempts(t, f)) != 3 || len(f.h.relays) != calls {
		t.Fatal("competing continuations created duplicate successors or sent before the explicit sender")
	}
	registered := extraPlanningOption(t, f, extraPlanningContinueAction)
	if registered.UnavailableReason != "CONTINUATION_REGISTERED" {
		t.Fatalf("unsent successor was not retained as one registered continuation: %+v", registered)
	}
	f.h.fail = false
	f.h.replies = append(f.h.replies, f.reply)
	if err := f.s.runComplexFlow(ctx, f.id); err != nil || len(f.h.relays) != calls+1 {
		t.Fatalf("one registered successor did not send exactly once: %v", err)
	}
}

func TestExtraPlanningAttemptThirdInvalidResultHasNoCorrectionOrFourthAttempt(t *testing.T) {
	for _, compilation := range []bool{false, true} {
		t.Run(map[bool]string{false: "discussion-no-previous-correction", true: "compilation-correction-already-used"}[compilation], func(t *testing.T) {
			ctx := context.Background()
			f := exhaustedExtraPlanningFixture(t, compilation)
			input := requestExtraPlanningAttempt(t, f)
			approveExtraPlanningAttempt(t, f)
			calls := len(f.h.relays)
			f.h.fail = false
			f.h.replies = append(f.h.replies, "third reply is invalid", "must never be used for a new correction")
			continuation := extraPlanningInput("third-invalid", extraPlanningContinueAction, input.TargetID)
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, continuation); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := f.s.ResumeComplexFlows(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, continuation); err != nil {
					t.Fatal(err)
				}
			}
			attempts := extraPlanningAttempts(t, f)
			if len(attempts) != 3 || attempts[2].SendStatus != core.AgentAttemptFailed || attempts[2].FailureCategory != domain.AgentFailureResultInvalid || len(f.h.relays) != calls+1 {
				t.Fatalf("third invalid result escaped its single-message terminal boundary: attempts=%+v messages=%d", attempts, len(f.h.relays)-calls)
			}
			if _, found, err := f.store.GetClearDevParseCorrection(ctx, f.step.ID); err != nil || found != compilation {
				t.Fatalf("third acquired a new mechanical correction: found=%t err=%v", found, err)
			}
			for _, action := range []string{extraPlanningRequestAction, extraPlanningContinueAction} {
				if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, extraPlanningInput("fourth-"+action, action, input.TargetID)); err == nil {
					t.Fatal("terminal extra permission admitted a fourth attempt")
				}
			}
			if len(extraPlanningAttempts(t, f)) != 3 || len(f.h.relays) != calls+1 {
				t.Fatal("terminal replay reset the attempt or message budget")
			}
		})
	}
}

func TestExtraPlanningAttemptPendingAndRejectedDoNotGrantPermission(t *testing.T) {
	for _, choice := range []core.HumanDecisionChoice{core.HumanDecisionLater, core.HumanDecisionReject} {
		t.Run(string(choice), func(t *testing.T) {
			ctx := context.Background()
			f := exhaustedExtraPlanningFixture(t, true)
			oldAttempts, oldBudget := extraPlanningAttempts(t, f), extraPlanningBudget(t, f)
			calls := len(f.h.relays)
			input := requestExtraPlanningAttempt(t, f)
			_, result := extraPlanningNativeOffer(t, f)
			result.Decision = choice
			if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, extraPlanningInput("without-approval", extraPlanningContinueAction, input.TargetID)); err == nil {
				t.Fatal("Later or Reject granted an extra attempt")
			}
			if len(f.h.relays) != calls || !reflect.DeepEqual(oldAttempts, extraPlanningAttempts(t, f)) || !reflect.DeepEqual(oldBudget, extraPlanningBudget(t, f)) {
				t.Fatal("Later or Reject changed the original attempt or budget facts")
			}
			option := extraPlanningOption(t, f, extraPlanningContinueAction)
			want := "EXTRA_ATTEMPT_DECISION_PENDING"
			if choice == core.HumanDecisionReject {
				want = "EXTRA_ATTEMPT_DECISION_REJECTED"
			}
			if option.UnavailableReason != want {
				t.Fatalf("decision state=%s, want %s", option.UnavailableReason, want)
			}
			if choice == core.HumanDecisionLater {
				history, err := f.store.ListClearDevHumanDecisionDispatchHistory(ctx, result.RequestID)
				if err != nil || len(history) != 1 {
					t.Fatalf("missing native Later dispatch: %+v %v", history, err)
				}
				if _, err := f.s.ReopenHumanDecision(ctx, f.id, ReopenHumanDecisionInput{RequestID: "reopen-extra-later", DecisionRequestID: result.RequestID, ContentSHA256: result.ContentSHA256, PreviousDispatchID: history[0].ID}); err != nil {
					t.Fatal("new decision kind cannot reopen after Later", err)
				}
				offer, approved := extraPlanningNativeOffer(t, f)
				if offer.RequestID != result.RequestID || offer.ContentSHA256 != result.ContentSHA256 || string(offer.Binding) != string(result.Binding) || offer.Nonce == result.Nonce {
					t.Fatal("reopening replaced the original authority binding or reused its nonce")
				}
				if err := f.s.ApplyHumanDecisionResult(ctx, approved); err != nil || len(f.h.relays) != calls || len(extraPlanningAttempts(t, f)) != 2 {
					t.Fatalf("reopened approval auto-continued the original step: %v", err)
				}
			}
		})
	}
}

func TestExtraPlanningAttemptRegisteredContinuationSurvivesDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	f := exhaustedExtraPlanningFixture(t, true)
	input := requestExtraPlanningAttempt(t, f)
	approveExtraPlanningAttempt(t, f)
	f.s.runBackground = func(func()) {}
	continuation := extraPlanningInput("persisted-extra-continuation", extraPlanningContinueAction, input.TargetID)
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, continuation); err != nil {
		t.Fatal(err)
	}
	beforeHistory := extraPlanningHistory(t, f, extraPlanningContinueAction)
	beforeBudget := extraPlanningBudget(t, f)
	beforeAttempts := extraPlanningAttempts(t, f)
	if len(beforeAttempts) != 3 || beforeAttempts[2].SendStatus != core.AgentAttemptPending || beforeAttempts[2].LastEventID != "" {
		t.Fatalf("registration did not preserve a wholly unsent successor: %+v", beforeAttempts)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.h.store = f.store
	f.s = planningContinuationService(f.store, f.h, f.s.newID, f.s.now)
	f.s.desktopRunID = extraPlanningTestDesktop
	f.h.fail = false
	f.h.replies = append(f.h.replies, f.reply)
	calls := len(f.h.relays)
	if !reflect.DeepEqual(beforeBudget, extraPlanningBudget(t, f)) || !reflect.DeepEqual(beforeAttempts, extraPlanningAttempts(t, f)) {
		t.Fatal("database reopen reset authorization or original attempt facts")
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, continuation); err != nil || len(f.h.relays) != calls+1 {
		t.Fatalf("registered third attempt could not continue after reopen: %v", err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, continuation); err != nil || len(f.h.relays) != calls+1 || !reflect.DeepEqual(beforeHistory, extraPlanningHistory(t, f, extraPlanningContinueAction)) {
		t.Fatalf("reopen replay changed immutable continuation or duplicated its message: %v", err)
	}
}

func TestExtraPlanningAttemptRequiresExplicitTrustedDesktop(t *testing.T) {
	f := exhaustedExtraPlanningFixture(t, true)
	option := extraPlanningOption(t, f, extraPlanningRequestAction)
	f.s.desktopRunID = ""
	calls := len(f.h.relays)
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), f.id, extraPlanningInput("bare-daemon-extra", extraPlanningRequestAction, option.TargetID)); err == nil {
		t.Fatal("bare daemon accepted a native extra-attempt request")
	}
	if len(extraPlanningHistory(t, f, extraPlanningRequestAction)) != 0 || len(extraPlanningAttempts(t, f)) != 2 || len(f.h.relays) != calls {
		t.Fatal("unavailable trusted desktop created a request or new attempt")
	}
}

func TestExtraPlanningAttemptGrantRejectsWrongNativeIdentity(t *testing.T) {
	for _, mode := range []string{"nonce", "desktop", "content", "binding", "expired"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := exhaustedExtraPlanningFixture(t, true)
			requestExtraPlanningAttempt(t, f)
			offer, result := extraPlanningNativeOffer(t, f)
			oldBudget := extraPlanningBudget(t, f)
			calls := len(f.h.relays)
			switch mode {
			case "nonce":
				result.Nonce = mustComplexNonce(t)
			case "desktop":
				result.DesktopRunID = "another-desktop"
			case "content":
				result.ContentSHA256 = strings.Repeat("f", 64)
			case "binding":
				result.Binding = append([]byte(nil), result.Binding...)
				result.Binding = []byte(strings.Replace(string(result.Binding), f.step.ID, "another-step", 1))
			case "expired":
				expires := offerExpiry(t, offer).Add(time.Second)
				f.s.now = func() time.Time { return expires }
			}
			if err := f.s.ApplyHumanDecisionResult(ctx, result); err == nil {
				t.Fatal("wrong native decision identity granted an extra attempt")
			}
			if !reflect.DeepEqual(oldBudget, extraPlanningBudget(t, f)) || len(extraPlanningAttempts(t, f)) != 2 || len(f.h.relays) != calls {
				t.Fatal("invalid native result changed message permission or created an attempt")
			}
		})
	}
}
