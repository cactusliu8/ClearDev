package cleardev

import (
	"strings"
	"time"
)

// EvidenceKind identifies the fact a trusted producer has evaluated.
type EvidenceKind string

const (
	EvidenceKindScope         EvidenceKind = "SCOPE"
	EvidenceKindRequiredCheck EvidenceKind = "REQUIRED_CHECK"
	EvidenceKindIntegration   EvidenceKind = "INTEGRATION"
	EvidenceKindReview        EvidenceKind = "REVIEW"
)

// EvidenceResult is the result a trusted producer recorded.
type EvidenceResult string

const (
	EvidenceResultPass EvidenceResult = "PASS"
	EvidenceResultFail EvidenceResult = "FAIL"
)

// EvidenceSource identifies the trusted internal path that wrote evidence.
type EvidenceSource string

const (
	EvidenceSourceControlPlaneChecker EvidenceSource = "CONTROL_PLANE_CHECKER"
	EvidenceSourceReviewAdapter       EvidenceSource = "REVIEW_ADAPTER"
)

// CompletionCandidate is the immutable candidate identity needed to derive a
// completion result. Persistent candidate records belong to the storage facts.
type CompletionCandidate struct {
	ID               string
	CommitSHA        string
	BuilderSessionID string
}

// CompletionEvidence is the evidence projection needed for a completion read.
// Sequence orders repeated records for the same evidence key; a larger sequence
// supersedes only the effective read result and never deletes history.
type CompletionEvidence struct {
	CandidateID     string
	CommitSHA       string
	Kind            EvidenceKind
	RequiredCheck   string
	Result          EvidenceResult
	Source          EvidenceSource
	SourceSessionID string
	Sequence        int64
	ExpiresAt       time.Time
}

// EvidenceRequirement describes one missing requirement in a completion read.
type EvidenceRequirement struct {
	Kind          EvidenceKind
	RequiredCheck string
}

// CompletionDecision is a derived read result. It is not a stored display
// status and is recalculated from the current candidate and appended evidence.
type CompletionDecision struct {
	Satisfied bool
	Reason    ReasonCode
	Missing   []EvidenceRequirement
}

// ValidateEvidenceBinding checks the invariant required before appending an
// evidence record to a work item: it must name the current candidate and its
// complete, equal commit SHA.
func ValidateEvidenceBinding(candidate CompletionCandidate, evidence CompletionEvidence) ReasonCode {
	if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(evidence.CandidateID) == "" || candidate.ID != evidence.CandidateID {
		return ReasonCandidateMismatch
	}
	candidateSHA, err := NormalizeCommitSHA(candidate.CommitSHA)
	if err != nil {
		return ReasonCandidateMismatch
	}
	evidenceSHA, err := NormalizeCommitSHA(evidence.CommitSHA)
	if err != nil || evidenceSHA != candidateSHA {
		return ReasonCandidateMismatch
	}
	return ReasonNone
}

// EvaluateCompletion determines whether the current candidate has all frozen
// QUICK or STANDARD evidence. Evidence for another candidate, a different
// commit, an expired record, or an untrusted review cannot satisfy a gate.
func EvaluateCompletion(mode WorkMode, candidate CompletionCandidate, requiredChecks []string, evidence []CompletionEvidence, now time.Time) CompletionDecision {
	if mode != WorkModeQuick && mode != WorkModeStandard {
		return CompletionDecision{Reason: ReasonEvidenceIncomplete}
	}
	if _, err := NormalizeCommitSHA(candidate.CommitSHA); err != nil || strings.TrimSpace(candidate.ID) == "" || (mode == WorkModeStandard && strings.TrimSpace(candidate.BuilderSessionID) == "") {
		return CompletionDecision{Reason: ReasonEvidenceIncomplete}
	}
	latest := latestEvidenceForCandidate(candidate, evidence)
	requirements := make([]EvidenceRequirement, 0, len(requiredChecks)+3)
	requirements = append(requirements,
		EvidenceRequirement{Kind: EvidenceKindScope},
		EvidenceRequirement{Kind: EvidenceKindIntegration},
	)
	for _, name := range requiredChecks {
		name = strings.TrimSpace(name)
		if name != "" {
			requirements = append(requirements, EvidenceRequirement{Kind: EvidenceKindRequiredCheck, RequiredCheck: name})
		}
	}
	if mode == WorkModeStandard {
		requirements = append(requirements, EvidenceRequirement{Kind: EvidenceKindReview})
	}

	missing := make([]EvidenceRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		evidence, ok := latest[evidenceRequirementKey(requirement)]
		if !ok || evidenceExpired(evidence, now) || evidence.Result != EvidenceResultPass || !evidenceSatisfiesRequirement(requirement, candidate, evidence) {
			missing = append(missing, requirement)
		}
	}
	if len(missing) != 0 {
		return CompletionDecision{Reason: ReasonEvidenceIncomplete, Missing: missing}
	}
	return CompletionDecision{Satisfied: true, Reason: ReasonNone}
}

func latestEvidenceForCandidate(candidate CompletionCandidate, evidence []CompletionEvidence) map[string]CompletionEvidence {
	latest := make(map[string]CompletionEvidence)
	for _, record := range evidence {
		if ValidateEvidenceBinding(candidate, record) != ReasonNone {
			continue
		}
		key := evidenceKey(record)
		if existing, ok := latest[key]; !ok || record.Sequence >= existing.Sequence {
			latest[key] = record
		}
	}
	return latest
}

func evidenceExpired(record CompletionEvidence, now time.Time) bool {
	return !record.ExpiresAt.IsZero() && !now.Before(record.ExpiresAt)
}

func evidenceKey(record CompletionEvidence) string {
	return evidenceRequirementKey(EvidenceRequirement{Kind: record.Kind, RequiredCheck: record.RequiredCheck})
}

func evidenceRequirementKey(requirement EvidenceRequirement) string {
	if requirement.Kind == EvidenceKindRequiredCheck {
		return string(requirement.Kind) + "\x00" + requirement.RequiredCheck
	}
	return string(requirement.Kind)
}

func evidenceSatisfiesRequirement(requirement EvidenceRequirement, candidate CompletionCandidate, evidence CompletionEvidence) bool {
	if requirement.Kind == EvidenceKindReview {
		return evidence.Source == EvidenceSourceReviewAdapter && evidence.SourceSessionID != "" && evidence.SourceSessionID != candidate.BuilderSessionID
	}
	return evidence.Source == EvidenceSourceControlPlaneChecker
}
