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
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type unifiedHandoffHarness struct {
	*projectExecutionFlowHarness
	reject  bool
	problem string
	before  bool
	digest  string
}

func (h *unifiedHandoffHarness) FreezeMailCandidate(ctx context.Context, r ports.ClearDevMailFreezeRequest) (ports.ClearDevCandidateInspection, error) {
	if h.reject {
		h.currentCandidate = ""
		return ports.ClearDevCandidateInspection{}, &ports.ClearDevCandidateHandoffError{Cause: ports.ErrClearDevCandidateInvalid, ProblemCode: h.problem, BeforePublication: h.before, Path: "app/main.cjs"}
	}
	return h.projectExecutionFlowHarness.FreezeMailCandidate(ctx, r)
}
func (h *unifiedHandoffHarness) InspectWorkflowWorkingTree(context.Context, ports.ClearDevMailFreezeRequest) (string, error) {
	return h.digest, nil
}

func unifiedHandoffFixture(t *testing.T, problem string) (*projectPlanningFixture, *unifiedHandoffHarness, string, core.ComplexExecutionSnapshot) {
	t.Helper()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &unifiedHandoffHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), reject: true, problem: problem, before: true, digest: strings.Repeat("b", 64)}
	f.s.inspector = h
	f.s.stepTimeout = 15 * time.Second
	f.s.automaticFailureRouting = true
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, child.Requirement.ID)
	return f, h, child.Requirement.ID, before
}

func TestUnifiedFailureHandoffAutomaticallyReturnsExactEvidenceAndCompletes(t *testing.T) {
	f, h, id, before := unifiedHandoffFixture(t, "NO_IMPLEMENTATION_CHANGE")
	ctx := context.Background()
	receipts, err := f.store.ListClearDevWorkflowFailures(ctx, id)
	if err != nil || len(receipts) != 1 || receipts[0].SourceID != before.Dispatches[0].ID || !receipts[0].BeforePublish {
		t.Fatal("missing exact handoff evidence", receipts, err)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.FailureHandling) == 0 {
		t.Fatal("no failure handling view", err)
	}
	if view.FailureHandling[0].Owner != core.TrustedOwnerBuilder || view.FailureHandling[0].Action != core.FailureRepair {
		t.Fatalf("wrong owner/action %+v", view.FailureHandling)
	}
	// No simulated correction or manual recovery POST: normal scheduler owns it.
	h.reject = false
	after := driveWorkflowRecovery(t, f, id)
	if after.Run.CompletedAt == nil || len(after.WorkflowRecoveries) != 1 || len(after.Dispatches) != 2 {
		t.Fatalf("not automatically completed: %+v", after.Dispatches)
	}
	if !core.IsAutomaticFailureRecovery(after.WorkflowRecoveries[0].ID) {
		t.Fatal("not a coordinated recovery")
	}
	if !reflect.DeepEqual(before.Dispatches[0], after.Dispatches[0]) {
		t.Fatal("old failure was rewritten")
	}
	sealed, err := json.Marshal(after.WorkflowRecoveries[0])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, relay := range h.relays {
		found = found || strings.Contains(relay.prompt, string(sealed))
	}
	if !strings.Contains(after.WorkflowRecoveries[0].Supplement, "No implementation change was available") || !strings.Contains(after.WorkflowRecoveries[0].Supplement, automaticFailureFeedback) {
		t.Fatal("stored feedback omitted exact cause or authority boundary")
	}
	if !found {
		t.Fatal("original Builder never received exact control-program evidence")
	}
	if after.FinalReview == nil || after.FinalReview.Verdict != "PASS" || len(after.Verifications) != 1 {
		t.Fatal("recovery substituted for independent verification")
	}
}

