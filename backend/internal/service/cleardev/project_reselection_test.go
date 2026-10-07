package cleardev

import (
	"context"
	"testing"

	sqlitedb "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestGenericProjectInterruptedDiscussionAllowsExplicitReselection(t *testing.T) {
	for _, when := range []string{"before-send", "after-reply"} {
		t.Run(when, func(t *testing.T) {
			ctx := context.Background()
			f := newProjectPlanningFixture(t, "EXISTING")
			initial := f.create(t)
			var resume func()
			if when == "before-send" {
				f.s.runBackground = func(run func()) { resume = run }
			} else {
				f.h.afterRelay = func() { f.h.source.BaseCommitSHA = forty("b") }
			}
			f.h.replies = append(f.h.replies, genericProjectReply("existing"))
			chosen, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, projectChoice(initial, "existing"))
			if err != nil {
				t.Fatal(err)
			}
			if when == "before-send" {
				f.h.source.BaseCommitSHA = forty("b")
				if resume == nil {
					t.Fatal("pending discussion did not schedule work")
				}
				resume()
				f.s.runBackground = func(run func()) { run() }
			}
			f.h.afterRelay = nil
			interrupted, err := f.s.GetProductGoal(ctx, chosen.Goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			last := interrupted.Discussions[len(interrupted.Discussions)-1]
			if last.SettledAt == nil || last.FailureReason == "" || last.Result != nil || !interrupted.CanDiscuss {
				t.Fatalf("source drift left no recoverable failed discussion: %+v canDiscuss=%v", last, interrupted.CanDiscuss)
			}
			calls := len(f.h.relays)
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
				t.Fatalf("restart resent an interrupted discussion: %v", err)
			}
			_, err = f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, ProductDiscussionInput{RequestID: "silent-recovery", ExpectedPreviousID: last.ID, Message: "continue"})
			if err == nil {
				t.Fatal("interrupted discussion silently inherited an obsolete choice")
			}
			input := projectChoice(initial, "existing")
			input.RequestID, input.ExpectedPreviousID = "explicit-reselection", last.ID
			input.Choice.ExpectedBaseCommitSHA = forty("b")
			input.Choice.Reason = "Explicitly use the moved repository without restoring the old source."
			f.h.replies = []string{genericProjectReply("existing")}
			recovered, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input)
			if err != nil || recovered.Phase != "READY" || recovered.Selection == nil || recovered.Selection.BaseCommitSHA != forty("b") || !recovered.SourceCurrent {
				t.Fatalf("explicit reselection did not recover: %+v err=%v", recovered, err)
			}
			if recovered.Discussions[1].FailureReason != last.FailureReason || recovered.Discussions[1].Result != nil || len(f.h.relays) != calls+1 {
				t.Fatal("reselection rewrote failure history or duplicated the model call")
			}
			if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err != nil || len(f.h.relays) != calls+1 {
				t.Fatalf("exact recovered choice replay failed: %v", err)
			}
			input.Choice = nil
			if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err == nil {
				t.Fatal("replay dropped the explicit recovery choice")
			}
		})
	}
}
