package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseComplexScopeExpansionRequestBindsPlanHashWithoutAgentEcho(t *testing.T) {
	planSHA := strings.Repeat("b", 64)
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "kind": ComplexScopeExpansionRequestKind,
		"requestedPaths": []string{"package.json"}, "summary": "需要写入 package.json。",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseComplexScopeExpansionRequest(raw, "d1", "t1", 0, "v2", strings.Repeat("a", 64), "p1", planSHA)
	if err != nil {
		t.Fatalf("legal scope request rejected: %v", err)
	}
	if got.PlanSHA256 != planSHA || got.DispatchID != "d1" || got.TaskID != "t1" || got.Round != 0 {
		t.Fatalf("bound facts = %#v", got)
	}
}

func TestParseComplexScopeExpansionRequestRejectsEchoedPlanHash(t *testing.T) {
	planSHA := strings.Repeat("b", 64)
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "kind": ComplexScopeExpansionRequestKind,
		"planSha256": planSHA, "requestedPaths": []string{"package.json"}, "summary": "需要写入 package.json。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseComplexScopeExpansionRequest(raw, "d1", "t1", 0, "v2", strings.Repeat("a", 64), "p1", planSHA); err == nil {
		t.Fatal("echoed planSha256 was accepted")
	}
	wrong := []byte(strings.Replace(string(raw), planSHA, strings.Repeat("c", 64), 1))
	if _, err := ParseComplexScopeExpansionRequest(wrong, "d1", "t1", 0, "v2", strings.Repeat("a", 64), "p1", planSHA); err == nil {
		t.Fatal("wrong planSha256 was corrected and accepted")
	}
}

func TestParseRequirementCompilationBindsContextHashWithoutAgentEcho(t *testing.T) {
	contextSHA := strings.Repeat("a", 64)
	raw := compilationJSON(t, nil)
	got, err := ParseRequirementCompilationResult(raw, "req-1", "ver-1", contextSHA, 0, false)
	if err != nil {
		t.Fatalf("legal compilation rejected: %v", err)
	}
	if got.CompilationContextSHA256 != contextSHA || got.CompilationRequestID != "req-1" || got.ClarificationRound != 0 {
		t.Fatalf("bound facts = %#v", got)
	}
	echoed := compilationJSON(t, map[string]any{"compilationContextSha256": contextSHA})
	if _, err := ParseRequirementCompilationResult(echoed, "req-1", "ver-1", contextSHA, 0, false); err == nil {
		t.Fatal("echoed compilationContextSha256 was accepted")
	}
}

func TestParseComplexEngineeringPlanBindsHashesWithoutAgentEcho(t *testing.T) {
	coverage := ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002", "REQ-003"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST", "REQ-003": "MUST"},
		AcceptanceIDs:  map[string]string{"ACC-001": "a", "ACC-002": "b", "ACC-003": "c"},
	}
	versionSHA := strings.Repeat("c", 64)
	compilationSHA := strings.Repeat("d", 64)
	got, _, planSHA, err := ParseComplexEngineeringPlanResult(planJSON(t, nil), "plan-req", "ver-1", versionSHA, compilationSHA, coverage)
	if err != nil {
		t.Fatalf("legal engineering plan rejected: %v", err)
	}
	if got.RequirementVersionSHA256 != versionSHA || got.CompilationSHA256 != compilationSHA || got.PlanningRequestID != "plan-req" {
		t.Fatalf("bound facts = %#v", got)
	}
	if len(planSHA) != 64 {
		t.Fatalf("stored plan hash = %q", planSHA)
	}
	echoed := planJSON(t, map[string]any{"requirementVersionSha256": versionSHA, "compilationSha256": compilationSHA})
	if _, _, _, err := ParseComplexEngineeringPlanResult(echoed, "plan-req", "ver-1", versionSHA, compilationSHA, coverage); err == nil {
		t.Fatal("echoed engineering-plan hashes were accepted")
	}
}
