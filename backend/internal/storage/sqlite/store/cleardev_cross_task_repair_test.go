package store

import (
	"encoding/json"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func TestCrossTaskRepairContextOnlyIgnoresItsOwnRecordedStop(t *testing.T) {
	original := map[string]any{"priorDecisions": []core.PlannerCoordinationDecision{}, "taskHead": "original", "budget": 2}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	request := gen.CleardevPlannerRuntimeRequest{ContextJson: string(raw), ContextSha256: complexExecutionRawDigest(raw)}
	stop := core.PlannerCoordinationDecision{EventID: "event", Source: "CONTROL_PLANE", Outcome: "STOP"}
	original["priorDecisions"] = []core.PlannerCoordinationDecision{stop}
	current, _ := json.Marshal(original)
	if !crossTaskRepairContextMatches(request, current, "event") {
		t.Fatal("own settled refusal incorrectly invalidates the original request")
	}
	for _, mode := range []string{"foreign-stop", "changed-head", "budget-change", "corrupt-request", "invalid-current"} {
		t.Run(mode, func(t *testing.T) {
			facts := map[string]any{"priorDecisions": []core.PlannerCoordinationDecision{stop}, "taskHead": "original", "budget": 2}
			r := request
			switch mode {
			case "foreign-stop":
				other := stop
				other.EventID = "another-event"
				facts["priorDecisions"] = []core.PlannerCoordinationDecision{stop, other}
			case "changed-head":
				facts["taskHead"] = "changed"
			case "budget-change":
				facts["budget"] = 3
			case "corrupt-request":
				r.ContextSha256 = "wrong"
			}
			body, _ := json.Marshal(facts)
			if mode == "invalid-current" {
				body = []byte("not-json")
			}
			if crossTaskRepairContextMatches(r, body, "event") {
				t.Fatal("foreign or changed facts became a current source")
			}
		})
	}
}
