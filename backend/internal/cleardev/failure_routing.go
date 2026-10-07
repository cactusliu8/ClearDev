package cleardev

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// WorkflowFailureReceipt preserves failures which otherwise only appeared in a
// daemon log. It references a source; it is never a grant, a candidate or PASS.
// Existing checks, Agent attempts and reviews remain their own authorities.
type WorkflowFailureReceipt struct {
	ID             string    `json:"id"`
	RequirementID  string    `json:"requirementId"`
	ExecutionRunID string    `json:"executionRunId,omitempty"`
	SourceKind     string    `json:"sourceKind"`
	SourceID       string    `json:"sourceId"`
	Role           string    `json:"role"`
	ReasonCode     string    `json:"reasonCode"`
	ProblemCode    string    `json:"problemCode"`
	Summary        string    `json:"summary"`
	Path           string    `json:"path,omitempty"`
	BindingSHA256  string    `json:"bindingSha256"`
	BeforePublish  bool      `json:"beforePublish"`
	ObservedAt     time.Time `json:"observedAt"`
}

// Failure sources and dispositions identify evidence and handling, not grants.
const (
	FailureSourceHandoff   = "CANDIDATE_HANDOFF"
	FailureSourceOperation = "CONTROL_OPERATION"
	FailureSourceRecovery  = "RECOVERY_ADMISSION"

	FailureRepair   = "REPAIR_ORIGINAL"
	FailureRetry    = "RETRY_ORIGINAL"
	FailureEscalate = "ASK_PLANNER"
	FailureObserve  = "WAIT_FOR_EVIDENCE"
	FailureHuman    = "ASK_HUMAN"
	FailureStopped  = "KEEP_STOPPED"
	FailureExisting = "EXISTING_RECOVERY"
)

// FailureSourceKey is stable across refresh, process restart and notifications.
// A successor has its own source identity; it cannot overwrite the old failure.
func FailureSourceKey(requirementID, kind, sourceID string) string {
	digest := sha256.Sum256([]byte(requirementID + "\x00" + kind + "\x00" + sourceID))
	return "failure:" + hex.EncodeToString(digest[:])
}

// FailureRecoveryRequestID binds an idempotent recovery to the exact action and target.
func FailureRecoveryRequestID(requirementID, action, target string) string {
	digest := sha256.Sum256([]byte(requirementID + "\x00" + action + "\x00" + target))
	return "failure-recovery:" + hex.EncodeToString(digest[:])
}

// IsAutomaticFailureRecovery identifies the automatic request namespace, not authority.
func IsAutomaticFailureRecovery(id string) bool { return strings.HasPrefix(id, "failure-recovery:") }

