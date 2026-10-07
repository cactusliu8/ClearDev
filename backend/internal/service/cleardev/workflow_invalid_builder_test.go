package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// The model, Git and checker are explicit doubles. The failure, attempts,
// budget, recovery and completion transactions use the real SQLite store.
func invalidBuilderWorkflowFixture(t *testing.T) (*projectPlanningFixture, *projectExecutionFlowHarness, string, core.ComplexExecutionSnapshot) {
	t.Helper()
	return invalidBuilderWorkflowFixtureKind(t, false)
}

func invalidBuilderWorkflowFixtureKind(t *testing.T, secondAttempt bool) (*projectPlanningFixture, *projectExecutionFlowHarness, string, core.ComplexExecutionSnapshot) {
	t.Helper()
	mode := "invalid"
	if secondAttempt {
		mode = "correction-provider-failure"
	}
	return invalidBuilderWorkflowFixtureSetup(t, mode, nil)
}

func invalidBuilderWorkflowFixtureSetup(t *testing.T, mode string, setup func(*projectPlanningFixture)) (*projectPlanningFixture, *projectExecutionFlowHarness, string, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	// Real SQLite bookkeeping under parallel/race instrumentation can exceed
	// the one-second generic fixture window; production timeouts do not change.
	f.s.stepTimeout = 10 * time.Second
	h := attachProjectFlow(f, preparer)
	h.builderInvalidLeft = 2
	if mode == "correction-provider-failure" {
		h.builderInvalidLeft = 1
		f.s.chat = &workflowCorrectionProviderFailure{projectExecutionFlowHarness: h, fail: true}
	}
	if mode == "original-provider-failure" {
		h.builderTurnFailures = 1
	}
	if setup != nil {
		setup(f)
	}
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedInvalidBuilderWorkflow(t, f, child.Requirement.ID)
	if len(before.Dispatches) != 1 || before.Dispatches[0].ReasonCode != "BUILDER_RESULT_INVALID" || before.Dispatches[0].CandidateCommitID != "" {
		t.Fatalf("fixture did not stop at the invalid result: %+v", before.Dispatches)
	}
	b, found := complexExecutionBindingByID(before, before.Dispatches[0].BuilderRoleBindingID)
	if !found {
		t.Fatal("missing original Builder binding")
	}
	record, found, err := f.store.GetSession(context.Background(), domain.SessionID(b.AOSessionID))
	if err != nil || !found {
		t.Fatalf("missing original Builder session: %v", err)
	}
	// Explicit provider-identity fixture; no native provider is contacted.
	record.Metadata.ProviderConversationID = "invalid-builder-original-native-session"
	if err := f.store.UpdateSession(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return f, h, child.Requirement.ID, before
}

type workflowCorrectionProviderFailure struct {
	*projectExecutionFlowHarness
	fail bool
}

func (h *workflowCorrectionProviderFailure) RelayChatTurnWithID(ctx context.Context, session domain.SessionID, prompt, key string) (string, error) {
	turn, err := h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, session, prompt, key)
	if err != nil || !h.fail || !strings.HasPrefix(prompt, parseCorrectionPromptPrefix) {
		return turn, err
	}
	h.fail = false
	h.currentCandidate = ""
	h.builderInvalidLeft = 1
	snapshot := h.snapshots[session]
	for i := range snapshot.Turns {
		if snapshot.Turns[i].ID == turn {
			snapshot.Turns[i].State = domain.TurnStateFailed
			snapshot.Turns[i].Failure = &domain.ConversationFailure{Category: domain.AgentFailureProviderUnavailable, ErrorSummary: "Explicit failed-correction provider fixture", Retryable: true}
		}
	}
	h.snapshots[session] = snapshot
	return turn, nil
}

func stoppedInvalidBuilderWorkflow(t *testing.T, f *projectPlanningFixture, id string) core.ComplexExecutionSnapshot {
	t.Helper()
	for i := 0; i < 120; i++ {
		before, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		phase, _ := core.DeriveComplexExecutionPhase(before)
		if phase == core.ComplexExecutionBlocked || phase == core.ComplexExecutionNeedsHuman {
			return before
		}
		_, _, err = f.s.advanceComplexStandardExecution(context.Background(), id)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) {
			t.Fatal(err)
		}
	}
	t.Fatal("fixture did not reach a durable stop")
	return core.ComplexExecutionSnapshot{}
}

func invalidBuilderRecoveryInput(before core.ComplexExecutionSnapshot) WorkflowRecoveryInput {
	return WorkflowRecoveryInput{RequestID: "invalid-builder-continue", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: before.Dispatches[0].ID, Supplement: "Keep the existing work and finish the original task under its unchanged scope. Return the required result format."}
}

