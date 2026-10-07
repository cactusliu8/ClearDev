package cleardev

import (
	"encoding/json"
	"testing"
)

func finalReviewCounterFixture(t *testing.T) ComplexExecutionSnapshot {
	t.Helper()
	run, review := finalReviewCoreFixture(t)
	run.FixedBuilderCount = 1
	contract := projectExecutionContractFixture(t)
	contract.ExecutionRunID, contract.RequirementVersionID = run.ID, run.RequirementVersionID
	contract.RequirementSHA256, contract.PlanID, contract.PlanSHA256 = run.RequirementSHA256, run.PlanID, run.PlanSHA256
	raw, digest, err := BindProjectExecutionPolicy([]byte(run.ExecutionPackageJSON), contract)
	if err != nil {
		t.Fatal(err)
	}
	run.ExecutionPackageJSON, run.ExecutionPackageSHA256 = string(raw), digest
	var packet RequirementFinalReviewPacket
	if err = json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		t.Fatal(err)
	}
	task := ComplexExecutionTask{ID: "task", CurrentRound: 8, ReworkCount: 9}
	packet.Run = run
	packet.PlannerRuntime = &PlannerRuntimeSnapshot{}
	packet.Tasks = []ComplexExecutionTask{task}
	packet.Verifications = []ComplexExecutionVerification{{ComplexExecutionTaskID: task.ID, Round: 8, CandidateCommitSHA: review.CandidateCommitSHA}}
	raw, err = json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	review.ReviewPacketJSON, review.ReviewPacketSHA256 = string(raw), sha256Hex(raw)
	review.Status, review.Verdict, review.ReasonCode = "SETTLED", "BLOCKED", "REQUIREMENT_FINAL_REVIEW_BLOCKED"
	if err = ValidateRequirementFinalReviewBinding(review, run, review.CandidateCommitSHA, review.CheckRunIDs); err != nil {
		t.Fatal(err)
	}
	if !BuilderFirstFailureEnabled(run) {
		t.Fatal("fixture lacks project recovery policy")
	}
	return ComplexExecutionSnapshot{Run: run, FinalReview: &review, Tasks: []ComplexExecutionTask{task}}
}

func TestFinalReviewCounterHistoricalDifferenceDoesNotHideRecovery(t *testing.T) {
	s := finalReviewCounterFixture(t)
	if FinalReviewTaskReturned(s, s.Tasks[0]) {
		t.Fatal("old 9/8 difference falsely became a new return")
	}
	if _, _, stopped := requirementFinalReviewStop(s); !stopped {
		t.Fatal("historical counters hid the settled final review stop")
	}
	options := WorkflowRecoveryOptions(s)
	if len(options) != 1 || options[0].Action != RecoveryRetryStage || options[0].TargetID != s.FinalReview.ID {
		t.Fatalf("lost original stage recovery: %+v", options)
	}
}

func TestFinalReviewCounterNewReturnStillClosesOldRecovery(t *testing.T) {
	s := finalReviewCounterFixture(t)
	s.Tasks[0].ReworkCount++
	if !FinalReviewTaskReturned(s, s.Tasks[0]) {
		t.Fatal("real return after frozen review ignored")
	}
	if len(WorkflowRecoveryOptions(s)) != 0 {
		t.Fatal("old stage recovery remained open after new return")
	}
}

func TestFinalReviewCounterChangedEvidenceCannotCreateReturn(t *testing.T) {
	s := finalReviewCounterFixture(t)
	s.Tasks[0].ReworkCount++
	s.FinalReview.CandidateCommitSHA = "wrong-candidate"
	if FinalReviewTaskReturned(s, s.Tasks[0]) {
		t.Fatal("invalid review created a return")
	}
}
