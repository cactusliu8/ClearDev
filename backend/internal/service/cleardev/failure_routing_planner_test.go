package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type unifiedPlannerHarness struct {
	*unifiedHandoffHarness
	requirementID string
	plannerTurns  int
	decision      string
	blockFirst    bool
	changeSource  bool
}

func (h *unifiedPlannerHarness) RelayChatTurnWithID(ctx context.Context, sid domain.SessionID, prompt, key string) (string, error) {
	if !strings.HasPrefix(prompt, "你是本阶段原工程 Planner") {
		turn, err := h.projectExecutionFlowHarness.RelayChatTurnWithID(ctx, sid, prompt, key)
		if err == nil && h.blockFirst && strings.Contains(prompt, `"kind":"BUILDER_RESULT"`) {
			h.blockFirst = false
			h.currentCandidate = ""
			snapshot := h.snapshots[sid]
			for i := range snapshot.Messages {
				m := &snapshot.Messages[i]
				if m.TurnID == turn && m.Role == domain.MessageRoleAssistant {
					m.Text = `{"schemaVersion":1,"kind":"BUILDER_RESULT","outcome":"BLOCKED","summary":"The original implementation needs an engineering diagnosis. Preserve its paths and checks."}`
				}
			}
			h.snapshots[sid] = snapshot
		}
		return turn, err
	}
	if prior := h.turnByClientMessageID[key]; prior != "" {
		return prior, nil
	}
	x, _, err := h.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		return "", err
	}
	amendments := []core.PlannerRemainingAmendment{}
	decision := h.decision
	if decision == core.PlannerRuntimeAmend {
		amendments = append(amendments, core.PlannerRemainingAmendment{TaskKey: x.Tasks[0].TaskKey, AdditionalReviewCriteria: []string{"Reconcile the original implementation before candidate handoff, preserving all approved checks."}})
	}
	if decision == "EXPAND_PATH" {
		decision = core.PlannerRuntimeAmend
		contract, _, err := core.ProjectContractFromRun(x.Run)
		if err != nil {
			return "", err
		}
		basis := contract.Basis
		basis.WritePaths = append(append([]string{}, basis.WritePaths...), "unauthorized-secret/**")
		amendments = append(amendments, core.PlannerRemainingAmendment{TaskKey: x.Tasks[0].TaskKey, ExecutionBasis: &basis, AdditionalReviewCriteria: []string{}})
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "kind": "PLANNER_RUNTIME_COORDINATION", "decision": decision, "summary": "Original Planner diagnosed the exact failure; no extra permission is granted.", "questions": []string{}, "amendments": amendments})
	if err != nil {
		return "", err
	}
	h.replies = append(h.replies, string(raw))
	turn, err := h.productTestAgent.RelayChatTurnWithID(ctx, sid, prompt, key)
	if err != nil {
		return turn, err
	}
	record, _, err := h.store.GetSession(ctx, sid)
	if err != nil {
		return turn, err
	}
	record.Activity.State = domain.ActivityIdle
	h.lastPromptBySession[sid] = prompt
	h.plannerTurns++
	if h.decision == core.PlannerRuntimeContinue || h.decision == core.PlannerRuntimeAmend {
		h.reject = false
	}
	if h.changeSource {
		h.digest = strings.Repeat("c", 64)
	}
	return turn, h.store.UpdateSession(ctx, record)
}

func driveUnifiedPlanner(t *testing.T, f *projectPlanningFixture, h *unifiedPlannerHarness) (core.ComplexExecutionSnapshot, error) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		x, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
		if err != nil {
			return x, err
		}
		if x.Run.CompletedAt != nil {
			return x, nil
		}
		progressed, stopped, err := f.s.advanceComplexStandardExecution(ctx, h.requirementID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			return x, err
		}
		if stopped && !progressed && h.plannerTurns > 0 {
			return x, nil
		}
	}
	x, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	return x, err
}

