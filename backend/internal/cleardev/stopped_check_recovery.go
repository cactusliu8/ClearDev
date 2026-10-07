package cleardev

import (
	"encoding/json"
	"errors"
	"strings"
)

// HumanDecisionKindStoppedCheckRecovery and RecoveryRequestStoppedCheck keep
// an operator's intent separate from the native human decision.
const (
	HumanDecisionKindStoppedCheckRecovery = "AUTHORIZE_STOPPED_CHECK_RECOVERY"
	RecoveryRequestStoppedCheck           = "REQUEST_STOPPED_CHECK_RECOVERY"
)

// StoppedCheckRecoveryBinding names one candidate/check and its preserved STOP.
// ContextSHA256 freezes the original task counter, specification and budgets.
// No caller-supplied command, broader quota or replacement session is accepted.
type StoppedCheckRecoveryBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	ExecutionRunID           string `json:"executionRunId"`
	EventID                  string `json:"eventId"`
	StopSHA256               string `json:"stopSha256"`
	ContextSHA256            string `json:"contextSha256"`
	TaskID                   string `json:"taskId"`
	DispatchID               string `json:"dispatchId"`
	CheckRunID               string `json:"checkRunId"`
	RetryCheckRunID          string `json:"retryCheckRunId"`
	CandidateSHA             string `json:"candidateSha"`
	TaskPacketSHA256         string `json:"taskPacketSha256"`
	ReworkCount              int    `json:"reworkCount"`
	BuilderRoleBindingID     string `json:"builderRoleBindingId"`
	AOSessionID              string `json:"aoSessionId"`
	ProviderConversationID   string `json:"providerConversationId"`
	WorkspacePath            string `json:"workspacePath"`
	SessionCreationKey       string `json:"sessionCreationKey"`
	CreationFingerprint      string `json:"creationFingerprint"`
	Harness                  string `json:"harness"`
	Model                    string `json:"model"`
}

// StoppedCheckRecoverySpec keeps the new permission on the native decision path.
func StoppedCheckRecoverySpec() DecisionKindSpec {
	return DecisionKindSpec{Kind: HumanDecisionKindStoppedCheckRecovery, BindingSchemaVersion: 1,
		Allowed: map[HumanDecisionChoice]bool{HumanDecisionApprove: true, HumanDecisionReject: true, HumanDecisionLater: true},
		ParseBinding: func(raw []byte) ([]byte, error) {
			b, err := ParseStoppedCheckRecoveryBinding(raw)
			if err != nil {
				return nil, err
			}
			return json.Marshal(b)
		},
	}
}

// ParseStoppedCheckRecoveryBinding rejects loose or noncanonical authorities.
func ParseStoppedCheckRecoveryBinding(raw []byte) (StoppedCheckRecoveryBinding, error) {
	var b StoppedCheckRecoveryBinding
	if err := decodeStrictAgentResult(raw, &b); err != nil {
		return b, err
	}
	if _, err := requireJSONObjectFields(raw, "developmentRequirementId", "executionRunId", "eventId", "stopSha256", "contextSha256", "taskId", "dispatchId", "checkRunId", "retryCheckRunId", "candidateSha", "taskPacketSha256", "reworkCount", "builderRoleBindingId", "aoSessionId", "providerConversationId", "workspacePath", "sessionCreationKey", "creationFingerprint", "harness", "model"); err != nil {
		return b, err
	}
	for _, s := range []string{b.DevelopmentRequirementID, b.ExecutionRunID, b.EventID, b.TaskID, b.DispatchID, b.CheckRunID, b.BuilderRoleBindingID, b.AOSessionID, b.ProviderConversationID, b.SessionCreationKey, b.CreationFingerprint, b.Harness} {
		if s == "" || strings.TrimSpace(s) != s || len(s) > 512 {
			return b, errors.New("invalid stopped-check identity")
		}
	}
	if !validProtocolSHA256(b.StopSHA256) || !validProtocolSHA256(b.ContextSHA256) || !validComplexExecutionCommitSHA(b.CandidateSHA) || !validProtocolSHA256(b.TaskPacketSHA256) || b.ReworkCount < 0 || b.ReworkCount > 1000 ||
		b.RetryCheckRunID != b.CheckRunID+":stopped-check-retry" || b.WorkspacePath == "" || strings.TrimSpace(b.WorkspacePath) != b.WorkspacePath || len(b.WorkspacePath) > 4096 || len(b.Model) > 512 {
		return b, errors.New("invalid same-candidate check recovery authority")
	}
	return b, nil
}

// StoppedCheckRecoveryTarget binds the five-field recovery intent to the source.
func StoppedCheckRecoveryTarget(b StoppedCheckRecoveryBinding) (string, error) {
	raw, err := CanonicalJSONBytes(b)
	if err != nil {
		return "", err
	}
	return b.CheckRunID + ":stopped:" + sha256Hex(raw), nil
}