func TestWorkflowInvalidBuilderContinuesOriginalTaskAndPreservesFailure(t *testing.T) {
	ctx := context.Background()
	f, h, id, before := invalidBuilderWorkflowFixture(t)
	oldAttempts, err := f.store.ListClearDevAgentStepAttempts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	step, found := complexExecutionStepByID(before, before.Dispatches[0].AgentStepID)
	if !found {
		t.Fatal("missing failed logical step")
	}
	correction, found, err := f.store.GetClearDevParseCorrection(ctx, step.ID)
	if err != nil || !found {
		t.Fatalf("fixture must have already used its one correction: %v", err)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.Options) != 1 || view.Options[0].Action != core.RecoveryContinueBuilder || view.Options[0].UnavailableReason != "" {
		t.Fatalf("invalid Builder has no safe continuation: %+v %v", view, err)
	}
	if view.Options[0].Summary == "" {
		t.Fatal("failure does not explain the invalid reply")
	}
	calls := len(h.relays)
	if _, err := f.s.GetWorkflowRecovery(ctx, id); err != nil || len(h.relays) != calls {
		t.Fatal("reading recovery options sent a model message")
	}
	input := invalidBuilderRecoveryInput(before)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 || len(after.WorkflowRecoveries) != 1 || after.Dispatches[1].Round != 1 || after.Dispatches[1].BuilderRoleBindingID != before.Dispatches[0].BuilderRoleBindingID {
		t.Fatal("continuation did not complete through one new bound round")
	}
	if !reflect.DeepEqual(after.Dispatches[0], before.Dispatches[0]) {
		t.Fatal("old failed dispatch was rewritten")
	}
	oldStep, found := complexExecutionStepByID(after, step.ID)
	if !found || !reflect.DeepEqual(oldStep, step) {
		t.Fatal("old invalid step was forged into a settled result")
	}
	newAttempts, err := f.store.ListClearDevAgentStepAttempts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, prior := range oldAttempts {
		matched := false
		for _, current := range newAttempts {
			if prior.ID == current.ID {
				matched = reflect.DeepEqual(prior, current)
			}
		}
		if !matched {
			t.Fatal("original attempt/result/parse history changed")
		}
	}
	saved, found, err := f.store.GetClearDevParseCorrection(ctx, step.ID)
	if err != nil || !found || !reflect.DeepEqual(saved, correction) {
		t.Fatal("original correction was changed or reset")
	}
	for _, budget := range after.Exception.Budgets {
		if budget.RoleKind == core.ComplexExceptionBudgetBuilder && budget.UsedTurns != 2 {
			t.Fatalf("new Builder round did not consume its budget: %+v", budget)
		}
	}
	foundContext := false
	for _, relay := range h.relays[calls:] {
		if strings.Contains(relay.prompt, input.Supplement) {
			foundContext = true
		}
		if relay.clientMessageID == correction.ClientMessageID || relay.clientMessageID == step.ClientMessageID {
			t.Fatal("old failed logical step was resent")
		}
	}
	if !foundContext {
		t.Fatal("new Builder round did not receive the saved recovery context")
	}
}

func TestWorkflowInvalidBuilderSecondAttemptDoesNotResetSharedCorrection(t *testing.T) {
	ctx := context.Background()
	f, h, id, before := invalidBuilderWorkflowFixtureKind(t, true)
	states, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, before.Dispatches[0].AgentStepID)
	if err != nil || len(states) != 2 || states[1].LastClientMessageID != states[1].ClientMessageID || states[1].FailureCategory != domain.AgentFailureResultInvalid {
		t.Fatalf("fixture did not end on invalid second original result: %+v %v", states, err)
	}
	correction, found, err := f.store.GetClearDevParseCorrection(ctx, before.Dispatches[0].AgentStepID)
	if err != nil || !found || correction.AttemptNumber != 1 {
		t.Fatalf("shared correction was not bound to the first attempt: %+v %v", correction, err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, invalidBuilderRecoveryInput(before)); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 {
		t.Fatal("second-original failure did not use a new budgeted round")
	}
	corrections := 0
	for _, relay := range h.relays {
		if strings.HasPrefix(relay.clientMessageID, states[0].ClientMessageID) && strings.HasSuffix(relay.clientMessageID, ":parse-correction") {
			corrections++
		}
	}
	if corrections != 1 {
		t.Fatalf("old step acquired %d corrections", corrections)
	}
}