// HandoffFailureBinding hashes only immutable authority, not the mutable
// dispatch status. Service and storage calculate this independently.
func HandoffFailureBinding(run ComplexExecutionRun, task ComplexExecutionTask, dispatch ComplexExecutionDispatch, step AgentStep) string {
	raw, _ := json.Marshal([]string{run.ID, run.DevelopmentRequirementID, run.RequirementVersionID,
		run.RequirementSHA256, run.PlanID, run.PlanSHA256,
		task.ID, dispatch.ID, dispatch.BaseCommitSHA,
		dispatch.BuilderRoleBindingID, step.ID, step.PromptSHA256, step.MessageSHA256})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// Validate checks receipt shape; storage independently verifies its original source.
func (r WorkflowFailureReceipt) Validate() error {
	if r.ID != FailureSourceKey(r.RequirementID, r.SourceKind, r.SourceID) || r.RequirementID == "" || r.SourceID == "" ||
		len(r.SourceID) > 400 || len(r.Summary) > 8000 || r.Summary == "" || len(r.Path) > 1000 ||
		len(r.BindingSHA256) != 64 || strings.Trim(r.BindingSHA256, "0123456789abcdef") != "" || r.ObservedAt.IsZero() {
		return fmt.Errorf("invalid workflow failure receipt")
	}
	switch r.SourceKind {
	case FailureSourceHandoff:
		if r.ExecutionRunID == "" || r.Role != TrustedOwnerBuilder || r.ReasonCode != "CANDIDATE_INVALID" && r.ReasonCode != "GIT_CHECKER_UNAVAILABLE" && r.ReasonCode != "CANDIDATE_NOT_DESCENDANT" && r.ReasonCode != "BUILDER_WORKTREE_DIRTY" {
			return fmt.Errorf("invalid handoff failure source")
		}
	case FailureSourceOperation, FailureSourceRecovery:
		if r.BeforePublish {
			return fmt.Errorf("an operation error cannot prove candidate publication state")
		}
	default:
		return fmt.Errorf("unsupported workflow failure source")
	}
	return nil
}

// WorkflowFailureInput is internal policy input. Eligible is established from
// the native receipt and existing recovery admissions, never an HTTP field.
// A routing decision is still revalidated by the original transactional action.
type WorkflowFailureInput struct {
	PlannerResolved                                                  bool
	Role, ReasonCode, ProblemCode, RecoveryAction, UnavailableReason string
	Current, Eligible, BeforePublish, Repeated                       bool
}

// WorkflowFailureDecision directs handling; the original action still enforces its gates.
type WorkflowFailureDecision struct{ Owner, Action, Reason string }

// RouteWorkflowFailure does not parse model prose, select commands, expand
// paths, grant permission or turn unknown delivery into failure. Unknown cases
// have an explicit owner and safe disposition, rather than a blind retry.
func RouteWorkflowFailure(f WorkflowFailureInput) WorkflowFailureDecision {
	human := func(reason string) WorkflowFailureDecision {
		return WorkflowFailureDecision{TrustedOwnerHuman, FailureHuman, reason}
	}
	if !f.Current {
		return WorkflowFailureDecision{TrustedOwnerHuman, FailureStopped, "SOURCE_NOT_CURRENT"}
	}
	if f.ProblemCode == "PATH_SCOPE" || f.ProblemCode == "UNSAFE_PATH" || f.ProblemCode == "AUTHORITY" || f.ProblemCode == "SOURCE_CHANGED" {
		return human(f.ProblemCode)
	}
	switch f.ReasonCode {
	case "PAUSED", "CANCELLED", "REQUIREMENT_CANCELLED", "PRODUCT_CANCELLED", "DIRECTION_CHANGE_STOPPED":
		return WorkflowFailureDecision{TrustedOwnerHuman, FailureStopped, f.ReasonCode}
	case "HUMAN_DECISION_REQUIRED", "BUILDER_NEEDS_HUMAN", "REVIEW_NEEDS_HUMAN", "REQUIREMENT_FINAL_REVIEW_NEEDS_HUMAN",
		"PENDING_DECISION", "PRODUCT_CLARIFICATION_REQUIRED", "PLANNER_COORDINATION_STOPPED", "PLANNER_COORDINATION_LIMIT_REACHED",
		"SCOPE_CHECK_FAILED", "SCOPE_PATH_FORBIDDEN", "SCOPE_PATH_NOT_LISTED", "SHARED_PATH_REQUIRES_APPROVAL",
		"CANDIDATE_NOT_DESCENDANT", "LOGIN_REQUIRED", "AUTHENTICATION_REQUIRED", "QUOTA_EXHAUSTED", "MODEL_NOT_AVAILABLE", "MODEL_UNAVAILABLE":
		return human(f.ReasonCode)
	case "DELIVERY_UNKNOWN", "DELIVERY_UNCONFIRMED", "OBSERVATION_TIMEOUT", "OBSERVATION_TIMED_OUT", "RESULT_NOT_SETTLED", "RESOURCE_CLEANUP_UNCONFIRMED":
		return WorkflowFailureDecision{TrustedOwnerControlPlane, FailureObserve, f.ReasonCode}
	}
	if f.UnavailableReason != "" {
		switch f.UnavailableReason {
		case "RESULT_NOT_SETTLED", "RECOVERY_RESULT_UNKNOWN", "RETRY_NOT_DUE", "RESOURCE_CLEANUP_UNCONFIRMED":
			return WorkflowFailureDecision{TrustedOwnerControlPlane, FailureObserve, f.UnavailableReason}
		case "CONTINUATION_REGISTERED", "BUILDER_RECHECK_REGISTERED":
			return WorkflowFailureDecision{f.Role, FailureExisting, f.UnavailableReason}
		default:
			return human(f.UnavailableReason)
		}
	}
	if f.ReasonCode == "BUILDER_BLOCKED" {
		if f.PlannerResolved && f.Eligible {
			return WorkflowFailureDecision{TrustedOwnerBuilder, FailureRepair, "ORIGINAL_PLANNER_RESOLVED_AGREEMENT"}
		}
		return WorkflowFailureDecision{TrustedOwnerPlanner, FailureEscalate, "ORIGINAL_BUILDER_CANNOT_COMPLETE"}
	}
	if f.ReasonCode == "CANDIDATE_INVALID" {
		// Only typed pre-publication failures may re-enter the editable tree.
		if !f.BeforePublish || f.ProblemCode != "NO_IMPLEMENTATION_CHANGE" && f.ProblemCode != "SOURCE_LIMIT" && f.ProblemCode != "HANDOFF_CONTENT" {
			return human("HANDOFF_PROOF_REQUIRED")
		}
		if f.Repeated {
			if f.PlannerResolved && f.Eligible {
				return WorkflowFailureDecision{TrustedOwnerBuilder, FailureRepair, "ORIGINAL_PLANNER_RESOLVED_AGREEMENT"}
			}
			return WorkflowFailureDecision{TrustedOwnerPlanner, FailureEscalate, "REPAIR_DID_NOT_RESOLVE_FAILURE"}
		}
	}
	if f.Repeated {
		return human("AUTOMATIC_RECOVERY_ALREADY_USED")
	}
	if !f.Eligible {
		return human("NO_SAFE_RECOVERY")
	}
	switch f.RecoveryAction {
	case RecoveryContinueBuilder:
		if f.ReasonCode == "CANDIDATE_INVALID" || f.ReasonCode == "BUILDER_RESULT_INVALID" || f.ReasonCode == "REVIEW_BLOCKED" {
			return WorkflowFailureDecision{TrustedOwnerBuilder, FailureRepair, "RETURN_SOURCE_WITH_EVIDENCE"}
		}
	case RecoveryRetryPlanningStep:
		switch f.ReasonCode {
		case "PRODUCT_DISCOVERY_INVALID", "COMPILATION_INVALID", "STEWARD_RESULT_INVALID", "PRODUCT_STEWARD_UNAVAILABLE", "STEWARD_UNAVAILABLE", "RESULT_INVALID", "PROVIDER_UNAVAILABLE", "PROVIDER_FAILURE", "RATE_LIMITED":
			return WorkflowFailureDecision{f.Role, FailureRetry, "BOUNDED_ORIGINAL_STEP_RETRY"}
		}
	case RecoveryRetryReview, RecoveryRetryStage, RecoveryRetryBuilderSession, RecoveryRetryPlannerCoordination:
		// The caller proves a never-sent/explicitly settled technical failure.
		// Business verdicts must not be resubmitted until a favourable review appears.
		if f.ProblemCode == "SETTLED_TECHNICAL_FAILURE" {
			return WorkflowFailureDecision{f.Role, FailureRetry, "BOUNDED_ORIGINAL_STEP_RETRY"}
		}
	}
	return human("NO_SAFE_RECOVERY")
}

// WorkflowFailureHandling is display only. REGISTERED never means repaired or
// verified. Historical records are kept separate from the current stop.
type WorkflowFailureHandling struct {
	ID         string `json:"id"`
	SourceKind string `json:"sourceKind"`
	SourceID   string `json:"sourceId"`
	SourceRole string `json:"sourceRole"`
	Owner      string `json:"owner"`
	Action     string `json:"action"`
	Reason     string `json:"reason"`
	Status     string `json:"status"`
	Summary    string `json:"summary,omitempty"`
	Path       string `json:"path,omitempty"`
	Historical bool   `json:"historical"`
}

// FailureCoordinationSource is a server-only bridge to the exact original
// Builder work. It is not an alternate authorization or editable client input.
type FailureCoordinationSource struct {
	EventID, ExecutionRunID, DispatchID, StepID, MessageSHA256, WorkingTreeSHA256 string
}
