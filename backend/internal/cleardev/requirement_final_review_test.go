package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func finalReviewCoreFixture(t *testing.T) (ComplexExecutionRun, RequirementFinalReview) {
	t.Helper()
	text, planJSON := "MUST R1: preserve existing behavior; acceptance A1: passing integration checks.", `{"tasks":[{"key":"task-1"}]}`
	run := ComplexExecutionRun{ID: "execution-1", DevelopmentRequirementID: "requirement-1", RequirementVersionID: "version-1", RequirementSHA256: sha256Hex([]byte(text)), PlanID: "plan-1", PlanSHA256: sha256Hex([]byte(planJSON)), Mode: WorkModeStandard, TaskSetVersion: ComplexStandardTaskSetVersion}
	raw, _, err := BuildComplexExecutionRunPackage(run.Mode, run.ID, run.RequirementVersionID, run.RequirementSHA256, run.PlanID, run.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	raw, digest, err := BindRequirementFinalReviewPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	run.ExecutionPackageJSON, run.ExecutionPackageSHA256 = string(raw), digest
	review := RequirementFinalReview{
		ID: "final-review-1", ExecutionRunID: run.ID, DevelopmentRequirementID: run.DevelopmentRequirementID,
		RequirementVersionID: run.RequirementVersionID, RequirementSHA256: run.RequirementSHA256,
		PlanID: run.PlanID, PlanSHA256: run.PlanSHA256, BaseCommitSHA: strings.Repeat("a", 40), CandidateCommitSHA: strings.Repeat("b", 40),
		CheckRunIDs: []string{"final-check-1"}, Status: "SETTLED", Verdict: "PASS", ResultID: "provider-result-1", AOSessionID: "independent-final-session",
	}
	packet := RequirementFinalReviewPacket{
		SchemaVersion: 1, ReviewID: review.ID, Run: run, CandidateCommitSHA: review.CandidateCommitSHA, BaseCommitSHA: review.BaseCommitSHA, CheckRunIDs: review.CheckRunIDs,
		Requirement: RequirementVersion{ID: run.RequirementVersionID, DevelopmentRequirementID: run.DevelopmentRequirementID, RequirementText: text, SHA256: run.RequirementSHA256, Status: RequirementVersionStatusConfirmed, TaskSetVersion: run.TaskSetVersion},
		Plan:        ComplexEngineeringPlan{ID: run.PlanID, DevelopmentRequirementID: run.DevelopmentRequirementID, RequirementVersionID: run.RequirementVersionID, RequirementSHA256: run.RequirementSHA256, PlanJSON: planJSON, PlanSHA256: run.PlanSHA256},
	}
	raw, err = json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	review.ReviewPacketJSON, review.ReviewPacketSHA256 = string(raw), sha256Hex(raw)
	return run, review
}

func TestRequirementFinalReviewCoreExactBindings(t *testing.T) {
	run, review := finalReviewCoreFixture(t)
	if err := ValidateRequirementFinalReviewBinding(review, run, review.CandidateCommitSHA, review.CheckRunIDs); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RequirementFinalReview){
		"execution":        func(r *RequirementFinalReview) { r.ExecutionRunID = "other-execution" },
		"requirement":      func(r *RequirementFinalReview) { r.DevelopmentRequirementID = "other-requirement" },
		"version":          func(r *RequirementFinalReview) { r.RequirementVersionID = "other-version" },
		"requirement-hash": func(r *RequirementFinalReview) { r.RequirementSHA256 = strings.Repeat("a", 64) },
		"plan":             func(r *RequirementFinalReview) { r.PlanID = "other-plan" },
		"plan-hash":        func(r *RequirementFinalReview) { r.PlanSHA256 = strings.Repeat("b", 64) },
		"candidate":        func(r *RequirementFinalReview) { r.CandidateCommitSHA = strings.Repeat("c", 40) },
		"checks":           func(r *RequirementFinalReview) { r.CheckRunIDs = []string{"other-check"} },
		"packet-hash":      func(r *RequirementFinalReview) { r.ReviewPacketSHA256 = strings.Repeat("c", 64) },
		"packet-extra-field": func(r *RequirementFinalReview) {
			r.ReviewPacketJSON = strings.TrimSuffix(r.ReviewPacketJSON, "}") + `,"untrustedExtra":"omit acceptance"}`
			r.ReviewPacketSHA256 = sha256Hex([]byte(r.ReviewPacketJSON))
		},
	} {
		t.Run(name, func(t *testing.T) {
			wrong := review
			mutate(&wrong)
			if err := ValidateRequirementFinalReviewBinding(wrong, run, review.CandidateCommitSHA, review.CheckRunIDs); err == nil {
				t.Fatal("wrong final review binding was accepted")
			}
		})
	}
	if err := ValidateRequirementFinalReviewBinding(review, run, strings.Repeat("d", 40), review.CheckRunIDs); err == nil {
		t.Fatal("old final review applied to a changed integration SHA")
	}
	if err := ValidateRequirementFinalReviewBinding(review, run, review.CandidateCommitSHA, []string{"final-check-1", "final-check-1"}); err == nil {
		t.Fatal("duplicate integration checks were accepted")
	}
}

