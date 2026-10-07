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

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// Model/Git/checks are explicit doubles. The real adapter's linked-worktree,
// crash, sandbox and Docker checks live in cleardevlocal; all control facts,
// attempts and completion transactions here use the production SQLite store.
type freezingMailHarness struct {
	*mailFlowChecks
	requests     []ports.ClearDevMailFreezeRequest
	saved        map[string]ports.ClearDevCandidateInspection
	mode         string
	crashed      bool
	freezeReady  bool
	failedChecks int
	emptyAfter   int
}

func newFreezingMailFixture(t *testing.T) (*autoExecutionFixture, *freezingMailHarness) {
	t.Helper()
	f, base := newMailFlowFixture(t)
	h := &freezingMailHarness{mailFlowChecks: base, saved: map[string]ports.ClearDevCandidateInspection{}}
	f.service.boundedMailAttempts = true
	f.service.inspector, f.service.checks, f.service.chat = h, h, h
	return f, h
}

func (h *freezingMailHarness) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	turn, err := h.mailFlowChecks.RelayChatTurnWithID(ctx, id, prompt, key)
	if err != nil || !strings.Contains(prompt, "TRUSTED_MAIL_FREEZE_V1") {
		return turn, err
	}
	record, found, err := h.store.GetSession(ctx, id)
	if err != nil || !found {
		return turn, errors.New("fixture missing Builder")
	}
	record.Activity.State = domain.ActivityIdle
	if h.mode == "wrong-project" {
		record.Metadata.WorkspaceRepoPath = "/foreign-repo"
	}
	if err := h.store.UpdateSession(ctx, record); err != nil {
		return turn, err
	}
	return turn, nil
}

func (h *freezingMailHarness) Snapshot(ctx context.Context, id domain.SessionID) (chatsvc.Snapshot, error) {
	snapshot, err := h.standardAgentHarness.Snapshot(ctx, id)
	if err != nil || !h.freezeReady {
		return snapshot, err
	}
	record, found, err := h.store.GetSession(ctx, id)
	if err != nil || !found || record.DisplayName != "ClearDev Complex Builder" {
		return snapshot, err
	}
	switch h.mode {
	case "unknown":
		return snapshot, errors.New("explicit unknown provider snapshot")
	case "wrong-session":
		snapshot.SessionID = "foreign-session"
	case "busy":
		snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{ID: "later-active-turn", State: domain.TurnStateRunning})
	case "missing-reply":
		snapshot.Messages = nil
	}
	return snapshot, nil
}

func (h *freezingMailHarness) FreezeMailCandidate(ctx context.Context, request ports.ClearDevMailFreezeRequest) (ports.ClearDevCandidateInspection, error) {
	if h.mode == "scope" {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevMailScopeViolation
	}
	if h.mode == "infra" {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevGitUnavailable
	}
	if h.emptyAfter > 0 && len(h.saved) >= h.emptyAfter {
		h.emptyAfter = 0
		h.requests = append(h.requests, request)
		h.mu.Lock()
		h.currentCandidate = request.ParentSHA
		h.mu.Unlock()
		return ports.ClearDevCandidateInspection{}, fmt.Errorf("%w: no implementation change to freeze", ports.ErrClearDevCandidateInvalid)
	}
	if saved, found := h.saved[request.RunID]; found {
		return saved, nil
	}
	inspection, err := h.InspectCandidate(ctx, request.WorkspacePath, request.BaseSHA)
	if err != nil {
		return inspection, err
	}
	if h.mode == "stale" {
		inspection.CandidateSHA = request.BaseSHA
	}
	h.requests = append(h.requests, request)
	h.saved[request.RunID] = inspection
	if h.mode == "crash-freeze" && !h.crashed {
		h.crashed = true
		return inspection, context.Canceled
	}
	return inspection, nil
}

func (h *freezingMailHarness) RunCandidateCheck(ctx context.Context, r ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	known := false
	for _, saved := range h.saved {
		known = known || saved.CandidateSHA == r.CandidateSHA
	}
	if !known {
		return ports.ClearDevCheckResult{}, errors.New("required check ran before freeze")
	}
	result, err := h.mailFlowChecks.RunCandidateCheck(ctx, r)
	if h.failedChecks > 0 {
		h.failedChecks--
		result.Outcome = ports.ClearDevCheckFail
		result.ExitCode = 1
		result.OutputSummary = "explicit trusted test failure"
		result.OutputSHA256 = coreDigest([]byte(result.OutputSummary))
	}
	return result, err
}

