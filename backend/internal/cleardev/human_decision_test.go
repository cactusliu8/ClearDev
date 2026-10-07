package cleardev

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseConfirmRequirementBindingRejectsUnknownAndMissing(t *testing.T) {
	valid := `{
		"developmentRequirementId":"req-1",
		"requirementVersionId":"ver-1",
		"requirementVersionSha256":"` + strings.Repeat("a", 64) + `",
		"taskSetVersion":0
	}`
	if _, err := ParseConfirmRequirementBinding([]byte(valid)); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}
	for _, raw := range []string{
		`{"developmentRequirementId":"req-1","requirementVersionId":"ver-1","requirementVersionSha256":"` + strings.Repeat("a", 64) + `","taskSetVersion":0,"extra":true}`,
		`{"developmentRequirementId":"req-1","requirementVersionId":"ver-1","requirementVersionSha256":"` + strings.Repeat("a", 64) + `"}`,
		`{"developmentRequirementId":"req-1","requirementVersionId":"ver-1","requirementVersionSha256":"` + strings.Repeat("a", 64) + `","taskSetVersion":null}`,
	} {
		if _, err := ParseConfirmRequirementBinding([]byte(raw)); err == nil {
			t.Fatalf("invalid binding accepted: %s", raw)
		}
	}
}

func TestHumanDecisionEnvelopeRoundTripAndUnknownKind(t *testing.T) {
	registry := ProductionDecisionRegistry()
	binding := ConfirmRequirementBinding{
		DevelopmentRequirementID: "req-1", RequirementVersionID: "ver-1",
		RequirementVersionSHA256: strings.Repeat("b", 64), TaskSetVersion: 0,
	}
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	display := HumanDecisionDisplay{
		Title: "Confirm requirement version v1", Summary: "Demo version 1 is waiting for confirmation.",
		FullContent: "Normalize email addresses.", ChangeSummary: "Submitted draft version 1 for confirmation. Task-set version 0. SHA-256 " + binding.RequirementVersionSHA256 + ".",
	}
	digest, err := HumanDecisionContentSHA256(HumanDecisionKindConfirmVersion, bindingJSON, display)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := newTestToken()
	if err != nil {
		t.Fatal(err)
	}
	offer := HumanDecisionOffer{
		ProtocolVersion: 1, Kind: HumanDecisionOfferKind, DesktopRunID: "run-1",
		RequestID: "decision-1", DecisionKind: HumanDecisionKindConfirmVersion,
		BindingSchemaVersion: 1, Binding: bindingJSON, ContentSHA256: digest,
		Nonce: nonce, ExpiresAt: time.Date(2026, 8, 25, 4, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Display: display,
	}
	raw, err := json.Marshal(offer)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseHumanDecisionOffer(raw, registry)
	if err != nil {
		t.Fatalf("valid offer rejected: %v", err)
	}
	resultRaw, err := json.Marshal(HumanDecisionResult{
		ProtocolVersion: 1, Kind: HumanDecisionResultKind, DesktopRunID: parsed.DesktopRunID,
		RequestID: parsed.RequestID, DecisionKind: parsed.DecisionKind, BindingSchemaVersion: parsed.BindingSchemaVersion,
		Binding: parsed.Binding, ContentSHA256: parsed.ContentSHA256, Nonce: parsed.Nonce, Decision: HumanDecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ParseHumanDecisionResult(resultRaw, registry)
	if err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if err := ResultMatchesOffer(result, parsed); err != nil {
		t.Fatal(err)
	}

	unknown := []byte(strings.Replace(string(raw), HumanDecisionKindConfirmVersion, "NOT_A_REGISTERED_KIND", 1))
	if _, err := ParseHumanDecisionOffer(unknown, registry); err == nil {
		t.Fatal("unregistered kind was accepted by the production registry")
	}
	extra := []byte(strings.TrimSuffix(string(raw), "}") + `,"extra":true}`)
	if _, err := ParseHumanDecisionOffer(extra, registry); err == nil {
		t.Fatal("offer with an extra field was accepted")
	}
}

func TestSecondRegisteredKindDoesNotChangeEnvelopeShape(t *testing.T) {
	registry := NewDecisionRegistry()
	if err := registry.Register(ConfirmRequirementVersionSpec()); err != nil {
		t.Fatal(err)
	}
	fakeKind := DecisionKindSpec{
		Kind: "TEST_SECOND_KIND", BindingSchemaVersion: 1,
		ParseBinding: func(raw []byte) ([]byte, error) {
			var parsed struct {
				ID string `json:"id"`
			}
			if err := decodeStrictAgentResult(raw, &parsed); err != nil {
				return nil, err
			}
			if _, err := requireJSONObjectFields(raw, "id"); err != nil {
				return nil, err
			}
			if strings.TrimSpace(parsed.ID) == "" {
				return nil, errors.New("fake binding id is required")
			}
			return json.Marshal(parsed)
		},
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
	}
	if err := registry.Register(fakeKind); err != nil {
		t.Fatal(err)
	}
	bindingJSON := []byte(`{"id":"object-1"}`)
	display := HumanDecisionDisplay{Title: "Second kind", Summary: "A test-only kind.", FullContent: "body", ChangeSummary: "none"}
	digest, err := HumanDecisionContentSHA256("TEST_SECOND_KIND", bindingJSON, display)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := newTestToken()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(HumanDecisionOffer{
		ProtocolVersion: 1, Kind: HumanDecisionOfferKind, DesktopRunID: "run-2",
		RequestID: "decision-2", DecisionKind: "TEST_SECOND_KIND", BindingSchemaVersion: 1,
		Binding: bindingJSON, ContentSHA256: digest, Nonce: nonce,
		ExpiresAt: time.Date(2026, 8, 25, 4, 10, 0, 0, time.UTC).Format(time.RFC3339), Display: display,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseHumanDecisionOffer(raw, registry); err != nil {
		t.Fatalf("second kind should reuse the same envelope: %v", err)
	}
	if _, err := ParseHumanDecisionOffer(raw, ProductionDecisionRegistry()); err == nil {
		t.Fatal("production registry must not accept the test-only kind")
	}
}

func newTestToken() (string, error) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	return encodeTestToken(raw), nil
}
