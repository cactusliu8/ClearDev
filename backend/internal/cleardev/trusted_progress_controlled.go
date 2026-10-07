package cleardev

import (
	"sort"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ControlledWork describes one current target; it grants no execution authority.
type ControlledWork struct {
	State                 string              `json:"state" enum:"WAITING_RETRY,RETRY_ELIGIBLE,AWAITING_USER,OBSERVING,DELIVERY_UNCONFIRMED,RECOVERY_PENDING,RECOVERY_UNCONFIRMED,RECOVERY_FAILED,BUDGET_BLOCKED,FAILED,UNKNOWN,READY"`
	ReasonCode            ReasonCode          `json:"reasonCode"`
	NextOwner             TrustedOwner        `json:"nextOwner"`
	RequirementVersionID  string              `json:"requirementVersionId,omitempty"`
	ExecutionRunID        string              `json:"executionRunId,omitempty"`
	DevelopmentTaskID     string              `json:"developmentTaskId,omitempty"`
	TaskMappingID         string              `json:"taskMappingId,omitempty"`
	DispatchID            string              `json:"dispatchId,omitempty"`
	LogicalStepID         string              `json:"logicalStepId,omitempty"`
	StepCategory          AgentStepCategory   `json:"stepCategory,omitempty"`
	AttemptID             string              `json:"attemptId,omitempty"`
	AttemptNumber         *int64              `json:"attemptNumber"`
	RoleBindingID         string              `json:"roleBindingId,omitempty"`
	Role                  string              `json:"role,omitempty"`
	AOSessionID           string              `json:"aoSessionId,omitempty"`
	ClientMessageID       string              `json:"clientMessageId,omitempty"`
	TurnID                string              `json:"turnId,omitempty"`
	FailureEventID        string              `json:"failureEventId,omitempty"`
	EvidenceID            string              `json:"evidenceId,omitempty"`
	PreflightID           string              `json:"preflightId,omitempty"`
	CandidateID           string              `json:"candidateId,omitempty"`
	CandidateSHA          string              `json:"candidateSha,omitempty"`
	ReviewID              string              `json:"reviewId,omitempty"`
	RecoveryRequestID     string              `json:"recoveryRequestId,omitempty"`
	RecoveryOperationID   string              `json:"recoveryOperationId,omitempty"`
	RecoveryOutcome       string              `json:"recoveryOutcome,omitempty"`
	OriginalRoleBindingID string              `json:"originalRoleBindingId,omitempty"`
	OriginalSessionID     string              `json:"originalSessionId,omitempty"`
	ReplacementResultID   string              `json:"replacementResultId,omitempty"`
	RetryAt               *time.Time          `json:"retryAt"`
	StepBudget            *MessageBudgetUsage `json:"stepBudget" nullable:"true"`
	RoleBudget            *MessageBudgetUsage `json:"roleBudget" nullable:"true"`
}

// ControlledProgressFacts contains only selected lightweight evidence for one business target.
type ControlledProgressFacts struct {
	Target         ControlledWork
	Step           *AgentStep
	Attempts       []AgentStepAttemptView
	Preflight      *ControlledPreflight
	Recovery       *FixedRecoveryEvidence
	BudgetVersion  MessageBudgetVersion
	ProposalID     string
	BindingUnknown bool
	BudgetUnknown  string
}

func controlledState(w *ControlledWork, state string, reason ReasonCode, role, action string) {
	w.State, w.ReasonCode, w.NextOwner = state, reason, TrustedOwner{Role: role, Action: action}
}

func deriveControlledWork(f ControlledProgressFacts, now time.Time) (ControlledWork, bool) {
	w := f.Target
	if r := f.Recovery; r != nil {
		w.RecoveryRequestID = r.Request.ID
		w.OriginalRoleBindingID, w.OriginalSessionID = r.Request.RoleBindingID, r.Request.SessionID
		if r.Claim != nil {
			w.RecoveryOperationID = r.Claim.OperationID
		}
		if r.Result != nil {
			w.RecoveryOutcome = r.Result.Outcome
		}
		if r.ReviewResult != nil {
			w.ReplacementResultID = r.ReviewResult.ResultID
		}
	}
	set := func(state string, reason ReasonCode, role, action string) (ControlledWork, bool) {
		controlledState(&w, state, reason, role, action)
		return w, true
	}
	unknown := func(reason ReasonCode) (ControlledWork, bool) {
		return set("UNKNOWN", reason, TrustedOwnerHuman, TrustedActionUnblock)
	}
	if f.BindingUnknown {
		return unknown("REPLACEMENT_BINDING_UNKNOWN")
	}
	seenAttempts := map[int64]bool{}
	var attempt *AgentStepAttemptView
	if len(f.Attempts) > 2 {
		return unknown("ATTEMPT_BINDING_UNKNOWN")
	}
	for i := range f.Attempts {
		a := &f.Attempts[i]
		if seenAttempts[a.AttemptNumber] {
			return unknown("ATTEMPT_BINDING_UNKNOWN")
		}
		seenAttempts[a.AttemptNumber] = true
		if a.LogicalStepID != w.LogicalStepID || a.StepCategory != w.StepCategory || a.AttemptNumber < 1 || a.AttemptNumber > 2 {
			return unknown("ATTEMPT_BINDING_UNKNOWN")
		}
		if attempt == nil || a.AttemptNumber > attempt.AttemptNumber {
			attempt = a
		}
	}
	if attempt != nil {
		if f.Step == nil || attempt.PromptSHA256 != f.Step.PromptSHA256 {
			return unknown("ATTEMPT_BINDING_UNKNOWN")
		}
		if attempt.AttemptNumber == 2 {
			var first *AgentStepAttemptView
			for i := range f.Attempts {
				if f.Attempts[i].AttemptNumber == 1 {
					first = &f.Attempts[i]
				}
			}
			regularRetry := first != nil && first.Retryable && FixedRecoveryFailureAllowed(first.FailureCategory) &&
				(first.SendStatus == AgentAttemptFailed || first.SendStatus == AgentAttemptInterrupted) && first.TurnID != "" &&
				(first.TurnState == domain.TurnStateFailed || first.TurnState == domain.TurnStateInterrupted)
			preSendRetry := first != nil && FailedBeforeSendViewValid(*first) && first.AOSessionID == attempt.AOSessionID && first.RoleBindingID == attempt.RoleBindingID
			if first == nil || (!regularRetry && !preSendRetry) || attempt.ClientMessageID == first.ClientMessageID || attempt.TriggerFailureEventID == "" || attempt.TriggerFailureEventID != first.LastEventID || attempt.PromptSHA256 != first.PromptSHA256 {
				return unknown("ATTEMPT_BINDING_UNKNOWN")
			}
		}
		if attempt.AttemptNumber == 2 && (attempt.RoleBindingID != f.Step.RoleBindingID) {
			r := f.Recovery
			if r == nil || r.Claim == nil || r.Result == nil || r.Claim.Action != ComplexRecoveryActionRebuildReviewer || r.Result.Outcome != "PASS" || r.Request.LogicalStepID != f.Step.ID || r.Request.RoleBindingID != f.Step.RoleBindingID || r.Request.FailureEventID != attempt.TriggerFailureEventID || r.Request.PromptSHA256 != attempt.PromptSHA256 || r.Result.RoleBindingID != attempt.RoleBindingID || r.Result.SessionID != attempt.AOSessionID || r.Request.ReviewID != w.ReviewID || r.Request.CandidateID != w.CandidateID || r.Request.CandidateSHA != w.CandidateSHA {
				return unknown("REPLACEMENT_BINDING_UNKNOWN")
			}
		}
		if attempt.RoleBindingID != w.RoleBindingID || attempt.AOSessionID != w.AOSessionID {
			return unknown("ATTEMPT_BINDING_UNKNOWN")
		}
		w.AttemptID, w.AttemptNumber = attempt.ID, &attempt.AttemptNumber
		w.ClientMessageID, w.TurnID, w.EvidenceID = attempt.LastClientMessageID, attempt.TurnID, attempt.LastEventID
		if w.ClientMessageID == "" {
			w.ClientMessageID = attempt.ClientMessageID
		}
		if attempt.SendStatus == AgentAttemptFailedBeforeSend {
			if !FailedBeforeSendViewValid(*attempt) {
				return unknown("FAILED_BEFORE_SEND_EVIDENCE_INVALID")
			}
			w.FailureEventID = attempt.LastEventID
			if attempt.AttemptNumber == 2 {
				return set("FAILED", ReasonCode(AgentAttemptFailedBeforeSend), TrustedOwnerHuman, TrustedActionUnblock)
			}
			// The first proof still passes the budget and preflight projections
			// below; local no-send evidence is not an auth/quota grant.
		}
		terminal := attempt.SendStatus == AgentAttemptFailed || attempt.SendStatus == AgentAttemptInterrupted
		if terminal && attempt.Retryable && attempt.TurnID != "" && attempt.TurnState != domain.TurnStateFailed && attempt.TurnState != domain.TurnStateInterrupted {
			return unknown("TERMINAL_TURN_EVIDENCE_MISSING")
		}
		if terminal {
			w.FailureEventID = attempt.LastEventID
			if attempt.AttemptNumber == 2 || !attempt.Retryable || !FixedRecoveryFailureAllowed(attempt.FailureCategory) || attempt.TurnID == "" {
				return set("FAILED", ReasonCode(attempt.FailureCategory), TrustedOwnerHuman, TrustedActionUnblock)
			}
		}
		switch attempt.SendStatus {
		case AgentAttemptDeliveryUnknown:
			return set("DELIVERY_UNCONFIRMED", "DELIVERY_UNCONFIRMED", TrustedOwnerControlPlane, "RECONCILE_DELIVERY")
		case AgentAttemptSent, AgentAttemptCorrectionSent, AgentAttemptObservationTimedOut:
			return set("OBSERVING", ReasonCode(attempt.SendStatus), TrustedOwnerControlPlane, "OBSERVE_ORIGINAL_TURN")
		case AgentAttemptCompleted:
			if attempt.ParseConclusion == AgentResultParseInvalid && f.Step.SendStatus == AgentStepSendStatusFailed {
				reason := f.Step.ReasonCode
				if reason == ReasonNone {
					reason = "RESULT_PARSE_FAILED"
				}
				return set("FAILED", reason, TrustedOwnerHuman, TrustedActionUnblock)
			}
			if attempt.ParseConclusion == AgentResultParseValid {
				return w, false
			}
			if attempt.ParseConclusion == AgentResultParseUnparsed {
				return set("OBSERVING", "RESULT_AWAITING_PARSE", TrustedOwnerControlPlane, "OBSERVE_ORIGINAL_TURN")
			}
		}
	}
	if f.Recovery != nil {
		r := f.Recovery
		w.RecoveryRequestID = r.Request.ID
		w.OriginalRoleBindingID, w.OriginalSessionID = r.Request.RoleBindingID, r.Request.SessionID
		if r.Claim != nil {
			w.RecoveryOperationID = r.Claim.OperationID
		}
		if r.Refusal != nil {
			return set("RECOVERY_FAILED", ReasonCode(r.Refusal.ReasonCode), TrustedOwnerHuman, TrustedActionUnblock)
		}
		if r.Result != nil {
			w.RecoveryOutcome = r.Result.Outcome
			if r.Result.Outcome == "UNKNOWN" {
				return set("RECOVERY_UNCONFIRMED", ReasonCode(r.Result.ReasonCode), TrustedOwnerControlPlane, "RECONCILE_RECOVERY")
			}
			if r.Result.Outcome != "PASS" {
				return set("RECOVERY_FAILED", ReasonCode(r.Result.ReasonCode), TrustedOwnerHuman, TrustedActionUnblock)
			}
			if r.ReviewResult != nil {
				w.ReplacementResultID = r.ReviewResult.ResultID
				if r.ReviewResult.Verdict == LocalReviewPass {
					return w, false
				}
				return set("FAILED", r.ReviewResult.ReasonCode, TrustedOwnerHuman, TrustedActionUnblock)
			}
		} else if r.Claim != nil {
			return set("RECOVERY_UNCONFIRMED", "RECOVERY_RESULT_MISSING", TrustedOwnerControlPlane, "RECONCILE_RECOVERY")
		}
	}
	if f.BudgetUnknown != "" || f.BudgetVersion == MessageBudgetLegacy {
		return set("BUDGET_BLOCKED", ReasonMessageBudgetUnknown, TrustedOwnerHuman, TrustedActionDecide)
	}
	for _, b := range []*MessageBudgetUsage{w.StepBudget, w.RoleBudget} {
		if b != nil && b.RemainingMessages != nil && *b.RemainingMessages == 0 {
			return set("BUDGET_BLOCKED", ReasonMessageBudgetExhausted, TrustedOwnerHuman, TrustedActionDecide)
		}
		if b != nil && f.Step == nil && b.RemainingSteps != nil && *b.RemainingSteps == 0 {
			return set("BUDGET_BLOCKED", ReasonMessageBudgetExhausted, TrustedOwnerHuman, TrustedActionDecide)
		}
	}
	if f.Preflight != nil && f.Preflight.Outcome == ControlledPreflightFailed {
		p := f.Preflight
		// Existing messages returned above; a new message must honor the current gate.
		w.PreflightID, w.EvidenceID = p.ID, p.ID
		switch p.ReasonCode {
		case "PROVIDER_QUOTA_EXHAUSTED", "PROVIDER_RATE_LIMITED", "RATE_LIMITED", "QUOTA_EXHAUSTED":
			if p.RetryAt != nil {
				w.RetryAt = p.RetryAt
				if now.Before(*p.RetryAt) {
					return set("WAITING_RETRY", p.ReasonCode, TrustedOwnerControlPlane, "WAIT_RETRY_AT")
				}
				return set("RETRY_ELIGIBLE", p.ReasonCode, TrustedOwnerControlPlane, "RECHECK_PREREQUISITES")
			}
		case "PROVIDER_UNAVAILABLE":
			return set("RETRY_ELIGIBLE", p.ReasonCode, TrustedOwnerControlPlane, "RECHECK_PREREQUISITES")
		}
		return set("AWAITING_USER", p.ReasonCode, TrustedOwnerHuman, TrustedActionUnblock)
	}
	if f.Recovery != nil && f.Recovery.Result == nil {
		role := TrustedOwnerRecovery
		if f.ProposalID != "" {
			role = TrustedOwnerControlPlane
		}
		return set("RECOVERY_PENDING", "RECOVERY_PENDING", role, TrustedActionCompleteRecovery)
	}
	if attempt != nil && attempt.SendStatus == AgentAttemptFailedBeforeSend {
		return set("RETRY_ELIGIBLE", ReasonCode(AgentAttemptFailedBeforeSend), TrustedOwnerControlPlane, "RECHECK_PREREQUISITES")
	}
	if attempt != nil && (attempt.SendStatus == AgentAttemptFailed || attempt.SendStatus == AgentAttemptInterrupted) {
		w.RetryAt = attempt.RetryAt
		if w.RetryAt != nil && now.Before(*w.RetryAt) {
			return set("WAITING_RETRY", ReasonCode(attempt.FailureCategory), TrustedOwnerControlPlane, "WAIT_RETRY_AT")
		}
		return set("RETRY_ELIGIBLE", ReasonCode(attempt.FailureCategory), TrustedOwnerControlPlane, "RECHECK_PREREQUISITES")
	}
	if f.Step != nil && attempt == nil && f.Step.SendStatus != AgentStepSendStatusPending {
		return unknown("ATTEMPT_EVIDENCE_MISSING")
	}
	if w.State == "FAILED" {
		return set("FAILED", w.ReasonCode, TrustedOwnerHuman, TrustedActionUnblock)
	}
	return set("READY", ReasonNone, TrustedOwnerControlPlane, "RECHECK_PREREQUISITES")
}

func controlledRank(w ControlledWork) int {
	switch w.State {
	case "AWAITING_USER", "BUDGET_BLOCKED", "FAILED", "RECOVERY_FAILED", "UNKNOWN":
		return 0
	case "DELIVERY_UNCONFIRMED", "RECOVERY_UNCONFIRMED", "OBSERVING":
		return 1
	case "RECOVERY_PENDING", "WAITING_RETRY", "RETRY_ELIGIBLE":
		return 2
	default:
		return 3
	}
}

func applyControlledProgress(summary *TrustedProgressSummary, facts TrustedProgressFacts) {
	summary.ControlledWork = []ControlledWork{}
	for _, f := range facts.Controlled {
		if w, active := deriveControlledWork(f, facts.Now); active {
			summary.ControlledWork = append(summary.ControlledWork, w)
		}
	}
	sort.Slice(summary.ControlledWork, func(i, j int) bool {
		a, b := summary.ControlledWork[i], summary.ControlledWork[j]
		if controlledRank(a) != controlledRank(b) {
			return controlledRank(a) < controlledRank(b)
		}
		if a.DevelopmentTaskID != b.DevelopmentTaskID {
			return a.DevelopmentTaskID < b.DevelopmentTaskID
		}
		if a.LogicalStepID != b.LogicalStepID {
			return a.LogicalStepID < b.LogicalStepID
		}
		return a.RoleBindingID < b.RoleBindingID
	})
	if summary.Phase == TrustedPhaseCancelled || controlledDirectionActive(facts) || summary.PendingRequirementVersionID != "" || len(summary.PendingDecisions) > 0 || len(summary.ControlledWork) == 0 {
		return
	}
	for _, w := range summary.ControlledWork {
		issue := TrustedIssue{Kind: w.State, SubjectType: "CONTROLLED_WORK", SubjectID: w.LogicalStepID, ReasonCode: w.ReasonCode}
		if issue.SubjectID == "" {
			issue.SubjectID = w.RoleBindingID
		}
		summary.CurrentWork = append(summary.CurrentWork, issue)
		if controlledWorkBlocksProgress(facts, w) {
			summary.Blockers = append(summary.Blockers, issue)
		}
	}
	w := summary.ControlledWork[0]
	summary.NextOwner = w.NextOwner
	summary.ReasonCode = w.ReasonCode
	if controlledWorkBlocksProgress(facts, w) {
		summary.Phase = TrustedPhaseBlocked
		summary.Attention = OverallAttentionBlocked
	} else if len(summary.Blockers) == 0 {
		summary.Attention = OverallAttentionNone
		switch w.State {
		case "RECOVERY_PENDING", "RECOVERY_UNCONFIRMED", "WAITING_RETRY", "RETRY_ELIGIBLE":
			if w.PreflightID == "" {
				summary.Phase = TrustedPhaseRecovering
			}
		}
	}
	sortTrustedIssues(summary.CurrentWork)
	sortTrustedIssues(summary.Blockers)
}

// A STANDARD role cannot make product progress while its current controlled
// preflight is waiting for a retry or is eligible to be rechecked. Keep the
// binding resumable, but surface the stopped user-visible flow as BLOCKED.
func controlledWorkBlocksProgress(facts TrustedProgressFacts, w ControlledWork) bool {
	if controlledRank(w) == 0 {
		return true
	}
	if facts.StandardFlow == nil || w.PreflightID == "" {
		return false
	}
	return w.State == "WAITING_RETRY" || w.State == "RETRY_ELIGIBLE"
}

// UncertainTrustedProgress prevents a mixed read from advertising an actionable result.
func UncertainTrustedProgress(summary TrustedProgressSummary) TrustedProgressSummary {
	w := ControlledWork{State: "UNKNOWN", ReasonCode: "CURRENT_BINDINGS_CHANGED", NextOwner: TrustedOwner{Role: TrustedOwnerHuman, Action: TrustedActionUnblock}}
	summary.ControlledWork = []ControlledWork{w}
	summary.Phase, summary.Attention, summary.ReasonCode = TrustedPhaseUnknown, OverallAttentionBlocked, w.ReasonCode
	summary.NextOwner = w.NextOwner
	summary.CurrentWork = []TrustedIssue{{Kind: "UNKNOWN", ReasonCode: w.ReasonCode}}
	summary.Blockers = summary.CurrentWork
	summary.MissingEvidence = []TrustedMissingEvidence{{Kind: "CONSISTENT_CURRENT_BINDINGS"}}
	summary.Explanation = nil
	summary.SortRank = trustedProgressSortRank(summary)
	summary.FactSummarySHA256 = trustedProgressFactHash(summary)
	return summary
}
