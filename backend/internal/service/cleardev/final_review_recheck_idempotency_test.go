package cleardev

import (
	"context"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestFinalRecheckCompletedTransactionRemainsIdempotent(t *testing.T) {
	f, h := legacyFinalRecheckFixture(t)
	r := finalRecheckOffer(t, f)
	h.verdict = "PASS"
	if err := f.service.ApplyHumanDecisionResult(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	e := f.completed(t)
	command := core.CompleteComplexExecutionCommand{ExecutionRunID: e.Run.ID, Integration: *e.Integration, At: f.clock()}
	if err := f.store.CompleteClearDevComplexExecution(context.Background(), command); err != nil {
		t.Fatalf("completed recheck replay rejected: %v", err)
	}
	f.reopen(t)
	attachRequirementFinalReviewFixture(f, h)
	if err := f.store.CompleteClearDevComplexExecution(context.Background(), command); err != nil {
		t.Fatalf("completed recheck replay after SQLite reopen: %v", err)
	}
	wrong := command
	wrong.Integration.CandidateCommitSHA = forty("f")
	if err := f.store.CompleteClearDevComplexExecution(context.Background(), wrong); err == nil {
		t.Fatal("completed replay accepted another candidate")
	}
	if h.finalSends != 2 {
		t.Fatal("completed replay sent another review")
	}
}

func TestFinalRecheckDirectionAfterApprovalStillStops(t *testing.T) {
	f, h := legacyFinalRecheckFixture(t)
	r := finalRecheckOffer(t, f)
	h.verdict = "PASS"
	f.service.runBackground = func(func()) {}
	if err := f.service.ApplyHumanDecisionResult(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ProposeDirectionIntent(context.Background(), f.view.Requirement.ID, ProposeDirectionIntentInput{RequestID: "direction-after-native-recheck", DevelopmentRequirementID: f.view.Requirement.ID, Message: directionMessage}); err != nil {
		t.Fatal(err)
	}
	f.service.runBackground = func(fn func()) { fn() }
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.finalSends != 1 {
		t.Fatal("recheck sent after direction intent")
	}
	assertMailNotCompleted(t, f)
}
