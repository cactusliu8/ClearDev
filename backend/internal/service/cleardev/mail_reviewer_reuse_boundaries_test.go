package cleardev

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// Fake provider and Git behavior, with real service and SQLite transactions.
type mailReuseRuntime struct {
	*mailFlowChecks
	failure  string
	switches int
}

func (h *mailReuseRuntime) InspectCandidate(ctx context.Context, workspace, base string) (ports.ClearDevCandidateInspection, error) {
	h.mu.Lock()
	head := h.reviewerWorkspaces[workspace]
	reworked := h.benchmarkCandidateOrdinal > 1
	h.mu.Unlock()
	if head == "" || !reworked {
		return h.mailFlowChecks.InspectCandidate(ctx, workspace, base)
	}
	if h.failure == "dirty" {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevWorkspaceDirty
	}
	if h.failure == "wrong-head" {
		head = forty("f")
	}
	paths := []ports.ClearDevDiffPath{}
	if head != base {
		paths = append(paths, ports.ClearDevDiffPath{Status: "M", Path: "backend/src/emails.ts"})
	}
	return ports.ClearDevCandidateInspection{BaseSHA: base, CandidateSHA: head, Paths: paths}, nil
}

func (h *mailReuseRuntime) PrepareBaseWorkspace(ctx context.Context, workspace, oldSHA, newSHA string) error {
	h.switches++
	return h.mailFlowChecks.PrepareBaseWorkspace(ctx, workspace, oldSHA, newSHA)
}

func (h *mailReuseRuntime) Snapshot(ctx context.Context, id domain.SessionID) (chatsvc.Snapshot, error) {
	snapshot, err := h.mailFlowChecks.Snapshot(ctx, id)
	if err != nil {
		return snapshot, err
	}
	h.mu.Lock()
	reworked := h.benchmarkCandidateOrdinal > 1 && h.reviewerRuns == 1
	h.mu.Unlock()
	record, _, _ := h.store.GetSession(ctx, id)
	if reworked && strings.Contains(record.CreationIdempotencyKey, ":review:") {
		switch h.failure {
		case "unknown-session":
			return chatsvc.Snapshot{}, errors.New("fake provider unavailable")
		case "busy-session":
			snapshot.Turns = append(append([]domain.ConversationTurn(nil), snapshot.Turns...), domain.ConversationTurn{ID: "unexpected-running-turn", State: domain.TurnStateRunning})
		case "missing-turn":
			snapshot.Turns = nil
		}
	}
	return snapshot, nil
}

type mailReuseSeam struct {
	ComplexExecutionFactStore
	AgentAttemptStore
	id       string
	boundary string
	fired    bool
	rejected error
}

func (s *mailReuseSeam) reuseBinding(ctx context.Context, id string) (core.ComplexExecutionRoleBinding, bool) {
	e, _, _ := s.GetClearDevComplexExecution(ctx, s.id)
	binding, found := complexExecutionBindingByID(e, id)
	return binding, found && strings.HasPrefix(binding.SessionCreationIdempotencyKey, mailReviewerReuseKeyPrefix)
}

func (s *mailReuseSeam) CreateClearDevComplexExecutionRoleBinding(ctx context.Context, binding core.ComplexExecutionRoleBinding) (core.ComplexExecutionRoleBinding, bool, error) {
	if strings.HasPrefix(binding.SessionCreationIdempotencyKey, mailReviewerReuseKeyPrefix) {
		switch s.boundary {
		case "wrong-task":
			binding.TaskMappingID = "unrelated-task"
		case "wrong-predecessor":
			binding.ContinuationOfRoleBindingID = "unrelated-reviewer"
		case "missing-predecessor":
			binding.ContinuationOfRoleBindingID = ""
		case "same-old-candidate":
			e, _, _ := s.GetClearDevComplexExecution(ctx, s.id)
			binding.CandidateCommitID = e.Reviews[0].CandidateCommitID
		case "forged-reuse-key":
			binding.SessionCreationIdempotencyKey = "arbitrary-key"
		}
	}
	result, changed, err := s.ComplexExecutionFactStore.CreateClearDevComplexExecutionRoleBinding(ctx, binding)
	if err != nil {
		s.rejected = err
	}
	return result, changed, err
}

