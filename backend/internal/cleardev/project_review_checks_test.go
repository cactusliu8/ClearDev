package cleardev

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProjectReviewerCheckProtocolCannotUpgradeOrInjectCommands(t *testing.T) {
	run := projectExecutionRunFixture(t)
	contract, project, err := ProjectContractFromRun(run)
	if err != nil || !project || !ReviewCheckRequestsAvailable(run) {
		t.Fatal("fixture has no project admission")
	}
	id := contract.Basis.Checks[0].ID
	raw := fmt.Sprintf(`{"schemaVersion":2,"kind":"REVIEW_CHECK_REQUEST","checkIds":[%q],"summary":"Request the admitted behavioral check"}`, id)
	parsed, err := ParseExecutionReviewCheckRequest([]byte(raw), run)
	if err != nil || len(parsed.CheckIDs) != 1 || parsed.CheckIDs[0] != id {
		t.Fatalf("project request: %+v %v", parsed, err)
	}
	for _, invalid := range []string{
		strings.Replace(raw, `"schemaVersion":2`, `"schemaVersion":1`, 1),
		strings.Replace(raw, fmt.Sprintf("[%q]", id), `["demo-api"]`, 1),
		strings.Replace(raw, fmt.Sprintf("[%q]", id), fmt.Sprintf("[%q,%q]", id, id), 1),
		strings.TrimSuffix(raw, "}") + `,"argv":["npm","run","always-green"]}`,
		strings.TrimSuffix(raw, "}") + `,"projectExecution":{}}`,
		strings.TrimSuffix(raw, "}") + `,"verdict":"PASS"}`,
	} {
		if _, err := ParseExecutionReviewCheckRequest([]byte(invalid), run); err == nil {
			t.Fatalf("accepted unknown, duplicated or unapproved protocol: %s", invalid)
		}
	}
	if _, err := ParseReviewCheckRequest([]byte(raw)); err == nil {
		t.Fatal("legacy mail parser upgraded itself to accept project IDs")
	}
	if _, err := ParseExecutionReviewCheckRequest([]byte(raw), ComplexExecutionRun{}); err == nil {
		t.Fatal("an execution without admission acquired project checks")
	}
	request := ReviewCheckRequest{CheckIDs: []string{id}, ProjectExecution: &contract}
	if err := ValidateReviewCheckRequestForRun(request, run); err != nil {
		t.Fatal(err)
	}
	request.ReplacementRecoveryID = "project-recovery"
	request.RequestAttemptID = "project-review-attempt"
	request.RequestResultID = "project-review-result"
	if err := ValidateReviewCheckRequestForRun(request, run); err != nil {
		t.Fatalf("control-plane replacement provenance rejected: %v", err)
	}
	request.RequestResultID = ""
	if ValidateReviewCheckRequestForRun(request, run) == nil {
		t.Fatal("incomplete replacement provenance was accepted")
	}
	request.ReplacementRecoveryID, request.RequestAttemptID, request.RequestResultID = "", "", ""
	request.ProjectExecution = nil
	if ValidateReviewCheckRequestForRun(request, run) == nil {
		t.Fatal("project request lost its contract")
	}
	contract.RequestID = "changed-admission"
	request.ProjectExecution = &contract
	if ValidateReviewCheckRequestForRun(request, run) == nil {
		t.Fatal("request changed the exact admitted execution grant")
	}
}

func projectReviewerEvidenceFixture(t *testing.T) (ReviewCheckRequest, ReviewCheckEvidence) {
	t.Helper()
	contract, receipt := projectCheckReceiptFixture(t)
	request := ReviewCheckRequest{ReviewID: "review", CandidateID: "candidate", CandidateSHA: receipt.CandidateSHA,
		CheckIDs: []string{receipt.CheckID}, ProjectExecution: &contract, RequestedAt: time.Now().UTC()}
	receipt.CheckRunID = ReviewCheckRunID(request.ReviewID, receipt.CheckID)
	proof := MailCheckProof{RunID: receipt.CheckRunID, CandidateSHA: receipt.CandidateSHA, Argv: receipt.Argv,
		SourceManifestID: receipt.SourceManifestID, SourceTreeOID: receipt.SourceRootTreeOID, ImageID: receipt.ImageID,
		EnvironmentID: receipt.CheckEnvironmentID, OutputSHA256: receipt.OutputSHA256, Passed: true}
	return request, ReviewCheckEvidence{ReviewID: request.ReviewID, CheckID: receipt.CheckID, Outcome: "PASS", Proof: proof, ProjectReceipt: &receipt,
		Output: receipt.OutputSummary, RecordedAt: request.RequestedAt}
}

