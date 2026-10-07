package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseEngineeringPlanResultAcceptsOnlyFrozenTask(t *testing.T) {
	result := EngineeringPlanResult{
		SchemaVersion: 1, Kind: "ENGINEERING_PLAN", PlanningRequestID: "planning-1",
		RequirementVersionID: "version-1", RequirementSHA256: strings.Repeat("a", 64),
		BaseTaskSetVersion: 0, Mode: "STANDARD", Task: FrozenStandardTaskPlan(),
	}
	raw, err := MarshalAgentChosenResult(result)
	if err != nil {
		t.Fatal(err)
	}
	parsed, normalized, digest, err := ParseEngineeringPlanResult(raw, "planning-1", "version-1", strings.Repeat("a", 64), 0)
	if err != nil {
		t.Fatalf("ParseEngineeringPlanResult: %v", err)
	}
	if parsed.PlanningRequestID != "planning-1" || parsed.RequirementSHA256 != strings.Repeat("a", 64) ||
		parsed.Task.Title != StandardTaskTitle || len(normalized) == 0 || len(digest) != 64 {
		t.Fatalf("parsed = %#v, normalized=%q digest=%q", parsed, normalized, digest)
	}

	result.Task.WritePaths = append(result.Task.WritePaths, "src/extra.js")
	raw, _ = MarshalAgentChosenResult(result)
	if _, _, _, err := ParseEngineeringPlanResult(raw, "planning-1", "version-1", strings.Repeat("a", 64), 0); err == nil || !strings.Contains(err.Error(), "PLAN_OUT_OF_SCOPE") {
		t.Fatalf("out-of-scope plan error = %v", err)
	}
}

func TestStrictAgentResultRejectsDuplicateUnknownNullAndTrailingJSON(t *testing.T) {
	cases := []string{
		`{"schemaVersion":1,"schemaVersion":1,"kind":"REQUEST_PLANNING","decision":"PLAN"}`,
		`{"schemaVersion":1,"kind":"REQUEST_PLANNING","decision":"PLAN","extra":true}`,
		`{"schemaVersion":1,"kind":"REQUEST_PLANNING","decision":null}`,
		`{"schemaVersion":1,"kind":"REQUEST_PLANNING","decision":"PLAN"}{}`,
		`{"schemaVersion":1,"kind":"REQUEST_PLANNING","planningRequestId":"p","requirementVersionId":"v","requirementSha256":"s","decision":"PLAN"}`,
	}
	for _, raw := range cases {
		if _, err := ParsePlanningResult([]byte(raw), "p", "v", "s"); err == nil {
			t.Fatalf("ParsePlanningResult(%s) unexpectedly passed", raw)
		}
	}
}

func TestStrictAgentResultRejectsMissingAgentChosenFields(t *testing.T) {
	plan := EngineeringPlanResult{
		SchemaVersion: 1, Kind: "ENGINEERING_PLAN", Mode: "STANDARD", Task: FrozenStandardTaskPlan(),
	}
	raw, err := MarshalAgentChosenResult(plan)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "task")
	raw, _ = json.Marshal(fields)
	if _, _, _, err := ParseEngineeringPlanResult(raw, "planning-1", "version-1", strings.Repeat("a", 64), 0); err == nil {
		t.Fatal("plan without task unexpectedly passed")
	}

	review := `{"schemaVersion":1,"kind":"LOCAL_REVIEW","verdict":"PASS","reasonCode":"REVIEW_PASSED","summary":"ok"}`
	if _, err := ParseLocalReviewResult([]byte(review), "review-1", "candidate-1", strings.Repeat("b", 40), strings.Repeat("c", 64), []string{"src/email.js"}); err == nil {
		t.Fatal("local review without findings unexpectedly passed")
	}
}

func TestParseLocalReviewResultBindsVerdictAndDiffPaths(t *testing.T) {
	result := LocalReviewResult{
		SchemaVersion: 1, Kind: "LOCAL_REVIEW", ReviewAssignmentID: "review-1",
		CandidateID: "candidate-1", CandidateSHA: strings.Repeat("b", 40),
		ReviewPacketSHA256: strings.Repeat("c", 64), Verdict: "REWORK",
		ReasonCode: "REVIEW_CHANGES_REQUIRED", Summary: "needs a fix",
		Findings: []ReviewFinding{{Severity: "BLOCKING", Path: "src/email.js", Message: "domain is not lowercased"}},
	}
	raw, err := MarshalAgentChosenResult(result)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseLocalReviewResult(raw, "review-1", "candidate-1", strings.Repeat("b", 40), strings.Repeat("c", 64), []string{"src/email.js"})
	if err != nil {
		t.Fatalf("ParseLocalReviewResult: %v", err)
	}
	if got.CandidateSHA != strings.Repeat("b", 40) || got.ReviewPacketSHA256 != strings.Repeat("c", 64) {
		t.Fatalf("bound review facts = %#v", got)
	}
	if _, err := ParseLocalReviewResult(raw, "review-1", "candidate-1", strings.Repeat("b", 40), strings.Repeat("c", 64), []string{"test/email.test.js"}); err == nil {
		t.Fatal("finding outside the saved diff unexpectedly passed")
	}

	result.Verdict = "PASS"
	result.ReasonCode = "REVIEW_PASSED"
	raw, err = MarshalAgentChosenResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLocalReviewResult(raw, "review-1", "candidate-1", strings.Repeat("b", 40), strings.Repeat("c", 64), []string{"src/email.js"}); err == nil {
		t.Fatal("PASS with a blocking finding unexpectedly passed")
	}
}