func (s *mailReuseSeam) BindClearDevComplexExecutionRoleBinding(ctx context.Context, id, session, workspace, sha string, at time.Time) (bool, error) {
	binding, reuse := s.reuseBinding(ctx, id)
	if reuse && s.boundary == "after-checkout" && !s.fired {
		s.fired = true
		return false, errAutoInjectedCrash
	}
	if reuse {
		e, _, _ := s.GetClearDevComplexExecution(ctx, s.id)
		switch s.boundary {
		case "builder-session":
			b, _ := complexExecutionBindingByID(e, e.Run.BuilderRoleBindingID)
			session = b.AOSessionID
		case "builder-workspace":
			b, _ := complexExecutionBindingByID(e, e.Run.BuilderRoleBindingID)
			workspace = b.WorkspacePath
		case "old-sha":
			prior, _ := complexExecutionBindingByID(e, binding.ContinuationOfRoleBindingID)
			sha = prior.BaseCommitSHA
		}
	}
	changed, err := s.ComplexExecutionFactStore.BindClearDevComplexExecutionRoleBinding(ctx, id, session, workspace, sha, at)
	if reuse && err != nil {
		s.rejected = err
	}
	if err == nil && reuse && s.boundary == "after-bind" && !s.fired {
		s.fired = true
		return changed, errAutoInjectedCrash
	}
	return changed, err
}

func (s *mailReuseSeam) CreateClearDevComplexExecutionReview(ctx context.Context, command core.CreateComplexExecutionReviewCommand) (core.ComplexExecutionReview, bool, error) {
	_, reuse := s.reuseBinding(ctx, command.Review.ReviewerRoleBindingID)
	result, changed, err := s.ComplexExecutionFactStore.CreateClearDevComplexExecutionReview(ctx, command)
	if err == nil && reuse && s.boundary == "after-request" && !s.fired {
		s.fired = true
		return result, changed, errAutoInjectedCrash
	}
	return result, changed, err
}

func (s *mailReuseSeam) MarkClearDevComplexExecutionAgentStepSent(ctx context.Context, id string, at time.Time) (bool, error) {
	e, _, _ := s.GetClearDevComplexExecution(ctx, s.id)
	step, _ := complexExecutionStepByID(e, id)
	_, reuse := s.reuseBinding(ctx, step.RoleBindingID)
	if reuse && s.boundary == "after-send" && !s.fired {
		s.fired = true
		return false, errAutoInjectedCrash
	}
	return s.ComplexExecutionFactStore.MarkClearDevComplexExecutionAgentStepSent(ctx, id, at)
}

func (s *mailReuseSeam) ConfirmClearDevAgentMessage(ctx context.Context, event core.AgentAttemptEvent) error {
	attempt, err := s.GetClearDevAgentAttemptState(ctx, s.id, event.AttemptID)
	if err != nil {
		return err
	}
	_, reuse := s.reuseBinding(ctx, attempt.RoleBindingID)
	if reuse && s.boundary == "before-receipt" && !s.fired {
		s.fired = true
		return errAutoInjectedCrash
	}
	return s.AgentAttemptStore.ConfirmClearDevAgentMessage(ctx, event)
}

func TestMailReviewerReuseRestartBoundaries(t *testing.T) {
	for _, boundary := range []string{"after-checkout", "after-bind", "after-request", "after-send", "before-receipt"} {
		t.Run(boundary, func(t *testing.T) {
			f, mail := newMailFlowFixture(t)
			mail.reworkOnce = true
			runtime := &mailReuseRuntime{mailFlowChecks: mail}
			seam := &mailReuseSeam{ComplexExecutionFactStore: f.store, AgentAttemptStore: f.store, id: f.view.Requirement.ID, boundary: boundary}
			f.service.complexExecution, f.service.attempts = seam, seam
			f.service.inspector, f.service.chat = runtime, runtime
			f.confirm(t)
			if !seam.fired || runtime.switches != 1 {
				t.Fatalf("boundary not reached: fired=%v switches=%d", seam.fired, runtime.switches)
			}
			before, _, _ := f.store.GetClearDevComplexExecution(context.Background(), seam.id)
			originalReview := before.Reviews[0]
			f.reopen(t) // Real database close/open, not a reconstructed in-memory fact set.
			f.service.checks, f.service.inspector, f.service.chat = mail, runtime, runtime
			if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			after := mailReworkCompleted(t, f)
			if runtime.switches != 1 || len(mail.spawnConfigs) != 4 || mail.reviewerRuns != 2 || !reflect.DeepEqual(originalReview, after.Reviews[0]) {
				t.Fatalf("repeated or rewritten work: switches=%d spawns=%d reviews=%d", runtime.switches, len(mail.spawnConfigs), mail.reviewerRuns)
			}
		})
	}
}

