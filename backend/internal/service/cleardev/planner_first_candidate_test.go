package cleardev

import (
	"context"
	"errors"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type firstCandidatePlannerHarness struct {
	*projectPlannerHarness
	freezes []ports.ClearDevMailFreezeRequest
}

func (h *firstCandidatePlannerHarness) RelayChatTurnWithID(ctx context.Context, id domain.SessionID, prompt, key string) (string, error) {
	if strings.Contains(prompt, "BUILDER_RESULT") && promptLineValue(prompt, "round=") == "0" {
		if prior := h.turnByClientMessageID[key]; prior != "" {
			return prior, nil
		}
		h.replies = append(h.replies, `{"schemaVersion":1,"kind":"BUILDER_RESULT","outcome":"BLOCKED","summary":"The project needs isolated trial preparation in its execution agreement before candidate handoff.","coordination":{"category":"ENGINEERING","summary":"Add isolated project trial preparation without changing the goal.","evidence":["The current runtime has no prepare-trial command; original browser acceptance remains required."],"affectedTaskKeys":["save-notes"]}}`)
		turn, err := h.productTestAgent.RelayChatTurnWithID(ctx, id, prompt, key)
		if err != nil {
			return turn, err
		}
		r, _, err := h.store.GetSession(ctx, id)
		if err != nil {
			return turn, err
		}
		r.Activity.State = domain.ActivityIdle
		return turn, h.store.UpdateSession(ctx, r)
	}
	return h.projectPlannerHarness.RelayChatTurnWithID(ctx, id, prompt, key)
}
func (h *firstCandidatePlannerHarness) FreezeMailCandidate(ctx context.Context, r ports.ClearDevMailFreezeRequest) (ports.ClearDevCandidateInspection, error) {
	h.freezes = append(h.freezes, r)
	return h.projectExecutionFlowHarness.FreezeMailCandidate(ctx, r)
}

// Production Service/SQLite; model, Git and checks are explicit doubles.
func TestProjectPlannerFirstCandidateAfterRevision(t *testing.T) {
	f, child, admission, preparer := projectCoordinationProjectPlan(t, false, false)
	base := attachProjectFlow(f, preparer)
	h := &firstCandidatePlannerHarness{projectPlannerHarness: &projectPlannerHarness{projectExecutionFlowHarness: base, requirementID: child.Requirement.ID}}
	h.finalVerdict = "PASS"
	f.s.chat, f.s.inspector = h, h
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	var x core.ComplexExecutionSnapshot
	checked := false
	for i := 0; i < 100; i++ {
		var err error
		x, _, err = f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		if x.Run.CompletedAt != nil {
			break
		}
		if !checked && x.PlannerRuntime != nil && len(x.PlannerRuntime.Amendments) > 0 {
			task := x.Tasks[0]
			d := core.ComplexExecutionDispatch{Round: task.ReworkCount, BaseCommitSHA: x.Run.InitialBaseCommitSHA}
			parent, err := complexExecutionDispatchParent(x, task, d)
			if err != nil || parent != d.BaseCommitSHA {
				t.Fatalf("first revised parent: %s %v", parent, err)
			}
			bad := d
			bad.BaseCommitSHA = forty("f")
			if _, err := complexExecutionDispatchParent(x, task, bad); err == nil {
				t.Fatal("wrong revised base accepted")
			}
			missing := x
			missing.PlannerRuntime = nil
			if _, err := complexExecutionDispatchParent(missing, task, d); err == nil {
				t.Fatal("missing saved amendment accepted")
			}
			// No generic fallback for later rounds lacking any candidate/recovery proof.
			d.Round++
			if _, err := complexExecutionDispatchParent(x, task, d); err == nil {
				t.Fatal("unproven later round accepted")
			}
			checked = true
		}
		_, _, err = f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID)
		if err != nil && !errors.Is(err, errComplexExecutionStopped) && !errors.Is(err, errComplexStopped) {
			t.Fatal(err)
		}
	}
	if !checked || x.Run.CompletedAt == nil || len(h.freezes) != 1 || h.freezes[0].ParentSHA != x.Run.InitialBaseCommitSHA || len(x.Dispatches) != 2 || len(x.CheckRuns) == 0 {
		t.Fatalf("first revised candidate did not complete: checked=%v phase=%v freezes=%+v dispatches=%+v planner=%+v", checked, x.Run.CompletedAt, h.freezes, x.Dispatches, x.PlannerRuntime)
	}
}
