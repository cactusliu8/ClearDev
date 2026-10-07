package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseTrustedProgressExplanationResultAcceptsMatchingPacket(t *testing.T) {
	summary := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "req-1", AOProjectID: "ao-1", Name: "Demo"},
			RequirementVersions: []RequirementVersion{{ID: "rv1", Version: 1, Status: RequirementVersionStatusPendingConfirmation}},
			Events:              []RequirementEvent{{Sequence: 3, Outcome: EventAccepted, Action: ActionSubmitRequirementConfirmation, SubjectID: "rv1"}},
		},
		Now: time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC),
	})
	packet := BuildTrustedProgressFactPacket(summary)
	raw, err := MarshalAgentChosenResult(TrustedProgressExplanationResult{
		SchemaVersion: 1, Kind: TrustedProgressExplanationKind, FactSummarySHA256: packet.FactSummarySHA256,
		Phase: string(packet.Phase), Attention: string(packet.Attention), NextOwnerRole: packet.NextOwnerRole,
		PendingDecision: packet.PendingDecision, CitedFactIDs: []string{"event:3", "version:rv1"},
		Summary: "Waiting for a human to confirm the current requirement version.",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseTrustedProgressExplanationResult(raw, packet)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Summary == "" || got.Phase != string(TrustedPhaseAwaitingConfirmation) {
		t.Fatalf("result = %+v", got)
	}
}

func TestParseTrustedProgressExplanationResultRejectsMismatchAndUnknownCitation(t *testing.T) {
	summary := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement: DevelopmentRequirement{ID: "req-1", AOProjectID: "ao-1", Name: "Demo"},
			Events:      []RequirementEvent{{Sequence: 1, Outcome: EventAccepted, Action: ActionCreateRequirement}},
		},
		Now: time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC),
	})
	packet := BuildTrustedProgressFactPacket(summary)
	base := TrustedProgressExplanationResult{
		SchemaVersion: 1, Kind: TrustedProgressExplanationKind,
		CitedFactIDs: []string{"event:1"}, Summary: "ok",
	}
	cases := []struct {
		name   string
		mutate func(*TrustedProgressExplanationResult)
		echo   bool
		want   string
	}{
		{name: "unknown citation", mutate: func(r *TrustedProgressExplanationResult) { r.CitedFactIDs = []string{"event:99"} }, want: "unknown fact"},
		{name: "echoed hash", mutate: func(r *TrustedProgressExplanationResult) { r.FactSummarySHA256 = packet.FactSummarySHA256 }, echo: true, want: "extra fields"},
		{name: "echoed phase", mutate: func(r *TrustedProgressExplanationResult) { r.Phase = "COMPLETED" }, echo: true, want: "extra fields"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			item := base
			tt.mutate(&item)
			var raw []byte
			var err error
			if tt.echo {
				raw, err = json.Marshal(item)
			} else {
				raw, err = MarshalAgentChosenResult(item)
			}
			_, err = ParseTrustedProgressExplanationResult(raw, packet)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestTrustedProgressExplanationPromptContainsPacket(t *testing.T) {
	summary := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{Requirement: DevelopmentRequirement{ID: "req-1", AOProjectID: "ao-1", Name: "Demo"}},
		Now:      time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC),
	})
	packet := BuildTrustedProgressFactPacket(summary)
	prompt, err := TrustedProgressExplanationPrompt(packet)
	if err != nil || !strings.Contains(prompt, packet.FactSummarySHA256) || !strings.Contains(prompt, TrustedProgressFactPacketKind) {
		t.Fatalf("prompt = %q err=%v", prompt, err)
	}
}
