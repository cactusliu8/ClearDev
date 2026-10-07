package cleardev

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MailAttemptPolicyV1 is frozen by the daemon, never selected by a model.
const MailAttemptPolicyV1 = "MAIL_ATTEMPTS_V1"

// Fixed attempt categories and their exact desktop-authority decision kind.
const (
	HumanDecisionKindExtraMailAttempt            = "AUTHORIZE_MAIL_EXTRA_ATTEMPT"
	MailAttemptDevelopment                       = "DEVELOPMENT"
	MailAttemptReviewRepair                      = "REVIEW_REPAIR"
	MailAttemptHumanExtra                        = "HUMAN_EXTRA"
	MailAttemptLimitReason            ReasonCode = "MAIL_ATTEMPTS_EXHAUSTED"
	MailAttemptFinalLimitReason       ReasonCode = "MAIL_EXTRA_ATTEMPT_EXHAUSTED"
)

// MailAttemptSlot is append-only and allocated in the existing dispatch transaction.
type MailAttemptSlot struct {
	DispatchID     string
	ExecutionRunID string
	TaskID         string
	Round          int
	Kind           string
	GrantRequestID string
	CreatedAt      time.Time
}

// BoundedMailAttempts identifies the policy in an already validated run envelope.
func BoundedMailAttempts(run ComplexExecutionRun) bool {
	_, mail, err := MailPolicyFromRun(run)
	if err != nil || !mail {
		return false
	}
	var pkg ComplexExecutionRunPackage
	return json.Unmarshal([]byte(run.ExecutionPackageJSON), &pkg) == nil && pkg.AttemptPolicy == MailAttemptPolicyV1
}

// BindMailAttemptPolicy freezes the policy on new control-plane mail executions.
func BindMailAttemptPolicy(raw []byte) ([]byte, string, error) {
	var pkg ComplexExecutionRunPackage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, "", err
	}
	if pkg.DeliveryPolicy != MailDeliveryPolicyV1 && pkg.DeliveryPolicy != MailDeliveryPolicyV2 {
		return nil, "", errors.New("mail attempts require a supported fixed delivery policy")
	}
	pkg.AttemptPolicy = MailAttemptPolicyV1
	encoded, err := marshalCanonicalJSON(pkg)
	return encoded, sha256Hex(encoded), err
}

// NextAutomaticMailAttempt never interprets an infrastructure failure as a
// business retry. Slots count implementations, not messages or check requests.
func NextAutomaticMailAttempt(slots []MailAttemptSlot, reviewerRework bool) (string, bool) {
	development, repair, extra := 0, 0, 0
	for _, slot := range slots {
		switch slot.Kind {
		case MailAttemptDevelopment:
			development++
		case MailAttemptReviewRepair:
			repair++
		case MailAttemptHumanExtra:
			extra++
		default:
			return "", false
		}
	}
	// A human-extra slot is terminal even when automatic repair capacity was
	// unused earlier. The desktop grant never authorizes a second implementation.
	if len(slots) >= 5 || extra > 0 || repair > 1 || development > 3 {
		return "", false
	}
	if reviewerRework && repair == 0 {
		return MailAttemptReviewRepair, true
	}
	if !reviewerRework && repair == 0 && extra == 0 && development < 3 {
		return MailAttemptDevelopment, true
	}
	return "", false
}

// ExtraMailAttemptBinding grants exactly the next implementation, not a new
// plan, candidate verdict, provider recovery, or an unbounded budget reset.
type ExtraMailAttemptBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	RequirementVersionID     string `json:"requirementVersionId"`
	RequirementVersionSHA256 string `json:"requirementVersionSha256"`
	ExecutionRunID           string `json:"executionRunId"`
	PlanSHA256               string `json:"planSha256"`
	TaskID                   string `json:"taskId"`
	DispatchID               string `json:"dispatchId"`
	CandidateSHA             string `json:"candidateSha"`
	NextRound                int    `json:"nextRound"`
}

// ExtraMailAttemptSpec registers the narrowly bound private desktop decision.
func ExtraMailAttemptSpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindExtraMailAttempt, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParseExtraMailAttemptBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		},
	}
}

// ParseExtraMailAttemptBinding rejects missing, extra, or mutable authority fields.
func ParseExtraMailAttemptBinding(raw []byte) (ExtraMailAttemptBinding, error) {
	var b ExtraMailAttemptBinding
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	if _, err := requireJSONObjectFields(raw, "developmentRequirementId", "requirementVersionId", "requirementVersionSha256", "executionRunId", "planSha256", "taskId", "dispatchId", "candidateSha", "nextRound"); err != nil {
		return b, err
	}
	for _, id := range []string{b.DevelopmentRequirementID, b.RequirementVersionID, b.ExecutionRunID, b.TaskID, b.DispatchID} {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 200 {
			return b, errors.New("invalid extra attempt identity")
		}
	}
	if !validProtocolSHA256(b.RequirementVersionSHA256) || !validProtocolSHA256(b.PlanSHA256) || !mailSHA1(b.CandidateSHA) || b.NextRound < 1 || b.NextRound > 4 {
		return b, errors.New("invalid extra attempt binding")
	}
	return b, nil
}

// ExtraMailAttemptDisplay describes the single grant without changing acceptance.
func ExtraMailAttemptDisplay(b ExtraMailAttemptBinding, reason string) HumanDecisionDisplay {
	return HumanDecisionDisplay{Title: "Authorize one additional development attempt", Summary: "Automatic attempts are exhausted. Allow exactly one additional implementation and its checks/review?",
		FullContent:   fmt.Sprintf("Requirement %s; task %s; next attempt %d. Failure: %s. Candidate %s. Plan %s.", b.DevelopmentRequirementID, b.TaskID, b.NextRound+1, reason, b.CandidateSHA, b.PlanSHA256),
		ChangeSummary: "No acceptance criteria, scope, model, existing counts or evidence will change. This one-time authorization cannot be reused. Reject stops; Later keeps the decision pending."}
}