type freezeReplySeam struct {
	*sqlite.Store
	harness               *freezingMailHarness
	crashAfterObservation bool
	hit                   bool
}

func (s *freezeReplySeam) SettleClearDevComplexExecutionAgentStep(ctx context.Context, step core.AgentStep) (bool, error) {
	changed, err := s.Store.SettleClearDevComplexExecutionAgentStep(ctx, step)
	if err == nil && step.Kind == core.ComplexExecutionAgentStepBuilderTask {
		s.harness.freezeReady = true
	}
	return changed, err
}
func (s *freezeReplySeam) AppendClearDevComplexExecutionCandidate(ctx context.Context, c core.AppendComplexExecutionCandidateCommand) (core.CandidateCommit, error) {
	result, err := s.Store.AppendClearDevComplexExecutionCandidate(ctx, c)
	if err == nil && s.crashAfterObservation && !s.hit {
		s.hit = true
		return result, context.Canceled
	}
	return result, err
}

func bindFreezingFixture(f *autoExecutionFixture, h *freezingMailHarness) {
	f.service.inspector, f.service.checks, f.service.chat = h, h, h
	f.service.complexExecution = &freezeReplySeam{Store: f.store, harness: h}
}

func TestTrustedMailHandoffFreezesBeforeChecksAndPreservesLegacyPrompt(t *testing.T) {
	f, h := newFreezingMailFixture(t)
	bindFreezingFixture(f, h)
	f.confirm(t)
	e := f.completed(t)
	if !core.TrustedMailFreeze(e.Run) || len(h.requests) != 1 || e.Dispatches[0].CandidateCommitSHA != h.saved[e.Dispatches[0].ID].CandidateSHA {
		t.Fatal("candidate freeze is not the program's automatic handoff")
	}
	if h.requests[0].ParentSHA != forty("a") || h.requests[0].RunID != e.Dispatches[0].ID || h.requests[0].RepoPath != "/tmp/s04-project" {
		t.Fatal("freeze request not bound to durable facts")
	}
	legacy := executionBuilderPromptForRun(core.ComplexExecutionRun{}, []byte("{}"), "d", "t", 0, "")
	if legacy != complexExecutionBuilderPrompt([]byte("{}"), "d", "t", 0, "") {
		t.Fatal("historical prompt changed")
	}
	counts := f.counts()
	f.reopen(t)
	bindFreezingFixture(f, h)
	if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.counts() != counts || len(h.requests) != 1 {
		t.Fatal("completed restart refroze or redispatched")
	}
}

func TestTrustedMailHandoffRestartUsesOneFreezeAndCandidate(t *testing.T) {
	for _, mode := range []string{"crash-freeze", "crash-observation"} {
		t.Run(mode, func(t *testing.T) {
			f, h := newFreezingMailFixture(t)
			h.mode = mode
			seam := &freezeReplySeam{Store: f.store, harness: h, crashAfterObservation: mode == "crash-observation"}
			f.service.complexExecution = seam
			f.confirm(t)
			if !h.crashed && !seam.hit {
				t.Fatal("crash not reached")
			}
			assertMailNotCompleted(t, f)
			f.reopen(t)
			bindFreezingFixture(f, h)
			var wg sync.WaitGroup
			errs := make([]error, 8)
			for i := range errs {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					errs[i] = f.service.ResumeComplexStandardExecutions(context.Background())
				}(i)
			}
			wg.Wait()
			for _, err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			f.completed(t)
			if len(h.requests) != 1 || f.counts().builderSends != 1 {
				t.Fatal("restart repeated external publication or Builder")
			}
		})
	}
}

func TestTrustedMailHandoffRejectsUnknownBusyForeignOrScope(t *testing.T) {
	for _, mode := range []string{"unknown", "wrong-session", "busy", "missing-reply", "wrong-project", "scope", "infra", "stale"} {
		t.Run(mode, func(t *testing.T) {
			f, h := newFreezingMailFixture(t)
			h.mode = mode
			bindFreezingFixture(f, h)
			f.confirm(t)
			assertMailNotCompleted(t, f)
			e := boundedExecution(t, f)
			if len(e.Reviews) != 0 {
				t.Fatal("invalid freeze reached Reviewer")
			}
		})
	}
}

