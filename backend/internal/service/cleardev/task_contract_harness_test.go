package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// contractAgentHarness retains the existing planning/chat test double while
// making its fake Git state workspace-local. The older auto fixture has only
// one global candidate and cannot model two independent Builders or composition.
// No real model, Git operation, check result, or product acceptance is claimed.
type contractAgentHarness struct {
	*standardAgentHarness
	final        *requirementFinalReviewHarness
	gitMu        sync.Mutex
	workspaces   map[string]*contractFakeWorkspace
	seenTurns    map[string]bool
	composeCalls int
	lastCompose  ports.ClearDevComposeRequest
	taskPaths    map[string][]ports.ClearDevDiffPath // Optional approved paths for policy-combination fixtures.
}

type contractFakeWorkspace struct {
	taskID string
	base   string
	head   string
	paths  []ports.ClearDevDiffPath
}

func newContractAgentHarness(base *standardAgentHarness) *contractAgentHarness {
	final := &requirementFinalReviewHarness{
		mailFlowChecks: &mailFlowChecks{baselineGateHarness: &baselineGateHarness{standardAgentHarness: base}},
		finalSessions:  map[domain.SessionID]bool{}, verdict: "PASS",
	}
	return &contractAgentHarness{standardAgentHarness: base, final: final, workspaces: map[string]*contractFakeWorkspace{}, seenTurns: map[string]bool{}}
}

func (h *contractAgentHarness) Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	// Reuse the existing independent final-review model double, including its
	// exact branch/session binding, instead of inventing a second protocol.
	session, a, b, err := h.final.Spawn(ctx, cfg)
	if err != nil {
		return session, a, b, err
	}
	h.gitMu.Lock()
	defer h.gitMu.Unlock()
	path := session.Metadata.WorkspacePath
	if h.workspaces[path] == nil {
		base := session.Metadata.DiffBaseSHA
		h.mu.Lock()
		if reviewed := h.reviewerWorkspaces[path]; reviewed != "" {
			base = reviewed
		}
		h.mu.Unlock()
		h.workspaces[path] = &contractFakeWorkspace{base: base, head: base}
	}
	return session, a, b, nil
}

func (h *contractAgentHarness) RelayChatTurnWithID(ctx context.Context, sessionID domain.SessionID, prompt, clientID string) (string, error) {
	h.mu.Lock()
	final := h.final.finalSessions[sessionID]
	h.mu.Unlock()
	if final {
		return h.final.RelayChatTurnWithID(ctx, sessionID, prompt, clientID)
	}
	turn, err := h.standardAgentHarness.RelayChatTurnWithID(ctx, sessionID, prompt, clientID)
	if err != nil || !strings.Contains(prompt, `"kind":"BUILDER_RESULT"`) {
		return turn, err
	}
	record, found, err := h.store.GetSession(ctx, sessionID)
	if err != nil || !found {
		return "", errors.New("test Builder session not found")
	}
	// The immutable package begins after the same marker used by the production
	// prompt. Decode one JSON object; later feedback/result examples are separate.
	_, tail, found := strings.Cut(prompt, "执行包：\n")
	if !found {
		return "", errors.New("test Builder received no execution package")
	}
	var pkg core.ComplexStandardExecutionPackage
	if err := json.NewDecoder(strings.NewReader(tail)).Decode(&pkg); err != nil {
		return "", err
	}
	paths, overridden := h.taskPaths[pkg.TaskKey]
	if !overridden {
		switch pkg.TaskKey {
		case "implement-core":
			paths = []ports.ClearDevDiffPath{{Status: "M", Path: "src/email.js"}, {Status: "A", Path: "test/email.test.js"}}
		case "complete-summary", "final-summary": // The third serial task exercises durable runtime quotas.
			paths = []ports.ClearDevDiffPath{{Status: "M", Path: "src/summary.js"}, {Status: "A", Path: "test/summary.test.js"}}
		default:
			return "", fmt.Errorf("unexpected test task %q", pkg.TaskKey)
		}
	}
	h.gitMu.Lock()
	defer h.gitMu.Unlock()
	if h.seenTurns[clientID] {
		return turn, nil
	}
	state := h.workspaces[record.Metadata.WorkspacePath]
	if state == nil {
		return "", errors.New("test Builder workspace was not spawned")
	}
	if state.taskID != pkg.TaskID {
		state.base, state.taskID = state.head, pkg.TaskID
	}
	state.head, state.paths = coreDigest([]byte(clientID))[:40], paths
	h.seenTurns[clientID] = true
	return turn, nil
}

func (h *contractAgentHarness) InspectCandidate(_ context.Context, workspace, baseSHA string) (ports.ClearDevCandidateInspection, error) {
	h.gitMu.Lock()
	defer h.gitMu.Unlock()
	state := h.workspaces[workspace]
	if state == nil || (baseSHA != state.base && baseSHA != state.head) {
		return ports.ClearDevCandidateInspection{}, ports.ErrClearDevCandidateInvalid
	}
	paths := append([]ports.ClearDevDiffPath{}, state.paths...)
	if baseSHA == state.head {
		paths = []ports.ClearDevDiffPath{}
	}
	return ports.ClearDevCandidateInspection{BaseSHA: baseSHA, CandidateSHA: state.head, Paths: paths}, nil
}

func (h *contractAgentHarness) PrepareBaseWorkspace(_ context.Context, workspace, expectedHead, newBase string) error {
	h.gitMu.Lock()
	defer h.gitMu.Unlock()
	state := h.workspaces[workspace]
	if state == nil || state.head != expectedHead || newBase == "" {
		return ports.ErrClearDevCandidateInvalid
	}
	state.base, state.head, state.paths = newBase, newBase, nil
	return nil
}

func (h *contractAgentHarness) PrepareReviewBranch(_ context.Context, workspace, branch, candidate string) error {
	h.gitMu.Lock()
	defer h.gitMu.Unlock()
	state := h.workspaces[workspace]
	if state == nil || state.head != candidate {
		return ports.ErrClearDevCandidateInvalid
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reviewBranchCandidates[branch] = candidate
	return nil
}

func (h *contractAgentHarness) ComposeCandidates(_ context.Context, request ports.ClearDevComposeRequest) (ports.ClearDevComposeResult, error) {
	h.gitMu.Lock()
	defer h.gitMu.Unlock()
	if request.RequestID == "" {
		return ports.ClearDevComposeResult{}, ports.ErrClearDevCandidateInvalid
	}
	h.composeCalls++
	h.lastCompose = request // Assert production policy intent; Git composition remains a double.
	workspace := "/managed/contract-compose-" + request.RequestID
	sha := coreDigest([]byte("composed:" + request.RequestID))[:40]
	h.workspaces[workspace] = &contractFakeWorkspace{base: sha, head: sha}
	return ports.ClearDevComposeResult{WorkspacePath: workspace, OutputSHA: sha}, nil
}

func attachContractHarness(f *autoExecutionFixture, harness *contractAgentHarness) {
	f.service.finalReviews = f.store
	f.service.sessions = harness
	f.service.chat = harness
	f.service.inspector = harness
	f.service.checks = harness
}