// StoppedCheckRecovery is an approved, immutable recovery, never a passed check.
// The old Planner decision remains in its original history collection.
type StoppedCheckRecovery struct {
	EventID           string `json:"eventId"`
	DecisionRequestID string `json:"decisionRequestId"`
	StopSHA256        string `json:"stopSha256"`
	TaskID            string `json:"taskId"`
	DispatchID        string `json:"dispatchId"`
	CandidateSHA      string `json:"candidateSha"`
	TaskPacketSHA256  string `json:"taskPacketSha256"`
	CheckRunID        string `json:"checkRunId"`
	RetryCheckRunID   string `json:"retryCheckRunId"`
	ReworkCount       int    `json:"reworkCount"`
}

// StoppedCheckRecoveryState is internal admission state, not public authority.
type StoppedCheckRecoveryState struct {
	Binding StoppedCheckRecoveryBinding
	Option  WorkflowRecoveryOption
	History []WorkflowRecovery
}

func stoppedCheckRecoveryMatches(s ComplexExecutionSnapshot, g StoppedCheckRecovery) bool {
	for _, task := range s.Tasks {
		if task.ID != g.TaskID || task.CurrentDispatchID != g.DispatchID || task.ReworkCount != g.ReworkCount || task.ExecutionPackageSHA256 != g.TaskPacketSHA256 {
			continue
		}
		if task.Status != DevelopmentTaskStatusRunning && task.Status != DevelopmentTaskStatusReview && task.Status != DevelopmentTaskStatusDone {
			return false
		}
		for _, d := range s.Dispatches {
			if d.ID == g.DispatchID && d.ComplexExecutionTaskID == g.TaskID && d.CandidateCommitSHA == g.CandidateSHA && d.Round == task.CurrentRound {
				return d.Status == ComplexExecutionDispatchObserved || d.Status == ComplexExecutionDispatchReviewing || d.Status == ComplexExecutionDispatchVerified
			}
		}
	}
	return false
}

// StoppedCheckRecoveryResolvesStop scopes human continuation to the same live
// candidate. An unrelated/new task attempt cannot borrow this permission.
func StoppedCheckRecoveryResolvesStop(s ComplexExecutionSnapshot, d PlannerCoordinationDecision) bool {
	if s.PlannerRuntime == nil || d.Source != "PLANNER" || d.Outcome != PlannerRuntimeStop || d.ResultSHA256 == "" || sha256Hex([]byte(d.ResultJSON)) != d.ResultSHA256 {
		return false
	}
	for _, g := range s.PlannerRuntime.CheckRecoveries {
		if g.EventID == d.EventID && g.StopSHA256 == d.ResultSHA256 && stoppedCheckRecoveryMatches(s, g) {
			return true
		}
	}
	return false
}

// StoppedCheckRecoveryRetainsReworkCount distinguishes the preserved spent
// counter from an instruction to send another Builder round after verification.
func StoppedCheckRecoveryRetainsReworkCount(s ComplexExecutionSnapshot, task ComplexExecutionTask) bool {
	if s.PlannerRuntime == nil {
		return false
	}
	for _, g := range s.PlannerRuntime.CheckRecoveries {
		if g.TaskID != task.ID {
			continue
		}
		d, found := PlannerRuntimeEffectiveDecision(s.PlannerRuntime, g.EventID)
		if found && StoppedCheckRecoveryResolvesStop(s, d) {
			return true
		}
	}
	return false
}

func stoppedPlannerCheckRequiresNative(s ComplexExecutionSnapshot, dispatchID string) bool {
	if s.PlannerRuntime == nil {
		return false
	}
	for _, event := range s.PlannerRuntime.Events {
		if event.DispatchID != dispatchID {
			continue
		}
		d, found := PlannerRuntimeEffectiveDecision(s.PlannerRuntime, event.ID)
		if found && d.Source == "PLANNER" && d.Outcome == PlannerRuntimeStop {
			// The native decision admitted continuation of this candidate. Subsequent
			// ended infrastructure checks use ordinary technical recovery, no new vote.
			for _, g := range s.PlannerRuntime.CheckRecoveries {
				if g.DispatchID != dispatchID || g.EventID != d.EventID || g.StopSHA256 != d.ResultSHA256 || sha256Hex([]byte(d.ResultJSON)) != d.ResultSHA256 {
					continue
				}
				for _, task := range s.Tasks {
					if task.ID != g.TaskID || task.CurrentDispatchID != g.DispatchID || task.ReworkCount != g.ReworkCount || task.ExecutionPackageSHA256 != g.TaskPacketSHA256 {
						continue
					}
					for _, dispatch := range s.Dispatches {
						if dispatch.ID == g.DispatchID && dispatch.CandidateCommitSHA == g.CandidateSHA && dispatch.Round == task.CurrentRound {
							return false
						}
					}
				}
			}
			return true
		}
	}
	return false
}