func TestRequirementFinalReviewCorePolicyAndProgress(t *testing.T) {
	run, review := finalReviewCoreFixture(t)
	if required, err := RequirementFinalReviewRequired(run); err != nil || !required {
		t.Fatalf("new policy not required: required=%v err=%v", required, err)
	}
	legacy := run
	raw, digest, err := BuildComplexExecutionRunPackage(run.Mode, run.ID, run.RequirementVersionID, run.RequirementSHA256, run.PlanID, run.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	legacy.ExecutionPackageJSON, legacy.ExecutionPackageSHA256 = string(raw), digest
	if required, err := RequirementFinalReviewRequired(legacy); err != nil || required {
		t.Fatal("historical run was silently upgraded to a new contract")
	}
	corrupt := run
	corrupt.ExecutionPackageSHA256 = strings.Repeat("f", 64)
	if _, err := RequirementFinalReviewRequired(corrupt); err == nil {
		t.Fatal("changed final-review envelope accepted")
	}
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	run.CompletedAt = &now
	snapshot := ComplexExecutionSnapshot{Run: run, Integration: &ComplexExecutionIntegration{ExecutionRunID: run.ID, CandidateCommitSHA: review.CandidateCommitSHA, CheckRunIDs: review.CheckRunIDs}}
	if phase, _ := DeriveComplexExecutionPhase(snapshot); phase == ComplexExecutionCompleted {
		t.Fatal("read model called a marked run completed without final evidence")
	}
	snapshot.FinalReview = &review
	if phase, _ := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionCompleted {
		t.Fatalf("exact final PASS not recognized: %s", phase)
	}
	review.PlanID = "wrong-plan"
	if phase, _ := DeriveComplexExecutionPhase(snapshot); phase == ComplexExecutionCompleted {
		t.Fatal("read model accepted a foreign plan's final PASS")
	}
	snapshot.Run = legacy
	snapshot.Run.CompletedAt = &now
	snapshot.FinalReview = nil
	if phase, _ := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionCompleted {
		t.Fatal("historical completion was reinterpreted under the new contract")
	}
}

func TestRequirementFinalReviewResultProtocol(t *testing.T) {
	valid := `{"schemaVersion":1,"kind":"REQUIREMENT_FINAL_REVIEW_RESULT","verdict":"PASS","summary":"whole requirement","acceptanceSummary":"R1 and A1","consistencySummary":"consistent","scopeSummary":"approved paths","regressionSummary":"old tests preserved","evidenceSummary":"same final SHA"}`
	if result, err := ParseRequirementFinalReviewResult([]byte(valid)); err != nil || result.Verdict != "PASS" {
		t.Fatalf("valid final protocol failed: result=%+v err=%v", result, err)
	}
	for _, raw := range []string{
		strings.Replace(valid, "REQUIREMENT_FINAL_REVIEW_RESULT", "LOCAL_REVIEW", 1),
		strings.Replace(valid, `"PASS"`, `"REPLAN"`, 1),
		strings.Replace(valid, `"R1 and A1"`, `""`, 1),
		strings.Replace(valid, `"R1 and A1"`, `null`, 1),
		strings.TrimSuffix(valid, "}") + `,"candidateCommitSha":"` + strings.Repeat("b", 40) + `"}`,
		strings.TrimSuffix(valid, "}") + `,"verdict":"PASS"}`,
	} {
		if _, err := ParseRequirementFinalReviewResult([]byte(raw)); err == nil {
			t.Fatalf("invalid final result accepted: %s", raw)
		}
	}
}
