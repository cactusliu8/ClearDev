package cleardev

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Trusted-progress steward protocol constants. The control plane binds packet hashes and derived status onto the result.
const (
	TrustedProgressFactPacketKind  = "CLEARDEV_PROGRESS_FACT_PACKET"
	TrustedProgressExplanationKind = "CLEARDEV_PROGRESS_EXPLANATION"
	TrustedProgressProtocolVersion = 1
	MaxTrustedProgressSummaryRunes = 2000
	MaxTrustedProgressCitedFacts   = 32
)

// TrustedProgressFactPacket is the bounded fact bundle sent to the steward.
type TrustedProgressFactPacket struct {
	SchemaVersion            int                      `json:"schemaVersion"`
	Kind                     string                   `json:"kind"`
	DevelopmentRequirementID string                   `json:"developmentRequirementId"`
	FactSummarySHA256        string                   `json:"factSummarySha256"`
	MaxEventSequence         int64                    `json:"maxEventSequence"`
	Phase                    TrustedPhase             `json:"phase"`
	Attention                OverallAttention         `json:"attention"`
	NextOwnerRole            string                   `json:"nextOwnerRole"`
	NextOwnerAction          string                   `json:"nextOwnerAction"`
	PendingDecision          bool                     `json:"pendingDecision"`
	Name                     string                   `json:"name"`
	CurrentWork              []TrustedIssue           `json:"currentWork"`
	Blockers                 []TrustedIssue           `json:"blockers"`
	PendingDecisions         []TrustedIssue           `json:"pendingDecisions"`
	MissingEvidence          []TrustedMissingEvidence `json:"missingEvidence"`
	Tasks                    []TrustedTaskItem        `json:"tasks"`
	Facts                    []TrustedCitableFact     `json:"facts"`
}

// TrustedProgressExplanationResult is the only steward result accepted for a note.
type TrustedProgressExplanationResult struct {
	SchemaVersion     int      `json:"schemaVersion"`
	Kind              string   `json:"kind"`
	FactSummarySHA256 string   `json:"factSummarySha256"`
	Phase             string   `json:"phase"`
	Attention         string   `json:"attention"`
	NextOwnerRole     string   `json:"nextOwnerRole"`
	PendingDecision   bool     `json:"pendingDecision"`
	CitedFactIDs      []string `json:"citedFactIds"`
	Summary           string   `json:"summary"`
}

// BuildTrustedProgressFactPacket copies the derived summary into a bounded packet.
func BuildTrustedProgressFactPacket(summary TrustedProgressSummary) TrustedProgressFactPacket {
	return TrustedProgressFactPacket{
		SchemaVersion: TrustedProgressProtocolVersion, Kind: TrustedProgressFactPacketKind,
		DevelopmentRequirementID: summary.DevelopmentRequirementID, FactSummarySHA256: summary.FactSummarySHA256,
		MaxEventSequence: summary.LatestFactSequence, Phase: summary.Phase, Attention: summary.Attention,
		NextOwnerRole: summary.NextOwner.Role, NextOwnerAction: summary.NextOwner.Action,
		PendingDecision: len(summary.PendingDecisions) > 0, Name: summary.Name,
		CurrentWork: summary.CurrentWork, Blockers: summary.Blockers, PendingDecisions: summary.PendingDecisions,
		MissingEvidence: summary.MissingEvidence, Tasks: currentTrustedTasks(summary.Tasks), Facts: summary.CitableFacts,
	}
}

func currentTrustedTasks(tasks []TrustedTaskItem) []TrustedTaskItem {
	out := make([]TrustedTaskItem, 0, len(tasks))
	for _, task := range tasks {
		if task.Current {
			out = append(out, task)
		}
	}
	return out
}

