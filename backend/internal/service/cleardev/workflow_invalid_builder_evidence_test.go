package cleardev

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var errInvalidBuilderEvidenceFixture = errors.New("explicit interrupted result persistence fixture")

type interruptedBuilderResultStore struct {
	AgentAttemptStore
	missing string
}

func (s interruptedBuilderResultStore) RecordClearDevAgentStepResult(ctx context.Context, result core.AgentStepResult) error {
	if s.missing == "result" && result.Source == core.AgentResultCorrection {
		return errInvalidBuilderEvidenceFixture
	}
	return s.AgentAttemptStore.RecordClearDevAgentStepResult(ctx, result)
}

func (s interruptedBuilderResultStore) RecordClearDevAgentAttemptEvent(ctx context.Context, event core.AgentAttemptEvent) error {
	if s.missing == "completed" && event.Status == core.AgentAttemptCompleted && strings.HasSuffix(event.ClientMessageID, ":parse-correction") {
		return errInvalidBuilderEvidenceFixture
	}
	return s.AgentAttemptStore.RecordClearDevAgentAttemptEvent(ctx, event)
}

func (s interruptedBuilderResultStore) RecordClearDevAgentStepResultParse(ctx context.Context, parse core.AgentStepResultParse) error {
	if s.missing == "parse" && strings.HasSuffix(parse.ResultID, ":result:2") {
		return errInvalidBuilderEvidenceFixture
	}
	return s.AgentAttemptStore.RecordClearDevAgentStepResultParse(ctx, parse)
}

func invalidBuilderStoreRecovery(t *testing.T, f *projectPlanningFixture, before core.ComplexExecutionSnapshot) core.WorkflowRecovery {
	t.Helper()
	d := before.Dispatches[0]
	summary, known, err := f.store.ReadClearDevStoppedBuilderResult(context.Background(), before.Run.ID, d.ID)
	if err != nil || !known {
		t.Fatalf("fixture has no complete invalid result proof: %v", err)
	}
	return core.WorkflowRecovery{ID: "direct-invalid-builder", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: d.ID,
		DispatchID: d.ID, TaskID: d.ComplexExecutionTaskID, StepID: d.AgentStepID, BindingID: d.BuilderRoleBindingID, SuccessorID: "direct-successor",
		CandidateSHA: d.BaseCommitSHA, ProviderConversationID: "invalid-builder-original-native-session", OriginalStoppedAt: *d.SettledAt,
		OriginalStatus: string(d.Status), OriginalReason: string(d.ReasonCode), OriginalSummary: summary, Supplement: "Preserve the work and continue under the original scope.", CreatedAt: time.Now().UTC()}
}

func TestWorkflowInvalidBuilderStoreRechecksProofAndSession(t *testing.T) {
	for _, mode := range []string{"unchanged", "unknown", "native-session", "summary"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, h, id, before := invalidBuilderWorkflowFixture(t)
			r := invalidBuilderStoreRecovery(t, f, before)
			switch mode {
			case "unknown":
				appendInvalidBuilderUnknownEvidence(t, f, id, before)
			case "native-session":
				b, _ := complexExecutionBindingByID(before, r.BindingID)
				record, _, err := f.store.GetSession(ctx, domain.SessionID(b.AOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				record.Metadata.ProviderConversationID = "different-provider-conversation"
				if err := f.store.UpdateSession(ctx, record); err != nil {
					t.Fatal(err)
				}
			case "summary":
				r.OriginalSummary = "Invented diagnosis"
			}
			calls := len(h.relays)
			err := f.store.ApplyClearDevWorkflowRecovery(ctx, r, nil)
			if mode == "unchanged" {
				if err != nil {
					t.Fatalf("direct valid fixture was rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("transaction trusted stale caller evidence")
			}
			after, _, readErr := f.store.GetClearDevComplexExecution(ctx, id)
			if readErr != nil || len(h.relays) != calls || mode != "unchanged" && len(after.WorkflowRecoveries) != 0 {
				t.Fatalf("rejected direct write changed work: %v", readErr)
			}
		})
	}
}

func TestWorkflowInvalidBuilderMissingResultEvidenceNeverEnablesRecovery(t *testing.T) {
	for _, missing := range []string{"result", "completed", "parse"} {
		t.Run(missing, func(t *testing.T) {
			ctx := context.Background()
			f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
			f.s.stepTimeout = 10 * time.Second
			h := attachProjectFlow(f, preparer)
			h.builderInvalidLeft = 2
			f.s.attempts = interruptedBuilderResultStore{AgentAttemptStore: f.store, missing: missing}
			if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
				t.Fatal(err)
			}
			injected := false
			for i := 0; i < 120; i++ {
				_, _, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
				if errors.Is(err, errInvalidBuilderEvidenceFixture) {
					injected = true
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !injected {
				t.Fatal("persistence fault did not occur")
			}
			f.s.attempts = f.store
			e, _, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
			if err != nil || len(e.Dispatches) != 1 {
				t.Fatalf("missing fixture dispatch: %v", err)
			}
			d := e.Dispatches[0]
			states, err := f.store.ListClearDevAgentStepAttemptStates(ctx, child.Requirement.ID, d.AgentStepID)
			if err != nil || len(states) != 1 {
				t.Fatalf("missing fixture attempt: %v", err)
			}
			a := states[0]
			// Deliberately model a legacy stop with incomplete persistence. The
			// missing facts are not fabricated; a terminal label alone is unsafe.
			if err := f.store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: "incomplete-result-stop", AttemptID: a.ID, Status: core.AgentAttemptFailed, ClientMessageID: a.ClientMessageID + ":parse-correction", FailureCategory: domain.AgentFailureResultInvalid, TurnState: domain.TurnStateFailed, RecordedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.s.failComplexExecutionDispatch(ctx, e, e.Tasks[0], d, false, "BUILDER_RESULT_INVALID"); err != nil {
				t.Fatal(err)
			}
			b, _ := complexExecutionBindingByID(e, d.BuilderRoleBindingID)
			record, _, err := f.store.GetSession(ctx, domain.SessionID(b.AOSessionID))
			if err != nil {
				t.Fatal(err)
			}
			record.Metadata.ProviderConversationID = "incomplete-result-native-session"
			if err := f.store.UpdateSession(ctx, record); err != nil {
				t.Fatal(err)
			}
			if _, known, err := f.store.ReadClearDevStoppedBuilderResult(ctx, e.Run.ID, d.ID); err != nil || known {
				t.Fatalf("missing %s still acquired result proof: %v", missing, err)
			}
			calls := len(h.relays)
			view, err := f.s.GetWorkflowRecovery(ctx, child.Requirement.ID)
			if err != nil || len(view.Options) != 1 || view.Options[0].UnavailableReason != "RESULT_NOT_SETTLED" {
				t.Fatalf("incomplete stop has no truthful explanation: %+v %v", view, err)
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, child.Requirement.ID, invalidBuilderRecoveryInput(e)); err == nil || len(h.relays) != calls {
				t.Fatal("incomplete persistence caused another send")
			}
		})
	}
}