func TestUnifiedFailureRepeatedHandoffEscalatesThenResumesWithinOriginalBudget(t *testing.T) {
	for _, decision := range []string{core.PlannerRuntimeContinue, core.PlannerRuntimeAmend} {
		t.Run(decision, func(t *testing.T) {
			f, base, id, before := unifiedHandoffFixture(t, "NO_IMPLEMENTATION_CHANGE")
			base.candidateSHAs = append(base.candidateSHAs, forty("d"))
			h := &unifiedPlannerHarness{unifiedHandoffHarness: base, requirementID: id, decision: decision}
			f.s.chat = h
			after, err := driveUnifiedPlanner(t, f, h)
			if err != nil {
				t.Fatal(err)
			}
			if after.Run.CompletedAt == nil || h.plannerTurns != 1 || len(after.Dispatches) != 3 || len(after.WorkflowRecoveries) != 2 {
				t.Fatalf("failure chain did not complete: phase=%v planner=%d recoveries=%d dispatches=%d", after.Run.CompletedAt, h.plannerTurns, len(after.WorkflowRecoveries), len(after.Dispatches))
			}
			if !reflect.DeepEqual(before.Dispatches[0], after.Dispatches[0]) || after.Tasks[0].ReworkCount != 2 {
				t.Fatal("original failure or budget was rewritten")
			}
			if len(after.PlannerRuntime.Events) != 1 || after.FinalReview == nil || after.FinalReview.Verdict != "PASS" {
				t.Fatal("coordination substituted for final verification")
			}
			source, found, err := f.store.GetClearDevFailureCoordinationSource(context.Background(), after.PlannerRuntime.Events[0].ID)
			if err != nil || !found || source.DispatchID != after.Dispatches[1].ID || source.WorkingTreeSHA256 != base.digest {
				t.Fatal("missing exact escalation source", err)
			}
			var reported map[string]any
			for _, step := range after.AgentSteps {
				if step.ID == source.StepID {
					if err := json.Unmarshal([]byte(step.FinalMessageText), &reported); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, exists := reported["coordination"]; exists {
				t.Fatal("control-derived evidence was forged into the old Builder report")
			}
		})
	}
}

func TestUnifiedFailurePlannerStopPathsAndChangedWorkRemainHuman(t *testing.T) {
	for _, mode := range []string{core.PlannerRuntimeStop, "EXPAND_PATH", "SOURCE_CHANGED"} {
		t.Run(mode, func(t *testing.T) {
			f, base, id, _ := unifiedHandoffFixture(t, "NO_IMPLEMENTATION_CHANGE")
			h := &unifiedPlannerHarness{unifiedHandoffHarness: base, requirementID: id, decision: mode}
			if mode == "SOURCE_CHANGED" {
				h.decision = core.PlannerRuntimeContinue
				h.changeSource = true
			}
			f.s.chat = h
			after, err := driveUnifiedPlanner(t, f, h)
			if err != nil {
				t.Fatal(err)
			}
			if h.plannerTurns != 1 || after.Run.CompletedAt != nil || len(after.Dispatches) != 2 || len(after.WorkflowRecoveries) != 1 || len(after.PlannerRuntime.Amendments) != 0 {
				t.Fatal("unsafe coordination continued or changed authority")
			}
			calls := len(h.relays)
			for range 3 {
				_, _, _ = f.s.advanceComplexStandardExecution(context.Background(), id)
			}
			if calls != len(h.relays) || h.plannerTurns != 1 {
				t.Fatal("stopped Planner was called repeatedly")
			}
		})
	}
}

func TestUnifiedFailureOriginalBuilderBlockedEscalatesWithoutFakeCandidate(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	base := &unifiedHandoffHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), digest: strings.Repeat("b", 64)}
	h := &unifiedPlannerHarness{unifiedHandoffHarness: base, requirementID: child.Requirement.ID, decision: core.PlannerRuntimeContinue, blockFirst: true}
	f.s.chat = h
	f.s.inspector = h
	f.s.automaticFailureRouting = true
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	after, err := driveUnifiedPlanner(t, f, h)
	if err != nil {
		t.Fatal(err)
	}
	if after.Run.CompletedAt == nil || h.plannerTurns != 1 || len(after.Dispatches) != 2 || after.Dispatches[0].CandidateCommitID != "" || after.Dispatches[0].ReasonCode != "BUILDER_BLOCKED" {
		t.Fatal("original BLOCKED did not return through Planner and original Builder", after.Dispatches)
	}
}