func TestWorkflowInvalidBuilderCanUseOnlyCorrectionOnSecondAttempt(t *testing.T) {
	ctx := context.Background()
	f, _, id, before := invalidBuilderWorkflowFixtureSetup(t, "original-provider-failure", nil)
	step, _ := complexExecutionStepByID(before, before.Dispatches[0].AgentStepID)
	correction, found, err := f.store.GetClearDevParseCorrection(ctx, step.ID)
	if err != nil || !found || correction.AttemptNumber != 2 || correction.ClientMessageID != step.ClientMessageID+":parse-correction" {
		t.Fatalf("second-attempt correction fixture lost existing ID semantics: %+v %v", correction, err)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.Options) != 1 || view.Options[0].UnavailableReason != "" {
		t.Fatalf("completed second-attempt correction was refused: %+v %v", view, err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, invalidBuilderRecoveryInput(before)); err != nil {
		t.Fatal(err)
	}
	completed := driveWorkflowRecovery(t, f, id)
	if completed.Run.CompletedAt == nil || len(completed.Dispatches) != 2 {
		t.Fatal("second-attempt correction stop did not continue normally")
	}
}

func TestWorkflowInvalidBuilderRecoverySurvivesDatabaseReopen(t *testing.T) {
	f, h, id, before := invalidBuilderWorkflowFixture(t)
	input := invalidBuilderRecoveryInput(before)
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), id, input); err != nil {
		t.Fatal(err)
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
	f.service()
	f.s.stepTimeout = 10 * time.Second
	h.store, h.service = f.store, f.s
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h
	f.s.finalReviews, f.s.resultPreview = f.store, h.trial
	f.s.runBackground = func(func()) {}
	if _, err := f.s.RequestWorkflowRecovery(context.Background(), id, input); err != nil {
		t.Fatal(err)
	}
	after := driveWorkflowRecovery(t, f, id)
	if after.Run.CompletedAt == nil || len(after.Dispatches) != 2 || len(after.WorkflowRecoveries) != 1 || !reflect.DeepEqual(before.Dispatches[0], after.Dispatches[0]) {
		t.Fatal("restart lost or repeated the original recovery")
	}
}

func TestWorkflowInvalidBuilderConcurrentRequestsCreateOneSuccessor(t *testing.T) {
	f, h, id, before := invalidBuilderWorkflowFixture(t)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := invalidBuilderRecoveryInput(before)
			input.RequestID = fmt.Sprintf("competing-recovery-%d", i)
			_, err := f.s.RequestWorkflowRecovery(context.Background(), id, input)
			errors <- err
		}(i)
	}
	wg.Wait()
	close(errors)
	succeeded := 0
	for err := range errors {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent recovery accepted %d requests", succeeded)
	}
	after := driveWorkflowRecovery(t, f, id)
	if len(after.WorkflowRecoveries) != 1 || len(after.Dispatches) != 2 {
		t.Fatal("concurrent requests produced multiple successor rounds")
	}
	builderMessages := 0
	for _, relay := range h.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) && !strings.Contains(relay.clientMessageID, "parse-correction") {
			builderMessages++
		}
	}
	if builderMessages != 2 {
		t.Fatalf("original plus recovered Builder requests = %d", builderMessages)
	}
}

func TestWorkflowInvalidBuilderRechecksEvidenceAndNativeIdentityBeforeSending(t *testing.T) {
	for _, change := range []string{"native-session", "unknown-evidence"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			f, h, id, before := invalidBuilderWorkflowFixture(t)
			if _, err := f.s.RequestWorkflowRecovery(ctx, id, invalidBuilderRecoveryInput(before)); err != nil {
				t.Fatal(err)
			}
			if change == "native-session" {
				b, _ := complexExecutionBindingByID(before, before.Dispatches[0].BuilderRoleBindingID)
				record, _, err := f.store.GetSession(ctx, domain.SessionID(b.AOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				record.Metadata.ProviderConversationID = "another-native-session"
				if err := f.store.UpdateSession(ctx, record); err != nil {
					t.Fatal(err)
				}
			} else {
				appendInvalidBuilderUnknownEvidence(t, f, id, before)
			}
			calls := len(h.relays)
			after := stoppedInvalidBuilderWorkflow(t, f, id)
			if len(h.relays) != calls || len(after.WorkflowRecoveries) != 1 || after.Run.CompletedAt != nil {
				t.Fatal("stale continuation sent a new message")
			}
		})
	}
}

