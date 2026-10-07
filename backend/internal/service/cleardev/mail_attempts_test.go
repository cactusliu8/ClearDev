package cleardev

import (
	"context"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Models/checks are explicit doubles. All counters, stops, grants, and final
// completion use the real SQLite implementation and protected transactions.
type boundedMailChecks struct {
	*mailFlowChecks
	failures       int
	infrastructure bool
}

func (h *boundedMailChecks) RunCandidateCheck(ctx context.Context, r ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	result, err := h.mailFlowChecks.RunCandidateCheck(ctx, r)
	if h.infrastructure {
		result.Outcome = ports.ClearDevCheckInfraError
		result.ExitCode = -1
		return result, err
	}
	if h.failures > 0 {
		h.failures--
		result.Outcome = ports.ClearDevCheckFail
		result.ExitCode = 1
		result.OutputSummary = "explicit deterministic test failure"
		result.OutputSHA256 = coreDigest([]byte(result.OutputSummary))
	}
	return result, err
}
func newBoundedMailFixture(t *testing.T, failures int) (*autoExecutionFixture, *boundedMailChecks) {
	t.Helper()
	f, h := newMailFlowFixture(t)
	f.service.boundedMailAttempts = true
	check := &boundedMailChecks{mailFlowChecks: h, failures: failures}
	f.service.checks = check
	return f, check
}
func boundedExecution(t *testing.T, f *autoExecutionFixture) core.ComplexExecutionSnapshot {
	t.Helper()
	e, ok, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil || !ok {
		t.Fatalf("execution=%v err=%v", ok, err)
	}
	return e
}
func TestBoundedMailThirdDevelopmentAttemptCanComplete(t *testing.T) {
	f, _ := newBoundedMailFixture(t, 2)
	f.confirm(t)
	e := boundedExecution(t, f)
	phase, reason := core.DeriveComplexExecutionPhase(e)
	if phase != core.ComplexExecutionCompleted || len(e.Dispatches) != 3 || len(e.Reviews) != 1 {
		t.Fatalf("phase=%s reason=%s dispatches=%+v reviews=%+v", phase, reason, e.Dispatches, e.Reviews)
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), e.Run.ID)
	if err != nil || len(slots) != 3 {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	for _, s := range slots {
		if s.Kind != core.MailAttemptDevelopment {
			t.Fatalf("slot=%+v", s)
		}
	}
	if f.counts().builderSends != 3 {
		t.Fatal("wrong number of implementation sends")
	}
}
func TestBoundedMailExhaustionNeedsExactDesktopGrant(t *testing.T) {
	f, h := newBoundedMailFixture(t, 3)
	f.confirm(t)
	e := boundedExecution(t, f)
	assertMailNotCompleted(t, f)
	if len(e.Dispatches) != 3 || e.Tasks[0].Status != core.DevelopmentTaskStatusNeedsHuman {
		t.Fatalf("dispatch=%+v task=%+v", e.Dispatches, e.Tasks)
	}
	applyFakeDesktopDecision(t, f.store, f.service, f.clock, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt, core.HumanDecisionApprove)
	e = boundedExecution(t, f)
	phase, reason := core.DeriveComplexExecutionPhase(e)
	if phase != core.ComplexExecutionCompleted || len(e.Dispatches) != 4 {
		t.Fatalf("phase=%s reason=%s dispatch=%+v", phase, reason, e.Dispatches)
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), e.Run.ID)
	if err != nil || len(slots) != 4 || slots[3].Kind != core.MailAttemptHumanExtra || slots[3].GrantRequestID == "" {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	counts := f.counts()
	f.reopen(t)
	f.service.checks = h
	f.service.inspector = h
	h.standardAgentHarness = f.harness
	if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if counts != f.counts() {
		t.Fatal("completed recovery repeated an authorized attempt")
	}
}
func TestBoundedMailRequestedChecksRepairAndHumanExtraShareFiniteBudget(t *testing.T) {
	f, h := newRequestingReviewer(t)
	f.service.boundedMailAttempts = true
	h.reviewVerdicts = []string{"PASS", "REWORK", "PASS", "REWORK", "PASS", "PASS"}
	f.confirm(t)
	e := boundedExecution(t, f)
	assertMailNotCompleted(t, f)
	if len(e.Dispatches) != 2 || len(e.Reviews) != 2 || len(h.reports) != 2 {
		t.Fatalf("before grant: dispatch=%+v reviews=%+v reports=%d", e.Dispatches, e.Reviews, len(h.reports))
	}
	applyFakeDesktopDecision(t, f.store, f.service, f.clock, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt, core.HumanDecisionApprove)
	e = boundedExecution(t, f)
	phase, reason := core.DeriveComplexExecutionPhase(e)
	if phase != core.ComplexExecutionCompleted || len(e.Dispatches) != 3 || len(e.Reviews) != 3 || len(h.reports) != 3 || len(h.sessions) != 6 {
		t.Fatalf("phase=%s reason=%s dispatch=%+v reviews=%+v reports=%d", phase, reason, e.Dispatches, e.Reviews, len(h.reports))
	}
	for _, session := range h.sessions {
		if session != h.sessions[0] {
			t.Fatal("human extra or check return replaced Reviewer session")
		}
	}
	if f.counts().spawns != 4 {
		t.Fatal("unexpected new provider session")
	}
	budget, err := f.store.GetClearDevMessageBudget(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, role := range budget.Roles {
		if role.RoleKind == core.ComplexExceptionBudgetReviewer {
			found = true
			// Three review rounds spend three turns; each round's check-results
			// follow-up shares the round's turn instead of spending another.
			if role.MaxSteps == nil || *role.MaxSteps != 6 || role.ReservedSteps == nil || *role.ReservedSteps != 3 {
				t.Fatalf("reviewer budget=%+v", role)
			}
		}
	}
	if !found {
		t.Fatal("reviewer budget missing")
	}
}

func TestBoundedMailInfrastructureDoesNotStartBusinessRetry(t *testing.T) {
	f, h := newBoundedMailFixture(t, 0)
	h.infrastructure = true
	f.confirm(t)
	e := boundedExecution(t, f)
	assertMailNotCompleted(t, f)
	if len(e.Dispatches) != 1 || f.counts().builderSends != 1 {
		t.Fatal("infrastructure failure started another business attempt")
	}
	pending, err := f.store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if r.DecisionKind == core.HumanDecisionKindExtraMailAttempt {
			t.Fatal("infrastructure failure consumed business retry authorization")
		}
	}
}
