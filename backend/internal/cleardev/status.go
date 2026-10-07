package cleardev

// RequirementVersionStatus is the public lifecycle of an immutable requirement
// version. Storage maps the 0106 IN_REVIEW and APPROVED values at its boundary.
type RequirementVersionStatus string

const (
	RequirementVersionStatusDraft               RequirementVersionStatus = "DRAFT"
	RequirementVersionStatusPendingConfirmation RequirementVersionStatus = "PENDING_CONFIRMATION"
	RequirementVersionStatusConfirmed           RequirementVersionStatus = "CONFIRMED"
	RequirementVersionStatusRejected            RequirementVersionStatus = "REJECTED"
	RequirementVersionStatusSuperseded          RequirementVersionStatus = "SUPERSEDED"
)

// DevelopmentTaskStatus is the public lifecycle of one development task.
// READY deliberately is not a new public state; 0106 READY rows read as
// PLANNED and storage only uses it for compatible compare-and-swap updates.
type DevelopmentTaskStatus string

const (
	DevelopmentTaskStatusPlanned    DevelopmentTaskStatus = "PLANNED"
	DevelopmentTaskStatusRunning    DevelopmentTaskStatus = "RUNNING"
	DevelopmentTaskStatusReview     DevelopmentTaskStatus = "REVIEW"
	DevelopmentTaskStatusRework     DevelopmentTaskStatus = "REWORK"
	DevelopmentTaskStatusNeedsHuman DevelopmentTaskStatus = "NEEDS_HUMAN"
	DevelopmentTaskStatusBlocked    DevelopmentTaskStatus = "BLOCKED"
	DevelopmentTaskStatusDone       DevelopmentTaskStatus = "DONE"
	DevelopmentTaskStatusCancelled  DevelopmentTaskStatus = "CANCELLED"
)

// WorkMode controls which evidence is needed before a development task is DONE.
type WorkMode string

const (
	WorkModeQuick    WorkMode = "QUICK"
	WorkModeStandard WorkMode = "STANDARD"
	// WorkModeParallel only labels how a complex execution run dispatches its
	// Builders. Individual work items keep WorkModeStandard evidence rules.
	WorkModeParallel WorkMode = "PARALLEL"
)

// ReasonCode is a stable, persistable reason for a rejected action or an
// accepted exceptional transition.
type ReasonCode string

const (
	ReasonNone                     ReasonCode = ""
	ReasonInvalidTransition        ReasonCode = "INVALID_TRANSITION"
	ReasonPreconditionNotMet       ReasonCode = "PRECONDITION_NOT_MET"
	ReasonHumanDecisionRequired    ReasonCode = "HUMAN_DECISION_REQUIRED"
	ReasonRequirementImmutable     ReasonCode = "REQUIREMENT_IMMUTABLE"
	ReasonModeNotImplemented       ReasonCode = "MODE_NOT_IMPLEMENTED"
	ReasonCandidateMismatch        ReasonCode = "CANDIDATE_MISMATCH"
	ReasonEvidenceIncomplete       ReasonCode = "EVIDENCE_INCOMPLETE"
	ReasonReworkLimitReached       ReasonCode = "REWORK_LIMIT_REACHED"
	ReasonFinalReviewReworkLimit   ReasonCode = "FINAL_REVIEW_REWORK_LIMIT"
	ReasonRequirementCancelled     ReasonCode = "REQUIREMENT_CANCELLED"
	ReasonUserCancelled            ReasonCode = "USER_CANCELLED"
	ReasonLegacyMultipleConfirmed  ReasonCode = "LEGACY_MULTIPLE_CONFIRMED"
	ReasonLegacyOpenVersionRetired ReasonCode = "LEGACY_OPEN_VERSION_RETIRED"
	ReasonLegacyCancelled          ReasonCode = "LEGACY_CANCELLED"
	ReasonRejectedByDesktopHuman   ReasonCode = "REJECTED_BY_DESKTOP_HUMAN"
	ReasonHumanDecisionInvalid     ReasonCode = "HUMAN_DECISION_INVALID"
	ReasonDirectionChangeStopped   ReasonCode = "DIRECTION_CHANGE_STOPPED"
	ReasonModelNotAvailable        ReasonCode = "MODEL_NOT_AVAILABLE"
	ReasonLoginRequired            ReasonCode = "LOGIN_REQUIRED"
	ReasonQuotaExhausted           ReasonCode = "QUOTA_EXHAUSTED"
	ReasonRateLimited              ReasonCode = "RATE_LIMITED"
	ReasonProviderUnavailable      ReasonCode = "PROVIDER_UNAVAILABLE"
	ReasonDriverIncompatible       ReasonCode = "DRIVER_INCOMPATIBLE"
)

