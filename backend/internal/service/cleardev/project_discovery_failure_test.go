package cleardev

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	sqlitedb "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// Real SQLite orchestration with explicit model/source test doubles. A rejected
// reply remains failed; a later user message consumes a new discussion round.
// No confirmation or execution is fabricated by this fixture.
func TestGenericProjectInvalidReplyAllowsNewDiscussionWithoutRewritingFailure(t *testing.T) {
	for _, option := range []string{"empty", "existing"} {
		t.Run(option, func(t *testing.T) {
			ctx := context.Background()
			f := newProjectPlanningFixture(t, strings.ToUpper(option))
			initial := f.create(t)
			var reply map[string]any
			if err := json.Unmarshal([]byte(genericProjectReply(option)), &reply); err != nil {
				t.Fatal(err)
			}
			reply["outcome"], reply["features"], reply["stages"] = "DISCUSS", []any{}, []any{}
			reply["questions"] = []any{map[string]string{"key": "next-goal", "question": "Which next user outcome?"}}
			invalid, err := json.Marshal(reply)
			if err != nil {
				t.Fatal(err)
			}
			reply["questions"] = []string{"Which next user outcome?"}
			invalidCorrection, err := json.Marshal(reply)
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range [][]byte{invalid, invalidCorrection} {
				if _, err := core.ParseProductDiscoveryResult(raw); err == nil {
					t.Fatal("the malformed question schema must remain rejected")
				}
			}
			f.h.replies = []string{string(invalid), string(invalidCorrection)}
			failed, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, projectChoice(initial, option))
			if err != nil || failed.Phase != "BLOCKED" || failed.Reason != "PRODUCT_DISCOVERY_INVALID" || !failed.CanDiscuss {
				t.Fatalf("invalid selected reply did not retain a new-discussion path: phase=%s reason=%s canDiscuss=%v err=%v", failed.Phase, failed.Reason, failed.CanDiscuss, err)
			}
			last := failed.Discussions[len(failed.Discussions)-1]
			if last.Result != nil || last.SettledAt == nil || last.FailureReason != "PRODUCT_DISCOVERY_INVALID" || failed.Selection == nil || !failed.SourceCurrent {
				t.Fatal("failure was not settled separately from the saved source choice")
			}
			calls := len(f.h.relays)
			if calls != 3 {
				t.Fatalf("expected initial reply, failed reply and one failed correction; calls=%d", calls)
			}
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			f.store, err = sqlitedb.Open(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.store.Close() })
			f.service()
			if err := f.s.ResumeComplexFlows(ctx); err != nil || len(f.h.relays) != calls {
				t.Fatalf("restart resent the failed discussion: %v", err)
			}
			input := ProductDiscussionInput{RequestID: "new-goal-after-invalid", ExpectedPreviousID: last.ID, Message: "Keep the selected project and plan the next observable user outcome."}
			f.h.source.BaseCommitSHA = forty("b")
			if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err == nil || len(f.h.relays) != calls {
				t.Fatal("new discussion silently accepted a changed inherited source")
			}
			f.h.source.BaseCommitSHA = forty("a")
			f.h.replies = []string{genericProjectReply(option)}
			next, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input)
			if err != nil || next.Phase != "READY" || next.Goal.ID != failed.Goal.ID || !reflect.DeepEqual(next.Selection, failed.Selection) {
				t.Fatalf("new user round did not retain the same product and source: phase=%s reason=%s err=%v", next.Phase, next.Reason, err)
			}
			if len(f.h.relays) != calls+1 || next.RemainingDiscussions != failed.RemainingDiscussions-1 || len(next.Discussions) != len(failed.Discussions)+1 || !reflect.DeepEqual(next.Discussions[:len(failed.Discussions)], failed.Discussions) {
				t.Fatal("new round rewrote failure history, reset budget, or duplicated a model turn")
			}
			if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err != nil || len(f.h.relays) != calls+1 {
				t.Fatalf("exact new-round replay resent the model: %v", err)
			}
			input.Message = "A different input must not reuse the request ID."
			if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err == nil {
				t.Fatal("replayed request changed the user message")
			}
			input.RequestID = "stale-previous"
			if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err == nil || len(f.h.relays) != calls+1 {
				t.Fatal("an old previous-discussion ID appended another turn")
			}
			for _, stage := range next.Stages {
				if stage.Stage.DevelopmentRequirementID != "" {
					t.Fatal("a new discussion implicitly prepared or approved a specification")
				}
			}
			assertNoDevelopmentWork(t, f.store, f.h.standardAgentHarness, next.Goal.ID)
		})
	}
}
