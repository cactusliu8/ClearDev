package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFinalReviewEvidencePromptDoesNotInlineRecursiveHistory(t *testing.T) {
	packet := RequirementFinalReviewPacket{SchemaVersion: 1, ReviewID: "review", EvidenceFile: "/tmp/ao/evidence.json"}
	packet.Requirement.RequirementText = "MUST: preserve all accepted requirements"
	packet.Run.ExecutionPackageJSON = strings.Repeat("historical nested context", 100000)
	packet.PlannerRuntime = &PlannerRuntimeSnapshot{Requests: []PlannerCoordinationRequest{{ContextJSON: `{"history":"old context"}`, Prompt: "HISTORICAL_PROMPT_MUST_NOT_BE_SENT"}}}
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	review := RequirementFinalReview{ReviewPacketJSON: string(raw), ReviewPacketSHA256: sha256Hex(raw), CandidateCommitSHA: strings.Repeat("a", 40)}
	prompt := RequirementFinalReviewPrompt(review)
	if len(prompt) > 32000 || strings.Contains(prompt, packet.Run.ExecutionPackageJSON) || strings.Contains(prompt, "HISTORICAL_PROMPT_MUST_NOT_BE_SENT") {
		t.Fatalf("prompt still inlines recursive packet: %d bytes", len(prompt))
	}
	for _, required := range []string{packet.EvidenceFile, review.ReviewPacketSHA256, "MUST: preserve all accepted requirements", "按字段"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("missing evidence access: %s", required)
		}
	}
	packet.EvidenceFile = ""
	raw, _ = json.Marshal(packet)
	review.ReviewPacketJSON = string(raw)
	if !strings.Contains(RequirementFinalReviewPrompt(review), string(raw)) {
		t.Fatal("legacy prompt changed")
	}
}