// ParseTrustedProgressExplanationResult accepts agent-chosen citations and summary,
// then binds packet hashes and derived status from the control plane.
func ParseTrustedProgressExplanationResult(raw []byte, packet TrustedProgressFactPacket) (TrustedProgressExplanationResult, error) {
	var result TrustedProgressExplanationResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return TrustedProgressExplanationResult{}, err
	}
	if _, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "citedFactIds", "summary"); err != nil {
		return TrustedProgressExplanationResult{}, err
	}
	if result.SchemaVersion != TrustedProgressProtocolVersion {
		return TrustedProgressExplanationResult{}, fmt.Errorf("progress explanation schemaVersion must be %d", TrustedProgressProtocolVersion)
	}
	if result.Kind != TrustedProgressExplanationKind {
		return TrustedProgressExplanationResult{}, fmt.Errorf("progress explanation kind must be %s", TrustedProgressExplanationKind)
	}
	result.FactSummarySHA256 = packet.FactSummarySHA256
	result.Phase = string(packet.Phase)
	result.Attention = string(packet.Attention)
	result.NextOwnerRole = packet.NextOwnerRole
	result.PendingDecision = packet.PendingDecision
	if !validProtocolSHA256(result.FactSummarySHA256) {
		return TrustedProgressExplanationResult{}, fmt.Errorf("progress explanation factSummarySha256 does not match the fact packet")
	}
	summary := strings.TrimSpace(result.Summary)
	if summary == "" || utf8.RuneCountInString(summary) > MaxTrustedProgressSummaryRunes {
		return TrustedProgressExplanationResult{}, fmt.Errorf("progress explanation summary is empty or too long")
	}
	result.Summary = summary
	if len(result.CitedFactIDs) == 0 || len(result.CitedFactIDs) > MaxTrustedProgressCitedFacts {
		return TrustedProgressExplanationResult{}, fmt.Errorf("progress explanation citedFactIds is empty or too long")
	}
	allowed := CitableFactIDSet(packet.Facts)
	seen := map[string]struct{}{}
	for _, id := range result.CitedFactIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return TrustedProgressExplanationResult{}, fmt.Errorf("progress explanation citedFactIds contains an empty id")
		}
		if _, ok := allowed[id]; !ok {
			return TrustedProgressExplanationResult{}, fmt.Errorf("progress explanation citedFactIds quotes unknown fact %q", id)
		}
		if _, dup := seen[id]; dup {
			return TrustedProgressExplanationResult{}, fmt.Errorf("progress explanation citedFactIds repeats %q", id)
		}
		seen[id] = struct{}{}
	}
	return result, nil
}

// TrustedProgressExplanationPrompt asks the steward to explain the packet only.
func TrustedProgressExplanationPrompt(packet TrustedProgressFactPacket) (string, error) {
	raw, err := marshalCanonicalJSON(packet)
	if err != nil {
		return "", err
	}
	return "You are the ClearDev Project Steward. Explain the current trusted progress from this fact packet only. Do not invent facts or change phase, attention, next owner, or pendingDecision. Return exactly one JSON object with schemaVersion 1, kind CLEARDEV_PROGRESS_EXPLANATION, citedFactIds from packet.facts[].id, and a short summary. Do not include hashes, phase, attention, nextOwnerRole, or pendingDecision in the JSON; the control plane binds those from the packet.\n\n" + string(raw), nil
}

const trustedProgressPromptPacketSep = "\n\n"

// PacketFromTrustedProgressPrompt recovers the frozen packet from a stored prompt.
func PacketFromTrustedProgressPrompt(prompt string) (TrustedProgressFactPacket, error) {
	index := strings.Index(prompt, trustedProgressPromptPacketSep)
	if index < 0 {
		return TrustedProgressFactPacket{}, fmt.Errorf("progress explanation prompt is missing its fact packet")
	}
	var packet TrustedProgressFactPacket
	if err := decodeStrictAgentResult([]byte(prompt[index+len(trustedProgressPromptPacketSep):]), &packet); err != nil {
		return TrustedProgressFactPacket{}, err
	}
	if packet.Kind != TrustedProgressFactPacketKind || packet.SchemaVersion != TrustedProgressProtocolVersion {
		return TrustedProgressFactPacket{}, fmt.Errorf("progress explanation prompt packet is invalid")
	}
	return packet, nil
}