type mailReuseAO struct {
	AOReader
	runtime *mailReuseRuntime
}

func (a mailReuseAO) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	record, found, err := a.AOReader.GetSession(ctx, id)
	a.runtime.mu.Lock()
	reworked := a.runtime.benchmarkCandidateOrdinal > 1
	a.runtime.mu.Unlock()
	if reworked && strings.Contains(record.CreationIdempotencyKey, ":review:") {
		switch a.runtime.failure {
		case "terminated":
			record.IsTerminated = true
		case "foreign-project":
			record.ProjectID = "other-project"
		case "missing-session":
			found = false
		}
	}
	return record, found, err
}

func TestMailReviewerReuseRefusesUnknownBusyOrDirtyWorkspace(t *testing.T) {
	for _, failure := range []string{"unknown-session", "busy-session", "missing-turn", "dirty", "wrong-head", "terminated", "foreign-project", "missing-session"} {
		t.Run(failure, func(t *testing.T) {
			f, mail := newMailFlowFixture(t)
			mail.reworkOnce = true
			runtime := &mailReuseRuntime{mailFlowChecks: mail, failure: failure}
			f.service.inspector, f.service.chat = runtime, runtime
			f.service.ao = mailReuseAO{AOReader: f.store, runtime: runtime}
			f.confirm(t)
			assertMailNotCompleted(t, f)
			if len(mail.spawnConfigs) != 4 || mail.reviewerRuns != 1 || runtime.switches != 0 {
				t.Fatalf("unsafe reuse: spawns=%d reviews=%d switches=%d", len(mail.spawnConfigs), mail.reviewerRuns, runtime.switches)
			}
		})
	}
}

func TestMailReviewerReuseConcurrentResumeDoesNotDuplicateReview(t *testing.T) {
	f, mail := newMailFlowFixture(t)
	mail.reworkOnce = true
	runtime := &mailReuseRuntime{mailFlowChecks: mail}
	seam := &mailReuseSeam{ComplexExecutionFactStore: f.store, AgentAttemptStore: f.store, id: f.view.Requirement.ID, boundary: "after-request"}
	f.service.complexExecution, f.service.attempts = seam, seam
	f.service.inspector, f.service.chat = runtime, runtime
	f.confirm(t)
	if !seam.fired {
		t.Fatal("new review request was not persisted")
	}
	f.reopen(t)
	f.service.checks, f.service.inspector, f.service.chat = mail, runtime, runtime
	var mu sync.Mutex
	queue := []func(){}
	f.service.runBackground = func(run func()) { mu.Lock(); queue = append(queue, run); mu.Unlock() }
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				errs[i] = f.service.ResumeComplexStandardExecutions(context.Background())
			} else {
				_, errs[i] = f.service.StartComplexStandardExecution(context.Background(), f.view.Requirement.ID)
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; ; i++ {
		mu.Lock()
		if len(queue) == 0 {
			mu.Unlock()
			break
		}
		run := queue[0]
		queue = queue[1:]
		mu.Unlock()
		if i > 4 {
			t.Fatal("re-review kept scheduling work")
		}
		run()
	}
	mailReworkCompleted(t, f)
	if runtime.switches != 1 || len(mail.spawnConfigs) != 4 || mail.reviewerRuns != 2 {
		t.Fatal("concurrent resume repeated Reviewer work")
	}
}

func TestMailReviewerReuseStorageRejectsWrongBindings(t *testing.T) {
	for _, boundary := range []string{"wrong-task", "wrong-predecessor", "missing-predecessor", "same-old-candidate", "forged-reuse-key", "builder-session", "builder-workspace", "old-sha"} {
		t.Run(boundary, func(t *testing.T) {
			f, mail := newMailFlowFixture(t)
			mail.reworkOnce = true
			seam := &mailReuseSeam{ComplexExecutionFactStore: f.store, AgentAttemptStore: f.store, id: f.view.Requirement.ID, boundary: boundary}
			f.service.complexExecution = seam
			f.confirm(t)
			if seam.rejected == nil {
				t.Fatal("invalid binding did not reach a rejecting SQLite transaction")
			}
			assertMailNotCompleted(t, f)
			if len(mail.spawnConfigs) != 4 || mail.reviewerRuns != 1 {
				t.Fatal("invalid binding started another reviewer or sent a review")
			}
		})
	}
}