func TestTrustedMailHandoffFailedCheckCreatesNewBoundFreeze(t *testing.T) {
	f, h := newFreezingMailFixture(t)
	h.failedChecks = 1
	bindFreezingFixture(f, h)
	f.confirm(t)
	e := boundedExecution(t, f)
	phase, reason := core.DeriveComplexExecutionPhase(e)
	if phase != core.ComplexExecutionCompleted || len(h.requests) != 2 || len(e.Dispatches) != 2 {
		t.Fatalf("phase=%s reason=%s freezes=%+v dispatches=%+v", phase, reason, h.requests, e.Dispatches)
	}
	if h.requests[1].ParentSHA != e.Dispatches[0].CandidateCommitSHA || h.requests[0].RunID == h.requests[1].RunID || reflect.DeepEqual(h.saved[h.requests[0].RunID], h.saved[h.requests[1].RunID]) {
		t.Fatal("retry used the old parent, request or candidate")
	}
}

func TestTrustedMailEmptyReworkFreezeUsesPriorCandidate(t *testing.T) {
	f, h := newFreezingMailFixture(t)
	h.failedChecks = 1
	h.emptyAfter = 1
	f.harness.candidateSHAs = []string{forty("b"), forty("c"), forty("d"), forty("e")}
	bindFreezingFixture(f, h)
	f.confirm(t)
	e := boundedExecution(t, f)
	phase, reason := core.DeriveComplexExecutionPhase(e)
	if phase != core.ComplexExecutionCompleted || len(e.Dispatches) != 3 || len(h.saved) != 2 {
		t.Fatalf("phase=%s reason=%s dispatches=%d saved=%d", phase, reason, len(e.Dispatches), len(h.saved))
	}
	if h.requests[2].ParentSHA != e.Dispatches[0].CandidateCommitSHA {
		t.Fatalf("empty freeze retry parent=%s first candidate=%s", h.requests[2].ParentSHA, e.Dispatches[0].CandidateCommitSHA)
	}
	pending, err := f.store.ListPendingClearDevHumanDecisionRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range pending {
		if request.DecisionKind == core.HumanDecisionKindExtraMailAttempt {
			t.Fatal("remaining automatic attempts issued Extra")
		}
	}
}

func TestTrustedMailEmptyFreezeAfterBudgetOffersExtra(t *testing.T) {
	f, h := newFreezingMailFixture(t)
	h.failedChecks = 2
	h.emptyAfter = 2
	f.harness.candidateSHAs = []string{forty("b"), forty("c"), forty("d"), forty("e")}
	bindFreezingFixture(f, h)
	f.confirm(t)
	e := boundedExecution(t, f)
	assertMailNotCompleted(t, f)
	if len(e.Dispatches) != 3 || e.Tasks[0].Status != core.DevelopmentTaskStatusNeedsHuman {
		t.Fatalf("dispatch=%+v task=%+v", e.Dispatches, e.Tasks)
	}
	pending := pendingHumanRequestByKind(t, f.store, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt)
	var binding core.ExtraMailAttemptBinding
	if err := json.Unmarshal([]byte(pending.BindingJSON), &binding); err != nil {
		t.Fatal(err)
	}
	if binding.CandidateSHA != e.Dispatches[1].CandidateCommitSHA || binding.DispatchID != e.Dispatches[2].ID {
		t.Fatalf("extra binding=%+v first=%s second=%s third=%s", binding, e.Dispatches[0].CandidateCommitSHA, e.Dispatches[1].CandidateCommitSHA, e.Dispatches[2].ID)
	}
	applyFakeDesktopDecision(t, f.store, f.service, f.clock, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt, core.HumanDecisionApprove)
	e = boundedExecution(t, f)
	phase, reason := core.DeriveComplexExecutionPhase(e)
	if phase != core.ComplexExecutionCompleted || len(e.Dispatches) != 4 {
		t.Fatalf("phase=%s reason=%s dispatch=%+v", phase, reason, e.Dispatches)
	}
}
