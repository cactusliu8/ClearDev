package cleardev

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluateCompletion(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	candidate := CompletionCandidate{ID: "candidate-2", CommitSHA: strings.Repeat("a", 40), BuilderSessionID: "builder"}
	pass := func(kind EvidenceKind, check string, sequence int64) CompletionEvidence {
		return CompletionEvidence{CandidateID: candidate.ID, CommitSHA: candidate.CommitSHA, Kind: kind, RequiredCheck: check, Result: EvidenceResultPass, Source: EvidenceSourceControlPlaneChecker, Sequence: sequence}
	}
	quick := []CompletionEvidence{
		pass(EvidenceKindScope, "", 1),
		pass(EvidenceKindRequiredCheck, "unit", 1),
		pass(EvidenceKindIntegration, "", 1),
	}
	tests := []struct {
		name          string
		mode          WorkMode
		evidence      []CompletionEvidence
		wantSatisfied bool
		wantMissing   int
	}{
		{"quick complete", WorkModeQuick, quick, true, 0},
		{"required check fails latest", WorkModeQuick, append(quick, CompletionEvidence{CandidateID: candidate.ID, CommitSHA: candidate.CommitSHA, Kind: EvidenceKindRequiredCheck, RequiredCheck: "unit", Result: EvidenceResultFail, Source: EvidenceSourceControlPlaneChecker, Sequence: 2}), false, 1},
		{"old candidate ignored", WorkModeQuick, append(append([]CompletionEvidence(nil), quick[:2]...), CompletionEvidence{CandidateID: "candidate-1", CommitSHA: candidate.CommitSHA, Kind: EvidenceKindIntegration, Result: EvidenceResultPass, Source: EvidenceSourceControlPlaneChecker, Sequence: 9}), false, 1},
		{"expired evidence ignored", WorkModeQuick, append(append([]CompletionEvidence(nil), quick[:2]...), CompletionEvidence{CandidateID: candidate.ID, CommitSHA: candidate.CommitSHA, Kind: EvidenceKindIntegration, Result: EvidenceResultPass, Source: EvidenceSourceControlPlaneChecker, Sequence: 1, ExpiresAt: now}), false, 1},
		{"newer expired result supersedes older pass", WorkModeQuick, append(append([]CompletionEvidence(nil), quick...), CompletionEvidence{CandidateID: candidate.ID, CommitSHA: candidate.CommitSHA, Kind: EvidenceKindIntegration, Result: EvidenceResultPass, Source: EvidenceSourceControlPlaneChecker, Sequence: 2, ExpiresAt: now}), false, 1},
		{"standard needs review", WorkModeStandard, quick, false, 1},
		{"standard rejects builder review", WorkModeStandard, append(quick, CompletionEvidence{CandidateID: candidate.ID, CommitSHA: candidate.CommitSHA, Kind: EvidenceKindReview, Result: EvidenceResultPass, Source: EvidenceSourceReviewAdapter, SourceSessionID: "builder", Sequence: 1}), false, 1},
		{"standard complete", WorkModeStandard, append(quick, CompletionEvidence{CandidateID: candidate.ID, CommitSHA: candidate.CommitSHA, Kind: EvidenceKindReview, Result: EvidenceResultPass, Source: EvidenceSourceReviewAdapter, SourceSessionID: "reviewer", Sequence: 1}), true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateCompletion(tt.mode, candidate, []string{"unit"}, tt.evidence, now)
			if got.Satisfied != tt.wantSatisfied || len(got.Missing) != tt.wantMissing {
				t.Fatalf("EvaluateCompletion() = %+v, want satisfied=%v missing=%d", got, tt.wantSatisfied, tt.wantMissing)
			}
			if !tt.wantSatisfied && got.Reason != ReasonEvidenceIncomplete {
				t.Fatalf("reason = %q, want %q", got.Reason, ReasonEvidenceIncomplete)
			}
		})
	}
}

func TestValidateEvidenceBinding(t *testing.T) {
	candidate := CompletionCandidate{ID: "c1", CommitSHA: strings.Repeat("A", 40)}
	tests := []struct {
		name     string
		evidence CompletionEvidence
		want     ReasonCode
	}{
		{"same candidate normalized sha", CompletionEvidence{CandidateID: "c1", CommitSHA: strings.Repeat("a", 40)}, ReasonNone},
		{"wrong candidate", CompletionEvidence{CandidateID: "c2", CommitSHA: strings.Repeat("a", 40)}, ReasonCandidateMismatch},
		{"wrong sha", CompletionEvidence{CandidateID: "c1", CommitSHA: strings.Repeat("b", 40)}, ReasonCandidateMismatch},
		{"short sha", CompletionEvidence{CandidateID: "c1", CommitSHA: "abc"}, ReasonCandidateMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateEvidenceBinding(candidate, tt.evidence); got != tt.want {
				t.Fatalf("ValidateEvidenceBinding() = %q, want %q", got, tt.want)
			}
		})
	}
}
