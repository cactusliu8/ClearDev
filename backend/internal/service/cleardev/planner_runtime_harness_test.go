package cleardev

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// This is an explicit deterministic provider/Git/check double. Only the
// control plane, SQLite transactions, role identity, message ledger, contract
// packets, reviews and completion are real here; it is NOT live-model proof.
type runtimeAgentHarness struct {
	*contractAgentHarness
	report        bool
	reportAll     bool
	category      string
	decision      string
	amendSource   bool
	runtimeTurns  int
	beforeRuntime func(context.Context, string) error
	afterRuntime  func(context.Context, string) error
}

const runtimeAddedCriterion = "The summary preserves the provider's stable first-occurrence order when normalized duplicates are removed."

func newRuntimeFixture(t *testing.T, scenario string) (*autoExecutionFixture, *runtimeAgentHarness) {
	t.Helper()
	f := newAutoExecutionFixture(t)
	enableContractFixture(f, scenario)
	h := &runtimeAgentHarness{contractAgentHarness: f.service.inspector.(*contractAgentHarness),
		report: true, category: "ENGINEERING", decision: core.PlannerRuntimeAmend}
	attachRuntimeHarness(f, h)
	return f, h
}

func attachRuntimeHarness(f *autoExecutionFixture, h *runtimeAgentHarness) {
	f.service.plannerTaskContracts = true
	f.service.plannerRuntimeCoordination = true
	attachContractHarness(f, h.contractAgentHarness)
	f.service.chat = h
}

func (h *runtimeAgentHarness) RelayChatTurnWithID(ctx context.Context, sessionID domain.SessionID, prompt, clientID string) (string, error) {
	runtimePrompt := strings.HasPrefix(prompt, "You are the SAME Stage Engineering Planner")
	if strings.Contains(prompt, parseCorrectionPromptPrefix) {
		h.mu.Lock()
		runtimePrompt = strings.HasPrefix(h.lastPromptBySession[sessionID], "You are the SAME Stage Engineering Planner")
		h.mu.Unlock()
	}
	if runtimePrompt {
		if h.beforeRuntime != nil {
			if err := h.beforeRuntime(ctx, clientID); err != nil {
				return "", err
			}
		}
		turn, err := h.runtimeTurn(sessionID, prompt, clientID)
		if err == nil && h.afterRuntime != nil {
			err = h.afterRuntime(ctx, clientID)
		}
		return turn, err
	}
	turn, err := h.contractAgentHarness.RelayChatTurnWithID(ctx, sessionID, prompt, clientID)
	if err != nil || !h.report || !strings.Contains(prompt, taskContractBuilderInstructions) {
		return turn, err
	}
	_, tail, ok := strings.Cut(prompt, "执行包：\n")
	if !ok {
		return "", fmt.Errorf("runtime test Builder has no task package")
	}
	var pkg core.ComplexStandardExecutionPackage
	if err := json.NewDecoder(strings.NewReader(tail)).Decode(&pkg); err != nil {
		return "", err
	}
	if pkg.TaskKey != "implement-core" && !h.reportAll {
		return turn, nil
	}
	affected := "complete-summary"
	if h.amendSource || h.reportAll {
		affected = pkg.TaskKey
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := h.snapshots[sessionID]
	for i := range snapshot.Messages {
		message := &snapshot.Messages[i]
		if message.TurnID != turn || message.Role != domain.MessageRoleAssistant {
			continue
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(message.Text), &result); err != nil {
			return "", err
		}
		result["coordination"] = core.PlannerCoordinationReport{Category: h.category,
			Summary:          "The provider's normalization preserves first-occurrence order; the remaining summary needs an explicit engineering expectation for duplicates.",
			Evidence:         []string{"Deterministic test provider observation: src/email.js preserves insertion order while eliminating normalized duplicates."},
			AffectedTaskKeys: []string{affected}}
		raw, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		message.Text = string(raw)
		for j := range h.relays {
			if h.relays[j].clientMessageID == clientID {
				h.relays[j].response = message.Text
			}
		}
	}
	h.snapshots[sessionID] = snapshot
	return turn, nil
}

func (h *runtimeAgentHarness) runtimeTurn(sessionID domain.SessionID, prompt, clientID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if prior := h.turnByClientMessageID[clientID]; prior != "" {
		return prior, nil
	}
	questions := []string{}
	amendments := []core.PlannerRemainingAmendment{}
	switch h.decision {
	case core.PlannerRuntimeAmend:
		key := "complete-summary"
		if h.amendSource {
			key = "implement-core"
		}
		amendments = append(amendments, core.PlannerRemainingAmendment{TaskKey: key, AdditionalReviewCriteria: []string{runtimeAddedCriterion}})
	case core.PlannerRuntimeProduct:
		questions = append(questions, "Which user-visible ordering should the product preserve?")
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "kind": core.PlannerRuntimeResultKind,
		"decision": h.decision, "summary": "The observed provider behavior is compatible with the fixed Stage goal; record only the safe engineering disposition.",
		"questions": questions, "amendments": amendments})
	if err != nil {
		return "", err
	}
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC).Add(time.Duration(len(h.relays)) * time.Second)
	turnID, messageID := fmt.Sprintf("runtime-turn-%03d", len(h.relays)+1), fmt.Sprintf("runtime-message-%03d", len(h.relays)+1)
	snapshot := h.snapshots[sessionID]
	snapshot.SessionID = sessionID
	snapshot.Turns = append(snapshot.Turns, domain.ConversationTurn{ID: turnID, HandledBySessionID: sessionID, State: domain.TurnStateCompleted, CompletedAt: &now})
	snapshot.Messages = append(snapshot.Messages,
		domain.ConversationMessage{ID: "user-" + messageID, TurnID: turnID, Sequence: int64(len(snapshot.Messages) + 1),
			Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: prompt, ClientMessageID: clientID},
		domain.ConversationMessage{ID: messageID, TurnID: turnID, Sequence: int64(len(snapshot.Messages) + 2),
			Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Text: string(raw)})
	h.snapshots[sessionID] = snapshot
	h.turnByClientMessageID[clientID], h.lastPromptBySession[sessionID] = turnID, prompt
	h.relays = append(h.relays, standardRelay{sessionID: sessionID, clientMessageID: clientID, prompt: prompt, response: string(raw)})
	h.runtimeTurns++
	return turnID, nil
}
