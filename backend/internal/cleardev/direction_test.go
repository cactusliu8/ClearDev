package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDirectionChangeRequestAcceptsExactResult(t *testing.T) {
	sha := strings.Repeat("a", 64)
	messageSHA := strings.Repeat("b", 64)
	raw := directionChangeJSON(t, map[string]any{})
	got, err := ParseDirectionChangeRequest(raw, "dir-1", "req-1", "ver-1", sha, messageSHA)
	if err != nil {
		t.Fatalf("valid direction change rejected: %v", err)
	}
	if got.Decision != DirectionChangeDecisionStop || got.Summary == "" || len(got.AffectedRequirementIDs) != 1 ||
		got.DirectionRequestID != "dir-1" || got.UserMessageSHA256 != messageSHA {
		t.Fatalf("parsed result = %#v", got)
	}
}

func TestParseDirectionChangeRequestRejectsUnknownWrongHashAndDecision(t *testing.T) {
	sha := strings.Repeat("a", 64)
	messageSHA := strings.Repeat("b", 64)
	withUnknown := directionChangeJSON(t, map[string]any{"extra": true})
	if _, err := ParseDirectionChangeRequest(withUnknown, "dir-1", "req-1", "ver-1", sha, messageSHA); err == nil {
		t.Fatal("unknown field was accepted")
	}
	echoed := directionChangeJSON(t, map[string]any{"userMessageSha256": strings.Repeat("c", 64)})
	if _, err := ParseDirectionChangeRequest(echoed, "dir-1", "req-1", "ver-1", sha, messageSHA); err == nil {
		t.Fatal("echoed user message hash was accepted")
	}
	wrongDecision := directionChangeJSON(t, map[string]any{"decision": "APPROVE"})
	if _, err := ParseDirectionChangeRequest(wrongDecision, "dir-1", "req-1", "ver-1", sha, messageSHA); err == nil {
		t.Fatal("non-stop decision was accepted")
	}
}

func TestParseApproveDirectionChangeBindingRejectsUnknownAndMissing(t *testing.T) {
	valid := `{
		"developmentRequirementId":"req-1",
		"directionRequestId":"dir-1",
		"requirementVersionId":"ver-1",
		"requirementVersionSha256":"` + strings.Repeat("a", 64) + `",
		"taskSetVersion":2,
		"snapshotSha256":"` + strings.Repeat("b", 64) + `"
	}`
	if _, err := ParseApproveDirectionChangeBinding([]byte(valid)); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}
	if _, err := ParseApproveDirectionChangeBinding([]byte(strings.TrimSuffix(valid, "}") + `,"extra":true}`)); err == nil {
		t.Fatal("binding with extra field was accepted")
	}
	registry := ProductionDecisionRegistry()
	if _, ok := registry.Lookup(HumanDecisionKindApproveDirectionChange); !ok {
		t.Fatal("production registry is missing APPROVE_DIRECTION_CHANGE")
	}
}

func TestDirectionCompilationContextHashChangesWithMessageOrDecision(t *testing.T) {
	base := DirectionCompilationContext(
		"req-1", "ver-2", "ver-1", strings.Repeat("a", 64), strings.Repeat("c", 64),
		DirectionIntent{RequestID: "intent-1", MessageSHA256: strings.Repeat("b", 64)},
		DirectionRequest{ID: "dir-1", ResultSHA256: strings.Repeat("d", 64)},
		"decision-1", strings.Repeat("e", 64),
		[]string{"stable-req"}, []string{"stable-acc"}, nil,
	)
	first, _, err := HashCompilationContext(base)
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.UserMessageSHA256 = strings.Repeat("f", 64)
	second, _, err := HashCompilationContext(changed)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("replacing the user message hash did not change the compilation context")
	}
	s04 := CompilationContext{
		SchemaVersion: ComplexProtocolVersion, DevelopmentRequirementID: "req-1",
		TargetRequirementVersionID: "ver-1", SourcePRDSHA256: strings.Repeat("c", 64),
		PreviousRounds: []CompilationContextRound{},
	}
	if _, raw, err := HashCompilationContext(s04); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(raw), "directionIntentId") {
		t.Fatalf("S04 compilation context included S05 fields: %s", raw)
	}
}

func TestDirectionChangeDisplayIncludesFullUserMessage(t *testing.T) {
	message := "只接受 example.com 域名，其他域名计入拒绝数量。"
	display := ApproveDirectionChangeDisplay(
		DevelopmentRequirement{Name: "复杂邮件名单"},
		message, "Stop current work.", strings.Repeat("a", 64), strings.Repeat("b", 64), 2,
	)
	if display.FullContent != message {
		t.Fatalf("full content = %q", display.FullContent)
	}
	if !strings.Contains(display.ChangeSummary, strings.Repeat("a", 64)) || !strings.Contains(display.ChangeSummary, "Task count 2") {
		t.Fatalf("change summary = %q", display.ChangeSummary)
	}
}

func TestDeriveDirectionChangePhaseVacuousOccupancyAfterApprove(t *testing.T) {
	intent := &DirectionIntent{RequestID: "intent-1"}
	request := &DirectionRequest{ID: "dir-1"}
	gate := &DirectionStopGate{Status: DirectionStopGateActive}
	waiting := DirectionChangeSnapshot{Intent: intent, Request: request, Gate: gate}
	if got := DeriveDirectionChangePhase(waiting, nil, nil, nil); got != DirectionChangeAwaitingDecision {
		t.Fatalf("before approve = %s", got)
	}
	approved := waiting
	approved.OccupancyApproved = true
	if got := DeriveDirectionChangePhase(approved, nil, nil, nil); got != DirectionChangeProcessing {
		t.Fatalf("approved empty occupancy without revision = %s", got)
	}
	withRevision := approved
	withRevision.Revision = &DirectionRevision{TargetRequirementVersionID: "ver-2"}
	if got := DeriveDirectionChangePhase(withRevision, nil, nil, nil); got != DirectionChangeCompiling {
		t.Fatalf("approved empty occupancy with revision = %s", got)
	}
	if AllDirectionProcessingsFinal(waiting) || !AllDirectionProcessingsFinal(approved) {
		t.Fatal("vacuous occupancy final flag did not follow desktop approve")
	}
}

func directionChangeJSON(t *testing.T, overlay map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"schemaVersion":          1,
		"kind":                   "DIRECTION_CHANGE_REQUEST",
		"decision":               "REQUEST_STOP_AND_REVISE",
		"summary":                "用户要求只接受 example.com 域名。",
		"affectedRequirementIds": []string{"stable-req"},
	}
	for key, value := range overlay {
		body[key] = value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
