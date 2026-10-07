package cleardev

import (
	"encoding/json"
	"errors"
	"strings"
)

// HumanDecisionKindFinalReviewRecheck grants one evidence-only native recheck.
const HumanDecisionKindFinalReviewRecheck = "AUTHORIZE_FINAL_REVIEW_EVIDENCE_RECHECK"

// FinalReviewAttemptEvidence carries executor facts, never model-selected limits.
type FinalReviewAttemptEvidence struct {
	Policy              string            `json:"policy"`
	DevelopmentLimit    int               `json:"developmentLimit"`
	ReviewerRepairLimit int               `json:"reviewerRepairLimit"`
	HumanExtraLimit     int               `json:"humanExtraLimit"`
	Slots               []MailAttemptSlot `json:"slots"`
}

// NewFinalReviewAttemptEvidence records the existing fixed mail attempt limits.
func NewFinalReviewAttemptEvidence(slots []MailAttemptSlot) *FinalReviewAttemptEvidence {
	return &FinalReviewAttemptEvidence{Policy: MailAttemptPolicyV1, DevelopmentLimit: 3, ReviewerRepairLimit: 1, HumanExtraLimit: 1, Slots: slots}
}

// FinalReviewPriorConclusion preserves the original decision and its provider identity.
type FinalReviewPriorConclusion struct {
	ReviewID           string `json:"reviewId"`
	ResultID           string `json:"resultId"`
	PacketSHA256       string `json:"packetSha256"`
	Verdict            string `json:"verdict"`
	Summary            string `json:"summary"`
	AOSessionID        string `json:"aoSessionId"`
	WorkspacePath      string `json:"workspacePath"`
	AuthorityRequestID string `json:"authorityRequestId"`
}

// SessionReviewID retains the original independent reviewer session identity.
func (r RequirementFinalReview) SessionReviewID() string {
	if r.PreviousReviewID != "" {
		return r.PreviousReviewID
	}
	return r.ID
}

// FinalReviewRecheckBinding pins the old conclusion, candidate and added evidence.
type FinalReviewRecheckBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	ExecutionRunID           string `json:"executionRunId"`
	PreviousReviewID         string `json:"previousReviewId"`
	PreviousResultID         string `json:"previousResultId"`
	PreviousPacketSHA256     string `json:"previousPacketSha256"`
	CandidateSHA             string `json:"candidateSha"`
	AttemptEvidenceSHA256    string `json:"attemptEvidenceSha256"`
}

// ParseFinalReviewRecheckBinding rejects missing, unknown and invalid authority fields.
func ParseFinalReviewRecheckBinding(raw []byte) (FinalReviewRecheckBinding, error) {
	var b FinalReviewRecheckBinding
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	if _, err := requireJSONObjectFields(raw, "developmentRequirementId", "executionRunId", "previousReviewId", "previousResultId", "previousPacketSha256", "candidateSha", "attemptEvidenceSha256"); err != nil {
		return b, err
	}
	for _, id := range []string{b.DevelopmentRequirementID, b.ExecutionRunID, b.PreviousReviewID, b.PreviousResultID} {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 300 {
			return b, errors.New("invalid final recheck identity")
		}
	}
	if !validProtocolSHA256(b.PreviousPacketSHA256) || !validProtocolSHA256(b.AttemptEvidenceSHA256) || !mailSHA1(b.CandidateSHA) {
		return b, errors.New("invalid final recheck digest")
	}
	return b, nil
}

// FinalReviewRecheckSpec registers the private desktop decision envelope.
func FinalReviewRecheckSpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindFinalReviewRecheck, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParseFinalReviewRecheckBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		}}
}