func TestWorkflowInvalidBuilderBudgetGrantDoesNotAutoContinue(t *testing.T) {
	ctx := context.Background()
	f, h, id, stopped := invalidBuilderWorkflowFixture(t)
	h.benchmarkSequentialCandidates = true
	for round := 0; round < 3; round++ {
		h.builderInvalidLeft = 2
		input := invalidBuilderRecoveryInput(stopped)
		input.RequestID = fmt.Sprintf("invalid-round-%d", round)
		input.TargetID = stopped.Tasks[0].CurrentDispatchID
		if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
			t.Fatal(err)
		}
		stopped = stoppedInvalidBuilderWorkflow(t, f, id)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.Options) != 1 || view.Options[0].UnavailableReason != "BUILDER_BUDGET_EXHAUSTED" {
		t.Fatalf("invalid results bypassed the Builder budget: %+v %v", view, err)
	}
	before := stopped
	calls := len(h.relays)
	if _, err := f.s.RequestExtraBuilderTurn(ctx, id, stopped.Tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	// Explicit test-only native authority; no actual user approval is performed.
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, id, core.HumanDecisionKindExtraBuilderTurn, core.HumanDecisionApprove)
	afterGrant, _, err := f.store.GetClearDevComplexExecution(ctx, id)
	if err != nil || len(afterGrant.Dispatches) != 4 || afterGrant.Tasks[0].Status != before.Tasks[0].Status || len(h.relays) != calls {
		t.Fatalf("grant started work without a recovery request: %v", err)
	}
	input := invalidBuilderRecoveryInput(stopped)
	input.RequestID, input.TargetID = "invalid-after-grant", stopped.Tasks[0].CurrentDispatchID
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	completed := driveWorkflowRecovery(t, f, id)
	if completed.Run.CompletedAt == nil || len(completed.Dispatches) != 5 || len(completed.WorkflowRecoveries) != 4 {
		t.Fatal("authorized and explicitly requested recovery did not finish")
	}
	for i := range before.Dispatches {
		if !reflect.DeepEqual(before.Dispatches[i], completed.Dispatches[i]) {
			t.Fatal("budget recovery rewrote an old invalid result stop")
		}
	}
}

func TestWorkflowInvalidBuilderPreservesUnfinishedWorkAndRejectsChanges(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			f, flow, id, before := invalidBuilderWorkflowFixture(t)
			h := &unfinishedWorkflowBuilder{workflowBlockedBuilderHarness: &workflowBlockedBuilderHarness{projectExecutionFlowHarness: flow}, dirty: true, digest: strings.Repeat("a", 64)}
			f.s.chat, f.s.inspector = h, h
			if _, err := f.s.RequestWorkflowRecovery(context.Background(), id, invalidBuilderRecoveryInput(before)); err != nil {
				t.Fatal(err)
			}
			if changed {
				calls := len(h.relays)
				h.digest = strings.Repeat("b", 64)
				stopped := stoppedInvalidBuilderWorkflow(t, f, id)
				if len(h.relays) != calls || len(stopped.WorkflowRecoveries) != 1 {
					t.Fatal("modified unfinished work was silently sent")
				}
				return
			}
			after := driveWorkflowRecovery(t, f, id)
			if after.Run.CompletedAt == nil || after.WorkflowRecoveries[0].WorkingTreeSHA256 != h.digest || h.reads < 2 {
				t.Fatal("unfinished work was not retained and rechecked")
			}
		})
	}
}

func appendInvalidBuilderUnknownEvidence(t *testing.T, f *projectPlanningFixture, id string, before core.ComplexExecutionSnapshot) {
	t.Helper()
	ctx := context.Background()
	attempts, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, before.Dispatches[0].AgentStepID)
	if err != nil || len(attempts) == 0 {
		t.Fatalf("no Builder attempt: %v", err)
	}
	a := attempts[len(attempts)-1]
	if err := f.store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: "new-unknown-after-invalid", AttemptID: a.ID, Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: a.LastClientMessageID, PromptSHA256: a.PromptSHA256, FailureCategory: domain.AgentFailureDeliveryUnknown, RecordedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowInvalidBuilderRejectsLaterUnknownEvidence(t *testing.T) {
	ctx := context.Background()
	f, h, id, before := invalidBuilderWorkflowFixture(t)
	attempts, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, before.Dispatches[0].AgentStepID)
	if err != nil || len(attempts) == 0 {
		t.Fatalf("no Builder attempt: %v", err)
	}
	a := attempts[len(attempts)-1]
	if err := f.store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: "new-unknown-after-invalid", AttemptID: a.ID, Status: core.AgentAttemptDeliveryUnknown, ClientMessageID: a.LastClientMessageID, PromptSHA256: a.PromptSHA256, FailureCategory: domain.AgentFailureDeliveryUnknown, RecordedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	calls := len(h.relays)
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.Options) != 1 || view.Options[0].UnavailableReason != "RESULT_NOT_SETTLED" {
		t.Fatalf("unknown delivery must show a concrete refusal: %+v %v", view, err)
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, invalidBuilderRecoveryInput(before)); err == nil {
		t.Fatal("unknown delivery acquired a new Builder round")
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	oldJSON, _ := json.Marshal(before)
	newJSON, _ := json.Marshal(after)
	if string(oldJSON) != string(newJSON) || calls != len(h.relays) {
		t.Fatal("refused recovery changed work or sent a message")
	}
}
