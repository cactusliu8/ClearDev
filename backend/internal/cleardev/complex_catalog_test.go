package cleardev

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestFrozenComplexCheckCatalogPinsDemoArgvAndHashes(t *testing.T) {
	want := map[string]string{
		"email-unit":       "feafc89ad5e5314058f839a1f280aa8cff20503583f40379c01f4d97dd031466",
		"deduplicate-unit": "04f123a6661183016601642f5aea4db485ef025a371e884a52b6ed4a11cee37e",
		"summary-unit":     "7de135c41f08e1eb60ace8d93642a74645118f3dfb54d515e0099a39059a372f",
		"all-tests":        "82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586",
		"demo-backend":     "002e3eeb106f74ca3bdd9e4ea429b3842579702f26a50361d791725ec4303d22",
		"demo-database":    "7545c63f42eca5bb8b9db44014a4850045b83c27375beaeb6fc5424cd2d3f787",
		"demo-api":         "c6c74e01bd47039cc69b2f71fa8948be8bcb1af25fcd4b2d467f9bd3d81b6be7",
		"demo-frontend":    "153f7220bfe994393806417c9b0259ecf1148c573f963a3e39ccfe3abc5196f1",
		"demo-integration": "527c484bcc3bb219e92ed61f99ff968f31143f89e53fda93d09b74c0ce3177d4",
	}
	got := FrozenComplexCheckCatalog()
	if len(got) != len(want) {
		t.Fatalf("catalog size = %d, want %d", len(got), len(want))
	}
	for _, spec := range got {
		raw, err := json.Marshal(spec.Argv)
		if err != nil {
			t.Fatal(err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(raw))
		if sum != want[spec.ID] {
			t.Errorf("%s argv hash = %s, want %s argv=%s", spec.ID, sum, want[spec.ID], raw)
		}
		if spec.TimeoutSeconds != 60 {
			t.Errorf("%s timeout = %d", spec.ID, spec.TimeoutSeconds)
		}
	}
}

func TestParseComplexEngineeringPlanAcceptsDemoFullStackPaths(t *testing.T) {
	coverage := ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002", "REQ-003"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST", "REQ-003": "MUST"},
		AcceptanceIDs:  map[string]string{"ACC-001": "a", "ACC-002": "b", "ACC-003": "c"},
	}
	raw := planJSON(t, map[string]any{
		"tasks": []any{
			planTask("schema", []string{"REQ-001"}, []string{"ACC-001"}, []string{"migrations/**"}, []string{"demo-database"}, []string{}),
			planTask("api", []string{"REQ-002"}, []string{"ACC-002"}, []string{"backend/**"}, []string{"demo-backend", "demo-api"}, []string{"schema"}),
			planTask("page", []string{"REQ-003"}, []string{"ACC-003"}, []string{"frontend/**"}, []string{"demo-frontend"}, []string{"schema"}),
		},
		"integrationCheckIds": []string{"demo-integration"},
		"parallelSuggestion":  map[string]any{"recommendedBuilderCount": 2, "reason": "backend and frontend write disjoint paths."},
	})
	parsed, _, _, err := ParseComplexEngineeringPlanResult(raw, "plan-req", "ver-1", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", coverage)
	if err != nil {
		t.Fatalf("demo plan rejected: %v", err)
	}
	selection, err := SelectComplexExecutionMode(parsed.Tasks, parsed.ParallelSuggestion.RecommendedBuilderCount)
	if err != nil || selection.Mode != WorkModeParallel || selection.BuilderCount != 2 {
		t.Fatalf("demo mode = %#v err=%v", selection, err)
	}
}

func TestParseComplexEngineeringPlanRejectsUnknownDemoCheck(t *testing.T) {
	coverage := ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST"},
		AcceptanceIDs:  map[string]string{"ACC-001": "a", "ACC-002": "b"},
	}
	raw := planJSON(t, map[string]any{
		"tasks": []any{
			planTask("schema", []string{"REQ-001"}, []string{"ACC-001"}, []string{"migrations/**"}, []string{"made-up-check"}, []string{}),
			planTask("api", []string{"REQ-002"}, []string{"ACC-002"}, []string{"backend/**"}, []string{"demo-backend"}, []string{"schema"}),
		},
		"integrationCheckIds": []string{"demo-integration"},
	})
	if _, _, _, err := ParseComplexEngineeringPlanResult(raw, "plan-req", "ver-1", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", coverage); err == nil {
		t.Fatal("unknown check was accepted")
	}
}

func TestComplexTemplatePathAllowsFullStackRoots(t *testing.T) {
	for _, path := range []string{"backend/src/emails.ts", "frontend/index.html", "migrations/001.sql", "src/email.js"} {
		if !complexTemplatePathAllowed(path) {
			t.Fatalf("allowed path rejected: %s", path)
		}
	}
	for _, path := range []string{".github/workflows/ci.yml", "scripts/build.mjs", "../secret", "tsconfig.json"} {
		if complexTemplatePathAllowed(path) {
			t.Fatalf("locked path accepted: %s", path)
		}
	}
}

func TestParseComplexEngineeringPlanRejectsTsconfigForbiddenPath(t *testing.T) {
	coverage := ComplexCoverage{
		MUSTIDs:        []string{"REQ-001", "REQ-002"},
		RequirementIDs: map[string]string{"REQ-001": "MUST", "REQ-002": "MUST"},
		AcceptanceIDs:  map[string]string{"ACC-001": "a", "ACC-002": "b"},
	}
	outside := planTask("schema", []string{"REQ-001"}, []string{"ACC-001"}, []string{"migrations/**"}, []string{"demo-database"}, []string{})
	outside["forbiddenPaths"] = []string{"tsconfig.json"}
	raw := planJSON(t, map[string]any{
		"tasks": []any{
			outside,
			planTask("api", []string{"REQ-002"}, []string{"ACC-002"}, []string{"backend/**"}, []string{"demo-backend"}, []string{"schema"}),
		},
		"integrationCheckIds": []string{"demo-integration"},
	})
	_, _, _, err := ParseComplexEngineeringPlanResult(raw, "plan-req", "ver-1", strings.Repeat("c", 64), strings.Repeat("d", 64), coverage)
	if err == nil {
		t.Fatal("tsconfig.json forbidden path was accepted")
	}
	if !strings.Contains(err.Error(), `path "tsconfig.json" is outside the frozen template`) {
		t.Fatalf("unexpected parse error: %v", err)
	}
}
