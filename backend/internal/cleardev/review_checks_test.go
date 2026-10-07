package cleardev

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestApprovedReviewCheckRequestIsBoundedAndStrict(t *testing.T) {
	valid := `{"schemaVersion":1,"kind":"REVIEW_CHECK_REQUEST","checkIds":["demo-frontend","demo-api"],"summary":"need evidence"}`
	result, err := ParseReviewCheckRequest([]byte(valid))
	if err != nil || !slices.Equal(result.CheckIDs, []string{"demo-api", "demo-frontend"}) {
		t.Fatalf("%+v %v", result, err)
	}
	for _, raw := range []string{
		strings.Replace(valid, "demo-frontend", "demo-api", 1),
		strings.Replace(valid, "demo-frontend", "demo-database", 1),
		strings.Replace(valid, "demo-api", "npm test; exit 0", 1),
		strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":2`, 1),
		strings.Replace(valid, `"summary":"need evidence"`, `"summary":"need evidence","argv":["true"]`, 1),
		strings.Replace(valid, `"summary":"need evidence"`, `"summary":"need evidence","candidateSha":"`+strings.Repeat("a", 40)+`"`, 1),
		strings.Replace(valid, `["demo-frontend","demo-api"]`, `[]`, 1), valid + ` {}`,
	} {
		if _, err := ParseReviewCheckRequest([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestReviewerCheckReportRejectsWrongIdentityAndFalsePass(t *testing.T) {
	r := ReviewCheckRequest{ReviewID: "review", CandidateSHA: strings.Repeat("a", 40), CheckIDs: []string{"demo-api"}}
	spec, _ := ApprovedReviewCheck("demo-api")
	fixture := ReviewCheckEvidence{ReviewID: r.ReviewID, CheckID: "demo-api", Outcome: "PASS", RecordedAt: time.Unix(1, 0), Proof: MailCheckProof{RunID: ReviewCheckRunID(r.ReviewID, "demo-api"), CandidateSHA: r.CandidateSHA, Argv: spec.Argv, Passed: true, SourceManifestID: strings.Repeat("b", 64), SourceTreeOID: strings.Repeat("c", 40), ImageID: "sha256:trusted", EnvironmentID: strings.Repeat("d", 64), OutputSHA256: strings.Repeat("e", 64)}}
	if _, pass, err := ReviewCheckReport(r, []ReviewCheckEvidence{fixture}); err != nil || !pass {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "sha", "run", "command", "exit", "timed", "truncated", "environment", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			raw, _ := json.Marshal(fixture)
			var e ReviewCheckEvidence
			_ = json.Unmarshal(raw, &e)
			results := []ReviewCheckEvidence{e}
			switch mode {
			case "missing":
				results = nil
			case "sha":
				results[0].Proof.CandidateSHA = strings.Repeat("f", 40)
			case "run":
				results[0].Proof.RunID = "another-run"
			case "command":
				results[0].Proof.Argv = []string{"sh", "-c", "true"}
			case "exit":
				results[0].Proof.ExitCode = 1
			case "timed":
				results[0].Proof.TimedOut = true
			case "truncated":
				results[0].Proof.Truncated = true
			case "environment":
				results[0].Proof.EnvironmentID = ""
			case "duplicate":
				results = append(results, e)
			}
			if _, _, err := ReviewCheckReport(r, results); err == nil {
				t.Fatal("accepted invalid report")
			}
		})
	}
	fixture.Outcome, fixture.Proof.Passed, fixture.Proof.ExitCode = "FAIL", false, 1
	if _, pass, err := ReviewCheckReport(r, []ReviewCheckEvidence{fixture}); err != nil || pass {
		t.Fatal("failed check was not reportable as failed")
	}
}