func TestUnifiedFailurePathAuthorityAndUnprovenPublicationStayHuman(t *testing.T) {
	for _, problem := range []string{"PATH_SCOPE", "UNSAFE_PATH", "UNKNOWN"} {
		t.Run(problem, func(t *testing.T) {
			f, h, id, before := unifiedHandoffFixture(t, problem)
			sends := len(h.relays)
			for range 3 {
				x, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				changed, err := f.s.advanceUnifiedFailureRecovery(context.Background(), id, &x)
				if err != nil || changed {
					t.Fatal("unsafe failure was acted on", changed, err)
				}
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil || len(h.relays) != sends || len(after.WorkflowRecoveries) != 0 || !reflect.DeepEqual(before.Dispatches, after.Dispatches) {
				t.Fatal("human boundary changed or sent a role", err)
			}
			view, err := f.s.GetWorkflowRecovery(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.FailureHandling) == 0 || view.FailureHandling[0].Owner != core.TrustedOwnerHuman {
				t.Fatal("missing human question", view.FailureHandling)
			}
		})
	}
}

func TestUnifiedFailureConcurrentRecoveryHasOneSuccessorAndSurvivesRestart(t *testing.T) {
	started := time.Now()
	t.Log("initializing fully migrated SQLite fixture before concurrent recovery")
	f, h, id, before := unifiedHandoffFixture(t, "NO_IMPLEMENTATION_CHANGE")
	t.Logf("fixture ready after %s; starting concurrent recovery", time.Since(started))
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.s.advanceUnifiedFailureRecovery(ctx, id, &before)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	t.Logf("concurrent recovery settled after %s; verifying unique durable successor", time.Since(started))
	after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
	if err != nil || len(after.WorkflowRecoveries) != 1 {
		t.Fatal("duplicated recovery", err)
	}
	raw, _ := json.Marshal(after.WorkflowRecoveries)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	t.Logf("original database reopened after %s; completing the existing recovery", time.Since(started))
	f.service()
	f.s.automaticFailureRouting = true
	h.store, h.service = f.store, f.s
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h
	f.s.finalReviews, f.s.resultPreview = f.store, h.trial
	f.s.runBackground = func(func()) {}
	h.reject = false
	completed := driveWorkflowRecovery(t, f, id)
	rawAfter, _ := json.Marshal(completed.WorkflowRecoveries)
	if completed.Run.CompletedAt == nil || string(raw) != string(rawAfter) {
		t.Fatal("restart duplicated/replaced recovery")
	}
	t.Logf("recovered workflow completed after %s without changing recovery history", time.Since(started))
}

func TestUnifiedFailureReceiptCannotBeReboundOrChanged(t *testing.T) {
	f, _, id, _ := unifiedHandoffFixture(t, "NO_IMPLEMENTATION_CHANGE")
	ctx := context.Background()
	rows, err := f.store.ListClearDevWorkflowFailures(ctx, id)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	r := rows[0]
	r.ObservedAt = r.ObservedAt.AddDate(0, 0, 1)
	if err := f.store.RecordClearDevWorkflowFailure(ctx, r); err != nil {
		t.Fatal("timestamp re-observation should be idempotent", err)
	}
	r.Summary = "replacement success"
	if err := f.store.RecordClearDevWorkflowFailure(ctx, r); err == nil {
		t.Fatal("failure was overwritten")
	}
	r = rows[0]
	r.SourceID = "another-dispatch"
	r.ID = core.FailureSourceKey(id, r.SourceKind, r.SourceID)
	if err := f.store.RecordClearDevWorkflowFailure(ctx, r); err == nil {
		t.Fatal("failure rebound to another source")
	}
}

func TestUnifiedFailurePauseAndChangedSpecificationPreventAutomaticWork(t *testing.T) {
	for _, boundary := range []string{"pause", "stale-spec"} {
		t.Run(boundary, func(t *testing.T) {
			f, h, id, before := unifiedHandoffFixture(t, "NO_IMPLEMENTATION_CHANGE")
			stopBuilderFirstFixture(t, f, before, boundary)
			calls := len(h.relays)
			for range 2 {
				_, _, _ = f.s.advanceComplexStandardExecution(context.Background(), id)
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
			if err != nil || len(after.WorkflowRecoveries) != 0 || len(h.relays) != calls || !reflect.DeepEqual(before.Dispatches, after.Dispatches) {
				t.Fatal("source or pause boundary gained an automatic recovery", err)
			}
		})
	}
}

func TestUnifiedFailureReadOnlyProjectionCannotManufactureARecovery(t *testing.T) {
	f, h, id, _ := unifiedHandoffFixture(t, "NO_IMPLEMENTATION_CHANGE")
	calls := len(h.relays)
	for range 3 {
		view, err := f.s.GetWorkflowRecovery(context.Background(), id)
		if err != nil || len(view.FailureHandling) == 0 {
			t.Fatal(err)
		}
	}
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
	if err != nil || len(h.relays) != calls || len(after.WorkflowRecoveries) != 0 {
		t.Fatal("diagnostic GET dispatched a repair", err)
	}
}