func TestProjectReviewerReceiptsCannotHideChangedOrUnexecutedEvidence(t *testing.T) {
	request, result := projectReviewerEvidenceFixture(t)
	if _, pass, err := ReviewCheckReport(request, []ReviewCheckEvidence{result}); err != nil || !pass {
		t.Fatalf("valid receipt: %v %v", pass, err)
	}
	for name, mutate := range map[string]func(*ReviewCheckEvidence){
		"missing-receipt":      func(e *ReviewCheckEvidence) { e.ProjectReceipt = nil },
		"other-candidate":      func(e *ReviewCheckEvidence) { e.ProjectReceipt.CandidateSHA = strings.Repeat("a", 40) },
		"other-contract":       func(e *ReviewCheckEvidence) { e.ProjectReceipt.ContractSHA256 = strings.Repeat("b", 64) },
		"other-command":        func(e *ReviewCheckEvidence) { e.ProjectReceipt.Argv = []string{"node", "empty-tests.js"} },
		"other-timeout":        func(e *ReviewCheckEvidence) { e.ProjectReceipt.TimeoutSeconds = 1 },
		"empty-output":         func(e *ReviewCheckEvidence) { e.Output = "all green" },
		"missing-dependencies": func(e *ReviewCheckEvidence) { e.ProjectReceipt.PackageLockSHA256 = "" },
		"false-pass":           func(e *ReviewCheckEvidence) { e.ProjectReceipt.ExitCode = 3 },
		"timeout-pass":         func(e *ReviewCheckEvidence) { e.ProjectReceipt.TimedOut = true },
		"truncated-pass":       func(e *ReviewCheckEvidence) { e.ProjectReceipt.OutputTruncated = true },
		"infra-pass":           func(e *ReviewCheckEvidence) { e.Outcome = "INFRA_ERROR" },
		"changed-summary":      func(e *ReviewCheckEvidence) { e.Proof.SourceManifestID = strings.Repeat("c", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			_, evidence := projectReviewerEvidenceFixture(t)
			mutate(&evidence)
			if ValidateReviewCheckEvidence(request, evidence) == nil {
				t.Fatal("forged or inconsistent Reviewer check was accepted")
			}
		})
	}
	for _, outcome := range []string{"FAIL", "TIMED_OUT", "INFRA_ERROR"} {
		_, evidence := projectReviewerEvidenceFixture(t)
		evidence.Outcome, evidence.Proof.Passed, evidence.Proof.ExitCode = outcome, false, 3
		if outcome == "INFRA_ERROR" {
			evidence.ProjectReceipt, evidence.Proof.ExitCode = nil, -1
		} else {
			evidence.ProjectReceipt.Outcome, evidence.ProjectReceipt.ExitCode = outcome, 3
			evidence.ProjectReceipt.TimedOut, evidence.Proof.TimedOut = outcome == "TIMED_OUT", outcome == "TIMED_OUT"
		}
		if _, pass, err := ReviewCheckReport(request, []ReviewCheckEvidence{evidence}); err != nil || pass {
			t.Fatalf("actual failed/unavailable check was lost or waived: outcome=%s pass=%v err=%v", outcome, pass, err)
		}
	}
	request.ProjectExecution = nil
	if ValidateReviewCheckEvidence(request, result) == nil {
		t.Fatal("historical mail policy accepted a project receipt")
	}
	old := ReviewCheckRequest{ReviewID: "old"}
	encoded, err := json.Marshal(old)
	if err != nil || strings.Contains(string(encoded), "projectExecution") {
		t.Fatal("adding project checks changed historical request serialization")
	}
}
