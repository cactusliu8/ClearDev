package cleardev

import "testing"

func TestValidateRequirementVersionTransition(t *testing.T) {
	valid := RequirementVersionTransitionInput{HasContent: true, HasDigest: true, TrustedHumanDecision: true, HasReplacement: true}
	tests := []struct {
		name string
		from RequirementVersionStatus
		to   RequirementVersionStatus
		in   RequirementVersionTransitionInput
		want TransitionDecision
	}{
		{"draft to pending confirmation", RequirementVersionStatusDraft, RequirementVersionStatusPendingConfirmation, valid, allowedTransition()},
		{"pending to confirmed", RequirementVersionStatusPendingConfirmation, RequirementVersionStatusConfirmed, valid, allowedTransition()},
		{"pending to rejected", RequirementVersionStatusPendingConfirmation, RequirementVersionStatusRejected, valid, allowedTransition()},
		{"confirmed to superseded", RequirementVersionStatusConfirmed, RequirementVersionStatusSuperseded, valid, allowedTransition()},
		{"draft needs text", RequirementVersionStatusDraft, RequirementVersionStatusPendingConfirmation, RequirementVersionTransitionInput{HasDigest: true}, rejectedTransition(ReasonPreconditionNotMet)},
		{"confirmation needs trusted decision", RequirementVersionStatusPendingConfirmation, RequirementVersionStatusConfirmed, RequirementVersionTransitionInput{}, rejectedTransition(ReasonHumanDecisionRequired)},
		{"replacement needs trusted decision", RequirementVersionStatusConfirmed, RequirementVersionStatusSuperseded, RequirementVersionTransitionInput{HasReplacement: true}, rejectedTransition(ReasonHumanDecisionRequired)},
		{"replacement must exist", RequirementVersionStatusConfirmed, RequirementVersionStatusSuperseded, RequirementVersionTransitionInput{TrustedHumanDecision: true}, rejectedTransition(ReasonPreconditionNotMet)},
		{"confirmed content is immutable", RequirementVersionStatusConfirmed, RequirementVersionStatusConfirmed, valid, rejectedTransition(ReasonRequirementImmutable)},
		{"rejected is terminal", RequirementVersionStatusRejected, RequirementVersionStatusDraft, valid, rejectedTransition(ReasonInvalidTransition)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateRequirementVersionTransition(tt.from, tt.to, tt.in); got != tt.want {
				t.Fatalf("ValidateRequirementVersionTransition() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidateDevelopmentTaskTransition(t *testing.T) {
	valid := DevelopmentTaskTransitionInput{
		Mode: WorkModeQuick, IsCurrentRequirementVersion: true, HasCurrentCandidate: true,
		CompletionSatisfied: true, EvidenceFailed: true, MaxReworkCount: 2,
	}
	tests := []struct {
		name string
		from DevelopmentTaskStatus
		to   DevelopmentTaskStatus
		in   DevelopmentTaskTransitionInput
		want TransitionDecision
	}{
		{"planned to running", DevelopmentTaskStatusPlanned, DevelopmentTaskStatusRunning, valid, allowedTransition()},
		{"running to review", DevelopmentTaskStatusRunning, DevelopmentTaskStatusReview, valid, allowedTransition()},
		{"review to done", DevelopmentTaskStatusReview, DevelopmentTaskStatusDone, valid, allowedTransition()},
		{"review to rework", DevelopmentTaskStatusReview, DevelopmentTaskStatusRework, valid, allowedTransition()},
		{"exhausted review is accepted human escalation with stable reason", DevelopmentTaskStatusReview, DevelopmentTaskStatusNeedsHuman, DevelopmentTaskTransitionInput{EvidenceFailed: true, MaxReworkCount: 1, CurrentReworkCount: 1}, allowedTransitionWithReason(ReasonReworkLimitReached)},
		{"review may manually pause for human", DevelopmentTaskStatusReview, DevelopmentTaskStatusNeedsHuman, valid, allowedTransition()},
		{"rework restarts and increments once", DevelopmentTaskStatusRework, DevelopmentTaskStatusRunning, DevelopmentTaskTransitionInput{Mode: WorkModeQuick, IsCurrentRequirementVersion: true, MaxReworkCount: 2, CurrentReworkCount: 1}, TransitionDecision{Allowed: true, NextReworkCount: 2}},
		{"active to blocked", DevelopmentTaskStatusReview, DevelopmentTaskStatusBlocked, valid, allowedTransition()},
		{"active to cancelled", DevelopmentTaskStatusPlanned, DevelopmentTaskStatusCancelled, valid, allowedTransition()},
		{"historical done task remains terminal", DevelopmentTaskStatusDone, DevelopmentTaskStatusCancelled, DevelopmentTaskTransitionInput{}, rejectedTransition(ReasonInvalidTransition)},
		{"current done task remains terminal", DevelopmentTaskStatusDone, DevelopmentTaskStatusCancelled, valid, rejectedTransition(ReasonInvalidTransition)},
		{"blocked restores exact state", DevelopmentTaskStatusBlocked, DevelopmentTaskStatusReview, DevelopmentTaskTransitionInput{IsCurrentRequirementVersion: true, SuspendedFrom: DevelopmentTaskStatusReview}, allowedTransition()},
		{"needs human restores exact state", DevelopmentTaskStatusNeedsHuman, DevelopmentTaskStatusRework, DevelopmentTaskTransitionInput{IsCurrentRequirementVersion: true, SuspendedFrom: DevelopmentTaskStatusRework}, allowedTransition()},
		{"start rejects historical version", DevelopmentTaskStatusPlanned, DevelopmentTaskStatusRunning, DevelopmentTaskTransitionInput{Mode: WorkModeQuick}, rejectedTransition(ReasonPreconditionNotMet)},
		{"start rejects cancelled requirement", DevelopmentTaskStatusPlanned, DevelopmentTaskStatusRunning, DevelopmentTaskTransitionInput{Mode: WorkModeQuick, IsCurrentRequirementVersion: true, RequirementCancelled: true}, rejectedTransition(ReasonRequirementCancelled)},
		{"unsupported mode rejected", DevelopmentTaskStatusPlanned, DevelopmentTaskStatusRunning, DevelopmentTaskTransitionInput{Mode: "PARALLEL", IsCurrentRequirementVersion: true}, rejectedTransition(ReasonModeNotImplemented)},
		{"review needs candidate", DevelopmentTaskStatusRunning, DevelopmentTaskStatusReview, DevelopmentTaskTransitionInput{}, rejectedTransition(ReasonPreconditionNotMet)},
		{"review done needs evidence", DevelopmentTaskStatusReview, DevelopmentTaskStatusDone, DevelopmentTaskTransitionInput{}, rejectedTransition(ReasonEvidenceIncomplete)},
		{"review rework cannot exceed limit", DevelopmentTaskStatusReview, DevelopmentTaskStatusRework, DevelopmentTaskTransitionInput{EvidenceFailed: true, MaxReworkCount: 0}, rejectedTransition(ReasonReworkLimitReached)},
		{"restart cannot exceed limit", DevelopmentTaskStatusRework, DevelopmentTaskStatusRunning, DevelopmentTaskTransitionInput{Mode: WorkModeQuick, IsCurrentRequirementVersion: true, MaxReworkCount: 1, CurrentReworkCount: 1}, rejectedTransition(ReasonReworkLimitReached)},
		{"resume repeats cancellation check", DevelopmentTaskStatusBlocked, DevelopmentTaskStatusRunning, DevelopmentTaskTransitionInput{IsCurrentRequirementVersion: true, RequirementCancelled: true, SuspendedFrom: DevelopmentTaskStatusRunning}, rejectedTransition(ReasonRequirementCancelled)},
		{"done is terminal", DevelopmentTaskStatusDone, DevelopmentTaskStatusBlocked, valid, rejectedTransition(ReasonInvalidTransition)},
		{"cancelled is terminal", DevelopmentTaskStatusCancelled, DevelopmentTaskStatusPlanned, valid, rejectedTransition(ReasonInvalidTransition)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateDevelopmentTaskTransition(tt.from, tt.to, tt.in); got != tt.want {
				t.Fatalf("ValidateDevelopmentTaskTransition() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