// TransitionDecision is the complete pure result of considering a state action.
// Storage atomically persists an accepted or rejected event from this decision.
type TransitionDecision struct {
	Allowed         bool
	Reason          ReasonCode
	NextReworkCount int
}

func allowedTransition() TransitionDecision {
	return TransitionDecision{Allowed: true, Reason: ReasonNone}
}

func allowedTransitionWithReason(reason ReasonCode) TransitionDecision {
	return TransitionDecision{Allowed: true, Reason: reason}
}

func rejectedTransition(reason ReasonCode) TransitionDecision {
	return TransitionDecision{Reason: reason}
}

// RequirementVersionTransitionInput supplies the facts that are not part of
// the current or requested version status.
type RequirementVersionTransitionInput struct {
	HasContent           bool
	HasDigest            bool
	TrustedHumanDecision bool
	HasReplacement       bool
}

// ValidateRequirementVersionTransition applies the frozen v3 requirement
// version transition table. Replacing a confirmed version is coordinated by
// storage in the same transaction as confirming the newer version.
func ValidateRequirementVersionTransition(from, to RequirementVersionStatus, in RequirementVersionTransitionInput) TransitionDecision {
	if from == RequirementVersionStatusConfirmed && to == RequirementVersionStatusConfirmed {
		return rejectedTransition(ReasonRequirementImmutable)
	}
	switch {
	case from == RequirementVersionStatusDraft && to == RequirementVersionStatusPendingConfirmation:
		if !in.HasContent || !in.HasDigest {
			return rejectedTransition(ReasonPreconditionNotMet)
		}
		return allowedTransition()
	case from == RequirementVersionStatusPendingConfirmation && (to == RequirementVersionStatusConfirmed || to == RequirementVersionStatusRejected):
		if !in.TrustedHumanDecision {
			return rejectedTransition(ReasonHumanDecisionRequired)
		}
		return allowedTransition()
	case from == RequirementVersionStatusConfirmed && to == RequirementVersionStatusSuperseded:
		if !in.TrustedHumanDecision {
			return rejectedTransition(ReasonHumanDecisionRequired)
		}
		if !in.HasReplacement {
			return rejectedTransition(ReasonPreconditionNotMet)
		}
		return allowedTransition()
	default:
		return rejectedTransition(ReasonInvalidTransition)
	}
}

// DevelopmentTaskTransitionInput supplies the non-status facts used for one
// task action. Current-version and cancellation checks are repeated when a
// blocked or human-paused task is resumed.
type DevelopmentTaskTransitionInput struct {
	Mode                        WorkMode
	IsCurrentRequirementVersion bool
	RequirementCancelled        bool
	HasCurrentCandidate         bool
	CompletionSatisfied         bool
	EvidenceFailed              bool
	MaxReworkCount              int
	CurrentReworkCount          int
	SuspendedFrom               DevelopmentTaskStatus
}

// ValidateDevelopmentTaskTransition applies the frozen v3 task transition
// table. A successful exhausted REVIEW -> NEEDS_HUMAN decision carries the
// stable REWORK_LIMIT_REACHED reason so its accepted event is auditable.
func ValidateDevelopmentTaskTransition(from, to DevelopmentTaskStatus, in DevelopmentTaskTransitionInput) TransitionDecision {
	if in.MaxReworkCount < 0 || in.CurrentReworkCount < 0 || in.CurrentReworkCount > in.MaxReworkCount {
		return rejectedTransition(ReasonPreconditionNotMet)
	}
	if to == DevelopmentTaskStatusCancelled {
		if isDevelopmentTaskContinuable(from) {
			return allowedTransition()
		}
		return rejectedTransition(ReasonInvalidTransition)
	}
	if isDevelopmentTaskTerminal(from) {
		return rejectedTransition(ReasonInvalidTransition)
	}
	if to == DevelopmentTaskStatusBlocked || to == DevelopmentTaskStatusNeedsHuman {
		if !isDevelopmentTaskContinuable(from) {
			return rejectedTransition(ReasonInvalidTransition)
		}
		if from == DevelopmentTaskStatusReview && to == DevelopmentTaskStatusNeedsHuman && in.EvidenceFailed && in.CurrentReworkCount >= in.MaxReworkCount {
			return allowedTransitionWithReason(ReasonReworkLimitReached)
		}
		return allowedTransition()
	}
	if (from == DevelopmentTaskStatusBlocked || from == DevelopmentTaskStatusNeedsHuman) && to == in.SuspendedFrom && isDevelopmentTaskContinuable(in.SuspendedFrom) {
		if in.RequirementCancelled {
			return rejectedTransition(ReasonRequirementCancelled)
		}
		if !in.IsCurrentRequirementVersion {
			return rejectedTransition(ReasonPreconditionNotMet)
		}
		return allowedTransition()
	}
	switch {
	case from == DevelopmentTaskStatusPlanned && to == DevelopmentTaskStatusRunning:
		if in.Mode != WorkModeQuick && in.Mode != WorkModeStandard {
			return rejectedTransition(ReasonModeNotImplemented)
		}
		if in.RequirementCancelled {
			return rejectedTransition(ReasonRequirementCancelled)
		}
		if !in.IsCurrentRequirementVersion {
			return rejectedTransition(ReasonPreconditionNotMet)
		}
		return allowedTransition()
	case from == DevelopmentTaskStatusRunning && to == DevelopmentTaskStatusReview:
		if !in.HasCurrentCandidate {
			return rejectedTransition(ReasonPreconditionNotMet)
		}
		return allowedTransition()
	case from == DevelopmentTaskStatusReview && to == DevelopmentTaskStatusDone:
		if !in.CompletionSatisfied {
			return rejectedTransition(ReasonEvidenceIncomplete)
		}
		return allowedTransition()
	case from == DevelopmentTaskStatusReview && to == DevelopmentTaskStatusRework:
		if !in.EvidenceFailed {
			return rejectedTransition(ReasonPreconditionNotMet)
		}
		if in.CurrentReworkCount >= in.MaxReworkCount {
			return rejectedTransition(ReasonReworkLimitReached)
		}
		return allowedTransition()
	case from == DevelopmentTaskStatusRework && to == DevelopmentTaskStatusRunning:
		if in.RequirementCancelled {
			return rejectedTransition(ReasonRequirementCancelled)
		}
		if !in.IsCurrentRequirementVersion {
			return rejectedTransition(ReasonPreconditionNotMet)
		}
		if in.CurrentReworkCount >= in.MaxReworkCount {
			return rejectedTransition(ReasonReworkLimitReached)
		}
		decision := allowedTransition()
		decision.NextReworkCount = in.CurrentReworkCount + 1
		return decision
	default:
		return rejectedTransition(ReasonInvalidTransition)
	}
}

func isDevelopmentTaskContinuable(status DevelopmentTaskStatus) bool {
	switch status {
	case DevelopmentTaskStatusPlanned, DevelopmentTaskStatusRunning,
		DevelopmentTaskStatusReview, DevelopmentTaskStatusRework:
		return true
	default:
		return false
	}
}

func isDevelopmentTaskTerminal(status DevelopmentTaskStatus) bool {
	return status == DevelopmentTaskStatusDone || status == DevelopmentTaskStatusCancelled
}
